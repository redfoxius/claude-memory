# Test Report — memory-mvp

- **Author:** test-writer (Sonnet), once over the whole changeset, 2026-10-01.
- **Scenarios derived from:** `02-plan.md` acceptance criteria + `01-spec.md` v0.5 ACs.

## Added

| Package | Tests | Covers |
|---|---|---|
| internal/config | defaults, required DSN, env-file permission checks (0600 ok; 0640/0644/0606/0666 refused), `MEMORY_PR_INGEST_REPOS` | AC-1, 15, 28, 30, 32 |
| internal/memory (`writepath_test.go`, new) | thresholds exactly at / just below 0.65 and 0.85; Similarity not Score; stale `ExtractionDecision` → ADD; SUPERSEDE in one tx + rollback; no write when Ollama/Postgres down; scrub before embed and persist; embed input order; seen_count promotion; PR source active/0.75 | AC-14, 15, 17, 18, 29, 34, 38, 39, 55, 57 |
| internal/memory (`lifecycle_test.go`, new) | feedback useful/outdated/wrong, not-found, validation | AC-10, 12, 34, 36 |
| internal/memory (`service_test.go`) | update re-embeds; no non-whitelisted keys reach the store | AC-8 |
| internal/scrub | AZDO-PAT false positives; URLs / git SHAs not redacted | AC-38, 39 |
| internal/postgres (`store_test.go`, no tag) | `Update` rejects non-whitelisted / injection-shaped columns | security |
| internal/postgres (integration) | TTL ±1h around cutoff; deprecated excluded by default, included on request | AC-4, 35, 37 |
| internal/extraction | `ProcessPR` stores with Source=pr; off-schema decision → ADD | AC-25, 45 |
| cmd/claude-memory (`hook_test.go`, new) | Similarity threshold + ≤3 cap; title/repo/id only; silent exit on error / timeout / bad stdin; Claude Code output contract | AC-31, 32, 46 |

## Results

- `go test -race ./...` — **4 failing on purpose** (bugs below); everything else passes.
- `go test -tags integration ./internal/postgres/...` — 10/10 pass.
- `go vet`, `golangci-lint` on touched packages — clean (3 pre-existing errcheck misses in `store_integration_test.go:67,509,526`).
- `-tags eval` not re-run here (needs local Ollama).

## Bugs found (tests left failing → fix loop)

1. `internal/memory/crud.go:28-81` `UpdateRecord` — never re-embeds on content change (AC-8) and sends `content_changed` in the update map, which `internal/postgres/store.go:177-191` rejects → every content-changing `memory_update` fails against real Postgres.
   Test: `TestService_UpdateRecord_ContentChangeRecomputesEmbedding`.
2. `internal/scrub/patterns.go:50-53` AZDO-PAT pattern `[A-Za-z0-9+/]{50,}` redacts long paths and hex ids (AC-39 over-redaction).
   Tests: `TestScrub_LongFilePath_FalsePositive_KnownBug`, `TestScrub_LongHexIdentifier_FalsePositive_KnownBug`.
3. `cmd/claude-memory/hook.go:21-30,81-92` — output is `{"additionalContext":[...]}`, not Claude Code's `UserPromptSubmit` contract (`hookSpecificOutput.hookEventName` + string `additionalContext`) → cards never reach the session.
   Test: `TestHookCmd_OutputMatchesClaudeCodeUserPromptSubmitContract`.
