# FAILURE_LOG.md — chronological log of real observed failures

Every entry: what failed, exact repro, root cause, fix status. No cosmetic entries.

## 2026-10-26 — OR-Set first implementation was order-dependent (I-02/I-03 violated)

- Symptom: `go test ./...` — `TestWorldPermutationInvariance` (internal/crdt): permutation 121 diverged; `TestDuplicateLadenSchedulesConverge` (tests/model): seed 1 diverged; `TestWorldDuplicatesAreIdempotent`: digests differed.
- Repro: apply `label.add("red", tag t1)` then `label.remove("red", [t1])` vs. the reverse order; the reverse order resurrected the removed value.
- Root cause: the first OR-Set deleted observed tags eagerly from a live-tag map. A remove that arrived before its target add found nothing to delete, so the later add survived — the remove's intent was lost. Eager deletion is not a lattice merge.
- Fix: rewrote OR-Set with persistent tombstones (`internal/crdt/crdt.go`): adds and tombstones are separate maps; live = adds minus tombstones. A remove tombstones exactly the tags it names, in any arrival order. Tombstones are never garbage-collected (no verified causal-stability predicate) — documented storage cost.
- Regression test: `TestORSetObservedRemoveSemantics`, `TestORSetAddWinsAcrossAnyApplyOrder` (internal/crdt/crdt_test.go), plus the pre-existing exhaustive permutation suite (tests/model/permutation_test.go). Status: FIXED, all tests pass.

## 2026-10-26 — circular design: content-addressed op ID + tag == op ID

- Symptom: caught while authoring `buildHistory` (internal/crdt/crdt_test.go) — label.add ops could not be constructed: the tag had to equal the op ID, but the op ID is a SHA-256 over content that includes the tag. The fixed-point equation `tag = hash(...tag...)` has no tractable solution.
- Root cause: two reasonable constraints (content-addressed operation IDs; add-tag tied to the add operation's identity) were combined in a circular way.
- Fix: add-tags are derived from the add operation's identity inputs instead: `core.DeriveAddTag(actor, actorSeq) = hex(sha256("convergedb:add-tag:" actor ":" seq))`. Unique because (actor, actor_seq) is unique; deterministic so seeded histories reproduce; covered by the op ID content hash. Validation enforces the derivation (fail closed).
- Regression test: any label.add construction in the test suites; `TestValidateRejectsMalformedOperations/label tag mismatch`. Status: FIXED.

## 2026-10-26 — sync pagination cursor silently dropped one operation per page boundary

- Symptom: `TestMissingOpsForIsHoleAware` — paging through 7 operations with page size 3 returned [1 2 3 5 6 7]; seq 4 disappeared exactly at the page boundary.
- Repro: `store.MissingOpsFor(nil, 3, ...)` looping via NextCursor (internal/store/ops.go).
- Root cause: NextCursor encoded the NEXT unsent candidate row, while resume skipped rows `<= cursor` — the candidate itself was skipped on resume and never sent. Classic off-by-one on a snapshot cursor.
- Fix: cursor now encodes the last RETURNED operation; resume re-examines strictly after it; page ends at the final row produce one empty has_more=false page.
- Regression test: `TestMissingOpsForIsHoleAware` (internal/store/store_test.go), cursor loop must recover all 7 ops. Status: FIXED, suite green including `-race`.

## 2026-10-26 — lab partition leaked one in-flight exchange (flaky under -race)

- Symptom: `TestPartitionBlocksSyncButNotLocalWrites` failed intermittently (≈1 in 3 runs under `-race`): alpha's store contained beta's isolated write although the alpha→beta link was partitioned.
- Repro: apply `Faults().Partition()` while a pull request from alpha to beta is in flight; the response (computed after the peer's local write) was ingested despite the partition.
- Root cause: the fault gate was consulted only BEFORE sending a request. A request already in flight when the partition landed still delivered its response — data crossed the "partition" by milliseconds.
- Fix: `PullFromPeer` re-checks the gate AFTER each response and discards in-flight responses when the link is now partitioned (`internal/node/replicate.go`). Documented semantics: the transport shim is authoritative for the whole exchange, which real networks cannot be — recorded in the demo report's limitations.
- Regression test: `TestPartitionBlocksSyncButNotLocalWrites` under `-race -count=8` (previously flaky, now 8/8 green). Status: FIXED.

## 2026-10-26 — data race: Node.Document returned shallow copies aliasing live CRDT state

- Symptom: `go test -race ./internal/api/` — DATA RACE: test goroutine read `PNCounter.Value()` on a document snapshot while the sync loop applied operations.
- Root cause: `Node.Document`/`Documents` returned `crdt.Document` copies whose `Labels`/`Votes` were POINTERS into the live world; reads outside the node lock raced with merges.
- Fix: added `Document.Clone()` (deep copy via `ORSet` tag walk + `PNCounter.Entries()`) and made the node return deep snapshots.
- Regression test: the whole api test package under `-race` (previously failed deterministically on the winning run). Status: FIXED.

## 2026-10-26 — validation error codes collapsed to E_SCHEMA in remote ingest

- Symptom: `TestRejectionOfInvalidRemoteOps` — a tampered op (same ID, different body) was rejected with the generic E_SCHEMA instead of its real code.
- Root cause: `IngestOne` asserted `err.(core.ValidationError)` (value) while `ve()` produced `*ValidationError` (pointer), so the type assertion failed and the fallback code was used.
- Fix: standardized validation errors as VALUE types (`ve()` returns `core.ValidationError`); assertion updated. Tampered ops are now rejected with E_ID by content-addressed validation before the store's E_CONFLICT layer (both paths tested).
- Regression test: `TestRejectionOfInvalidRemoteOps`, `TestValidateRejectsMalformedOperations`. Status: FIXED.
