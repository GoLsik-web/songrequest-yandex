package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"songrequest-yandex/internal/app"
	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/queue"
)

// Ручки управления играющим заказом.
//
// Все они — КОМАНДЫ: один запрос к Spotify на одно нажатие человека. Читать
// положение в треке через них нельзя и не нужно: панель считает его сама, от
// момента запуска, и рисует полосу без единого запроса. Это не придирка, а
// главное ограничение проекта: Client ID у стримера в режиме разработки, норма
// там тесная, и однажды секундный опрос плеера довёл до паузы в четыре с
// половиной часа — заказы встали на весь вечер.
//
// Своей музыкой стримера отсюда не управляет ничего: он сам ей хозяин, а
// каждая кнопка стоила бы запроса ни за чем.

// orderVolume — на чём стоит ползунок громкости для играющего заказа.
//
// У заказов с YouTube это настройка из config.json: стример выставляет её один
// раз, и она обязана дожить до следующего запуска. У заказов из Spotify —
// громкость устройства, которую помнит сам плеер.
func (s *Server) orderVolume(provider string) int {
	if provider == "youtube" {
		if v := s.cfg.Get().YouTubeVolume; v > 0 {
			return v
		}
		return 100
	}
	if v := s.player.VolumeLevel(); v > 0 {
		return v
	}
	return 100
}

// playerCtx — общий срок для команды плеера. Команды короткие; если Spotify не
// ответил за это время, человеку лучше увидеть отказ и нажать ещё раз, чем
// смотреть на задумавшуюся кнопку.
func playerCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 15*time.Second)
}

// handlePlayerPlay снимает заказ с паузы.
func (s *Server) handlePlayerPlay(w http.ResponseWriter, r *http.Request) {
	s.holdPlayer(w, r, false)
}

// handlePlayerPause ставит заказ на паузу.
//
// Отдельной кнопки «стоп» нет нарочно: остановка — это пауза, а убрать заказ с
// эфира умеет «Скипнуть». Третья кнопка с похожим значком означала бы только
// лишний вопрос «а чем они отличаются».
func (s *Server) handlePlayerPause(w http.ResponseWriter, r *http.Request) {
	s.holdPlayer(w, r, true)
}

func (s *Server) holdPlayer(w http.ResponseWriter, r *http.Request, on bool) {
	ctx, cancel := playerCtx(r)
	defer cancel()

	// Играет заказ — командуем очередью. Играет свой плейлист — Spotify
	// напрямую. Раньше второй ветки не было вовсе, и в панели между заказами
	// не показывалось ни одной кнопки: см. internal/server/owncontrol.go.
	if s.player == nil || s.player.Now() == nil {
		if !s.ownPlaying() {
			s.fail(w, errs.New(errs.PlayerIdle, "Сейчас ничего не играет."))
			return
		}
		if err := s.holdOwn(ctx, on); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true, "paused": on})
		return
	}

	if err := s.player.Hold(ctx, on); err != nil {
		s.fail(w, err)
		return
	}
	s.syncPlayback()
	writeJSON(w, map[string]bool{"ok": true, "paused": on})
}

// handlePlayerNext — «следующий».
//
// У заказа это скип: заказ уходит с эфира, играет следующий из очереди, баллы
// возвращаются заказчику. У своей музыки — команда Spotify переключить трек в
// плейлисте стримера. Кнопка одна, потому что для человека это одно и то же
// действие; развилка живёт здесь, а не у него в голове.
func (s *Server) handlePlayerNext(w http.ResponseWriter, r *http.Request) {
	if s.player != nil && (s.player.Now() != nil || s.player.Waiting()) {
		s.handleSkip(w, r)
		return
	}
	if !s.ownPlaying() {
		s.fail(w, errs.New(errs.PlayerIdle, "Сейчас ничего не играет."))
		return
	}

	ctx, cancel := playerCtx(r)
	defer cancel()
	if err := s.nextOwn(ctx); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handlePlayerSeek перематывает заказ.
//
// Панель шлёт сюда одно число — то место, где человек отпустил ползунок. На
// каждое движение мыши слать нельзя: у Spotify это запрос, а за одно
// перетаскивание их набежали бы сотни.
func (s *Server) handlePlayerSeek(w http.ResponseWriter, r *http.Request) {
	if s.player == nil {
		s.fail(w, errs.New(errs.PlayerIdle, "Сейчас не играет ни один заказ."))
		return
	}
	var body struct {
		PositionMs int `json:"position_ms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		s.fail(w, errs.Wrap(errs.Code(""), "Непонятно, куда перематывать.", err))
		return
	}

	ctx, cancel := playerCtx(r)
	defer cancel()

	if err := s.player.Seek(ctx, body.PositionMs); err != nil {
		s.fail(w, err)
		return
	}
	s.syncPlayback()
	writeJSON(w, map[string]bool{"ok": true})
}

// handlePlayerPrev отматывает заказ в начало.
//
// Именно в начало, а не «предыдущий трек»: предыдущего в очереди физически нет,
// сыгравшее из неё удаляется. Вернуться к отыгравшему можно кнопкой «повторить»
// рядом с ним на главном экране — это другое действие, и путать их не надо.
func (s *Server) handlePlayerPrev(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := playerCtx(r)
	defer cancel()

	// У своей музыки «назад» — это настоящий предыдущий трек плейлиста: там
	// он есть, в отличие от очереди заказов.
	if s.player == nil || s.player.Now() == nil {
		if !s.ownPlaying() {
			s.fail(w, errs.New(errs.PlayerIdle, "Сейчас ничего не играет."))
			return
		}
		if err := s.prevOwn(ctx); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}

	if err := s.player.Seek(ctx, 0); err != nil {
		s.fail(w, err)
		return
	}
	s.syncPlayback()
	writeJSON(w, map[string]bool{"ok": true})
}

// handlePlayerVolume меняет громкость того, что играет.
func (s *Server) handlePlayerVolume(w http.ResponseWriter, r *http.Request) {
	if s.player == nil {
		s.fail(w, errs.New(errs.PlayerIdle, "Сейчас не играет ни один заказ."))
		return
	}
	var body struct {
		Percent int `json:"percent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		s.fail(w, errs.Wrap(errs.Code(""), "Непонятная громкость.", err))
		return
	}

	ctx, cancel := playerCtx(r)
	defer cancel()

	if err := s.player.SetVolumeLevel(ctx, body.Percent); err != nil {
		s.fail(w, err)
		return
	}

	// У заказов с YouTube громкость — это настройка, а не разовое действие:
	// стример выставляет её один раз, и она должна дожить до следующего
	// запуска приложения. Так было и до общего плеера, ломать это незачем.
	if now := s.player.Now(); now != nil && now.Item.Provider == "youtube" {
		if err := s.cfg.Update(func(c *config.Config) { c.YouTubeVolume = body.Percent }); err != nil {
			s.log.Warn("не запомнил громкость заказов с YouTube", "ошибка", err)
		}
	}

	s.syncPlayback()
	writeJSON(w, map[string]bool{"ok": true})
}

// handlePlayerRepeat ставит в очередь трек, который играл последним.
//
// Отдельная кнопка, а не «предыдущий» у плеера: это не перемотка, а новый
// заказ. Баллов он не стоит и вернуть их не может — стример ставит его сам,
// поэтому источник «вручную».
func (s *Server) handlePlayerRepeat(w http.ResponseWriter, r *http.Request) {
	if s.queue == nil {
		s.fail(w, errs.New(errs.PlayerIdle, "Очередь недоступна."))
		return
	}

	last := s.state.Snapshot().LastPlayed
	if last == nil || last.Title == "" {
		s.fail(w, errs.New(errs.PlayerIdle, "Пока нечего повторять: ещё ничего не играло."))
		return
	}
	// У музыки самого стримера адреса нет: что играет, приложение читает из
	// заголовка окна Spotify, а там только название и артист. Ставить нечего.
	if last.Source != app.SourceOrder || last.URI == "" {
		s.fail(w, errs.New(errs.PlayerIdle,
			"Это твоя музыка, а не заказ — приложение её не ставит. Включи в самой Яндекс Музыке."))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	s.enqueue(ctx, queue.Item{
		Source:     queue.SourceManual,
		Requester:  last.Requester,
		RawRequest: last.RawRequest,
		Provider:   last.Provider,
		Via:        last.Via,
		URI:        last.URI,
		Title:      last.Title,
		Artist:     last.Artist,
		DurationMs: last.DurationMs,
		CoverURL:   last.CoverURL,
	})
	s.log.Info("повтор последнего трека", "трек", last.Artist+" — "+last.Title)
	writeJSON(w, map[string]bool{"ok": true})
}
