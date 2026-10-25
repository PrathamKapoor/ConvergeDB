# SECURITY.md — threat model and supported scope

## Supported scope: trusted-local

ConvergeDB is designed and tested for **trusted, single-user, localhost operation** — a laboratory for replication semantics. This is the whole of the supported threat model.

What is enabled by default:

- HTTP binds to `127.0.0.1` (or `localhost`). The CLI **refuses to start** on a non-loopback address without `--token`.
- Without a token, all endpoints are open to local processes only. This is the documented trusted-local mode.
- With `--token <secret>` (or the `token` config field), every path except `/health/live` and `/health/ready` requires `Authorization: Bearer <token>`; failures are `401 E_AUTH` (`TestAuthBoundaryFailsClosed`).
- Lab fault-injection endpoints (`/api/v1/lab/*` mutations) answer only when the node was started with `--lab`; otherwise `403 E_LAB` (`TestLabEndpointsDisabledWithoutLabMode`). Fault injection is a localhost-only demo facility and must never be exposed on an untrusted network.

## What is explicitly NOT supported / NOT hardened against

- **No TLS.** Traffic is plaintext HTTP. Do not expose a node beyond loopback.
- **No peer identity verification.** A shared bearer token authenticates "someone who knows the token"; there is no per-peer keying, no certificate checks, no signing of operations. The op checksum is corruption detection, NOT a signature.
- **Hostile peers can corrupt the data model within protocol rules.** A malicious peer can write arbitrary valid operations (including winning LWW with a future-skewed HLC) and push unbounded (but per-request-bounded) operations into the log. There is no authorization model on documents or fields.
- **No rate limiting** beyond body-size and page-size bounds (16 KiB/op canonical, 4 MiB request, 200 ops/page, 64 SSE subscribers).
- **No secret material anywhere**: no credentials in configs, no telemetry, no external services. Demo data is synthetic.
- **DoS resistance is not a goal.** The bounds above are sanity limits, not defenses against a determined adversary.

## Failure-closed behavior (tested)

- Unknown protocol version → `409 E_PROTOCOL`; malformed identities/schema/sizes → `400` with stable codes; body over 4 MiB → `413 E_LIMIT`; unknown lab route without `--lab` → `403 E_LAB` (Level D suite, `internal/api/hardening_test.go`).
- A peer reusing an op ID with different content is rejected `E_CONFLICT` and logged — never silently overwritten (I-04).
- A database file refuses to start under a different replica ID than the one stored in it.
- Stored rows whose checksum no longer matches their canonical bytes fail closed on read (`TestCorruptedStoredOperationFailsClosed`) — no silent "repair" by deletion.

## Data handling

- Runtime databases live under the operator's chosen paths (`var/` by default for demos) and are git-ignored. The repository contains no runtime `.db` files.
- The demo, UI, and fixtures contain only synthetic data.
- Logs (per-replica `*.db.log` files in demo dirs) contain request/error lines, never credentials (there are none to log).

## Residual risks (honest list)

1. A local process can read/modify the SQLite files directly; the DB is only as safe as your user account.
2. The fault shim's authority over in-flight exchanges (discard-on-partition) is a lab convenience that no real network layer could offer; treat lab results as modeling the sync protocol, not firewall behavior.
3. HLCs from any writer are accepted within protocol bounds — a future-skewed clock wins LWW conflicts (trusted-local assumption).
4. Tombstones and the operation log grow unboundedly by design (no safe GC).
