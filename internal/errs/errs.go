// Package errs — ошибки с коротким кодом и человеческим текстом.
//
// Смысл кода: стример на своём компьютере видит в панели «SP-04» и просто
// называет его голосом, а разработчик по коду сразу знает место в программе.
// Текст при этом пишем так, чтобы человек понял, что делать, без слова «токен».
package errs

import "fmt"

// Code — короткий код ошибки, который видно в панели.
type Code string

const (
	// Настройки
	NoClientID   Code = "CFG-01" // не заполнен client_id Spotify
	ConfigSave   Code = "CFG-02" // не смогли сохранить настройки
	ConfigBroken Code = "CFG-03" // файл настроек повреждён

	// Spotify
	SpotifyAuthStart    Code = "SP-01" // не удалось открыть страницу входа
	SpotifyAuthDenied   Code = "SP-02" // вход отклонён или подделан ответ
	SpotifyAuthToken    Code = "SP-03" // не удалось завершить вход
	SpotifyAuthExpired  Code = "SP-04" // авторизация слетела, нужен повторный вход
	SpotifyNoPremium    Code = "SP-05" // на аккаунте нет Premium
	SpotifyNoDevice     Code = "SP-06" // нет устройства, где играть
	SpotifyUnreachable  Code = "SP-07" // Spotify не отвечает
	SpotifyRateLimit    Code = "SP-08" // Spotify просит подождать
	SpotifySnapshot     Code = "SP-09" // не удалось запомнить, что играло
	SpotifyRestore      Code = "SP-10" // не удалось вернуть воспроизведение
	SpotifyStale        Code = "SP-11" // за время заказа музыку переключили руками
	SpotifyNothing      Code = "SP-12" // возвращать нечего
	SpotifyBadResponse  Code = "SP-13" // Spotify ответил не тем, чего мы ждали
	SpotifyNoScope      Code = "SP-14" // не выданы права, нужен повторный вход
	SpotifyPlanUnknown  Code = "SP-15" // Spotify не сказал, какая подписка
	SpotifyCountry      Code = "SP-16" // Spotify недоступен в стране аккаунта
	SpotifyProxy        Code = "SP-17" // прокси для Spotify настроен неверно
	SpotifyForeignTrack Code = "SP-18" // трек не издан в стране аккаунта стримера
	SpotifySearchLimit  Code = "SP-19" // Spotify отдаёт меньше результатов, чем просим
	// SpotifyQuota — кончился дневной объём запросов на весь аккаунт
	// разработчика. Отдельно от SP-08 нарочно: у той пауза меряется
	// секундами и лечится темпом, у этой — часами, и лечится только
	// ожиданием. Человеку это разные новости.
	SpotifyQuota Code = "SP-20"

	// Twitch
	TwitchNoClientID   Code = "TW-01" // не заполнен client_id Twitch
	TwitchAuthStart    Code = "TW-02" // не удалось начать вход
	TwitchAuthPending  Code = "TW-03" // код не подтвердили вовремя
	TwitchAuthExpired  Code = "TW-04" // авторизация слетела
	TwitchNoAffiliate  Code = "TW-05" // на канале нет баллов
	TwitchReward       Code = "TW-06" // не удалось создать награду
	TwitchRefund       Code = "TW-07" // не удалось вернуть баллы
	TwitchUnreachable  Code = "TW-08" // Twitch не отвечает
	TwitchRateLimit    Code = "TW-09" // Twitch просит подождать
	TwitchEventSub     Code = "TW-10" // оборвалась подписка на события
	TwitchBadResponse  Code = "TW-11" // Twitch ответил не тем, чего мы ждали
	TwitchForeignAward Code = "TW-12" // награду создали не мы, баллы не вернуть
	TwitchChat         Code = "TW-13" // Twitch не пропустил сообщение в чат
	// TwitchScopes — вход выдан без части прав. Чаще всего не хватает чтения
	// чата: право появилось позже приложения, старый вход про него не знает —
	// заказы идут, команды в чате молчат.
	TwitchScopes Code = "TW-14"

	// Донаты
	DonationsNoClientID  Code = "DN-01" // не заполнен client_id сервиса
	DonationsAuth        Code = "DN-02" // вход не выполнен или слетел
	DonationsUnreachable Code = "DN-03" // сервис не отвечает
	DonationsBadResponse Code = "DN-04" // сервис ответил не тем
	DonationsTooSmall    Code = "DN-05" // донат меньше порога заказа

	// YouTube
	YouTubeNoTool      Code = "YT-01" // нет yt-dlp
	YouTubeNoMpv       Code = "YT-02" // нет mpv
	YouTubeNotFound    Code = "YT-03" // на YouTube ничего не нашлось
	YouTubePlay        Code = "YT-04" // не получилось включить
	YouTubeBadResponse Code = "YT-05" // yt-dlp ответил не тем
	YouTubeCookies     Code = "YT-06" // YouTube требует подтвердить, что мы не робот
	// YouTubeAskNone — решают судьбу заказа с YouTube, который уже не ждёт:
	// стример нажал кнопку ровно тогда, когда вышел срок.
	YouTubeAskNone Code = "YT-07"

	// Обход блокировок
	TunnelNoKey        Code = "OB-01" // ключ не вставлен или в нём ничего нет
	TunnelBadKey       Code = "OB-02" // ключ записан непонятно
	TunnelSubscription Code = "OB-03" // не удалось скачать список серверов по ссылке
	TunnelNoTool       Code = "OB-04" // не удалось получить программу обхода
	TunnelStart        Code = "OB-05" // обход не запустился
	TunnelCheck        Code = "OB-06" // через обход Spotify всё равно не отвечает
	// TunnelCountry — обход работает, но Spotify не пускает адрес этого
	// сервера: «unavailable in this country». Отдельный код от OB-06, потому
	// что лечится он по-другому — не подъёмом обхода заново, а сменой сервера.
	// Ловить его по тексту ошибки нельзя: текст переписывают, код — нет.
	TunnelCountry Code = "OB-07"

	// Управление воспроизведением.
	//
	// PlayerIdle — нажали кнопку плеера, когда заказ не играет. Не поломка, но
	// сказать надо: иначе кнопка выглядит сломанной.
	PlayerIdle Code = "PL-01"
	// PlayerNoTool — заказ играет запасным проигрывателем, а его уже нет.
	PlayerNoTool Code = "PL-02"
	// PlayerNothingBack — «вернуть» нажали, а возвращать нечего: за этот
	// запуск ничего не скипали, или скипнутый заказ уже вернули.
	PlayerNothingBack Code = "PL-03"

	// Заказ плейлиста.
	//
	// PlaylistNone — решают судьбу плейлиста, которого в ожидании нет:
	// его уже одобрили, отклонили или номер назвали не тот.
	PlaylistNone Code = "PLS-01"
	// PlaylistEmpty — плейлист одобрили, а сыграть из него нечего: ни один
	// трек не нашёлся ни в Spotify, ни на YouTube.
	PlaylistEmpty Code = "PLS-02"

	// Обновление приложения
	//
	// UpdateNoRepo — не сказано, откуда брать обновления, или репозитория нет.
	UpdateNoRepo Code = "UPD-01"
	// UpdateCheck — не удалось спросить GitHub про выпуски.
	UpdateCheck Code = "UPD-02"
	// UpdateNoFile — выпуск есть, а файла программы в нём нет.
	UpdateNoFile Code = "UPD-03"
	// UpdateDownload — не удалось скачать или распаковать сборку.
	UpdateDownload Code = "UPD-04"
	// UpdateApply — скачали, а подменить программу не вышло: чаще всего нет
	// прав на папку, где она лежит.
	UpdateApply Code = "UPD-05"

	// Яндекс.Музыка
	YandexRead         Code = "YM-01" // не удалось прочитать страницу трека
	YandexNoToken      Code = "YM-02" // не выполнен вход в Яндекс
	YandexAuthExpired  Code = "YM-03" // вход слетел, нужен повторный
	YandexUnreachable  Code = "YM-04" // Яндекс не отвечает
	YandexBadResponse  Code = "YM-05" // Яндекс ответил не тем, чего мы ждали
	YandexNoPlus       Code = "YM-06" // на аккаунте нет Плюса
	YandexRateLimit    Code = "YM-07" // Яндекс просит подождать
	YandexYnison       Code = "YM-08" // нет связи с Ynison (очередь приложения)
	YandexNoPlayer     Code = "YM-09" // Ynison на связи, но играющего устройства нет
	YandexPlaylist     Code = "YM-10" // не вышло записать в плейлист «Заказы»
	YandexSnapshot     Code = "YM-11" // не удалось запомнить, что играло
	YandexRestore      Code = "YM-12" // не удалось вернуть музыку на место
	YandexStale        Code = "YM-13" // за время заказа музыку переключили руками
	YandexNothing      Code = "YM-14" // возвращать нечего
	YandexNotAvailable Code = "YM-15" // трек есть, но недоступен для прослушивания

	// Диагностика
	DiagExport Code = "DIAG-01" // не собрался архив с логом
)

// Error — ошибка с кодом. Message показываем стримеру, cause уходит в лог.
type Error struct {
	Code    Code
	Message string
	cause   error
}

// New создаёт ошибку с кодом и понятным текстом.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Wrap оборачивает техническую ошибку в понятную.
func Wrap(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

func (e *Error) Error() string {
	if e.cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.cause)
}

// Unwrap открывает исходную ошибку для errors.Is и errors.As.
func (e *Error) Unwrap() error { return e.cause }

// UserText — то, что видит стример: «SP-04 · Слетела авторизация Spotify…».
func (e *Error) UserText() string {
	return fmt.Sprintf("%s · %s", e.Code, e.Message)
}

// CodeOf достаёт код из любой ошибки; если кода нет — пустая строка.
func CodeOf(err error) Code {
	var e *Error
	if As(err, &e) {
		return e.Code
	}
	return ""
}

// Describe разбирает ошибку на код и текст по отдельности. Панель показывает
// их разными элементами, поэтому склеивать их здесь нельзя — иначе код
// напечатается дважды.
//
// К тексту добавляется настоящая причина, если она у нас же и с кодом. Иначе
// получается разговор ни о чём: владелец нажал «Запомнить», увидел «SP-09 Не
// смог запомнить, что играло в Spotify» — и всё. А внутри лежало объяснение,
// ради которого код и писался: «Spotify временно ограничил приложение и просит
// долгую паузу (осталось 3ч56м)». Человек в это время думал на свежий обход
// блокировок и чинил не то.
func Describe(err error) (Code, string) {
	var e *Error
	if !As(err, &e) {
		return "", "Непонятная ошибка. Загляни в лог приложения."
	}
	text := e.Message
	if root := rootMessage(e); root != "" && root != e.Message {
		text += " " + root
	}
	return e.Code, text
}

// rootMessage достаёт самое внутреннее наше объяснение. Чужие ошибки (обрыв
// сети, отказ библиотеки) сюда не попадают: они написаны не для людей.
func rootMessage(e *Error) string {
	message := ""
	for cause := e.cause; cause != nil; {
		var inner *Error
		if !As(cause, &inner) {
			break
		}
		message = inner.Message
		cause = inner.cause
	}
	return message
}
