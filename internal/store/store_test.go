package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func localOp(actor, doc string, kind core.Kind, field string, payload core.Payload) *core.Operation {
	return &core.Operation{
		ProtocolVersion: core.ProtocolVersion,
		OpID:            "", // assigned by the store
		DocID:           doc,
		Field:           field,
		Kind:            kind,
		Actor:           actor,
		Payload:         payload,
	}
}

func TestFreshOpenMigratesAndReopens(t *testing.T) {
	st, path := openTemp(t)
	if v, err := st.SchemaVersion(); err != nil || v < 1 {
		t.Fatalf("schema version = %d, %v", v, err)
	}
	if err := st.SetMeta("replica_id", "alpha"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	v, _ := st2.SchemaVersion()
	if v < 1 {
		t.Fatalf("schema version lost after restart: %d", v)
	}
	got, ok, err := st2.GetMeta("replica_id")
	if err != nil || !ok || got != "alpha" {
		t.Fatalf("meta lost after restart: %q %v %v", got, ok, err)
	}
}

func TestAppendLocalAllocatesDenseSequencesAndSurvivesRestart(t *testing.T) {
	st, path := openTemp(t)
	clk := clock.New("alpha", func() int64 { return 1000 })
	for i := 0; i < 5; i++ {
		op := localOp("alpha", "board", core.KindDocCreate, "", core.Payload{Value: "t"})
		op.Payload.Value = fmt.Sprintf("title-%d", i)
		op.Kind, op.Field = core.KindFieldSet, core.FieldTitle
		if err := st.AppendLocal(op, clk.Now()); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if op.ActorSeq != uint64(i+1) {
			t.Fatalf("seq = %d, want %d", op.ActorSeq, i+1)
		}
		if len(op.OpID) != 64 {
			t.Fatalf("op id not assigned")
		}
	}
	ops, err := st.AllOps()
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 5 {
		t.Fatalf("stored %d ops, want 5", len(ops))
	}
	st.Close()

	// Restart: everything, including sequence state, must come back (I-01).
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	ops2, err := st2.AllOps()
	if err != nil {
		t.Fatal(err)
	}
	if len(ops2) != 5 {
		t.Fatalf("ops lost after restart: %d", len(ops2))
	}
	for i := range ops {
		if ops[i].OpID != ops2[i].OpID {
			t.Fatalf("op %d changed across restart", i)
		}
	}
	// Next append continues the sequence — no reset, no reuse.
	op := localOp("alpha", "board", core.KindFieldSet, core.FieldTitle, core.Payload{Value: "after-restart"})
	if err := st2.AppendLocal(op, clock.HLC{Wall: 1000, Counter: 9, Actor: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if op.ActorSeq != 6 {
		t.Fatalf("post-restart seq = %d, want 6", op.ActorSeq)
	}
	// HLC watermark persisted with the ops must be readable.
	if _, ok, _ := st2.GetMeta("hlc_wall"); !ok {
		t.Fatal("hlc watermark not persisted")
	}
}

func TestConcurrentAppendsGetUniqueDenseSequences(t *testing.T) {
	st, _ := openTemp(t)
	clk := clock.New("alpha", func() int64 { return 2000 })
	const n = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	seqs := map[uint64]bool{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op := localOp("alpha", "board", core.KindFieldSet, core.FieldTitle, core.Payload{Value: fmt.Sprintf("c%d", i)})
			if err := st.AppendLocal(op, clk.Now()); err != nil {
				t.Errorf("concurrent append: %v", err)
				return
			}
			mu.Lock()
			seqs[op.ActorSeq] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(seqs) != n {
		t.Fatalf("got %d unique seqs, want %d", len(seqs), n)
	}
	for i := uint64(1); i <= n; i++ {
		if !seqs[i] {
			t.Fatalf("sequence %d missing (not dense)", i)
		}
	}
}

func TestIngestRemoteIdempotentAndConflictDetection(t *testing.T) {
	st, _ := openTemp(t)

	// Build a valid remote op from beta.
	op := core.Operation{
		ProtocolVersion: core.ProtocolVersion,
		DocID:           "board",
		Kind:            core.KindFieldSet,
		Field:           core.FieldTitle,
		Actor:           "beta",
		ActorSeq:        1,
		HLC:             clock.HLC{Wall: 1000, Counter: 0, Actor: "beta"},
		Payload:         core.Payload{Value: "from-beta"},
	}
	id, err := core.ComputeOpID(op)
	if err != nil {
		t.Fatal(err)
	}
	op.OpID = id

	out, err := st.IngestRemote(op, "gamma")
	if err != nil || out.Result != IngestAccepted {
		t.Fatalf("first ingest: %+v %v", out, err)
	}
	// Re-delivery of the identical op is an idempotent duplicate.
	out, err = st.IngestRemote(op, "gamma")
	if err != nil || out.Result != IngestDuplicate {
		t.Fatalf("re-ingest: %+v %v", out, err)
	}
	// Same op ID, different content: protocol conflict, never overwrite (I-04).
	tampered := op
	tampered.Payload.Value = "evil-overwrite"
	out, err = st.IngestRemote(tampered, "gamma")
	var ce *ConflictError
	if err == nil || out.Result != IngestRejected || out.Reason != core.CodeConflict {
		t.Fatalf("tampered ingest not rejected: %+v %v", out, err)
	}
	if _, ok := err.(*ConflictError); !ok {
		if !asConflict(err, &ce) {
			t.Fatalf("expected ConflictError, got %T", err)
		}
	}
	// Stored content must still be the original.
	ops, _ := st.AllOps()
	if len(ops) != 1 || ops[0].Payload.Value != "from-beta" {
		t.Fatalf("stored content was overwritten: %+v", ops)
	}
	// Same (actor, seq), different op id: also a conflict.
	impostor := op
	impostor.Payload.Value = "other"
	impostor.OpID = strings.Repeat("f", 64)
	out, err = st.IngestRemote(impostor, "gamma")
	if err == nil || out.Result != IngestRejected {
		t.Fatalf("(actor,seq) collision not rejected: %+v %v", out, err)
	}
}

func asConflict(err error, target **ConflictError) bool {
	if ce, ok := err.(*ConflictError); ok {
		*target = ce
		return true
	}
	return false
}

func TestInventoryTracksFrontierAndHoles(t *testing.T) {
	st, _ := openTemp(t)
	ingest := func(actor string, seq uint64, wall int64, val string) {
		t.Helper()
		op := core.Operation{
			ProtocolVersion: core.ProtocolVersion,
			DocID:           "board",
			Kind:            core.KindFieldSet,
			Field:           core.FieldTitle,
			Actor:           actor,
			ActorSeq:        seq,
			HLC:             clock.HLC{Wall: wall, Counter: 0, Actor: actor},
			Payload:         core.Payload{Value: val},
		}
		id, err := core.ComputeOpID(op)
		if err != nil {
			t.Fatal(err)
		}
		op.OpID = id
		out, err := st.IngestRemote(op, "peer")
		if err != nil || out.Result != IngestAccepted {
			t.Fatalf("ingest %s:%d: %+v %v", actor, seq, out, err)
		}
	}
	// Receive 1, 2 then 5: frontier must stay at 2 with hole list [5] (I-06).
	ingest("beta", 1, 100, "a")
	ingest("beta", 2, 200, "b")
	ingest("beta", 5, 500, "e")
	inv, err := st.Inventory(64)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv) != 1 || inv[0].Actor != "beta" {
		t.Fatalf("inventory = %+v", inv)
	}
	if inv[0].Frontier != 2 || inv[0].MaxSeen != 5 {
		t.Fatalf("frontier/max = %d/%d, want 2/5", inv[0].Frontier, inv[0].MaxSeen)
	}
	if len(inv[0].ReceivedAbove) != 1 || inv[0].ReceivedAbove[0] != 5 {
		t.Fatalf("received_above = %v, want [5]", inv[0].ReceivedAbove)
	}
	// The missing 3,4 arrive: frontier closes to 5 and the hole list empties.
	ingest("beta", 4, 400, "d")
	ingest("beta", 3, 300, "c")
	inv, _ = st.Inventory(64)
	if inv[0].Frontier != 5 || len(inv[0].ReceivedAbove) != 0 {
		t.Fatalf("after backfill: %+v", inv[0])
	}
}

func TestMissingOpsForIsHoleAware(t *testing.T) {
	local, _ := openTemp(t)
	// Local holds alpha seqs 1..7; the peer claims frontier 4, received
	// above = {6} (it has 6 but is missing 5 and 7).
	for i := uint64(1); i <= 7; i++ {
		op := localOp("alpha", "board", core.KindFieldSet, core.FieldTitle, core.Payload{Value: fmt.Sprintf("v%d", i)})
		if err := local.AppendLocal(op, clock.HLC{Wall: int64(1000 + i), Counter: 0, Actor: "alpha"}); err != nil {
			t.Fatal(err)
		}
	}
	peerInv := []ActorInventory{{Actor: "alpha", Frontier: 4, MaxSeen: 6, ReceivedAbove: []uint64{6}}}
	page, err := local.MissingOpsFor(peerInv, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []uint64
	for _, op := range page.Ops {
		got = append(got, op.ActorSeq)
	}
	if len(got) != 2 || got[0] != 5 || got[1] != 7 {
		t.Fatalf("hole-aware pull returned %v, want [5 7] (I-06)", got)
	}
	// Peer with NO entry for the actor: everything is sent.
	page, err = local.MissingOpsFor(nil, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Ops) != 3 || !page.HasMore {
		t.Fatalf("paged full pull: %d ops hasMore=%v", len(page.Ops), page.HasMore)
	}
	// Resume via cursor until drained.
	var total []uint64
	for {
		for _, op := range page.Ops {
			total = append(total, op.ActorSeq)
		}
		if !page.HasMore {
			break
		}
		page, err = local.MissingOpsFor(nil, 3, page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(total) != 7 {
		t.Fatalf("cursor pagination lost ops: %v", total)
	}
	// Partial hole list forces the safe full-range resend.
	peerPartial := []ActorInventory{{Actor: "alpha", Frontier: 4, MaxSeen: 6, ReceivedAbove: nil, Partial: true}}
	page, _ = local.MissingOpsFor(peerPartial, 100, "")
	if len(page.Ops) != 2 { // 5 and 6
		t.Fatalf("partial fallback expected [5 6], got %d ops", len(page.Ops))
	}
}

func TestCorruptedStoredOperationFailsClosed(t *testing.T) {
	st, path := openTemp(t)
	op := localOp("alpha", "board", core.KindFieldSet, core.FieldTitle, core.Payload{Value: "intact"})
	if err := st.AppendLocal(op, clock.HLC{Wall: 1000, Counter: 0, Actor: "alpha"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Simulate storage corruption by rewriting the canonical bytes.
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if _, err := st2.db.Exec(`UPDATE operations SET canonical = replace(canonical, 'intact', 'tampered')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st2.AllOps(); err == nil {
		t.Fatal("corrupted canonical bytes must fail closed on read")
	}
}

func TestListOpsPaginationIsBounded(t *testing.T) {
	st, _ := openTemp(t)
	clk := clock.New("alpha", func() int64 { return 3000 })
	for i := 0; i < 10; i++ {
		op := localOp("alpha", "board", core.KindFieldSet, core.FieldTitle, core.Payload{Value: fmt.Sprintf("p%d", i)})
		if err := st.AppendLocal(op, clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var all []core.Operation
	after := ""
	for {
		ops, err := st.ListOps(4, after)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, ops...)
		if len(ops) < 4 {
			break
		}
		after = ops[len(ops)-1].OpID
	}
	if len(all) != 10 {
		t.Fatalf("pagination returned %d ops, want 10", len(all))
	}
}
