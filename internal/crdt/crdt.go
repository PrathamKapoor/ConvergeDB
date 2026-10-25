// Package crdt implements the three ConvergeDB merge types — LWW register,
// OR-Set (observed-remove), and delta counter — plus deterministic document
// materialization over an operation stream.
//
// Every Apply is a join-semilattice merge: commutative, associative and
// idempotent. Materialized state therefore depends only on the SET of
// applied operations, never on their arrival order (invariant I-02/I-03).
// No function here performs I/O.
package crdt

import (
	"sort"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
)

// LWWRegister is a last-writer-wins register. The winner is the write with
// the greatest hybrid logical clock; identical clocks (a clock bug, since a
// single actor never repeats an HLC) are broken deterministically by op ID
// so every replica still agrees. LWW can discard one concurrent semantic
// intent by design — it is not a merge of both text edits.
type LWWRegister struct {
	Value string
	H     clock.HLC
	OpID  string
	set   bool
}

// Apply merges one write and reports whether the register's value changed.
func (r *LWWRegister) Apply(value string, h clock.HLC, opID string) bool {
	if r.set {
		cmp := clock.Compare(h, r.H)
		if cmp < 0 || (cmp == 0 && opID <= r.OpID) {
			return false
		}
	}
	r.Value, r.H, r.OpID, r.set = value, h, opID, true
	return true
}

// Set reports whether any write has been applied.
func (r *LWWRegister) Set() bool { return r.set }

// ORSet is an observed-remove set. Each add carries a unique tag (derived
// from the add operation's identity). A remove records the add-tags it
// observed as TOMBSTONES; a value is live while it has at least one add-tag
// that no remove has tombstoned. Tombstones are what make the merge
// commutative: a remove that races with an add kills that add only if it
// named the add's tag, and a remove that arrives before its target add still
// wins when the add shows up later. Tombstones are never garbage-collected
// (no verified causal-stability predicate exists); that is a documented
// storage cost, not an oversight.
type ORSet struct {
	adds  map[string]map[string]struct{}
	tombs map[string]map[string]struct{}
}

func NewORSet() *ORSet {
	return &ORSet{
		adds:  map[string]map[string]struct{}{},
		tombs: map[string]map[string]struct{}{},
	}
}

// Add records tag for value; returns true if the value's live-tag set grew.
// Re-delivered adds are no-ops; an add whose tag a remove already
// tombstoned stays dead (the remove observed this add's tag).
func (s *ORSet) Add(value, tag string) bool {
	if s.adds[value] == nil {
		s.adds[value] = map[string]struct{}{}
	}
	if _, dup := s.adds[value][tag]; dup {
		return false
	}
	s.adds[value][tag] = struct{}{}
	return len(s.liveTags(value)) > 0
}

// RemoveObserved tombstones exactly the given tags of value; returns true if
// any LIVE tag was tombstoned. Tags the remover never saw are untouched.
func (s *ORSet) RemoveObserved(value string, tags []string) bool {
	before := s.liveTags(value)
	if s.tombs[value] == nil && len(tags) > 0 {
		s.tombs[value] = map[string]struct{}{}
	}
	for _, t := range tags {
		s.tombs[value][t] = struct{}{}
	}
	after := s.liveTags(value)
	if len(before) != len(after) {
		return true
	}
	for i := range before {
		if before[i] != after[i] {
			return true
		}
	}
	return false
}

// Contains reports whether value has at least one live add-tag.
func (s *ORSet) Contains(value string) bool {
	return len(s.liveTags(value)) > 0
}

// Values returns the live values sorted ascending.
func (s *ORSet) Values() []string {
	out := make([]string, 0, len(s.adds))
	for v := range s.adds {
		if len(s.liveTags(v)) > 0 {
			out = append(out, v)
		}
	}
	sortStrings(out)
	return out
}

// LiveTags returns the live (non-tombstoned) add-tags of value sorted
// ascending. The remove side captures this list when generating an observed
// remove.
func (s *ORSet) LiveTags(value string) []string {
	return s.liveTags(value)
}

func (s *ORSet) liveTags(value string) []string {
	out := make([]string, 0, len(s.adds[value]))
	for t := range s.adds[value] {
		if _, dead := s.tombs[value][t]; !dead {
			out = append(out, t)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) { sort.Strings(s) }
