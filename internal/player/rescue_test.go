package player

import (
	"context"
	"errors"
	"sync"
	"testing"

	"songrequest-yandex/internal/queue"
)

// Запасной источник, когда заказ не заиграл.
//
// До этого цепочка источников работала только до старта: выбрали, откуда
// играть, — и дальше как получится. А получалось по-разному: Spotify отвечал
// отказом на саму команду «играй», mpv падал через полсекунды после запуска.
// В обоих случаях заказ пропадал целиком, баллы списывались, зритель ничего не
// слышал — хотя тот же трек нашёлся бы другим источником.

// rescueTo — заглушка сервера: отдаёт заранее заготовленный запасной заказ и
// считает, сколько раз её спросили.
type rescueTo struct {
	mu    sync.Mutex
	item  queue.Item
	ok    bool
	calls int
}

func (r *rescueTo) fn(ctx context.Context, item queue.Item) (queue.Item, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if !r.ok {
		return queue.Item{}, false
	}
	next := item
	next.Provider = r.item.Provider
	next.URI = r.item.URI
	next.Title = r.item.Title
	next.DurationMs = r.item.DurationMs
	return next, true
}

func (r *rescueTo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Spotify отказался включать заказ — играем его с YouTube, а не выбрасываем.
func TestOrderThatSpotifyRefusesGoesToYouTube(t *testing.T) {
	p, q, sp := newPlayer(t)
	yt := newFakeYouTube()
	p.SetYouTube(yt)

	sp.failPlay = errors.New("устройство не отвечает")

	saver := &rescueTo{ok: true, item: queue.Item{
		Provider: "youtube", URI: "https://youtu.be/запасной",
		Title: "запасной", DurationMs: 600000,
	}}
	p.SetRescue(saver.fn)

	if _, err := q.Add(queue.Item{
		Source: queue.SourcePoints, Requester: "зритель", RawRequest: "трек",
		Provider: "yandex", URI: "spotify:track:нет", Title: "трек",
		Artist: "кто-то", DurationMs: 600000,
	}, false); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "заказ ушёл на запасной источник", func() bool {
		played, _ := yt.stats()
		return len(played) == 1 && played[0] == "https://youtu.be/запасной"
	})
	waitFor(t, "панель показывает играющий заказ", func() bool { return p.Now() != nil })
}

// Проигрыватель умер через мгновение после старта — это не «доиграл» и не
// «скипнули». Пробуем то же самое другим источником.
func TestPlayerThatDiesEarlyIsRescued(t *testing.T) {
	p, q, _ := newPlayer(t)
	yt := newFakeYouTube()
	p.SetYouTube(yt)

	saver := &rescueTo{ok: true, item: queue.Item{
		Provider: "youtube", URI: "https://youtu.be/другой",
		Title: "другой", DurationMs: 600000,
	}}
	p.SetRescue(saver.fn)

	addYouTubeTrack(t, q, "первый", 600000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый ролик пошёл", func() bool {
		played, _ := yt.stats()
		return len(played) >= 1
	})

	// mpv умер, не доиграв.
	yt.done <- false

	waitFor(t, "заказ переиграли с другого источника", func() bool {
		played, _ := yt.stats()
		return len(played) >= 2 && played[1] == "https://youtu.be/другой"
	})
}

// Второй раз подряд один и тот же заказ не спасаем: если и запасной источник
// молчит, дело не в источнике, а очередь обязана двигаться дальше.
func TestRescueIsTriedOnlyOnce(t *testing.T) {
	p, q, _ := newPlayer(t)
	yt := newFakeYouTube()
	p.SetYouTube(yt)

	saver := &rescueTo{ok: true, item: queue.Item{
		Provider: "youtube", URI: "https://youtu.be/другой",
		Title: "другой", DurationMs: 600000,
	}}
	p.SetRescue(saver.fn)

	addYouTubeTrack(t, q, "первый", 600000)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor(t, "первый ролик пошёл", func() bool {
		played, _ := yt.stats()
		return len(played) >= 1
	})
	yt.done <- false
	waitFor(t, "пошёл запасной", func() bool {
		played, _ := yt.stats()
		return len(played) >= 2
	})
	// И запасной тоже умер.
	yt.done <- false

	waitFor(t, "спасали ровно один раз", func() bool { return saver.count() == 1 })
	if played, _ := yt.stats(); len(played) > 2 {
		t.Fatalf("заказ переигрывали больше одного раза: %v", played)
	}
}
