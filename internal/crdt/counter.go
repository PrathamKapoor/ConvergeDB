package crdt

import (
	"errors"
	"math"
)

// ErrCounterOverflow and ErrCounterConflict are returned by PNCounter.Apply.
// Overflow rejects the merge without mutating state; a conflict means the
// same op ID arrived with a different delta, which validation makes
// impossible for content-addressed operations — treat it as a protocol bug.
var (
	ErrCounterOverflow = errors.New("counter merge would overflow int64")
	ErrCounterConflict = errors.New("same op_id already applied with a different delta")
)

// PNCounter is a positive/negative counter implemented as an idempotent
// operation-delta log: each contributing operation contributes exactly one
// keyed entry, and the value is the sum of entries. It is NOT the textbook
// state-based G-Counter pair; correctness rests on (a) deduplication by op
// ID — replaying the same operation never double-counts — and (b) integer
// addition being commutative and associative, so merge order is irrelevant.
// Entries stay for the replica lifetime; there is no tombstone GC.
type PNCounter struct {
	entries map[string]int64
	value   int64
}

func NewPNCounter() *PNCounter {
	return &PNCounter{entries: map[string]int64{}}
}

// Apply merges delta under opID. It is idempotent for a repeated (opID,
// delta) pair and fails closed on overflow or an op-ID/delta contradiction.
// Returns true if the counter value changed.
func (c *PNCounter) Apply(opID string, delta int64) (bool, error) {
	if existing, dup := c.entries[opID]; dup {
		if existing != delta {
			return false, ErrCounterConflict
		}
		return false, nil
	}
	if delta > 0 && c.value > math.MaxInt64-delta {
		return false, ErrCounterOverflow
	}
	if delta < 0 && c.value < math.MinInt64-delta {
		return false, ErrCounterOverflow
	}
	c.entries[opID] = delta
	c.value += delta
	return true, nil
}

// Value returns the current sum.
func (c *PNCounter) Value() int64 { return c.value }

// Len returns the number of contributing operations (test/inspection aid).
func (c *PNCounter) Len() int { return len(c.entries) }

// Entries returns a copy of the contributing (opID -> delta) map.
func (c *PNCounter) Entries() map[string]int64 {
	out := make(map[string]int64, len(c.entries))
	for k, v := range c.entries {
		out[k] = v
	}
	return out
}
