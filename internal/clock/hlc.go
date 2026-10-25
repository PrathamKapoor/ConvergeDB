// Package clock implements the hybrid logical clock (HLC) used by ConvergeDB.
//
// Timestamps are totally ordered by (Wall, Counter, Actor): Wall is
// milliseconds since the Unix epoch, Counter disambiguates events issued
// within the same millisecond by one replica, and Actor (the replica ID) is
// the final deterministic tie-break so that timestamps issued by different
// replicas always compare strictly. The ordering is part of the canonical
// convergence contract: every replica derives the same winner from the same
// set of timestamps without communication.
//
// Wall-clock rollback is tolerated: if the physical clock moves backwards,
// the last issued Wall is retained and Counter advances, so issued
// timestamps remain monotonically non-decreasing. The clock only bounds
// ordering; it is NOT proof of real-time causality across partitions.
package clock

import (
	"sync"
	"time"
)

// HLC is a hybrid logical clock timestamp. The JSON field order below is
// part of ConvergeDB's canonical operation encoding and must not change
// within protocol version 1.
type HLC struct {
	Wall    int64  `json:"wall_ms"`
	Counter int64  `json:"counter"`
	Actor   string `json:"actor"`
}

// Compare returns -1 if a sorts before b, 1 if a sorts after b, 0 if equal.
func Compare(a, b HLC) int {
	switch {
	case a.Wall != b.Wall:
		if a.Wall < b.Wall {
			return -1
		}
		return 1
	case a.Counter != b.Counter:
		if a.Counter < b.Counter {
			return -1
		}
		return 1
	case a.Actor != b.Actor:
		if a.Actor < b.Actor {
			return -1
		}
		return 1
	}
	return 0
}

// Clock is a mutable HLC bound to a single replica. It is safe for
// concurrent use.
type Clock struct {
	mu    sync.Mutex
	actor string
	last  HLC
	now   func() int64
}

// New returns a clock for actor. nowFn supplies wall milliseconds; nil uses
// time.Now.
func New(actor string, nowFn func() int64) *Clock {
	if nowFn == nil {
		nowFn = func() int64 { return time.Now().UnixMilli() }
	}
	return &Clock{actor: actor, now: nowFn, last: HLC{Actor: actor}}
}

// Restore returns a clock seeded from a persisted timestamp so that a
// restarted replica never issues timestamps below its pre-restart ones.
// The Actor of issued timestamps is always actor (this replica), regardless
// of the persisted timestamp's actor field.
func Restore(actor string, persisted HLC, nowFn func() int64) *Clock {
	c := New(actor, nowFn)
	c.last.Wall = persisted.Wall
	c.last.Counter = persisted.Counter
	return c
}

// Last returns the most recent state of the clock; callers persist this next
// to the operation log so it survives restarts.
func (c *Clock) Last() HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// Now issues the timestamp for a local event (e.g. a local write). If the
// physical clock has not advanced past the last issued wall value (same
// millisecond or rollback), the counter advances instead.
func (c *Clock) Now() HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	phys := c.now()
	if phys > c.last.Wall {
		c.last.Wall = phys
		c.last.Counter = 0
	} else {
		c.last.Counter++
	}
	return c.last
}

// Observe records that a remote timestamp has been seen. It raises the
// internal watermark so subsequent local events order strictly above
// everything observed so far. Observe issues no timestamp by itself;
// receiving a remote operation is not a local operation.
func (c *Clock) Observe(remote HLC) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if Compare(remote, c.last) > 0 {
		c.last.Wall = remote.Wall
		c.last.Counter = remote.Counter
	}
}
