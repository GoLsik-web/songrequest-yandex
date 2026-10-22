// Package yandex — всё общение с Яндекс Музыкой.
//
// Официального API у Яндекс Музыки нет. Работаем через то же, через что
// работают её собственные приложения:
//
//   - api.music.yandex.net — поиск, треки, плейлисты, аккаунт. Запросы с
//     заголовком «Authorization: OAuth <токен>»;
//   - Ynison — постоянное соединение, через которое приложения Яндекса
//     делят между собой одну очередь воспроизведения. Через него мы ставим
//     заказ в очередь программы стримера и узнаём, что играет (ynison.go).
//
// И то и другое может поменяться без предупреждения, поэтому каждая ошибка
// здесь получает свой код (YM-xx): по нему с чужого компьютера сразу видно,
// какая часть отвалилась.
package yandex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
)

// SecretStore — хранилище токена. Интерфейсом, чтобы тесты не лезли в
// «Диспетчер учётных данных» настоящей машины.
type SecretStore interface {
	PutJSON(name string, v any) error
	GetJSON(name string, v any) error
	Delete(name string) error
}

// secretName — под каким именем токен лежит в хранилище паролей Windows.
const secretName = "yandex-music-token"

// ClientID — приложение Яндекс Музыки, от имени которого выдаётся токен.
//
// Своё приложение здесь завести нельзя: доступ к музыке Яндекс выдаёт только
// собственным клиентам. Этим же номером пользуются все сторонние программы
// для Яндекс Музыки.
const ClientID = "23cabbbdc6cd418abb4b39c32c41195d"

// storedToken — то, что лежит в хранилище.
type storedToken struct {
	AccessToken string    `json:"access_token"`
	ObtainedAt  time.Time `json:"obtained_at"`
}

// Me — аккаунт стримера.
type Me struct {
	UID         int64  `json:"uid"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	HasPlus     bool   `json:"has_plus"`
	// PlusKnown — Яндекс прислал сведения о подписке. Без них молчим, а не
	// пишем «Плюса нет»: путать «не сказал» и «нет» нельзя.
	PlusKnown bool `json:"plus_known"`
	// Country — страна аккаунта. Яндекс её не сообщает, а треки по странам
	// не делит так, как Spotify; поле пустое и нужно общему коду подбора.
	Country string `json:"country,omitempty"`
}

// Name — как показать аккаунт в панели.
func (m *Me) Name() string {
	if m == nil {
		return ""
	}
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Login
}

// Client — один экземпляр на приложение.
type Client struct {
	cfg     *config.File
	log     *logx.Logger
	secrets SecretStore
	http    *http.Client

	// Адреса вынесены в поля, чтобы тесты подставляли свой сервер.
	apiBase    string
	ynisonBase string

	mu       sync.RWMutex
	token    string
	me       *Me
	checked  bool
	checkErr error
	// lastRate — когда Яндекс последний раз просил сбавить темп.
	lastRate time.Time

	// ynison — постоянное соединение с очередью. Поднимается после входа.
	ynison *Ynison
}

// New создаёт клиент и достаёт сохранённый токен.
func New(cfg *config.File, log *logx.Logger, sec SecretStore) *Client {
	c := &Client{
		cfg:        cfg,
		log:        log,
		secrets:    sec,
		http:       &http.Client{Timeout: 15 * time.Second},
		apiBase:    "https://api.music.yandex.net",
		ynisonBase: "wss://ynison.music.yandex.ru",
	}
	var t storedToken
	if sec != nil {
		if err := sec.GetJSON(secretName, &t); err == nil {
			c.token = t.AccessToken
		}
	}
	c.ynison = newYnison(c)
	return c
}

// AuthURL — страница входа. После входа Яндекс возвращает человека на
// music.yandex.ru с токеном в адресе после «#»; его забирает окно входа.
func AuthURL() string {
	q := url.Values{}
	q.Set("response_type", "token")
	q.Set("client_id", ClientID)
	return "https://oauth.yandex.ru/authorize?" + q.Encode()
}

// TokenFromURL вынимает токен из адреса, на который Яндекс вернул после
// входа. Пустая строка — токена там нет.
func TokenFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	frag, err := url.ParseQuery(u.Fragment)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(frag.Get("access_token"))
}

// SetToken сохраняет токен и проверяет его на аккаунте.
func (c *Client) SetToken(ctx context.Context, token string) (*Me, error) {
	token = strings.TrimSpace(token)
	// Люди вставляют целиком адрес из браузера — принимаем и так.
	if strings.Contains(token, "access_token=") {
		token = TokenFromURL(token)
	}
	if token == "" {
		return nil, errs.New(errs.YandexNoToken, "Токен пустой.")
	}

	c.mu.Lock()
	old := c.token
	c.token = token
	c.mu.Unlock()

	me, err := c.CheckAccount(ctx)
	if err != nil {
		// Плохой токен не должен затирать рабочий.
		c.mu.Lock()
		c.token = old
		c.mu.Unlock()
		return nil, err
	}
	if c.secrets != nil {
		if err := c.secrets.PutJSON(secretName, storedToken{AccessToken: token, ObtainedAt: time.Now()}); err != nil {
			c.log.Warn("не сохранил токен Яндекса", "ошибка", err)
		}
	}
	c.ynison.Restart()
	return me, nil
}

// Connected — есть ли токен.
func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token != ""
}

// Account — аккаунт с последней проверки.
func (c *Client) Account() *Me {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.me
}

// LastCheck — чем кончилась последняя проверка аккаунта.
func (c *Client) LastCheck() (done bool, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.checked, c.checkErr
}

// Logout забывает токен.
func (c *Client) Logout() {
	c.mu.Lock()
	c.token = ""
	c.me = nil
	c.checked = false
	c.checkErr = nil
	c.mu.Unlock()
	if c.secrets != nil {
		_ = c.secrets.Delete(secretName)
	}
	c.ynison.Stop()
}

// RecentlyLimited — Яндекс недавно просил сбавить темп. Тогда опросы
// переходят на щадящий шаг.
func (c *Client) RecentlyLimited() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.lastRate.IsZero() && time.Since(c.lastRate) < 10*time.Minute
}

// Ynison — соединение с очередью.
func (c *Client) Ynison() *Ynison { return c.ynison }

// accessToken — токен под блокировкой.
func (c *Client) accessToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// CheckAccount спрашивает, чей это токен и есть ли Плюс.
func (c *Client) CheckAccount(ctx context.Context) (*Me, error) {
	var resp struct {
		Account struct {
			UID         int64  `json:"uid"`
			Login       string `json:"login"`
			DisplayName string `json:"displayName"`
			FullName    string `json:"fullName"`
		} `json:"account"`
		Plus *struct {
			HasPlus bool `json:"hasPlus"`
		} `json:"plus"`
	}
	err := c.get(ctx, "/account/status", nil, &resp)
	if err == nil && resp.Account.UID == 0 {
		// Яндекс отвечает 200 и на чужой токен, но без номера аккаунта.
		err = errs.New(errs.YandexAuthExpired, "Яндекс не узнал токен — войдите заново.")
	}

	var me *Me
	if err == nil {
		me = &Me{
			UID:         resp.Account.UID,
			Login:       resp.Account.Login,
			DisplayName: firstNonEmpty(resp.Account.DisplayName, resp.Account.FullName),
		}
		if resp.Plus != nil {
			me.PlusKnown = true
			me.HasPlus = resp.Plus.HasPlus
		}
	}

	c.mu.Lock()
	c.checked = true
	c.checkErr = err
	if me != nil {
		c.me = me
	}
	c.mu.Unlock()
	return me, err
}

// PlusProblem — что сказать стримеру о подписке. nil — всё хорошо или
// неизвестно.
func PlusProblem(me *Me) *errs.Error {
	if me == nil || !me.PlusKnown || me.HasPlus {
		return nil
	}
	return errs.New(errs.YandexNoPlus,
		"На аккаунте Яндекса нет Плюса — без него заказы будут играть только отрывками по 30 секунд.")
}

// uid — номер аккаунта; без него не работают плейлисты.
func (c *Client) uid(ctx context.Context) (int64, error) {
	if me := c.Account(); me != nil && me.UID != 0 {
		return me.UID, nil
	}
	me, err := c.CheckAccount(ctx)
	if err != nil {
		return 0, err
	}
	return me.UID, nil
}

// get — запрос с разбором поля result.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, q, nil, out)
}

// post — запрос с формой.
func (c *Client) post(ctx context.Context, path string, form url.Values, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, form, out)
}

// envelope — во что Яндекс заворачивает любой ответ.
type envelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, form url.Values, out any) error {
	token := c.accessToken()
	if token == "" {
		return errs.New(errs.YandexNoToken, "Яндекс Музыка не подключена.")
	}

	u := c.apiBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("Accept-Language", "ru")
	req.Header.Set("X-Yandex-Music-Client", "YandexMusicDesktopAppWindows/5.0.0")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errs.Wrap(errs.YandexUnreachable, "Яндекс Музыка не отвечает.", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return errs.Wrap(errs.YandexUnreachable, "Ответ Яндекса оборвался.", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errs.New(errs.YandexAuthExpired, "Вход в Яндекс слетел — войди заново.")
	case resp.StatusCode == http.StatusForbidden:
		// 403 — это не «вход слетел»: вход живой, но Яндекс не пускает.
		// Обычно из-за страны (VPN, сеть за границей) или ограничений
		// аккаунта. Перевход тут не поможет, и говорить про него нельзя.
		return errRegion()
	case resp.StatusCode == http.StatusTooManyRequests:
		c.mu.Lock()
		c.lastRate = time.Now()
		c.mu.Unlock()
		return errs.New(errs.YandexRateLimit, "Яндекс просит подождать: слишком много запросов.")
	case resp.StatusCode == http.StatusNotFound:
		return errs.New(errs.YandexBadResponse, "Яндекс не нашёл то, что просили.")
	case resp.StatusCode >= 500:
		return errs.Wrap(errs.YandexUnreachable, "Яндекс Музыка сейчас не работает. Попробуй ещё раз чуть позже.", fmt.Errorf("HTTP %d", resp.StatusCode))
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return errs.Wrap(errs.YandexBadResponse, "Яндекс ответил непонятно.", err)
	}
	if env.Error != nil {
		if env.Error.Name == "session-expired" {
			return errs.New(errs.YandexAuthExpired, "Вход в Яндекс слетел — войди заново.")
		}
		if env.Error.Name == "not-allowed" {
			return errRegion()
		}
		return errs.New(errs.YandexBadResponse, "Яндекс отказал: "+firstNonEmpty(env.Error.Message, env.Error.Name))
	}
	if resp.StatusCode >= 400 {
		return errs.New(errs.YandexBadResponse, fmt.Sprintf("Яндекс ответил ошибкой %d.", resp.StatusCode))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return errs.Wrap(errs.YandexBadResponse, "Яндекс прислал не то, чего ждали.", err)
	}
	return nil
}

// IsAuthError — ошибка значит «надо войти заново».
func IsAuthError(err error) bool {
	c := errs.CodeOf(err)
	return c == errs.YandexAuthExpired || c == errs.YandexNoToken
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

var errNoState = errors.New("ynison ещё не прислал состояние")

// errRegion — Яндекс отказал, хотя вход в порядке.
func errRegion() error {
	return errs.New(errs.YandexRegion,
		"Яндекс Музыка не пускает с этого подключения. Чаще всего мешает VPN или прокси "+
			"с адресом за границей — выключи его для Яндекса. Если не помогло, проверь, "+
			"что на music.yandex.ru в браузере музыка играет с этого же аккаунта.")
}
