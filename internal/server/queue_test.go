package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/queue"
)

// Отказ до попадания в очередь — место, где приложение чаще всего обидит
// зрителя: баллы списались, а музыки нет. Поэтому каждый отказ обязан вернуть
// баллы и объяснить причину.

func sampleItem(who, title string, ms int) queue.Item {
	return queue.Item{
		Source: queue.SourcePoints, Requester: who, RawRequest: title,
		Provider: "yandex", TrackID: title, URI: "spotify:track:" + title,
		Title: title, Artist: "кто-то", DurationMs: ms,
		RedemptionID: "redemption-" + title, RewardID: "reward-1",
	}
}

func refunds(calls []recorded) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c.Path, "custom_rewards/redemptions") && c.Method == http.MethodPatch {
			n++
		}
	}
	return n
}

func noContent(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/users" {
		writeTestJSON(w, map[string]any{"data": []any{
			map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
		}})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func TestLongTrackIsRejectedAndRefunded(t *testing.T) {
	srv, calls := newTestServer(t,
		func(c *config.Config) { c.MaxTrackSeconds = 480 },
		noContent)
	srv.twitch.CheckAccount(context.Background())

	// Девять минут при лимите восемь.
	srv.enqueue(context.Background(), sampleItem("зритель", "длинный", 9*60*1000))

	if n, _ := srv.queue.Len(); n != 0 {
		t.Fatalf("длинный трек попал в очередь")
	}
	if refunds(*calls) == 0 {
		t.Fatal("баллы за отклонённый заказ не вернулись")
	}
	if text := lastNoticeText(srv); !strings.Contains(text, "длиннее") {
		t.Fatalf("в панели должна быть причина отказа: %q", text)
	}
}

func TestNotMusicIsRejected(t *testing.T) {
	srv, calls := newTestServer(t, nil, noContent)
	srv.twitch.CheckAccount(context.Background())

	// «подкаст» есть в списке ключевых слов по умолчанию.
	srv.enqueue(context.Background(), sampleItem("зритель", "подкаст про котиков", 300000))

	if n, _ := srv.queue.Len(); n != 0 {
		t.Fatal("не-музыка попала в очередь")
	}
	if refunds(*calls) == 0 {
		t.Fatal("баллы не вернулись")
	}
}

func TestTooManyOrdersFromOnePerson(t *testing.T) {
	srv, calls := newTestServer(t,
		func(c *config.Config) { c.MaxPerUser = 2 },
		noContent)
	srv.twitch.CheckAccount(context.Background())

	ctx := context.Background()
	srv.enqueue(ctx, sampleItem("жадный", "раз", 200000))
	srv.enqueue(ctx, sampleItem("жадный", "два", 200000))
	before := refunds(*calls)
	srv.enqueue(ctx, sampleItem("жадный", "три", 200000))

	if n, _ := srv.queue.Len(); n != 2 {
		t.Fatalf("в очереди %d заказов, ждали 2", n)
	}
	if refunds(*calls) == before {
		t.Fatal("за третий заказ баллы не вернулись")
	}
	// Лимит на одного не должен мешать остальным.
	srv.enqueue(ctx, sampleItem("другой", "четыре", 200000))
	if n, _ := srv.queue.Len(); n != 3 {
		t.Fatalf("чужой заказ тоже не прошёл: в очереди %d", n)
	}
}

func TestBannedViewerCannotOrder(t *testing.T) {
	srv, calls := newTestServer(t, nil, noContent)
	srv.twitch.CheckAccount(context.Background())

	if err := srv.banMusic("хулиган", "спамил"); err != nil {
		t.Fatal(err)
	}
	srv.enqueue(context.Background(), sampleItem("Хулиган", "раз", 200000))

	if n, _ := srv.queue.Len(); n != 0 {
		t.Fatal("забаненный смог заказать")
	}
	if refunds(*calls) == 0 {
		t.Fatal("баллы не вернулись")
	}
	// Причина бана должна дойти до человека.
	if text := lastNoticeText(srv); !strings.Contains(text, "спамил") {
		t.Fatalf("причина бана потерялась: %q", text)
	}

	// Разбан возвращает право заказывать.
	if err := srv.unbanMusic("хулиган"); err != nil {
		t.Fatal(err)
	}
	srv.enqueue(context.Background(), sampleItem("Хулиган", "два", 200000))
	if n, _ := srv.queue.Len(); n != 1 {
		t.Fatal("после разбана заказ должен проходить")
	}
}

func TestGoodOrderGoesToQueue(t *testing.T) {
	srv, calls := newTestServer(t, nil, noContent)
	srv.twitch.CheckAccount(context.Background())

	srv.enqueue(context.Background(), sampleItem("зритель", "хорошая песня", 200000))

	items, err := srv.queue.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("в очереди %d заказов, ждали 1", len(items))
	}
	if refunds(*calls) != 0 {
		t.Fatal("за принятый заказ баллы возвращать нельзя")
	}
	// Панель должна увидеть очередь сразу.
	if len(srv.state.Snapshot().Queue) != 1 {
		t.Fatal("очередь не доехала до панели")
	}
}

// Удаление из очереди с возвратом и без — это разные действия, и путать их
// нельзя: зритель либо получает баллы обратно, либо нет.
func TestRemoveWithAndWithoutRefund(t *testing.T) {
	srv, calls := newTestServer(t, nil, noContent)
	srv.twitch.CheckAccount(context.Background())
	ctx := context.Background()

	srv.enqueue(ctx, sampleItem("a", "раз", 200000))
	srv.enqueue(ctx, sampleItem("b", "два", 200000))
	items, _ := srv.queue.List()
	before := refunds(*calls)

	if err := srv.removeFromQueue(ctx, items[0].ID, false, "мод"); err != nil {
		t.Fatal(err)
	}
	if refunds(*calls) != before {
		t.Fatal("без возврата баллы трогать нельзя")
	}

	if err := srv.removeFromQueue(ctx, items[1].ID, true, "мод"); err != nil {
		t.Fatal(err)
	}
	if refunds(*calls) == before {
		t.Fatal("с возвратом баллы должны вернуться")
	}

	if n, _ := srv.queue.Len(); n != 0 {
		t.Fatalf("в очереди осталось %d", n)
	}
}

// Стример должен видеть, кто из модераторов что сделал.
func TestModActionsAreLogged(t *testing.T) {
	srv, _ := newTestServer(t, nil, noContent)
	srv.twitch.CheckAccount(context.Background())
	ctx := context.Background()

	srv.enqueue(ctx, sampleItem("зритель", "раз", 200000))
	items, _ := srv.queue.List()
	srv.removeFromQueue(ctx, items[0].ID, true, "ilya_mod")

	var actor, action string
	err := srv.db.SQL().QueryRow(
		`SELECT actor, action FROM mod_log ORDER BY created_at DESC LIMIT 1`).Scan(&actor, &action)
	if err != nil {
		t.Fatalf("действие модератора не записалось: %v", err)
	}
	if actor != "ilya_mod" {
		t.Fatalf("в логе не тот модератор: %q", actor)
	}
	if !strings.Contains(action, "удалил") {
		t.Fatalf("в логе не то действие: %q", action)
	}
}

// История нужна, чтобы потом найти, что играло и почему отказали.
func TestHistoryRecordsRejections(t *testing.T) {
	srv, _ := newTestServer(t,
		func(c *config.Config) { c.MaxTrackSeconds = 60 },
		noContent)
	srv.twitch.CheckAccount(context.Background())

	srv.enqueue(context.Background(), sampleItem("зритель", "длинный", 300000))

	var outcome, reason string
	err := srv.db.SQL().QueryRow(
		`SELECT outcome, reason FROM history ORDER BY played_at DESC LIMIT 1`).Scan(&outcome, &reason)
	if err != nil {
		t.Fatalf("отказ не попал в историю: %v", err)
	}
	if outcome != "rejected" || reason == "" {
		t.Fatalf("история без причины: %q / %q", outcome, reason)
	}
}
