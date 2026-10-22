package twitch

import (
	"errors"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"songrequest-yandex/internal/errs"
)

// scopes — всё, что понадобится приложению. Просим сразу за один заход:
// стример вводит код на своей странице Twitch, и гонять его туда второй раз
// ради ещё одного права — это лишний повод бросить установку.
var scopes = []string{
	"channel:manage:redemptions", // создать награду и вернуть баллы
	"channel:read:redemptions",   // получать события о заказах
	"user:read:chat",             // читать команды модераторов
	"user:write:chat",            // отвечать в чат
	"channel:bot",                // писать в чат от имени канала
	"moderation:read",            // знать, кто модератор
}

type tokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope"`
	// RefreshedAt — когда получен нынешний ключ обновления. У публичных
	// клиентов Twitch он живёт 30 дней с момента выдачи, поэтому за его
	// возрастом надо следить и предупреждать заранее, а не ловить отказ
	// посреди стрима.
	RefreshedAt time.Time `json:"refreshed_at"`
}

// refreshLifetime — сколько живёт ключ обновления у публичного клиента.
const refreshLifetime = 30 * 24 * time.Hour

// warnBefore — за сколько до конца начинаем предупреждать.
const warnBefore = 5 * 24 * time.Hour

func (t tokens) valid() bool {
	return t.AccessToken != "" && time.Now().Add(time.Minute).Before(t.ExpiresAt)
}

// deviceFlow — начатый вход по коду.
type deviceFlow struct {
	DeviceCode string
	UserCode   string
	VerifyURL  string
	Interval   time.Duration
	ExpiresAt  time.Time
}

// DeviceLogin — то, что показываем стримеру: код и адрес, где его ввести.
type DeviceLogin struct {
	UserCode  string    `json:"user_code"`
	VerifyURL string    `json:"verify_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// StartLogin начинает вход по коду.
//
// Обычный вход через браузер тут не подходит: у публичного клиента нет
// секрета, а Twitch для таких приложений предлагает device flow — стример
// открывает страницу Twitch и вводит короткий код. Заодно это единственный
// способ подключить чужой канал, не пуская человека к своему компьютеру.
func (c *Client) StartLogin(ctx context.Context) (*DeviceLogin, error) {
	clientID := strings.TrimSpace(c.cfg.Get().TwitchClientID)
	if clientID == "" {
		return nil, errs.New(errs.TwitchNoClientID,
			"Не заполнен Client ID Twitch. Как его получить — написано в инструкции.")
	}

	form := url.Values{
		"client_id": {clientID},
		"scopes":    {strings.Join(scopes, " ")},
	}

	var out struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		VerifyURL  string `json:"verification_uri"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	if err := c.postForm(ctx, "/device", form, &out); err != nil {
		return nil, err
	}
	if out.UserCode == "" || out.DeviceCode == "" {
		return nil, errs.New(errs.TwitchAuthStart, "Twitch не выдал код для входа.")
	}

	interval := time.Duration(out.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	expires := time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)

	c.mu.Lock()
	c.pending = &deviceFlow{
		DeviceCode: out.DeviceCode,
		UserCode:   out.UserCode,
		VerifyURL:  out.VerifyURL,
		Interval:   interval,
		ExpiresAt:  expires,
	}
	c.mu.Unlock()

	c.log.Info("начат вход в Twitch по коду",
		"адрес", out.VerifyURL, "код_действителен_до", expires.Format(time.TimeOnly))

	return &DeviceLogin{UserCode: out.UserCode, VerifyURL: out.VerifyURL, ExpiresAt: expires}, nil
}

// WaitLogin ждёт, пока стример подтвердит код на странице Twitch.
//
// Опрашивать приходится: Twitch не умеет сообщать о подтверждении сам.
// Интервал берём тот, который он назвал, и увеличиваем по его просьбе —
// иначе он начнёт отвечать slow_down и вход затянется.
func (c *Client) WaitLogin(ctx context.Context) error {
	c.mu.RLock()
	flow := c.pending
	c.mu.RUnlock()

	if flow == nil {
		return errs.New(errs.TwitchAuthStart, "Вход не начинался. Нажми «Подключить Twitch».")
	}

	clientID := c.cfg.Get().TwitchClientID
	interval := flow.Interval

	for {
		if !sleepCtx(ctx, interval) {
			return ctx.Err()
		}

		// Начали новый вход — этот больше никому не нужен: продолжать опрос
		// значит соревноваться с ним за одно и то же поле.
		c.mu.RLock()
		current := c.pending
		c.mu.RUnlock()
		if current != flow {
			c.log.Info("вход в Twitch начат заново, старое ожидание брошено")
			return nil
		}
		if time.Now().After(flow.ExpiresAt) {
			c.clearThisPending(flow)
			return errs.New(errs.TwitchAuthPending,
				"Код не подтвердили вовремя. Нажми «Подключить Twitch» и введи новый код.")
		}

		form := url.Values{
			"client_id":   {clientID},
			"scopes":      {strings.Join(scopes, " ")},
			"device_code": {flow.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}

		var out tokenResponse
		err := c.postForm(ctx, "/token", form, &out)

		switch {
		case err == nil && out.AccessToken != "":
			c.clearThisPending(flow)
			t := tokens{
				AccessToken:  out.AccessToken,
				RefreshToken: out.RefreshToken,
				ExpiresAt:    time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
				Scope:        strings.Join(out.Scope, " "),
				RefreshedAt:  time.Now(),
			}
			if err := c.saveTokens(t); err != nil {
				return errs.Wrap(errs.TwitchAuthStart, "Вход прошёл, но сохранить его не удалось.", err)
			}
			c.log.Info("вход в Twitch выполнен")
			return nil

		case isPending(err):
			continue // человек ещё не ввёл код, это нормально

		case isSlowDown(err):
			interval += 2 * time.Second
			c.log.Debug("Twitch просит опрашивать реже", "новый_интервал", interval.String())

		case err != nil:
			c.clearThisPending(flow)
			return err
		}
	}
}

// CancelLogin отменяет начатый вход.
func (c *Client) CancelLogin() { c.clearPending() }

// PendingCode отдаёт код, если вход начат и ещё не подтверждён.
func (c *Client) PendingCode() *DeviceLogin {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pending == nil || time.Now().After(c.pending.ExpiresAt) {
		return nil
	}
	return &DeviceLogin{
		UserCode:  c.pending.UserCode,
		VerifyURL: c.pending.VerifyURL,
		ExpiresAt: c.pending.ExpiresAt,
	}
}

func (c *Client) clearPending() {
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
}

// clearThisPending убирает только тот вход, который сам же и ждал.
//
// Иначе выходит так: стример нажал «Подключить Twitch», не успел ввести код,
// нажал ещё раз. Старая горутина живёт до получаса, дожидается «код истёк» и
// стирает ожидание — но уже новое. У человека на глазах пропадает код,
// который он в эту секунду вводит, и всплывает «Код истёк, нажми заново».
func (c *Client) clearThisPending(flow *deviceFlow) {
	c.mu.Lock()
	if c.pending == flow {
		c.pending = nil
	}
	c.mu.Unlock()
}

type tokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	Scope        []string `json:"scope"`
	TokenType    string   `json:"token_type"`
}

// authError — ответ Twitch, когда что-то пошло не так.
type authError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	code    string
}

func (e *authError) Error() string { return e.Message }

func isPending(err error) bool {
	var e *authError
	return errs.As(err, &e) && e.code == "authorization_pending"
}

func isSlowDown(err error) bool {
	var e *authError
	return errs.As(err, &e) && e.code == "slow_down"
}

// token отдаёт годный ключ доступа, при необходимости обновляя его.
func (c *Client) token(ctx context.Context) (string, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.mu.RLock()
	t := c.tokens
	c.mu.RUnlock()

	if t.valid() {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" {
		return "", errs.New(errs.TwitchAuthExpired,
			"Twitch не подключён. Нажми «Подключить Twitch» в панели.")
	}

	// Пустой Client ID — поломка настроек, а не входа. Раньше запрос уходил
	// как есть, Twitch отвечал «missing client id», и сохранённый вход
	// стирался: вернул Client ID на место — а входить всё равно заново.
	clientID := strings.TrimSpace(c.cfg.Get().TwitchClientID)
	if clientID == "" {
		return "", errs.New(errs.TwitchNoClientID,
			"Не заполнен Client ID Twitch. Впиши его в настройках — вход сохранён, заново подключаться не придётся.")
	}

	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	}

	var out tokenResponse
	if err := c.postForm(ctx, "/token", form, &out); err != nil {
		// Моргнувшая сеть — не повод выбрасывать вход.
		//
		// Раньше ключи стирались от любой ошибки, включая «Twitch не
		// отвечает». Ключ доступа живёт около четырёх часов, то есть
		// обновление приходится ровно на середину стрима: одна неудачная
		// попытка — и заказы за баллы перестают приходить, а стримеру надо
		// заново вводить код с телефона. Выбрасываем вход только когда Twitch
		// прямо сказал, что ключ ему больше не нравится.
		switch errs.CodeOf(err) {
		case errs.TwitchUnreachable, errs.TwitchRateLimit, errs.TwitchBadResponse:
			c.log.Warn("не обновил ключ доступа Twitch, вход сохраняю", "ошибка", err)
			return "", err
		}
		c.log.Warn("Twitch отказал в обновлении ключа, вход сброшен", "ошибка", err)
		c.forgetTokens()
		return "", errs.Wrap(errs.TwitchAuthExpired,
			"Слетела авторизация Twitch. Нажми «Подключить Twitch» заново.", err)
	}

	// У Twitch ключ обновления одноразовый: если не сохранить новый, следующее
	// обновление провалится и стримеру придётся входить заново посреди стрима.
	// Twitch присылает новый ключ обновления не всегда. Если прислал —
	// тридцатидневный отсчёт начинается заново; если нет — продолжает идти
	// от старой даты, и рано или поздно понадобится вход заново.
	rotated := out.RefreshToken != "" && out.RefreshToken != t.RefreshToken
	refreshedAt := t.RefreshedAt
	if rotated {
		refreshedAt = time.Now()
	}
	if refreshedAt.IsZero() {
		refreshedAt = time.Now()
	}

	updated := tokens{
		AccessToken:  out.AccessToken,
		RefreshToken: firstNonEmpty(out.RefreshToken, t.RefreshToken),
		ExpiresAt:    time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
		Scope:        firstNonEmpty(strings.Join(out.Scope, " "), t.Scope),
		RefreshedAt:  refreshedAt,
	}
	if err := c.saveTokens(updated); err != nil {
		c.log.Warn("не сохранил обновлённый вход в Twitch", "ошибка", err)
	}
	// Пишем в лог, обновился ли ключ обновления: от этого зависит, придётся
	// ли стримеру вводить код заново через месяц, и по одному этому полю
	// потом будет видно, как оно на самом деле.
	c.log.Info("ключ доступа Twitch обновлён",
		"ключ_обновления_сменился", rotated,
		"ему_дней", int(time.Since(refreshedAt).Hours()/24))
	return updated.AccessToken, nil
}

// postForm обращается к серверу авторизации Twitch.
func (c *Client) postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.idURL+path,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return errs.Wrap(errs.TwitchUnreachable, "Twitch не отвечает. Проверь интернет.", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errs.Wrap(errs.TwitchUnreachable, "Twitch оборвал ответ.", err)
	}

	if resp.StatusCode != http.StatusOK {
		var raw struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		json.Unmarshal(data, &raw)

		// «Ещё не подтвердили» — это не ошибка, а обычный ход событий, поэтому
		// в лог оно не идёт: иначе лог заполнится им за минуту ожидания.
		code := firstNonEmpty(raw.Error, raw.Message)
		if code == "authorization_pending" || code == "slow_down" {
			return &authError{Status: raw.Status, Message: raw.Message, code: code}
		}

		c.log.Error("Twitch отказал во входе",
			"путь", path, "код_http", resp.StatusCode, "ответ", string(data))
		return authErrorText(resp.StatusCode, raw.Status, code, raw.Message)
	}

	if err := json.Unmarshal(data, out); err != nil {
		c.log.Error("не разобрал ответ Twitch", "путь", path, "ответ", string(data))
		return errs.Wrap(errs.TwitchBadResponse, "Twitch ответил непонятным образом.", err)
	}

	// Токены знаем — значит вычищаем их из лога.
	if tr, ok := out.(*tokenResponse); ok {
		c.log.Redactor.Add(tr.AccessToken, tr.RefreshToken)
	}
	return nil
}

// authErrorText переводит отказ во входе на человеческий язык.
//
// httpStatus — настоящий код ответа, а status — тот, что Twitch иногда кладёт
// в тело. Различать их важно: token() стирает вход насовсем на всём, кроме
// «Twitch не отвечает», «просит подождать» и «ответил непонятным образом», —
// а раньше 500, 502 и 429 от сервера авторизации сюда не попадали и давали
// TW-03, то есть сброс входа посреди стрима с требованием заново вводить код
// с телефона. Ровно та беда, от которой защита писалась.
func authErrorText(httpStatus, status int, code, message string) error {
	switch {
	case httpStatus == http.StatusTooManyRequests:
		return errs.New(errs.TwitchRateLimit,
			"Twitch просит подождать. Попробуй ещё раз через минуту.")
	case httpStatus >= 500:
		return errs.New(errs.TwitchUnreachable,
			"У Twitch временные неполадки со входом. Попробуй ещё раз.")
	}
	return authErrorReason(status, code, message)
}

// authErrorReason разбирает уже настоящий отказ — тот, после которого вход
// действительно надо начинать заново.
func authErrorReason(status int, code, message string) error {
	switch code {
	case "authorization_declined", "access_denied":
		return errs.New(errs.TwitchAuthPending,
			"Доступ не выдан: на странице Twitch нажали «Отмена». Попробуй ещё раз.")
	case "expired_token", "device_code_expired", "invalid device code":
		return errs.New(errs.TwitchAuthPending,
			"Код истёк. Нажми «Подключить Twitch» и введи новый.")
	case "invalid_client":
		return errs.New(errs.TwitchNoClientID,
			"Twitch не узнал Client ID. Проверь, что он вставлен целиком и без пробелов.")
	}
	if status == http.StatusBadRequest && message != "" {
		// Текст Twitch — по-английски и для разработчиков: он уходит в лог,
		// а стримеру — понятная фраза.
		return errs.Wrap(errs.TwitchAuthStart,
			"Twitch не принял вход. Нажми «Подключить Twitch» и попробуй ещё раз.", errors.New(message))
	}
	return errs.New(errs.TwitchAuthStart,
		"Не удалось войти в Twitch. Попробуй ещё раз, а если не выйдет — выгрузи лог.")
}

func (c *Client) saveTokens(t tokens) error {
	c.mu.Lock()
	c.tokens = t
	c.mu.Unlock()
	c.log.Redactor.Add(t.AccessToken, t.RefreshToken)
	return c.secrets.PutJSON(keyringName, t)
}

func (c *Client) forgetTokens() {
	c.mu.Lock()
	c.tokens = tokens{}
	c.user = nil
	c.mu.Unlock()
	// Список модераторов принадлежал прежнему каналу. У нового канала
	// модераторы другие, и отвечать по чужому списку — верный способ пустить
	// к командам не того человека.
	c.ForgetModerators()
	c.secrets.Delete(keyringName)
}

// Logout забывает вход по кнопке в панели.
func (c *Client) Logout() {
	c.forgetTokens()
	c.clearPending()
	c.log.Info("вход в Twitch сброшен")
}

// RefreshExpiry сообщает, когда придётся входить заново, и осталось ли до
// этого меньше нескольких дней.
//
// Ноль означает «неизвестно»: вход был сделан старой версией приложения,
// которая дату не записывала.
func (c *Client) RefreshExpiry() (at time.Time, soon bool) {
	c.mu.RLock()
	refreshedAt := c.tokens.RefreshedAt
	connected := c.tokens.RefreshToken != ""
	c.mu.RUnlock()

	if !connected || refreshedAt.IsZero() {
		return time.Time{}, false
	}
	at = refreshedAt.Add(refreshLifetime)
	return at, time.Until(at) < warnBefore
}

// MissingScopes — права, которых не хватает у нынешнего входа.
//
// Зачем. Список нужных прав со временем растёт: «user:read:chat» появилось
// позже самого приложения. Вход, выданный до этого, продолжает работать —
// заказы за баллы идут, панель зелёная, — но подписаться на чат им нельзя, и
// все команды молчат. Снаружи это выглядит как «!скип сломался», хотя ломаться
// нечему: приложению просто не дали права, о котором никто не спрашивал.
//
// Пустой ответ означает «всё на месте». Пустой вход — тоже: спрашивать не о
// чем, пока человек не подключился.
func (c *Client) MissingScopes() []string {
	granted := c.GrantedScopes()
	if granted == "" {
		return nil
	}
	have := map[string]bool{}
	for _, s := range strings.Fields(granted) {
		have[s] = true
	}
	var missing []string
	for _, want := range scopes {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	return missing
}

// GrantedScopes — права, которые стример выдал при входе.
func (c *Client) GrantedScopes() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tokens.Scope
}
