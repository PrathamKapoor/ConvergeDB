package clock

import "testing"

func testClock(actor string, ms []int64) *Clock {
	i := 0
	return New(actor, func() int64 {
		// Replays the supplied sequence, clamping at the last value.
		if i >= len(ms) {
			return ms[len(ms)-1]
		}
		v := ms[i]
		i++
		return v
	})
}

func TestNowAdvancesCounterWithinSameMillisecond(t *testing.T) {
	c := testClock("alpha", []int64{1000, 1000, 1000})
	a := c.Now()
	b := c.Now()
	if !(a.Wall == 1000 && a.Counter == 0) {
		t.Fatalf("first tick = %+v, want {1000 0}", a)
	}
	if !(b.Wall == 1000 && b.Counter == 1) {
		t.Fatalf("second tick = %+v, want {1000 1}", b)
	}
	if Compare(a, b) >= 0 {
		t.Fatalf("timestamps within one millisecond must strictly increase")
	}
}

func TestRollbackKeepsIssuedTimestampsMonotonic(t *testing.T) {
	// Wall clock moves 1000 -> 999: issued timestamps must not regress.
	c := testClock("alpha", []int64{1000, 999, 999})
	first := c.Now()
	second := c.Now()
	if second.Wall != 1000 || second.Counter != first.Counter+1 {
		t.Fatalf("after rollback got %+v, want wall=1000 counter=%d+1", second, first.Counter)
	}
	if Compare(first, second) >= 0 {
		t.Fatalf("rollback must preserve strict monotonicity")
	}
}

func TestObserveRaisesWatermarkAboveRemote(t *testing.T) {
	c := testClock("alpha", []int64{1000, 1000})
	c.Now()
	// A peer far ahead (skewed clock) is observed.
	c.Observe(HLC{Wall: 5000, Counter: 7, Actor: "beta"})
	next := c.Now()
	if !(next.Wall == 5000 && next.Counter == 8 && next.Actor == "alpha") {
		t.Fatalf("after observing remote %+v got %+v, want wall=5000 counter=8 actor=alpha", HLC{Wall: 5000, Counter: 7, Actor: "beta"}, next)
	}
}

func TestObserveWithStaleRemoteDoesNotRegress(t *testing.T) {
	c := testClock("alpha", []int64{9000})
	local := c.Now()
	c.Observe(HLC{Wall: 5, Counter: 5, Actor: "beta"})
	if got := c.Last(); got.Wall != local.Wall {
		t.Fatalf("stale remote regressed watermark: %+v", got)
	}
}

func TestRestoreResumesAbovePersistedState(t *testing.T) {
	// Physical clock behind persisted state (e.g. reboot with skew).
	c := Restore("alpha", HLC{Wall: 7000, Counter: 3, Actor: "alpha"}, func() int64 { return 6000 })
	got := c.Now()
	if !(got.Wall == 7000 && got.Counter == 4) {
		t.Fatalf("restored clock issued %+v, want {7000 4}", got)
	}
	// Physical clock ahead of persisted state.
	c2 := Restore("alpha", HLC{Wall: 100, Counter: 9, Actor: "alpha"}, func() int64 { return 200 })
	got2 := c2.Now()
	if !(got2.Wall == 200 && got2.Counter == 0) {
		t.Fatalf("clock ahead of persisted state issued %+v, want {200 0}", got2)
	}
}

func TestCompareTotalOrder(t *testing.T) {
	a := HLC{Wall: 1, Counter: 0, Actor: "alpha"}
	b := HLC{Wall: 1, Counter: 0, Actor: "beta"}
	c := HLC{Wall: 1, Counter: 1, Actor: "alpha"}
	d := HLC{Wall: 2, Counter: 0, Actor: "aardvark"}
	if !(Compare(a, b) < 0 && Compare(b, c) < 0 && Compare(c, d) < 0 && Compare(a, a) == 0) {
		t.Fatalf("total order violated: a<b, b<c, c<d must all hold")
	}
	if Compare(HLC{Actor: "x"}, HLC{Actor: "x"}) != 0 {
		t.Fatalf("identical timestamps must compare equal")
	}
}
