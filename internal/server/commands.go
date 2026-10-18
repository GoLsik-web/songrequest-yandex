package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/twitch"
)

// Команды в чате — для стримера и модераторов. Обычным зрителям они
// недоступны: заказ идёт только через награду или донат, иначе баллы теряют
// смысл. Права берём из значков сообщения, а не отдельным запросом к API.
//
// ГЛАВНОЕ ПРАВИЛО ЭТОГО ФАЙЛА: на всякую команду, которая дошла до сюда от
// человека с правами, в чат уходит ответ. Раньше половина команд работала
// молча — и «сработало» выглядело ровно как «не сработало». Отсюда и жалоба
// «пишу !скип, ничего не происходит»: команда часто как раз происходила.
//
// Молчим только в одном случае: слово после знака команды нам незнакомо. Там
// вполне может быть чужой бот с тем же знаком, и спорить с ним в чате незачем.

// say пишет в чат, если Twitch подключён. Молчаливый отказ здесь уместен:
// не работает чат — заказы всё равно принимаются.
func (s *Server) say(ctx context.Context, text string) {
	if s.twitch == nil || !s.twitch.Connected() {
		return
	}
	if err := s.twitch.Say(ctx, text); err != nil {
		// С кодом и с самой ошибкой, а не молчаливой строкой в Debug.
		//
		// Жалоба «приложение не отвечает зрителям» иначе неразбираема: в
		// логе строка есть, а причины — нет прав на чат, 401, слоумод,
		// AutoMod — нет. Весь способ работы держится на разборе по логу.
		code, _ := errs.Describe(err)
		s.log.Warn("сообщение в чат не ушло", "текст", text, "код", code, "ошибка", err)
		s.state.NotifyError(err)
	}
}

// dropInvisible выбрасывает знаки, которых не видно.
//
// Зачем. Часть чат-клиентов и ботов дописывает к сообщению «пустой» знак
// (U+200B, U+FEFF, метки направления письма), чтобы Twitch не счёл сообщение
// повтором и не проглотил его. Управляющие знаки вычищает cleanInput в
// internal/twitch, а форматирующие — нет: они не управляющие. До сюда доезжала
// строка вида «!скип» с пустым знаком на конце, не совпадала ни с одной командой, и команда молча
// пропадала. Разобрать такое по жалобе «пишу !скип, ничего не происходит»
// невозможно: в чате обе строки выглядят одинаково.
func dropInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r): // управляющие форматом: U+200B и родня
			return -1
		case r == '\uFEFF': // метка порядка байтов
			return -1
		case r >= 0xE0000 && r <= 0xE007F:
			// Блок «Tags». Именно им чаще всего и дописывают: у Twitch-клиентов
			// это самый ходовой способ обмануть проверку на повтор сообщения. В
			// таблице знаков он числится незанятым, поэтому под unicode.Cf не
			// попадает и до этой правки проходил насквозь.
			return -1
		}
		return r
	}, s)
}

// parseCommand разбирает строку чата на команду и её доводы.
//
// ok=false означает «это не команда»: нет знака в начале или после него пусто.
//
// Что здесь важно:
//
//   - Регистр не имеет значения: !Скип, !СКИП и !SKIP — одно и то же.
//     strings.ToLower умеет и кириллицу, отдельной ветки для неё не нужно.
//   - Пробел после знака допустим: «! скип» — обычная опечатка на телефоне.
//   - Невидимые знаки выбрасываются, см. dropInvisible.
func parseCommand(text, prefix string) (cmd string, args []string, ok bool) {
	if prefix == "" {
		prefix = "!"
	}
	text = strings.TrimSpace(dropInvisible(text))
	if !strings.HasPrefix(text, prefix) {
		return "", nil, false
	}
	fields := strings.Fields(strings.TrimPrefix(text, prefix))
	if len(fields) == 0 {
		return "", nil, false
	}
	return strings.ToLower(fields[0]), fields[1:], true
}

// onChat разбирает сообщение из чата.
func (s *Server) onChat(msg twitch.ChatMessage) {
	cfg := s.cfg.Get()
	prefix := cfg.CommandPrefix
	if prefix == "" {
		prefix = "!"
	}

	cmd, args, ok := parseCommand(msg.Text, prefix)
	if !ok {
		return
	}

	actor := msg.DisplayName
	if actor == "" {
		actor = msg.Login
	}

	ctx, cancel := context.WithTimeout(s.baseContext(), 30*time.Second)
	defer cancel()

	// В чате отказ молчаливый — спорить со зрителем при всех незачем. А вот в
	// логе он обязан быть: жалоба «пишу !скип, ничего не происходит» иначе
	// неразбираема. Без этой строки одинаково выглядят «сообщение не дошло
	// вовсе» и «дошло, но у человека нет прав», а чинить это надо по-разному.
	if !s.canCommand(ctx, msg) {
		s.log.Debug("команда от того, кому нельзя",
			"кто", msg.Login, "команда", cmd,
			"стример", msg.IsBroadcaster, "модератор", msg.IsModerator,
			"значки", msg.Badges)
		return
	}

	s.log.Debug("команда из чата", "кто", actor, "команда", cmd, "аргументы", args)

	switch cmd {
	case "очередь", "queue", "q":
		s.say(ctx, s.queueLine())

	case "трек", "что", "сейчас", "now", "np":
		s.say(ctx, s.nowLine())

	case "скип", "skip", "s":
		s.cmdSkip(ctx, actor, args)

	case "вернуть", "undo", "назад":
		s.cmdUndoSkip(ctx, actor)

	case "плейлисты", "плейлист", "playlists", "pl":
		s.say(ctx, s.playlistsLine())

	case "одобрить", "принять", "approve", "ok":
		s.cmdApprovePlaylist(ctx, actor, args)

	case "отклонить", "отказать", "reject":
		s.cmdRejectPlaylist(ctx, actor, args)

	case "удалить", "remove", "rm":
		s.cmdRemove(ctx, actor, args)

	case "вверх", "поднять", "top":
		s.cmdTop(ctx, actor, args)

	case "громкость", "volume", "vol":
		s.cmdVolume(ctx, actor, args)

	case "пауза", "пауза-трека", "паузатрека", "pause":
		s.cmdHold(ctx, actor, true)

	case "продолжить", "resume", "play":
		s.cmdHold(ctx, actor, false)

	case "очистить", "clear":
		if err := s.clearQueue(ctx, true, actor); err != nil {
			s.log.Warn("не очистил очередь", "ошибка", err)
			s.say(ctx, "@"+actor+", очередь очистить не вышло — посмотри в приложении")
			return
		}
		s.say(ctx, "Очередь очищена, баллы вернулись")

	case "бан", "ban":
		s.cmdBan(ctx, actor, args)

	case "разбан", "unban":
		s.cmdUnban(ctx, actor, args)

	case "стоп", "стопзаказы", "stop":
		s.player.SetPaused(true)
		s.modLog(actor, "остановил приём заказов", "")
		// Прямо говорим, чего эта команда НЕ делает. Именно на этом месте
		// люди спотыкались чаще всего: писали «!стоп», ожидая, что музыка
		// замолчит, и считали приложение сломанным. Пауза музыки — «!пауза».
		s.say(ctx, "Новые заказы больше не принимаются. Музыка играет дальше — "+
			"остановить её это "+prefix+"пауза")

	case "старт", "start":
		s.player.SetPaused(false)
		s.modLog(actor, "возобновил приём заказов", "")
		s.say(ctx, "Заказы снова принимаются")

	default:
		// Знак команды угадан верно, а слово — нет. Молчим в чате (мало ли
		// что там пишут другие боты с тем же знаком), но пишем в лог: так
		// видно опечатки и чужие команды, которые к нам не относятся.
		s.log.Debug("незнакомая команда", "кто", actor, "команда", cmd)

	case "помощь", "help", "команды":
		s.say(ctx, "Команды: "+prefix+"очередь, "+prefix+"трек, "+prefix+
			"скип (можно "+prefix+"скип 2 или "+prefix+"скип название), "+
			prefix+"вернуть, "+prefix+"удалить N, "+prefix+"вверх N, "+
			prefix+"громкость 40, "+prefix+"пауза, "+prefix+"продолжить, "+
			prefix+"плейлисты, "+prefix+"одобрить, "+prefix+"отклонить, "+
			prefix+"очистить, "+prefix+"бан ник, "+prefix+"разбан ник, "+
			prefix+"стоп, "+prefix+"старт")
	}
}

// queueLine — очередь одной строкой для чата.
func (s *Server) queueLine() string {
	items, err := s.queue.List()
	if err != nil {
		return "Не смог прочитать очередь"
	}

	now := s.player.Now()
	if now == nil && len(items) == 0 {
		return "Очередь пуста"
	}

	var b strings.Builder
	if now != nil {
		b.WriteString("Сейчас: " + now.Item.Artist + " — " + now.Item.Title)
	}
	if len(items) == 0 {
		b.WriteString(" · дальше ничего")
		return b.String()
	}

	if now != nil {
		b.WriteString(" · далее: ")
	} else {
		b.WriteString("В очереди: ")
	}

	// В чат влезает немного, поэтому показываем первые несколько и число
	// остальных: длинное сообщение Twitch обрежет посреди слова.
	const show = 5
	for i, it := range items {
		if i >= show {
			b.WriteString(fmt.Sprintf(" … и ещё %d", len(items)-show))
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(i+1) + ". " + it.Artist + " — " + it.Title)
	}
	return b.String()
}

// nowLine — что играет прямо сейчас, одной строкой.
//
// Отдельно от «очереди»: чаще всего спрашивают именно это, а список из шести
// позиций в ответ на «что играет» приходится вычитывать глазами.
func (s *Server) nowLine() string {
	if now := s.player.Now(); now != nil {
		line := "Сейчас: " + now.Item.Artist + " — " + now.Item.Title
		if now.Item.Requester != "" {
			line += " · заказал " + now.Item.Requester
		}
		if now.Paused() {
			line += " · на паузе"
		}
		return line
	}
	if s.player.Waiting() {
		return "Заказ ждёт конца трека канала. Чтобы не ждать — скип"
	}
	if own := s.own(); own != nil {
		return "Сейчас музыка канала: " + own.Artist + " — " + own.Title
	}
	return "Сейчас ничего не играет"
}

// ── скип ─────────────────────────────────────────────────────────────
//
// У скипа три способа сказать, что именно убрать, и все три придумал не я —
// так спрашивают в чате:
//
//	!скип            то, что играет
//	!скип 0          то же самое, для тех, кто считает с нуля
//	!скип 2          второй заказ в очереди (нумерация из «!очередь»)
//	!скип doja cat   любой заказ, в названии или артисте которого это есть
//
// Баллы возвращаются во всех трёх случаях. Это решение владельца, и оно
// снимает самую частую жалобу зрителей: заказ оборвали, а баллы списаны.

func (s *Server) cmdSkip(ctx context.Context, actor string, args []string) {
	arg := strings.TrimSpace(strings.Join(args, " "))

	// Без доводов и «0» — то, что в эфире.
	if arg == "" || arg == "0" {
		s.skipOnAir(ctx, actor)
		return
	}

	// Число — позиция в очереди из «!очередь».
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 0 {
			s.say(ctx, "@"+actor+", номер не бывает отрицательным. 0 — то, что играет, 1 — следующий")
			return
		}
		s.skipAt(ctx, actor, n)
		return
	}

	// Всё остальное — название. Ищем и в эфире, и в очереди.
	s.skipNamed(ctx, actor, arg)
}

// skipOnAir убирает с эфира то, что играет.
func (s *Server) skipOnAir(ctx context.Context, actor string) {
	// Своя музыка канала — не заказ, и «скипать» её приложению нечего:
	// очередь тут ни при чём, а плейлистом стример правит сам. Молчать об
	// этом нельзя, иначе команда выглядит сломанной.
	if s.player.Now() == nil && !s.player.Waiting() {
		if s.own() != nil {
			s.say(ctx, "@"+actor+", сейчас играет музыка канала, а не заказ — скипать нечего")
			return
		}
		s.say(ctx, "@"+actor+", сейчас ничего не играет")
		return
	}

	res, err := s.skipPlaying(actor)
	if err != nil {
		s.say(ctx, "@"+actor+", "+errText(err))
		return
	}
	if res.Hurried {
		s.say(ctx, "Не жду конца трека канала, включаю заказ")
		return
	}
	s.say(ctx, "Скипнул: "+trackLine(res.Item)+refundTail(res.Refunded))
}

// skipAt убирает заказ по номеру из «!очередь».
func (s *Server) skipAt(ctx context.Context, actor string, n int) {
	if n == 0 {
		s.skipOnAir(ctx, actor)
		return
	}

	items, err := s.queue.List()
	if err != nil {
		s.say(ctx, "@"+actor+", не смог прочитать очередь")
		return
	}
	if n > len(items) {
		s.say(ctx, "@"+actor+", в очереди столько нет: "+queueSize(len(items)))
		return
	}

	s.takeOutOfQueue(ctx, actor, items[n-1])
}

// skipNamed ищет заказ по названию — и в эфире, и в очереди.
func (s *Server) skipNamed(ctx context.Context, actor, name string) {
	if now := s.player.Now(); now != nil && trackMatches(now.Item, name) {
		s.skipOnAir(ctx, actor)
		return
	}

	items, err := s.queue.List()
	if err != nil {
		s.say(ctx, "@"+actor+", не смог прочитать очередь")
		return
	}

	found := -1
	for i, it := range items {
		if !trackMatches(it, name) {
			continue
		}
		if found >= 0 {
			// Двух одинаково подходящих трогать нельзя: угадаешь не тот —
			// и зритель лишится заказа ни за что. Пусть человек назовёт номер.
			s.say(ctx, "@"+actor+", под «"+name+"» подходит больше одного заказа — скажи номер из "+
				s.prefix()+"очередь")
			return
		}
		found = i
	}

	if found < 0 {
		s.say(ctx, "@"+actor+", не нашёл «"+name+"» ни в эфире, ни в очереди")
		return
	}
	s.takeOutOfQueue(ctx, actor, items[found])
}

// takeOutOfQueue убирает заказ из очереди с возвратом баллов и говорит об этом.
//
// Имя не dropFromQueue: так уже называется соседняя работа в twitch.go —
// «зритель сам отменил награду, убери его заказ». Две разные вещи с одним
// именем однажды перепутались бы.
func (s *Server) takeOutOfQueue(ctx context.Context, actor string, item queue.Item) {
	s.rememberSkip(item, actor)
	if err := s.removeFromQueue(ctx, item.ID, true, actor); err != nil {
		s.say(ctx, "@"+actor+", "+errText(err))
		return
	}
	s.say(ctx, "Убрал из очереди: "+trackLine(item)+refundTail(item.RedemptionID != ""))
}

// cmdUndoSkip возвращает в очередь то, что скипнули последним.
//
// Баллы при этом обратно НЕ списываются, и это не недосмотр: Twitch умеет
// вернуть баллы за награду, но забрать их снова у него нечем — отменённый
// заказ отменён навсегда. Поэтому возвращённый трек играет бесплатно, и в
// чате об этом сказано прямо, чтобы зритель не решил, что с него взяли дважды.
func (s *Server) cmdUndoSkip(ctx context.Context, actor string) {
	item, who, err := s.takeSkipBack()
	if err != nil {
		s.say(ctx, "@"+actor+", "+errText(err))
		return
	}

	// Новый заказ, а не оживший старый: у прежнего баллы уже возвращены, и
	// тянуть за собой его номер награды нельзя — иначе приложение однажды
	// попробует вернуть их второй раз.
	back := queue.Item{
		Source:         queue.SourceManual,
		Requester:      item.Requester,
		RequesterLogin: item.RequesterLogin,
		RawRequest:     item.RawRequest,
		Provider:       item.Provider,
		Via:            item.Via,
		TrackID:        item.TrackID,
		URI:            item.URI,
		Title:          item.Title,
		Artist:         item.Artist,
		DurationMs:     item.DurationMs,
		CoverURL:       item.CoverURL,
		Uncertain:      item.Uncertain,
	}

	added, err := s.queue.Add(back, false)
	if err != nil {
		s.log.Warn("не вернул скипнутый заказ", "ошибка", err)
		s.say(ctx, "@"+actor+", не получилось вернуть заказ в очередь")
		return
	}
	if err := s.queue.MoveTop(added.ID); err != nil {
		// Не беда: заказ в очереди, просто не первым. Говорить об этом
		// отдельно незачем — позицию человек увидит в «!очередь».
		s.log.Warn("вернул заказ, но не поднял в начало", "ошибка", err)
	}

	s.modLog(actor, "вернул скипнутое", trackLine(item))
	s.state.Notify("info", actor+" вернул в очередь: "+trackLine(item))
	s.syncPlayback()
	s.player.Nudge()

	tail := ""
	if item.RedemptionID != "" {
		tail = " Баллы уже вернулись, так что этот раз бесплатно."
	}
	s.say(ctx, "Вернул в начало очереди: "+trackLine(item)+" (скипнул "+who+")."+tail)
}

// cmdTop поднимает заказ в начало очереди.
func (s *Server) cmdTop(ctx context.Context, actor string, args []string) {
	if len(args) == 0 {
		s.say(ctx, "@"+actor+", нужен номер из "+s.prefix()+"очередь: вверх 3")
		return
	}

	items, err := s.queue.List()
	if err != nil {
		s.say(ctx, "@"+actor+", не смог прочитать очередь")
		return
	}

	item, ok := s.pickFromQueue(ctx, actor, items, strings.Join(args, " "))
	if !ok {
		return
	}
	if err := s.queue.MoveTop(item.ID); err != nil {
		s.say(ctx, "@"+actor+", не получилось поднять заказ")
		return
	}

	s.modLog(actor, "поднял заказ в начало", trackLine(item))
	s.state.Notify("info", actor+" поднял в начало: "+trackLine(item))
	s.syncPlayback()
	s.say(ctx, "Поднял в начало очереди: "+trackLine(item))
}

// pickFromQueue выбирает заказ по номеру или названию и сам объясняет отказ.
func (s *Server) pickFromQueue(ctx context.Context, actor string, items []queue.Item, arg string) (queue.Item, bool) {
	arg = strings.TrimSpace(arg)
	if len(items) == 0 {
		s.say(ctx, "@"+actor+", очередь пуста")
		return queue.Item{}, false
	}

	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(items) {
			s.say(ctx, "@"+actor+", в очереди столько нет: "+queueSize(len(items)))
			return queue.Item{}, false
		}
		return items[n-1], true
	}

	found := -1
	for i, it := range items {
		if !trackMatches(it, arg) {
			continue
		}
		if found >= 0 {
			s.say(ctx, "@"+actor+", под «"+arg+"» подходит больше одного заказа — скажи номер")
			return queue.Item{}, false
		}
		found = i
	}
	if found < 0 {
		s.say(ctx, "@"+actor+", не нашёл «"+arg+"» в очереди")
		return queue.Item{}, false
	}
	return items[found], true
}

// cmdVolume меняет громкость играющего заказа прямо из чата.
//
// Только заказа: своей музыкой стример правит в самом Spotify, а каждая
// команда громкости — отдельный запрос к нему.
func (s *Server) cmdVolume(ctx context.Context, actor string, args []string) {
	if len(args) == 0 {
		s.say(ctx, "@"+actor+", нужно число от 0 до 100: громкость 40")
		return
	}
	percent, err := strconv.Atoi(strings.TrimSuffix(args[0], "%"))
	if err != nil || percent < 0 || percent > 100 {
		s.say(ctx, "@"+actor+", громкость — это число от 0 до 100")
		return
	}

	if s.player.Now() == nil {
		s.say(ctx, "@"+actor+", сейчас не играет ни один заказ")
		return
	}
	if err := s.player.SetVolumeLevel(ctx, percent); err != nil {
		s.say(ctx, "@"+actor+", "+errText(err))
		return
	}

	s.modLog(actor, "поставил громкость", strconv.Itoa(percent)+"%")
	s.syncPlayback()
	s.say(ctx, "Громкость заказа: "+strconv.Itoa(percent)+"%")
}

// cmdHold ставит на паузу то, что играет, и снимает с неё.
//
// Работает и для заказа, и для музыки канала — развилка живёт в приложении, а
// не в голове у модератора: он видит одну плашку «сейчас играет» и хочет её
// остановить.
func (s *Server) cmdHold(ctx context.Context, actor string, on bool) {
	word := "Продолжаю"
	deed := "снял с паузы"
	if on {
		word, deed = "Пауза", "поставил на паузу"
	}

	if now := s.player.Now(); now != nil {
		if err := s.player.Hold(ctx, on); err != nil {
			s.say(ctx, "@"+actor+", "+errText(err))
			return
		}
		s.modLog(actor, deed, trackLine(now.Item))
		s.syncPlayback()
		s.say(ctx, word+": "+trackLine(now.Item))
		return
	}

	own := s.own()
	if own == nil {
		s.say(ctx, "@"+actor+", сейчас ничего не играет")
		return
	}
	if err := s.holdOwn(ctx, on); err != nil {
		s.say(ctx, "@"+actor+", "+errText(err))
		return
	}
	s.modLog(actor, deed+" музыку канала", own.Artist+" — "+own.Title)
	s.say(ctx, word+": "+own.Artist+" — "+own.Title)
}

func (s *Server) cmdRemove(ctx context.Context, actor string, args []string) {
	if len(args) == 0 {
		s.say(ctx, "@"+actor+", нужен номер: удалить 2")
		return
	}

	items, err := s.queue.List()
	if err != nil {
		s.say(ctx, "@"+actor+", не смог прочитать очередь")
		return
	}
	item, ok := s.pickFromQueue(ctx, actor, items, strings.Join(args, " "))
	if !ok {
		return
	}

	s.rememberSkip(item, actor)
	if err := s.removeFromQueue(ctx, item.ID, true, actor); err != nil {
		s.say(ctx, "@"+actor+", "+errText(err))
		return
	}
	s.say(ctx, "Удалил: "+trackLine(item)+refundTail(item.RedemptionID != ""))
}

func (s *Server) cmdBan(ctx context.Context, actor string, args []string) {
	if len(args) == 0 {
		s.say(ctx, "@"+actor+", нужен ник: бан ник причина")
		return
	}
	login := strings.TrimPrefix(strings.ToLower(args[0]), "@")
	reason := strings.Join(args[1:], " ")

	if err := s.banMusic(login, reason); err != nil {
		s.log.Warn("не забанил", "ошибка", err)
		s.say(ctx, "@"+actor+", не получилось закрыть заказы для "+login)
		return
	}
	s.modLog(actor, "закрыл заказы", login)
	s.state.Notify("info", actor+" закрыл заказы для "+login)
	s.say(ctx, login+" больше не может заказывать музыку")
}

func (s *Server) cmdUnban(ctx context.Context, actor string, args []string) {
	if len(args) == 0 {
		s.say(ctx, "@"+actor+", нужен ник: разбан ник")
		return
	}
	login := strings.TrimPrefix(strings.ToLower(args[0]), "@")

	if err := s.unbanMusic(login); err != nil {
		s.log.Warn("не разбанил", "ошибка", err)
		s.say(ctx, "@"+actor+", не получилось вернуть заказы для "+login)
		return
	}
	s.modLog(actor, "вернул заказы", login)
	s.state.Notify("info", actor+" вернул заказы для "+login)
	s.say(ctx, login+" снова может заказывать музыку")
}

// announceQueued говорит зрителю, что его заказ принят и когда заиграет.
func (s *Server) announceQueued(ctx context.Context, item queue.Item, position int, wait time.Duration) {
	where := "следующим"
	if position > 1 {
		where = fmt.Sprintf("%d-м в очереди", position)
	}

	text := fmt.Sprintf("@%s, принято: %s — %s, %s", item.Requester, item.Artist, item.Title, where)
	if wait > time.Minute {
		text += fmt.Sprintf(" (примерно через %s)", humanDuration(wait))
	}
	// Зритель должен знать, что звук пойдёт не из Spotify: иначе «почему
	// играет какая-то запись с ютуба» — вопрос в чат через три минуты.
	if item.Provider == "youtube" {
		text += " — в Яндексе нет, сыграет с YouTube"
	}
	s.say(ctx, text)
}

// ── мелочи, из которых собираются ответы ─────────────────────────────

// prefix — знак команд прямо сейчас. Подставляется в подсказки: в чате нельзя
// написать «!очередь», если у стримера стоит другой знак.
func (s *Server) prefix() string {
	if p := s.cfg.Get().CommandPrefix; p != "" {
		return p
	}
	return "!"
}

func trackLine(item queue.Item) string {
	if item.Artist == "" {
		return item.Title
	}
	return item.Artist + " — " + item.Title
}

// refundTail — хвост про баллы.
//
// Говорим об этом в том же сообщении, а не отдельным: два сообщения подряд от
// бота в чате читаются хуже одного, а зритель должен увидеть «баллы вернулись»
// в ту же секунду, что и «твой трек убрали».
func refundTail(refunded bool) string {
	if refunded {
		return ". Баллы вернул"
	}
	return ""
}

func queueSize(n int) string {
	if n == 0 {
		return "очередь пуста"
	}
	return strconv.Itoa(n) + " " + plural(n, "заказ", "заказа", "заказов")
}

// errText — текст отказа для чата, без кода.
//
// Код (PL-01, SP-08) нужен в панели и в логе: по нему стример находит, что
// делать. В чате он только пугает зрителей, а модератору всё равно не поможет.
func errText(err error) string {
	_, text := errs.Describe(err)
	if text == "" {
		return "не получилось"
	}
	// С маленькой буквы: текст встаёт после «@ник, ».
	r := []rune(text)
	r[0] = unicode.ToLower(r[0])
	return strings.TrimSuffix(string(r), ".")
}

// trackMatches — подходит ли заказ под то, что написали в чате.
//
// Сравниваем по-человечески, а не побуквенно: match.Normalize приводит регистр,
// ё/е, дефисы и лишние пробелы к одному виду — тот же разбор, которым
// приложение ищет треки по заказу зрителя. Иначе «!скип Ария» не находило бы
// «АРИЯ», а «!скип doja» — «Doja Cat».
//
// Достаточно вхождения куска: в чате пишут «!скип бэд гай», а не полное
// название с артистом. Ложное попадание не страшно — на два подходящих
// приложение отвечает «скажи номер» и ничего не трогает.
func trackMatches(item queue.Item, want string) bool {
	want = match.Normalize(want)
	if want == "" {
		return false
	}
	hay := match.Normalize(item.Artist + " " + item.Title + " " + item.RawRequest)
	return strings.Contains(hay, want)
}

// ── плейлисты, ждущие решения ────────────────────────────────────────
//
// Заказанный плейлист сам в эфир не идёт: сорок минут чужой музыки подряд
// решает хозяин эфира. Пока решения нет, плейлист виден в панели и здесь, в
// чате, — чтобы модератор мог одобрить его, не подходя к компьютеру стримера.

// playlistsLine — что ждёт решения, одной строкой.
func (s *Server) playlistsLine() string {
	list := s.pendingPlaylists()
	if len(list) == 0 {
		return "Сейчас ни один плейлист не ждёт решения"
	}

	var b strings.Builder
	b.WriteString("Ждут решения: ")
	for i, p := range list {
		if i > 0 {
			b.WriteString(" · ")
		}
		b.WriteString(fmt.Sprintf("%s. «%s» от %s — %d %s, %s",
			p.ID, p.Title, p.Requester,
			len(p.Tracks), plural(len(p.Tracks), "трек", "трека", "треков"),
			humanDuration(time.Duration(p.TotalMs())*time.Millisecond)))
	}
	b.WriteString(". " + s.prefix() + "одобрить N или " + s.prefix() + "отклонить N")
	return b.String()
}

func (s *Server) cmdApprovePlaylist(ctx context.Context, actor string, args []string) {
	p, added, err := s.approvePlaylist(ctx, strings.Join(args, " "), actor)
	if err != nil {
		s.say(ctx, "@"+actor+", "+errText(err))
		return
	}

	// Сколько встало в очередь, а не сколько было в плейлисте: часть треков
	// могла не найтись нигде или оказаться длиннее разрешённого, и обещать
	// зрителю больше, чем сыграет, нельзя.
	tail := ""
	if added < len(p.Tracks) {
		tail = fmt.Sprintf(" (из %d — остальные не нашлись или слишком длинные)", len(p.Tracks))
	}
	s.say(ctx, fmt.Sprintf("Плейлист «%s» от %s одобрен: %d %s в очереди%s",
		p.Title, p.Requester, added, plural(added, "трек", "трека", "треков"), tail))
}

func (s *Server) cmdRejectPlaylist(ctx context.Context, actor string, args []string) {
	p, err := s.rejectPlaylist(ctx, strings.Join(args, " "), actor)
	if err != nil {
		s.say(ctx, "@"+actor+", "+errText(err))
		return
	}
	s.say(ctx, fmt.Sprintf("@%s, плейлист «%s» не одобрили. Баллы вернул.", p.Requester, p.Title))
}

// canCommand решает, слушать ли команду от этого человека.
//
// Два пути, и порядок важен.
//
// Первый — значки сообщения. Они приходят вместе с текстом, бесплатно, и в
// девяноста девяти случаях из ста вопрос закрывают.
//
// Второй — список модераторов у самого Twitch. Он нужен потому, что значков
// может не оказаться и у настоящего модератора: 09.09 владелец написал «!скип»
// с аккаунта, который на канале модератор, и получил отказ со строкой
// «стример=false модератор=false». Значки в чате — украшение, а не документ;
// кто модератор, знает Twitch, и спрашивать надо его. Запрос идёт только тогда,
// когда значков не хватило, и не чаще раза в минуту, см. Client.IsModerator.
func (s *Server) canCommand(ctx context.Context, msg twitch.ChatMessage) bool {
	if msg.CanCommand() {
		return true
	}
	if s.twitch == nil || !s.twitch.Connected() {
		return false
	}
	if !s.twitch.IsModerator(ctx, msg.Login) {
		return false
	}
	// Раз уж выяснили — скажем об этом в лог: иначе разбор следующей жалобы
	// снова упрётся в вопрос «а по чему приложение решило, что можно».
	s.log.Info("команда принята по списку модераторов канала, значков в сообщении не было",
		"кто", msg.Login, "значки", msg.Badges)
	return true
}
