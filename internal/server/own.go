package server

import (
	"context"
	"strings"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/localapp"
	"songrequest-yandex/internal/player"
	"songrequest-yandex/internal/smtc"
	"songrequest-yandex/internal/yandex"
)

// Своя музыка стримера в виджете.
//
// Приложение живёт заказами и про фоновый плейлист ничего не знает: очередь
// пуста — значит показывать нечего. Но в кадре зритель хочет видеть «что
// играет», а не «что заказали», поэтому в паузах между заказами мы
// спрашиваем у Spotify, что там сейчас.
//
// Спрашиваем, а не подписываемся, потому что у Spotify нет уведомлений о
// смене трека — только опрос. Поэтому опрос устроен так, чтобы стоить как
// можно меньше:
//
//   - молчим, пока играет заказ: там трек известен точно и без запросов;
//   - молчим, если стример выключил показ своей музыки;
//   - молчим, если Spotify не подключён.
//
// Шаг опроса зависит от того, смотрит ли кто-нибудь в панель.
//
// Шесть секунд — экономный шаг для виджета: смена трека в кадре отстаёт
// максимум на этот срок.
//
// Две секунды — пока панель открыта. Там стример видит полосу времени и
// перематывает трек прямо в Spotify; с шестисекундным шагом полоса ехала
// следом с заметным опозданием, и выглядело это как «панель врёт».
//
// Была одна секунда, и это оказалось слишком жадно. Живьём: 29.08 Spotify
// ответил приложению «подождите четыре с половиной часа» — заказы встали на
// весь вечер. У приложения в режиме разработки (а другого у нас не будет:
// Client ID стример заводит на своём аккаунте) норма запросов тесная, и
// шестьдесят в минуту часами она не выдерживает. Полосу времени панель и так
// отсчитывает у себя, между ответами, — от двух секунд вместо одной человек
// не заметит ничего, кроме того, что Spotify перестал ругаться.
const (
	ownPollFast = 2 * time.Second
	ownPollSlow = 6 * time.Second

	// ownPollCalm — шаг после того, как Spotify пожаловался на частоту.
	// Держится десять минут (calmFor в internal/spotify), потом сам
	// возвращается к обычному.
	ownPollCalm = 15 * time.Second
)

// watchOwnPlayback держит в состоянии то, что стример слушает сам.
func (s *Server) watchOwnPlayback(ctx context.Context) {
	// Состояние опроса живёт здесь, в единственной горутине, которая его
	// читает и меняет. Полями сервера оно быть не должно: к ним лезут
	// обработчики панели из десятка других горутин.
	var w ownWatch

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.pollStep()):
			// Заодно переставляем шаг проверки играющего заказа: он живёт в
			// плеере и сам про жалобы Spotify не знает.
			s.applyPollStep()
			s.pollOwn(ctx, &w)
		}
	}
}

// readLocal читает играющий трек у программы Spotify на этом компьютере.
//
// Через поле, а не напрямую, ради проверок: на машине разработчика Spotify
// тоже запущен, и тесты ловили бы его настоящий трек вместо выдуманного.
func (s *Server) readLocal() (localapp.Track, localapp.Status) {
	if s.localTrack != nil {
		return s.localTrack()
	}
	return localapp.Read()
}

// pollStep — с каким шагом сейчас заглядывать, что играет.
//
// Шаг дешёвый: почти всегда это чтение заголовка окна программы Spotify, без
// всякой сети. По сети приложение ходит несравнимо реже — см. ownRefreshPanel.
func (s *Server) pollStep() time.Duration {
	// Spotify недавно просил сбавить темп — сбавляем. Продолжать в прежнем
	// темпе значит выпросить паузу на часы, а с ней встанут и заказы.
	if s.ym != nil && s.ym.RecentlyLimited() {
		return ownPollCalm
	}
	if s.panels.Load() > 0 {
		return ownPollFast
	}
	return ownPollSlow
}

// panelOpened и panelClosed считают открытые панели и заодно переводят на тот
// же шаг проверку играющего заказа: перемотка внутри заказа обязана доезжать
// до полосы так же быстро, как перемотка своей музыки.
func (s *Server) panelOpened() {
	s.panels.Add(1)
	s.applyPollStep()
}

func (s *Server) panelClosed() {
	s.panels.Add(-1)
	s.applyPollStep()
}

func (s *Server) applyPollStep() {
	if s.player != nil {
		s.player.SetPollEvery(s.orderPollStep())
	}
}

// orderPollStep — как часто спрашивать Spotify про играющий заказ.
//
// ЭТО САМЫЙ ДОРОГОЙ ОПРОС В ПРИЛОЖЕНИИ, и он же дважды доводил до беды.
// 29.08 Spotify закрыл приложению плеер на четыре с половиной часа, 10.09 — на
// двадцать один час. Оба раза перед этим шло плотное тестирование: заказы один
// за другим при открытой панели.
//
// Считалось так: пока панель открыта, шаг равен шагу опроса своей музыки —
// две секунды. Для своей музыки это бесплатно (читается заголовок окна
// Spotify), а вот заказ проверяется по сети, и две секунды — это тридцать
// запросов в минуту. За вечер стрима набегают тысячи, а норма у Client ID в
// режиме разработки тесная, и другого режима у нас не будет: стример заводит
// Client ID на своём аккаунте.
//
// Теперь раз в минуту. Пауз между треками от этого не появилось: приложение
// знает длительность заказа и просыпается ровно к его концу (см. awaitNap в
// internal/player) — конец трека ловится по часам, а не опросом.
//
// Частый опрос нужен был ровно для одного: заметить, что стример переключил
// или поставил на паузу музыку руками в самом Spotify. Это приложение теперь
// узнаёт с опозданием до минуты. Такова плата, и она несравнимо меньше суток
// без Spotify.
const (
	orderPoll = time.Minute
	// orderPollCalm — шаг после того, как Spotify пожаловался на частоту.
	// Раз в минуту: он только что сказал, что ему часто.
	orderPollCalm = time.Minute
)

func (s *Server) orderPollStep() time.Duration {
	if s.ym != nil && s.ym.RecentlyLimited() {
		return orderPollCalm
	}
	return orderPoll
}

// ownWatch — что мы уже знаем про свою музыку стримера.
//
// Смысл всей этой памяти в том, чтобы не спрашивать Spotify по сети чаще, чем
// нужно. Имя трека приходит бесплатно из заголовка окна программы Spotify, а
// по сети мы ходим только за тем, чего в заголовке нет: обложкой,
// длительностью и точным положением.
type ownWatch struct {
	// track — что показывает программа Spotify прямо сейчас.
	track localapp.Track
	// remote — когда последний раз спрашивали Spotify по сети без подсказки
	// от программы: когда её нет или музыка в ней стоит.
	remote time.Time
	// now — подробности, добытые по сети для этого трека. Пусто, если Spotify
	// недоступен: тогда в панели будут только артист и название.
	now *app.NowPlaying
	// at и pos — когда и на каком месте трека мы последний раз спрашивали
	// Spotify. Между запросами положение считаем сами.
	at  time.Time
	pos int
	// asked — когда последний раз ходили за подробностями. Нужно, чтобы
	// подхватывать перемотку внутри трека, но не каждую секунду.
	asked time.Time
	// cover — обложка нынешнего трека. Живёт отдельно от now, потому что при
	// чтении из системной панели Windows всё остальное приходит бесплатно и
	// каждый раз заново, а обложка стоит запроса и берётся один раз на трек.
	cover string
	// coverTried — за обложкой этого трека уже ходили.
	//
	// Признак нужен отдельно от самой обложки: спросить и не получить —
	// обычное дело (Spotify молчит, играет с телефона, кончилась норма). Без
	// него приложение просило бы обложку на каждом опросе, то есть раз в две
	// секунды, — ровно та утечка, ради устранения которой всё и затевалось.
	// Поймано проверкой TestIdleSpotifyIsNotAskedOften.
	coverTried bool
}

// forget сбрасывает всё, что знали про трек. Время последнего запроса по сети
// остаётся: оно про частоту запросов, а не про трек.
func (w *ownWatch) forget() {
	remote := w.remote
	*w = ownWatch{remote: remote}
}

// ownRefreshPanel и ownRefreshWidget — как часто уточнять положение по сети,
// пока играет один и тот же трек.
//
// Само имя трека уточнять не нужно: оно приходит из заголовка окна мгновенно
// и бесплатно. По сети мы ходим только за перемоткой — стример может двинуть
// ползунок в самом Spotify, и полоса в панели обязана поехать следом.
//
// Здесь была секунда, потом пятнадцать секунд, потом тридцать — и каждый раз
// Spotify всё равно однажды закрывал доступ: 29.08 на четыре с половиной часа,
// 10.09 на двадцать один. Дело в том, что этот запрос идёт постоянно, весь
// стрим, независимо от того, происходит ли что-нибудь. Пять часов эфира при
// шаге в тридцать секунд — это шестьсот запросов из воздуха.
//
// Поэтому теперь шаг длинный, а вся настоящая работа держится на другом:
// подробности спрашиваются, когда трек СМЕНИЛСЯ (см. fresh в pollOwnLocal), а
// смену трека приложение узнаёт бесплатно, из заголовка окна программы Spotify.
// Пять минут — это уже не «слежение», а редкая сверка на случай, если стример
// перемотал свою музыку руками: до сверки полоса в кадре будет врать, и это
// сознательная плата.
const (
	ownRefreshPanel  = 5 * time.Minute
	ownRefreshWidget = 10 * time.Minute
)

// pollOwn обновляет то, что стример слушает сам.
func (s *Server) pollOwn(ctx context.Context, w *ownWatch) {
	// Играет заказ — своя музыка сейчас не звучит, и спрашивать не о чем.
	if s.player.Now() != nil {
		w.forget()
		s.setOwn(nil)
		return
	}
	if !s.cfg.Get().Widget.ShowOwn {
		w.forget()
		s.setOwn(nil)
		return
	}

	// Первым делом — системная панель управления медиа Windows.
	//
	// Она знает всё, что нам нужно, и знает точно: название, артиста,
	// положение внутри трека, длительность и паузу. Всё это лежит внутри
	// Windows, стоит пять миллисекунд и не считается никакой нормой запросов.
	// Именно она снимает последний постоянный расход: раньше приложение
	// каждые несколько минут ходило к Spotify по сети только затем, чтобы
	// узнать, где сейчас игла.
	//
	// Заодно оттуда приходит то, чего у нас не было вовсе: пауза. В заголовке
	// окна Spotify её нет, а по сети за ней ходить слишком дорого — и до сих
	// пор приложение помнило только свои же нажатия.
	if done := s.pollOwnPanel(ctx, w); done {
		return
	}

	// Дальше начинаются пути, которым нужен работающий вход в Spotify: они
	// либо спрашивают его по сети, либо этим и заканчиваются.
	//
	// Проверка стоит здесь, а не выше, нарочно. Системная панель Windows
	// работает вообще без Spotify: она читает то, что программа сама
	// рассказала операционной системе. Поэтому в кадре и в панели трек виден
	// даже тогда, когда Spotify закрыл приложению доступ на сутки, — а
	// раньше в такой момент экран становился пустым, и выглядело это как
	// «приложение умерло».
	if !s.ym.Connected() {
		w.forget()
		s.setOwn(nil)
		return
	}

	// Панель промолчала — например, Windows старая или Spotify играет не с
	// этого компьютера. Тогда прежним путём: заголовок окна программы.
	track, status := s.readLocal()
	if status == localapp.Playing {
		s.pollOwnLocal(ctx, w, track)
		return
	}

	// Программа молчит или её нет вовсе. Спрашивать по сети всё-таки надо —
	// музыка могла играть с телефона или из браузера, — но редко: именно
	// частые запросы и довели 29.08 до четырёхчасовой паузы.
	remembered := w.track
	w.forget()
	w.track = localapp.Track{}

	if time.Since(w.remote) < ownRefreshWidget {
		// Между редкими запросами ничего не трогаем: мигать в кадре нечем.
		// Но если только что играл трек из программы Spotify, а теперь она
		// молчит, — тишину показать надо сразу.
		if status == localapp.Idle && !remembered.Empty() {
			s.setOwn(nil)
		}
		return
	}
	w.remote = time.Now()
	s.pollOwnRemote(ctx)
}

// pollOwnLocal ведёт трек, имя которого мы прочитали у программы Spotify.
func (s *Server) pollOwnLocal(ctx context.Context, w *ownWatch, track localapp.Track) {
	fresh := !track.Same(w.track)
	if fresh {
		// Трек сменился — всё, что мы знали про прошлый, больше не годится.
		w.forget()
		w.track = track
		// Заодно снимаем свою же отметку о паузе: музыка едет дальше, значит
		// на паузе она не стоит. Без этого кнопка в панели навсегда осталась
		// бы «продолжить» — стример снял бы паузу в самом Spotify, а панель
		// продолжала обещать обратное.
		s.ownPaused.Store(false)
	}

	if fresh || time.Since(w.asked) > s.ownRefresh() {
		w.asked = time.Now()
		s.fetchOwnDetails(ctx, w, track)
	}

	// Пока музыка стоит на паузе, точку отсчёта двигаем вместе с часами —
	// тогда положение остаётся там, где остановились. Иначе полоса в панели
	// продолжала бы ехать по стоящему треку и добралась бы до конца.
	if s.ownPaused.Load() && !w.at.IsZero() {
		w.at = time.Now()
	}

	s.setOwn(w.playing(track))
}

// ownRefresh — как часто уточнять положение по сети.
func (s *Server) ownRefresh() time.Duration {
	if s.panels.Load() > 0 {
		return ownRefreshPanel
	}
	return ownRefreshWidget
}

// fetchOwnDetails спрашивает у Spotify то, чего нет в заголовке окна:
// обложку, длительность и точное положение.
//
// Не получилось — не беда: в панели останутся артист и название, а полосы и
// обложки не будет. Это несравнимо лучше пустого места, которое человек видел
// раньше при любой заминке со Spotify.
func (s *Server) fetchOwnDetails(ctx context.Context, w *ownWatch, track localapp.Track) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	st, ok, err := s.ym.State(ctx)
	if err != nil {
		s.noteYandexError(err)
		s.log.Debug("не уточнил, что играет у стримера", "ошибка", err)
		return
	}
	if !ok || st == nil || st.Item == nil {
		return
	}

	// Spotify мог ответить про другой трек: заголовок окна меняется раньше,
	// чем это доезжает до его сервера. Тогда подробности не наши.
	if !sameTrack(track, st) {
		s.log.Debug("подробности пришли про другой трек",
			"в_окне", track.Artist+" — "+track.Title, "в_ответе", st.Item.Name)
		return
	}

	cover := ""
	if len(st.Item.Album.Images) > 0 {
		cover = st.Item.Album.Images[0].URL
	}
	w.now = &app.NowPlaying{
		Provider:   "yandex",
		Source:     app.SourceOwn,
		Title:      st.Item.Name,
		Artist:     track.Artist,
		CoverURL:   cover,
		DurationMs: st.Item.DurationMs,
	}
	w.pos, w.at = st.ProgressMs, time.Now()
}

// playing собирает то, что показать в панели и в кадре.
//
// Положение внутри трека считаем сами от последнего ответа Spotify: между
// запросами оно едет ровно так же, как едет музыка, и лишний запрос ради
// этого не нужен.
func (w *ownWatch) playing(track localapp.Track) *app.NowPlaying {
	if w.now == nil {
		// Подробностей нет — показываем то, что знаем наверняка.
		return &app.NowPlaying{
			Provider: "yandex",
			Source:   app.SourceOwn,
			Title:    track.Title,
			Artist:   track.Artist,
		}
	}

	now := *w.now
	now.PositionMs = w.pos + int(time.Since(w.at)/time.Millisecond)
	if now.DurationMs > 0 && now.PositionMs > now.DurationMs {
		now.PositionMs = now.DurationMs
	}
	return &now
}

// sameTrack сверяет трек из заголовка окна с ответом Spotify.
//
// Сверяем по названию, а не по артисту: в заголовке Spotify пишет главного
// исполнителя, а в ответе их бывает несколько, и первым не всегда тот же.
func sameTrack(track localapp.Track, st *yandex.PlayerState) bool {
	return strings.EqualFold(strings.TrimSpace(track.Title), strings.TrimSpace(st.Item.Name))
}

// pollOwnRemote — прежний путь: спросить Spotify по сети целиком.
//
// Нужен, когда программы Spotify на этом компьютере нет: стример слушает с
// телефона или из браузера. Тогда заголовок окна читать негде.
func (s *Server) pollOwnRemote(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	st, ok, err := s.ym.State(ctx)
	if err != nil {
		s.noteYandexError(err)
		// Обычное дело: сеть моргнула, токен обновляется, Spotify молчит.
		// Шуметь об этом в лог каждые шесть секунд незачем — уровень отладки.
		s.log.Debug("не прочитал, что играет у стримера", "ошибка", err)
		return
	}
	if !ok || st == nil || st.Item == nil || !st.IsPlaying {
		s.setOwn(nil)
		return
	}

	artist := ""
	if len(st.Item.Artists) > 0 {
		artist = st.Item.Artists[0].Name
	}
	cover := ""
	if len(st.Item.Album.Images) > 0 {
		cover = st.Item.Album.Images[0].URL
	}

	s.setOwn(&app.NowPlaying{
		Provider:   "yandex",
		Source:     app.SourceOwn,
		Title:      st.Item.Name,
		Artist:     artist,
		CoverURL:   cover,
		PositionMs: st.ProgressMs,
		DurationMs: st.Item.DurationMs,
	})
}

// setOwn запоминает свою музыку и рассылает состояние.
//
// Рассылаем на каждый опрос, а не только при смене трека, и вот почему:
// вместе с треком едет его позиция, а по ней страницы заводят свой отсчёт
// времени. Без регулярной рассылки только что открытая панель получала
// позицию той давности, когда трек начался, и полоса вставала не на своё
// место. Дёргаться от этого нечему: и панель, и виджет сверяют, что именно
// изменилось, и лишнего не перерисовывают.
func (s *Server) setOwn(now *app.NowPlaying) {
	s.mu.Lock()
	had := s.ownNow != nil
	s.ownNow = now
	s.mu.Unlock()

	// Тишина, которая была тишиной и осталась, — единственный случай, когда
	// рассылать нечего.
	if now == nil && !had {
		return
	}
	s.syncPlayback()
}

// own отдаёт то, что стример слушает сам, если это сейчас уместно.
func (s *Server) own() *app.NowPlaying {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownNow
}

// ownTrackLeft — сколько осталось до конца трека, который стример слушает сам.
//
// Берём из того же опроса, что кормит виджет: лишний запрос к Spotify ради
// оценки в чате не нужен, а точность до шести секунд здесь никому не важна.
func (s *Server) ownTrackLeft() time.Duration {
	own := s.own()
	if own == nil || own.DurationMs <= own.PositionMs {
		return 0
	}
	return time.Duration(own.DurationMs-own.PositionMs) * time.Millisecond
}

// ── системная панель Windows ─────────────────────────────────────────

// pollOwnPanel читает музыку стримера из системной панели управления медиа.
//
// Возвращает true, если разобрался сам и лезть куда-то ещё не надо. false
// означает «панель молчит»: Windows старая, Spotify в ней не виден или музыка
// идёт вообще не с этого компьютера — тогда работают прежние пути.
//
// По сети отсюда уходит ровно один запрос, и только когда сменился трек: за
// обложкой. Всё остальное Windows отдаёт бесплатно. См. internal/smtc.
func (s *Server) pollOwnPanel(ctx context.Context, w *ownWatch) bool {
	seen, ok, err := s.readPanel()
	if err != nil {
		// Не поломка, из-за которой стоит беспокоить человека: панель — это
		// удобство, а не работа приложения. Но в лог надо, иначе «почему-то
		// перестало показывать паузу» будет неразбираемо.
		s.log.Debug("системная панель управления медиа не ответила", "ошибка", err)
		return false
	}
	if !ok || strings.TrimSpace(seen.Title) == "" {
		return false
	}

	// Остановлен или закрыт — это тишина, и показывать нечего.
	if seen.Status == smtc.StatusClosed || seen.Status == smtc.StatusStopped {
		w.forget()
		s.setOwn(nil)
		return true
	}

	// Своя отметка о паузе больше не нужна: настоящая приезжает из Windows.
	// Гасим её, чтобы две правды не спорили между собой.
	s.ownPaused.Store(false)

	track := localapp.Track{Artist: seen.Artist, Title: seen.Title}
	if !track.Same(w.track) {
		// Трек сменился — прежняя обложка больше не годится.
		w.forget()
		w.track = track
	}

	now := &app.NowPlaying{
		Provider:   "yandex",
		Source:     app.SourceOwn,
		Title:      seen.Title,
		Artist:     seen.Artist,
		PositionMs: seen.PositionMs,
		DurationMs: seen.DurationMs,
		Paused:     seen.Paused(),
		CoverURL:   w.cover,
	}

	// Обложка — единственное, чего в панели Windows нет в готовом виде
	// (она отдаёт картинку потоком, а это отдельная возня с COM). Спрашиваем
	// её у Spotify один раз на трек и запоминаем.
	if w.cover == "" && !w.coverTried && s.wantCover() && s.ym.Connected() {
		w.coverTried = true
		if url := s.fetchOwnCover(ctx, seen.Title); url != "" {
			w.cover = url
			now.CoverURL = url
		}
	}

	s.setOwn(now)
	return true
}

// readPanel читает системную панель. Через поле, а не напрямую, ради
// проверок — как и readLocal.
func (s *Server) readPanel() (smtc.Now, bool, error) {
	if s.panelTrack != nil {
		return s.panelTrack()
	}
	return smtc.Read(smtc.YandexApp)
}

// orderPanel отдаёт плееру то, что показывает панель Windows.
type orderPanel struct{ s *Server }

func (o orderPanel) Track() (player.PanelTrack, bool) {
	seen, ok, err := o.s.readPanel()
	if err != nil || !ok || strings.TrimSpace(seen.Title) == "" {
		return player.PanelTrack{}, false
	}
	// Закрытый или остановленный Spotify — не ответ «играет другое», а
	// молчание: пусть решает Spotify по сети, как раньше.
	if !seen.Playing() && !seen.Paused() {
		return player.PanelTrack{}, false
	}
	return player.PanelTrack{
		Title:      seen.Title,
		Artist:     seen.Artist,
		PositionMs: seen.PositionMs,
		DurationMs: seen.DurationMs,
		Playing:    seen.Playing(),
	}, true
}

// wantCover — нужна ли обложка прямо сейчас.
//
// Если панель закрыта, а виджет обложку не показывает, то и запрос за ней —
// это запрос ни за чем. Мелочь, но из таких мелочей и складывалась норма,
// которую Spotify однажды закрыл на сутки.
func (s *Server) wantCover() bool {
	if s.panels.Load() > 0 {
		return true
	}
	return s.cfg.Get().Widget.ShowArt
}

// fetchOwnCover спрашивает у Spotify обложку играющего трека.
//
// Один запрос на смену трека. Сверяем название: заголовок в панели Windows
// меняется раньше, чем это доезжает до серверов Spotify, и без сверки к
// новому треку прилипла бы обложка предыдущего.
func (s *Server) fetchOwnCover(ctx context.Context, title string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	st, ok, err := s.ym.State(ctx)
	if err != nil {
		s.noteYandexError(err)
		s.log.Debug("не спросил обложку своей музыки", "ошибка", err)
		return ""
	}
	if !ok || st == nil || st.Item == nil || len(st.Item.Album.Images) == 0 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(st.Item.Name), strings.TrimSpace(title)) {
		s.log.Debug("обложка пришла от другого трека",
			"в_панели", title, "в_ответе", st.Item.Name)
		return ""
	}
	return st.Item.Album.Images[0].URL
}
