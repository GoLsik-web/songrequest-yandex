package yandex

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"sync"

	"songrequest-yandex/internal/errs"
)

// Playlist — плейлист стримера, для выбора в настройках.
type Playlist struct {
	// ID — «uid:kind», как его понимает PlaylistCollection.
	ID       string `json:"id"`
	Kind     int    `json:"kind"`
	Name     string `json:"name"`
	Tracks   int    `json:"tracks"`
	Revision int    `json:"revision"`
}

type playlistJSON struct {
	Kind       int    `json:"kind"`
	Title      string `json:"title"`
	Revision   int    `json:"revision"`
	TrackCount int    `json:"trackCount"`
	Owner      struct {
		UID int64 `json:"uid"`
	} `json:"owner"`
	Tracks []struct {
		ID    flexID     `json:"id"`
		Track *trackJSON `json:"track"`
	} `json:"tracks"`
}

// Playlists — плейлисты стримера.
func (c *Client) Playlists(ctx context.Context) ([]Playlist, error) {
	uid, err := c.uid(ctx)
	if err != nil {
		return nil, err
	}
	var list []playlistJSON
	if err := c.get(ctx, "/users/"+strconv.FormatInt(uid, 10)+"/playlists/list", nil, &list); err != nil {
		return nil, err
	}
	out := make([]Playlist, 0, len(list))
	for _, p := range list {
		out = append(out, Playlist{
			ID:       strconv.FormatInt(uid, 10) + ":" + strconv.Itoa(p.Kind),
			Kind:     p.Kind,
			Name:     p.Title,
			Tracks:   p.TrackCount,
			Revision: p.Revision,
		})
	}
	return out, nil
}

// PlaylistTrackURIs — треки плейлиста по порядку.
func (c *Client) PlaylistTrackURIs(ctx context.Context, playlistID string, limit int) ([]string, error) {
	col, err := c.PlaylistCollection(ctx, playlistID, limit)
	if err != nil {
		return nil, err
	}
	uris := make([]string, 0, len(col.Tracks))
	for _, t := range col.Tracks {
		uris = append(uris, t.URI)
	}
	return uris, nil
}

// OrdersPlaylistTitle — плейлист, куда пишутся заказы, если Ynison не
// отвечает. Приложение создаёт его само.
const OrdersPlaylistTitle = "Заказы со стрима"

// ordersMu — две записи в плейлист разом поссорились бы из-за ревизии.
var ordersMu sync.Mutex

// ordersPlaylist находит плейлист заказов или создаёт его.
func (c *Client) ordersPlaylist(ctx context.Context) (Playlist, error) {
	list, err := c.Playlists(ctx)
	if err != nil {
		return Playlist{}, err
	}
	for _, p := range list {
		if p.Name == OrdersPlaylistTitle {
			return p, nil
		}
	}
	uid, err := c.uid(ctx)
	if err != nil {
		return Playlist{}, err
	}
	form := url.Values{}
	form.Set("title", OrdersPlaylistTitle)
	form.Set("visibility", "private")
	var created playlistJSON
	if err := c.post(ctx, "/users/"+strconv.FormatInt(uid, 10)+"/playlists/create", form, &created); err != nil {
		return Playlist{}, errs.Wrap(errs.YandexPlaylist, "Не создался плейлист «"+OrdersPlaylistTitle+"».", err)
	}
	c.log.Info("создан плейлист для заказов", "kind", created.Kind)
	return Playlist{
		ID:       strconv.FormatInt(uid, 10) + ":" + strconv.Itoa(created.Kind),
		Kind:     created.Kind,
		Name:     OrdersPlaylistTitle,
		Revision: created.Revision,
	}, nil
}

// AddToOrders дописывает трек в конец плейлиста заказов.
func (c *Client) AddToOrders(ctx context.Context, trackURI string) error {
	trackID, albumID, ok := ParseURI(trackURI)
	if !ok {
		return errs.New(errs.YandexPlaylist, "В плейлист Яндекса можно положить только трек из Яндекса.")
	}
	ordersMu.Lock()
	defer ordersMu.Unlock()

	pl, err := c.ordersPlaylist(ctx)
	if err != nil {
		return err
	}
	diff, _ := json.Marshal([]map[string]any{{
		"op":     "insert",
		"at":     pl.Tracks,
		"tracks": []map[string]string{{"id": trackID, "albumId": albumID}},
	}})
	form := url.Values{}
	form.Set("diff", string(diff))
	form.Set("revision", strconv.Itoa(pl.Revision))
	uid, _ := c.uid(ctx)
	path := "/users/" + strconv.FormatInt(uid, 10) + "/playlists/" + strconv.Itoa(pl.Kind) + "/change-relative"
	if err := c.post(ctx, path, form, nil); err != nil {
		return errs.Wrap(errs.YandexPlaylist, "Не записал трек в плейлист «"+OrdersPlaylistTitle+"».", err)
	}
	return nil
}
