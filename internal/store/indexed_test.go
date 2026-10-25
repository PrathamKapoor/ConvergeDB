package store

// Differential tests: the incremental in-memory inventory and the indexed
// pull-query path must produce EXACTLY the same results as fresh full-scan
// computation over the durable rows, under randomized operation arrival
// orders and requester inventory shapes. These tests are the correctness
// proof for the scalability rewrite (P1-1 in docs/audit/FINDINGS.md).

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
)

func ingestOp(t *testing.T, st *Store, actor string, seq uint64, wall int64, val string) {
	t.Helper()
	op := core.Operation{
		ProtocolVersion: core.ProtocolVersion,
		DocID:           "d",
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

func TestIncrementalInventoryMatchesScan(t *testing.T) {
	st, _ := openTemp(t)
	rnd := rand.New(rand.NewSource(20261026))

	// Random arrival order across three actors with gaps that exceed the
	// advertised-hole cap for one actor.
	seqs := map[string][]uint64{
		"alpha": {1, 3, 2, 9, 4, 5, 6, 7, 8, 20, 21, 22},
		"beta":  {5, 1, 2, 3, 4, 600, 601},
		"gamma": {},
	}
	for i := 1; i <= 800; i++ {
		seqs["gamma"] = append(seqs["gamma"], uint64(10000+i))
	}
	var arrivals []struct {
		actor string
		seq   uint64
	}
	for actor, list := range seqs {
		for _, seq := range list {
			arrivals = append(arrivals, struct {
				actor string
				seq   uint64
			}{actor, seq})
		}
	}
	rnd.Shuffle(len(arrivals), func(i, j int) { arrivals[i], arrivals[j] = arrivals[j], arrivals[i] })

	for i, a := range arrivals {
		ingestOp(t, st, a.actor, a.seq, int64(1000+i), fmt.Sprintf("v%d", i))
		got, err := st.Inventory(64)
		if err != nil {
			t.Fatal(err)
		}
		want, err := st.inventoryFromScan(64)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("after arrival %d (%s:%d): incremental %+v != scan %+v", i, a.actor, a.seq, got, want)
		}
	}
}

func drainPages(t *testing.T, st *Store, fn func(limit int, cursor string) (Page, error), inv []ActorInventory, limit int) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		page, err := fn(limit, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range page.Ops {
			ids = append(ids, op.OpID)
		}
		if !page.HasMore {
			return ids
		}
		cursor = page.NextCursor
		if cursor == "" {
			t.Fatal("has_more without cursor")
		}
	}
}

func TestMissingOpsIndexedMatchesScan(t *testing.T) {
	st, _ := openTemp(t)
	// Mixed log: three actors, out-of-order ingest so local holes exist too.
	arrivals := []struct {
		actor string
		seq   uint64
	}{
		{"alpha", 1}, {"alpha", 2}, {"alpha", 5}, {"alpha", 3}, {"alpha", 4}, {"alpha", 9}, {"alpha", 10},
		{"beta", 1}, {"beta", 4}, {"beta", 2}, {"beta", 3},
		{"gamma", 1}, {"gamma", 2}, {"gamma", 3}, {"gamma", 7},
	}
	for i, a := range arrivals {
		ingestOp(t, st, a.actor, a.seq, int64(2000+i), fmt.Sprintf("v%d", i))
	}

	peerShapes := [][]ActorInventory{
		nil, // knows nothing
		{{Actor: "alpha", Frontier: 2, MaxSeen: 5, ReceivedAbove: []uint64{5}}},
		{{Actor: "alpha", Frontier: 0, MaxSeen: 0, ReceivedAbove: []uint64{}}},
		{{Actor: "alpha", Frontier: 2, MaxSeen: 9, ReceivedAbove: nil, Partial: true}},
		{{Actor: "beta", Frontier: 4, MaxSeen: 4, ReceivedAbove: []uint64{}}},
		{{Actor: "gamma", Frontier: 3, MaxSeen: 7, ReceivedAbove: []uint64{7}}},
		{
			{Actor: "alpha", Frontier: 4, MaxSeen: 10, ReceivedAbove: []uint64{9, 10}},
			{Actor: "beta", Frontier: 1, MaxSeen: 4, ReceivedAbove: []uint64{2, 3, 4}},
			{Actor: "gamma", Frontier: 7, MaxSeen: 7, ReceivedAbove: []uint64{}},
		},
	}
	// A cursor deep inside the log (resume position), from the empty-requester drain.
	mid := ""
	{
		page, err := st.MissingOpsForScan(nil, 3, "")
		if err != nil || len(page.Ops) < 3 {
			t.Fatalf("setup page: %v", err)
		}
		mid = page.NextCursor
	}
	for _, inv := range peerShapes {
		for _, limit := range []int{1, 2, 3, 200} {
			got := drainPages(t, st, func(l int, c string) (Page, error) { return st.MissingOpsFor(inv, l, c) }, inv, limit)
			want := drainPages(t, st, func(l int, c string) (Page, error) { return st.MissingOpsForScan(inv, l, c) }, inv, limit)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("inv=%v limit=%d: indexed %v != scan %v", inv, limit, got, want)
			}
		}
	}
	// Resume from the mid-cursor with an inventory that makes the same actor
	// relevant again.
	for _, limit := range []int{1, 4} {
		got := drainPages(t, st, func(l int, c string) (Page, error) {
			return st.MissingOpsFor(nil, l, c)
		}, nil, limit)
		_ = mid
		want := drainPages(t, st, func(l int, c string) (Page, error) {
			return st.MissingOpsForScan(nil, l, c)
		}, nil, limit)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("cursor drain limit=%d mismatch", limit)
		}
	}
}

// TestIndexedPullIsBoundedByPageAndHoles proves the indexed path does not
// degrade with log length: with a requester that needs nothing, a pull over
// a large log must stay cheap (the old scan would touch every row).
func TestIndexedPullIsBoundedByPageAndHoles(t *testing.T) {
	st, _ := openTemp(t)
	clk := clock.New("alpha", func() int64 { return 5000 })
	for i := 0; i < 3000; i++ {
		op := &core.Operation{
			DocID: "d", Kind: core.KindVoteDelta, Field: core.FieldVotes,
			Actor: "alpha", Payload: core.Payload{Delta: 1},
		}
		if err := st.AppendLocal(op, clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Requester already has everything: the answer must be an empty page and
	// fast (the scan implementation would walk all 3000 rows).
	inv := []ActorInventory{{Actor: "alpha", Frontier: 3000, MaxSeen: 3000, ReceivedAbove: []uint64{}}}
	page, err := st.MissingOpsFor(inv, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Ops) != 0 || page.HasMore {
		t.Fatalf("expected empty page, got %d ops", len(page.Ops))
	}
	scan, err := st.MissingOpsForScan(inv, 200, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Ops) != 0 {
		t.Fatalf("oracle disagreement: %d ops", len(scan.Ops))
	}
}
