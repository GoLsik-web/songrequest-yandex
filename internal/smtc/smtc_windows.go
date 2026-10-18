//go:build windows

// Package smtc читает и переключает музыку через системную панель Windows.
//
// ЗАЧЕМ ЭТО ВООБЩЕ. Главное ограничение всего приложения — норма запросов к
// Spotify. Client ID стример заводит на своём аккаунте, режим у такого ключа
// всегда «разработка», а норма там тесная: 29.08 Spotify закрыл приложению
// доступ на четыре с половиной часа, 10.09 — на двадцать один. Расширенную
// норму с мая 2025 дают только организациям от четверти миллиона
// пользователей, а несколько ключей с июля 2026 делят один общий запас. То
// есть выпросить больше нельзя ни за какие деньги.
//
// Значит надо перестать спрашивать. И оказывается, почти всё, что приложение
// спрашивало у Spotify по сети, Windows знает и так.
//
// ЧТО ТАКОЕ SMTC. Системная панель управления медиа — та самая, что всплывает
// при нажатии кнопок громкости. Любой проигрыватель, который в ней виден,
// отдаёт название, артиста, альбом, обложку, точное положение внутри трека,
// длительность и состояние (играет, на паузе, остановлен). Всё это лежит
// внутри Windows, стоит долю миллисекунды и не считается никакой нормой.
// Через неё же можно управлять: пауза, дальше, назад, перемотка.
//
// Проверено на этой машине: Spotify отдаёт название, артиста, альбом,
// положение с точностью до миллисекунд, длительность и паузу. Последнее
// особенно важно — паузу своей музыки приложение раньше не видело в принципе:
// в заголовке окна Spotify её нет, а по сети за ней ходить слишком дорого.
//
// ПОЧЕМУ ЗДЕСЬ СТОЛЬКО ВОЗНИ. Это WinRT, то есть COM: готовой библиотеки для
// Go нет, и разговаривать приходится через таблицы методов вручную. Взамен —
// ни одной чужой зависимости и по-прежнему один .exe без CGO.
//
// ДВА ПРАВИЛА, БЕЗ КОТОРЫХ ЭТО НЕ РАБОТАЕТ:
//
//  1. COM живёт по потокам операционной системы, а горутины между ними
//     переезжают. Поэтому вся работа идёт в одной горутине, прибитой к своему
//     потоку (runtime.LockOSThread), а остальные общаются с ней через канал.
//     Без этого вызов однажды попадёт на поток, где COM не заведён, и вернёт
//     невнятный отказ.
//
//  2. Ожидание асинхронных вызовов сделано опросом состояния, а не
//     обработчиком события. Обработчик — это объект COM, написанный на Go, со
//     своей таблицей методов и счётчиком ссылок; ошибка в нём роняет
//     приложение целиком. Опрос уродлив, но эти вызовы отвечают за
//     миллисекунды, и цена ему — несколько холостых проверок.
package smtc

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Status — что делает проигрыватель. Числа заданы самой Windows.
type Status int32

const (
	StatusClosed   Status = 0
	StatusOpened   Status = 1
	StatusChanging Status = 2
	StatusStopped  Status = 3
	StatusPlaying  Status = 4
	StatusPaused   Status = 5
)

// Now — что играет прямо сейчас.
type Now struct {
	// App — чем играет, как это называет Windows. У Spotify из магазина это
	// «SpotifyAB.SpotifyMusic_...!Spotify», у обычного — «Spotify.exe».
	App    string
	Title  string
	Artist string
	Album  string
	Status Status
	// PositionMs и DurationMs — точные, из самой Windows. Ноль у
	// проигрывателей, которые не сообщают времени.
	PositionMs int
	DurationMs int
}

// Playing сообщает, звучит ли музыка прямо сейчас.
func (n Now) Playing() bool { return n.Status == StatusPlaying }

// Paused сообщает, стоит ли она на паузе.
func (n Now) Paused() bool { return n.Status == StatusPaused }

// Cmd — команда проигрывателю.
type Cmd int

const (
	CmdPlay Cmd = iota
	CmdPause
	CmdNext
	CmdPrevious
)

// ErrNoSession означает, что подходящего проигрывателя в системе нет.
var ErrNoSession = errors.New("подходящего проигрывателя в системе нет")

// YandexApp — подходит ли этот источник под Яндекс Музыку.
//
// По вхождению, а не по точному имени: в разных версиях приложение Яндекса
// называет себя по-разному («ru.yandex.desktop.music», «Яндекс Музыка.exe»,
// «YandexMusic»), а точное сравнение сломалось бы на первой же чужой машине.
func YandexApp(app string) bool {
	a := strings.ToLower(app)
	return strings.Contains(a, "yandex") || strings.Contains(a, "яндекс")
}

// ── разговор с Windows ───────────────────────────────────────────────

var (
	combase = syscall.NewLazyDLL("combase.dll")

	procRoInitialize            = combase.NewProc("RoInitialize")
	procRoGetActivationFactory  = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString     = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString     = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuff = combase.NewProc("WindowsGetStringRawBuffer")
)

// guid — опознавательный номер интерфейса COM.
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// Номера взяты из описания самих интерфейсов Windows. Ошибка в любой цифре
// означает «такого интерфейса нет», поэтому менять их наугад бесполезно.
var (
	iidSessionManagerStatics = guid{0x2050C4EE, 0x11A0, 0x57DE, [8]byte{0xAE, 0xD7, 0xC9, 0x7C, 0x70, 0x33, 0x82, 0x45}}
	iidAsyncInfo             = guid{0x00000036, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
)

const classSessionManager = "Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager"

// Порядковые номера методов в таблице.
//
// Первые шесть заняты всегда: три от IUnknown (QueryInterface, AddRef,
// Release) и три от IInspectable. Свои методы интерфейса идут с шестого.
const firstMethod = 6

const (
	// IAsyncOperation<T>: put_Completed, get_Completed, GetResults.
	mAsyncGetResults = firstMethod + 2
	// IAsyncInfo: get_Id, get_Status, get_ErrorCode, Cancel, Close.
	mAsyncInfoStatus = firstMethod + 1

	// IGlobalSystemMediaTransportControlsSessionManagerStatics: RequestAsync.
	mStaticsRequestAsync = firstMethod

	// IGlobalSystemMediaTransportControlsSessionManager: GetCurrentSession,
	// GetSessions, дальше подписки на события — они нам не нужны.
	mManagerGetSessions = firstMethod + 1

	// IVectorView<T>: GetAt, get_Size, IndexOf, GetMany.
	mVectorGetAt = firstMethod
	mVectorSize  = firstMethod + 1

	// IGlobalSystemMediaTransportControlsSession, по порядку описания.
	mSessionSourceAppID     = firstMethod
	mSessionMediaProperties = firstMethod + 1
	mSessionTimeline        = firstMethod + 2
	mSessionPlaybackInfo    = firstMethod + 3
	mSessionPlay            = firstMethod + 4
	mSessionPause           = firstMethod + 5
	mSessionNext            = firstMethod + 11
	mSessionPrevious        = firstMethod + 12

	// IGlobalSystemMediaTransportControlsSessionMediaProperties.
	mPropsTitle      = firstMethod
	mPropsArtist     = firstMethod + 3
	mPropsAlbumTitle = firstMethod + 4

	// IGlobalSystemMediaTransportControlsSessionTimelineProperties:
	// StartTime, EndTime, MinSeekTime, MaxSeekTime, Position, LastUpdatedTime.
	mTimelineEndTime  = firstMethod + 1
	mTimelinePosition = firstMethod + 4

	// IGlobalSystemMediaTransportControlsSessionPlaybackInfo:
	// Controls, PlaybackStatus, ...
	mPlaybackStatus = firstMethod + 1
)

// call зовёт метод по его номеру в таблице.
func call(this uintptr, index int, args ...uintptr) uintptr {
	if this == 0 {
		return uintptr(syscall.EINVAL)
	}
	vtbl := *(**[128]uintptr)(unsafe.Pointer(this))
	all := append([]uintptr{this}, args...)
	ret, _, _ := syscall.SyscallN(vtbl[index], all...)
	return ret
}

func release(this uintptr) {
	if this != 0 {
		call(this, 2) // Release
	}
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

func hrError(what string, hr uintptr) error {
	return fmt.Errorf("%s: 0x%08X", what, uint32(hr))
}

// ── строки ───────────────────────────────────────────────────────────

// hstring — строка в том виде, в каком её понимает WinRT. Создавать и
// удалять её обязано одно и то же место, иначе течёт память.
type hstring uintptr

func newHString(s string) (hstring, error) {
	u16, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h hstring
	hr, _, _ := procWindowsCreateString.Call(
		uintptr(unsafe.Pointer(&u16[0])), uintptr(len(u16)-1), uintptr(unsafe.Pointer(&h)))
	if failed(hr) {
		return 0, hrError("не создал строку", hr)
	}
	return h, nil
}

func (h hstring) free() {
	if h != 0 {
		procWindowsDeleteString.Call(uintptr(h))
	}
}

// text превращает строку WinRT в обычную.
func (h hstring) text() string {
	if h == 0 {
		return ""
	}
	var n uint32
	ptr, _, _ := procWindowsGetStringRawBuff.Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
	if ptr == 0 || n == 0 {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), n))
}

// getString читает строковое свойство и сразу убирает за собой.
func getString(this uintptr, method int) string {
	var h hstring
	if hr := call(this, method, uintptr(unsafe.Pointer(&h))); failed(hr) {
		return ""
	}
	defer h.free()
	return h.text()
}

// ── ожидание асинхронного вызова ─────────────────────────────────────

// await дожидается конца асинхронного вызова и отдаёт его итог.
//
// Опросом, а не обработчиком события: обработчик пришлось бы собирать как
// объект COM с рукописной таблицей методов, а это тот род кода, в котором
// ошибка роняет приложение целиком. Здешние вызовы отвечают за миллисекунды.
func await(op uintptr) (uintptr, error) {
	if op == 0 {
		return 0, errors.New("асинхронный вызов ничего не вернул")
	}
	defer release(op)

	var info uintptr
	if hr := call(op, 0, uintptr(unsafe.Pointer(&iidAsyncInfo)), uintptr(unsafe.Pointer(&info))); failed(hr) {
		return 0, hrError("не спросил состояние вызова", hr)
	}
	defer release(info)

	const (
		started   = 0
		completed = 1
	)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status int32
		if hr := call(info, mAsyncInfoStatus, uintptr(unsafe.Pointer(&status))); failed(hr) {
			return 0, hrError("не прочитал состояние вызова", hr)
		}
		if status != started {
			if status != completed {
				return 0, fmt.Errorf("Windows не выполнила вызов (состояние %d)", status)
			}
			break
		}
		if time.Now().After(deadline) {
			return 0, errors.New("Windows не ответила вовремя")
		}
		// Пауза короткая: обычно хватает первой же проверки.
		time.Sleep(time.Millisecond)
	}

	var result uintptr
	if hr := call(op, mAsyncGetResults, uintptr(unsafe.Pointer(&result))); failed(hr) {
		return 0, hrError("не забрал итог вызова", hr)
	}
	return result, nil
}

// ── работник ─────────────────────────────────────────────────────────

// Вся работа с COM идёт в одной горутине, прибитой к своему потоку.
// См. правило 1 в описании пакета.

type job struct {
	do   func() (Now, error)
	done chan result
}

type result struct {
	now Now
	err error
}

var (
	once  sync.Once
	queue chan job
	// startErr — чем кончился запуск. Если COM не завёлся, все вызовы
	// отвечают этой ошибкой, а не пытаются работать дальше.
	startErr error
)

func start() {
	queue = make(chan job)
	ready := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// RO_INIT_MULTITHREADED. Приложение зовёт нас из своих горутин, и
		// однопоточная квартира потребовала бы качать очередь сообщений.
		const roInitMultithreaded = 1
		hr, _, _ := procRoInitialize.Call(roInitMultithreaded)
		// S_FALSE (0x1) означает «уже заведено» — это не беда.
		if failed(hr) {
			ready <- hrError("не завёл COM", hr)
			return
		}
		ready <- nil

		for j := range queue {
			now, err := j.do()
			j.done <- result{now, err}
		}
	}()

	startErr = <-ready
}

// run выполняет работу в потоке, где заведён COM.
func run(do func() (Now, error)) (Now, error) {
	once.Do(start)
	if startErr != nil {
		return Now{}, startErr
	}
	done := make(chan result, 1)
	queue <- job{do: do, done: done}
	r := <-done
	return r.now, r.err
}

// ── сама работа ──────────────────────────────────────────────────────

// manager добывает у Windows управляющего сеансами.
//
// Берём его заново на каждый запрос. Держать про запас можно, но объект живёт
// в COM и переживает засыпание компьютера, смену пользователя и перезапуск
// проигрывателя хуже, чем свежий: цена вопроса — доля миллисекунды.
func manager() (uintptr, error) {
	name, err := newHString(classSessionManager)
	if err != nil {
		return 0, err
	}
	defer name.free()

	var statics uintptr
	hr, _, _ := procRoGetActivationFactory.Call(uintptr(name),
		uintptr(unsafe.Pointer(&iidSessionManagerStatics)), uintptr(unsafe.Pointer(&statics)))
	if failed(hr) {
		return 0, hrError("Windows не дала доступ к панели управления медиа", hr)
	}
	defer release(statics)

	var op uintptr
	if hr := call(statics, mStaticsRequestAsync, uintptr(unsafe.Pointer(&op))); failed(hr) {
		return 0, hrError("не запросил список проигрывателей", hr)
	}
	return await(op)
}

// pick находит сеанс подходящего проигрывателя.
//
// Возвращает и сам сеанс, и его имя: имя потом попадает в лог, а по логу
// разбираются чужие машины, где Spotify может называться иначе.
func pick(mgr uintptr, want func(string) bool) (uintptr, string, error) {
	var list uintptr
	if hr := call(mgr, mManagerGetSessions, uintptr(unsafe.Pointer(&list))); failed(hr) {
		return 0, "", hrError("не прочитал список проигрывателей", hr)
	}
	if list == 0 {
		return 0, "", ErrNoSession
	}
	defer release(list)

	var size uint32
	if hr := call(list, mVectorSize, uintptr(unsafe.Pointer(&size))); failed(hr) {
		return 0, "", hrError("не посчитал проигрыватели", hr)
	}

	for i := uint32(0); i < size; i++ {
		var session uintptr
		if hr := call(list, mVectorGetAt, uintptr(i), uintptr(unsafe.Pointer(&session))); failed(hr) || session == 0 {
			continue
		}
		app := getString(session, mSessionSourceAppID)
		if want == nil || want(app) {
			return session, app, nil
		}
		release(session)
	}
	return 0, "", ErrNoSession
}

// Read спрашивает Windows, что играет.
//
// want отбирает проигрыватель по его имени; nil означает «первый попавшийся».
// Второе значение — нашёлся ли подходящий.
func Read(want func(app string) bool) (Now, bool, error) {
	now, err := run(func() (Now, error) {
		mgr, err := manager()
		if err != nil {
			return Now{}, err
		}
		defer release(mgr)

		session, app, err := pick(mgr, want)
		if err != nil {
			return Now{}, err
		}
		defer release(session)

		out := Now{App: app}

		// Название и артист — асинхронный вызов, всё остальное обычный.
		var op uintptr
		if hr := call(session, mSessionMediaProperties, uintptr(unsafe.Pointer(&op))); !failed(hr) {
			if props, err := await(op); err == nil && props != 0 {
				out.Title = getString(props, mPropsTitle)
				out.Artist = getString(props, mPropsArtist)
				out.Album = getString(props, mPropsAlbumTitle)
				release(props)
			}
		}

		var status int32
		var info uintptr
		if hr := call(session, mSessionPlaybackInfo, uintptr(unsafe.Pointer(&info))); !failed(hr) && info != 0 {
			call(info, mPlaybackStatus, uintptr(unsafe.Pointer(&status)))
			release(info)
		}
		out.Status = Status(status)

		var timeline uintptr
		if hr := call(session, mSessionTimeline, uintptr(unsafe.Pointer(&timeline))); !failed(hr) && timeline != 0 {
			out.PositionMs = ticksToMs(readTicks(timeline, mTimelinePosition))
			out.DurationMs = ticksToMs(readTicks(timeline, mTimelineEndTime))
			release(timeline)
		}
		return out, nil
	})
	if errors.Is(err, ErrNoSession) {
		return Now{}, false, nil
	}
	if err != nil {
		return Now{}, false, err
	}
	return now, true, nil
}

// Control отдаёт команду проигрывателю.
func Control(want func(app string) bool, cmd Cmd) error {
	method, ok := map[Cmd]int{
		CmdPlay:     mSessionPlay,
		CmdPause:    mSessionPause,
		CmdNext:     mSessionNext,
		CmdPrevious: mSessionPrevious,
	}[cmd]
	if !ok {
		return errors.New("непонятная команда проигрывателю")
	}

	_, err := run(func() (Now, error) {
		mgr, err := manager()
		if err != nil {
			return Now{}, err
		}
		defer release(mgr)

		session, _, err := pick(mgr, want)
		if err != nil {
			return Now{}, err
		}
		defer release(session)

		var op uintptr
		if hr := call(session, method, uintptr(unsafe.Pointer(&op))); failed(hr) {
			return Now{}, hrError("проигрыватель не принял команду", hr)
		}
		// Итог — «получилось или нет». Сам ответ нам не нужен: если
		// проигрыватель откажется, это будет видно по тому, что ничего не
		// изменилось, а гадать за него мы всё равно не станем.
		res, err := await(op)
		if err != nil {
			return Now{}, err
		}
		_ = res
		return Now{}, nil
	})
	return err
}

// readTicks читает время из свойства. Windows меряет его сотнями наносекунд.
func readTicks(this uintptr, method int) int64 {
	var ticks int64
	if hr := call(this, method, uintptr(unsafe.Pointer(&ticks))); failed(hr) {
		return 0
	}
	return ticks
}

func ticksToMs(ticks int64) int {
	if ticks <= 0 {
		return 0
	}
	return int(ticks / 10_000)
}
