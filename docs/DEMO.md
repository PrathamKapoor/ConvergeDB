# DEMO.md — the partition-heal scenario, step by step

Command: `convergedb demo partition-heal [--seed N] [--out path] [--dir path] [--document id] [--keep]`

The demo starts **three real operating-system processes** (`convergedb serve` children), each with its own SQLite database file, full-mesh peering, lab mode on, and a 150 ms sync interval. It then executes the user story end to end and writes a JSON report. Any failed step or check → nonzero exit.

## Steps

1. **Launch three replicas** — dynamic free ports; each waits for `/health/ready`.
2. **Create the document** on alpha (`doc.create`, durable commit before ACK).
3. **Initial sync** — waits until beta and gamma both serve the document.
4. **Partition** alpha↔beta and alpha↔gamma (both directions, via the lab API).
5. **Isolated writes** — while cut off: alpha writes title + status + label + vote; beta writes title + status + label + vote. Both islands keep ACKing writes (I-08). The step records the HLC walls of both title writes for the LWW check later.
6. **Verify divergence** — alpha's convergence checker must report `DIVERGED` while partitioned (a truthful management-plane read; peers are alive).
7. **Heal + fault the recovery** — partitions cleared, then the first exchange alpha→beta is forced through duplicate_rate=1.0 and alpha→gamma through page reordering. After one round the faults are cleared. Deduplication and reorder robustness are thereby exercised on real traffic (I-05).
8. **Kill and restart beta** — SIGKILL on the beta process AFTER its writes were ACKed; restart with the SAME database file and identity; verify beta's op count never regressed (I-01). Identity binding means the file refuses to run as any other replica.
9. **Heal to convergence** — poll the convergence checker (max 30 s) until `CONVERGED`, which requires operation-inventory digest equality AND canonical state digest equality on every reachable replica (I-07).
10. **Semantic checks** (each recorded in the report):
    - `I-07-inventory` — op digests equal on all three replicas.
    - `I-07-state` — canonical state digests equal.
    - `I-10-counter` — votes == 3 (alpha's +1 and beta's +2 both survived) everywhere.
    - `I-09-orset` — both isolated labels present everywhere (add-wins).
    - `I-03-lww` — exactly ONE title on all replicas, and it equals one of the two isolated writes (the HLC winner), with the losing intent recorded.
    - `I-01-restart` — beta's durable op count across the kill.

## Report

`var/reports/partition-heal-report.json` contains: schema version, kind, seed, start/finish, every step with duration, all checks with pass/fail, per-node op counts and digests, the list of simulated faults, and an explicit limitations section (transport-shim semantics; process-kill vs power-loss durability; intra-run vs cross-run determinism).

## What is simulated vs real

- REAL: three OS processes, real SQLite files, real HTTP requests, real crash+restart, real digests.
- SIMULATED: the network fault shim inside each replica's sync client (partition/drop/duplicate/reorder/delay). It models the network, not the nodes — and, unlike a real network, it discards in-flight responses when a partition is applied so lab runs are deterministic.

## Inspect a live lab afterwards

```bash
./convergedb demo partition-heal --keep     # leaves alpha/beta/gamma running
./convergedb inspect --node http://127.0.0.1:8101 --document demo-board
./convergedb verify --nodes alpha=http://127.0.0.1:8101,beta=http://127.0.0.1:8102,gamma=http://127.0.0.1:8103
```
