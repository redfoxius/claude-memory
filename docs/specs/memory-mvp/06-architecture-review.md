# Architecture Review — memory-mvp (iteration 0)

- **Reviewer:** architecture-reviewer (Sonnet), skills: golang-architecture, security.
- **Target:** all Go files under `cmd/` and `internal/` (greenfield).
- **Gate:** **FAIL** — 3 critical, 2 high.

## Findings

| Sev | file:line | Rule | Claim | Recommendation |
|---|---|---|---|---|
| CRITICAL | `cmd/claude-memory/cleanup.go:39` | composition root (plan constraint `02-plan.md:88-89`) | `postgres.New` built again after `cmdCleanup` already called `buildService` → two pools per run | build once in `main.go`; expose cleanup via a `Service` passthrough or return the store from `buildService` |
| CRITICAL | `cmd/claude-memory/eval.go:39,51,54,57` | composition root | hand-duplicates `buildService` (postgres, ollama, scrub, clock) → drift risk | call `buildService` |
| CRITICAL | `cmd/claude-memory/ingestpr.go:52-53` | composition root | `prcursor.NewStore`, `azuredevops.New` built outside `main.go` | construct in `main.go`'s `cmdIngestPR`, pass in as ports |
| HIGH | `internal/postgres/store.go:103-106,117` | security A05 | tsvector built by string-concatenating title/tags/content with manual quote doubling, spliced into SQL | bind as `$N` params to `to_tsvector`/`setweight` |
| HIGH | `internal/postgres/advisory_lock.go:146-149,160` | security A05 | same pattern in `txStoreImpl.Create` — the real write path | same |

Already known and not re-reported: hook output shape, AZDO-PAT regex, `UpdateRecord` re-embed / `content_changed`.

## Checked clean
`internal/memory` (no infra imports, consumer-declared ports, context-first), `internal/record`, `internal/transcript`, `internal/mcpserver`, `internal/extraction` (delimited prompts + `target_id` revalidation, slice args), `internal/azuredevops` (slice args incl. `az rest`), `internal/config` (DSN never logged, env-file perms), `internal/prcursor` (atomic write, 0600/0700), hybrid-search SQL (only placeholders/static text interpolated).

## Iteration 1 re-review (fix loop) — **PASS**

Scope: files changed in the fix loop. All 5 prior findings **resolved**:
- composition root — adapters constructed only in `cmd/claude-memory/main.go` (`buildPostgresStore`, `buildService`, `cmdIngestPR`); `cleanup.go`, `eval.go`, `ingestpr.go` receive ports;
- tsvector — `internal/postgres/tsvector.go` builds `$N`-only expressions for `Create` (both paths) and `Update` (new values for changed fields, column refs for unchanged ones).

New checks clean: hook output contract (`hook.go:25-34`), AC-4 rank factor is a compile-time constant, `UpdateRecord` order Get → merge → scrub → re-embed → whitelisted update with no partial write, PAT patterns. No new findings. (Stale test comment at `hook_test.go:287` fixed by the orchestrator.)
