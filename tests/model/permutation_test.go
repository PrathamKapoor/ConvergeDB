// Package model exhaustively checks ConvergeDB's convergence contract on
// small bounded histories: for every permutation (and duplicate-laden
// schedule) of a valid operation set, every replica materialization path
// must produce byte-identical canonical state.
package model

import (
	"math/rand"
	"testing"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/crdt"
)

// perms generates all permutations of ops via Heap's algorithm.
func perms(op []core.Operation, emit func([]core.Operation)) {
	p := append([]core.Operation(nil), op...)
	n := len(p)
	c := make([]int, n)
	emit(p)
	i := 1
	for i < n {
		if c[i] < i {
			if i%2 == 0 {
				p[0], p[i] = p[i], p[0]
			} else {
				p[c[i]], p[i] = p[i], p[c[i]]
			}
			emit(append([]core.Operation(nil), p...))
			c[i]++
			i = 1
		} else {
			c[i] = 0
			i++
		}
	}
}

func hlc(w int64, a string) clock.HLC { return clock.HLC{Wall: w, Counter: 0, Actor: a} }

// smallHistory: a partition-friendly mix of all typed kinds.
func smallHistory(t *testing.T) []core.Operation {
	t.Helper()
	mk := func(actor string, seq uint64, wall int64, mutate func(*core.Operation)) core.Operation {
		op := core.Operation{
			ProtocolVersion: core.ProtocolVersion,
			DocID:           "b",
			Actor:           actor,
			ActorSeq:        seq,
			HLC:             hlc(wall, actor),
		}
		mutate(&op)
		if op.Kind == core.KindLabelAdd {
			op.Payload.Tag = core.DeriveAddTag(op.Actor, op.ActorSeq)
		}
		id, err := core.ComputeOpID(op)
		if err != nil {
			t.Fatal(err)
		}
		op.OpID = id
		if err := core.Validate(op); err != nil {
			t.Fatalf("op %s:%d invalid: %v", actor, seq, err)
		}
		return op
	}
	redTag := core.DeriveAddTag("alpha", 3)
	ops := []core.Operation{
		mk("alpha", 1, 100, func(o *core.Operation) {
			o.Kind, o.Payload = core.KindDocCreate, core.Payload{Value: "B"}
		}),
		mk("alpha", 2, 200, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindFieldSet, core.FieldTitle, core.Payload{Value: "T1"}
		}),
		mk("alpha", 3, 400, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindLabelAdd, core.FieldLabels, core.Payload{Value: "red"}
		}),
		mk("alpha", 4, 500, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindVoteDelta, core.FieldVotes, core.Payload{Delta: 1}
		}),
		mk("beta", 1, 300, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindLabelAdd, core.FieldLabels, core.Payload{Value: "blue"}
		}),
		mk("beta", 2, 600, func(o *core.Operation) {
			// beta removes only the red tag it observed.
			o.Kind, o.Field, o.Payload = core.KindLabelRemove, core.FieldLabels, core.Payload{Value: "red", Tags: []string{redTag}}
		}),
		mk("beta", 3, 700, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindFieldSet, core.FieldTitle, core.Payload{Value: "T2"}
		}),
	}
	return ops
}

func digestOf(t *testing.T, ops []core.Operation) string {
	t.Helper()
	w := crdt.NewWorld()
	for _, op := range ops {
		if _, err := w.Apply(op); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	d, err := w.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExhaustivePermutationsConverge(t *testing.T) {
	ops := smallHistory(t)
	count := 0
	var want string
	perms(ops, func(p []core.Operation) {
		d := digestOf(t, p)
		count++
		if count == 1 {
			want = d
			return
		}
		if d != want {
			t.Fatalf("permutation %d diverged (I-03 violated)", count)
		}
	})
	if count != 5040 { // 7!
		t.Fatalf("expected 5040 permutations, emitted %d", count)
	}
}

func TestDuplicateLadenSchedulesConverge(t *testing.T) {
	ops := smallHistory(t)
	want := digestOf(t, ops)
	for seed := int64(0); seed < 300; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		w := crdt.NewWorld()
		// Random schedule: each op delivered 1..3 times in random order.
		pool := make([]core.Operation, 0, len(ops)*3)
		for _, op := range ops {
			n := 1 + rnd.Intn(3)
			for i := 0; i < n; i++ {
				pool = append(pool, op)
			}
		}
		rnd.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		for _, op := range pool {
			if _, err := w.Apply(op); err != nil {
				t.Fatalf("seed %d: apply: %v", seed, err)
			}
		}
		d, _ := w.Digest()
		if d != want {
			t.Fatalf("seed %d: duplicate-laden schedule diverged (I-02)", seed)
		}
	}
}

func TestTwoReplicaExchangeModel(t *testing.T) {
	ops := smallHistory(t)
	rnd := rand.New(rand.NewSource(42))

	// Replica A applies a random subset; replica B the complement (plus
	// duplicates). A complete bidirectional exchange then delivers the full
	// set to both sides; both must equal the full-set materialization.
	want := digestOf(t, ops)
	for trial := 0; trial < 200; trial++ {
		a := crdt.NewWorld()
		b := crdt.NewWorld()
		for _, op := range ops {
			if rnd.Intn(2) == 0 {
				a.Apply(op)
				if rnd.Intn(2) == 0 {
					b.Apply(op) // duplicate delivery
				}
			} else {
				b.Apply(op)
			}
		}
		for _, op := range ops { // the exchange itself
			a.Apply(op)
			b.Apply(op)
		}
		da, _ := a.Digest()
		db, _ := b.Digest()
		if da != want || db != want {
			t.Fatalf("trial %d: exchange did not converge (I-07): %s / %s vs %s", trial, da, db, want)
		}
	}
}
