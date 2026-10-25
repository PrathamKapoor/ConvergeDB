// Package store implements ConvergeDB's durable SQLite persistence: schema
// migrations, the append-only operation log, actor sequence allocation and
// hole-aware inventory queries.
//
// Durability contract: operations are inserted inside explicit transactions
// with journal_mode=WAL and synchronous=FULL. A caller may treat a returned
// commit as durable against process crash; power-loss durability further
// depends on OS/disk behavior and is NOT tested here. Nothing in this
// package deletes operations or tombstones.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver (no CGO)
)

// DriverName is the database/sql driver registered by modernc.org/sqlite.
const DriverName = "sqlite"

// Store wraps one SQLite database file (one per replica).
type Store struct {
	db *sql.DB
	mu sync.Mutex // serializes local sequence allocation + appends
}

const dsnPragmas = "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_txlock=immediate"

// Open opens (creating if needed) the database at path, runs pending
// migrations and returns the store.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	db, err := sql.Open(DriverName, "file:"+filepath.ToSlash(path)+"?"+dsnPragmas)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// One writer at a time is the model; a small pool keeps memory bounded.
	db.SetMaxOpenConns(4)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// migrations are applied in ascending version order inside a transaction.
var migrations = []struct {
	version int
	stmts   []string
}{
	{
		version: 1,
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS meta (
				key   TEXT PRIMARY KEY,
				value TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS operations (
				op_id            TEXT PRIMARY KEY,
				actor            TEXT    NOT NULL,
				actor_seq        INTEGER NOT NULL,
				protocol_version INTEGER NOT NULL,
				doc_id           TEXT    NOT NULL,
				field            TEXT    NOT NULL,
				kind             TEXT    NOT NULL,
				canonical        TEXT    NOT NULL,
				checksum         TEXT    NOT NULL,
				origin           TEXT    NOT NULL,
				received_ms      INTEGER NOT NULL,
				hlc_wall         INTEGER NOT NULL,
				hlc_counter      INTEGER NOT NULL,
				hlc_actor        TEXT    NOT NULL,
				UNIQUE (actor, actor_seq)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_operations_actor_seq
				ON operations (actor, actor_seq)`,
			`CREATE INDEX IF NOT EXISTS idx_operations_doc
				ON operations (doc_id)`,
		},
	},
	{
		version: 2,
		stmts: []string{
			// Idempotency keys for local API mutations: a retried POST with
			// the same key replays the original response instead of creating
			// a second operation.
			`CREATE TABLE IF NOT EXISTS idempotency (
				key        TEXT PRIMARY KEY,
				op_id      TEXT    NOT NULL,
				response   TEXT    NOT NULL,
				created_ms INTEGER NOT NULL
			)`,
		},
	},
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_ms INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		for _, stmt := range m.stmts {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d failed: %w", m.version, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_ms) VALUES (?, strftime('%s','now') * 1000)`, m.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}
	return nil
}

// SchemaVersion returns the applied schema version.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// GetMeta returns the value for key and whether it existed.
func (s *Store) GetMeta(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta upserts a meta key.
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
