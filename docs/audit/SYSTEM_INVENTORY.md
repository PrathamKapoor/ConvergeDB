# SYSTEM INVENTORY — second pass (2026-10-26, base commit d264fb4)

Every meaningful component accounted for: path, purpose, inputs → outputs, side effects, trust boundary, failure mode, tests, verification status. Generated during the second-pass audit; statuses are evidence-based, not aspirational.

## Go core

| Path | Purpose | Inputs → Outputs | Side effects | Trust boundary | Failure mode | Tests | Status |
|---|---|---|---|---|---|---|---|
| internal/clock/hlc.go | hybrid logical clock | local/remote events → HLC | none (pure) | none | wall-clock rollback tolerated (counter++); int64 counter overflow theoretical | TestNow*, TestRollback*, TestObserve*, TestRestore*, TestCompare | VERIFIED |
| internal/core/op.go | operation envelope, canonical JSON, op ID, checksum, add-tag derivation | Operation → bytes/ids | none | canonical encoding is wire-frozen (v1) | encoding cannot fail for valid structs | TestCanonicalEncoding*, TestOpID*, TestChecksum*, TestDeriveAddTag (via suite) | VERIFIED |
| internal/core/validate.go | fail-closed schema validation | Operation → error | none | rejects malformed remote input | none known; error codes stable | TestValidate* (18 cases) | VERIFIED |
| internal/crdt/crdt.go | LWW register, tombstoned OR-Set | ops → merged state | none | merge is order-independent | none known | TestLWW*, TestORSet* | VERIFIED |
| internal/crdt/counter.go | delta counter | (opID, delta) → sum | none | dedup by opID; overflow fail-closed | ErrCounterConflict on opID/delta contradiction (protocol bug only) | TestCounterDedup* | VERIFIED |
| internal/crdt/world.go | document materializer + canonical state + digest | op stream → docs, bytes, digest | none | deterministic function of op SET | counter overflow → error (extreme edge, documented) | TestWorld*, model suite | VERIFIED |
| internal/store/store.go | SQLite open/migrate/meta | path → Store | file I/O, WAL+FULL | local file trust | migration failure aborts startup; corrupt DB fails closed | TestFreshOpen*, TestCorruptedStored* | VERIFIED |
| internal/store/ops.go | append/ingest/inventory/pull queries | ops → durable rows, pages | file I/O | UNIQUE(actor,seq), PK op_id, checksum verify on read | **O(N) scan per Inventory/MissingOpsFor call — P1 bottleneck** | store suite | PARTIAL (correct; scale fix pending) |
| internal/node/node.go | replica lifecycle, write path, ingest | API calls → durable ops + events | SQLite, goroutine (sync loop) | loopback HTTP; local file | unknown doc/existing doc errors; materialize errors surface loudly | node tests via api suite | VERIFIED |
| internal/node/replicate.go | sync loop, pull/push client+handlers | peers → op transfer | outbound HTTP | **new http.Client per pull — no keep-alive (P2)**; fault gate re-check after response | peer down → LastError recorded; partition → blocked | replication suite | PARTIAL (perf fix pending) |
| internal/node/faults.go | seeded fault controller | links → Gate decisions | none | lab-only | none | used by replication tests | VERIFIED |
| internal/node/lab.go | node info + convergence check | peers → report | outbound HTTP (mgmt plane) | bypasses sync shim BY DESIGN | unreachable → UNKNOWN | TestConvergence* | VERIFIED |
| internal/node/events.go | event bus | Emit → subscribers | in-memory | bounded 128/sub, drop-oldest | slow subscriber loses oldest (documented) | TestEvents* | VERIFIED |
| internal/api/server.go | HTTP surface | requests → responses | SSE streams | auth (optional token), CORS localhost-only, 4 MiB bodies, lab gating | oversized → 413; malformed → 400 stable codes | hardening suite | PARTIAL (no server write/idle timeouts; operations limit unclamped — P2) |
| cmd/convergedb/*.go | CLI | flags → processes | child processes (demo), HTTP client (bench) | refuses non-loopback bind w/o token; --allow-unsafe-bind escape hatch (D-016) | partial-startup impossible (config validated first) | demo/doctor/verify run in CI + locally | VERIFIED |
| tests/model | exhaustive permutation + randomized exchange | op sets → digests | none | independent of production helpers | — | itself | VERIFIED |
| tests/adversarial | external-protocol scale/fault harness | stores/nodes → measurements | temp DBs | independent assertions | — | itself | PARTIAL (growing this pass) |

## HTTP surface (all v1, real paths)

| Route | Handler | Backing store/flow | Auth | Status |
|---|---|---|---|---|
| GET /health/live | api.handleLive | none | open | VERIFIED |
| GET /health/ready | api.handleReady | OpDigest (storage check) | token-gated when configured | VERIFIED |
| GET /api/v1/node | api.handleNodeInfo | Node.Info | gated | VERIFIED |
| POST /api/v1/documents | handleCreateDocument | Node.CreateDocument → commitLocal | gated | VERIFIED |
| GET /api/v1/documents | handleListDocuments | Node.Documents (deep clones) | gated | **PARTIAL — unbounded clone/serialization; limit param pending (P2)** |
| GET /api/v1/documents/{id} | handleGetDocument | Node.Document | gated | VERIFIED |
| POST /api/v1/documents/{id}/operations | handleMutate | Node.Mutate; Idempotency-Key durable | gated | VERIFIED |
| GET /api/v1/operations | handleOperations | Store.ListOps (cursor) | gated | **PARTIAL — limit not clamped to max (P2)** |
| GET /api/v1/sync/inventory | node.ServeInventory | Store.Inventory | gated | VERIFIED (perf pending) |
| POST /api/v1/sync/pull | node.ServePull | Store.MissingOpsFor | gated | VERIFIED (perf pending) |
| POST /api/v1/sync/push | node.ServePush | Node.IngestOne ×N | gated | VERIFIED |
| POST /api/v1/lab/partition|heal|faults | api handlers | FaultController | gated + --lab | VERIFIED |
| GET /api/v1/lab/convergence | api.handleConvergence | Node.Convergence | gated | VERIFIED |
| GET /api/v1/events | api.handleEvents | EventBus SSE | gated | VERIFIED |
| / (static) | spaHandler | web/dist | open | VERIFIED |

## Data model (SQLite, one file per replica)

- `schema_migrations(version, applied_ms)` — v1 tables, v2 idempotency. Migration runner: one tx per migration.
- `operations(op_id PK, actor, actor_seq UNIQUE(actor,actor_seq), protocol_version, doc_id, field, kind, canonical, checksum, origin, received_ms, hlc_*)` + idx (actor, actor_seq), idx (doc_id). Append-only; never updated/deleted.
- `meta(key,value)` — replica_id, hlc watermark.
- `idempotency(key, op_id, response, created_ms)` — bounded 10k rows.

## Frontend (web/)

Components: App (polling+SSE orchestration), Topology (SVG links, click=partition/heal), ReplicaPanel (editors, per-field HLC metadata), FaultPanel (per-direction controls), Timeline (SSE feed, bounded 120 shown), Inspector (ops list 40, per-actor holes), ConvergenceBanner (status + digests + age/staleness). All numbers from live APIs. UI audit table: docs/audit/FINDINGS.md §UI.

## CI / release infra

- .github/workflows/ci.yml — vet, tests, race, web build, seeded demo (+artifact). Verified green on GitHub (2/2 runs at d264fb4).
- Dockerfile + docker-compose.yml — provided, **NOT TESTED** (daemon unavailable) — carried over from first pass; still true.
- docs/* — README, PROTOCOL, CONSISTENCY, ARCHITECTURE, SECURITY, TEST_MATRIX, RESULTS, REPRODUCIBILITY, DEMO, COMPARISON, FAILURE_LOG.

## Known dead/unused/pending code

- `node.PushToPeer` — implemented, protocol-complete, not called by the sync loop (documented as opportunistic path; pull is the backstop). Not dead code (tested via fault paths? actually only via future use) — **flagged: unused in production flows (P3)**.
- `api.(*Server).spaHandler` cache headers — none (fine for lab; P3: immutable asset caching).
- `store.ActorInventory.Partial` fallback — tested, exercised in TestMissingOpsForIsHoleAware.
