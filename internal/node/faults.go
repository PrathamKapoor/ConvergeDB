package node

import (
	"math/rand"
	"sync"
	"time"
)

// LinkFault describes the imposed behavior of ONE DIRECTION of a sync link
// (from -> to). It gates the sync transport only: local writes are never
// affected, so partitioned replicas keep accepting work (invariant I-08).
//
// Honesty note: the fault controller is a transport shim inside each node's
// sync client, not a firewall. Every gated exchange is still a real HTTP
// request between real processes when it is allowed through; when it is
// dropped, no request is sent at all. It simulates the NETWORK, not the
// nodes.
type LinkFault struct {
	Partitioned   bool    `json:"partitioned"`
	DelayMS       int     `json:"delay_ms"`
	DropRate      float64 `json:"drop_rate"`
	DuplicateRate float64 `json:"duplicate_rate"`
	Reorder       bool    `json:"reorder"`
}

type linkKey struct{ from, to string }

// FaultController holds per-direction link faults with a seeded RNG so
// scenarios are reproducible.
type FaultController struct {
	mu    sync.Mutex
	links map[linkKey]LinkFault
	rnd   *rand.Rand
	seq   uint64
}

func NewFaultController(seed int64) *FaultController {
	return &FaultController{
		links: map[linkKey]LinkFault{},
		rnd:   rand.New(rand.NewSource(seed)),
	}
}

// Partition severs the link between a and b in BOTH directions (the usual
// meaning of "partition a from b").
func (f *FaultController) Partition(a, b string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links[linkKey{a, b}] = LinkFault{Partitioned: true}
	f.links[linkKey{b, a}] = LinkFault{Partitioned: true}
	f.seq++
}

// HealLink clears all faults on one directed link.
func (f *FaultController) HealLink(from, to string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.links, linkKey{from, to})
	f.seq++
}

// HealAll clears every fault.
func (f *FaultController) HealAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links = map[linkKey]LinkFault{}
	f.seq++
}

// SetLink installs (or replaces) the fault for one directed link.
func (f *FaultController) SetLink(from, to string, lf LinkFault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if lf == (LinkFault{}) {
		delete(f.links, linkKey{from, to})
	} else {
		f.links[linkKey{from, to}] = lf
	}
	f.seq++
}

// Gate is the decision for one attempted sync exchange.
type Gate struct {
	Allowed   bool
	DelayMS   int
	Duplicate bool
	Reorder   bool
}

// Gate consults (and advances) the seeded RNG for the from->to direction.
func (f *FaultController) Gate(from, to string) Gate {
	f.mu.Lock()
	defer f.mu.Unlock()
	lf, ok := f.links[linkKey{from, to}]
	if !ok {
		return Gate{Allowed: true}
	}
	if lf.Partitioned {
		return Gate{Allowed: false}
	}
	g := Gate{Allowed: true, DelayMS: lf.DelayMS, Reorder: lf.Reorder}
	if lf.DropRate > 0 && f.rnd.Float64() < lf.DropRate {
		g.Allowed = false
		return g
	}
	if lf.DuplicateRate > 0 && f.rnd.Float64() < lf.DuplicateRate {
		g.Duplicate = true
	}
	return g
}

// Snapshot returns the current fault map for inspection/UI.
func (f *FaultController) Snapshot() map[string]LinkFault {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]LinkFault{}
	for k, v := range f.links {
		out[k.from+"->"+k.to] = v
	}
	return out
}

// Version increments on every fault change (UI freshness signal).
func (f *FaultController) Version() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq
}

// PartitionRequest is the body of POST /api/v1/lab/partition.
type PartitionRequest struct {
	A string `json:"a"`
	B string `json:"b"`
}

// FaultRequest is the body of POST /api/v1/lab/faults.
type FaultRequest struct {
	From          string  `json:"from"`
	To            string  `json:"to"`
	Partitioned   bool    `json:"partitioned"`
	DelayMS       int     `json:"delay_ms"`
	DropRate      float64 `json:"drop_rate"`
	DuplicateRate float64 `json:"duplicate_rate"`
	Reorder       bool    `json:"reorder"`
}

// maybeDelay sleeps out the configured link delay.
func maybeDelay(ms int) {
	if ms > 0 {
		if ms > 5000 {
			ms = 5000
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}
