package yandex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"songrequest-yandex/internal/errs"
)

// Ynison — постоянное соединение с общей очередью приложений Яндекса.
//
// Так приложение Яндекс Музыки на компьютере и, скажем, телефон знают, что
// играет у соседа, и могут управлять друг другом. Мы подключаемся к нему
// как ещё один «пульт», который сам ничего не играет: видим очередь и
// играющий трек, а заказ вставляем в очередь сразу за текущим треком.
//
// Протокол не документирован, устроен так (по работе веб-плеера Яндекса):
//
//  1. Короткое соединение с redirector: он отвечает, на какой сервер идти,
//     и выдаёт билет.
//  2. Постоянное соединение с этим сервером. Первым сообщением говорим, кто
//     мы такие; дальше сервер присылает состояние целиком на каждое
//     изменение, а мы присылаем своё, когда хотим что-то поменять.
//
// Состояние держим в памяти: панель и плеер читают его без сети.
type Ynison struct {
	c        *Client
	deviceID string

	mu        sync.Mutex
	state     *ynState
	devices   []ynDevice
	active    string
	connected bool
	lastErr   error
	updatedAt time.Time
	conn      *websocket.Conn
	cancel    context.CancelFunc
	running   bool
	// waiters — кто ждёт следующего состояния от сервера.
	waiters []chan struct{}

	// OnChange — состояние обновилось. Панели этого достаточно, чтобы
	// перерисовать «сейчас играет».
	OnChange func()

	writeMu sync.Mutex
}

func newYnison(c *Client) *Ynison {
	return &Ynison{c: c, deviceID: randomID()}
}

// stableDeviceID — номер устройства, постоянный для одного токена. Со
// случайным каждый запуск приложения добавлял бы в список устройств Яндекса
// ещё одно «Заказ музыки».
func stableDeviceID(token string) string {
	sum := sha256.Sum256([]byte("yandex-music-ynison:" + token))
	return hex.EncodeToString(sum[:])[:12]
}

// ynState — состояние плеера, как его присылает Ynison.
type ynState struct {
	Status ynStatus `json:"status"`
	Queue  ynQueue  `json:"player_queue"`
}

type ynStatus struct {
	DurationMs    flexInt    `json:"duration_ms"`
	Paused        bool       `json:"paused"`
	PlaybackSpeed float64    `json:"playback_speed"`
	ProgressMs    flexInt    `json:"progress_ms"`
	Version       *ynVersion `json:"version,omitempty"`
}

type ynQueue struct {
	CurrentPlayableIndex flexInt         `json:"current_playable_index"`
	EntityID             string          `json:"entity_id"`
	EntityType           string          `json:"entity_type"`
	EntityContext        string          `json:"entity_context,omitempty"`
	FromOptional         string          `json:"from_optional,omitempty"`
	PlayableList         []ynPlayable    `json:"playable_list"`
	Options              ynOptions       `json:"options"`
	Version              *ynVersion      `json:"version,omitempty"`
	Shuffle              json.RawMessage `json:"shuffle_optional,omitempty"`
	Queue                json.RawMessage `json:"queue,omitempty"`
}

type ynOptions struct {
	RepeatMode string `json:"repeat_mode"`
}

type ynPlayable struct {
	PlayableID       string `json:"playable_id"`
	AlbumIDOptional  string `json:"album_id_optional,omitempty"`
	PlayableType     string `json:"playable_type"`
	From             string `json:"from"`
	Title            string `json:"title,omitempty"`
	CoverURLOptional string `json:"cover_url_optional,omitempty"`
}

type ynVersion struct {
	DeviceID    string  `json:"device_id"`
	Version     flexInt `json:"version"`
	TimestampMs flexInt `json:"timestamp_ms"`
}

type ynDevice struct {
	Info struct {
		DeviceID string `json:"device_id"`
		Title    string `json:"title"`
		Type     string `json:"type"`
		AppName  string `json:"app_name"`
	} `json:"info"`
	VolumeInfo struct {
		Volume float64 `json:"volume"`
	} `json:"volume_info"`
	IsOffline bool `json:"is_offline"`
}

// ynMessage — что присылает сервер.
type ynMessage struct {
	PlayerState *ynState   `json:"player_state"`
	Devices     []ynDevice `json:"devices"`
	ActiveID    string     `json:"active_device_id_optional"`
	TimestampMs flexInt    `json:"timestamp_ms"`
	Error       *struct {
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

// flexInt — число, которое Ynison присылает то числом, то строкой
// (int64 в protobuf-JSON — строка). Отправляем числом.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// Встречаются и дробные: прогресс бывает «1234.5».
		fv, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return err
		}
		v = int64(fv)
	}
	*f = flexInt(v)
	return nil
}

// Start поднимает соединение и держит его до отмены ctx.
func (y *Ynison) Start(ctx context.Context) {
	y.mu.Lock()
	if y.running {
		y.mu.Unlock()
		return
	}
	y.running = true
	ctx, cancel := context.WithCancel(ctx)
	y.cancel = cancel
	y.mu.Unlock()

	go y.loop(ctx)
}

// Stop рвёт соединение.
func (y *Ynison) Stop() {
	y.mu.Lock()
	cancel := y.cancel
	y.cancel = nil
	y.running = false
	y.connected = false
	y.state = nil
	y.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	y.changed()
}

// Restart — после нового входа: старый токен больше не годится.
func (y *Ynison) Restart() {
	y.mu.Lock()
	conn := y.conn
	y.mu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "новый вход")
	}
}

func (y *Ynison) loop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if !y.c.Connected() {
			if !sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}
		started := time.Now()
		err := y.session(ctx)
		if ctx.Err() != nil {
			return
		}
		y.mu.Lock()
		y.connected = false
		y.lastErr = err
		y.conn = nil
		y.mu.Unlock()
		y.changed()
		y.c.log.Warn("связь с Ynison оборвалась", "ошибка", err)

		// Долго проработавшее соединение — повод начать сначала, а не ждать.
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// session — одно соединение от redirector до обрыва.
func (y *Ynison) session(ctx context.Context) error {
	if token := y.c.accessToken(); token != "" {
		y.mu.Lock()
		y.deviceID = stableDeviceID(token)
		y.mu.Unlock()
	}
	host, ticket, session, err := y.redirect(ctx)
	if err != nil {
		return err
	}

	proto := y.protocol(map[string]string{
		"Ynison-Redirect-Ticket": ticket,
		"Ynison-Session-Id":      session,
	})
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	scheme := "wss://"
	if strings.HasPrefix(y.c.ynisonBase, "ws://") {
		scheme = "ws://" // проверки ходят на свой сервер без шифрования
	}
	conn, _, err := websocket.Dial(dialCtx, scheme+host+"/ynison_state.YnisonStateService/PutYnisonState", &websocket.DialOptions{
		HTTPHeader:   y.headers(),
		Subprotocols: proto,
	})
	if err != nil {
		return errs.Wrap(errs.YandexYnison, "Ynison не пустил к очереди.", err)
	}
	conn.SetReadLimit(16 << 20)
	defer conn.CloseNow()

	y.mu.Lock()
	y.conn = conn
	y.mu.Unlock()

	if err := y.write(ctx, conn, y.hello()); err != nil {
		return err
	}

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return errs.Wrap(errs.YandexYnison, "Ynison закрыл соединение.", err)
		}
		var msg ynMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			y.c.log.Warn("Ynison прислал непонятное", "ошибка", err)
			continue
		}
		if msg.Error != nil {
			return errs.New(errs.YandexYnison, "Ynison отказал: "+msg.Error.Message)
		}
		y.apply(msg)
	}
}

// apply запоминает присланное состояние.
func (y *Ynison) apply(msg ynMessage) {
	y.mu.Lock()
	if msg.PlayerState != nil {
		y.state = msg.PlayerState
	}
	if msg.Devices != nil {
		y.devices = msg.Devices
	}
	y.active = msg.ActiveID
	y.connected = true
	y.lastErr = nil
	y.updatedAt = time.Now()
	waiters := y.waiters
	y.waiters = nil
	y.mu.Unlock()
	for _, w := range waiters {
		close(w)
	}
	y.changed()
}

// redirect спрашивает, к какому серверу идти.
func (y *Ynison) redirect(ctx context.Context) (host, ticket, session string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, y.c.ynisonBase+"/redirector.YnisonRedirectService/GetRedirectToYnison", &websocket.DialOptions{
		HTTPHeader:   y.headers(),
		Subprotocols: y.protocol(nil),
	})
	if err != nil {
		return "", "", "", errs.Wrap(errs.YandexYnison, "Ynison не отвечает.", err)
	}
	defer conn.CloseNow()

	_, data, err := conn.Read(ctx)
	if err != nil {
		return "", "", "", errs.Wrap(errs.YandexYnison, "Ynison не сказал, куда подключаться.", err)
	}
	var resp struct {
		Host           string  `json:"host"`
		RedirectTicket string  `json:"redirect_ticket"`
		SessionID      flexInt `json:"session_id"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || resp.Host == "" {
		return "", "", "", errs.New(errs.YandexYnison, "Ynison ответил непонятно на первом шаге.")
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	return resp.Host, resp.RedirectTicket, strconv.FormatInt(int64(resp.SessionID), 10), nil
}

// headers — заголовки, без которых Ynison не разговаривает.
func (y *Ynison) headers() http.Header {
	h := http.Header{}
	h.Set("Origin", "https://music.yandex.ru")
	h.Set("Authorization", "OAuth "+y.c.accessToken())
	return h
}

// protocol — Ynison принимает свои параметры через Sec-WebSocket-Protocol:
// «Bearer, v2, {json}».
func (y *Ynison) protocol(extra map[string]string) []string {
	// type строкой, а весь набор — в URL-кодировке: так делает веб-плеер
	// Яндекса, и сырой JSON с кавычками сервер не принимает.
	info, _ := json.Marshal(map[string]any{"app_name": "Заказ музыки", "type": "1"})
	params := map[string]string{
		"Ynison-Device-Id":   y.deviceID,
		"Ynison-Device-Info": string(info),
	}
	for k, v := range extra {
		params[k] = v
	}
	raw, _ := json.Marshal(params)
	return []string{"Bearer", "v2", url.QueryEscape(string(raw))}
}

// hello — первое сообщение: «я пульт, сам ничего не играю».
//
// can_be_player=false важно: без него Ynison считает нас плеером и может
// перевести воспроизведение к нам — музыка у стримера замолчит.
func (y *Ynison) hello() map[string]any {
	v := y.version()
	return map[string]any{
		"update_full_state": map[string]any{
			"player_state": map[string]any{
				"player_queue": ynQueue{
					CurrentPlayableIndex: -1,
					EntityType:           "VARIOUS",
					EntityContext:        "BASED_ON_ENTITY_BY_DEFAULT",
					PlayableList:         []ynPlayable{},
					Options:              ynOptions{RepeatMode: "NONE"},
					Version:              &v,
				},
				"status": ynStatus{Paused: true, PlaybackSpeed: 1, Version: &v},
			},
			"device": map[string]any{
				"capabilities": map[string]any{
					"can_be_player":            false,
					"can_be_remote_controller": true,
					"volume_granularity":       0,
				},
				"info": map[string]any{
					"device_id": y.deviceID,
					"type":      "WEB",
					"title":     "Заказ музыки",
					"app_name":  "yandex-music",
				},
				"volume_info": map[string]any{"volume": 0},
			},
			"is_currently_active": false,
		},
		"rid":                        randomUUID(),
		"player_action_timestamp_ms": 0,
		"activity_interception_type": "DO_NOT_INTERCEPT_BY_DEFAULT",
	}
}

// version — отметка об изменении от нашего имени.
func (y *Ynison) version() ynVersion {
	return ynVersion{
		DeviceID:    y.deviceID,
		Version:     flexInt(randomInt63()),
		TimestampMs: flexInt(time.Now().UnixMilli()),
	}
}

func (y *Ynison) write(ctx context.Context, conn *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	y.writeMu.Lock()
	defer y.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		return errs.Wrap(errs.YandexYnison, "Не отправил команду в Ynison.", err)
	}
	return nil
}

// send шлёт сообщение по живому соединению.
func (y *Ynison) send(ctx context.Context, body map[string]any) error {
	y.mu.Lock()
	conn := y.conn
	ok := y.connected
	y.mu.Unlock()
	if conn == nil || !ok {
		return errs.New(errs.YandexYnison, "Нет связи с Ynison — очередь Яндекса сейчас недоступна.")
	}
	body["rid"] = randomUUID()
	body["player_action_timestamp_ms"] = time.Now().UnixMilli()
	body["activity_interception_type"] = "DO_NOT_INTERCEPT_BY_DEFAULT"
	return y.write(ctx, conn, body)
}

// putState отправляет новое состояние плеера целиком.
func (y *Ynison) putState(ctx context.Context, st ynState) error {
	v := y.version()
	st.Queue.Version = &v
	st.Status.Version = &v
	if st.Status.PlaybackSpeed == 0 {
		st.Status.PlaybackSpeed = 1
	}
	if err := y.send(ctx, map[string]any{
		"update_player_state": map[string]any{"player_state": st},
	}); err != nil {
		return err
	}
	// Своё изменение тоже держим: сервер пришлёт подтверждение, но до него
	// панель показывала бы прошлое.
	y.mu.Lock()
	cp := st
	y.state = &cp
	y.mu.Unlock()
	y.changed()
	return nil
}

// putStatus меняет только паузу и положение.
func (y *Ynison) putStatus(ctx context.Context, status ynStatus) error {
	v := y.version()
	status.Version = &v
	if status.PlaybackSpeed == 0 {
		status.PlaybackSpeed = 1
	}
	if err := y.send(ctx, map[string]any{
		"update_playing_status": map[string]any{"playing_status": status},
	}); err != nil {
		return err
	}
	y.mu.Lock()
	if y.state != nil {
		y.state.Status = status
	}
	y.mu.Unlock()
	y.changed()
	return nil
}

// snapshot — копия состояния. ok=false: состояния ещё нет.
func (y *Ynison) snapshot() (ynState, bool) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.state == nil || !y.connected {
		return ynState{}, false
	}
	st := *y.state
	st.Queue.PlayableList = append([]ynPlayable(nil), y.state.Queue.PlayableList...)
	return st, true
}

// Status — для панели: на связи ли, когда было последнее состояние и чем
// кончилась последняя попытка.
func (y *Ynison) Status() (connected bool, device string, err error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	for _, d := range y.devices {
		if d.Info.DeviceID == y.active {
			device = d.Info.Title
		}
	}
	return y.connected, device, y.lastErr
}

// HasPlayer — есть ли устройство, на котором играет музыка (кроме нас).
func (y *Ynison) HasPlayer() bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.active != "" && y.active != y.deviceID
}

// waitUpdate ждёт следующего состояния от сервера не дольше d.
func (y *Ynison) waitUpdate(ctx context.Context, d time.Duration) bool {
	ch := make(chan struct{})
	y.mu.Lock()
	y.waiters = append(y.waiters, ch)
	y.mu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (y *Ynison) changed() {
	if y.OnChange != nil {
		y.OnChange()
	}
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

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func randomInt63() int64 {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	b[0] &= 0x7f
	var v int64
	for _, x := range b {
		v = v<<8 | int64(x)
	}
	return v
}
