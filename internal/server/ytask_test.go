package server

import (
	"context"
	"testing"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/queue"
)

func ytItem() queue.Item {
	return queue.Item{Provider: "youtube", URI: "https://youtu.be/x", Title: "Мем", Artist: "Кто-то", DurationMs: 60000, Via: viaMissing}
}

// Режим «спрашивать»: заказ ждёт, в очередь не идёт, пока не нажали.
func TestYouTubeAskWaitsThenPlays(t *testing.T) {
	srv, _ := newTestServer(t, func(c *config.Config) { c.YouTubeMode = "ask"; c.YouTubeAskSeconds = 0 }, nil)
	r := placeOrder(srv, "мем")
	srv.offerYouTube(context.Background(), r, ytItem(), "")

	asks := srv.state.Snapshot().YouTubeAsks
	if len(asks) != 1 {
		t.Fatalf("ждущих %d, ждали 1", len(asks))
	}
	if n, _ := srv.queue.Len(); n != 0 {
		t.Fatalf("заказ ушёл в очередь без решения")
	}
	if _, err := srv.decideYouTube(context.Background(), asks[0].ID, true, panelActor); err != nil {
		t.Fatal(err)
	}
	if n, _ := srv.queue.Len(); n != 1 {
		t.Fatalf("после «включить» в очереди %d", n)
	}
	if len(srv.state.Snapshot().YouTubeAsks) != 0 {
		t.Fatal("заказ остался ждать после решения")
	}
	// Второе нажатие по тому же заказу — понятный отказ, а не второй трек.
	if _, err := srv.decideYouTube(context.Background(), asks[0].ID, true, panelActor); err == nil {
		t.Fatal("второе решение по тому же заказу прошло")
	}
}

// Отклонили — в очереди пусто.
func TestYouTubeAskReject(t *testing.T) {
	srv, _ := newTestServer(t, func(c *config.Config) { c.YouTubeMode = "ask" }, nil)
	r := placeOrder(srv, "мем")
	srv.offerYouTube(context.Background(), r, ytItem(), "")
	id := srv.state.Snapshot().YouTubeAsks[0].ID
	if _, err := srv.decideYouTube(context.Background(), id, false, panelActor); err != nil {
		t.Fatal(err)
	}
	if n, _ := srv.queue.Len(); n != 0 {
		t.Fatalf("отклонённый заказ в очереди")
	}
}

// Режим «сам»: сразу в очередь и в список недавних.
func TestYouTubeAutoQueues(t *testing.T) {
	srv, _ := newTestServer(t, func(c *config.Config) { c.YouTubeMode = "auto" }, nil)
	srv.offerYouTube(context.Background(), placeOrder(srv, "мем"), ytItem(), "")
	if n, _ := srv.queue.Len(); n != 1 {
		t.Fatalf("в очереди %d, ждали 1", n)
	}
	if len(srv.state.Snapshot().YouTubeRecent) != 1 {
		t.Fatal("недавние с YouTube пусты")
	}
}
