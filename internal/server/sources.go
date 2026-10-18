package server

import (
	"context"
	"strings"

	"songrequest-yandex/internal/links"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/twitch"
	"songrequest-yandex/internal/youtube"
)

// Цепочка источников.
//
// Как было. Запасной путь описывался парой одинаковых `if` в трёх местах
// resolve.go: «есть ссылка на ролик — играем её, иначе ищем на YouTube».
// Стоило добавить четвёртый случай — и его надо было вписать во все три места
// одинаково, ничего не забыв. Один раз именно так и вышло: 27.08 отказ поиска
// выбрасывал вместе с собой уже разобранную ссылку на ролик, потому что
// запасной путь стоял только в ветке «поиск отработал, но не нашёл».
//
// Как стало. Звенья перечислены один раз, по порядку, и всякий, кому нужен
// запасной источник, идёт по этому списку. Звено умеет ровно одно: сказать
// «вот заказ, который я сыграю» или «я тут не помогу». Добавить новый источник
// — значит дописать одно звено, а не искать все места, где перебор.
//
// Порядок звеньев — от точного к приблизительному:
//
//  1. Ссылка зрителя на ролик. Он уже сказал, что именно хочет.
//  2. Поиск на YouTube по тексту заказа. В Spotify нет половины русского
//     андеграунда и почти ничего из мемов.
//  3. Ссылка на Яндекс.Музыку — играем прямо её. Раньше со страницы Яндекса
//     брались только имя артиста и название, а сыграть её было нечем: не
//     нашлось в Spotify и на YouTube — заказ пропадал, хотя ссылка была.

// Почему заказ играет мимо Spotify. Короткие строчки для человека: они едут
// вместе с заказом в очередь, показываются в панели и переживают перезапуск.
const (
	viaLink      = "по ссылке зрителя"
	viaMissing   = "в Spotify такого нет"
	viaNoAnswer  = "Spotify не ответил на поиск"
	viaYandex    = "по ссылке на Яндекс.Музыку"
	viaRescueYT  = "Spotify не сыграл этот трек"
	viaRescueAlt = "первый ролик не заиграл"
)

// sourceLink — одно звено цепочки.
type sourceLink struct {
	// name — как звено называется в логе. Человеку показывается не оно, а
	// строчка via: имя звена ему ничего не говорит.
	name string
	// take отдаёт готовый заказ или nil, если это звено здесь не поможет.
	take func(ctx context.Context) *queue.Item
	// via — что написать человеку, если сыграло именно это звено.
	via string
}

// fallbackChain — по каким источникам идти, когда Spotify трека не дал.
//
// why — общая причина («в Spotify такого нет» или «Spotify не ответил»). Она
// достаётся звеньям, у которых своей причины нет: для поиска на YouTube важно
// именно то, почему мы вообще ушли из Spotify.
func (s *Server) fallbackChain(raw string, req match.Request, link linkResult, why string) []sourceLink {
	// Всё мимо Spotify играет один и тот же проигрыватель. Не готов — цепочки
	// нет вовсе, и это честнее, чем перебирать звенья, каждое из которых
	// упрётся в отсутствующий mpv.
	if s.youtube == nil || s.ytTools == nil || !s.ytTools.Ready() {
		return nil
	}
	// Стример запретил играть мимо Spotify — и искать незачем.
	if s.youtubeOff() {
		return nil
	}

	var chain []sourceLink

	// 1. Ссылка зрителя на ролик: уже разобрана, играть можно сразу.
	if link.YouTube != nil {
		ready := *link.YouTube
		chain = append(chain, sourceLink{
			name: "ссылка на ролик",
			via:  viaLink,
			take: func(context.Context) *queue.Item { return &ready },
		})
	}

	// 2. Поиск на YouTube по тексту заказа.
	if query := strings.TrimSpace(req.Clean); query != "" {
		chain = append(chain, sourceLink{
			name: "поиск на YouTube",
			via:  why,
			take: func(ctx context.Context) *queue.Item { return s.findOnYouTube(ctx, raw, query) },
		})
	}

	// 3. Ссылка на Яндекс.Музыку — играем её саму через yt-dlp.
	//
	// Получается не всегда: часть треков Яндекс отдаёт только со входом. Но
	// когда получается, заказ спасён, а раньше он в этом месте просто пропадал.
	if l, ok := links.Find(raw); ok && l.Kind == links.Yandex {
		url := l.URL
		chain = append(chain, sourceLink{
			name: "ссылка на Яндекс.Музыку",
			via:  viaYandex,
			take: func(ctx context.Context) *queue.Item { return s.lookupTrack(ctx, url) },
		})
	}

	return chain
}

// playElsewhere проводит заказ по цепочке и ставит в очередь первое, что
// получилось. false означает «нечем играть, возвращай баллы».
func (s *Server) playElsewhere(ctx context.Context, r twitch.Redemption,
	req match.Request, link linkResult, why string) bool {

	for _, source := range s.fallbackChain(r.UserInput, req, link, why) {
		item := source.take(ctx)
		if item == nil {
			s.log.Debug("звено не помогло", "звено", source.name, "заказ", r.UserInput)
			continue
		}
		s.log.Info("заказ сыграет мимо Spotify",
			"звено", source.name, "заказ", r.UserInput,
			"трек", item.Artist+" — "+item.Title, "почему", source.via)

		item.Via = source.via
		s.offerYouTube(ctx, r, *item, "Играем с YouTube — "+source.via)
		return true
	}
	return false
}

// findOnYouTube ищет трек на YouTube по тексту заказа.
//
// Зрителю в yt-dlp не отдаётся ни буквы: адрес мы собираем сами. Раньше текст
// заказа уезжал туда как есть, и заказ вида «--config-location=\\чужой\yt.conf»
// разбирался yt-dlp как свой ключ — то есть чужой человек за баллы подсовывал
// стримеру настройки yt-dlp на его же компьютере, а yt-dlp ходит в куки
// браузера.
func (s *Server) findOnYouTube(ctx context.Context, raw, query string) *queue.Item {
	// По ссылке не ищем: зритель уже сказал, что именно хочет.
	if l, ok := links.Find(raw); ok && (l.Kind == links.YouTube || l.Kind == links.VK) {
		return s.lookupTrack(ctx, l.URL)
	}
	track, err := s.youtube.Search(ctx, query)
	if err != nil {
		s.log.Info("на YouTube не нашлось", "запрос", query, "ошибка", err)
		return nil
	}
	return fromYouTube(track, s)
}

// lookupTrack читает метаданные по адресу и делает из них заказ.
func (s *Server) lookupTrack(ctx context.Context, url string) *queue.Item {
	track, err := s.youtube.Lookup(ctx, url)
	if err != nil {
		s.log.Info("не разобрал ссылку", "ссылка", url, "ошибка", err)
		return nil
	}
	return fromYouTube(track, s)
}

// fromYouTube превращает найденное в заказ. nil — играть это нельзя.
func fromYouTube(track *youtube.Track, s *Server) *queue.Item {
	if track == nil {
		return nil
	}
	// Стрим — не музыка, и играть его нельзя: он не кончится, а очередь
	// встанет до тех пор, пока стример не заметит и не скипнет.
	if track.IsLive {
		s.log.Info("это стрим, а не трек", "название", track.Title)
		return nil
	}
	return &queue.Item{
		Provider:   "youtube",
		TrackID:    track.ID,
		URI:        track.URL,
		Title:      track.Title,
		Artist:     track.Artist,
		DurationMs: track.DurationMs,
		CoverURL:   track.CoverURL,
	}
}

// rescue — что сыграть вместо заказа, который не заиграл. Зовёт плеер.
//
// Зачем. До этого цепочка работала только до старта: выбрали источник — и
// дальше как получится. А получалось по-разному: Spotify отвечал отказом на
// саму команду «играй» (уснувшее устройство, ограничение по стране, трек снят
// с продажи), mpv падал через полсекунды после запуска, ролик по ссылке
// оказывался закрыт по региону. Во всех этих случаях заказ пропадал целиком,
// хотя тот же трек прекрасно нашёлся бы на YouTube.
//
// Второй раз подряд спасать один и тот же заказ приложение не будет: если и
// запасной источник молчит, дело не в источнике.
func (s *Server) rescue(ctx context.Context, item queue.Item) (queue.Item, bool) {
	if s.youtube == nil || s.ytTools == nil || !s.ytTools.Ready() {
		return queue.Item{}, false
	}
	// Стример запретил YouTube. Режим «спрашивать» здесь не спрашивает: заказ
	// уже в эфире, и держать очередь, пока стример заметит вопрос, нельзя —
	// поэтому спасаем сразу, как раньше, но с уведомлением.
	if s.youtubeOff() {
		return queue.Item{}, false
	}
	// Этот заказ уже спасали — второй раз не пробуем.
	if item.Via == viaRescueYT || item.Via == viaRescueAlt {
		return queue.Item{}, false
	}

	// Ищем по тому, что уже известно про трек, а не по сырому тексту заказа:
	// имя артиста и название у нас на руках, и это лучший запрос из возможных.
	query := strings.TrimSpace(item.Artist + " " + item.Title)
	if query == "" {
		query = strings.TrimSpace(links.Strip(item.RawRequest))
	}
	if query == "" {
		return queue.Item{}, false
	}

	track, err := s.youtube.Search(ctx, query)
	if err != nil {
		s.log.Info("запасной источник тоже не нашёл", "запрос", query, "ошибка", err)
		return queue.Item{}, false
	}
	found := fromYouTube(track, s)
	if found == nil {
		return queue.Item{}, false
	}

	// Тот же ролик, что уже не заиграл, брать незачем.
	if found.URI == item.URI {
		return queue.Item{}, false
	}

	next := item
	next.Provider = found.Provider
	next.TrackID = found.TrackID
	next.URI = found.URI
	next.Title = found.Title
	next.Artist = found.Artist
	next.DurationMs = found.DurationMs
	next.CoverURL = found.CoverURL
	if item.Provider == "youtube" {
		next.Via = viaRescueAlt
	} else {
		next.Via = viaRescueYT
	}

	s.log.Warn("заказ не заиграл — беру запасной источник",
		"было", item.Artist+" — "+item.Title, "откуда_было", item.Provider,
		"стало", next.Artist+" — "+next.Title, "почему", next.Via)
	s.state.Notify("info", "«"+item.Title+"» не заиграл там, где мы его нашли — "+
		"играю с YouTube ("+next.Via+").")
	return next, true
}
