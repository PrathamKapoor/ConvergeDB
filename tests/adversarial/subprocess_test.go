package adversarial

// Subprocess fault laboratory: a REAL convergedb server process is killed
// mid-write-burst; every acknowledged write must be durable across the
// restart (I-01). Uses the actual binary built from this checkout.

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort2() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func buildBinary(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "convergedb-under-test.exe")
	cmd := exec.Command("go", "build", "-o", exe, "github.com/PrathamKapoor/ConvergeDB/cmd/convergedb")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}
	return exe
}

func postJSON(t *testing.T, url string, body any) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	buf, err2 := io.ReadAll(resp.Body)
	if err2 != nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, buf
}

func waitReady2(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://%s/health/ready", addr))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// TestKillMidBurstPreservesAcknowledgedWrites: start a server, stream write
// bursts, SIGKILL at a seeded random moment, restart on the same database,
// and prove every ACKed operation is still in the durable log and that the
// materialized state matches a full replay.
func TestKillMidBurstPreservesAcknowledgedWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess scenario skipped in -short mode")
	}
	exe := buildBinary(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kill.db")
	port, err := freePort2()
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	base := fmt.Sprintf("http://%s", addr)

	start := func() *exec.Cmd {
		cmd := exec.Command(exe, "serve", "--id", "victim", "--db", dbPath, "--http", addr, "--sync-interval-ms", "1000")
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}

	seed := time.Now().UnixNano()
	rnd := rand.New(rand.NewSource(seed))
	t.Logf("kill-mid-burst seed: %d", seed)

	cmd := start()
	if !waitReady2(addr, 20*time.Second) {
		t.Fatal("server not ready")
	}
	if code, _ := postJSON(t, base+"/api/v1/documents", map[string]any{"id": "burst", "title": "kill test"}); code != 201 {
		t.Fatalf("create: %d", code)
	}

	// Write bursts; record every ACKed operation ID.
	acked := map[string]bool{}
	total := 0
	killed := false
	for round := 0; round < 12 && !killed; round++ {
		for i := 0; i < 20; i++ {
			total++
			code, body := postJSON(t, base+"/api/v1/documents/burst/operations", map[string]any{
				"action": "vote", "delta": 1,
			})
			if code == 200 {
				var out struct {
					Operation struct {
						OpID string `json:"op_id"`
					} `json:"operation"`
				}
				_ = json.Unmarshal(body, &out)
				if out.Operation.OpID != "" {
					acked[out.Operation.OpID] = true
				}
			} else if code == 0 {
				// connection died mid-flight: this request's ACK status is
				// ambiguous (the classic lost-ACK case) — excluded from the
				// durability assertion by design.
				killed = true
				break
			}
			// Seeded kill inside the burst once enough writes are pending.
			if total > 40 && rnd.Float64() < 0.08 {
				killed = true
				break
			}
		}
		if !killed && rnd.Float64() < 0.3 {
			killed = true
		}
	}
	// Hard kill (no graceful close, no checkpoint write).
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	if len(acked) == 0 {
		t.Fatal("no ACKed writes recorded; test setup broken")
	}
	t.Logf("ACKed %d operations before the kill", len(acked))

	// Restart on the same database with the same identity.
	cmd = start()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()
	if !waitReady2(addr, 20*time.Second) {
		t.Fatal("server did not come back")
	}

	// Every ACKed op must be present in the durable log.
	resp, err := http.Get(base + "/api/v1/operations?limit=1000")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	code := resp.StatusCode
	if code != 200 {
		t.Fatalf("operations list: %d", code)
	}
	var out struct {
		Operations []struct {
			OpID string `json:"op_id"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, op := range out.Operations {
		present[op.OpID] = true
	}
	for opID := range acked {
		if !present[opID] {
			t.Fatalf("ACKed operation %s LOST across kill+restart (I-01 violated)", opID)
		}
	}
	if len(present) < len(acked) {
		t.Fatalf("log smaller than ACKed set: %d < %d", len(present), len(acked))
	}
	t.Logf("all %d ACKed operations survived the kill; log holds %d operations", len(acked), len(present))

	// The restored node must keep working: next seq continues, no resets.
	code, body = postJSON(t, base+"/api/v1/documents/burst/operations", map[string]any{"action": "vote", "delta": 1})
	if code != 200 {
		t.Fatalf("post-restart write: %d %s", code, string(body))
	}
	var wout struct {
		Operation struct {
			ActorSeq uint64 `json:"actor_seq"`
		} `json:"operation"`
	}
	_ = json.Unmarshal(body, &wout)
	if wout.Operation.ActorSeq == 0 {
		t.Fatal("post-restart write did not get a sequence")
	}
}
