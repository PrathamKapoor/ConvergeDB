package main

// harness.go — subprocess + HTTP helpers shared by demo, verify and bench.
// These helpers manage REAL separate OS processes; nothing here fakes a
// replica.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type replicaProc struct {
	ID   string
	Addr string
	DB   string
	cmd  *exec.Cmd
	log  *os.File
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// startReplica launches one convergedb serve child process.
func startReplica(selfExe, id, dbPath, addr string, peers []string, lab bool, intervalMS int) (*replicaProc, error) {
	args := []string{"serve", "--id", id, "--db", dbPath, "--http", addr,
		"--sync-interval-ms", fmt.Sprint(intervalMS)}
	if len(peers) > 0 {
		args = append(args, "--peers", strings.Join(peers, ","))
	}
	if lab {
		args = append(args, "--lab")
	}
	logPath := dbPath + ".log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(selfExe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start replica %s: %w", id, err)
	}
	return &replicaProc{ID: id, Addr: addr, DB: dbPath, cmd: cmd, log: logFile}, nil
}

func (rp *replicaProc) Stop() {
	if rp.cmd != nil && rp.cmd.Process != nil {
		_ = rp.cmd.Process.Kill()
		_, _ = rp.cmd.Process.Wait()
	}
	if rp.log != nil {
		rp.log.Close()
	}
}

// waitReady polls /health/ready until it answers or the timeout elapses.
func waitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://%s/health/ready", addr)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("replica at %s not ready within %s", addr, timeout)
}

// apiCall performs one JSON HTTP request against a replica.
func apiCall(method, addr, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = strings.NewReader(string(raw))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://%s%s", addr, path), reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return resp.StatusCode, err
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", path, err)
		}
	} else {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	}
	return resp.StatusCode, nil
}

// nodeExistsIn reports whether the document is visible on a replica.
func nodeHasDoc(addr, docID string) bool {
	var out struct {
		Document map[string]any `json:"document"`
	}
	code, err := apiCall("GET", addr, "/api/v1/documents/"+docID, nil, &out)
	return err == nil && code == 200 && out.Document != nil
}

func docTitleHLC(addr, docID string) (title string, hlcWall int64, ok bool) {
	var out struct {
		Document struct {
			Title struct {
				Value string `json:"value"`
				HLC   struct {
					Wall int64 `json:"wall_ms"`
				} `json:"hlc"`
			} `json:"title"`
		} `json:"document"`
	}
	code, err := apiCall("GET", addr, "/api/v1/documents/"+docID, nil, &out)
	if err != nil || code != 200 {
		return "", 0, false
	}
	return out.Document.Title.Value, out.Document.Title.HLC.Wall, true
}

type convergenceReport struct {
	Status      string `json:"status"`
	CheckedAtMS int64  `json:"checked_at_ms"`
	Self        struct {
		ID          string `json:"id"`
		OpCount     int    `json:"op_count"`
		OpDigest    string `json:"op_digest"`
		StateDigest string `json:"state_digest"`
	} `json:"self"`
	Peers []struct {
		ID          string `json:"id"`
		Reachable   bool   `json:"reachable"`
		OpCount     int    `json:"op_count"`
		OpDigest    string `json:"op_digest"`
		StateDigest string `json:"state_digest"`
		MatchesSelf bool   `json:"matches_self"`
		Error       string `json:"error"`
	} `json:"peers"`
}

func getConvergence(addr string) (*convergenceReport, error) {
	var rep convergenceReport
	code, err := apiCall("GET", addr, "/api/v1/lab/convergence", nil, &rep)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("convergence: HTTP %d", code)
	}
	return &rep, nil
}

// waitForConvergence polls until CONVERGED or timeout. Returns the last report.
func waitForConvergence(addr string, timeout time.Duration) (*convergenceReport, bool) {
	deadline := time.Now().Add(timeout)
	var last *convergenceReport
	for time.Now().Before(deadline) {
		rep, err := getConvergence(addr)
		if err == nil {
			last = rep
			if rep.Status == "CONVERGED" {
				return rep, true
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return last, false
}

func defaultExePath() (string, error) {
	return os.Executable()
}

func ensureDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

// netListen is a tiny indirection for port probing.
func netListen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
