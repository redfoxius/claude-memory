# Architecture Review — namespaces (iteration 0)

- **Reviewer:** architecture-reviewer, skills: golang-architecture, security.
- **Target:** namespaces feature as of `e2cfbf3` (`5f21cb7` core scoping,
  `32a5d57` fallback → `global`, `e2cfbf3` `namespaces init|add|which`),
  against `01-spec.md` v0.1, `02-plan.md`, and backlog item 1 in
  `docs/specs/README.md`. Line numbers are at `e2cfbf3`.
- **Owner decisions taken during the review (supersede the spec):**
  1. Unresolvable namespace (no file, no match, broken file) → `global`, not
     `work`. No project is special-cased in code; 0002 still backfills
     legacy rows to `work`.
  2. Single-user local database: cross-namespace leakage is a relevance
     concern, not a security one. No fail-closed machinery.
  3. `namespaces.yaml` is created at install time by the new subcommand.
- **Verified locally:** `go build ./...`, `go vet ./...`,
  `go vet -tags integration ./internal/postgres/...`, `go test ./...` all
  green. The integration suite cannot run here (no Docker); every SQL claim
  below is from reading the statements and hand-checking placeholder numbers.
- **Gate:** **FAIL** — 0 critical, 2 high, 6 medium, 10 low. The isolation
  code is sound; the gate fails on unexecuted SQL tests, a spec that now
  contradicts the code, and a rollout order that hides existing records.

## Findings

| Sev | file:line | Rule | Claim | Recommendation |
|---|---|---|---|---|
| HIGH | `internal/postgres/store_integration_test.go:1252`; plan WI-7 | verification (plan `02-plan.md:176-181`) | Every namespace SQL change (3 search WHERE blocks, 2 `FindCandidates`, `List`, 2 `Create`, lock key) and migration 0002 are verified only by `go vet` and this read-through. `TestNamespaceIsolation` has never executed; the migration test (AC-2..4) and lock test (AC-18) do not exist. There is no CI (`.github/workflows` absent). | Before marking done: add the two missing tests, run `go test -tags integration ./internal/postgres/...` on a Docker host, and add a GitHub Actions job for the `integration` tag (ubuntu runners have Docker) so this stops depending on the owner's laptop. |
| HIGH | `01-spec.md:41,62-65,159-174,231-235,266,302` (glossary "built-in fallback: work", §3 Upgrade, AC-8, AC-9, AC-22, §8, clarification 3); `02-plan.md:123-131,215-221,235-251` | spec/plan/code agreement | After decisions 1-2 the DoD is unattainable as written: AC-9 ("`global` can never be the resolved namespace") and AC-22 ("nothing reaches `global` automatically") are the opposite of `namespace.go:20` (`Fallback = "global"`), `service.go:26`, and `extract.go:143`. AC-8's fail-closed rules and plan WI-5's "reject `global`" items are void. §3 "Acme sessions behave exactly as before" is false without a mapping (see M1). Clarification 3 is still marked NEEDS CLARIFICATION although it is now decided. | Re-baseline to spec v0.2 before sign-off: fallback = `global`; AC-8 → "a broken file is logged and resolves as if absent"; drop AC-9; AC-22 → "no *automatic* writer sets `StoreRequest.Namespace`; unmapped directories write to `global` by resolution"; add ACs for `namespaces init|add|which`; record decisions 1-3 in §13; rewrite plan WI-5 remaining, Risks and Rollout. |
| MEDIUM | `02-plan.md:235-251` (rollout steps 3→5); `integration/INSTALL.md:186-208`; `DEPLOY.md` (no upgrade note) | upgrade safety | With `global` as fallback and legacy rows in `work`, the plan still installs the binary (step 3) *before* the mapping file (step 5). In that window every Acme session sees none of its history (search = `global` only) and every extraction/PR ingest writes Acme facts to `global`. INSTALL.md now says to map `work`, but the rollout order and DEPLOY.md do not. | Make `claude-memory namespaces init work='~/work/acme/**'` step 1 of the upgrade, before the first `serve`. Add to DEPLOY.md: backup, then init, then binary, then one `serve` to migrate, then `SELECT namespace, count(*) …`. Document the recovery for anything that slipped into `global` (L1c). |
| MEDIUM | `internal/mcpserver/convert.go:12-45`, `handlers.go:43-54`, `types.go:20-31,135-155` | AC-23 | No tool output carries `namespace`. Under decision 1 this matters more: unmapped projects write to `global`, and `global` rows are updatable from any namespace (AC-20). The model cannot tell a shared record from a project one, so it cannot follow the CLAUDE.md rule ("no company content in `global`") when enriching via `memory_update`, nor explain a card that came from another project. | Add `Namespace string \`json:"namespace"\`` to `SearchResultItem` and `RecordOutput`, set in `handleSearch` and `recordToOutput`; add it to `StoreOutput` (the namespace written). Server tests per plan WI-6. |
| MEDIUM | `cmd/claude-memory/ingestpr.go:82-84` | consumer-declared ports (MVP review, `06-architecture-review.md:25`) | `svc.(interface{ WithNamespace(string) *memory.Service })` is a concrete-type probe on a port. The `ok=false` branch silently keeps the unscoped service, so with `mock.NewMemoryService()` (`ingestpr_test.go:132,165,193,231`) no re-scoping happens at all. AC-13 ("two repos → two namespaces") therefore has no test and cannot get one through this seam. | Let the composition root own scoping: `cmdIngestPR` passes `scope func(repoPath string) extraction.StoreWriter` (built from `svc.WithNamespace(resolveNamespace(p))`), `ingestOneRepo` calls it once per repo. Test: fake `scope` records the paths and returns a fake writer per namespace; assert each PR record lands in its repo's writer. |
| MEDIUM | `cmd/claude-memory/hook_test.go:115` → `hook.go:83`; no test references `resolveNamespace` or `WithNamespace` in `cmd/` | testability | `resolveNamespace` is a package variable (good) but nothing stubs it. `hookCmd`, `runExtract` and `ingestOneRepo` call it directly, so `hook_test.go` reads the developer's real `$HOME/.config/claude-memory/namespaces.yaml` and `MEMORY_NAMESPACE`. AC-10..AC-13 have zero automated coverage at the cmd layer. | In `cmd` tests set `resolveNamespace = func(dir string) string { seen = append(seen, dir); return "t-"+filepath.Base(dir) }` with `t.Cleanup` restore. Add: hook → `SearchOptions.Namespaces == ["t-<cwd>", "global"]` (capture in `fakeHookStore.Search`); extract → namespace from transcript `cwd`, and `""` when absent; ingest-pr → per-repo (after M3). Also pass `home` into `namespace.Load` (or `LoadWithHome`) so `TestTildeExpansion` stops poking an unexported field. |
| MEDIUM | `cmd/claude-memory/main.go:222-233` (`cmdEvalRetrieval` → `buildService`), `internal/evalset/run.go:99,173`; fixtures `testdata/evalset/seed_records.json` | eval hygiene | `eval-retrieval` stores its fixtures (Acme-specific: `billing-service`, "acme/CLAUDE.md convention") as permanent records in whatever namespace the cwd resolves to, with no teardown. Run from an unmapped checkout they land in `global` and surface as hook cards in every project. Pre-existing in the MVP (one pool), now reachable from everywhere. | `cmdEvalRetrieval`: `svc = svc.WithNamespace("eval")` unless `MEMORY_NAMESPACE` is set; plan WI-5 already lists this. Optionally delete the stored ids at the end of `evalset.Run`. |
| MEDIUM | `integration/INSTALL.md:55` (`claude mcp add --scope user … serve`), `main.go:115-117` | AC-10 assumption (spec §4) | `serve` scopes everything to `os.Getwd()` at startup. Whether Claude Code starts a user-scope stdio server in the project directory is still an unverified assumption (plan WI-6). If it does not, every `memory_store`/`memory_search` from every project uses `default:` (`global`), and the feature is inert for the MCP path. | Verify on the laptop with `claude-memory namespaces which` semantics in mind (one session per mapped dir, `memory_list` must differ). Have `serve` log `namespace=<ns>` at Info on startup so a wrong cwd is visible in the MCP server log. Keep the `.claude/settings.json` `MEMORY_NAMESPACE` fallback documented. |
| LOW | `cmd/claude-memory/namespace.go:23-36`, `extract.go:143` (no `cwd` → `global`), `ingestpr.go:83` (unmapped repo → `global`; PR records are `active` at creation, `writepath.go:136`) | decision 2: relevance, not security | Company content reaches `global` whenever a directory is unmapped, the YAML is broken, or a transcript has no `cwd`; PR ingest is the highest-volume writer and skips the candidate gate. Per the owner this is noise, not a breach, and the spec excludes moving records (§12). | Cheap visibility and recovery, no gating: (a) `namespaces which` prints provenance (`env` / rule / `default:` / fallback) — `Resolve` returns it alongside the name; (b) `serve`, `extract --run` and `ingest-pr` log once at Warn when the namespace came from the fallback or from a broken file; (c) document the re-home SQL in INSTALL/DEPLOY — records carry `repo`, so `UPDATE records SET namespace='work' WHERE namespace='global' AND repo IN (…)` is a safe one-liner after a mapping is added; consider `namespaces rehome NS REPO…` later. |
| LOW | `internal/namespace/namespace.go:109-114` | AC-6 / clarification 5 | "Exact path beats a wildcard with the same prefix" does not hold for the common pair: `/a/b` scores `len+1 = 5`, `/a/b/**` scores `IndexAny = 5` (its literal prefix is `/a/b/`). The tie falls to file order (`:86`, strict `>`). Harmless in the example file (both globs map to the same namespace) but wrong when two namespaces nest. No test covers it. | Measure the literal prefix with trailing `/` trimmed (`strings.TrimRight(g[:i], "/")`) and give an exact path `len(g)+1`; or compare `(prefixLen, isExact)` tuples. Add the `~/work/acme` vs `~/work/acme/**` case to `TestResolve`. |
| LOW | `internal/memory/service.go:37-40,53-57`; `record.go` `Validate` (no namespace check); 0002 `NOT NULL` accepts `''` | AC-1 "non-empty namespace" | Nothing in code enforces a valid, non-empty namespace: `WithNamespace("")` yields a service that inserts `namespace = ''` (NOT NULL does not reject the empty string). The resolver never returns `""`, so only a programming error reaches it. | `WithNamespace`/`New` validate with the same regex (`record.ValidNamespace`, moved from `namespace.ValidName` so `record` owns the rule), or `record.Validate` rejects an empty namespace. |
| LOW | `internal/memory/crud.go:153-155` | AC-19 completeness | `DeprecateRequest.SupersededBy` is written without `accessible()`; a project can link its record to another namespace's id (FK satisfied, cross-namespace reference stored). | `getAccessible(ctx, *req.SupersededBy)` before building `updates`. |
| LOW | `internal/memory/writepath.go:64,75` + `internal/postgres/advisory_lock.go:27` | AC-18 | Lock key is `ns:repo:repo:hash` — `computeLockKey` already embeds `repo`, then `AcquireLock` prepends `namespace:repo` again. Correct (namespace is in the key) but redundant; FNV-64 collisions only cause spurious serialization. | Either drop `repo` from `AcquireLock` or make `computeLockKey(ns, repo, title)` and pass the finished key. |
| LOW | `cmd/claude-memory/main.go:115-117` then `hook.go:83`; `extract.go:140` + `extraction.ProcessSession` | hot-path economy | The hook resolves twice (once in `buildService` from the hook's own cwd, once from the payload `cwd`), reading the YAML twice per prompt. `extract --run` parses the transcript twice (once for `cwd`, again inside `ProcessSession`). Both are tiny but needless. | `buildService(ctx, cfg, migrate, ns string)` — callers that re-scope pass `""` and skip the first resolve; have `ProcessSession` accept a parsed transcript or return its `cwd`. |
| LOW | `internal/record/record.go:13`, `internal/memory/service.go:26`, `internal/namespace/namespace.go:20` | single source of truth | Three constants for one concept; `memory.DefaultNamespace` and `namespace.Fallback` are both "global" by convention, not by reference. | `namespace.Fallback = record.GlobalNamespace` (importing `record` keeps the package free of `memory`/`postgres`), or drop `memory.DefaultNamespace` and require callers to pass a namespace. |
| LOW | `internal/namespace/file.go:84-103`, `cmd/claude-memory/namespaces.go:42-49,56-64` | input validation | `init`/`add` validate the namespace name but not the glob: a bad bracket pattern is accepted, then `filepath.Match` returns `ErrBadPattern` at resolve time and the rule silently never matches (`namespace.go:137`). `init` validates `--default` after adding rules (order nit). | In `add`, run `filepath.Match(seg, "")` per segment and reject `ErrBadPattern`; validate `def` first. |
| LOW | `internal/postgres/migrations/0001_init.sql:7-10`, `store.go:30,83-100` | migration mechanics | `schema_migrations` exists but is never written; migrations rely on per-statement idempotency and a `;` split (fine for 0002: no `;` in literals, `ADD COLUMN IF NOT EXISTS`, `DROP DEFAULT` is a no-op when absent, `CREATE INDEX IF NOT EXISTS`). The `"already exists"` swallow at `:94` is broad. Not a defect today; fragile for the first non-idempotent migration. | Note only. When a third migration arrives, record versions in `schema_migrations` and stop re-running 0001/0002 on every start. |
| LOW | `DEPLOY.md` (no mention of 0002); `02-plan.md:248-251` rollback | rollback | Between installing the new binary and the first migrating subcommand, the hook (`postgres.Open`, no migrate) queries `namespace` on a schema without it → SQL error → silent, no cards; hybrid's degrade to full-text fails the same way (`store.go:327`). Rollback to the old binary breaks INSERTs (NOT NULL, no default). Both are known in the plan but absent from DEPLOY.md. | DEPLOY.md upgrade note: run `deploy/backup.sh`, run `serve` or `seed` once to migrate, rollback = `ALTER TABLE records ALTER COLUMN namespace SET DEFAULT 'global'` (not `work`, per decision 1) or restore the dump. |

## The two open decisions in the spec (§13 #3, #4)

- **#3 broken config / `MEMORY_NAMESPACE=global`** — superseded by decisions
  1-2. Under a single-user, relevance-only model the review agrees: the cost
  of fail-closed (memory silently off for a session) is paid on every typo,
  while the cost of fail-open is a stray card and a one-line re-home
  (`repo` is stored on every record). What remains worth doing is
  visibility, not gating: provenance in `namespaces which` and one Warn line
  in the writers (L1). `MEMORY_NAMESPACE=global` is now simply a valid
  override.
- **#4 mutating `global` from any namespace (AC-20)** — keep. Shared facts
  must be retirable from wherever they turn out to be stale. The precondition
  is AC-23 (M2): a model that cannot see `namespace` in `memory_get` output
  will "enrich" a shared record with project detail without knowing it. Ship
  AC-23 in the same release and have the CLAUDE.md snippet say "do not add
  project-specific detail to a `global` record; store a new one instead".

## Isolation walk-through (checked clean)

- **Search:** `namespace = ANY($N)` in all three WHERE blocks of the RRF
  query (`hybrid_search.go:63` applied at `:85-87`, `:93-96`, `:112-113`) and
  in full-text-only (`:187`). Placeholders hand-checked: args
  `[emb, repo, query, namespaces, kind?, tags?, limit]` ↔ `$4` namespaces.
  Empty/nil `Namespaces` → `ANY(NULL)` → no rows: the store fails closed when
  the service forgets to scope.
- **Candidates / dedup:** `namespace = $4` in both pool and tx variants
  (`store.go:365,371`; `advisory_lock.go:61,67`); args `[emb, repo, limit,
  ns]` ↔ `$1..$4`. `Create` binds `namespace` as `$24` after the three
  tsvector params `$21..$23` (`store.go:121-142`; `advisory_lock.go:148-169`).
- **SUPERSEDE / UPDATE / NOOP targets:** `ExtractionDecision.TargetID` is
  re-validated against the ns-scoped fresh top-5 under the lock
  (`writepath.go:336-352`) and earlier against the ns-scoped candidates in
  extraction (`processor.go:244-265`); an id from another namespace or from
  `global` (when writing to a project namespace) falls back to ADD. Lock,
  candidates and the new row all use the same `ns` (`writepath.go:58-59,
  75, 81, 118`).
- **By-id tools:** get/update/deprecate/feedback go through `getAccessible`
  (`service.go:79-88`; `crud.go:18,48,144`; `lifecycle.go:30`). The `Update`
  whitelist has no `namespace` column (`store.go:194-208`), so a record's
  namespace is immutable and the check-then-update has no TOCTOU.
- **List:** `ListRecords` overwrites any caller-supplied `Namespace`
  (`crud.go:170-171`); `global` excluded by design (AC-17).
- **Cleanup:** `DeleteCandidatesByTTL` sweeps every namespace
  (`store.go:488-494`) — matches AC-14.
- **Hook cards:** shape unchanged (`hook.go:36-41,53-64`); `global` cards
  show their `repo`, which is the only hint a card is shared — another
  argument for M2.
- **`global`-scoped service** (now the fallback): `searchNamespaces`
  returns `[global]` once (`service.go:64-69`); list/get/store behave as a
  normal namespace.
- **Security (spec §7):** namespace values reach SQL only as bound params;
  `cwd` is used for glob matching and as `git`'s working directory, never in
  SQL; `file.go` `Save` writes 0600 via temp + rename under a 0700 dir.
- **Migration 0002:** backfill via `DEFAULT 'work'` then `DROP DEFAULT`
  (AC-2), two composite indexes (AC-3), idempotent and safe under concurrent
  `serve` starts (AC-4).

## Ports / adapter boundaries

- Adapters are still constructed only in `main.go` (`buildPostgresStore`,
  `buildService`, `cmdIngestPR`). `internal/memory` reads no env or files;
  `internal/namespace` imports neither `memory` nor `postgres`; the
  `namespaces` subcommand is dispatched before `config.Load` so it needs no
  DSN (`main.go:35-38`). `resolveNamespace` lives in the composition root.
  One regression: the concrete-type probe in `ingestpr.go:82` (M3).
- `config.Config.Namespace` is mutated by `buildService` and then diverges
  from `Service.namespace` after `WithNamespace`; nothing reads it back
  today (`svc.Cfg()` is used for thresholds only). Prefer passing the
  namespace to `memory.New` as its own argument rather than through the
  shared config pointer.

## Spec ↔ plan ↔ code

Already agreeing: AC-1 (field + persisted), AC-2..AC-4 (by reading),
AC-5..AC-7 (resolver), AC-10..AC-14 wiring, AC-15..AC-21, AC-24 partially
(example YAML and INSTALL.md done; DEPLOY.md and the CLAUDE.md snippet
pending). Disagreeing after decisions 1-3: see the HIGH spec finding, M1
and L2. Plan WI-5 still lists "extract: resolve with `""` when no cwd" as
remaining although `extract.go:143` already does it. The plan's Modules
list does not mention `cmd/claude-memory/namespaces.go` or
`internal/namespace/file.go`.

## The `namespaces` subcommand (e2cfbf3)

`init` refuses to overwrite without `--force`, `add` loads before saving
(so a broken file is reported, not clobbered), `Save` is atomic and 0600,
names are validated, paths are de-duplicated, `which` honours
`MEMORY_NAMESPACE`. `TestInitAddAndRoundTrip` covers the round trip. Gaps:
glob validation (L8), provenance in `which` (L1a), and no `cmd` test for the
subcommand's argument parsing (`NAME=GLOB`, missing args). Documentation in
INSTALL.md is correct for a fresh install; the upgrade path still needs the
ordering fix (M1).

## Known, not re-reported

- `writepath.go:197` writes `superseded_by: ""` into a `UUID` column during
  SUPERSEDE — fails on real Postgres; backlog item 6.
- `plainto_tsquery` AND semantics (backlog item 5, `ed65ec9` not wired).

## Blockers (before the feature is marked done)

1. **HIGH** — run the integration suite on a Docker host, including the
   missing migration (AC-2..4) and lock (AC-18) tests; add a CI job.
2. **HIGH** — re-baseline `01-spec.md` to v0.2 and update `02-plan.md`
   (WI-5 remaining, Risks, Rollout) to decisions 1-3; the current DoD
   contains ACs the code intentionally violates.
3. **MEDIUM (M1)** — rollout order: `namespaces init` with the `work`
   mapping before the new binary's first `serve`; DEPLOY.md upgrade and
   rollback notes.
4. **MEDIUM (M3)** — replace the `ingestpr.go:82` type probe with a
   composition-root scoping port and add the two-repos → two-namespaces test
   (AC-13 is otherwise unverifiable).
5. **MEDIUM (M2)** — `namespace` in tool outputs (AC-23), with server tests.

Non-blocking but recommended in the same change set: M4 (stub the resolver
in `cmd` tests), M5 (eval namespace), M6 (verify `serve` cwd; log the
namespace at startup), L1a-c (provenance, Warn line, re-home SQL), L2
(exact-vs-`/**` specificity).
