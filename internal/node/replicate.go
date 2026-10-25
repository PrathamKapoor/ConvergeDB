package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/store"
)

// Sync protocol constants (see docs/PROTOCOL.md).
const (
	MaxPageOps    = 200
	MaxPullPages  = 200 // per exchange; resumable via cursor afterwards
	MaxBodyBytes  = 4 << 20
	SyncTimeout   = 10 * time.Second
	MaxSyncEvents = 1000
)

// SyncSummary reports one pull exchange against one peer.
type SyncSummary struct {
	Peer       string `json:"peer"`
	Pages      int    `json:"pages"`
	Pulled     int    `json:"pulled"`
	Accepted   int    `json:"accepted"`
	Duplicates int    `json:"duplicates"`
	Rejected   int    `json:"rejected"`
	DurationMS int64  `json:"duration_ms"`
	Complete   bool   `json:"complete"` // false if page budget ran out (still resumable)
	Simulated  bool   `json:"simulated"`
	Note       string `json:"note,omitempty"`
}

var ErrPartitioned = errors.New("sync link partitioned (lab fault)")

// PullRequest is the body of POST /api/v1/sync/pull.
type PullRequest struct {
	ProtocolVersion int                    `json:"protocol_version"`
	Requester       string                 `json:"requester"`
	Inventory       []store.ActorInventory `json:"inventory"`
	Cursor          string                 `json:"cursor,omitempty"`
	Limit           int                    `json:"limit,omitempty"`
}

// PullResponse is the reply of POST /api/v1/sync/pull.
type PullResponse struct {
	ProtocolVersion int        `json:"protocol_version"`
	Responder       string     `json:"responder"`
	Page            store.Page `json:"page"`
}

// PushRequest is the body of POST /api/v1/sync/push.
type PushRequest struct {
	ProtocolVersion int              `json:"protocol_version"`
	Sender          string           `json:"sender"`
	Ops             []core.Operation `json:"ops"`
}

// PushResponse reports per-op ingest outcomes.
type PushResponse struct {
	ProtocolVersion int                   `json:"protocol_version"`
	Responder       string                `json:"responder"`
	Outcomes        []store.IngestOutcome `json:"outcomes"`
}

// ServePull handles POST /api/v1/sync/pull.
func (n *Node) ServePull(w http.ResponseWriter, r *http.Request) {
	var req PullRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, core.CodeSchema, "malformed pull request")
		return
	}
	if req.ProtocolVersion != core.ProtocolVersion {
		httpError(w, http.StatusConflict, core.CodeProtocol, fmt.Sprintf("protocol %d not supported (want %d)", req.ProtocolVersion, core.ProtocolVersion))
		return
	}
	if req.Requester == "" || req.Requester == n.cfg.ID {
		httpError(w, http.StatusBadRequest, core.CodeID, "requester must be a non-empty identity distinct from responder")
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > MaxPageOps {
		limit = MaxPageOps
	}
	page, err := n.store.MissingOpsFor(req.Inventory, limit, req.Cursor)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "E_STORE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, PullResponse{
		ProtocolVersion: core.ProtocolVersion,
		Responder:       n.cfg.ID,
		Page:            page,
	})
}

// ServePush handles POST /api/v1/sync/push.
func (n *Node) ServePush(w http.ResponseWriter, r *http.Request) {
	var req PushRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, core.CodeSchema, "malformed push request")
		return
	}
	if req.ProtocolVersion != core.ProtocolVersion {
		httpError(w, http.StatusConflict, core.CodeProtocol, fmt.Sprintf("protocol %d not supported (want %d)", req.ProtocolVersion, core.ProtocolVersion))
		return
	}
	if len(req.Ops) > MaxPageOps {
		httpError(w, http.StatusRequestEntityTooLarge, core.CodeLimit, fmt.Sprintf("push carries %d ops (max %d per request)", len(req.Ops), MaxPageOps))
		return
	}
	outcomes := make([]store.IngestOutcome, 0, len(req.Ops))
	for _, op := range req.Ops {
		outcomes = append(outcomes, n.IngestOne(op, req.Sender))
	}
	writeJSON(w, http.StatusOK, PushResponse{ProtocolVersion: core.ProtocolVersion, Responder: n.cfg.ID, Outcomes: outcomes})
}

// ServeInventory handles GET /api/v1/sync/inventory.
func (n *Node) ServeInventory(w http.ResponseWriter, r *http.Request) {
	inv, err := n.Inventory()
	if err != nil {
		httpError(w, http.StatusInternalServerError, "E_STORE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"protocol_version": core.ProtocolVersion,
		"node":             n.cfg.ID,
		"inventory":        inv,
	})
}

// kickSync nudges the sync loop without blocking the write path.
func (n *Node) kickSync() {
	select {
	case n.kickCh <- struct{}{}:
	default:
	}
}

func (n *Node) syncLoop() {
	defer n.wg.Done()
	interval := n.cfg.SyncIntervalMS
	if interval <= 0 {
		interval = 1000
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-n.stopCh:
			return
		case <-ticker.C:
		case <-n.kickCh:
		}
		n.SyncRound()
	}
}

// SyncRound pulls once from every configured peer. Errors are recorded per
// peer and emitted as events; a failing peer never blocks the others.
func (n *Node) SyncRound() {
	n.syncMu.Lock()
	defer n.syncMu.Unlock()
	for _, p := range n.peersSnapshot() {
		select {
		case <-n.stopCh:
			return
		default:
		}
		sum := n.PullFromPeer(p)
		ps := PeerStatus{ID: p.ID, URL: p.URL, LastSyncMS: time.Now().UnixMilli()}
		switch {
		case sum == nil:
			ps.LastOK = false
			ps.LastError = "no result"
		case sum.Note == ErrPartitioned.Error():
			ps.LastOK = false
			ps.LastError = ErrPartitioned.Error()
		case sum.Pages == 0:
			ps.LastOK = false
			ps.LastError = sum.Note
		default:
			ps.LastOK = true
			ps.LastError = sum.Note // empty or an informational note
			ps.LastPulled = sum.Pulled
		}
		n.psMu.Lock()
		n.peerState[p.ID] = ps
		n.psMu.Unlock()
	}
}

// PeerStatuses returns the last observed per-peer sync state.
func (n *Node) PeerStatuses() []PeerStatus {
	n.psMu.Lock()
	defer n.psMu.Unlock()
	out := make([]PeerStatus, 0, len(n.peerState))
	for _, p := range n.cfg.Peers {
		ps := n.peerState[p.ID]
		ps.ID, ps.URL = p.ID, p.URL
		out = append(out, ps)
	}
	return out
}

// PullFromPeer runs one bounded, fault-gated, cursor-resumable pull
// exchange with a peer. It is the ONLY place network faults are applied;
// every allowed exchange is a real HTTP round trip.
func (n *Node) PullFromPeer(p PeerConfig) *SyncSummary {
	start := time.Now()
	gate := n.faults.Gate(n.cfg.ID, p.ID)
	if !gate.Allowed {
		n.events.Emit("sync.blocked", map[string]any{"peer": p.ID, "reason": "partitioned"})
		return &SyncSummary{Peer: p.ID, Note: ErrPartitioned.Error(), DurationMS: 0, Complete: false}
	}
	maybeDelay(gate.DelayMS)

	sum := &SyncSummary{Peer: p.ID}
	client := &http.Client{Timeout: SyncTimeout}
	inv, err := n.Inventory()
	if err != nil {
		sum.Note = err.Error()
		n.events.Emit("sync.fail", map[string]any{"peer": p.ID, "error": err.Error()})
		return sum
	}
	cursor := ""
	for page := 0; page < MaxPullPages; page++ {
		reqBody := PullRequest{
			ProtocolVersion: core.ProtocolVersion,
			Requester:       n.cfg.ID,
			Inventory:       inv,
			Cursor:          cursor,
			Limit:           MaxPageOps,
		}
		resp, err := n.postPull(client, p, reqBody)
		if err != nil {
			sum.Note = err.Error()
			n.events.Emit("sync.fail", map[string]any{"peer": p.ID, "error": err.Error()})
			return sum
		}
		// Re-check the gate AFTER the response: a partition applied while a
		// request was in flight must not let that response through, or lab
		// partitions would leak by one in-flight exchange.
		if post := n.faults.Gate(n.cfg.ID, p.ID); !post.Allowed {
			n.events.Emit("sync.blocked", map[string]any{"peer": p.ID, "reason": "partitioned (in-flight response discarded)"})
			sum.Note = ErrPartitioned.Error()
			return sum
		}
		sum.Pages++
		ops := resp.Page.Ops
		if gate.Reorder && len(ops) > 1 {
			// Reorder fault: deliver this page's ops reversed.
			for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
				ops[i], ops[j] = ops[j], ops[i]
			}
			sum.Simulated = true
		}
		apply := func(ops []core.Operation) {
			for _, op := range ops {
				out := n.IngestOne(op, p.ID)
				sum.Pulled++
				switch out.Result {
				case store.IngestAccepted:
					sum.Accepted++
				case store.IngestDuplicate:
					sum.Duplicates++
				default:
					sum.Rejected++
				}
			}
		}
		apply(ops)
		if gate.Duplicate {
			// Duplicate fault: the page is delivered twice; dedup must
			// make the second pass a no-op.
			apply(ops)
			sum.Simulated = true
		}
		if !resp.Page.HasMore {
			sum.Complete = true
			break
		}
		cursor = resp.Page.NextCursor
		if cursor == "" {
			// Defensive: responder violated the protocol (has_more without
			// a cursor). Treat as complete to avoid an infinite loop, but
			// flag it.
			sum.Note = "responder set has_more without next_cursor"
			sum.Complete = true
			break
		}
	}
	if !sum.Complete {
		sum.Note = "page budget exhausted; next round resumes from cursor state"
	}
	sum.DurationMS = time.Since(start).Milliseconds()
	n.events.Emit("sync.ok", map[string]any{
		"peer": p.ID, "pulled": sum.Pulled, "accepted": sum.Accepted,
		"duplicates": sum.Duplicates, "rejected": sum.Rejected, "pages": sum.Pages,
		"duration_ms": sum.DurationMS, "faulted": sum.Simulated,
	})
	return sum
}

func (n *Node) postPull(client *http.Client, p PeerConfig, body PullRequest) (*PullResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(p.URL, "/")+"/api/v1/sync/pull", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := n.cfg.AuthToken; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	} else if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("pull: peer replied %d: %s", resp.StatusCode, truncate(string(data), 256))
	}
	var pr PullResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxBodyBytes)).Decode(&pr); err != nil {
		return nil, fmt.Errorf("pull: malformed peer reply: %w", err)
	}
	if pr.ProtocolVersion != core.ProtocolVersion {
		return nil, fmt.Errorf("pull: peer protocol %d not supported", pr.ProtocolVersion)
	}
	if len(pr.Page.Ops) > MaxPageOps {
		return nil, fmt.Errorf("pull: peer sent oversized page (%d ops)", len(pr.Page.Ops))
	}
	return &pr, nil
}

// PushToPeer sends ops the peer is missing (per the peer's inventory) —
// the low-latency path; the pull loop remains the correctness backstop.
func (n *Node) PushToPeer(p PeerConfig, ops []core.Operation) ([]store.IngestOutcome, error) {
	gate := n.faults.Gate(n.cfg.ID, p.ID)
	if !gate.Allowed {
		return nil, ErrPartitioned
	}
	maybeDelay(gate.DelayMS)
	raw, err := json.Marshal(PushRequest{ProtocolVersion: core.ProtocolVersion, Sender: n.cfg.ID, Ops: ops})
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: SyncTimeout}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(p.URL, "/")+"/api/v1/sync/push", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := n.cfg.AuthToken; tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	} else if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("push: peer replied %d", resp.StatusCode)
	}
	var pr PushResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxBodyBytes)).Decode(&pr); err != nil {
		return nil, fmt.Errorf("push: malformed peer reply: %w", err)
	}
	return pr.Outcomes, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// writeJSON and httpError are shared by the node's protocol handlers and
// the API package.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func httpError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "detail": detail},
	})
}
