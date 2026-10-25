# FINDINGS — second-pass audit (2026-10-26, base d264fb4 → 7eea73b)

Severity: P0 data loss/security, P1 incorrect results or availability, P2 measured bottleneck/operational gap, P3 polish. Every finding has evidence, a fix or an explicit open status, and a regression test where applicable.

## P0 — none found

No proven data-loss or authorization defect survived the audit. The candidate P0s below were proven real defects and FIXED.

## P1 — fixed

### P1-1: Anti-entropy pull scanned the entire log on every request (measured O(N) per pull)
- Evidence: `tests/adversarial/scale_test.go` BEFORE — Inventory() 6.5 ms and MissingOpsFor 1.6 ms at 10k ops, both O(N) per call; with 10 peers on a 1s interval a 1M-op node would spend >6.5 s/peer/round inside inventory scans alone.
- Root cause: `MissingOpsFor` and `Inventory` executed full-log SQL scans and filtered in Go.
- Fix: incremental in-memory per-actor receive state (frontier/holes/partial) + indexed per-actor segment queries + shared keep-alive HTTP client (D-017).
- Verification: differential tests (`TestIncrementalInventoryMatchesScan`, `TestMissingOpsIndexedMatchesScan`) prove exact scan-equivalence; AFTER: inventory ~0 ms, page ~1 ms at 100k ops — flat vs N.
- Remaining risk: differential tests must be extended if the state gains new fields.

### P1-2: Startup replay linear in log size
- Evidence: 1.55 s at 100k ops (15.5 µs/op), ~15.5 s projected at 1M.
- Fix: verified materializer checkpoints (per-actor world-applied watermark + hash + prefix-count verification, fail-closed fallback) (D-020).
- Verification: 4.1× faster startup at 100k (1.55 s → 381 ms); corruption/staleness/prefix-mismatch all fall back to full replay with identical digests (4 checkpoint tests).
- Remaining risk: checkpoint content is an optimization only; a malicious local process can corrupt it, which degrades to slower (not wrong) startups.

### P1-3: Partial-inventory fallback could strand operations forever
- Evidence: differential harness mismatch — the original partial fallback resent only `(frontier, requester.max_seen]`; a requester with a stale max_seen never learns about newer ops (it cannot advertise sequences it has never seen).
- Fix: partial fallback resends up to the LOCAL max seen (D-018); the Partial flag also now reflects the ADVERTISED cap (D-019) so truncated hole lists never look complete.
- Verification: `TestMissingOpsForIsHoleAware` partial case expects [5,6,7] with stale max_seen 6; strict differential suite.
- Remaining risk: none known.

### P1-4: Checkpoint coverage race (found in design review of the first checkpoint draft)
- Evidence: deriving the coverage watermark from the store inventory could overcover — an op committed to SQLite but not yet applied to the world would be marked covered and never replayed → silently missing from materialized state after restart.
- Fix: coverage = the world-APPLIED watermark, updated only after a successful apply, read atomically with the world snapshot (D-020).
- Verification: checkpoint tests; race detector.
- Remaining risk: none known.

## P2 — fixed

- P2-1: no server Read/Idle timeouts (slowloris-style exposure) → ReadTimeout 30s, IdleTimeout 120s, header cap; deliberately NO WriteTimeout (SSE) (D-021).
- P2-2: unbounded concurrent pull queries → 8-slot semaphore.
- P2-3: `GET /api/v1/operations?limit` unclamped; `GET /api/v1/documents` cloned every document per request → clamped/bounded with `truncated` flag; UI passes an explicit limit.
- P2-4: per-pull `http.Client` allocation (no connection reuse) → shared transport (part of D-017).
- P2-5: **test-construction flake** in the 10-node scenario — the digest-equality helpers used a zero-value sentinel, failing whenever map iteration visited alpha after another node. Diagnostics proved all 10 nodes identical (count=30, same digests). Fixed with a visited flag; 8/8 race runs green. No product defect.

## P2 — open (documented, not blocking the research-prototype classification)

- **Tombstone/log growth is unbounded by design.** Safe GC requires a replica-membership/retention contract (epochs, leases) that is NOT implemented; the mission forbids shipping provably-unsafe GC. Mitigations shipped instead: verified checkpoints bound STARTUP cost, not disk. Documented in docs/SCALABILITY.md with the design sketch; compaction remains disabled.
- **Catch-up bootstrap for a fresh replica is page-streamed** (indexed pulls, keep-alive): measured 100k-op logs restore via ~500 pages; a dedicated snapshot-bootstrap endpoint is future work. No correctness risk.
- **Docker image build NOT TESTED** (daemon unavailable on the host; compose files provided).
- **Level E browser automation** (Playwright) not installed; UI verified interactively (first pass) plus a stale-view banner (D-015). UI list reads are now bounded.

- P2-6: **demo CLI flags were silently ignored** — `cmdDemo` received the subcommand name as args[0] and `flag.Parse` stopped on it, so `--seed/--keep/--out/--dir` never took effect in ANY demo invocation (first pass included). Fixed by skipping the leading subcommand token.
- P2-7: **demo was not idempotent across runs on a reused scratch directory** — re-running on var/demo accumulated state, which produced a FALSE counter-double-counting alarm (votes 6 instead of 3: two runs' worth of votes). The demo now fails fast when the target dir already holds replica databases, with an actionable message; it never deletes operator data. The counter itself was verified correct by the differential and kill-mid-burst suites.

## P3 — fixed / noted

- `PushToPeer` remains unused by the loop (documented opportunistic path).
- Static assets served without cache headers (lab scale; harmless).
- Test RNG/HTTP helper cleanup in tests/adversarial (used io.ReadAll etc.).

## Invariant status (I-01 … I-12) after the second pass

| # | Status | Evidence |
|---|---|---|
| I-01 | PASS | store restart tests; `TestKillMidBurstPreservesAcknowledgedWrites` (5/5 runs incl. varied seeds): all ACKed ops survive SIGKILL |
| I-02 | PASS | idempotence suite + differential ingest tests |
| I-03 | PASS | exhaustive 7! permutations + 200-shuffle + duplicate-laden schedules |
| I-04 | PASS | store conflict tests; content-addressed validation |
| I-05 | PASS | bidirectional/pagination tests + `TestIndexedPullIsBoundedByPageAndHoles` |
| I-06 | PASS | hole tests + `TestOfflineNodeRejoinsAfterManyOps` (150-op catch-up) |
| I-07 | PASS | demo + `TestTenNodeAsymmetricPartitions` (digest equality across 10 nodes) |
| I-08 | PASS | partition tests (writes continue while isolated) |
| I-09 | PASS | OR-Set semantics suite |
| I-10 | PASS | counter suite + bench exact counter (300=300, 3/3 trials) + 10-node 29-vote check |
| I-11 | PASS | Level D hardening suite |
| I-12 | PASS | reproducibility docs + fresh-clone verification (first pass) + secret scan |
