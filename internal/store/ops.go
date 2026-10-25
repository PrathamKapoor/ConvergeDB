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
	if op.Kind == core.KindLabelAdd {
		// The add-tag derives from (actor, actor_seq), which only the store
		// knows; set it here so the op ID covers the finalized body.
		op.Payload.Tag = core.DeriveAddTag(op.Actor, op.ActorSeq)
	}
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
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invMu.Lock()
	a := s.inv[op.Actor]
	if a == nil {
		a = newActorState()
		s.inv[op.Actor] = a
	}
	a.observe(op.ActorSeq, InventoryHoleCap)
	s.invMu.Unlock()
	return nil
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
	s.invMu.Lock()
	a := s.inv[op.Actor]
	if a == nil {
		a = newActorState()
		s.inv[op.Actor] = a
	}
	a.observe(op.ActorSeq, InventoryHoleCap)
	s.invMu.Unlock()
	return IngestOutcome{Result: IngestAccepted, OpID: op.OpID}, nil
}

// CheckpointData is a loaded checkpoint with its verification outcome.
type CheckpointData struct {
	Covered       map[string]uint64
	State         []byte
	Valid         bool
	InvalidReason string
}

// SaveCheckpoint atomically records the materializer checkpoint for the
// given per-actor coverage watermarks.
func (s *Store) SaveCheckpoint(covered map[string]uint64, state []byte) error {
	sum := sha256.Sum256(state)
	coveredJSON, err := json.Marshal(covered)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO checkpoint (id, protocol_version, covered, state, state_sha256, created_ms)
		VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET protocol_version=excluded.protocol_version, covered=excluded.covered,
			state=excluded.state, state_sha256=excluded.state_sha256, created_ms=excluded.created_ms`,
		core.ProtocolVersion, string(coveredJSON), string(state), hex.EncodeToString(sum[:]), time.Now().UnixMilli())
	return err
}

// LoadCheckpoint returns the stored checkpoint after verifying (a) its own
// integrity hash and (b) that the durable log still contains exactly the
// dense per-actor prefixes the checkpoint claims to cover. Any mismatch or
// absence yields Valid=false with a reason; callers must fall back to full
// replay in that case. The count check is an indexed aggregate over the
// covered prefix — far cheaper than decoding operations.
func (s *Store) LoadCheckpoint() (CheckpointData, error) {
	var cd CheckpointData
	var protocolVersion int
	var coveredJSON, state, shaHex string
	var createdMS int64
	err := s.db.QueryRow(`SELECT protocol_version, covered, state, state_sha256, created_ms
		FROM checkpoint WHERE id = 1`).Scan(&protocolVersion, &coveredJSON, &state, &shaHex, &createdMS)
	if err == sql.ErrNoRows {
		cd.InvalidReason = "no checkpoint"
		return cd, nil
	}
	if err != nil {
		return cd, err
	}
	sum := sha256.Sum256([]byte(state))
	if hex.EncodeToString(sum[:]) != shaHex {
		cd.InvalidReason = "checkpoint state hash mismatch (corruption)"
		return cd, nil
	}
	if protocolVersion != core.ProtocolVersion {
		cd.InvalidReason = fmt.Sprintf("checkpoint protocol %d != %d", protocolVersion, core.ProtocolVersion)
		return cd, nil
	}
	if err := json.Unmarshal([]byte(coveredJSON), &cd.Covered); err != nil {
		cd.InvalidReason = "covered watermark undecodable"
		return cd, nil
	}
	// Prefix verification: for each covered actor, the log must hold EXACTLY
	// covered[actor] rows with seq <= covered[actor] (seqs are dense from 1
	// and append-only, so the prefix is immutable).
	for actor, frontier := range cd.Covered {
		if frontier == 0 {
			continue
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM operations WHERE actor = ? AND actor_seq <= ?`,
			actor, frontier).Scan(&n); err != nil {
			return cd, err
		}
		if uint64(n) != frontier {
			cd.InvalidReason = fmt.Sprintf("covered prefix mismatch for %s: log has %d rows <= %d", actor, n, frontier)
			return cd, nil
		}
	}
	cd.Valid = true
	cd.State = []byte(state)
	return cd, nil
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

// AllOpsTail returns operations NOT covered by the given per-actor dense
// watermarks (seq > covered[actor]), in (actor, seq) order. Used to replay
// only the tail after a checkpoint restore. Under-coverage is safe: CRDT
// merges are idempotent, so a re-applied tail op is a no-op.
func (s *Store) AllOpsTail(covered map[string]uint64) ([]core.Operation, error) {
	s.invMu.Lock()
	actors := make([]string, 0, len(s.inv))
	for actor := range s.inv {
		actors = append(actors, actor)
	}
	s.invMu.Unlock()
	sort.Strings(actors)
	var ops []core.Operation
	for _, actor := range actors {
		lo := covered[actor] // 0 when absent: full actor history
		rows, err := s.db.Query(`SELECT canonical, checksum FROM operations
			WHERE actor = ? AND actor_seq > ? ORDER BY actor_seq`, actor, lo)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c, cs string
			if err := rows.Scan(&c, &cs); err != nil {
				rows.Close()
				return nil, err
			}
			op, err := decodeStoredOp(c, cs)
			if err != nil {
				rows.Close()
				return nil, err
			}
			ops = append(ops, op)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return ops, nil
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

// Inventory returns the local per-actor inventory from the incremental
// in-memory state (O(actors) per call). The state is maintained on every
// accepted operation; TestIncrementalInventoryMatchesScan proves it stays
// identical to a fresh full-scan computation.
//
// receivedAboveCap bounds the advertised hole list; when more holes exist,
// Partial is set and peers fall back to resending the whole
// (frontier, max_seen] range for that actor.
func (s *Store) Inventory(receivedAboveCap int) ([]ActorInventory, error) {
	s.invMu.Lock()
	defer s.invMu.Unlock()
	out := make([]ActorInventory, 0, len(s.inv))
	for actor, a := range s.inv {
		ai := ActorInventory{
			Actor:         actor,
			Frontier:      a.frontier,
			MaxSeen:       a.maxSeen,
			ReceivedAbove: []uint64{},
		}
		for seq := range a.receivedAbove {
			ai.ReceivedAbove = append(ai.ReceivedAbove, seq)
		}
		sort.Slice(ai.ReceivedAbove, func(i, j int) bool { return ai.ReceivedAbove[i] < ai.ReceivedAbove[j] })
		if len(ai.ReceivedAbove) > receivedAboveCap {
			ai.ReceivedAbove = ai.ReceivedAbove[:receivedAboveCap]
		}
		// Partial must be set whenever the TRUE hole count exceeds the
		// ADVERTISED cap — otherwise a truncated hole list looks complete
		// and the peer would skip ops it still needs.
		if a.partial || len(a.receivedAbove) > receivedAboveCap {
			ai.Partial = true
		}
		out = append(out, ai)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Actor < out[j].Actor })
	return out, nil
}

// inventoryFromScan recomputes the inventory from the durable rows. It is
// the verification oracle used by TestIncrementalInventoryMatchesScan and by
// rebuildInventory at startup.
func (s *Store) inventoryFromScan(holeCap int) ([]ActorInventory, error) {
	rows, err := s.db.Query(`SELECT actor, actor_seq FROM operations ORDER BY actor, actor_seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inv := map[string]*actorState{}
	for rows.Next() {
		var actor string
		var seq uint64
		if err := rows.Scan(&actor, &seq); err != nil {
			return nil, err
		}
		a := inv[actor]
		if a == nil {
			a = newActorState()
			inv[actor] = a
		}
		a.observe(seq, holeCap)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ActorInventory, 0, len(inv))
	for actor, a := range inv {
		ai := ActorInventory{Actor: actor, Frontier: a.frontier, MaxSeen: a.maxSeen, ReceivedAbove: []uint64{}}
		for seq := range a.receivedAbove {
			ai.ReceivedAbove = append(ai.ReceivedAbove, seq)
		}
		sort.Slice(ai.ReceivedAbove, func(i, j int) bool { return ai.ReceivedAbove[i] < ai.ReceivedAbove[j] })
		if len(ai.ReceivedAbove) > holeCap {
			ai.ReceivedAbove = ai.ReceivedAbove[:holeCap]
		}
		if a.partial {
			ai.Partial = true
		}
		out = append(out, ai)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Actor < out[j].Actor })
	return out, nil
}

// Page is one bounded page of operations offered to a peer.
type Page struct {
	Ops        []core.Operation `json:"ops"`
	HasMore    bool             `json:"has_more"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// MissingOpsFor returns the operations a peer with the given inventory is
// missing, in (actor, seq) order, bounded by limit and resumable via cursor.
//
// Implementation: for each LOCAL actor (sorted), compute the candidate range
// (requester frontier, local max_seen] minus the requester's advertised
// received-above set (or the full range for a partial/missing entry), split
// it into contiguous SEGMENTS at the holes, and serve each segment with one
// indexed range query. Cost per request is O(pages served + holes), not
// O(entire log): the previous implementation scanned every row on every
// request, which made per-pull cost grow linearly with log size.
//
// Cursor semantics are unchanged: NextCursor encodes the LAST RETURNED op;
// resume re-examines strictly after it, so nothing between pages is skipped.
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

	// Snapshot the local per-actor state (frontier/maxSeen) without holding
	// the lock during query execution.
	s.invMu.Lock()
	actors := make([]string, 0, len(s.inv))
	local := make(map[string]*actorState, len(s.inv))
	for actor, a := range s.inv {
		actors = append(actors, actor)
		cp := *a
		cp.receivedAbove = nil
		local[actor] = &cp
	}
	s.invMu.Unlock()
	sort.Strings(actors)

	page := Page{Ops: []core.Operation{}}
	for _, actor := range actors {
		if page.HasMore {
			break
		}
		st := local[actor]
		var lo uint64 // exclusive lower bound
		if actor == afterActor {
			lo = afterSeq
		} else if actor < afterActor {
			continue
		}
		pi, hasEntry := byActor[actor]
		if hasEntry && pi.Frontier > lo {
			lo = pi.Frontier
		}
		hi := st.maxSeen
		if lo >= hi {
			continue
		}
		// Holes to SKIP: sequences the requester already holds above its
		// frontier. A partial inventory advertises an incomplete hole list,
		// so the safe fallback is to treat it as empty (full-range resend).
		var holes []uint64
		if hasEntry && !pi.Partial {
			for _, h := range pi.ReceivedAbove {
				if h > lo && h <= hi {
					holes = append(holes, h)
				}
			}
			sort.Slice(holes, func(i, j int) bool { return holes[i] < holes[j] })
		}
		// Contiguous segments of wanted sequences in (lo, hi].
		segments := make([][2]uint64, 0, len(holes)+1)
		next := lo + 1
		for _, h := range holes {
			if h > hi {
				break
			}
			if h > next {
				segments = append(segments, [2]uint64{next, h - 1})
			}
			if h >= next {
				next = h + 1
			}
		}
		if next <= hi {
			segments = append(segments, [2]uint64{next, hi})
		}

		for _, seg := range segments {
			if page.HasMore {
				break
			}
			rows, err := s.db.Query(`SELECT actor_seq, canonical, checksum FROM operations
				WHERE actor = ? AND actor_seq BETWEEN ? AND ? ORDER BY actor_seq`, actor, seg[0], seg[1])
			if err != nil {
				return Page{}, err
			}
			for rows.Next() {
				var seq uint64
				var canonical, cs string
				if err := rows.Scan(&seq, &canonical, &cs); err != nil {
					rows.Close()
					return Page{}, err
				}
				op, err := decodeStoredOp(canonical, cs)
				if err != nil {
					rows.Close()
					return Page{}, err
				}
				if len(page.Ops) >= limit {
					page.HasMore = true
					last := page.Ops[len(page.Ops)-1]
					page.NextCursor = encodeCursor(last.Actor, last.ActorSeq)
					break
				}
				page.Ops = append(page.Ops, op)
			}
			errRows := rows.Err()
			rows.Close()
			if errRows != nil {
				return Page{}, errRows
			}
		}
	}
	return page, nil
}

// MissingOpsForScan is the pre-optimization implementation that scans every
// row on each request. It is retained as the differential oracle for
// TestMissingOpsIndexedMatchesScan and as a behavioral reference for the
// indexed path.
func (s *Store) MissingOpsForScan(peerInv []ActorInventory, limit int, cursor string) (Page, error) {
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
		if afterActor != "" {
			if actor < afterActor || (actor == afterActor && seq <= afterSeq) {
				continue
			}
		}
		want := false
		if pi, ok := byActor[actor]; ok {
			if pi.Partial {
				// Safe fallback: resend everything above the requester's
				// frontier up to the LOCAL max seen — the requester's own
				// max_seen may be stale, and ops above it would otherwise
				// be stranded (it cannot advertise what it never saw).
				want = seq > pi.Frontier
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

// IdempotencyGet returns the stored op ID and response body for a replayed
// mutation key.
func (s *Store) IdempotencyGet(key string) (opID, response string, ok bool, err error) {
	err = s.db.QueryRow(`SELECT op_id, response FROM idempotency WHERE key = ?`, key).Scan(&opID, &response)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return opID, response, true, nil
}

// IdempotencyPut records a mutation response under its key, capping the
// table at maxIdempotencyRows by evicting the oldest entries.
func (s *Store) IdempotencyPut(key, opID, response string) error {
	if _, err := s.db.Exec(`INSERT INTO idempotency (key, op_id, response, created_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO NOTHING`, key, opID, response, time.Now().UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM idempotency WHERE key IN (
		SELECT key FROM idempotency ORDER BY created_ms DESC LIMIT -1 OFFSET ?)`, maxIdempotencyRows)
	return err
}

const maxIdempotencyRows = 10000

// ExecRaw executes a raw statement against the store. It exists for
// administrative tooling and adversarial tests (e.g. simulating checkpoint
// corruption); application code must never use it.
func (s *Store) ExecRaw(query string, args ...any) (sql.Result, error) {
	return s.db.Exec(query, args...)
}
