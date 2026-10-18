package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"net/http/httptest"
	"net/url"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/links"
)

// Ссылка точнее любого поиска: зритель уже указал конкретную запись.
// Поиском тут можно только испортить, поэтому его не должно быть вовсе.
func TestYandexLinkTakesTrackDirectly(t *testing.T) {
	var searched bool
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search"):
			searched = true
			writeSearch(w)
		case r.URL.Path == "/tracks/2":
			ymJSON(w, []any{track("2", "Bohemian Rhapsody", "Queen", 354000, 82)})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv.twitch.CheckAccount(context.Background())

	r := placeOrder(srv, "врубай https://music.yandex.ru/album/1/track/2 пж")
	srv.resolveOrder(context.Background(), r)

	if searched {
		t.Fatal("по прямой ссылке искать нечего — трек указан точно")
	}
	m := orderMatch(t, srv, r.ID)
	if m.Title != "Bohemian Rhapsody" || m.Artist != "Queen" {
		t.Fatalf("взялось не то: %s — %s", m.Artist, m.Title)
	}
	items, _ := srv.queue.List()
	if len(items) != 1 || items[0].URI != ymURI("2") {
		t.Fatalf("в очередь попало не то: %+v", items)
	}
}

// Ссылка Spotify: song.link знает тот же трек в Яндексе — поиск не нужен.
func TestSpotifyLinkGoesThroughSongLink(t *testing.T) {
	var searched bool
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/odesli"):
			w.Write([]byte(`{"entityUniqueId":"S::1","entitiesByUniqueId":{"S::1":{"title":"Bohemian Rhapsody","artistName":"Queen"}},
				"linksByPlatform":{"yandex":{"url":"https://music.yandex.ru/album/9/track/77","entityUniqueId":"Y::77"}}}`))
		case strings.HasPrefix(r.URL.Path, "/search"):
			searched = true
			writeSearch(w)
		case r.URL.Path == "/tracks/77":
			ymJSON(w, []any{track("77", "Bohemian Rhapsody", "Queen", 354000, 82)})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv.twitch.CheckAccount(context.Background())

	r := placeOrder(srv, "https://open.spotify.com/track/11dFghVXANMlKmJXsNCbNl")
	srv.resolveOrder(context.Background(), r)

	if searched {
		t.Fatal("song.link дал прямую ссылку на Яндекс — искать было нечего")
	}
	items, _ := srv.queue.List()
	if len(items) != 1 || items[0].URI != ymURI("77") {
		t.Fatalf("в очередь попало не то: %+v", items)
	}
}

// Ссылка Apple Music без Яндекса в song.link — ищем по названию.
func TestAppleLinkFallsBackToSearch(t *testing.T) {
	var query string
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/odesli"):
			w.Write([]byte(`{"entityUniqueId":"A::1","entitiesByUniqueId":{"A::1":{"title":"Тамада","artistName":"Miyagi"}},"linksByPlatform":{}}`))
		case strings.HasPrefix(r.URL.Path, "/search"):
			query = r.URL.Query().Get("text")
			writeSearch(w, track("5", "Тамада", "Miyagi", 220000, 70))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv.twitch.CheckAccount(context.Background())

	r := placeOrder(srv, "https://music.apple.com/ru/song/tamada/1")
	srv.resolveOrder(context.Background(), r)

	if !strings.Contains(strings.ToLower(query), "тамада") {
		t.Fatalf("искали не то: %q", query)
	}
	items, _ := srv.queue.List()
	if len(items) != 1 || items[0].URI != ymURI("5") {
		t.Fatalf("в очередь попало не то: %+v", items)
	}
}

// Ссылка Яндекс Музыки, а трек по номеру не отдался: читаем открытую точку
// и ищем по названию.
func TestYandexLinkBecomesSearch(t *testing.T) {
	page := `<html><head>
		<meta property="og:title" content="Кино — Группа крови">
		<meta property="og:description" content="Кино • Трек • 1988">
	</head></html>`

	yandex := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})

	var queries []string
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/search") {
			queries = append(queries, r.URL.Query().Get("text"))
			ymJSON(w, map[string]any{"tracks": map[string]any{"results": []any{
				track("kino", "Группа крови", "Кино", 286000, 60),
			}}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv.twitch.CheckAccount(context.Background())
	srv.ymLinks = yandexReaderFor(yandex)

	r := placeOrder(srv, "https://music.yandex.ru/album/1/track/2")
	srv.resolveOrder(context.Background(), r)

	if len(queries) == 0 {
		t.Fatal("после разбора страницы должен был пойти поиск в Spotify")
	}
	// В запрос должно уйти то, что вычитано со страницы, а не сам адрес.
	joined := strings.Join(queries, " | ")
	if !strings.Contains(joined, "Кино") || strings.Contains(joined, "http") {
		t.Fatalf("искали не по названию со страницы: %s", joined)
	}

	if m := orderMatch(t, srv, r.ID); m.State != app.MatchFound {
		t.Fatalf("трек не нашёлся: %q %s", m.State, m.Note)
	}
}

// Страница Яндекса не открылась — заказ не должен пропасть: ищем по
// остальному тексту сообщения.
func TestYandexLinkFallsBackToText(t *testing.T) {
	dead := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var queries []string
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/search") {
			queries = append(queries, r.URL.Query().Get("text"))
			ymJSON(w, map[string]any{"tracks": map[string]any{"results": []any{
				track("kino", "Группа крови", "Кино", 286000, 60),
			}}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv.twitch.CheckAccount(context.Background())
	srv.ymLinks = yandexReaderFor(dead)

	r := placeOrder(srv, "Кино - Группа крови https://music.yandex.ru/album/1/track/2")
	srv.resolveOrder(context.Background(), r)

	if len(queries) == 0 {
		t.Fatal("после неудачи со страницей надо искать по тексту заказа")
	}
	if strings.Contains(strings.Join(queries, " "), "http") {
		t.Fatalf("адрес попал в поисковый запрос: %v", queries)
	}
	if m := orderMatch(t, srv, r.ID); m.State != app.MatchFound {
		t.Fatalf("заказ пропал: %q", m.State)
	}
}

// newStubServer поднимает подставной сайт и отдаёт его адрес.
func newStubServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// yandexReaderFor делает читалку, которая ходит на подставной сайт вместо
// настоящего Яндекса: адрес узнаётся по домену, поэтому подменяем не адрес,
// а то, куда уходит запрос.
func yandexReaderFor(base string) *links.YandexReader {
	target, _ := url.Parse(base)
	r := links.NewYandexReader()
	r.HTTP = &http.Client{Transport: &rewrite{to: target}}
	return r
}

type rewrite struct{ to *url.URL }

func (t *rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.to.Scheme
	clone.URL.Host = t.to.Host
	clone.Host = t.to.Host
	return http.DefaultTransport.RoundTrip(clone)
}
