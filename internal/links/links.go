// Package links разбирает то, что кинул зритель.
//
// Ссылка точнее любого поиска: зритель уже указал конкретный трек, и гадать
// по названию не нужно. Поэтому ссылка всегда важнее текста, а поиск по
// словам остаётся запасным вариантом.
package links

import (
	"net/url"
	"regexp"
	"strings"
)

// Kind — откуда ссылка.
type Kind string

const (
	// None — ссылки нет, это просто текст.
	None Kind = ""
	// Spotify — можно взять трек напрямую по идентификатору, без поиска.
	Spotify Kind = "spotify"
	// YouTube — метаданные достанет yt-dlp.
	YouTube Kind = "youtube"
	// Yandex — трек на Яндекс.Музыке. Что за трек, спрашиваем у самого
	// Яндекса по открытой точке, см. yandex.go.
	Yandex Kind = "yandex"
	// VK — метаданные достанет yt-dlp, он умеет и VK.
	VK Kind = "vk"
	// Apple — Apple Music. Что за трек, узнаём через song.link (odesli.go).
	Apple Kind = "apple"
	// Deezer — тоже через song.link.
	Deezer Kind = "deezer"

	// Дальше — не один трек, а сразу пачка.
	//
	// Заказывают их отдельной наградой и по отдельным правилам: цена своя,
	// число треков ограничено, и в эфир пачка идёт только после того, как её
	// одобрил стример или модератор. Поэтому и вид ссылки отдельный: обычная
	// награда за трек такие ссылки не принимает вовсе, иначе зритель отдал бы
	// баллы за один трек и ждал бы сорок.

	// SpotifyPlaylist — плейлист Spotify. Треки приезжают готовыми: искать
	// ничего не надо, они уже там, где играют.
	SpotifyPlaylist Kind = "spotify_playlist"
	// SpotifyAlbum — альбом Spotify, тоже готовые треки.
	SpotifyAlbum Kind = "spotify_album"
	// YandexPlaylist — плейлист Яндекс.Музыки. Оттуда приезжают имена
	// артистов и названия; играть каждый трек всё равно придётся из Spotify
	// или с YouTube.
	YandexPlaylist Kind = "yandex_playlist"
	// YandexAlbum — альбом Яндекс.Музыки, то же самое.
	YandexAlbum Kind = "yandex_album"
)

// Link — разобранная ссылка.
type Link struct {
	Kind Kind
	// URL — очищенный адрес: без меток рекламных кампаний и прочего мусора,
	// который ломает и сравнение, и запросы.
	URL string
	// ID — идентификатор трека, если он есть в самой ссылке. У плейлиста
	// Яндекса здесь его номер у владельца («kind» в терминах Яндекса).
	ID string
	// Owner — чей плейлист. Заполняется только у плейлистов Яндекса: там
	// адрес состоит из логина владельца и номера, и без логина плейлист не
	// запросить.
	Owner string
}

// IsCollection сообщает, что по ссылке лежит не один трек, а пачка.
func (l Link) IsCollection() bool {
	switch l.Kind {
	case SpotifyPlaylist, SpotifyAlbum, YandexPlaylist, YandexAlbum:
		return true
	}
	return false
}

// Найденное — не ссылка, а адрес внутри текста: зрители пишут
// «врубай вот это <ссылка> пж», и вырезать её надо аккуратно.
var urlPattern = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"']+`)

var (
	spotifyTrack = regexp.MustCompile(`(?i)(?:open\.spotify\.com/(?:intl-[a-z]{2}/)?track/|spotify:track:)([a-zA-Z0-9]{22})`)
	youtubeID    = regexp.MustCompile(`(?i)(?:youtube\.com/(?:watch\?(?:.*&)?v=|shorts/|embed/|live/)|youtu\.be/|music\.youtube\.com/watch\?(?:.*&)?v=)([a-zA-Z0-9_\-]{11})`)
	yandexTrack  = regexp.MustCompile(`(?i)music\.yandex\.[a-z]+/(?:album/(\d+)/track/(\d+)|track/(\d+))`)
	vkAny        = regexp.MustCompile(`(?i)(?:vk\.com|vk\.ru|vkvideo\.ru)/`)

	// Пачки треков.
	//
	// Порядок проверки важен: ссылка на трек внутри альбома выглядит как
	// «album/N/track/M», и если сначала искать альбом, трек потеряется — а
	// зритель заказывал именно его.
	spotifyList  = regexp.MustCompile(`(?i)(?:open\.spotify\.com/(?:intl-[a-z]{2}/)?playlist/|spotify:playlist:)([a-zA-Z0-9]{22})`)
	spotifyAlbum = regexp.MustCompile(`(?i)(?:open\.spotify\.com/(?:intl-[a-z]{2}/)?album/|spotify:album:)([a-zA-Z0-9]{22})`)
	// Логин владельца у Яндекса бывает с точками, дефисами и подчёркиваниями.
	yandexList  = regexp.MustCompile(`(?i)music\.yandex\.[a-z]+/users/([a-zA-Z0-9._\-]{1,64})/playlists/(\d+)`)
	yandexAlbum = regexp.MustCompile(`(?i)music\.yandex\.[a-z]+/album/(\d+)`)
)

// Find ищет в тексте ссылку и определяет, что это.
//
// Возвращает false, если ссылки нет или она не из тех, что мы умеем.
func Find(text string) (Link, bool) {
	// Спотифаевский uri пишется без http, поэтому его ищем прямо в тексте.
	//
	// Трек первым: ссылка на трек внутри альбома содержит и то и другое, а
	// заказывал зритель именно трек.
	if m := spotifyTrack.FindStringSubmatch(text); m != nil {
		return Link{Kind: Spotify, ID: m[1], URL: "https://open.spotify.com/track/" + m[1]}, true
	}
	if m := spotifyList.FindStringSubmatch(text); m != nil {
		return Link{Kind: SpotifyPlaylist, ID: m[1],
			URL: "https://open.spotify.com/playlist/" + m[1]}, true
	}
	if m := spotifyAlbum.FindStringSubmatch(text); m != nil {
		return Link{Kind: SpotifyAlbum, ID: m[1],
			URL: "https://open.spotify.com/album/" + m[1]}, true
	}

	raw := urlPattern.FindString(text)
	if raw == "" {
		return Link{}, false
	}
	if strings.HasPrefix(strings.ToLower(raw), "www.") {
		raw = "https://" + raw
	}
	// Зрители часто копируют ссылку вместе с точкой или скобкой в конце.
	raw = strings.TrimRight(raw, ".,;:!?)]}»\"'")

	// Дальше решаем по настоящему имени хоста, а не по подстроке в адресе.
	//
	// Раньше здесь искалось «music.yandex.» и «vk.com/» где угодно в строке —
	// в том числе в пути. Зритель за баллы писал заказ вида
	// «https://192.168.1.1/music.yandex.ru/track/1», приложение объявляло это
	// Яндекс.Музыкой и уходило читать страницу с адреса, который он выбрал:
	// с компьютера стримера, из его домашней сети. С «vk.com/» в пути тот же
	// адрес уезжал в yt-dlp, который ходит с куками из браузера стримера.
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Link{}, false
	}
	host := strings.ToLower(u.Hostname())

	switch {
	case hostIn(host, "youtube.com", "youtu.be", "music.youtube.com"):
		m := youtubeID.FindStringSubmatch(raw)
		if m == nil {
			return Link{}, false
		}
		return Link{Kind: YouTube, ID: m[1], URL: "https://www.youtube.com/watch?v=" + m[1]}, true

	case host == "music.yandex.ru" || host == "music.yandex.com" ||
		host == "music.yandex.by" || host == "music.yandex.kz" || host == "music.yandex.uz":
		// Опять же трек первым: «album/N/track/M» — это трек, а не альбом.
		if m := yandexTrack.FindStringSubmatch(raw); m != nil {
			id := firstFilled(m[2], m[3])
			return Link{Kind: Yandex, ID: id, URL: clean(raw)}, true
		}
		if m := yandexList.FindStringSubmatch(raw); m != nil {
			return Link{Kind: YandexPlaylist, Owner: m[1], ID: m[2], URL: clean(raw)}, true
		}
		if m := yandexAlbum.FindStringSubmatch(raw); m != nil {
			return Link{Kind: YandexAlbum, ID: m[1], URL: clean(raw)}, true
		}
		return Link{}, false

	case hostIn(host, "vk.com", "vk.ru", "vkvideo.ru"):
		return Link{Kind: VK, URL: clean(raw)}, true

	case hostIn(host, "music.apple.com", "itunes.apple.com"):
		// Трек — это «/song/…» или альбом с «?i=<номер трека>». Просто
		// альбом заказать как один трек нельзя.
		if strings.Contains(u.Path, "/song/") || u.Query().Get("i") != "" {
			return Link{Kind: Apple, URL: raw}, true
		}
		return Link{}, false

	case hostIn(host, "deezer.com", "deezer.page.link"):
		if strings.Contains(u.Path, "/track/") || host == "deezer.page.link" {
			return Link{Kind: Deezer, URL: raw}, true
		}
		return Link{}, false
	}

	return Link{}, false
}

// hostIn сверяет имя хоста со списком: сам домен или что-то под ним.
//
// Суффиксное сравнение обязательно с точкой: без неё «злойyoutube.com» и
// «notvk.com» прошли бы как свои.
func hostIn(host string, domains ...string) bool {
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Strip убирает из текста любые адреса — остаётся то, что зритель написал
// словами. Искать в Spotify по адресу бесполезно, чей бы он ни был.
func Strip(text string) string {
	out := urlPattern.ReplaceAllString(text, " ")
	out = spotifyTrack.ReplaceAllString(out, " ")
	return strings.Join(strings.Fields(out), " ")
}

// clean снимает с адреса рекламные метки и якоря: они не часть ссылки на
// трек, а сравнивать и запрашивать с ними хуже.
func clean(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	for key := range q {
		low := strings.ToLower(key)
		if strings.HasPrefix(low, "utm_") || low == "si" || low == "feature" ||
			low == "from" || low == "reqid" || low == "context" {
			q.Del(key)
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String()
}

func firstFilled(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
