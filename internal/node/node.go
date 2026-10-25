// Package node wires the durable store, hybrid clock and CRDT
// materializer into a running replica: local write path, remote ingest,
// event bus, fault injection and the anti-entropy loop.
package node

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/crdt"
	"github.com/PrathamKapoor/ConvergeDB/internal/store"
)

// ProtocolVersion mirrors core.ProtocolVersion for convenience.
const ProtocolVersion = core.ProtocolVersion

// InventoryHoleCap bounds how many out-of-order sequences a node advertises
// per actor before declaring its inventory partial.
const InventoryHoleCap = 512

// PeerConfig identifies one replication peer.
type PeerConfig struct {
	ID    string `json:"id"`
	URL   string `json:"url"` // e.g. http://127.0.0.1:8102
	Token string `json:"-"`   // optional shared bearer for peer-to-peer sync
}

// Config is the replica configuration.
type Config struct {
	ID             string       `json:"id"`
	DBPath         string       `json:"db"`
	HTTPAddr       string       `json:"http"`
	Peers          []PeerConfig `json:"peers"`
	LabMode        bool         `json:"lab"`
	AuthToken      string       `json:"-"` // optional; gates mutations/sync/lab when set
	SyncIntervalMS int          `json:"sync_interval_ms"`
	FaultSeed      int64        `json:"fault_seed"`
	WebDir         string       `json:"web,omitempty"`
}

// Node is one ConvergeDB replica.
type Node struct {
	cfg   Config
	store *store.Store
	clk   *clock.Clock

	mu    sync.RWMutex // guards world
	world *crdt.World

	faults *FaultController
	events *EventBus

	syncMu   sync.Mutex // one sync exchange per node at a time
	kickCh   chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	startMS int64

	psMu      sync.Mutex
	peerState map[string]PeerStatus
}

// PeerStatus is the last observed sync state of a peer.
type PeerStatus struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	LastSyncMS int64  `json:"last_sync_ms"`
	LastOK     bool   `json:"last_ok"`
	LastError  string `json:"last_error,omitempty"`
	LastPulled int    `json:"last_pulled"`
}

// ErrUnknownDocument is returned when a mutation targets a document this
// replica has not seen. It is a LOCAL view error: after reconciliation the
// document may exist.
var ErrUnknownDocument = errors.New("unknown document (local view)")

// ErrDocumentExists is returned when creating a document that already exists
// locally. Concurrent creation of the same ID on different replicas is not a
// protocol error — the creates merge deterministically.
var ErrDocumentExists = errors.New("document already exists (local view)")

// ErrNotLabMode guards lab-only endpoints.
var ErrNotLabMode = errors.New("lab endpoints require lab mode (start with --lab)")

// Open initializes a replica: opens storage, restores or creates the
// persistent replica identity and HLC watermark, rebuilds the materialized
// view from the durable log and starts the anti-entropy loop.
func Open(cfg Config) (*Node, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("node id is required")
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	// Persistent identity: a database file keeps its replica ID forever.
	// Refusing to run with a different ID prevents two "selves" from
	// forging one actor's history.
	if stored, ok, err := st.GetMeta("replica_id"); err != nil {
		st.Close()
		return nil, err
	} else if ok {
		if stored != cfg.ID {
			st.Close()
			return nil, fmt.Errorf("database %s belongs to replica %q, refusing to start as %q", cfg.DBPath, stored, cfg.ID)
		}
	} else {
		if err := st.SetMeta("replica_id", cfg.ID); err != nil {
			st.Close()
			return nil, err
		}
	}

	// Restore the HLC watermark persisted with the last local append.
	wallS, _, _ := st.GetMeta("hlc_wall")
	cntS, _, _ := st.GetMeta("hlc_counter")
	wall, _ := strconv.ParseInt(wallS, 10, 64)
	cnt, _ := strconv.ParseInt(cntS, 10, 64)
	clk := clock.Restore(cfg.ID, clock.HLC{Wall: wall, Counter: cnt}, nil)

	// Rebuild the materialized view from the durable operation log.
	world := crdt.NewWorld()
	ops, err := st.AllOps()
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("replay operations: %w", err)
	}
	for _, op := range ops {
		if _, err := world.Apply(op); err != nil {
			st.Close()
			return nil, fmt.Errorf("replay op %s: %w", op.OpID, err)
		}
	}

	interval := cfg.SyncIntervalMS
	if interval <= 0 {
		interval = 1000
	}
	if cfg.FaultSeed == 0 {
		cfg.FaultSeed = 1
	}
	n := &Node{
		cfg:       cfg,
		store:     st,
		clk:       clk,
		world:     world,
		faults:    NewFaultController(cfg.FaultSeed),
		events:    NewEventBus(cfg.ID),
		kickCh:    make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		startMS:   time.Now().UnixMilli(),
		peerState: map[string]PeerStatus{},
	}
	for _, p := range cfg.Peers {
		n.peerState[p.ID] = PeerStatus{ID: p.ID, URL: p.URL}
	}
	n.wg.Add(1)
	go n.syncLoop()
	n.events.Emit("lifecycle", map[string]any{"event": "started", "ops_replayed": len(ops)})
	return n, nil
}

// Close stops the sync loop and closes storage.
func (n *Node) Close() error {
	n.stopOnce.Do(func() { close(n.stopCh) })
	n.wg.Wait()
	return n.store.Close()
}

// ID returns the replica identity.
func (n *Node) ID() string { return n.cfg.ID }

// LabMode reports whether lab endpoints are enabled.
func (n *Node) LabMode() bool { return n.cfg.LabMode }

// Events returns the event bus (SSE source).
func (n *Node) Events() *EventBus { return n.events }

// Faults returns the fault controller (lab endpoints).
func (n *Node) Faults() *FaultController { return n.faults }

// nowHLC issues the next local timestamp; must be called with n.mu held OR
// from single-threaded write paths (clock has its own lock).
func (n *Node) nowHLC() clock.HLC { return n.clk.Now() }

// CreateDocument issues a durable doc.create operation.
func (n *Node) CreateDocument(docID, title string) (core.Operation, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.world.Document(docID); exists {
		return core.Operation{}, ErrDocumentExists
	}
	op := core.Operation{
		DocID:   docID,
		Kind:    core.KindDocCreate,
		Actor:   n.cfg.ID,
		Payload: core.Payload{Value: title},
	}
	return n.commitLocal(op)
}

// commitLocal appends, materializes and announces one local operation.
// Caller must hold n.mu (write).
func (n *Node) commitLocal(op core.Operation) (core.Operation, error) {
	issued := n.clk.Now()
	if err := n.store.AppendLocal(&op, issued); err != nil {
		return core.Operation{}, err
	}
	if _, err := n.world.Apply(op); err != nil {
		// The op is durably stored; replay on next start would hit the same
		// deterministic failure. Surface it loudly instead of hiding it.
		n.events.Emit("error", map[string]any{"op_id": op.OpID, "where": "materialize", "detail": err.Error()})
		return op, fmt.Errorf("materialize local op: %w", err)
	}
	n.events.Emit("op.local", map[string]any{
		"op_id": op.OpID, "doc_id": op.DocID, "kind": string(op.Kind),
		"actor": op.Actor, "seq": op.ActorSeq, "hlc_wall": op.HLC.Wall,
	})
	n.kickSync()
	return op, nil
}

// MutationRequest is a typed local mutation on an existing document.
type MutationRequest struct {
	Action string `json:"action"` // set_title|set_status|add_label|remove_label|vote|delete|restore
	Value  string `json:"value,omitempty"`
	Delta  int64  `json:"delta,omitempty"`
}

// Mutate issues a durable typed operation against an existing document.
// The document must exist in the LOCAL view; label removal captures the
// add-tags observed locally at generation time.
func (n *Node) Mutate(docID string, req MutationRequest) (core.Operation, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	doc, exists := n.world.Document(docID)
	if !exists || !doc.Created {
		return core.Operation{}, ErrUnknownDocument
	}
	op := core.Operation{DocID: docID, Actor: n.cfg.ID}
	switch req.Action {
	case "set_title":
		op.Kind, op.Field, op.Payload = core.KindFieldSet, core.FieldTitle, core.Payload{Value: req.Value}
	case "set_status":
		op.Kind, op.Field, op.Payload = core.KindFieldSet, core.FieldStatus, core.Payload{Value: req.Value}
	case "add_label":
		op.Kind, op.Field = core.KindLabelAdd, core.FieldLabels
		op.Payload = core.Payload{Value: req.Value}
	case "remove_label":
		live := doc.Labels.LiveTags(req.Value)
		if len(live) == 0 {
			return core.Operation{}, fmt.Errorf("label %q is not present (local view)", req.Value)
		}
		op.Kind, op.Field = core.KindLabelRemove, core.FieldLabels
		op.Payload = core.Payload{Value: req.Value, Tags: live}
	case "vote":
		op.Kind, op.Field, op.Payload = core.KindVoteDelta, core.FieldVotes, core.Payload{Delta: req.Delta}
	case "delete":
		op.Kind = core.KindDocDelete
	case "restore":
		op.Kind = core.KindDocRestore
	default:
		return core.Operation{}, fmt.Errorf("unknown action %q", req.Action)
	}
	return n.commitLocal(op)
}

// Documents returns deep-copy snapshots of the local materialized view
// (created docs, optionally including deleted tombstoned ones). Snapshots
// are safe to read while the node keeps replicating.
func (n *Node) Documents(includeDeleted bool) []crdt.Document {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var out []crdt.Document
	for _, d := range n.world.Documents() {
		if !d.Created {
			continue
		}
		if d.DeletedFlag() && !includeDeleted {
			continue
		}
		out = append(out, d.Clone())
	}
	return out
}

// Document returns a deep-copy snapshot of one document from the local view.
func (n *Node) Document(id string) (crdt.Document, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	d, ok := n.world.Document(id)
	if !ok || !d.Created {
		return crdt.Document{}, false
	}
	return d.Clone(), true
}

// StateDigest returns the local canonical state digest.
func (n *Node) StateDigest() (string, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.world.Digest()
}

// CanonicalState returns the local canonical JSON.
func (n *Node) CanonicalState() ([]byte, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.world.CanonicalState()
}

// OpDigest returns (count, sorted-op-id digest) from durable storage.
func (n *Node) OpDigest() (int, string, error) { return n.store.OpDigest() }

// Inventory returns the local hole-aware per-actor inventory.
func (n *Node) Inventory() ([]store.ActorInventory, error) {
	return n.store.Inventory(InventoryHoleCap)
}

// Operations lists the durable operation log (bounded, cursor-paginated).
func (n *Node) Operations(limit int, afterOpID string) ([]core.Operation, error) {
	return n.store.ListOps(limit, afterOpID)
}

// IngestOne validates and idempotently stores one remote operation, then
// materializes it. Rejections carry a stable core error code.
func (n *Node) IngestOne(op core.Operation, origin string) store.IngestOutcome {
	if err := core.Validate(op); err != nil {
		if ve, ok := err.(core.ValidationError); ok {
			n.events.Emit("op.rejected", map[string]any{"op_id": op.OpID, "from": origin, "code": ve.Code, "detail": ve.Detail})
			return store.IngestOutcome{Result: store.IngestRejected, Reason: ve.Code, OpID: op.OpID}
		}
		return store.IngestOutcome{Result: store.IngestRejected, Reason: core.CodeSchema, OpID: op.OpID}
	}
	outcome, err := n.store.IngestRemote(op, origin)
	if err != nil || outcome.Result != store.IngestAccepted {
		if outcome.Result == store.IngestRejected {
			n.events.Emit("op.rejected", map[string]any{"op_id": op.OpID, "from": origin, "code": outcome.Reason})
		}
		return outcome
	}
	// Accepted: advance the local clock watermark and materialize.
	n.clk.Observe(op.HLC)
	n.mu.Lock()
	changed, applyErr := n.world.Apply(op)
	n.mu.Unlock()
	if applyErr != nil {
		// Extreme-edge deterministic failure (e.g. int64 counter overflow —
		// needs ~9e9 maximal deltas). The op stays durable; convergence
		// checks will report the truth.
		n.events.Emit("error", map[string]any{"op_id": op.OpID, "where": "materialize_remote", "detail": applyErr.Error()})
	}
	if changed {
		n.events.Emit("op.remote", map[string]any{
			"op_id": op.OpID, "doc_id": op.DocID, "kind": string(op.Kind),
			"actor": op.Actor, "seq": op.ActorSeq, "from": origin,
		})
	}
	return outcome
}

// HLC returns the current local HLC (inspection aid).
func (n *Node) HLC() clock.HLC { return n.clk.Last() }

// UptimeMS returns process uptime in milliseconds.
func (n *Node) UptimeMS() int64 { return time.Now().UnixMilli() - n.startMS }

// DocCount returns the number of visible documents in the local view.
func (n *Node) DocCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	c := 0
	for _, d := range n.world.Documents() {
		if d.Created {
			c++
		}
	}
	return c
}

// Config returns the node's configuration (read-only copy semantics: the
// caller must not mutate slices).
func (n *Node) Config() Config { return n.cfg }

// IdempotencyLookup returns the stored response for a mutation key, if any.
func (n *Node) IdempotencyLookup(key string) (string, bool, error) {
	_, resp, ok, err := n.store.IdempotencyGet(key)
	return resp, ok, err
}

// IdempotencyStore records a mutation response under its key.
func (n *Node) IdempotencyStore(key, opID, response string) error {
	return n.store.IdempotencyPut(key, opID, response)
}

// AddPeer registers (or updates) a replication peer at runtime. Both sides
// of a link learn each other's addresses dynamically, so peers may appear
// after startup.
func (n *Node) AddPeer(p PeerConfig) {
	n.psMu.Lock()
	defer n.psMu.Unlock()
	replaced := false
	for i, existing := range n.cfg.Peers {
		if existing.ID == p.ID {
			n.cfg.Peers[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		n.cfg.Peers = append(n.cfg.Peers, p)
	}
	if ps, ok := n.peerState[p.ID]; ok {
		ps.URL = p.URL
		n.peerState[p.ID] = ps
	} else {
		n.peerState[p.ID] = PeerStatus{ID: p.ID, URL: p.URL}
	}
}

// peersSnapshot returns a copy of the current peer list (safe for the sync
// loop to iterate while peers are added).
func (n *Node) peersSnapshot() []PeerConfig {
	n.psMu.Lock()
	defer n.psMu.Unlock()
	return append([]PeerConfig(nil), n.cfg.Peers...)
}
