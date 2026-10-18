package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/links"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/twitch"
)

// Что делать с заказом, которого нет в Spotify.
//
// КАК БЫЛО. Не нашлось в Spotify — приложение молча искало на YouTube и
// включало найденное отдельным проигрывателем. Друг владельца жаловался: трек
// «играл непонятно где», а понять, откуда звук и как это настроить, было
// нельзя.
//
// КАК СТАЛО (11.10). Стример выбирает сам, настройка youtube_mode:
//
//   - «auto» — включать самим, но громко: уведомление в панели, строчка в
//     чате, значок YouTube в панели и в виджете;
//   - «ask»  — каждый раз спрашивать; заказ ждёт здесь, в панели две кнопки,
//     а через заданный срок решение принимается само (настройка
//     youtube_ask_else: пропустить или включить);
//   - «off»  — не играть вовсе, баллы вернуть.
//
// Ждущие заказы живут в памяти, а не в базе: срок ожидания — минута-другая,
// и перезапуск приложения ровно в эту минуту — редкость. Если всё же
// случится, заказ останется в очереди наград Twitch, откуда стример вернёт
// баллы руками.

// ytAsk — заказ с YouTube, ждущий решения.
type ytAsk struct {
	view app.YouTubeAsk
	r    twitch.Redemption
	item queue.Item
	note string
}

// youtubeMode — нынешний режим, всегда одно из трёх.
func (s *Server) youtubeMode() string {
	switch m := s.cfg.Get().YouTubeMode; m {
	case "auto", "off":
		return m
	default:
		return "ask"
	}
}

// youtubeOff — стример запретил играть мимо Spotify.
func (s *Server) youtubeOff() bool { return s.youtubeMode() == "off" }

// offerYouTube решает судьбу найденного на YouTube заказа по настройке.
func (s *Server) offerYouTube(ctx context.Context, r twitch.Redemption, item queue.Item, note string) {
	if s.youtubeMode() != "ask" {
		s.state.Notify("info", "Нет в Spotify — играет с YouTube: «"+trackLine(item)+"» ("+item.Via+")")
		s.acceptYouTube(ctx, r, item, note)
		return
	}

	cfg := s.cfg.Get()
	ask := ytAsk{r: r, item: item, note: note}
	ask.view = app.YouTubeAsk{
		Requester:  r.UserName,
		Request:    r.UserInput,
		Title:      item.Title,
		Artist:     item.Artist,
		URL:        item.URI,
		CoverURL:   item.CoverURL,
		DurationMs: item.DurationMs,
		Via:        item.Via,
		Else:       cfg.YouTubeAskElse,
	}
	wait := time.Duration(cfg.YouTubeAskSeconds) * time.Second
	if wait > 0 {
		ask.view.Until = time.Now().Add(wait)
	}

	s.mu.Lock()
	s.ytAskNo++
	ask.view.ID = strconv.FormatInt(s.ytAskNo, 10)
	s.ytAsks = append(s.ytAsks, ask)
	s.mu.Unlock()
	s.syncYouTubeAsks()

	s.state.SetOrderMatch(r.ID, app.OrderMatch{
		State: app.MatchFound, URI: item.URI, Title: item.Title, Artist: item.Artist,
		CoverURL: item.CoverURL, Duration: item.DurationMs,
		Note: "Ждёт решения: играть ли с YouTube",
	})
	s.state.Notify("info", "Нет в Spotify. Нашёл на YouTube: «"+trackLine(item)+
		"» — включить или отклонить, решайте в панели.")
	s.say(ctx, "@"+r.UserName+", в Spotify такого нет. Нашёл на YouTube: "+trackLine(item)+
		" — стример решает, включать ли.")

	// Срок ожидания. Решение по сроку идёт тем же путём, что и кнопка, — но
	// только если заказ ещё ждёт: стример мог успеть нажать сам.
	if wait > 0 {
		id := ask.view.ID
		time.AfterFunc(wait, func() {
			ctx, cancel := context.WithTimeout(s.baseContext(), 30*time.Second)
			defer cancel()
			play := s.cfg.Get().YouTubeAskElse == "play"
			if _, err := s.decideYouTube(ctx, id, play, "время вышло"); err == nil {
				s.log.Info("заказ с YouTube решён по сроку", "номер", id, "включён", play)
			}
		})
	}
}

// decideYouTube включает или отклоняет ждущий заказ.
func (s *Server) decideYouTube(ctx context.Context, id string, play bool, actor string) (ytAsk, error) {
	s.mu.Lock()
	var (
		ask   ytAsk
		found bool
	)
	for i, a := range s.ytAsks {
		if a.view.ID == id {
			ask, found = a, true
			s.ytAsks = append(s.ytAsks[:i], s.ytAsks[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return ytAsk{}, errs.New(errs.YouTubeAskNone, "Этот заказ уже не ждёт решения.")
	}
	s.syncYouTubeAsks()

	what := trackLine(ask.item)
	if play {
		s.modLog(actor, "включил с YouTube", what+" от "+ask.r.UserName)
		s.acceptYouTube(ctx, ask.r, ask.item, ask.note)
		return ask, nil
	}

	s.modLog(actor, "не стал включать с YouTube", what+" от "+ask.r.UserName)
	s.state.SetOrderMatch(ask.r.ID, app.OrderMatch{State: app.MatchFailed, Note: "С YouTube не включили"})
	reason := "стример не стал включать трек с YouTube"
	if actor == "время вышло" {
		reason = "стример не успел решить, включать ли трек с YouTube"
	}
	s.rejectRedemption(ctx, ask.r, reason)
	return ask, nil
}

// syncYouTubeAsks переносит ждущие заказы в панель.
func (s *Server) syncYouTubeAsks() {
	s.mu.Lock()
	list := make([]app.YouTubeAsk, 0, len(s.ytAsks))
	for _, a := range s.ytAsks {
		list = append(list, a.view)
	}
	s.mu.Unlock()
	s.state.SetYouTubeAsks(list)
}

// handleYouTubeTestSound — кнопка «Проверить звук» в разделе «YouTube».
func (s *Server) handleYouTubeTestSound(w http.ResponseWriter, r *http.Request) {
	if s.youtube == nil || s.ytTools == nil || !s.ytTools.Ready() {
		s.fail(w, errs.New(errs.YouTubeNoMpv, "Проигрыватель YouTube ещё не готов — подожди, пока он поставится."))
		return
	}
	if err := s.youtube.TestSound(r.Context(), s.cfg.Get().AudioDevice); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// rememberYouTube кладёт заказ в список «недавно с YouTube» во вкладке.
func (s *Server) rememberYouTube(r twitch.Redemption, item queue.Item) {
	s.state.AddYouTubeRecent(app.YouTubeRecent{
		Requester: r.UserName,
		Request:   r.UserInput,
		Title:     item.Title,
		Artist:    item.Artist,
		URL:       item.URI,
		Via:       item.Via,
		At:        time.Now(),
	})
}

// youtubeOffReason — что сказать зрителю, когда трека нет в Spotify, а
// играть мимо него стример запретил.
func youtubeOffReason() string {
	return "этого трека нет в Spotify, а с YouTube на этом канале не играют"
}

// isElsewhereLink — ссылка, которую играть можно только мимо Spotify.
func isElsewhereLink(text string) bool {
	l, ok := links.Find(text)
	return ok && (l.Kind == links.YouTube || l.Kind == links.VK || l.Kind == links.Yandex)
}
