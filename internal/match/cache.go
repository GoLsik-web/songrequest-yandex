package match

import (
	"context"
	"database/sql"
	"time"
)

// Cache помнит, чем закончился поиск по такому же запросу.
//
// Один и тот же популярный трек закажут два десятка раз за стрим, и каждый
// раз ходить в Spotify пятью запросами незачем. Сюда же ложатся ручные
// исправления стримера: «это не тот трек, вот правильный» — такой ответ
// сильнее автоматического и не устаревает.
type Cache struct {
	db *sql.DB
	// TTL — сколько живёт автоматический ответ. Ручные исправления вечны.
	TTL time.Duration
}

// NewCache создаёт кэш поверх уже открытой базы.
func NewCache(db *sql.DB) *Cache {
	return &Cache{db: db, TTL: 30 * 24 * time.Hour}
}

// Key — по чему ищем в кэше. Нормализованный текст заказа: «QUEEN -
// Bohemian Rhapsody» и «queen bohemian rhapsody» должны попасть в одну ячейку.
func Key(req Request) string {
	if req.HasParts() {
		return Clean(req.Artist + " " + req.Title)
	}
	return Clean(req.Title)
}

// Hit — что лежало в кэше.
type Hit struct {
	TrackID string
	Title   string
	Artist  string
	// DurationMs и CoverURL нужны очереди: заказ, взятый из памяти, должен
	// проходить те же фильтры и выглядеть в панели так же, как найденный.
	DurationMs int
	CoverURL   string
	Score      float64
	Manual     bool
}

// Get ищет готовый ответ. ok=false означает, что надо искать заново.
func (c *Cache) Get(ctx context.Context, key string) (Hit, bool, error) {
	if c == nil || c.db == nil || key == "" {
		return Hit{}, false, nil
	}

	var (
		hit     Hit
		manual  int
		created int64
	)
	err := c.db.QueryRowContext(ctx,
		`SELECT track_id, title, artist, duration_ms, cover_url, score, manual, created_at
		   FROM match_cache WHERE query = ?`, key).
		Scan(&hit.TrackID, &hit.Title, &hit.Artist, &hit.DurationMs, &hit.CoverURL,
			&hit.Score, &manual, &created)

	if err == sql.ErrNoRows {
		return Hit{}, false, nil
	}
	if err != nil {
		return Hit{}, false, err
	}

	hit.Manual = manual == 1
	if hit.Manual {
		return hit, true, nil
	}

	// Автоматический ответ протухает: каталог Spotify меняется, и вчерашний
	// «ничего лучше не нашлось» сегодня может найтись лучше.
	if time.Since(time.Unix(created, 0)) > c.TTL {
		return Hit{}, false, nil
	}
	return hit, true, nil
}

// Put запоминает автоматический ответ, не затирая ручное исправление.
func (c *Cache) Put(ctx context.Context, key string, hit Hit) error {
	if c == nil || c.db == nil || key == "" || hit.TrackID == "" {
		return nil
	}

	manual := 0
	if hit.Manual {
		manual = 1
	}

	// Ручное исправление важнее любого автоматического ответа: перезаписываем
	// строку, только если новая запись тоже ручная.
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO match_cache(query, track_id, title, artist, duration_ms, cover_url,
		                         score, manual, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(query) DO UPDATE SET
		   track_id    = excluded.track_id,
		   title       = excluded.title,
		   artist      = excluded.artist,
		   duration_ms = excluded.duration_ms,
		   cover_url   = excluded.cover_url,
		   score       = excluded.score,
		   manual      = excluded.manual,
		   created_at  = excluded.created_at
		 WHERE match_cache.manual = 0 OR excluded.manual = 1`,
		key, hit.TrackID, hit.Title, hit.Artist, hit.DurationMs, hit.CoverURL,
		hit.Score, manual, time.Now().Unix())
	return err
}

// Forget убирает запись — например, когда стример сказал, что трек неверный.
func (c *Cache) Forget(ctx context.Context, key string) error {
	if c == nil || c.db == nil {
		return nil
	}
	_, err := c.db.ExecContext(ctx, `DELETE FROM match_cache WHERE query = ?`, key)
	return err
}
