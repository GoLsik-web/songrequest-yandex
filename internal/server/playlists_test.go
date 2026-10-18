package server

import (
	"context"
	"net/http"
	"testing"

	"songrequest-yandex/internal/config"
	"songrequest-yandex/internal/queue"
)

// Плейлисты, ждущие решения.
//
// Проверяем то, что не требует ни Twitch, ни сети: очередь ожидания, номера
// для команд, превращение трека плейлиста в заказ. Само чтение плейлистов
// живьём проверяется в internal/links (Яндекс) и руками (Spotify).

func playlistServer(t *testing.T) *Server {
	t.Helper()
	srv, _ := newTestServer(t, func(c *config.Config) {
		c.PlaylistReward = true
		c.PlaylistMaxTracks = 3
		c.MaxTrackSeconds = 8 * 60
	}, func(w http.ResponseWriter, r *http.Request) {})
	return srv
}

func samplePlaylist(title string, tracks ...pendingTrack) pendingPlaylist {
	return pendingPlaylist{
		Requester: "zritel", RequesterLogin: "zritel",
		Source: "Spotify", Title: title, Tracks: tracks,
		Total: len(tracks),
	}
}

func TestPendingPlaylistsGetOwnNumbers(t *testing.T) {
	srv := playlistServer(t)

	srv.addPending(samplePlaylist("первый", pendingTrack{Artist: "а", Title: "б"}))
	srv.addPending(samplePlaylist("второй", pendingTrack{Artist: "в", Title: "г"}))

	list := srv.pendingPlaylists()
	if len(list) != 2 {
		t.Fatalf("в ожидании %d плейлистов", len(list))
	}
	if list[0].ID == list[1].ID {
		t.Fatalf("номера совпали: %q", list[0].ID)
	}

	// Пока ждёт больше одного, номер обязателен: одобрить наугад не тот
	// плейлист значит пустить в эфир полчаса не той музыки.
	if _, err := srv.takePending(""); err == nil {
		t.Fatal("без номера решили судьбу одного из двух плейлистов")
	}

	got, err := srv.takePending(list[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "второй" {
		t.Fatalf("забрали %q вместо «второй»", got.Title)
	}

	// А когда остался один — номер уже не нужен.
	last, err := srv.takePending("")
	if err != nil {
		t.Fatal(err)
	}
	if last.Title != "первый" {
		t.Fatalf("забрали %q вместо «первый»", last.Title)
	}
	if len(srv.pendingPlaylists()) != 0 {
		t.Fatal("список ожидания не опустел")
	}
}

// Один и тот же плейлист нельзя решить дважды: иначе «одобрить» два раза
// подряд поставило бы в очередь два одинаковых набора треков.
func TestPendingPlaylistIsTakenOnce(t *testing.T) {
	srv := playlistServer(t)
	srv.addPending(samplePlaylist("единственный", pendingTrack{Artist: "а", Title: "б"}))

	id := srv.pendingPlaylists()[0].ID
	if _, err := srv.takePending(id); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.takePending(id); err == nil {
		t.Fatal("тот же плейлист забрали второй раз")
	}
}

// Ждущие плейлисты переживают перезапуск: заказ оплачен, и забыть о нём
// значит молча съесть чужие баллы.
func TestPendingPlaylistsSurviveRestart(t *testing.T) {
	srv := playlistServer(t)
	srv.addPending(samplePlaylist("переживёт", pendingTrack{Artist: "а", Title: "б"}))
	was := srv.pendingPlaylists()[0].ID

	// Забываем всё, что держали в памяти, и читаем заново — ровно то, что
	// делает приложение при запуске.
	srv.mu.Lock()
	srv.playlists = nil
	srv.playlistNo = 0
	srv.mu.Unlock()
	srv.loadPlaylists()

	list := srv.pendingPlaylists()
	if len(list) != 1 || list[0].ID != was {
		t.Fatalf("после перезапуска в ожидании %#v", list)
	}

	// Номера продолжаются с того места, где остановились: иначе новый
	// плейлист получил бы уже занятый номер, и «одобрить 1» решало бы
	// судьбу не того заказа.
	srv.addPending(samplePlaylist("новый", pendingTrack{Artist: "в", Title: "г"}))
	fresh := srv.pendingPlaylists()
	if fresh[0].ID == fresh[1].ID {
		t.Fatalf("номер повторился: %q", fresh[1].ID)
	}
}

// Готовый трек Spotify превращается в заказ без единого запроса в сеть.
func TestPlaylistItemFromSpotifyTrackNeedsNoSearch(t *testing.T) {
	srv := playlistServer(t)
	cfg := srv.cfg.Get()

	p := samplePlaylist("набор")
	item, ok := srv.playlistItem(context.Background(), p, pendingTrack{
		Artist: "Кино", Title: "Группа крови", DurationMs: 235100,
		Provider: "yandex", URI: "spotify:track:xxx", TrackID: "xxx",
	}, cfg)
	if !ok {
		t.Fatal("готовый трек Spotify не стал заказом")
	}
	if item.Provider != "yandex" || item.URI != "spotify:track:xxx" {
		t.Fatalf("заказ собрался не так: %#v", item)
	}
	if item.Source != queue.SourcePlaylist {
		t.Fatalf("источник заказа %q, ждали %q", item.Source, queue.SourcePlaylist)
	}
	if item.Requester != "zritel" {
		t.Fatalf("заказчик %q", item.Requester)
	}
}

// Слишком длинный трек не попадает в очередь даже из одобренного плейлиста:
// часовой микс посреди эфира — не то, на что соглашались, нажимая «одобрить».
func TestPlaylistSkipsTooLongTracks(t *testing.T) {
	srv := playlistServer(t)
	cfg := srv.cfg.Get()

	_, ok := srv.playlistItem(context.Background(), samplePlaylist("набор"), pendingTrack{
		Artist: "кто-то", Title: "часовой микс", DurationMs: 60 * 60 * 1000,
		Provider: "yandex", URI: "spotify:track:long",
	}, cfg)
	if ok {
		t.Fatal("часовой трек прошёл в очередь")
	}
}
