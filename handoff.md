# handoff.md — current state for the next session

## 1. Current Phase
- Phase: Second pass — scalability, correctness, and total system audit (COMPLETE)
- Subphase: audit docs, measured scale fixes, fault laboratory, operations runbook shipped
- Objective: none open; remaining stretch items listed in section 9
- Status: complete

## 2. Work Completed
- Full second-pass audit: docs/audit/SYSTEM_INVENTORY.md (every component, route, table accounted for), docs/audit/CLAIM_TO_EVIDENCE.md (claims mapped to executed evidence), docs/audit/FINDINGS.md (P0: none; P1 ×4 fixed; P2 ×7: 5 fixed, 2 open and documented).
- Measured scale fixes: anti-entropy pull/inventory were O(N) per request — rebuilt as incremental inventory + indexed segment queries + shared keep-alive client (differential tests prove exact scan-equivalence). Measured: inventory/pull flat vs log size (0ms/1.0ms at 100k ops; previously ~65ms+ projected per call at 100k).
- CORRECTNESS FIX (found by the new differential harness): the partial-inventory fallback could strand ops above a stale requester max_seen forever; now resends up to LOCAL max_seen. Partial flag also reflects the advertised cap; smallest-k holes kept on overflow.
- Verified materializer checkpoints: per-actor world-applied watermark + tail replay, hash + prefix-count verification, fail-closed full-replay fallback. Startup at 100k ops: 1.55s → 381ms (4.1×).
- Fault laboratory: 10-node asymmetric-partition scenario with staged randomized heals (digest equality across all nodes, exact vote totals), offline-rejoin catch-up (150 ops), restart-all-nodes in two orders, REAL subprocess SIGKILL mid-burst (every ACKed op survived, 5 seeds).
- Operations: `convergedb admin backup` (VACUUM INTO, point-in-time, identity-preserving) with round-trip test; docs/OPERATIONS.md runbook incl. troubleshooting.
- Benchmarks: docs/BENCHMARKS.md with BEFORE/AFTER tables and workload matrix (concurrency tiers, 2000-doc tier, 3-replica convergence 188–340ms with exact 300=300 counter).
- Demo hardening: fixed silent CLI flag ignorance (flags were never parsed in any prior demo run!) and fail-fast on reused scratch dirs (a false counter-double-counting alarm was traced to state accumulation, not a merge defect).

## 3. Files Changed
- internal/store (store.go, ops.go, indexed_test.go, store_test.go), internal/crdt (crdt.go, world.go), internal/node (node.go, replicate.go), internal/api (server.go), cmd/convergedb (serve.go, demo.go, admin.go, tools.go, main.go), tests/adversarial/ (scale, checkpoint, multinode, subprocess, backup), web/src/api.ts, docs/* (audit trio, BENCHMARKS, SCALABILITY, OPERATIONS, TEST_MATRIX, README), decisions.md, flow.md, handoff.md.

## 4. Current Architecture / State
- Unchanged boundaries (docs/ARCHITECTURE.md) plus: checkpoint table (schema v3), incremental in-memory inventory, world-applied watermark for checkpoint coverage, MissingOpsForScan oracle retained for differential testing.

## 5. Decisions Made
- D-017..D-022 (see decisions.md): indexed anti-entropy, partial-fallback correctness, partial-flag semantics, verified checkpoints, HTTP hardening, demo flag/dir fixes.

## 6. Requirements and Constraints
- Unchanged from the first pass: commit author identity, no attribution lines, no paper/research/.tex, no runtime DBs or secrets in git, non-destructive publishing.

## 7. Testing and Verification
- `go test ./...` — 60 tests green; `go test -race ./internal/... ./tests/...` — 7 packages green (scenario suites run repeatedly; the 10-node scenario 8/8 after fixing a test-sentinel flake).
- `convergedb demo partition-heal` (seed 123) — PASS with 6/6 semantic checks on a fresh dir; dirty-dir rerun rejected with an actionable error.
- Scale: 10k and 100k-op tiers measured (tests/adversarial/scale_test.go; SCALE_OPS env). NOT run: 25 replicas, 1M ops, 50/100 concurrent clients, multi-host.
- NOT TESTED: Docker image build (daemon unavailable), Playwright UI automation, power-loss durability, GC (unimplemented by design).

## 8. Known Issues / Risks
- Open P2s (documented, deliberate): unbounded log/tombstone growth (safe GC needs a membership/retention contract that does not exist yet — design sketch in docs/SCALABILITY.md); Docker path untested.
- The demo requires a fresh --dir per run (fail-fast); CI uses a fresh checkout so it is unaffected.

## 9. Unfinished Work
- Snapshot-bootstrap endpoint for fresh replicas (page-streamed catch-up works today; measured acceptable at 100k).
- Epoch-based retention/GC protocol (design sketch only).
- Playwright UI suite; Docker verification; 25-replica and 1M-op tiers on bigger hardware.

## 10. Next Subphase
- If resuming: take the GC/membership design to a proof-of-safety document, then implement behind a protocol version bump; or add the snapshot-bootstrap endpoint using the existing checkpoint format.

## 11. Critical Context
- Go toolchain: C:\Users\LENOVO\tools\go (prefix PATH per shell). Docker daemon NOT running. `gh` absent; the repo was created via REST API using the machine's Git Credential Manager credential (never printed).
- Origin: https://github.com/PrathamKapoor/ConvergeDB (main). Never force-push.

## 12. Agent Instructions
- Maintain decisions.md/flow.md in the same step as changes (Rules 1–6). Any merge-related change needs differential or permutation evidence. The scan oracle (MissingOpsForScan) and inventoryFromScan MUST stay in sync with the incremental paths — extend the differential tests when touching them. Keep the fail-closed fallbacks: checkpoints and every ingestion path must degrade to full replay, never to wrong state.
