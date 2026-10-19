// Package twitch — вход в Twitch, награда за баллы и приём заказов.
package twitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
	"songrequest-yandex/internal/secrets"
)

const (
	helixBase   = "https://api.twitch.tv/helix"
	idBase      = "https://id.twitch.tv/oauth2"
	keyringName = "twitch"
)

// SecretStore — хранилище токенов, интерфейсом ради тестов.
type SecretStore interface {
	PutJSON(name string, v any) error
	GetJSON(name string, v any) error
	Delete(name string) error
}

// Client — всё общение с Twitch.
type Client struct {
	cfg     *config.File
	log     *logx.Logger
	secrets SecretStore
	http    *http.Client

	// Адреса вынесены в поля, чтобы тесты подставляли свой сервер.
	helixURL string
	idURL    string

	mu      sync.RWMutex
	tokens  tokens
	user    *User
	pending *deviceFlow
	// checked и checkErr — чем кончилась последняя проверка канала.
	checked  bool
	checkErr error

	// mods — кто на канале модератор. Со своим замком: список спрашивается у
	// Twitch и живёт своей жизнью, а под общим замком висели бы сетевые
	// запросы. См. moderators.go.
	mods mods

	refreshMu sync.Mutex
}

// User — владелец канала, под которым выполнен вход.
type User struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	Type        string `json:"broadcaster_type"` // partner | affiliate | пусто
}

// HasChannelPoints сообщает, есть ли на канале баллы. Они бывают только у
// аффилиатов и партнёров — на обычном канале награду создать невозможно,
// и узнать об этом надо до стрима, а не в момент первого заказа.
func (u *User) HasChannelPoints() bool {
	return u != nil && (u.Type == "affiliate" || u.Type == "partner")
}

// StatusLabel — как показать тип канала стримеру.
func (u *User) StatusLabel() string {
	switch {
	case u == nil:
		return "неизвестно"
	case u.Type == "partner":
		return "партнёр"
	case u.Type == "affiliate":
		return "аффилиат"
	default:
		return "обычный канал, баллов нет"
	}
}

// New создаёт клиент и подтягивает сохранённый вход.
func New(cfg *config.File, log *logx.Logger, sec SecretStore) *Client {
	c := &Client{
		cfg:      cfg,
		log:      log,
		secrets:  sec,
		helixURL: helixBase,
		idURL:    idBase,
		http:     &http.Client{Timeout: 15 * time.Second},
	}

	// Подмена адресов для проверки на подставном сервере. В обычной работе
	// переменных нет, и приложение ходит в настоящий Twitch.
	if base := os.Getenv("SONGREQUEST_TWITCH_BASE"); base != "" {
		c.helixURL = base + "/helix"
		c.idURL = base + "/oauth2"
		log.Warn("Twitch подменён на тестовый сервер", "адрес", base)
	}

	var saved tokens
	switch err := sec.GetJSON(keyringName, &saved); {
	case err == nil:
		c.tokens = saved
		log.Redactor.Add(saved.AccessToken, saved.RefreshToken)
		log.Info("нашёл сохранённый вход в Twitch")
	case errors.Is(err, secrets.ErrNotFound):
		log.Debug("сохранённого входа в Twitch нет")
	default:
		log.Warn("не смог прочитать сохранённый вход в Twitch", "ошибка", err)
	}
	return c
}

// Connected сообщает, есть ли вход (без похода в сеть).
func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tokens.RefreshToken != ""
}

// Account отдаёт последние сведения о канале.
func (c *Client) Account() *User {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.user
}

// LastCheck сообщает, чем кончилась последняя проверка канала. См. такую же
// у Spotify: done=false означает «ещё не доходили», а не «сломалось».
func (c *Client) LastCheck() (done bool, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.checked, c.checkErr
}

func (c *Client) rememberCheck(err error) {
	c.mu.Lock()
	c.checked = true
	c.checkErr = err
	c.mu.Unlock()
}

// CheckAccount узнаёт, кто вошёл и есть ли на канале баллы.
func (c *Client) CheckAccount(ctx context.Context) (*User, error) {
	var out struct {
		Data []User `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/users", nil, &out); err != nil {
		c.rememberCheck(err)
		return nil, err
	}
	if len(out.Data) == 0 {
		err := errs.New(errs.TwitchBadResponse, "Twitch не сказал, кто вошёл.")
		c.rememberCheck(err)
		return nil, err
	}

	user := out.Data[0]
	c.mu.Lock()
	c.user = &user
	c.checked = true
	c.checkErr = nil
	c.mu.Unlock()

	c.log.Info("Twitch подключён",
		"канал", user.Login, "тип_канала", user.Type, "id", user.ID)
	return &user, nil
}

// do выполняет запрос к Helix: подставляет заголовки, повторяет при временных
// сбоях и переводит ответы в понятные ошибки с кодом.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}

	const maxAttempts = 4
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		token, err := c.token(ctx)
		if err != nil {
			return err
		}

		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.helixURL+path, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Client-Id", c.cfg.Get().TwitchClientID)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = errs.Wrap(errs.TwitchUnreachable, "Twitch не отвечает. Проверь интернет.", err)
			c.log.Warn("запрос к Twitch не дошёл", "путь", path, "попытка", attempt, "ошибка", err)
			if !sleepCtx(ctx, backoff(attempt)) {
				return lastErr
			}
			continue
		}

		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = errs.Wrap(errs.TwitchUnreachable, "Twitch оборвал ответ.", readErr)
			if !sleepCtx(ctx, backoff(attempt)) {
				return lastErr
			}
			continue
		}

		switch {
		case resp.StatusCode == http.StatusNoContent:
			return nil

		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if out == nil || len(data) == 0 {
				return nil
			}
			if err := json.Unmarshal(data, out); err != nil {
				c.log.Error("не разобрал ответ Twitch", "путь", path, "ответ", string(data))
				return errs.Wrap(errs.TwitchBadResponse, "Twitch ответил непонятным образом.", err)
			}
			return nil

		case resp.StatusCode == http.StatusUnauthorized:
			c.log.Debug("Twitch вернул 401, обновляю вход", "путь", path)
			c.mu.Lock()
			c.tokens.AccessToken = ""
			c.tokens.ExpiresAt = time.Time{}
			c.mu.Unlock()
			lastErr = errs.New(errs.TwitchAuthExpired,
				"Слетела авторизация Twitch. Нажми «Подключить Twitch» заново.")
			continue

		case resp.StatusCode == http.StatusTooManyRequests:
			wait := rateLimitWait(resp, attempt)
			c.log.Warn("Twitch просит подождать", "путь", path, "пауза", wait.String())
			lastErr = errs.New(errs.TwitchRateLimit,
				"Twitch попросил сделать паузу. Приложение подождёт и попробует снова.")
			if !sleepCtx(ctx, wait) {
				return lastErr
			}
			continue

		case resp.StatusCode >= 500:
			c.log.Warn("Twitch отвечает ошибкой", "путь", path,
				"код_http", resp.StatusCode, "ответ", string(data), "попытка", attempt)
			lastErr = errs.New(errs.TwitchUnreachable, "У Twitch временные неполадки. Пробую ещё раз.")
			if !sleepCtx(ctx, backoff(attempt)) {
				return lastErr
			}
			continue

		default:
			c.log.Error("Twitch отказал", "путь", path, "метод", method,
				"код_http", resp.StatusCode, "ответ", string(data))
			return apiError(resp.StatusCode, data)
		}
	}
	return lastErr
}

// apiError переводит отказ Twitch в понятную стримеру формулировку.
func apiError(statusCode int, data []byte) error {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	json.Unmarshal(data, &e)

	switch {
	case statusCode == http.StatusForbidden:
		// Twitch отвечает 403 и когда на канале нет баллов, и когда прав не
		// хватает. Разбирать по тексту ненадёжно, поэтому текст для человека
		// покрывает оба случая, а точную формулировку Twitch мы кладём в лог.
		return errs.New(errs.TwitchNoAffiliate,
			"Twitch не разрешил работать с наградами. Чаще всего это значит, что на канале нет баллов — они бывают только у аффилиатов и партнёров. Заказы за баллы работать не будут, донаты — будут.")
	case statusCode == http.StatusBadRequest:
		// Объяснение Twitch английское и техническое — оно для лога.
		return errs.Wrap(errs.TwitchBadResponse,
			"Twitch не принял запрос. Подробности в логе.",
			errors.New(firstNonEmpty(e.Message, "без объяснения")))
	case statusCode == http.StatusNotFound:
		return errs.New(errs.TwitchBadResponse, "Twitch не нашёл то, что мы просили.")
	}
	return errs.New(errs.TwitchBadResponse,
		"Twitch отказал. Подробности в логе.")
}

// rateLimitWait читает, когда лимит обновится. Twitch присылает время в виде
// метки Unix, а не количества секунд, — тут легко ошибиться на несколько часов.
func rateLimitWait(resp *http.Response, attempt int) time.Duration {
	if v := resp.Header.Get("Ratelimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			wait := time.Until(time.Unix(unix, 0))
			if wait > 0 && wait < time.Minute {
				return wait + time.Second
			}
		}
	}
	return backoff(attempt)
}

func backoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	return base + time.Duration(rand.Int63n(int64(250*time.Millisecond)))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func contains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
