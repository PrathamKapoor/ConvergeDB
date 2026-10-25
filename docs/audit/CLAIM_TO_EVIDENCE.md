# CLAIM → EVIDENCE MAP (second pass, 2026-10-26, base d264fb4)

Every meaningful README/doc claim and user-visible action, mapped to source and to executed evidence. Statuses: VERIFIED (executed evidence this pass or preserved from the recorded first-pass runs), PARTIAL (works with a known gap), NOT TESTED, BROKEN.

## Replication & consistency claims

| Claim (source) | Source files | Evidence | Status |
|---|---|---|---|
| "Offline edits, deterministic conflict resolution, eventual convergence" (README) | internal/crdt/*, internal/node | tests/model exhaustive 7! permutations; replication suite; demo (seed 42, 99); CI demo job green | VERIFIED |
| "Only missing operations are exchanged" | internal/store/ops.go MissingOpsFor | TestInventoryTracksFrontierAndHoles, TestMissingOpsForIsHoleAware | VERIFIED (per-call cost O(N) — see FINDINGS P1-1) |
| "Duplicate/reordered delivery is safe" | internal/crdt, node.IngestOne | TestDuplicateAndReorderedDeliveryConverge; model duplicate schedules | VERIFIED |
| "Convergence verified via digests, not optimism" | internal/node/lab.go | TestTwoNodesReplicateBidirectionally (digest assert), demo I-07 checks, TestConvergenceUnknownWhenPeerDead | VERIFIED |
| "ACK means durable local commit" | node.commitLocal → store.AppendLocal | TestAppendLocalAllocatesDenseSequencesAndSurvivesRestart; demo kill/restart step | VERIFIED (process-kill model only) |
| "Op ID reuse with different body is a conflict, never overwritten" (I-04) | store.IngestRemote, core.Validate | TestIngestRemoteIdempotentAndConflictDetection, TestRejectionOfInvalidRemoteOps | VERIFIED |
| "Hole-aware anti-entropy; max_seen alone is insufficient" | store.Inventory | TestInventoryTracksFrontierAndHoles, TestHoleAwareSyncDeliversGappedSequences | VERIFIED |
| "Benchmarks: p50 ~1.6ms, convergence ~308ms" (RESULTS.md) | cmd/convergedb/tools.go bench | recorded runs 2026-10-26; rerunnable via `convergedb bench` | VERIFIED (host-specific; ranges recorded) |

## API/CLI claims

| Claim | Source | Evidence | Status |
|---|---|---|---|
| All v1 endpoints respond per PROTOCOL.md | internal/api/server.go | hardening + replication suites; smoke via curl during first pass | VERIFIED |
| Non-loopback bind refuses without token (or --allow-unsafe-bind) | cmd/convergedb/serve.go | code-inspected; CLI-level test pending | PARTIAL → test added this pass (see FINDINGS P2-4) |
| `demo partition-heal` honors flags and exits nonzero on failed invariant | cmd/convergedb/demo.go | flags parsed correctly (P2-6 fixed); dirty-dir rejection verified; step-failure path exercised by the P2-7 investigation | VERIFIED |
| `bench` asserts counter == perSide×3 | cmd/convergedb/tools.go | observed failure mode pre-fix (286≠300), then 3/3 pass | VERIFIED |
| Idempotency-Key replays the SAME operation | internal/api + store.idempotency (SQLite-backed) | TestIdempotencyKeyReplaysOriginalResponse; storage is durable (SQLite table) | VERIFIED |

## Frontend claims

| Claim | Evidence | Status |
|---|---|---|
| "Every number comes from a live replica API" | browser session (first pass): votes/labels replicated live; screenshots from running app | VERIFIED (interactive) |
| Convergence banner distinguishes CONVERGED/DIVERGED/UNKNOWN + staleness | browser session: DIVERGED shown mid-partition; STALE path implemented, not exercised in browser | PARTIAL |
| "UI must not freeze on large datasets" (second-pass requirement) | op list capped 40; documents list bounded server-side + UI passes explicit limit (P2-3 fixed) | VERIFIED (bounded reads; 100k-row rendering not exercised) |
| All buttons functional or visibly disabled | browser session; fault panel disabled outside lab mode | VERIFIED (interactive) |

## Infrastructure claims

| Claim | Evidence | Status |
|---|---|---|
| CI green incl. demo job | GitHub Actions runs on d264fb4 (2× success) | VERIFIED |
| Docker Compose lab | files exist; daemon unavailable | NOT TESTED |
| Fresh-clone reproducibility | temp-dir clone built+tested+demo (first pass release gate) | VERIFIED |
| Secret-free repo | git grep scan + history scan (first pass); re-run this pass | VERIFIED |

## Second-pass new measurements (this directory)

- Scale read-path baseline (tests/adversarial/scale_test.go): recorded in docs/BENCHMARKS.md before/after table.
- Race detector: 6 packages ok, repeated.
