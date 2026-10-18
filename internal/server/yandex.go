package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/diag"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/yandex"
)

// connYandex — как Яндекс называется в списке подключений панели.
const connYandex = "Яндекс"

// handleYandexLogin открывает вход в Яндекс.
//
// В своём окне: оно видит адрес, на который Яндекс возвращает после входа, и
// забирает токен само. Окна нет (панель открыта в браузере) — отдаём адрес,
// а токен человек вставит руками: браузер нам его не покажет.
func (s *Server) handleYandexLogin(w http.ResponseWriter, r *http.Request) {
	url := yandex.AuthURL()
	if open := s.openAuth.Load(); open != nil {
		err := (*open)(url, func(token string) {
			ctx, cancel := context.WithTimeout(s.baseContext(), 30*time.Second)
			defer cancel()
			s.acceptYandexToken(ctx, token)
		})
		if err == nil {
			writeJSON(w, map[string]any{"url": url, "in_app": true})
			return
		}
		s.log.Warn("не смог открыть вход в своём окне", "ошибка", err)
	}
	writeJSON(w, map[string]any{"url": url, "in_app": false})
}

// handleYandexToken принимает токен, вставленный руками: целиком адрес из
// браузера после входа или сам токен.
func (s *Server) handleYandexToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "не разобрал запрос", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	me, err := s.acceptYandexToken(ctx, body.Token)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, me)
}

// acceptYandexToken проверяет токен и включает всё, что от него зависит.
func (s *Server) acceptYandexToken(ctx context.Context, token string) (*yandex.Me, error) {
	me, err := s.ym.SetToken(ctx, token)
	s.syncYandexInfo()
	if err != nil {
		s.log.Warn("токен Яндекса не подошёл", "ошибка", err)
		s.state.NotifyError(err)
		return nil, err
	}
	s.log.Info("Яндекс подключён", "аккаунт", me.Name())
	s.state.Notify("info", "Яндекс Музыка подключена: "+me.Name())
	if problem := yandex.PlusProblem(me); problem != nil {
		s.state.NotifyError(problem)
	}
	s.ym.Ynison().Start(s.baseContext())
	return me, nil
}

func (s *Server) handleYandexLogout(w http.ResponseWriter, r *http.Request) {
	s.ym.Logout()
	s.syncYandexInfo()
	s.state.Notify("info", "Яндекс Музыка отключена")
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleYandexCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	me, err := s.ym.CheckAccount(ctx)
	s.syncYandexInfo()
	if err != nil {
		s.state.NotifyError(err)
		s.fail(w, err)
		return
	}
	text := "Яндекс на связи: " + me.Name()
	if ok, device, _ := s.ym.Ynison().Status(); ok {
		if device != "" {
			text += " · играет на «" + device + "»"
		}
	} else {
		text += " · очередь Ynison недоступна, заказы пойдут в плейлист"
	}
	s.state.Notify("info", text)
	if problem := yandex.PlusProblem(me); problem != nil {
		s.state.NotifyError(problem)
	}
	writeJSON(w, me)
}

// handleYandexSnapshot запоминает, что играет, — кнопкой из панели.
func (s *Server) handleYandexSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	snap, err := s.ym.Capture(ctx)
	if err != nil {
		s.state.NotifyError(err)
		s.fail(w, err)
		return
	}
	if s.player != nil {
		s.player.SetSnapshot(snap)
	}
	s.snapMu.Lock()
	s.snap = snap
	s.snapMu.Unlock()

	at := snap.CapturedAt
	s.state.UpdateYandex(func(info *app.YandexInfo) {
		info.SnapshotText = snap.Describe()
		info.SnapshotAt = &at
	})
	s.state.Notify("info", "Запомнил: "+snap.Describe())
	writeJSON(w, snap)
}

// handleYandexRestore возвращает музыку в запомненное состояние.
func (s *Server) handleYandexRestore(w http.ResponseWriter, r *http.Request) {
	if s.player != nil {
		if fromPlayer := s.player.Snapshot(); fromPlayer != nil {
			s.snapMu.Lock()
			s.snap = fromPlayer
			s.snapMu.Unlock()
		}
	}
	s.snapMu.Lock()
	snap := s.snap
	s.snapMu.Unlock()

	if snap == nil {
		err := errs.New(errs.YandexNothing, "Сначала нажми «Запомнить состояние».")
		s.state.NotifyError(err)
		s.fail(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// Кнопку нажал человек — возвращаем, даже если он только что
	// переключил трек руками.
	outcome, err := s.ym.Restore(ctx, snap, "", true)
	if err != nil {
		s.state.NotifyError(err)
		s.startFresh(ctx, s.cfg.Get(), s.cfg.Get().EffectiveResumeMode(), snap)
		s.fail(w, err)
		return
	}
	if outcome.Restored {
		s.state.Notify("info", "Музыка возвращена: "+snap.Describe())
		if s.player != nil {
			s.player.SetSnapshot(nil)
		}
	} else {
		s.state.NotifyCode("warn", string(outcome.Code), outcome.Message)
	}
	s.afterRestore(ctx, snap, outcome)
	writeJSON(w, outcome)
}

// syncYandexInfo пересобирает карточку Яндекса.
func (s *Server) syncYandexInfo() {
	me := s.ym.Account()
	info := app.YandexInfo{Connected: s.ym.Connected(), Plus: "unknown"}
	if me != nil {
		info.Account = me.Name()
		info.Login = me.Login
		if me.PlusKnown {
			info.Plus = "no"
			if me.HasPlus {
				info.Plus = "yes"
			}
		}
		if problem := yandex.PlusProblem(me); problem != nil {
			info.PlusNote = problem.Message
			info.PlusNoteCode = string(problem.Code)
		}
	}

	ynOK, device, ynErr := s.ym.Ynison().Status()
	info.Ynison = ynOK
	info.YnisonDevice = device
	switch {
	case !info.Connected:
	case ynOK && !s.ym.Ynison().HasPlayer():
		info.YnisonNote = "Яндекс Музыка нигде не играет — включите любой трек в приложении."
	case !ynOK && ynErr != nil:
		code, text := errs.Describe(ynErr)
		info.YnisonNote = string(code) + " · " + text + " Заказы пойдут в плейлист «" + yandex.OrdersPlaylistTitle + "»."
	case !ynOK:
		info.YnisonNote = "Подключаюсь к очереди Яндекса…"
	}

	s.snapMu.Lock()
	snap := s.snap
	s.snapMu.Unlock()
	if s.player != nil {
		if fromPlayer := s.player.Snapshot(); fromPlayer != nil {
			snap = fromPlayer
		}
	}
	if snap != nil {
		at := snap.CapturedAt
		info.SnapshotText = snap.Describe()
		info.SnapshotAt = &at
	}
	s.state.SetYandex(info)

	switch {
	case !info.Connected:
		s.state.SetConnIdle(connYandex, "Не подключён")
	case me == nil:
		if done, err := s.ym.LastCheck(); done && err != nil {
			code, text := errs.Describe(err)
			s.state.SetConnFail(connYandex, string(code)+" · "+text)
		} else {
			s.state.SetConnIdle(connYandex, "Проверяю связь…")
		}
	case info.Plus == "no":
		s.state.SetConnFail(connYandex, string(errs.YandexNoPlus)+" · нет Плюса")
	case !ynOK:
		s.state.SetConnOK(connYandex, me.Name()+" · очередь недоступна, пишу в плейлист")
	default:
		s.state.SetConnOK(connYandex, me.Name())
	}
}

// noteYandexError — отказ Яндекса. Слетевший вход должен сразу показаться
// в карточке, а не ждать следующей проверки.
func (s *Server) noteYandexError(err error) {
	if yandex.IsAuthError(err) {
		s.syncYandexInfo()
	}
}

// handleYandexPlaylists отдаёт плейлисты стримера для выпадающего списка.
func (s *Server) handleYandexPlaylists(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := s.ym.Playlists(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, list)
}

// handleDiagExport отдаёт zip с логом и настройками без секретов.
func (s *Server) handleDiagExport(w http.ResponseWriter, r *http.Request) {
	snapshot := s.state.Snapshot()
	conns := map[string]string{}
	for _, c := range snapshot.Connections {
		status := "не подключено"
		if c.Connected {
			status = "подключено"
		}
		if c.Detail != "" {
			status += " (" + c.Detail + ")"
		}
		conns[c.Name] = status
	}
	data, name, err := diag.Build(s.dataDir, s.cfg.Path(), s.log.Redactor, diag.Info{
		Version:     s.version,
		Connections: conns,
	}, s.diagTables())
	if err != nil {
		s.state.NotifyError(err)
		s.fail(w, err)
		return
	}
	s.log.Info("выгрузил архив с логом", "размер_байт", len(data))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Write(data)
}

// fail отвечает панели кодом и понятным текстом. Настоящую ошибку пишем в
// лог обязательно: без неё причину отказа потом не найти.
func (s *Server) fail(w http.ResponseWriter, err error) {
	code, text := errs.Describe(err)
	s.log.Error("отказ панели", "код", code, "текст", text, "ошибка", err)
	s.noteYandexError(err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, map[string]string{"code": string(code), "error": text})
}

// SyncYandex обновляет карточку Яндекса при старте и поднимает очередь,
// если вход уже был.
func (s *Server) SyncYandex(ctx context.Context) {
	s.ym.Ynison().OnChange = s.onYnisonChange
	if s.ym.Connected() {
		s.ym.Ynison().Start(ctx)
	}
	s.syncYandexInfo()
}

// onYnisonChange — очередь Яндекса прислала новое состояние. Карточку
// пересобираем только при смене связи: состояние приходит на каждый трек.
func (s *Server) onYnisonChange() {
	ok, device, _ := s.ym.Ynison().Status()
	s.mu.Lock()
	changed := ok != s.ynisonWas || device != s.ynisonDevice
	s.ynisonWas, s.ynisonDevice = ok, device
	s.mu.Unlock()
	if changed {
		s.syncYandexInfo()
	}
}
