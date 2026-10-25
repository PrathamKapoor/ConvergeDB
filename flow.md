# flow.md — how the system works

This file documents REAL call/data flows using real names from the code. When a flow changes, edit the section that describes it (Rule 4), and add a row to the "Changes in this session" table at the end (Rule 5).

Status legend: each flow section is marked PLANNED until the code exists, then kept in sync with the implementation.

## 1. Repository layout (implemented = files exist)

- `cmd/convergedb/` — CLI entrypoint (serve, demo, inspect, verify, bench, doctor) — PLANNED
- `internal/clock/` — hybrid logical clock — **IMPLEMENTED** (`hlc.go`: `HLC`, `Compare`, `New`, `Restore`, `Now`, `Observe`)
- `internal/core/` — typed operations, canonical encoding, checksums, validation — **IMPLEMENTED** (`op.go`: `Operation`, `Canonical`, `Checksum`, `ComputeOpID`, `DeriveAddTag`; `validate.go`: `Validate`, rejection codes)
- `internal/crdt/` — LWW register, OR-Set, PN/delta counter, document materialization — **IMPLEMENTED** (`crdt.go`: `LWWRegister`, `ORSet`; `counter.go`: `PNCounter`; `world.go`: `World`, `Document`, `CanonicalState`, `Digest`)
- `internal/store/` — SQLite migrations, durable operation log, actor sequences — **IMPLEMENTED** (`store.go`: `Open`/migrations/`GetMeta`/`SetMeta`; `ops.go`: `AppendLocal`, `IngestRemote`, `Inventory`, `MissingOpsFor`, `AllOps`, `OpDigest`, `ListOps`)
- `internal/sync/` — inventory, gap detection, pull/push paging, transport faults — PLANNED
- `internal/node/` — node lifecycle, local writes, remote apply, event bus — PLANNED
- `internal/api/` — HTTP endpoints, auth, limits, SSE events — PLANNED
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
