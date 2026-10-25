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
- Date / phase / commit: 2026-10-26, Phase 01, uncommitted
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
- Date / phase / commit: 2026-10-26, Phase 01, uncommitted
- Context: Content-addressed op IDs (SHA-256 over canonical content) plus "tag == op ID" for label.add are mutually circular: the tag would have to contain its own hash.
- Decision: `core.DeriveAddTag(actor, actorSeq)` = hex SHA-256 of `"convergedb:add-tag:" + actor + ":" + seq`. Validation requires label.add payloads to carry exactly this tag.
- Why: (actor, actor_seq) is unique per operation, so the tag is unique; derivation is deterministic, so seeded demos and property tests reproduce identical histories; the tag is covered by the op ID content hash, tying it to the add operation.
- Alternatives considered: random 128-bit tags (breaks reproducibility of seeded tests); tag == op ID (circular, unimplementable).
- Consequences: PROTOCOL.md must specify this derivation; changing the derivation string would invalidate stored tags, so it is frozen for protocol v1.
- Files: internal/core/op.go, internal/core/validate.go, internal/crdt/crdt_test.go, tests/model/permutation_test.go

### D-007: Canonical encoding = fixed-order struct JSON, no HTML escaping; op ID = SHA-256 of canonical bytes with OpID blanked; checksum = SHA-256 of full canonical bytes
- Date / phase / commit: 2026-10-26, Phase 01, uncommitted
- Context: Convergence requires byte-equality of canonical state and tamper-evident operation identity; the wire needs one deterministic encoding.
- Decision: Canonical form is the Go struct's declared field order, encoded with `encoding/json` and `SetEscapeHTML(false)` (internal/core/op.go). `ComputeOpID` = SHA-256 over canonical bytes with OpID blanked (content-addressed identity). `Checksum` = SHA-256 over canonical bytes including OpID (integrity only, NOT a signature).
- Why: Struct order is stable across platforms for a fixed Go struct; round-trip stability is tested (`TestCanonicalEncodingIsDeterministicAndStable`); validation fails closed when op_id ≠ derived ID, making accidental ID reuse with a different body impossible for validated ops.
- Alternatives considered: protobuf/ CBOR canonicalization (extra dependency, no gain at this scale); random UUID op IDs (weaker: IDs wouldn't detect payload mismatches at validation time).
- Consequences: Struct field order in Operation/Payload/HLC is frozen for protocol v1; changing it breaks every stored checksum and requires a protocol version bump.
- Files: internal/core/op.go, internal/core/op_test.go

### D-008: SQLite driver = modernc.org/sqlite v1.61.0 (pure Go, no CGO)
- Date / phase / commit: 2026-10-26, Phase 02, uncommitted
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
- Date / phase / commit: 2026-10-26, Phase 02, uncommitted
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
- Date / phase / commit: 2026-10-26, Phase 02, uncommitted
- Context: MissingOpsFor pages results; the first cursor design pointed at the NEXT unsent candidate, and the resume filter skipped `seq <= cursor`, so that candidate was never delivered (test caught op 4 silently lost).
- Decision: NextCursor encodes the last RETURNED operation; resume re-examines everything strictly after it. A page that ends exactly at the final row yields one empty follow-up page with has_more=false.
- Why: "Resume after what you actually received" cannot skip rows; simplicity beats saving one empty round-trip.
- Alternatives considered: cursor = next candidate with `seq < cursor` filter (equally correct but easier to reason wrong about under concurrent appends).
- Consequences: Documented in PROTOCOL.md; bounded redundancy on skip-heavy ranges.
- Files: internal/store/ops.go, internal/store/store_test.go
- Symptom: `TestMissingOpsForIsHoleAware` — cursor pagination returned [1 2 3 5 6 7]; op 4 vanished.
- Root cause: cursor pointed at the first unsent candidate; resume skipped `<= cursor`.
- Regression test: TestMissingOpsForIsHoleAware (cursor loop must recover all 7 ops).
