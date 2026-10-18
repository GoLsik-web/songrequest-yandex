package player

import (
	"context"
	"sync"
	"testing"
	"time"

	"songrequest-yandex/internal/queue"
)

// fakeYouTube изображает запасной проигрыватель: помнит, что включали и
// сколько раз его останавливали, а конец ролика наступает по команде теста.
type fakeYouTube struct {
	mu      sync.Mutex
	played  []string
	stopped int
	// base — громкость Spotify, которую передал плеер.
	base int
	// held, sought и volume нужны проверкам управления заказом.
	held   bool
	sought []float64
	volume int
	// done — «ролик доиграл сам». Тест шлёт сюда, когда ему нужно.
	done chan bool
}

func newFakeYouTube() *fakeYouTube { return &fakeYouTube{done: make(chan bool, 1)} }

func (f *fakeYouTube) Play(ctx context.Context, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.played = append(f.played, url)
	return nil
}

func (f *fakeYouTube) Wait(ctx context.Context) bool {
	select {
	case v := <-f.done:
		return v
	case <-ctx.Done():
		return false
	}
}

func (f *fakeYouTube) SetBase(volume int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.base = volume
}

func (f *fakeYouTube) SetPaused(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = on
}

func (f *fakeYouTube) Seek(seconds float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sought = append(f.sought, seconds)
}

func (f *fakeYouTube) SetVolumePercent(percent int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volume = percent
}

func (f *fakeYouTube) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
}

func (f *fakeYouTube) stats() (played []string, stopped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.played...), f.stopped
}

func addYouTubeTrack(t *testing.T, q *queue.Queue, name string, ms int) {
	t.Helper()
	_, err := q.Add(queue.Item{
		Source: queue.SourcePoints, Requester: "зритель", RawRequest: name,
		Provider: "youtube", TrackID: name, URI: "https://youtu.be/" + name,
		Title: name, Artist: "кто-то", DurationMs: ms,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
}

// Жалоба 27.08: заказ заиграл с YouTube, и скипнуть его не вышло — кнопка в
// панели будто ничего не делает. Проверяем весь путь: кнопка зовёт Skip(),
// он обязан оборвать mpv и пустить следующий заказ.
func TestSkipStopsYouTubeTrack(t *testing.T) {
	p, q, _ := newPlayer(t)
	yt := newFakeYouTube()
	p.SetYouTube(yt)

	addYouTubeTrack(t, q, "первый", 600000) // десять минут: сам не кончится
	addYouTubeTrack(t, q, "второй", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый ролик пошёл", func() bool {
		played, _ := yt.stats()
		return len(played) >= 1
	})
	// Панель показывает трек — именно по этому кнопка «Скипнуть» и рисуется.
	waitFor(t, "панель показала играющий заказ", func() bool { return p.Now() != nil })

	p.Skip()

	waitFor(t, "mpv остановлен", func() bool {
		_, stopped := yt.stats()
		return stopped >= 1
	})
	waitFor(t, "второй ролик пошёл после скипа", func() bool {
		played, _ := yt.stats()
		return len(played) >= 2
	})
}

// Скип, нажатый в первую же секунду ролика, обязан сработать так же: между
// «панель показала заказ» и «плеер начал ждать конца» есть щель, и раньше
// такие поломки в неё проваливались.
func TestSkipRightAfterStartWorks(t *testing.T) {
	p, q, _ := newPlayer(t)
	yt := newFakeYouTube()
	p.SetYouTube(yt)

	addYouTubeTrack(t, q, "первый", 600000)
	addYouTubeTrack(t, q, "второй", 30)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый ролик пошёл", func() bool {
		played, _ := yt.stats()
		return len(played) >= 1
	})
	time.Sleep(5 * time.Millisecond)
	p.Skip()

	waitFor(t, "второй ролик пошёл после скипа", func() bool {
		played, _ := yt.stats()
		return len(played) >= 2
	})
}
