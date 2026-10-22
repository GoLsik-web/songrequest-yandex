package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/twitch"
)

// kvRewardID — идентификатор созданной награды. Хранится между запусками,
// чтобы приложение не плодило по новой награде на каждый старт.
const kvRewardID = "twitch_reward_id"

// kvPlaylistRewardID — то же самое для второй награды, за плейлист.
// Отдельным ключом, а не полем рядом: наградами приложение управляет
// поодиночке, и путать их номера нельзя — за чужую награду Twitch не даст
// вернуть баллы.
const kvPlaylistRewardID = "twitch_playlist_reward_id"

func (s *Server) handleTwitchLogin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	login, err := s.twitch.StartLogin(ctx)
	if err != nil {
		s.state.NotifyError(err)
		s.fail(w, err)
		return
	}

	expires := login.ExpiresAt
	s.state.UpdateTwitch(func(t *app.TwitchInfo) {
		t.PendingCode = login.UserCode
		t.PendingURL = login.VerifyURL
		t.PendingExpires = &expires
		t.Note = ""
		t.NoteCode = ""
	})
	s.state.Notify("info", "Открой "+login.VerifyURL+" и введи код "+login.UserCode)

	// Ждём подтверждения в фоне: стример вводит код на телефоне, а панель
	// в это время должна оставаться живой.
	go s.awaitTwitchLogin()

	writeJSON(w, login)
}

// awaitTwitchLogin дожидается подтверждения кода и поднимает всё остальное.
func (s *Server) awaitTwitchLogin() {
	ctx, cancel := context.WithTimeout(s.baseContext(), 31*time.Minute)
	defer cancel()

	if err := s.twitch.WaitLogin(ctx); err != nil {
		if ctx.Err() != nil {
			return // приложение закрывают, это не ошибка
		}
		s.state.NotifyError(err)
		s.clearPendingCode()
		s.syncTwitchInfo()
		return
	}

	s.clearPendingCode()
	s.state.Notify("info", "Twitch подключён")
	s.startTwitch(s.baseContext())
}

func (s *Server) clearPendingCode() {
	s.state.UpdateTwitch(func(t *app.TwitchInfo) {
		t.PendingCode = ""
		t.PendingURL = ""
		t.PendingExpires = nil
	})
}

// startTwitch доводит подключение до рабочего состояния: узнаёт канал,
// заводит награду и подписывается на заказы.
func (s *Server) startTwitch(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	user, err := s.twitch.CheckAccount(checkCtx)
	if err != nil {
		s.state.NotifyError(err)
		s.syncTwitchInfo()
		return
	}
	s.syncTwitchInfo()

	// Баллов на обычном канале нет. Это не поломка приложения, а условие
	// самого Twitch, поэтому говорим прямо и работу не прекращаем: донаты
	// на таком канале будут работать.
	if !user.HasChannelPoints() {
		problem := errs.New(errs.TwitchNoAffiliate,
			"Канал подключён, но баллов на нём нет — они бывают только у аффилиатов и партнёров Twitch. Приложение исправно, просто заказывать за баллы не получится. Донаты будут работать.")
		s.state.NotifyWarn(problem.Code, problem.Message)
		s.log.Info("на канале нет баллов, награду не создаём",
			"канал", user.Login, "тип_канала", user.Type)
		s.state.UpdateTwitch(func(t *app.TwitchInfo) {
			t.Note = problem.Message
			t.NoteCode = string(problem.Code)
		})

		// Соединение с Twitch всё равно поднимаем: через него работает чат,
		// то есть команды и все ответы зрителям. Без него на канале без
		// баллов молчала половина приложения.
		s.startEventSub(ctx, nil)
		return
	}

	// На награду — свой срок: проверка аккаунта могла съесть почти все
	// тридцать секунд, и тогда награда не создавалась из-за чужой задержки.
	rewardCtx, cancelReward := context.WithTimeout(ctx, 30*time.Second)
	defer cancelReward()

	knownID, _, err := s.db.GetKV(kvRewardID)
	if err != nil {
		s.log.Warn("не прочитал прежнюю награду", "ошибка", err)
	}

	reward, err := s.ensureMainReward(rewardCtx, knownID)
	if err != nil {
		s.state.NotifyError(err)
		code, text := errs.Describe(err)
		s.state.UpdateTwitch(func(t *app.TwitchInfo) {
			t.Note = text
			t.NoteCode = string(code)
		})
		// Без награды чат всё равно нужен: команды и ответы зрителям идут
		// через то же соединение. Раньше отсюда просто выходили, и вместе с
		// наградой молчало всё остальное.
		s.startEventSub(ctx, nil)
		if s.rewardRetryable(err) {
			go s.retryReward(ctx)
		}
		return
	}

	s.mu.Lock()
	s.rewardID = reward.ID
	s.mu.Unlock()

	s.state.UpdateTwitch(func(t *app.TwitchInfo) {
		t.RewardTitle = reward.Title
		t.RewardCost = reward.Cost
		t.RewardReady = true
		t.Note = ""
		t.NoteCode = ""
	})
	s.state.Notify("info", "Награда «"+reward.Title+"» готова, стоит "+strconv.Itoa(reward.Cost)+" "+pointsWord(reward.Cost))

	// Вторая награда — за плейлист. Её может не быть вовсе: это отдельная
	// галочка в настройках, и включают её не все.
	//
	// Отказ здесь не роняет первую награду: заказы поштучно должны работать,
	// даже если со второй наградой что-то не заладилось.
	playlistID := s.ensurePlaylistReward(rewardCtx)

	// Предупреждаем заранее: доступ к Twitch у публичных приложений живёт
	// тридцать дней, и «заказы перестали приходить» посреди стрима — худший
	// момент, чтобы это выяснить.
	if at, soon := s.twitch.RefreshExpiry(); soon {
		days := int(time.Until(at).Hours() / 24)
		s.state.NotifyWarn(errs.TwitchAuthExpired, fmt.Sprintf(
			"Доступ к Twitch кончается через %d %s — нажми «Подключить Twitch» ещё раз, это займёт минуту.",
			days, plural(days, "день", "дня", "дней")))
	}

	s.startEventSub(ctx, []string{reward.ID, playlistID})
}

// warnAboutScopes говорит, если вход в Twitch выдан без нужных прав.
//
// Список нужных прав со временем рос: право на чтение чата появилось позже
// самого приложения. Вход, выданный до этого, продолжает работать — заказы за
// баллы идут, лампочка зелёная, — но подписаться на чат им нельзя, и все
// команды молчат. Снаружи это выглядит как «!скип сломался», хотя приложению
// просто не дали права, о котором оно тогда не спрашивало.
//
// Лечится одним нажатием «Подключить Twitch»: вход выдаётся заново, уже со
// всеми правами. Сказать об этом надо в панели, а не в логе.
func (s *Server) warnAboutScopes() {
	if s.twitch == nil || !s.twitch.Connected() {
		return
	}
	missing := s.twitch.MissingScopes()
	if len(missing) == 0 {
		return
	}
	s.log.Warn("вход в Twitch выдан без части прав", "чего_не хватает", missing)

	what := "часть возможностей"
	if slices.Contains(missing, "user:read:chat") {
		what = "команды в чате"
	}
	s.state.NotifyCode("warn", string(errs.TwitchScopes),
		"Вход в Twitch выдан без всех прав, поэтому "+what+" работать не будут. "+
			"Нажми «Подключить Twitch» ещё раз — это займёт минуту.")
}

// startEventSub поднимает подписку на заказы, если она ещё не поднята.
func (s *Server) startEventSub(ctx context.Context, rewardIDs []string) {
	s.warnAboutScopes()

	// Пустые номера выбрасываем: наград бывает одна, две или ни одной, а
	// подписываться на пустоту нельзя — Twitch откажет и уронит соединение
	// вместе с чатом.
	rewardIDs = notEmpty(rewardIDs)
	// Подпись всего набора одной строкой: по ней видно, изменился ли он.
	rewardID := strings.Join(rewardIDs, ",")

	s.mu.Lock()
	if s.eventsRunning {
		if s.eventsReward == rewardID {
			s.mu.Unlock()
			return
		}
		// Награду пересоздали, и у неё новый номер. Старая подписка ждёт
		// события по прежнему — то есть заказы не придут вообще, при зелёной
		// лампочке. Гасим её и поднимаем заново.
		s.log.Info("награда сменилась, переподписываюсь",
			"было", s.eventsReward, "стало", rewardID)
		if s.stopEvents != nil {
			s.stopEvents()
		}
	}
	s.eventsRunning = true
	s.eventsReward = rewardID

	// Свой контекст, чтобы подписку можно было погасить, не гася приложение.
	subCtx, stop := context.WithCancel(ctx)
	s.stopEvents = stop
	s.mu.Unlock()

	ctx = subCtx

	events := twitch.NewEventSub(s.twitch)
	events.OnRedemption = s.onRedemption
	events.OnChat = s.onChat
	events.OnStatus = func(connected bool, detail string) {
		if connected {
			s.state.SetConnOK("Twitch", detail)
			return
		}
		s.state.SetConnFail("Twitch", detail)
	}

	go func() {
		events.Run(ctx, rewardIDs)
		s.mu.Lock()
		// Гасим флаг, только если это всё ещё наша подписка: более свежая
		// могла подняться, пока эта доживала.
		if s.eventsReward == rewardID {
			s.eventsRunning = false
		}
		s.mu.Unlock()
	}()
}

// onRedemption принимает заказ за баллы.
//
// Очереди и поиска трека ещё нет, поэтому заказ просто попадает в список
// панели: так проверяется весь путь от нажатия зрителем до возврата баллов.
func (s *Server) onRedemption(r twitch.Redemption) {
	s.state.AddRedemption(app.RedemptionView{
		ID:       r.ID,
		RewardID: r.RewardID,
		User:     r.UserName,
		Text:     r.UserInput,
		Cost:     r.RewardCost,
		At:       r.RedeemedAt,
		Status:   app.OrderNew,
		Match:    app.OrderMatch{State: app.MatchSearching},
	})
	s.state.Notify("info", "Заказ от "+r.UserName+": "+r.UserInput)

	// Наград две, и заказы у них разные: за трек ищем один трек, за плейлист
	// читаем пачку и кладём её ждать одобрения. Разбираемся здесь, по номеру
	// награды: дальше по коду это уже две разные дороги.
	//
	// Подбор идёт в фоне: EventSub ждать нельзя, иначе следующие заказы
	// встанут в очередь за этим.
	if playlist := s.playlistReward(); playlist != "" && r.RewardID == playlist {
		// Пауза приёма касается и плейлистов: раньше награда за плейлист
		// принималась и при остановленных заказах.
		if s.player != nil && s.player.Paused() {
			go func() {
				ctx, cancel := context.WithTimeout(s.baseContext(), 20*time.Second)
				defer cancel()
				s.state.SetOrderMatch(r.ID, app.OrderMatch{State: app.MatchFailed, Note: "Приём заказов остановлен"})
				s.rejectRedemption(ctx, r, "стример остановил приём заказов")
			}()
			return
		}
		go s.resolvePlaylist(s.baseContext(), r)
		return
	}
	go s.resolveOrder(s.baseContext(), r)
}

// handleRedemptionAction возвращает баллы или отмечает заказ выполненным.
func (s *Server) handleRedemptionAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.URL.Query().Get("action")

	var rewardID, user string
	for _, item := range s.state.Snapshot().Redemptions {
		if item.ID == id {
			rewardID = item.RewardID
			user = item.User
			break
		}
	}
	if rewardID == "" {
		s.mu.Lock()
		rewardID = s.rewardID
		s.mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var err error
	var status string
	switch action {
	case "refund":
		// Сначала баллы, потом очередь — порядок здесь важен.
		//
		// Раньше заказ вычёркивался первым. Если Twitch не отвечал, заказ уже
		// пропал, а баллы у зрителя не вернулись: и трека нет, и баллов нет,
		// и повторить нечем. Теперь очередь трогаем только после того, как
		// Twitch подтвердил возврат: иначе заказ просто останется на месте.
		err, status = s.twitch.RefundRedemption(ctx, rewardID, id), app.OrderRefunded
		if err == nil {
			// Оставить его в очереди нельзя: он отыграет бесплатно, а в конце
			// приложение попробует отметить выполненным уже отменённый заказ.
			s.dropFromQueue(id)
		}
	case "fulfill":
		err, status = s.twitch.FulfillRedemption(ctx, rewardID, id), app.OrderFulfilled
	default:
		s.fail(w, errs.New(errs.TwitchRefund, "Непонятное действие с заказом."))
		return
	}

	if err != nil {
		s.state.NotifyError(err)
		s.fail(w, err)
		return
	}

	s.state.SetRedemptionStatus(id, status)
	if status == app.OrderRefunded {
		s.state.Notify("info", "Баллы за заказ возвращены")
		// Зритель не видит панель: без строки в чате заказ у него просто
		// пропадал, и было непонятно, вернулись ли баллы.
		if user != "" {
			s.say(ctx, "@"+user+", заказ снят, баллы вернул")
		}
	} else {
		s.state.Notify("info", "Заказ принят, баллы не возвращаются")
	}
	writeJSON(w, map[string]string{"status": status})
}

func (s *Server) handleTwitchLogout(w http.ResponseWriter, r *http.Request) {
	s.twitch.Logout()
	s.clearPendingCode()
	s.syncTwitchInfo()
	s.state.Notify("info", "Twitch отключён")
	writeJSON(w, map[string]bool{"ok": true})
}

// syncTwitchInfo обновляет карточку Twitch в панели.
func (s *Server) syncTwitchInfo() {
	user := s.twitch.Account()
	cfg := s.cfg.Get()

	s.mu.Lock()
	rewardID := s.rewardID
	s.mu.Unlock()

	s.state.UpdateTwitch(func(t *app.TwitchInfo) {
		t.HasClientID = strings.TrimSpace(cfg.TwitchClientID) != ""
		t.Connected = s.twitch.Connected()
		t.RewardReady = rewardID != ""
		// Название и цену берём из настроек всегда, а не только пока они
		// пусты: иначе стример правит цену, а панель до перезапуска
		// показывает старую — и это выглядит как «не сохранилось».
		t.RewardTitle = cfg.RewardTitle
		t.RewardCost = cfg.RewardCost
		if at, soon := s.twitch.RefreshExpiry(); !at.IsZero() {
			when := at
			t.RenewAt = &when
			t.RenewSoon = soon
		} else {
			t.RenewAt = nil
			t.RenewSoon = false
		}

		if user != nil {
			t.Channel = user.Login
			t.ChannelType = user.StatusLabel()
			t.HasPoints = user.HasChannelPoints()
		} else {
			t.Channel = ""
			t.ChannelType = ""
			t.HasPoints = false
		}
	})

	info := s.state.Snapshot().Twitch
	switch {
	case !info.HasClientID:
		s.state.SetConnIdle("Twitch", "Не настроено")
	case !info.Connected:
		s.state.SetConnIdle("Twitch", "Не подключён")
	case user == nil:
		if done, err := s.twitch.LastCheck(); done && err != nil {
			code, text := errs.Describe(err)
			s.state.SetConnFail("Twitch", string(code)+" · "+text)
		} else if done {
			s.state.SetConnFail("Twitch", "Вход есть, а данных нет. Отключи и подключи заново.")
		} else {
			s.state.SetConnIdle("Twitch", "Проверяю связь…")
		}
	case !info.HasPoints:
		// Не ошибка, а свойство канала: чинить нечего, красный цвет тут
		// сказал бы «приложение сломалось», и человек бросил бы установку.
		s.state.SetConnIdle("Twitch", user.Login+" · баллов на канале нет")
	case !info.RewardReady:
		s.state.SetConnFail("Twitch", user.Login+" · награда не создана")
	default:
		s.state.SetConnOK("Twitch", user.Login)
	}
}

// pushReward доносит название и цену награды до самого канала.
//
// Настройка, которая молча ничего не меняет, хуже отсутствующей: человек
// правит цену, видит «Настройки сохранены» и уходит уверенным, что сделал
// дело. Поэтому правка едет на Twitch сразу, а о неудаче говорим вслух.
func (s *Server) pushReward() {
	if !s.twitch.Connected() {
		return
	}

	s.mu.Lock()
	id := s.rewardID
	s.mu.Unlock()
	// Пустой номер означает, что награду ещё не создавали: Twitch мог лежать
	// при запуске. Молча выходить нельзя — правка цены не значила бы ничего,
	// и никакой повторной сверки в приложении нет. EnsureReward с пустым
	// номером просто создаст награду.

	ctx, cancel := context.WithTimeout(s.baseContext(), 20*time.Second)
	defer cancel()

	reward, err := s.ensureMainReward(ctx, id)
	if err != nil {
		s.log.Error("не обновил награду на канале", "ошибка", err)
		s.state.NotifyError(err)
		return
	}

	s.mu.Lock()
	changed := s.rewardID != reward.ID
	s.rewardID = reward.ID
	s.mu.Unlock()

	// Награду за плейлист правки тоже касаются: её цена считается от цены
	// трека, и после смены цены она обязана поехать следом. Заодно это
	// создаёт награду, если галочку только что поставили, и убирает, если
	// сняли.
	playlistID := s.ensurePlaylistReward(ctx)

	// Награду могли пересоздать — тогда подписка ждёт события по старому
	// номеру, и заказы просто не придут.
	if changed || playlistID != "" {
		s.startEventSub(s.baseContext(), []string{reward.ID, playlistID})
	}

	s.syncTwitchInfo()
	s.state.Notify("info", fmt.Sprintf("Награда на канале обновлена: «%s», %d",
		reward.Title, reward.Cost))
}

// SyncTwitch обновляет карточку Twitch снаружи (при старте).
func (s *Server) SyncTwitch() { s.syncTwitchInfo() }

// StartTwitchIfConnected поднимает Twitch при запуске приложения, если вход
// уже был выполнен раньше.
func (s *Server) StartTwitchIfConnected(ctx context.Context) {
	if !s.twitch.Connected() {
		s.syncTwitchInfo()
		return
	}
	go s.startTwitch(ctx)
}

// dropFromQueue убирает из очереди заказ по его номеру на Twitch.
func (s *Server) dropFromQueue(redemptionID string) {
	items, err := s.queue.List()
	if err != nil {
		return
	}
	for _, it := range items {
		if it.RedemptionID != redemptionID {
			continue
		}
		if err := s.queue.Remove(it.ID); err != nil {
			s.log.Warn("не убрал заказ из очереди", "заказ", it.Title, "ошибка", err)
			return
		}
		s.log.Info("заказ убран из очереди вместе с возвратом баллов",
			"трек", it.Artist+" — "+it.Title)
		s.syncPlayback()
		return
	}
}

// notEmpty выбрасывает пустые номера наград.
func notEmpty(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			out = append(out, id)
		}
	}
	return out
}

// ensurePlaylistReward приводит награду за плейлист в согласие с настройками.
//
// Возвращает её номер или пустую строку, если награды быть не должно либо
// создать её не вышло. Отказ здесь нарочно не считается бедой: заказы
// поштучно — главное, что делает приложение, и ронять их из-за второй награды
// нельзя. О неудаче человек узнаёт из панели, а не из молчания.
func (s *Server) ensurePlaylistReward(ctx context.Context) string {
	known, _, err := s.db.GetKV(kvPlaylistRewardID)
	if err != nil {
		s.log.Warn("не прочитал прежнюю награду за плейлист", "ошибка", err)
	}

	if !s.cfg.Get().PlaylistReward {
		// Галочку сняли — гасим награду на канале, чтобы зрители её не
		// нажимали. Именно гасим, а не удаляем: удалённая награда уносит с
		// собой историю, а вернуть её обратно одним нажатием уже нельзя.
		if known != "" {
			if err := s.twitch.SetRewardPaused(ctx, known, true); err != nil {
				s.log.Warn("не выключил награду за плейлист", "ошибка", err)
			}
		}
		s.state.UpdateTwitch(func(t *app.TwitchInfo) {
			t.PlaylistReady = false
			t.PlaylistTitle = ""
			t.PlaylistCost = 0
		})
		return ""
	}

	reward, err := s.twitch.EnsurePlaylistReward(ctx, known)
	if err != nil {
		code, text := errs.Describe(err)
		s.log.Warn("не создал награду за плейлист", "код", code, "ошибка", err)
		s.state.NotifyWarn(code, "Награда за плейлист: "+text)
		s.state.UpdateTwitch(func(t *app.TwitchInfo) { t.PlaylistReady = false })
		return ""
	}

	if err := s.db.SetKV(kvPlaylistRewardID, reward.ID); err != nil {
		s.log.Warn("не запомнил награду за плейлист", "ошибка", err)
	}

	s.mu.Lock()
	changed := s.playlistRewardID != reward.ID
	s.playlistRewardID = reward.ID
	s.mu.Unlock()

	s.state.UpdateTwitch(func(t *app.TwitchInfo) {
		t.PlaylistReady = true
		t.PlaylistTitle = reward.Title
		t.PlaylistCost = reward.Cost
	})
	if changed {
		n := s.cfg.Get().PlaylistTrackCount()
		s.state.Notify("info", "Награда «"+reward.Title+"» готова, стоит "+
			strconv.Itoa(reward.Cost)+" баллов за "+
			strconv.Itoa(n)+" "+plural(n, "трек", "трека", "треков"))
	}
	return reward.ID
}

// playlistReward — номер награды за плейлист прямо сейчас.
func (s *Server) playlistReward() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playlistRewardID
}

// ensureMainReward заводит награду за трек и запоминает её номер.
//
// Частый случай у тех, кто раньше пользовался другим приложением заказов:
// награда «Заказ трека» на канале уже есть, но создана не нами, и Twitch
// отказывает в такой же. Человек при этом видит «награда не создалась» и не
// понимает, что делать. Поэтому один раз пробуем своё название с пометкой и,
// если вышло, записываем его в настройки — иначе при следующем запуске
// приложение попыталось бы вернуть прежнее и упёрлось бы в тот же отказ.
func (s *Server) ensureMainReward(ctx context.Context, knownID string) (*twitch.Reward, error) {
	reward, err := s.twitch.EnsureReward(ctx, knownID)
	if err != nil && errs.CodeOf(err) == errs.TwitchForeignAward {
		title := s.cfg.Get().RewardTitle
		alt := title + " (Яндекс)"
		if len([]rune(alt)) > 45 { // предел длины названия у Twitch
			alt = "Трек в Яндекс Музыке"
		}
		s.log.Info("название награды занято чужой наградой, беру другое",
			"было", title, "стало", alt)
		if uerr := s.cfg.Update(func(c *config.Config) { c.RewardTitle = alt }); uerr != nil {
			return nil, err
		}
		reward, err = s.twitch.EnsureReward(ctx, knownID)
		if err != nil {
			// Не вышло и так — возвращаем настройку как была, чтобы не
			// оставлять человеку название, которого он не выбирал.
			s.cfg.Update(func(c *config.Config) { c.RewardTitle = title })
			return nil, err
		}
		s.state.NotifyWarn(errs.TwitchForeignAward, fmt.Sprintf(
			"Награда «%s» на канале уже была, но от другого приложения, поэтому создал свою — «%s». Название можно поменять в настройках.",
			title, alt))
	}
	if err != nil {
		return nil, err
	}

	if err := s.db.SetKV(kvRewardID, reward.ID); err != nil {
		s.log.Warn("не запомнил награду", "ошибка", err)
	}
	return reward, nil
}

// rewardRetryable — стоит ли пробовать создать награду ещё раз самим.
//
// Нет баллов, занятое название и выключенная галочка от повтора не
// пройдут — тут нужен человек. Всё прочее (Twitch задумался, сеть моргнула)
// обычно проходит через минуту, и ждать от стримера повторного входа ради
// этого незачем.
func (s *Server) rewardRetryable(err error) bool {
	switch errs.CodeOf(err) {
	case errs.TwitchNoAffiliate, errs.TwitchForeignAward, errs.TwitchAuthExpired, errs.TwitchScopes:
		return false
	}
	return s.cfg.Get().AutoCreateReward
}

// retryReward ещё несколько раз пробует завести награду после неудачи при
// подключении. Получилось — поднимает подписку уже с наградой.
func (s *Server) retryReward(ctx context.Context) {
	for _, wait := range []time.Duration{20 * time.Second, time.Minute, 3 * time.Minute, 10 * time.Minute} {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		s.mu.Lock()
		done := s.rewardID != ""
		s.mu.Unlock()
		if done || !s.twitch.Connected() {
			return
		}
		s.log.Info("пробую ещё раз создать награду")
		s.pushReward()
		s.mu.Lock()
		done = s.rewardID != ""
		s.mu.Unlock()
		if done {
			return
		}
	}
}

// pointsWord — «балл/балла/баллов» по числу: «стоит 1 баллов» резало глаз.
func pointsWord(n int) string { return plural(n, "балл", "балла", "баллов") }
