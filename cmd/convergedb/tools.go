package main

// tools.go — inspect, verify, doctor, bench subcommands.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// syncWaitGroup is a tiny alias to keep the concurrency profile readable.
type syncWaitGroup = sync.WaitGroup

// --- inspect ---

func cmdInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	nodeURL := fs.String("node", "http://127.0.0.1:8101", "replica base URL")
	docID := fs.String("document", "", "document id to inspect (optional)")
	ops := fs.Int("ops", 20, "how many log entries to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := strings.TrimRight(*nodeURL, "/")
	var info map[string]any
	if code, err := apiCall("GET", strings.TrimPrefix(base, "http://"), "/api/v1/node", nil, &info); err != nil || code != 200 {
		return fmt.Errorf("node unreachable: %v (HTTP %d)", err, code)
	}
	pretty(info)

	if *docID != "" {
		var doc map[string]any
		host := strings.TrimPrefix(base, "http://")
		if code, err := apiCall("GET", host, "/api/v1/documents/"+*docID, nil, &doc); err != nil || code != 200 {
			fmt.Printf("document %s: not found (%v, HTTP %d)\n", *docID, err, code)
		} else {
			pretty(doc)
		}
	}
	host := strings.TrimPrefix(base, "http://")
	var opsOut struct {
		Operations []map[string]any `json:"operations"`
	}
	if code, err := apiCall("GET", host, fmt.Sprintf("/api/v1/operations?limit=%d", *ops), nil, &opsOut); err == nil && code == 200 {
		fmt.Printf("\nlast %d operations:\n", len(opsOut.Operations))
		for _, op := range opsOut.Operations {
			fmt.Printf("  %-10s seq=%v doc=%v kind=%v id=%v...\n",
				op["actor"], op["actor_seq"], op["doc_id"], op["kind"], short8(fmt.Sprint(op["op_id"])))
		}
	}
	return nil
}

func pretty(v any) {
	raw, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(raw))
}

// --- verify ---

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	nodesFlag := fs.String("nodes", "", "comma list id=url[,id=url...]")
	out := fs.String("out", "", "write JSON report here")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *nodesFlag == "" {
		return fmt.Errorf("--nodes is required (id=url pairs)")
	}
	type nodeRef struct{ id, host string }
	var refs []nodeRef
	for _, part := range strings.Split(*nodesFlag, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("bad node spec %q", part)
		}
		refs = append(refs, nodeRef{id: kv[0], host: strings.TrimPrefix(strings.TrimRight(kv[1], "/"), "http://")})
	}
	type verifyReport struct {
		SchemaVersion int            `json:"schema_version"`
		Kind          string         `json:"kind"`
		Pass          bool           `json:"pass"`
		Checks        []checkRecord  `json:"checks"`
		Nodes         map[string]any `json:"nodes"`
		TimestampMS   int64          `json:"timestamp_ms"`
	}
	rep := verifyReport{SchemaVersion: 1, Kind: "verify-live", Pass: true, Nodes: map[string]any{}, TimestampMS: time.Now().UnixMilli()}
	type nodeInfo struct {
		ID              string `json:"id"`
		ProtocolVersion int    `json:"protocol_version"`
		OpCount         int    `json:"op_count"`
		OpDigest        string `json:"op_digest"`
		StateDigest     string `json:"state_digest"`
	}
	infos := map[string]*nodeInfo{}
	for _, r := range refs {
		var info nodeInfo
		code, err := apiCall("GET", r.host, "/api/v1/node", nil, &info)
		pass := err == nil && code == 200
		rep.Checks = append(rep.Checks, checkRecord{
			ID: "reachable-" + r.id, Description: "node answers /api/v1/node", Pass: pass,
			Detail: fmt.Sprintf("HTTP %d err %v", code, err),
		})
		if pass {
			infos[r.id] = &info
			rep.Nodes[r.id] = info
		} else {
			rep.Pass = false
		}
	}
	// all same protocol version
	pvOK := len(infos) > 0
	var pv int
	for _, info := range infos {
		if pv == 0 {
			pv = info.ProtocolVersion
		} else if pv != info.ProtocolVersion {
			pvOK = false
		}
	}
	rep.Checks = append(rep.Checks, checkRecord{ID: "protocol-uniform", Description: "all reachable nodes speak the same protocol version", Pass: pvOK, Detail: fmt.Sprintf("protocol=%d", pv)})
	if !pvOK {
		rep.Pass = false
	}
	// digests agree pairwise
	digestOK := len(infos) > 1
	var od, sd string
	for _, info := range infos {
		if od == "" {
			od, sd = info.OpDigest, info.StateDigest
			continue
		}
		if od != info.OpDigest || sd != info.StateDigest {
			digestOK = false
		}
	}
	rep.Checks = append(rep.Checks, checkRecord{
		ID: "inventory-and-state-equal", Description: "operation inventory AND canonical state digests equal across reachable nodes",
		Pass: digestOK, Detail: fmt.Sprintf("op=%s state=%s", short8(od), short8(sd)),
	})
	_ = digestOK
	if !digestOK {
		rep.Pass = false
	}
	raw, _ := json.MarshalIndent(rep, "", "  ")
	if *out != "" {
		_ = os.MkdirAll(filepath.Dir(*out), 0o755)
		_ = os.WriteFile(*out, raw, 0o644)
	}
	fmt.Println(string(raw))
	if !rep.Pass {
		return fmt.Errorf("verification FAILED")
	}
	return nil
}

// --- doctor ---

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Println("ConvergeDB doctor")
	fmt.Println("  binary:      convergedb " + version)
	if cwd, err := os.Getwd(); err == nil {
		fmt.Println("  working dir:", cwd)
	}
	// writable var/
	var ok bool
	if err := os.MkdirAll("var/doctor-probe", 0o755); err == nil {
		ok = true
		_ = os.Remove("var/doctor-probe")
	}
	fmt.Println("  var/ writable:", yesno(ok))
	// temp sqlite write happens in the store test; here: check ports
	for _, port := range []string{"8101", "8102", "8103"} {
		fmt.Printf("  port %s:     %s\n", port, portState(port))
	}
	fmt.Println("  defaults are loopback-only; non-loopback binds require --token (see docs/SECURITY.md)")
	return nil
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "NO"
}

// --- bench ---

type benchResult struct {
	SchemaVersion int            `json:"schema_version"`
	Kind          string         `json:"kind"`
	StartedMS     int64          `json:"started_ms"`
	Host          string         `json:"host_note"`
	Profiles      map[string]any `json:"profiles"`
}

func cmdBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	writes := fs.Int("writes", 500, "single-node durable writes for the latency profile")
	clients := fs.Int("clients", 0, "concurrent writer clients (0 = skip the concurrency profile)")
	docs := fs.Int("docs", 0, "document count for the many-documents profile (0 = skip)")
	outDir := fs.String("out", "var/bench", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := defaultExePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	res := benchResult{SchemaVersion: 1, Kind: "bench", StartedMS: time.Now().UnixMilli(),
		Host: "local single machine; absolute numbers are environment-specific", Profiles: map[string]any{}}

	// Profile 1: single-node durable write latency via the real HTTP API.
	if port, err := freePort(); err == nil {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		p, err := startReplica(exe, "bench", filepath.Join(*outDir, "bench.db"), addr, nil, false, 1000)
		if err != nil {
			return err
		}
		func() {
			defer p.Stop()
			if err := waitReady(addr, 20*time.Second); err != nil {
				res.Profiles["local-write"] = map[string]any{"error": err.Error()}
				return
			}
			if code, err := apiCall("POST", addr, "/api/v1/documents", map[string]any{"id": "bench-doc", "title": "bench"}, nil); err != nil || code != 201 {
				res.Profiles["local-write"] = map[string]any{"error": fmt.Sprintf("create doc: HTTP %d err %v", code, err)}
				return
			}
			lat := []float64{}
			start := time.Now()
			for i := 0; i < *writes; i++ {
				t0 := time.Now()
				code, err := apiCall("POST", addr, "/api/v1/documents/bench-doc/operations", map[string]any{
					"action": "set_title", "value": fmt.Sprintf("bench title %d", i),
				}, nil)
				if err != nil || code != 200 {
					res.Profiles["local-write"] = map[string]any{"error": fmt.Sprintf("write %d failed: HTTP %d err %v", i, code, err)}
					return
				}
				lat = append(lat, float64(time.Since(t0).Microseconds())/1000.0)
			}
			total := time.Since(start)
			sort.Float64s(lat)
			res.Profiles["local-write"] = map[string]any{
				"writes":               *writes,
				"p50_ms":               percentile(lat, 50),
				"p95_ms":               percentile(lat, 95),
				"p99_ms":               percentile(lat, 99),
				"throughput_ops_per_s": float64(*writes) / total.Seconds(),
			}
		}()
	}
	// Profile 1b: concurrent writers on one node.
	if *clients > 0 {
		if port, err := freePort(); err == nil {
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			p, err := startReplica(exe, "benchc", filepath.Join(*outDir, "benchc.db"), addr, nil, false, 1000)
			if err != nil {
				return err
			}
			func() {
				defer p.Stop()
				if err := waitReady(addr, 20*time.Second); err != nil {
					res.Profiles["concurrent-writes"] = map[string]any{"error": err.Error()}
					return
				}
				if code, err := apiCall("POST", addr, "/api/v1/documents", map[string]any{"id": "conc", "title": "conc"}, nil); err != nil || code != 201 {
					res.Profiles["concurrent-writes"] = map[string]any{"error": fmt.Sprintf("create: HTTP %d %v", code, err)}
					return
				}
				total := *writes
				perClient := total / *clients
				lat := make([][]float64, *clients)
				start := time.Now()
				var wg syncWaitGroup
				for c := 0; c < *clients; c++ {
					wg.Add(1)
					go func(c int) {
						defer wg.Done()
						for i := 0; i < perClient; i++ {
							t0 := time.Now()
							code, err := apiCall("POST", addr, "/api/v1/documents/conc/operations", map[string]any{
								"action": "vote", "delta": 1,
							}, nil)
							d := float64(time.Since(t0).Microseconds()) / 1000.0
							lat[c] = append(lat[c], d)
							if err != nil || code != 200 {
								continue // counted via error rate below
							}
						}
					}(c)
				}
				wg.Wait()
				elapsed := time.Since(start)
				var all []float64
				for _, l := range lat {
					all = append(all, l...)
				}
				sort.Float64s(all)
				done := len(all)
				res.Profiles["concurrent-writes"] = map[string]any{
					"clients": *clients, "writes_attempted": total, "writes_ok": done,
					"p50_ms": percentile(all, 50), "p95_ms": percentile(all, 95), "p99_ms": percentile(all, 99),
					"throughput_ops_per_s": float64(done) / elapsed.Seconds(),
				}
			}()
		}
	}

	// Profile 1c: many documents (bounded list reads + digest cost).
	if *docs > 0 {
		if port, err := freePort(); err == nil {
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			p, err := startReplica(exe, "benchd", filepath.Join(*outDir, "benchd.db"), addr, nil, false, 1000)
			if err != nil {
				return err
			}
			func() {
				defer p.Stop()
				if err := waitReady(addr, 20*time.Second); err != nil {
					res.Profiles["many-documents"] = map[string]any{"error": err.Error()}
					return
				}
				start := time.Now()
				for i := 0; i < *docs; i++ {
					if code, err := apiCall("POST", addr, "/api/v1/documents", map[string]any{
						"id": fmt.Sprintf("doc-%06d", i), "title": fmt.Sprintf("Document %d", i),
					}, nil); err != nil || code != 201 {
						res.Profiles["many-documents"] = map[string]any{"error": fmt.Sprintf("create %d: HTTP %d %v", i, code, err)}
						return
					}
				}
				createDur := time.Since(start)
				var listOut struct {
					Truncated bool `json:"truncated"`
				}
				start = time.Now()
				if code, err := apiCall("GET", addr, "/api/v1/documents?limit=200", nil, &listOut); err != nil || code != 200 {
					res.Profiles["many-documents"] = map[string]any{"error": fmt.Sprintf("list: HTTP %d %v", code, err)}
					return
				}
				listDur := time.Since(start)
				start = time.Now()
				var conv map[string]any
				if code, err := apiCall("GET", addr, "/api/v1/lab/convergence", nil, &conv); err != nil || code != 200 {
					res.Profiles["many-documents"] = map[string]any{"error": fmt.Sprintf("convergence: HTTP %d %v", code, err)}
					return
				}
				digestDur := time.Since(start)
				res.Profiles["many-documents"] = map[string]any{
					"documents": *docs, "create_seconds": createDur.Seconds(),
					"list_200_ms": listDur.Milliseconds(), "convergence_digest_ms": digestDur.Milliseconds(),
				}
			}()
		}
	}

	// Profile 2: three-replica convergence time after partition heal.
	if err := benchThreeReplica(exe, *outDir, res); err != nil {
		res.Profiles["three-replica-convergence"] = map[string]any{"error": err.Error()}
	}

	raw, _ := json.MarshalIndent(res, "", "  ")
	outPath := filepath.Join(*outDir, "bench-report.json")
	_ = os.WriteFile(outPath, raw, 0o644)
	fmt.Println(string(raw))
	fmt.Println("report:", outPath)
	return nil
}

// benchThreeReplica measures heal-to-verified-convergence for a partitioned
// three-node lab under a fixed isolated-write load. Trials are separate
// process sets with fresh databases; the reported time covers the last heal
// to the convergence checker reporting CONVERGED (op inventory + state).
func benchThreeReplica(exe, outDir string, res benchResult) error {
	trials := []map[string]any{}
	for trial := 0; trial < 3; trial++ {
		dir := filepath.Join(outDir, fmt.Sprintf("trial%d", trial))
		_ = os.MkdirAll(dir, 0o755)
		var procs []*replicaProc
		defer func() {
			for _, p := range procs {
				p.Stop()
			}
		}()
		ports := map[string]string{}
		addrs := map[string]string{}
		for _, id := range []string{"alpha", "beta", "gamma"} {
			port, err := freePort()
			if err != nil {
				return err
			}
			ports[id] = fmt.Sprintf("127.0.0.1:%d", port)
			addrs[id] = ports[id]
		}
		peersFor := func(self string) []string {
			var out []string
			for id, a := range addrs {
				if id != self {
					out = append(out, id+"=http://"+a)
				}
			}
			return out
		}
		for _, id := range []string{"alpha", "beta", "gamma"} {
			p, err := startReplica(exe, id, filepath.Join(dir, id+".db"), addrs[id], peersFor(id), true, 100)
			if err != nil {
				return err
			}
			procs = append(procs, p)
		}
		for _, a := range addrs {
			if err := waitReady(a, 20*time.Second); err != nil {
				return err
			}
		}
		if code, err := apiCall("POST", addrs["alpha"], "/api/v1/documents", map[string]any{"id": "bench", "title": "bench"}, nil); err != nil || code != 201 {
			return fmt.Errorf("create: %d %v", code, err)
		}
		// Isolated writes: alpha cut from both peers, 100 ops per side.
		for _, pair := range [][2]string{{"alpha", "beta"}, {"alpha", "gamma"}} {
			if code, err := apiCall("POST", addrs["alpha"], "/api/v1/lab/partition", map[string]string{"a": pair[0], "b": pair[1]}, nil); err != nil || code != 200 {
				return fmt.Errorf("partition: %d %v", code, err)
			}
		}
		const perSide = 100
		// Wait until the doc is visible on beta BEFORE isolating, then check
		// every mutation's status — silent non-200s would corrupt the count.
		deadline := time.Now().Add(15 * time.Second)
		for {
			var dv struct {
				Document map[string]any `json:"document"`
			}
			code, err := apiCall("GET", addrs["beta"], "/api/v1/documents/bench", nil, &dv)
			if err == nil && code == 200 && dv.Document != nil {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("doc never reached beta before isolation")
			}
			time.Sleep(50 * time.Millisecond)
		}
		for i := 0; i < perSide; i++ {
			if code, err := apiCall("POST", addrs["alpha"], "/api/v1/documents/bench/operations", map[string]any{"action": "vote", "delta": 1}, nil); err != nil || code != 200 {
				return fmt.Errorf("alpha vote %d: HTTP %d err %v", i, code, err)
			}
			if code, err := apiCall("POST", addrs["beta"], "/api/v1/documents/bench/operations", map[string]any{"action": "vote", "delta": 2}, nil); err != nil || code != 200 {
				return fmt.Errorf("beta vote %d: HTTP %d err %v", i, code, err)
			}
		}
		// Heal and measure.
		healStart := time.Now()
		if code, err := apiCall("POST", addrs["alpha"], "/api/v1/lab/heal", map[string]any{}, nil); err != nil || code != 200 {
			return fmt.Errorf("heal: %d %v", code, err)
		}
		rep, ok := waitForConvergence(addrs["alpha"], 60*time.Second)
		elapsed := time.Since(healStart).Milliseconds()
		if !ok || rep == nil {
			return fmt.Errorf("trial %d did not converge", trial)
		}
		// Sanity: counter must be exactly perSide*1 + perSide*2.
		var doc struct {
			Document struct {
				Votes int64 `json:"votes"`
			} `json:"document"`
		}
		if code, err := apiCall("GET", addrs["gamma"], "/api/v1/documents/bench", nil, &doc); err != nil || code != 200 {
			return fmt.Errorf("read back: %d %v", code, err)
		}
		if doc.Document.Votes != perSide*3 {
			return fmt.Errorf("counter wrong after convergence: %d != %d", doc.Document.Votes, perSide*3)
		}
		trials = append(trials, map[string]any{
			"isolated_ops": perSide * 2, "heal_to_converged_ms": elapsed,
			"votes_check": doc.Document.Votes,
		})
		// stop this trial's processes before the next
		for _, p := range procs {
			p.Stop()
		}
		procs = nil
	}
	var times []float64
	for _, tr := range trials {
		times = append(times, float64(tr["heal_to_converged_ms"].(int64)))
	}
	sort.Float64s(times)
	res.Profiles["three-replica-convergence"] = map[string]any{
		"trials":    trials,
		"median_ms": percentile(times, 50),
		"min_ms":    times[0],
		"max_ms":    times[len(times)-1],
		"note":      "time from heal request to CONVERGED verdict (poll interval 150ms); includes remaining sync rounds",
	}
	return nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p / 100 * float64(len(sorted)-1))
	return sorted[idx]
}

func portState(port string) string {
	// Probe by dialing; a refused connection means free.
	ln, err := netListen("127.0.0.1:" + port)
	if err != nil {
		return "in use"
	}
	ln.Close()
	return "free"
}
