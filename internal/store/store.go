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

	invMu sync.Mutex
	inv   map[string]*actorState // incremental per-actor receive state
}

// actorState tracks what this replica holds for one actor: the contiguous
// frontier, the highest sequence seen, and the out-of-order holes above the
// frontier. It is maintained incrementally on every accepted operation and
// rebuilt by one indexed scan at startup; a differential test
// (TestIncrementalInventoryMatchesScan) proves it stays identical to a fresh
// full-scan computation.
type actorState struct {
	frontier      uint64
	maxSeen       uint64
	receivedAbove map[uint64]struct{}
	partial       bool
}

func newActorState() *actorState {
	return &actorState{receivedAbove: map[uint64]struct{}{}}
}

// observe applies one accepted (actor, seq) to the incremental state.
func (a *actorState) observe(seq uint64, holeCap int) {
	if seq > a.maxSeen {
		a.maxSeen = seq
	}
	if seq == a.frontier+1 {
		a.frontier = seq
		for {
			next := a.frontier + 1
			if _, hole := a.receivedAbove[next]; !hole {
				break
			}
			delete(a.receivedAbove, next)
			a.frontier = next
		}
		return
	}
	if seq > a.frontier {
		if _, seen := a.receivedAbove[seq]; !seen {
			if len(a.receivedAbove) >= holeCap {
				a.partial = true
				// Keep the SMALLEST holeCap holes so the advertised list is
				// deterministic and identical to a fresh full-scan: evict
				// the current largest if the new hole is smaller. Overflow
				// arrivals only happen under pathological reordering; the
				// eviction scan is bounded by the cap.
				largest, maxSeq := uint64(0), uint64(0)
				for h := range a.receivedAbove {
					if h > maxSeq {
						maxSeq, largest = h, h
					}
				}
				if seq < maxSeq {
					delete(a.receivedAbove, largest)
					a.receivedAbove[seq] = struct{}{}
				}
				return
			}
			a.receivedAbove[seq] = struct{}{}
		}
	}
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
	s := &Store{db: db}
	if err := s.rebuildInventory(InventoryHoleCap); err != nil {
		db.Close()
		return nil, fmt.Errorf("rebuild inventory: %w", err)
	}
	return s, nil
}

// InventoryHoleCap bounds the advertised out-of-order hole list per actor.
const InventoryHoleCap = 512

// rebuildInventory recomputes the incremental per-actor state with one
// indexed scan. Called once at startup; afterwards state is maintained
// incrementally.
func (s *Store) rebuildInventory(holeCap int) error {
	rows, err := s.db.Query(`SELECT actor, actor_seq FROM operations ORDER BY actor, actor_seq`)
	if err != nil {
		return err
	}
	defer rows.Close()
	inv := map[string]*actorState{}
	for rows.Next() {
		var actor string
		var seq uint64
		if err := rows.Scan(&actor, &seq); err != nil {
			return err
		}
		a := inv[actor]
		if a == nil {
			a = newActorState()
			inv[actor] = a
		}
		a.observe(seq, holeCap)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.invMu.Lock()
	s.inv = inv
	s.invMu.Unlock()
	return nil
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
		version: 3,
		stmts: []string{
			// Materializer checkpoint: the JSON state of the CRDT
			// accumulator for the per-actor dense operation prefixes in
			// `covered`. Append-only logging makes those prefixes immutable,
			// so a checkpoint can never be invalidated by later writes; its
			// validity is re-checked against per-actor row counts on load
			// and ANY mismatch falls back to a full replay.
			`CREATE TABLE IF NOT EXISTS checkpoint (
				id               INTEGER PRIMARY KEY CHECK (id = 1),
				protocol_version INTEGER NOT NULL,
				covered          TEXT    NOT NULL,
				state            TEXT    NOT NULL,
				state_sha256     TEXT    NOT NULL,
				created_ms       INTEGER NOT NULL
			)`,
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
