// Package core defines the ConvergeDB operation envelope: the typed
// operation kinds, payload shapes, canonical JSON encoding, content-derived
// operation IDs and integrity checksums, plus fail-closed validation.
//
// Convergence contract: an operation's canonical bytes depend only on its
// logical content, so every replica computes the same op ID and checksum for
// the same operation, and duplicate op IDs with differing content are
// detectable protocol conflicts rather than silent overwrites.
package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
)

// ProtocolVersion is the wire/canvas protocol version this build speaks.
// Replicas reject operations and messages with other versions (fail closed).
const ProtocolVersion = 1

// Kind enumerates every operation kind of protocol version 1. The set is
// deliberately narrow: one LWW register kind, two OR-Set kinds, one counter
// kind, and document lifecycle kinds.
type Kind string

const (
	KindDocCreate   Kind = "doc.create"
	KindDocDelete   Kind = "doc.delete"
	KindDocRestore  Kind = "doc.restore"
	KindFieldSet    Kind = "field.set"
	KindLabelAdd    Kind = "label.add"
	KindLabelRemove Kind = "label.remove"
	KindVoteDelta   Kind = "vote.delta"
)

// Field identifiers of the bundled document schema (schema version 1).
const (
	FieldTitle  = "title"
	FieldStatus = "status"
	FieldLabels = "labels"
	FieldVotes  = "votes"
)

// Payload is the typed operation payload. Which members are meaningful is
// fixed per Kind (see Validate); unrelated members must be zero. Struct
// field order below is part of the canonical encoding.
type Payload struct {
	Value string   `json:"value,omitempty"` // field.set text / label value / initial title
	Tag   string   `json:"tag,omitempty"`   // label.add unique tag (always == OpID)
	Tags  []string `json:"tags,omitempty"`  // label.remove observed add-tags (sorted, unique)
	Delta int64    `json:"delta,omitempty"` // vote.delta, non-zero, bounded
}

// Operation is the unit of replication. Struct field order below is part of
// the canonical encoding.
type Operation struct {
	ProtocolVersion int       `json:"protocol_version"`
	OpID            string    `json:"op_id"`
	DocID           string    `json:"doc_id"`
	Field           string    `json:"field"`
	Kind            Kind      `json:"kind"`
	Actor           string    `json:"actor"`
	ActorSeq        uint64    `json:"actor_seq"`
	HLC             clock.HLC `json:"hlc"`
	Payload         Payload   `json:"payload"`
}

// Canonical returns the deterministic JSON encoding of op: struct field
// order, no HTML escaping, and Payload.Tags serialized in the stored order
// (validation enforces sorted, unique tags). Two replicas holding the same
// logical operation produce identical bytes.
func Canonical(op Operation) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(op); err != nil {
		return nil, fmt.Errorf("canonical encode: %w", err)
	}
	// json.Encoder appends a newline; strip it so checksums are stable.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Checksum returns the hex SHA-256 of the canonical encoding. This is an
// integrity/corruption-digest only — it is NOT an authentication signature
// and must never be described as one.
func Checksum(op Operation) (string, error) {
	b, err := Canonical(op)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// ComputeOpID returns the content-derived operation ID: SHA-256 over the
// canonical encoding of the operation with its OpID field blanked. Writers
// must set op.OpID to this value; validation rejects mismatches so an
// operation ID is never reused with a different body.
func ComputeOpID(op Operation) (string, error) {
	op.OpID = ""
	b, err := Canonical(op)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// DeriveAddTag returns the unique OR-Set add-tag bound to the add operation
// identified by (actor, actorSeq). The tag is unique because (actor,
// actor_seq) is unique, deterministic so histories are reproducible, and it
// is covered by the operation's content-derived ID. This is the tie between
// an add-tag and the operation that created it.
func DeriveAddTag(actor string, actorSeq uint64) string {
	sum := sha256.Sum256([]byte("convergedb:add-tag:" + actor + ":" + strconv.FormatUint(actorSeq, 10)))
	return hex.EncodeToString(sum[:])
}
