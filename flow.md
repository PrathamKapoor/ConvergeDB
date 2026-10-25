# flow.md — how the system works

This file documents REAL call/data flows using real names from the code. When a flow changes, edit the section that describes it (Rule 4), and add a row to the "Changes in this session" table at the end (Rule 5).

Status legend: each flow section is marked PLANNED until the code exists, then kept in sync with the implementation.

## 1. Repository layout (implemented = files exist)

- `cmd/convergedb/` — CLI entrypoint (serve, demo, inspect, verify, bench, doctor) — PLANNED
- `internal/clock/` — hybrid logical clock — PLANNED
- `internal/core/` — typed operations, canonical encoding, checksums, validation — PLANNED
- `internal/crdt/` — LWW register, OR-Set, PN/delta counter, document materialization — PLANNED
- `internal/store/` — SQLite migrations, durable operation log, actor sequences — PLANNED
- `internal/sync/` — inventory, gap detection, pull/push paging, transport faults — PLANNED
- `internal/node/` — node lifecycle, local writes, remote apply, event bus — PLANNED
- `internal/api/` — HTTP endpoints, auth, limits, SSE events — PLANNED
- `tests/model/` — permutation/property tests — PLANNED
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
| Project bootstrap | Created root ledgers, scope doc, license, .gitignore | decisions.md, flow.md, handoff.md, docs/SCOPE.md, LICENSE, .gitignore | (this commit) | D-001, D-003 |
| Toolchain | Portable Go 1.27.3 installed at C:\Users\LENOVO\tools\go (outside repo, no global changes) | none | (this commit) | D-002 |
