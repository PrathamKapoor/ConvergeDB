package crdt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/PrathamKapoor/ConvergeDB/internal/core"
)

// Document is the materialized accumulator of one document. All members are
// CRDT merges; a document becomes visible once its doc.create operation has
// been applied, but field operations that arrive before the create are
// retained in their registers and appear when the create arrives — no
// operation is dropped for ordering reasons.
type Document struct {
	ID      string
	Title   LWWRegister
	Status  LWWRegister
	Labels  *ORSet
	Votes   *PNCounter
	Deleted LWWRegister // "1" = deleted, "0" = restored
	Created bool
}

// DeletedFlag is the current value of the deleted tombstone register.
func (d *Document) DeletedFlag() bool {
	return d.Deleted.Set() && d.Deleted.Value == "1"
}

// World materializes a full operation set into documents. Apply is
// deliberately total: it never fails on accumulated state (that would make
// the result order-dependent), only on genuine protocol violations such as
// a counter op-ID contradiction. Callers validate operations before apply.
//
// World is not safe for concurrent use; the node serializes access.
type World struct {
	docs map[string]*Document
}

func NewWorld() *World {
	return &World{docs: map[string]*Document{}}
}

func (w *World) acc(docID string) *Document {
	d, ok := w.docs[docID]
	if !ok {
		d = &Document{ID: docID, Labels: NewORSet(), Votes: NewPNCounter()}
		w.docs[docID] = d
	}
	return d
}

// Apply merges one validated operation and reports whether any materialized
// state changed.
func (w *World) Apply(op core.Operation) (bool, error) {
	d := w.acc(op.DocID)
	switch op.Kind {
	case core.KindDocCreate:
		changed := false
		if !d.Created {
			d.Created = true
			changed = true
		}
		if op.Payload.Value != "" {
			// The initial title behaves as an LWW write issued at creation.
			if d.Title.Apply(op.Payload.Value, op.HLC, op.OpID) {
				changed = true
			}
		}
		return changed, nil
	case core.KindDocDelete:
		return d.Deleted.Apply("1", op.HLC, op.OpID), nil
	case core.KindDocRestore:
		return d.Deleted.Apply("0", op.HLC, op.OpID), nil
	case core.KindFieldSet:
		switch op.Field {
		case core.FieldTitle:
			return d.Title.Apply(op.Payload.Value, op.HLC, op.OpID), nil
		case core.FieldStatus:
			return d.Status.Apply(op.Payload.Value, op.HLC, op.OpID), nil
		}
		return false, core.ValidationError{Code: core.CodeField, Detail: "field.set on unknown field " + op.Field}
	case core.KindLabelAdd:
		return d.Labels.Add(op.Payload.Value, op.Payload.Tag), nil
	case core.KindLabelRemove:
		return d.Labels.RemoveObserved(op.Payload.Value, op.Payload.Tags), nil
	case core.KindVoteDelta:
		return d.Votes.Apply(op.OpID, op.Payload.Delta)
	default:
		return false, core.ValidationError{Code: core.CodeKind, Detail: "unknown kind " + string(op.Kind)}
	}
}

// Document returns a copy of the accumulator for id, if any op has ever
// referenced it.
func (w *World) Document(id string) (Document, bool) {
	d, ok := w.docs[id]
	if !ok {
		return Document{}, false
	}
	return *d, true
}

// Documents returns all accumulators sorted by ID (including documents whose
// create has not yet arrived; callers filter on Created/DeletedFlag).
func (w *World) Documents() []Document {
	out := make([]Document, 0, len(w.docs))
	for _, d := range w.docs {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// docJSON is the canonical per-document projection. Field order is part of
// the convergence contract: byte-equal canonical state is the definition of
// convergence (I-07).
type docJSON struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Status  string   `json:"status"`
	Labels  []string `json:"labels"`
	Votes   int64    `json:"votes"`
	Deleted bool     `json:"deleted"`
}

// CanonicalState returns deterministic JSON over all CREATED documents,
// sorted by ID. Replicas holding the same set of valid operations produce
// byte-identical output here.
func (w *World) CanonicalState() ([]byte, error) {
	docs := w.Documents()
	visible := make([]docJSON, 0, len(docs))
	for _, d := range docs {
		if !d.Created {
			continue
		}
		visible = append(visible, docJSON{
			ID:      d.ID,
			Title:   d.Title.Value,
			Status:  d.Status.Value,
			Labels:  d.Labels.Values(),
			Votes:   d.Votes.Value(),
			Deleted: d.DeletedFlag(),
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(visible); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Digest returns the hex SHA-256 of CanonicalState — the per-replica state
// fingerprint used by the convergence check.
func (w *World) Digest() (string, error) {
	b, err := w.CanonicalState()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
