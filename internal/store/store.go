// Package store — SQLite-хранилище: очередь, история, баны, кэш матчинга.
// Драйвер modernc.org/sqlite — чистый Go, поэтому сборка не требует cgo и
// компилятора C, и всё приложение остаётся одним .exe.
package store

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB — обёртка над соединением с базой.
type DB struct {
	sql *sql.DB
}

// migrations применяются по порядку; номер последней применённой лежит в
// user_version, поэтому повторный запуск ничего не ломает.
var migrations = []string{
	`
	CREATE TABLE queue (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		position      INTEGER NOT NULL,
		source        TEXT NOT NULL,           -- points | donation | manual
		requester     TEXT NOT NULL,
		raw_request   TEXT NOT NULL,
		provider      TEXT NOT NULL,           -- spotify | youtube
		track_id      TEXT,
		title         TEXT NOT NULL,
		artist        TEXT NOT NULL,
		duration_ms   INTEGER NOT NULL DEFAULT 0,
		cover_url     TEXT,
		uncertain     INTEGER NOT NULL DEFAULT 0,
		redemption_id TEXT,                    -- нужен, чтобы вернуть баллы
		reward_id     TEXT,
		created_at    INTEGER NOT NULL
	);
	CREATE INDEX idx_queue_position ON queue(position);

	CREATE TABLE history (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		requester   TEXT NOT NULL,
		raw_request TEXT NOT NULL,
		provider    TEXT NOT NULL,
		track_id    TEXT,
		title       TEXT NOT NULL,
		artist      TEXT NOT NULL,
		outcome     TEXT NOT NULL,             -- played | skipped | rejected
		reason      TEXT,
		played_at   INTEGER NOT NULL
	);
	CREATE INDEX idx_history_played_at ON history(played_at DESC);

	CREATE TABLE mod_log (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		actor      TEXT NOT NULL,
		action     TEXT NOT NULL,
		target     TEXT,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX idx_mod_log_created_at ON mod_log(created_at DESC);

	CREATE TABLE music_bans (
		login      TEXT PRIMARY KEY,
		reason     TEXT,
		created_at INTEGER NOT NULL
	);

	-- Кэш матчинга: ключ — нормализованный запрос. Сюда же ложатся ручные
	-- исправления стримера (manual=1), они всегда сильнее автоматического ответа.
	CREATE TABLE match_cache (
		query      TEXT PRIMARY KEY,
		track_id   TEXT NOT NULL,
		title      TEXT NOT NULL,
		artist     TEXT NOT NULL,
		score      REAL NOT NULL DEFAULT 0,
		manual     INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE kv (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`,

	// Для воспроизведения нужен не только идентификатор трека, но и его uri:
	// именно его принимает Spotify. Отдельной миграцией, потому что база у
	// тестера уже создана по первой.
	`ALTER TABLE queue ADD COLUMN uri TEXT NOT NULL DEFAULT '';`,

	// Заказ, взятый из памяти, должен проходить те же фильтры и выглядеть
	// в панели так же, как найденный, — значит длительность и обложку тоже
	// надо помнить.
	`ALTER TABLE match_cache ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE match_cache ADD COLUMN cover_url TEXT NOT NULL DEFAULT '';`,

	// Бан-лист хранит логин, а в заказе лежало только отображаемое имя. У
	// зрителей с кириллическим ником это разные строки, и бан не срабатывал
	// вовсе. Теперь логин едет вместе с заказом.
	`ALTER TABLE queue ADD COLUMN requester_login TEXT NOT NULL DEFAULT '';`,

	// Через какое звено цепочки заказ сыграет и почему именно через него.
	// Раньше это знал только лог: в панели заказ с YouTube выглядел так же,
	// как заказ из Spotify, и вопрос «почему этот играет мимо Spotify» решался
	// чтением лога. Очередь переживает перезапуск, поэтому объяснение хранится
	// вместе с ней.
	`ALTER TABLE queue ADD COLUMN via TEXT NOT NULL DEFAULT '';`,
}

// Open открывает базу в dir/songrequest.db и доводит схему до последней версии.
func Open(dir string) (*DB, error) {
	path := filepath.Join(dir, "songrequest.db")

	// _pragma=busy_timeout — чтобы параллельная запись ждала, а не падала.
	// journal_mode=WAL даёт читателям не блокировать писателя.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("открытие базы: %w", err)
	}

	// Одно соединение на запись: SQLite всё равно сериализует записи, а так мы
	// не плодим лишние хэндлы и не ловим "database is locked" на пустом месте.
	conn.SetMaxOpenConns(1)

	db := &DB{sql: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) migrate() error {
	var version int
	if err := d.sql.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("чтение версии схемы: %w", err)
	}

	for i := version; i < len(migrations); i++ {
		tx, err := d.sql.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("миграция %d: %w", i+1, err)
		}
		// PRAGMA не принимает параметры, поэтому номер подставляем в текст.
		// Значение берётся из длины среза, а не от пользователя.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("миграция %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SQL даёт доступ к соединению пакетам, которые появятся на следующих этапах.
func (d *DB) SQL() *sql.DB { return d.sql }

// Close закрывает базу.
func (d *DB) Close() error { return d.sql.Close() }

// SetKV сохраняет мелкое значение (например, id созданной награды Twitch).
func (d *DB) SetKV(key, value string) error {
	_, err := d.sql.Exec(
		`INSERT INTO kv(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// GetKV читает значение; ok=false, если ключа нет.
func (d *DB) GetKV(key string) (value string, ok bool, err error) {
	err = d.sql.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}
