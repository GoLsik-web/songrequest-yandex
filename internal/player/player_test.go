package player

import (
	"context"
	"sync"
	"testing"
	"time"

	"songrequest-yandex/internal/logx"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/store"
	"songrequest-yandex/internal/yandex"
)

// fakeSpotify изображает плеер Spotify: помнит, что включили, и умеет
// «доигрывать» трек по команде теста.
type fakeSpotify struct {
	mu sync.Mutex

	played []string
	// devices — на какое устройство уходил каждый запуск заказа.
	devices  []string
	captured int
	restored int
	// playing — что играет сейчас; пустая строка означает тишину.
	playing  string
	snapshot *yandex.Snapshot
	failPlay error
	// failRestore и forced нужны проверкам возврата: первая изображает
	// сорвавшийся возврат, вторая запоминает, с какой оглядкой он звался.
	failRestore error
	forced      []bool
	// duration и progress нужны проверкам ожидания: плеер считает по ним,
	// сколько осталось до конца трека стримера.
	duration int
	progress int
	paused   bool
	// held, sought и volumes нужны проверкам управления заказом: пауза,
	// перемотка и громкость.
	held    bool
	sought  []int
	volumes []int
	// states — сколько раз спросили Spotify, что играет.
	states int
}

// setPlaying изображает, что у стримера что-то играет.
func (f *fakeSpotify) setPlaying(uri string, durationMs, progressMs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.playing = uri
	f.duration = durationMs
	f.progress = progressMs
}

func (f *fakeSpotify) Capture(ctx context.Context) (*yandex.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured++
	if f.snapshot != nil {
		return f.snapshot, nil
	}
	return &yandex.Snapshot{
		TrackURI: "spotify:track:исходный", TrackName: "Исходный",
		ContextURI: "spotify:playlist:любимое", PositionMs: 30000, IsPlaying: true,
	}, nil
}

func (f *fakeSpotify) Restore(ctx context.Context, snap *yandex.Snapshot, playedURI string, force bool) (yandex.RestoreOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored++
	f.forced = append(f.forced, force)
	if f.failRestore != nil {
		return yandex.RestoreOutcome{}, f.failRestore
	}
	f.playing = snap.TrackURI
	return yandex.RestoreOutcome{Restored: true, Message: "вернул"}, nil
}

// lastForce — с какой оглядкой звался последний возврат.
func (f *fakeSpotify) lastForce() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forced) == 0 {
		return false
	}
	return f.forced[len(f.forced)-1]
}

func (f *fakeSpotify) PlayTrack(ctx context.Context, trackURI, deviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPlay != nil {
		return f.failPlay
	}
	f.played = append(f.played, trackURI)
	f.devices = append(f.devices, deviceID)
	f.playing = trackURI
	return nil
}

// lastDevice — куда ушёл последний запуск заказа.
func (f *fakeSpotify) lastDevice() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.devices) == 0 {
		return ""
	}
	return f.devices[len(f.devices)-1]
}

func (f *fakeSpotify) Pause(ctx context.Context, deviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = true
	return nil
}

func (f *fakeSpotify) Resume(ctx context.Context, deviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = false
	return nil
}

func (f *fakeSpotify) Seek(ctx context.Context, positionMs int, deviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sought = append(f.sought, positionMs)
	f.progress = positionMs
	return nil
}

func (f *fakeSpotify) SetVolume(ctx context.Context, percent int, deviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes = append(f.volumes, percent)
	return nil
}

func (f *fakeSpotify) State(ctx context.Context) (*yandex.PlayerState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states++
	if f.playing == "" {
		return nil, false, nil
	}
	st := &yandex.PlayerState{IsPlaying: !f.paused}
	st.Item = &yandex.StateItem{URI: f.playing, Name: f.playing, DurationMs: f.dur()}
	st.ProgressMs = f.progress
	return st, true, nil
}

// pause изображает остановленное воспроизведение.
func (f *fakeSpotify) pause(uri string, durationMs, progressMs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.playing = uri
	f.duration = durationMs
	f.progress = progressMs
	f.paused = true
}

// dur — длительность трека; по умолчанию секунда, как было раньше.
func (f *fakeSpotify) dur() int {
	if f.duration > 0 {
		return f.duration
	}
	return 1000
}

// finish изображает, что трек доиграл.
func (f *fakeSpotify) finish() {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Заказ кончился — Яндекс ушёл на другой трек. Молчание (нет ответа)
	// плеер заказом не считает и досиживает по своим часам.
	f.playing = "yandex:track:после-заказа"
}

func (f *fakeSpotify) stats() (played []string, captured, restored int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.played...), f.captured, f.restored
}

func newPlayer(t *testing.T) (*Player, *queue.Queue, *fakeSpotify) {
	t.Helper()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	log, err := logx.New(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })

	q := queue.New(db.SQL())
	sp := &fakeSpotify{}
	p := New(q, sp, log)
	// В тестах ждать три секунды перед возвратом незачем.
	p.ResumeDelay = 20 * time.Millisecond
	// И шесть секунд до первой проверки Spotify — тоже: проверяем поведение,
	// а не терпение.
	p.SetPollEvery(100 * time.Millisecond)
	return p, q, sp
}

func addTrack(t *testing.T, q *queue.Queue, name string, ms int) {
	t.Helper()
	_, err := q.Add(queue.Item{
		Source: queue.SourcePoints, Requester: "зритель", RawRequest: name,
		Provider: "yandex", TrackID: name, URI: "spotify:track:" + name,
		Title: name, Artist: "кто-то", DurationMs: ms,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

// Главный сценарий целиком: заказ играет, после очереди музыка возвращается
// туда, где стример остановился.
func TestPlaysQueueThenRestores(t *testing.T) {
	p, q, sp := newPlayer(t)
	addTrack(t, q, "заказ", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "заказ начал играть", func() bool {
		played, _, _ := sp.stats()
		return len(played) == 1
	})

	sp.finish()

	waitFor(t, "музыка вернулась на место", func() bool {
		_, _, restored := sp.stats()
		return restored == 1
	})

	played, captured, _ := sp.stats()
	if played[0] != "spotify:track:заказ" {
		t.Fatalf("играл не тот трек: %q", played[0])
	}
	if captured != 1 {
		t.Fatalf("снимков снято %d, ждали один", captured)
	}
	if p.Snapshot() != nil {
		t.Fatal("отработавший снимок надо забыть, иначе следующая пачка заказов вернёт музыку на вчерашнее место")
	}
}

// Заказы подряд не должны возвращать музыку между собой: включить исходный
// трек на две секунды и снова прервать — худшее, что можно сделать со звуком.
func TestDoesNotRestoreBetweenOrders(t *testing.T) {
	p, q, sp := newPlayer(t)
	addTrack(t, q, "первый", 30)
	addTrack(t, q, "второй", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 1
	})
	sp.finish()

	waitFor(t, "второй заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 2
	})

	if _, _, restored := sp.stats(); restored != 0 {
		t.Fatalf("между заказами музыку вернули %d раз", restored)
	}

	sp.finish()
	waitFor(t, "возврат после очереди", func() bool {
		_, _, restored := sp.stats()
		return restored == 1
	})

	if _, captured, _ := sp.stats(); captured != 1 {
		t.Fatalf("снимок сняли %d раз, а надо один — перед первым заказом", captured)
	}
}

// Скип обрывает трек и переходит к следующему.
func TestSkipMovesToNextTrack(t *testing.T) {
	p, q, sp := newPlayer(t)
	addTrack(t, q, "первый", 600000) // десять минут: сам не кончится
	addTrack(t, q, "второй", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 1
	})

	p.Skip()

	waitFor(t, "второй заказ пошёл после скипа", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 2
	})
}

// Пауза не обрывает играющий трек, но следующий не начинает.
func TestPauseStopsAfterCurrentTrack(t *testing.T) {
	p, q, sp := newPlayer(t)
	addTrack(t, q, "первый", 30)
	addTrack(t, q, "второй", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) >= 1
	})

	p.SetPaused(true)
	sp.finish()

	time.Sleep(200 * time.Millisecond)
	if played, _, _ := sp.stats(); len(played) != 1 {
		t.Fatalf("на паузе начали играть следующий: %v", played)
	}
	if n, _ := q.Len(); n != 1 {
		t.Fatalf("второй заказ пропал из очереди: осталось %d", n)
	}

	p.SetPaused(false)
	waitFor(t, "после снятия паузы заказ пошёл", func() bool {
		played, _, _ := sp.stats()
		return len(played) == 2
	})
}

// Не смогли включить — заказ не должен зависать в плеере навсегда.
func TestFailedPlaybackDoesNotStall(t *testing.T) {
	p, q, sp := newPlayer(t)
	sp.failPlay = context.DeadlineExceeded
	addTrack(t, q, "проблемный", 30)

	var failed int
	var mu sync.Mutex
	p.OnError = func(error) {
		mu.Lock()
		failed++
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "ошибка дошла до панели", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return failed > 0
	})

	if p.Now() != nil {
		t.Fatal("неудачный заказ остался висеть как играющий")
	}
}
