package node

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/clock"
	"github.com/PrathamKapoor/ConvergeDB/internal/core"
)

// NodeInfo is the payload of GET /api/v1/node — everything the UI and the
// convergence checker need about one replica. Peer reachability reflects
// the LAST sync exchange, not a live probe; it is honest about that.
type NodeInfo struct {
	ID              string       `json:"id"`
	ProtocolVersion int          `json:"protocol_version"`
	HTTPAddr        string       `json:"http_addr"`
	LabMode         bool         `json:"lab_mode"`
	UptimeMS        int64        `json:"uptime_ms"`
	HLC             clock.HLC    `json:"hlc"`
	DocCount        int          `json:"doc_count"`
	OpCount         int          `json:"op_count"`
	OpDigest        string       `json:"op_digest"`
	StateDigest     string       `json:"state_digest"`
	StateCanonical  string       `json:"state_canonical,omitempty"`
	Peers           []PeerStatus `json:"peers"`
}

// Info builds the NodeInfo snapshot.
func (n *Node) Info(includeCanonical bool) NodeInfo {
	opCount, opDigest, _ := n.OpDigest()
	stateDigest, _ := n.StateDigest()
	info := NodeInfo{
		ID:              n.cfg.ID,
		ProtocolVersion: core.ProtocolVersion,
		HTTPAddr:        n.cfg.HTTPAddr,
		LabMode:         n.cfg.LabMode,
		UptimeMS:        n.UptimeMS(),
		HLC:             n.HLC(),
		DocCount:        n.DocCount(),
		OpCount:         opCount,
		OpDigest:        opDigest,
		StateDigest:     stateDigest,
		Peers:           n.PeerStatuses(),
	}
	if includeCanonical {
		if b, err := n.CanonicalState(); err == nil {
			info.StateCanonical = truncate(string(b), 64*1024)
		}
	}
	return info
}

// PeerReport is one peer's self-reported state during a convergence check.
type PeerReport struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Reachable   bool   `json:"reachable"`
	Error       string `json:"error,omitempty"`
	OpCount     int    `json:"op_count"`
	OpDigest    string `json:"op_digest"`
	StateDigest string `json:"state_digest"`
	MatchesSelf bool   `json:"matches_self"`
}

// ConvergenceStatus values.
const (
	StatusConverged = "CONVERGED"
	StatusDiverged  = "DIVERGED"
	StatusUnknown   = "UNKNOWN"
)

// ConvergenceReport is the truthful result of a convergence check:
// CONVERGED requires operation-inventory equality (sorted-op-id digest AND
// count) AND canonical state equality on every REACHABLE peer. Any
// unreachable peer forces UNKNOWN — unreachability is not convergence.
type ConvergenceReport struct {
	Status      string       `json:"status"`
	CheckedAtMS int64        `json:"checked_at_ms"`
	Self        PeerReport   `json:"self"`
	Peers       []PeerReport `json:"peers"`
	Details     string       `json:"details,omitempty"`
}

// Convergence asks every configured peer for its NodeInfo and compares.
func (n *Node) Convergence() ConvergenceReport {
	selfCount, selfOpDigest, _ := n.OpDigest()
	selfState, _ := n.StateDigest()
	rep := ConvergenceReport{
		CheckedAtMS: time.Now().UnixMilli(),
		Self: PeerReport{
			ID: n.cfg.ID, URL: "http://" + n.cfg.HTTPAddr, Reachable: true,
			OpCount: selfCount, OpDigest: selfOpDigest, StateDigest: selfState, MatchesSelf: true,
		},
	}
	allReachable := true
	for _, p := range n.cfg.Peers {
		pr := PeerReport{ID: p.ID, URL: p.URL}
		info, err := fetchNodeInfo(p, n.cfg.AuthToken)
		if err != nil {
			pr.Error = err.Error()
			allReachable = false
		} else {
			pr.Reachable = true
			pr.OpCount = info.OpCount
			pr.OpDigest = info.OpDigest
			pr.StateDigest = info.StateDigest
			pr.MatchesSelf = info.OpDigest == selfOpDigest && info.OpCount == selfCount && info.StateDigest == selfState
		}
		rep.Peers = append(rep.Peers, pr)
	}
	switch {
	case !allReachable:
		rep.Status = StatusUnknown
		rep.Details = "one or more peers unreachable; convergence cannot be verified (this is not convergence)"
	default:
		converged := true
		for _, pr := range rep.Peers {
			if !pr.MatchesSelf {
				converged = false
				break
			}
		}
		if converged {
			rep.Status = StatusConverged
		} else {
			rep.Status = StatusDiverged
		}
	}
	return rep
}

func fetchNodeInfo(p PeerConfig, token string) (*NodeInfo, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(p.URL, "/")+"/api/v1/node", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("node info: replied %d", resp.StatusCode)
	}
	var info NodeInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxBodyBytes)).Decode(&info); err != nil {
		return nil, fmt.Errorf("node info: malformed reply: %w", err)
	}
	return &info, nil
}
