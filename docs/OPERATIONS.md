# OPERATIONS — running ConvergeDB for real (within its lab scope)

## Deployment profiles

1. **Trusted-local lab (supported):** N processes on 127.0.0.1, full mesh, `--lab` for fault injection. Start via `convergedb serve` flags, `examples/*.json` config files, or `docker-compose.yml`.
2. **Containerized lab (provided, NOT TESTED — daemon was unavailable):** `docker compose up --build`; three replicas on an internal network, alpha serves the UI. Uses `--allow-unsafe-bind` for container-internal 0.0.0.0 binds.
3. **Production Internet-facing: UNSUPPORTED.** No TLS, no peer identity verification, no multi-tenancy (docs/SECURITY.md). If you must bridge hosts, front the API with a TLS proxy AND set `--token`, and understand you are outside the tested threat model.

## Startup & readiness

- Readiness (`/health/ready`) verifies the store answers — it is NOT a claim that replication is caught up. Convergence is a separate, explicit check (`/api/v1/lab/convergence`): `CONVERGED` requires operation-inventory AND canonical-state digest equality on every reachable peer; `UNKNOWN` means a peer is unreachable.
- Configuration failures (missing id/db, identity mismatch, unsafe bind) abort BEFORE the HTTP listener starts — no partial startup.
- Startup replay: verified checkpoint + tail (fast path) or full replay (fallback); `GET /api/v1/events` emits a `lifecycle` event carrying `replay_mode`.

## Signals to watch

- `ops_replayed` / `replay_mode` in the lifecycle event; op_count/op_digest/state_digest on `/api/v1/node`.
- Per-peer `last_ok`, `last_error`, `last_pulled` (same endpoint) — sync failures, not just liveness.
- `sync.blocked` events (fault partitions), `op.rejected` events (protocol conflicts — a peer sending conflicting content is fail-closed by design and should be investigated).
- Storage pressure: log size grows monotonically (no GC — docs/SCALABILITY.md).

## Backup & restore (tested: tests/adversarial/backup_test.go)

```bash
# consistent online backup (VACUUM INTO; safe while running, includes WAL content)
convergedb admin backup --db ./var/alpha.db --out ./var/backups/alpha-$(date +%s).db.bak
```

- The backup is a complete standalone database at that POINT IN TIME, including the replica identity and durable operation log.
- **Restore-as-same-replica:** stop the replica, replace its database file with the backup, restart. Identity, sequence allocation, and HLC watermark restore exactly (verified: post-restore seq continues where the backup left off).
- **Bootstrap-as-new-replica:** NOT implicit. A restored file carries the original replica ID; running a second process on it elsewhere would create a duplicate actor identity — detectable (ops collide on actor_seq) but harmful. To clone history into a NEW replica, copy the file and consciously change the stored `replica_id` in `meta` BEFORE first use, accepting that the new node shares the old node's historical ops under its own actor ID only for FUTURE ops. If in doubt: start a fresh replica and let anti-entropy catch it up instead.
- RPO observation: an online backup captures everything committed before `VACUUM INTO` returns; writes after it are not in the backup (verified: post-backup ops absent, point-in-time semantics). RTO observation: restoring a 150-op backup and reopening is sub-second on the test host; larger logs restore in the time of a file copy + checkpoint/tail replay. These are OBSERVATIONS, not SLAs.

## Migrations

- Versioned (`schema_migrations`), applied in one transaction each at open; re-running is idempotent (current version checked first). v1: core tables; v2: idempotency; v3: checkpoint table.
- Forward-only. Before ANY manual intervention on a database: take a backup (above). Never edit the `operations` table by hand — canonical bytes + checksums are the integrity contract and every read verifies them (corruption fails closed).

## Troubleshooting

| symptom | likely cause | action |
|---|---|---|
| peer stuck `last_ok=false` | wrong URL, node down, or fault partition active | check `/api/v1/lab/faults`; check reachability; heal |
| `op.rejected E_CONFLICT` from a peer | two replicas running the SAME actor id, or an op ID reused with different content | stop the duplicate; this is fail-closed by design (I-04) |
| holes in the inspector never close | a peer advertising a partial inventory | they will full-range resend; if stuck, restart sync or verify both sides' `/api/v1/sync/inventory` |
| startup slow | no valid checkpoint (first run, or fallback after corruption) | expected on first run; checkpoints are written on graceful close / every CheckpointEveryOps ops |
| `state hash mismatch` on checkpoint | local corruption | safe: automatic full-replay fallback; consider a backup + fresh file |
| disk pressure | log + tombstones grow forever (no GC) | backup, then rebootstrap a replica from scratch and let it re-pull (bounded by peer log size) |

## Graceful shutdown

SIGINT/SIGTERM → sync loop stops → final materializer checkpoint written → SQLite closed. A SIGKILL skips the checkpoint (pure optimization — the next start full-replays and loses nothing; verified by the kill-mid-burst scenario).
