package crdt

import (
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
)

func h(wall int64, counter int64, actor string) clock.HLC {
	return clock.HLC{Wall: wall, Counter: counter, Actor: actor}
}

func TestLWWRegisterOrderingAndIdempotence(t *testing.T) {
	var r LWWRegister
	if r.Apply("v1", h(100, 0, "alpha"), "op1") != true {
		t.Fatal("first apply must set the register")
	}
	if r.Apply("v0", h(99, 0, "alpha"), "op2") {
		t.Fatal("older write must lose")
	}
	if r.Value != "v1" {
		t.Fatalf("older write changed value to %q", r.Value)
	}
	if r.Apply("v2", h(100, 0, "alpha"), "op1") {
		t.Fatal("replaying the winning op must be a no-op")
	}
	if r.Apply("v3", h(100, 1, "beta"), "op3") != true {
		t.Fatal("higher counter must win")
	}
	// Equal (wall, counter): the HIGHER actor wins the deterministic
	// tie-break — "zzz" > "beta".
	if r.Apply("v4", h(100, 1, "zzz"), "op4") != true {
		t.Fatal("equal clock: higher actor must win the tie-break")
	}
	if r.Value != "v4" {
		t.Fatalf("value = %q, want v4", r.Value)
	}
	// Equal HLC from different actors: same winner regardless of arrival.
	var r2 LWWRegister
	r2.Apply("a", h(5, 0, "beta"), "opA")
	r2.Apply("b", h(5, 0, "alpha"), "opB") // alpha < beta, later arrival must lose
	if r2.Value != "a" {
		t.Fatalf("actor tie-break violated: %q", r2.Value)
	}
	var r3 LWWRegister
	r3.Apply("b", h(5, 0, "alpha"), "opB")
	r3.Apply("a", h(5, 0, "beta"), "opA")
	if r3.Value != "a" {
		t.Fatalf("actor tie-break not arrival-order independent: %q", r3.Value)
	}
}

func TestORSetObservedRemoveSemantics(t *testing.T) {
	s := NewORSet()
	s.Add("red", "t1")
	s.Add("red", "t2")
	s.Add("blue", "t3")

	// Remove observes only t1: red survives via t2, blue untouched.
	if !s.RemoveObserved("red", []string{"t1"}) {
		t.Fatal("removing a live tag must change state")
	}
	if !s.Contains("red") || !s.Contains("blue") {
		t.Fatalf("add-wins violated: live = %v", s.Values())
	}

	// Remove-before-add, where the remove NAMED the tag: the tombstone wins
	// and the later-arriving add stays dead — deterministic in every order.
	s2 := NewORSet()
	if s2.RemoveObserved("green", []string{"tagX"}) {
		t.Fatal("removing a never-added value must not change state")
	}
	s2.Add("green", "tagX")
	if s2.Contains("green") {
		t.Fatal("remove observed this tag; the later-arriving add must stay tombstoned")
	}
	// Remove-before-add where the remove did NOT observe the tag: add-wins.
	s3 := NewORSet()
	s3.RemoveObserved("green", []string{"other"})
	s3.Add("green", "tagX")
	if !s3.Contains("green") {
		t.Fatal("concurrent unseen add must survive the remove (I-09)")
	}

	// Duplicate deliveries are idempotent.
	s.Add("red", "t1")
	if s.Add("red", "t1") {
		t.Fatal("duplicate add must be a no-op")
	}
	before := len(s.Values())
	s.RemoveObserved("red", []string{"t1"})
	s.RemoveObserved("red", []string{"t1"})
	if len(s.Values()) != before {
		t.Fatal("duplicate remove must be a no-op")
	}
	// red is still live via its second, un-removed tag t2.
	if got := s.Values(); len(got) != 2 || got[0] != "blue" || got[1] != "red" {
		t.Fatalf("values = %v, want [blue red]", got)
	}
	if got := s.LiveTags("blue"); len(got) != 1 || got[0] != "t3" {
		t.Fatalf("live tags = %v, want [t3]", got)
	}
}

func TestORSetAddWinsAcrossAnyApplyOrder(t *testing.T) {
	// The classic race: add(t1) on A, remove(observations=[]) on B.
	// Whichever order the two ops arrive in, red must be present.
	build := func(removeFirst bool) *ORSet {
		s := NewORSet()
		if removeFirst {
			s.RemoveObserved("red", nil)
			s.Add("red", "t1")
		} else {
			s.Add("red", "t1")
			s.RemoveObserved("red", nil)
		}
		return s
	}
	a, b := build(true), build(false)
	if !a.Contains("red") || !b.Contains("red") {
		t.Fatal("concurrent unseen add must survive remove regardless of order")
	}
}

func TestCounterDedupCommutativityAndOverflow(t *testing.T) {
	c := NewPNCounter()
	if changed, err := c.Apply("op1", 5); err != nil || !changed {
		t.Fatalf("first apply: changed=%v err=%v", changed, err)
	}
	if _, err := c.Apply("op2", -2); err != nil {
		t.Fatal(err)
	}
	if c.Value() != 3 {
		t.Fatalf("value = %d, want 3", c.Value())
	}
	// Replay of the same op: never double-counts (I-10).
	if changed, err := c.Apply("op1", 5); err != nil || changed {
		t.Fatalf("replay changed=%v err=%v, want false nil", changed, err)
	}
	if c.Value() != 3 {
		t.Fatalf("replay double-counted: %d", c.Value())
	}
	// Same op ID with different delta is a protocol contradiction.
	if _, err := c.Apply("op1", 6); !errors.Is(err, ErrCounterConflict) {
		t.Fatalf("want ErrCounterConflict, got %v", err)
	}
	// Overflow fails closed without mutating.
	c2 := NewPNCounter()
	c2.Apply("big", math.MaxInt64)
	if _, err := c2.Apply("more", 1); !errors.Is(err, ErrCounterOverflow) {
		t.Fatalf("want overflow error, got %v", err)
	}
	if c2.Value() != math.MaxInt64 {
		t.Fatalf("failed merge mutated value: %d", c2.Value())
	}
	// Independent replicas' increments both survive (I-10).
	concurrent := NewPNCounter()
	concurrent.Apply("a-op", 1)
	concurrent.Apply("b-op", 1)
	if concurrent.Value() != 2 {
		t.Fatalf("independent increments lost: %d", concurrent.Value())
	}
}

// buildHistory returns a valid, diverse operation set for the demo document.
func buildHistory(t *testing.T) []core.Operation {
	t.Helper()
	mk := func(seq uint64, wall int64, mutate func(*core.Operation)) core.Operation {
		op := core.Operation{
			ProtocolVersion: core.ProtocolVersion,
			DocID:           "board",
			Actor:           "alpha",
			ActorSeq:        seq,
			HLC:             h(wall, 0, "alpha"),
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
			t.Fatalf("history op %d invalid: %v", seq, err)
		}
		return op
	}
	redTag := core.DeriveAddTag("alpha", 4)
	ops := []core.Operation{
		mk(1, 100, func(o *core.Operation) {
			o.Kind, o.Payload = core.KindDocCreate, core.Payload{Value: "Board"}
		}),
		mk(2, 200, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindFieldSet, core.FieldTitle, core.Payload{Value: "Sprint 1"}
		}),
		mk(3, 300, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindFieldSet, core.FieldStatus, core.Payload{Value: "in_progress"}
		}),
		mk(4, 400, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindLabelAdd, core.FieldLabels, core.Payload{Value: "red"}
		}),
		mk(5, 500, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindVoteDelta, core.FieldVotes, core.Payload{Delta: 2}
		}),
		mk(6, 600, func(o *core.Operation) {
			o.Kind, o.Field, o.Payload = core.KindLabelAdd, core.FieldLabels, core.Payload{Value: "blue"}
		}),
		mk(7, 700, func(o *core.Operation) {
			// Observed remove: only the tag of the concurrent "red" add.
			o.Kind, o.Field, o.Payload = core.KindLabelRemove, core.FieldLabels, core.Payload{Value: "red", Tags: []string{redTag}}
		}),
	}
	return ops
}

func TestWorldPermutationInvariance(t *testing.T) {
	ops := buildHistory(t)
	var want string
	for seed := 0; seed < 200; seed++ {
		shuffled := append([]core.Operation(nil), ops...)
		rnd := rand.New(rand.NewSource(int64(seed)))
		rnd.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		w := NewWorld()
		for _, op := range shuffled {
			if _, err := w.Apply(op); err != nil {
				t.Fatalf("apply failed for seed %d: %v", seed, err)
			}
		}
		got, err := w.Digest()
		if err != nil {
			t.Fatal(err)
		}
		if seed == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("seed %d diverged: %s != %s (I-03 violated)", seed, got, want)
		}
	}
}

func TestWorldDuplicatesAreIdempotent(t *testing.T) {
	ops := buildHistory(t)
	w1 := NewWorld()
	for _, op := range ops {
		w1.Apply(op)
	}
	w2 := NewWorld()
	// Deliver everything twice, interleaved and reversed.
	var doubled []core.Operation
	for i := len(ops) - 1; i >= 0; i-- {
		doubled = append(doubled, ops[i], ops[i])
	}
	for _, op := range doubled {
		if _, err := w2.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	d1, _ := w1.Digest()
	d2, _ := w2.Digest()
	if d1 != d2 {
		t.Fatalf("duplicate delivery diverged (I-02 violated): %s vs %s", d1, d2)
	}
}

func TestWorldDocumentLifecycleSemantics(t *testing.T) {
	ops := buildHistory(t)
	w := NewWorld()
	for _, op := range ops {
		w.Apply(op)
	}
	d, ok := w.Document("board")
	if !ok || !d.Created {
		t.Fatal("document must be created")
	}
	if d.Title.Value != "Sprint 1" || d.Status.Value != "in_progress" {
		t.Fatalf("fields wrong: %+v", d)
	}
	if d.Votes.Value() != 2 {
		t.Fatalf("votes = %d, want 2", d.Votes.Value())
	}
	if labels := d.Labels.Values(); len(labels) != 1 || labels[0] != "blue" {
		t.Fatalf("labels = %v, want [blue] (red was observed-removed)", labels)
	}
	// Delete then concurrent-ish restore: highest HLC wins (LWW).
	del := ops[0]
	del.Kind, del.Payload, del.HLC, del.ActorSeq = core.KindDocDelete, core.Payload{}, h(800, 0, "alpha"), 8
	id, _ := core.ComputeOpID(del)
	del.OpID = id
	res := ops[0]
	res.Kind, res.Payload, res.HLC, res.ActorSeq = core.KindDocRestore, core.Payload{}, h(700, 0, "alpha"), 9
	rid, _ := core.ComputeOpID(res)
	res.OpID = rid
	w.Apply(del)
	w.Apply(res)
	d, _ = w.Document("board")
	if !d.DeletedFlag() {
		t.Fatal("delete at HLC 800 must beat restore at 700")
	}
	// Canonical state marks deleted docs but keeps their fields.
	b, err := w.CanonicalState()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"deleted":true`) {
		t.Fatalf("canonical state missing deleted flag: %s", b)
	}
}

func TestWorldCreateArrivesLast(t *testing.T) {
	ops := buildHistory(t)
	w := NewWorld()
	// Apply everything EXCEPT the create, then the create last.
	for _, op := range ops {
		if op.Kind != core.KindDocCreate {
			w.Apply(op)
		}
	}
	if docs := w.Documents(); len(docs) == 0 {
		t.Fatal("field ops must retain an accumulator")
	}
	d, _ := w.Document("board")
	if d.Created {
		t.Fatal("doc must stay invisible before create arrives")
	}
	w.Apply(ops[0])
	d, _ = w.Document("board")
	if !d.Created || d.Title.Value != "Sprint 1" {
		t.Fatalf("late create must reveal buffered fields: %+v", d)
	}
	digLate, _ := w.Digest()
	w2 := NewWorld()
	for _, op := range ops {
		w2.Apply(op)
	}
	digEarly, _ := w2.Digest()
	if digLate != digEarly {
		t.Fatalf("create arrival order changed convergence (I-03)")
	}
}

func TestCanonicalStateIsSortedAndStable(t *testing.T) {
	w := NewWorld()
	// Two docs inserted in reverse ID order.
	for _, id := range []string{"zeta", "alpha"} {
		op := core.Operation{
			ProtocolVersion: core.ProtocolVersion,
			DocID:           id,
			Kind:            core.KindDocCreate,
			Actor:           "alpha",
			ActorSeq:        1,
			HLC:             h(100, 0, "alpha"),
			Payload:         core.Payload{Value: id},
		}
		id2, _ := core.ComputeOpID(op)
		op.OpID = id2
		if _, err := w.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := w.CanonicalState()
	// alpha must appear before zeta regardless of insertion order.
	ai, zi := strings.Index(string(b), `"alpha"`), strings.Index(string(b), `"zeta"`)
	if ai < 0 || zi < 0 || ai > zi {
		t.Fatalf("canonical ordering violated: %s", b)
	}
}
