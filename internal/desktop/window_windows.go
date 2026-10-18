//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// ErrNoWebView — в системе нет движка WebView2, показать окно не в чем.
//
// Отдельная ошибка, а не общая: на неё приложение отвечает не отказом, а
// запасным путём — открывает панель в браузере, как до появления окна.
var ErrNoWebView = errors.New("в системе нет WebView2")

// Минимальный размер окна. Ниже 1000 точек панель перестраивается в один
// столбец (см. app.css), а ниже примерно 820 столбцы настроек начинают
// налезать друг на друга — такое окно человек всё равно сразу растянет.
const (
	minWidth  = 820
	minHeight = 560

	defWidth  = 1180
	defHeight = 820
)

// Window — окно программы с панелью внутри.
type Window struct {
	opts Options
	wv   webview.WebView
	hwnd uintptr
	prev uintptr // прежняя оконная процедура библиотеки

	// quitting отличает «человек нажал крестик» (прячем в трей) от «выходим
	// по-настоящему» (окно уничтожается и цикл сообщений заканчивается).
	quitting atomic.Bool
}

// Open создаёт окно и загружает в него панель.
//
// Вызывать только из горутины, закреплённой за потоком (runtime.LockOSThread),
// и из неё же потом звать Run: окно Windows живёт в том потоке, где создано, и
// сообщения приходят только туда. Go без закрепления переносит горутину с
// потока на поток, и окно замерло бы навсегда.
func Open(opts Options) (*Window, error) {
	// Сначала спрашиваем систему, есть ли движок: без этой проверки библиотека
	// падает где-то внутри, и человек видит пустой экран вместо объяснения.
	version, err := webviewloader.GetInstalledVersion()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoWebView, err)
	}
	if version == "" {
		return nil, ErrNoWebView
	}

	geom := opts.Geometry
	if !geom.sane() {
		geom = Geometry{Width: defWidth, Height: defHeight}
	}

	wv := webview.NewWithOptions(webview.WebViewOptions{
		DataPath:  opts.DataPath,
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  opts.Title,
			Width:  uint(geom.Width),
			Height: uint(geom.Height),
			Center: geom.X == 0 && geom.Y == 0,
		},
	})
	if wv == nil {
		return nil, fmt.Errorf("%w: окно не создалось", ErrNoWebView)
	}

	w := &Window{opts: opts, wv: wv, hwnd: uintptr(wv.Window())}
	w.prev = setWindowProc(w.hwnd, windows.NewCallback(w.wndProc))

	// Своя полоса заголовка вместо системной.
	//
	// Системную рисует Windows по своему вкусу — у стримера она оказалась
	// белой поперёк чёрной панели. Закрасить её нельзя, поэтому снимаем
	// совсем, а название, кнопки и место для перетаскивания рисует сама
	// страница (см. титульную полосу в index.html). Тёмную рамку просим в
	// любом случае: у окна остаются тень, тонкий кант и скруглённые углы, и
	// они тоже не должны быть белыми.
	darkFrame(w.hwnd)
	if !opts.SystemFrame {
		dropCaption(w.hwnd)
	}

	wv.SetSize(minWidth, minHeight, webview.HintMin)
	w.place(geom)
	setWindowIcon(w.hwnd, opts.IconPath)

	// Ссылки наружу отдаём обычному браузеру.
	//
	// Внутри окна нет ни адресной строки, ни кнопки «назад»: если увести его
	// на страницу входа Spotify, человек останется один на один с чужим
	// сайтом без выхода. Вход как открывался в браузере, так и открывается.
	wv.Bind("srOpenExternal", func(url string) {
		if w.opts.OnExternal != nil && url != "" {
			w.opts.OnExternal(url)
		}
	})

	// Кнопки и перетаскивание своей полосы заголовка.
	//
	// Панель — обычная страница, и своими силами подвинуть окно Windows она не
	// может. Поэтому все четыре действия она просит у приложения, а приложение уже
	// разговаривает с Windows. В браузере этих имён нет, и полоса там не
	// показывается — панель проверяет их наличие сама.
	wv.Bind("srWindowDrag", func() { dragWindow(w.hwnd) })
	wv.Bind("srWindowMinimize", func() { procShowWindow.Call(w.hwnd, swShowMinimized) })
	wv.Bind("srWindowMaxToggle", func() bool {
		if isMaximized(w.hwnd) {
			procShowWindow.Call(w.hwnd, swRestore)
			return false
		}
		procShowWindow.Call(w.hwnd, swShowMaximized)
		return true
	})
	// Крестик в своей полосе делает то же, что делал системный: прячет
	// программу к часам, а не закрывает её.
	wv.Bind("srWindowHide", func() {
		procSendMessage.Call(w.hwnd, wmClose, 0, 0)
	})
	wv.Init(openExternalJS)

	wv.Navigate(opts.URL)
	return w, nil
}

// openExternalJS перехватывает в панели всё, что открывается «в новом окне».
//
// WebView2 на window.open отвечает своим голым окошком без адресной строки, а
// на ссылку с target=_blank — тем же самым. Поэтому и то и другое переводим на
// внешний браузер. Скрипт вставляется до загрузки страницы, поэтому успевает
// подменить window.open раньше, чем панель им воспользуется.
const openExternalJS = `
(function () {
  var sendOut = function (u) { try { window.srOpenExternal(String(u)); } catch (e) {} };
  window.open = function (u) { if (u) sendOut(u); return null; };
  document.addEventListener("click", function (e) {
    var a = e.target && e.target.closest ? e.target.closest("a[href]") : null;
    if (!a) return;
    var href = a.getAttribute("href") || "";
    var external = /^https?:\/\//i.test(href) && href.indexOf(location.origin) !== 0;
    if (a.target === "_blank" || external) { e.preventDefault(); sendOut(a.href); }
  }, true);
})();
`

// Run крутит цикл сообщений окна и возвращается только после выхода.
func (w *Window) Run() { w.wv.Run() }

// Show достаёт окно обратно из трея. Можно звать из любой горутины: работа
// делается в потоке окна через Dispatch.
func (w *Window) Show() {
	w.wv.Dispatch(func() {
		// Развёрнутое окно после swShow остаётся развёрнутым, свёрнутое надо
		// поднимать отдельно — иначе оно «покажется» в виде значка в панели.
		if w.placement().ShowCmd == swShowMinimized {
			procShowWindow.Call(w.hwnd, swRestore)
		} else {
			procShowWindow.Call(w.hwnd, swShow)
		}
		procSetForegroundWin.Call(w.hwnd)
	})
}

// Quit закрывает окно и заканчивает цикл сообщений: после этого Run вернётся.
func (w *Window) Quit() {
	w.quitting.Store(true)
	w.wv.Dispatch(func() {
		w.remember()
		// Прячем окно сразу: дальше приложение ещё гасит mpv и снимает паузу
		// со Spotify, и всё это время окно висело бы мёртвым на экране.
		procShowWindow.Call(w.hwnd, swHide)
		// PostQuitMessage кладёт сообщение в очередь того потока, который его
		// позвал, поэтому звать только отсюда — изнутри потока окна.
		w.wv.Terminate()
	})
}

// wndProc — наша оконная процедура. Всё, что не про крестик и не про размеры,
// уходит прежней процедуре библиотеки без изменений.
func (w *Window) wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmClose:
		if !w.quitting.Load() {
			// Решение владельца: крестик прячет программу в трей, а не
			// закрывает её. Случайный клик посреди стрима не должен убивать
			// очередь заказов и виджет в OBS.
			w.remember()
			procShowWindow.Call(hwnd, swHide)
			if w.opts.OnHide != nil {
				w.opts.OnHide()
			}
			return 0
		}
	case wmGetMinMax:
		// Своя полоса заголовка означает окно без рамки Windows, а такое окно
		// при развороте заезжает за края экрана. Размер развёрнутого окна
		// считаем сами.
		if !w.opts.SystemFrame && fitMaximized(hwnd, lp) {
			return 0
		}
	case wmSize:
		// Окно развернули или вернули обратно — на кнопке в нашей полосе
		// заголовка должен смениться значок. Сообщение приходит в поток окна,
		// поэтому Eval зовётся отсюда напрямую.
		if !w.quitting.Load() {
			w.tellMaximized()
		}
	case wmExitSizeMove:
		// Человек отпустил край окна — запоминаем новый размер. Ждать выхода
		// нельзя: приложение могут закрыть выключением компьютера.
		w.remember()
	}
	return callWindowProc(w.prev, hwnd, msg, wp, lp)
}

// tellMaximized говорит странице, развёрнуто ли окно.
//
// Панель об этом сама узнать не может: для страницы разворот окна ничем не
// отличается от того, что человек растянул его мышью, — а значок на кнопке
// разный.
func (w *Window) tellMaximized() {
	state := "false"
	if isMaximized(w.hwnd) {
		state = "true"
	}
	w.wv.Eval("window.srMaximized && window.srMaximized(" + state + ")")
}

// remember отдаёт наружу текущий размер и положение окна.
func (w *Window) remember() {
	if w.opts.OnGeometry == nil {
		return
	}
	if g, ok := w.geometry(); ok {
		w.opts.OnGeometry(g)
	}
}

func (w *Window) placement() windowPlacement {
	p := windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
	procGetWindowPlacement.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
	return p
}

// geometry читает у Windows, где сейчас окно.
func (w *Window) geometry() (Geometry, bool) {
	p := w.placement()
	g := Geometry{Maximized: p.ShowCmd == swShowMaximized}

	// У развёрнутого окна спрашивать GetWindowRect бесполезно — он вернёт
	// весь экран, и в следующий раз окно открылось бы во весь экран, но уже
	// не развёрнутым (без кнопки «свернуть обратно»). Поэтому у развёрнутого
	// берём его «нормальный» прямоугольник, который Windows держит про запас.
	r := p.RcNormalPosition
	if !g.Maximized {
		var live rect
		if ok, _, _ := procGetWindowRect.Call(w.hwnd, uintptr(unsafe.Pointer(&live))); ok != 0 {
			r = live
		}
	}
	g.X, g.Y = int(r.Left), int(r.Top)
	g.Width, g.Height = int(r.Right-r.Left), int(r.Bottom-r.Top)
	if !g.sane() {
		return Geometry{}, false
	}
	return g, true
}

// place ставит окно туда, где оно было в прошлый раз.
func (w *Window) place(g Geometry) {
	if g.X != 0 || g.Y != 0 {
		procSetWindowPos.Call(w.hwnd, 0, uintptr(int32(g.X)), uintptr(int32(g.Y)), 0, 0,
			swpNoSize|swpNoZOrder|swpNoActivate)
	}
	if g.Maximized {
		procShowWindow.Call(w.hwnd, swShowMaximized)
	}
}

// sane отсеивает сохранённые размеры, с которыми окном нельзя пользоваться.
//
// Так бывает не от поломки: человек отключил второй монитор, и окно осталось
// «где-то справа за краем экрана» — на пустом месте, куда мышь не доедет.
func (g Geometry) sane() bool {
	if g.Width < minWidth || g.Height < minHeight {
		return false
	}
	// Заголовок окна должен попадать на какой-нибудь экран: за него окно
	// двигают, и без него оно потеряно.
	left := systemMetric(smXVirtualScreen)
	top := systemMetric(smYVirtualScreen)
	right := left + systemMetric(smCXVirtualScreen)
	bottom := top + systemMetric(smCYVirtualScreen)
	const titleBar = 40
	x, y := int32(g.X), int32(g.Y)
	return x+int32(g.Width) > left && x < right && y+titleBar > top && y < bottom
}
