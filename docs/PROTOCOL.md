# PROTOCOL.md — ConvergeDB wire contract (protocol version 1)

All sync traffic is HTTP + JSON between replicas. Default binding is loopback; see SECURITY.md for the trust model. Every JSON body below is canonical for operations (fixed struct field order, no HTML escaping).

## Versions and negotiation

- `protocol_version: 1` is carried by every operation and every sync request/response.
- A replica receiving any other version replies `409` with code `E_PROTOCOL`. There is no downgrade path.

## Operation envelope

```json
{
  "protocol_version": 1,
  "op_id": "<64 lowercase hex>",
  "doc_id": "<[a-z0-9][a-z0-9-]{0,63}>",
  "field": "" | "title" | "status" | "labels" | "votes",
  "kind": "doc.create|doc.delete|doc.restore|field.set|label.add|label.remove|vote.delta",
  "actor": "<[a-z0-9][a-z0-9-]{0,31}>",
  "actor_seq": 1,
  "hlc": { "wall_ms": 1690000000000, "counter": 0, "actor": "alpha" },
  "payload": { "value": "", "tag": "", "tags": [], "delta": 0 }
}
```

- **Canonical encoding**: Go struct field order as shown, `encoding/json` with HTML escaping disabled, no trailing newline. `payload` members unused by the kind MUST be zero/absent (enforced).
- **op ID** = `sha256(canonical(envelope with op_id blanked))`, hex. Validation fails closed (`E_ID`) if a submitted op ID does not match its content — an operation ID can never travel with a different body.
- **Checksum** = `sha256(canonical(envelope))`, hex, stored with the row and verified on every read (corruption detection only — it is NOT an authentication signature).
- **HLC** is issued by `op.actor` (`hlc.actor == actor` is enforced). Ordering: `(wall_ms, counter, actor)` lexicographic. `wall_ms` is milliseconds since the Unix epoch; `counter` disambiguates events within one millisecond or after wall-clock rollback; `actor` is the final total-order tie-break.
- **Bounds** (fail closed, `E_LIMIT`): canonical op ≤ 16 KiB; title ≤ 512 runes; label value 1..64 runes; `label.remove` carries 1..200 tags; `|delta|` ≤ 1e9 and non-zero.
- **Add-tag derivation** (protocol-frozen): `label.add` payloads carry `tag = hex(sha256("convergedb:add-tag:" + actor + ":" + actor_seq))`. Unique because `(actor, actor_seq)` is unique; deterministic so seeded histories reproduce.

## Sync endpoints

### `GET /api/v1/sync/inventory`
Returns `{protocol_version, node, inventory: ActorInventory[]}`.

`ActorInventory` — what this replica holds per actor:

```json
{
  "actor": "alpha",
  "frontier": 42,          // highest seq with NO gaps from 1
  "max_seen": 50,          // highest seq received at all
  "received_above": [45, 47], // sorted seqs in (frontier, max_seen] already held
  "partial": false         // received_above was truncated to the cap (512)
}
```

Hole-awareness contract: advertising only `max_seen` would hide permanently missing middles; the frontier + `received_above` pair lets a peer compute exactly which sequences it must send. `partial: true` forces the safe fallback: resend the whole `(frontier, max_seen]` range (bounded redundancy, never loss).

### `POST /api/v1/sync/pull`
Request: `{protocol_version, requester, inventory: ActorInventory[], cursor?, limit?}`.
Response: `{protocol_version, responder, page: {ops: Operation[], has_more, next_cursor?}}`.

- The responder computes, for each of its actors, the ops the requester is missing (above the requester's frontier, skipping its `received_above`; everything if the requester has no entry for that actor).
- **Pagination**: `limit` ≤ 200 ops/page (default and max). `next_cursor` encodes the LAST RETURNED op; resume re-examines strictly after it, so nothing between pages is skipped. When a page ends exactly at the final row, the follow-up page is empty with `has_more=false`.
- **Cursor semantics**: cursors remain valid as other operations arrive (the log is append-only; the cursor is an (actor, seq) position, not a snapshot timestamp). There is deliberately no time-based cursor — clock skew would skip operations.
- Pull is the correctness backstop and runs periodically per peer; every allowed exchange is a real HTTP round trip.

### `POST /api/v1/sync/push`
Request: `{protocol_version, sender, ops: Operation[]}` (≤200 ops).
Response: `{protocol_version, responder, outcomes: [{result: "accepted"|"duplicate"|"rejected", reason?, op_id}]}`.

Ingest is idempotent and fails closed per op:

| case | result | code |
|---|---|---|
| op_id exists with identical checksum | `duplicate` | — |
| op_id exists with different checksum | `rejected` | `E_CONFLICT` |
| `(actor, actor_seq)` exists with different op_id | `rejected` | `E_CONFLICT` |
| fails schema/validation | `rejected` | `E_ID`/`E_SCHEMA`/`E_LIMIT`/`E_PROTOCOL`/`E_CLOCK` |

## Error responses

All errors are `{"error": {"code": "...", "detail": "..."}}` with stable codes: `E_PROTOCOL, E_ID, E_KIND, E_FIELD, E_SCHEMA, E_LIMIT, E_CLOCK, E_CONFLICT, E_AUTH, E_LAB, E_STORE, E_INTERNAL, E_NOT_FOUND, E_CORS`. HTTP mapping: 400 schema/identity, 401 auth, 403 lab/CORS, 404 unknown, 405 method, 409 protocol/conflict, 413 size.

## Fault-shim semantics (lab mode)

Each replica's sync client consults a seeded fault gate per directed link. Allowed exchanges are real HTTP; the shim may delay, drop, duplicate a page, or reorder a page's ops before ingest. A partition set while a request is in flight causes the in-flight response to be DISCARDED — the shim is authoritative for the whole exchange. A real network cannot do this; the shim simulates the network, not the nodes.

## Explicitly out of scope for v1

TLS and peer identity verification; incremental snapshot bootstrap; tombstone GC; multi-document transactions; rich-text merging. Changing canonical encoding, op kinds, bounds, or the add-tag derivation requires a protocol version bump.
