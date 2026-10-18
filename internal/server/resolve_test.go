package server

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/twitch"
)

// Сквозная проверка пути заказа: текст зрителя → поиск в Spotify → строка в
// панели. Отдельные части покрыты в internal/match, здесь важно, что они
// связаны правильно и результат доезжает до экрана.

func spotifySearchStub(items ...map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/search") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeSearch(w, items...)
	}
}

// writeSearch отвечает так, как отвечает поиск Яндекса.
func writeSearch(w http.ResponseWriter, items ...map[string]any) {
	if items == nil {
		items = []map[string]any{}
	}
	ymJSON(w, map[string]any{"tracks": map[string]any{"results": items}})
}

// ymJSON заворачивает ответ так, как это делает Яндекс: {"result": …}.
func ymJSON(w http.ResponseWriter, v any) {
	writeTestJSON(w, map[string]any{"result": v})
}

// ymURI — URI трека, собранного track().
func ymURI(id string) string { return "ym:" + id + ":al" + id }

// track — трек в том виде, в каком его присылает Яндекс. Популярность
// пересчитывается в лайки альбома так, чтобы подбор увидел то же число.
func track(id, title, artist string, ms, pop int) map[string]any {
	likes := int(math.Pow(10, float64(pop)/20))
	return map[string]any{
		"id": id, "title": title, "durationMs": ms, "available": true,
		"coverUri": "avatars.yandex.net/get-music-content/" + id + "/%%",
		"artists":  []any{map[string]any{"id": 1, "name": artist}},
		"albums": []any{map[string]any{
			"id": "al" + id, "title": title, "type": "album", "likesCount": likes,
		}},
	}
}

func orderMatch(t *testing.T, srv *Server, id string) app.OrderMatch {
	t.Helper()
	for _, r := range srv.state.Snapshot().Redemptions {
		if r.ID == id {
			return r.Match
		}
	}
	t.Fatalf("заказ %q пропал из панели", id)
	return app.OrderMatch{}
}

func placeOrder(srv *Server, text string) twitch.Redemption {
	r := twitch.Redemption{
		ID: "order-1", RewardID: "reward-1", UserLogin: "viewer",
		UserName: "Viewer", UserInput: text, RewardCost: 1000, RedeemedAt: time.Now(),
	}
	srv.state.AddRedemption(app.RedemptionView{
		ID: r.ID, RewardID: r.RewardID, User: r.UserName, Text: r.UserInput,
		Cost: r.RewardCost, At: r.RedeemedAt, Status: app.OrderNew,
		Match: app.OrderMatch{State: app.MatchSearching},
	})
	return r
}

func TestOrderTextBecomesTrack(t *testing.T) {
	srv, _ := newTestServer(t, nil, spotifySearchStub(
		track("orig", "Bohemian Rhapsody", "Queen", 354000, 82),
		track("other", "Blinding Lights", "The Weeknd", 200040, 93),
	))

	r := placeOrder(srv, "Queen - Bohemian Rhapsody (Official Video)")
	srv.resolveOrder(context.Background(), r)

	m := orderMatch(t, srv, r.ID)
	if m.State != app.MatchFound {
		t.Fatalf("ждали найденный трек, получили %q (%s)", m.State, m.Note)
	}
	if m.Artist != "Queen" || m.Title != "Bohemian Rhapsody" {
		t.Fatalf("подобрано не то: %s — %s", m.Artist, m.Title)
	}
	if m.CoverURL == "" || m.URI == "" {
		t.Fatalf("панели нужны обложка и идентификатор трека: %+v", m)
	}
}

// Заказ без маркера должен дать оригинал, даже если ускоренная версия
// популярнее. Это самый частый промах поиска.
func TestOrderWithoutMarkerPicksOriginal(t *testing.T) {
	srv, _ := newTestServer(t, nil, spotifySearchStub(
		track("sped", "Bohemian Rhapsody (Sped Up)", "Queen", 290000, 99),
		track("orig", "Bohemian Rhapsody", "Queen", 354000, 82),
	))

	r := placeOrder(srv, "Queen Bohemian Rhapsody")
	srv.resolveOrder(context.Background(), r)

	if m := orderMatch(t, srv, r.ID); m.TrackID != "orig" {
		t.Fatalf("вместо оригинала подобрано %q (%s — %s)", m.TrackID, m.Artist, m.Title)
	}
}

// Ничего похожего нет — честно говорим об этом, а не подсовываем случайное.
func TestOrderWithNoMatchIsReported(t *testing.T) {
	srv, _ := newTestServer(t, nil, spotifySearchStub(
		track("other", "Blinding Lights", "The Weeknd", 200040, 93),
	))

	r := placeOrder(srv, "асдфгыч выапролдж несуществующий")
	srv.resolveOrder(context.Background(), r)

	m := orderMatch(t, srv, r.ID)
	if m.State != app.MatchMissing {
		t.Fatalf("ждали «не нашлось», получили %q с треком %q (%s)", m.State, m.Title, m.Why)
	}
	if m.TrackID != "" {
		t.Fatalf("при отказе трека быть не должно: %q", m.TrackID)
	}
}

func TestOrderWithoutTextIsHandled(t *testing.T) {
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Error("искать нечего — в Spotify ходить не нужно")
	})

	r := placeOrder(srv, "   ")
	srv.resolveOrder(context.Background(), r)

	if m := orderMatch(t, srv, r.ID); m.State != app.MatchFailed {
		t.Fatalf("пустой заказ должен быть помечен как неразобранный, а он %q", m.State)
	}
}

// Ссылка на видео без проигрывателя — это не «зритель ничего не написал».
//
// У тестера не стоял mpv, ссылку играть было нечем, адрес вырезался из текста
// заказа — и зритель получал «ты не написал, что заказываешь», хотя написал
// ровно то, что просили.
func TestYouTubeLinkWithoutPlayerSaysWhy(t *testing.T) {
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Error("играть нечем — в Spotify ходить не нужно")
	})

	r := placeOrder(srv, "https://youtu.be/hZEPgjqOhS8")
	srv.resolveOrder(context.Background(), r)

	m := orderMatch(t, srv, r.ID)
	if m.State != app.MatchFailed {
		t.Fatalf("состояние заказа: %q", m.State)
	}
	if !strings.Contains(m.Note, "проигрыватель") {
		t.Fatalf("в панели не сказано про проигрыватель: %q", m.Note)
	}
}

// 27.08 владелец кинул обычную ссылку на YouTube и получил в ответ «ты не
// написал, что заказываешь. Баллы вернул» — при том что написал он ровно то,
// что просили. Ссылку не удалось прочитать, но зритель об этом не узнал:
// приложение отвечало ему самым сбивающим с толку текстом из возможных.
//
// Здесь то же самое, но ссылкой на Spotify: он на неё не отвечает.
func TestUnreadableLinkSaysWhy(t *testing.T) {
	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tracks/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r := placeOrder(srv, "https://open.spotify.com/track/7MZyREIMPkgc7v5vzCGw87")
	srv.resolveOrder(context.Background(), r)

	m := orderMatch(t, srv, r.ID)
	if m.State != app.MatchFailed {
		t.Fatalf("состояние заказа: %q", m.State)
	}
	if strings.Contains(m.Note, "не написал") {
		t.Fatalf("зритель написал ссылку, а ему говорят обратное: %q", m.Note)
	}
	if !strings.Contains(m.Note, "Ссылку прочитать не вышло") {
		t.Fatalf("в панели не сказано, что случилось со ссылкой: %q", m.Note)
	}
}

// Второй такой же заказ обязан браться из памяти: за стрим один трек
// заказывают десятками, и каждый раз ходить в Spotify пятью запросами незачем.
func TestRepeatedOrderComesFromCache(t *testing.T) {
	var searches int

	srv, _ := newTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/search") {
			searches++
			ymJSON(w, map[string]any{"tracks": map[string]any{
				"results": []any{track("orig", "Bohemian Rhapsody", "Queen", 354000, 82)},
			}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	first := placeOrder(srv, "Queen - Bohemian Rhapsody")
	srv.resolveOrder(context.Background(), first)
	after := searches
	if after == 0 {
		t.Fatal("первый заказ должен был сходить в Spotify")
	}

	// Тот же трек, написанный иначе, — ключ кэша это переживает.
	second := twitch.Redemption{ID: "order-2", UserName: "Другой", UserInput: "QUEEN — bohemian rhapsody"}
	srv.state.AddRedemption(app.RedemptionView{
		ID: second.ID, User: second.UserName, Text: second.UserInput,
		Status: app.OrderNew, Match: app.OrderMatch{State: app.MatchSearching},
	})
	srv.resolveOrder(context.Background(), second)

	if searches != after {
		t.Fatalf("повторный заказ снова полез в Spotify: было %d, стало %d", after, searches)
	}
	if m := orderMatch(t, srv, second.ID); m.TrackID != "orig" {
		t.Fatalf("из памяти пришло не то: %+v", m)
	}
}

// Пороги живут в настройках: их приходится подгонять по живым заказам.
func TestThresholdsComeFromConfig(t *testing.T) {
	srv, _ := newTestServer(t,
		func(c *config.Config) {
			// Требуем почти полного совпадения — тогда даже похожий трек
			// должен быть помечен как неточный.
			c.MatchAccept = 0.99
			c.MatchMaybe = 0.1
		},
		spotifySearchStub(track("orig", "Bohemian Rhapsody", "Queen", 354000, 82)))

	r := placeOrder(srv, "Queen - Bohemian Rhapsody")
	srv.resolveOrder(context.Background(), r)

	if m := orderMatch(t, srv, r.ID); m.State != app.MatchUncertain {
		t.Fatalf("при завышенном пороге совпадение должно быть неточным, а оно %q", m.State)
	}
}
