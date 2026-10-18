package server

import (
	"context"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/youtube"
)

// setupYouTube готовит запасной проигрыватель.
//
// Инструменты ищутся и качаются в фоне: первый запуск не должен ждать
// загрузки, а без YouTube приложение прекрасно работает — просто часть
// заказов не сыграет.
func (s *Server) setupYouTube(ctx context.Context) {
	s.ytTools = youtube.NewTools(s.log, s.dataDir)
	s.youtube = youtube.NewPlayer(s.log, s.ytTools)
	s.applyYouTubeSettings()

	if s.player != nil {
		s.player.SetYouTube(s.youtube)
	}

	go func() {
		cfg := s.cfg.Get()

		// Первый запуск качает yt-dlp и mpv — вместе это тридцать с лишним
		// мегабайт и до минуты времени. Пока идёт загрузка, в панели должно
		// быть написано, что происходит: иначе там просто «не готов», и
		// человек решает, что сломалось.
		s.state.UpdateYouTube(func(y *app.YouTubeInfo) {
			y.Ready = false
			y.Note = "Готовлю запасной проигрыватель: качаю yt-dlp и mpv (около 35 МБ). Это разовая загрузка."
			y.NoteCode = ""
		})

		if err := s.ytTools.Ensure(ctx, cfg.YtDlpPath, cfg.MpvPath); err != nil {
			code, text := errs.Describe(err)
			s.log.Warn("запасной проигрыватель не готов", "код", code, "ошибка", err)
			s.state.UpdateYouTube(func(y *app.YouTubeInfo) {
				y.Ready = false
				y.Note = text
				y.NoteCode = string(code)
			})
			return
		}

		ytdlp, mpv := s.ytTools.Paths()
		s.log.Info("запасной проигрыватель готов", "yt-dlp", ytdlp, "mpv", mpv)
		s.state.UpdateYouTube(func(y *app.YouTubeInfo) {
			y.Ready = true
			y.YtDlp = ytdlp
			y.Mpv = mpv
			y.Device = s.cfg.Get().AudioDevice
			y.Note = ""
			y.NoteCode = ""
		})

		// Список устройств нужен, чтобы стример выбрал виртуальный кабель
		// из списка, а не вписывал его имя руками с ошибкой в регистре.
		if devices, err := s.youtube.Devices(ctx); err == nil {
			s.state.UpdateYouTube(func(y *app.YouTubeInfo) { y.Devices = devices })
		}
	}()
}

// StartYouTube поднимает запасной проигрыватель.
func (s *Server) StartYouTube(ctx context.Context) { s.setupYouTube(ctx) }

// StopYouTube убивает mpv при закрытии приложения: иначе он останется
// занимать звуковое устройство после того, как приложение закрыли.
func (s *Server) StopYouTube() {
	if s.youtube != nil {
		s.youtube.Stop()
	}
}

// applyYouTubeSettings переносит настройки в проигрыватель YouTube.
//
// Раньше они читались один раз при запуске, а панель обещала «применится к
// следующему заказу». Обещание было ложным: до перезапуска ничего не менялось.
func (s *Server) applyYouTubeSettings() {
	if s.youtube == nil {
		return
	}
	cfg := s.cfg.Get()
	s.youtube.SetOptions(cfg.AudioDevice, cfg.YouTubeBrowser, cfg.YouTubeVolume)
}
