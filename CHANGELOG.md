# Changelog

All notable changes to this project are documented here. Format: Keep a Changelog; versioning: SemVer.

## [0.1.0] — 2026-10-26

### Added
- Hybrid logical clock with rollback safety, persisted watermark and total ordering.
- Typed operation envelope (protocol v1): canonical JSON, content-addressed op IDs, SHA-256 integrity checksums, fail-closed validation with stable error codes.
- Merge types: LWW register (deterministic tie-break), OR-Set with persistent tombstones (add-wins), operation-delta counter (idempotent, overflow-safe).
- Durable SQLite store (WAL, synchronous=FULL): versioned migrations, dense actor-sequence allocation inside the commit transaction, hole-aware inventories, checksum-verified reads, idempotent remote ingest with I-04 conflict rejection.
- Replica node: persistent identity bound to the database file, replay-rebuild, event bus, seeded fault controller (partition/drop/duplicate/delay/reorder), anti-entropy sync loop, convergence checker (CONVERGED / DIVERGED / UNKNOWN).
- Versioned HTTP API: health, node info, documents, operations, sync pull/push/inventory, lab fault endpoints, SSE events, bearer auth, durable idempotency keys.
- CLI: serve, demo partition-heal (seeded 3-process scenario with JSON report), inspect, verify, bench, doctor.
- Web workbench (React + TypeScript + Vite): topology, live replica editors, event timeline, operation inspector, convergence banner with staleness indicator, fault panel.
- Verification: 48 tests incl. exhaustive 7!-permutation convergence, duplicate-laden schedules, HTTP-level replication, Level D hardening; race-clean.
- Benchmarks with exact counter assertions and an honest NOT-MEASURED ledger.
