//go:build windows

package desktop

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Здесь собраны вызовы самой Windows, которые нужны окну и трею. Отдельным
// файлом, чтобы в window_windows.go осталась только логика окна, а не разбор
// того, каким числом обозначается «показать окно».
var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	procCallWindowProc     = user32.NewProc("CallWindowProcW")
	procSetWindowLongPtr   = user32.NewProc("SetWindowLongPtrW")
	procSetWindowLong      = user32.NewProc("SetWindowLongW") // 32-битная Windows
	procShowWindow         = user32.NewProc("ShowWindow")
	procSetForegroundWin   = user32.NewProc("SetForegroundWindow")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procGetWindowPlacement = user32.NewProc("GetWindowPlacement")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procLoadImage          = user32.NewProc("LoadImageW")
	procSendMessage        = user32.NewProc("SendMessageW")
	procMessageBox         = user32.NewProc("MessageBoxW")
	procPostMessage        = user32.NewProc("PostMessageW")
	procAllowForeground    = user32.NewProc("AllowSetForegroundWindow")
	procGetWindowLongPtr   = user32.NewProc("GetWindowLongPtrW")
	procGetWindowLong      = user32.NewProc("GetWindowLongW") // 32-битная Windows
	procReleaseCapture     = user32.NewProc("ReleaseCapture")
	procIsZoomed           = user32.NewProc("IsZoomed")
	procMonitorFromWindow  = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfo     = user32.NewProc("GetMonitorInfoW")

	procDwmSetAttr = dwmapi.NewProc("DwmSetWindowAttribute")

	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
)

const (
	wmClose        = 0x0010
	wmSetIcon      = 0x0080
	wmExitSizeMove = 0x0232
	wmNCLButtonDn  = 0x00A1
	wmSize         = 0x0005
	wmGetMinMax    = 0x0024

	monitorNearest = 2 // MONITOR_DEFAULTTONEAREST

	htCaption = 2

	gwlStyle  = -16        // GWL_STYLE
	wsCaption = 0x00C00000 // рамка с заголовком: её и снимаем

	swpNoMove      = 0x0002
	swpFrameChange = 0x0020

	// Отношения с DWM — той частью Windows, которая рисует рамки окон.
	// Номера — это «какое свойство окна меняем»; названия у Microsoft
	// длинные, здесь оставлены в комментариях.
	dwmDarkMode = 20 // DWMWA_USE_IMMERSIVE_DARK_MODE
	dwmCorner   = 33 // DWMWA_WINDOW_CORNER_PREFERENCE
	dwmBorder   = 34 // DWMWA_BORDER_COLOR
	dwmCaption  = 35 // DWMWA_CAPTION_COLOR

	cornerRound = 2 // DWMWCP_ROUND

	swHide          = 0
	swShowNormal    = 1
	swShowMinimized = 2
	swShowMaximized = 3
	swShow          = 5
	swRestore       = 9

	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	imageIcon     = 1
	lrLoadFromDsk = 0x0010 // LR_LOADFROMFILE
	iconSmall     = 0
	iconBig       = 1

	smCXSmIcon        = 49
	smCXIcon          = 11
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	mbIconError     = 0x00000010
	mbIconInfo      = 0x00000040
	mbSetForeground = 0x00010000

	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// windowPlacement — как Windows отдаёт положение окна. Берём именно его, а не
// GetWindowRect: у развёрнутого окна прямоугольник равен всему экрану, и после
// перезапуска окно осталось бы растянутым, но уже не «развёрнутым».
type windowPlacement struct {
	Length           uint32
	Flags            uint32
	ShowCmd          uint32
	PtMinPosition    point
	PtMaxPosition    point
	RcNormalPosition rect
}

type point struct{ X, Y int32 }

type rect struct{ Left, Top, Right, Bottom int32 }

// setWindowProc подменяет оконную процедуру и возвращает прежнюю.
//
// Так перехватывается крестик: библиотека окна на него всегда отвечает
// «уничтожить окно», а владелец решил, что крестик прячет программу в трей.
// Своей процедуры библиотека не предусматривает, поэтому подменяем её поверх —
// приём для Windows обычный, все чужие сообщения передаём прежней процедуре.
func setWindowProc(hwnd, proc uintptr) uintptr {
	// GWLP_WNDPROC — это -4. Числом со знаком его в uintptr не записать, а
	// Windows ждёт именно его, поэтому переводим через переменную.
	index := -4
	if err := procSetWindowLongPtr.Find(); err == nil {
		prev, _, _ := procSetWindowLongPtr.Call(hwnd, uintptr(index), proc)
		return prev
	}
	// 32-битная Windows знает только SetWindowLongW.
	prev, _, _ := procSetWindowLong.Call(hwnd, uintptr(index), proc)
	return prev
}

func callWindowProc(prev, hwnd, msg, wp, lp uintptr) uintptr {
	r, _, _ := procCallWindowProc.Call(prev, hwnd, msg, wp, lp)
	return r
}

func systemMetric(index int) int32 {
	v, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int32(v)
}

// setWindowIcon вешает на окно наш значок. Без этого Windows показывает в углу
// окна и в панели задач общий значок «неизвестная программа».
func setWindowIcon(hwnd uintptr, icoPath string) {
	if icoPath == "" {
		return
	}
	path, err := windows.UTF16PtrFromString(icoPath)
	if err != nil {
		return
	}
	load := func(size int32) uintptr {
		h, _, _ := procLoadImage.Call(0, uintptr(unsafe.Pointer(path)),
			imageIcon, uintptr(size), uintptr(size), lrLoadFromDsk)
		return h
	}
	if h := load(systemMetric(smCXSmIcon)); h != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconSmall, h)
	}
	if h := load(systemMetric(smCXIcon)); h != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconBig, h)
	}
}

// ShowError показывает окно с ошибкой (см. showError).
func ShowError(title, text string) { showError(title, text) }

// ShowInfo — то же окно, но со спокойным значком: не поломка, а сообщение.
func ShowInfo(title, text string) { messageBox(title, text, mbIconInfo) }

// showError показывает окно с ошибкой.
//
// Нужно потому, что после перехода на окно у приложения больше нет консоли:
// напечатать «не смог открыть базу» некуда, и программа просто не появлялась
// бы на экране без единого слова.
func showError(title, text string) { messageBox(title, text, mbIconError) }

func messageBox(title, text string, icon uintptr) {
	t, err1 := windows.UTF16PtrFromString(title)
	b, err2 := windows.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		return
	}
	procMessageBox.Call(0, uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(t)),
		icon|mbSetForeground)
}

// allowForeground разрешает другому процессу вывести своё окно вперёд.
//
// Нужно второй копии приложения: она просит первую показать окно, но Windows
// по умолчанию не даёт программе выхватывать себе передний план — окно
// показалось бы за чужими окнами, и человек решил бы, что запуск не сработал.
// Право отдаёт тот, кто сейчас впереди, — то есть как раз запущенная вторая
// копия; поэтому вызов здесь, а не в первой.
func allowForeground() {
	const asfwAny = ^uintptr(0) // (DWORD)-1 — «любому процессу»
	procAllowForeground.Call(asfwAny)
}

// setClipboard кладёт текст в буфер обмена. Нужно трею: адрес виджета для OBS
// человек всё равно копирует, а панель ради этого открывать необязательно.
func setClipboard(text string) error {
	u16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	if r, _, err := procOpenClipboard.Call(0); r == 0 {
		return fmt.Errorf("буфер обмена занят: %w", err)
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	size := uintptr(len(u16) * 2)
	h, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("не выделил память под текст: %w", err)
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("не смог заполнить буфер обмена")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u16)), u16)
	procGlobalUnlock.Call(h)

	if r, _, err := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("не положил текст в буфер обмена: %w", err)
	}
	return nil
}

// windowStyle читает нынешний набор свойств окна.
func windowStyle(hwnd uintptr) uintptr {
	// GWL_STYLE — это -16. Отрицательное число в uintptr прямо не записать
	// (тот же приём, что в setWindowProc), поэтому переводим через переменную.
	index := gwlStyle
	if err := procGetWindowLongPtr.Find(); err == nil {
		v, _, _ := procGetWindowLongPtr.Call(hwnd, uintptr(index))
		return v
	}
	v, _, _ := procGetWindowLong.Call(hwnd, uintptr(index))
	return v
}

func setWindowStyle(hwnd, style uintptr) {
	index := gwlStyle
	if err := procSetWindowLongPtr.Find(); err == nil {
		procSetWindowLongPtr.Call(hwnd, uintptr(index), style)
		return
	}
	procSetWindowLong.Call(hwnd, uintptr(index), style)
}

// dropCaption снимает с окна полосу заголовка, оставляя всё остальное.
//
// Зачем. Полосу рисует сама Windows, и рисует по своему вкусу: у стримера она
// была белой поперёк чёрной панели — «выделяется из общего концепта». Закрасить
// её нельзя, поверх неё нельзя рисовать — можно только убрать и нарисовать свою
// внутри страницы (см. титульную полосу в index.html).
//
// Снимается ровно WS_CAPTION. Рамка для растягивания (WS_THICKFRAME) остаётся:
// на ней держатся и изменение размера мышью, и прилипание окна к краям экрана,
// и разворот двойным щелчком.
func dropCaption(hwnd uintptr) {
	style := windowStyle(hwnd)
	if style&wsCaption == 0 {
		return
	}
	setWindowStyle(hwnd, style&^wsCaption)
	// Без этого Windows не пересчитает рамку и полоса останется на экране до
	// первого изменения размера.
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpFrameChange)
}

// darkFrame просит Windows рисовать всё, что осталось от рамки, тёмным.
//
// Нужно даже при снятой полосе заголовка: у окна остаётся тонкая рамка, тень и
// скруглённые углы, а по умолчанию они светлые. Плюс это же красит полосу в
// запасном пути «оставить рамку Windows» (флаг -рамка-windows).
//
// Свойства появились в разных сборках Windows 10 и 11; на старых вызов просто
// отвечает отказом, и это не поломка — окно останется прежним.
func darkFrame(hwnd uintptr) {
	on := int32(1)
	procDwmSetAttr.Call(hwnd, dwmDarkMode, uintptr(unsafe.Pointer(&on)), 4)

	// Цвет пишется как 0x00BBGGRR — у Windows задом наперёд. #08090a.
	back := uint32(0x000A0908)
	procDwmSetAttr.Call(hwnd, dwmCaption, uintptr(unsafe.Pointer(&back)), 4)
	edge := uint32(0x00161514) // чуть светлее фона, чтобы окно не сливалось со стримом
	procDwmSetAttr.Call(hwnd, dwmBorder, uintptr(unsafe.Pointer(&edge)), 4)

	corner := int32(cornerRound)
	procDwmSetAttr.Call(hwnd, dwmCorner, uintptr(unsafe.Pointer(&corner)), 4)
}

// dragWindow начинает перетаскивание окна за нашу собственную полосу заголовка.
//
// Хитрость обычная для окон без рамки: мы говорим Windows «на этом месте у меня
// заголовок, и по нему только что щёлкнули мышью», и дальше она сама ведёт
// окно за курсором — со всеми привычками, включая прилипание к краям экрана.
// Своими руками считать смещение мыши нельзя: окно дёргалось бы и не знало ни
// про прилипание, ни про несколько мониторов.
//
// ReleaseCapture обязателен: пока мышь «захвачена» страницей, Windows не отдаст
// её своему перетаскиванию.
func dragWindow(hwnd uintptr) {
	procReleaseCapture.Call()
	procPostMessage.Call(hwnd, wmNCLButtonDn, htCaption, 0)
}

// isMaximized — развёрнуто ли окно на весь экран.
func isMaximized(hwnd uintptr) bool {
	v, _, _ := procIsZoomed.Call(hwnd)
	return v != 0
}

// minMaxInfo — то, как Windows спрашивает у окна про его предельные размеры.
type minMaxInfo struct {
	Reserved     point
	MaxSize      point
	MaxPosition  point
	MinTrackSize point
	MaxTrackSize point
}

// monitorInfo — размеры экрана: весь и рабочая часть (без панели задач).
type monitorInfo struct {
	CbSize    uint32
	RcMonitor rect
	RcWork    rect
	DwFlags   uint32
}

// fitMaximized не даёт развёрнутому окну заехать под панель задач.
//
// У окна без полосы заголовка остаётся рамка для растягивания, и при развороте
// Windows по привычке вылезает за края экрана на её толщину — обычному окну это
// незаметно (за краями оказывается рамка), а у нашего за край уезжает верх
// страницы вместе с полосой заголовка, а снизу окно накрывает панель задач.
//
// Поэтому на вопрос «какого размера ты станешь развёрнутым» отвечаем сами:
// ровно рабочая часть того экрана, на котором окно сейчас лежит.
func fitMaximized(hwnd, lp uintptr) bool {
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorNearest)
	if mon == 0 {
		return false
	}
	mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := procGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return false
	}
	// lp — это адрес чужой памяти: её выделила Windows и держит на всё время
	// сообщения. Проверка `go vet` на такое ругается всегда (то же самое она
	// говорит про работу с буфером обмена выше) — иначе разговаривать с Windows
	// нельзя вообще.
	info := (*minMaxInfo)(unsafe.Pointer(lp))
	info.MaxPosition = point{X: mi.RcWork.Left - mi.RcMonitor.Left, Y: mi.RcWork.Top - mi.RcMonitor.Top}
	info.MaxSize = point{X: mi.RcWork.Right - mi.RcWork.Left, Y: mi.RcWork.Bottom - mi.RcWork.Top}
	info.MinTrackSize = point{X: minWidth, Y: minHeight}
	return true
}
