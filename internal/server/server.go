// Package server — локальная панель управления и виджет для OBS.
//
// Слушаем строго 127.0.0.1: наружу порт не открывается никогда. Статика зашита
// в бинарник через go:embed, поэтому распространяется один .exe без папок рядом.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/donations"
	"songrequest-yandex/internal/links"
	"songrequest-yandex/internal/localapp"
	"songrequest-yandex/internal/logx"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/player"
	"songrequest-yandex/internal/queue"
	"songrequest-yandex/internal/smtc"
	"songrequest-yandex/internal/store"
	"songrequest-yandex/internal/twitch"
	"songrequest-yandex/internal/yandex"
	"songrequest-yandex/internal/youtube"
)

//go:embed all:web
var webFS embed.FS

// Deps — всё, что панели нужно для работы. Отдельной структурой, чтобы
// добавление Twitch и донатов не переписывало сигнатуру каждый раз.
type Deps struct {
	State   *app.State
	Cfg     *config.File
	Log     *logx.Logger
	Yandex  *yandex.Client
	Twitch  *twitch.Client
	DB      *store.DB
	Secrets Secrets
	DataDir string
	Version string
}

// Secrets — хранилище паролей Windows. Интерфейс, а не *secrets.Store, чтобы
// тесты не лезли в «Диспетчер учётных данных» настоящей машины.
type Secrets interface {
	PutJSON(name string, v any) error
	GetJSON(name string, v any) error
	Delete(name string) error
}

// Server — HTTP-сервер панели.
type Server struct {
	state   *app.State
	cfg     *config.File
	log     *logx.Logger
	ym      *yandex.Client
	twitch  *twitch.Client
	db      *store.DB
	secrets Secrets
	// matchCache помнит, чем закончился поиск по такому же запросу.
	matchCache *match.Cache
	queue      *queue.Queue
	player     *player.Player

	donations      *donations.Hub
	donationAlerts *donations.DonationAlerts

	ytTools *youtube.Tools
	youtube *youtube.Player
	ymLinks *links.YandexReader
	odesli  *links.Odesli
	dataDir string
	version string

	http *http.Server
	ln   net.Listener
	addr string

	// probeOnce держит автоматическую проверку поиска: один раз за запуск.
	probeOnce sync.Once

	// covers — обложки, скачанные для виджета. См. cover.go.
	covers covers

	// introShown — показывали ли уже заставку в этом запуске. См. handleSetup:
	// заставка полагается на запуск приложения, а не на загрузку страницы.
	introShown atomic.Bool

	// localTrack подменяет чтение играющего трека у программы Spotify.
	// Пусто — читаем по-настоящему. Заполняется только в проверках.
	localTrack func() (localapp.Track, localapp.Status)

	// panelTrack подменяет чтение из системной панели управления медиа
	// Windows. Пусто — читаем по-настоящему.
	//
	// Нужно ровно затем же, зачем localTrack: на машине разработчика Spotify
	// тоже запущен, и без подмены проверки ловили бы его настоящий трек
	// вместо выдуманного. Без этого четыре проверки своей музыки начали
	// падать в ту же секунду, как приложение научилось читать панель.
	panelTrack func() (smtc.Now, bool, error)

	// panels — сколько панелей открыто прямо сейчас. Пока хоть одна открыта,
	// Spotify спрашивается раз в секунду: стример может перемотать трек в
	// самом Spotify, и полоса в панели обязана поехать следом сразу.
	// Виджет в OBS сюда не считается — он висит весь стрим, и держать из-за
	// него секундный опрос значит платить запросами за картинку, которая
	// меняется раз в три минуты.
	panels atomic.Int64

	// showWindow — как показать окно программы. Кладёт сюда main, когда окно
	// создано. В режиме «панель в браузере» остаётся пустым: показывать
	// нечего, и запрос честно отвечает отказом.
	showWindow atomic.Pointer[func()]

	// quit — как закрыть программу. Кладёт сюда main. Нужно обновлению: оно
	// подменяет .exe и должно запустить новую копию вместо себя.
	quit atomic.Pointer[func()]

	// openAuth — как открыть страницу входа в Spotify внутри приложения,
	// через поднятый обход. Кладёт сюда main, когда окно есть. Пусто — вход
	// открывается в браузере, как раньше.
	openAuth atomic.Pointer[func(url string, onToken func(string)) error]

	// snapMu защищает снимок состояния Spotify. На этом этапе его снимают
	// кнопкой из панели; дальше это будет делать очередь заказов.
	snapMu sync.Mutex
	snap   *yandex.Snapshot

	// mu защищает то, что заполняется по ходу подключения Twitch.
	mu       sync.Mutex
	rewardID string
	// playlistRewardID — вторая награда, за плейлист. Пусто, если она
	// выключена в настройках или создать её не вышло.
	playlistRewardID string
	eventsRunning    bool
	// eventsReward — награда, на которую подписана живая подписка. Если она
	// разошлась с текущей, подписку надо поднимать заново.
	eventsReward string
	stopEvents   context.CancelFunc
	ctx          context.Context

	// ynisonWas, ynisonDevice — что карточка Яндекса показывала о связи с
	// очередью в последний раз. См. onYnisonChange.
	ynisonWas    bool
	ynisonDevice string

	// skipActor — кто оборвал текущий заказ. Живёт до конца этого заказа:
	// историю пишет плеер, а имя человека знает только панель.
	skipActor string

	// skipRefund — вернуть ли баллы за оборванный заказ.
	//
	// Раньше скип баллы не возвращал вовсе, и это была самая частая жалоба
	// зрителей: заказ оборвали на десятой секунде, а баллы списаны целиком.
	// Теперь возвращаются всегда, но признак всё равно нужен: заказ уходит с
	// эфира и по другим причинам (перескочили в самом Spotify, заказ бросили
	// на середине), и решение «возвращать или нет» принимает то место,
	// которое эту причину знает.
	skipRefund bool

	// lastSkip — заказ, оборванный последним, и когда это случилось.
	// Нужен команде «!вернуть»: скипнули не то, и трек надо поставить обратно.
	// Живёт до следующего скипа.
	lastSkip     *queue.Item
	lastSkipAt   time.Time
	lastSkipWho  string
	lastSkipBack bool

	// ownPaused — стоит ли на паузе музыка самого стримера.
	//
	// Спросить об этом Spotify нельзя: программа Spotify заголовок окна на
	// паузе посреди трека не меняет, а лишний запрос по сети — та самая
	// частота, из-за которой заказы однажды встали на четыре часа. Поэтому
	// помним своё же нажатие: приложение само эту паузу и поставило.
	// Сбрасывается, когда трек сменился, — значит музыка снова идёт.
	ownPaused atomic.Bool

	// playing — что сейчас в эфире, в виде «последнего трека». Нужно только
	// затем, чтобы заметить момент, когда трек ушёл: см. noteLastPlayed.
	playing *app.LastPlayed

	// ownNow — то, что стример слушает сам между заказами. Показывается в
	// виджете, когда очередь пуста.
	ownNow *app.NowPlaying

	// playlists — заказанные плейлисты, которые ждут решения стримера или
	// модератора. См. internal/server/playlists.go: сам собой плейлист в
	// эфир не идёт никогда.
	playlists []pendingPlaylist
	// playlistNo — счётчик номеров для этих плейлистов. Номер нужен командам
	// в чате: «одобрить 2». В номера очереди он не превращается — очередь
	// двигается, а этот номер обязан пережить любое движение.
	playlistNo int64

	// ytAsks — заказы, которых нет в Spotify и которые ждут решения: играть
	// ли их с YouTube. См. ytask.go.
	ytAsks  []ytAsk
	ytAskNo int64
}

// New поднимает слушатель на 127.0.0.1. Если желаемый порт занят, берём любой
// свободный — приложение не должно отказываться стартовать из-за этого.
func New(d Deps) (*Server, error) {
	state, cfg, log := d.State, d.Cfg, d.Log
	want := cfg.Get().Port

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", want))
	if err != nil && want != 0 {
		log.Warn("порт занят, беру свободный", "порт", want)
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return nil, fmt.Errorf("не удалось занять локальный порт: %w", err)
	}

	s := &Server{
		state:   state,
		cfg:     cfg,
		log:     log,
		ym:      d.Yandex,
		twitch:  d.Twitch,
		db:      d.DB,
		secrets: d.Secrets,
		dataDir: d.DataDir,
		version: d.Version,
		ln:      ln,
		addr:    "http://" + ln.Addr().String(),
	}

	if d.DB != nil {
		s.matchCache = match.NewCache(d.DB.SQL())
		s.queue = queue.New(d.DB.SQL())
		s.setupPlayer(d.Cfg)
		// Что играло в прошлый раз — читается сразу: панель на пустом месте
		// должна сказать что-то осмысленное ещё до первого заказа.
		s.loadLastPlayed()
		// Плейлисты, оставшиеся без решения. Забыть их при перезапуске
		// значило бы молча съесть чужие баллы: заказ оплачен, а решать по
		// нему уже нечего.
		s.loadPlaylists()
		s.syncPlaylists()
	}
	s.setupDonations()
	s.ymLinks = links.NewYandexReader()
	s.odesli = links.NewOdesli()
	// Своя версия и адрес выпусков известны сразу, без всякой сети: панель
	// показывает их ещё до того, как человек нажмёт «Проверить обновления».
	s.syncUpdateInfo()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	mux.HandleFunc("GET /{$}", s.page(sub, "index.html"))
	mux.HandleFunc("GET /widget", s.page(sub, "widget.html"))
	mux.HandleFunc("GET /cover", s.handleCover)
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/setup/done", s.handleSetupDone)
	mux.HandleFunc("POST /api/window/show", s.handleShowWindow)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("POST /api/config", s.handleSetConfig)
	mux.HandleFunc("GET /api/diag/export", s.handleDiagExport)
	// Справочник команд для модераторов. Без ?save=1 — текст для панели,
	// с ним — файл, который стример пересылает модераторам.
	mux.HandleFunc("GET /api/mods", s.handleMods)

	mux.HandleFunc("POST /api/yandex/login", s.handleYandexLogin)
	mux.HandleFunc("POST /api/yandex/token", s.handleYandexToken)
	mux.HandleFunc("POST /api/yandex/logout", s.handleYandexLogout)
	mux.HandleFunc("POST /api/yandex/check", s.handleYandexCheck)
	mux.HandleFunc("GET /api/yandex/playlists", s.handleYandexPlaylists)
	mux.HandleFunc("POST /api/yandex/snapshot", s.handleYandexSnapshot)
	mux.HandleFunc("POST /api/yandex/restore", s.handleYandexRestore)

	mux.HandleFunc("POST /api/twitch/login", s.handleTwitchLogin)
	mux.HandleFunc("POST /api/twitch/logout", s.handleTwitchLogout)
	mux.HandleFunc("POST /api/redemptions/{id}", s.handleRedemptionAction)

	mux.HandleFunc("POST /api/queue/skip", s.handleSkip)

	// Управление играющим заказом. Всё это команды, по одному запросу на
	// нажатие: см. internal/server/playercontrol.go.
	mux.HandleFunc("POST /api/player/play", s.handlePlayerPlay)
	mux.HandleFunc("POST /api/player/pause", s.handlePlayerPause)
	mux.HandleFunc("POST /api/player/seek", s.handlePlayerSeek)
	mux.HandleFunc("POST /api/player/volume", s.handlePlayerVolume)
	mux.HandleFunc("POST /api/player/prev", s.handlePlayerPrev)
	// «Дальше»: у заказа это скип, у своей музыки — следующий трек плейлиста.
	mux.HandleFunc("POST /api/player/next", s.handlePlayerNext)
	mux.HandleFunc("POST /api/player/repeat", s.handlePlayerRepeat)
	mux.HandleFunc("POST /api/queue/{id}/remove", s.handleQueueRemove)
	mux.HandleFunc("POST /api/queue/{id}/top", s.handleQueueTop)
	mux.HandleFunc("POST /api/queue/{id}/fix", s.handleQueueFix)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/queue/reorder", s.handleQueueReorder)
	mux.HandleFunc("POST /api/update/check", s.handleUpdateCheck)
	mux.HandleFunc("POST /api/obs/check", s.obsHandler(obsCheck))
	mux.HandleFunc("POST /api/obs/fit", s.obsHandler(obsFit))
	mux.HandleFunc("POST /api/obs/add", s.obsHandler(obsAdd))
	mux.HandleFunc("POST /api/update/install", s.handleUpdateInstall)

	mux.HandleFunc("POST /api/queue/clear", s.handleQueueClear)

	// Плейлисты, ждущие решения. См. internal/server/playlists.go.
	mux.HandleFunc("POST /api/playlists/{id}/approve", s.handlePlaylistApprove)
	mux.HandleFunc("POST /api/playlists/{id}/reject", s.handlePlaylistReject)
	// Заказы с YouTube, ждущие решения. См. ytask.go.
	mux.HandleFunc("POST /api/youtube-asks/{id}/play", s.handleYouTubeAsk(true))
	mux.HandleFunc("POST /api/youtube-asks/{id}/reject", s.handleYouTubeAsk(false))
	mux.HandleFunc("POST /api/youtube/test-sound", s.handleYouTubeTestSound)
	mux.HandleFunc("POST /api/queue/pause", s.handlePause)
	mux.HandleFunc("POST /api/bans", s.handleBan)
	mux.HandleFunc("GET /api/bans", s.handleBans)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/modlog", s.handleModLog)

	mux.HandleFunc("POST /api/donations/login", s.handleDonationAlertsLogin)
	mux.HandleFunc("POST /api/donations/logout", s.handleDonationAlertsLogout)
	mux.HandleFunc("POST /api/donations/token", s.handleDonationsToken)
	mux.HandleFunc("GET /donations/callback", s.handleDonationAlertsCallback)

	// Виджет должен получить оформление сразу при подключении, ещё до первой
	// правки настроек.
	s.state.SetWidget(s.cfg.Get().Widget)

	s.http = &http.Server{
		Handler:           s.guard(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

// Addr — адрес панели, его показываем в консоли и открываем в браузере.
func (s *Server) Addr() string { return s.addr }

// baseContext — контекст жизни приложения. Фоновые задачи (ожидание кода,
// подписка на события) должны переживать отдельный HTTP-запрос, но умирать
// вместе с приложением.
func (s *Server) baseContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// Serve обслуживает запросы до отмены контекста.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.http.Shutdown(shutCtx)
	}()

	err := s.http.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// guard закрывает две дыры разом: запросы с чужим Host (DNS rebinding, когда
// сайт в браузере резолвит свой домен в 127.0.0.1 и стучится к нам) и кэш
// страниц панели, из-за которого после обновления показывалась бы старая версия.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			failText(w, http.StatusForbidden, "Запрос не с этого компьютера.")
			return
		}

		// Origin у любого запроса, изменяющего состояние, обязан быть нашим.
		//
		// Проверки одного Host мало. Панель открыта в обычном браузере
		// стримера, а браузер честно исполняет запросы, которые ему велела
		// сделать любая другая вкладка. Сайт, открытый в соседней вкладке,
		// мог отправить нам форму или fetch — Host в таком запросе всё равно
		// наш, — и очистить очередь, разлогинить Spotify или, что хуже
		// всего, записать в настройки свой адрес прокси: после этого все
		// запросы к Spotify вместе с ключом доступа пошли бы через чужой
		// сервер.
		//
		// Браузеры на межсайтовый POST заголовок Origin шлют всегда, поэтому
		// проверять его достаточно; а на своих запросах он равен нашему
		// адресу. Если Origin нет вовсе (старый браузер, curl, наш же код) —
		// пропускаем: подделать так может только программа на этом же
		// компьютере, а у неё и без нас есть все права.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" && !localOrigin(origin) {
				s.log.Warn("запрос с чужого сайта отклонён", "origin", origin, "путь", r.URL.Path)
				failText(w, http.StatusForbidden, "Запрос с чужого сайта.")
				return
			}
		}

		w.Header().Set("Cache-Control", "no-store")
		// Чтобы панель нельзя было спрятать в прозрачном кадре на чужом сайте
		// и подловить нажатие стримера.
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}

// localHost сообщает, обращаются ли к нам по нашему собственному адресу.
func localHost(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
	}
	return host == "127.0.0.1" || host == "localhost" || host == "[::1]" || host == "::1"
}

// localOrigin разбирает заголовок Origin и говорит, наш ли это адрес.
func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return localHost(u.Host)
}

func (s *Server) page(sub fs.FS, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			http.Error(w, "страница не найдена", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.state.Snapshot())
}

// OnShowWindow говорит панели, как показать окно программы.
func (s *Server) OnShowWindow(f func()) { s.showWindow.Store(&f) }

// OnOpenAuth говорит панели, как открыть вход в Яндекс в своём окне.
// Окно само вынимает токен из адреса и отдаёт его onToken.
func (s *Server) OnOpenAuth(f func(url string, onToken func(string)) error) { s.openAuth.Store(&f) }

// handleShowWindow достаёт окно из трея.
//
// Зовёт его не панель, а вторая копия приложения: человек, не найдя окна на
// экране, запускает .exe ещё раз. Вместо второй программы на ту же базу
// поднимаем окно у первой — так это и выглядит для человека: «запустил, и оно
// открылось».
func (s *Server) handleShowWindow(w http.ResponseWriter, r *http.Request) {
	show := s.showWindow.Load()
	if show == nil {
		failText(w, http.StatusServiceUnavailable, "Окна нет, панель открыта в браузере.")
		return
	}
	(*show)()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.cfg.Get())
}

func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	// Начинаем с текущих настроек, а не с пустой структуры.
	//
	// Раньше сюда разбирался нулевой config.Config, и всё, чего не оказалось
	// в теле запроса, молча становилось нулём и уезжало на диск. Панель
	// показывает не все поля: пороги подбора и веса оценок она не знает
	// вовсе. Достаточно было один раз сохранить настройки из старой вкладки
	// (или из чужого скрипта), и match_accept с match_maybe обнулялись —
	// после чего текстовый заказ либо не находил ничего никогда, либо
	// принимал первый попавшийся трек. Починить это из панели было нельзя:
	// полей в ней нет, а переживало оно перезапуск.
	current := s.cfg.Get()
	incoming := current
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&incoming); err != nil {
		failText(w, http.StatusBadRequest, "Не разобрал настройки.")
		return
	}

	// Порт меняется только при перезапуске, поэтому текущий сохраняем как есть.
	incoming.Port = current.Port
	// Пустой знак команд означал бы, что командой считается любое сообщение в
	// чате. Пустое поле в панели — это «оставь как было», а не «убери».
	if incoming.CommandPrefix = strings.TrimSpace(incoming.CommandPrefix); incoming.CommandPrefix == "" {
		incoming.CommandPrefix = "!"
	}
	// А пороги подбора чиним, даже если их прислали испорченными: заказ по
	// тексту не должен зависеть от того, что кто-то записал в config.json.
	incoming.NormalizeMatching()

	incoming.Widget.Normalize()

	// Название и цена награды живут не только в конфиге, но и на самом канале.
	// Раньше они доезжали туда лишь при следующем запуске: стример менял цену,
	// на канале оставалась старая, и выглядело это как «настройки не работают».
	rewardChanged := incoming.RewardTitle != current.RewardTitle ||
		incoming.RewardCost != current.RewardCost
	// Ключи донат-сервисов: вставил в панели — подключение обязано подняться
	// сейчас, а не после перезапуска приложения.
	donatePayChanged := incoming.DonatePayKey != current.DonatePayKey
	donateXChanged := incoming.DonateXKey != current.DonateXKey
	// Сменили формат кадра — источник в OBS должен поменяться сразу, а не
	// по отдельной кнопке. Сменили порт или пароль — тоже переподключаемся:
	// человек ждёт, что статус в панели ответит на новый пароль.
	obsChanged := incoming.Widget.Frame != current.Widget.Frame ||
		incoming.OBSPort != current.OBSPort || incoming.OBSPassword != current.OBSPassword

	if err := s.cfg.Update(func(c *config.Config) { *c = incoming }); err != nil {
		s.log.Error("сохранение настроек", "ошибка", err)
		failText(w, http.StatusInternalServerError, "Не смог сохранить настройки.")
		return
	}
	// Обе карточки: сохранили Client ID Twitch — лампочка Twitch обязана
	// перестать говорить «Не настроено» прямо сейчас, иначе это выглядит
	// так, будто сохранение не сработало.
	if rewardChanged {
		go s.pushReward()
	}
	if donatePayChanged {
		s.donations.Restart("DonatePay")
	}
	if donateXChanged {
		s.donations.Restart("DonateX")
	}
	if obsChanged {
		s.fitOBSLater()
	}
	// Настройки, которые раньше читались только при запуске: задержка
	// возврата, «дожидаться конца трека», устройство и браузер для YouTube.
	// Подпись в панели обещала, что они применятся к следующему заказу, —
	// теперь это правда.
	s.applyPlayerSettings()
	// Адрес обновлений мог поменяться — панель должна показать новый.
	s.syncUpdateInfo()
	s.applyYouTubeSettings()
	s.syncYandexInfo()
	s.syncTwitchInfo()
	// Оформление виджета едет в OBS через то же состояние: правка в панели
	// видна на сцене сразу, перезагружать источник не нужно.
	s.state.SetWidget(s.cfg.Get().Widget)
	s.state.Notify("info", "Настройки сохранены")
	writeJSON(w, s.cfg.Get())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
