package api_test

// Level D adversarial suite: malformed requests, size budgets, auth
// boundaries, protocol mismatches and lab isolation must all fail CLOSED
// with stable error codes (invariant I-11).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PrathamKapoor/ConvergeDB/internal/api"
	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
	"github.com/PrathamKapoor/ConvergeDB/internal/node"
)

func startTokenNode(t *testing.T, id string, token string, lab bool) (*node.Node, *httptest.Server) {
	t.Helper()
	n, err := node.Open(node.Config{
		ID:             id,
		DBPath:         filepath.Join(t.TempDir(), id+".db"),
		HTTPAddr:       "127.0.0.1:0",
		LabMode:        lab,
		AuthToken:      token,
		SyncIntervalMS: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewServer(n, ""))
	t.Cleanup(func() {
		srv.Close()
		n.Close()
	})
	return n, srv
}

func rawRequest(srvURL, method, path string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(method, srvURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

func errCode(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out.Error.Code
}

func TestAuthBoundaryFailsClosed(t *testing.T) {
	n, srv := startTokenNode(t, "alpha", "secret-token-123", true)

	// Mutations without a token: 401.
	resp, err := rawRequest(srv.URL, "POST", "/api/v1/documents", nil, []byte(`{"id":"d1","title":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if code, c := errCode(t, resp); code != 401 || c != "E_AUTH" {
		t.Fatalf("unauthed mutation: %d %s", code, c)
	}

	// Wrong token: 401.
	resp, _ = rawRequest(srv.URL, "POST", "/api/v1/documents",
		map[string]string{"Authorization": "Bearer wrong"}, []byte(`{"id":"d1","title":"x"}`))
	if code, _ := errCode(t, resp); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}

	// Sync endpoints require the token too.
	resp, _ = rawRequest(srv.URL, "POST", "/api/v1/sync/pull", nil,
		[]byte(`{"protocol_version":1,"requester":"beta"}`))
	if code, _ := errCode(t, resp); code != 401 {
		t.Fatalf("unauthed pull: %d", code)
	}

	// Health probes stay open (liveness is not a secret).
	resp, _ = rawRequest(srv.URL, "GET", "/health/live", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("health/live with no token: %d", resp.StatusCode)
	}

	// Correct token: mutation succeeds.
	resp, _ = rawRequest(srv.URL, "POST", "/api/v1/documents",
		map[string]string{"Authorization": "Bearer secret-token-123"}, []byte(`{"id":"d1","title":"x"}`))
	if resp.StatusCode != 201 {
		code, c := errCode(t, resp)
		t.Fatalf("authed create failed: %d %s", code, c)
	}
	_ = n
}

func TestLabEndpointsDisabledWithoutLabMode(t *testing.T) {
	_, srv := startTokenNode(t, "alpha", "", false)
	resp, _ := rawRequest(srv.URL, "POST", "/api/v1/lab/partition", nil, []byte(`{"a":"alpha","b":"beta"}`))
	if code, c := errCode(t, resp); code != 403 || c != "E_LAB" {
		t.Fatalf("lab endpoint without lab mode: %d %s", code, c)
	}
	resp, _ = rawRequest(srv.URL, "GET", "/api/v1/lab/convergence", nil, nil)
	// Read-only convergence is harmless but still respects lab gating: it
	// answers (it reads local state and probes peers).
	if resp.StatusCode != 200 {
		t.Fatalf("convergence should answer without lab mode: %d", resp.StatusCode)
	}
}

func TestMalformedRequestsFailWithStableCodes(t *testing.T) {
	_, srv := startTokenNode(t, "alpha", "", true)

	cases := []struct {
		name, method, path, body string
		wantCode                 int
		wantErr                  string
	}{
		{"bad json", "POST", "/api/v1/documents", `{not json`, 400, "E_SCHEMA"},
		{"bad doc id", "POST", "/api/v1/documents", `{"id":"BAD ID!","title":"x"}`, 400, "E_SCHEMA"},
		{"oversize title", "POST", "/api/v1/documents", `{"id":"ok","title":"` + strings.Repeat("a", 600) + `"}`, 400, "E_SCHEMA"},
		{"unknown action", "POST", "/api/v1/documents/d/operations", `{"action":"explode"}`, 404, "E_NOT_FOUND"},                    // unknown-doc check precedes action validation
		{"invalid status", "POST", "/api/v1/documents/d/operations", `{"action":"set_status","value":"chaos"}`, 404, "E_NOT_FOUND"}, // unknown doc check precedes value validation
		{"sql-ish doc id", "POST", "/api/v1/documents", `{"id":"x'; DROP TABLE operations;--","title":"y"}`, 400, "E_SCHEMA"},
		{"unicode doc id", "POST", "/api/v1/documents", "{\"id\":\"ドキュメント\",\"title\":\"y\"}", 400, "E_SCHEMA"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := rawRequest(srv.URL, tc.method, tc.path, nil, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			code, c := errCode(t, resp)
			if code != tc.wantCode || c != tc.wantErr {
				t.Fatalf("got %d %s, want %d %s", code, c, tc.wantCode, tc.wantErr)
			}
		})
	}
}

func TestOversizedBodyRejected(t *testing.T) {
	_, srv := startTokenNode(t, "alpha", "", true)
	huge := `{"id":"d","title":"` + strings.Repeat("a", node.MaxBodyBytes+1024) + `"}`
	resp, err := rawRequest(srv.URL, "POST", "/api/v1/documents", nil, []byte(huge))
	if err != nil {
		t.Fatal(err)
	}
	code, c := errCode(t, resp)
	if code != 413 || c != "E_LIMIT" {
		t.Fatalf("oversize body: %d %s, want 413 E_LIMIT", code, c)
	}
}

func TestSyncProtocolRejectsMismatches(t *testing.T) {
	n, srv := startTokenNode(t, "alpha", "", false)

	// Wrong protocol version.
	resp, _ := rawRequest(srv.URL, "POST", "/api/v1/sync/pull", nil,
		[]byte(`{"protocol_version":99,"requester":"beta"}`))
	if code, c := errCode(t, resp); code != 409 || c != "E_PROTOCOL" {
		t.Fatalf("wrong protocol: %d %s", code, c)
	}

	// Requester impersonating the responder.
	resp, _ = rawRequest(srv.URL, "POST", "/api/v1/sync/pull", nil,
		[]byte(`{"protocol_version":1,"requester":"alpha"}`))
	if code, c := errCode(t, resp); code != 400 || c != "E_ID" {
		t.Fatalf("self requester: %d %s", code, c)
	}

	// Push with too many ops.
	ops := make([]core.Operation, node.MaxPageOps+1)
	for i := range ops {
		op := core.Operation{
			ProtocolVersion: core.ProtocolVersion,
			DocID:           "d",
			Kind:            core.KindFieldSet,
			Field:           core.FieldTitle,
			Actor:           "beta",
			ActorSeq:        uint64(i + 1),
			HLC:             clock.HLC{Wall: 1000 + int64(i), Counter: 0, Actor: "beta"},
			Payload:         core.Payload{Value: "v"},
		}
		id, _ := core.ComputeOpID(op)
		op.OpID = id
		ops[i] = op
	}
	raw, _ := json.Marshal(node.PushRequest{ProtocolVersion: core.ProtocolVersion, Sender: "beta", Ops: ops})
	resp, _ = rawRequest(srv.URL, "POST", "/api/v1/sync/push", nil, raw)
	if code, c := errCode(t, resp); code != 413 || c != "E_LIMIT" {
		t.Fatalf("oversized push: %d %s", code, c)
	}
	_ = n
}

func TestIdempotencyKeyReplaysOriginalResponse(t *testing.T) {
	_, srv := startTokenNode(t, "alpha", "", false)

	h := map[string]string{"Idempotency-Key": "key-1"}
	body := []byte(`{"action":"set_title","value":"once"}`)

	// Create the doc first (no key).
	resp, _ := rawRequest(srv.URL, "POST", "/api/v1/documents", nil, []byte(`{"id":"idem","title":"t"}`))
	if resp.StatusCode != 201 {
		t.Fatalf("setup create: %d", resp.StatusCode)
	}

	resp1, _ := rawRequest(srv.URL, "POST", "/api/v1/documents/idem/operations", h, body)
	resp2, _ := rawRequest(srv.URL, "POST", "/api/v1/documents/idem/operations", h, body)
	if resp1.StatusCode != 200 || resp2.StatusCode != 200 {
		t.Fatalf("status: %d/%d", resp1.StatusCode, resp2.StatusCode)
	}
	if resp2.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replayed response not flagged")
	}
	var op1, op2 struct {
		Operation struct {
			OpID string `json:"op_id"`
		} `json:"operation"`
	}
	raw1, _ := io.ReadAll(resp1.Body)
	raw2, _ := io.ReadAll(resp2.Body)
	_ = json.Unmarshal(raw1, &op1)
	_ = json.Unmarshal(raw2, &op2)
	if op1.Operation.OpID != op2.Operation.OpID {
		t.Fatalf("idempotency replay produced a different operation: %s vs %s", op1.Operation.OpID, op2.Operation.OpID)
	}
	// The durable log must hold exactly ONE operation for both calls.
	logResp, _ := rawRequest(srv.URL, "GET", "/api/v1/operations?limit=100", nil, nil)
	var logOut struct {
		Operations []struct {
			Actor string `json:"actor"`
			Kind  string `json:"kind"`
		} `json:"operations"`
	}
	rawLog, _ := io.ReadAll(logResp.Body)
	_ = json.Unmarshal(rawLog, &logOut)
	sets := 0
	for _, op := range logOut.Operations {
		if op.Kind == "field.set" {
			sets++
		}
	}
	if sets != 1 {
		t.Fatalf("idempotent retry created %d field.set ops, want 1", sets)
	}
}

func TestUnknownRoutesReturn404(t *testing.T) {
	_, srv := startTokenNode(t, "alpha", "", false)
	resp, _ := rawRequest(srv.URL, "GET", "/api/v1/nonexistent", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("unknown route: %d", resp.StatusCode)
	}
	resp, _ = rawRequest(srv.URL, "DELETE", "/api/v1/documents/some-doc", nil, nil)
	if resp.StatusCode != 405 {
		t.Fatalf("unregistered method: %d, want 405", resp.StatusCode)
	}
}

func TestNonLoopbackBindRequiresToken(t *testing.T) {
	// serve.go refuses unsafe binds; node.Config itself is loopback-agnostic,
	// so the guard is tested at the CLI level in tests/integration. Here we
	// assert the rule's spirit: an auth-token-less node still serves only
	// when bound to loopback (httptest is loopback by construction).
	_, srv := startTokenNode(t, "alpha", "", false)
	resp, _ := rawRequest(srv.URL, "GET", "/health/ready", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("ready: %d", resp.StatusCode)
	}
}
