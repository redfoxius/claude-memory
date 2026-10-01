# Claude Memory — Namespaces

**Status:** in progress. Core scoping and resolver wiring are committed in
`5f21cb7`. The fail-closed rules, tool outputs, integration test run and
docs are still open (see Work Items).

## Spec
- `docs/specs/namespaces/01-spec.md` (SPEC-2026-10-01-namespaces, v0.1,
  AC-1..AC-24)

## Context
- MVP is shipped (`4012902`, plus `0181f7a`: the hook skips migrations).
  Records have no partition key yet.
- Commit `5f21cb7` already adds:
  - `record.Record.Namespace` and `record.GlobalNamespace`.
  - `memory.Service` scoping: a `namespace` field (from `cfg.Namespace`,
    default `acme`), `WithNamespace()`, `searchNamespaces()` (own +
    `global`) and `getAccessible()` (another namespace's id →
    `ErrNotFound`).
  - Store ports: `FindCandidates` and `TxStore.AcquireLock` take a
    namespace; `SearchOptions.Namespaces`; `ListFilters.Namespace`.
  - Postgres filters on namespace in the hybrid, full-text-only, candidate
    and list queries, and the lock key hashes `namespace:repo:titleHash`.
  - Migration `0002_namespaces.sql`.
  - `internal/namespace`: YAML loader and glob resolver.
  - `cmd/claude-memory/namespace.go` (`resolveNamespace`), wired into
    `serve`, `hook`, `extract` and `ingest-pr`.
  - `memory_store` `namespace` input.
  - `integration/namespaces.example.yaml` and an INSTALL.md section.
  - Unit tests: `internal/memory/namespace_test.go` and
    `internal/namespace/namespace_test.go`.
  - Integration test `TestNamespaceIsolation` (tag `integration`). It
    compiles but has never run.
- `ed65ec9` (OR-semantics tsquery builder) is backlog item 5. It is not
  part of this feature.

## Scope
- In scope: AC-1..AC-24.
- Out of scope (spec §12): per-namespace DB or role, moving records between
  namespaces, listing `global`, a `lang` field, per-namespace `pr_ingest`,
  symlink and git-toplevel resolution.

## Modules Touched
- `internal/record/` — `Namespace` field, `GlobalNamespace`
- `internal/memory/` — service scoping, ports, write path, CRUD, lifecycle
- `internal/postgres/` — queries, lock key, migration 0002, integration tests
- `internal/namespace/` (new) — `namespaces.yaml` loader and resolver (pure:
  no `memory` or `postgres` imports)
- `internal/mcpserver/` — `memory_store` input, `namespace` in outputs
- `cmd/claude-memory/` — resolution for each subcommand (composition root)
- `integration/` (example YAML, INSTALL.md, CLAUDE.md snippet) and `DEPLOY.md`

## Architectural Constraints
- `internal/memory` never reads env vars or files. The composition root
  resolves the namespace and passes it through `cfg.Namespace` or
  `Service.WithNamespace`.
- `internal/namespace` is pure and testable with a temp dir and a fake
  `$HOME`.
- Namespace values reach SQL only as bound parameters (`$N`, `ANY($N)`).
- The store applies no namespace policy itself. It filters by exactly what
  it is given, and the service decides the namespace set.

## Work Items

```
01 ─> 02 ─> 03 ─┬─> 05 ─> 06
04 ─────────────┘   07 (needs 05)   08 (needs 05, 07)
```

1. **Domain + migration** — DONE (`5f21cb7`). files:
   `internal/record/record.go`,
   `internal/postgres/migrations/0002_namespaces.sql`,
   `internal/postgres/store.go` (embeds both migrations, run in order).
   Satisfies AC-1..AC-4.
   Note: the migration is idempotent because it uses `ADD COLUMN IF NOT
   EXISTS … DEFAULT 'acme'`, then `DROP DEFAULT`, then
   `CREATE INDEX IF NOT EXISTS`.

2. **Store adapter scoping** — DONE (`5f21cb7`). files:
   `internal/postgres/store.go`, `hybrid_search.go`, `advisory_lock.go`.
   Changes:
   - `namespace = ANY($N)` on both search paths.
   - `namespace = $4` in both `FindCandidates` (pool and tx).
   - Optional `namespace = $N` in `List`.
   - `namespace` in INSERT, SELECT and RETURNING.
   - Lock key `namespace:repo:titleHash`.

   Satisfies AC-15..AC-18 (store half).

3. **Service scoping** — DONE (`5f21cb7`). files:
   `internal/memory/{service,ports,crud,lifecycle,writepath}.go`, `mock/`,
   `namespace_test.go`. Changes:
   - Search uses `searchNamespaces()`.
   - `ListRecords` forces the own namespace.
   - Get, update, deprecate and feedback go through `getAccessible`.
   - The write path resolves `ns` (own, or `global` only when
     `StoreRequest.Namespace == "global"`), then locks, finds candidates
     and creates in `ns`.
   - `validate` rejects any other value.

   Satisfies AC-15..AC-17, AC-19..AC-21 (service half).

4. **`internal/namespace` resolver** — DONE (`5f21cb7`). files:
   `internal/namespace/namespace.go`, `namespace_test.go`. Provides:
   - `Load` (a missing file is not an error; invalid names are rejected).
   - `Resolve` (most specific glob by literal-prefix length; an exact path
     beats a wildcard; ties go to file order).
   - `ForDir(override, dir)`.

   Satisfies AC-5..AC-7.

5. **Subcommand wiring** — PARTIAL (`5f21cb7`). files:
   `cmd/claude-memory/{namespace,main,hook,extract,ingestpr}.go`.

   Done:
   - `buildService` resolves from `os.Getwd()` (`serve`, `seed`,
     `eval-retrieval`).
   - `hook` uses `WithNamespace(resolveNamespace(payload.cwd))`.
   - `extract --run` uses the transcript `Cwd`.
   - `ingest-pr` re-scopes per repo path.

   Remaining:
   - **Fail closed (AC-8, AC-9).** `resolveNamespace` currently logs and
     falls back to `acme` when the YAML is broken, and to YAML
     resolution when `MEMORY_NAMESPACE` is invalid. It also accepts
     `global`, because `ValidName("global")` is true. Change it to return
     `(string, error)` and reject `global` in `ForDir` and `Load`. Then
     handle the error per subcommand: the hook returns silently; `extract`
     and `ingest-pr` log and skip the write (for `ingest-pr`, skip that
     repo and leave its cursor untouched); `serve` and `seed` exit non-zero.
   - `extract`: when the transcript has no `cwd`, resolve with `""` (env,
     then `default:`, then `acme`). Do not guess another way.
   - `ingest-pr`: replace the anonymous type assertion
     `svc.(interface{ WithNamespace(string) *memory.Service })` with a
     small consumer-declared port, e.g. `scoper` in `ingestpr.go`, so a
     fake service in tests is re-scoped too. Add a test in which two repos
     map to two namespaces.
   - `eval-retrieval`: run under a dedicated `MEMORY_NAMESPACE=eval` (or
     have the subcommand set it), so fixtures never land in `acme`.
   - Tests: make `resolveNamespace` a variable (already done) and add
     `cmd/claude-memory` tests for hook, extract and ingest-pr with a stub
     resolver and with an error-returning resolver.

   Satisfies AC-8..AC-14.

6. **MCP surface** — PARTIAL. files: `internal/mcpserver/{types,handlers,
   convert}.go`, `server_test.go`.

   Done: `StoreInput.Namespace` is passed through to `StoreRequest`.

   Remaining:
   - Add `namespace` to the search, get, list and store outputs (AC-23).
   - In server tests:
     - `namespace="global"` → stored in `global`;
     - `namespace="other"` → tool error;
     - `memory_get` on an id from another namespace → the same not-found
       error as for an unknown id.

   Satisfies AC-21, AC-23 (and AC-19 end to end).

7. **Integration tests** — PARTIAL. file:
   `internal/postgres/store_integration_test.go` (tag `integration`,
   testcontainers `pgvector/pgvector:pg16`).

   Done: existing tests now pass `Namespace` and `SearchOptions.Namespaces`.
   `TestNamespaceIsolation` covers hybrid search, full-text-only search,
   `FindCandidates` and `List`.

   Remaining:
   - **Migration test (AC-2..AC-4).** Apply `0001` only, insert rows,
     apply all migrations twice, then check the backfill, that an INSERT
     without `namespace` fails, and the two indexes in `pg_indexes`.
   - **Lock test (AC-18).** Concurrent near-duplicate writes in `ns-a` and
     `ns-a` give 1 row; in `ns-a` and `ns-b` they give 2 rows.
   - **Service ↔ Postgres test (overlaps backlog item 6).** An exact
     duplicate stored from another namespace gives ADD, not NOOP.
   - **Run.** These tests **cannot run in the dev container** (no Docker
     or pgvector). They must be run with
     `go test -tags integration ./internal/postgres/...` on a machine with
     Docker before this feature is marked done. Until then,
     `go vet -tags integration ./...` is the gate (it passes at `5f21cb7`).

   Satisfies AC-2..AC-4, AC-15..AC-18 (real SQL).

8. **Docs** — PARTIAL. files: `integration/namespaces.example.yaml` (done),
   `integration/INSTALL.md` (section done), `DEPLOY.md`,
   `integration/claude-md-snippet.md`.

   Remaining:
   - `DEPLOY.md`: an upgrade note (0002 runs on the first
     `serve`/`seed`/`cleanup`/`ingest-pr`, not on the hook; take a backup
     first; backfill to `acme`).
   - The capture-rule snippet: when `namespace="global"` is allowed.
   - INSTALL.md: describe the fail-closed behaviour once WI-5 lands.

   Satisfies AC-24.

## Test Strategy
- **Unit (fakes, run in CI and in the dev container):**
  - `internal/namespace`: table tests for order, specificity, ties, `~`,
    `**`, a missing file, invalid names and `global` rejection.
  - `internal/memory`: search set, list confinement, by-id hiding, store
    namespace selection, foreign-namespace rejection.
  - `internal/mcpserver`: the store input and the output field.
  - `cmd/claude-memory`: per-subcommand resolution and fail-closed paths
    with a stubbed `resolveNamespace`.
- **Integration (tag `integration`, testcontainers):** WI-7. It needs
  Docker, so it is run outside the dev container on the owner's laptop or
  in CI with Docker.
- **Manual:** two real project directories mapped to two namespaces. The
  hook card in each shows only its own records plus `global`. An explicit
  `memory_store(namespace="global")` from one shows up in the other.

## Risks
- **Fail-open fallback (current code).** A broken `namespaces.yaml` or a
  typo in `MEMORY_NAMESPACE` silently writes side-project facts into
  `acme`, which is exactly the leakage this feature exists to prevent.
  Mitigation: WI-5 fail-closed handling.
- **`global` via configuration.** `MEMORY_NAMESPACE=global` today makes
  extraction and PR ingest write to `global` automatically.
  Mitigation: reject `global` during resolution (AC-9).
- **Wrong working directory for `serve`.** If Claude Code does not start
  the stdio MCP server in the project directory, every tool call uses the
  `default:` namespace. Mitigation: verify on the laptop (WI-6 manual
  step). Repos outside mapped paths can set `MEMORY_NAMESPACE` in
  `.claude/settings.json`.
- **Integration tests not run.** SQL changes are verified only by
  compilation until WI-7 runs on a machine with Docker.
- **Glob surprises.** A wildcard-free path matches only that exact
  directory, and symlinked checkouts do not match. Both are documented in
  the example file. Use `/**` and `MEMORY_NAMESPACE` to cover these cases.
- **Hook latency.** The hook reads YAML on every call. The file is tiny
  (< 1 ms), but check it in the AC-30 p95 re-measurement.

## Rollout
1. Merge WI-5..WI-8. Run `go test ./... -race` and `go vet -tags
   integration ./...` locally, and the integration suite on a host with
   Docker.
2. Before deploying, run `deploy/backup.sh` (`pg_dump`).
3. Install the new binary on the laptop. Start one Claude Code session so
   `serve` starts and migration 0002 applies. The hook never migrates.
4. Check that `SELECT namespace, count(*) FROM records GROUP BY 1` shows
   only `acme`.
5. Create `~/.config/claude-memory/namespaces.yaml` from the example. Add
   `MEMORY_NAMESPACE` to any repo outside the mapped paths.
6. Smoke test: one prompt in a Acme repo (cards as before) and one in a
   side project (no Acme cards).
7. Rollback: the old binary ignores the extra column, but its INSERTs fail
   because `namespace` is NOT NULL with no default. To roll back, restore
   the default first with `ALTER TABLE records ALTER COLUMN namespace SET
   DEFAULT 'acme'`, or restore the backup.

## Verification
- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`,
  `go test ./... -race` all green.
- Integration suite green on a Docker host (WI-7).
- Every AC-1..AC-24 appears in at least one Work Item's `Satisfies` line
  and in at least one named test or manual check.
