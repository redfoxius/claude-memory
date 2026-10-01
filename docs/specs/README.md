# Specs index

| Feature | Spec ID | Status |
|---|---|---|
| [memory-mvp](memory-mvp/01-spec.md) | SPEC-2026-10-01-memory-mvp | clarifying |
| [namespaces](namespaces/01-spec.md) | namespaces | implemented (integration tests pending a Docker run) |
| [staleness-metrics](staleness-metrics/01-spec.md) | SPEC-2026-10-01-staleness-metrics | PR A (staleness) implemented; PR B (events + stats) not started |

## Backlog (next, in order)

### 1. namespaces — start right after memory-mvp ships

Input for `spec-creator` (agreed with the owner 2026-10-01, not yet a spec):

- New `namespace` field on every record (e.g. `acme`, `pet-game`). All
  reads and writes are scoped to it: search, dedup candidates, list,
  advisory-lock key. Records from different namespaces never mix or merge.
- Namespace resolved automatically from the project directory via
  `~/.config/claude-memory/namespaces.yaml` (glob path → namespace, most
  specific match wins, `default:` fallback); an explicit
  `MEMORY_NAMESPACE` (e.g. in a repo's `.claude/settings.json` env) overrides.
  Applies to the MCP server, hook, session extraction and PR ingest.
- Optional `global` namespace for stack-generic facts (Go, Docker, Postgres
  gotchas); search = current namespace + `global`. Nothing is written to
  `global` automatically — explicit only, so company content never leaks.
- Language/stack inside one namespace (e.g. Go vs JavaScript at Acme) is a
  tag / `lang` field, not a separate namespace — company-wide conventions
  should still surface across stacks.
- Migration: existing records backfilled to `acme`; indexes include
  `namespace`.
- Out of scope for now (possible later): physically separate Postgres
  database + role per namespace for hard isolation (same code, different DSN
  per namespace).
- Rough size: ~1 day incl. tests; full SDD flow (next spec version → plan → plan
  review ×2 → implementation).

### 2. staleness check + usage metrics — after namespaces

- **Staleness check on retrieval.** Records already carry `files[]` and
  `commit_sha`. When a record is returned (hook card, `memory_search`,
  `memory_get`) and the current repo matches, run
  `git log --oneline <commit_sha>..HEAD -- <files>`; if any listed file
  changed since the record was written, flag it (`stale_hint: true` + count of
  commits) and show "⚠ code changed since this was recorded" on the card.
  Must stay within the hook latency budget (cap / skip on timeout; cache per
  session). Missing sha/files or a foreign repo → no flag, not an error.
- **Usage metrics.** Append-only `events` table: hook card injected (record
  id, similarity), `memory_feedback` outcome, record created/updated/
  superseded/deprecated with its source (inline/session/pr), cleanup deletes.
  `claude-memory stats [--since 30d]`: cards injected vs marked useful
  (precision proxy), records per source, candidate→active promotion rate,
  stale-flag rate. No content in events — ids and enums only.
- Rough size: S each.

### 3. management CLI (`review`) + import of existing knowledge — after 2

- **CLI:** `claude-memory ls|show|rm|edit|promote` and an interactive
  `claude-memory review` that walks `candidate` records (newest first, with
  stale flag and similar records) and lets the owner approve (→ active),
  edit, deprecate or delete each — goal: a weekly ~2-minute pass.
  Goes through the same service write path (scrubbing, dedup, events).
- **Import:** one-off `claude-memory import` for existing knowledge —
  Claude Code auto-memory files (`~/.claude/projects/*/memory/*.md`,
  frontmatter → kind/title/tags, project dir → repo) and repo `INSIGHTS.md`
  entries (date + file:line → commit/files). Imported records start as
  `candidate`, go through dedup (re-running import is a no-op), never import
  `CLAUDE.md`/`MEMORY.md` index files.
- Rough size: S–M.

### 4. PR ingest: GitHub + GitLab providers, per-namespace config — after namespaces

MVP already has the provider-neutral `PRSource` port, auto-detection from the
repo's git `origin`, and per-provider cursors (spec AC-58); only Azure DevOps
is implemented. This item adds:
- `PRSource` adapters for GitHub (`gh` CLI / REST, incl. GitHub Enterprise
  hosts) and GitLab (`glab` CLI / REST, incl. self-hosted hosts) — completed
  PRs/MRs since cursor, description, diff summary, review comments.
- Per-namespace `pr_ingest` section in `namespaces.yaml`: optional
  `provider` / `host` override when detection can't tell (self-hosted),
  `auth: {cli: gh|glab|az}` (default — reuses the CLI's own login) or
  `auth: {token_env: NAME}` for a second account on the same platform (env var
  name only, never the token itself).
- Repos to ingest = git repos under the namespace's `paths`.
- Rough size: S per provider + S for config.

### 5. full-text search for natural-language queries — implemented; review fixes applied; eval pending

Implemented: the tsquery is built inside SQL from Postgres' own parser (`to_tsvector('simple', prompt)` lexemes; identifier-like tokens through `phraseto_tsquery`), so identifiers the index keeps whole (`db.withtx`, `pg_hba.conf`, `100.64.0.0` + `/10`) match and survive long prompts. OR-noise guard: identifier matches always count; others must reach 25% of the best flat-weight ts_rank (an absolute floor cannot work: ts_rank scales with the number of OR terms). Eval category `long_prompt_identifier` (4 cases) + integration tests; review: `fts-and-service-tests-review.md`. **Not verified:** "no paraphrase regression" needs `claude-memory eval-retrieval` against real Ollama.

Measured 2026-10-01 (eval harness): `plainto_tsquery` ANDs every word of
the query, so any natural-language prompt (hook, `memory_search`) gets **no**
full-text hits — hybrid search degenerates to vector-only, and identifier
matches help only for short, identifier-style queries.
- Build the tsquery with OR semantics over non-stopword terms (e.g. tokens
  joined with `|` via `to_tsquery` with safe quoting, or `websearch_to_tsquery`
  with `OR` rewriting) so long prompts still surface records that share rare
  identifiers / terms; keep values bound as `$N` params.
- Guard against OR-noise: full-text rank only counts in RRF when the match
  has a minimum `ts_rank` or ≥1 rare term; keep raw `Similarity` untouched.
- Eval harness: add long-prompt cases that contain an identifier buried in a
  sentence; require identifier recall@3 = 100% and no paraphrase regression.
- Rough size: S.

### 6. integration tests for UPDATE / SUPERSEDE through the service — DONE (2026-10-01)

Implemented in `internal/postgres/service_integration_test.go` (NOOP promotion, ExtractionDecision UPDATE, SUPERSEDE + rollback, UpdateRecord reindex, judgment band); runnable locally via `MEMORY_TEST_PG_ADMIN_DSN` / `make test-integration`.

`writepath.go` sent `updated_at` in UPDATE / SUPERSEDE / NOOP update maps and
every such write failed on real Postgres; it was caught only by the eval's
NOOP case, not by tests (unit tests use mocks; postgres integration tests
call the adapter directly).
- Add `internal/memory` ↔ `internal/postgres` integration tests (tag
  `integration`, testcontainers pgvector, fake embedder with fixed vectors)
  that drive `memory.Service` end-to-end: inline NOOP (seen_count++ and
  candidate→active at 2), ExtractionDecision UPDATE, SUPERSEDE (old row
  deprecated + superseded_by, new row active, one transaction, rollback on
  failure), `UpdateRecord` content change (re-embedded + full-text reflects new
  content).
- Rough size: S.

### 7. hook latency: skip migrations on the hot path — DONE in code (re-measure p95 on the real setup)

Measured 2026-10-01 on the real setup (laptop → tailnet Postgres): hook
round-trip 215–343 ms, at/over the AC-30 p95 budget of 300 ms. Every
`claude-memory hook` start runs the migration check in `postgres.New`.
Run migrations only from `serve` / `seed` / `cleanup` / `ingest-pr` (or an
explicit `migrate` subcommand) and open the hook's pool without them; then
re-measure p95 over ~50 prompts.
