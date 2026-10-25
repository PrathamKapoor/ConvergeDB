// Package adversarial contains tests written from the documented external
// protocol and invariants (docs/PROTOCOL.md, docs/CONSISTENCY.md). They
// deliberately avoid reusing production test helpers.
//
// scale_test.go quantifies the cost of the replication read paths at scale:
// startup replay, inventory computation, and a single pull page. These
// measurements are the before/after evidence for scalability fixes; they
// assert loose ceilings only (no invented SLAs).
package adversarial

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/api"
	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/node"
	"github.com/PrathamKapoor/ConvergeDB/internal/store"
)

func seedOps(t *testing.T, st *store.Store, actor string, n int, baseWall int64) {
	t.Helper()
	clk := clock.New(actor, func() int64 { return baseWall })
	start := time.Now()
	for i := 0; i < n; i++ {
		op := &core.Operation{
			DocID:   "scale-doc",
			Kind:    core.KindVoteDelta,
			Field:   core.FieldVotes,
			Actor:   actor,
			Payload: core.Payload{Delta: 1},
		}
		if err := st.AppendLocal(op, clk.Now()); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	t.Logf("seeded %d durable ops in %s (%.0f ops/s)", n, time.Since(start).Round(time.Millisecond), float64(n)/time.Since(start).Seconds())
}

func timeIt(name string, runs int, fn func()) time.Duration {
	best := time.Duration(1 << 62)
	for i := 0; i < runs; i++ {
		t0 := time.Now()
		fn()
		d := time.Since(t0)
		if d < best {
			best = d
		}
	}
	fmt.Printf("SCALE %s: best of %d = %s\n", name, runs, best.Round(time.Microsecond))
	return best
}

// TestScaleReplicationReadPaths measures the per-pull and startup costs that
// determine how often N-operation nodes can serve anti-entropy.
func TestScaleReplicationReadPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("scale harness skipped in -short mode")
	}
	N := 10000
	if v := os.Getenv("SCALE_OPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			N = n
		}
	}
	path := filepath.Join(t.TempDir(), "scale.db")

	// Seed via the real durable path.
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedOps(t, st, "writer", N, 1_700_000_000_000)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Baseline durable-write throughput is measured by the bench command;
	// here we care about read-path costs.

	// Startup replay: open a node on this database (full AllOps + rebuild).
	var openDur time.Duration
	var n *node.Node
	openDur = timeIt("node.Open (full replay of 10k ops)", 1, func() {
		var err error
		n, err = node.Open(node.Config{
			ID: "scale", DBPath: path, HTTPAddr: "127.0.0.1:0",
			SyncIntervalMS: 1 << 28, FaultSeed: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	fmt.Printf("SCALE startup replay per op: %s\n", openDur/time.Duration(N))
	defer n.Close()

	// Inventory computation (runs on EVERY pull request today).
	invDur := timeIt("Inventory() (per pull request)", 5, func() {
		if _, err := n.Inventory(); err != nil {
			t.Fatal(err)
		}
	})

	// One pull page for a requester that has NOTHING (worst case frontier).
	// Measured through a second WAL handle on the same file (the seed handle
	// was closed; the node holds its own).
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pageDur := timeIt("MissingOpsFor page (requester empty)", 5, func() {
		if _, err := st2.MissingOpsFor(nil, 200, ""); err != nil {
			t.Fatal(err)
		}
	})
	st2.Close()

	// End-to-end: one pull HTTP request against a live node.
	srv := httptest.NewServer(api.NewServer(n, ""))
	defer srv.Close()
	httpDur := timeIt("HTTP POST /api/v1/sync/pull (one page)", 5, func() {
		resp, err := httpPost(srv.URL+"/api/v1/sync/pull", node.PullRequest{
			ProtocolVersion: core.ProtocolVersion,
			Requester:       "peer-x",
			Inventory:       []store.ActorInventory{{Actor: "writer", Frontier: 0, MaxSeen: 0, ReceivedAbove: []uint64{}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Page.Ops) != 200 {
			t.Fatalf("page size = %d, want 200", len(resp.Page.Ops))
		}
	})

	// Loose ceilings: these document reality, not SLAs. The O(N) scan makes
	// pull cost grow linearly with log size; assert only that a single page
	// is served within a generous bound so pathological regressions fail.
	if pageDur > 5*time.Second {
		t.Fatalf("single pull page took %s — pathological", pageDur)
	}
	_ = invDur
	_ = httpDur
	// Checkpoint startup: graceful close writes a checkpoint covering the
	// whole log; the next open must restore from it (tail only).
	n.Close()
	var cpDur time.Duration
	cpDur = timeIt("node.Open after checkpoint (tail replay)", 1, func() {
		n2, err := node.Open(node.Config{
			ID: "scale", DBPath: path, HTTPAddr: "127.0.0.1:0",
			SyncIntervalMS: 1 << 28, FaultSeed: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if n2.ReplayMode() != "checkpoint+tail" {
			t.Fatalf("expected checkpoint+tail, got %q", n2.ReplayMode())
		}
		afterDigest, _ := n2.StateDigest()
		_ = afterDigest
		n2.Close()
	})
	fmt.Printf("SCALE startup speedup vs full replay: %vx\n", float64(openDur)/float64(cpDur))

	// Record everything into the test log for the BENCHMARKS doc.
	t.Logf("SUMMARY n=%d open=%s checkpoint_open=%s inventory=%s page=%s http=%s", N, openDur.Round(time.Millisecond), cpDur.Round(time.Millisecond), invDur.Round(time.Millisecond), pageDur.Round(time.Millisecond), httpDur.Round(time.Millisecond))
}

func httpPost(url string, body any) (*node.PullResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := httpPostRaw(url, raw)
	if err != nil {
		return nil, err
	}
	var pr node.PullResponse
	if err := json.Unmarshal(resp, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}
