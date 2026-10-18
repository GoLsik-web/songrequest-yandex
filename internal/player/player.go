// Package player проигрывает очередь заказов и возвращает Spotify на место.
//
// Опроса здесь нет. Длительность трека известна заранее, поэтому приложение
// просто спит до его конца и один раз проверяет, что трек действительно
// доиграл. На заказ уходит два запроса к Spotify вместо сотни, а в простое
// горутина спит на канале и не тратит процессор.
package player

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/yandex"
)

// YouTube — запасной проигрыватель для того, чего нет в Spotify.
// Интерфейсом, чтобы плеер не зависел от yt-dlp и mpv в тестах.
type YouTube interface {
	Play(ctx context.Context, url string) error
	Wait(ctx context.Context) bool
	Stop()
	// SetBase сообщает громкость Spotify, от которой считается громкость
	// заказа. Без неё mpv играет на полную и оглушает эфир.
	SetBase(volume int)
	// Управление на ходу. Всё это бесплатно: канал управления mpv местный,
	// никакой сети и никаких норм запросов.
	SetPaused(on bool)
	Seek(seconds float64)
	SetVolumePercent(percent int)
}

// Spotify — то, что умеет играть. Интерфейсом ради тестов.
type Music interface {
	Capture(ctx context.Context) (*yandex.Snapshot, error)
	Restore(ctx context.Context, snap *yandex.Snapshot, playedURI string, force bool) (yandex.RestoreOutcome, error)
	PlayTrack(ctx context.Context, trackURI, deviceID string) error
	Pause(ctx context.Context, deviceID string) error
	State(ctx context.Context) (*yandex.PlayerState, bool, error)
	// Resume снимает с паузы, не начиная трек заново.
	Resume(ctx context.Context, deviceID string) error
	Seek(ctx context.Context, positionMs int, deviceID string) error
	SetVolume(ctx context.Context, percent int, deviceID string) error
}

// Now — что играет прямо сейчас.
type Now struct {
	Item      queue.Item
	StartedAt time.Time

	// HeldFrom — с какого момента заказ стоит на паузе. Ноль означает «играет».
	HeldFrom time.Time
	// Held — сколько всего простояли на паузе за этот трек. Без этого счёта
	// полоса в панели продолжала бы ехать на паузе: она считается от момента
	// запуска, а не спрашивается у Spotify.
	Held time.Duration
}

// Elapsed — сколько уже отыграно, без учёта пауз.
func (n *Now) Elapsed() time.Duration {
	if n == nil {
		return 0
	}
	end := time.Now()
	if !n.HeldFrom.IsZero() {
		end = n.HeldFrom
	}
	played := end.Sub(n.StartedAt) - n.Held
	if played < 0 {
		return 0
	}
	return played
}

// Paused — стоит ли заказ на паузе прямо сейчас.
func (n *Now) Paused() bool { return n != nil && !n.HeldFrom.IsZero() }

// Player проигрывает очередь.
type Player struct {
	q       *queue.Queue
	music   Music
	youtube YouTube
	log     *logx.Logger

	// pollEvery — шаг проверки Spotify, пока играет заказ.
	//
	// Атомарный, потому что меняется на ходу: пока панель открыта, стример
	// может перемотать трек в самом Spotify, и полоса в панели обязана
	// поехать следом сразу, а не через шесть секунд. Как только панель
	// закрыли, шаг возвращается к экономному.
	pollEvery atomic.Int64

	// WaitForCurrent — дожидаться конца трека, который играет у стримера,
	// прежде чем включить первый заказ.
	WaitForCurrent bool

	// Настройки ниже читает горутина очереди, а меняет их панель. Только
	// через SetOptions: голое присваивание — гонка.

	// OnDropped — заказ не удалось включить: баллы надо вернуть, а зрителю
	// сказать. Без этого списанные баллы просто пропадают.
	OnDropped func(item queue.Item, err error)

	// ResumeDelay — пауза перед возвратом. Заказы часто идут подряд, и
	// возвращать музыку между ними, чтобы через секунду снова прервать, —
	// худшее, что можно сделать со звуком на стриме.
	ResumeDelay time.Duration

	// OnWaiting — заказ не заиграл сразу, а ждёт конца трека стримера.
	// Второй аргумент — сколько ждать. Без этого ожидание выглядит как
	// зависание: заказ принят, а музыка не меняется несколько минут.
	OnWaiting func(item queue.Item, left time.Duration)

	// Обратные вызовы наружу: пакет не знает ни про панель, ни про Twitch.
	OnChange func()
	// OnFinished — заказ закончился. natural=false означает «оборвали»:
	// скипнули из панели или переключили музыку прямо в Spotify. Отличать
	// это обязательно: оборванный заказ — не «отыгравший».
	OnFinished func(item queue.Item, natural bool)
	OnRestored func(outcome yandex.RestoreOutcome, snap *yandex.Snapshot)
	OnError    func(err error)

	// rescue — что играть, если заказ не заиграл. См. SetRescue.
	rescue Rescue

	// panel — панель Windows; через неё await узнаёт, играет ли заказ, не
	// тратя запросов к Spotify. См. panel.go.
	panel Panel
	// panelMiss — сколько раз подряд панель не узнала играющий заказ.
	panelMiss int

	// volumeTouched — стример двигал громкость Spotify из панели. Тогда после
	// очереди её надо вернуть на место вместе со всем остальным: иначе заказ
	// навсегда меняет громкость самому стримеру, а он об этом даже не узнает —
	// заметит через полчаса, что музыка тише обычного.
	volumeTouched bool

	// volume — на чём стоит ползунок громкости. Панель показывает его по этому
	// числу, а не спрашивает Spotify: спрашивать пришлось бы на каждую
	// перерисовку, а это запрос ни за чем.
	volume int

	mu  sync.Mutex
	now *Now
	// lastPlayed — URI последнего отыгравшего заказа. Нужен возврату,
	// чтобы отличить «в Spotify наш заказ» от «стример переключил сам».
	lastPlayed string
	// endedNaturally — заказ доиграл сам, а не был оборван. Это разрешение
	// возврату работать без оглядки на то, что сейчас в Spotify: см. restore.
	endedNaturally bool
	// restoreTries — сколько раз возврат подряд сорвался ошибкой. Пока
	// попытки не кончились, снимок держим: место возврата — единственное,
	// что у нас есть, и терять его из-за моргнувшей сети нельзя.
	restoreTries int
	snap         *yandex.Snapshot
	paused       bool
	// waiting — заказ взят, но ждёт конца трека стримера.
	waiting bool
	// waitingItem — что именно ждёт. Панель показывает его в очереди, иначе
	// заказ исчезает с экрана на всё время ожидания.
	waitingItem *queue.Item
	wake        chan struct{}
	skip        chan struct{}
	running     bool
}

// SetYouTube подключает запасной проигрыватель.
func (p *Player) SetYouTube(y YouTube) {
	p.mu.Lock()
	p.youtube = y
	p.mu.Unlock()
}

// Rescue — где взять запасной способ сыграть заказ, который не заиграл.
//
// Отдаёт тот же заказ с другим источником или false, если играть больше нечем.
// Сам плеер про источники ничего не знает и знать не должен: искать треки —
// дело сервера, здесь только «включи и дождись».
type Rescue func(ctx context.Context, item queue.Item) (queue.Item, bool)

// SetRescue подключает запасной путь. Без него заказ, который не заиграл,
// просто пропадает с возвратом баллов — как было раньше.
func (p *Player) SetRescue(f Rescue) {
	p.mu.Lock()
	p.rescue = f
	p.mu.Unlock()
}

// New создаёт плеер.
func New(q *queue.Queue, sp Music, log *logx.Logger) *Player {
	p := &Player{
		q:           q,
		music:       sp,
		log:         log,
		ResumeDelay: time.Second,
		wake:        make(chan struct{}, 1),
		skip:        make(chan struct{}, 1),
	}
	p.SetPollEvery(defaultPoll)
	return p
}

// Now отдаёт текущий заказ.
//
// Копией, а не указателем: снаружи Elapsed() читает StartedAt, а перемотка
// двигает это же поле из горутины очереди. Отдавать живую структуру значит
// отдавать гонку.
func (p *Player) Now() *Now {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.now == nil {
		return nil
	}
	n := *p.now
	return &n
}

// Waiting сообщает, что заказ уже взят в работу, но ждёт конца трека
// стримера. Для панели и для чата это «сейчас играет» в смысле «есть что
// торопить скипом».
// WaitingItem — заказ, который уже вынут из очереди, но ещё не заиграл:
// ждёт, пока доиграет трек стримера.
//
// Нужен панели. Без него такой заказ не виден нигде: из очереди он уже взят,
// а «В эфире» ещё пусто. 27.08 владелец кинул ссылку, приложение всё нашло
// правильно — и полминуты на экране не было ничего, будто заказ потерялся.
func (p *Player) WaitingItem() *queue.Item {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitingItem == nil {
		return nil
	}
	item := *p.waitingItem
	return &item
}

func (p *Player) Waiting() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waiting
}

// setWaitingItem запоминает, чего именно мы ждём.
func (p *Player) setWaitingItem(item *queue.Item) {
	p.mu.Lock()
	p.waitingItem = item
	p.mu.Unlock()
}

func (p *Player) setWaiting(v bool) {
	p.mu.Lock()
	p.waiting = v
	p.mu.Unlock()
}

// Snapshot отдаёт запомненное состояние Spotify.
func (p *Player) Snapshot() *yandex.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snap
}

// SetSnapshot подменяет снимок — например, когда стример нажал «запомнить».
func (p *Player) SetSnapshot(s *yandex.Snapshot) {
	p.mu.Lock()
	p.snap = s
	p.mu.Unlock()
	p.changed()
}

// Nudge будит плеер: в очередь что-то добавили.
func (p *Player) Nudge() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Skip обрывает текущий трек.
func (p *Player) Skip() {
	select {
	case p.skip <- struct{}{}:
	default:
	}
}

// SetPaused ставит воспроизведение заказов на паузу. Уже играющий трек
// доигрывает: обрывать его на полуслове — не то, что имеют в виду.
func (p *Player) SetPaused(v bool) {
	p.mu.Lock()
	p.paused = v
	p.mu.Unlock()
	if !v {
		p.Nudge()
	}
	p.changed()
}

// Paused сообщает, стоит ли воспроизведение на паузе.
func (p *Player) Paused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// Run крутит очередь до отмены контекста.
func (p *Player) Run(ctx context.Context) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	for ctx.Err() == nil {
		item, err := p.take()
		if err != nil {
			// Очередь пуста — ждём, пока что-нибудь появится.
			p.idle(ctx)
			continue
		}
		p.playOne(ctx, item)
	}
}

// take забирает следующий заказ, если можно играть.
func (p *Player) take() (queue.Item, error) {
	if p.Paused() {
		return queue.Item{}, queue.ErrEmpty
	}
	return p.q.Next()
}

// idle ждёт нового заказа и по дороге возвращает Spotify на место.
func (p *Player) idle(ctx context.Context) {
	// Возвращаем, только когда очередь действительно пуста, и с задержкой:
	// следующий заказ может прийти через секунду.
	if p.Snapshot() != nil && p.Now() == nil {
		delay, _ := p.options()
		if !sleep(ctx, delay) {
			return
		}
		if n, _ := p.q.Len(); n == 0 && !p.Paused() {
			p.restore(ctx)
		}
	}

	select {
	case <-ctx.Done():
	case <-p.wake:
	case <-p.skip: // скип на пустой очереди — просто будим
	case <-time.After(30 * time.Second):
		// Редкая подстраховка на случай, если сигнал потерялся: раз в
		// полминуты заглядываем в очередь сами.
	}
}

// playOne проигрывает один заказ от начала до конца.
func (p *Player) playOne(ctx context.Context, item queue.Item) {
	// Скип, нажатый между заказами — пока шёл запуск, снимок или возврат, —
	// оставался лежать в канале и обрывал следующий заказ в первую же
	// секунду. Со стороны выглядело как «заказ пропал сам».
	p.drainSkip()

	// Первый заказ подряд не должен обрывать то, что стример слушает: ждём,
	// пока трек доиграет сам. Снимок делается уже после ожидания — тогда
	// Spotify успевает перевести стрелку на следующий трек плейлиста, и
	// возврат приведёт туда, где музыка и была бы без заказа.
	if p.Snapshot() == nil {
		// Пока идёт ожидание, наружу надо показывать, что заказ уже в работе.
		// Без этого «Скип» в панели и !скип в чате молчали: оба начинаются с
		// проверки Now()!=nil, а во время ожидания Now() ещё пуст. Стример
		// видел «заиграет примерно через три минуты» и не мог это ускорить
		// ничем — при том что сам скип ожидание прерывать умеет.
		p.setWaiting(true)
		p.setWaitingItem(&item)
		p.waitForOwnTrack(ctx, item)
		p.setWaitingItem(nil)
		p.setWaiting(false)
		if ctx.Err() != nil {
			return
		}
	}

	// Первый заказ подряд — запоминаем, куда возвращаться.
	if p.Snapshot() == nil {
		snap, err := p.music.Capture(ctx)
		if err != nil {
			p.fail(err)
			// Без снимка играть можно, но вернуться потом будет некуда —
			// об этом уже сказано в панели.
		} else {
			p.SetSnapshot(p.notOurOwn(snap))
		}
	}

	// Дальше — запуск и ожидание, с одной попыткой спастись.
	//
	// Раньше это были две прямые ветки: не включилось — заказ пропал, упал mpv
	// через полсекунды — заказ считался оборванным. И в том, и в другом случае
	// баллы списаны, зритель ничего не услышал, а тот же трек прекрасно нашёлся
	// бы другим источником. Теперь неудача — повод спросить сервер, чем ещё это
	// можно сыграть (см. Server.rescue), и попробовать ещё раз.
	for attempt := 0; ; attempt++ {
		if err := p.launch(ctx, item); err != nil {
			p.log.Error("не смог включить заказ",
				"трек", item.Artist+" — "+item.Title, "откуда", item.Provider, "ошибка", err)
			next, ok := p.callRescue(ctx, item, attempt)
			if !ok {
				p.fail(err)
				p.dropped(item, err)
				return
			}
			item = next
			continue
		}

		p.mu.Lock()
		p.now = &Now{Item: item, StartedAt: time.Now()}
		p.mu.Unlock()
		p.changed()

		p.log.Info("играет заказ",
			"трек", item.Artist+" — "+item.Title, "заказал", item.Requester,
			"откуда", item.Provider, "длительность_мс", item.DurationMs)

		natural, fell := p.hold(ctx, item)

		p.mu.Lock()
		p.now = nil
		p.mu.Unlock()

		// Проигрыватель умер посреди трека. Это не «доиграл» и не «скипнули»:
		// зритель ничего не услышал, а баллы уже списаны.
		if fell {
			if next, ok := p.callRescue(ctx, item, attempt); ok {
				p.changed()
				item = next
				continue
			}
		}

		p.mu.Lock()
		// Нужен возврату: он должен отличать «в Spotify наш отыгравший заказ»
		// от «стример переключил музыку сам».
		p.lastPlayed = item.URI
		p.endedNaturally = natural
		p.mu.Unlock()
		p.changed()

		p.finish(item, natural)
		return
	}
}

// launch включает заказ там, откуда он взят.
func (p *Player) launch(ctx context.Context, item queue.Item) error {
	// Заказ с YouTube играется иначе: Spotify ставится на паузу, звук идёт
	// отдельной программой на отдельное устройство.
	if item.Provider == "youtube" {
		return p.playYouTube(ctx, item)
	}
	return p.music.PlayTrack(ctx, item.URI, p.playDevice())
}

// hold ждёт конца заказа. natural — доиграл сам; fell — проигрыватель умер, не
// доиграв, и заказ стоит попробовать сыграть иначе.
func (p *Player) hold(ctx context.Context, item queue.Item) (natural, fell bool) {
	if item.Provider == "youtube" {
		return p.awaitYouTube(ctx, item)
	}
	// У Spotify «упал посреди трека» отдельно не ловится: обрыв там выглядит
	// так же, как «стример переключил музыку сам», а обрывать за стримером его
	// же выбор запасным источником — худшее, что можно сделать.
	return p.await(ctx, item), false
}

// callRescue спрашивает сервер, чем ещё можно сыграть этот заказ.
//
// Одна попытка на заказ. Если и запасной источник молчит, дело не в источнике,
// а очередь обязана двигаться дальше — иначе один неудачный заказ встанет
// поперёк всего вечера.
func (p *Player) callRescue(ctx context.Context, item queue.Item, attempt int) (queue.Item, bool) {
	if attempt > 0 || ctx.Err() != nil {
		return queue.Item{}, false
	}
	p.mu.Lock()
	f := p.rescue
	p.mu.Unlock()
	if f == nil {
		return queue.Item{}, false
	}
	return f(ctx, item)
}

// playDevice — где играть заказ.
//
// Это то же устройство, на котором играла музыка стримера в момент снимка:
// именно оттуда идёт звук в эфир. Без явного указания Spotify играет «где
// активно сейчас», а после доигравшего заказа активным не остаётся ничего —
// и заказ либо не включался вовсе, либо мог уехать на телефон, который у
// стримера тоже залогинен.
//
// Пустая строка означает «решай сам»: снимка ещё нет, значит и музыки не
// было, и выбирать не из чего.
func (p *Player) playDevice() string {
	snap := p.Snapshot()
	if snap == nil {
		return ""
	}
	return snap.DeviceID
}

// playYouTube ставит Spotify на паузу и запускает звук с YouTube.
func (p *Player) playYouTube(ctx context.Context, item queue.Item) error {
	p.mu.Lock()
	yt := p.youtube
	p.mu.Unlock()

	if yt == nil {
		return errs.New(errs.YouTubeNoTool, "Проигрыватель YouTube не готов.")
	}

	// Spotify обязательно на паузу: иначе два трека заиграют одновременно.
	if err := p.music.Pause(ctx, ""); err != nil {
		p.log.Warn("не поставил Spotify на паузу перед заказом с YouTube", "ошибка", err)
	}

	// Громкость берём из снимка: заказ должен звучать так же, как звучала
	// музыка стримера секунду назад. Снимка нет — mpv посчитает от ста, и
	// это честнее тишины.
	base := 0
	if snap := p.Snapshot(); snap != nil {
		base = snap.Volume
	}
	yt.SetBase(base)

	return yt.Play(ctx, item.URI)
}

// awaitYouTube ждёт, пока mpv доиграет, и убивает его после.
//
// natural — доиграл сам, а не «оборвали». fell — умер, не доиграв: это не то
// же самое, что скип, и заказ стоит попробовать сыграть другим источником.
func (p *Player) awaitYouTube(ctx context.Context, item queue.Item) (natural, fell bool) {
	p.mu.Lock()
	yt := p.youtube
	p.mu.Unlock()
	if yt == nil {
		return false, false
	}
	started := time.Now()

	// mpv не должен пережить трек: висящий процесс занимает звуковое
	// устройство, и следующий заказ окажется без звука.
	defer yt.Stop()

	// Ответ Wait важен: mpv может упасть через полсекунды после старта, и
	// раньше это засчитывалось как «доиграл». Баллы списаны, зритель ничего
	// не услышал, на Twitch заказ отмечен выполненным.
	done := make(chan bool, 1)
	go func() { done <- yt.Wait(ctx) }()

	// Жёсткий срок, как и у заказов Spotify. Без него зависший mpv (не
	// открылся поток, занято звуковое устройство, yt-dlp внутри ждёт ответа
	// YouTube) держал очередь навсегда: тишина в эфире, панель показывает
	// «играет», новые заказы копятся, и выйти из этого можно только скипом,
	// о котором ещё надо догадаться.
	length := time.Duration(item.DurationMs) * time.Millisecond
	if length <= 0 {
		length = 10 * time.Minute
	}
	// Срок проверяем тиканьем, а не одним таймером: пауза не должна съедать
	// его. Поставил стример заказ на паузу на пять минут — и одноразовый
	// таймер снял бы трек, которого никто не слышал.
	limit := time.NewTicker(time.Second)
	defer limit.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, false
		case <-p.skip:
			p.log.Info("заказ с YouTube скипнут", "трек", item.Title)
			return false, false
		case <-limit.C:
			if time.Since(started)-p.held() <= length+30*time.Second {
				continue
			}
			p.log.Warn("заказ с YouTube не кончился в срок — снимаю",
				"трек", item.Title, "длительность_мс", item.DurationMs)
			return false, false
		case ok := <-done:
			if ok {
				return true, false
			}
			// Не доиграл. Считаем падением только то, что случилось заметно
			// раньше конца: mpv, оборвавшийся на последних секундах, зритель
			// уже послушал, и переигрывать ему трек с начала — хуже, чем
			// промолчать.
			played := time.Since(started) - p.held()
			fell = played < length*4/5
			if fell {
				p.log.Warn("проигрыватель умер посреди заказа",
					"трек", item.Title, "сыграно_мс", played.Milliseconds(),
					"длительность_мс", item.DurationMs)
			}
			return false, fell
		}
	}
}

// await ждёт конца трека.
//
// Спим до расчётного конца, потом один раз спрашиваем Spotify. Если трек
// ещё играет — стример поставил паузу или перемотал назад, — ждём остаток.
//
// Возвращает true, если заказ доиграл сам, и false, если его оборвали —
// скипом или руками в самом Spotify. Разница важна возврату: см. restore.
func (p *Player) await(ctx context.Context, item queue.Item) bool {
	// Главное правило: очередь обязана двигаться сама. Что бы ни ответил
	// Spotify — ошибку, молчание, чужой трек, тот же трек на паузе, — заказ
	// заканчивается не позже своей длительности с небольшим запасом.
	//
	// Раньше здесь был один сон на всю длительность и одна проверка в конце.
	// Из-за этого приложение узнавало о происходящем последним: скипнул
	// стример заказ прямо в Spotify — панель показывала его ещё три минуты.
	// Теперь заглядываем регулярно, но выход по времени остался жёстким.
	length := time.Duration(item.DurationMs) * time.Millisecond
	if length <= 0 {
		length = 30 * time.Second
	}
	deadline := time.Now().Add(length + 15*time.Second)
	p.resetPanel()

	left := length
	// endBy — когда трек доиграет по нашим часам. Пока музыка идёт, срок
	// переставляется по настоящему положению в треке; на паузе он стоит
	// вместе с треком. Нужен он ради одного случая: доигравший заказ Spotify
	// показывает как тот же трек с нулевой позиции и на паузе, и отличить
	// это от настоящей паузы можно только по своим часам.
	endBy := time.Now().Add(length)
	// paused — сколько в сумме простояли на паузе. Своя мерка, отдельная от
	// общего срока: пауза не должна его растягивать без предела.
	var paused time.Duration
	// wasPaused — на прошлой проверке музыка стояла. Тогда сменившийся трек
	// означает «переключил человек», а не «доиграл сам».
	var wasPaused bool

	for ctx.Err() == nil {
		if time.Now().After(deadline) {
			p.log.Info("заказ доиграл по времени", "трек", item.Artist+" — "+item.Title)
			return true
		}

		// Шаг зависит от остатка: посреди трека заглядывать часто незачем,
		// а под конец — наоборот, иначе следующий заказ опоздает.
		nap := p.awaitNap(left)

		// Проснёмся ровно к концу трека? Тогда всё, что окажется в Spotify
		// после сна, — это уже не наш заказ, а то, чем Spotify продолжил сам.
		// Отличать это от «стример переключил руками» приходится здесь:
		// позже, из возврата, чужой трек выглядит одинаково в обоих случаях.
		toEnd := nap+2*time.Second >= left && !wasPaused

		if !p.napOrWake(ctx, nap) {
			p.log.Info("заказ скипнут", "трек", item.Title)
			return false
		}

		st, ok, err := p.peek(ctx, item)
		if err != nil {
			// Spotify не ответил. Гадать не будем: досидим по своим часам.
			p.log.Warn("не проверил, доиграл ли трек", "ошибка", err)
			left = time.Until(deadline)
			continue
		}
		if !ok || st.Item == nil || !yandex.SameTrack(st.Item.URI, item.URI) {
			p.log.Info("заказ больше не играет в Spotify",
				"трек", item.Artist+" — "+item.Title, "доиграл_сам", toEnd)
			return toEnd
		}

		left = time.Duration(st.Item.DurationMs-st.ProgressMs) * time.Millisecond

		if st.IsPlaying {
			// Перемотка сбивает отсчёт: точка отсчёта переставляется на
			// настоящее положение, и панель с виджетом узнают об этом сразу.
			//
			// Читаем её только у играющей музыки. На паузе Spotify
			// показывает не то место, где остановился стример: доигравший
			// заказ он отматывает на ноль — и перемотка «на ноль» каждые
			// шесть секунд двигала точку отсчёта на «сейчас», отчего остаток
			// трека всегда равнялся полной длительности, а срок не наступал
			// никогда.
			p.reposition(st.ProgressMs)
			endBy = time.Now().Add(left)
			if left <= 0 {
				return true
			}
			wasPaused = false
			continue
		}

		// Дальше музыка стоит.

		// Заказ доиграл, а Spotify показывает его же с нулевой позиции.
		//
		// Так кончается каждый заказ: включаем мы один трек без источника,
		// продолжать Spotify нечем, и он отматывает тот же трек на начало и
		// встаёт на паузу. Раньше это считалось паузой стримера — очередь
		// замирала на пять минут, следующий заказ не играл, возврата
		// плейлиста не было. Со стороны: «поиграл — и всё стопится».
		//
		// Порог по своим часам обязателен: без него настоящая пауза на первых
		// секундах заказа тоже сошла бы за конец.
		slack := p.endSlack(length)
		if time.Duration(st.ProgressMs)*time.Millisecond <= slack &&
			!time.Now().Add(slack).Before(endBy) {
			p.log.Info("заказ доиграл: Spotify отмотал трек на начало и встал",
				"трек", item.Artist+" — "+item.Title)
			return true
		}

		// Трек на паузе у самого конца — это конец, а не пауза: Spotify так
		// показывает доигравший трек, и ждать тут нечего.
		if left <= 2*time.Second {
			return true
		}

		// Настоящая пауза: стример остановил музыку сам. Ждём его, но не
		// бесконечно.
		{
			//
			// Раньше срок продлевался на десять секунд на каждой проверке, а
			// проверки шли чаще, — то есть убегал быстрее, чем шло время, и
			// заказ висел «играет» вечно. Теперь у паузы свой общий запас.
			paused += p.awaitStep()
			if paused > maxPause {
				// Музыку остановил человек — возврату здесь делать нечего.
				p.log.Info("заказ снят: Spotify стоит на паузе слишком долго",
					"трек", item.Artist+" — "+item.Title)
				return false
			}

			// Срок только продлеваем, никогда не сокращаем.
			//
			// Раньше здесь стояло `deadline = time.Now().Add(...)`, то есть
			// срок пересчитывался заново и переставал зависеть от остатка
			// трека. Десятиминутный заказ, поставленный на паузу на десятой
			// секунде, получал срок «шесть минут от сих» — и ровно на шестой
			// минуте обрывался посреди песни, а в историю писался как
			// «отыгравший». Стример при этом ничего не нажимал.
			if until := time.Now().Add(left + maxPause - paused); until.After(deadline) {
				deadline = until
			}

			// И отмечаем, что музыка стоит: если на следующей проверке в
			// Spotify окажется другой трек, это переключил человек, а не
			// «заказ доиграл». Раньше здесь ещё и подменялся остаток
			// (`left = p.step()`), из-за чего toEnd на паузе выходил всегда
			// истинным, и оборванный заказ выдавался за отыгравший — вместе
			// с возвратом музыки поверх того, что стример только что выбрал.
			// Трек стоит — вместе с ним стоит и срок его конца. Иначе через
			// время, равное остатку трека, пауза превратилась бы в «доиграл».
			//
			// Но первую проверку на паузе срок не двигаем: между нашим
			// расчётом конца и настоящим есть задержка сети, и пауза может
			// прийти на мгновение раньше срока. Сдвинь мы его сразу — он
			// убегал бы ровно с той же скоростью, что идёт время, и «трек
			// доиграл» не наступило бы никогда.
			if wasPaused {
				endBy = endBy.Add(nap)
			}
			wasPaused = true
		}
	}
	return false
}

// endSlack — насколько близко к нулю должна быть позиция и насколько
// разрешаем ошибиться своим часам, чтобы счесть трек доигравшим.
//
// Меньше шага проверки взять нельзя: раньше него мы о конце трека всё равно
// не узнаем. Больше трёх секунд — тоже: тогда под конец трека настоящая
// пауза начнёт сходить за конец.
func (p *Player) endSlack(length time.Duration) time.Duration {
	slack := p.awaitStep()
	if slack > 3*time.Second {
		slack = 3 * time.Second
	}
	if quarter := length / 4; slack > quarter {
		slack = quarter
	}
	return slack
}

// maxPause — сколько всего готовы ждать стримера, если он поставил паузу.
// Больше пяти минут — это уже не пауза, а «отошёл»: очередь при этом стоять
// не должна.
const maxPause = 5 * time.Minute

// awaitNap — сколько спать до следующей проверки.
func (p *Player) awaitNap(left time.Duration) time.Duration {
	step := p.awaitStep()
	// Нижний порог держим коротким независимо от шага. Он работает только на
	// последних мгновениях трека, и от него зависит, будет ли между заказами
	// слышна пауза. С длинным шагом (двадцать пять секунд) десятая доля дала
	// бы две с половиной секунды тишины на каждой смене трека.
	floor := step / 10
	if floor > time.Second {
		floor = time.Second
	}
	if floor < 50*time.Millisecond {
		floor = 50 * time.Millisecond
	}
	switch {
	case left < floor:
		return floor
	case left > step:
		return step
	default:
		return left
	}
}

// SetPollEvery меняет шаг проверки Spotify на ходу.
func (p *Player) SetPollEvery(d time.Duration) {
	if d <= 0 {
		d = defaultPoll
	}
	p.pollEvery.Store(int64(d))
}

func (p *Player) step() time.Duration {
	if d := p.pollEvery.Load(); d > 0 {
		return time.Duration(d)
	}
	return defaultPoll
}

// defaultPoll — как часто заглядываем в Spotify, пока играет заказ.
//
// Значение на случай, если шаг никто не выставил: обычно его задаёт сервер
// (см. orderPollStep в internal/server/own.go), и там же объяснено, почему он
// такой длинный. Коротко: этот опрос дважды доводил до многочасового
// ограничения от Spotify, а пользы от частого приносит немного — конец трека
// приложение и так ловит точно, по его длительности.
const defaultPoll = 25 * time.Second

// reposition переставляет точку отсчёта на настоящее положение в треке.
//
// Elapsed() считает от момента запуска, а трек можно перемотать — тогда весь
// дальнейший отсчёт врёт. Двигаем момент запуска так, чтобы Elapsed() снова
// показывал правду.
func (p *Player) reposition(progressMs int) {
	p.mu.Lock()
	if p.now == nil {
		p.mu.Unlock()
		return
	}
	want := time.Now().Add(-time.Duration(progressMs) * time.Millisecond)
	// Точка отсчёта берётся из настоящего положения в треке, значит счёт пауз
	// в неё уже включён и складывать его второй раз нельзя.
	p.now.Held = 0
	drift := want.Sub(p.now.StartedAt)
	if drift < 0 {
		drift = -drift
	}
	// Мелкие расхождения — обычная задержка сети, дёргать из-за них панель
	// незачем. Двигаем, только когда действительно перемотали.
	if drift < 2*time.Second {
		p.mu.Unlock()
		return
	}
	p.now.StartedAt = want
	p.mu.Unlock()

	p.log.Debug("положение в треке уточнено", "позиция_мс", progressMs)
	p.changed()
}

// restore возвращает Spotify туда, где он был до заказов.
func (p *Player) restore(ctx context.Context) {
	snap := p.Snapshot()
	if snap == nil {
		return
	}

	// Возврату надо сказать, что играло последним. Без этого он видит в
	// Spotify незнакомый трек — наш же заказ — и решает, что стример
	// переключил музыку сам, а значит трогать её нельзя. Итог: музыка не
	// возвращалась почти никогда, а в панели висело «переключили вручную».
	p.mu.Lock()
	played := p.lastPlayed
	// Заказ доиграл сам — значит стример за это время ни во что не вмешался,
	// и защита «музыку переключили вручную» здесь только мешает.
	//
	// Мешает она вот почему: заказ мы включаем одним треком, без источника, и
	// Spotify после него запускает автоподбор — какую-то похожую музыку. Для
	// возврата это чужой трек, неотличимый от того, что стример выбрал сам, —
	// и возврат отказывался работать, а плейлист не возвращался вообще
	// никогда. Оборванный заказ — другое дело: там чужой трек и правда может
	// оказаться выбором человека, и туда мы не лезем.
	force := p.endedNaturally
	p.mu.Unlock()

	// Возврат — это несколько запросов подряд с повторами. Своего срока у
	// него не было, и на упавшей сети он останавливал всю очередь.
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()

	// Громкость возвращаем вместе с музыкой — но только если её двигали из
	// панели. Иначе заказ навсегда менял бы громкость самому стримеру, а он
	// узнал бы об этом через полчаса, заметив, что музыка тише обычного.
	// Лишнего запроса это не стоит: не трогали ползунок — не ходим никуда.
	p.mu.Lock()
	touched := p.volumeTouched
	p.volumeTouched = false
	p.mu.Unlock()
	if touched && snap.Volume > 0 {
		if err := p.music.SetVolume(ctx, snap.Volume, snap.DeviceID); err != nil {
			p.log.Warn("не вернул громкость Spotify", "ошибка", err)
		}
	}

	outcome, err := p.music.Restore(ctx, snap, played, force)
	if err != nil {
		p.log.Warn("не вернул Spotify на место", "ошибка", err)
		p.fail(err)

		// Сорвалось ошибкой — снимок не отработал. Выбросить его сейчас
		// значит потерять плейлист стримера навсегда: следующий снимок
		// снимется уже с нашего же заказа. Держим и пробуем ещё.
		p.mu.Lock()
		p.restoreTries++
		again := p.restoreTries < maxRestoreTries
		p.mu.Unlock()
		if again {
			p.log.Info("возврат попробуем ещё раз", "попытка", p.restoreTries)
			return
		}
		p.log.Warn("возврат не вышел, снимок забыт", "попыток", p.restoreTries)
	}

	if p.OnRestored != nil {
		p.OnRestored(outcome, snap)
	}

	// Снимок отработал: держать его дальше нельзя, иначе следующая пачка
	// заказов вернёт музыку на вчерашнее место.
	p.mu.Lock()
	p.snap = nil
	p.restoreTries = 0
	p.endedNaturally = false
	p.mu.Unlock()
	p.changed()
}

// maxRestoreTries — сколько раз пробуем вернуть музыку, если возврат падает
// ошибкой. Три попытки с промежутком в полминуты переживают и перезапуск
// Spotify, и моргнувшую сеть; дальше держаться за снимок бессмысленно.
const maxRestoreTries = 3

// notOurOwn не даёт запомнить как «музыку стримера» наш же отыгравший заказ.
//
// Так бывает, когда прошлый возврат не сработал: заказ доиграл, в Spotify
// висит он же, приходит следующий заказ — и снимок снимается с нашего трека.
// Возвращаться по такому снимку некуда: плейлист стримера в нём уже потерян,
// а приложение уверено, что всё в порядке. Честнее сказать «возвращать
// нечего» — тогда после очереди включится запасной плейлист.
func (p *Player) notOurOwn(snap *yandex.Snapshot) *yandex.Snapshot {
	if snap == nil || snap.Empty || snap.TrackURI == "" {
		return snap
	}

	p.mu.Lock()
	ours := yandex.SameTrack(snap.TrackURI, p.lastPlayed)
	p.mu.Unlock()
	if !ours {
		return snap
	}

	p.log.Warn("в Spotify всё ещё наш прошлый заказ — запоминать нечего",
		"трек", snap.ArtistName+" — "+snap.TrackName)
	return &yandex.Snapshot{
		CapturedAt: snap.CapturedAt,
		Empty:      true,
		DeviceID:   snap.DeviceID,
		DeviceName: snap.DeviceName,
	}
}

// restoreTimeout — сколько даём возврату. Внутри несколько запросов с
// повторами; без срока плеер застревал в возврате на минуты, и новые заказы
// всё это время не играли.
const restoreTimeout = 45 * time.Second

// dropped — заказ не удалось включить.
//
// Отмечать такой заказ отыгравшим нельзя: баллы списаны, зритель ничего не
// услышал, и по истории всё выглядит благополучно. Раньше при закрытом
// Spotify так молча прокручивалась вся очередь разом.
func (p *Player) dropped(item queue.Item, err error) {
	p.log.Warn("заказ не сыграл", "трек", item.Artist+" — "+item.Title, "ошибка", err)
	if p.OnDropped != nil {
		p.OnDropped(item, err)
	}
}

func (p *Player) finish(item queue.Item, natural bool) {
	if p.OnFinished != nil {
		p.OnFinished(item, natural)
	}
}

func (p *Player) changed() {
	if p.OnChange != nil {
		p.OnChange()
	}
}

func (p *Player) fail(err error) {
	if err == nil {
		return
	}
	if p.OnError != nil {
		p.OnError(err)
	}
}

// sleep ждёт d; false означает, что приложение закрывают.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// SetOptions меняет настройки работающего плеера.
func (p *Player) SetOptions(resumeDelay time.Duration, waitForCurrent bool) {
	p.mu.Lock()
	p.ResumeDelay = resumeDelay
	p.WaitForCurrent = waitForCurrent
	p.mu.Unlock()
}

// options отдаёт настройки под блокировкой.
func (p *Player) options() (resumeDelay time.Duration, waitForCurrent bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ResumeDelay, p.WaitForCurrent
}

// drainSkip выбрасывает залежавшийся сигнал скипа.
func (p *Player) drainSkip() {
	select {
	case <-p.skip:
	default:
	}
}
