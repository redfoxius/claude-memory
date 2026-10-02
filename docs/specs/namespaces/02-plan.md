# Claude Memory — Namespaces

**Status (2026-10-02):** implemented. Core scoping, resolver wiring, the
`global` fallback, `namespaces init|add|which`, install/upgrade docs, store
output `namespace`, `eval-retrieval` scoped to an `eval` namespace, the
`serve` startup log, and the architecture-review blockers are done; the
integration suite (including namespace isolation) passes on a local
Postgres+pgvector (`make test-integration`). Still open (see
`docs/specs/README.md` item 1): migration backfill/idempotency and
cross-namespace lock tests, ingest-pr routing test, per-subcommand `cmd`
tests, no-`cwd` warning in `extract --run`, glob validation,
`SupersededBy` accessibility check, per-namespace `pr_ingest` config (item 4).

## Spec
- `docs/specs/namespaces/01-spec.md` (SPEC-2026-10-01-namespaces, **v0.2**,
  AC-1..AC-30; AC-9 withdrawn)
- Review: `docs/specs/namespaces/03-architecture-review.md` (iteration 0,
  gate FAIL on: integration tests not run, spec/plan contradicting the code,
  rollout order, ingest-pr type probe, AC-23).

## Owner decisions (2026-10-01, supersede plan v0.1)
1. No namespace can be chosen (no file, no match, unreadable/broken file,
   invalid `MEMORY_NAMESPACE`) → the shared `global` namespace. Never
   `acme`, never fail-closed. No project is special-cased in code
   (`namespace.Fallback` = `memory.DefaultNamespace` = `"global"`).
   Migration 0002 still backfills existing rows to `acme` as historic
   data.
2. Unresolved-context writes (inline, session, PR) land in `global`;
   explicit `memory_store(namespace="global")` also works.
3. Local single-user database: cross-namespace leakage is a relevance
   concern, not security. No fail-closed machinery.
4. `namespaces.yaml` is created at installation with
   `claude-memory namespaces init [--default NAME] [--force] [NAME=GLOB...]`,
   extended with `namespaces add NAME GLOB...`, checked with
   `namespaces which [DIR]`. No database needed.

## Context
- MVP is shipped (`4012902`, plus `0181f7a`: the hook skips migrations).
- `5f21cb7` — core scoping:
  - `record.Record.Namespace` and `record.GlobalNamespace`.
  - `memory.Service` scoping: a `namespace` field (from `cfg.Namespace`),
    `WithNamespace()`, `searchNamespaces()` (own + `global`) and
    `getAccessible()` (another namespace's id → `ErrNotFound`).
  - Store ports: `FindCandidates` and `TxStore.AcquireLock` take a
    namespace; `SearchOptions.Namespaces`; `ListFilters.Namespace`.
  - Postgres filters on namespace in the hybrid, full-text-only, candidate
    and list queries; the lock key hashes `namespace:repo:titleHash`.
  - Migration `0002_namespaces.sql`.
  - `internal/namespace`: YAML loader and glob resolver;
    `cmd/claude-memory/namespace.go` (`resolveNamespace`), wired into
    `serve`, `hook`, `extract` and `ingest-pr`.
  - `memory_store` `namespace` input; example YAML; INSTALL.md section;
    unit tests; `TestNamespaceIsolation` (tag `integration`, never run).
- `32a5d57` — fallback `acme` → `global` (`namespace.Fallback`,
  `memory.DefaultNamespace`).
- `e2cfbf3` — `namespaces init|add|which` (`cmd/claude-memory/namespaces.go`,
  `internal/namespace/file.go`: atomic 0600 save, refuses overwrite without
  `--force`, `add` loads before saving), dispatched before config load.
- `1d7deb6` — INSTALL.md step 2a and "Using namespaces", DEPLOY.md upgrade
  note, CLAUDE.md snippet section.
- `5b4b82b` — architecture review.
- `6b6638b` — review fixes: `namespace` in search/get/list outputs;
  ingest-pr `scopeFunc` injected from `main.go`; exact path beats
  `<path>/**`; `Config.Explain` + provenance in `namespaces which`;
  `warnIfFallback` in `extract --run` and `ingest-pr`; `TestMain` stubs
  `resolveNamespace` in `cmd/claude-memory`; `.github/workflows/ci.yml`
  (unit + integration jobs); DEPLOY.md rollback default `'global'` and
  re-home SQL.
- `ed65ec9` (OR-semantics tsquery builder) is backlog item 5. It is not
  part of this feature.

## Scope
- In scope: AC-1..AC-30 (AC-9 withdrawn).
- Out of scope (spec §12): per-namespace DB or role, fail-closed handling,
  a `rehome` command/tool, listing `global` from another namespace, a
  `lang` field, per-namespace `pr_ingest`, symlink and git-toplevel
  resolution.

## Modules Touched
- `internal/record/` — `Namespace` field, `GlobalNamespace`
- `internal/memory/` — service scoping, ports, write path, CRUD, lifecycle
- `internal/postgres/` — queries, lock key, migration 0002, integration tests
- `internal/namespace/` (new) — `namespace.go` (loader, resolver,
  `Explain`), `file.go` (`Init`, `Add`, `Save`); pure: no `memory` or
  `postgres` imports
- `internal/mcpserver/` — `memory_store` input, `namespace` in outputs
- `cmd/claude-memory/` — `namespace.go` (`resolveNamespace`,
  `explainNamespace`, `warnIfFallback`), `namespaces.go` (subcommand),
  per-subcommand resolution and the ingest-pr `scopeFunc` (composition
  root), `main_test.go` (resolver stub)
- `.github/workflows/ci.yml` (new) — unit and `integration`-tag jobs
- `integration/` (example YAML, INSTALL.md, CLAUDE.md snippet) and `DEPLOY.md`

## Architectural Constraints
- `internal/memory` never reads env vars or files. The composition root
  resolves the namespace and passes it through `cfg.Namespace` or
  `Service.WithNamespace`.
- Re-scoping inside command code goes through a function the composition
  root injects (`scopeFunc` for ingest-pr). Inner code never type-asserts a
  port to a concrete service.
- `internal/namespace` is pure and testable with a temp dir and a fake
  `$HOME`.
- Namespace values reach SQL only as bound parameters (`$N`, `ANY($N)`).
- The store applies no namespace policy itself. It filters by exactly what
  it is given (an empty set matches nothing), and the service decides the
  namespace set.
- Resolution never fails a subcommand: errors are logged and degrade to the
  next step, ending at `global` (spec AC-8).

## Work Items

```
01 ─> 02 ─> 03 ─┬─> 05 ─> 06
04 ─────────────┤   07 ─> 10 (CI)
09 (subcmd) ────┘   08 (needs 05, 09)
```

1. **Domain + migration** — DONE (`5f21cb7`). files:
   `internal/record/record.go`,
   `internal/postgres/migrations/0002_namespaces.sql`,
   `internal/postgres/store.go` (embeds both migrations, run in order).
   Satisfies AC-1..AC-4 (by reading; see WI-7 for execution).
   Note: idempotent via `ADD COLUMN IF NOT EXISTS … DEFAULT 'acme'`,
   then `DROP DEFAULT`, then `CREATE INDEX IF NOT EXISTS`. The `acme`
   backfill is historic data only.

2. **Store adapter scoping** — DONE (`5f21cb7`; search rows return
   `namespace` since `6b6638b`). files: `internal/postgres/store.go`,
   `hybrid_search.go`, `advisory_lock.go`.
   - `namespace = ANY($N)` on both search paths; `namespace` selected.
   - `namespace = $4` in both `FindCandidates` (pool and tx).
   - Optional `namespace = $N` in `List`.
   - `namespace` in INSERT, SELECT and RETURNING.
   - Lock key `namespace:repo:titleHash`.

   Satisfies AC-15..AC-18 (store half).

3. **Service scoping** — DONE (`5f21cb7`, `32a5d57`). files:
   `internal/memory/{service,ports,crud,lifecycle,writepath}.go`, `mock/`,
   `namespace_test.go`.
   - Default namespace `memory.DefaultNamespace` = `global`.
   - Search uses `searchNamespaces()`; a `global`-scoped service searches
     `[global]` once.
   - `ListRecords` forces the own namespace.
   - Get, update, deprecate and feedback go through `getAccessible`.
   - The write path stores in the own namespace, or `global` only when
     `StoreRequest.Namespace == "global"`; `validate` rejects any other
     value.

   Satisfies AC-15..AC-17, AC-19..AC-22 (service half).

   Remaining (non-blocking, review L3/L4/L6):
   - `WithNamespace`/`New` reject an empty or invalid name (AC-1), with the
     name rule owned by `record`.
   - `DeprecateRequest.SupersededBy` checked with `getAccessible`.
   - `namespace.Fallback` defined as `record.GlobalNamespace` (one source
     of truth).

4. **`internal/namespace` resolver** — DONE (`5f21cb7`, `32a5d57`,
   `6b6638b`). files: `internal/namespace/namespace.go`,
   `namespace_test.go`.
   - `Load` (a missing file is not an error; invalid names are rejected).
   - `Resolve` / `Explain` (most specific glob by literal-prefix length;
     an exact path scores `len+2` and beats `<path>/**`; ties go to file
     order; provenance `rule <glob>` / `default` / `fallback`).
   - `ForDir(override, dir)`; `Fallback = "global"`.
   - Tests: order, specificity, `~`, `**`, missing file, `TestExplainAndTieBreak`
     (exact vs `/**`, default, fallback).

   Satisfies AC-5..AC-8, AC-6 tie-break (c).

5. **Subcommand wiring** — PARTIAL. files:
   `cmd/claude-memory/{namespace,main,hook,extract,ingestpr}.go`,
   `main_test.go`, `ingestpr_test.go`.

   Done:
   - `buildService` resolves from `os.Getwd()` (`serve`, `seed`,
     `eval-retrieval`).
   - `hook` uses `WithNamespace(resolveNamespace(payload.cwd))`.
   - `extract --run` uses the transcript `cwd`, and `""` when absent.
   - Degrade, don't fail (AC-8): `resolveNamespace` logs an unusable file
     and treats it as absent; logs and ignores an invalid
     `MEMORY_NAMESPACE`.
   - **(a) ingest-pr scoping (AC-13, review M3)** — DONE (`6b6638b`):
     `ingestOneRepo` takes a `scopeFunc`; `cmdIngestPR` builds it from
     `WithNamespace(resolveNamespace(repoPath))`; the type probe is gone.
     `TestIngestOneRepoUsesScopedWriter` asserts the scope is called with
     the repo path.
   - **(d, writers) fallback Warn (AC-28)** — DONE for `extract --run`
     (when the transcript has a `cwd`) and per repo in `ingest-pr`
     (`warnIfFallback`).
   - **(e) resolver stub (AC-29)** — DONE for isolation: `TestMain` sets
     `resolveNamespace` to a constant, so `cmd` tests no longer read the
     developer's file or env.

   Remaining:
   - (a) Extend the ingest-pr test to two repos with a fake `scope`
     returning one writer per path, and assert each repo's PR records land
     in its own writer (AC-13 "no cross-over"). Optional cleanup: in
     `cmdIngestPR`, capture the `*memory.Service` from `buildService`
     instead of re-asserting `svc.(*memory.Service)`.
   - (d) `extract --run` with no transcript `cwd` that falls back: also
     Warn. `serve`: log `namespace=<ns> provenance=<why>` at Info on
     startup (review M6), so a wrong MCP working directory is visible.
   - (e) Per-subcommand tests with a recording stub
     (`resolveNamespace = func(d string) string { seen = append(seen, d);
     return "t-" + filepath.Base(d) }`, restored with `t.Cleanup`):
     hook → `SearchOptions.Namespaces == ["t-<cwd>", "global"]` (capture in
     `fakeHookStore.Search`); extract → namespace from transcript `cwd`,
     and `""` when absent.
   - `eval-retrieval`: `svc.WithNamespace("eval")` unless
     `MEMORY_NAMESPACE` is set (AC-14, review M5).
   - Optional (review L5): `buildService(ctx, cfg, migrate, ns)` so the
     hook resolves once; `warnIfFallback` reuses the loaded config instead
     of reading the YAML a second time.

   Satisfies AC-8, AC-10..AC-14, AC-22, AC-28, AC-29.

6. **MCP surface** — PARTIAL. files: `internal/mcpserver/{types,handlers,
   convert}.go`, `server_test.go`.

   Done:
   - `StoreInput.Namespace` is passed through to `StoreRequest`.
   - **(b) AC-23, review M2** — `namespace` on `SearchResultItem` and
     `RecordOutput` (so `memory_search`, `memory_get`, `memory_list`)
     (`6b6638b`).

   Remaining:
   - (b) `StoreOutput.Namespace` (the namespace written: own, or `global`
     when requested). Needs `StoreResponse` to carry it from the write
     path.
   - Server tests: `namespace` present on all four outputs;
     `namespace="global"` → stored in `global`; `namespace="other"` → tool
     error; `memory_get` on another namespace's id → the same not-found
     error as an unknown id.
   - Manual: confirm Claude Code starts the user-scope stdio server in the
     project directory (spec §4); one session per mapped dir,
     `memory_list` must differ.

   Satisfies AC-19 (end to end), AC-21, AC-23.

7. **Integration tests** — PARTIAL. file:
   `internal/postgres/store_integration_test.go` (tag `integration`,
   testcontainers `pgvector/pgvector:pg16`).

   Done: existing tests pass `Namespace` and `SearchOptions.Namespaces`.
   `TestNamespaceIsolation` covers hybrid search, full-text-only search,
   `FindCandidates` and `List`. It compiles (`go vet -tags integration`).

   Remaining:
   - **Migration test (AC-2..AC-4).** Apply `0001` only, insert rows,
     apply all migrations twice, then check the `acme` backfill, that an
     INSERT without `namespace` fails, and the two indexes in `pg_indexes`.
   - **Lock test (AC-18).** Concurrent near-duplicate writes in `ns-a` and
     `ns-a` give 1 row; in `ns-a` and `ns-b` they give 2 rows.
   - **Service ↔ Postgres test (overlaps backlog item 6).** An exact
     duplicate stored from another namespace gives ADD, not NOOP.
   - **Run green** — in CI (WI-10) or on a Docker host with
     `go test -tags integration ./internal/postgres/...`. These tests
     cannot run in the dev container (no Docker).

   Satisfies AC-2..AC-4, AC-15..AC-18 (real SQL).

8. **Docs** — PARTIAL. files: `integration/namespaces.example.yaml`,
   `integration/INSTALL.md`, `DEPLOY.md`, `integration/claude-md-snippet.md`.

   Done:
   - Example YAML: format, resolution order, fallback `global`
     (`32a5d57`, `e2cfbf3`).
   - INSTALL.md: step 2a (`namespaces init`, upgrade note to map
     `acme`, `which` check) and "Using namespaces" (`1d7deb6`).
   - DEPLOY.md: upgrade note (hook skips migrations; run `cleanup` once;
     0002 backfill; map `acme`), rollback default `'global'`, **(g)**
     re-home SQL keyed on `repo` (`1d7deb6`, `6b6638b`).
   - CLAUDE.md snippet: when `namespace="global"` is appropriate.

   Remaining:
   - DEPLOY.md: state the order explicitly — map `acme`
     (`namespaces init`) before relying on the new binary, then
     `claude-memory cleanup` to migrate, then the count check (review M1).
     Today the note says to map "then", after migrating.
   - INSTALL.md: (g) the re-home SQL (or a pointer to DEPLOY.md) in
     "Using namespaces"; mention the `which` provenance output.
   - CLAUDE.md snippet: "do not add project-specific detail to an existing
     `global` record; store a new one instead".

   Satisfies AC-24.

9. **`namespaces` subcommand** — DONE (`e2cfbf3`, provenance `6b6638b`).
   files: `cmd/claude-memory/namespaces.go`, `internal/namespace/file.go`,
   `internal/namespace/namespace_test.go` (`TestInitAddAndRoundTrip`).
   - `init [--default NAME] [--force] [NAME=GLOB...]`, `add NAME GLOB...`,
     `which [DIR]` printing `<ns>\t(<provenance>)` — **(d)** env / rule
     `<glob>` / default / fallback.
   - Dispatched before config load: no DB, DSN or Ollama.

   Remaining (non-blocking, review L8):
   - Reject bad glob patterns in `init`/`add` (`filepath.Match` per segment
     → `ErrBadPattern`); validate `--default` before the rules.
   - A `cmd` test for argument parsing (`NAME=GLOB`, missing args, unknown
     command) and for each `which` provenance.

   Satisfies AC-25..AC-27.

10. **CI (f)** — PARTIAL (`6b6638b`). file: `.github/workflows/ci.yml`.

    Done: on push and pull request, job `unit` runs `go vet ./...` and
    `go test -race ./...`; job `integration` (ubuntu runner, Docker
    available) runs `go vet -tags integration ./...` and
    `go test -tags integration -race -count=1 ./internal/postgres/...`.

    Remaining: a first green run of both jobs on the branch (not yet
    observed); fix whatever the first real execution of the namespace SQL
    turns up.

    Satisfies AC-30 (and gates WI-7).

## Test Strategy
- **Unit (fakes, run in CI and in the dev container):**
  - `internal/namespace`: order, specificity, exact vs `/**`, ties, `~`,
    `**`, missing file, invalid names, `Explain` provenance, `Init`/`Add`
    round trip and 0600 mode.
  - `internal/memory`: search set, list confinement, by-id hiding, store
    namespace selection (own / explicit `global`), foreign-namespace
    rejection, `global`-scoped service.
  - `internal/mcpserver`: the store input and the `namespace` output field
    on all four tools.
  - `cmd/claude-memory`: resolver stubbed in `TestMain`; per-subcommand
    namespace assertions with a recording stub (hook, extract, ingest-pr
    via `scopeFunc`).
- **Integration (tag `integration`, testcontainers):** WI-7, run by the CI
  `integration` job (WI-10) or on a Docker host.
- **Manual:** two real project directories mapped to two namespaces. The
  hook card in each shows only its own records plus `global`. An explicit
  `memory_store(namespace="global")` from one shows up in the other.
  `namespaces which` in each prints the expected rule. An unmapped
  directory prints `global (fallback)` and `extract --run` there logs one
  Warn.

## Risks
- **Unmapped work lands in `global` (by design).** Unmapped directories,
  transcripts without `cwd` and unmapped ingest-pr repos write to
  `global`, which every project searches; PR ingest is the highest-volume
  writer. Per the owner this is relevance noise, not a breach.
  Mitigation: `namespaces which` provenance, the fallback Warn line
  (AC-28), and the re-home SQL (spec §10).
- **Upgrade window hides Acme history.** If the new binary runs before
  `acme` is mapped, Acme directories resolve to `global`: their
  sessions see none of the backfilled records, and extraction/PR ingest
  write Acme facts to `global`. Mitigation: the rollout below maps
  `acme` first; re-home anything that slipped through.
- **Hook before migration.** The hook skips migrations; until 0002 is
  applied its query fails and it stays silent (no cards). Mitigation: run
  `claude-memory cleanup` once right after installing the binary.
- **`global` records edited from a project.** AC-20 allows it; without
  `namespace` in outputs the model could add project detail to a shared
  record. Mitigation: AC-23 (search/get/list done; store remaining) and
  the CLAUDE.md snippet rule.
- **Wrong working directory for `serve`.** If Claude Code does not start
  the stdio MCP server in the project directory, every tool call uses the
  `default:` (or `global`). Mitigation: verify on the laptop (WI-6
  manual); `serve` startup log (WI-5); `MEMORY_NAMESPACE` in
  `.claude/settings.json`.
- **Integration tests not yet run.** SQL changes are verified only by
  compilation and reading until the CI `integration` job (WI-10) is green.
- **Glob surprises.** A wildcard-free path matches only that exact
  directory, symlinked checkouts do not match, and a bad pattern silently
  never matches until WI-9 validates globs. Use `/**`, `namespaces which`
  and `MEMORY_NAMESPACE`.
- **Eval fixtures in `global`.** `eval-retrieval` from an unmapped checkout
  stores Acme-flavoured fixtures in `global` until WI-5 scopes it to
  `eval`.
- **Hook latency.** The hook reads YAML on every call. The file is tiny
  (< 1 ms), but check it in the AC-30 (MVP) p95 re-measurement.

## Rollout
1. Finish the remaining WI-5..WI-10 items. CI green (unit and
   `integration` jobs); `go test ./... -race` locally.
2. Close Claude Code sessions on the laptop (so neither the hook nor
   `serve` runs mid-upgrade) and run `deploy/backup.sh` (`pg_dump`).
3. Install the new binary.
4. **Before relying on it** (before starting Claude Code), create the
   mapping with Acme mapped:
   `claude-memory namespaces init acme='~/work/acme/**' [pet-game='~/src/pet-game/**' …]`
   (or write the file by hand from `integration/namespaces.example.yaml`).
   Add `MEMORY_NAMESPACE` to any repo outside the mapped paths. Check with
   `claude-memory namespaces which ~/work/acme/<repo>` → `acme (rule …)`.
5. Run `claude-memory cleanup` once. It applies migration 0002; the hook
   never migrates.
6. Check `SELECT namespace, count(*) FROM records GROUP BY 1` shows only
   `acme`.
7. Smoke test: one prompt in a Acme repo (cards as before) and one in a
   side project (no Acme cards); `memory_search` output shows
   `namespace`.
8. If anything reached `global` from a Acme repo, re-home it with the
   documented SQL (spec §10, DEPLOY.md).
9. Rollback: the old binary ignores the extra column, but its INSERTs fail
   because `namespace` is NOT NULL with no default. Restore a default first
   with `ALTER TABLE records ALTER COLUMN namespace SET DEFAULT 'global'`
   (the code's fallback, per decision 1), or restore the backup.

## Verification
- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`,
  `go test ./... -race` all green.
- CI `unit` and `integration` jobs green (WI-10, WI-7).
- Every AC-1..AC-30 except the withdrawn AC-9 appears in at least one Work
  Item's `Satisfies` line and in at least one named test or manual check.
