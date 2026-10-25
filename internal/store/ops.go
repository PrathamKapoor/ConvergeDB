package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
)

// AppendLocal durably appends a locally generated operation. The store
// finalizes local identity here: it allocates the next actor sequence
// number, derives the content-addressed op ID over the finalized body
// (sequence included), validates fail-closed, and persists the HLC
// watermark in the SAME transaction as the operation row — a crash can
// never yield an ACKed operation without its sequence and clock state, or
// vice versa. On return op.ActorSeq and op.OpID are assigned and the commit
// is durable. Caller-supplied OpID/ActorSeq values are overwritten.
func (s *Store) AppendLocal(op *core.Operation, issued clock.HLC) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op.HLC = issued
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin append: %w", err)
	}
	defer tx.Rollback()

	var next uint64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(actor_seq), 0) + 1 FROM operations WHERE actor = ?`, op.Actor).Scan(&next); err != nil {
		return fmt.Errorf("next seq: %w", err)
	}
	op.ActorSeq = next
	op.ProtocolVersion = core.ProtocolVersion
	id, err := core.ComputeOpID(*op)
	if err != nil {
		return fmt.Errorf("derive op id: %w", err)
	}
	op.OpID = id
	if err := core.Validate(*op); err != nil {
		return fmt.Errorf("local op invalid: %w", err)
	}
	checksum, err := core.Checksum(*op)
	if err != nil {
		return err
	}
	canonical, err := core.Canonical(*op)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO operations
		(op_id, actor, actor_seq, protocol_version, doc_id, field, kind, canonical, checksum, origin, received_ms, hlc_wall, hlc_counter, hlc_actor)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'local', ?, ?, ?, ?)`,
		op.OpID, op.Actor, op.ActorSeq, op.ProtocolVersion, op.DocID, op.Field, string(op.Kind),
		string(canonical), checksum, time.Now().UnixMilli(), issued.Wall, issued.Counter, issued.Actor)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return &ConflictError{OpID: op.OpID, Detail: "operation id or (actor,seq) already stored"}
		}
		return fmt.Errorf("insert op: %w", err)
	}
	if err := setMetaTx(tx, "hlc_wall", strconv.FormatInt(issued.Wall, 10)); err != nil {
		return err
	}
	if err := setMetaTx(tx, "hlc_counter", strconv.FormatInt(issued.Counter, 10)); err != nil {
		return err
	}
	return tx.Commit()
}

// IngestResult classifies a remote ingest outcome.
type IngestResult int

const (
	IngestAccepted IngestResult = iota
	IngestDuplicate
	IngestRejected
)

func (r IngestResult) String() string {
	switch r {
	case IngestAccepted:
		return "accepted"
	case IngestDuplicate:
		return "duplicate"
	default:
		return "rejected"
	}
}

// IngestOutcome reports what happened to one remote operation.
type IngestOutcome struct {
	Result IngestResult
	Reason string // rejection code (core.E_*) or empty
	OpID   string
}

// ConflictError signals an identity collision: an operation ID reused with
// different content, or an (actor, seq) pair reused with a different
// operation ID. Callers must surface this as a protocol conflict, never as a
// silent overwrite (invariant I-04).
type ConflictError struct {
	OpID   string
	Detail string
}

func (e *ConflictError) Error() string { return "conflict: " + e.Detail }

// IngestRemote idempotently stores a validated remote operation. Re-delivery
// of an identical operation is Duplicate; any identity collision with
// different content is Rejected with a core error code.
func (s *Store) IngestRemote(op core.Operation, origin string) (IngestOutcome, error) {
	checksum, err := core.Checksum(op)
	if err != nil {
		return IngestOutcome{Result: IngestRejected, Reason: core.CodeSchema, OpID: op.OpID}, err
	}
	canonical, err := core.Canonical(op)
	if err != nil {
		return IngestOutcome{Result: IngestRejected, Reason: core.CodeSchema, OpID: op.OpID}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return IngestOutcome{OpID: op.OpID}, err
	}
	defer tx.Rollback()

	var storedChecksum string
	err = tx.QueryRow(`SELECT checksum FROM operations WHERE op_id = ?`, op.OpID).Scan(&storedChecksum)
	switch {
	case err == sql.ErrNoRows:
		// fall through to (actor,seq) check + insert
	case err != nil:
		return IngestOutcome{OpID: op.OpID}, err
	default:
		if storedChecksum == checksum {
			_ = tx.Rollback()
			return IngestOutcome{Result: IngestDuplicate, OpID: op.OpID}, nil
		}
		// I-04: same op ID, different canonical content.
		_ = tx.Rollback()
		return IngestOutcome{Result: IngestRejected, Reason: core.CodeConflict, OpID: op.OpID},
			&ConflictError{OpID: op.OpID, Detail: "op_id reuse with different checksum"}
	}

	var existingID string
	err = tx.QueryRow(`SELECT op_id FROM operations WHERE actor = ? AND actor_seq = ?`, op.Actor, op.ActorSeq).Scan(&existingID)
	switch {
	case err == sql.ErrNoRows:
		// ok to insert
	case err != nil:
		return IngestOutcome{OpID: op.OpID}, err
	default:
		if existingID == op.OpID {
			_ = tx.Rollback()
			return IngestOutcome{Result: IngestDuplicate, OpID: op.OpID}, nil
		}
		_ = tx.Rollback()
		return IngestOutcome{Result: IngestRejected, Reason: core.CodeConflict, OpID: op.OpID},
			&ConflictError{OpID: op.OpID, Detail: "actor_seq reuse with different op_id"}
	}

	_, err = tx.Exec(`INSERT INTO operations
		(op_id, actor, actor_seq, protocol_version, doc_id, field, kind, canonical, checksum, origin, received_ms, hlc_wall, hlc_counter, hlc_actor)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.OpID, op.Actor, op.ActorSeq, op.ProtocolVersion, op.DocID, op.Field, string(op.Kind),
		string(canonical), checksum, origin, time.Now().UnixMilli(), op.HLC.Wall, op.HLC.Counter, op.HLC.Actor)
	if err != nil {
		return IngestOutcome{OpID: op.OpID}, fmt.Errorf("insert remote op: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return IngestOutcome{OpID: op.OpID}, err
	}
	return IngestOutcome{Result: IngestAccepted, OpID: op.OpID}, nil
}

func setMetaTx(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) checksumOf(opID string) (string, error) {
	var cs string
	err := s.db.QueryRow(`SELECT checksum FROM operations WHERE op_id = ?`, opID).Scan(&cs)
	return cs, err
}

// decodeStoredOp unmarshals canonical bytes and verifies the content hash
// against the stored checksum, failing closed on storage corruption.
func decodeStoredOp(canonical, checksum string) (core.Operation, error) {
	var op core.Operation
	if err := json.Unmarshal([]byte(canonical), &op); err != nil {
		return op, fmt.Errorf("decode stored operation: %w", err)
	}
	sum, err := core.Checksum(op)
	if err != nil {
		return op, err
	}
	if sum != checksum {
		return op, &ConflictError{OpID: op.OpID, Detail: "stored checksum does not match content (storage corruption)"}
	}
	return op, nil
}

// AllOps returns every stored operation in the canonical replay order:
// (hlc_wall, hlc_counter, hlc_actor, actor, actor_seq). Materialized state
// must be identical regardless of order, but a single fixed order makes
// debugging and digests deterministic.
func (s *Store) AllOps() ([]core.Operation, error) {
	rows, err := s.db.Query(`SELECT canonical, checksum FROM operations
		ORDER BY hlc_wall, hlc_counter, hlc_actor, actor, actor_seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []core.Operation
	for rows.Next() {
		var c, cs string
		if err := rows.Scan(&c, &cs); err != nil {
			return nil, err
		}
		op, err := decodeStoredOp(c, cs)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// OpCount returns the number of stored operations.
func (s *Store) OpCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&n)
	return n, err
}

// StateDigest returns (count, sha256-of-sorted-op-ids). Because it hashes
// the SORTED op ID list (not just a count), two replicas agree here only
// when their operation SETS match — not merely their sizes.
func (s *Store) OpDigest() (int, string, error) {
	rows, err := s.db.Query(`SELECT op_id FROM operations ORDER BY op_id`)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	h := sha256.New()
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, "", err
		}
		h.Write([]byte(id))
		h.Write([]byte{0})
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	return count, hex.EncodeToString(h.Sum(nil)), nil
}

// ActorInventory is one replica's view of what it holds for a given actor.
// Frontier is the highest sequence received with NO gaps from 1;
// ReceivedAbove lists received sequences above the frontier (the holes
// view); MaxSeen is the highest sequence seen at all. Advertising only
// MaxSeen would let permanently missing middle sequences go unnoticed
// (invariant I-06).
type ActorInventory struct {
	Actor         string   `json:"actor"`
	Frontier      uint64   `json:"frontier"`
	MaxSeen       uint64   `json:"max_seen"`
	ReceivedAbove []uint64 `json:"received_above"`
	Partial       bool     `json:"partial"` // ReceivedAbove was truncated to the cap
}

// Inventory computes the local per-actor inventory. receivedAboveCap bounds
// the hole list; when more holes exist, Partial is set and peers must fall
// back to resending the whole (frontier, max_seen] range for that actor.
func (s *Store) Inventory(receivedAboveCap int) ([]ActorInventory, error) {
	rows, err := s.db.Query(`SELECT actor, actor_seq FROM operations ORDER BY actor, actor_seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inv := map[string]*ActorInventory{}
	for rows.Next() {
		var actor string
		var seq uint64
		if err := rows.Scan(&actor, &seq); err != nil {
			return nil, err
		}
		a := inv[actor]
		if a == nil {
			a = &ActorInventory{Actor: actor, Frontier: 0, MaxSeen: 0, ReceivedAbove: []uint64{}}
			inv[actor] = a
		}
		if seq == a.Frontier+1 {
			a.Frontier = seq
			// The frontier advance may close previously-listed holes.
			for len(a.ReceivedAbove) > 0 && a.ReceivedAbove[0] <= a.Frontier {
				a.ReceivedAbove = a.ReceivedAbove[1:]
			}
		} else {
			if len(a.ReceivedAbove) >= receivedAboveCap {
				a.Partial = true
			} else {
				a.ReceivedAbove = append(a.ReceivedAbove, seq)
			}
		}
		if seq > a.MaxSeen {
			a.MaxSeen = seq
		}
	}
	out := make([]ActorInventory, 0, len(inv))
	for _, a := range inv {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Actor < out[j].Actor })
	return out, rows.Err()
}

// Page is one bounded page of operations offered to a peer.
type Page struct {
	Ops        []core.Operation `json:"ops"`
	HasMore    bool             `json:"has_more"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// MissingOpsFor returns the operations a peer with the given inventory is
// missing, in (actor, seq) order, bounded by limit and resumable via
// cursor. For each local actor: if the peer sent an inventory entry, every
// sequence above its frontier that is NOT in its received_above list is
// sent (the holes); if the peer advertised no entry, everything is sent.
// A partial hole list forces the safe fallback: resend the full
// (frontier, max_seen] range.
func (s *Store) MissingOpsFor(peerInv []ActorInventory, limit int, cursor string) (Page, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	byActor := map[string]ActorInventory{}
	for _, a := range peerInv {
		byActor[a.Actor] = a
	}

	var afterActor string
	var afterSeq uint64
	if cursor != "" {
		raw, err := base64.StdEncoding.DecodeString(cursor)
		if err == nil {
			parts := strings.SplitN(string(raw), ":", 2)
			if len(parts) == 2 {
				afterActor = parts[0]
				afterSeq, _ = strconv.ParseUint(parts[1], 10, 64)
			}
		}
	}

	rows, err := s.db.Query(`SELECT actor, actor_seq, canonical, checksum FROM operations
		ORDER BY actor, actor_seq`)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()

	page := Page{Ops: []core.Operation{}}
	for rows.Next() {
		var actor, canonical, cs string
		var seq uint64
		if err := rows.Scan(&actor, &seq, &canonical, &cs); err != nil {
			return Page{}, err
		}
		// Cursor resume: skip everything up to and including the cursor op.
		if afterActor != "" {
			if actor < afterActor || (actor == afterActor && seq <= afterSeq) {
				continue
			}
		}
		want := false
		if pi, ok := byActor[actor]; ok {
			if pi.Partial {
				want = seq > pi.Frontier && seq <= pi.MaxSeen
			} else {
				want = seq > pi.Frontier && !containsSeq(pi.ReceivedAbove, seq)
			}
		} else {
			want = true
		}
		if !want {
			continue
		}
		op, err := decodeStoredOp(canonical, cs)
		if err != nil {
			return Page{}, err
		}
		if len(page.Ops) >= limit {
			// Resume from the LAST RETURNED op so nothing between pages is
			// skipped; the next page re-examines rows after it. When the
			// final page ends exactly at the last row, callers simply get
			// one empty follow-up page with has_more=false.
			page.HasMore = true
			last := page.Ops[len(page.Ops)-1]
			page.NextCursor = encodeCursor(last.Actor, last.ActorSeq)
			break
		}
		page.Ops = append(page.Ops, op)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	return page, nil
}

func containsSeq(list []uint64, v uint64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func encodeCursor(actor string, seq uint64) string {
	return base64.StdEncoding.EncodeToString([]byte(actor + ":" + strconv.FormatUint(seq, 10)))
}

// ListOps returns a bounded, op-ID-cursor-paginated view of the log for
// debugging/API use.
func (s *Store) ListOps(limit int, afterOpID string) ([]core.Operation, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT canonical, checksum FROM operations
		WHERE (? = '' OR op_id > ?)
		ORDER BY op_id LIMIT ?`, afterOpID, afterOpID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []core.Operation
	for rows.Next() {
		var c, cs string
		if err := rows.Scan(&c, &cs); err != nil {
			return nil, err
		}
		op, err := decodeStoredOp(c, cs)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}
