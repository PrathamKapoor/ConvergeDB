# COMPARISON.md — where ConvergeDB sits

ConvergeDB is an educational/research-scale replicated document engine with a verification laboratory. It is NOT a competitor to production local-first stacks, and this page deliberately avoids superiority claims — these systems are mature, field-tested software.

## Versus CRDT libraries (Automerge, Yjs)

| | Automerge / Yjs | ConvergeDB |
|---|---|---|
| Focus | rich text / JSON merging in applications, client-side | server/daemon-side replication semantics you can watch and verify |
| Data types | large family incl. sequence/text CRDTs, rich JSON | three types only (LWW register, OR-Set, delta counter) + a fixed document schema |
| Convergence check | internal | first-class: op-inventory + canonical-state digests, `DIVERGED`/`UNKNOWN`/`CONVERGED` API |
| Transport | embedded (in-process documents) | HTTP anti-entropy between processes with hole-aware inventories |
| Maturity | production, years of hardening | laboratory, single release |

What ConvergeDB offers that those libraries don't target: an inspectable wire protocol (`docs/PROTOCOL.md`), durable per-replica operation logs with checksums and identity binding, fault injection against real processes, and convergence verification as an API. What they offer that ConvergeDB does not: text sequencing, offline-first app embedding, and battle-tested scale.

## Versus sync engines (ElectricSQL, RxDB)

ElectricSQL syncs Postgres ↔ clients (read-your-writes and CRDT-free conflict handling by design); RxDB is a client database with replication plugins. Both solve application data sync at production quality. ConvergeDB instead makes the *replication machinery itself* the product: every operation, gap, duplicate delivery, and conflict resolution is observable through the API and the UI, and the correctness obligations (I-01..I-12 in docs/SCOPE.md) are pinned by tests you can run.

## Versus distributed databases (Riak, CouchDB, Redis CRDB)

Production systems carry capabilities ConvergeDB explicitly does not have: multi-version concurrency, hinted handoff at scale, authentication/trust models, compaction/GC, formal operational tooling. Riak's dtype OR-sets and CouchDB's replication influenced the concepts implemented here; ConvergeDB's contribution is scope reduction with verification, not scale or features.

## Positioning summary

Use ConvergeDB to **learn, teach, demonstrate, and adversarially test** how operation-based replication, hole-aware anti-entropy, and verified convergence behave under partitions — including in a classroom or an interview, from a fresh clone, offline. Use the production tools above to ship applications.
