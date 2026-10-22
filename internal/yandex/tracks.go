package yandex

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"songrequest-yandex/internal/errs"
	"songrequest-yandex/internal/match"
)

// URI трека у нас — «ym:<номер трека>:<номер альбома>». Альбом обязателен:
// без него Ynison ставит трек в очередь, но приложение Яндекса не может его
// включить, а плейлист отказывается его принять.
const uriPrefix = "ym:"

// TrackURI собирает URI.
func TrackURI(trackID, albumID string) string {
	if albumID == "" {
		return uriPrefix + trackID
	}
	return uriPrefix + trackID + ":" + albumID
}

// ParseURI разбирает URI обратно. ok=false — это не наш трек (например,
// заказ с YouTube).
func ParseURI(uri string) (trackID, albumID string, ok bool) {
	if !strings.HasPrefix(uri, uriPrefix) {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(uri, uriPrefix), ":", 2)
	if parts[0] == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		albumID = parts[1]
	}
	return parts[0], albumID, true
}

// flexID — номер, который Яндекс присылает то числом, то строкой.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		s = ""
	}
	*f = flexID(s)
	return nil
}

// trackJSON — трек в ответах Яндекса.
type trackJSON struct {
	ID             flexID `json:"id"`
	RealID         flexID `json:"realId"`
	Title          string `json:"title"`
	Version        string `json:"version"`
	DurationMs     int    `json:"durationMs"`
	Available      *bool  `json:"available"`
	CoverURI       string `json:"coverUri"`
	Explicit       bool   `json:"explicit"`
	ContentWarning string `json:"contentWarning"`
	Artists        []struct {
		ID   flexID `json:"id"`
		Name string `json:"name"`
	} `json:"artists"`
	Albums []struct {
		ID       flexID `json:"id"`
		Title    string `json:"title"`
		Type     string `json:"type"`
		CoverURI string `json:"coverUri"`
		Likes    int    `json:"likesCount"`
	} `json:"albums"`
}

// fullTitle — название вместе с версией: «Трек (Remix)».
func (t trackJSON) fullTitle() string {
	if t.Version == "" {
		return t.Title
	}
	return t.Title + " (" + t.Version + ")"
}

func (t trackJSON) albumID() string {
	if len(t.Albums) == 0 {
		return ""
	}
	return string(t.Albums[0].ID)
}

// available — можно ли трек послушать. Не прислано — считаем, что можно.
func (t trackJSON) available() bool { return t.Available == nil || *t.Available }

// candidate переводит трек в вид, понятный подбору.
func (t trackJSON) candidate() match.Candidate {
	c := match.Candidate{
		ID:         string(t.ID),
		URI:        TrackURI(string(t.ID), t.albumID()),
		Title:      t.fullTitle(),
		DurationMs: t.DurationMs,
		CoverURL:   CoverURL(t.CoverURI, 800),
	}
	for _, a := range t.Artists {
		c.Artists = append(c.Artists, a.Name)
	}
	if len(t.Albums) > 0 {
		al := t.Albums[0]
		c.Album = al.Title
		switch al.Type {
		case "single":
			c.AlbumType = "single"
		case "compilation":
			c.AlbumType = "compilation"
		default:
			c.AlbumType = "album"
		}
		if c.CoverURL == "" {
			c.CoverURL = CoverURL(al.CoverURI, 800)
		}
	}
	// Популярности в ответе нет; лайки альбома — лучшее, что есть. Шкала
	// грубая, но подбору нужно только «известное выше безвестного».
	if len(t.Albums) > 0 {
		c.Popularity = popularity(t.Albums[0].Likes)
	}
	return c
}

// popularity переводит лайки в шкалу 0..100: двадцать очков за каждый
// десятичный порядок, сто тысяч лайков — потолок.
func popularity(likes int) int {
	if likes <= 0 {
		return 0
	}
	p := int(20 * math.Log10(float64(likes)+1))
	return min(p, 100)
}

// CoverURL собирает адрес обложки. Яндекс присылает шаблон вида
// «avatars.yandex.net/get-music-content/…/%%», где %% — размер.
func CoverURL(raw string, size int) string {
	if raw == "" {
		return ""
	}
	u := "https://" + strings.Replace(raw, "%%", fmt.Sprintf("%dx%d", size, size), 1)
	if p, err := url.Parse(u); err != nil || !strings.HasSuffix(p.Hostname(), ".yandex.net") {
		return ""
	}
	return u
}

// SearchTracks ищет треки.
func (c *Client) SearchTracks(ctx context.Context, query string, limit int) ([]match.Candidate, error) {
	q := url.Values{}
	q.Set("text", query)
	q.Set("type", "track")
	q.Set("page", "0")
	q.Set("nocorrect", "false")
	var resp struct {
		Tracks *struct {
			Results []trackJSON `json:"results"`
		} `json:"tracks"`
	}
	if err := c.get(ctx, "/search", q, &resp); err != nil {
		return nil, err
	}
	if resp.Tracks == nil {
		return nil, nil
	}
	var out []match.Candidate
	for _, t := range resp.Tracks.Results {
		// Недоступное не выбрасываем, а помечаем: подбор его не возьмёт, но
		// посчитает — и стример узнает «трек есть, но слушать нельзя»
		// вместо «не нашлось».
		if !t.available() {
			c := t.candidate()
			c.Markets = []string{match.Nowhere}
			out = append(out, c)
			continue
		}
		rememberTrack(t)
		out = append(out, t.candidate())
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Track — один трек по номеру.
func (c *Client) Track(ctx context.Context, id string) (match.Candidate, error) {
	if trackID, _, ok := ParseURI(id); ok {
		id = trackID
	}
	var tracks []trackJSON
	if err := c.get(ctx, "/tracks/"+url.PathEscape(id), nil, &tracks); err != nil {
		return match.Candidate{}, err
	}
	if len(tracks) == 0 {
		return match.Candidate{}, errs.New(errs.YandexBadResponse, "Такого трека в Яндекс Музыке нет.")
	}
	if !tracks[0].available() {
		return match.Candidate{}, errs.New(errs.YandexNotAvailable, "Трек есть в Яндекс Музыке, но слушать его нельзя.")
	}
	rememberTrack(tracks[0])
	return tracks[0].candidate(), nil
}

// CollectionTrack — трек из плейлиста или альбома.
type CollectionTrack struct {
	ID         string
	URI        string
	Title      string
	Artist     string
	DurationMs int
	CoverURL   string
}

// Collection — плейлист или альбом.
type Collection struct {
	Title  string
	Tracks []CollectionTrack
	Total  int
}

func collectionTrack(t trackJSON) CollectionTrack {
	cand := t.candidate()
	return CollectionTrack{
		ID:         cand.ID,
		URI:        cand.URI,
		Title:      cand.Title,
		Artist:     strings.Join(cand.Artists, ", "),
		DurationMs: cand.DurationMs,
		CoverURL:   cand.CoverURL,
	}
}

// AlbumCollection — первые want треков альбома.
func (c *Client) AlbumCollection(ctx context.Context, id string, want int) (Collection, error) {
	var resp struct {
		Title   string        `json:"title"`
		Volumes [][]trackJSON `json:"volumes"`
	}
	if err := c.get(ctx, "/albums/"+url.PathEscape(id)+"/with-tracks", nil, &resp); err != nil {
		return Collection{}, err
	}
	col := Collection{Title: resp.Title}
	for _, vol := range resp.Volumes {
		for _, t := range vol {
			col.Total++
			if t.available() && (want <= 0 || len(col.Tracks) < want) {
				col.Tracks = append(col.Tracks, collectionTrack(t))
			}
		}
	}
	return col, nil
}

// PlaylistCollection — первые want треков плейлиста. id — «владелец:номер»,
// как в ссылке music.yandex.ru/users/<владелец>/playlists/<номер>. Для
// новых ссылок вида /playlists/<uuid> владельца нет — тогда id и есть uuid.
func (c *Client) PlaylistCollection(ctx context.Context, id string, want int) (Collection, error) {
	path := "/playlist/" + url.PathEscape(id)
	if owner, kind, ok := strings.Cut(id, ":"); ok {
		path = "/users/" + url.PathEscape(owner) + "/playlists/" + url.PathEscape(kind)
	}
	var resp playlistJSON
	if err := c.get(ctx, path, nil, &resp); err != nil {
		return Collection{}, err
	}
	col := Collection{Title: resp.Title, Total: resp.TrackCount}
	for _, it := range resp.Tracks {
		if it.Track == nil || !it.Track.available() {
			continue
		}
		col.Tracks = append(col.Tracks, collectionTrack(*it.Track))
		if want > 0 && len(col.Tracks) >= want {
			break
		}
	}
	if col.Total == 0 {
		col.Total = len(resp.Tracks)
	}
	return col, nil
}

// ArtistTopTrackURIs — популярные треки артиста.
func (c *Client) ArtistTopTrackURIs(ctx context.Context, artistID string, limit int) ([]string, error) {
	q := url.Values{}
	q.Set("page", "0")
	q.Set("page-size", strconv.Itoa(max(limit, 1)))
	var resp struct {
		Tracks []trackJSON `json:"tracks"`
	}
	if err := c.get(ctx, "/artists/"+url.PathEscape(artistID)+"/tracks", q, &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, t := range resp.Tracks {
		if t.available() {
			out = append(out, TrackURI(string(t.ID), t.albumID()))
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
