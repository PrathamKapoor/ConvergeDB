# ConvergeDB — Scope

## What ConvergeDB IS

ConvergeDB is a small, open-source, self-hostable **local-first replicated document database** with:

- Three data types with precisely specified merge semantics: **LWW register** (last-writer-wins with deterministic tie-break), **OR-Set** (observed-remove set, add-wins), **PN/delta counter** (commutative, idempotent via operation dedup).
- A **durable operation log per replica** (SQLite, WAL): a local write is acknowledged only after the SQLite transaction commits.
- **Bidirectional anti-entropy replication** over plain HTTP+JSON: per-actor frontier + hole tracking (out-of-order arrivals are not lost), cursor pagination, integrity checksums, idempotent ingest, rejection of op-ID reuse with different content.
- **Fault injection** (lab mode): partition/heal, delayed, dropped, duplicated, reordered sync traffic between REAL separate processes with REAL persisted state.
- A **visual replication laboratory** (React + TypeScript) wired to the real node APIs: three-replica topology, per-replica live state editors, event timeline, operation inspector, convergence verification that checks operation-inventory equality AND canonical state digests.

A **replica** is an independent OS process with its own SQLite database file and stable replica ID. Convergence means: replicas holding the same set of valid operations produce **byte-equivalent canonical logical state** (verified via digests over sorted operation IDs and canonical materialized documents).

## What ConvergeDB IS NOT

- Not a production replacement for Postgres/CouchDB/Riak.
- Not a drop-in Automerge/Yjs replacement (no rich text CRDT, no client-side library).
- Not formally verified — correctness claims are backed by executed tests only, within the stated scope.
- Not safe for public Internet exposure by default: binds to 127.0.0.1; peer/client auth beyond the configured token is out of scope (documented in SECURITY.md).
- Not a mocked dashboard: every number shown in the UI comes from a real node API.

## Invariants (I-01 … I-12) — each must have an executed test or an explicit open gap

| # | Invariant |
|---|---|
| I-01 | Every locally ACKed write survives restart of its own replica (tested crash model: process kill). |
| I-02 | Applying the same valid operation 0/1/many times yields the same state after first application. |
| I-03 | Applying the same operation set in ANY tested permutation yields identical canonical state. |
| I-04 | Duplicate `actor:seq` with a different checksum is REJECTED and logged, never silently overwritten. |
| I-05 | Replication is bidirectional, paginated, resumable, robust to reorder/duplication/interruption. |
| I-06 | Anti-entropy is hole-aware: per-actor contiguous frontier + received-set; `max_seen_seq` alone is insufficient. |
| I-07 | After healing and flushing, operation inventories AND materialized states agree across replicas. |
| I-08 | During a partition, isolated replicas keep accepting local writes (no central coordinator). |
| I-09 | OR-Set concurrency: concurrent adds survive observed removes; remove only kills tags it observed. |
| I-10 | Counter updates from independent replicas are neither lost nor double-applied. |
| I-11 | Invalid schemas, oversized payloads, malformed replies, unknown protocol versions, invalid identities fail closed with stable errors. |
| I-12 | Source, schema, fixtures, commands, benchmark outputs are reproducible and free of secrets or misleading claims. |

## Target MVP (must actually work and be tested)

1. HLC with persisted monotonicity, rollback safety, deterministic total order.
2. Typed operations with canonical encoding + SHA-256 checksum; op ID uniqueness; strict validation.
3. LWW register, OR-Set, delta counter; document materialization; document create/delete/restore semantics.
4. SQLite store: migrations, atomic seq allocation + insert + ACK-after-commit, restart recovery.
5. HTTP node API: health, node info, documents CRUD, operation log, sync endpoints, lab endpoints (lab-mode gated), SSE events.
6. Anti-entropy replication: inventory with holes, pull/push, paging, retries, fault shim, convergence check.
7. Seeded one-command 3-replica partition→heal demo producing a JSON verification report.
8. Web laboratory wired to the real APIs.
9. Test levels A–E per the mission; honest RESULTS/FAILURE_LOG.

## Stretch (only after MVP)

- Benchmarks (latency/convergence) with published scripts and raw data; naive-baseline comparison.
- Docker Compose 3-node lab; CI workflow.
- Snapshot bootstrap, richer trace export.

## Risks / constraints

- Windows host: use pure-Go SQLite driver (no CGO); ports 8101–8103 localhost.
- GitHub push authentication unverified (no gh CLI) — test before claiming publication.
- Single session budget: prioritize verified core over cosmetics; record open gaps truthfully.

## Assumptions about the environment

- Trusted single-user machine; lab mode authorized on localhost only.
- Demo data is synthetic; no real user data anywhere in the repo.
