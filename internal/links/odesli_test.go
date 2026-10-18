package links

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOdesliFindsYandexTrack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") == "" {
			t.Error("ссылку не передали")
		}
		w.Write([]byte(`{
		  "entityUniqueId": "SPOTIFY_SONG::abc",
		  "entitiesByUniqueId": {
		    "SPOTIFY_SONG::abc": {"title": "Blinding Lights", "artistName": "The Weeknd", "type": "song"},
		    "YANDEX_SONG::60292250": {"title": "Blinding Lights", "artistName": "The Weeknd", "type": "song"}
		  },
		  "linksByPlatform": {
		    "yandex": {"url": "https://music.yandex.ru/album/10030483/track/60292250", "entityUniqueId": "YANDEX_SONG::60292250"}
		  }
		}`))
	}))
	defer srv.Close()

	o := NewOdesli()
	o.base = srv.URL
	res, err := o.Lookup(context.Background(), "https://open.spotify.com/track/0VjIjW4GlUZAMYd2vXMi3b")
	if err != nil {
		t.Fatal(err)
	}
	if res.YandexTrackID != "60292250" || res.Query() != "The Weeknd - Blinding Lights" {
		t.Fatalf("получили %+v", res)
	}
}

func TestOdesliWithoutYandexGivesQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"entityUniqueId":"ITUNES_SONG::1","entitiesByUniqueId":{"ITUNES_SONG::1":{"title":"Тамада","artistName":"Miyagi & Эндшпиль"}},"linksByPlatform":{}}`))
	}))
	defer srv.Close()
	o := NewOdesli()
	o.base = srv.URL
	res, err := o.Lookup(context.Background(), "https://music.apple.com/ru/song/x/1")
	if err != nil {
		t.Fatal(err)
	}
	if res.YandexTrackID != "" || res.Title != "Тамада" {
		t.Fatalf("получили %+v", res)
	}
}
