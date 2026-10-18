package youtube

import (
	"testing"
)

// Разбор ответа yt-dlp: он отдаёт то одиночный ролик, то список результатов
// поиска, и поля называются по-разному в зависимости от источника.
func TestParseSingleVideo(t *testing.T) {
	raw := `{
		"id": "fJ9rUzIMcZQ",
		"title": "Queen - Bohemian Rhapsody (Official Video Remastered)",
		"uploader": "Queen Official",
		"artist": "Queen",
		"track": "Bohemian Rhapsody",
		"duration": 355.0,
		"is_live": false,
		"thumbnail": "https://i.ytimg.com/vi/fJ9rUzIMcZQ/hq.jpg",
		"webpage_url": "https://www.youtube.com/watch?v=fJ9rUzIMcZQ"
	}`

	tr := mustParse(t, raw)
	// Название трека точнее заголовка ролика: в заголовке обычно мусор.
	if tr.Title != "Bohemian Rhapsody" {
		t.Fatalf("взяли заголовок ролика вместо названия трека: %q", tr.Title)
	}
	if tr.Artist != "Queen" {
		t.Fatalf("артист: %q", tr.Artist)
	}
	if tr.DurationMs != 355000 {
		t.Fatalf("длительность: %d", tr.DurationMs)
	}
}

// Результат поиска приезжает списком — берём первый.
func TestParseSearchResult(t *testing.T) {
	raw := `{
		"entries": [{
			"id": "abc123",
			"title": "Молчат Дома - Судно",
			"uploader": "Sacred Bones",
			"duration": 221.0,
			"webpage_url": "https://www.youtube.com/watch?v=abc123"
		}]
	}`

	tr := mustParse(t, raw)
	if tr.ID != "abc123" {
		t.Fatalf("id: %q", tr.ID)
	}
	// Отдельного названия трека нет — берём заголовок ролика.
	if tr.Title != "Молчат Дома - Судно" {
		t.Fatalf("название: %q", tr.Title)
	}
	// Артиста тоже нет — подставляем канал.
	if tr.Artist != "Sacred Bones" {
		t.Fatalf("артист: %q", tr.Artist)
	}
}

// Стрим играть нельзя: он не кончится, и очередь встанет навсегда.
func TestLiveStreamIsMarked(t *testing.T) {
	raw := `{"id":"live1","title":"24/7 lofi radio","duration":0,"is_live":true}`
	if tr := mustParse(t, raw); !tr.IsLive {
		t.Fatal("стрим не помечен")
	}
}

func TestLinkDetection(t *testing.T) {
	yes := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"смотри https://YouTube.com/watch?v=dQw4w9WgXcQ",
	}
	no := []string{
		"Queen - Bohemian Rhapsody", "https://open.spotify.com/track/x", "",
		// Так зритель за баллы подсовывал yt-dlp свои ключи: текст содержит
		// «youtube.com/», но идентификатора ролика в нём нет.
		`--config-location=\чужой-сервер\yt.conf youtube.com/`,
		"youtube.com/",
	}

	for _, s := range yes {
		if !IsLink(s) {
			t.Errorf("%q не распознано как ссылка", s)
		}
	}
	for _, s := range no {
		if IsLink(s) {
			t.Errorf("%q ошибочно принято за ссылку", s)
		}
	}
}

// mustParse повторяет разбор из metadata — вынесен, чтобы проверять его
// без запуска yt-dlp.
func mustParse(t *testing.T, s string) *Track {
	t.Helper()
	tr, err := parseInfo([]byte(s))
	if err != nil {
		t.Fatalf("не разобралось: %v", err)
	}
	return tr
}
