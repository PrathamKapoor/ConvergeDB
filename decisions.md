# decisions.md — why things were done (append-only)

Format per rule: `### D-NNN:` entries appended at the end. Never edit or delete old entries.

### D-001: Project location and repository name
- Date / phase / commit: 2026-10-26, Phase 00, uncommitted
- Context: Mission requires a NEW dedicated project directory named ConvergeDB, published later as github.com/PrathamKapoor/ConvergeDB.
- Decision: Use `C:\Projects\ConvergeDB` (verified empty before starting) as the project root; repository name `ConvergeDB`.
- Why: `GET https://api.github.com/repos/PrathamKapoor/ConvergeDB` returned HTTP 404 on 2026-10-26 (no collision, no duplicate). Local directory was empty. Existing repositories of the owner were checked (ArcTrellis, AegisCPG, Experiment-Fabric, BoundaryProof, Closure-Trace, GhostDep, etc.) — none is a distributed database, so scope does not collide.
- Alternatives considered: a differently named repo variant — not needed because the name is free.
- Consequences: Future work must keep this directory the only write target; no other repository may be touched.
- Files: . (root)

### D-002: Portable user-local Go toolchain instead of a system-global install
- Date / phase / commit: 2026-10-26, Phase 00, uncommitted
- Context: Go is required for the preferred implementation stack, but no Go toolchain existed on the machine (checked PATH, `C:\Program Files\Go`, `C:\Go`, scoop, chocolatey, `where go`). The mission forbids silent system-global dependency installs.
- Decision: Downloaded the official `go1.27.3.windows-amd64.zip` from go.dev and extracted it to `C:\Users\LENOVO\tools\go` (user profile, outside any repository). The toolchain is invoked via explicit PATH prefix per shell invocation; no system PATH, environment variable, or global configuration was modified.
- Why: Verified `go version` → `go version go1.27.3 windows/amd64`. User-local and fully reversible (delete the folder). Keeps the mission's Go-first stack intact.
- Alternatives considered: winget system-wide install (rejected: system-global change); pivoting the whole stack to Node/TypeScript (rejected: Go preferred and no longer blocked); chocolatey (not installed).
- Consequences: Every shell that builds/tests must prefix `PATH=/c/Users/LENOVO/tools/go/bin:$PATH`. CI/Docker use official Go images instead. The toolchain directory is NOT part of the repository.
- Files: none in repository (toolchain only)

### D-003: Go module path, stack, and license
- Date / phase / commit: 2026-10-26, Phase 00, uncommitted
- Context: The repository needs a stable module identity, an implementation stack, and an OSI license before the first code commit.
- Decision: Go module path `github.com/PrathamKapoor/ConvergeDB`. Core + node server + CLI in Go; web laboratory in React + TypeScript + Vite. License: MIT (copyright "ConvergeDB contributors").
- Why: Go standard library covers HTTP/JSON/crypto/hash; the mission names Go as preferred. MIT is OSI-compatible, is the license used by most existing repos of this owner (verified in the public repo list), and satisfies "check existing conventions" for a user-owned project.
- Alternatives considered: Apache-2.0 (also a mission candidate; MIT chosen to match the owner's existing convention); keeping the module unversioned (rejected: breaks reproducible imports).
- Consequences: All internal imports use the `github.com/PrathamKapoor/ConvergeDB/...` prefix. LICENSE must ship in the repo and be honored for vendored/third-party code.
- Files: go.mod, LICENSE

### D-004: SQLite driver choice to be verified before recording
- Date / phase / commit: 2026-10-26, Phase 00, uncommitted
- Context: Persistence requires SQLite with WAL. On this Windows host no C toolchain (gcc) is confirmed present, so a CGO-dependent driver may not build.
- Decision: Record exact driver and version only after installing and running it. Candidate: `modernc.org/sqlite` (pure Go, no CGO). Placeholder entry so the numbering stays ordered; this entry will be superseded by the verified choice in Phase 02.
- Why: Mission requires recording the verified choice, not guessing versions.
- Alternatives considered: unknown until Phase 02 verification runs.
- Consequences: none yet.
- Files: none yet

### D-005: OR-Set uses persistent tombstones (fix for order-dependent divergence)
- Date / phase / commit: 2026-10-26, Phase 01, 35b740a
- Context: The first OR-Set implementation deleted observed tags eagerly. Exhaustive permutation tests diverged: a remove arriving before the add it observed did nothing, and the later add resurrected the value — state depended on arrival order (I-02/I-03 violated).
- Decision: Rebuild OR-Set as `adds` + `tombstones` maps (`internal/crdt/crdt.go`); live = adds minus tombstones. A remove tombstones exactly the tags it names. Tombstones are never garbage-collected.
- Why: Commutative union of tombstones makes merge order irrelevant; the mission requires "retain required tombstones" and forbids unverified GC. Tests: exhaustive 7!-permutation suite plus duplicate-laden schedules now pass deterministically.
- Alternatives considered: eager deletion (failed, see docs/FAILURE_LOG.md); full state-based OR-Set serialization for sync (not needed: operations carry observed tag sets directly).
- Consequences: Storage grows with removed tags — an honest, documented cost until causal-stability GC is proven safe (mission §7 forbids it now).
- Files: internal/crdt/crdt.go, internal/crdt/crdt_test.go, docs/FAILURE_LOG.md
- Symptom: `TestWorldPermutationInvariance` divergence at permutation 121; digests differed across apply orders.
- Root cause: eager tag deletion is not a commutative merge; remove-before-add lost the remove's intent.
- Regression test: `TestORSetObservedRemoveSemantics`, `TestORSetAddWinsAcrossAnyApplyOrder`, `tests/model` exhaustive permutations.

### D-006: Add-tag derives from (actor, actor_seq), not from the op ID
- Date / phase / commit: 2026-10-26, Phase 01, 35b740a
- Context: Content-addressed op IDs (SHA-256 over canonical content) plus "tag == op ID" for label.add are mutually circular: the tag would have to contain its own hash.
- Decision: `core.DeriveAddTag(actor, actorSeq)` = hex SHA-256 of `"convergedb:add-tag:" + actor + ":" + seq`. Validation requires label.add payloads to carry exactly this tag.
- Why: (actor, actor_seq) is unique per operation, so the tag is unique; derivation is deterministic, so seeded demos and property tests reproduce identical histories; the tag is covered by the op ID content hash, tying it to the add operation.
- Alternatives considered: random 128-bit tags (breaks reproducibility of seeded tests); tag == op ID (circular, unimplementable).
- Consequences: PROTOCOL.md must specify this derivation; changing the derivation string would invalidate stored tags, so it is frozen for protocol v1.
- Files: internal/core/op.go, internal/core/validate.go, internal/crdt/crdt_test.go, tests/model/permutation_test.go

### D-007: Canonical encoding = fixed-order struct JSON, no HTML escaping; op ID = SHA-256 of canonical bytes with OpID blanked; checksum = SHA-256 of full canonical bytes
- Date / phase / commit: 2026-10-26, Phase 01, 35b740a
- Context: Convergence requires byte-equality of canonical state and tamper-evident operation identity; the wire needs one deterministic encoding.
- Decision: Canonical form is the Go struct's declared field order, encoded with `encoding/json` and `SetEscapeHTML(false)` (internal/core/op.go). `ComputeOpID` = SHA-256 over canonical bytes with OpID blanked (content-addressed identity). `Checksum` = SHA-256 over canonical bytes including OpID (integrity only, NOT a signature).
- Why: Struct order is stable across platforms for a fixed Go struct; round-trip stability is tested (`TestCanonicalEncodingIsDeterministicAndStable`); validation fails closed when op_id ≠ derived ID, making accidental ID reuse with a different body impossible for validated ops.
- Alternatives considered: protobuf/ CBOR canonicalization (extra dependency, no gain at this scale); random UUID op IDs (weaker: IDs wouldn't detect payload mismatches at validation time).
- Consequences: Struct field order in Operation/Payload/HLC is frozen for protocol v1; changing it breaks every stored checksum and requires a protocol version bump.
- Files: internal/core/op.go, internal/core/op_test.go

### D-008: SQLite driver = modernc.org/sqlite v1.61.0 (pure Go, no CGO)
- Date / phase / commit: 2026-10-26, Phase 02, f815e59
- Context: Persistence requires SQLite with WAL; the host is Windows with no confirmed C toolchain, so a CGO driver may not build.
- Decision: Use `modernc.org/sqlite` v1.61.0 as the database/sql driver (registered name "sqlite").
- Why: Installed and verified by the full store test suite (`go test ./internal/store/` passes, including `-race`): migrations, WAL (journal_mode set via DSN pragmas), busy_timeout, synchronous=FULL, unique (actor, actor_seq), idempotent ingest. Pure Go — builds with the portable toolchain without gcc.
- Alternatives considered: mattn/go-sqlite3 (rejected: requires CGO + gcc on Windows); ncruces/go-sqlite3 (WASM-based; not chosen — larger runtime surface, no observed need). Supersedes: D-004 (the placeholder).
- Consequences: go.mod/go.sum carry the modernc dependency tree; the driver is slower than CGO sqlite — acceptable for this project's scale and measured later in benchmarks.
- Package: modernc.org/sqlite v1.61.0
- Why this package: pure-Go SQLite with database/sql compatibility, WAL support, no CGO toolchain requirement.
- Packages compared: mattn/go-sqlite3 (CGO build risk on this host), ncruces/go-sqlite3 (WASM overhead).
- Files: go.mod, go.sum, internal/store/store.go, internal/store/ops.go, internal/store/store_test.go

### D-009: Local op identity is finalized inside AppendLocal (seq first, then op ID)
- Date / phase / commit: 2026-10-26, Phase 02, f815e59
- Context: Op IDs are content-addressed over the canonical body, and the body includes actor_seq — which only the store assigns in its transaction. Computing the ID before allocation would bind the ID to a body the op never had.
- Decision: `Store.AppendLocal` (internal/store/ops.go) assigns actor_seq, derives the op ID, validates fail-closed, then inserts — all in one transaction that also persists the HLC watermark. Caller-supplied OpID/ActorSeq are overwritten.
- Why: Guarantees a stored op always satisfies op_id == hash(body), keeping I-04 detection meaningful and post-restart validation exact.
- Alternatives considered: client-supplied sequence numbers (rejected: central-free uniqueness is the store's job).
- Consequences: Node layer never fabricates identities; remote ops (which arrive already identified) use IngestRemote instead.
- Files: internal/store/ops.go, internal/core/validate.go
- Symptom: discovered in review — stored ops would fail re-validation after restart (ID bound to seq=0 body).
- Root cause: content-addressed ID computed before sequence allocation.
- Regression test: TestAppendLocalAllocatesDenseSequencesAndSurvivesRestart (re-derives and verifies on reload).

### D-010: Sync pagination cursor = last returned op, resume strictly after it
- Date / phase / commit: 2026-10-26, Phase 02, f815e59
- Context: MissingOpsFor pages results; the first cursor design pointed at the NEXT unsent candidate, and the resume filter skipped `seq <= cursor`, so that candidate was never delivered (test caught op 4 silently lost).
- Decision: NextCursor encodes the last RETURNED operation; resume re-examines everything strictly after it. A page that ends exactly at the final row yields one empty follow-up page with has_more=false.
- Why: "Resume after what you actually received" cannot skip rows; simplicity beats saving one empty round-trip.
- Alternatives considered: cursor = next candidate with `seq < cursor` filter (equally correct but easier to reason wrong about under concurrent appends).
- Consequences: Documented in PROTOCOL.md; bounded redundancy on skip-heavy ranges.
- Files: internal/store/ops.go, internal/store/store_test.go
- Symptom: `TestMissingOpsForIsHoleAware` — cursor pagination returned [1 2 3 5 6 7]; op 4 vanished.
- Root cause: cursor pointed at the first unsent candidate; resume skipped `<= cursor`.
- Regression test: TestMissingOpsForIsHoleAware (cursor loop must recover all 7 ops).

### D-011: Node layer + HTTP API + CLI structure
- Date / phase / commit: 2026-10-26, Phases 03-04, bbd2c72
- Context: The durable store and CRDTs need a runnable replica, a versioned HTTP surface, and a CLI.
- Decision: `internal/node` owns lifecycle (persistent replica identity bound to the DB file, HLC restore, replay-rebuild, write path, ingest, sync loop, fault controller, event bus, convergence checker). `internal/api` is a thin versioned HTTP layer over it (health, node, documents, operations, sync, lab, SSE). `cmd/convergedb` has serve/demo/inspect/verify/bench/doctor using only the standard library flag package. Lab endpoints 403 without `--lab`; non-loopback binds refuse to start without `--token`.
- Why: keeps merge semantics free of I/O; the API maps 1:1 onto protocol operations; fault injection lives in the node's sync client so EVERY allowed exchange is real HTTP between real processes.
- Alternatives considered: gRPC sync (heavier, less inspectable); third-party router (stdlib ServeMux patterns suffice); YAML configs (JSON keeps stdlib-only).
- Consequences: sync is pull-driven per peer with opportunistic push available; partitions gate both directions of a link from either endpoint's API.
- Files: internal/node/*.go, internal/api/*.go, cmd/convergedb/*.go

### D-012: Lab partitions are transport-shim authoritative (in-flight responses discarded)
- Date / phase / commit: 2026-10-26, Phase 04, bbd2c72
- Context: A partition applied while a pull request was in flight let the response through, leaking one exchange across the "partition" (caught by a flaky -race test).
- Decision: `PullFromPeer` re-checks the fault gate after each response and discards in-flight responses when the link is now partitioned.
- Why: makes lab partitions deterministic and reproducible. Honest caveat (recorded in demo reports): a real network cannot recall in-flight packets; the shim simulates the network, it is not a firewall.
- Alternatives considered: leaving the real-network behavior and relaxing the test (rejected: deterministic lab semantics are the point of the shim).
- Consequences: PROTOCOL/DEMO docs must state this shim behavior.
- Files: internal/node/replicate.go
- Symptom: flaky partition isolation under -race.
- Root cause: gate checked only before send.
- Regression test: TestPartitionBlocksSyncButNotLocalWrites under -race -count=8.

### D-013: Node read API returns deep-copy snapshots
- Date / phase / commit: 2026-10-26, Phase 04, bbd2c72
- Context: `Node.Document` returned shallow copies whose CRDT pointers aliased live merge state — a real data race under concurrent replication.
- Decision: `crdt.Document.Clone()` deep-copies labels (tag walk) and counter (entries map); node read paths return clones.
- Why: snapshots must be safe to read while the node keeps replicating; -race is part of the test gate.
- Alternatives considered: forcing all readers through node locks (leaks locking into callers).
- Consequences: snapshot reads are O(doc size) — fine at this scale, measured in benchmarks.
- Files: internal/crdt/counter.go, internal/crdt/world.go, internal/node/node.go
- Symptom: DATA RACE on PNCounter.Value under -race.
- Root cause: shallow copy aliasing.
- Regression test: internal/api suite under -race.

### D-016: Containerized deployments opt out of the loopback guard via --allow-unsafe-bind
- Date / phase / commit: 2026-10-26, Phase 09, c393ab8
- Context: docker-compose replicas must bind 0.0.0.0 inside containers, but serve refuses non-loopback binds without a token (safety baseline).
- Decision: new `--allow-unsafe-bind` flag used ONLY in docker-compose.yml; the guard remains default-on for bare processes.
- Why: the guard prevents accidental public exposure of a bare host process; inside a container the bind is internal and port publishing is the operator's explicit act.
- Alternatives considered: shipping a token in compose files (normalizes secrets in configs); removing the guard (unsafe).
- Consequences: docs/SECURITY.md documents the flag; the Docker path itself is NOT TESTED (daemon not running) — recorded in TEST_MATRIX/REPRODUCIBILITY.
- Files: cmd/convergedb/serve.go, docker-compose.yml

### D-017: Anti-entropy read paths rebuilt: incremental inventory + indexed segment pulls + shared keep-alive client
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: Second-pass scale measurements (tests/adversarial/scale_test.go) showed Inventory() at 6.5ms and MissingOpsFor scanning EVERY row per pull request at 10k ops — per-pull cost grew linearly with log size (O(N) per request, O(peers×N) per interval).
- Decision: (1) per-actor receive state (frontier/holes/partial) maintained incrementally in memory on every accepted op, rebuilt once at startup; (2) MissingOpsFor computes contiguous candidate segments per actor (requester frontier → local max_seen, minus the requester's advertised holes) and serves each with one indexed range query; (3) one shared keep-alive http.Client for all peer sync. The old scan implementation is retained as MissingOpsForScan, a differential oracle.
- Why: differential tests (TestIncrementalInventoryMatchesScan, TestMissingOpsIndexedMatchesScan) prove exact equivalence with the scan under randomized arrivals and requester shapes; measured result: inventory and pull cost flat vs log size (0ms/1.0ms at 100k ops).
- Alternatives considered: summarizing digests per range (adds protocol complexity without removing the decode cost); caching pages (staleness risk).
- Consequences: the incremental state must remain exactly scan-equivalent — enforced by the differential tests; hole lists beyond the cap are partial by contract.
- Files: internal/store/store.go, internal/store/ops.go, internal/store/indexed_test.go, internal/node/node.go, internal/node/replicate.go

### D-018: Partial-inventory fallback rescues ops above a stale requester max_seen (correctness fix)
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: The differential harness exposed that the ORIGINAL partial-inventory fallback resent only (frontier, requester.max_seen]. A requester whose max_seen was stale could never learn about newer ops — they were stranded forever, because the requester cannot advertise sequences it has never seen.
- Decision: the partial fallback (and the indexed path) resends everything above the requester's frontier up to the LOCAL max seen.
- Why: redundancy is free (idempotent ingest); completeness is not recoverable once skipped. Regression: TestMissingOpsForIsHoleAware partial case now expects [5,6,7] with a stale max_seen of 6.
- Alternatives considered: trusting requester max_seen (strandable), epoch-based sync (bigger protocol change, unnecessary).
- Consequences: partial-fallback resends may include more ops than before; still bounded and idempotent.
- Files: internal/store/ops.go, internal/store/store_test.go
- Symptom: differential mismatch — indexed delivered an op the scan excluded under a partial inventory with stale max_seen.
- Root cause: fallback upper bound used the requester's stale max_seen.
- Regression test: TestMissingOpsForIsHoleAware (partial case), TestMissingOpsIndexedMatchesScan.

### D-019: Inventory Partial flag reflects the ADVERTISED cap, not the internal 512-state cap
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: Differential test showed a requester could receive a truncated hole list WITHOUT the partial flag (internal state cap 512 vs advertised cap parameter), silently breaking the full-range-resend fallback.
- Decision: Partial = internal partial flag OR true hole count > advertised cap. Additionally, on hole overflow the state keeps the SMALLEST k holes (evict-largest), making the advertised list deterministic and identical to a fresh scan.
- Why: the hole list must never look complete when it is not; deterministic smallest-k matches the scan oracle exactly (differential test now strict-equal).
- Alternatives considered: relaxing the differential test when partial (weaker invariant).
- Consequences: eviction scan is O(cap) per overflow arrival — only under pathological reordering.
- Files: internal/store/store.go, internal/store/ops.go
- Symptom: advertised hole lists differed between incremental and scan states.
- Root cause: two different cap semantics for the same field.
- Regression test: TestIncrementalInventoryMatchesScan.

### D-020: Verified materializer checkpoints (watermark + tail replay) with fail-closed fallback
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: Startup replay is linear (1.55s at 100k ops, ~15.5s at 1M) — every restart paid it.
- Decision: migration 3 adds a `checkpoint` table holding the deterministic CRDT accumulator snapshot plus a per-actor coverage watermark (the world-APPLIED max seq, never the store inventory — the two can race). Startup: verify checkpoint hash + per-actor prefix row counts → restore + replay tail (seq > watermark); ANY failure falls back to full replay. Checkpoints are written on graceful close and optionally every CheckpointEveryOps accepted ops. Under-coverage is safe because CRDT merges are idempotent.
- Why: measured startup at 100k ops: 1.55s → 381ms (4.1x); at 1M ops the gap grows to ~15s vs ~1s. No durability trade-off: the operation log remains the source of truth.
- Alternatives considered: verifying state digests against a full replay on every load (defeats the purpose); trusting the checkpoint blindly (corruption risk).
- Consequences: checkpoint content is an optimization, never authoritative; corruption/staleness/prefix mismatch all degrade to the proven full-replay path (tested). New failure mode documented: a checkpoint written by a future protocol version is rejected by version check.
- Files: internal/store/store.go, internal/store/ops.go, internal/crdt/crdt.go, internal/crdt/world.go, internal/node/node.go, tests/adversarial/checkpoint_test.go
- Symptom: startup replay time grows linearly with log size.
- Root cause: full canonical decode + merge of every operation at Open.
- Regression test: TestCheckpointRestartEquivalence, TestCheckpointTailReplayAfterStaleCheckpoint, TestCheckpointCorruptionFallsBackToFullReplay, TestCheckpointPrefixMismatchFallsBack.

### D-021: HTTP hardening: shared server timeouts, bounded pull concurrency, clamped list endpoints
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: The HTTP server had no Read/Idle timeouts; pull requests could run unbounded concurrently (SQLite pressure); /api/v1/operations?limit was unclamped and /api/v1/documents cloned every document per request.
- Decision: ReadTimeout 30s + IdleTimeout 120s + 16KiB header cap (NO WriteTimeout — SSE streams are long-lived writes); pull handlers bounded by an 8-slot semaphore; operations limit clamped to 1000; documents list bounded (limit param, default 200, clamp 1000, `truncated` flag).
- Why: slowloris-style pressure and unbounded serialization are denied by policy; SSE correctness requires the absence of a write deadline (documented inline).
- Alternatives considered: global WriteTimeout (breaks SSE); per-IP rate limits (out of lab scope, documented).
- Consequences: long pulls beyond ~30s body-read are cut; SSE unaffected.
- Files: cmd/convergedb/serve.go, internal/api/server.go

### D-017: Anti-entropy read paths rebuilt: incremental inventory + indexed segment pulls + shared keep-alive client
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: Second-pass scale measurements showed Inventory() at 6.5ms and MissingOpsFor scanning EVERY row per pull request at 10k ops - per-pull cost grew linearly with log size.
- Decision: (1) per-actor receive state maintained incrementally in memory on every accepted op, rebuilt once at startup; (2) MissingOpsFor computes contiguous candidate segments per actor and serves each with one indexed range query; (3) one shared keep-alive http.Client for all peer sync. The old scan implementation is retained as MissingOpsForScan, a differential oracle.
- Why: differential tests prove exact equivalence with the scan under randomized arrivals and requester shapes; measured result: inventory and pull cost flat vs log size (0ms/1.0ms at 100k ops).
- Alternatives considered: range digests (protocol complexity without removing decode cost); page caching (staleness risk).
- Consequences: incremental state must remain scan-equivalent - enforced by differential tests.
- Files: internal/store/store.go, internal/store/ops.go, internal/store/indexed_test.go, internal/node/node.go, internal/node/replicate.go

### D-018: Partial-inventory fallback rescues ops above a stale requester max_seen (correctness fix)
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: The differential harness exposed that the ORIGINAL partial fallback resent only (frontier, requester.max_seen]; a requester with a stale max_seen could never learn about newer ops - stranded forever, because it cannot advertise sequences it has never seen.
- Decision: the partial fallback (and indexed path) resends everything above the requester's frontier up to the LOCAL max seen.
- Why: redundancy is free (idempotent ingest); completeness is unrecoverable once skipped. Regression: partial case now expects [5,6,7] with a stale max_seen of 6.
- Alternatives considered: trusting requester max_seen (strandable); epoch-based sync (bigger protocol change).
- Consequences: partial resends may include more ops; bounded and idempotent.
- Files: internal/store/ops.go, internal/store/store_test.go
- Symptom: differential mismatch under partial inventory with stale max_seen.
- Root cause: fallback upper bound used the stale requester max_seen.
- Regression test: TestMissingOpsForIsHoleAware, TestMissingOpsIndexedMatchesScan.

### D-019: Inventory Partial flag reflects the ADVERTISED cap; smallest-k holes on overflow
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: A requester could receive a truncated hole list WITHOUT the partial flag (internal state cap 512 vs advertised cap), silently breaking the full-range-resend fallback.
- Decision: Partial = internal flag OR true hole count > advertised cap; on overflow the state keeps the SMALLEST k holes (evict-largest) so advertised lists are deterministic and scan-identical.
- Why: a truncated hole list must never look complete; the differential test is strict-equal.
- Alternatives considered: relaxing the differential test when partial (weaker invariant).
- Consequences: eviction scan is O(cap) per overflow arrival - pathological reordering only.
- Files: internal/store/store.go, internal/store/ops.go
- Symptom: advertised hole lists differed between incremental and scan states.
- Root cause: two different cap semantics for one field.
- Regression test: TestIncrementalInventoryMatchesScan.

### D-020: Verified materializer checkpoints (watermark + tail replay) with fail-closed fallback
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: Startup replay is linear (1.55s at 100k ops, ~15.5s at 1M) - every restart paid it.
- Decision: migration 3 adds a checkpoint table holding the deterministic CRDT accumulator snapshot plus a per-actor coverage watermark (the world-APPLIED max seq, never the store inventory - they can race). Startup: verify checkpoint hash + per-actor prefix row counts, restore, replay tail; ANY failure falls back to full replay. Written on graceful close and every CheckpointEveryOps accepted ops. Under-coverage is safe because CRDT merges are idempotent.
- Why: measured startup at 100k ops: 1.55s -> 381ms (4.1x). The operation log remains the sole source of truth.
- Alternatives considered: full-replay verification on every load (defeats the purpose); trusting the checkpoint blindly (corruption risk).
- Consequences: checkpoint is an optimization, never authoritative; corruption/staleness/prefix mismatch degrade to the proven full-replay path (tested).
- Files: internal/store/store.go, internal/store/ops.go, internal/crdt/crdt.go, internal/crdt/world.go, internal/node/node.go, tests/adversarial/checkpoint_test.go
- Symptom: startup replay grows linearly with log size.
- Root cause: full canonical decode + merge of every operation at Open.
- Regression test: TestCheckpointRestartEquivalence, TestCheckpointTailReplayAfterStaleCheckpoint, TestCheckpointCorruptionFallsBackToFullReplay, TestCheckpointPrefixMismatchFallsBack.

### D-021: HTTP hardening: shared server timeouts, bounded pull concurrency, clamped list endpoints
- Date / phase / commit: 2026-10-26, Second pass / scale, uncommitted
- Context: The HTTP server had no Read/Idle timeouts; pull requests could run unbounded concurrently; operations/documents list endpoints were unbounded.
- Decision: ReadTimeout 30s + IdleTimeout 120s + 16KiB header cap (NO WriteTimeout - SSE streams are long-lived writes); pull handlers bounded by an 8-slot semaphore; operations limit clamped to 1000; documents list bounded (default 200, clamp 1000, truncated flag).
- Why: slowloris-style pressure and unbounded serialization denied by policy; SSE correctness requires no write deadline (documented inline).
- Alternatives considered: global WriteTimeout (breaks SSE); per-IP rate limits (out of lab scope).
- Consequences: request bodies beyond 30s are cut; SSE unaffected.
- Files: cmd/convergedb/serve.go, internal/api/server.go

### D-022: Demo CLI flags honored; scenario refuses reused scratch directories
- Date / phase / commit: 2026-10-26, Second pass / operations, uncommitted
- Context: The second-pass demo gate failed with votes=6 instead of 3. Investigation: (1) cmdDemo parsed flags AFTER a positional subcommand token, so flag.Parse stopped immediately and --seed/--keep/--out/--dir were silently ignored in every demo invocation; (2) re-running on the same --dir accumulated state, and the check's absolute expectation (votes == 3) saw two runs' worth of votes - a false double-counting alarm.
- Decision: skip the leading subcommand token before flag parsing; the demo fails fast (clear message, nonzero exit) when the target directory already contains replica databases, and NEVER deletes them.
- Why: the scenario asserts absolute values by design; silently reusing state produces misleading results, and auto-deleting operator data is forbidden. The counter itself was verified correct via differential and kill-mid-burst suites.
- Alternatives considered: auto-cleaning the directory (destroys evidence/data); relative assertions (weaker checks).
- Consequences: repeated demo runs need a fresh --dir (documented in DEMO.md); flags now behave as documented.
- Files: cmd/convergedb/demo.go
- Symptom: demo semantic check failed with exactly doubled votes on repeated runs.
- Root cause: ignored flags + state accumulation across runs, not a merge defect.
- Regression test: dirty-dir rejection verified manually; fresh-dir demo green (seed 123).
