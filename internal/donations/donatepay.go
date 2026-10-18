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

// DonatePay проще DonationAlerts: OAuth нет вообще, стример копирует ключ
// из своего профиля и вставляет в настройки. Для нетехнического человека это
// даже легче, чем вход через браузер.
type DonatePay struct {
	log    *logx.Logger
	key    func() string // ключ живёт в настройках, а не в хранилище
	status func(bool, string)

	apiBase string
	wsURL   string
	http    *http.Client
	msgID   int64
}

// NewDonatePay создаёт источник.
func NewDonatePay(log *logx.Logger, key func() string, status func(bool, string)) *DonatePay {
	return &DonatePay{
		log:     log,
		key:     key,
		status:  status,
		apiBase: "https://donatepay.ru/api/v1",
		wsURL:   "wss://centrifugo.donatepay.ru:43002/connection/websocket",
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Name — как сервис называется в панели.
func (d *DonatePay) Name() string { return "DonatePay" }

// Configured сообщает, вставлен ли ключ.
func (d *DonatePay) Configured() bool { return strings.TrimSpace(d.key()) != "" }

// Run держит подключение до отмены контекста.
func (d *DonatePay) Run(ctx context.Context, onDonation func(Donation)) {
	attempt := 0

	for ctx.Err() == nil {
		startedAt := time.Now()
		err := d.session(ctx, onDonation)
		if ctx.Err() != nil {
			return
		}

		// Проработавшая сессия обнуляет счётчик: иначе к середине стрима
		// пауза перед переподключением упирается в потолок, и донаты,
		// пришедшие в эти полминуты, пропадают.
		if time.Since(startedAt) > time.Minute {
			attempt = 0
		}

		attempt++
		wait := backoff(attempt)
		if err != nil {
			d.log.Warn("DonatePay отвалился", "ошибка", err, "повтор_через", wait.String())
			code, text := errs.Describe(err)
			d.status(false, string(code)+" · "+text)
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

func (d *DonatePay) session(ctx context.Context, onDonation func(Donation)) error {
	key := strings.TrimSpace(d.key())
	if key == "" {
		return errs.New(errs.DonationsAuth, "Ключ DonatePay не заполнен.")
	}
	d.log.Redactor.Add(key)

	userID, token, err := d.subscribeToken(ctx, key)
	if err != nil {
		return err
	}

	conn, _, err := websocket.Dial(ctx, d.wsURL, nil)
	if err != nil {
		return fmt.Errorf("не подключился к DonatePay: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	if err := d.send(ctx, conn, map[string]any{
		"params": map[string]any{"token": token, "user": userID},
		"id":     d.nextID(),
	}); err != nil {
		return err
	}

	channel := "$public:" + userID
	if err := d.send(ctx, conn, map[string]any{
		"params": map[string]any{"channel": channel},
		"method": 1,
		"id":     d.nextID(),
	}); err != nil {
		return err
	}

	d.log.Info("DonatePay подключён", "канал", channel)
	d.status(true, "подключено")

	return d.pump(ctx, conn, onDonation)
}

// subscribeToken берёт у DonatePay токен подключения и идентификатор канала.
func (d *DonatePay) subscribeToken(ctx context.Context, key string) (userID, token string, err error) {
	form := url.Values{
		"access_token": {key},
		"channels[]":   {"$public"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.apiBase+"/subscribe", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.http.Do(req)
	if err != nil {
		return "", "", errs.Wrap(errs.DonationsUnreachable, "DonatePay не отвечает.", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", "", errs.New(errs.DonationsAuth,
			"DonatePay не принял ключ. Проверь, что скопировал его целиком.")
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", errs.New(errs.DonationsUnreachable,
			fmt.Sprintf("DonatePay ответил ошибкой (%d).", resp.StatusCode))
	}

	var out struct {
		Token    string   `json:"token"`
		Channels []string `json:"channels"`
		User     string   `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", errs.Wrap(errs.DonationsBadResponse, "DonatePay ответил непонятным образом.", err)
	}
	if out.Token == "" {
		return "", "", errs.New(errs.DonationsBadResponse, "DonatePay не выдал токен подключения.")
	}
	d.log.Redactor.Add(out.Token)

	userID = out.User
	if userID == "" && len(out.Channels) > 0 {
		// Идентификатор бывает только внутри имени канала: «$public:12345».
		if _, after, ok := strings.Cut(out.Channels[0], ":"); ok {
			userID = after
		}
	}
	if userID == "" {
		return "", "", errs.New(errs.DonationsBadResponse, "DonatePay не сказал, чей это канал.")
	}
	return userID, out.Token, nil
}

func (d *DonatePay) pump(ctx context.Context, conn *websocket.Conn, onDonation func(Donation)) error {
	for {
		readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, raw, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}

		d.log.Debug("сообщение DonatePay", "тело", string(raw))

		var msg struct {
			Result struct {
				Data json.RawMessage `json:"data"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil || len(msg.Result.Data) == 0 {
			if len(raw) <= 2 {
				d.send(ctx, conn, map[string]any{})
			}
			continue
		}

		if don, ok := parseDonatePay(msg.Result.Data); ok {
			onDonation(don)
		}
	}
}

// parseDonatePay разбирает донат. Структура у них другая, чем у
// DonationAlerts, поэтому отдельный разбор, а не общий.
func parseDonatePay(raw json.RawMessage) (Donation, bool) {
	var p struct {
		Notification struct {
			Type string `json:"type"`
			Vars struct {
				Name    string `json:"name"`
				Comment string `json:"comment"`
				Sum     any    `json:"sum"`
			} `json:"vars"`
			ID any `json:"id"`
		} `json:"notification"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Donation{}, false
	}
	if p.Notification.Type != "donation" || p.Notification.Vars.Name == "" {
		return Donation{}, false
	}

	return Donation{
		ID:       donationID(p.Notification.ID),
		Source:   "donatepay",
		Username: p.Notification.Vars.Name,
		Message:  p.Notification.Vars.Comment,
		Amount:   toFloat(p.Notification.Vars.Sum),
		Currency: "RUB",
		At:       time.Now(),
	}, true
}

// toFloat: DonatePay присылает сумму то числом, то строкой.
func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscanf(x, "%f", &f)
		return f
	}
	return 0
}

func (d *DonatePay) send(ctx context.Context, conn *websocket.Conn, v any) error {
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(writeCtx, conn, v)
}

func (d *DonatePay) nextID() int64 { return atomic.AddInt64(&d.msgID, 1) }
