
### D-014: Web workbench = React 19 + TypeScript + Vite with zero UI-framework dependencies
- Date / phase / commit: 2026-10-26, Phase 06, uncommitted
- Context: The mission requires a polished, honest visual laboratory wired to the real node APIs.
- Decision: `web/` React 19 + TS strict + Vite; hand-written dark CSS; no component/UI/state libraries. Reads: per-node /api/v1/node + /api/v1/documents (1s), /api/v1/lab/convergence + faults (1.5s), SSE /api/v1/events per node. Node URLs configurable via ?nodes= override; defaults serve same-origin + fixed peer ports.
- Why: the UI must show REAL state (verified in browser: live vote/label replication, partition->DIVERGED->heal->CONVERGED with LWW winner, stale-view banner). Zero framework deps keeps the supply chain small and the build reproducible.
- Alternatives considered: Tailwind/MUI (extra deps for no functional gain); WebSocket-only live data (SSE is simpler and already implemented); polling-only (misses event timeline).
- Consequences: `npm run build` output in web/dist is served by `convergedb serve --web`; the UI never fabricates state (offline nodes show explicit stale/unreachable banners).
- Files: web/* (package.json, tsconfig.json, vite.config.ts, index.html, src/*), web/screenshots/*
- Package: react ^19.2.0, react-dom ^19.2.0, vite ^7.1.0, typescript ~5.9.0
- Why this package: standard, maintained React toolchain; Vite build <2s.
- Packages compared: Preact (smaller but React API drift), Next.js (server-rendering not needed), svelte (different paradigm, no gain).

### D-015: All API responses send Cache-Control: no-store; UI banner exposes data age
- Date / phase / commit: 2026-10-26, Phase 06, uncommitted
- Context: During browser testing a convergence banner once displayed a stale CONVERGED while the API reported DIVERGED; no repro across 3 attempts, but browsers may heuristically cache un-headered GETs and the mission requires stale views to be distinguishable from fresh reads.
- Decision: `writeJSON` (both node and api packages) sets `Cache-Control: no-store`. The UI convergence banner shows "checked Ns ago" and switches to an explicit STALE VIEW state when the last successful check is >= 5s old.
- Why: honesty of the display under degraded conditions; prevents HTTP-cache-induced staleness.
- Alternatives considered: ETags/revalidation (unnecessary for lab-scale polling).
- Consequences: slight polling overhead; UI now self-reports staleness.
- Files: internal/node/replicate.go, internal/api/server.go, web/src/ConvergenceBanner.tsx, web/src/styles.css
