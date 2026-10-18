//go:build windows

package youtube

// isWindows отделяет имя именованного канала Windows от пути к сокету.
func isWindows() bool { return true }
