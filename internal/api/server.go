// Package api exposes ConvergeDB's versioned HTTP surface: health, node
// info, document operations, the sync protocol, lab fault injection and the
// SSE event stream. All bodies are bounded; all rejections are stable JSON
// errors; unsafe endpoints require either lab mode or a configured bearer
// token.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/crdt"
	"github.com/PrathamKapoor/ConvergeDB/internal/node"
)

// Server serves one node's HTTP API.
type Server struct {
	n        *node.Node
	webDir   string
	sseCount atomic.Int64
	maxSSE   int64

	pullSem chan struct{} // bounds concurrent pull executions (store queries)
}

// maxConcurrentPulls bounds how many sync-pull requests execute store
// queries at once; excess requests wait, so a flood of peer pulls cannot
// monopolize SQLite.
const maxConcurrentPulls = 8

// NewServer builds the HTTP handler for a node.
func NewServer(n *node.Node, webDir string) http.Handler {
	return (&Server{n: n, webDir: webDir, maxSSE: 64, pullSem: make(chan struct{}, maxConcurrentPulls)}).routes()
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.handleLive)
	mux.HandleFunc("GET /health/ready", s.handleReady)
	mux.HandleFunc("GET /api/v1/node", s.handleNodeInfo)
	mux.HandleFunc("POST /api/v1/documents", s.handleCreateDocument)
	mux.HandleFunc("GET /api/v1/documents", s.handleListDocuments)
	mux.HandleFunc("GET /api/v1/documents/{id}", s.handleGetDocument)
	mux.HandleFunc("POST /api/v1/documents/{id}/operations", s.handleMutate)
	mux.HandleFunc("GET /api/v1/operations", s.handleOperations)
	mux.HandleFunc("GET /api/v1/sync/inventory", s.handleInventory)
	mux.HandleFunc("POST /api/v1/sync/pull", s.handlePull)
	mux.HandleFunc("POST /api/v1/sync/push", s.handlePush)
	mux.HandleFunc("POST /api/v1/lab/partition", s.handlePartition)
	mux.HandleFunc("POST /api/v1/lab/heal", s.handleHeal)
	mux.HandleFunc("GET /api/v1/lab/faults", s.handleGetFaults)
	mux.HandleFunc("POST /api/v1/lab/faults", s.handleSetFaults)
	mux.HandleFunc("GET /api/v1/lab/convergence", s.handleConvergence)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	if s.webDir != "" {
		mux.Handle("/", s.spaHandler())
	}
	return s.middleware(mux)
}

// middleware: panic recovery, auth, CORS, request-id, body limits.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = fmt.Sprintf("req-%d", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-ID", reqID)
		defer func() {
			if rec := recover(); rec != nil {
				writeErr(w, http.StatusInternalServerError, "E_INTERNAL", fmt.Sprintf("internal error (request %s)", reqID))
			}
		}()
		if !s.corsAllowed(r, w) {
			writeErr(w, http.StatusForbidden, "E_CORS", "origin not allowed")
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.authorized(r) {
			writeErr(w, http.StatusUnauthorized, "E_AUTH", "valid bearer token required (node started with an auth token)")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, node.MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// corsAllowed allows same-origin and localhost/127.0.0.1 origins (the lab
// UI and CLI tools). Cross-host origins are refused.
func (s *Server) corsAllowed(r *http.Request, w http.ResponseWriter) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u := origin
	if strings.HasPrefix(u, "http://") {
		u = strings.TrimPrefix(u, "http://")
	} else if strings.HasPrefix(u, "https://") {
		u = strings.TrimPrefix(u, "https://")
	} else {
		return false
	}
	host := u
	if i := strings.LastIndex(u, ":"); i >= 0 {
		host = u[:i]
	}
	if host == "localhost" || host == "127.0.0.1" || host == "[::1]" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Request-ID")
		w.Header().Set("Vary", "Origin")
		return true
	}
	return false
}

// authorized: when the node runs with an auth token, everything except the
// two health probes requires a matching bearer. Without a token the node is
// in trusted-local mode (binds to loopback only).
func (s *Server) authorized(r *http.Request) bool {
	tok := s.n.Config().AuthToken
	if tok == "" {
		return true
	}
	if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
		return true
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	return len(auth) > len(prefix) && auth[:len(prefix)] == prefix && auth[len(prefix):] == tok
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "live", "node": s.n.ID()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	// Ready = storage answers; liveness is never a sync claim.
	if _, _, err := s.n.OpDigest(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "E_STORE", "storage not answering: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "node": s.n.ID()})
}

func (s *Server) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.n.Info(false))
}

func (s *Server) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := decodeInto(w, r, &req); err != nil {
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("doc-%s-%d", s.n.ID(), time.Now().UnixNano())
	}
	if err := core.CheckCreatePayload(req.ID, req.Title); err != nil {
		writeErr(w, http.StatusBadRequest, "E_SCHEMA", err.Error())
		return
	}
	op, err := s.n.CreateDocument(req.ID, req.Title)
	if err != nil {
		writeErr(w, http.StatusConflict, "E_EXISTS", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"operation": op, "document": s.docView(req.ID)})
}

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	includeDeleted := r.URL.Query().Get("include_deleted") == "1"
	limit := parseIntDefault(r.URL.Query().Get("limit"), 200)
	if limit > 1000 {
		limit = 1000 // bound: listing must not clone every document per request
	}
	docs := s.n.Documents(includeDeleted)
	if len(docs) > limit {
		docs = docs[:limit]
	}
	views := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		views = append(views, docViewOf(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"documents": views, "node": s.n.ID(), "truncated": len(views) == limit})
}

func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view := s.docView(id)
	if view == nil {
		writeErr(w, http.StatusNotFound, "E_NOT_FOUND", "document not found in local view")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"document": view, "node": s.n.ID()})
}

// docView builds the API view of one document including merge metadata.
func (s *Server) docView(id string) map[string]any {
	d, ok := s.n.Document(id)
	if !ok {
		return nil
	}
	return docViewOf(d)
}

func docViewOf(d crdt.Document) map[string]any {
	labels := d.Labels.Values()
	labelTags := map[string][]string{}
	for _, l := range labels {
		labelTags[l] = d.Labels.LiveTags(l)
	}
	return map[string]any{
		"id":         d.ID,
		"created":    d.Created,
		"deleted":    d.DeletedFlag(),
		"title":      registerView(d.Title),
		"status":     registerView(d.Status),
		"labels":     labels,
		"label_tags": labelTags,
		"votes":      d.Votes.Value(),
		"vote_ops":   d.Votes.Len(),
	}
}

func registerView(r crdt.LWWRegister) map[string]any {
	return map[string]any{
		"value": r.Value,
		"set":   r.Set(),
		"hlc":   r.H,
		"op_id": r.OpID,
	}
}

func (s *Server) handleMutate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req node.MutationRequest
	if err := decodeInto(w, r, &req); err != nil {
		return
	}
	// Idempotency-Key: a retried request replays the original response
	// (durable in SQLite, survives node restarts).
	key := r.Header.Get("Idempotency-Key")
	if key != "" {
		if len(key) > 128 {
			writeErr(w, http.StatusBadRequest, "E_LIMIT", "Idempotency-Key exceeds 128 chars")
			return
		}
		if resp, ok, err := s.n.IdempotencyLookup(key); err == nil && ok {
			w.Header().Set("Idempotency-Replayed", "true")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
			return
		}
	}
	op, err := s.n.Mutate(id, req)
	if err != nil {
		s.mutationError(w, err)
		return
	}
	body, _ := json.Marshal(map[string]any{"operation": op, "document": s.docView(id)})
	if key != "" {
		_ = s.n.IdempotencyStore(key, op.OpID, string(body))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) mutationError(w http.ResponseWriter, err error) {
	switch {
	case err == node.ErrUnknownDocument:
		writeErr(w, http.StatusNotFound, "E_NOT_FOUND", err.Error())
	default:
		writeErr(w, http.StatusBadRequest, "E_SCHEMA", err.Error())
	}
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
	if limit > 1000 {
		limit = 1000 // bound: a huge limit must not materialize the whole log
	}
	after := r.URL.Query().Get("cursor")
	ops, err := s.n.Operations(limit, after)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "E_STORE", err.Error())
		return
	}
	next := ""
	if len(ops) == limit {
		next = ops[len(ops)-1].OpID
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": ops, "next_cursor": next, "node": s.n.ID()})
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	s.n.ServeInventory(w, r)
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	select {
	case s.pullSem <- struct{}{}:
		defer func() { <-s.pullSem }()
	case <-r.Context().Done():
		return
	}
	s.n.ServePull(w, r)
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	s.n.ServePush(w, r)
}

// --- lab endpoints (only meaningful with --lab) ---

func (s *Server) labGuard(w http.ResponseWriter) bool {
	if !s.n.LabMode() {
		writeErr(w, http.StatusForbidden, "E_LAB", "lab endpoints are disabled; start the node with --lab to enable fault injection")
		return false
	}
	return true
}

func (s *Server) handlePartition(w http.ResponseWriter, r *http.Request) {
	if !s.labGuard(w) {
		return
	}
	var req node.PartitionRequest
	if err := decodeInto(w, r, &req); err != nil {
		return
	}
	if req.A == "" || req.B == "" || req.A == req.B {
		writeErr(w, http.StatusBadRequest, "E_SCHEMA", "partition needs two distinct node ids")
		return
	}
	s.n.Faults().Partition(req.A, req.B)
	s.n.Events().Emit("fault.changed", map[string]any{"op": "partition", "a": req.A, "b": req.B})
	writeJSON(w, http.StatusOK, map[string]any{"partitioned": []string{req.A, req.B}, "faults": s.n.Faults().Snapshot()})
}

func (s *Server) handleHeal(w http.ResponseWriter, r *http.Request) {
	if !s.labGuard(w) {
		return
	}
	var req node.PartitionRequest
	_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req) // optional body
	if req.A != "" && req.B != "" {
		s.n.Faults().HealLink(req.A, req.B)
		s.n.Faults().HealLink(req.B, req.A)
		s.n.Events().Emit("fault.changed", map[string]any{"op": "heal_link", "a": req.A, "b": req.B})
	} else {
		s.n.Faults().HealAll()
		s.n.Events().Emit("fault.changed", map[string]any{"op": "heal_all"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"faults": s.n.Faults().Snapshot()})
}

func (s *Server) handleGetFaults(w http.ResponseWriter, r *http.Request) {
	if !s.labGuard(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"faults": s.n.Faults().Snapshot(), "version": s.n.Faults().Version()})
}

func (s *Server) handleSetFaults(w http.ResponseWriter, r *http.Request) {
	if !s.labGuard(w) {
		return
	}
	var req node.FaultRequest
	if err := decodeInto(w, r, &req); err != nil {
		return
	}
	if req.From == "" || req.To == "" || req.From == req.To {
		writeErr(w, http.StatusBadRequest, "E_SCHEMA", "faults need two distinct node ids (from/to)")
		return
	}
	if req.DropRate < 0 || req.DropRate > 1 || req.DuplicateRate < 0 || req.DuplicateRate > 1 {
		writeErr(w, http.StatusBadRequest, "E_SCHEMA", "rates must be within [0,1]")
		return
	}
	if req.DelayMS < 0 || req.DelayMS > 5000 {
		writeErr(w, http.StatusBadRequest, "E_SCHEMA", "delay_ms must be within [0,5000]")
		return
	}
	s.n.Faults().SetLink(req.From, req.To, node.LinkFault{
		Partitioned: req.Partitioned, DelayMS: req.DelayMS,
		DropRate: req.DropRate, DuplicateRate: req.DuplicateRate, Reorder: req.Reorder,
	})
	s.n.Events().Emit("fault.changed", map[string]any{"op": "set", "from": req.From, "to": req.To, "fault": req})
	writeJSON(w, http.StatusOK, map[string]any{"faults": s.n.Faults().Snapshot()})
}

func (s *Server) handleConvergence(w http.ResponseWriter, r *http.Request) {
	rep := s.n.Convergence()
	writeJSON(w, http.StatusOK, rep)
}

// --- SSE events ---

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.sseCount.Load() >= s.maxSSE {
		writeErr(w, http.StatusServiceUnavailable, "E_LIMIT", "too many event stream subscribers")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "E_INTERNAL", "streaming unsupported")
		return
	}
	s.sseCount.Add(1)
	defer s.sseCount.Add(-1)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	events, stop := s.n.Events().Subscribe()
	defer stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			raw, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, raw)
			flusher.Flush()
		}
	}
}

// --- static SPA ---

func (s *Server) spaHandler() http.Handler {
	fs := http.FileServer(http.Dir(s.webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := s.webDir + r.URL.Path
		if r.URL.Path == "/" || !fileExists(path) {
			http.ServeFile(w, r, s.webDir+"/index.html")
			return
		}
		fs.ServeHTTP(w, r)
	})
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// --- helpers ---

func decodeInto(w http.ResponseWriter, r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "E_LIMIT",
				fmt.Sprintf("request body exceeds %d bytes", maxErr.Limit))
			return err
		}
		writeErr(w, http.StatusBadRequest, "E_SCHEMA", "malformed JSON body: "+err.Error())
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "detail": detail}})
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
