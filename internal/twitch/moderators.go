package twitch

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"songrequest-yandex/internal/errs"
)

// Кто на канале модератор.
//
// ПОЧЕМУ ЭТОГО НЕ ХВАТАЛО. Права проверялись только по значкам, которые Twitch
// присылает вместе с сообщением: есть значок «broadcaster» или «moderator» —
// команда принимается, нет — молча отбрасывается. Способ дешёвый и на вид
// надёжный, ради него в приложении даже не делалось лишних запросов.
//
// А 09.09 владелец написал «!скип» с аккаунта, который на канале модератор, и
// получил в лог «команда от того, кому нельзя, стример=false модератор=false».
// Значков в сообщении не было. Причин, по которым так бывает, несколько, и
// разбирать их по одной бесполезно: значки в чате — украшение, а не документ.
// Список модераторов знает сам Twitch, и спросить надо его.
//
// КАК УСТРОЕНО. Значки остаются быстрым путём: пришёл значок — верим сразу,
// без единого запроса. Не пришёл — спрашиваем Twitch и запоминаем ответ.
// Список модераторов на канале меняется раз в месяц, поэтому память живёт
// долго (modCacheFor), а перечитывается она только тогда, когда кто-то без
// значков пытается командовать, — то есть почти никогда.
//
// ЧТО БЫВАЕТ ПРИ ОТКАЗЕ. Право `moderation:read` приложение просит с самого
// начала, но вход, выданный до его появления, живёт без него. Тогда список не
// придёт, и остаются одни значки — то есть ровно то поведение, что было
// раньше. Молча хуже не станет, а в панели про недостающие права и так
// написано (TW-14).

// modCacheFor — сколько верим запомненному списку модераторов.
//
// Полчаса. Модератора назначают не каждый день, а вот выдать права посреди
// стрима и тут же попробовать команду — обычное дело. Поэтому есть ещё и
// modRetryAfter: незнакомый человек заставляет перечитать список, но не чаще
// чем раз в минуту, иначе любой зритель, пишущий «!!!» в чат, гонял бы нас в
// Twitch на каждое сообщение.
const (
	modCacheFor   = 30 * time.Minute
	modRetryAfter = time.Minute
)

// mods — запомненный список модераторов канала.
type mods struct {
	mu sync.Mutex
	// logins — логины в нижнем регистре. Пусто и at не нулевое означает
	// «список получен и он пуст»: модераторов на канале правда нет.
	logins map[string]bool
	// at — когда список получен, tried — когда в последний раз пытались.
	at    time.Time
	tried time.Time
	// ok — удалось ли получить список хоть раз. Пока нет, работаем по
	// значкам и не притворяемся, что знаем ответ.
	ok bool
}

// IsModerator сообщает, модератор ли этот человек на канале.
//
// Спрашивает Twitch, но не чаще, чем нужно: см. modCacheFor и modRetryAfter.
// При любой неудаче отвечает false — и это правильный ответ по умолчанию:
// команды достаются тем, чьи права мы подтвердили, а не тем, про кого не
// удалось выяснить.
func (c *Client) IsModerator(ctx context.Context, login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}

	// Сам стример модератором не числится — он хозяин канала. Проверяем
	// отдельно: без этого владелец, написавший из чужого клиента без значков,
	// оказался бы бесправным на собственном канале.
	if user := c.Account(); user != nil && strings.EqualFold(user.Login, login) {
		return true
	}

	c.mods.mu.Lock()
	fresh := c.mods.ok && time.Since(c.mods.at) < modCacheFor
	known := c.mods.logins[login]
	// Список свежий и человек в нём — ответ готов.
	if fresh && known {
		c.mods.mu.Unlock()
		return true
	}
	// Список свежий, а человека в нём нет. Обычно это правда «не модератор»,
	// но права могли выдать минуту назад — тогда перечитываем, не чаще раза
	// в минуту.
	if fresh && time.Since(c.mods.tried) < modRetryAfter {
		c.mods.mu.Unlock()
		return false
	}
	if !fresh && time.Since(c.mods.tried) < modRetryAfter && c.mods.ok {
		c.mods.mu.Unlock()
		return known
	}
	c.mods.tried = time.Now()
	c.mods.mu.Unlock()

	list, err := c.Moderators(ctx)
	if err != nil {
		code, _ := errs.Describe(err)
		c.log.Warn("не смог узнать список модераторов канала",
			"код", code, "ошибка", err)
		c.mods.mu.Lock()
		defer c.mods.mu.Unlock()
		return c.mods.logins[login]
	}

	c.mods.mu.Lock()
	defer c.mods.mu.Unlock()
	c.mods.logins = list
	c.mods.at = time.Now()
	c.mods.ok = true
	return list[login]
}

// ForgetModerators забывает запомненный список.
//
// Нужно после смены входа: у другого канала другие модераторы, и отвечать по
// чужому списку — верный способ пустить к командам не того.
func (c *Client) ForgetModerators() {
	c.mods.mu.Lock()
	defer c.mods.mu.Unlock()
	c.mods.logins = nil
	c.mods.at = time.Time{}
	c.mods.tried = time.Time{}
	c.mods.ok = false
}

// Moderators забирает у Twitch список модераторов канала.
//
// Ключи — логины в нижнем регистре: именно в таком виде логин приходит вместе
// с сообщением чата, и сравнивать надо по нему, а не по отображаемому имени.
// Отображаемое имя зритель меняет когда угодно и на что угодно.
func (c *Client) Moderators(ctx context.Context) (map[string]bool, error) {
	user := c.Account()
	if user == nil {
		return nil, errs.New(errs.TwitchAuthExpired, "Сначала подключи Twitch.")
	}

	out := map[string]bool{}
	cursor := ""
	// Сто за страницу — предел Twitch. Больше десяти страниц не читаем: тысяча
	// модераторов на канале не бывает, а бесконечный цикл из-за странного
	// ответа — бывает.
	for page := 0; page < 10; page++ {
		path := "/moderation/moderators?first=100&broadcaster_id=" + url.QueryEscape(user.ID)
		if cursor != "" {
			path += "&after=" + url.QueryEscape(cursor)
		}

		var reply struct {
			Data []struct {
				UserID    string `json:"user_id"`
				UserLogin string `json:"user_login"`
			} `json:"data"`
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &reply); err != nil {
			return nil, err
		}
		for _, m := range reply.Data {
			if login := strings.ToLower(strings.TrimSpace(m.UserLogin)); login != "" {
				out[login] = true
			}
		}
		cursor = reply.Pagination.Cursor
		if cursor == "" || len(reply.Data) == 0 {
			break
		}
	}

	c.log.Info("список модераторов канала получен", "сколько", len(out))
	return out, nil
}
