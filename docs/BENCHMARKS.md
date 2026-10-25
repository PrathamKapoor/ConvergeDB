# BENCHMARKS — second pass (2026-10-26)

All numbers measured on the development host: **Windows 11 (10.0.26200), amd64 laptop, Go 1.27.3 (portable user-local toolchain), SQLite via modernc.org/sqlite v1.61.0, WAL + synchronous=FULL, loopback HTTP**. Absolute numbers are host-specific; the VALUE of this page is the before/after comparison and the shape of the curves. Rerun: `convergedb bench --help`, `SCALE_OPS=100000 go test ./tests/adversarial/ -run TestScaleReplicationReadPaths -v`.

## Anti-entropy read paths: BEFORE vs AFTER (P1-1 fix, commit 7eea73b)

Measured by `tests/adversarial/scale_test.go` (best of 5 in-process runs; the seed builds the log through the real durable write path).

| measurement | BEFORE (d264fb4) | AFTER (7eea73b) | scaling |
|---|---|---|---|
| Inventory() per call, 10k ops | 6.5 ms | **< 0.1 ms** | O(N) → O(actors) |
| Inventory() per call, 100k ops | ~65 ms (projected) | **~0 ms (measured)** | flat |
| MissingOpsFor single page, 10k ops | 1.6 ms | 1.7 ms | page decode dominates |
| MissingOpsFor single page, 100k ops | ~16 ms (projected) | **~1.0 ms (measured)** | O(N) → O(page+holes) |
| HTTP POST /sync/pull one page, 100k ops | — | 1.0–1.6 ms | flat |

Interpretation: the old code scanned the whole log on EVERY inventory and pull request. With P peers polling each interval, cost was O(peers × N × interval⁻¹); it is now O(peers × (page + holes)). The correctness of the rewrite is pinned by strict differential tests against the retained scan oracle.

## Startup: full replay vs verified checkpoint (P1-2, commit 7eea73b)

| log size | full replay | checkpoint+tail | speedup |
|---|---|---|---|
| 10k ops | ~147–165 ms | — (tail empty) | — |
| 100k ops | 1.55 s | **381 ms** | 4.1× |

Extrapolation (measured 15.5 µs/op replay): 1M ops ≈ 15.5 s full replay vs a bounded checkpoint load + tail. Checkpoint failure modes (corruption, stale watermark, prefix mismatch) fall back to full replay with byte-identical state — tested, not assumed.

## Workload matrix (convergedb bench, same host)

| profile | config | p50 | p95 | p99 | throughput |
|---|---|---|---|---|---|
| local durable write | 1 client × 300 ops | 1.6 ms | 2.9 ms | 3.8 ms | ~550 ops/s |
| concurrent durable writes | 10 clients × 30 ops | 14.3 ms | 17.6 ms | 18.3 ms | ~681 ops/s |
| many documents | 2000 docs created | 3.46 s total (~578/s) | — | — | list(limit 200) 7 ms; convergence digest 5 ms |
| 3-replica heal → CONVERGED | 200 isolated ops, 3 trials | 308 ms | — | — | 188–340 ms; counter check 300=300 every trial |

Notes: single-client p50 is the fsync path (synchronous=FULL); 10-client p50 includes queueing behind the single-writer serialization — throughput improves slightly with pipelining while per-request latency grows. The 3-replica profile asserts the exact counter (300) after convergence; a mismatch fails the benchmark.

## Multi-node fault scenarios (correctness under scale, tests/adversarial/multinode_test.go)

- 10 nodes, asymmetric two-island partition + sub-split + one delayed peer, writes on 5 replicas, staged randomized heal: CONVERGED, all digests equal, votes exactly 29 (8/8 race runs after fixing a test-sentinel bug — see FINDINGS P2-5).
- Offline rejoin after 150 ops: caught up with exact inventory/state equality.
- Restart-all-nodes in two different orders on reused databases: identical durable inventories and state digests.
- Subprocess SIGKILL mid-burst (real binary): every ACKed write survived, sequences continue (5 runs, varied seeds).

## Explicitly NOT measured

- 25-replica tier, 1M-op catch-up end-to-end, multi-host deployments, WAN latency, CPU/memory profiles under sustained load, WAL checkpointing behavior over multi-GB logs. No SLA is claimed anywhere.
