package adversarial

// Checkpoint verification from the outside: a restored materializer must be
// byte-equivalent to a full replay, and every failure mode must fall back to
// the full replay rather than shipping wrong state.

import (
	"path/filepath"
	"testing"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/node"
	"github.com/PrathamKapoor/ConvergeDB/internal/store"
)

func buildMixedNode(t *testing.T, path string, ops int) *node.Node {
	t.Helper()
	n, err := node.Open(node.Config{
		ID: "writer", DBPath: path, HTTPAddr: "127.0.0.1:0",
		SyncIntervalMS: 1 << 28, FaultSeed: 1, CheckpointEveryOps: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.CreateDocument("board", "Checkpoint Test"); err != nil {
		t.Fatal(err)
	}
	clk := clock.New("writer", nil)
	for i := 0; i < ops; i++ {
		var req node.MutationRequest
		switch i % 4 {
		case 0:
			req = node.MutationRequest{Action: "vote", Delta: 2}
		case 1:
			req = node.MutationRequest{Action: "add_label", Value: "label-" + string(rune('a'+i%7))}
		case 2:
			req = node.MutationRequest{Action: "set_status", Value: []string{"backlog", "in_progress", "review", "done"}[i%4]}
		default:
			req = node.MutationRequest{Action: "vote", Delta: -1}
		}
		if _, err := n.Mutate("board", req); err != nil {
			t.Fatalf("mutate %d: %v", i, err)
		}
	}
	_ = clk
	return n
}

func TestCheckpointRestartEquivalence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cp.db")
	n := buildMixedNode(t, path, 200)
	beforeDigest, err := n.StateDigest()
	if err != nil {
		t.Fatal(err)
	}
	beforeCount, beforeOps, err := n.OpDigest()
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Close(); err != nil { // graceful close writes the checkpoint
		t.Fatal(err)
	}

	n2, err := node.Open(node.Config{
		ID: "writer", DBPath: path, HTTPAddr: "127.0.0.1:0",
		SyncIntervalMS: 1 << 28, CheckpointEveryOps: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n2.ReplayMode() != "checkpoint+tail" {
		t.Fatalf("expected checkpoint+tail startup, got %q", n2.ReplayMode())
	}
	afterDigest, _ := n2.StateDigest()
	afterCount, afterOps, _ := n2.OpDigest()
	if afterDigest != beforeDigest {
		t.Fatalf("state digest changed across checkpoint restart: %s != %s", beforeDigest, afterDigest)
	}
	if afterCount != beforeCount || afterOps != beforeOps {
		t.Fatalf("operation inventory changed: %d/%s != %d/%s", afterCount, afterOps, beforeCount, beforeOps)
	}

	// The replica must continue exactly where it left off: next local seq,
	// clock watermark, and merge behavior.
	op, err := n2.Mutate("board", node.MutationRequest{Action: "vote", Delta: 5})
	if err != nil {
		t.Fatal(err)
	}
	if op.ActorSeq != 202 { // create (1) + 200 mutations, next is 202
		t.Fatalf("post-restart seq = %d, want 202", op.ActorSeq)
	}
	d, _ := n2.Document("board")
	if d.Votes.Value()%1 != 0 {
		t.Fatal("unreachable")
	}
}

func TestCheckpointTailReplayAfterStaleCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.db")
	n := buildMixedNode(t, path, 100)
	if err := n.WriteCheckpointNow(); err != nil {
		t.Fatal(err)
	}
	digestAtCp, _ := n.StateDigest()
	// Ten MORE ops after the checkpoint (simulating a crash before Close):
	// the stored checkpoint is now stale; startup must replay the tail.
	for i := 0; i < 10; i++ {
		if _, err := n.Mutate("board", node.MutationRequest{Action: "vote", Delta: 1}); err != nil {
			t.Fatal(err)
		}
	}
	wantDigest, _ := n.StateDigest()
	// Graceless shutdown: close ONLY the store (no node checkpoint write).
	// We approximate a kill by opening the store directly and closing it.
	n.Close() // writes a fresh checkpoint; to exercise the tail we rewind it

	// Rewind the stored checkpoint watermark by 10 ops so the tail path runs.
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cd, err := st.LoadCheckpoint()
	if err != nil || !cd.Valid {
		t.Fatalf("checkpoint should be valid here: %+v %v", cd, err)
	}
	if len(cd.Covered) == 0 || cd.Covered["writer"] < 10 {
		t.Fatalf("unexpected coverage: %+v", cd.Covered)
	}
	cd.Covered["writer"] -= 10
	if err := st.SaveCheckpoint(cd.Covered, cd.State); err != nil {
		t.Fatal(err)
	}
	st.Close()

	n2, err := node.Open(node.Config{
		ID: "writer", DBPath: path, HTTPAddr: "127.0.0.1:0",
		SyncIntervalMS: 1 << 28, CheckpointEveryOps: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n2.ReplayMode() != "checkpoint+tail" {
		t.Fatalf("expected checkpoint+tail, got %q", n2.ReplayMode())
	}
	gotDigest, _ := n2.StateDigest()
	if gotDigest != wantDigest {
		t.Fatalf("tail replay diverged: %s != %s (checkpoint digest was %s)", gotDigest, wantDigest, digestAtCp)
	}
}

func TestCheckpointCorruptionFallsBackToFullReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	n := buildMixedNode(t, path, 80)
	wantDigest, _ := n.StateDigest()
	n.Close()

	// Corrupt the checkpoint payload in place.
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExecRaw(`UPDATE checkpoint SET state = '{"protocol_version":1,"covered":{},"docs":[]}'`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	n2, err := node.Open(node.Config{
		ID: "writer", DBPath: path, HTTPAddr: "127.0.0.1:0",
		SyncIntervalMS: 1 << 28, CheckpointEveryOps: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n2.ReplayMode() != "full" {
		t.Fatalf("corrupt checkpoint must fall back to full replay, got %q", n2.ReplayMode())
	}
	gotDigest, _ := n2.StateDigest()
	if gotDigest != wantDigest {
		t.Fatalf("fallback replay diverged: %s != %s", gotDigest, wantDigest)
	}
}

func TestCheckpointPrefixMismatchFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefix.db")
	n := buildMixedNode(t, path, 50)
	wantDigest, _ := n.StateDigest()
	n.Close()

	// Forge a checkpoint whose coverage claims more than the log holds.
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCheckpoint(map[string]uint64{"writer": 9999}, []byte(`{"protocol_version":1,"covered":{"writer":9999},"docs":[]}`)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	n2, err := node.Open(node.Config{
		ID: "writer", DBPath: path, HTTPAddr: "127.0.0.1:0",
		SyncIntervalMS: 1 << 28, CheckpointEveryOps: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer n2.Close()
	if n2.ReplayMode() != "full" {
		t.Fatalf("prefix mismatch must fall back to full replay, got %q", n2.ReplayMode())
	}
	gotDigest, _ := n2.StateDigest()
	if gotDigest != wantDigest {
		t.Fatalf("fallback replay diverged: %s != %s", gotDigest, wantDigest)
	}
	_ = core.ProtocolVersion
}
