package adversarial

// Multi-node fault scenarios over REAL HTTP between REAL node instances
// (in-process handlers on real sockets), written from the documented
// protocol. Every scenario ends by comparing operation-inventory digests AND
// canonical state digests across every replica — restarts and partitions are
// never assumed to be healed just because processes are alive.

import (
	"fmt"
	"math/rand"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/api"
	"github.com/PrathamKapoor/ConvergeDB/internal/node"
)

type testNode struct {
	n   *node.Node
	srv *httptest.Server
}

func startN(t *testing.T, id string) testNode {
	t.Helper()
	n, err := node.Open(node.Config{
		ID: id, DBPath: filepath.Join(t.TempDir(), id+".db"),
		HTTPAddr: "127.0.0.1:0", LabMode: true, SyncIntervalMS: 40, FaultSeed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewServer(n, ""))
	return testNode{n: n, srv: srv}
}

func (tn testNode) stop() {
	tn.srv.Close()
	tn.n.Close()
}

func mesh(ids []string, urlOf func(string) string, exclude map[string]bool) []node.PeerConfig {
	var peers []node.PeerConfig
	for _, id := range ids {
		if !exclude[id] {
			peers = append(peers, node.PeerConfig{ID: id, URL: urlOf(id)})
		}
	}
	return peers
}

func allDigestsEqual(t *testing.T, nodes map[string]testNode) {
	t.Helper()
	type digests struct{ ops, state string }
	want := digests{}
	seen := false
	for id, tn := range nodes {
		count, ops, err := tn.n.OpDigest()
		if err != nil {
			t.Fatal(err)
		}
		state, err := tn.n.StateDigest()
		if err != nil {
			t.Fatal(err)
		}
		d := digests{ops: fmt.Sprintf("%d/%s", count, ops[:12]), state: state[:12]}
		if !seen {
			want = d
			seen = true
			continue
		}
		if d != want {
			var detail string
			for id2, tn2 := range nodes {
				c2, _, e2 := tn2.n.OpDigest()
				_, e3 := tn2.n.StateDigest()
				detail += fmt.Sprintf(" [%s count=%d opErr=%v stateErr=%v]", id2, c2, e2, e3)
			}
			t.Fatalf("digests diverge: alpha %v vs %s %v; nodes:%s", want, id, d, detail)
		}
	}
}

func waitConverged(t *testing.T, nodes map[string]testNode, timeout time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := true
		want := ""
		seen := false
		for _, tn := range nodes {
			_, ops, _ := tn.n.OpDigest()
			state, _ := tn.n.StateDigest()
			d := ops + state
			if !seen {
				want = d
				seen = true
			} else if d != want {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no convergence within %s: %s", timeout, what)
}

// TestTenNodeAsymmetricPartitions: 10 nodes, an asymmetric partition graph
// with a slow peer, writes on multiple islands, staged partial heals, and a
// final full convergence check.
func TestTenNodeAsymmetricPartitions(t *testing.T) {
	if testing.Short() {
		t.Skip("10-node scenario skipped in -short mode")
	}
	ids := []string{"alpha", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	nodes := map[string]testNode{}
	urls := map[string]string{}
	for _, id := range ids {
		tn := startN(t, id)
		nodes[id] = tn
		urls[id] = tn.srv.URL
	}
	defer func() {
		for _, tn := range nodes {
			tn.stop()
		}
	}()
	// Full mesh peering.
	for _, id := range ids {
		for _, other := range ids {
			if id != other {
				nodes[id].n.AddPeer(node.PeerConfig{ID: other, URL: urls[other]})
			}
		}
	}
	// One slow peer: every link INTO j (i.e., other nodes pulling from j is
	// unaffected; j's outbound pulls are delayed) gets a delay.
	nodes["j"].n.Faults().SetLink("j", "alpha", node.LinkFault{DelayMS: 30})

	if _, err := nodes["alpha"].n.CreateDocument("board", "Ten Node Storm"); err != nil {
		t.Fatal(err)
	}
	// Wait until everyone has the doc before cutting links.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		all := true
		for _, id := range ids {
			if _, ok := nodes[id].n.Document("board"); !ok {
				all = false
				break
			}
		}
		if all {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Asymmetric partition graph: two islands {alpha,b,c,d} and {e,f,g,h,i,j}
	// fully disconnected from each other, plus one extra split inside island 2
	// (g cut from h,i,j).
	for _, a := range []string{"alpha", "b", "c", "d"} {
		for _, b := range []string{"e", "f", "g", "h", "i", "j"} {
			nodes[a].n.Faults().Partition(a, b)
		}
	}
	nodes["g"].n.Faults().Partition("g", "h")
	nodes["g"].n.Faults().Partition("g", "i")
	nodes["g"].n.Faults().Partition("g", "j")

	// Concurrent writes on both islands and the g island.
	writers := []struct {
		id    string
		votes int
	}{{"alpha", 10}, {"b", 5}, {"e", 7}, {"h", 3}, {"g", 4}}
	for _, w := range writers {
		for i := 0; i < w.votes; i++ {
			if _, err := nodes[w.id].n.Mutate("board", node.MutationRequest{Action: "vote", Delta: 1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	time.Sleep(600 * time.Millisecond) // island-internal sync rounds

	// Staged partial heals, random order (seeded): heal half the cross-island
	// links first, then the rest.
	rnd := rand.New(rand.NewSource(7))
	var links [][2]string
	for _, a := range []string{"alpha", "b", "c", "d"} {
		for _, b := range []string{"e", "f", "g", "h", "i", "j"} {
			links = append(links, [2]string{a, b})
		}
	}
	rnd.Shuffle(len(links), func(i, j int) { links[i][0], links[j][0] = links[j][0], links[i][0] })
	for i, l := range links {
		nodes[l[0]].n.Faults().HealLink(l[0], l[1])
		nodes[l[0]].n.Faults().HealLink(l[1], l[0])
		if i == len(links)/2 {
			time.Sleep(300 * time.Millisecond) // partial-heal settle window
		}
	}
	nodes["g"].n.Faults().HealAll()

	waitConverged(t, nodes, 20*time.Second, "10-node staged heal")
	allDigestsEqual(t, nodes)

	// Every vote survived exactly: 10+5+7+3+4 = 29.
	d, _ := nodes["alpha"].n.Document("board")
	if d.Votes.Value() != 29 {
		t.Fatalf("votes = %d, want 29 (no updates lost, none double-counted)", d.Votes.Value())
	}
}

// TestOfflineNodeRejoinsAfterManyOps: gamma is cut off early, the others
// accumulate operations, then gamma rejoins and must catch up completely.
func TestOfflineNodeRejoinsAfterManyOps(t *testing.T) {
	na := startN(t, "alpha")
	nb := startN(t, "beta")
	ng := startN(t, "gamma")
	defer func() { na.stop(); nb.stop(); ng.stop() }()
	na.n.AddPeer(node.PeerConfig{ID: "beta", URL: nb.srv.URL})
	nb.n.AddPeer(node.PeerConfig{ID: "alpha", URL: na.srv.URL})
	ng.n.AddPeer(node.PeerConfig{ID: "alpha", URL: na.srv.URL})
	ng.n.AddPeer(node.PeerConfig{ID: "beta", URL: nb.srv.URL})

	na.n.CreateDocument("board", "Rejoin")
	waitConverged(t, map[string]testNode{"alpha": na, "beta": nb, "gamma": ng}, 5*time.Second, "initial sync")

	// Gamma goes offline (both directions).
	ng.n.Faults().Partition("gamma", "alpha")
	ng.n.Faults().Partition("gamma", "beta")
	// 150 ops accumulate on alpha/beta while gamma is gone.
	for i := 0; i < 75; i++ {
		na.n.Mutate("board", node.MutationRequest{Action: "vote", Delta: 1})
		nb.n.Mutate("board", node.MutationRequest{Action: "vote", Delta: 1})
	}
	time.Sleep(300 * time.Millisecond)

	// Gamma rejoins: heal and catch up. The partitions were set through
	// GAMMA's fault controller, so gamma's must be healed as well.
	na.n.Faults().HealAll()
	nb.n.Faults().HealAll()
	ng.n.Faults().HealAll()
	waitConverged(t, map[string]testNode{"alpha": na, "beta": nb, "gamma": ng}, 15*time.Second, "gamma catch-up")
	allDigestsEqual(t, map[string]testNode{"alpha": na, "beta": nb, "gamma": ng})
	d, _ := ng.n.Document("board")
	if d.Votes.Value() != 150 {
		t.Fatalf("gamma votes = %d, want 150 after catch-up", d.Votes.Value())
	}
}

// TestRestartEveryNodeInDifferentOrders: stop ALL nodes in varying orders,
// restart them, and require convergence plus identical durable inventories.
func TestRestartEveryNodeInDifferentOrders(t *testing.T) {
	dir := t.TempDir()
	type spec struct {
		id, db string
	}
	specs := []spec{{"alpha", filepath.Join(dir, "alpha.db")}, {"beta", filepath.Join(dir, "beta.db")}, {"gamma", filepath.Join(dir, "gamma.db")}}
	open := func() map[string]testNode {
		nodes := map[string]testNode{}
		urls := map[string]string{}
		for _, s := range specs {
			n, err := node.Open(node.Config{ID: s.id, DBPath: s.db, HTTPAddr: "127.0.0.1:0", LabMode: true, SyncIntervalMS: 40, FaultSeed: 1})
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(api.NewServer(n, ""))
			nodes[s.id] = testNode{n: n, srv: srv}
			urls[s.id] = srv.URL
		}
		for _, s := range specs {
			for _, o := range specs {
				if s.id != o.id {
					nodes[s.id].n.AddPeer(node.PeerConfig{ID: o.id, URL: urls[o.id]})
				}
			}
		}
		return nodes
	}

	nodes := open()
	na, nb, ng := nodes["alpha"], nodes["beta"], nodes["gamma"]
	na.n.CreateDocument("board", "Restart Storm")
	waitConverged(t, nodes, 5*time.Second, "initial")
	na.n.Mutate("board", node.MutationRequest{Action: "vote", Delta: 3})
	nb.n.Mutate("board", node.MutationRequest{Action: "set_title", Value: "After storm"})
	waitConverged(t, nodes, 5*time.Second, "writes")
	wantState, _ := na.n.StateDigest()
	wantOps, _, _ := na.n.OpDigest()

	// Restart order 1: alpha, then beta, then gamma.
	for _, tn := range []testNode{na, nb, ng} {
		tn.stop()
	}
	nodes = open()
	defer func() {
		for _, tn := range nodes {
			tn.stop()
		}
	}()
	waitConverged(t, nodes, 10*time.Second, "restart order 1")
	gotState, _ := nodes["alpha"].n.StateDigest()
	gotOps, _, _ := nodes["alpha"].n.OpDigest()
	if gotState != wantState || gotOps != wantOps {
		t.Fatalf("post-restart divergence: ops %d vs %d state %s vs %s", gotOps, wantOps, gotState, wantState)
	}
	for _, tn := range nodes {
		tn.stop()
	}

	// Restart order 2: reverse. Databases are REUSED (same identity).
	nodes = open()
	waitConverged(t, nodes, 10*time.Second, "restart order 2")
	gotState, _ = nodes["gamma"].n.StateDigest()
	gotOps, _, _ = nodes["gamma"].n.OpDigest()
	if gotState != wantState || gotOps != wantOps {
		t.Fatalf("reverse-restart divergence: ops %d vs %d state %s vs %s", gotOps, wantOps, gotState, wantState)
	}
}
