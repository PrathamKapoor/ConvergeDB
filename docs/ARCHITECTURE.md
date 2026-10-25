# ARCHITECTURE.md — modules, call graph, data flow

## Module boundaries

```
cmd/convergedb/          CLI: serve, demo partition-heal, inspect, verify, bench, doctor
internal/clock/          hybrid logical clock (no I/O)
internal/core/           operation envelope, canonical encoding, checksums, validation (no I/O)
internal/crdt/           LWW register, OR-Set, delta counter, World materializer (no I/O)
internal/store/          SQLite: migrations, durable op log, seq allocation, inventories
internal/node/           replica lifecycle, write path, ingest, sync loop, faults, events
internal/api/            versioned HTTP surface, auth, limits, SSE, static UI
tests/model/             exhaustive permutation + randomized exchange model checks
internal/api/…_test      HTTP-level replication + Level D hardening suites
web/                     React + TypeScript workbench (built to web/dist, served by --web)
```

Dependency direction: `api → node → {store, sync-client, clock, crdt → core → clock}`. The pure packages (clock, core, crdt) never do I/O, which is what makes exhaustive ordering tests possible.

## Local write flow (implemented)

```text
POST /api/v1/documents | /api/v1/documents/{id}/operations   internal/api/server.go
 └─ Node.CreateDocument / Node.Mutate                        internal/node/node.go
     ├─ local-view precondition (doc exists / not exists)     crdt.World snapshot
     ├─ Clock.Now() issues the HLC                            internal/clock
     └─ Store.AppendLocal                                     internal/store/ops.go
         ├─ BEGIN IMMEDIATE (txlock=immediate)
         ├─ next actor_seq = MAX(actor_seq)+1 for actor
         ├─ label.add: set Payload.Tag = DeriveAddTag(actor, seq)
         ├─ derive content-addressed op_id, Validate (fail closed)
         ├─ INSERT operations row (canonical bytes + checksum)
         ├─ UPSERT meta hlc_wall / hlc_counter          ← same transaction
         └─ COMMIT  ←──── local ACK happens only after this
             └─ World.Apply (materialize)                     internal/crdt/world.go
                 └─ EventBus.Emit("op.local")                 internal/node/events.go
                     └─ kickSync() → sync loop pulls from peers on its next tick
```

## Replication flow (implemented)

```text
syncLoop (every --sync-interval-ms, or kicked)               internal/node/replicate.go
 └─ SyncRound → for each peer in peersSnapshot()
     └─ PullFromPeer(peer)
         ├─ FaultController.Gate(self, peer)                  internal/node/faults.go
         │    (delay / drop / duplicate / reorder / partition; re-checked AFTER each response)
         ├─ Store.Inventory(512)                              per-actor frontier + holes
         ├─ POST peer /api/v1/sync/pull  (real HTTP, ≤200 ops/page, cursor-resumable)
         │    └─ Node.ServePull → Store.MissingOpsFor(requester inventory)
         └─ for each op: Node.IngestOne(op, peer)
             ├─ core.Validate (fail closed → rejected with stable code)
             ├─ Store.IngestRemote (accepted | duplicate | rejected E_CONFLICT)
             ├─ Clock.Observe(op.HLC)   (future local writes dominate what we saw)
             ├─ World.Apply (materialize)
             └─ EventBus.Emit("op.remote" | "op.rejected")
```

Bidirectionality: every node pulls from every peer, so both directions drain without a coordinator. `PushToPeer` exists as a low-latency path but the pull loop is the correctness backstop.

## Convergence check

```text
GET /api/v1/lab/convergence        internal/node/lab.go
 └─ self: Store.OpDigest() (count + sha256 over sorted op IDs), World.Digest()
 └─ each peer: GET /api/v1/node  ← management plane, deliberately OUTSIDE
 │                              the simulated sync network (a partitioned-but-alive
 │                              peer is reported truthfully as DIVERGED)
 └─ CONVERGED iff every reachable peer matches on count + op digest + state digest
   DIVERGED  if reachable but different
   UNKNOWN   if any peer unreachable (unreachability is NOT convergence)
```

## Persistence (internal/store)

- `schema_migrations(version, applied_ms)` — versioned migrations, applied in one transaction each.
- `operations` — one row per operation: canonical JSON, checksum, origin (`local`/peer id), received_ms, `(actor, actor_seq)` UNIQUE, op_id PRIMARY KEY. Nothing is ever deleted or updated.
- `meta` — replica identity (`replica_id`), HLC watermark. A database file REFUSES to run under a different replica ID (`TestIdentityIsBoundToDatabaseFile`).
- `idempotency` — durable Idempotency-Key → response map for API retries (bounded to 10k rows).
- Materialized state is rebuilt by replaying `AllOps()` at startup; correctness never depends on arrival order (permutation tests).

## Event bus

`internal/node/events.go` fans out bounded (128) per-subscriber channels with drop-oldest on overflow; every event carries a monotonically increasing `seq` so UIs can detect loss. The API exposes it as SSE (`GET /api/v1/events`, ≤64 subscribers, 15s heartbeats).
