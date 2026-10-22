//go:build windows

package desktop

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Lock — именованный мьютекс Windows, знак «эта копия приложения уже живёт».
//
// Зачем, если есть проверка по порту (RaiseRunning). Порт открывается не
// сразу: между запуском и готовой панелью проходит секунда-другая, и две копии,
// запущенные двойным щелчком подряд, обе видели свободный порт и обе
// стартовали. Мьютекс занимается первой строкой run() и отпускается самой
// Windows при выходе процесса — даже если он упал.
type Lock struct{ h windows.Handle }

// TryLock пытается занять мьютекс с этим именем. ok=false — его держит другая
// копия; ошибка — Windows не дала создать мьютекс вовсе (тогда лучше жить без
// него, чем не запуститься).
func TryLock(name string) (l *Lock, ok bool, err error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// Ручку закрываем сразу: держать чужой мьютекс — значит не дать ему
		// исчезнуть, когда первая копия выйдет.
		if h != 0 {
			windows.CloseHandle(h)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &Lock{h: h}, true, nil
}

// Release отпускает мьютекс раньше выхода процесса.
func (l *Lock) Release() {
	if l != nil && l.h != 0 {
		windows.CloseHandle(l.h)
		l.h = 0
	}
}
