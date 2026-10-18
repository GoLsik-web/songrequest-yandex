// Package config хранит настройки приложения в JSON-файле рядом с базой данных.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ResumeFailMode — что делать, если вернуть Spotify в исходное состояние не удалось.
type ResumeFailMode string

const (
	// ResumeNothing — ничего не включать, только показать уведомление в панели.
	ResumeNothing ResumeFailMode = "nothing"
	// ResumeFallbackPlaylist — включить запасной плейлист, выбранный в настройках.
	// Режим по умолчанию: он самый предсказуемый, стример заранее знает, что заиграет.
	ResumeFallbackPlaylist ResumeFailMode = "playlist"
	// ResumeArtistRadio — включить треки последнего игравшего артиста.
	// Настоящее радио Spotify через API недоступно: эндпоинт рекомендаций
	// закрыт для новых приложений. Это ближайшая замена, которая работает.
	ResumeArtistRadio ResumeFailMode = "radio"
)

// Config — всё, что стример может настроить. Поля с тегом json попадают в файл.
type Config struct {
	// Локальный сервер
	Port int `json:"port"` // 0 = выбрать свободный порт

	// Window — размер и положение окна программы с прошлого запуска.
	// Пустое (все нули) означает «открыть по умолчанию, посередине экрана».
	Window Window `json:"window"`
	// TrayHintShown — показывали ли уже подсказку «программа свернулась к
	// часам». Один раз объяснить надо: Windows 11 прячет новые значки в
	// список под стрелкой, и человек уверен, что программа закрылась.
	TrayHintShown bool `json:"tray_hint_shown"`

	// SetupDone — прошёл ли человек первую настройку по шагам.
	//
	// Пока признака нет, приложение после заставки открывает не панель, а
	// мастер настройки: без ключей от Spotify и Twitch панель всё равно
	// пустая, а куда в ней нажимать, новому человеку неоткуда узнать.
	// Мастер можно пройти заново из настроек — люди ломают настройку и
	// хотят повторить, а не разбираться, что именно они сломали.
	SetupDone bool `json:"setup_done"`

	// Обход блокировок. Сам ключ здесь не лежит — он секрет и хранится в
	// хранилище паролей Windows (internal/secrets). Тут только то, что можно
	// показывать: включён ли обход, что за ключ вставлен (без секретной части)
	// и какой сервер сработал в прошлый раз.
	TunnelOn      bool   `json:"tunnel_on"`
	TunnelKeyHint string `json:"tunnel_key_hint"`
	TunnelServer  string `json:"tunnel_server"`
	// TunnelToolPath — путь к xray.exe, если человек положил его руками.
	// Пусто — приложение скачает и будет держать свою копию.
	TunnelToolPath string `json:"tunnel_tool_path"`
	// TunnelBad — серверы, через которые Spotify отказал по стране, и когда
	// это случилось. Тут только подписи (имя, вид, адрес и порт) — секретов в
	// них нет, они и в панели показываются.
	//
	// Зачем хранить: перезапуск приложения не делает негодный сервер годным.
	// Раньше список забывался вместе с процессом, и вечер начинался с тех же
	// самых граблей — перебор упирался в те же серверы. Пометки истекают сами
	// (см. badFor в internal/tunnel).
	TunnelBad map[string]time.Time `json:"tunnel_bad,omitempty"`

	// Spotify
	SpotifyClientID string `json:"spotify_client_id"`
	// SpotifyProxy — посредник только для запросов к Spotify. Пусто — прямое
	// соединение. Нужен там, где до Spotify не достучаться напрямую; всё
	// остальное (Twitch, чат, донаты) при этом идёт своим путём и не теряет
	// в скорости, в отличие от системного VPN.
	SpotifyProxy string `json:"spotify_proxy"`

	// Twitch
	TwitchClientID   string `json:"twitch_client_id"`
	RewardTitle      string `json:"reward_title"`
	RewardCost       int    `json:"reward_cost"`
	CommandPrefix    string `json:"command_prefix"`
	AutoCreateReward bool   `json:"auto_create_reward"`

	// Заказ плейлиста — вторая награда на канале.
	//
	// Отдельная, а не «кинь плейлист в ту же награду», по двум причинам.
	// Первая: цена. За плейлист платят один раз и заметно меньше, чем за те
	// же треки поштучно, — иначе смысла в нём нет. Вторая: плейлист не идёт
	// в эфир сам. Он ждёт, пока стример или модератор его одобрит: сорок
	// минут чужой музыки подряд — это решение хозяина эфира, а не зрителя.
	PlaylistReward      bool   `json:"playlist_reward"`
	PlaylistRewardTitle string `json:"playlist_reward_title"`
	// PlaylistMaxTracks — сколько треков берём из плейлиста. Остальные не
	// попадают в очередь вовсе, и зрителю об этом говорится в чате.
	PlaylistMaxTracks int `json:"playlist_max_tracks"`
	// PlaylistDiscount — насколько плейлист выгоднее поштучного заказа, в
	// процентах. Цена считается сама: цена трека × число треков − скидка.
	PlaylistDiscount int `json:"playlist_discount"`
	// PlaylistCost — своя цена вместо посчитанной. Ноль означает «считай
	// сам»; так оно и стоит, пока стример не захочет иначе.
	PlaylistCost int `json:"playlist_cost"`

	// Донаты
	DonationAlertsClientID string  `json:"donationalerts_client_id"`
	DonatePayKey           string  `json:"donatepay_key"`
	DonateXKey             string  `json:"donatex_key"`
	DonationMin            float64 `json:"donation_min"` // от какой суммы принимаем заказ

	// Лимиты заказов
	MaxTrackSeconds  int  `json:"max_track_seconds"`
	MaxPerUser       int  `json:"max_per_user"`
	DonationPriority bool `json:"donation_priority"`

	// Возврат контекста Spotify
	ResumeFail         ResumeFailMode `json:"resume_fail_mode"`
	FallbackPlaylistID string         `json:"fallback_playlist_id"`
	FallbackPlaylist   string         `json:"fallback_playlist_name"` // для показа в панели
	ResumeDelaySeconds int            `json:"resume_delay_seconds"`
	// WaitForCurrent — не обрывать трек, который играет у стримера: первый
	// заказ дожидается его конца. По умолчанию включено — обрывать музыку
	// на середине хуже, чем подождать.
	WaitForCurrent bool `json:"wait_for_current"`

	// Матчинг: пороги уверенности и веса. Вынесены наружу, чтобы крутить без пересборки.
	MatchAccept float64      `json:"match_accept"` // выше — берём молча
	MatchMaybe  float64      `json:"match_maybe"`  // между — «неточное совпадение»
	MatchWeight MatchWeights `json:"match_weights"`

	// YouTube-фоллбэк
	AudioDevice string `json:"audio_device"` // имя устройства для mpv, пусто = системное
	// YouTubeBrowser — откуда брать куки, когда YouTube требует подтвердить,
	// что запросы не от робота. Пусто = приложение переберёт браузеры само.
	YouTubeBrowser string `json:"youtube_browser"`
	YtDlpPath      string `json:"ytdlp_path"` // пусто = встроенная копия
	MpvPath        string `json:"mpv_path"`
	// YouTubeVolume — громкость заказов с YouTube в процентах от той, что
	// стоит в Spotify. Сто означает «ровно как Spotify», пятьдесят — вдвое
	// тише. Считаем именно от Spotify, а не сами по себе: заказ звучит
	// вперемешку с музыкой стримера, и громкости обязаны совпадать.
	//
	// Появилось после 27.08: mpv запускался вообще без --volume, то есть на
	// сто процентов, и первый же заказ с YouTube оглушил эфир.
	YouTubeVolume int `json:"youtube_volume"`

	// YouTubeMode — что делать, когда трека нет в Spotify, а на YouTube он
	// нашёлся:
	//
	//	"auto" — включать самим и показать уведомление;
	//	"ask"  — каждый раз спрашивать стримера;
	//	"off"  — не играть, вернуть баллы.
	//
	// Появилось 11.10. Раньше было только «auto», и молча: друг владельца
	// жаловался, что заказ «играл непонятно где». По умолчанию «ask» —
	// так стример точно ничего не пропустит.
	YouTubeMode string `json:"youtube_mode"`
	// YouTubeAskSeconds — сколько ждать решения в режиме «ask». Ноль — ждать,
	// пока не ответят.
	YouTubeAskSeconds int `json:"youtube_ask_seconds"`
	// YouTubeAskElse — что делать, если не ответили в срок: "skip" или "play".
	YouTubeAskElse string `json:"youtube_ask_else"`

	// Фильтр «это не музыка»
	RejectKeywords []string `json:"reject_keywords"`

	// UpdateRepo — откуда брать обновления: «имя/репозиторий» на GitHub.
	//
	// Хранится в настройках, а не вшито намертво, по простой причине:
	// репозиторий может переехать, а сборка у людей на руках останется старой
	// — и обновиться она уже не сможет никогда. Значение по умолчанию всё же
	// есть, чтобы кнопка работала сразу после установки.
	UpdateRepo string `json:"update_repo"`

	// Виджет для OBS
	Widget Widget `json:"widget"`
}

// MatchWeights — веса слагаемых в оценке кандидата из Spotify.
type MatchWeights struct {
	Title      float64 `json:"title"`
	Artist     float64 `json:"artist"`
	Duration   float64 `json:"duration"`
	Popularity float64 `json:"popularity"`
	Version    float64 `json:"version"`
}

// Defaults возвращает конфиг со значениями по умолчанию.
func Defaults() Config {
	return Config{
		Port:                8977,
		RewardTitle:         "Заказ трека",
		RewardCost:          1000,
		CommandPrefix:       "!",
		UpdateRepo:          "GoLsik-web/songrequest",
		AutoCreateReward:    true,
		PlaylistRewardTitle: "Заказ плейлиста",
		PlaylistMaxTracks:   5,
		PlaylistDiscount:    20,
		MaxTrackSeconds:     8 * 60,
		MaxPerUser:          3,
		DonationPriority:    true,
		DonationMin:         100,
		ResumeFail:          ResumeFallbackPlaylist,
		ResumeDelaySeconds:  1,
		WaitForCurrent:      true,
		YouTubeVolume:       100,
		YouTubeMode:         "ask",
		YouTubeAskSeconds:   60,
		YouTubeAskElse:      "skip",
		MatchAccept:         0.80,
		MatchMaybe:          0.55,
		MatchWeight: MatchWeights{
			Title:      1.0,
			Artist:     0.8,
			Duration:   0.6,
			Popularity: 0.1,
			Version:    1.0,
		},
		Widget: DefaultWidget(),
		RejectKeywords: []string{
			"нарезка", "нарезки", "подкаст", "стрим", "compilation", "mix",
			"1 hour", "1 час", "10 hours", "podcast", "livestream", "best moments",
		},
	}
}

// Window — где и какого размера было окно программы, когда его закрыли.
//
// Живёт в настройках, а не вычисляется заново: стример один раз растянул окно
// под свой экран, и каждый следующий запуск обязан открыться так же. В панели
// этих полей нет — их пишет само окно.
type Window struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`
}

// File — конфиг на диске. Все обращения идут через него, поэтому чтение из
// панели и запись из настроек не мешают друг другу.
//
// Broken — путь к отложенному в сторону испорченному файлу, если он был.
// Пустая строка означает «всё в порядке». Сказать об этом человеку должен
// тот, кто открывает конфиг: молчаливая потеря настроек хуже поломки.
type File struct {
	mu   sync.RWMutex
	cfg  Config
	path string

	// Broken — куда отложен испорченный config.json, если он был.
	Broken string

	// saveMu отдельный: он держится на всю запись файла, а mu — только на
	// снятие копии настроек. Один замок на оба дела заставил бы панель ждать
	// диска ради обычного чтения.
	saveMu sync.Mutex
}

// Load читает конфиг из dir/config.json. Если файла нет — создаёт со значениями
// по умолчанию. Неизвестные поля в файле игнорируются, отсутствующие остаются
// дефолтными, поэтому старый конфиг переживает обновление приложения.
func Load(dir string) (*File, error) {
	f := &File{cfg: Defaults(), path: filepath.Join(dir, "config.json")}

	data, err := os.ReadFile(f.path)
	switch {
	case os.IsNotExist(err):
		if err := f.Save(); err != nil {
			return nil, err
		}
		return f, nil
	case err != nil:
		return nil, fmt.Errorf("чтение настроек: %w", err)
	}

	if err := json.Unmarshal(data, &f.cfg); err != nil {
		// Битый файл не должен превращать приложение в неоткрывающееся.
		//
		// Раньше отсюда возвращалась ошибка, и человек, который не
		// программист, получал окно «Ошибка: настройки повреждены»,
		// закрывающееся по Enter, — и всё. А недописаться файл может от
		// одного внезапного выключения компьютера. Отодвигаем испорченный
		// в сторону (вдруг пригодится) и стартуем на значениях по умолчанию.
		broken := f.path + ".битый"
		os.Remove(broken)
		if err := os.Rename(f.path, broken); err != nil {
			return nil, fmt.Errorf("настройки повреждены (%s): %w", f.path, err)
		}
		f.cfg = Defaults()
		f.Broken = broken
		if err := f.Save(); err != nil {
			return nil, err
		}
		return f, nil
	}
	// Конфиг мог быть от прошлой версии или поправлен руками: приводим в
	// рабочий вид сразу, а не когда виджет уже висит на стриме.
	f.cfg.Widget.Normalize()
	f.cfg.NormalizeMatching()
	return f, nil
}

// Save записывает конфиг атомарно: сначала во временный файл, потом переименование.
// Так недописанный файл не заменит рабочий, если приложение убьют посреди записи.
func (f *File) Save() error {
	// Запись целиком под своим замком, и вот почему. Панель сохраняет
	// настройки целым файлом, а стример успевает нажать две галочки подряд.
	// Без замка две записи шли одновременно в один и тот же .tmp: одна правка
	// молча пропадала, а на Windows переименование ещё и упиралось в чужой
	// открытый файл — «не смог сохранить настройки» на ровном месте.
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.mu.RLock()
	data, err := json.MarshalIndent(f.cfg, "", "  ")
	f.mu.RUnlock()
	if err != nil {
		return err
	}

	// Имя временного файла своё у каждой записи: если приложение убьют
	// посреди сохранения, чужой огрызок не помешает следующему запуску.
	tmp := fmt.Sprintf("%s.%d.tmp", f.path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("запись настроек: %w", err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("запись настроек: %w", err)
	}
	return nil
}

// Update меняет поля под блокировкой и сразу сохраняет результат на диск.
func (f *File) Update(fn func(*Config)) error {
	f.mu.Lock()
	fn(&f.cfg)
	f.mu.Unlock()
	return f.Save()
}

// Get отдаёт копию настроек для чтения без гонок.
func (f *File) Get() Config {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cp := f.cfg
	cp.RejectKeywords = append([]string(nil), f.cfg.RejectKeywords...)
	return cp
}

// NormalizeMatching чинит пороги и веса подбора, если их испортили.
//
// Панель эти поля не показывает, а испортить их легко: старая вкладка,
// присланная не целиком настройка, ручная правка config.json. Цена ошибки
// непропорционально велика — при нулях подбор либо принимает первый
// попавшийся трек, либо не находит ничего и никогда, и зритель получает
// «трека нет» на любой заказ. Чинить это из панели нельзя, а переживает
// оно перезапуск, поэтому проверяем при каждой записи.
func (c *Config) NormalizeMatching() {
	d := Defaults()

	// Ноль здесь законен — это «выключить звук заказов». А вот отсутствие
	// поля в старом config.json тоже даёт ноль, и тихий эфир после
	// обновления выглядел бы как поломка. Различить их нечем, поэтому
	// нижняя граница — единица: почти тишина, но слышно, что играет.
	if c.YouTubeMode != "auto" && c.YouTubeMode != "ask" && c.YouTubeMode != "off" {
		c.YouTubeMode = d.YouTubeMode
	}
	if c.YouTubeAskElse != "skip" && c.YouTubeAskElse != "play" {
		c.YouTubeAskElse = d.YouTubeAskElse
	}
	if c.YouTubeAskSeconds < 0 || c.YouTubeAskSeconds > 600 {
		c.YouTubeAskSeconds = d.YouTubeAskSeconds
	}
	if c.YouTubeVolume < 1 || c.YouTubeVolume > 100 {
		c.YouTubeVolume = d.YouTubeVolume
	}

	if c.MatchAccept <= 0 || c.MatchAccept > 1 {
		c.MatchAccept = d.MatchAccept
	}
	if c.MatchMaybe <= 0 || c.MatchMaybe > 1 {
		c.MatchMaybe = d.MatchMaybe
	}
	// «Берём молча» не может быть строже, чем «неточное совпадение»: иначе
	// один и тот же трек то принимается, то отвергается — смотря по тому,
	// на каком запросе он нашёлся.
	if c.MatchMaybe > c.MatchAccept {
		c.MatchMaybe = c.MatchAccept
	}

	// Все веса нулями означают «оценка у всех одинаковая», то есть подбора
	// нет вовсе. Одиночный ноль — законная настройка: так выключают признак.
	w := c.MatchWeight
	if w.Title+w.Artist+w.Duration+w.Popularity+w.Version <= 0 {
		c.MatchWeight = d.MatchWeight
	}
}

// EffectiveResumeMode — режим, который действительно сработает.
//
// Если выбран запасной плейлист, но сам плейлист не выбран, включать нечего:
// молча деградируем до «ничего не включать». Панель подсветит это в настройках,
// но посреди стрима приложение не должно ругаться на настройку.
func (c Config) EffectiveResumeMode() ResumeFailMode {
	if c.ResumeFail == ResumeFallbackPlaylist && c.FallbackPlaylistID == "" {
		return ResumeNothing
	}
	return c.ResumeFail
}

// ResumeModeIncomplete сообщает панели, что выбранный режим не настроен до конца.
func (c Config) ResumeModeIncomplete() bool {
	return c.ResumeFail == ResumeFallbackPlaylist && c.FallbackPlaylistID == ""
}

// Path — путь к файлу настроек, показываем его в панели.
func (f *File) Path() string { return f.path }
