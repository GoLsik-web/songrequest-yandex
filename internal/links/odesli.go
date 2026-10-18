package links

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Odesli — song.link: по ссылке на трек в одном сервисе отдаёт ссылки на
// тот же трек во всех остальных, Яндекс Музыку в том числе.
//
// Зачем. Зрители кидают ссылки на Spotify, Apple Music, Deezer. Названия по
// такой ссылке без ключа к самому сервису не узнать (а Spotify из России ещё
// и не отвечает), зато song.link знает и название, и — чаще всего — прямую
// ссылку на Яндекс. Тогда поиск не нужен вовсе.
//
// Без ключа сервис пускает десять запросов в минуту. На заказы со стрима
// этого хватает с запасом, а повторы берёт на себя память подбора.
type Odesli struct {
	http *http.Client
	base string
}

// NewOdesli создаёт клиент.
func NewOdesli() *Odesli {
	return &Odesli{
		http: &http.Client{Timeout: 10 * time.Second},
		base: "https://api.song.link/v1-alpha.1/links",
	}
}

// OdesliResult — что узнали по ссылке.
type OdesliResult struct {
	Artist string
	Title  string
	// YandexTrackID — номер трека в Яндекс Музыке. Пусто — song.link
	// такого трека в Яндексе не знает, остаётся искать по названию.
	YandexTrackID string
}

// Query — текст для поиска.
func (r OdesliResult) Query() string {
	if r.Artist == "" {
		return r.Title
	}
	return r.Artist + " - " + r.Title
}

var yandexTrackInURL = regexp.MustCompile(`/track/(\d+)`)

// Lookup спрашивает song.link о ссылке.
func (o *Odesli) Lookup(ctx context.Context, link string) (OdesliResult, error) {
	q := url.Values{}
	q.Set("url", link)
	q.Set("userCountry", "RU")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+"?"+q.Encode(), nil)
	if err != nil {
		return OdesliResult{}, err
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return OdesliResult{}, fmt.Errorf("song.link не отвечает: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return OdesliResult{}, errors.New("song.link просит подождать")
	}
	if resp.StatusCode != http.StatusOK {
		return OdesliResult{}, fmt.Errorf("song.link ответил %d", resp.StatusCode)
	}

	var body struct {
		EntityUniqueID  string `json:"entityUniqueId"`
		LinksByPlatform map[string]struct {
			URL            string `json:"url"`
			EntityUniqueID string `json:"entityUniqueId"`
		} `json:"linksByPlatform"`
		Entities map[string]struct {
			Title      string `json:"title"`
			ArtistName string `json:"artistName"`
			Type       string `json:"type"`
		} `json:"entitiesByUniqueId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(&body); err != nil {
		return OdesliResult{}, fmt.Errorf("song.link ответил непонятно: %w", err)
	}

	var res OdesliResult
	if e, ok := body.Entities[body.EntityUniqueID]; ok {
		res.Artist, res.Title = e.ArtistName, e.Title
	}
	if y, ok := body.LinksByPlatform["yandex"]; ok {
		if m := yandexTrackInURL.FindStringSubmatch(y.URL); m != nil {
			res.YandexTrackID = m[1]
		}
		if e, ok := body.Entities[y.EntityUniqueID]; ok && res.Title == "" {
			res.Artist, res.Title = e.ArtistName, e.Title
		}
	}
	res.Artist = strings.TrimSpace(res.Artist)
	res.Title = strings.TrimSpace(res.Title)
	if res.Title == "" && res.YandexTrackID == "" {
		return OdesliResult{}, errors.New("song.link не узнал трек по ссылке")
	}
	return res, nil
}

// NewOdesliAt — клиент с другим адресом, для проверок.
func NewOdesliAt(base string) *Odesli {
	o := NewOdesli()
	o.base = base
	return o
}
