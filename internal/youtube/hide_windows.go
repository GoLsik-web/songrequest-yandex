//go:build windows

package youtube

import (
	"os/exec"
	"syscall"
)

// hideWindow прячет консольное окно дочернего процесса.
//
// Без этого при каждом заказе на секунду выскакивает чёрное окно поверх игры.
// На стриме это видят зрители, и выглядит оно как сбой.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
