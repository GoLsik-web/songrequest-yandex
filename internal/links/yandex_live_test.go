package links

import (
	"context"
	"os"
	"testing"
)

// Живая проверка: настоящая Яндекс.Музыка отвечает по настоящей ссылке.
//
// Ходит в сеть, поэтому включается только по просьбе:
//
//	SONGREQUEST_LIVE=1 go test ./internal/links/ -run TestLiveYandex -v
//
// Зачем она нужна отдельно от обычных проверок. Обычные проверяют разбор на
// сохранённом ответе — и они спокойно проходили всё то время, пока Яндекс уже
// отдавал страницы без мета-тегов, а приложение подставляло зрителям
// случайные ролики вместо заказанных песен. Разбор был исправен; сломалось то,
// что он разбирал. Поймать такое можно только живым запросом.
func TestLiveYandexTrackByLink(t *testing.T) {
	if os.Getenv("SONGREQUEST_LIVE") == "" {
		t.Skip("живая проверка выключена")
	}

	// Ссылка на трек, который никуда не денется: «Группа крови» группы «Кино».
	const page = "https://music.yandex.ru/album/5307396/track/38633712"

	link, ok := Find(page)
	if !ok || link.Kind != Yandex {
		t.Fatalf("ссылка не опознана: %#v", link)
	}

	meta, err := NewYandexReader().Lookup(context.Background(), link)
	if err != nil {
		t.Fatalf("Яндекс не ответил: %v", err)
	}
	t.Logf("артист=%q название=%q длительность=%d обложка=%q",
		meta.Artist, meta.Title, meta.DurationMs, meta.CoverURL)

	if meta.Artist == "" || meta.Title == "" {
		t.Fatal("Яндекс ответил, но трек не назван")
	}
	// Ровно та поломка, ради которой всё это переписано: общий заголовок
	// сайта вместо названия трека.
	if isGenericYandexTitle(meta.Title) || isGenericYandexTitle(meta.Artist) {
		t.Fatalf("вместо трека приехала общая страница: %q — %q", meta.Artist, meta.Title)
	}
	if meta.DurationMs <= 0 {
		t.Fatal("длительность не приехала, подбор в Spotify будет хуже")
	}
}

// Живая проверка плейлиста и альбома Яндекса — тем же выключателем:
//
//	SONGREQUEST_LIVE=1 go test ./internal/links/ -run TestLiveYandex -v
func TestLiveYandexCollections(t *testing.T) {
	if os.Getenv("SONGREQUEST_LIVE") == "" {
		t.Skip("живая проверка выключена")
	}
	r := NewYandexReader()

	// Редакционный плейлист Яндекса: он публичный и никуда не денется.
	link, ok := Find("https://music.yandex.ru/users/music-blog/playlists/2137")
	if !ok || link.Kind != YandexPlaylist {
		t.Fatalf("ссылка на плейлист не опознана: %#v", link)
	}
	pl, err := r.Playlist(context.Background(), link.Owner, link.ID, 5)
	if err != nil {
		t.Fatalf("плейлист не прочитался: %v", err)
	}
	t.Logf("плейлист %q: взято %d из %d", pl.Title, len(pl.Tracks), pl.Total)
	if len(pl.Tracks) != 5 || pl.Total < 5 {
		t.Fatalf("взято %d из %d, ждали пять", len(pl.Tracks), pl.Total)
	}
	for i, tr := range pl.Tracks {
		t.Logf("  %d. %s — %s (%d мс)", i+1, tr.Artist, tr.Title, tr.DurationMs)
		if tr.Artist == "" || tr.Title == "" {
			t.Fatal("трек без артиста или названия")
		}
	}

	// Альбом «Легенда» группы «Кино»: сборник на 85 треков, публичный.
	alink, ok := Find("https://music.yandex.ru/album/5307396")
	if !ok || alink.Kind != YandexAlbum {
		t.Fatalf("ссылка на альбом не опознана: %#v", alink)
	}
	al, err := r.Album(context.Background(), alink.ID, 3)
	if err != nil {
		t.Fatalf("альбом не прочитался: %v", err)
	}
	t.Logf("альбом %q: взято %d из %d, первый — %s — %s",
		al.Title, len(al.Tracks), al.Total, al.Tracks[0].Artist, al.Tracks[0].Title)
	if len(al.Tracks) != 3 {
		t.Fatalf("взято %d треков, ждали три", len(al.Tracks))
	}
}
