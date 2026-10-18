//go:build !windows

package desktop

import (
	"errors"
	"fmt"
	"os"
)

// Заглушки для не-Windows. Приложение живёт на Windows (Spotify-плеер, mpv,
// хранилище паролей — всё оттуда), но пакеты обязаны собираться и на другой
// системе: иначе на ней не запустить даже `go vet` и тесты остального кода.

// ErrNoWebView — своего окна тут не бывает, панель открывается в браузере.
var ErrNoWebView = errors.New("окно программы бывает только на Windows")

// Window — пустышка, до которой на этой системе дело не доходит.
type Window struct{}

func Open(Options) (*Window, error) { return nil, ErrNoWebView }

func (w *Window) Run()  {}
func (w *Window) Show() {}
func (w *Window) Quit() {}

// OpenAuth — окна входа тут тоже не бывает.
func OpenAuth(AuthOptions) error { return ErrNoWebView }

// RunTray на не-Windows просто ничего не делает.
func RunTray(TrayOptions) {}

// StopTray — то же самое.
func StopTray() {}

// ShowError печатает ошибку в консоль: окон с сообщением тут нет.
func ShowError(title, text string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, text)
}

// allowForeground на не-Windows не нужен.
func allowForeground() {}
