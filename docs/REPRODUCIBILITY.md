# REPRODUCIBILITY.md — how to replicate everything

## Prerequisites

- Go ≥ 1.22 (developed on 1.27.3) — https://go.dev/dl/
- Node.js ≥ 20 + npm (only for the web workbench build)
- Optional: Docker with Compose v2

No API keys, no cloud services, no external databases. Everything runs on loopback.

## Fresh-clone path

```bash
git clone https://github.com/PrathamKapoor/ConvergeDB
cd ConvergeDB
go build -o convergedb ./cmd/convergedb     # downloads modules (go.sum pinned)
(cd web && npm install && npm run build)    # builds web/dist
./convergedb doctor                          # environment sanity
```

## Reproduce the hero demo

```bash
./convergedb demo partition-heal --seed 42 --out var/reports/partition-heal-report.json
```

Deterministic in the ways stated in the report itself: fault decisions are seeded; operation IDs, HLC walls, and therefore digests depend on real time, so **cross-run byte equality is NOT a claim** — the demo verifies and reports INTRA-run equality (all three replicas identical), which is the actual convergence property.

Vary `--seed` to rerun with different fault decisions. Exit code is nonzero if any check fails.

## Reproduce tests

```bash
go test ./...
go test -race ./...
go test ./tests/model -v
```

Expected (2026-10-26): 48 passing tests, 0 failures, race-clean. Exact test names and per-invariant mapping: docs/TEST_MATRIX.md.

## Reproduce benchmarks

```bash
./convergedb bench --writes 300 --out var/bench
cat var/bench/bench-report.json
```

Absolute numbers are host-specific (see docs/RESULTS.md for the recorded environment). The three-replica profile asserts the counter equals exactly `perSide × 3` after convergence; a mismatch fails the benchmark.

## Docker path

```bash
docker compose up --build
```

Builds the Go binary and the web bundle in multi-stage images and starts three replicas (alpha serves the UI at http://localhost:8101). Data lives in named volumes — `docker compose down -v` removes it.

**Status: NOT TESTED** — the Docker CLI is installed on the development host but the daemon was not running, so no image was built or run from these files. The container path additionally uses `--allow-unsafe-bind` (container-internal 0.0.0.0 binds; see docs/SECURITY.md). Report any image-tag or compose-version issues as issues.

## What is NOT reproducible byte-for-byte

- HLC wall values (real clock), hence op IDs and digests differ between runs.
- Benchmark absolutes (hardware, OS, background load).
- The exact UI event ordering (SSE arrival interleaving).
