package links

import "testing"

// Разбор страницы Яндекс.Музыки — самое хрупкое место во всём приложении:
// официального API нет, вёрстку могут поменять когда угодно. Поэтому здесь
// проверяются все встречающиеся варианты разметки, а не один.

func TestParseYandexOgTags(t *testing.T) {
	html := `<html><head>
	<meta property="og:title" content="Кино — Группа крови">
	<meta property="og:description" content="Кино • Трек • 1988">
	</head></html>`

	m := ParseYandexPage(html)
	if m.Artist != "Кино" || m.Title != "Группа крови" {
		t.Fatalf("разобралось как %q — %q", m.Artist, m.Title)
	}
}

// Порядок в заголовке бывает обратным, и понять это можно только по
// описанию, где артист стоит первым.
func TestParseYandexReversedOrder(t *testing.T) {
	html := `<html><head>
	<meta property="og:title" content="Группа крови — Кино. Слушать онлайн на Яндекс Музыке">
	<meta property="og:description" content="Кино • Трек">
	</head></html>`

	m := ParseYandexPage(html)
	if m.Artist != "Кино" || m.Title != "Группа крови" {
		t.Fatalf("разобралось как %q — %q", m.Artist, m.Title)
	}
}

// Атрибуты в теге могут стоять в другом порядке — регулярка обязана это
// пережить, иначе разбор ломается от косметической правки вёрстки.
func TestParseYandexAttributeOrder(t *testing.T) {
	html := `<meta content="Queen — Bohemian Rhapsody" property="og:title">
	         <meta content="Queen • Трек" property="og:description">`

	m := ParseYandexPage(html)
	if m.Artist != "Queen" || m.Title != "Bohemian Rhapsody" {
		t.Fatalf("разобралось как %q — %q", m.Artist, m.Title)
	}
}

// Мета-тегов может не быть вовсе — тогда остаётся заголовок вкладки.
func TestParseYandexFallsBackToTitleTag(t *testing.T) {
	html := `<html><head><title>Слушать Судно — Молчат Дома на Яндекс Музыке</title></head></html>`

	m := ParseYandexPage(html)
	if m.Title == "" {
		t.Fatal("из заголовка вкладки ничего не вышло")
	}
	if q := m.Query(); q == "" {
		t.Fatal("пустой запрос для поиска")
	}
	t.Logf("получилось: %q", m.Query())
}

func TestParseYandexHandlesEntities(t *testing.T) {
	html := `<meta property="og:title" content="Simon &amp; Garfunkel — The Boxer">
	         <meta property="og:description" content="Simon &amp; Garfunkel • Трек">`

	m := ParseYandexPage(html)
	if m.Artist != "Simon & Garfunkel" {
		t.Fatalf("артист разобрался как %q", m.Artist)
	}
}

// Разметка поменялась и разобрать нечего — это не повод падать: заказ уйдёт
// в обычный поиск по тексту.
func TestParseYandexSurvivesGarbage(t *testing.T) {
	for _, html := range []string{"", "<html></html>", "не html вовсе", "<meta property=\"og:title\">"} {
		m := ParseYandexPage(html)
		if m.Title != "" {
			t.Fatalf("из мусора %q что-то разобралось: %+v", html, m)
		}
	}
}

func TestYandexQueryFormat(t *testing.T) {
	cases := map[Meta]string{
		{Artist: "Кино", Title: "Группа крови"}: "Кино - Группа крови",
		{Title: "Группа крови"}:                 "Группа крови",
		{}:                                      "",
	}
	for m, want := range cases {
		if got := m.Query(); got != want {
			t.Errorf("%+v → %q, ждали %q", m, got, want)
		}
	}
}

// ── ответ Яндекса о треке ────────────────────────────────────────────
//
// Это главный путь с 0.43.1: артиста, название и длительность отдаёт сам
// Яндекс по открытой точке, которой не нужен ключ. Образец ответа взят с
// живого запроса (трек «Группа крови»), лишние поля из него выброшены.

const yandexTrackReplyExample = `{"result":[{
  "title":"Группа крови",
  "durationMs":235100,
  "coverUri":"avatars.yandex.net/get-music-content/95061/4f3808a0.a.5307396-3/%%",
  "available":true,
  "version":"",
  "artists":[{"name":"Кино"}],
  "albums":[{"id":5307396,"coverUri":"avatars.yandex.net/get-music-content/95061/4f3808a0.a.5307396-3/%%"}]
}]}`

func TestYandexTrackReplyIsRead(t *testing.T) {
	meta, ok := ParseYandexTrack([]byte(yandexTrackReplyExample))
	if !ok {
		t.Fatal("ответ Яндекса не разобрался")
	}
	if meta.Artist != "Кино" || meta.Title != "Группа крови" {
		t.Fatalf("артист=%q название=%q", meta.Artist, meta.Title)
	}
	// Длительность — то, ради чего это в первую очередь и затевалось: она
	// отсеивает каверы и часовые лупы при подборе в Spotify.
	if meta.DurationMs != 235100 {
		t.Fatalf("длительность %d", meta.DurationMs)
	}
	if meta.CoverURL != "https://avatars.yandex.net/get-music-content/95061/4f3808a0.a.5307396-3/400x400" {
		t.Fatalf("обложка %q", meta.CoverURL)
	}
	if meta.Query() != "Кино - Группа крови" {
		t.Fatalf("запрос %q", meta.Query())
	}
}

// Приписка к названию («feat», «remix», «live») обязана доехать: без неё
// ремикс и оригинал выглядят одинаково, и в Spotify найдётся не тот.
func TestYandexTrackKeepsVersion(t *testing.T) {
	body := `{"result":[{"title":"Плот","version":"live","artists":[{"name":"Юрий Лоза"}],"durationMs":100}]}`
	meta, ok := ParseYandexTrack([]byte(body))
	if !ok || meta.Title != "Плот live" {
		t.Fatalf("название %q (ok=%v)", meta.Title, ok)
	}
}

// Мусор и пустой ответ — это отказ, а не «трек без названия».
func TestYandexTrackRefusesJunk(t *testing.T) {
	for _, body := range []string{
		``, `{}`, `{"result":[]}`, `не json вовсе`,
		`{"result":[{"title":"   "}]}`,
	} {
		if _, ok := ParseYandexTrack([]byte(body)); ok {
			t.Errorf("мусор принят за трек: %q", body)
		}
	}
}

// Обложка с чужого хоста не принимается: адрес уходит в браузер стримера.
func TestYandexCoverHostIsChecked(t *testing.T) {
	body := `{"result":[{"title":"т","artists":[{"name":"а"}],"coverUri":"злой.example.com/x/%%"}]}`
	meta, ok := ParseYandexTrack([]byte(body))
	if !ok {
		t.Fatal("трек не разобрался")
	}
	if meta.CoverURL != "" {
		t.Fatalf("принята чужая обложка: %q", meta.CoverURL)
	}
}

// ── общая страница Яндекса ───────────────────────────────────────────
//
// 09.09 Яндекс стал отдавать страницу трека с одним общим заголовком на все
// треки. Разбор честно делил его по тире и выдавал артиста «Яндекс Музыка» с
// названием «собираем музыку для вас» — а приложение искало эти слова и
// ставило в эфир случайный ролик, найденный по ним. Зритель платил баллы за
// свою песню и получал чужую.
func TestGenericYandexPageIsRefused(t *testing.T) {
	pages := []string{
		`<html><head><title>Яндекс Музыка — собираем музыку для вас</title></head></html>`,
		`<html><head><title>Яндекс Музыка</title></head></html>`,
		`<html><head><title>  собираем музыку для вас  </title></head></html>`,
		`<html><head><title>Яндекс Музыка · Страница не найдена</title></head></html>`,
	}
	for _, html := range pages {
		if meta := ParseYandexPage(html); meta.Title != "" || meta.Artist != "" {
			t.Errorf("общая страница принята за трек: артист=%q название=%q",
				meta.Artist, meta.Title)
		}
	}
}
