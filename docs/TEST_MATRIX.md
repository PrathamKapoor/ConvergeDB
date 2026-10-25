# TEST_MATRIX.md — risk-weighted verification program

All results below are from real runs on the development host (Windows 11, Go 1.27.3, 2026-10-26). `go test ./...` executes Levels A–D; the demo executes the scenario suite; the browser session exercised Level E.

## Level A — deterministic unit / property tests (internal/clock, core, crdt, tests/model)

| Test | What it proves |
|---|---|
| TestNowAdvancesCounterWithinSameMillisecond, TestRollbackKeepsIssuedTimestampsMonotonic | HLC monotonicity incl. wall-clock rollback |
| TestObserveRaisesWatermarkAboveRemote, TestObserveWithStaleRemoteDoesNotRegress | clock adoption + skew safety |
| TestRestoreResumesAbovePersistedState, TestCompareTotalOrder | persisted watermark; total order |
| TestCanonicalEncodingIsDeterministicAndStable, TestOpIDIsContentAddressed, TestChecksumDetectsTampering | canonical bytes; content-addressed identity; tamper detection |
| TestValidateRejectsMalformedOperations (15 cases), TestValidateLabelRemoveRules, TestValidateAcceptsValidExoticPayloads | fail-closed schema incl. unicode + bounds |
| TestLWWRegisterOrderingAndIdempotence | LWW tie-breaks, arrival-order independence |
| TestORSetObservedRemoveSemantics, TestORSetAddWinsAcrossAnyApplyOrder | observed-remove, tombstones, add-wins |
| TestCounterDedupCommutativityAndOverflow | counter dedup, conflict, overflow fail-closed |
| TestWorldPermutationInvariance (200 seeded shuffles), TestExhaustivePermutationsConverge (ALL 7! = 5040 orderings), TestDuplicateLadenSchedulesConverge (300 seeds), TestTwoReplicaExchangeModel (200 trials) | I-02/I-03: order independence and duplicate idempotence of full histories |
| TestWorldCreateArrivesLast, TestWorldDocumentLifecycleSemantics, TestCanonicalStateIsSortedAndStable | lifecycle + canonical output |

## Level B — persistence (internal/store)

| Test | What it proves |
|---|---|
| TestFreshOpenMigratesAndReopens | migrations + meta survive restart |
| TestAppendLocalAllocatesDenseSequencesAndSurvivesRestart | dense seqs; I-01 at storage level; HLC watermark persisted in-tx |
| TestConcurrentAppendsGetUniqueDenseSequences (32 goroutines) | unique seq assignment under concurrency |
| TestIngestRemoteIdempotentAndConflictDetection | I-04: duplicate vs conflict, content preserved |
| TestInventoryTracksFrontierAndHoles | frontier closes only on backfill (I-06) |
| TestMissingOpsForIsHoleAware | exact hole-aware pull; cursor resumability; partial fallback |
| TestCorruptedStoredOperationFailsClosed | checksum mismatch fails closed |
| TestListOpsPaginationIsBounded | bounded cursor pagination |

## Level C — real multi-replica (internal/api/replication_test.go — real HTTP via httptest; plus the subprocess demo)

| Test | What it proves |
|---|---|
| TestTwoNodesReplicateBidirectionally | bidirectional drain; votes 1+2=3; digest equality |
| TestPartitionBlocksSyncButNotLocalWrites | I-08; DIVERGED while partitioned; LWW reconcile after heal |
| TestDuplicateAndReorderedDeliveryConverge | dup=1.0 + reorder storm converges |
| TestHoleAwareSyncDeliversGappedSequences | 26 ops through reorder+dup; holes close to frontier=max_seen |
| TestRejectionOfInvalidRemoteOps | invalid ops rejected with codes; tamper → E_ID |
| TestEventsEmittedForLocalAndRemoteOps | real event stream |
| TestIdentityIsBoundToDatabaseFile | identity theft refused; restart retains ops |
| TestConvergenceUnknownWhenPeerDead | unreachable ⇒ UNKNOWN, never CONVERGED |
| `convergedb demo partition-heal` (3 real OS processes, seeded) | full I-01..I-10 scenario; JSON report; nonzero exit on failure |

## Level D — adversarial / security (internal/api/hardening_test.go)

Auth boundary (401s, wrong token, sync auth, health stays open), lab gating (403), malformed JSON/IDs/payloads with stable codes, SQL-ish and unicode doc IDs, 4 MiB body → 413 E_LIMIT, protocol mismatch → 409, self-requester rejection, oversized push → 413, durable Idempotency-Key replay (same op, exactly one stored), unknown routes 404 / methods 405.

## Level E — browser / demo (manual-automated session, 2026-10-26)

- Built UI (`npm run build`) served by a replica; loaded in Chromium (in-app browser) against a live 3-node lab.
- Verified live: vote +1 on alpha → appeared on beta and gamma within ~2s; label add replicated likewise; UI-driven partition of alpha↔beta (mesh kept convergence via gamma relay — correct behavior); full isolation of alpha → DIVERGED banner with marked digests; isolated titles diverged; heal → CONVERGED; LWW winner ("Beta Isolated Edit") on all three panels.
- Screenshots captured from the running app: `web/screenshots/dashboard-converged.png`, `web/screenshots/dashboard-partitioned.png`.
- NOT run: automated Playwright suite and 360/390px visual checks (no Playwright dependency installed); console-error smoke was checked implicitly via the error-free session but not asserted programmatically. Recorded honestly as a gap.

## Commands

```bash
go test ./...          # Levels A–D
go test -race ./...    # concurrency gate (green, 8/8 repeated runs on the flake-prone suite)
./convergedb demo partition-heal --seed 42    # Level C scenario
./convergedb bench --writes 300 --out var/bench # benchmarks (see RESULTS.md)
```

## Skipped / open

- Playwright browser automation and mobile-width screenshots — not installed in the environment; Level E was executed interactively instead (above).
- Docker Compose lab build — compose file and Dockerfile are provided, but the image build was NOT TESTED: the Docker CLI (29.8.1) is installed while the Docker Desktop daemon was not running on the development host. First `docker compose up --build` on a running daemon may surface image-version issues (golang:1.27-alpine, node:22-alpine).
- Formal verification — none claimed.
