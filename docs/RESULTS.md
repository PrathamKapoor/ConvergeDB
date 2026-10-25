# RESULTS.md — measured outcomes ledger

Every number below was produced by a command in this repository on the recorded host. Numbers are environment-specific; rerun with the given commands. `NOT MEASURED` is used where a claim would otherwise be unsupported.

## Environment

- Host: Windows 11 (10.0.26200), amd64, single machine; Go 1.27.3 (portable, user-local); SQLite via modernc.org/sqlite v1.61.0 (pure Go).
- All benchmarks: loopback HTTP, `synchronous=FULL` + WAL, one process per replica.
- Command: `convergedb bench --writes N --out var/bench` (source: `cmd/convergedb/tools.go`). Raw JSON: `var/bench/bench-report.json` (generated at run time; not committed).

## Profile: single-node durable write latency (2026-10-26, writes=300..500)

| metric | value |
|---|---|
| p50 | ~1.6 ms |
| p95 | ~2.4–2.7 ms |
| p99 | ~3.1–4.5 ms |
| throughput | ~570 ops/s |

Each write is a full HTTP mutation whose ACK follows the SQLite commit (WAL, synchronous=FULL). The dominant cost is the per-commit fsync.

## Profile: three-replica heal → verified convergence (2026-10-26, 3 trials × 200 isolated ops)

Setup: three real processes; alpha partitioned from beta and gamma; 100 votes (+1) on alpha and 100 votes (+2) on beta while isolated; heal; measure heal→CONVERGED (checker verifies op-inventory digests AND canonical state digests, poll interval 150 ms). Sanity assertion: counter must equal exactly 300 afterwards.

| metric | value |
|---|---|
| heal → CONVERGED (median) | ~308 ms |
| min / max | ~308 / ~321 ms |
| counter check | 300 = 300 on every trial |

## Not measured / not claimed

- Power-loss durability (only process-kill durability is tested).
- Throughput under concurrent multi-client load (the write profile is single sequential client).
- Comparison against other systems (no baseline engine was implemented; any comparison would be apples-to-oranges).
- WAN behavior, large payloads at scale, multi-million-op logs.

## Negative results and anomalies

- 2026-10-26: the first bench run reported counter 286 ≠ 300 after convergence. Root cause: the bench harness partitioned BEFORE the created document had replicated to beta, then ignored non-200 mutation responses — 14 votes silently 404'd on beta. The replication engine lost nothing; the harness lacked precondition checks. Fix: bench waits for the document on beta before isolating and asserts every mutation status (see FAILURE_LOG.md). After the fix, 3/3 trials converge with counter exactly 300.
