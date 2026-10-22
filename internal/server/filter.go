package server

import (
	"fmt"
	"strings"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/queue"
)

// Rejection — почему заказ не попал в очередь.
//
// Причина нужна в двух местах сразу: в панели стримеру и в чате зрителю.
// Формулировка одна, чтобы они не расходились.
type Rejection struct {
	Reason string
}

func (r *Rejection) Error() string { return r.Reason }

// check проверяет заказ до попадания в очередь. nil означает «пропускаем».
//
// Порядок проверок от дешёвых к дорогим: бан и лимит считаются локально, а
// длительность известна только после подбора трека.
func (s *Server) check(requester string, item queue.Item, cfg config.Config) *Rejection {
	// Пауза должна останавливать не только музыку, но и списание баллов.
	//
	// Кнопка «Стоп» ставит награду на паузу на самом Twitch, но эта попытка
	// может не пройти (сеть, лимит) — и тогда результат был такой: панель
	// пишет «Приём заказов остановлен», а зрители продолжают тратить баллы,
	// заказы копятся, и стример получает десять треков подряд, как только
	// снимет паузу.
	if s.player != nil && s.player.Paused() {
		return &Rejection{Reason: "стример остановил приём заказов"}
	}

	// Бан-лист хранит логин. Отображаемое имя может быть совсем другим —
	// у зрителей с кириллическим ником оно другое всегда, — и по нему бан
	// не срабатывал вовсе.
	who := item.RequesterLogin
	if who == "" {
		who = requester // старые заказы из базы, до появления логина
	}
	if banned, reason := s.isMusicBanned(who); banned {
		if reason != "" {
			return &Rejection{Reason: "тебе закрыт заказ музыки: " + reason}
		}
		return &Rejection{Reason: "тебе закрыт заказ музыки"}
	}

	if cfg.MaxPerUser > 0 {
		// По тому же ключу, что и бан: отображаемое имя зритель меняет в
		// Twitch мгновенно и без ограничений, то есть лимит по нему
		// обходится за десять секунд.
		n, err := s.queue.CountBy(who)
		if err != nil {
			s.log.Warn("не посчитал заказы зрителя", "ошибка", err)
		} else if n >= cfg.MaxPerUser {
			return &Rejection{Reason: fmt.Sprintf(
				"у тебя уже %d %s в очереди — дождись, пока отыграют",
				n, plural(n, "заказ", "заказа", "заказов"))}
		}
	}

	if cfg.MaxTrackSeconds > 0 && item.DurationMs > cfg.MaxTrackSeconds*1000 {
		return &Rejection{Reason: fmt.Sprintf(
			"трек длиннее %s — такое не ставим",
			humanDuration(time.Duration(cfg.MaxTrackSeconds)*time.Second))}
	}

	if reason := notMusic(item, cfg); reason != "" {
		return &Rejection{Reason: reason}
	}

	return nil
}

// notMusic отсеивает то, что музыкой не является: нарезки, часовые лупы,
// подкасты, записи стримов.
func notMusic(item queue.Item, cfg config.Config) string {
	haystack := match.Normalize(item.Title + " " + item.RawRequest)

	for _, word := range cfg.RejectKeywords {
		w := match.Normalize(word)
		if w == "" {
			continue
		}
		// Только целым словом. Вторая половина условия («просто вхождение»)
		// сводила первую на нет: в списке по умолчанию есть «mix», и из-за
		// него отсеивался любой Remix — то есть ровно то, что заказывают чаще
		// всего. Так же ловилось «стрим» внутри других слов.
		//
		// Составные стоп-слова («1 hour») по-прежнему ищутся как есть: там
		// пробел внутри, и границы слова уже заданы им самим.
		if strings.Contains(" "+haystack+" ", " "+w+" ") {
			return "это не похоже на музыку (" + word + ")"
		}
	}

	// Часовой луп по одной длительности: даже если в названии ничего нет.
	//
	// Порог берём из настроек, но не ниже двадцати минут: настройку «не
	// длиннее восьми минут» проверяет отдельное правило со своим текстом, а
	// здесь речь про заведомую не-музыку. Раньше двадцать минут стояли
	// намертво и молча перекрывали настройку, поднятую выше.
	limit := 20 * time.Minute
	if want := time.Duration(cfg.MaxTrackSeconds) * time.Second; want > limit {
		limit = want
	}
	if time.Duration(item.DurationMs)*time.Millisecond > limit {
		return "это слишком длинное для песни"
	}
	return ""
}

// isMusicBanned смотрит бан-лист музыки. Это отдельный список: человек может
// быть нормальным в чате, но неуместным в заказах.
func (s *Server) isMusicBanned(login string) (bool, string) {
	var reason string
	err := s.db.SQL().QueryRow(
		`SELECT COALESCE(reason, '') FROM music_bans WHERE login = ? COLLATE NOCASE`,
		strings.ToLower(login)).Scan(&reason)
	if err != nil {
		return false, ""
	}
	return true, reason
}

// banMusic закрывает зрителю заказы.
func (s *Server) banMusic(login, reason string) error {
	_, err := s.db.SQL().Exec(
		`INSERT INTO music_bans(login, reason, created_at) VALUES(?, ?, ?)
		 ON CONFLICT(login) DO UPDATE SET reason = excluded.reason`,
		strings.ToLower(login), reason, time.Now().Unix())
	return err
}

// unbanMusic возвращает зрителю право заказывать.
func (s *Server) unbanMusic(login string) error {
	_, err := s.db.SQL().Exec(`DELETE FROM music_bans WHERE login = ?`, strings.ToLower(login))
	return err
}

// syncExtras переносит в панель то, что живёт только в базе.
func (s *Server) syncExtras() {
	s.state.SetBans(s.musicBans())
}

// musicBans отдаёт бан-лист для панели.
func (s *Server) musicBans() []app.Ban {
	rows, err := s.db.SQL().Query(
		`SELECT login, COALESCE(reason, ''), created_at FROM music_bans ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []app.Ban
	for rows.Next() {
		var b app.Ban
		var created int64
		if err := rows.Scan(&b.Login, &b.Reason, &created); err != nil {
			continue
		}
		b.At = time.Unix(created, 0)
		out = append(out, b)
	}
	return out
}

// modLog записывает, кто из модераторов что сделал. Стример должен это видеть.
func (s *Server) modLog(actor, action, target string) {
	if _, err := s.db.SQL().Exec(
		`INSERT INTO mod_log(actor, action, target, created_at) VALUES(?, ?, ?, ?)`,
		actor, action, target, time.Now().Unix()); err != nil {
		s.log.Warn("не записал действие модератора", "ошибка", err)
	}
	s.log.Info("действие модератора", "кто", actor, "что", action, "над чем", target)
}

// history складывает отыгранное и отказы, чтобы их можно было найти потом.
func (s *Server) history(item queue.Item, outcome, reason string) {
	if _, err := s.db.SQL().Exec(`
		INSERT INTO history(requester, raw_request, provider, track_id, title, artist,
		                    outcome, reason, played_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Requester, item.RawRequest, item.Provider, item.TrackID,
		item.Title, item.Artist, outcome, reason, time.Now().Unix()); err != nil {
		s.log.Warn("не записал историю", "ошибка", err)
	}
}

func plural(n int, one, few, many string) string {
	a := n % 100
	b := a % 10
	switch {
	case a > 10 && a < 20:
		return many
	case b > 1 && b < 5:
		return few
	case b == 1:
		return one
	default:
		return many
	}
}

func humanDuration(d time.Duration) string {
	// Формы идут в порядке «одна, две, пять»: plural так и объявлена.
	// Раньше сюда передавали «секунды, секунд, секунд» — и зрители в чате
	// читали «1 секунды» и «примерно через 3 минут».
	m := int(d.Minutes())
	if m < 1 {
		return fmt.Sprintf("%d %s", int(d.Seconds()), plural(int(d.Seconds()), "секунда", "секунды", "секунд"))
	}
	return fmt.Sprintf("%d %s", m, plural(m, "минута", "минуты", "минут"))
}
