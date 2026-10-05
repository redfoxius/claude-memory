# Implementation Report — memory-mvp

- **Plan:** `02-plan.md` · **Spec:** `01-spec.md` v0.5 (AC-1..AC-58, AC-41 withdrawn)
- **Run:** `run-plan`, multi-agent, 2026-10-01. Implementers on Haiku by
  default; escalated to Sonnet where Haiku output was incomplete or wrong
  (see "Escalations").
- **State at end of implementation:** `go build ./...`, `go vet ./...`,
  `go test -race ./...`, `golangci-lint run ./...` (0 issues) all green;
  `internal/postgres` integration suite (tag `integration`, 8 tests) and the
  retrieval eval (tag `eval`) green against real pgvector + local Ollama.

## Work Items

| WI | Status | Implementer | Notes |
|---|---|---|---|
| 01 config/scaffold | done | haiku | defaults later retuned (see thresholds) |
| 02 record | done | haiku | |
| 04 scrub | done | haiku | JWT + Bearer patterns added; AZDO-PAT pattern flagged as over-broad (review) |
| 3a ports/search/CRUD | done | haiku | orchestrator added `WithTx`/`TxStore` (lock had no transaction) and `Similarity` (thresholds were compared to RRF score) |
| 3b write path | done | haiku | |
| 3c lifecycle | done | haiku | |
| 05 postgres | done | haiku → sonnet fix | hybrid SQL failed on every call (missing CTE alias) and silently fell back to full-text; `<->` instead of `<=>`; bogus FTS param in FindCandidates — fixed, regression test added |
| 06 ollama | done | haiku | `num_ctx`/`num_batch` = embed limit |
| 07 eval harness | done | haiku → sonnet fix | harness never ran; thresholds were hardcoded; now derived from measured distributions |
| 08 MCP server | done | haiku → **redone by sonnet** | haiku hand-rolled JSON-RPC with 2/7 tools; replaced with go-sdk v1.8.0, all 7 tools, in-memory transport tests, live smoke |
| 09 | withdrawn | — | topology B |
| 10 hook | done | haiku | **known bug:** stdout shape not Claude Code's contract (fix loop) |
| 11 extraction | done | haiku → sonnet fix | candidates never fetched (haiku decided blind) → real 2-step flow, invented `target_id` falls back to ADD |
| 11 transcript | done | haiku → sonnet fix | rendering was a stub; edit-tool detection used non-existent fields (always false) — rewritten against real JSONL format |
| 12 extract CLI | done | sonnet | detached re-exec, < 100ms measured |
| 13 ingest-pr | done | sonnet | `PRSource` + `Detect` (AC-58), Azure only; dry-run on billing-service: 24 PRs |
| 14 cleanup | done | haiku | |
| 15 deploy | done | session + haiku (DEPLOY.md) | host networking after docker-proxy source-IP issue on the real server |
| 16 integration | done | sonnet | |
| 17 seed | done | sonnet | 14 facts; not yet run against the server (owner) |
| 18 egress audit | done | sonnet | `egress-audit.md`: no unexpected destinations |

## Measured on real systems (2026-10-01)

- Embedding: older x86 server (AVX-only) ~17 ms/token; M1 Pro Metal 0.02–0.21s;
  Ollama cap = `num_batch` (default 2048).
- Retrieval eval: paraphrase recall@3 4/6, identifier 5/5; relevant
  similarity 0.45–0.72, unrelated 0.28–0.42, near-duplicates 0.79/0.88;
  Store p95 0.21s. Defaults set to hook 0.50 / store ask 0.65 / update 0.85.
- Server (manual, owner): laptop connects over Tailscale with password;
  wrong password rejected; LAN IP `192.168.1.38:5432` times out; source
  IP seen by Postgres is `100.x`; systemd backup ran (`backup ok`).

## Escalations (Haiku → Sonnet)

Haiku reports claimed completion for WI-08, WI-11 (candidates, transcript)
and WI-07 that the code did not support; each was caught by orchestrator
verification (real eval run, code inspection) and redone on Sonnet.

## Known open items going into review

- `cmd/claude-memory/hook.go` output shape — must be
  `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"<text>"}}`
  or plain text.
- `internal/scrub` AZDO-PAT regex `[A-Za-z0-9+/]{50,}` redacts long paths/URLs.
- Adapters constructed in several `cmd/claude-memory/*.go` files (same
  package as `main.go`) — check against the composition-root constraint.
- `az rest` PR-threads call only fixture-tested.

## Fix loop and final canary (2026-10-01)

Fix loop, 2 iterations (Sonnet), then one Opus investigation:
- composition root moved fully into `main.go`; hook output switched to
  Claude Code's `hookSpecificOutput` contract; tsvector bound as params in
  Create and Update (Update now uses the new values); `UpdateRecord`
  Get → merge → scrub → re-embed; AZDO-PAT patterns anchored.
- Opus, paraphrase recall regression (2/6): the 0.85 candidate multiplier on
  RRF buried strong candidates ~11 places → AC-4 is now a tie-breaker
  (`statusTieBreak`), regression test with synthetic vectors; eval harness
  searched with empty repo (only `*` visible) → per-case `repo`; harness now
  uses real config defaults and reports expected-record rank.
- **Also found:** `writepath.go` sent `updated_at` in UPDATE / SUPERSEDE /
  NOOP update maps, rejected by the Postgres allow-list → those writes failed
  on real Postgres (bug already in commit `e7edbd5`). Fixed.

Final canary (orchestrator, no cache): `go build` (+ linux/amd64), `go vet`
(all tags), `golangci-lint` all tags 0 issues, `go test -race ./...` all ok,
`-tags integration` postgres ok, `-tags ollama` ok, `-tags eval` ok —
paraphrase 6/6, identifiers 5/5, negatives 3/3 below 0.50, near-duplicates
2/2 (ASK 0.79, NOOP 0.88), seeds 11/11, Store p95 0.11–0.2 s.

Follow-ups (not blocking): `plainto_tsquery` ANDs all words, so long
natural-language queries never hit full-text (hybrid helps only short /
identifier queries) — consider OR / `websearch_to_tsquery`; no integration
test drives UPDATE / SUPERSEDE through `memory.Service` against real Postgres.
