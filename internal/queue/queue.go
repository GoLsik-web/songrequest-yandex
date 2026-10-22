// Package queue — очередь заказов, которая переживает перезапуск приложения.
//
// Своя очередь, а не родная очередь Spotify: из неё нельзя удалить трек и
// нельзя менять порядок, а стримеру нужно и то и другое.
package queue

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Источники заказа.
const (
	SourcePoints   = "points"
	SourceDonation = "donation"
	SourceManual   = "manual"
	// SourcePlaylist — трек приехал из плейлиста, одобренного стримером.
	// Отдельно от «за баллы» нарочно: за плейлист платят один раз, а треков
	// в очередь ложится сразу несколько, и в хронике это должно быть видно.
	SourcePlaylist = "playlist"
)

// Item — заказ в очереди.
type Item struct {
	ID        int64  `json:"id"`
	Position  int    `json:"position"`
	Source    string `json:"source"`
	Requester string `json:"requester"`
	// RequesterLogin — логин на Twitch, в нижнем регистре. Отличается от
	// Requester у всех, кто поставил себе отображаемое имя на другом языке,
	// а бан-лист работает именно по логину.
	RequesterLogin string `json:"requester_login"`
	// RawRequest — что написал зритель. Показываем рядом с найденным треком.
	RawRequest string `json:"raw_request"`

	Provider string `json:"provider"` // spotify | youtube
	// Via — почему заказ играет через это звено, словами для человека:
	// «в Spotify такого нет», «Spotify не ответил», «по ссылке зрителя».
	// Пусто у обычного заказа из Spotify: там объяснять нечего.
	Via        string `json:"via"`
	TrackID    string `json:"track_id"`
	URI        string `json:"uri"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	DurationMs int    `json:"duration_ms"`
	CoverURL   string `json:"cover_url"`
	Uncertain  bool   `json:"uncertain"`

	// RedemptionID и RewardID нужны, чтобы вернуть баллы за этот заказ.
	RedemptionID string    `json:"redemption_id"`
	RewardID     string    `json:"reward_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// ErrEmpty означает, что очередь пуста.
var ErrEmpty = errors.New("очередь пуста")

// ErrNotFound — заказа с таким номером в очереди уже нет. Чаще всего его
// только что забрал плеер: тогда возвращать за него баллы нельзя, он играет.
var ErrNotFound = errors.New("заказа в очереди уже нет")

// Queue — очередь поверх SQLite.
type Queue struct {
	db *sql.DB
}

// New создаёт очередь на уже открытой базе.
func New(db *sql.DB) *Queue { return &Queue{db: db} }

// Add ставит заказ в конец очереди.
//
// Донаты по умолчанию идут вперёд заказов за баллы, но не вперёд других
// донатов: иначе последний задонативший всегда обгонял бы предыдущего.
func (q *Queue) Add(item Item, donationsFirst bool) (Item, error) {
	tx, err := q.db.Begin()
	if err != nil {
		return item, err
	}
	defer tx.Rollback()

	pos, err := insertPosition(tx, item.Source, donationsFirst)
	if err != nil {
		return item, err
	}
	item.Position = pos
	item.CreatedAt = time.Now()

	res, err := tx.Exec(`
		INSERT INTO queue(position, source, requester, requester_login, raw_request,
		                  provider, via, track_id, uri, title, artist, duration_ms, cover_url,
		                  uncertain, redemption_id, reward_id, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Position, item.Source, item.Requester, item.RequesterLogin, item.RawRequest,
		item.Provider, item.Via, item.TrackID, item.URI, item.Title, item.Artist, item.DurationMs,
		item.CoverURL, boolToInt(item.Uncertain), item.RedemptionID, item.RewardID,
		item.CreatedAt.Unix())
	if err != nil {
		return item, err
	}
	if item.ID, err = res.LastInsertId(); err != nil {
		return item, err
	}

	return item, tx.Commit()
}

// insertPosition считает, куда встанет новый заказ.
func insertPosition(tx *sql.Tx, source string, donationsFirst bool) (int, error) {
	if !donationsFirst || source != SourceDonation {
		var max sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(position) FROM queue`).Scan(&max); err != nil {
			return 0, err
		}
		return int(max.Int64) + 1, nil
	}

	// Донат встаёт после последнего доната и перед первым заказом за баллы.
	var after sql.NullInt64
	if err := tx.QueryRow(
		`SELECT MAX(position) FROM queue WHERE source = ?`, SourceDonation).Scan(&after); err != nil {
		return 0, err
	}

	var firstPoints sql.NullInt64
	if err := tx.QueryRow(
		`SELECT MIN(position) FROM queue WHERE source <> ?`, SourceDonation).Scan(&firstPoints); err != nil {
		return 0, err
	}

	if !firstPoints.Valid {
		return int(after.Int64) + 1, nil
	}

	pos := int(firstPoints.Int64)
	if after.Valid && int(after.Int64) >= pos {
		pos = int(after.Int64) + 1
	}
	// Освобождаем место, сдвигая всё, что за ним.
	if _, err := tx.Exec(`UPDATE queue SET position = position + 1 WHERE position >= ?`, pos); err != nil {
		return 0, err
	}
	return pos, nil
}

// List отдаёт очередь по порядку.
func (q *Queue) List() ([]Item, error) { return readQueue(q.db) }

// rowsSource — то, у чего можно спросить строки: и база, и открытая сделка.
type rowsSource interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// readQueue читает очередь по порядку.
func readQueue(src rowsSource) ([]Item, error) {
	rows, err := src.Query(`
		SELECT id, position, source, requester, requester_login, raw_request, provider,
		       via, track_id, uri, title, artist, duration_ms, cover_url, uncertain,
		       redemption_id, reward_id, created_at
		  FROM queue ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		var uncertain int
		var created int64
		if err := rows.Scan(&it.ID, &it.Position, &it.Source, &it.Requester,
			&it.RequesterLogin, &it.RawRequest, &it.Provider, &it.Via, &it.TrackID, &it.URI,
			&it.Title, &it.Artist, &it.DurationMs, &it.CoverURL, &uncertain,
			&it.RedemptionID, &it.RewardID, &created); err != nil {
			return nil, err
		}
		it.Uncertain = uncertain == 1
		it.CreatedAt = time.Unix(created, 0)
		out = append(out, it)
	}
	return out, rows.Err()
}

// Next забирает первый заказ из очереди и удаляет его оттуда.
//
// Одной сделкой: между «посмотреть» и «удалить» стример успевал удалить тот
// же заказ из панели с возвратом баллов — и трек всё равно играл, уже
// бесплатно.
func (q *Queue) Next() (Item, error) {
	tx, err := q.db.Begin()
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()

	var (
		it        Item
		uncertain int
		created   int64
	)
	err = tx.QueryRow(`
		SELECT id, position, source, requester, requester_login, raw_request, provider,
		       via, track_id, uri, title, artist, duration_ms, cover_url, uncertain,
		       redemption_id, reward_id, created_at
		  FROM queue ORDER BY position LIMIT 1`).
		Scan(&it.ID, &it.Position, &it.Source, &it.Requester, &it.RequesterLogin,
			&it.RawRequest, &it.Provider, &it.Via, &it.TrackID, &it.URI, &it.Title, &it.Artist,
			&it.DurationMs, &it.CoverURL, &uncertain, &it.RedemptionID, &it.RewardID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrEmpty
	}
	if err != nil {
		return Item{}, err
	}

	if _, err := tx.Exec(`DELETE FROM queue WHERE id = ?`, it.ID); err != nil {
		return Item{}, err
	}
	it.Uncertain = uncertain == 1
	it.CreatedAt = time.Unix(created, 0)

	// Взятый заказ запоминаем той же сделкой — см. TakeInflight.
	if raw, err := json.Marshal(it); err == nil {
		if _, err := tx.Exec(`INSERT INTO kv(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, inflightKey, string(raw)); err != nil {
			return Item{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Item{}, err
	}
	return it, nil
}

// inflightKey — заказ, который вынут из очереди и ещё не закончился.
//
// Приложение закрыли посреди заказа — и он пропадал бесследно: из очереди
// уже вынут, а плеер о нём забыл. Зритель заплатил, трек не дослушал.
const inflightKey = "inflight"

// Done — взятый заказ закончился (любым исходом).
func (q *Queue) Done() {
	_, _ = q.db.Exec(`DELETE FROM kv WHERE key = ?`, inflightKey)
}

// TakeInflight отдаёт заказ, оставшийся незаконченным с прошлого запуска.
// Запись не стирается: это делает ForgetInflight, когда заказ уже снова в
// очереди (или решён иначе).
func (q *Queue) TakeInflight() (Item, bool) {
	var raw string
	if err := q.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, inflightKey).Scan(&raw); err != nil {
		return Item{}, false
	}
	var it Item
	if err := json.Unmarshal([]byte(raw), &it); err != nil || it.URI == "" {
		q.Done()
		return Item{}, false
	}
	return it, true
}

// ForgetInflight стирает запись о незаконченном заказе — но только если это
// всё ещё он: плеер мог уже взять новый и записать его на то же место.
func (q *Queue) ForgetInflight(it Item) {
	var raw string
	if err := q.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, inflightKey).Scan(&raw); err != nil {
		return
	}
	var cur Item
	if json.Unmarshal([]byte(raw), &cur) == nil && (cur.ID != it.ID || cur.URI != it.URI) {
		return
	}
	_, _ = q.db.Exec(`DELETE FROM kv WHERE key = ? AND value = ?`, inflightKey, raw)
}

// PutJSON кладёт значение в kv; nil — стирает ключ.
func (q *Queue) PutJSON(key string, v any) {
	if v == nil {
		_, _ = q.db.Exec(`DELETE FROM kv WHERE key = ?`, key)
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = q.db.Exec(`INSERT INTO kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(raw))
}

// GetJSON читает значение из kv. false — ключа нет или он испорчен.
func (q *Queue) GetJSON(key string, v any) bool {
	var raw string
	if err := q.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&raw); err != nil {
		return false
	}
	return json.Unmarshal([]byte(raw), v) == nil
}

// Peek смотрит на первый заказ, не забирая его.
func (q *Queue) Peek() (Item, error) {
	items, err := q.List()
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrEmpty
	}
	return items[0], nil
}

// Get находит заказ по идентификатору.
func (q *Queue) Get(id int64) (Item, error) {
	items, err := q.List()
	if err != nil {
		return Item{}, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return Item{}, fmt.Errorf("заказа %d в очереди нет", id)
}

// Remove убирает заказ.
func (q *Queue) Remove(id int64) error {
	res, err := q.db.Exec(`DELETE FROM queue WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Clear очищает очередь и возвращает то, что в ней было, — чтобы вызывающий
// код мог вернуть за эти заказы баллы.
func (q *Queue) Clear() ([]Item, error) {
	// Одной сделкой: между «посмотреть» и «удалить» плеер успевал забрать
	// заказ через Next(), и тот отыгрывал уже после очистки — бесплатно и
	// без всякой возможности его остановить.
	tx, err := q.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	items, err := readQueue(tx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM queue`); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

// MoveTop поднимает заказ в начало очереди.
func (q *Queue) MoveTop(id int64) error {
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var min sql.NullInt64
	if err := tx.QueryRow(`SELECT MIN(position) FROM queue`).Scan(&min); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE queue SET position = ? WHERE id = ?`, int(min.Int64)-1, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// Reorder расставляет заказы в переданном порядке — так работает
// перетаскивание в панели.
func (q *Queue) Reorder(ids []int64) error {
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	seen := make(map[int64]bool, len(ids))
	pos := 0
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.Exec(`UPDATE queue SET position = ? WHERE id = ?`, pos, id); err != nil {
			return err
		}
		pos++
	}
	// Панель могла прислать список неполным: пока тащили заказ, пришёл новый.
	// Пропущенные дописываем в конец в прежнем взаимном порядке, иначе их
	// старые позиции совпали бы с новыми.
	rows, err := tx.Query(`SELECT id FROM queue ORDER BY position, id`)
	if err != nil {
		return err
	}
	var rest []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range rest {
		if _, err := tx.Exec(`UPDATE queue SET position = ? WHERE id = ?`, pos, id); err != nil {
			return err
		}
		pos++
	}
	return tx.Commit()
}

// CountBy считает активные заказы одного зрителя — по этому числу работает
// ограничение «не больше трёх в очереди».
// Считаем и по логину, и по отображаемому имени.
//
// Логин надёжнее: отображаемое имя зритель меняет в Twitch мгновенно и без
// ограничений, то есть лимит по нему обходится за десять секунд. Но в старых
// строках базы логина ещё нет, поэтому старое сравнение оставлено запасным.
func (q *Queue) CountBy(requester string) (int, error) {
	var n int
	err := q.db.QueryRow(
		`SELECT COUNT(*) FROM queue
		  WHERE requester_login = ? COLLATE NOCASE
		     OR (requester_login = '' AND requester = ? COLLATE NOCASE)`,
		requester, requester).Scan(&n)
	return n, err
}

// Len — сколько заказов в очереди.
func (q *Queue) Len() (int, error) {
	var n int
	err := q.db.QueryRow(`SELECT COUNT(*) FROM queue`).Scan(&n)
	return n, err
}

// TotalDuration — сколько всего осталось играть.
func (q *Queue) TotalDuration() (time.Duration, error) {
	var ms sql.NullInt64
	if err := q.db.QueryRow(`SELECT SUM(duration_ms) FROM queue`).Scan(&ms); err != nil {
		return 0, err
	}
	return time.Duration(ms.Int64) * time.Millisecond, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Replace подменяет трек в уже стоящем заказе.
//
// Нужно для ручного исправления: подбор ошибся, стример выбрал правильный
// трек. Всё остальное — кто заказал, что написал, место в очереди, чем
// возвращать баллы — остаётся прежним, меняется только сама музыка. Метка
// «неточное совпадение» снимается: выбор сделан человеком.
func (q *Queue) Replace(id int64, track Item) (Item, error) {
	res, err := q.db.Exec(
		`UPDATE queue SET provider = ?, via = ?, track_id = ?, uri = ?, title = ?, artist = ?,
		                  duration_ms = ?, cover_url = ?, uncertain = 0
		 WHERE id = ?`,
		track.Provider, track.Via, track.TrackID, track.URI, track.Title, track.Artist,
		track.DurationMs, track.CoverURL, id)
	if err != nil {
		return Item{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Item{}, fmt.Errorf("заказа %d в очереди нет", id)
	}
	return q.Get(id)
}
