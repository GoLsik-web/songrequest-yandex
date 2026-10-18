package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Стример поменял цену награды в настройках — и ничего не изменилось: цена
// доезжала до канала только при следующем запуске приложения. Настройка,
// которая молча ничего не делает, хуже отсутствующей.
func TestRewardPriceReachesTheChannel(t *testing.T) {
	var (
		patched  bool
		gotCost  int
		gotTitle string
	)

	srv, _ := newTestServer(t, withClientID, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			writeTestJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "42", "login": "streamer", "broadcaster_type": "affiliate"},
			}})

		case strings.Contains(r.URL.Path, "custom_rewards") && r.Method == http.MethodGet:
			writeTestJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "r1", "title": "Заказ трека", "cost": 1000, "is_enabled": true},
			}})

		case strings.Contains(r.URL.Path, "custom_rewards") && r.Method == http.MethodPatch:
			patched = true
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			gotTitle, _ = body["title"].(string)
			if c, ok := body["cost"].(float64); ok {
				gotCost = int(c)
			}
			writeTestJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "r1", "title": gotTitle, "cost": gotCost, "is_enabled": true},
			}})

		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	srv.twitch.CheckAccount(t.Context())
	srv.mu.Lock()
	srv.rewardID = "r1"
	srv.mu.Unlock()

	// Стример правит цену в панели.
	cfg := srv.cfg.Get()
	cfg.RewardCost = 500
	body, _ := json.Marshal(cfg)
	rec := httptest.NewRecorder()
	srv.handleSetConfig(rec, httptest.NewRequest(http.MethodPost, "/api/config",
		strings.NewReader(string(body))))

	if rec.Code != http.StatusOK {
		t.Fatalf("настройки не сохранились: %d", rec.Code)
	}

	// Обновление канала идёт в фоне, чтобы не держать ответ панели.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !patched {
		time.Sleep(10 * time.Millisecond)
	}

	if !patched {
		t.Fatal("цена сохранилась в настройках, но до канала не доехала")
	}
	if gotCost != 500 {
		t.Fatalf("на канал уехала цена %d вместо 500", gotCost)
	}
}
