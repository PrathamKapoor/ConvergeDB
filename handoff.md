# handoff.md — current state for the next session

Rewritten at the end of every session with the 12 required sections. Intermediate updates happen at stop points.

## 1. Current Phase
- Phase: 00 — Reconnaissance and bootstrap
- Subphase: environment verified, ledgers created
- Objective: confirm tools/auth/no-collision, then start Phase 01 (pure data semantics)
- Status: partial (recon done; Phase 01 not started)

## 2. Work Completed
- Verified environment: Windows 11 (10.0.26200), Git Bash, git 2.55.0.windows.2, Node v24.19.0, npm 12.0.2, Docker 29.8.1. Go was NOT installed; installed portable Go 1.27.3 at `C:\Users\LENOVO\tools\go` (user-local, outside repo, no global config changes). `gh` CLI is NOT installed.
- Confirmed global git identity: `PrathamKapoor` (the exact author identity the mission requires).
- Confirmed `github.com/PrathamKapoor/ConvergeDB` does not exist (api.github.com HTTP 404) — no duplicate repository.
- Network reachability verified: api.github.com 200, go.dev 200, proxy.golang.org 200, registry.npmjs.org 200.
- Created decisions.md (D-001..D-004), flow.md, handoff.md, docs/SCOPE.md, LICENSE (MIT), .gitignore.

## 3. Files Changed
- decisions.md, flow.md, handoff.md — root ledgers (required by documentation rules)
- docs/SCOPE.md — product scope, invariants, MVP
- LICENSE — MIT
- .gitignore — excludes runtime DBs, var/, node_modules, dist, env files

## 4. Current Architecture / State
- No source code yet. Planned: Go core (internal/core, clock, crdt, store, sync, node, api), CLI (cmd/convergedb), React+TS+Vite web laboratory, Go tests (unit/model/integration).

## 5. Decisions Made
- D-001 project dir/name; D-002 portable Go; D-003 module path + stack + MIT license; D-004 SQLite driver pending verification (candidate modernc.org/sqlite, pure Go, no CGO).

## 6. Requirements and Constraints
- Commits only as `PrathamKapoor` with the configured global email (already correct). No Co-authored-by/attribution lines. Never commit paper/, research/, .tex. Never publish runtime DBs or secrets. Publish to github.com/PrathamKapoor/ConvergeDB only after safety gates; if push auth is unavailable, report NOT PUBLISHED with the exact blocker.

## 7. Testing and Verification
- Commands run: tool version checks, GitHub API queries (404 for repo, 200 for user), network checks, `go version` (go1.27.3 windows/amd64). No code tests yet (no code).

## 8. Known Issues / Risks
- GitHub push authentication unverified (no `gh` CLI). Must test `git push` capability before Phase 09; if it fails, report NOT PUBLISHED + exact blocker.
- Docker is installed but unverified for compose builds.

## 9. Unfinished Work
- Everything from Phase 01 onward (core semantics, store, replication, faults, UI, hardening, benchmarks, CI, release).

## 10. Next Subphase
- Phase 01: Go module + internal/clock + internal/core + internal/crdt with deterministic unit and seeded property tests.

## 11. Critical Context
- Go toolchain must be on PATH per shell: `export PATH=/c/Users/LENOVO/tools/go/bin:$PATH` (GNU tar in Git Bash cannot read zips; use `/c/Windows/System32/tar.exe`).
- The mission prompt is pasted at the top of the session; Appendix A documentation rules and Appendix B release rules are mandatory.

## 12. Agent Instructions
- Read decisions.md + flow.md fully before code changes. Append decisions in the same step as changes. Keep flow.md real (actual names). Update handoff.md before stopping. Never fabricate test results or commit hashes.
