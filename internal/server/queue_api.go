package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/queue"
)

// Управление очередью из панели. Частые действия — скип и удаление — идут
// в один запрос без подтверждений. Опасные — очистка очереди и бан — панель
// подтверждает у себя, здесь же требует явного признака.

const panelActor = "стример"

func (s *Server) handleSkip(w http.ResponseWriter, r *http.Request) {
	s.skipCurrent(panelActor)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleQueueRemove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, errs.New(errs.Code(""), "Непонятный номер заказа."))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	refund := r.URL.Query().Get("refund") == "1"
	item, _ := s.queue.Get(id)
	if err := s.removeFromQueue(ctx, id, refund, panelActor); err != nil {
		s.fail(w, errs.Wrap(errs.Code(""), "Не получилось удалить заказ.", err))
		return
	}
	// Зритель панель не видит: без строки в чате его заказ просто пропадал,
	// и было непонятно, вернулись ли баллы.
	if item.Requester != "" && item.Source != queue.SourceDonation {
		s.say(ctx, "@"+item.Requester+", заказ «"+trackLine(item)+"» убрали из очереди"+
			refundTail(refund && item.RedemptionID != ""))
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleQueueTop(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, errs.New(errs.Code(""), "Непонятный номер заказа."))
		return
	}
	if err := s.queue.MoveTop(id); err != nil {
		s.fail(w, errs.Wrap(errs.Code(""), "Не получилось поднять заказ.", err))
		return
	}
	s.syncPlayback()
	writeJSON(w, map[string]bool{"ok": true})
}

// handleQueueReorder принимает новый порядок целиком — так работает
// перетаскивание в панели.
func (s *Server) handleQueueReorder(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ids); err != nil {
		s.fail(w, errs.New(errs.Code(""), "Не разобрал новый порядок очереди."))
		return
	}
	if err := s.queue.Reorder(ids); err != nil {
		s.fail(w, errs.Wrap(errs.Code(""), "Не получилось поменять порядок.", err))
		return
	}
	s.modLog(panelActor, "поменял порядок очереди", "")
	s.syncPlayback()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleQueueClear(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	refund := r.URL.Query().Get("refund") == "1"
	if _, err := s.clearQueue(ctx, refund, panelActor); err != nil {
		s.fail(w, errs.Wrap(errs.Code(""), "Не получилось очистить очередь.", err))
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handlePause останавливает и возобновляет приём заказов. Награда на канале
// тоже приостанавливается: пусть зрители не тратят баллы впустую.
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	on := r.URL.Query().Get("on") == "1"
	s.setOrdersPaused(r.Context(), on)

	if on {
		s.state.Notify("info", "Приём заказов остановлен")
	} else {
		s.state.Notify("info", "Приём заказов возобновлён")
	}
	writeJSON(w, map[string]bool{"paused": on})
}

// handleBan закрывает и открывает зрителю заказы.
func (s *Server) handleBan(w http.ResponseWriter, r *http.Request) {
	login := r.URL.Query().Get("login")
	if login == "" {
		s.fail(w, errs.New(errs.Code(""), "Не сказано, кого банить."))
		return
	}

	if r.URL.Query().Get("undo") == "1" {
		if err := s.unbanMusic(login); err != nil {
			s.fail(w, errs.Wrap(errs.Code(""), "Не получилось снять бан.", err))
			return
		}
		s.modLog(panelActor, "вернул заказы", login)
		s.state.Notify("info", "Заказы снова открыты для "+login)
	} else {
		if err := s.banMusic(login, r.URL.Query().Get("reason")); err != nil {
			s.fail(w, errs.Wrap(errs.Code(""), "Не получилось закрыть заказы.", err))
			return
		}
		s.modLog(panelActor, "закрыл заказы", login)
		s.state.Notify("info", "Заказы закрыты для "+login)
	}

	s.syncExtras()
	writeJSON(w, map[string]bool{"ok": true})
}

// handleHistory отдаёт историю заказов с поиском.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	search := "%" + r.URL.Query().Get("q") + "%"

	rows, err := s.db.SQL().Query(`
		SELECT requester, raw_request, title, artist, outcome, COALESCE(reason, ''), played_at
		  FROM history
		 WHERE requester LIKE ? OR title LIKE ? OR artist LIKE ? OR raw_request LIKE ?
		 ORDER BY played_at DESC LIMIT 200`, search, search, search, search)
	if err != nil {
		s.fail(w, errs.Wrap(errs.Code(""), "Не получилось прочитать историю.", err))
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var requester, raw, title, artist, outcome, reason string
		var at int64
		if err := rows.Scan(&requester, &raw, &title, &artist, &outcome, &reason, &at); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"requester": requester, "raw": raw, "title": title, "artist": artist,
			"outcome": outcome, "reason": reason, "at": time.Unix(at, 0),
		})
	}
	writeJSON(w, out)
}

// handleModLog отдаёт лог действий модераторов: стример должен видеть,
// кто что скипнул и когда.
func (s *Server) handleModLog(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.SQL().Query(
		`SELECT actor, action, COALESCE(target, ''), created_at
		   FROM mod_log ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		s.fail(w, errs.Wrap(errs.Code(""), "Не получилось прочитать лог модераторов.", err))
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var actor, action, target string
		var at int64
		if err := rows.Scan(&actor, &action, &target, &at); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"actor": actor, "action": action, "target": target, "at": time.Unix(at, 0),
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleBans(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.musicBans())
}

// ── плейлисты, ждущие решения ────────────────────────────────────────
//
// Те же два действия, что и в чате, только кнопками. Одно и то же решение
// принимают в двух местах нарочно: стример сидит перед панелью, а модератор —
// в чате, и заставлять кого-то из них ходить в чужое место незачем.

func (s *Server) handlePlaylistApprove(w http.ResponseWriter, r *http.Request) {
	// Срок больше обычного: у плейлиста Яндекса каждый трек ещё надо найти в
	// Spotify, а это запрос на трек. Пять треков — пять запросов подряд.
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	p, added, err := s.approvePlaylist(ctx, r.PathValue("id"), panelActor)
	if err != nil {
		s.fail(w, err)
		return
	}

	tail := ""
	if added < len(p.Tracks) {
		tail = fmt.Sprintf(" (из %d — остальные не нашлись или слишком длинные)", len(p.Tracks))
	}
	s.say(ctx, fmt.Sprintf("Плейлист «%s» от %s одобрен: %d %s в очереди%s",
		p.Title, p.Requester, added, plural(added, "трек", "трека", "треков"), tail))

	writeJSON(w, map[string]any{"ok": true, "added": added})
}

// handleYouTubeAsk — кнопки «Включить» и «Отклонить» у заказа с YouTube.
func (s *Server) handleYouTubeAsk(play bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if _, err := s.decideYouTube(ctx, r.PathValue("id"), play, panelActor); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *Server) handlePlaylistReject(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	p, err := s.rejectPlaylist(ctx, r.PathValue("id"), panelActor)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.say(ctx, fmt.Sprintf("@%s, плейлист «%s» не одобрили. Баллы вернул.", p.Requester, p.Title))
	writeJSON(w, map[string]bool{"ok": true})
}

// setOrdersPaused останавливает или возобновляет приём заказов вместе с
// наградами на Twitch — обеими, за трек и за плейлист.
//
// Одно место для панели и для «!стоп»/«!старт»: команда из чата раньше
// останавливала только приём, а награды на канале оставались открытыми —
// зрители тратили баллы и получали «заказ не принят».
func (s *Server) setOrdersPaused(ctx context.Context, on bool) {
	s.player.SetPaused(on)

	s.mu.Lock()
	ids := []string{s.rewardID, s.playlistRewardID}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, rewardID := range ids {
		if rewardID == "" {
			continue
		}
		if err := s.twitch.SetRewardPaused(ctx, rewardID, on); err != nil {
			s.log.Warn("не приостановил награду", "ошибка", err, "награда", rewardID)
		}
	}
}
