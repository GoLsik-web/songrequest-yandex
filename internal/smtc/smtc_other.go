//go:build !windows

// Заглушки для не-Windows.
//
// Приложение живёт на Windows, но собираться и проверяться оно должно везде:
// иначе `go build ./...` на чужой машине падает не на том, на чём надо, и
// разбор поломки начинается с починки сборки. Тот же приём уже применён в
// internal/desktop.
package smtc

import "errors"

type Status int32

const (
	StatusClosed   Status = 0
	StatusOpened   Status = 1
	StatusChanging Status = 2
	StatusStopped  Status = 3
	StatusPlaying  Status = 4
	StatusPaused   Status = 5
)

type Now struct {
	App        string
	Title      string
	Artist     string
	Album      string
	Status     Status
	PositionMs int
	DurationMs int
}

func (n Now) Playing() bool { return n.Status == StatusPlaying }
func (n Now) Paused() bool  { return n.Status == StatusPaused }

type Cmd int

const (
	CmdPlay Cmd = iota
	CmdPause
	CmdNext
	CmdPrevious
)

var ErrNoSession = errors.New("подходящего проигрывателя в системе нет")

// YandexApp — тот же отбор, что и на Windows: нужен, чтобы код вызывающих
// собирался одинаково везде.
func YandexApp(string) bool { return false }

func Read(func(app string) bool) (Now, bool, error) { return Now{}, false, nil }

func Control(func(app string) bool, Cmd) error {
	return errors.New("системная панель управления медиа есть только в Windows")
}
