package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/obs"
)

// Связь с OBS.
//
// Формат кадра в панели меняет источник виджета в самом OBS: приложение
// находит его по адресу, ставит размер выбранного формата и растягивает на
// весь кадр. Нет источника — панель предлагает «Добавить в OBS».
//
// Всё делается короткими подключениями: подключились, сделали, ушли. OBS
// запускают и закрывают когда угодно, и держать с ним постоянную связь
// значило бы чинить её каждые полчаса.

// obsSourceName — как называется источник, который приложение добавляет само.
const obsSourceName = "Заказ музыки"

// obsTimeout — сколько ждём OBS на одно действие. Он на этом же компьютере:
// если за десять секунд не ответил, значит, не ответит.
const obsTimeout = 10 * time.Second

// obsMu не даёт двум действиям с OBS идти одновременно: стример может
// щёлкать форматы быстрее, чем OBS отвечает, и тогда последним должен
// победить последний щелчок, а не самый медленный ответ.
var obsMu sync.Mutex

// obsAction — что сделать после подключения.
type obsAction int

const (
	obsCheck obsAction = iota // только посмотреть, есть ли источник
	obsFit                    // найти и подогнать под формат
	obsAdd                    // добавить, если нет, и подогнать
)

func (s *Server) obsClient() obs.Client {
	c := s.cfg.Get()
	return obs.Client{Port: c.OBSPort, Password: c.OBSPassword}
}

// widgetPort — порт, на котором приложение отдаёт виджет: по нему источник
// в OBS и узнаётся.
func (s *Server) widgetPort() int {
	if s.ln == nil {
		return s.cfg.Get().Port
	}
	_, port, _ := net.SplitHostPort(s.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return p
}

// runOBS делает действие и записывает итог в состояние, откуда его видит
// панель.
func (s *Server) runOBS(ctx context.Context, act obsAction) error {
	obsMu.Lock()
	defer obsMu.Unlock()

	s.state.UpdateOBS(func(o *app.OBSInfo) { o.Busy = true })
	err := s.doOBS(ctx, act)
	if err != nil {
		code, text := errs.Describe(err)
		status := "error"
		switch code {
		case errs.OBSUnreachable:
			status = "off"
		case errs.OBSAuth:
			status = "auth"
		case errs.OBSNoSource:
			status = "nosource"
		}
		s.log.Info("OBS", "итог", status, "ошибка", err)
		s.state.UpdateOBS(func(o *app.OBSInfo) {
			o.Busy = false
			o.Status, o.Note, o.NoteCode = status, text, string(code)
			if status != "nosource" {
				o.Source, o.Width, o.Height, o.Scenes = "", 0, 0, nil
			}
		})
	}
	return err
}

func (s *Server) doOBS(parent context.Context, act obsAction) error {
	ctx, cancel := context.WithTimeout(parent, obsTimeout)
	defer cancel()

	conn, err := s.obsClient().Dial(ctx)
	if err != nil {
		return obsError(err)
	}
	defer conn.Close()

	w := s.cfg.Get().Widget
	width, height := config.FrameSize(w.Frame)
	port := s.widgetPort()

	src, err := conn.FindWidget(port)
	switch {
	case errors.Is(err, obs.ErrNoSource) && act == obsAdd:
		url := "http://127.0.0.1:" + strconv.Itoa(port) + "/widget"
		if src, err = conn.Add(obsSourceName, url, width, height); err != nil {
			return obsError(err)
		}
	case err != nil:
		s.state.UpdateOBS(func(o *app.OBSInfo) {
			o.Version = conn.Version
			o.Source, o.Width, o.Height, o.Scenes = "", 0, 0, nil
		})
		return obsError(err)
	case act != obsCheck:
		if err := conn.Fit(src.Name, width, height); err != nil {
			return obsError(err)
		}
		src.Width, src.Height = width, height
	}

	s.log.Info("OBS: источник виджета", "имя", src.Name, "размер", strconv.Itoa(src.Width)+"×"+strconv.Itoa(src.Height),
		"сцены", src.Scenes, "действие", int(act))
	s.state.UpdateOBS(func(o *app.OBSInfo) {
		o.Busy = false
		o.Status, o.Note, o.NoteCode = "ok", "", ""
		o.Version = conn.Version
		o.Source, o.Width, o.Height, o.Scenes = src.Name, src.Width, src.Height, src.Scenes
	})
	return nil
}

// obsError переводит отказ OBS на человеческий язык.
func obsError(err error) error {
	var req *obs.RequestError
	switch {
	case errors.Is(err, obs.ErrAuth):
		return errs.Wrap(errs.OBSAuth, "OBS не принял пароль. Возьми его в OBS: «Инструменты → Настройки сервера WebSocket → Показать сведения о подключении».", err)
	case errors.Is(err, obs.ErrUnreachable):
		return errs.Wrap(errs.OBSUnreachable, "OBS не отвечает. Запусти OBS и включи «Инструменты → Настройки сервера WebSocket → Включить сервер WebSocket».", err)
	case errors.Is(err, obs.ErrNoSource):
		return errs.Wrap(errs.OBSNoSource, "В OBS нет источника с виджетом. Нажми «Добавить в OBS».", err)
	case errors.As(err, &req):
		return errs.Wrap(errs.OBSRequest, "OBS отказался: "+req.Comment, err)
	}
	return errs.Wrap(errs.OBSRequest, "С OBS что-то пошло не так.", err)
}

func (s *Server) obsHandler(act obsAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.runOBS(r.Context(), act); err != nil {
			code, text := errs.Describe(err)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"code": string(code), "error": text})
			return
		}
		writeJSON(w, s.state.Snapshot().OBS)
	}
}

// fitOBSLater подгоняет источник в фоне после сохранения настроек. Молча:
// если OBS не запущен, это не ошибка — стример просто настраивает панель
// заранее, а подгонка случится, когда он нажмёт «Проверить» при живом OBS.
func (s *Server) fitOBSLater() {
	go s.runOBS(s.baseContext(), obsFit)
}

// CheckOBS смотрит при запуске, на связи ли OBS. Тоже молча.
func (s *Server) CheckOBS(ctx context.Context) {
	go s.runOBS(ctx, obsCheck)
}
