# flow.md — how the system works

This file documents REAL call/data flows using real names from the code. When a flow changes, edit the section that describes it (Rule 4), and add a row to the "Changes in this session" table at the end (Rule 5).

Status legend: each flow section is marked PLANNED until the code exists, then kept in sync with the implementation.

## 1. Repository layout (implemented = files exist)

- `cmd/convergedb/` — CLI entrypoint (serve, demo, inspect, verify, bench, doctor) — **IMPLEMENTED** (`main.go`, `serve.go`, `demo.go`: seeded partition-heal scenario, `tools.go`, `harness.go`)
- `internal/clock/` — hybrid logical clock — **IMPLEMENTED** (`hlc.go`: `HLC`, `Compare`, `New`, `Restore`, `Now`, `Observe`)
- `internal/core/` — typed operations, canonical encoding, checksums, validation — **IMPLEMENTED** (`op.go`: `Operation`, `Canonical`, `Checksum`, `ComputeOpID`, `DeriveAddTag`; `validate.go`: `Validate`, rejection codes)
- `internal/crdt/` — LWW register, OR-Set, PN/delta counter, document materialization — **IMPLEMENTED** (`crdt.go`: `LWWRegister`, `ORSet`; `counter.go`: `PNCounter`; `world.go`: `World`, `Document`, `CanonicalState`, `Digest`)
- `internal/store/` — SQLite migrations, durable operation log, actor sequences — **IMPLEMENTED** (`store.go`: `Open`/migrations/`GetMeta`/`SetMeta`; `ops.go`: `AppendLocal`, `IngestRemote`, `Inventory`, `MissingOpsFor`, `AllOps`, `OpDigest`, `ListOps`)
- `internal/sync/` — inventory, gap detection, pull/push paging, transport faults — PLANNED
- `internal/node/` — node lifecycle, local writes, remote apply, event bus — **IMPLEMENTED** (`node.go`: `Open`/`CreateDocument`/`Mutate`/`IngestOne`; `replicate.go`: `syncLoop`/`SyncRound`/`PullFromPeer`/`ServePull`/`ServePush`; `faults.go`: `FaultController`; `lab.go`: `Convergence`; `events.go`: `EventBus`)
- `internal/api/` — HTTP endpoints, auth, limits, SSE events — **IMPLEMENTED** (`server.go`: routes, middleware, lab guards, SSE)
- `tests/model/` — exhaustive permutation + randomized exchange model tests — **IMPLEMENTED** (`permutation_test.go`)
- `tests/integration/` — multi-process scenario tests — PLANNED
- `web/` — React + TypeScript visual workbench — PLANNED

## 2. Canonical local write flow (target; the implemented truth replaces this text as code lands)

```text
POST /api/v1/documents | /api/v1/documents/{id}/operations
 └─ validate payload (schema, size, types)                internal/core
     └─ allocate actor_seq + advance HLC                  internal/clock, internal/store
         └─ INSERT operation + UPDATE meta in ONE SQLite transaction (WAL)
             └─ COMMIT  ← local ACK happens only after this
                 └─ apply operation to in-memory materializer  internal/crdt
                     └─ emit local event (SSE)         internal/node event bus
                         └─ opportunistic push to peers internal/sync
```

Why: local acknowledgment must mean durable local commit, not network delivery.

## 3. Replication / anti-entropy flow (target)

```text
sync loop per peer (interval + jitter)
 └─ GET/POST /api/v1/sync/inventory — per-actor {frontier, received_above, max_seen}
     └─ diff against local operations (hole-aware: seq 7 before 6 must not hide 6)
         └─ POST /api/v1/sync/pull with cursor paging → validate → ingest idempotently
             └─ per-op result: accepted | duplicate | rejected(reason)
                 └─ rematerialize if changed; emit sync events
```

## Changes in this session (Phase 00)

| Area | Change | Files | Commit | Decision |
|---|---|---|---|---|
| Project bootstrap | Created root ledgers, scope doc, license, .gitignore | decisions.md, flow.md, handoff.md, docs/SCOPE.md, LICENSE, .gitignore | 0f94404 | D-001, D-003 |
| Toolchain | Portable Go 1.27.3 installed at C:\Users\LENOVO\tools\go (outside repo, no global changes) | none | 0f94404 | D-002 |

## Changes in this session (Phase 01)

| Area | Change | Files | Commit | Decision |
|---|---|---|---|---|
| HLC | Hybrid logical clock: total order (wall, counter, actor), rollback-safe, persisted-restore, observe watermark | internal/clock/hlc.go, internal/clock/hlc_test.go | (this commit) | D-007 |
| Core operations | Typed op envelope, canonical JSON, content-addressed op ID, checksum, add-tag derivation, fail-closed validation with stable codes | internal/core/op.go, internal/core/validate.go, internal/core/op_test.go | (this commit) | D-006, D-007 |
| CRDTs | LWW register (max-merge + deterministic tie-break), OR-Set with tombstones, delta counter with dedup + overflow fail-closed | internal/crdt/crdt.go, internal/crdt/counter.go | (this commit) | D-005 |
| Materialization | World: op stream -> documents -> canonical JSON state + SHA-256 digest; create-arrives-last buffered | internal/crdt/world.go | (this commit) | D-005 |
| Model tests | Exhaustive 7!-permutation convergence, duplicate-laden schedules, two-replica exchange model | tests/model/permutation_test.go | 35b740a | D-005 |
| Bug fix | OR-Set eager deletion was order-dependent; rebuilt with tombstones | internal/crdt/crdt.go, docs/FAILURE_LOG.md | 35b740a | D-005 |

## Changes in this session (Phase 02)

| Area | Change | Files | Commit | Decision |
|---|---|---|---|---|
| Persistence | SQLite store: migrations, WAL+FULL sync pragmas, meta table, dense actor-seq allocation in-tx | internal/store/store.go, internal/store/ops.go | (this commit) | D-008, D-009 |
| Ingest | Idempotent remote ingest: duplicate vs rejected (I-04 conflicts), checksum-verified reads, corruption fails closed | internal/store/ops.go | (this commit) | D-008 |
| Inventory | Hole-aware per-actor inventory (frontier + received_above + partial flag) and MissingOpsFor paging with resume cursor | internal/store/ops.go | (this commit) | D-006, D-010 |
| Bug fix | Pagination cursor skipped the next unsent op at page boundaries; cursor now = last returned op | internal/store/ops.go, docs/FAILURE_LOG.md | (this commit) | D-010 |
| Dependency | modernc.org/sqlite v1.61.0 added (pure Go) | go.mod, go.sum | (this commit) | D-008 |

## Changes in this session (Phases 03-05: node, API, replication, demo)

| Area | Change | Files | Commit | Decision |
|---|---|---|---|---|
| Node | Replica lifecycle, persistent identity, local write path, remote ingest, event bus, fault controller, convergence checker | internal/node/*.go | bbd2c72 | D-011 |
| API | Versioned HTTP endpoints incl. sync protocol, lab endpoints, SSE, bearer auth, durable Idempotency-Key | internal/api/server.go, internal/store/store.go | bbd2c72 | D-011 |
| CLI | serve/demo/inspect/verify/bench/doctor; seeded 3-replica partition-heal demo writing a JSON report | cmd/convergedb/*.go | bbd2c72 | D-011 |
| Demo gate | partition -> isolated writes -> DIVERGED -> heal with duplicate+reorder -> kill/restart beta -> CONVERGED + 6 semantic checks | cmd/convergedb/demo.go, var/reports | bbd2c72 | D-011 |
| Bug fix | Lab partition leaked one in-flight exchange; gate re-checked after response | internal/node/replicate.go, docs/FAILURE_LOG.md | bbd2c72 | D-012 |
| Bug fix | Node read API aliased live CRDT state (data race); deep snapshots added | internal/crdt/world.go, internal/node/node.go | bbd2c72 | D-013 |
| Tests | HTTP-level replication tests: bidirectional sync, partition/heal, duplicate+reorder storms, hole closing, rejections, convergence statuses, identity binding | internal/api/replication_test.go | bbd2c72 | D-012, D-013 |

## Changes in this session (Phases 07-10: hardening, benchmarks, docs, release)

| Area | Change | Files | Commit | Decision |
|---|---|---|---|---|
| Hardening | Level D suite: auth, lab gating, malformed input, 413/409/405/404, durable idempotency replay | internal/api/hardening_test.go, internal/api/server.go | b38c60f | D-011 |
| Benchmarks | three-replica heal→converged profile with exact counter assertion; bench harness precondition fix | cmd/convergedb/tools.go, docs/RESULTS.md | b38c60f | D-011 |
| Web | Cache-Control no-store + stale-view banner | internal/api/server.go, web/src | 3e5d2af | D-015 |
| Docs | README/PROTOCOL/CONSISTENCY/ARCHITECTURE/SECURITY/TEST_MATRIX/REPRODUCIBILITY/DEMO/COMPARISON, CI, Docker files, examples | docs/*, .github/workflows/ci.yml, Dockerfile, docker-compose.yml | c393ab8 | D-016 |
| Release | Repo created on GitHub and pushed; HEAD verified on remote | (remote) | c393ab8 (pushed) | — |
