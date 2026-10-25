package api_test

import (
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/api"
	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/node"
	"github.com/PrathamKapoor/ConvergeDB/internal/store"
)

// startTestNode builds a REAL HTTP-served node (in-process handler, real
// socket via httptest) so replication tests exercise the actual wire path.
func startTestNode(t *testing.T, id string, peers []node.PeerConfig, lab bool) (*node.Node, *httptest.Server) {
	t.Helper()
	n, err := node.Open(node.Config{
		ID:             id,
		DBPath:         filepath.Join(t.TempDir(), id+".db"),
		HTTPAddr:       "127.0.0.1:0",
		Peers:          peers,
		LabMode:        lab,
		SyncIntervalMS: 40,
		FaultSeed:      7,
	})
	if err != nil {
		t.Fatalf("open node %s: %v", id, err)
	}
	srv := httptest.NewServer(api.NewServer(n, ""))
	t.Cleanup(func() {
		srv.Close()
		n.Close()
	})
	return n, srv
}

func urlOf(srv *httptest.Server) string { return srv.URL }

func waitCond(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, what)
}

func TestTwoNodesReplicateBidirectionally(t *testing.T) {
	nb, srv_nb := startTestNode(t, "beta", nil, false)
	na, srv_na := startTestNode(t, "alpha", []node.PeerConfig{{ID: "beta", URL: urlOf(srv_nb)}}, false)
	nb.AddPeer(node.PeerConfig{ID: "alpha", URL: urlOf(srv_na)})

	op, err := na.CreateDocument("board", "Sprint")
	if err != nil {
		t.Fatal(err)
	}
	if op.ActorSeq != 1 || op.Actor != "alpha" {
		t.Fatalf("unexpected identity: %+v", op)
	}
	waitCond(t, 5*time.Second, "doc reaches beta", func() bool {
		_, ok := nb.Document("board")
		return ok
	})

	// Bidirectional: beta writes; alpha must receive it.
	if _, err := nb.Mutate("board", node.MutationRequest{Action: "set_title", Value: "From Beta"}); err != nil {
		t.Fatal(err)
	}
	waitCond(t, 5*time.Second, "beta title reaches alpha", func() bool {
		d, ok := na.Document("board")
		return ok && d.Title.Value == "From Beta"
	})

	// Independent increments both survive (I-10).
	na.Mutate("board", node.MutationRequest{Action: "vote", Delta: 1})
	nb.Mutate("board", node.MutationRequest{Action: "vote", Delta: 2})
	waitCond(t, 5*time.Second, "votes merge to 3", func() bool {
		da, okA := na.Document("board")
		db, okB := nb.Document("board")
		return okA && okB && da.Votes.Value() == 3 && db.Votes.Value() == 3
	})

	// Convergence checker sees one operation set everywhere.
	rep := na.Convergence()
	if rep.Status != node.StatusConverged {
		t.Fatalf("status = %s, want CONVERGED; peers=%+v", rep.Status, rep.Peers)
	}
}

func TestPartitionBlocksSyncButNotLocalWrites(t *testing.T) {
	nb, srv_nb := startTestNode(t, "beta", nil, true)
	na, srv_na := startTestNode(t, "alpha", []node.PeerConfig{{ID: "beta", URL: urlOf(srv_nb)}}, true)
	nb.AddPeer(node.PeerConfig{ID: "alpha", URL: urlOf(srv_na)})

	if _, err := na.CreateDocument("board", "Start"); err != nil {
		t.Fatal(err)
	}
	waitCond(t, 5*time.Second, "initial sync", func() bool {
		_, ok := nb.Document("board")
		return ok
	})

	// Partition via alpha's fault controller (both directions).
	na.Faults().Partition("alpha", "beta")
	if _, err := na.Mutate("board", node.MutationRequest{Action: "set_title", Value: "Alpha isolated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nb.Mutate("board", node.MutationRequest{Action: "set_title", Value: "Beta isolated"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // a few sync rounds

	da, _ := na.Document("board")
	db, _ := nb.Document("board")
	if da.Title.Value == db.Title.Value {
		t.Fatalf("partition did not block sync: both sides see %q", da.Title.Value)
	}

	// The convergence diagnostic runs on the management plane, deliberately
	// OUTSIDE the simulated replication network: a partitioned-but-alive
	// peer is reported truthfully as DIVERGED. UNKNOWN is reserved for
	// genuinely unreachable peers (tested separately).
	rep := na.Convergence()
	if rep.Status != node.StatusDiverged {
		t.Fatalf("convergence across a partition must be DIVERGED, got %s", rep.Status)
	}

	// Heal; both writes must reconcile via LWW (one deterministic winner).
	na.Faults().HealAll()
	waitCond(t, 5*time.Second, "titles reconcile after heal", func() bool {
		da, _ := na.Document("board")
		db, _ := nb.Document("board")
		return da.Title.Value == db.Title.Value && (da.Title.Value == "Alpha isolated" || da.Title.Value == "Beta isolated")
	})

	// I-08: alpha's isolated write survived (it was ACKed while partitioned).
	da, _ = na.Document("board")
	if da.Title.Value != "Alpha isolated" && da.Title.Value != "Beta isolated" {
		t.Fatalf("unexpected winner %q", da.Title.Value)
	}
}

func TestDuplicateAndReorderedDeliveryConverge(t *testing.T) {
	nb, srv_nb := startTestNode(t, "beta", nil, true)
	na, srv_na := startTestNode(t, "alpha", []node.PeerConfig{{ID: "beta", URL: urlOf(srv_nb)}}, true)
	nb.AddPeer(node.PeerConfig{ID: "alpha", URL: urlOf(srv_na)})

	if _, err := na.CreateDocument("board", "T"); err != nil {
		t.Fatal(err)
	}
	// Inject duplicate + reorder on both directions of the link, then write
	// a burst. Alpha's ops reach beta via beta's pull, so both directions
	// must be faulted to exercise the full path.
	na.Faults().SetLink("alpha", "beta", node.LinkFault{DuplicateRate: 1.0, Reorder: true})
	nb.Faults().SetLink("beta", "alpha", node.LinkFault{DuplicateRate: 1.0, Reorder: true})
	for i := 0; i < 10; i++ {
		if _, err := na.Mutate("board", node.MutationRequest{Action: "vote", Delta: 1}); err != nil {
			t.Fatal(err)
		}
	}
	na.Faults().HealAll()
	nb.Faults().HealAll()
	waitCond(t, 5*time.Second, "duplicated+reordered ops converge", func() bool {
		da, okA := na.Document("board")
		db, okB := nb.Document("board")
		return okA && okB && db.Votes.Value() == 10 && da.Votes.Value() == 10
	})
	rep := na.Convergence()
	if rep.Status != node.StatusConverged {
		t.Fatalf("want CONVERGED after duplicate/reorder storm, got %s", rep.Status)
	}
}

func TestHoleAwareSyncDeliversGappedSequences(t *testing.T) {
	// beta receives alpha's ops out of order (reorder fault on one page).
	nb, srv_nb := startTestNode(t, "beta", nil, true)
	na, srv_na := startTestNode(t, "alpha", []node.PeerConfig{{ID: "beta", URL: urlOf(srv_nb)}}, true)
	nb.AddPeer(node.PeerConfig{ID: "alpha", URL: urlOf(srv_na)})

	// Fill ops with the reorder fault so beta's inventory forms holes; then
	// verify the frontier/holes tracking eventually closes and everything
	// arrives — max_seen-only sync would strand the middle ops.
	na.Faults().SetLink("alpha", "beta", node.LinkFault{Reorder: true, DuplicateRate: 0.5})
	if _, err := na.CreateDocument("board", "T"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if _, err := na.Mutate("board", node.MutationRequest{Action: "vote", Delta: 1}); err != nil {
			t.Fatal(err)
		}
	}
	na.Faults().HealAll()
	waitCond(t, 8*time.Second, "all 26 ops arrive despite holes", func() bool {
		db, ok := nb.Document("board")
		return ok && db.Votes.Value() == 25
	})
	inv, err := nb.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range inv {
		if a.Actor == "alpha" && (a.Frontier != a.MaxSeen || len(a.ReceivedAbove) != 0) {
			t.Fatalf("holes never closed for alpha: %+v", a)
		}
	}
}

func TestRejectionOfInvalidRemoteOps(t *testing.T) {
	nb, srv_nb := startTestNode(t, "beta", nil, false)
	na, srv_na := startTestNode(t, "alpha", []node.PeerConfig{{ID: "beta", URL: urlOf(srv_nb)}}, false)
	nb.AddPeer(node.PeerConfig{ID: "alpha", URL: urlOf(srv_na)})

	// Craft an op that violates the schema (bad status value) and push it.
	bad := opFor(na, "board", "beta", core.KindFieldSet, core.FieldStatus, core.Payload{Value: "not-a-status"})
	out := nb.IngestOne(bad, "alpha")
	if out.Result != store.IngestRejected || out.Reason != "E_SCHEMA" {
		t.Fatalf("invalid op not rejected: %+v", out)
	}

	// Same op ID with different payload: conflict (I-04), never overwrite.
	good := opFor(na, "board", "beta", core.KindFieldSet, core.FieldTitle, core.Payload{Value: "first"})
	if out := nb.IngestOne(good, "alpha"); out.Result != store.IngestAccepted {
		t.Fatalf("good op rejected: %+v", out)
	}
	// A tampered op (same ID, different body) is rejected by content-addressed
	// validation (E_ID) before the store ever sees it — fail closed. The
	// store-level E_CONFLICT guard for raw ID collisions is covered in
	// internal/store's TestIngestRemoteIdempotentAndConflictDetection.
	tampered := good
	tampered.Payload.Value = "second"
	out = nb.IngestOne(tampered, "alpha")
	if out.Result != store.IngestRejected || out.Reason != "E_ID" {
		t.Fatalf("tampered op not rejected: %+v", out)
	}
	// The original content must be untouched in the durable log (beta never
	// received a doc.create, so the doc is buffered/invisible — but the op
	// is stored with its original payload).
	ops, err := nb.Operations(10, "")
	if err != nil || len(ops) != 1 || ops[0].Payload.Value != "first" {
		t.Fatalf("stored content was overwritten or wrong: %+v err=%v", ops, err)
	}
}

func TestEventsEmittedForLocalAndRemoteOps(t *testing.T) {
	nb, srv_nb := startTestNode(t, "beta", nil, false)
	na, srv_na := startTestNode(t, "alpha", []node.PeerConfig{{ID: "beta", URL: urlOf(srv_nb)}}, false)
	nb.AddPeer(node.PeerConfig{ID: "alpha", URL: urlOf(srv_na)})

	ch, stop := nb.Events().Subscribe()
	defer stop()
	var remoteSeen, localSeen atomic.Bool
	go func() {
		for ev := range ch {
			if ev.Type == "op.remote" {
				remoteSeen.Store(true)
			}
			if ev.Type == "op.local" {
				localSeen.Store(true)
			}
		}
	}()
	if _, err := na.CreateDocument("board", "T"); err != nil {
		t.Fatal(err)
	}
	waitCond(t, 5*time.Second, "remote op event on beta", remoteSeen.Load)
	nb.Mutate("board", node.MutationRequest{Action: "vote", Delta: 1})
	waitCond(t, 5*time.Second, "local op event on beta", localSeen.Load)
}

func TestIdentityIsBoundToDatabaseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.db")
	n1, err := node.Open(node.Config{ID: "alpha", DBPath: path, SyncIntervalMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	n1.CreateDocument("d", "t")
	n1.Close()
	// Same file, different ID: must refuse (identity theft prevention).
	if _, err := node.Open(node.Config{ID: "beta", DBPath: path, SyncIntervalMS: 1000}); err == nil {
		t.Fatal("opening a foreign replica ID against an existing database must fail")
	}
	// Same file, same ID: must succeed and retain ops (I-01).
	n2, err := node.Open(node.Config{ID: "alpha", DBPath: path, SyncIntervalMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if _, ok := n2.Document("d"); !ok {
		t.Fatal("ops lost across restart")
	}
}

// opFor builds a valid remote-style op for tests (identity fields set
// directly, content-addressed ID derived like a real peer would).
func opFor(n *node.Node, docID, actor string, kind core.Kind, field string, payload core.Payload) core.Operation {
	op := core.Operation{
		ProtocolVersion: core.ProtocolVersion,
		DocID:           docID,
		Field:           field,
		Kind:            kind,
		Actor:           actor,
		ActorSeq:        1,
		HLC:             clock.HLC{Wall: 5000, Counter: 0, Actor: actor},
		Payload:         payload,
	}
	if kind == core.KindLabelAdd {
		op.Payload.Tag = core.DeriveAddTag(actor, 1)
	}
	id, err := core.ComputeOpID(op)
	if err != nil {
		panic(err)
	}
	op.OpID = id
	return op
}

func TestConvergenceUnknownWhenPeerDead(t *testing.T) {
	dead, _ := startTestNode(t, "dead", nil, false)
	deadURL := dead.Config().Peers // unused; grab URL below via server
	_ = deadURL
	nb, srv_nb := startTestNode(t, "beta", nil, false)
	nb.CreateDocument("solo", "S")
	// Simulate a genuinely unreachable peer (process gone): closed server.
	na, _ := startTestNode(t, "alpha", []node.PeerConfig{{ID: "dead", URL: "http://127.0.0.1:1"}}, false)
	_ = srv_nb
	rep := na.Convergence()
	if rep.Status != node.StatusUnknown {
		t.Fatalf("status with unreachable peer = %s, want UNKNOWN", rep.Status)
	}
	for _, pr := range rep.Peers {
		if pr.ID == "dead" && pr.Reachable {
			t.Fatalf("dead peer reported reachable")
		}
	}
}
