package core

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
)

// mustOp builds a validated operation, deriving its content-addressed ID.
func mustOp(t *testing.T, op Operation) Operation {
	t.Helper()
	id, err := ComputeOpID(op)
	if err != nil {
		t.Fatalf("ComputeOpID: %v", err)
	}
	op.OpID = id
	if err := Validate(op); err != nil {
		t.Fatalf("built invalid op: %v", err)
	}
	return op
}

func baseOp() Operation {
	return Operation{
		ProtocolVersion: ProtocolVersion,
		DocID:           "board",
		Kind:            KindFieldSet,
		Field:           FieldTitle,
		Actor:           "alpha",
		ActorSeq:        1,
		HLC:             clock.HLC{Wall: 1000, Counter: 0, Actor: "alpha"},
		Payload:         Payload{Value: "hello"},
	}
}

func TestCanonicalEncodingIsDeterministicAndStable(t *testing.T) {
	op := mustOp(t, baseOp())
	b1, err := Canonical(op)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := Canonical(op)
	if string(b1) != string(b2) {
		t.Fatalf("canonical encoding unstable")
	}
	// Round-trip through JSON must re-encode to identical bytes.
	var back Operation
	if err := json.Unmarshal(b1, &back); err != nil {
		t.Fatal(err)
	}
	b3, _ := Canonical(back)
	if string(b1) != string(b3) {
		t.Fatalf("canonical encoding not round-trip stable:\n%s\n%s", b1, b3)
	}
	// Fixed field order: protocol_version is the first key, payload the last.
	if !regexp.MustCompile(`^\{"protocol_version":`).Match(b1) {
		t.Fatalf("unexpected canonical field order: %s", b1)
	}
	if !strings.HasSuffix(string(b1), `"payload":{"value":"hello"}}`) {
		t.Fatalf("unexpected canonical tail: %s", b1)
	}
}

func TestOpIDIsContentAddressed(t *testing.T) {
	op := baseOp()
	id1, _ := ComputeOpID(op)
	op.Payload.Value = "changed"
	id2, _ := ComputeOpID(op)
	if id1 == id2 {
		t.Fatalf("different bodies must produce different op IDs")
	}
	if len(id1) != 64 || !hashRe.MatchString(id1) {
		t.Fatalf("op ID %q is not 64 lowercase hex chars", id1)
	}
	// Same body via a different struct copy (e.g. after JSON round-trip)
	// must produce the same ID.
	op.Payload.Value = "hello"
	id3, _ := ComputeOpID(op)
	if id3 != id1 {
		t.Fatalf("same body must reproduce the op ID")
	}
}

func TestChecksumDetectsTampering(t *testing.T) {
	op := mustOp(t, baseOp())
	sum, _ := Checksum(op)
	tampered := op
	tampered.Payload.Value = "tampered"
	sum2, _ := Checksum(tampered)
	if sum == sum2 {
		t.Fatalf("checksum must change when content changes")
	}
}

func TestValidateRejectsMalformedOperations(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Operation)
		wantC string
	}{
		{"wrong protocol", func(o *Operation) { o.ProtocolVersion = 2 }, CodeProtocol},
		{"bad actor case", func(o *Operation) { o.Actor = "Alpha" }, CodeID},
		{"empty actor", func(o *Operation) { o.Actor = "" }, CodeID},
		{"bad doc id", func(o *Operation) { o.DocID = "Board!" }, CodeID},
		{"zero seq", func(o *Operation) { o.ActorSeq = 0 }, CodeSchema},
		{"zero wall", func(o *Operation) { o.HLC = clock.HLC{Wall: 0, Counter: 0, Actor: "alpha"} }, CodeClock},
		{"bad hlc actor", func(o *Operation) { o.HLC.Actor = "ALPHA" }, CodeClock},
		{"unknown kind", func(o *Operation) { o.Kind = Kind("field.explode") }, CodeKind},
		{"field.set on votes", func(o *Operation) { o.Field = FieldVotes }, CodeField},
		{"invalid status", func(o *Operation) {
			o.Kind, o.Field, o.Payload = KindFieldSet, FieldStatus, Payload{Value: "on-fire"}
		}, CodeSchema},
		{"oversize title", func(o *Operation) {
			o.Payload.Value = strings.Repeat("a", MaxTitleRunes+1)
		}, CodeLimit},
		{"label too short", func(o *Operation) {
			o.Kind, o.Field, o.Payload = KindLabelAdd, FieldLabels, Payload{Value: "", Tag: "x"}
		}, CodeLimit},
		{"label tag mismatch", func(o *Operation) {
			o.Kind, o.Field = KindLabelAdd, FieldLabels
			o.Payload = Payload{Value: "red", Tag: strings.Repeat("a", 64)}
		}, CodeSchema}, // tag != derived op id -> payload inconsistent
		{"delta zero", func(o *Operation) {
			o.Kind, o.Field, o.Payload = KindVoteDelta, FieldVotes, Payload{Delta: 0}
		}, CodeSchema},
		{"delta overflow", func(o *Operation) {
			o.Kind, o.Field, o.Payload = KindVoteDelta, FieldVotes, Payload{Delta: MaxDeltaAbs + 1}
		}, CodeLimit},
		{"create with field", func(o *Operation) {
			o.Kind, o.Field, o.Payload = KindDocCreate, FieldTitle, Payload{}
		}, CodeField},
		{"delete with payload", func(o *Operation) {
			o.Kind, o.Field, o.Payload = KindDocDelete, "", Payload{Value: "x"}
		}, CodeSchema},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := mustOp(t, baseOp()) // start valid
			tc.mut(&op)               // then break it (id no longer matches content)
			err := Validate(op)
			if err == nil {
				t.Fatalf("expected rejection")
			}
			if ve, ok := err.(ValidationError); !ok || ve.Code != tc.wantC {
				t.Fatalf("got %v (%T), want code %s", err, err, tc.wantC)
			}
		})
	}
}

func TestValidateLabelRemoveRules(t *testing.T) {
	mk := func(tags []string) Operation {
		op := baseOp()
		op.Kind, op.Field, op.Payload = KindLabelRemove, FieldLabels, Payload{Value: "red", Tags: tags}
		return op
	}
	if err := Validate(mk([]string{"b", "a"})); err == nil {
		t.Fatalf("unsorted tags must be rejected")
	}
	if err := Validate(mk([]string{"a", "a"})); err == nil {
		t.Fatalf("duplicate tags must be rejected")
	}
	if err := Validate(mk([]string{strings.Repeat("z", 64)})); err == nil {
		t.Fatalf("non-hex tag must be rejected")
	}
	if err := Validate(mk(nil)); err == nil {
		t.Fatalf("empty tag list must be rejected")
	}
	// Build MaxRemoveTags+1 unique, sorted, valid hex tags.
	many := make([]string, MaxRemoveTags+1)
	for i := range many {
		h := strconv.FormatUint(uint64(i), 16)
		if len(h) < 2 {
			h = "0" + h
		}
		many[i] = strings.Repeat("0", 62) + h
	}
	if err := Validate(mk(many)); err == nil {
		t.Fatalf("over-limit tag list must be rejected")
	}
}

func TestValidateAcceptsValidExoticPayloads(t *testing.T) {
	// Unicode title of exactly the limit must pass; multibyte runes counted as runes.
	op := baseOp()
	op.Payload.Value = strings.Repeat("ж", MaxTitleRunes)
	mustOp(t, op)
	// Minus delta at the bound.
	op2 := baseOp()
	op2.Kind, op2.Field, op2.Payload = KindVoteDelta, FieldVotes, Payload{Delta: -MaxDeltaAbs}
	mustOp(t, op2)
	// Doc create with empty initial title is allowed (explicit semantics).
	op3 := baseOp()
	op3.Kind, op3.Field, op3.Payload = KindDocCreate, "", Payload{}
	mustOp(t, op3)
}
