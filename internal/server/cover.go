package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Обложка трека для виджета в OBS.
//
// Зачем это вообще понадобилось. Раньше виджет вставлял в себя прямой адрес
// картинки со склада Spotify (`https://i.scdn.co/...`), и её тянул из интернета
// сам браузер внутри OBS. В панели приложения обложка при этом была видна, а в
// кадре — нет: браузер OBS старый, живёт своей жизнью и про обход блокировок,
// поднятый приложением, ничего не знает. Стример видел плашку без картинки и
// сделать с этим ничего не мог.
//
// Теперь картинку качает само приложение — тем же путём, каким ходит к Spotify,
// то есть через обход, — а виджету отдаёт со своего адреса 127.0.0.1. Браузеру
// OBS остаётся забрать её у соседа по компьютеру, и это у него получается
// всегда.
//
// Если скачать не вышло (нет обхода, склад не ответил), приложение отправляет
// браузер по прежнему прямому адресу: пусть попробует сам, как делал раньше.
// Хуже, чем было, от этого не станет.

// coverHosts — откуда позволено качать картинки.
//
// Список закрытый, а не «качаем что попросят»: иначе любая страница, открытая
// в браузере стримера, могла бы через наше приложение сходить куда угодно —
// хоть в его домашний роутер, — да ещё и через его VPN.
var coverHosts = map[string]bool{
	"i.scdn.co":                   true, // обложки альбомов Spotify
	"mosaic.scdn.co":              true, // «мозаика» из четырёх обложек у плейлистов
	"image-cdn-ak.spotifycdn.com": true,
	"image-cdn-fa.spotifycdn.com": true,
	"i.ytimg.com":                 true, // кадры роликов YouTube
	"img.youtube.com":             true,
	"avatars.yandex.net":          true, // обложки Яндекс.Музыки
	"avatars.ym.net":              true,
}

const (
	// coverMaxBytes — больше этого обложки не бывают: у Spotify самая крупная
	// около 60 килобайт. Ограничение на случай, если по адресу картинки вдруг
	// окажется не картинка.
	coverMaxBytes = 4 << 20

	// coverKeep — сколько картинок держим в памяти. Их размер известен, и
	// два десятка — это пара мегабайт: дешевле, чем качать одну и ту же
	// обложку заново каждые несколько секунд, пока виджет перерисовывается.
	coverKeep = 24

	// coverTimeout — сколько ждём склад с картинками. Дольше ждать нет смысла:
	// плашка в кадре уже висит, и обложка нужна сейчас, а не через полминуты.
	coverTimeout = 10 * time.Second
)

// cover — скачанная картинка вместе с тем, чем она оказалась.
type cover struct {
	body []byte
	typ  string
}

// covers — маленький склад скачанных обложек.
//
// Порядок хранится отдельным списком: как только картинок становится больше
// coverKeep, выбрасывается самая старая. Городить ради этого настоящий кэш с
// вытеснением по обращениям незачем — обложек за вечер десятки, а не тысячи.
type covers struct {
	mu    sync.Mutex
	items map[string]cover
	order []string
}

func (c *covers) get(key string) (cover, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

func (c *covers) put(key string, v cover) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]cover{}
	}
	if _, ok := c.items[key]; !ok {
		c.order = append(c.order, key)
	}
	c.items[key] = v
	for len(c.order) > coverKeep {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

// handleCover отдаёт обложку трека виджету.
//
// Адрес картинки приходит в `u`. Отвечаем самой картинкой, а при неудаче —
// переводом на прямой адрес: пусть браузер попробует сам.
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	if raw == "" {
		http.Error(w, "не сказано, какую картинку", http.StatusBadRequest)
		return
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !coverHosts[strings.ToLower(u.Host)] {
		http.Error(w, "такие картинки приложение не качает", http.StatusBadRequest)
		return
	}

	if v, ok := s.covers.get(raw); ok {
		writeCover(w, v)
		return
	}

	v, err := s.fetchCover(r.Context(), raw)
	if err != nil {
		// Не поломка, из-за которой стоит беспокоить человека: обложка —
		// украшение, а не работа приложения. В лог пишем, в панель нет.
		s.log.Debug("обложку скачать не вышло, отправляю виджет напрямую", "ошибка", err)
		http.Redirect(w, r, raw, http.StatusFound)
		return
	}

	s.covers.put(raw, v)
	writeCover(w, v)
}

func writeCover(w http.ResponseWriter, v cover) {
	w.Header().Set("Content-Type", v.typ)
	// Обложка у трека не меняется никогда, а браузер OBS перерисовывает плашку
	// на каждой смене оформления. Пусть держит её у себя.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(v.body)
}

// fetchCover качает картинку тем же путём, каким приложение ходит к Spotify.
func (s *Server) fetchCover(ctx context.Context, raw string) (cover, error) {
	ctx, cancel := context.WithTimeout(ctx, coverTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return cover{}, err
	}
	resp, err := s.coverClient().Do(req)
	if err != nil {
		return cover{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return cover{}, &coverError{code: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, coverMaxBytes))
	if err != nil {
		return cover{}, err
	}

	typ := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(typ, "image/") {
		// По адресу оказалась не картинка. Отдавать это виджету нельзя: в
		// лучшем случае он покажет пустоту, в худшем — чужую страницу.
		return cover{}, &coverError{code: resp.StatusCode, notImage: true}
	}
	return cover{body: body, typ: typ}, nil
}

// coverClient — через кого качать картинки.
//
// Через тот же обход, что и Spotify: склад картинок у Spotify тот же самый и
// закрыт для России ровно так же. Отдельный клиент, а не клиент Spotify,
// потому что тому нельзя навязывать чужие сроки ожидания и, главное, обложки
// не должны считаться его запросами: у Spotify тесная норма, и упереться в
// неё из-за картинок было бы обидно (склад картинок норму не считает, но
// путать их учёт всё равно ни к чему).
func (s *Server) coverClient() *http.Client {
	return &http.Client{Timeout: coverTimeout}
}

// coverError — почему картинку не отдали. Своя ошибка нужна только логу.
type coverError struct {
	code     int
	notImage bool
}

func (e *coverError) Error() string {
	if e.notImage {
		return "по адресу обложки лежит не картинка"
	}
	return "склад картинок ответил " + http.StatusText(e.code)
}
