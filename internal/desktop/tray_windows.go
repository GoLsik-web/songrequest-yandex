//go:build windows

package desktop

import (
	"runtime"

	"fyne.io/systray"
)

// RunTray заводит значок возле часов и крутится до StopTray.
//
// Вызывать в отдельной горутине: работа блокирующая. Внутри поток закрепляется
// за горутиной — у значка, как и у окна, свой цикл сообщений Windows, и
// приходят они только в тот поток, где значок создан.
func RunTray(o TrayOptions) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	systray.Run(func() {
		if o.IconPath != "" {
			systray.SetIconFromFilePath(o.IconPath)
		}
		if o.Tooltip != "" {
			systray.SetTooltip(o.Tooltip)
		}

		// Левый клик открывает панель. Меню остаётся на правой кнопке — так
		// же, как решил владелец про выход: чтобы случайное нажатие ничего
		// не закрывало.
		if o.OnOpen != nil {
			systray.SetOnTapped(o.OnOpen)
		}

		открыть := systray.AddMenuItem("Открыть панель", "Показать окно программы")
		копия := systray.AddMenuItem("Скопировать адрес виджета для OBS", o.WidgetURL)
		systray.AddSeparator()
		выход := systray.AddMenuItem("Выход", "Закрыть программу совсем")

		go func() {
			for {
				select {
				case <-открыть.ClickedCh:
					if o.OnOpen != nil {
						o.OnOpen()
					}
				case <-копия.ClickedCh:
					err := setClipboard(o.WidgetURL)
					if o.OnCopy != nil {
						o.OnCopy(err)
					}
				case <-выход.ClickedCh:
					if o.OnQuit != nil {
						o.OnQuit()
					}
				}
			}
		}()
	}, nil)
}

// StopTray убирает значок возле часов. Без этого он остаётся висеть до тех
// пор, пока по нему не проведёшь мышью, — Windows чистит такие «мёртвые»
// значки только при наведении, и человек думает, что программа не закрылась.
func StopTray() { systray.Quit() }
