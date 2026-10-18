package server

import (
	"context"
	"strings"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/links"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/twitch"
)

// linkResult — что удалось понять из ссылки.
type linkResult struct {
	// Track — готовый трек Яндекса: подбирать нечего, зритель указал точно.
	Track *match.Candidate
	// Query — текст для обычного поиска, восстановленный по ссылке.
	Query string
	// WantMs — длительность из ссылки. Самый сильный сигнал против каверов,
	// ускоренных версий и часовых лупов.
	WantMs int
	// YouTube — играть напрямую с YouTube, минуя Яндекс.
	YouTube *queue.Item
	// Note — почему ссылку прочитать не вышло. Заполняется как раз тогда,
	// когда fromLink отвечает «не получилось»: без этого зритель со ссылкой
	// и зритель с пустым сообщением получали от приложения один и тот же
	// ответ — «ты не написал, что заказываешь».
	Note string
}

// fromLink разбирает ссылку в заказе.
//
// Порядок важен: ссылка на Яндекс даёт трек сразу, остальные — текст для
// поиска. Если поиск по нему ничего не даст, ссылка на YouTube или VK
// останется запасным вариантом, и заказ всё равно сыграет.
func (s *Server) fromLink(ctx context.Context, text string) (linkResult, bool) {
	link, ok := links.Find(text)
	if !ok {
		return linkResult{}, false
	}

	switch link.Kind {
	case links.Yandex:
		// Ссылка на сам Яндекс — точнее не бывает: берём трек по номеру.
		track, err := s.ym.Track(ctx, link.ID)
		if err == nil {
			s.log.Info("трек взят по ссылке Яндекса",
				"трек", firstArtist(track.Artists)+" — "+track.Title)
			return linkResult{Track: &track}, true
		}
		if errs.CodeOf(err) == errs.YandexNotAvailable {
			return linkResult{Note: "этот трек в Яндекс Музыке недоступен для прослушивания"}, false
		}
		// Без входа или при сбое — спросим открытую точку, она отдаст
		// название, и дальше пойдёт обычный поиск.
		meta, lerr := s.ymLinks.Lookup(ctx, link)
		if lerr != nil {
			s.log.Info("не узнал трек Яндекс Музыки", "ссылка", link.URL, "ошибка", err, "запасной", lerr)
			return linkResult{Note: "не удалось узнать трек по ссылке на Яндекс Музыку"}, false
		}
		return linkResult{Query: meta.Query(), WantMs: meta.DurationMs}, true

	case links.Spotify, links.Apple, links.Deezer:
		// Spotify из России не отвечает, у Apple и Deezer нужен свой ключ.
		// song.link знает всё сразу — и чаще всего прямую ссылку на Яндекс.
		res, err := s.odesli.Lookup(ctx, link.URL)
		if err != nil {
			s.log.Info("song.link не узнал ссылку", "ссылка", link.URL, "ошибка", err)
			return linkResult{Note: "не удалось узнать трек по ссылке"}, false
		}
		if res.YandexTrackID != "" {
			if track, err := s.ym.Track(ctx, res.YandexTrackID); err == nil {
				s.log.Info("трек найден через song.link", "откуда", link.Kind,
					"трек", firstArtist(track.Artists)+" — "+track.Title)
				return linkResult{Track: &track}, true
			}
		}
		s.log.Info("ищу по названию из song.link", "откуда", link.Kind, "запрос", res.Query())
		return linkResult{Query: res.Query()}, true

	case links.YouTube, links.VK:
		if s.youtube == nil || !s.ytTools.Ready() {
			return linkResult{Note: "запасной проигрыватель ещё не готов"}, false
		}
		track, err := s.youtube.Lookup(ctx, link.URL)
		if err != nil {
			s.log.Info("не разобрал ссылку", "ссылка", link.URL, "ошибка", err)
			_, note := errs.Describe(err)
			return linkResult{Note: note}, false
		}
		if track.IsLive {
			s.log.Info("по ссылке стрим, а не трек", "ссылка", link.URL)
			return linkResult{Note: "по ссылке идёт стрим, а не трек — он не кончится"}, false
		}

		s.log.Info("метаданные по ссылке",
			"откуда", link.Kind, "артист", track.Artist, "название", track.Title,
			"длительность_мс", track.DurationMs)

		// Сначала попробуем найти то же самое в Яндексе: играть оттуда
		// лучше — не нужен отдельный проигрыватель и звук не расходится.
		return linkResult{
			Query:  track.Artist + " - " + track.Title,
			WantMs: track.DurationMs,
			YouTube: &queue.Item{
				Provider:   "youtube",
				TrackID:    track.ID,
				URI:        track.URL,
				Title:      track.Title,
				Artist:     track.Artist,
				DurationMs: track.DurationMs,
				CoverURL:   track.CoverURL,
			},
		}, true
	}

	return linkResult{}, false
}

// acceptTrack ставит готовый трек Яндекса в очередь.
//
// Общий путь для всех источников: ссылка, память подбора и обычный поиск
// заканчиваются здесь, чтобы заказ выглядел и проверялся одинаково.
func (s *Server) acceptTrack(ctx context.Context, r twitch.Redemption,
	track match.Candidate, uncertain bool, note string) {

	state := app.MatchFound
	if uncertain {
		state = app.MatchUncertain
	}

	artist := firstArtist(track.Artists)

	s.state.SetOrderMatch(r.ID, app.OrderMatch{
		State:    state,
		TrackID:  track.ID,
		URI:      track.URI,
		Title:    track.Title,
		Artist:   artist,
		CoverURL: track.CoverURL,
		Duration: track.DurationMs,
		Note:     note,
	})

	s.enqueue(ctx, queue.Item{
		Source:         queue.SourcePoints,
		Requester:      r.UserName,
		RequesterLogin: strings.ToLower(r.UserLogin),
		RawRequest:     r.UserInput,
		Provider:       "yandex",
		TrackID:        track.ID,
		URI:            track.URI,
		Title:          track.Title,
		Artist:         artist,
		DurationMs:     track.DurationMs,
		CoverURL:       track.CoverURL,
		Uncertain:      uncertain,
		RedemptionID:   r.ID,
		RewardID:       r.RewardID,
	})
}

// acceptYouTube ставит в очередь то, что играется мимо Яндекса.
func (s *Server) acceptYouTube(ctx context.Context, r twitch.Redemption, item queue.Item, note string) {
	s.state.SetOrderMatch(r.ID, app.OrderMatch{
		State:    app.MatchFound,
		TrackID:  item.TrackID,
		URI:      item.URI,
		Title:    item.Title,
		Artist:   item.Artist,
		CoverURL: item.CoverURL,
		Duration: item.DurationMs,
		Note:     note,
	})

	item.Source = queue.SourcePoints
	item.Requester = r.UserName
	item.RequesterLogin = strings.ToLower(r.UserLogin)
	item.RawRequest = r.UserInput
	item.RedemptionID = r.ID
	item.RewardID = r.RewardID
	s.rememberYouTube(r, item)
	s.enqueue(ctx, item)
}

// youtubeLinkNotReady сообщает, что в заказе ссылка на видео, а сыграть её
// нечем: инструменты ещё качаются или не поставились.
//
// Отдельная проверка нужна потому, что fromLink в этом случае отвечает
// «ссылки нет» — и заказ дальше выглядит как пустой.
func (s *Server) youtubeLinkNotReady(text string) bool {
	if s.youtube != nil && s.ytTools != nil && s.ytTools.Ready() {
		return false
	}
	link, ok := links.Find(text)
	return ok && (link.Kind == links.YouTube || link.Kind == links.VK)
}
