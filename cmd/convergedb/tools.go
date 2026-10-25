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
	"time"
)

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

func cmdBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	writes := fs.Int("writes", 500, "single-node durable writes for the latency profile")
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
	type benchResult struct {
		SchemaVersion int            `json:"schema_version"`
		Kind          string         `json:"kind"`
		StartedMS     int64          `json:"started_ms"`
		Host          string         `json:"host_note"`
		Profiles      map[string]any `json:"profiles"`
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
	raw, _ := json.MarshalIndent(res, "", "  ")
	outPath := filepath.Join(*outDir, "bench-report.json")
	_ = os.WriteFile(outPath, raw, 0o644)
	fmt.Println(string(raw))
	fmt.Println("report:", outPath)
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
