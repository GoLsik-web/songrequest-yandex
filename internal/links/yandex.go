package links

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"songrequest-yandex/internal/errs"
)

// Как приложение узнаёт, что за трек лежит по ссылке на Яндекс.Музыку.
//
// ЧТО БЫЛО И ПОЧЕМУ СЛОМАЛОСЬ. Официального ключа к Яндекс.Музыке у нас нет,
// поэтому артиста и название доставали из мета-тегов самой страницы — из тех
// же, по которым мессенджеры рисуют превью ссылки. Это было заведомо хрупко, и
// оно сломалось: 09.09 страница трека отдаёт разметку без единого тега og, а
// заголовок вкладки у всех треков теперь один и тот же — «Яндекс Музыка,
// собираем музыку для вас».
//
// Хуже того, ломалось оно молча и опасно. Разбор честно делил этот общий
// заголовок по тире, получал артиста «Яндекс Музыка» и название «собираем
// музыку для вас», и заказ уходил в поиск с этими словами. В Spotify такого
// нет, поэтому дальше включался запасной путь — поиск на YouTube по той же
// строке, — и зритель за свои баллы получал в эфир случайный ролик про Яндекс
// Музыку вместо песни, которую заказывал. Баллы при этом не возвращались:
// с точки зрения приложения заказ удался.
//
// ЧТО СТАЛО. Спрашиваем сам Яндекс. У него есть открытая точка, которой не
// нужен ни ключ, ни вход:
//
//	https://api.music.yandex.net/tracks/<номер трека>
//
// Она отдаёт название, всех исполнителей, длительность и обложку. Номер трека
// у нас уже есть — его вынимает Find из самой ссылки.
//
// Подсказал эту точку друг владельца: она же работает в его YandexRPC
// (github.com/Eneryleen/YandexRPC), который показывает трек из Яндекс.Музыки
// в Discord. Там она используется для поиска по названию, у нас — для чтения
// по номеру; ключ не нужен в обоих случаях.
//
// ЗАЧЕМ ДЛИТЕЛЬНОСТЬ. Это самый сильный признак против каверов, ускоренных
// версий и часовых лупов: см. WantMs в internal/match. Со страницы её взять
// было негде, и заказ по ссылке на Яндекс подбирался хуже, чем по ссылке на
// YouTube, где длительность есть.
//
// РАЗБОР СТРАНИЦЫ ОСТАЛСЯ ЗАПАСНЫМ ПУТЁМ — на случай, если открытая точка
// однажды закроется. Но теперь он отказывается работать, когда на странице
// нет ничего похожего на трек: молчаливый мусор хуже честного отказа.

// Meta — то, что удалось узнать про трек.
type Meta struct {
	Artist string
	Title  string
	// DurationMs — длительность трека. Ноль означает «не знаем»: так бывает
	// у запасного пути со страницы.
	DurationMs int
	// CoverURL — обложка. Пусто, если не отдали.
	CoverURL string
}

// Query собирает строку для поиска в Spotify.
func (m Meta) Query() string {
	switch {
	case m.Artist != "" && m.Title != "":
		return m.Artist + " - " + m.Title
	case m.Title != "":
		return m.Title
	default:
		return ""
	}
}

// YandexReader узнаёт трек по ссылке.
type YandexReader struct {
	// HTTP можно подменить в тестах, чтобы проверять разбор на подставном
	// сайте, а не ходить в настоящий Яндекс.
	HTTP *http.Client
	// API — откуда спрашивать трек. Поле есть только ради тестов: в работе
	// оно пустое и берётся yandexAPI.
	API string
}

// NewYandexReader создаёт читалку.
func NewYandexReader() *YandexReader {
	return &YandexReader{HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// yandexAPI — открытая точка Яндекс.Музыки. Ключ ей не нужен.
//
// Через обход блокировок сюда не ходим нарочно: обход поднят ради Spotify и
// выходит в интернет из чужой страны, а Яндекс из России отвечает и напрямую.
// Гнать его через заграничный сервер значило бы добавить секунды ожидания и
// напроситься на отказ по стране.
const yandexAPI = "https://api.music.yandex.net"

// Lookup узнаёт, что за трек лежит по ссылке.
//
// Сначала спрашиваем Яндекс по номеру трека, и это главный путь. Не вышло —
// пробуем прочитать страницу, как делали раньше.
func (y *YandexReader) Lookup(ctx context.Context, link Link) (Meta, error) {
	if link.ID != "" {
		meta, err := y.track(ctx, link.ID)
		if err == nil {
			return meta, nil
		}
		// Не отказываем сразу: страница может ответить там, где не ответила
		// открытая точка. Причину запоминаем — она пригодится, если и
		// страница молчит.
		metaPage, pageErr := y.readPage(ctx, link.URL)
		if pageErr == nil {
			return metaPage, nil
		}
		return Meta{}, err
	}
	return y.readPage(ctx, link.URL)
}

// track спрашивает Яндекс о треке по его номеру.
func (y *YandexReader) track(ctx context.Context, id string) (Meta, error) {
	// Номер вынут регулярным выражением из ссылки и состоит из одних цифр —
	// но проверим ещё раз здесь: он уходит в адрес запроса, а этот файл
	// когда-нибудь позовут из другого места.
	if !onlyDigits(id) || len(id) > 20 {
		return Meta{}, errs.New(errs.YandexRead, "Непонятный номер трека в ссылке Яндекс.Музыки.")
	}

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	base := y.API
	if base == "" {
		base = yandexAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/tracks/"+id, nil)
	if err != nil {
		return Meta{}, errs.Wrap(errs.YandexRead, "Не получилось спросить Яндекс.Музыку о треке.", err)
	}
	req.Header.Set("User-Agent", "songrequest/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := y.client().Do(req)
	if err != nil {
		return Meta{}, errs.Wrap(errs.YandexRead, "Яндекс.Музыка не отвечает.", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Meta{}, errs.New(errs.YandexRead,
			fmt.Sprintf("Яндекс.Музыка ответила ошибкой (%d).", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Meta{}, errs.Wrap(errs.YandexRead, "Ответ Яндекс.Музыки не дочитался.", err)
	}

	meta, ok := ParseYandexTrack(body)
	if !ok {
		return Meta{}, errs.New(errs.YandexRead, "Яндекс.Музыка не сказала, что это за трек.")
	}
	return meta, nil
}

// yandexTrackReply — то немногое, что нам нужно из ответа.
type yandexTrackReply struct {
	Result []struct {
		Title   string `json:"title"`
		Version string `json:"version"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
		DurationMs int    `json:"durationMs"`
		CoverURI   string `json:"coverUri"`
		Albums     []struct {
			CoverURI string `json:"coverUri"`
		} `json:"albums"`
	} `json:"result"`
}

// ParseYandexTrack разбирает ответ Яндекса о треке.
//
// Отдельной функцией, чтобы разбор проверялся на настоящем ответе, а не
// только живым запросом.
func ParseYandexTrack(body []byte) (Meta, bool) {
	var reply yandexTrackReply
	if err := json.Unmarshal(body, &reply); err != nil || len(reply.Result) == 0 {
		return Meta{}, false
	}
	t := reply.Result[0]
	if strings.TrimSpace(t.Title) == "" {
		return Meta{}, false
	}

	title := strings.TrimSpace(t.Title)
	// Version — это «feat. кто-то», «remix», «live». Приписываем к названию:
	// без него ремикс и оригинал выглядят одинаково, и в Spotify найдётся не
	// тот. Именно так же поступает разбор заказов текстом.
	if v := strings.TrimSpace(t.Version); v != "" {
		title += " " + v
	}

	// Берём первого исполнителя. Остальные обычно приглашённые, и в запросе
	// к Spotify они мешают больше, чем помогают: там тот же трек часто
	// записан вообще без них.
	artist := ""
	if len(t.Artists) > 0 {
		artist = strings.TrimSpace(t.Artists[0].Name)
	}

	cover := t.CoverURI
	if cover == "" {
		for _, a := range t.Albums {
			if a.CoverURI != "" {
				cover = a.CoverURI
				break
			}
		}
	}

	return Meta{
		Artist:     artist,
		Title:      title,
		DurationMs: t.DurationMs,
		CoverURL:   coverURL(cover),
	}, true
}

// coverURL достраивает адрес обложки.
//
// Яндекс отдаёт его без «https://» и с «%%» вместо размера: сколько надо,
// столько и подставляешь. Хост проверяем: адрес уходит в браузер стримера, и
// принимать оттуда что попало нельзя.
func coverURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	url := "https://" + strings.Replace(raw, "%%", "800x800", 1)
	if !strings.HasPrefix(url, "https://avatars.yandex.net/") || len(url) > 256 {
		return ""
	}
	if strings.ContainsAny(url, " \"'<>") {
		return ""
	}
	return url
}

func onlyDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (y *YandexReader) client() *http.Client {
	if y.HTTP != nil {
		return y.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

var (
	ogTitle       = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']*)["']`)
	ogTitleRev    = regexp.MustCompile(`(?is)<meta[^>]+content=["']([^"']*)["'][^>]+property=["']og:title["']`)
	ogDescription = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:description["'][^>]+content=["']([^"']*)["']`)
	ogDescRev     = regexp.MustCompile(`(?is)<meta[^>]+content=["']([^"']*)["'][^>]+property=["']og:description["']`)
	titleTag      = regexp.MustCompile(`(?is)<title[^>]*>([^<]*)</title>`)
)

// readPage — запасной путь: достать артиста и название из самой страницы.
func (y *YandexReader) readPage(ctx context.Context, pageURL string) (Meta, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return Meta{}, errs.Wrap(errs.YandexRead, "Не получилось открыть ссылку Яндекс.Музыки.", err)
	}
	// Без обычного заголовка браузера Яндекс отдаёт страницу-заглушку без
	// мета-тегов, и разбирать становится нечего.
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("Accept-Language", "ru,en;q=0.9")

	resp, err := y.client().Do(req)
	if err != nil {
		return Meta{}, errs.Wrap(errs.YandexRead, "Яндекс.Музыка не отвечает.", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Meta{}, errs.New(errs.YandexRead,
			fmt.Sprintf("Яндекс.Музыка ответила ошибкой (%d).", resp.StatusCode))
	}

	// Страница большая, а нужное лежит в самом начале, в head.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return Meta{}, errs.Wrap(errs.YandexRead, "Страница Яндекс.Музыки не дочиталась.", err)
	}

	meta := ParseYandexPage(string(body))
	if meta.Title == "" {
		return Meta{}, errs.New(errs.YandexRead,
			"На странице Яндекс.Музыки не нашлось названия трека.")
	}
	return meta, nil
}

// ParseYandexPage вытаскивает артиста и название из разметки.
//
// Отдельной функцией, чтобы разбор проверялся тестами на настоящей вёрстке,
// а не только живым запросом.
func ParseYandexPage(html string) Meta {
	title := unescape(firstMatch(html, ogTitle, ogTitleRev))
	desc := unescape(firstMatch(html, ogDescription, ogDescRev))

	if title == "" {
		// Запасной вариант: заголовок вкладки. Он выглядит как
		// «Слушать Название — Артист на Яндекс Музыке».
		title = unescape(firstMatch(html, titleTag))
	}
	if title == "" || isGenericYandexTitle(title) {
		return Meta{}
	}

	return splitTitle(title, desc)
}

// genericTitles — заголовки, за которыми нет никакого трека.
//
// Это не придирка, а защита от того, что уже случилось. 09.09 Яндекс стал
// отдавать страницу трека с общим заголовком «Яндекс Музыка, собираем музыку
// для вас». Разбор честно делил его по тире и выдавал артиста «Яндекс Музыка»
// с названием «собираем музыку для вас» — а дальше приложение искало эти слова
// и ставило в эфир случайный ролик, найденный по ним. Зритель платил баллы за
// свою песню и получал чужую.
//
// Поэтому здесь правило простое: не узнали трек — так и скажем. Отказ зритель
// поймёт, и баллы к нему вернутся.
var genericTitles = []string{
	"собираем музыку для вас",
	"яндекс музыка",
	"яндекс.музыка",
	"страница не найдена",
	"ничего не найдено",
	"доступ ограничен",
}

func isGenericYandexTitle(title string) bool {
	low := strings.ToLower(strings.TrimSpace(title))
	// Точное совпадение с общей страницей — самый частый случай.
	for _, g := range genericTitles {
		if low == g {
			return true
		}
	}
	// «Яндекс Музыка — собираем музыку для вас» и родня: в заголовке нет
	// ничего, кроме имени сервиса и его девиза.
	stripped := low
	for _, g := range genericTitles {
		stripped = strings.ReplaceAll(stripped, g, " ")
	}
	stripped = strings.Trim(stripped, " -—–·,.:;|")
	return strings.TrimSpace(stripped) == ""
}

// splitTitle разбирает заголовок страницы.
//
// Яндекс пишет его несколькими способами, и все встречаются:
//
//	«Артист — Название»
//	«Название — Артист. Слушать онлайн на Яндекс Музыке»
//	«Слушать Название — Артист на Яндекс Музыке»
func splitTitle(title, desc string) Meta {
	title = trimYandexTail(title)

	// Описание обычно начинается с имени артиста — по нему и понимаем,
	// с какой стороны от тире он стоит.
	artistHint := strings.TrimSpace(strings.SplitN(desc, "•", 2)[0])
	artistHint = strings.TrimSpace(strings.SplitN(artistHint, "·", 2)[0])
	artistHint = trimYandexTail(artistHint)

	left, right, ok := splitDash(title)
	if !ok {
		return Meta{Title: title, Artist: artistHint}
	}

	switch {
	case artistHint != "" && strings.EqualFold(artistHint, right):
		return Meta{Artist: right, Title: left}
	case artistHint != "" && strings.EqualFold(artistHint, left):
		return Meta{Artist: left, Title: right}
	default:
		// Подсказки нет — берём привычный порядок «Артист — Название».
		return Meta{Artist: left, Title: right}
	}
}

var yandexTails = []string{
	"слушать онлайн на яндекс музыке", "на яндекс музыке", "яндекс музыка",
	"слушать онлайн", "listen online on yandex music", "yandex music",
}

// trimYandexTail убирает приписки самого сервиса.
func trimYandexTail(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	for _, tail := range yandexTails {
		if i := strings.Index(low, tail); i > 0 {
			s = strings.TrimSpace(s[:i])
			low = strings.ToLower(s)
		}
	}
	s = strings.TrimPrefix(s, "Слушать ")
	return strings.Trim(strings.TrimSpace(s), ".,—-–|")
}

// splitDash делит по тире с пробелами — внутри названий дефис встречается
// часто, а тире с пробелами почти всегда разделитель.
func splitDash(s string) (left, right string, ok bool) {
	for _, sep := range []string{" — ", " – ", " - "} {
		if i := strings.Index(s, sep); i > 0 {
			left = strings.TrimSpace(s[:i])
			right = strings.TrimSpace(s[i+len(sep):])
			if left != "" && right != "" {
				return left, right, true
			}
		}
	}
	return "", "", false
}

func firstMatch(s string, patterns ...*regexp.Regexp) string {
	for _, re := range patterns {
		if m := re.FindStringSubmatch(s); m != nil && strings.TrimSpace(m[1]) != "" {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

var htmlEntities = strings.NewReplacer(
	"&amp;", "&", "&quot;", `"`, "&#39;", "'", "&apos;", "'",
	"&lt;", "<", "&gt;", ">", "&nbsp;", " ", "&mdash;", "—", "&ndash;", "–",
)

func unescape(s string) string {
	return strings.TrimSpace(htmlEntities.Replace(s))
}

// ── пачки треков: плейлисты и альбомы ────────────────────────────────
//
// Заказ плейлиста — отдельная награда со своей ценой, см. ПРОДОЛЖИТЬ.md.
// Здесь только чтение: что за плейлист и какие в нём треки.
//
// Обе точки открытые, ключ им не нужен — те же самые, что и для одного трека:
//
//	https://api.music.yandex.net/users/<логин>/playlists/<номер>
//	https://api.music.yandex.net/albums/<номер>/with-tracks
//
// Сыграть эти треки напрямую нельзя: Яндекс отдаёт только описание. Поэтому
// каждый из них потом ищется в Spotify, а если там нет — на YouTube, ровно так
// же, как обычный заказ текстом. Зато артист, название и длительность приезжают
// точными, и подбор получается несравнимо лучше, чем по строке из чата.

// Collection — плейлист или альбом.
type Collection struct {
	// Title — название плейлиста или альбома, для человека.
	Title string
	// Tracks — треки по порядку, уже обрезанные до нужного числа.
	Tracks []Meta
	// Total — сколько треков было всего. Нужно, чтобы честно сказать
	// «взял первые пять из сорока».
	Total int
}

// Playlist читает плейлист Яндекс.Музыки.
//
// limit — сколько треков взять с начала. Ноль означает «все», но так его никто
// не зовёт: число треков в заказе ограничено настройкой.
func (y *YandexReader) Playlist(ctx context.Context, owner, kind string, limit int) (Collection, error) {
	if !safeYandexLogin(owner) || !onlyDigits(kind) || len(kind) > 20 {
		return Collection{}, errs.New(errs.YandexRead, "Непонятная ссылка на плейлист Яндекс.Музыки.")
	}
	body, err := y.get(ctx, "/users/"+owner+"/playlists/"+kind)
	if err != nil {
		return Collection{}, err
	}
	col, ok := ParseYandexPlaylist(body, limit)
	if !ok {
		return Collection{}, errs.New(errs.YandexRead,
			"Яндекс.Музыка не отдала треки этого плейлиста. Бывает у закрытых плейлистов.")
	}
	return col, nil
}

// Album читает альбом Яндекс.Музыки.
func (y *YandexReader) Album(ctx context.Context, id string, limit int) (Collection, error) {
	if !onlyDigits(id) || len(id) > 20 {
		return Collection{}, errs.New(errs.YandexRead, "Непонятная ссылка на альбом Яндекс.Музыки.")
	}
	body, err := y.get(ctx, "/albums/"+id+"/with-tracks")
	if err != nil {
		return Collection{}, err
	}
	col, ok := ParseYandexAlbum(body, limit)
	if !ok {
		return Collection{}, errs.New(errs.YandexRead, "Яндекс.Музыка не отдала треки этого альбома.")
	}
	return col, nil
}

// get — общий запрос к открытой точке Яндекса.
func (y *YandexReader) get(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	base := y.API
	if base == "" {
		base = yandexAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, errs.Wrap(errs.YandexRead, "Не получилось спросить Яндекс.Музыку.", err)
	}
	req.Header.Set("User-Agent", "songrequest/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := y.client().Do(req)
	if err != nil {
		return nil, errs.Wrap(errs.YandexRead, "Яндекс.Музыка не отвечает.", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errs.New(errs.YandexRead,
			fmt.Sprintf("Яндекс.Музыка ответила ошибкой (%d).", resp.StatusCode))
	}
	// Плейлисты бывают большими: у редакционных Яндекса это две сотни
	// килобайт. Предел на всякий случай, а не потому, что столько нужно.
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// safeYandexLogin проверяет логин владельца плейлиста.
//
// Логин уходит прямо в адрес запроса, а приходит он из сообщения зрителя.
// Без проверки достаточно было бы заказать плейлист по адресу с «../», чтобы
// приложение сходило совсем не туда, куда собиралось.
func safeYandexLogin(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// ParseYandexPlaylist разбирает ответ о плейлисте.
func ParseYandexPlaylist(body []byte, limit int) (Collection, bool) {
	var reply struct {
		Result struct {
			Title  string `json:"title"`
			Tracks []struct {
				Track json.RawMessage `json:"track"`
			} `json:"tracks"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return Collection{}, false
	}

	col := Collection{Title: strings.TrimSpace(reply.Result.Title), Total: len(reply.Result.Tracks)}
	for _, row := range reply.Result.Tracks {
		if limit > 0 && len(col.Tracks) >= limit {
			break
		}
		if meta, ok := parseOneYandexTrack(row.Track); ok {
			col.Tracks = append(col.Tracks, meta)
		}
	}
	return col, len(col.Tracks) > 0
}

// ParseYandexAlbum разбирает ответ об альбоме.
//
// Треки там разложены по дискам («volumes»), и у двойных альбомов их правда
// два. Идём по дискам подряд — это и есть порядок альбома.
func ParseYandexAlbum(body []byte, limit int) (Collection, bool) {
	var reply struct {
		Result struct {
			Title   string              `json:"title"`
			Volumes [][]json.RawMessage `json:"volumes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return Collection{}, false
	}

	col := Collection{Title: strings.TrimSpace(reply.Result.Title)}
	for _, volume := range reply.Result.Volumes {
		col.Total += len(volume)
	}
	for _, volume := range reply.Result.Volumes {
		for _, raw := range volume {
			if limit > 0 && len(col.Tracks) >= limit {
				return col, len(col.Tracks) > 0
			}
			if meta, ok := parseOneYandexTrack(raw); ok {
				col.Tracks = append(col.Tracks, meta)
			}
		}
	}
	return col, len(col.Tracks) > 0
}

// parseOneYandexTrack разбирает один трек внутри пачки.
//
// Тот же разбор, что и у одиночного трека, — через ParseYandexTrack: у Яндекса
// трек везде описан одинаково, и держать два разбора значило бы однажды их
// разъехать. Оборачиваем в «result», потому что там трек лежит именно так.
func parseOneYandexTrack(raw json.RawMessage) (Meta, bool) {
	if len(raw) == 0 {
		return Meta{}, false
	}
	wrapped := append(append([]byte(`{"result":[`), raw...), []byte(`]}`)...)
	meta, ok := ParseYandexTrack(wrapped)
	if !ok || meta.Title == "" {
		return Meta{}, false
	}
	return meta, true
}
