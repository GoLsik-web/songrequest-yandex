// Команда songrequest-yandex — заказ музыки зрителями Twitch с проигрыванием в
// Яндекс Музыке самого стримера. Приложение локальное: наружу ничего не слушает.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/desktop"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/logx"
	"songrequest-yandex/internal/secrets"
	"songrequest-yandex/internal/server"
	"songrequest-yandex/internal/store"
	"songrequest-yandex/internal/twitch"
	"songrequest-yandex/internal/update"
	"songrequest-yandex/internal/yandex"
)

// version подставляется при сборке релиза через -ldflags.
var version = "dev"

// flavor — пометка отдельной сборки, тоже подставляется при сборке.
//
// ЗАЧЕМ. Пробную сборку надо уметь поставить рядом с рабочей и запустить, не
// трогая её. Просто скопировать .exe для этого мало: обе копии полезли бы в
// одну папку настроек, одну базу, один порт — и вторая, увидев работающую
// первую, честно решила бы, что её запустили дважды, показала бы чужое окно и
// вышла.
//
// Поэтому пометка меняет три вещи разом: папку данных, порт по умолчанию и
// подпись окна. Пусто — обычная сборка, всё как было.
var flavor = ""

// appName — имя папки с настройками, базой и логом.
func appName() string {
	if flavor == "" {
		return "songrequest-yandex"
	}
	return "songrequest-yandex-" + flavor
}

// defaultPort — на каком порту поднимать панель.
//
// У пробной сборки свой, иначе она столкнулась бы с рабочей: та занимает 8977
// и отвечает на проверку «уже запущено».
func defaultPort() int {
	if flavor == "" {
		return 0 // берём из настроек, как раньше
	}
	return 8992
}

// windowTitle — заголовок окна и подпись значка возле часов.
var windowTitle = "Заказ музыки · Яндекс"

func main() {
	// Окно Windows живёт в том потоке, где создано, и сообщения приходят
	// только туда. Go без этой строчки свободно переносит главную горутину с
	// потока на поток — и окно замерло бы, не отвечая на нажатия.
	runtime.LockOSThread()

	if err := run(); err != nil {
		// Консоли у приложения больше нет (собирается как обычная программа с
		// окном), поэтому про поломку рассказываем окном с сообщением. В
		// консоль тоже пишем — на случай запуска из PowerShell при разборе.
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		desktop.ShowError(windowTitle+" — не запустилось", humanErr(err)+
			"\n\nПодробности в логе:\n"+logPathHint())
		os.Exit(1)
	}
}

func run() error {
	// Режимов запуска три. Обычный — своё окно; остальные два нужны при
	// разборе поломок, когда окно только мешает.
	inBrowser := flag.Bool("в-браузере", false, "открыть панель в браузере, без окна программы")
	silent := flag.Bool("без-браузера", false, "не открывать ни окна, ни браузера")
	// Запасной путь для окна: вернуть системную полосу заголовка. Нужен, если
	// на чужой Windows со своей полосой что-то не заладится — окно без
	// заголовка и без возможности его вернуть было бы ловушкой.
	sysFrame := flag.Bool("рамка-windows", false, "оставить обычную полосу заголовка Windows")
	// Этот флаг ставит себе само приложение, когда обновляется: новая копия
	// запускается раньше, чем старая успела закрыться, и должна её дождаться.
	// Без ожидания она увидела бы работающее приложение, решила бы, что
	// запущена второй раз, показала бы чужое окно и вышла.
	afterUpdate := flag.Bool("после-обновления", false, "подождать, пока закроется прежняя копия")
	flag.Parse()

	dir, err := dataDir()
	if err != nil {
		return err
	}

	// Подробный лог включён всегда. Приложение чинится по логу с чужого
	// компьютера, а просить человека «включи галочку и повтори» — значит
	// потерять тот единственный раз, когда всё сломалось.
	log, err := logx.New(dir, true)
	if err != nil {
		return err
	}
	defer log.Close()

	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}

	// Пробная сборка живёт своей жизнью: своя папка настроек, свой порт, своя
	// подпись окна. Иначе рядом с рабочей копией её просто не запустить.
	if flavor != "" {
		windowTitle += " · " + flavor
		if p := defaultPort(); p > 0 && cfg.Get().Port != p {
			if err := cfg.Update(func(c *config.Config) { c.Port = p }); err != nil {
				log.Warn("не записал свой порт пробной сборки", "ошибка", err)
			}
		}
		log.Info("пробная сборка", "пометка", flavor, "папка", dir, "порт", cfg.Get().Port)
	}
	if cfg.Broken != "" {
		// Молча стартовать на значениях по умолчанию нельзя: человек должен
		// понимать, почему его настройки вдруг стали другими.
		log.Warn("настройки были повреждены, начал с значений по умолчанию",
			"испорченный_файл", cfg.Broken)
	}

	// Единственность копии — именованным мьютексом Windows, а не только по
	// порту: порт открывается через секунду-другую после запуска, и два
	// быстрых щелчка по .exe давали две живые копии на одну базу.
	if *afterUpdate {
		// Обновились — ждём, пока прежняя копия отпустит мьютекс и порт.
		// Выходить по «уже запущено» здесь нельзя: запущена как раз та, что
		// нас позвала и сейчас закрывается.
		lock := waitForPrevious(log, cfg.Get().Port)
		defer lock.Release()
	} else {
		lock, ok, err := desktop.TryLock(lockName())
		if err != nil {
			log.Warn("не создал знак единственной копии, проверяю только по порту", "ошибка", err)
		}
		defer lock.Release()
		if !ok && err == nil {
			// Первая копия ещё может подниматься: даём ей время открыть панель,
			// чтобы показать её окно, а не молча выйти.
			raiseFirst(log, cfg.Get().Port)
			return nil
		}
		// Мьютекс наш, но рядом может работать сборка постарше, которая
		// мьютекса не знает. Её ловит прежняя проверка по порту.
		if running, raised := desktop.RaiseRunning(cfg.Get().Port); running {
			log.Info("приложение уже запущено, вторая копия не нужна", "окно_показано", raised)
			if !raised {
				alreadyRunningInfo()
			}
			return nil
		}
	}

	db, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	state := app.New(version)

	keys := secrets.New()
	ym := yandex.New(cfg, log, keys)
	tw := twitch.New(cfg, log, keys)

	srv, err := server.New(server.Deps{
		State:   state,
		Cfg:     cfg,
		Log:     log,
		Yandex:  ym,
		Twitch:  tw,
		DB:      db,
		Secrets: keys,
		DataDir: dir,
		Version: version,
	})
	if err != nil {
		return err
	}

	// Ctrl+C, выключение компьютера и выход из трея должны гасить приложение
	// чисто: закрыть базу и убить mpv. Своя отмена поверх сигналов нужна
	// потому, что выход теперь бывает и без всякого сигнала — из меню значка.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, shutdown := context.WithCancel(ctx)
	defer shutdown()

	log.Info("приложение запущено", "версия", version, "данные", dir, "панель", srv.Addr())
	state.Notify("info", "Приложение запущено")

	// Панель уходит в фон: главный поток теперь занят окном программы.
	go func() {
		if err := srv.Serve(ctx); err != nil {
			log.Error("панель управления остановилась", "ошибка", err)
			state.Notify("error", "Панель управления остановилась: "+humanErr(err))
		}
	}()

	// Отставленный файл прошлой версии удаляем только когда новая версия
	// доказала, что живёт: панель ответила.
	go cleanOldWhenServing(ctx, log, srv.Addr())

	// Если вход был сохранён с прошлого раза, проверяем его сразу: лучше
	// увидеть «Яндекс отвалился» до стрима, а не во время первого заказа.
	if ym.Connected() {
		go func() {
			checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if _, err := ym.CheckAccount(checkCtx); err != nil {
				state.NotifyError(err)
			}
			srv.SyncYandex(ctx)
		}()
	} else {
		srv.SyncYandex(ctx)
	}

	// Twitch поднимаем следом: если вход был, приложение само заведёт награду
	// и подпишется на заказы, без единого нажатия.
	srv.SyncTwitch()
	srv.StartTwitchIfConnected(ctx)
	srv.StartPlayer(ctx)
	srv.StartDonations(ctx)
	srv.StartYouTube(ctx)
	srv.CheckOBS(ctx)

	// mpv не должен пережить приложение: иначе он останется занимать
	// звуковое устройство, а музыка продолжит играть после закрытия.
	defer srv.StopYouTube()

	showPanel, quit, wait := setupUI(uiDeps{
		log:       log,
		state:     state,
		cfg:       cfg,
		srv:       srv,
		dir:       dir,
		ctx:       ctx,
		shutdown:  shutdown,
		inBrowser: *inBrowser,
		silent:    *silent,
		sysFrame:  *sysFrame,
	})
	srv.OnShowWindow(showPanel)
	srv.OnQuit(quit)
	srv.OnOpenAuth(authOpener(log, dir))

	wait()

	log.Info("выходим")
	shutdown()
	desktop.StopTray()
	return nil
}

// authOpener открывает вход в Яндекс в отдельном окне приложения.
//
// Своё окно нужно, чтобы забрать токен: после входа Яндекс возвращает на
// music.yandex.ru с токеном в адресе, и обычный браузер нам его не покажет.
// Окно видит адрес само, отдаёт токен и закрывается.
func authOpener(log *logx.Logger, dir string) func(string, func(string)) error {
	return func(url string, onToken func(string)) error {
		icon, err := desktop.IconFile(dir)
		if err != nil {
			log.Warn("не выложил значок приложения", "ошибка", err)
		}
		log.Info("открываю вход в Яндекс в своём окне")
		go func() {
			err := desktop.OpenAuth(desktop.AuthOptions{
				URL:      url,
				Title:    "Вход в Яндекс",
				IconPath: icon,
				DataPath: filepath.Join(dir, "webview-вход"),
				OnURL: func(u string) bool {
					token := yandex.TokenFromURL(u)
					if token == "" {
						return false
					}
					go onToken(token)
					return true
				},
			})
			if err != nil {
				log.Warn("окно входа закрылось с ошибкой", "ошибка", err)
			}
		}()
		return nil
	}
}

// uiDeps — то, что нужно окну, трею и запасному пути через браузер.
type uiDeps struct {
	log      *logx.Logger
	state    *app.State
	cfg      *config.File
	srv      *server.Server
	dir      string
	ctx      context.Context
	shutdown context.CancelFunc

	inBrowser bool
	silent    bool
	sysFrame  bool
}

// setupUI поднимает то, что человек видит на экране, и возвращает две вещи:
// как показать панель по просьбе извне и чем занять главный поток до выхода.
//
// Путей три:
//   - обычный: своё окно с панелью внутри плюс значок возле часов;
//   - запасной: если в системе нет WebView2 (движка Edge) — панель в браузере,
//     как было до этого шага. Отказываться работать из-за этого нельзя;
//   - отладочные флаги: панель в браузере или вообще ничего.
func setupUI(d uiDeps) (showPanel, quit, wait func()) {
	icon, err := desktop.IconFile(d.dir)
	if err != nil {
		d.log.Warn("не выложил значок приложения", "ошибка", err)
	}

	openPanel := func() { openBrowser(d.srv.Addr(), d.log) }

	// Ждать через контекст — общий для всех путей без окна способ: выход из
	// трея и Ctrl+C гасят один и тот же контекст.
	waitCtx := func() { <-d.ctx.Done() }

	if d.silent {
		d.log.Info("запуск без окна и браузера (флаг -без-браузера)")
		return func() {}, func() { d.shutdown() }, waitCtx
	}

	win, err := openWindow(d, icon)
	if err != nil {
		if !d.inBrowser {
			// Не поломка, а редкий случай: WebView2 нет в системе. Говорим
			// человеку, где панель, и работаем дальше как раньше.
			d.log.Warn("окно программы не открылось, показываю панель в браузере", "причина", err)
			d.state.Notify("info", "Окно программы открыть не удалось — панель открыта в браузере. "+
				"Адрес: "+d.srv.Addr())
		}
		openPanel()
		go desktop.RunTray(trayMenu(d, icon, openPanel, func() { d.shutdown() }))
		return openPanel, func() { d.shutdown() }, waitCtx
	}

	go desktop.RunTray(trayMenu(d, icon, win.Show, win.Quit))

	// Ctrl+C и выключение компьютера должны закрывать и окно тоже, иначе
	// приложение останется висеть картинкой без начинки.
	go func() {
		<-d.ctx.Done()
		win.Quit()
	}()

	return win.Show, win.Quit, win.Run
}

// openWindow открывает окно программы. Возвращает ошибку, если открывать его
// не надо (флаг) или не в чем (нет WebView2).
func openWindow(d uiDeps, icon string) (*desktop.Window, error) {
	if d.inBrowser {
		d.log.Info("запуск с панелью в браузере (флаг -в-браузере)")
		return nil, fmt.Errorf("окно отключено флагом")
	}
	g := d.cfg.Get().Window
	return desktop.Open(desktop.Options{
		URL:      d.srv.Addr(),
		Title:    windowTitle,
		IconPath: icon,
		// Свои временные файлы WebView2 кладёт рядом с .exe, если не сказать
		// иначе. У стримера .exe лежит в «Загрузках», и мусорить там нельзя.
		DataPath:    filepath.Join(d.dir, "webview"),
		Geometry:    desktop.Geometry{X: g.X, Y: g.Y, Width: g.Width, Height: g.Height, Maximized: g.Maximized},
		SystemFrame: d.sysFrame,
		OnGeometry: func(g desktop.Geometry) {
			// Пишем на диск в стороне от потока окна: запись файла посреди
			// перетаскивания окна дёргала бы картинку.
			go func() {
				err := d.cfg.Update(func(c *config.Config) {
					c.Window = config.Window{X: g.X, Y: g.Y, Width: g.Width, Height: g.Height, Maximized: g.Maximized}
				})
				if err != nil {
					d.log.Warn("не запомнил размер окна", "ошибка", err)
				}
			}()
		},
		OnExternal: func(url string) { openBrowser(url, d.log) },
		OnHide: func() {
			d.log.Info("окно свёрнуто в трей, приложение продолжает работать")
			// Подсказка идёт в стороне от потока окна: окно с сообщением
			// держит поток до нажатия «ОК», а в этом потоке крутится сама
			// программа.
			go trayHint(d)
		},
	})
}

// trayHint один раз объясняет, куда делась программа после крестика.
//
// Windows 11 новые значки возле часов прячет под стрелку, и человек, закрыв
// окно, видит: программа исчезла, значка нет. Первым делом он запустит .exe
// заново — это сработает (вторая копия покажет окно первой), но пугаться он
// будет каждый раз. Поэтому один раз показываем окно с объяснением.
func trayHint(d uiDeps) {
	if d.cfg.Get().TrayHintShown {
		return
	}
	if err := d.cfg.Update(func(c *config.Config) { c.TrayHintShown = true }); err != nil {
		d.log.Warn("не запомнил, что подсказка про трей показана", "ошибка", err)
	}
	desktop.ShowInfo(windowTitle+" продолжает работать",
		"Программа свернулась к часам, справа внизу. Заказы, очередь и виджет в OBS работают дальше.\n\n"+
			"Не видно значка — нажми стрелку рядом с часами: Windows прячет новые значки туда. "+
			"Значок можно перетащить оттуда к остальным, тогда он будет на виду.\n\n"+
			"Открыть окно обратно — щелчок по значку. Закрыть программу совсем — правой кнопкой "+
			"по значку и «Выход».")
}

// trayMenu собирает меню значка возле часов.
func trayMenu(d uiDeps, icon string, onOpen, onQuit func()) desktop.TrayOptions {
	return desktop.TrayOptions{
		IconPath:  icon,
		Tooltip:   windowTitle + " — работает",
		WidgetURL: d.srv.Addr() + "/widget",
		OnOpen:    onOpen,
		OnCopy: func(err error) {
			if err != nil {
				d.log.Warn("не скопировал адрес виджета", "ошибка", err)
				d.state.Notify("error", "Не получилось скопировать адрес виджета. Скопируй его из панели руками.")
				return
			}
			d.log.Info("адрес виджета скопирован из трея")
			d.state.Notify("info", "Адрес виджета скопирован — вставь его в источник «Браузер» в OBS.")
		},
		OnQuit: func() {
			d.log.Info("выход через значок возле часов")
			onQuit()
		},
	}
}

// dataDir — папка с настройками, базой и логом: %APPDATA%\songrequest-yandex на Windows.
// Держим данные там, а не рядом с .exe, чтобы обновление сводилось к замене файла.
func dataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("не нашёл папку для настроек: %w", err)
	}
	dir := filepath.Join(base, appName())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("не смог создать папку %s: %w", dir, err)
	}
	return dir, nil
}

// logPathHint — где искать лог, если приложение не поднялось. Считается без
// логгера: он-то как раз мог и не открыться.
func logPathHint() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return "папка songrequest-yandex в %APPDATA%"
	}
	return filepath.Join(base, appName(), logx.LogFileName)
}

// openBrowser открывает ссылку в обычном браузере. Так открывается вход в
// Twitch: страницы входа тяжёлые, а в окне программы нет ни
// адресной строки, ни кнопки «назад».
func openBrowser(url string, log *logx.Logger) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Warn("не открыл браузер сам", "адрес", url)
		return
	}
	go cmd.Wait() // не оставляем зомби-процесс
}

// waitForPrevious ждёт, пока прежняя копия приложения закроется.
//
// Нужно ровно при обновлении: новая копия запускается той, которую она
// заменяет, и стартовать ей можно только после того, как старая отпустила
// порт, базу и звуковое устройство. Ждём с запасом, но не бесконечно: если
// старая почему-то зависла, лучше честно попробовать и получить понятное
// «уже запущено», чем висеть молча.
//
// Ждём двух вещей: мьютекса (его держит прежняя копия новой сборки) и
// свободного порта (сборки постарше мьютекса не знают). Порт проверяем без
// показа окна — раньше прежняя копия на каждой проверке мигала окном.
func waitForPrevious(log *logx.Logger, port int) *desktop.Lock {
	const (
		limit = 30 * time.Second
		step  = 300 * time.Millisecond
	)
	log.Info("обновился, жду, пока закроется прежняя копия")
	deadline := time.Now().Add(limit)
	var lock *desktop.Lock
	for time.Now().Before(deadline) {
		if lock == nil {
			l, ok, err := desktop.TryLock(lockName())
			if err != nil {
				log.Warn("не создал знак единственной копии", "ошибка", err)
				lock = &desktop.Lock{}
			} else if ok {
				lock = l
			}
		}
		if lock != nil && !desktop.IsRunning(port) {
			log.Info("прежняя копия закрылась, продолжаю запуск")
			return lock
		}
		time.Sleep(step)
	}
	log.Warn("прежняя копия не закрылась вовремя, запускаюсь как есть")
	return lock
}

// lockName — имя мьютекса единственной копии: Local\songrequest-yandex. У
// пробной сборки своё — она нарочно живёт рядом с рабочей.
func lockName() string {
	return `Local\` + appName()
}

// raiseFirst показывает окно уже запущенной копии. Та может ещё подниматься,
// поэтому ждём её ответа до десяти секунд.
func raiseFirst(log *logx.Logger, port int) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if running, raised := desktop.RaiseRunning(port); running {
			log.Info("приложение уже запущено, вторая копия не нужна", "окно_показано", raised)
			if !raised {
				alreadyRunningInfo()
			}
			return
		}
		if time.Now().After(deadline) {
			log.Info("приложение уже запущено, но панель не ответила")
			alreadyRunningInfo()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func alreadyRunningInfo() {
	desktop.ShowInfo(windowTitle+" уже работает",
		"Программа уже запущена. Её значок — возле часов, справа внизу.")
}

// cleanOldWhenServing удаляет .old прошлой версии, как только панель
// ответила. Не ответила за минуту — оставляем файл: пригодится для отката.
// Раньше .old удалялся первой строкой запуска, и если новая сборка падала
// дальше, откатываться было не на что.
func cleanOldWhenServing(ctx context.Context, log *logx.Logger, addr string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if _, err := os.Stat(exe + ".old"); err != nil {
		return
	}
	port := portOf(addr)
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if desktop.IsRunning(port) {
			update.CleanOld(exe)
			log.Info("новая версия поднялась, убрал файл прошлой")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	log.Warn("панель не ответила, файл прошлой версии оставил для отката")
}

// portOf достаёт порт из адреса панели вида «http://127.0.0.1:8992».
func portOf(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimRight(addr[i+1:], "/"))
	return n
}

// humanErr — текст ошибки для окна и панели. Наши ошибки с кодом уже
// написаны для людей; чужие (английские, из библиотек) — только в лог.
func humanErr(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		code, text := errs.Describe(err)
		if code != "" {
			return string(code) + " · " + text
		}
		return text
	}
	// Свои обёртки вида «не открыл базу: <английская причина>» — показываем
	// русскую часть до двоеточия, хвост остаётся в логе.
	head, _, _ := strings.Cut(err.Error(), ": ")
	for _, r := range head {
		if unicode.Is(unicode.Cyrillic, r) {
			rs := []rune(head)
			rs[0] = unicode.ToUpper(rs[0])
			return string(rs) + "."
		}
	}
	return "Что-то пошло не так."
}
