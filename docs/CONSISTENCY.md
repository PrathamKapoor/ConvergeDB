# CONSISTENCY.md — invariants, merge semantics, and their limits

## Definitions

- **Local acknowledgment** means the operation is durably committed to the local SQLite log (WAL, `synchronous=FULL`) — it says NOTHING about other replicas having it. The UI distinguishes local state, sync progress, and global convergence.
- **Convergence** means replicas holding the same set of valid operations produce **byte-identical canonical state**. It is verified by comparing (a) a digest over the SORTED operation-ID list plus the count, and (b) a SHA-256 of the canonical materialized documents. Count alone would be insufficient.
- **Eventual convergence** is conditional on eventual communication and successful delivery of all valid operations. During a permanent partition, replicas stay diverged — the checker reports `DIVERGED` (reachable, different) or `UNKNOWN` (unreachable). Unreachability is never reported as convergence.

## Invariants and where their tests live

| # | Invariant | Tests |
|---|---|---|
| I-01 | A locally ACKed write survives a process kill + restart | `TestAppendLocalAllocatesDenseSequencesAndSurvivesRestart` (store), `TestIdentityIsBoundToDatabaseFile` (node), demo step "kill and restart beta" |
| I-02 | Applying an op 0/1/many times yields the same state | `TestWorldDuplicatesAreIdempotent`, `TestIngestRemoteIdempotentAndConflictDetection`, `TestDuplicateLadenSchedulesConverge` |
| I-03 | Any tested permutation of the same op set yields identical canonical state | `TestWorldPermutationInvariance` (200 seeded shuffles), `TestExhaustivePermutationsConverge` (ALL 7! orderings), `TestCanonicalStateIsSortedAndStable` |
| I-04 | Duplicate op ID with different content is rejected, never overwritten | `TestIngestRemoteIdempotentAndConflictDetection` (store), `TestRejectionOfInvalidRemoteOps` (node), content-addressed validation (`E_ID`) |
| I-05 | Replication is bidirectional, paginated, resumable, robust to reorder/duplication | `TestTwoNodesReplicateBidirectionally`, `TestDuplicateAndReorderedDeliveryConverge`, `TestMissingOpsForIsHoleAware`, `TestListOpsPaginationIsBounded` |
| I-06 | Anti-entropy is hole-aware (frontier + received set, not max_seen) | `TestInventoryTracksFrontierAndHoles`, `TestMissingOpsForIsHoleAware`, `TestHoleAwareSyncDeliversGappedSequences` |
| I-07 | After heal, op inventories AND materialized states agree | demo step "heal to convergence" + `TestTwoNodesReplicateBidirectionally` (digest check) |
| I-08 | Partitioned replicas keep accepting local writes | `TestPartitionBlocksSyncButNotLocalWrites`, demo isolated-write step |
| I-09 | OR-Set observed-remove, add-wins | `TestORSetObservedRemoveSemantics`, `TestORSetAddWinsAcrossAnyApplyOrder` |
| I-10 | Counter updates are not lost, never double-applied | `TestCounterDedupCommutativityAndOverflow`, demo/bench counter checks (300 = 300) |
| I-11 | Invalid input fails closed with stable errors | `TestValidateRejectsMalformedOperations`, `TestMalformedRequestsFailWithStableCodes`, `TestSyncProtocolRejectsMismatches`, `TestOversizedBodyRejected`, `TestAuthBoundaryFailsClosed` |
| I-12 | Reproducible, secret-free source/commands | `docs/REPRODUCIBILITY.md`, tracked-file secret scan at release |

## Merge semantics

### LWW register (title, status, deleted)
A write is `(value, HLC, opID)`. Winner = greatest `(wall, counter, actor)`; exact ties (a clock bug, since one actor never repeats an HLC) break by greatest op ID. Every replica derives the same winner without communication.

**What LWW costs you:** one of two concurrent semantic intents is discarded. The losing text is not stored as "content" anywhere visible. This is a deliberate, documented trade — ConvergeDB does not pretend to merge text.

### OR-Set (labels)
Adds carry unique tags (`DeriveAddTag(actor, seq)`). Removes capture the live tags they observed at generation. Live = adds minus tombstones. Consequences:

- Remove-before-add where the remove NAMED the tag: the tombstone wins — the add stays dead in every order.
- Concurrent add the remover never saw: survives (add-wins).
- Tombstones are permanent (no verified causal-stability predicate ⇒ GC would be unsafe). This is a documented storage cost.

### Delta counter (votes)
Each op contributes exactly one keyed entry; value = sum. Correctness rests on (a) dedup by op ID — replay never double-counts — and (b) commutative addition. This is an **operation-delta counter**, NOT the textbook state-based PN-counter (which stores per-replica positive/negative pairs); ConvergeDB's replication is operation-based, so the delta form is the honest description.

Counter overflow (needs ~9×10⁹ maximal deltas) fails closed at merge time; the op stays durable and the convergence checker will report the truth rather than paper over it.

### Document lifecycle
`doc.create` makes a document visible; field ops that arrive before their create are retained in registers and appear once the create lands (`TestWorldCreateArrivesLast`). `doc.delete`/`doc.restore` are two LWW values of one tombstone register — concurrent delete + edit resolves by HLC like any register write; edits to a deleted doc are retained and visible again after restore. Two concurrent `doc.create`s for the same ID merge into one document (idempotent at the accumulator level).

## Counterexamples to naive designs (and what ConvergeDB does instead)

1. **`max_seen_seq` anti-entropy.** If a peer advertises "I have up to 7" and 6 was lost in transit, 6 is invisible forever. ConvergeDB advertises frontier + received-above; `TestInventoryTracksFrontierAndHoles` pins the 1,2,5 → frontier 2, holes [5] behavior, and `TestHoleAwareSyncDeliversGappedSequences` closes real gaps under reorder storms.
2. **Eager tag deletion in OR-Sets.** Deleting observed tags on remove makes remove-before-add resurrect values (this was ConvergeDB's FIRST implementation — caught by permutation tests, see FAILURE_LOG.md). Tombstones fix it.
3. **Time-based cursors.** Wall-clock cursors skip ops under skew; ConvergeDB uses (actor, seq) positions.
4. **Count-based convergence.** Same op count with different content is easy; digests over sorted op IDs + canonical state are the check.
5. **ACK-before-commit.** Acknowledging a write before the SQLite commit means a crash can ACK something that never existed. ConvergeDB commits, then ACKs, in one transaction with sequence allocation.

## Liveness assumptions

- Convergence requires eventual pairwise connectivity and bounded delivery — no finite bound is promised under arbitrary partitions.
- HLC ordering assumes the wall clock is roughly sane; arbitrary forward jumps are adopted (they win LWW), rollback is tolerated (counter advances). A maliciously future-skewed writer can therefore win LWW — a trusted-local assumption, stated in SECURITY.md.
- Recovery after disk-full or mid-write OS crash follows SQLite WAL semantics; ConvergeDB verifies checksums on read and fails closed rather than silently "repairing".

## Formal verification status

NOT formally verified. Correctness claims rest exclusively on the executed tests listed above (including exhaustive small-history enumeration) within their stated scope.
