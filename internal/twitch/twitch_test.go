package twitch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
)

type fakeSecrets struct {
	mu   sync.Mutex
	data map[string]string
}

func (f *fakeSecrets) PutJSON(name string, v any) error {
	b, _ := json.Marshal(v)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		f.data = map[string]string{}
	}
	f.data[name] = string(b)
	return nil
}

func (f *fakeSecrets) GetJSON(name string, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, ok := f.data[name]
	if !ok {
		return io.EOF
	}
	return json.Unmarshal([]byte(raw), v)
}

func (f *fakeSecrets) Delete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, name)
	return nil
}

type call struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
	Form   map[string]string
}

type recorder struct {
	mu    sync.Mutex
	calls []call
}

func (r *recorder) add(c call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *recorder) find(method, path string) *call {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.calls {
		if r.calls[i].Method == method && r.calls[i].Path == path {
			return &r.calls[i]
		}
	}
	return nil
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *recorder) {
	t.Helper()

	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
				c.Form = map[string]string{}
				for k, v := range parseForm(string(raw)) {
					c.Form[k] = v
				}
			} else {
				json.Unmarshal(raw, &c.Body)
			}
		}
		rec.add(c)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Update(func(c *config.Config) { c.TwitchClientID = "тестовый-client-id" })

	log, err := logx.New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })

	c := New(cfg, log, &fakeSecrets{})
	c.helixURL = srv.URL
	c.idURL = srv.URL
	c.tokens = tokens{
		AccessToken:  "тестовый-ключ",
		RefreshToken: "тестовый-ключ-обновления",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	return c, rec
}

func parseForm(raw string) map[string]string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for k := range values {
		out[k] = values.Get(k)
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ── вход по коду ───────────────────────────────────────────────────────

func TestDeviceLoginAsksForRequiredScopes(t *testing.T) {
	c, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"device_code": "устройство", "user_code": "ABCD-1234",
			"verification_uri": "https://twitch.tv/activate", "expires_in": 1800, "interval": 5,
		})
	})

	login, err := c.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if login.UserCode != "ABCD-1234" {
		t.Fatalf("код для стримера: %q", login.UserCode)
	}

	got := rec.find(http.MethodPost, "/device")
	if got == nil {
		t.Fatal("не сходили за кодом")
	}
	if got.Form["client_id"] == "" {
		t.Fatal("без client_id Twitch код не выдаст")
	}
	// Без этих прав нельзя ни создать награду, ни вернуть баллы.
	for _, want := range []string{"channel:manage:redemptions", "channel:read:redemptions"} {
		if !strings.Contains(got.Form["scopes"], want) {
			t.Errorf("в запросе прав не хватает %q (там %q)", want, got.Form["scopes"])
		}
	}
	if strings.Contains(got.Form["scopes"], "client_secret") {
		t.Error("публичному клиенту секрет не нужен")
	}
}

func TestDeviceLoginWithoutClientIDIsClear(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	c.cfg.Update(func(cfg *config.Config) { cfg.TwitchClientID = "" })

	_, err := c.StartLogin(context.Background())
	if errs.CodeOf(err) != errs.TwitchNoClientID {
		t.Fatalf("ждали код %s, получили %v", errs.TwitchNoClientID, err)
	}
}

// Пока стример не ввёл код, Twitch отвечает отказом authorization_pending.
// Это обычный ход событий, а не ошибка: ждём дальше.
func TestWaitLoginKeepsWaitingWhilePending(t *testing.T) {
	var polls int32

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			writeJSON(w, map[string]any{
				"device_code": "устройство", "user_code": "ABCD-1234",
				"verification_uri": "https://twitch.tv/activate", "expires_in": 1800, "interval": 1,
			})
		case "/token":
			if atomic.AddInt32(&polls, 1) < 3 {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"status":400,"message":"authorization_pending"}`))
				return
			}
			writeJSON(w, map[string]any{
				"access_token": "новый-ключ", "refresh_token": "новый-ключ-обновления",
				"expires_in": 14400, "scope": []string{"channel:manage:redemptions"},
			})
		}
	})

	if _, err := c.StartLogin(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.WaitLogin(ctx); err != nil {
		t.Fatalf("ожидание кода должно завершиться успехом: %v", err)
	}
	if polls < 3 {
		t.Fatalf("ждали несколько опросов, было %d", polls)
	}
	if !c.Connected() {
		t.Fatal("после подтверждения кода вход должен быть сохранён")
	}
}

func TestWaitLoginStopsWhenUserDeclines(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			writeJSON(w, map[string]any{
				"device_code": "устройство", "user_code": "ABCD-1234",
				"verification_uri": "https://twitch.tv/activate", "expires_in": 1800, "interval": 1,
			})
		case "/token":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"status":400,"message":"authorization_declined"}`))
		}
	})

	if _, err := c.StartLogin(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := c.WaitLogin(ctx)
	if errs.CodeOf(err) != errs.TwitchAuthPending {
		t.Fatalf("ждали код %s, получили %v", errs.TwitchAuthPending, err)
	}
	if c.PendingCode() != nil {
		t.Fatal("после отказа код должен быть забыт")
	}
}

// ── канал и награда ────────────────────────────────────────────────────

func TestChannelWithoutPointsIsReportedNotHidden(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{
			map[string]any{"id": "42", "login": "обычный", "broadcaster_type": ""},
		}})
	})

	user, err := c.CheckAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user.HasChannelPoints() {
		t.Fatal("на обычном канале баллов нет")
	}
	if user.StatusLabel() != "обычный канал, баллов нет" {
		t.Fatalf("подпись типа канала: %q", user.StatusLabel())
	}

	_, err = c.EnsureReward(context.Background(), "")
	if errs.CodeOf(err) != errs.TwitchNoAffiliate {
		t.Fatalf("ждали код %s, получили %v", errs.TwitchNoAffiliate, err)
	}
}

func TestCreateRewardRequiresUserInput(t *testing.T) {
	c, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
			}})
		case r.URL.Path == "/channel_points/custom_rewards" && r.Method == http.MethodPost:
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "награда-1", "title": "Заказ трека", "cost": 1000, "is_enabled": true},
			}})
		}
	})

	if _, err := c.CheckAccount(context.Background()); err != nil {
		t.Fatal(err)
	}

	reward, err := c.EnsureReward(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if reward.ID != "награда-1" {
		t.Fatalf("id награды: %q", reward.ID)
	}

	got := rec.find(http.MethodPost, "/channel_points/custom_rewards")
	if got.Body["is_user_input_required"] != true {
		t.Fatal("без текста от зрителя заказывать нечего — поле обязано быть включено")
	}
	if !strings.Contains(got.Query, "broadcaster_id=42") {
		t.Fatalf("награда создаётся не на том канале: %q", got.Query)
	}
}

// Награда с прошлого запуска должна переиспользоваться, иначе на канале
// вырастет частокол одинаковых наград.
func TestExistingRewardIsReused(t *testing.T) {
	var created int32

	c, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "42", "login": "стример", "broadcaster_type": "partner"},
			}})
		case r.URL.Path == "/channel_points/custom_rewards" && r.Method == http.MethodGet:
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "старая", "title": "Заказ трека", "cost": 1000, "is_enabled": true},
			}})
		case r.URL.Path == "/channel_points/custom_rewards" && r.Method == http.MethodPost:
			atomic.AddInt32(&created, 1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if _, err := c.CheckAccount(context.Background()); err != nil {
		t.Fatal(err)
	}

	reward, err := c.EnsureReward(context.Background(), "старая")
	if err != nil {
		t.Fatal(err)
	}
	if reward.ID != "старая" {
		t.Fatalf("должны были переиспользовать прежнюю награду, а получили %q", reward.ID)
	}
	if created != 0 {
		t.Fatal("вторую награду создавать было незачем")
	}

	got := rec.find(http.MethodGet, "/channel_points/custom_rewards")
	// Без этого фильтра в список попадут чужие награды, за которые
	// приложение не может вернуть баллы.
	if !strings.Contains(got.Query, "only_manageable_by_broadcaster=true") {
		t.Fatalf("ищем не только свои награды: %q", got.Query)
	}
}

func TestForeignRewardWithSameTitleIsExplained(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
			}})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"Bad Request","status":400,"message":"CREATE_CUSTOM_REWARD_DUPLICATE_REWARD"}`))
		}
	})

	if _, err := c.CheckAccount(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err := c.EnsureReward(context.Background(), "")
	if errs.CodeOf(err) != errs.TwitchForeignAward {
		t.Fatalf("ждали код %s, получили %v", errs.TwitchForeignAward, err)
	}

	var e *errs.Error
	errs.As(err, &e)
	if !strings.Contains(e.Message, "переименуй") {
		t.Fatalf("текст должен подсказывать выход: %q", e.Message)
	}
}

// ── возврат баллов ─────────────────────────────────────────────────────

func TestRefundCancelsRedemption(t *testing.T) {
	c, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users" {
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
			}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if _, err := c.CheckAccount(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := c.RefundRedemption(context.Background(), "награда-1", "заказ-7"); err != nil {
		t.Fatal(err)
	}

	got := rec.find(http.MethodPatch, "/channel_points/custom_rewards/redemptions")
	if got == nil {
		t.Fatal("возврат баллов не ушёл в Twitch")
	}
	// Возврата как отдельного действия в Twitch нет: баллы возвращает
	// перевод заказа в CANCELED.
	if got.Body["status"] != "CANCELED" {
		t.Fatalf("статус для возврата баллов: %v", got.Body["status"])
	}
	for _, want := range []string{"broadcaster_id=42", "reward_id=", "id=", "%D0%B7%D0%B0%D0%BA%D0%B0%D0%B7-7"} {
		if !strings.Contains(got.Query, want) {
			t.Errorf("в запросе не хватает %q: %q", want, got.Query)
		}
	}
}

func TestFulfillMarksRedemptionDone(t *testing.T) {
	c, rec := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users" {
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
			}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c.CheckAccount(context.Background())

	if err := c.FulfillRedemption(context.Background(), "награда-1", "заказ-7"); err != nil {
		t.Fatal(err)
	}
	if got := rec.find(http.MethodPatch, "/channel_points/custom_rewards/redemptions"); got.Body["status"] != "FULFILLED" {
		t.Fatalf("статус выполненного заказа: %v", got.Body["status"])
	}
}

func TestRefundWithoutIdsFailsClearly(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{
			map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
		}})
	})
	c.CheckAccount(context.Background())

	err := c.RefundRedemption(context.Background(), "", "")
	if errs.CodeOf(err) != errs.TwitchRefund {
		t.Fatalf("ждали код %s, получили %v", errs.TwitchRefund, err)
	}
}

// ── обновление ключа ───────────────────────────────────────────────────

// У Twitch ключ обновления одноразовый: не сохранив новый, приложение
// разлогинится посреди стрима.
func TestRefreshStoresNewRefreshToken(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			writeJSON(w, map[string]any{
				"access_token": "ключ-2", "refresh_token": "обновление-2", "expires_in": 14400,
			})
		case "/users":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "42", "login": "стример", "broadcaster_type": "affiliate"},
			}})
		}
	})
	c.tokens.ExpiresAt = time.Now().Add(-time.Hour)

	if _, err := c.CheckAccount(context.Background()); err != nil {
		t.Fatal(err)
	}

	var saved tokens
	if err := c.secrets.GetJSON(keyringName, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.RefreshToken != "обновление-2" {
		t.Fatalf("новый ключ обновления не сохранён: %q", saved.RefreshToken)
	}
}
