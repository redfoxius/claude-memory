# Specs index

| Feature | Spec ID | Status |
|---|---|---|
| [memory-mvp](memory-mvp/01-spec.md) | SPEC-2026-10-01-memory-mvp | implemented and verified (`memory-mvp/07-verification.md`); operational ACs (AC-30/48/49/50/51/53) still need runs on the real setup |
| [namespaces](namespaces/01-spec.md) | namespaces | implemented; integration suite passes on a local Postgres+pgvector; review follow-ups open (see item 1) |
| [staleness-metrics](staleness-metrics/01-spec.md) | SPEC-2026-10-01-staleness-metrics | PR A (staleness) implemented and reviewed; PR B (events + stats) implemented (unit + integration tests, not yet reviewed); manual latency/Azure checks open (WI-0/WI-14) |
| [install-doctor](install-doctor/01-spec.md) | SPEC-2026-10-01-install-doctor | v0.2; slice 1 (`doctor` + plumbing) implemented; its Fable review is FAIL (1 high, 5 medium — see item 8), fixes pending; slice 2 (`install`/`uninstall`) not started; slice 3 (Docker) deferred |

## Backlog (next, in order)

### 1. namespaces — IMPLEMENTED (2026-10-01); follow-ups open

**Done:** namespace on every record, scoped search/dedup/list/lock/by-id,
`global` fallback and explicit writes, `namespaces.yaml` resolver wired into
serve/hook/extract/ingest-pr, `namespaces init|add|which`, install docs,
migration 0002, reviews `namespaces/03` and `04`. The integration isolation
tests pass on a local Postgres+pgvector (`make test-integration`).
**Open (from `namespaces/04-implementation-review.md`):** tests for migration
backfill/idempotency and cross-namespace lock; ingest-pr routing test (two
repos → two writers) and per-subcommand namespace tests; warn when
`extract --run` has no `cwd`; `eval-retrieval` ignores `MEMORY_NAMESPACE`;
glob validation in `namespaces init|add`; `SupersededBy` not checked for
namespace accessibility; per-namespace `pr_ingest` config (item 4).


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

### 2. staleness check + usage metrics — PART A DONE, PART B IMPLEMENTED (review pending)

**Done (PR A):** staleness hint on cards/search/get (tree-compare detector,
cached, deadline-bounded), commit-baseline stamping, PR merge-commit
baseline, extraction repo from checkout, docs; spec v0.2, plan, reviews
`staleness-metrics/03`, `04`. **Done (PR B, not yet reviewed):** `events`
table (migration 0003), hook spool file + drains, service events, `stats`,
cleanup pruning, docs (plan WI-7..12, 13b). **Not done:** optional WI-15 (PR
changed paths). **Manual/open:** hook
latency baseline and re-measure (WI-0/WI-14); `merge-base --is-ancestor`
check of the Azure DevOps merge commit on a real squash PR (AC-11); known gap:
write-path UPDATE with no `files` never re-baselines.


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

### 3. management CLI (`review`) + import of existing knowledge — spec/plan `mgmt-cli/`; PR 1 (ls/show/rm/edit/promote/review, stats import line) implemented, PR 2 (import, migration 0004) implemented

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

### 4. PR ingest: GitHub + GitLab providers, per-namespace config — implemented (spec: [pr-providers](pr-providers/01-spec.md)); GitLab verified with fixtures only

MVP already has the provider-neutral `PRSource` port, auto-detection from the
repo's git `origin`, and per-provider cursors (spec AC-58); only Azure DevOps
was implemented; GitHub and GitLab adapters now exist (v0.2 narrowed the
scope below: github.com only, gitlab.com auto-detected, no `host`/`auth`
keys, repos only from `MEMORY_PR_INGEST_REPOS`). This item added:
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

### 8. `claude-memory install` + `doctor` — interactive, re-runnable setup

**Status (2026-10-02):** specified in `install-doctor/` (spec v0.2, plan,
architecture review `03`, slice-1 implementation review `04`). **Slice 1 implemented**: `claude-memory doctor` (23
read-only checks, `--json`, `--strict`, `--timeout`/`--deadline`, exit codes
0–3), `version`, `migrate`, early dispatch, `config.ParseEnvFile`, the
`integration`/`deploy` embed packages, the `internal/setup` ports, redactor,
settings.json merge, CLAUDE.md block, `.claude.json` reader, manifest reader
and launchd detection, and the DEPLOY.md env-file fix. **Slice 2**
(`install`, `--upgrade`, `uninstall`) is next; slice 3 (Docker topology) and
the §12.1 follow-ups (latency probe, cron, purge, …) are deferred. **Slice-1 review (`04-slice-1-review.md`): gate FAIL — 1 high, 5 medium, 9
low.** Open before slice 1 is marked done: (1) `claude-memory migrate` can
print a password fragment for a DSN whose password contains `/` — route it
through `postgres.Prober{}.Migrate`; (2) record the manual `doctor` run on the
owner's live Mac (WI-S1-12) and tick spec §14 S1. Also recommended: ignore
CLAUDE.md markers inside fenced code blocks; let doctor read a `settings.json`
symlinked outside HOME; make a shell-env `MEMORY_PG_DSN` override a `fail`
when the env file's own line is unusable; tighten hook identity to the first
argv word; CRLF-preserving settings writer. **Not verifiable here:** anything
launchd/macOS. The text below is the original request, kept for the record.

Added 2026-10-01 at the owner's request. Today installation is ~10 manual steps
across `integration/INSTALL.md` and `DEPLOY.md` (only `namespaces init` is a
command). Goal: one guided, safe, repeatable setup plus a health check.

- **`claude-memory install`** — interactive wizard (prompts with sensible
  defaults; `--yes`/flags and `--dry-run` for non-interactive use).
  - Detects the OS/arch (macOS, Linux; say clearly what is unsupported) and
    picks the matching service manager (launchd vs systemd user timers vs
    cron) and package hints (brew/apt).
  - Asks the **topology**: (a) everything local on this machine (local Postgres
    + pgvector + Ollama), (b) Postgres on a remote server (e.g. over Tailscale)
    with Ollama local, (c) Postgres in Docker (local or on a server — generate
    the compose file/run it on request); embeddings via local Ollama or a
    remote Ollama URL.
  - Steps, each shown before it runs and individually skippable: env file
    (0600, DSN + Ollama), database reachable + extensions + migrations,
    Ollama model pull, `namespaces init` (interactive: add projects), MCP
    registration (`claude mcp add`), hooks merged into `~/.claude/settings.json`
    (backup + idempotent JSON merge, never clobber), CLAUDE.md snippet between
    markers, skills copy, scheduled jobs (PR ingest, cleanup), optional seed.
  - **Re-runnable**: detects what is already done (idempotent steps, marker
    blocks, hash/diff of installed files), shows a status table, offers
    repair/upgrade/reconfigure per step, never duplicates or overwrites
    user edits without asking; `install --upgrade` after a new binary.
  - `claude-memory uninstall` reverses everything it installed (keeps data
    unless asked).
- **`claude-memory doctor`** — read-only health check with actionable output:
  binary/version, env file perms, Postgres reachable + pgvector + schema
  version, Ollama up + model present + embed round-trip, MCP registered,
  hooks present and valid, `git` on PATH, namespace resolution for the cwd,
  scheduled jobs loaded, spool/cache dirs, optional hook latency probe
  (p50/p95 over N synthetic prompts). Exit code non-zero on failures;
  `--json` for scripting; `install` runs it at the end.
- Open design points for the spec: which platforms to support first, how to
  drive Docker safely, secrets handling for the DSN prompt, how much of
  `DEPLOY.md` (server side) the wizard should automate vs. print.
- Rough size: M–L.
