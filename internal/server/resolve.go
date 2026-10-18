package server

import (
	"context"
	"fmt"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/links"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/twitch"
)

// resolveTimeout — сколько ждём подбор трека. Пять запросов к Spotify с
// повторами укладываются с запасом, а зритель не должен ждать минуту.
const resolveTimeout = 25 * time.Second

// resolveOrder ищет заказанный трек в Spotify и ставит его в очередь.
//
// Не нашли — баллы возвращаются, и зритель получает объяснение в чат. Заказ,
// за который списали баллы и промолчали, — это жалоба в чат через минуту.
func (s *Server) resolveOrder(ctx context.Context, r twitch.Redemption) {
	// Срок ограничивает только поиск. Возврат баллов и сообщение зрителю
	// живут отдельно: раньше они шли с тем же контекстом, и если поиск съел
	// все двадцать пять секунд, возврат падал сразу — баллы не возвращались,
	// зритель ничего не узнавал, в логе оставалась одна строка.
	base := ctx
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	// Плейлист в награде за трек — отдельный разговор.
	//
	// Раньше такой ссылки просто не существовало для приложения: она не
	// опознавалась вовсе, текст после вырезания адреса оказывался пустым, и
	// зритель получал «ты не написал, что заказываешь». Теперь плейлисты
	// приложение читает, но за них платят другой наградой и по другой цене —
	// и сказать об этом надо прямо, иначе человек будет кидать плейлист в ту
	// же награду, пока не кончатся баллы.
	if l, ok := links.Find(r.UserInput); ok && l.IsCollection() {
		s.log.Info("плейлист заказан не той наградой",
			"зритель", r.UserLogin, "ссылка", l.URL)
		s.state.SetOrderMatch(r.ID, app.OrderMatch{
			State: app.MatchFailed,
			Note:  "Это плейлист, а не трек — нужна награда за плейлист",
		})
		s.rejectRedemption(s.afterSearch(base), r, s.playlistRewardHint())
		return
	}

	// Ссылка важнее текста: зритель уже указал конкретный трек, и гадать
	// по названию незачем.
	link, hasLink := s.fromLink(ctx, r.UserInput)
	if hasLink && link.Track != nil {
		s.acceptTrack(ctx, r, *link.Track, false, "Взято по ссылке")
		return
	}

	// Адрес в поисковом запросе бесполезен всегда — и когда мы ссылку
	// разобрали, и когда она от незнакомого сервиса.
	text := links.Strip(r.UserInput)
	if hasLink && link.Query != "" {
		text = link.Query
	}

	req := match.Parse(text)
	key := match.Key(req)

	if req.Title == "" {
		// Текста нет, но есть ссылка — по цепочке она сыграется.
		if hasLink && s.playElsewhere(ctx, r, req, link, viaLink) {
			return
		}
		// Ссылка на ролик, а YouTube стример выключил.
		if hasLink && s.youtubeOff() && isElsewhereLink(r.UserInput) {
			s.state.SetOrderMatch(r.ID, app.OrderMatch{State: app.MatchFailed, Note: "YouTube выключен в настройках"})
			s.rejectRedemption(s.afterSearch(base), r, "ссылки на YouTube и Яндекс.Музыку здесь не играют — закажи трек из Spotify")
			return
		}
		// Ссылка есть, а играть её нечем: запасной проигрыватель ещё не
		// готов. Раньше зритель получал в ответ «ты не написал, что
		// заказываешь» — самый сбивающий с толку ответ из возможных, ведь
		// написал он ровно то, что просили.
		if s.youtubeLinkNotReady(r.UserInput) {
			s.log.Info("ссылка на видео, а проигрыватель не готов",
				"зритель", r.UserLogin, "заказ", r.UserInput, "код", errs.YouTubeNoMpv)
			s.state.SetOrderMatch(r.ID, app.OrderMatch{
				State: app.MatchFailed,
				Note:  "Ссылка на видео, а запасной проигрыватель ещё не готов",
			})
			s.rejectRedemption(s.afterSearch(base), r,
				"это ссылка на видео, а проигрыватель для него ещё не готов — попробуй позже или закажи текстом")
			return
		}

		// Ссылка в заказе была, но прочитать её не вышло: YouTube не отдал
		// ролик, страница Яндекс.Музыки не открылась, Spotify не ответил.
		//
		// 27.08 владелец кинул обычную ссылку на YouTube и получил в ответ
		// «ты не написал, что заказываешь. Баллы вернул» — при том что
		// написал он ровно то, что просили. Проверка ниже ловила только
		// один случай из многих: «проигрыватель ещё не качается». Здесь
		// причина уже известна, её и говорим.
		if link.Note != "" {
			s.log.Info("ссылку прочитать не вышло",
				"зритель", r.UserLogin, "заказ", r.UserInput, "причина", link.Note)
			s.state.SetOrderMatch(r.ID, app.OrderMatch{
				State: app.MatchFailed,
				Note:  "Ссылку прочитать не вышло: " + link.Note,
			})
			s.rejectRedemption(s.afterSearch(base), r,
				"твою ссылку не удалось прочитать ("+link.Note+") — попробуй другую или напиши название текстом")
			return
		}

		s.log.Info("заказ без текста", "зритель", r.UserLogin)
		s.state.SetOrderMatch(r.ID, app.OrderMatch{
			State: app.MatchFailed,
			Note:  "Зритель не написал, что заказывает",
		})
		s.rejectRedemption(s.afterSearch(base), r, "ты не написал, что заказываешь")
		return
	}

	// Сначала кэш: один и тот же трек заказывают десятками за стрим.
	if hit, ok, err := s.matchCache.Get(ctx, key); err != nil {
		s.log.Warn("не прочитал кэш подбора", "ошибка", err)
	} else if ok {
		s.log.Info("трек взят из памяти",
			"заказ", r.UserInput, "трек", hit.Artist+" — "+hit.Title, "ручное", hit.Manual)
		s.acceptTrack(ctx, r, match.Candidate{
			ID: hit.TrackID, URI: "spotify:track:" + hit.TrackID,
			Title: hit.Title, Artists: []string{hit.Artist},
			DurationMs: hit.DurationMs, CoverURL: hit.CoverURL,
		}, false, memoryNote(hit.Manual))
		return
	}

	cfg := s.cfg.Get()
	opts := matchOptionsFromConfig(cfg)
	// Страна аккаунта: у Spotify свой каталог в каждой стране, и трек,
	// не лицензированный в ней, попросту не заиграет.
	if me := s.ym.Account(); me != nil {
		opts.Market = me.Country
	}
	// Длительность из ссылки — самый сильный сигнал против каверов,
	// ускоренных версий и часовых лупов.
	opts.WantMs = link.WantMs

	res, err := match.Find(ctx, s.ym, req, opts)
	if err != nil {
		code, text := errs.Describe(err)
		s.log.Error("поиск трека не удался", "заказ", r.UserInput, "код", code, "ошибка", err)

		// Отказ поиска — не конец заказа. Запасные пути раньше стояли
		// только ниже, в ветке «поиск отработал, но ничего не подошло», и
		// любая ошибка Spotify выбрасывала вместе с собой уже разобранную
		// ссылку на ролик: название, длительность и адрес лежали готовые,
		// mpv стоял готовый, а зритель получал «попробуй ещё раз».
		//
		// Ровно это и увидел тестер 27.08: Spotify отвечал 400 на каждый
		// поиск трека, и вместе с текстовыми заказами перестали работать
		// ссылки на YouTube и Яндекс.Музыку — хотя играть их Spotify не
		// требуется вовсе.
		if s.playElsewhere(ctx, r, req, link, viaNoAnswer) {
			s.log.Info("Spotify не ответил — заказ сыграет мимо него",
				"заказ", r.UserInput, "код", code)
			return
		}

		s.state.SetOrderMatch(r.ID, app.OrderMatch{State: app.MatchFailed, Note: text})
		s.rejectRedemption(s.afterSearch(base), r, "не получилось поискать трек, попробуй ещё раз")
		return
	}

	if !res.Found {
		// «Кандидатов 0» и «кандидатов 20, но все мимо» — два разных диагноза,
		// и по телефону их не различить. Поэтому пишем и то, что Spotify
		// вернул, и почему это не подошло.
		why := make([]string, 0, len(res.Rejected))
		for _, bad := range res.Rejected {
			why = append(why, fmt.Sprintf("%s — %s (%.2f, %s)",
				bad.Artist, bad.Title, bad.Score, bad.Why))
		}
		s.log.Info("трек не найден",
			"заказ", r.UserInput, "артист", req.Artist, "название", req.Title,
			"запросы", res.Attempts, "кандидатов", res.Considered,
			"порог", cfg.MatchMaybe, "страна_аккаунта", opts.Market,
			"отсеяно_страной", res.AbroadOnly, "лучшие_из_отвергнутых", why)

		// «Нет в Spotify» и «есть, но не в твоей стране» — разные вещи.
		// Первое лечится другим запросом, второе — только сменой страны
		// аккаунта, и пока об этом не сказать, стример будет думать, что
		// сломан поиск.
		note := "В Spotify не нашлось"
		if res.AbroadOnly > 0 {
			note = "Есть в Spotify, но не в стране аккаунта (" + opts.Market + ")"
			s.state.NotifyWarn(errs.SpotifyCountry,
				"«"+req.Clean+"» есть в Spotify, но не издан в стране твоего аккаунта ("+
					opts.Market+"). Такие треки играть нельзя — их не найдёт и поиск.")
		}
		s.state.SetOrderMatch(r.ID, app.OrderMatch{
			State: app.MatchMissing,
			Note:  note,
		})

		// Spotify не всесилен: в нём нет половины русского андеграунда и
		// почти ничего из мемов. Дальше — по цепочке запасных источников.
		if s.playElsewhere(ctx, r, req, link, viaMissing) {
			return
		}
		if res.AbroadOnly > 0 {
			s.rejectRedemption(s.afterSearch(base), r,
				"этот трек не издан в стране аккаунта стримера, Spotify его не отдаёт")
			return
		}
		if s.youtubeOff() {
			s.rejectRedemption(s.afterSearch(base), r, youtubeOffReason())
			return
		}
		s.rejectRedemption(s.afterSearch(base), r, "не нашёл такого трека ни в Spotify, ни на YouTube")
		return
	}

	s.log.Info("трек найден",
		"заказ", r.UserInput,
		"трек", firstArtist(res.Track.Artists)+" — "+res.Track.Title,
		"оценка", res.Score.Total, "почему", res.Score.Why,
		"неточно", res.Uncertain, "кандидатов", res.Considered)

	// В памяти держим только уверенные ответы: сомнительный подбор
	// закрепится и будет повторяться до конца стрима.
	if !res.Uncertain {
		if err := s.matchCache.Put(ctx, key, match.Hit{
			TrackID: res.Track.ID,
			Title:   res.Track.Title,
			Artist:  artistOf(res.Track),
			// Длительность и обложка обязательны: по ним считается лимит
			// длины, время до конца трека и картинка в панели. Без них заказ
			// из памяти обрывался на сорок пятой секунде.
			DurationMs: res.Track.DurationMs,
			CoverURL:   res.Track.CoverURL,
			Score:      res.Score.Total,
		}); err != nil {
			s.log.Warn("не запомнил подбор", "ошибка", err)
		}
	}

	note := ""
	if res.Uncertain {
		note = "Совпадение неточное — проверь, тот ли трек"
	}

	s.acceptTrack(ctx, r, res.Track, res.Uncertain, note)
}

// rejectRedemption возвращает баллы и объясняет зрителю, почему.
func (s *Server) rejectRedemption(ctx context.Context, r twitch.Redemption, reason string) {
	if r.ID != "" && r.RewardID != "" {
		if err := s.twitch.RefundRedemption(ctx, r.RewardID, r.ID); err != nil {
			s.log.Error("не смог вернуть баллы за неудачный заказ", "ошибка", err)
			s.state.NotifyError(err)
		} else {
			s.state.SetRedemptionStatus(r.ID, app.OrderRefunded)
		}
	}
	s.say(ctx, "@"+r.UserName+", "+reason+". Баллы вернул.")
}

func memoryNote(manual bool) string {
	if manual {
		return "Из твоего исправления"
	}
	return ""
}

// matchOptionsFromConfig нужен тестам и панели, чтобы не собирать структуру
// вручную в двух местах.
func matchOptionsFromConfig(cfg config.Config) match.Options {
	return match.Options{
		Accept: cfg.MatchAccept,
		Maybe:  cfg.MatchMaybe,
		Weights: match.Weights{
			Title:      cfg.MatchWeight.Title,
			Artist:     cfg.MatchWeight.Artist,
			Duration:   cfg.MatchWeight.Duration,
			Popularity: cfg.MatchWeight.Popularity,
			Version:    cfg.MatchWeight.Version,
		},
	}
}

// artistOf — первый артист кандидата или пусто.
func artistOf(c match.Candidate) string {
	return firstArtist(c.Artists)
}

// firstArtist — первый артист трека или пустая строка.
//
// Отдельная функция, потому что мест, где брали Artists[0] голым, набралось
// три штуки, и каждое из них зритель может дёрнуть сам: Spotify изредка
// отдаёт трек без артистов (локальный файл, эпизод подкаста, снятый с
// продажи трек), и приложение падало целиком. Заказ за баллы, роняющий
// программу посреди стрима, — это слишком дешёвая цена за одну проверку.
func firstArtist(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// afterSearch даёт свежий срок на то, что делается после поиска: возврат
// баллов и ответ зрителю. Поиск мог израсходовать весь свой, а эти два дела
// обязаны случиться в любом случае.
func (s *Server) afterSearch(base context.Context) context.Context {
	ctx, cancel := context.WithTimeout(base, 20*time.Second)
	// Отменяем по сроку, а не по возврату из функции: вызывающий уходит
	// сразу, а запросу надо дожить.
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ctx
}
