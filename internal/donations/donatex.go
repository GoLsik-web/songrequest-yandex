package donations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
)

// DonateX подключается по бессрочному ключу из личного кабинета: ни
// регистрации приложения, ни OAuth — стример копирует строку из настроек.
// Для нетехнического человека это самый простой из трёх сервисов.
//
// События приходят через SignalR — это протокол ASP.NET поверх WebSocket.
// Отдельная библиотека для него не нужна: там рукопожатие одной строкой и
// кадры, разделённые символом с кодом 0x1E.
type DonateX struct {
	log    *logx.Logger
	key    func() string
	status func(bool, string)

	base string
	http *http.Client
}

// NewDonateX создаёт источник.
func NewDonateX(log *logx.Logger, key func() string, status func(bool, string)) *DonateX {
	return &DonateX{
		log:    log,
		key:    key,
		status: status,
		base:   "https://donatex.gg/api",
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Name — как сервис называется в панели.
func (d *DonateX) Name() string { return "DonateX" }

// Configured сообщает, вставлен ли ключ.
func (d *DonateX) Configured() bool { return strings.TrimSpace(d.key()) != "" }

// separator разделяет сообщения SignalR. Одним кадром может приехать
// несколько сообщений подряд, поэтому читать надо по этому символу, а не
// по кадру.
const separator = 0x1E

// Run держит подключение до отмены контекста.
func (d *DonateX) Run(ctx context.Context, onDonation func(Donation)) {
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
		// DonateX ограничивает подключения: два в секунду с одного адреса.
		// Наша растущая пауза в этот лимит укладывается с запасом.
		if wait < time.Second {
			wait = time.Second
		}
		if err != nil {
			d.log.Warn("DonateX отвалился", "ошибка", err, "повтор_через", wait.String())
			code, text := errs.Describe(err)
			d.status(false, string(code)+" · "+text)
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

func (d *DonateX) session(ctx context.Context, onDonation func(Donation)) error {
	key := strings.TrimSpace(d.key())
	if key == "" {
		return errs.New(errs.DonationsAuth, "Ключ DonateX не заполнен.")
	}
	d.log.Redactor.Add(key)

	name, err := d.whoami(ctx, key)
	if err != nil {
		return err
	}

	// Сначала negotiate: сервер выдаёт разовый идентификатор подключения.
	// Без него можно обойтись только там, где это явно разрешено, а гадать
	// про чужой сервер не стоит.
	connToken, err := d.negotiate(ctx, key)
	if err != nil {
		return err
	}

	wsURL := strings.Replace(d.base, "https://", "wss://", 1) + "/public-donations-hub" +
		"?id=" + url.QueryEscape(connToken) + "&access_token=" + url.QueryEscape(key)

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return errs.Wrap(errs.DonationsUnreachable, "Не получилось подключиться к DonateX.", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	if err := d.handshake(ctx, conn); err != nil {
		return err
	}

	d.log.Info("DonateX подключён", "аккаунт", name)
	d.status(true, name)

	return d.pump(ctx, conn, onDonation)
}

// whoami проверяет ключ и заодно узнаёт, чей он.
func (d *DonateX) whoami(ctx context.Context, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/v1/user/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := d.http.Do(req)
	if err != nil {
		return "", errs.Wrap(errs.DonationsUnreachable, "DonateX не отвечает.", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", errs.New(errs.DonationsAuth,
			"DonateX не принял ключ. Возьми его заново в личном кабинете: Настройки → Api.")
	case resp.StatusCode != http.StatusOK:
		return "", errs.New(errs.DonationsUnreachable,
			fmt.Sprintf("DonateX ответил ошибкой (%d).", resp.StatusCode))
	}

	var out struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", errs.Wrap(errs.DonationsBadResponse, "DonateX ответил непонятным образом.", err)
	}
	if name := firstFilled(out.Username, out.Name); name != "" {
		return name, nil
	}
	return "подключено", nil
}

// negotiate берёт у SignalR разовый идентификатор подключения.
func (d *DonateX) negotiate(ctx context.Context, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.base+"/public-donations-hub/negotiate?negotiateVersion=1", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Length", "0")

	resp, err := d.http.Do(req)
	if err != nil {
		return "", errs.Wrap(errs.DonationsUnreachable, "DonateX не отвечает.", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", errs.New(errs.DonationsUnreachable,
			fmt.Sprintf("DonateX не дал подключиться к событиям (%d).", resp.StatusCode))
	}

	var out struct {
		ConnectionToken string `json:"connectionToken"`
		ConnectionID    string `json:"connectionId"`
		Error           string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", errs.Wrap(errs.DonationsBadResponse, "DonateX ответил непонятным образом.", err)
	}
	if out.Error != "" {
		return "", errs.New(errs.DonationsAuth, "DonateX отказал: "+out.Error)
	}

	token := firstFilled(out.ConnectionToken, out.ConnectionID)
	if token == "" {
		return "", errs.New(errs.DonationsBadResponse, "DonateX не выдал идентификатор подключения.")
	}
	d.log.Redactor.Add(token)
	return token, nil
}

// handshake договаривается о формате сообщений.
func (d *DonateX) handshake(ctx context.Context, conn *websocket.Conn) error {
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	hello := append([]byte(`{"protocol":"json","version":1}`), separator)
	if err := conn.Write(writeCtx, websocket.MessageText, hello); err != nil {
		return errs.Wrap(errs.DonationsUnreachable, "DonateX оборвал подключение.", err)
	}

	readCtx, cancelRead := context.WithTimeout(ctx, 20*time.Second)
	defer cancelRead()

	_, raw, err := conn.Read(readCtx)
	if err != nil {
		return errs.Wrap(errs.DonationsUnreachable, "DonateX не ответил на подключение.", err)
	}

	// Пустой объект означает согласие; иначе там причина отказа.
	for _, part := range split(raw) {
		var reply struct {
			Error string `json:"error"`
		}
		json.Unmarshal(part, &reply)
		if reply.Error != "" {
			return errs.New(errs.DonationsAuth, "DonateX отказал: "+reply.Error)
		}
	}
	return nil
}

// pump читает события.
func (d *DonateX) pump(ctx context.Context, conn *websocket.Conn, onDonation func(Donation)) error {
	for {
		// Хаб шлёт пинг сам; долгое молчание означает мёртвое соединение.
		readCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		_, raw, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}

		for _, part := range split(raw) {
			d.handleMessage(ctx, conn, part, onDonation)
		}
	}
}

func (d *DonateX) handleMessage(ctx context.Context, conn *websocket.Conn, raw []byte, onDonation func(Donation)) {
	var msg struct {
		Type      int               `json:"type"`
		Target    string            `json:"target"`
		Arguments []json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	switch msg.Type {
	case 6: // пинг — отвечаем тем же, иначе нас отключат
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn.Write(writeCtx, websocket.MessageText, append([]byte(`{"type":6}`), separator))
		cancel()
		return
	case 7: // сервер закрывает соединение
		d.log.Info("DonateX закрыл соединение", "сообщение", string(raw))
		return
	case 1: // событие
	default:
		return
	}

	// DonationRerun — это повторный показ уже отыгравшего доната стримером.
	// Заказ по нему создавать нельзя: трек уже играл, и второй раз его
	// никто не просил.
	if msg.Target != "DonationCreated" {
		d.log.Debug("событие DonateX пропущено", "тип", msg.Target)
		return
	}
	if len(msg.Arguments) == 0 {
		return
	}

	if don, ok := parseDonateX(msg.Arguments[0]); ok {
		onDonation(don)
	}
}

// parseDonateX разбирает донат.
func parseDonateX(raw json.RawMessage) (Donation, bool) {
	var p struct {
		ID       any     `json:"id"`
		Username string  `json:"username"`
		Message  string  `json:"message"`
		Currency string  `json:"currency"`
		Amount   float64 `json:"amount"`
		// AmountInRub — сумма в рублях. С порогом сравниваем именно её,
		// иначе донат в другой валюте пройдёт мимо настройки.
		AmountInRub float64 `json:"amountInRub"`
		Timestamp   string  `json:"timestamp"`
		IsTest      bool    `json:"isTest"`
		// MusicLink — у DonateX есть своя форма заказа музыки. Если зритель
		// заполнил её, ссылка точнее любого разбора сообщения.
		MusicLink string `json:"musicLink"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Username == "" {
		return Donation{}, false
	}
	if p.IsTest {
		// Тестовый донат — это проверка виджетов перед стримом, а не заказ.
		// Раньше поле разбиралось и не использовалось: стример жал в панели
		// DonateX «отправить тестовый донат» и получал настоящий трек в
		// очереди, перебивающий музыку в эфире.
		return Donation{}, false
	}

	amount := p.AmountInRub
	if amount == 0 {
		amount = p.Amount
	}

	message := strings.TrimSpace(p.Message)
	if link := strings.TrimSpace(p.MusicLink); link != "" {
		message = link
	}

	at := time.Now()
	if p.Timestamp != "" {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, p.Timestamp); err == nil {
				at = parsed
				break
			}
		}
	}

	currency := p.Currency
	if currency == "" {
		currency = "RUB"
	}

	return Donation{
		ID:       donationID(p.ID),
		Source:   "donatex",
		Username: p.Username,
		Message:  message,
		Amount:   amount,
		Currency: currency,
		At:       at,
	}, true
}

// split режет кадр на отдельные сообщения: их может приехать несколько разом.
func split(raw []byte) [][]byte {
	var out [][]byte
	for _, part := range bytes.Split(raw, []byte{separator}) {
		if len(bytes.TrimSpace(part)) > 0 {
			out = append(out, part)
		}
	}
	return out
}

func firstFilled(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
