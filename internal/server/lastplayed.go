package server

import (
	"encoding/json"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/queue"
)

// Последний игравший трек.
//
// Зачем это вообще. Между заказами в панели пусто: карточка «В эфире» либо
// показывает музыку самого стримера, либо не показывает ничего. А главный
// вопрос после тишины — «что это только что было». Раньше ответа не было
// нигде: трек уходил из состояния и не оставался ни в одном месте, кроме
// хроники, где он лежит строчкой текста без обложки и вперемешку с отказами.
//
// Теперь ушедший трек запоминается целиком и переживает перезапуск: приложение
// закрыли посреди стрима, открыли снова — и панель по-прежнему знает, чем
// кончилось. Хранится в таблице kv одной строкой JSON, а не отдельными полями:
// это одна запись, которую всегда читают и пишут целиком.

// kvLastPlayed — под каким именем последний трек лежит в базе.
const kvLastPlayed = "last_played"

// fromOrder — заказ зрителя в виде «последнего трека».
func fromOrder(it queue.Item) *app.LastPlayed {
	return &app.LastPlayed{
		Provider:   it.Provider,
		Via:        it.Via,
		Source:     app.SourceOrder,
		Title:      it.Title,
		Artist:     it.Artist,
		CoverURL:   it.CoverURL,
		Requester:  it.Requester,
		DurationMs: it.DurationMs,
		URI:        it.URI,
		RawRequest: it.RawRequest,
	}
}

// fromOwn — музыка самого стримера в виде «последнего трека».
//
// Адреса у неё нет: что играет, приложение читает из заголовка окна программы
// Spotify, а там только название и артист. Поставить такой трек заново
// приложение не сможет — и не должно: это музыка стримера, он сам ей хозяин.
func fromOwn(n *app.NowPlaying) *app.LastPlayed {
	if n == nil {
		return nil
	}
	return &app.LastPlayed{
		Provider:   n.Provider,
		Source:     n.Source,
		Title:      n.Title,
		Artist:     n.Artist,
		CoverURL:   n.CoverURL,
		Requester:  n.Requester,
		DurationMs: n.DurationMs,
	}
}

// trackKey — по чему считаем, что трек тот же самый.
//
// Не по указателю и не по всей структуре: у играющего трека каждую секунду
// меняется положение, и любое сравнение целиком считало бы сменой трека каждый
// тик. Название, артист и откуда он взялся — этого достаточно.
func trackKey(l *app.LastPlayed) string {
	if l == nil {
		return ""
	}
	return l.Source + "|" + l.Provider + "|" + l.Artist + "|" + l.Title
}

// noteLastPlayed следит за сменой того, что в эфире, и запоминает ушедшее.
//
// Зовётся на каждый пересчёт состояния, то есть часто. Поэтому первым делом —
// сравнение ключей: совпало, значит ничего не произошло, и дальше идти незачем.
func (s *Server) noteLastPlayed(cur *app.LastPlayed) {
	s.mu.Lock()
	prev := s.playing
	if trackKey(prev) == trackKey(cur) {
		s.mu.Unlock()
		return
	}
	s.playing = cur
	s.mu.Unlock()

	// Первый пересчёт после запуска: до него в эфире не было ничего, забывать
	// нечего. Прочитанный из базы прошлый трек при этом остаётся на месте.
	if prev == nil || prev.Title == "" {
		return
	}
	s.saveLastPlayed(*prev)
}

// saveLastPlayed кладёт ушедший трек в панель и в базу.
func (s *Server) saveLastPlayed(l app.LastPlayed) {
	l.At = time.Now()
	s.state.SetLastPlayed(&l)
	s.log.Debug("трек ушёл из эфира", "трек", l.Artist+" — "+l.Title, "откуда", l.Source)

	if s.db == nil {
		return
	}
	raw, err := json.Marshal(l)
	if err != nil {
		s.log.Warn("не собрал запись о последнем треке", "ошибка", err)
		return
	}
	if err := s.db.SetKV(kvLastPlayed, string(raw)); err != nil {
		s.log.Warn("не запомнил последний трек", "ошибка", err)
	}
}

// loadLastPlayed достаёт последний трек из базы при запуске.
//
// Ошибки здесь несмертельные: без последнего трека панель работает, просто в
// тишине ей нечего сказать. Поэтому все они — предупреждения в лог, а не отказ
// запускаться.
func (s *Server) loadLastPlayed() {
	if s.db == nil {
		return
	}
	raw, ok, err := s.db.GetKV(kvLastPlayed)
	if err != nil {
		s.log.Warn("не прочитал последний трек", "ошибка", err)
		return
	}
	if !ok || raw == "" {
		return
	}
	var l app.LastPlayed
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		s.log.Warn("запись о последнем треке не разобралась", "ошибка", err)
		return
	}
	if l.Title == "" {
		return
	}
	s.state.SetLastPlayed(&l)
}
