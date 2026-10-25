package core

import (
	"fmt"
	"regexp"
	"sort"
	"unicode/utf8"
)

// ValidationError is a stable, machine-readable rejection. Code values are
// part of the API contract (see docs/PROTOCOL.md once written).
type ValidationError struct {
	Code   string
	Detail string
}

func (e ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

func ve(code, format string, args ...any) *ValidationError {
	return &ValidationError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Rejection codes.
const (
	CodeProtocol = "E_PROTOCOL" // wrong protocol version
	CodeID       = "E_ID"       // malformed actor/doc/op id or op-id/content mismatch
	CodeKind     = "E_KIND"     // unknown operation kind
	CodeField    = "E_FIELD"    // field not valid for kind or unknown
	CodeSchema   = "E_SCHEMA"   // malformed payload shape for the kind
	CodeLimit    = "E_LIMIT"    // size / range bound violated
	CodeClock    = "E_CLOCK"    // malformed HLC timestamp
	CodeConflict = "E_CONFLICT" // identity collision with different content
)

var (
	actorRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	docIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	hashRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Schema bounds of protocol version 1. All are explicit and tested; nothing
// is unbounded.
const (
	MaxTitleRunes     = 512
	MaxLabelRunes     = 64
	MaxLabelsPerDoc   = 200 // enforced by materializer, not per-op
	MaxRemoveTags     = 200
	MaxDeltaAbs       = 1_000_000_000
	MaxCanonicalBytes = 16 << 10 // 16 KiB per canonical operation
)

// StatusEnum is the closed set of values the status LWW register accepts.
var StatusEnum = []string{"backlog", "in_progress", "review", "done"}

func validStatus(s string) bool {
	for _, v := range StatusEnum {
		if s == v {
			return true
		}
	}
	return false
}

// Validate checks op against the protocol-v1 schema and fails closed on any
// violation. It also enforces the content-addressed op ID: op.OpID must
// equal ComputeOpID(op), so an operation ID can never travel with a
// different body.
func Validate(op Operation) error {
	if op.ProtocolVersion != ProtocolVersion {
		return ve(CodeProtocol, "protocol_version %d not supported (want %d)", op.ProtocolVersion, ProtocolVersion)
	}
	if !actorRe.MatchString(op.Actor) {
		return ve(CodeID, "actor %q must match %s", op.Actor, actorRe)
	}
	if !hashRe.MatchString(op.OpID) {
		return ve(CodeID, "op_id must be 64 lowercase hex chars")
	}
	if !docIDRe.MatchString(op.DocID) {
		return ve(CodeID, "doc_id %q must match %s", op.DocID, docIDRe)
	}
	if op.ActorSeq == 0 {
		return ve(CodeSchema, "actor_seq must be >= 1")
	}
	if op.HLC.Wall <= 0 || op.HLC.Counter < 0 {
		return ve(CodeClock, "hlc wall_ms must be > 0 and counter >= 0")
	}
	if !actorRe.MatchString(op.HLC.Actor) {
		return ve(CodeClock, "hlc.actor %q must match %s", op.HLC.Actor, actorRe)
	}

	p := op.Payload
	switch op.Kind {
	case KindDocCreate:
		if op.Field != "" {
			return ve(CodeField, "doc.create must not target a field (got %q)", op.Field)
		}
		if p.Tag != "" || p.Tags != nil || p.Delta != 0 {
			return ve(CodeSchema, "doc.create payload must only carry Value")
		}
		if utf8.RuneCountInString(p.Value) > MaxTitleRunes {
			return ve(CodeLimit, "initial title exceeds %d runes", MaxTitleRunes)
		}
	case KindDocDelete, KindDocRestore:
		if op.Field != "" {
			return ve(CodeField, "%s must not target a field (got %q)", op.Kind, op.Field)
		}
		if p.Value != "" || p.Tag != "" || p.Tags != nil || p.Delta != 0 {
			return ve(CodeSchema, "%s payload must be empty", op.Kind)
		}
	case KindFieldSet:
		switch op.Field {
		case FieldTitle:
			if p.Tag != "" || p.Tags != nil || p.Delta != 0 {
				return ve(CodeSchema, "field.set payload must only carry Value")
			}
			if utf8.RuneCountInString(p.Value) > MaxTitleRunes {
				return ve(CodeLimit, "title exceeds %d runes", MaxTitleRunes)
			}
		case FieldStatus:
			if p.Tag != "" || p.Tags != nil || p.Delta != 0 {
				return ve(CodeSchema, "field.set payload must only carry Value")
			}
			if !validStatus(p.Value) {
				return ve(CodeSchema, "status %q not in enum %v", p.Value, StatusEnum)
			}
		default:
			return ve(CodeField, "field.set cannot target field %q", op.Field)
		}
	case KindLabelAdd:
		if op.Field != FieldLabels {
			return ve(CodeField, "label.add must target field %q (got %q)", FieldLabels, op.Field)
		}
		if p.Tags != nil || p.Delta != 0 {
			return ve(CodeSchema, "label.add payload must carry Value and Tag only")
		}
		n := utf8.RuneCountInString(p.Value)
		if n < 1 || n > MaxLabelRunes {
			return ve(CodeLimit, "label value must be 1..%d runes (got %d)", MaxLabelRunes, n)
		}
		if p.Tag != DeriveAddTag(op.Actor, op.ActorSeq) {
			return ve(CodeSchema, "label.add tag must be DeriveAddTag(actor, actor_seq)")
		}
	case KindLabelRemove:
		if op.Field != FieldLabels {
			return ve(CodeField, "label.remove must target field %q (got %q)", FieldLabels, op.Field)
		}
		if p.Tag != "" || p.Delta != 0 {
			return ve(CodeSchema, "label.remove payload must carry Value and Tags only")
		}
		n := utf8.RuneCountInString(p.Value)
		if n < 1 || n > MaxLabelRunes {
			return ve(CodeLimit, "label value must be 1..%d runes (got %d)", MaxLabelRunes, n)
		}
		if len(p.Tags) == 0 || len(p.Tags) > MaxRemoveTags {
			return ve(CodeLimit, "label.remove must carry 1..%d tags (got %d)", MaxRemoveTags, len(p.Tags))
		}
		if !sort.StringsAreSorted(p.Tags) {
			return ve(CodeSchema, "label.remove tags must be sorted ascending")
		}
		seen := map[string]bool{}
		for _, t := range p.Tags {
			if !hashRe.MatchString(t) {
				return ve(CodeID, "remove tag %q must be 64 lowercase hex chars", t)
			}
			if seen[t] {
				return ve(CodeSchema, "remove tags must be unique")
			}
			seen[t] = true
		}
	case KindVoteDelta:
		if op.Field != FieldVotes {
			return ve(CodeField, "vote.delta must target field %q (got %q)", FieldVotes, op.Field)
		}
		if p.Value != "" || p.Tag != "" || p.Tags != nil {
			return ve(CodeSchema, "vote.delta payload must only carry Delta")
		}
		if p.Delta == 0 {
			return ve(CodeSchema, "vote.delta must be non-zero")
		}
		if p.Delta > MaxDeltaAbs || p.Delta < -MaxDeltaAbs {
			return ve(CodeLimit, "vote.delta magnitude exceeds %d", MaxDeltaAbs)
		}
	default:
		return ve(CodeKind, "unknown operation kind %q", op.Kind)
	}

	b, err := Canonical(op)
	if err != nil {
		return ve(CodeSchema, "canonical encoding failed: %v", err)
	}
	if len(b) > MaxCanonicalBytes {
		return ve(CodeLimit, "canonical operation is %d bytes (max %d)", len(b), MaxCanonicalBytes)
	}
	wantID, err := ComputeOpID(op)
	if err != nil {
		return ve(CodeSchema, "op id computation failed: %v", err)
	}
	if wantID != op.OpID {
		return ve(CodeID, "op_id does not match operation content (content-addressed id required)")
	}
	return nil
}
