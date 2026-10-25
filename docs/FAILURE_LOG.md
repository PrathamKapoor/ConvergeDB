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
