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
