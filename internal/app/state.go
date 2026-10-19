// Package app хранит состояние приложения, которое видит панель.
//
// Состояние меняется только из обработчиков событий (редемпшен, конец трека,
// команда модератора). Никаких фоновых опросов здесь нет: панель узнаёт об
// изменениях из WebSocket, а в простое приложение не делает вообще ничего.
package app

import (
	"sync"
	"time"
)

// Marker — по этому слову в ответе /api/state приложение узнаёт само себя.
//
// Нужно второму запуску: он стучится на наш порт и должен понять, там наша же
// копия или чужая программа, которая случайно села на 8977. Проверять одну
// занятость порта мало — из-за чужого соседа приложение молча не стартовало бы.
const Marker = "songrequest-yandex"

// Уровни состояния подключения. Разделять «сломалось» и «недоступно» важно:
// красная ячейка означает «чини», и показывать её там, где чинить нечего —
// например, когда на канале в принципе нет баллов, — значит врать человеку.
const (
	// ConnOK — работает.
	ConnOK = "ok"
	// ConnIdle — не настроено или недоступно на этом аккаунте. Не ошибка.
	ConnIdle = "idle"
	// ConnFail — сломалось, надо чинить.
	ConnFail = "fail"
)

// ConnState — состояние одного внешнего подключения для лампочки в панели.
type ConnState struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	// Level — как красить: ok, idle или fail. Панель не должна догадываться
	// об этом по тексту, иначе новая формулировка молча меняет цвет.
	Level string `json:"level"`
	// Detail — человеческая подсказка: «Не настроено», «Слетела авторизация».
	// Никаких «401 Unauthorized» тут быть не должно.
	Detail string `json:"detail"`
}

// NowPlaying — что играет прямо сейчас.
// Откуда взялся трек.
const (
	// SourceOrder — заказ зрителя.
	SourceOrder = "order"
	// SourceOwn — стример поставил сам.
	SourceOwn = "own"
)

type NowPlaying struct {
	Provider string `json:"provider"` // spotify | youtube | ""
	// Source — откуда взялся трек: "order" — заказ зрителя, "own" — стример
	// поставил сам. Зритель должен видеть разницу: иначе он решит, что
	// заказы кто-то занял на полчаса вперёд.
	Source string `json:"source"`
	// Via — почему трек играет через этот источник, словами для человека.
	// Пусто у обычного заказа из Spotify: объяснять нечего.
	Via        string `json:"via"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	CoverURL   string `json:"cover_url"`
	Requester  string `json:"requester"`
	PositionMs int    `json:"position_ms"`
	DurationMs int    `json:"duration_ms"`
	Uncertain  bool   `json:"uncertain"`
	// Paused — заказ стоит на паузе. Панель по нему останавливает полосу:
	// считает она сама, у себя, и без этого признака ехала бы на паузе.
	Paused bool `json:"paused"`
	// Volume — на чём стоит ползунок громкости, в процентах. Панель берёт его
	// отсюда, а не спрашивает Spotify: спрашивать пришлось бы на каждую
	// перерисовку.
	Volume int `json:"volume"`
}

// LastPlayed — трек, который только что ушёл из эфира.
//
// Зачем отдельный тип, а не тот же NowPlaying: у ушедшего трека нет
// положения (он кончился), зато есть время и есть адрес в Spotify — по нему
// его можно завести заново.
type LastPlayed struct {
	Provider string `json:"provider"` // spotify | youtube
	// Source — откуда взялся трек: "order" — заказ зрителя, "own" —
	// стример слушал сам.
	Source string `json:"source"`
	// Via — почему трек играл мимо Spotify. Пусто у обычного заказа.
	Via        string    `json:"via"`
	Title      string    `json:"title"`
	Artist     string    `json:"artist"`
	CoverURL   string    `json:"cover_url"`
	Requester  string    `json:"requester"`
	DurationMs int       `json:"duration_ms"`
	At         time.Time `json:"at"`

	// URI и RawRequest нужны, чтобы трек можно было поставить ещё раз: первый
	// — для Spotify, второй — для всего остального (ссылка на ролик, текст
	// заказа). Панель их не показывает.
	URI        string `json:"uri"`
	RawRequest string `json:"raw_request"`
}

// QueueItem — заказ в очереди.
type QueueItem struct {
	ID        int64  `json:"id"`
	Source    string `json:"source"`
	Requester string `json:"requester"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Provider  string `json:"provider"`
	Via       string `json:"via"`
	// RawRequest — что написал зритель. Показываем рядом с найденным треком:
	// сразу видно, если подобралось не то.
	RawRequest string `json:"raw_request"`
	CoverURL   string `json:"cover_url"`
	DurationMs int    `json:"duration_ms"`
	Uncertain  bool   `json:"uncertain"`
	// Waiting — заказ уже взят из очереди и ждёт конца трека стримера.
	// Двигать и удалять такую строку нечего: в очереди её больше нет,
	// она у плеера.
	Waiting bool `json:"waiting"`
}

// Notice — сообщение для стримера в панели.
//
// Code — короткий код вроде SP-04. Он нужен, чтобы стример мог назвать ошибку
// голосом, не пересказывая текст и не выгружая лог ради одной строчки.
type Notice struct {
	Level string    `json:"level"` // info | warn | error
	Code  string    `json:"code"`
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
	// Count — сколько раз подряд повторилось одно и то же. Нажатие кнопки,
	// которое пять раз отвечает одинаково, должно быть одной строкой со
	// счётчиком, а не пятью: иначе хроника забивается и в ней тонет важное.
	Count int `json:"count"`
}

// YandexInfo — состояние Яндекс Музыки для панели.
type YandexInfo struct {
	Connected bool   `json:"connected"`
	Account   string `json:"account"`
	Login     string `json:"login"`
	// Plus: yes | no | unknown. «Яндекс не сказал» — не то же, что «нет».
	Plus         string `json:"plus"`
	PlusNote     string `json:"plus_note"`
	PlusNoteCode string `json:"plus_note_code"`

	// Ynison — на связи ли очередь Яндекса. Без неё заказы пишутся в
	// плейлист «Заказы со стрима» и включаются стримером вручную.
	Ynison       bool   `json:"ynison"`
	YnisonDevice string `json:"ynison_device"`
	YnisonNote   string `json:"ynison_note"`

	SnapshotText string     `json:"snapshot_text"`
	SnapshotAt   *time.Time `json:"snapshot_at"`
}

// TwitchInfo — состояние Twitch для панели.
type TwitchInfo struct {
	HasClientID bool   `json:"has_client_id"`
	Connected   bool   `json:"connected"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"` // партнёр | аффилиат | обычный
	HasPoints   bool   `json:"has_points"`

	RewardTitle string `json:"reward_title"`
	RewardCost  int    `json:"reward_cost"`
	RewardReady bool   `json:"reward_ready"`

	// Вторая награда — за плейлист. Пустая и не готовая, если стример её не
	// включал: это отдельная галочка в настройках.
	PlaylistTitle string `json:"playlist_title"`
	PlaylistCost  int    `json:"playlist_cost"`
	PlaylistReady bool   `json:"playlist_ready"`

	// Код для входа: стример открывает адрес на любом устройстве и вводит код.
	PendingCode    string     `json:"pending_code"`
	PendingURL     string     `json:"pending_url"`
	PendingExpires *time.Time `json:"pending_expires"`

	Note     string `json:"note"`
	NoteCode string `json:"note_code"`
	// RenewAt — когда придётся вводить код заново. У публичных клиентов
	// Twitch доступ живёт тридцать дней, и узнать об этом лучше заранее,
	// а не в момент, когда заказы перестали приходить.
	RenewAt   *time.Time `json:"renew_at"`
	RenewSoon bool       `json:"renew_soon"`
}

// RedemptionView — заказ за баллы. На этом этапе очереди ещё нет, поэтому
// заказы просто копятся списком, чтобы можно было проверить приём и возврат.
type RedemptionView struct {
	ID       string    `json:"id"`
	RewardID string    `json:"reward_id"`
	User     string    `json:"user"`
	Text     string    `json:"text"`
	Cost     int       `json:"cost"`
	At       time.Time `json:"at"`
	// Status — код, а не готовая фраза: панель сравнивает состояние заказа с
	// ним, и переписанная формулировка не должна молча ломать эту проверку.
	Status string `json:"status"` // new | refunded | fulfilled
	// Match — что нашлось в Spotify по тексту заказа.
	Match OrderMatch `json:"match"`
}

// Состояния заказа.
const (
	OrderNew       = "new"
	OrderRefunded  = "refunded"
	OrderFulfilled = "fulfilled"
)

// Состояния подбора трека.
const (
	MatchSearching = "searching"
	MatchFound     = "found"
	MatchUncertain = "uncertain"
	MatchMissing   = "missing" // в Spotify нет — дальше будет YouTube
	MatchFailed    = "failed"  // не смогли поискать
)

// OrderMatch — что удалось подобрать по тексту заказа.
type OrderMatch struct {
	State    string `json:"state"`
	TrackID  string `json:"track_id"`
	URI      string `json:"uri"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	CoverURL string `json:"cover_url"`
	Duration int    `json:"duration_ms"`
	// Note — что сказать стримеру: «совпадение неточное», «в Spotify нет».
	Note string `json:"note"`
	// Why — расшифровка оценки. В панели не показывается, нужна для разбора.
	Why string `json:"why"`
}

// YouTubeInfo — состояние запасного проигрывателя.
type YouTubeInfo struct {
	// Ready означает, что оба инструмента на месте и заказ, которого нет
	// в Spotify, всё-таки заиграет.
	Ready    bool     `json:"ready"`
	YtDlp    string   `json:"ytdlp"`
	Mpv      string   `json:"mpv"`
	Device   string   `json:"device"`
	Devices  []AudioDevice `json:"devices"`
	Note     string   `json:"note"`
	NoteCode string   `json:"note_code"`
}

// Session — итоги с момента запуска. Панель на этапе, когда заказов ещё нет,
// иначе состоит из одних пустых блоков; это настоящие числа, а не украшение.
type Session struct {
	StartedAt time.Time `json:"started_at"`
	Orders    int       `json:"orders"`
	Refunded  int       `json:"refunded"`
	Accepted  int       `json:"accepted"`
	Points    int       `json:"points"` // потрачено зрителями баллов
}

// Snapshot — вся картинка целиком, ровно то, что уходит в панель одним JSON.
type Snapshot struct {
	// App — всегда Marker. Смотри его описание: по нему вторая копия
	// приложения узнаёт первую.
	App         string      `json:"app"`
	Connections []ConnState `json:"connections"`
	Now         *NowPlaying `json:"now"`
	// LastPlayed — что играло перед этим. Показывается в тишине, когда
	// показывать больше нечего, и переживает перезапуск приложения.
	LastPlayed *LastPlayed `json:"last_played"`
	Queue      []QueueItem `json:"queue"`
	// Playlists — заказанные плейлисты, ждущие решения стримера.
	Playlists []PlaylistView `json:"playlists"`
	// YouTubeAsks — заказы, которых нет в Spotify и которые ждут решения
	// стримера: играть ли их с YouTube.
	YouTubeAsks []YouTubeAsk `json:"youtube_asks"`
	// YouTubeRecent — последние заказы, сыгравшие с YouTube. Для вкладки
	// «YouTube»: видно, что именно нашлось и правильно ли.
	YouTubeRecent []YouTubeRecent  `json:"youtube_recent"`
	Notices       []Notice         `json:"notices"`
	Paused        bool             `json:"paused"` // приём заказов остановлен
	Version       string           `json:"version"`
	Yandex        YandexInfo       `json:"yandex"`
	Twitch        TwitchInfo       `json:"twitch"`
	Redemptions   []RedemptionView `json:"redemptions"`
	Session       Session          `json:"session"`
	// Bans — бан-лист музыки. Отдельный от бана в чате: человек может быть
	// нормальным в чате и неуместным в заказах.
	Bans    []Ban       `json:"bans"`
	YouTube YouTubeInfo `json:"youtube"`
	// Tunnel — обход блокировок: включён ли и через какой сервер.
	Tunnel TunnelInfo `json:"tunnel"`
	// Update — что известно про обновление приложения.
	Update UpdateInfo `json:"update"`
	// Widget — оформление виджета. Едет вместе с состоянием, чтобы правка в
	// панели доезжала до OBS сразу: перезагружать источник не нужно.
	Widget any `json:"widget"`
}

// UpdateInfo — что известно про обновление приложения.
//
// Приложение само в интернет за обновлениями не ходит: проверка только по
// кнопке. Поэтому здесь почти всегда пусто, и кнопка «Обновить» появляется
// ровно тогда, когда человек нажал «Проверить» и обновление правда есть.
type UpdateInfo struct {
	// Repo — откуда берём выпуски, «имя/репозиторий» на GitHub. Пусто —
	// обновляться неоткуда, и панель говорит об этом словами.
	Repo string `json:"repo"`
	// Current — версия, которая работает прямо сейчас.
	Current string `json:"current"`

	// Checking и Installing — идёт ли прямо сейчас проверка или установка.
	// Панель по ним гасит кнопки: обе работы долгие, и второе нажатие
	// посреди первой ничего хорошего не даст.
	Checking   bool `json:"checking"`
	Installing bool `json:"installing"`

	// Available — есть что ставить. Только по этому признаку панель
	// показывает кнопку «Обновить».
	Available bool   `json:"available"`
	Version   string `json:"version"`
	// Notes — что изменилось, как написано в выпуске.
	Notes string `json:"notes"`
	Size  int64  `json:"size"`

	CheckedAt *time.Time `json:"checked_at"`

	// Note и NoteCode — чем кончилась последняя попытка, словами и кодом.
	Note     string `json:"note"`
	NoteCode string `json:"note_code"`
	// Page — страница выпусков, куда отправить человека, если обновиться
	// само не вышло.
	Page string `json:"page"`
}

// YouTubeAsk — заказ, которого нет в Spotify, найденный на YouTube.
//
// Появилось 11.10: друг владельца жаловался, что такой заказ «играл
// непонятно где» — приложение молча включало его отдельным проигрывателем, и
// стример не понимал, откуда звук. Теперь стример выбирает в настройках, как
// с этим быть, и в режиме «спрашивать» заказ ждёт здесь.
type YouTubeAsk struct {
	ID        string `json:"id"`
	Requester string `json:"requester"`
	// Request — что написал зритель. Рядом с найденным, чтобы сразу видеть,
	// то ли нашлось.
	Request    string `json:"request"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	URL        string `json:"url"`
	CoverURL   string `json:"cover_url"`
	DurationMs int    `json:"duration_ms"`
	// Via — почему не Spotify, словами: «в Spotify такого нет».
	Via string `json:"via"`
	// Until — когда решение примется само. Ноль — ждём сколько угодно.
	Until time.Time `json:"until"`
	// Else — что будет по истечении срока: «play» или «skip».
	Else string `json:"else"`
}

// YouTubeRecent — заказ, который недавно играл с YouTube.
type YouTubeRecent struct {
	Requester string    `json:"requester"`
	Request   string    `json:"request"`
	Title     string    `json:"title"`
	Artist    string    `json:"artist"`
	URL       string    `json:"url"`
	Via       string    `json:"via"`
	At        time.Time `json:"at"`
}

// PlaylistView — заказанный плейлист, который ждёт решения стримера.
//
// Плейлист не попадает в очередь сам: сорок минут чужой музыки подряд решает
// хозяин эфира. Пока решения нет, он живёт здесь и виден в панели, а стример
// или модератор говорит «да» кнопкой или командой в чате.
type PlaylistView struct {
	// ID — номер для кнопок и для команды «одобрить 2».
	ID        string `json:"id"`
	Requester string `json:"requester"`
	// Source — откуда плейлист, словами: «Spotify» или «Яндекс.Музыка».
	Source string `json:"source"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	// Tracks — что сыграет, строками «артист — название», по порядку.
	Tracks []string `json:"tracks"`
	// Total — сколько треков было в плейлисте всего. Больше длины Tracks,
	// когда плейлист длиннее разрешённого настройкой.
	Total int `json:"total"`
	// LengthMs — сколько всё это будет играть. Главное число для решения:
	// стример смотрит не на список, а на «сколько это займёт эфира».
	LengthMs int       `json:"length_ms"`
	At       time.Time `json:"at"`
}

// Состояние обхода одним словом — для панели и для лампочки в верхней полосе.
//
// Раньше состояние приходилось угадывать по трём признакам сразу (включён ли,
// есть ли ключ, поднят ли посредник) плюс по тексту в Note. Угадывалось плохо:
// «поднять не вышло» и «поднимать не понадобилось» выглядели одинаково —
// обход не работает, ключ есть, — и панель писала «наготове» там, где на самом
// деле всё лежало.
const (
	TunnelOff          = "off"          // ключа нет или обход выключен человеком
	TunnelConnecting   = "connecting"   // поднимаем: качаем список, перебираем серверы
	TunnelConnected    = "connected"    // работает
	TunnelReconnecting = "reconnecting" // работал и отвалился, поднимаем заново
	TunnelStandby      = "standby"      // ключ есть, но Spotify отвечает и без обхода
	TunnelDown         = "down"         // должен работать, а не работает
)

// TunnelInfo — состояние обхода блокировок для панели.
//
// Ключа здесь нет и быть не может: панель его не показывает никогда, даже
// звёздочками. Есть только признак «ключ вставлен» и подпись сервера — то,
// что человеку нужно, чтобы понять, работает обход или нет.
type TunnelInfo struct {
	On     bool   `json:"on"`
	HasKey bool   `json:"has_key"`
	Hint   string `json:"hint"`   // что вставлено: «подписка sub.example.com»
	Server string `json:"server"` // какой сервер сейчас работает
	Note   string `json:"note"`   // что сказать человеку: чем кончилась проверка

	// Standby — ключ есть и обход разрешён, но сейчас он не поднят: Spotify
	// отвечает и без него. Так бывает, когда у стримера включён свой VPN на
	// весь компьютер. Для панели это не «выключено», а «наготове».
	Standby bool `json:"standby"`

	// Phase — то же самое одним словом, см. постоянные Tunnel* выше. Панель
	// красит карточку и лампочку по нему, а не разбирает текст в Note.
	Phase string `json:"phase"`

	// FromCache — список серверов взят из памяти: сервис подписки не ответил.
	// Обход при этом работает, но список мог устареть, и сказать об этом надо.
	FromCache bool `json:"from_cache"`
}

// Ban — закрытый доступ к заказам.
type Ban struct {
	Login  string    `json:"login"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// State — потокобезопасное состояние с уведомлением подписчиков об изменениях.
type State struct {
	mu          sync.RWMutex
	conns       map[string]ConnState
	order       []string // порядок лампочек в панели, фиксированный
	now         *NowPlaying
	lastPlayed  *LastPlayed
	queue       []QueueItem
	playlists   []PlaylistView
	ytAsks      []YouTubeAsk
	ytRecent    []YouTubeRecent
	update      UpdateInfo
	notices     []Notice
	paused      bool
	version     string
	yandex      YandexInfo
	twitch      TwitchInfo
	redemptions []RedemptionView
	session     Session
	tunnel      TunnelInfo
	bans        []Ban
	youtube     YouTubeInfo
	widget      any

	subs map[int]chan struct{}
	next int
}

const maxNotices = 50

// maxRedemptions — сколько заказов держим в списке до появления очереди.
const maxRedemptions = 20

// New создаёт состояние с погашенными лампочками подключений.
func New(version string) *State {
	s := &State{
		conns:   map[string]ConnState{},
		subs:    map[int]chan struct{}{},
		version: version,
	}
	s.session.StartedAt = time.Now()

	for _, name := range []string{"Яндекс", "Twitch", "DonationAlerts", "DonatePay", "DonateX"} {
		s.order = append(s.order, name)
		s.conns[name] = ConnState{Name: name, Level: ConnIdle, Detail: "Не настроено"}
	}
	return s
}

// Subscribe отдаёт канал-звонок: в него приходит сигнал при любом изменении.
// Канал буферизован на 1 — если подписчик отстал, лишние сигналы отбрасываются,
// он всё равно прочитает актуальный снимок целиком.
func (s *State) Subscribe() (<-chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.next
	s.next++
	ch := make(chan struct{}, 1)
	s.subs[id] = ch

	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}

// notify вызывается под уже взятой блокировкой записи.
func (s *State) notify() {
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // подписчик ещё не забрал прошлый сигнал — и не надо
		}
	}
}

// SetConnOK — подключение работает.
func (s *State) SetConnOK(name, detail string) {
	s.setConn(ConnState{Name: name, Connected: true, Level: ConnOK, Detail: detail})
}

// SetConnIdle — не настроено или недоступно. Чинить нечего, тревожить незачем.
func (s *State) SetConnIdle(name, detail string) {
	s.setConn(ConnState{Name: name, Level: ConnIdle, Detail: detail})
}

// SetConnFail — сломалось, нужно вмешательство.
func (s *State) SetConnFail(name, detail string) {
	s.setConn(ConnState{Name: name, Level: ConnFail, Detail: detail})
}

func (s *State) setConn(c ConnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[c.Name] = c
	s.notify()
}

// SetWidget запоминает оформление виджета.
func (s *State) SetWidget(w any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.widget = w
	s.notify()
}

// SetNow задаёт текущий трек (nil — тишина).
func (s *State) SetNow(n *NowPlaying) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = n
	s.notify()
}

// SetLastPlayed запоминает трек, который только что доиграл.
func (s *State) SetLastPlayed(l *LastPlayed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPlayed = l
	s.notify()
}

// SetQueue заменяет очередь целиком.
func (s *State) SetQueue(q []QueueItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = q
	s.notify()
}

// SetPlaylists обновляет список плейлистов, ждущих решения.
func (s *State) SetPlaylists(list []PlaylistView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playlists = list
	s.notify()
}

// SetYouTubeAsks обновляет список заказов с YouTube, ждущих решения.
func (s *State) SetYouTubeAsks(list []YouTubeAsk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ytAsks = list
	s.notify()
}

// AddYouTubeRecent запоминает заказ, сыгравший с YouTube. Держим десять
// последних: вкладка нужна, чтобы проверить «что там нашлось», а не для
// истории за вечер — история есть отдельно.
func (s *State) AddYouTubeRecent(r YouTubeRecent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ytRecent = append([]YouTubeRecent{r}, s.ytRecent...)
	if len(s.ytRecent) > 10 {
		s.ytRecent = s.ytRecent[:10]
	}
	s.notify()
}

// SetPaused включает или выключает приём заказов.
func (s *State) SetPaused(p bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = p
	s.notify()
}

// SetYandex обновляет карточку Яндекс Музыки в панели.
func (s *State) SetYandex(info YandexInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.yandex = info
	s.notify()
}

// UpdateYandex меняет часть сведений о Яндексе, не трогая остальные.
func (s *State) UpdateYandex(fn func(*YandexInfo)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.yandex)
	s.notify()
}

// SetTwitch обновляет карточку Twitch в панели.
func (s *State) SetTwitch(info TwitchInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.twitch = info
	s.notify()
}

// UpdateTwitch меняет часть сведений о Twitch, не трогая остальные.
func (s *State) UpdateSelf(fn func(*UpdateInfo)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.update)
	s.notify()
}

// UpdateTwitch меняет карточку Twitch.
func (s *State) UpdateTwitch(fn func(*TwitchInfo)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.twitch)
	s.notify()
}

// AddRedemption кладёт новый заказ в начало списка.
func (s *State) AddRedemption(r RedemptionView) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session.Orders++
	s.session.Points += r.Cost
	s.redemptions = append([]RedemptionView{r}, s.redemptions...)
	if len(s.redemptions) > maxRedemptions {
		s.redemptions = s.redemptions[:maxRedemptions]
	}
	s.notify()
}

// SetOrderMatch записывает результат подбора трека.
func (s *State) SetOrderMatch(id string, m OrderMatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.redemptions {
		if s.redemptions[i].ID == id {
			s.redemptions[i].Match = m
			break
		}
	}
	s.notify()
}

// SetRedemptionStatus помечает заказ как возвращённый или выполненный.
func (s *State) SetRedemptionStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.redemptions {
		if s.redemptions[i].ID != id {
			continue
		}
		// Счётчики считаем один раз: повторное нажатие не должно их накручивать.
		if s.redemptions[i].Status == OrderNew {
			switch status {
			case OrderRefunded:
				s.session.Refunded++
			case OrderFulfilled:
				s.session.Accepted++
			}
		}
		s.redemptions[i].Status = status
		break
	}
	s.notify()
}

// CountOrder засчитывает заказ, который не проходил через список наград.
//
// Заказы за баллы попадают в счётчик через AddRedemption, а донаты идут прямо
// в очередь и мимо него: в итогах сессии их просто не было, и при включённых
// донатах числа выходили меньше настоящих.
func (s *State) CountOrder() {
	// Блокировку держим и на notify — как во всех остальных методах.
	//
	// Раньше здесь стоял Unlock перед notify, и это был единственный такой
	// случай на весь файл. notify обходит карту подписчиков и пишет в их
	// каналы, а unsubscribe в это же время из карты удаляет и канал
	// закрывает. Донат, пришедший ровно в тот миг, когда стример перезагрузил
	// вкладку панели или OBS переподключил виджет, ронял всё приложение:
	// «concurrent map iteration and map write» или «send on closed channel».
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session.Orders++
	s.notify()
}

// SetBans обновляет бан-лист в панели.
func (s *State) SetBans(list []Ban) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bans = list
	s.notify()
}

// UpdateYouTube меняет сведения о запасном проигрывателе.
func (s *State) UpdateYouTube(fn func(*YouTubeInfo)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.youtube)
	s.notify()
}

// SetTunnel меняет сведения об обходе блокировок.
func (s *State) SetTunnel(info TunnelInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tunnel = info
	s.notify()
}

// Notify добавляет сообщение для стримера.
func (s *State) Notify(level, text string) {
	s.NotifyCode(level, "", text)
}

// NotifyCode добавляет сообщение с кодом ошибки.
func (s *State) NotifyCode(level, code, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Повтор последнего сообщения не плодит строки, а увеличивает счётчик.
	if n := len(s.notices); n > 0 {
		last := &s.notices[n-1]
		if last.Text == text && last.Code == code && last.Level == level {
			last.Count++
			last.At = time.Now()
			s.notify()
			return
		}
	}

	s.notices = append(s.notices, Notice{
		Level: level, Code: code, Text: text, At: time.Now(), Count: 1,
	})
	if len(s.notices) > maxNotices {
		s.notices = s.notices[len(s.notices)-maxNotices:]
	}
	s.notify()
}

// Snapshot собирает копию состояния для отправки в панель.
func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Пустые срезы, а не nil: панель ждёт массив и на null споткнётся.
	snap := Snapshot{
		App:           Marker,
		Queue:         append(make([]QueueItem, 0, len(s.queue)), s.queue...),
		Playlists:     append(make([]PlaylistView, 0, len(s.playlists)), s.playlists...),
		YouTubeAsks:   append(make([]YouTubeAsk, 0, len(s.ytAsks)), s.ytAsks...),
		YouTubeRecent: append(make([]YouTubeRecent, 0, len(s.ytRecent)), s.ytRecent...),
		Update:        s.update,
		Notices:       append(make([]Notice, 0, len(s.notices)), s.notices...),
		Paused:        s.paused,
		Version:       s.version,
		Yandex:        s.yandex,
		Twitch:        s.twitch,
		Redemptions:   append(make([]RedemptionView, 0, len(s.redemptions)), s.redemptions...),
		Session:       s.session,
		Bans:          append(make([]Ban, 0, len(s.bans)), s.bans...),
		YouTube:       s.youtube,
		Tunnel:        s.tunnel,
		Widget:        s.widget,
	}
	for _, name := range s.order {
		snap.Connections = append(snap.Connections, s.conns[name])
	}
	if s.now != nil {
		now := *s.now
		snap.Now = &now
	}
	if s.lastPlayed != nil {
		last := *s.lastPlayed
		snap.LastPlayed = &last
	}
	return snap
}

// AudioDevice — устройство звука для заказов с YouTube: ID уходит в mpv,
// Name видит стример. ID вида «wasapi/{1a7e…}» человеку ничего не говорит.
type AudioDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
