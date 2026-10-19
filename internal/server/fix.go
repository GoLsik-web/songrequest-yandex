package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/links"
	"songrequest-yandex/internal/match"
	"songrequest-yandex/internal/queue"
)

// Ручное исправление подбора.
//
// Подбор по названию угадывает, и иногда мимо: кавер вместо оригинала,
// «сокращённая версия», однофамилец. Без возможности поправить это руками
// стримеру остаётся только выкинуть заказ, и зритель теряет и трек, и баллы.
//
// Исправление запоминается: тот же текст заказа в следующий раз даст сразу
// правильный трек, у любого зрителя и без поиска. Поэтому исправлять выгодно —
// каждое действие работает на будущее, а не только на текущий заказ.

// searchLimit — сколько вариантов показываем. Больше десятка никто не
// просматривает, а список перестаёт помещаться на экран целиком.
const searchLimit = 8

// handleSearch ищет треки для ручного выбора.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, []any{})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Ссылку в поле поиска тоже принимаем: проще вставить её, чем
	// перепечатывать название.
	if link, ok := links.Find(query); ok && !link.IsCollection() && link.Kind != links.YouTube && link.Kind != links.VK {
		res, ok := s.fromLink(ctx, query)
		if ok && res.Track != nil {
			writeJSON(w, []foundTrack{asFound(*res.Track)})
			return
		}
		if ok && res.Query != "" {
			query = res.Query
		}
	}

	found, err := s.ym.SearchTracks(ctx, query, searchLimit)
	if err != nil {
		code, text := errs.Describe(err)
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(w, map[string]string{"code": string(code), "error": text})
		return
	}

	out := make([]foundTrack, 0, len(found))
	for _, c := range found {
		out = append(out, asFound(c))
	}
	writeJSON(w, out)
}

// foundTrack — вариант для выбора. Отдельная структура, а не match.Candidate:
// панели нужен готовый к показу артист одной строкой, а не список.
type foundTrack struct {
	ID         string `json:"id"`
	URI        string `json:"uri"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	DurationMs int    `json:"duration_ms"`
	CoverURL   string `json:"cover_url"`
}

func asFound(c match.Candidate) foundTrack {
	return foundTrack{
		ID: c.ID, URI: c.URI, Title: c.Title,
		Artist:     strings.Join(c.Artists, ", "),
		Album:      c.Album,
		DurationMs: c.DurationMs, CoverURL: c.CoverURL,
	}
}

// handleQueueFix подменяет трек в заказе на выбранный вручную.
func (s *Server) handleQueueFix(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		failText(w, http.StatusBadRequest, "Непонятный номер заказа.")
		return
	}

	var body struct {
		TrackID string `json:"track_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil ||
		strings.TrimSpace(body.TrackID) == "" {
		failText(w, http.StatusBadRequest, "Не понял, какой трек ставить.")
		return
	}

	item, err := s.queue.Get(id)
	if err != nil {
		failText(w, http.StatusNotFound, "Этого заказа в очереди уже нет.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	track, err := s.ym.Track(ctx, strings.TrimSpace(body.TrackID))
	if err != nil {
		code, text := errs.Describe(err)
		s.log.Error("не смог взять выбранный трек", "код", code, "ошибка", err)
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(w, map[string]string{"code": string(code), "error": text})
		return
	}

	artist := firstArtist(track.Artists)

	fixed, err := s.queue.Replace(id, queue.Item{
		Provider: "yandex", TrackID: track.ID, URI: track.URI,
		Title: track.Title, Artist: artist,
		DurationMs: track.DurationMs, CoverURL: track.CoverURL,
	})
	if err != nil {
		s.log.Error("не смог подменить трек в очереди", "заказ", id, "ошибка", err)
		failText(w, http.StatusInternalServerError, "Не получилось заменить трек.")
		return
	}

	s.rememberFix(ctx, item, track)

	s.log.Info("трек исправлен вручную",
		"заказ", item.RawRequest, "было", item.Artist+" — "+item.Title,
		"стало", artist+" — "+track.Title)
	s.modLog(panelActor, "исправил трек",
		item.Requester+": было «"+item.Artist+" — "+item.Title+"», стало «"+artist+" — "+track.Title+"»")
	s.state.Notify("info", "Исправлено: "+artist+" — "+track.Title)

	s.syncPlayback()
	writeJSON(w, fixed)
}

// rememberFix запоминает выбор стримера, чтобы тот же заказ впредь находился
// сразу и правильно.
func (s *Server) rememberFix(ctx context.Context, item queue.Item, track match.Candidate) {
	key := match.Key(match.Parse(links.Strip(item.RawRequest)))
	if key == "" {
		return
	}

	artist := firstArtist(track.Artists)

	if err := s.matchCache.Put(ctx, key, match.Hit{
		TrackID: track.ID, Title: track.Title, Artist: artist,
		DurationMs: track.DurationMs, CoverURL: track.CoverURL,
		Score: 1, Manual: true,
	}); err != nil {
		// Заказ уже исправлен — не запомнить обидно, но не страшно.
		s.log.Warn("не запомнил ручное исправление", "заказ", item.RawRequest, "ошибка", err)
	}
}
