# Contributing

## Ground rules
- The three CRDT types and the protocol envelope are intentionally narrow. Proposals to add types or change the wire format must include a protocol version bump plan and exhaustive ordering tests.
- Every invariant change ships with a test that fails without the change. Permutation/ordering tests are the standard of proof for anything merge-related.
- No new dependencies in the Go core without justification (stdlib first). Web deps: minimal by policy.

## Workflow
1. Fork / branch from main.
2. `go test ./... && go test -race ./...` must pass; `gofmt` clean.
3. For UI changes: `cd web && npm run build` and verify against a live lab (`convergedb demo partition-heal --keep`).
4. Keep PRs focused; document behavior changes in docs/ (PROTOCOL/CONSISTENCY/SECURITY) in the same change.
5. Never commit runtime databases (`var/`), credentials, or generated artifacts.

## Commit style
Conventional-ish prefixes (`feat:`, `fix:`, `test:`, `docs:`, `chore:`) with a body explaining the why.
