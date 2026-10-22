package donations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
)

// DonationAlerts — самый распространённый у русскоязычных стримеров сервис.
//
// Вход у него без секрета приложения: response_type=token отдаёт ключ прямо
// в адресной строке. Дальше — Centrifugo: сначала берём у DonationAlerts
// токен подключения, потом отдельный токен на канал, и только после этого
// начинают приходить донаты.
type DonationAlerts struct {
	log     *logx.Logger
	tokens  TokenStore
	status  func(bool, string)
	apiBase string
	wsURL   string

	http  *http.Client
	msgID int64
}

// TokenStore — где лежит ключ доступа. Интерфейсом, чтобы пакет не знал про
// хранилище учётных данных Windows.
type TokenStore interface {
	PutJSON(name string, v any) error
	GetJSON(name string, v any) error
	Delete(name string) error
}

const daKeyring = "donationalerts"

type daToken struct {
	AccessToken string `json:"access_token"`
}

// NewDonationAlerts создаёт источник.
func NewDonationAlerts(log *logx.Logger, tokens TokenStore, status func(bool, string)) *DonationAlerts {
	return &DonationAlerts{
		log:     log,
		tokens:  tokens,
		status:  status,
		apiBase: "https://www.donationalerts.com/api/v1",
		wsURL:   "wss://centrifugo.donationalerts.com/connection/websocket",
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Name — как сервис называется в панели.
func (d *DonationAlerts) Name() string { return "DonationAlerts" }

// Configured сообщает, выполнен ли вход.
func (d *DonationAlerts) Configured() bool {
	var t daToken
	return d.tokens.GetJSON(daKeyring, &t) == nil && t.AccessToken != ""
}

// AuthURL строит ссылку входа.
//
// response_type=token, потому что секрета у приложения нет и быть не должно:
// оно живёт на компьютере стримера, откуда секрет несложно достать.
func (d *DonationAlerts) AuthURL(clientID, redirectURI string) (string, error) {
	if strings.TrimSpace(clientID) == "" {
		return "", errs.New(errs.DonationsNoClientID,
			"Не заполнен Client ID DonationAlerts. Как его получить — написано в инструкции.")
	}
	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"token"},
		"scope":         {"oauth-user-show oauth-donation-subscribe"},
	}
	return "https://www.donationalerts.com/oauth/authorize?" + q.Encode(), nil
}

// SaveToken запоминает ключ доступа после входа.
func (d *DonationAlerts) SaveToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errs.New(errs.DonationsAuth, "DonationAlerts не выдал ключ доступа.")
	}
	d.log.Redactor.Add(token)
	return d.tokens.PutJSON(daKeyring, daToken{AccessToken: token})
}

// Logout забывает вход.
func (d *DonationAlerts) Logout() { d.tokens.Delete(daKeyring) }

// daUser — то, что нужно для подписки: идентификатор и токен подключения.
type daUser struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	SocketConn string `json:"socket_connection_token"`
}

// user спрашивает у DonationAlerts, кто вошёл.
func (d *DonationAlerts) user(ctx context.Context, token string) (*daUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.apiBase+"/user/oauth", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := d.http.Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.DonationsUnreachable, "DonationAlerts не отвечает.", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		d.Logout()
		return nil, errs.New(errs.DonationsAuth,
			"Слетела авторизация DonationAlerts. Подключи его заново.")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errs.Wrap(errs.DonationsUnreachable,
			"DonationAlerts ответил ошибкой. Попробуй ещё раз чуть позже.", fmt.Errorf("HTTP %d", resp.StatusCode))
	}

	var out struct {
		Data daUser `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, errs.Wrap(errs.DonationsBadResponse, "DonationAlerts ответил непонятным образом.", err)
	}
	d.log.Redactor.Add(out.Data.SocketConn)
	return &out.Data, nil
}

// channelToken берёт у DonationAlerts разрешение слушать канал.
//
// Токен подключения и токен канала — разные вещи, и это главная ловушка их
// API: с одним только первым подписка молча не работает.
func (d *DonationAlerts) channelToken(ctx context.Context, token, channel, clientID string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"channels": []string{channel},
		"client":   clientID,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.apiBase+"/centrifuge/subscribe", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.http.Do(req)
	if err != nil {
		return "", errs.Wrap(errs.DonationsUnreachable, "DonationAlerts не отвечает.", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", errs.Wrap(errs.DonationsUnreachable,
			"DonationAlerts не дал доступ к каналу. Попробуй ещё раз чуть позже.", fmt.Errorf("HTTP %d", resp.StatusCode))
	}

	var out struct {
		Channels []struct {
			Channel string `json:"channel"`
			Token   string `json:"token"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", errs.Wrap(errs.DonationsBadResponse, "DonationAlerts ответил непонятным образом.", err)
	}
	for _, c := range out.Channels {
		if c.Channel == channel {
			d.log.Redactor.Add(c.Token)
			return c.Token, nil
		}
	}
	return "", errs.New(errs.DonationsBadResponse, "DonationAlerts не выдал доступ к каналу донатов.")
}

// Run держит подключение до отмены контекста.
func (d *DonationAlerts) Run(ctx context.Context, onDonation func(Donation)) {
	attempt := 0

	for ctx.Err() == nil {
		startedAt := time.Now()
		err := d.session(ctx, onDonation)
		if ctx.Err() != nil {
			return
		}

		// Проработавшая сессия обнуляет счётчик: иначе к середине стрима
		// пауза перед переподключением упирается в потолок, и донаты,
		// пришедшие в эти полминуты, пропадают — переприсылки у сервисов
		// после разрыва нет.
		if time.Since(startedAt) > time.Minute {
			attempt = 0
		}

		attempt++
		wait := backoff(attempt)
		if err != nil {
			d.log.Warn("DonationAlerts отвалился", "ошибка", err, "повтор_через", wait.String())
			code, text := errs.Describe(err)
			d.status(false, string(code)+" · "+text)
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// session проживает одно подключение.
func (d *DonationAlerts) session(ctx context.Context, onDonation func(Donation)) error {
	var saved daToken
	if err := d.tokens.GetJSON(daKeyring, &saved); err != nil || saved.AccessToken == "" {
		return errs.New(errs.DonationsAuth, "DonationAlerts не подключён.")
	}

	user, err := d.user(ctx, saved.AccessToken)
	if err != nil {
		return err
	}

	conn, _, err := websocket.Dial(ctx, d.wsURL, nil)
	if err != nil {
		// Английская причина обрыва — в лог, стримеру понятная фраза.
		return errs.Wrap(errs.DonationsUnreachable, "Не подключился к DonationAlerts. Проверь интернет — приложение попробует ещё раз само.", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	// 1. Представляемся токеном подключения и получаем свой client id.
	clientID, err := d.connect(ctx, conn, user.SocketConn)
	if err != nil {
		return err
	}

	// 2. Берём отдельное разрешение на канал донатов.
	channel := fmt.Sprintf("$alerts:donation_%d", user.ID)
	chanToken, err := d.channelToken(ctx, saved.AccessToken, channel, clientID)
	if err != nil {
		return err
	}

	// 3. Подписываемся.
	if err := d.subscribe(ctx, conn, channel, chanToken); err != nil {
		return err
	}

	d.log.Info("DonationAlerts подключён", "аккаунт", user.Name, "канал", channel)
	d.status(true, user.Name)

	return d.pump(ctx, conn, onDonation)
}

// connect представляется и возвращает выданный нам идентификатор клиента.
func (d *DonationAlerts) connect(ctx context.Context, conn *websocket.Conn, token string) (string, error) {
	if err := d.send(ctx, conn, map[string]any{
		"params": map[string]any{"token": token},
		"id":     d.nextID(),
	}); err != nil {
		return "", err
	}

	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var reply struct {
		Result struct {
			Client string `json:"client"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := wsjson.Read(readCtx, conn, &reply); err != nil {
		return "", err
	}
	if reply.Error != nil {
		return "", errs.New(errs.DonationsAuth, "DonationAlerts отклонил подключение: "+reply.Error.Message)
	}
	if reply.Result.Client == "" {
		return "", errs.New(errs.DonationsBadResponse, "DonationAlerts не выдал идентификатор подключения.")
	}
	return reply.Result.Client, nil
}

// subscribe оформляет подписку на канал донатов.
func (d *DonationAlerts) subscribe(ctx context.Context, conn *websocket.Conn, channel, token string) error {
	return d.send(ctx, conn, map[string]any{
		"params": map[string]any{"channel": channel, "token": token},
		"method": 1,
		"id":     d.nextID(),
	})
}

// pump читает сообщения.
func (d *DonationAlerts) pump(ctx context.Context, conn *websocket.Conn, onDonation func(Donation)) error {
	for {
		// Centrifugo шлёт пустые кадры как пинг. Если молчание затянулось —
		// соединение мертво, и лучше переподключиться, чем ждать вечно.
		readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, raw, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}

		d.log.Debug("сообщение DonationAlerts", "тело", string(raw))

		var msg struct {
			Result struct {
				Channel string          `json:"channel"`
				Data    json.RawMessage `json:"data"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if len(msg.Result.Data) == 0 {
			// Пинг или ответ на команду — отвечаем пустотой, как ждёт Centrifugo.
			if len(raw) <= 2 {
				d.send(ctx, conn, map[string]any{})
			}
			continue
		}

		if don, ok := parseDonation(msg.Result.Data); ok {
			onDonation(don)
		}
	}
}

// daPayload — донат внутри сообщения Centrifugo. Он лежит на уровень глубже,
// чем кажется: data.data.
type daPayload struct {
	Data struct {
		ID       int     `json:"id"`
		Username string  `json:"username"`
		Message  string  `json:"message"`
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
		// AmountMain — сумма в валюте стримера. Именно её сравниваем с
		// порогом: иначе донат в другой валюте пройдёт мимо настройки.
		AmountMain float64 `json:"amount_main"`
		CreatedAt  string  `json:"created_at"`
	} `json:"data"`
}

func parseDonation(raw json.RawMessage) (Donation, bool) {
	var p daPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.Data.Username == "" {
		return Donation{}, false
	}

	// amount_main — сумма в валюте основного счёта стримера, и именно с ней
	// сравнивается порог. Подпись валюты зрителя к ней не подходит, а саму
	// валюту счёта DonationAlerts в донате не присылает — поэтому пусто, и
	// сервер подпишет «в валюте счёта». Валюту зрителя оставляем, только
	// если сумма и есть сумма зрителя.
	amount, currency := p.Data.AmountMain, ""
	if amount == 0 || amount == p.Data.Amount {
		amount, currency = p.Data.Amount, p.Data.Currency
	}

	// Без номера доната ID пустой: «0» у всех таких донатов совпал бы, и
	// фильтр повторов склеил бы разные донаты в один.
	id := ""
	if p.Data.ID != 0 {
		id = fmt.Sprintf("%d", p.Data.ID)
	}

	at := time.Now()
	if p.Data.CreatedAt != "" {
		if parsed, err := time.Parse("2006-01-02 15:04:05", p.Data.CreatedAt); err == nil {
			at = parsed
		}
	}

	return Donation{
		ID:       id,
		Source:   "donationalerts",
		Username: p.Data.Username,
		Message:  p.Data.Message,
		Amount:   amount,
		Currency: currency,
		At:       at,
	}, true
}

func (d *DonationAlerts) send(ctx context.Context, conn *websocket.Conn, v any) error {
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(writeCtx, conn, v)
}

func (d *DonationAlerts) nextID() int64 { return atomic.AddInt64(&d.msgID, 1) }
