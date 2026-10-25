# SCALABILITY — where the architecture stops scaling, and what was done about it

## Measured bottlenecks found in the second-pass audit (and their fixes)

| bottleneck (measured) | fix | status |
|---|---|---|
| O(N) full-log scan on EVERY inventory/pull request (6.5 ms @ 10k per call, per peer, per interval) | incremental per-actor inventory + indexed segment queries (D-017) | FIXED, differential-tested |
| Startup replay O(N) canonical decodes (15.5 µs/op) | verified checkpoints + tail replay (D-020) | FIXED, 4.1× @ 100k |
| New TCP connection per pull (no keep-alive) | shared transport (D-017) | FIXED |
| Unbounded concurrent pull queries against SQLite | 8-slot semaphore (D-021) | FIXED |
| Unbounded list endpoints (documents/operations) | clamped + truncated flag (D-021) | FIXED |
| Partial-inventory stranding (correctness at scale: big logs make truncation likely) | local-max fallback (D-018) | FIXED |

## Known structural limits (honest list)

1. **Storage grows forever.** The operation log and OR-Set tombstones are never deleted. Checkpoints bound STARTUP time, not disk. Safe GC requires a replica-membership/retention contract (epochs or retention leases, an explicit "too old to catch up" path, protection for offline/rejoining replicas) that is deliberately NOT implemented — shipping plausible-looking GC would risk permanently breaking an offline peer. Design sketch for a future epoch protocol:
   - Cluster membership maintained as a replicated CRDT set with epochs; a node may only prune history below an epoch watermark acknowledged by EVERY current member; joining from a snapshot+epoch instead of full history; a member offline past N epochs must rebootstrap from a snapshot. This changes the wire protocol (version bump) and needs its own exhaustive safety proofs. Until then: keep history; document the disk cost (measured: ~150 bytes/op for vote ops — verify on your hardware).
2. **Single-writer SQLite per node.** Throughput is fsync-bound (~550 ops/s at synchronous=FULL; drop to `NORMAL` to trade safety for speed — a documented, operator-visible tradeoff, not silently changed).
3. **Full-mesh peering is a LAB topology.** At 25 replicas, 24 peers × sync interval is sustained fan-out traffic; production deployments would want a partial topology (ring/star with relayed ops) — the protocol supports arbitrary peer sets; no membership discovery is implemented (a trusted, static peer list only, per the threat model).
4. **Canonical materialization is O(live docs)** for digests/list endpoints (bounded reads shipped: list limit 200/1000, digest is O(docs+ops)).

## What the tier matrix showed

- Replicas 1/3/10 verified (25 = stretch, not run); documents 2,000 measured (100k = designed for via bounded reads, not run); operations 10k/100k measured with flat per-pull costs; concurrency 1/10 measured (50/100 = not run on this host within budget).
- Every skipped tier is skipped for TIME budget, not architecture: the paths involved are the same code at larger parameters.

## Queue/backpressure inventory (bounded by design)

- Sync page size 200 ops; pull page budget 200 pages per exchange (resumable).
- Pull handler semaphore: 8 concurrent.
- Event bus: 128 events/subscriber, drop-oldest, sequence-numbered; SSE subscribers ≤ 64.
- Request bodies ≤ 4 MiB; canonical op ≤ 16 KiB; header cap 16 KiB.
- Idempotency table ≤ 10k rows (oldest evicted).
- Received-above hole advertisement ≤ 512/actor before the `partial` fallback.
