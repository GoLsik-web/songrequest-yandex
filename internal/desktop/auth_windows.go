//go:build windows

package desktop

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	webview "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
)

// Окно входа в Spotify — то самое место, где обход блокировок обязан работать
// не только для нашего кода, но и для чужой страницы.
//
// WebView2 умеет ходить через посредника, но сказать ему об этом можно
// единственным способом: переменной окружения WEBVIEW2_ADDITIONAL_BROWSER_-
// ARGUMENTS, которую движок читает в момент создания. Библиотека, которой мы
// пользуемся, передать настройки напрямую не даёт (options там жёстко nil),
// поэтому переменную ставим сами и сразу убираем — чтобы она не досталась
// следующему окну.
//
// Второе, о чём легко забыть: движок делит один браузер между всеми окнами с
// общей папкой данных, и настройки берутся от того, кто создан первым. Панель
// создаётся раньше и без посредника, поэтому окну входа нужна своя папка —
// иначе аргумент про посредника просто ничего не изменит.

// authMu не даёт открыть два окна входа сразу: они спорили бы за переменную
// окружения, и второе получило бы настройки первого.
var authMu sync.Mutex

// OpenAuth открывает окно входа и не возвращается, пока его не закроют.
//
// Крутит собственный цикл сообщений, поэтому зовут его из отдельной горутины;
// закрепление за потоком делается здесь же — окно Windows живёт только в том
// потоке, где создано.
func OpenAuth(o AuthOptions) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	authMu.Lock()
	defer authMu.Unlock()

	version, err := webviewloader.GetInstalledVersion()
	if err != nil || version == "" {
		return ErrNoWebView
	}

	if o.Proxy != "" {
		// Свой адрес (127.0.0.1) движок и так обходит стороной по умолчанию —
		// это важно: именно на него Spotify возвращает человека после входа, и
		// гнать этот запрос через обход было бы и лишним, и небезопасным.
		args := "--proxy-server=" + o.Proxy
		old, had := os.LookupEnv(browserArgsEnv)
		os.Setenv(browserArgsEnv, args)
		defer func() {
			if had {
				os.Setenv(browserArgsEnv, old)
			} else {
				os.Unsetenv(browserArgsEnv)
			}
		}()
	}

	title := o.Title
	if title == "" {
		title = "Вход"
	}
	wv := webview.NewWithOptions(webview.WebViewOptions{
		DataPath:  o.DataPath,
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title: title, Width: 520, Height: 720, Center: true,
		},
	})
	if wv == nil {
		return fmt.Errorf("%w: окно входа не создалось", ErrNoWebView)
	}
	defer wv.Destroy()

	setWindowIcon(uintptr(wv.Window()), o.IconPath)

	// Страница возврата у нас своя, и на ней написано «можно закрывать». Ждём
	// секунду, чтобы человек успел это прочитать, и закрываем сами: закрывать
	// окна за людей нехорошо, но здесь окно ровно одноразовое, и оставлять его
	// висеть — значит собрать те самые лишние окна, от которых уходим.
	wv.Bind("srAuthDone", func() {
		wv.Terminate()
	})
	if o.OnURL != nil {
		wv.Bind("srAuthURL", func(u string) {
			if o.OnURL(u) {
				wv.Terminate()
			}
		})
		wv.Init(authURLJS)
	}
	wv.Init(authDoneJS(o.DonePath))

	wv.Navigate(o.URL)
	wv.Run()

	if o.OnDone != nil {
		o.OnDone()
	}
	return nil
}

// browserArgsEnv — переменная, через которую движку передаются аргументы
// запуска. Имя задано самим WebView2, менять его нельзя.
const browserArgsEnv = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"

// authDoneJS закрывает окно, когда вход закончился.
//
// Скрипт вставляется на каждую страницу до её загрузки, поэтому срабатывает
// ровно на нашей странице возврата и никогда — на страницах самого Spotify.
func authDoneJS(donePath string) string {
	path := donePath
	if path == "" {
		path = "/callback"
	}
	return `
(function () {
  if (location.pathname !== ` + jsString(path) + `) return;
  setTimeout(function () { try { window.srAuthDone(); } catch (e) {} }, 1200);
})();
`
}

// authURLJS сообщает приложению адрес каждой страницы, вместе с частью
// после «#»: именно туда Яндекс кладёт токен.
const authURLJS = `
(function () {
  try { window.srAuthURL(location.href); } catch (e) {}
  window.addEventListener('hashchange', function () {
    try { window.srAuthURL(location.href); } catch (e) {}
  });
})();
`

// jsString заворачивает строку в кавычки для вставки в скрипт.
func jsString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
