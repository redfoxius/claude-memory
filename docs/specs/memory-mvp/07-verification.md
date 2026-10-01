# Plan Verification — memory-mvp (iteration 0)

- **Verifier:** plan-verifier (Sonnet). Re-ran: `go build`, `go vet`, `golangci-lint` (0), `go test -race ./...` (exactly the 4 known failing tests), `go test -tags integration ./internal/postgres/...` (10/10). `-tags eval` not re-run (needs Ollama).
- **Verdict:** **REVIEW** — 51 of 57 active ACs MET; AC-41 withdrawn.

## Non-MET rows

| AC / item | Verdict | Evidence |
|---|---|---|
| AC-4 | PARTIAL | `internal/postgres/hybrid_search.go:62-93` — RRF has no status term; "candidate ranked lower than active" not implemented or tested (deprecated exclusion and `Unverified` flag are fine) |
| AC-8 | NOT MET | `internal/memory/crud.go:28-52` — no re-embed; `content_changed` rejected by `internal/postgres/store.go:177-191` whitelist → content updates fail on real Postgres |
| AC-32 / AC-46 | PARTIAL | selection logic correct (`hook.go:63-83`); output shape `hook.go:21-30` not Claude Code's contract → cards never reach a session |
| AC-39 | PARTIAL | `internal/scrub/patterns.go:50-53` over-redacts paths / hex ids |
| AC-53 | PARTIAL | `DEPLOY.md` complete; no restore drill recorded |
| AC-30, AC-48 | UNVERIFIABLE here | no real-tailnet hook/store p95 recorded |
| AC-49, AC-50, AC-51 | UNVERIFIABLE here | no 24h `docker stats`, kill/restart drill or retention check recorded (backup *run* recorded) |
| AC-52, AC-54 | MET (manual, owner) | see `04-implementation-report.md` "Measured on real systems" |
| Constraint: adapters only in `main.go` | NOT MET (critical) | `cmd/claude-memory/eval.go:39,51`, `ingestpr.go:52-53` (+ `cleanup.go:39` per architecture review) |
| WI-07 `eval-results.md` | stale | generated before the 0.50/0.65/0.85 retune |

## Process notes (not defects)
- `docs/specs/README.md` backlog and spec/plan edits (v0.3→v0.5, AC-58) were made in the orchestrating session on the owner's decisions and are logged in spec §13 — outside implementer scope by design.

## Iteration 1 re-verification (fix loop) — **PASS**

| Item | Verdict | Evidence |
|---|---|---|
| AC-4 | MET | `hybrid_search.go:15-21,91-93` candidate factor 0.85 on fused score only; `TestSearchRanksActiveAboveCandidateAtEqualRelevance` (integration) pass |
| AC-8 | MET | `crud.go` Get → merge → scrub → re-embed → whitelisted update; `TestUpdateRecomputesTsvectorFromNewContent` (integration) + `TestService_UpdateRecord_*` pass |
| AC-32 / AC-46 | MET | `hook.go:23-34` `hookSpecificOutput` contract; hook tests pass |
| AC-39 | MET | `patterns.go:49-59` anchored PAT shapes; false-positive + true-positive tests pass |
| Adapters only in `main.go` | MET | grep: `postgres.New`, `ollama.New`, `scrub.NewAdapter`, `prcursor.NewStore`, `azuredevops.New` only in `main.go` |

Commands (no cache): `go build ./...` ok; `go test -race -count=1 ./...` all 14 packages ok; `go test -tags integration -race -count=1 ./internal/postgres/...` 16/16 pass.

**Remaining, operational only (not code defects):** AC-30, AC-48 (latency over the real tailnet), AC-49 (24h `docker stats`), AC-50 (kill/restart drill), AC-51 / AC-53 (backup retention + restore drill) — run on the server/laptop after install. AC-52, AC-54 MET (manual, owner).
