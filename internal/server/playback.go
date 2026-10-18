package server

import (
	"context"
	"strconv"
	"strings"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/player"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/yandex"
)

// setupPlayer собирает плеер и подписывает панель на его события.
func (s *Server) setupPlayer(cfg *config.File) {
	s.player = player.New(s.queue, s.ym, s.log)
	s.player.ResumeDelay = time.Duration(cfg.Get().ResumeDelaySeconds) * time.Second
	if s.player.ResumeDelay <= 0 {
		s.player.ResumeDelay = 3 * time.Second
	}

	s.player.WaitForCurrent = cfg.Get().WaitForCurrent

	// Заказ не заиграл — спрашиваем цепочку источников, чем ещё это сыграть.
	// См. Server.rescue и internal/server/sources.go.
	s.player.SetRescue(s.rescue)

	// Играет ли ещё заказ — спрашиваем у Windows, а не у Spotify. Это был
	// последний опрос по сети, который шёл весь стрим. См. player/panel.go.
	s.player.SetPanel(orderPanel{s})

	s.player.OnChange = s.syncPlayback
	s.player.OnError = func(err error) { s.state.NotifyError(err) }

	// Заказ ждёт конца трека стримера. Молчать об этом нельзя: со стороны
	// панели это выглядит как «заказ принят и завис», и именно так это и
	// описывали — «еле включается».
	s.player.OnWaiting = func(item queue.Item, left time.Duration) {
		s.state.Notify("info", "«"+item.Artist+" — "+item.Title+
			"» заиграет после текущего трека, примерно через "+humanDuration(left)+".")
	}

	// Заказ не удалось включить — это не «отыграл». Баллы списаны, зритель
	// ничего не услышал; отметить такое выполненным значит забрать баллы ни
	// за что. Раньше при закрытом Spotify так молча прокручивалась вся
	// очередь разом.
	s.player.OnDropped = func(item queue.Item, err error) {
		go func() {
			// Очередь Яндекса недоступна — заказ не пропадает, а ложится в
			// плейлист «Заказы со стрима»: стример включит его сам.
			if s.toOrdersPlaylist(item, err) {
				return
			}
			_, text := errs.Describe(err)
			s.history(item, "rejected", text)

			ctx, cancel := context.WithTimeout(s.baseContext(), 20*time.Second)
			defer cancel()
			s.refund(ctx, item, "не получилось включить трек: "+text)
			s.syncPlayback()
		}()
	}

	// Трек отыграл — помечаем заказ выполненным на Twitch, чтобы он ушёл из
	// очереди наград, и складываем его в историю.
	//
	// В стороне от очереди: Twitch умеет отвечать двадцать секунд, и всё это
	// время следующий заказ просто не играл. Тишина между треками слышна, а
	// порядок этих отметок никому не важен.
	//
	// Оборванный заказ — не «отыгравший». Раньше скип писал в историю свою
	// строку, а следом плеер писал вторую, «played»: один трек двумя записями
	// с разным исходом. Теперь строку в историю пишет только это место, и
	// пишет ту, которая правда.
	s.player.OnFinished = func(item queue.Item, natural bool) {
		go func() {
			who, wantRefund := s.takeSkip()
			if natural {
				s.history(item, "played", "")
			} else {
				s.history(item, "skipped", who)
			}
			if item.RedemptionID == "" {
				return
			}

			ctx, cancel := context.WithTimeout(s.baseContext(), 20*time.Second)
			defer cancel()

			// Оборванный заказ возвращает баллы.
			//
			// Раньше скипнутый заказ отмечался выполненным — то есть баллы
			// списывались за трек, который зритель почти не услышал. Это была
			// самая частая жалоба в чат, и чинить её вручную стример мог
			// только через награду на Twitch, помня, кому и сколько.
			//
			// Возврат тоже убирает заказ из очереди наград на Twitch (он
			// становится отменённым), так что прежняя причина ставить отметку
			// «выполнено» здесь больше не действует.
			if !natural && wantRefund {
				s.refund(ctx, item, "заказ скипнули")
				return
			}

			// Отмечаем выполненным всё остальное: и отыгравшее до конца, и
			// оборванное без возврата (музыку переключили в самом Spotify).
			// Иначе заказ навсегда останется висеть в очереди наград.
			if err := s.twitch.FulfillRedemption(ctx, item.RewardID, item.RedemptionID); err != nil {
				s.log.Warn("не отметил заказ выполненным", "ошибка", err)
				return
			}
			// Без этой строки в итогах сессии вечно висело «12 заказов ·
			// 0 принято»: счётчик двигала только ручная кнопка в панели.
			s.state.SetRedemptionStatus(item.RedemptionID, app.OrderFulfilled)
		}()
	}

	// Очередь опустела и музыка вернулась на место — говорим об этом в панели
	// и, если источник вернуть не вышло, дозаполняем по выбранному режиму.
	s.player.OnRestored = func(outcome yandex.RestoreOutcome, snap *yandex.Snapshot) {
		go s.afterRestoreAsync(outcome, snap)
	}
}

// afterRestoreAsync досказывает про возврат и дозаполняет тишину.
//
// Тоже в стороне от очереди: внутри новые запросы к Spotify, а очередь в это
// время должна уметь принять следующий заказ.
func (s *Server) afterRestoreAsync(outcome yandex.RestoreOutcome, snap *yandex.Snapshot) {
	{
		if outcome.Restored {
			s.state.Notify("info", outcome.Message)
		} else if outcome.Code != "" {
			s.state.NotifyCode("warn", string(outcome.Code), outcome.Message)
		}
		ctx, cancel := context.WithTimeout(s.baseContext(), 30*time.Second)
		defer cancel()
		s.afterRestore(ctx, snap, outcome)
	}
}

// applyPlayerSettings переносит настройки в работающий плеер.
//
// Раньше они копировались один раз при создании сервера, и правка «ждать
// конца трека» или «задержка возврата» не значила ничего до перезапуска.
func (s *Server) applyPlayerSettings() {
	if s.player == nil {
		return
	}
	cfg := s.cfg.Get()

	delay := time.Duration(cfg.ResumeDelaySeconds) * time.Second
	if delay <= 0 {
		delay = time.Second
	}
	s.player.SetOptions(delay, cfg.WaitForCurrent)
}

// StartPlayer запускает проигрывание очереди.
func (s *Server) StartPlayer(ctx context.Context) {
	if s.player == nil {
		return
	}
	go s.player.Run(ctx)
	// Между заказами в кадре показываем то, что стример слушает сам.
	go s.watchOwnPlayback(ctx)
	s.syncPlayback()
}

// syncPlayback переносит состояние очереди и плеера в панель.
func (s *Server) syncPlayback() {
	if s.player == nil || s.queue == nil {
		return
	}

	items, err := s.queue.List()
	if err != nil {
		s.log.Warn("не прочитал очередь", "ошибка", err)
		return
	}

	view := make([]app.QueueItem, 0, len(items)+1)
	// Заказ, который ждёт конца трека стримера, идёт первым: он и заиграет
	// первым. Из самой очереди он уже вынут, поэтому в список его добавляем
	// отдельно.
	if w := s.player.WaitingItem(); w != nil {
		view = append(view, app.QueueItem{
			ID:         w.ID,
			Source:     w.Source,
			Requester:  w.Requester,
			Title:      w.Title,
			Artist:     w.Artist,
			Provider:   w.Provider,
			Via:        w.Via,
			DurationMs: w.DurationMs,
			Uncertain:  w.Uncertain,
			RawRequest: w.RawRequest,
			CoverURL:   w.CoverURL,
			Waiting:    true,
		})
	}
	for _, it := range items {
		view = append(view, app.QueueItem{
			ID:         it.ID,
			Source:     it.Source,
			Requester:  it.Requester,
			Title:      it.Title,
			Artist:     it.Artist,
			Provider:   it.Provider,
			Via:        it.Via,
			DurationMs: it.DurationMs,
			Uncertain:  it.Uncertain,
			RawRequest: it.RawRequest,
			CoverURL:   it.CoverURL,
		})
	}
	s.state.SetQueue(view)
	s.state.SetPaused(s.player.Paused())

	now := s.player.Now()
	if now == nil {
		// Заказов сейчас нет — показываем то, что стример слушает сам.
		// Может быть и пусто: тогда в кадре не будет ничего.
		own := s.own()
		if own != nil && s.ownPaused.Load() {
			// Свою паузу приложение знает только по своему же нажатию:
			// заголовок окна Spotify при паузе посреди трека не меняется, а
			// спрашивать по сети — та самая частота, из-за которой заказы
			// однажды встали на четыре часа. См. Server.ownPaused.
			held := *own
			held.Paused = true
			own = &held
		}
		s.noteLastPlayed(fromOwn(own))
		s.state.SetNow(own)
	} else {
		s.noteLastPlayed(fromOrder(now.Item))
		s.state.SetNow(&app.NowPlaying{
			Source:     app.SourceOrder,
			Provider:   now.Item.Provider,
			Via:        now.Item.Via,
			Title:      now.Item.Title,
			Artist:     now.Item.Artist,
			CoverURL:   now.Item.CoverURL,
			Requester:  now.Item.Requester,
			PositionMs: int(now.Elapsed().Milliseconds()),
			DurationMs: now.Item.DurationMs,
			Uncertain:  now.Item.Uncertain,
			Paused:     now.Paused(),
			Volume:     s.orderVolume(now.Item.Provider),
		})
	}

	// Снимок живёт в плеере: панель показывает, куда вернётся музыка.
	snap := s.player.Snapshot()
	s.state.UpdateYandex(func(info *app.YandexInfo) {
		if snap == nil {
			info.SnapshotText = ""
			info.SnapshotAt = nil
			return
		}
		at := snap.CapturedAt
		info.SnapshotText = snap.Describe()
		info.SnapshotAt = &at
	})
}

// enqueue ставит подобранный трек в очередь, проверив его фильтрами.
//
// Отказ — не молчаливый: зритель получает объяснение в чате и баллы обратно,
// стример видит причину в панели. Заказ, за который списали баллы и ничего не
// сказали, — это жалоба в чат через минуту.
func (s *Server) enqueue(ctx context.Context, item queue.Item) {
	cfg := s.cfg.Get()

	if reject := s.check(item.Requester, item, cfg); reject != nil {
		// Свой срок: контекст поиска мог уже истечь, и тогда баллы не
		// вернулись бы, а зритель не узнал бы причину.
		ctx, cancel := context.WithTimeout(s.baseContext(), 20*time.Second)
		defer cancel()

		s.log.Info("заказ отклонён",
			"зритель", item.Requester, "заказ", item.RawRequest, "причина", reject.Reason)
		s.history(item, "rejected", reject.Reason)
		s.refund(ctx, item, reject.Reason)
		s.state.NotifyWarn(errs.Code(""), "Отказ · "+item.Requester+": "+reject.Reason)
		s.say(ctx, "@"+item.Requester+", заказ не принят: "+reject.Reason)
		return
	}

	added, err := s.queue.Add(item, cfg.DonationPriority)
	if err != nil {
		s.log.Error("не смог поставить заказ в очередь", "ошибка", err)
		s.refund(ctx, item, "у приложения не получилось поставить трек в очередь")
		return
	}

	s.log.Info("заказ в очереди",
		"позиция", added.Position, "трек", added.Artist+" — "+added.Title,
		"заказал", added.Requester)

	if added.Source == queue.SourceDonation {
		// Заказ за баллы уже посчитан списком наград, а донат — нет.
		s.state.CountOrder()
	}

	s.syncPlayback()
	s.player.Nudge()

	// Зритель должен понимать, что заказ принят и когда его ждать: иначе
	// через минуту он спросит об этом в чате.
	wait, _ := s.queue.TotalDuration()
	if now := s.player.Now(); now != nil {
		wait += time.Duration(now.Item.DurationMs)*time.Millisecond - now.Elapsed()
	} else if cfg.WaitForCurrent {
		// Заказов сейчас нет, но первый ждёт конца трека стримера — и это
		// главная часть ожидания. Без неё зритель слышит «принято, следующим»
		// и три минуты тишины.
		wait += s.ownTrackLeft()
	}
	s.announceQueued(ctx, added, added.Position, wait)
}

// refund возвращает баллы за заказ и говорит зрителю, почему.
// Возвращает false, если баллы вернуть не вышло. Вызывающий обязан на это
// смотреть, когда собирается ещё и выкинуть заказ из очереди.
func (s *Server) refund(ctx context.Context, item queue.Item, reason string) bool {
	if item.RedemptionID == "" {
		return true // заказ пришёл не за баллы — возвращать нечего
	}
	if err := s.twitch.RefundRedemption(ctx, item.RewardID, item.RedemptionID); err != nil {
		s.log.Error("не смог вернуть баллы",
			"зритель", item.Requester, "причина_отказа", reason, "ошибка", err)
		s.state.NotifyError(err)
		return false
	}
	s.log.Info("баллы возвращены", "зритель", item.Requester, "причина", reason)
	s.state.SetRedemptionStatus(item.RedemptionID, app.OrderRefunded)
	return true
}

// skipCurrent обрывает текущий трек — или прекращает ожидание конца трека
// стримера, если заказ ещё не заиграл.
//
// Второе так же важно, как первое: заказ может ждать конца чужого трека
// минутами, и скип — единственная кнопка, которой это ускоряют. Раньше здесь
// стояло только `if now == nil { return }`, поэтому во время ожидания и
// кнопка в панели, и !скип в чате молча ничего не делали.
// skipResult — что именно случилось по команде «скип». Нужен для ответа в
// чат: молчаливый скип — это жалоба «команда не работает» через минуту.
type skipResult struct {
	// Item — заказ, который убрали. Пусто у ожидания (см. Hurried).
	Item queue.Item
	// Refunded — вернулись ли баллы. false бывает и когда заказ пришёл не за
	// баллы (донат, ручная постановка) — возвращать тогда нечего.
	Refunded bool
	// FromQueue — заказ убрали из очереди, а не с эфира.
	FromQueue bool
	// Hurried — не скип, а «не жди конца трека стримера, включай заказ».
	Hurried bool
}

func (s *Server) skipCurrent(actor string) {
	_, _ = s.skipPlaying(actor)
}

// skipPlaying обрывает текущий трек — или прекращает ожидание конца трека
// стримера, если заказ ещё не заиграл.
func (s *Server) skipPlaying(actor string) (skipResult, error) {
	now := s.player.Now()
	if now == nil {
		if !s.player.Waiting() {
			return skipResult{}, errs.New(errs.PlayerIdle, "Сейчас ничего не играет.")
		}
		s.state.Notify("info", actor+" не стал ждать конца трека — включаю заказ")
		s.modLog(actor, "не стал ждать конца трека", "")
		s.player.Skip()
		return skipResult{Hurried: true}, nil
	}

	// Историю пишет плеер, когда трек действительно закончится: отсюда её
	// писать нельзя, иначе на один заказ выходит две записи. Оставляем плееру
	// имя того, кто нажал, и решение про баллы.
	s.setSkip("скипнул "+actor, true)
	s.rememberSkip(now.Item, actor)

	s.modLog(actor, "скипнул", now.Item.Artist+" — "+now.Item.Title)
	s.state.Notify("info", actor+" скипнул: "+now.Item.Artist+" — "+now.Item.Title)
	s.player.Skip()

	return skipResult{Item: now.Item, Refunded: now.Item.RedemptionID != ""}, nil
}

// rememberSkip запоминает оборванный заказ для команды «вернуть».
//
// Один заказ, а не список: «вернуть» отменяет последнюю ошибку, а не ведёт
// корзину. Разбирать корзину посреди эфира никто не станет.
func (s *Server) rememberSkip(item queue.Item, actor string) {
	s.mu.Lock()
	copyItem := item
	s.lastSkip = &copyItem
	s.lastSkipAt = time.Now()
	s.lastSkipWho = actor
	s.lastSkipBack = false
	s.mu.Unlock()
}

// takeSkipBack забирает последний скипнутый заказ и помечает его возвращённым.
//
// Помечаем, а не забываем совсем: сам заказ пригодится в сообщении, а второй
// раз вернуть один и тот же трек нельзя — в очереди оказались бы два.
func (s *Server) takeSkipBack() (queue.Item, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastSkip == nil {
		return queue.Item{}, "", errs.New(errs.PlayerNothingBack,
			"За этот запуск ничего не скипали — возвращать нечего.")
	}
	if s.lastSkipBack {
		return queue.Item{}, "", errs.New(errs.PlayerNothingBack,
			"Этот заказ уже вернули в очередь.")
	}
	// Час — не придирка, а защита от неожиданности: «вернуть» через полстрима
	// поставило бы в эфир трек, про который все давно забыли, и выглядело бы
	// это как самовольная выходка приложения.
	if time.Since(s.lastSkipAt) > time.Hour {
		return queue.Item{}, "", errs.New(errs.PlayerNothingBack,
			"Последний скип был слишком давно — возвращать уже нечего.")
	}

	s.lastSkipBack = true
	return *s.lastSkip, s.lastSkipWho, nil
}

// removeFromQueue убирает заказ, при желании вернув за него баллы.
func (s *Server) removeFromQueue(ctx context.Context, id int64, refund bool, actor string) error {
	item, err := s.queue.Get(id)
	if err != nil {
		return err
	}

	// Порядок важен: сначала баллы, потом удаление.
	//
	// Раньше заказ сначала выкидывался из очереди, и если Twitch в этот
	// момент не отвечал, у зрителя не оставалось ни трека, ни баллов, а
	// повторить было нечем — заказа уже нет. В карточке заказов этот
	// порядок давно правильный, здесь остался старый.
	what := "удалил"
	if refund {
		what = "удалил и вернул баллы"
		if !s.refund(ctx, item, "заказ удалён из очереди") {
			return errs.New(errs.TwitchRefund,
				"Не смог вернуть баллы, поэтому заказ оставил в очереди. Попробуй ещё раз.")
		}
	}

	if err := s.queue.Remove(id); err != nil {
		return err
	}
	s.history(item, "removed", what)
	s.modLog(actor, what, item.Artist+" — "+item.Title)
	s.state.Notify("info", actor+" "+what+": "+item.Artist+" — "+item.Title)

	s.syncPlayback()
	return nil
}

// clearQueue очищает очередь целиком.
func (s *Server) clearQueue(ctx context.Context, refund bool, actor string) error {
	// Очистка идёт одной сделкой нарочно: иначе заказ, выхваченный плеером
	// ровно в этот миг, заиграет бесплатно. Значит вернуть баллы до удаления
	// нельзя, и остаётся честно сказать, за кого вернуть не вышло.
	gone, err := s.queue.Clear()
	if err != nil {
		return err
	}
	var lost []string
	for _, item := range gone {
		if refund && !s.refund(ctx, item, "очередь очищена") {
			lost = append(lost, item.Requester)
		}
		s.history(item, "removed", "очередь очищена")
	}
	if len(lost) > 0 {
		// Молчать здесь нельзя: зритель потратил баллы и не получил ни
		// трека, ни возврата, а узнать об этом стример может только отсюда.
		s.log.Error("баллы вернулись не всем", "кому_не_вернулись", lost)
		s.state.NotifyWarn(errs.TwitchRefund,
			"Баллы вернулись не всем: "+strings.Join(lost, ", ")+
				". Верни им вручную через награду на Twitch.")
	}

	s.modLog(actor, "очистил очередь", strconv.Itoa(len(gone))+" заказов")
	s.state.Notify("info", actor+" очистил очередь: "+strconv.Itoa(len(gone))+" "+
		plural(len(gone), "заказ", "заказа", "заказов"))

	s.syncPlayback()
	return nil
}

// Кто оборвал заказ.
//
// Плеер знает, что заказ оборвали, но не знает кем: скип мог прийти из
// панели, из чата от модератора или вовсе не прийти — стример переключил
// музыку руками в самом Spotify. Имя кладём здесь, а забирает его тот, кто
// пишет историю. Пусто — значит переключили в Spotify.
func (s *Server) setSkip(who string, refund bool) {
	s.mu.Lock()
	s.skipActor = who
	s.skipRefund = refund
	s.mu.Unlock()
}

// takeSkip забирает имя и решение про баллы и тут же их забывает: они годятся
// ровно на один заказ, а следующий скип может и не случиться.
//
// Пустое имя означает, что скипа не было вовсе: музыку переключили в самом
// Spotify. Баллы в этом случае не возвращаются — заказ, скорее всего, отыграл
// своё, а приложение просто увидело смену трека раньше конца.
func (s *Server) takeSkip() (who string, refund bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	who, refund = s.skipActor, s.skipRefund
	s.skipActor, s.skipRefund = "", false
	if who == "" {
		return "музыку переключили в самом Spotify", false
	}
	return who, refund
}

// toOrdersPlaylist пишет заказ в плейлист «Заказы со стрима», если включить
// его сразу не дала очередь Яндекса. true — заказ записан и баллы остаются
// списанными: трек будет, просто стример включит его сам.
func (s *Server) toOrdersPlaylist(item queue.Item, cause error) bool {
	code := errs.CodeOf(cause)
	if code != errs.YandexYnison && code != errs.YandexNoPlayer {
		return false
	}
	if _, _, ok := yandex.ParseURI(item.URI); !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(s.baseContext(), 20*time.Second)
	defer cancel()
	if err := s.ym.AddToOrders(ctx, item.URI); err != nil {
		s.log.Warn("не записал заказ и в плейлист", "трек", item.Artist+" — "+item.Title, "ошибка", err)
		return false
	}
	s.log.Info("очередь Яндекса недоступна — заказ записан в плейлист",
		"трек", item.Artist+" — "+item.Title, "причина", code)
	s.history(item, "playlist", yandex.OrdersPlaylistTitle)
	s.state.Notify("warn", "Очередь Яндекса недоступна — «"+item.Artist+" — "+item.Title+
		"» записан в плейлист «"+yandex.OrdersPlaylistTitle+"».")
	s.say(ctx, "@"+item.Requester+", трек записан в плейлист заказов — стример включит его чуть позже.")
	if item.RedemptionID != "" {
		if err := s.twitch.FulfillRedemption(ctx, item.RewardID, item.RedemptionID); err == nil {
			s.state.SetRedemptionStatus(item.RedemptionID, app.OrderFulfilled)
		}
	}
	s.syncPlayback()
	return true
}
