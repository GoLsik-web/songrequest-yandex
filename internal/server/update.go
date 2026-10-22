package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/update"
)

// Обновление приложения по кнопке.
//
// ЗАЧЕМ. Сборки ездили архивом в Телеграме: собрал, отправил, человек
// распаковал и заменил файл руками. На двоих это терпимо, на третьем ломается
// — всегда найдётся тот, кто месяц сидит на старой сборке и жалуется на давно
// починенное.
//
// КАК РЕШЕНО (выбор владельца). Приложение само в интернет за обновлениями не
// ходит: проверка только по кнопке «Проверить обновления». Одним поводом для
// фоновых запросов меньше, а стрим — не то время, когда стоит узнавать про
// новую версию. Нашлась новая — рядом появляется кнопка «Обновить», и она
// делает всё: качает, подменяет программу и запускает её заново.
//
// ЧТО ПРИ ЭТОМ НЕ ТЕРЯЕТСЯ. Настройки, база, очередь и вход в Spotify с
// Twitch лежат в папке данных, а не рядом с программой. Подменяется только
// .exe, поэтому после перезапуска всё на месте.

// updateBusy — идёт ли прямо сейчас проверка или установка.
//
// Отдельным признаком, а не по состоянию в панели: панель может быть закрыта,
// а два нажатия подряд из двух вкладок — обычное дело.
var updateBusy atomic.Bool

// handleUpdateCheck спрашивает GitHub, есть ли версия новее.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !updateBusy.CompareAndSwap(false, true) {
		s.fail(w, errs.New(errs.UpdateCheck, "Проверка уже идёт."))
		return
	}
	defer updateBusy.Store(false)

	repo := strings.TrimSpace(s.cfg.Get().UpdateRepo)
	s.state.UpdateSelf(func(u *app.UpdateInfo) {
		u.Checking = true
		u.Note, u.NoteCode = "", ""
	})
	defer s.state.UpdateSelf(func(u *app.UpdateInfo) { u.Checking = false })

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	rel, err := update.Latest(ctx, repo)
	if err != nil {
		err = manualHint(err, repo)
		code, text := errs.Describe(err)
		s.log.Warn("не проверил обновления", "код", code, "ошибка", err)
		s.noteUpdate(code, text)
		s.fail(w, err)
		return
	}

	now := time.Now()
	newer := update.Newer(s.version, rel.Version)
	s.state.UpdateSelf(func(u *app.UpdateInfo) {
		u.Repo = repo
		u.Current = s.version
		u.Available = newer
		u.Version = rel.Version
		u.Notes = rel.Notes
		u.Size = rel.Size
		u.CheckedAt = &now
		u.Page = update.ReleasesPage(repo)
		u.NoteCode = ""
		if newer {
			u.Note = "Есть новая версия " + rel.Version
		} else {
			u.Note = "У тебя последняя версия"
		}
	})
	s.log.Info("проверил обновления",
		"своя_версия", s.version, "на_гитхабе", rel.Version, "новее", newer)

	writeJSON(w, map[string]any{
		"available": newer,
		"version":   rel.Version,
		"current":   s.version,
	})
}

// handleUpdateInstall качает новую сборку, подменяет программу и перезапускает
// её.
//
// Порядок важен и переставлять его нельзя: сначала скачали и убедились, что
// приехала программа, потом подменили, и только потом запустили новую копию.
// На каждом шаге до подмены отказ ничем не грозит — работает прежняя версия.
func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if !updateBusy.CompareAndSwap(false, true) {
		s.fail(w, errs.New(errs.UpdateCheck, "Обновление уже идёт."))
		return
	}
	done := false
	defer func() {
		if !done {
			updateBusy.Store(false)
		}
	}()

	quit := s.quit.Load()
	if quit == nil {
		// Панель открыта в браузере, а программой никто не управляет: подменить
		// себя можно, а перезапуститься — нет. Честнее не начинать.
		s.fail(w, errs.New(errs.UpdateApply,
			"Обновление на ходу работает только в окне программы. Скачай новую версию со страницы выпусков."))
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		s.fail(w, errs.Wrap(errs.UpdateApply, "Не понял, где лежит сама программа.", err))
		return
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	repo := strings.TrimSpace(s.cfg.Get().UpdateRepo)
	s.state.UpdateSelf(func(u *app.UpdateInfo) {
		u.Installing = true
		u.Note, u.NoteCode = "Качаю обновление…", ""
	})

	ctx, cancel := context.WithTimeout(s.baseContext(), 12*time.Minute)
	defer cancel()

	rel, err := update.Latest(ctx, repo)
	if err != nil {
		s.failUpdate(w, err)
		return
	}
	if !update.Newer(s.version, rel.Version) {
		s.state.UpdateSelf(func(u *app.UpdateInfo) {
			u.Installing = false
			u.Available = false
			u.Note = "У тебя последняя версия"
		})
		s.fail(w, errs.New(errs.UpdateCheck, "Обновлять нечего: у тебя последняя версия."))
		return
	}

	fresh, err := update.Download(ctx, rel, filepath.Join(s.dataDir, "update"))
	if err != nil {
		s.failUpdate(w, err)
		return
	}

	s.state.UpdateSelf(func(u *app.UpdateInfo) { u.Note = "Ставлю " + rel.Version + "…" })
	if err := update.Apply(exePath, fresh); err != nil {
		s.failUpdate(w, err)
		return
	}

	// Новая копия должна дождаться, пока эта освободит порт: иначе она увидит
	// работающее приложение, решит, что запущена второй раз, покажет чужое
	// окно и выйдет. Поэтому у неё свой флаг ожидания.
	cmd := exec.Command(exePath, "-после-обновления")
	cmd.Dir = filepath.Dir(exePath)
	if err := cmd.Start(); err != nil {
		s.failUpdate(w, errs.Wrap(errs.UpdateApply,
			"Программа обновлена, но запустить её заново не вышло. Закрой и открой её сама.", err))
		return
	}

	s.log.Info("обновление поставлено, перезапускаюсь",
		"было", s.version, "стало", rel.Version, "новый_процесс", cmd.Process.Pid)
	s.state.UpdateSelf(func(u *app.UpdateInfo) {
		u.Note = "Обновился до " + rel.Version + ", перезапускаюсь…"
	})

	writeJSON(w, map[string]any{"ok": true, "version": rel.Version})
	done = true

	// Признак работы снимаем по таймеру: если выход почему-то не случился
	// (окно зависло, quit не сработал), кнопка иначе отвечала бы «обновление
	// уже идёт» до самого перезапуска.
	time.AfterFunc(30*time.Second, func() { updateBusy.Store(false) })

	// Выходим не сразу: ответ должен доехать до панели, а окну — успеть его
	// показать. Полторы секунды человек воспримет как «нажал и оно закрылось».
	go func() {
		time.Sleep(1500 * time.Millisecond)
		(*quit)()
	}()
}

// failUpdate сообщает об отказе и снимает признак работы.
func (s *Server) failUpdate(w http.ResponseWriter, err error) {
	err = manualHint(err, strings.TrimSpace(s.cfg.Get().UpdateRepo))
	code, text := errs.Describe(err)
	s.log.Warn("обновление не встало", "код", code, "ошибка", err)
	s.state.UpdateSelf(func(u *app.UpdateInfo) { u.Installing = false })
	s.noteUpdate(code, text)
	s.fail(w, err)
}

// noteUpdate записывает причину отказа в карточку обновления.
func (s *Server) noteUpdate(code errs.Code, text string) {
	repo := strings.TrimSpace(s.cfg.Get().UpdateRepo)
	s.state.UpdateSelf(func(u *app.UpdateInfo) {
		u.Note = text
		u.NoteCode = string(code)
		u.Repo = repo
		u.Current = s.version
		u.Page = update.ReleasesPage(repo)
	})
}

// syncUpdateInfo кладёт в панель то, что известно про обновления без единого
// запроса в сеть: своя версия и куда смотреть.
func (s *Server) syncUpdateInfo() {
	repo := strings.TrimSpace(s.cfg.Get().UpdateRepo)
	s.state.UpdateSelf(func(u *app.UpdateInfo) {
		u.Repo = repo
		u.Current = s.version
		u.Page = update.ReleasesPage(repo)
	})
}

// OnQuit говорит панели, как закрыть программу. Кладёт сюда main — тем же
// способом, каким выходят через значок возле часов.
func (s *Server) OnQuit(f func()) { s.quit.Store(&f) }

// manualHint дописывает к отказу проверки или закачки путь «скачай руками».
// Ссылка есть всегда: при пустом или сломанном репозитории в настройках — на
// страницу по умолчанию.
func manualHint(err error, repo string) error {
	code, text := errs.Describe(err)
	switch code {
	case errs.UpdateCheck, errs.UpdateDownload, errs.UpdateNoFile, errs.UpdateNoRepo:
	default:
		return err
	}
	// Причиной кладём голый текст, а не нашу ошибку: иначе Describe ещё раз
	// дописал бы её объяснение, которое уже в text.
	return errs.Wrap(code, text+" Скачай вручную со страницы выпусков: "+update.ReleasesPage(repo),
		errors.New(err.Error()))
}
