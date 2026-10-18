//go:build !windows

package youtube

import "os/exec"

// hideWindow на других системах не нужен: там консольное окно не всплывает.
func hideWindow(cmd *exec.Cmd) {}
