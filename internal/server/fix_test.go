package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"songrequest-yandex/internal/links"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/queue"
)

// Ручное исправление должно не только починить текущий заказ, но и научить
// подбор: иначе стример будет исправлять один и тот же трек каждый стрим.
func TestFixReplacesTrackAndIsRemembered(t *testing.T) {
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tracks/") {
			ymJSON(w, []any{track("right", "Группа крови", "Кино", 286000, 70)})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	added, err := srv.queue.Add(queue.Item{
		Source: queue.SourcePoints, Requester: "Viewer",
		RawRequest: "кино группа крови",
		Provider:   "yandex", TrackID: "wrong", URI: "spotify:track:wrong",
		Title: "Группа крови (cover)", Artist: "Кто-то ещё",
		DurationMs: 200000, Uncertain: true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/queue/1/fix",
		strings.NewReader(`{"track_id":"right"}`))
	req.SetPathValue("id", strconv.FormatInt(added.ID, 10))
	srv.handleQueueFix(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("исправление не прошло: %d %s", rec.Code, rec.Body.String())
	}

	got, err := srv.queue.Get(added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrackID != "right" || got.Artist != "Кино" {
		t.Fatalf("трек не заменился: %+v", got)
	}
	if got.Uncertain {
		t.Fatal("после ручного выбора метка «неточно» должна сниматься")
	}
	// Заказ остаётся тем же самым: и кто просил, и чем возвращать баллы.
	if got.Requester != "Viewer" || got.RawRequest != "кино группа крови" {
		t.Fatalf("исправление потеряло сам заказ: %+v", got)
	}

	// Главное: то же самое впредь находится без поиска.
	key := match.Key(match.Parse(links.Strip("кино группа крови")))
	hit, ok, err := srv.matchCache.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || hit.TrackID != "right" {
		t.Fatalf("исправление не запомнилось: %+v ok=%v", hit, ok)
	}
	if !hit.Manual {
		t.Fatal("выбор человека должен быть помечен ручным, иначе его перезапишет автоподбор")
	}
}

// Поиск для выбора должен понимать вставленную ссылку: перепечатывать
// название, когда ссылка уже в буфере, — лишняя работа и лишняя ошибка.
func TestFixSearchAcceptsYandexLink(t *testing.T) {
	var searched bool
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search"):
			searched = true
			writeSearch(w)
		case strings.HasPrefix(r.URL.Path, "/tracks/"):
			ymJSON(w, []any{track("42", "Bohemian Rhapsody", "Queen", 354000, 82)})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	rec := httptest.NewRecorder()
	srv.handleSearch(rec, httptest.NewRequest(http.MethodGet,
		"/api/search?q=https://music.yandex.ru/album/1/track/42", nil))

	if searched {
		t.Fatal("по ссылке искать нечего")
	}
	if !strings.Contains(rec.Body.String(), "Bohemian Rhapsody") {
		t.Fatalf("трек по ссылке не вернулся: %s", rec.Body.String())
	}
}
