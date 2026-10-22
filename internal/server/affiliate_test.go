package server

import (
	"encoding/json"
	"context"
	"net/http"
	"strings"
	"testing"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
)

// Канал без баллов — самый опасный путь для доверия к приложению: если на нём
// вылезет «ошибка Twitch», человек решит, что программа нерабочая, и бросит
// установку. Поэтому путь проверяется целиком и отдельно.

func twitchState(s *Server) app.TwitchInfo { return s.state.Snapshot().Twitch }

func connState(s *Server, name string) app.ConnState {
	for _, c := range s.state.Snapshot().Connections {
		if c.Name == name {
			return c
		}
	}
	return app.ConnState{}
}

func TestOrdinaryChannelIsExplainedNotBroken(t *testing.T) {
	var createTried bool

	srv, _ := newTestServer(t,
		func(c *config.Config) { c.TwitchClientID = "есть" },
		func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/users":
				// Обычный канал: broadcaster_type пустой.
				writeTestJSON(w, map[string]any{"data": []any{
					map[string]any{"id": "42", "login": "обычныйканал", "broadcaster_type": ""},
				}})
			case strings.Contains(r.URL.Path, "custom_rewards"):
				createTried = true
				w.WriteHeader(http.StatusForbidden)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		})

	srv.startTwitch(context.Background())

	// К созданию награды даже не подступаемся: тип канала известен заранее,
	// и лишний отказ от Twitch в логе только запутает разбор.
	if createTried {
		t.Fatal("на канале без баллов награду создавать не нужно")
	}

	info := twitchState(srv)
	if info.HasPoints {
		t.Fatal("баллов на обычном канале нет")
	}
	if info.RewardReady {
		t.Fatal("награды нет и быть не может")
	}
	if info.NoteCode != "TW-05" {
		t.Fatalf("ждали код TW-05, получили %q", info.NoteCode)
	}

	// Ячейка подключения не должна быть красной: чинить нечего.
	conn := connState(srv, "Twitch")
	if conn.Level != app.ConnIdle {
		t.Fatalf("канал без баллов — не поломка, ждали уровень %q, получили %q",
			app.ConnIdle, conn.Level)
	}
	if !strings.Contains(conn.Detail, "баллов на канале нет") {
		t.Fatalf("в ленте должно быть понятно написано, что происходит: %q", conn.Detail)
	}

	// Сообщение — предупреждение, а не ошибка, и без технических слов.
	notices := srv.state.Snapshot().Notices
	last := notices[len(notices)-1]
	if last.Level != "warn" {
		t.Fatalf("это не ошибка, а условие Twitch: %+v", last)
	}
	for _, forbidden := range []string{"403", "Forbidden", "error", "broadcaster_type"} {
		if strings.Contains(last.Text, forbidden) {
			t.Fatalf("в тексте для человека не должно быть %q: %q", forbidden, last.Text)
		}
	}
	if !strings.Contains(last.Text, "аффилиат") {
		t.Fatalf("человек должен понять причину: %q", last.Text)
	}
	// Главное: он не должен решить, что программа сломана.
	if !strings.Contains(last.Text, "исправно") {
		t.Fatalf("текст обязан снять подозрение в поломке: %q", last.Text)
	}
}

// Тип канала мог измениться или Twitch мог ответить иначе — тогда отказ
// придёт уже на создании награды. Он тоже обязан превращаться в тот же
// понятный текст, а не в «403».
func TestForbiddenOnRewardBecomesHumanText(t *testing.T) {
	srv, _ := newTestServer(t,
		func(c *config.Config) { c.TwitchClientID = "есть" },
		func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/users":
				// Twitch говорит «аффилиат», а награду создать не даёт.
				writeTestJSON(w, map[string]any{"data": []any{
					map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
				}})
			case strings.Contains(r.URL.Path, "custom_rewards"):
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":"Forbidden","status":403,"message":"The ID in broadcaster_id must match the user ID found in the request's OAuth token."}`))
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		})

	srv.startTwitch(context.Background())

	info := twitchState(srv)
	if info.NoteCode != "TW-05" {
		t.Fatalf("ждали код TW-05, получили %q", info.NoteCode)
	}
	if strings.Contains(info.Note, "403") || strings.Contains(info.Note, "OAuth") {
		t.Fatalf("текст для человека не должен быть техническим: %q", info.Note)
	}
	if !strings.Contains(info.Note, "баллов") {
		t.Fatalf("причина должна быть названа: %q", info.Note)
	}

	// Приложение осталось живым: награды нет, но соединение ради чата
	// поднято — команды и ответы зрителям работать обязаны.
	srv.mu.Lock()
	running, reward := srv.eventsRunning, srv.eventsReward
	srv.mu.Unlock()
	if !running || reward != "" {
		t.Fatalf("ждали подписку только на чат, получили running=%v награды=%q", running, reward)
	}
}

// Аффилиат должен проходить весь путь до конца — иначе первый тест
// проверял бы просто «ничего не работает».
func TestAffiliateGetsRewardAndGreenLight(t *testing.T) {
	srv, _ := newTestServer(t,
		func(c *config.Config) { c.TwitchClientID = "есть" },
		func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/users":
				writeTestJSON(w, map[string]any{"data": []any{
					map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
				}})
			case strings.Contains(r.URL.Path, "custom_rewards") && r.Method == http.MethodPost:
				writeTestJSON(w, map[string]any{"data": []any{
					map[string]any{"id": "награда-1", "title": "Заказ трека", "cost": 1000, "is_enabled": true},
				}})
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		})

	srv.startTwitch(context.Background())

	info := twitchState(srv)
	if !info.HasPoints || !info.RewardReady {
		t.Fatalf("у аффилиата награда должна создаться: %+v", info)
	}
	if info.NoteCode != "" {
		t.Fatalf("жаловаться не на что, а в панели: %q %q", info.NoteCode, info.Note)
	}
}

// Награда «Заказ трека» на канале уже есть, но от другого приложения
// заказов. Раньше на этом всё и кончалось: награда не создавалась, а
// человек не понимал почему. Теперь приложение заводит свою, с пометкой.
func TestForeignRewardTitleGetsOwnName(t *testing.T) {
	var titles []string
	srv, _ := newTestServer(t,
		func(c *config.Config) { c.TwitchClientID = "есть" },
		func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/users":
				writeTestJSON(w, map[string]any{"data": []any{
					map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
				}})
			case strings.Contains(r.URL.Path, "custom_rewards") && r.Method == http.MethodPost:
				var body struct {
					Title string `json:"title"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				titles = append(titles, body.Title)
				if body.Title == "Заказ трека" {
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte(`{"error":"Bad Request","status":400,"message":"CREATE_CUSTOM_REWARD_DUPLICATE_REWARD"}`))
					return
				}
				writeTestJSON(w, map[string]any{"data": []any{
					map[string]any{"id": "награда-2", "title": body.Title, "cost": 1000, "is_enabled": true},
				}})
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		})

	srv.startTwitch(context.Background())

	info := twitchState(srv)
	if !info.RewardReady || info.RewardTitle != "Заказ трека (Яндекс)" {
		t.Fatalf("ждали свою награду с пометкой, получили %+v (пробовали %q)", info, titles)
	}
	if got := srv.cfg.Get().RewardTitle; got != "Заказ трека (Яндекс)" {
		t.Fatalf("новое название должно остаться в настройках, а там %q", got)
	}
}
