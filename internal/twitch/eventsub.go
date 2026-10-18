package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"songrequest-yandex/internal/errs"
)

// eventSubURL — адрес WebSocket EventSub. Именно WebSocket, а не webhook:
// у приложения на домашнем компьютере нет публичного адреса, куда Twitch
// мог бы стучаться.
const eventSubURL = "wss://eventsub.wss.twitch.tv/ws"

// Redemption — заказ, который сделал зритель.
type Redemption struct {
	ID          string    `json:"id"`
	RewardID    string    `json:"reward_id"`
	RewardTitle string    `json:"reward_title"`
	RewardCost  int       `json:"reward_cost"`
	UserID      string    `json:"user_id"`
	UserLogin   string    `json:"user_login"`
	UserName    string    `json:"user_name"`
	UserInput   string    `json:"user_input"`
	RedeemedAt  time.Time `json:"redeemed_at"`
}

// EventSub держит подписку на события канала.
type EventSub struct {
	client *Client

	// OnRedemption вызывается на каждый заказ за баллы.
	OnRedemption func(Redemption)
	// OnStatus сообщает панели, жива ли подписка.
	OnStatus func(connected bool, detail string)
	// OnChat вызывается на каждое сообщение в чате канала.
	OnChat func(ChatMessage)

	wsURL string
}

// NewEventSub создаёт подписку.
func NewEventSub(c *Client) *EventSub {
	url := eventSubURL
	if base := os.Getenv("SONGREQUEST_TWITCH_WS"); base != "" {
		url = base
	}
	return &EventSub{client: c, wsURL: url}
}

// Run держит соединение живым до отмены контекста.
//
// Соединение рвётся по десятку причин — от перезагрузки роутера до плановой
// переброски на другой сервер Twitch. Стример об этом знать не должен:
// переподключаемся сами, с растущей паузой, и заново оформляем подписку.
func (e *EventSub) Run(ctx context.Context, rewardIDs []string) {
	attempt := 0

	for ctx.Err() == nil {
		startedAt := time.Now()
		err := e.session(ctx, rewardIDs)
		if ctx.Err() != nil {
			return
		}

		// Счётчик попыток обнуляем после сессии, которая реально работала.
		//
		// Раньше он рос всё время работы приложения: к середине стрима любая
		// моргнувшая сеть стоила уже максимальной паузы, хотя связь вернулась
		// через секунду. Заказы, присланные в эту паузу, теряются насовсем.
		if time.Since(startedAt) > time.Minute {
			attempt = 0
		}

		// Отозванную подписку переподключением не вернуть — нужен новый вход.
		// Раньше приложение этого не различало и ломилось обратно каждые
		// восемь секунд весь стрим: панель мигала красным, а каждый круг
		// дёргал обновление ключа доступа и добивал вход окончательно.
		if errors.Is(err, errRevoked) {
			e.client.log.Warn("подписка на заказы отозвана, переподключаться бессмысленно")
			return
		}

		attempt++
		wait := backoff(min(attempt, 5))
		if err != nil {
			e.client.log.Warn("подписка на события Twitch оборвалась",
				"ошибка", err, "повтор_через", wait.String())
			e.status(false, string(errs.TwitchEventSub)+" · переподключаюсь")
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// session проживает одно соединение от приветствия до разрыва.
func (e *EventSub) session(ctx context.Context, rewardIDs []string) error {
	conn, _, err := websocket.Dial(ctx, e.wsURL, nil)
	if err != nil {
		return fmt.Errorf("не подключился к событиям Twitch: %w", err)
	}
	// Сообщения бывают большими: у заказа есть текст зрителя.
	conn.SetReadLimit(1 << 20)

	subscribe := true
	keepalive := defaultKeepalive

	// Twitch может попросить переехать на другой адрес прямо посреди работы;
	// подписки при этом переносятся сами, оформлять их заново не нужно.
	for {
		next, err := e.pump(ctx, conn, subscribe, rewardIDs, keepalive)
		if err != nil || next == "" {
			conn.CloseNow()
			return err
		}

		e.client.log.Info("Twitch попросил переехать на другой адрес")

		fresh, _, err := websocket.Dial(ctx, next, nil)
		if err != nil {
			conn.CloseNow()
			return fmt.Errorf("не переехал на новый адрес событий Twitch: %w", err)
		}
		fresh.SetReadLimit(1 << 20)

		// Порядок здесь прописан у самого Twitch: старое соединение нельзя
		// закрывать, пока новое не поздоровается, — именно в этот промежуток
		// оно досылает последние события. Закрывали раньше — и заказ иногда
		// пропадал совсем: баллы списаны, а в приложении его нет.
		fresh, keepalive, err = e.welcome(ctx, fresh)
		if err != nil {
			conn.CloseNow()
			return err
		}

		e.drain(ctx, conn)
		conn.CloseNow()

		conn = fresh
		subscribe = false
	}
}

// welcome дожидается приветствия на новом соединении и узнаёт из него срок
// молчания. Возвращает то же соединение — чтобы вызов читался одной строкой.
func (e *EventSub) welcome(ctx context.Context, conn *websocket.Conn) (*websocket.Conn, time.Duration, error) {
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			conn.CloseNow()
			return nil, 0, fmt.Errorf("новый адрес событий Twitch молчит: %w", err)
		}

		var msg struct {
			Metadata struct {
				Type string `json:"message_type"`
			} `json:"metadata"`
			Payload struct {
				Session struct {
					Timeout int `json:"keepalive_timeout_seconds"`
				} `json:"session"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Metadata.Type != "session_welcome" {
			continue
		}

		keepalive := defaultKeepalive
		if msg.Payload.Session.Timeout > 0 {
			keepalive = time.Duration(msg.Payload.Session.Timeout) * time.Second
		}
		return conn, keepalive, nil
	}
}

// drain дочитывает то, что старое соединение успело досказать перед закрытием.
// Секунды хватает: Twitch к этому моменту уже перевёл поток на новый адрес.
func (e *EventSub) drain(ctx context.Context, conn *websocket.Conn) {
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			return
		}

		var msg struct {
			Metadata struct {
				Type             string `json:"message_type"`
				SubscriptionType string `json:"subscription_type"`
			} `json:"metadata"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Metadata.Type != "notification" {
			continue
		}

		e.client.log.Info("забрал событие со старого соединения перед переездом")
		if msg.Metadata.SubscriptionType == "channel.chat.message" {
			e.handleChat(msg.Payload)
			continue
		}
		e.handleNotification(msg.Payload)
	}
}

// defaultKeepalive — с каким сроком молчания живём, пока Twitch не назвал
// свой. С запасом: до приветствия настоящий срок неизвестен.
const defaultKeepalive = 30 * time.Second

// errRevoked — Twitch отозвал подписку. Отдельной ошибкой, потому что это
// единственный разрыв, после которого переподключаться бессмысленно.
var errRevoked = errs.New(errs.TwitchEventSub,
	"Twitch отозвал доступ к заказам. Нажми «Подключить Twitch» заново.")

// pump читает сообщения одного соединения. Возвращает адрес для переезда,
// если Twitch попросил переподключиться.
func (e *EventSub) pump(ctx context.Context, conn *websocket.Conn, subscribe bool,
	rewardIDs []string, keepalive time.Duration) (string, error) {

	for {
		readCtx, cancel := context.WithTimeout(ctx, keepalive+15*time.Second)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return "", err
		}

		var msg struct {
			Metadata struct {
				Type             string `json:"message_type"`
				SubscriptionType string `json:"subscription_type"`
			} `json:"metadata"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			e.client.log.Warn("непонятное сообщение от Twitch", "сообщение", string(data))
			continue
		}

		switch msg.Metadata.Type {
		case "session_welcome":
			var p struct {
				Session struct {
					ID      string `json:"id"`
					Timeout int    `json:"keepalive_timeout_seconds"`
				} `json:"session"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return "", err
			}
			if p.Session.Timeout > 0 {
				keepalive = time.Duration(p.Session.Timeout) * time.Second
			}

			chatOK := true
			if subscribe {
				// Подписку надо оформить быстро: Twitch рвёт неиспользуемое
				// соединение через десять секунд после приветствия.
				if err := e.subscribe(ctx, p.Session.ID, rewardIDs); err != nil {
					return "", err
				}
				if e.OnChat != nil {
					chatOK = e.subscribeChat(ctx, p.Session.ID) == nil
				}
			}

			// «Команды в чате не работают» — это то, что стример обязан
			// увидеть в панели, а не вычитать в логе. Самая частая причина —
			// вход в Twitch выдан до того, как приложение стало просить право
			// на чтение чата: заказы за баллы при этом идут как ни в чём не
			// бывало, а !скип молчит.
			points := len(rewardIDs) > 0
			switch {
			case !chatOK && !points:
				e.status(true, "команды в чате не работают — подключи Twitch заново")
			case !chatOK:
				e.status(true, "заказы принимаются, но команды в чате не работают")
			case !points:
				e.status(true, "чат подключён, баллов на канале нет")
			default:
				e.status(true, "заказы принимаются")
			}
			e.client.log.Info("подписка на события Twitch активна",
				"наград", len(rewardIDs), "чат", chatOK,
				"молчание_до", keepalive.String())

		case "session_keepalive":
			// Тишина в эфире — соединение живо, делать нечего.

		case "session_reconnect":
			var p struct {
				Session struct {
					ReconnectURL string `json:"reconnect_url"`
				} `json:"session"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				return "", err
			}
			return p.Session.ReconnectURL, nil

		case "revocation":
			// Twitch отозвал подписку: стример отключил приложение или
			// удалил награду. Переподключение не поможет, нужен вход заново.
			e.client.log.Warn("Twitch отозвал подписку на заказы", "ответ", string(msg.Payload))
			e.status(false, string(errs.TwitchEventSub)+" · подписка отозвана, подключись заново")
			return "", errRevoked

		case "notification":
			// Тип события разный, и разбирать их надо по-разному.
			if msg.Metadata.SubscriptionType == "channel.chat.message" {
				e.handleChat(msg.Payload)
				break
			}
			e.handleNotification(msg.Payload)

		default:
			e.client.log.Debug("неизвестный тип сообщения", "тип", msg.Metadata.Type)
		}
	}
}

// handleNotification разбирает событие заказа.
func (e *EventSub) handleNotification(payload json.RawMessage) {
	var p struct {
		Event struct {
			ID         string    `json:"id"`
			UserID     string    `json:"user_id"`
			UserLogin  string    `json:"user_login"`
			UserName   string    `json:"user_name"`
			UserInput  string    `json:"user_input"`
			Status     string    `json:"status"`
			RedeemedAt time.Time `json:"redeemed_at"`
			Reward     struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Cost  int    `json:"cost"`
			} `json:"reward"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		e.client.log.Warn("не разобрал событие заказа", "событие", string(payload))
		return
	}

	r := Redemption{
		ID:          p.Event.ID,
		RewardID:    p.Event.Reward.ID,
		RewardTitle: p.Event.Reward.Title,
		RewardCost:  p.Event.Reward.Cost,
		UserID:      p.Event.UserID,
		UserLogin:   p.Event.UserLogin,
		UserName:    p.Event.UserName,
		// Текст чистим сразу: зрители вставляют его из буфера обмена вместе с
		// переводами строк и лишними пробелами, а дальше по нему будет
		// искаться трек, и невидимый символ в начале сломает поиск.
		UserInput:  cleanInput(p.Event.UserInput),
		RedeemedAt: p.Event.RedeemedAt,
	}

	e.client.log.Info("заказ за баллы",
		"зритель", r.UserLogin, "текст", r.UserInput, "id", r.ID)

	if e.OnRedemption != nil {
		// В отдельной горутине: пока обработчик работает, цикл чтения сокета
		// стоит, а Twitch ждать не будет. Подбор трека — это запросы к
		// Spotify на секунды, команда «!очистить» при десяти заказах — это
		// десять обращений к Twitch подряд. Пауза дольше keepalive рвёт
		// соединение, панель краснеет, а заказы, присланные в эту минуту,
		// теряются совсем.
		go e.OnRedemption(r)
	}
}

// cleanInput убирает из текста заказа переводы строк, табуляции и повторные
// пробелы. Twitch отдаёт то, что ввёл зритель, слово в слово.
func cleanInput(s string) string {
	// Переводы строк, табуляции и прочие управляющие символы превращаем в
	// пробелы, а затем схлопываем пробелы. strings.Fields делает это сам.
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// subscribe оформляет подписку на заказы за баллы.
//
// Наград может быть две — за трек и за плейлист, — и подписка у Twitch своя на
// каждую: в условии подписки лежит номер одной награды. Без фильтра по награде
// посыпались бы события от всех наград канала, включая чужие, за которые мы
// даже баллы вернуть не можем.
func (e *EventSub) subscribe(ctx context.Context, sessionID string, rewardIDs []string) error {
	user := e.client.Account()
	if user == nil {
		return errs.New(errs.TwitchAuthExpired, "Сначала подключи Twitch.")
	}

	// Наград нет вовсе — значит нет и баллов на канале.
	//
	// Подписываться на заказы за баллы там бессмысленно — Twitch откажет, а
	// отказ уронит всё соединение. Но само соединение нужно: только через
	// него работает чат, то есть команды `!очередь`, `!скип` и любые ответы
	// зрителям. Раньше на таком канале молчала вся вторая половина
	// приложения, и выглядело это как «половина мертва».
	if len(rewardIDs) == 0 {
		e.client.log.Info("баллов на канале нет — подписываюсь только на чат")
		return nil
	}

	for _, rewardID := range rewardIDs {
		if rewardID == "" {
			continue
		}
		body := map[string]any{
			"type":    "channel.channel_points_custom_reward_redemption.add",
			"version": "1",
			"condition": map[string]any{
				"broadcaster_user_id": user.ID,
				"reward_id":           rewardID,
			},
			"transport": map[string]any{
				"method":     "websocket",
				"session_id": sessionID,
			},
		}
		if err := e.client.do(ctx, http.MethodPost, "/eventsub/subscriptions", body, nil); err != nil {
			return errs.Wrap(errs.TwitchEventSub, "Не получилось подписаться на заказы.", err)
		}
	}
	return nil
}

func (e *EventSub) status(connected bool, detail string) {
	if e.OnStatus != nil {
		e.OnStatus(connected, detail)
	}
}
