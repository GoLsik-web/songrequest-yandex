package links

import "testing"

func TestFindSpotify(t *testing.T) {
	cases := []string{
		"https://open.spotify.com/track/11dFghVXANMlKmJXsNCbNl",
		"https://open.spotify.com/intl-ru/track/11dFghVXANMlKmJXsNCbNl?si=abc123",
		"spotify:track:11dFghVXANMlKmJXsNCbNl",
		"врубай https://open.spotify.com/track/11dFghVXANMlKmJXsNCbNl пж",
	}
	for _, text := range cases {
		l, ok := Find(text)
		if !ok || l.Kind != Spotify {
			t.Fatalf("%q не распознано как Spotify: %+v", text, l)
		}
		if l.ID != "11dFghVXANMlKmJXsNCbNl" {
			t.Fatalf("%q → id %q", text, l.ID)
		}
	}
}

func TestFindYouTube(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=fJ9rUzIMcZQ":           "fJ9rUzIMcZQ",
		"https://youtu.be/fJ9rUzIMcZQ":                          "fJ9rUzIMcZQ",
		"https://youtu.be/fJ9rUzIMcZQ?si=xyz":                   "fJ9rUzIMcZQ",
		"https://music.youtube.com/watch?v=fJ9rUzIMcZQ&list=RD": "fJ9rUzIMcZQ",
		"https://www.youtube.com/shorts/fJ9rUzIMcZQ":            "fJ9rUzIMcZQ",
		"https://www.youtube.com/watch?t=42&v=fJ9rUzIMcZQ":      "fJ9rUzIMcZQ",
		"вот это https://youtu.be/fJ9rUzIMcZQ, врубай":          "fJ9rUzIMcZQ",
	}
	for text, want := range cases {
		l, ok := Find(text)
		if !ok || l.Kind != YouTube {
			t.Fatalf("%q не распознано как YouTube: %+v", text, l)
		}
		if l.ID != want {
			t.Fatalf("%q → id %q, ждали %q", text, l.ID, want)
		}
	}
}

func TestFindYandex(t *testing.T) {
	cases := []string{
		"https://music.yandex.ru/album/12345/track/67890",
		"https://music.yandex.com/album/12345/track/67890?from=serp",
		"https://music.yandex.ru/track/67890",
	}
	for _, text := range cases {
		l, ok := Find(text)
		if !ok || l.Kind != Yandex {
			t.Fatalf("%q не распознано как Яндекс.Музыка: %+v", text, l)
		}
		if l.ID != "67890" {
			t.Fatalf("%q → id %q", text, l.ID)
		}
	}
}

func TestFindVK(t *testing.T) {
	for _, text := range []string{
		"https://vk.com/video-123_456",
		"https://vk.ru/audio123_456",
		"https://vkvideo.ru/video-1_2",
	} {
		if l, ok := Find(text); !ok || l.Kind != VK {
			t.Fatalf("%q не распознано как VK: %+v", text, l)
		}
	}
}

func TestPlainTextIsNotALink(t *testing.T) {
	for _, text := range []string{
		"Queen - Bohemian Rhapsody",
		"Кино — Группа крови",
		"",
		"смотри на сайте example.com",
	} {
		if l, ok := Find(text); ok {
			t.Fatalf("%q принято за ссылку: %+v", text, l)
		}
	}
}

// Зрители копируют ссылку вместе со знаком препинания — адрес от этого
// ломается, а трек не находится.
func TestTrailingPunctuationIsTrimmed(t *testing.T) {
	l, ok := Find("врубай (https://music.yandex.ru/album/1/track/2).")
	if !ok {
		t.Fatal("ссылка не нашлась")
	}
	if want := "https://music.yandex.ru/album/1/track/2"; l.URL != want {
		t.Fatalf("адрес %q, ждали %q", l.URL, want)
	}
}

// Рекламные метки не часть ссылки на трек.
func TestTrackingParamsAreDropped(t *testing.T) {
	l, _ := Find("https://music.yandex.ru/album/1/track/2?utm_source=share&from=serp&reqid=xxx")
	if want := "https://music.yandex.ru/album/1/track/2"; l.URL != want {
		t.Fatalf("адрес %q, ждали %q", l.URL, want)
	}
}

// Из текста ссылку надо вырезать: по «врубай вот это пж» искать нечего,
// а «врубай https://... пж» ломает поиск по словам.
func TestStripRemovesLinks(t *testing.T) {
	cases := map[string]string{
		"врубай https://youtu.be/abc123abcde пж":     "врубай пж",
		"spotify:track:11dFghVXANMlKmJXsNCbNl":       "",
		"Queen - Bohemian Rhapsody":                  "Queen - Bohemian Rhapsody",
		"https://music.yandex.ru/track/1 Кино Судно": "Кино Судно",
	}
	for in, want := range cases {
		if got := Strip(in); got != want {
			t.Errorf("%q → %q, ждали %q", in, got, want)
		}
	}
}

// Зритель за баллы может написать что угодно, и адрес из его заказа
// приложение открывает само — со своего компьютера и с куками стримера.
// Раньше домен искался подстрокой в любом месте адреса, включая путь.
func TestForeignHostIsNotOurs(t *testing.T) {
	bad := []string{
		"https://192.168.1.1/music.yandex.ru/track/1",
		"https://злой.example/vk.com/audio",
		"http://127.0.0.1:8977/youtube.com/watch?v=dQw4w9WgXcQ",
		"https://notvk.com/audio1_2",
		"https://злойyoutube.com/watch?v=dQw4w9WgXcQ",
	}
	for _, raw := range bad {
		if link, ok := Find(raw); ok {
			t.Errorf("%q принято за свою ссылку: %s %s", raw, link.Kind, link.URL)
		}
	}
}

// А настоящие ссылки, в том числе с поддомена, разбираться обязаны.
func TestRealHostsStillParse(t *testing.T) {
	good := map[string]Kind{
		"https://music.yandex.ru/album/1/track/2":       Yandex,
		"https://vk.com/audio1_2":                       VK,
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ":     YouTube,
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ": YouTube,
	}
	for raw, kind := range good {
		link, ok := Find(raw)
		if !ok || link.Kind != kind {
			t.Errorf("%q не разобралось как %s (получили %q, %v)", raw, kind, link.Kind, ok)
		}
	}
}

// ── ссылки на пачки треков ───────────────────────────────────────────
//
// Плейлисты и альбомы заказывают отдельной наградой, поэтому отличать их от
// одного трека надо надёжно. Самое опасное место — ссылка на трек внутри
// альбома: она выглядит и как трек, и как альбом, а зритель заказывал трек.

func TestFindCollections(t *testing.T) {
	cases := []struct {
		text  string
		kind  Kind
		id    string
		owner string
	}{
		{"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M", SpotifyPlaylist, "37i9dQZF1DXcBWIGoYBM5M", ""},
		{"spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", SpotifyPlaylist, "37i9dQZF1DXcBWIGoYBM5M", ""},
		{"https://open.spotify.com/intl-ru/album/1ATL5GLyefJaxhQzSPVrLX", SpotifyAlbum, "1ATL5GLyefJaxhQzSPVrLX", ""},
		{"кинь вот это https://music.yandex.ru/users/music-blog/playlists/2137 пж", YandexPlaylist, "2137", "music-blog"},
		{"https://music.yandex.ru/album/5307396", YandexAlbum, "5307396", ""},
	}
	for _, c := range cases {
		link, ok := Find(c.text)
		if !ok {
			t.Errorf("%q не опознана вовсе", c.text)
			continue
		}
		if link.Kind != c.kind || link.ID != c.id || link.Owner != c.owner {
			t.Errorf("%q → вид %q id %q владелец %q; ждали %q %q %q",
				c.text, link.Kind, link.ID, link.Owner, c.kind, c.id, c.owner)
		}
		if !link.IsCollection() {
			t.Errorf("%q не считается пачкой треков", c.text)
		}
	}
}

// Ссылка на трек внутри альбома — это трек. Если спутать, зритель заказал одну
// песню, а приложение поставило бы весь альбом.
func TestTrackInsideAlbumStaysTrack(t *testing.T) {
	for _, text := range []string{
		"https://music.yandex.ru/album/5307396/track/38633712",
		"https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT",
		"spotify:track:4cOdK2wGLETKBW3PvgPWqT",
	} {
		link, ok := Find(text)
		if !ok {
			t.Fatalf("%q не опознана", text)
		}
		if link.IsCollection() {
			t.Errorf("%q принята за плейлист (вид %q)", text, link.Kind)
		}
	}
}

// Один трек пачкой не считается — иначе обычный заказ уходил бы не в ту
// награду и зритель получал бы отказ на ровном месте.
func TestSingleTracksAreNotCollections(t *testing.T) {
	for _, text := range []string{
		"https://youtu.be/dQw4w9WgXcQ",
		"https://music.yandex.ru/track/38633712",
		"https://vk.com/video-1_1",
	} {
		link, ok := Find(text)
		if !ok {
			t.Fatalf("%q не опознана", text)
		}
		if link.IsCollection() {
			t.Errorf("%q принята за пачку", text)
		}
	}
}

func TestAppleAndDeezerLinks(t *testing.T) {
	cases := map[string]Kind{
		"https://music.apple.com/ru/album/blinding-lights/1488408555?i=1488408568": Apple,
		"https://music.apple.com/us/song/blinding-lights/1488408568":               Apple,
		"https://www.deezer.com/track/908604612":                                   Deezer,
	}
	for in, want := range cases {
		l, ok := Find(in)
		if !ok || l.Kind != want {
			t.Errorf("%s: получили %q, ждали %q", in, l.Kind, want)
		}
	}
	if _, ok := Find("https://music.apple.com/ru/album/after-hours/1499378108"); ok {
		t.Error("альбом Apple не должен считаться треком")
	}
}
