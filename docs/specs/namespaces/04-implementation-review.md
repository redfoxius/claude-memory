# Implementation Review — namespaces (iteration 1)

- **Reviewer:** architecture-reviewer (second pass, post-implementation).
- **Target:** branch `claude/task-rb9lio` at `cd98340`, diff `4012902..HEAD`
  (code only), against `01-spec.md` **v0.2**, `02-plan.md`, and
  `03-architecture-review.md` (iteration 0, blockers 1-5). Line numbers are
  at `cd98340`.
- **Owner decisions honoured:** fallback is the shared `global` (no project
  special-cased in code; 0002 backfills legacy rows to `acme` as historic
  data); single-user local DB, so cross-namespace visibility is a relevance
  concern only (LOW, no fail-closed); `namespaces.yaml` is an install step
  via `namespaces init|add|which`; the hook skips migrations.
- **Verified locally (this review ran them):** `go build ./...`,
  `go vet ./...`, `go vet -tags integration ./...`, `go test ./...` and
  `go test -race ./...` — all green, 15 packages `ok`, no failures. The
  `integration`-tag suite still cannot run here (no Docker/pgvector); every
  SQL claim below is from reading the statements and hand-checking
  placeholders.
- **Gate:** **FAIL** — 1 high, 3 medium, 12 low. The isolation code is
  correct on every query path I could find, the four code-level blockers
  from iteration 0 are fixed, and the docs now match the CLI. The gate fails
  on (a) a new HIGH defect on the SUPERSEDE write path that drops the
  namespace, (b) the integration suite still never having executed with the
  migration and lock tests still missing (iteration-0 blocker 1, unchanged),
  and (c) three spec v0.2 ACs whose required tests do not exist.

## Blockers (before the feature is marked done)

1. **HIGH — SUPERSEDE creates a record with an empty namespace.**
   `internal/memory/writepath.go:205-247`: the superseding `record.New(...)`
   never gets `newRec.Namespace = ns` (the ADD branch does, at `:118`).
   `record.New` (`internal/record/record.go:142-163`) leaves `Namespace`
   `""`, `0002` is `NOT NULL` without a `CHECK`, so the INSERT succeeds with
   `namespace = ''`. The old record is deprecated, the new one is invisible
   to every search (`searchNamespaces()` never includes `''`), i.e. the fact
   is lost. Today this is masked only because the same branch still writes
   `superseded_by: ""` into a UUID column (`writepath.go:197`, backlog item
   6) and the transaction rolls back on real Postgres; the moment item 6 is
   fixed, every SUPERSEDE from session/PR extraction or an explicit inline
   decision silently loses its record. Unit tests only cover ADD
   (`internal/memory/namespace_test.go:98-126`). Violates AC-1.
   **Fix:** add `newRec.Namespace = ns` after `writepath.go:214`; extend
   `TestNamespace_StoreWritesOwnNamespace` with an
   `ExtractionDecision{Action: supersede, TargetID: cand}` case asserting
   `created.Namespace == "pet-game"`; and make the service refuse to persist
   `Namespace == ""` (one check before `tx.Create` in both branches, or in
   `record.Validate`) so no future branch can repeat this.

2. **HIGH (iteration-0 blocker 1, still open) — integration suite never
   executed; migration and lock tests still missing.** `.github/workflows/
   ci.yml:19-27` now defines the `integration` job (good), but there is no
   evidence of a green run and the two tests the spec requires do not exist:
   no test applies `0001` alone, inserts rows, runs all migrations twice and
   checks the `acme` backfill, the failing bare INSERT and the two
   `pg_indexes` rows (AC-2..AC-4); no test does concurrent near-duplicate
   writes in one vs two namespaces (AC-18) — `TestConcurrentWrites`
   (`store_integration_test.go:329-410`) uses `testNS` for both goroutines.
   `TestNamespaceIsolation` (`:1252-1322`) is well-shaped and covers hybrid,
   full-text-only, `FindCandidates` and `List`, but has also never run.
   **Fix:** add the two tests, push, and record the green `unit` +
   `integration` run in `02-plan.md` WI-7/WI-10 before sign-off.

3. **MEDIUM — three spec v0.2 ACs have no test although their Verify
   clauses name one.**
   - AC-23 (`memory_search`/`get`/`list`/`store` outputs carry `namespace`;
     spec calls it a release blocker): fields exist
     (`internal/mcpserver/types.go:25,76,143`, `handlers.go:48,126`,
     `convert.go:19`) but `server_test.go` has no assertion containing
     `namespace` at all (`grep -n namespace internal/mcpserver/*_test.go` is
     empty).
   - AC-13 ("two repos → two writers, no cross-over"):
     `TestIngestOneRepoUsesScopedWriter` (`cmd/claude-memory/ingestpr_test.go:255-273`)
     checks one repo and only that `scope` saw the path; with
     `emptyHaikuRunner` no record is produced, so it cannot show that records
     reach the scoped writer and not `unscoped`.
   - AC-11/AC-12/AC-29 (per-subcommand namespace tests with a recording
     stub): `main_test.go:10-13` stubs `resolveNamespace` to a constant for
     isolation, but no test asserts `SearchOptions.Namespaces ==
     ["t-<cwd>", "global"]` for the hook, nor the transcript-`cwd` /
     `""` cases for `extract --run`.
   **Fix:** server tests asserting `namespace` on all four tool outputs plus
   `namespace="other"` → tool error; a two-repo ingest test with a haiku
   runner that returns one draft and a fake `scope` returning a distinct
   `mock.Service` per path, asserting each mock's `storedRecords`; a hook
   test capturing `opts.Namespaces` in `fakeHookStore.Search`
   (`hook_test.go:26-28`) under a recording `resolveNamespace` stub.

## Findings (severity ranked)

| Sev | file:line | Rule / AC | Claim | Recommendation |
|---|---|---|---|---|
| HIGH | `internal/memory/writepath.go:205-247` (vs `:118`) | AC-1 | SUPERSEDE branch never sets `newRec.Namespace`; superseding record is inserted with `namespace = ''` and becomes unreachable. Masked today only by the pre-existing `superseded_by: ""` UUID failure at `:197`. | Blocker 1 above. |
| HIGH | `internal/postgres/store_integration_test.go` (no migration/lock test); `.github/workflows/ci.yml:19-27` | AC-2..AC-4, AC-18, AC-30 | Namespace SQL and 0002 still verified only by `go vet` and reading; required tests absent; no observed green run. | Blocker 2 above. |
| MEDIUM | `internal/mcpserver/server_test.go` (no `namespace` assertions); `cmd/claude-memory/ingestpr_test.go:255-273`; `cmd/claude-memory/main_test.go:10-13` | AC-23, AC-13, AC-11, AC-12, AC-29 | Code paths exist; the tests the spec's Verify clauses demand do not. | Blocker 3 above. |
| MEDIUM | `cmd/claude-memory/extract.go:144-146` | AC-28 | When the transcript cannot be parsed or has no `cwd`, the namespace is resolved with `""` and **no** fallback Warn is emitted (`warnIfFallback` is only in the `cwd != ""` branch at `:141-142`). Spec: "WHEN a background writer resolves through the built-in fallback, it shall log exactly one Warn line". | `ns := resolveNamespace(dir); warnIfFallback(dir, ns)` for both branches with `dir = tr.Cwd` or `""`; the Warn should say "transcript has no cwd" in the empty case. |
| MEDIUM | `cmd/claude-memory/namespace.go:23-36` + `:40-47` + `:65-69`; `cmd/claude-memory/main.go:115-118` + `hook.go:83` | AC-8 "log one warning", AC-28 "exactly one Warn", MVP AC-31 | `warnIfFallback` → `explainNamespace` → `loadNamespaces` re-reads the YAML and, when the file is broken, logs a **second** "namespaces.yaml unusable" Warn (`:43`) after `resolveNamespace` already logged one (`:27`), then a third line for the fallback (`:67`): three Warns per repo in `ingest-pr`, not one. The hook resolves twice per prompt (`buildService` from `os.Getwd()`, then from payload `cwd`) and with a broken file writes two Warn lines to stderr on every prompt (slog default handler → stderr), which the hook wrapper promises not to do (`integration/hooks/user-prompt-submit.sh:9-11`). | Load the config once per process: `resolveNamespace` returns `(ns, why)` (or a small struct) and `warnIfFallback(dir, why)` takes the provenance instead of re-resolving; `buildService(ctx, cfg, migrate, ns string)` so the hook passes `""` and skips the first resolve (iteration-0 L5). For the hook specifically, log resolution problems at Debug. |
| LOW | `cmd/claude-memory/main.go:240-241` | AC-14 | `eval-retrieval` is scoped to `eval` **unconditionally**; spec says "unless `MEMORY_NAMESPACE` is set". The env override that `buildService` already resolved into `cfg.Namespace` is thrown away. | `if os.Getenv(config.NamespaceEnv) == "" { svc = svc.WithNamespace("eval") }`. |
| LOW | `cmd/claude-memory/serve.go:22` | AC-28 | `serve` logs `namespace` but not its provenance ("shall log its resolved namespace and provenance at startup"). | Have `buildService` keep the `why` from `explainNamespace(wd)` and log `Str("provenance", why)`. |
| LOW | `integration/INSTALL.md:222-241`, `:74-75` | AC-24 | "Using namespaces" has no re-home SQL and no pointer to `DEPLOY.md`'s; the `which` examples show `-> acme` / `-> global (the default)` while the command prints `acme\t(rule ~/work/acme/**)` and `global\t(default)`. Everything else in 2a and the table matches the CLI. | Add one row "move stray `global` records to a project → see re-home SQL in `DEPLOY.md`" (or inline the two statements); show the real two-column output. |
| LOW | `internal/memory/crud.go:153-155` | AC-19 completeness (iteration-0 L4, still open) | `DeprecateRequest.SupersededBy` is written without `getAccessible`; a project can link its record to another namespace's id. Relevance-only harm, but the spec's AC-19 list covers `memory_deprecate`. | `if req.SupersededBy != nil { if _, err := s.getAccessible(ctx, *req.SupersededBy); err != nil { return nil, err } }`. |
| LOW | `internal/memory/service.go:36-40,53-57`; `internal/record/record.go` (no namespace check in `Validate`) | AC-1 (iteration-0 L3, still open) | `WithNamespace("")`/`New` accept an empty or invalid name; `NOT NULL` does not reject `''`. The resolver never returns `""`, so only a programming error reaches it — which is exactly what blocker 1 is. | Validate in `New`/`WithNamespace` with the name rule (move `ValidName` to `record` so `record` owns it), or have the store's `Create` refuse `Namespace == ""`. |
| LOW | `internal/namespace/namespace.go:20` vs `internal/record/record.go:13` vs `internal/memory/service.go:26`; `record.go:11-12` comment; `docs/specs/README.md:17-19` | single source of truth; stale text | `memory.DefaultNamespace` now references `record.GlobalNamespace` (good), but `namespace.Fallback` is still an independent literal `"global"`. The `record.go:12` comment ("Nothing is written to it automatically") and the README backlog entry still state the v0.1 rule the owner reversed. | `const Fallback = record.GlobalNamespace` (the package stays free of `memory`/`postgres`); fix the two comments. |
| LOW | `internal/namespace/file.go:84-103`; `cmd/claude-memory/namespaces.go:40-50` | spec §8 (bad globs), plan WI-9 | `init`/`add` validate names but not globs; an unclosed `[` is accepted and then silently never matches (`namespace.go:150` returns `false` on `ErrBadPattern`). `--default` is validated after the rules (`file.go:65`). | In `add`, `filepath.Match(seg, "")` per non-`**` segment and reject `ErrBadPattern`; validate `def` first. |
| LOW | `cmd/claude-memory/ingestpr.go:87-90` before `:92-99` | noise | `scope(repoPath)` (and thus the fallback Warn) runs before the unsupported-provider check, so a skipped GitHub repo still produces a "writing to global" Warn. | Move the `scope` call after the provider check (it is only needed for the write loop at `:144`). |
| LOW | `cmd/claude-memory/main.go:206-213` | plan WI-5 optional | `svc.(*memory.Service)` type assertion survives in the composition root (allowed there, but pointless: `s` from `buildService` at `:195` is already concrete). | Build `scope` inside the `!*dryRun` branch from `s`. |
| LOW | `cmd/claude-memory/main_test.go:10-13` | AC-29 | Only `resolveNamespace` is stubbed; `explainNamespace`/`warnIfFallback` read the developer's real `~/.config/claude-memory/namespaces.yaml`. No current test reaches them, but the first `extract --run` test will. | Make `explainNamespace` a package variable too and stub both in `TestMain`. |
| LOW | `internal/postgres/ftsquery.go`, `ftsquery_test.go` | scope hygiene | `buildORTSQuery`/`minFTSRank` (commit `ed65ec9`, backlog item 5) ship in this range unused; vet/tests pass. Harmless, but the diff for "namespaces" carries unrelated dead code. | Note only; leave for item 5 or drop from this branch. |
| INFO | `internal/postgres/store.go:28-30,83-100`; `0001_init.sql:7-10` | migration mechanics (iteration-0 L9) | `schema_migrations` still never written; both files re-run on every start, relying on per-statement idempotency. Fine for 0001/0002 (see §Migration 0002 below). | When a third migration arrives, record versions and stop re-running. |

## Status of iteration-0 blockers

| # | Iteration-0 blocker | Status | Evidence |
|---|---|---|---|
| 1 | HIGH — run integration suite incl. migration (AC-2..4) and lock (AC-18) tests; add CI job | **OPEN** | CI job added (`ci.yml:19-27`); migration and lock tests absent; no green run observed. Carried as blocker 2. |
| 2 | HIGH — re-baseline spec to v0.2 and plan | **RESOLVED** | `01-spec.md` v0.2 (changes §0, AC-8 rewritten, AC-9 withdrawn, AC-22 reworded, AC-25..AC-30 added, §13 all resolved); `02-plan.md` owner decisions, WI-5 remaining, Risks, Rollout rewritten. Two stale sentences remain (`record.go:12`, `docs/specs/README.md:17-19`), LOW. |
| 3 | MEDIUM (M1) — rollout order; DEPLOY.md upgrade/rollback | **RESOLVED** | `DEPLOY.md:196-208`: map `acme` **before** starting Claude Code on the new binary, run `claude-memory cleanup` once (hook skips migrations), rollback `SET DEFAULT 'global'`, re-home SQL. Plan Rollout steps 4-5 match. |
| 4 | MEDIUM (M3) — replace ingest-pr type probe with injected scoping; two-repo test | **RESOLVED in code, test incomplete** | `scopeFunc` type `ingestpr.go:22`, applied `:87-90`; built in composition root `main.go:206-213`; `ingestOneRepo` has no type assertion. Test covers one repo and only the path argument (blocker 3). |
| 5 | MEDIUM (M2) — `namespace` in tool outputs with server tests | **RESOLVED in code, tests missing** | `types.go:25` (search item), `:76` (store output), `:143` (record output); `handlers.go:48,126`; `convert.go:19`; `StoreResponse.Namespace` set on both return paths `writepath.go:304,312`. No server test asserts it (blocker 3). |

Non-blocking recommendations from iteration 0: M4 (stub resolver) — done
for isolation only; M5 (eval namespace) — done, deviates from AC-14 on the
env override; M6 (`serve` log) — namespace yes, provenance no; L1a-c
(provenance in `which`, fallback Warn, re-home SQL) — done
(`namespaces.go:76-77`, `namespace.go:65-69`, `DEPLOY.md:202-208`); L2
(exact beats `/**`) — done (`namespace.go:123-128`, `TestExplainAndTieBreak`
`namespace_test.go:126-143`); L3, L4, L5, L8 — still open (table above); L6
— half done; L10 — done.

## Isolation re-check (every query path, including new code)

Checked clean unless noted.

- **Search, hybrid RRF** (`internal/postgres/hybrid_search.go`): the
  namespace predicate is appended to `whereClause` at `:63-65` and that
  clause is substituted into all three WHERE blocks (`:87` vector CTE, `:96`
  fts CTE, `:114` final join). Args are `[emb, repo, query, namespaces,
  kind?, tags?, limit]`; `baseArgCount` starts at 4, so `ANY($4)` is the
  `[]string` (pgx encodes as `text[]`); `LIMIT $N` uses the final count
  (`:117-120`). `r.namespace` is selected and scanned (`:103`, `:141`).
- **Search, full-text-only** (`:190-192`, `:220`): `namespace = ANY($3)`
  after `[query, repo]`; selected/scanned (`:212`, `:245`). The degrade path
  (`store.go:321-329`) reuses the same `opts`, so a hybrid failure cannot
  widen the namespace set.
- **Empty/nil `Namespaces`** → `ANY(NULL)` → no rows: the store fails
  closed if the service forgets to scope (plan constraint "an empty set
  matches nothing"). Every integration-test `Search` passes `Namespaces`
  (grep for `SearchOptions{` without it is empty).
- **Service search set** (`internal/memory/service.go:64-69`, applied
  `:122`): own + `global`, or `[global]` once for a `global`-scoped
  service. Tested (`namespace_test.go:26-43`).
- **Dedup candidates**: pool `store.go:365` (`namespace = $4`, args
  `embeddingVec, repo, limit, namespace` ↔ `$1..$4`) and tx
  `advisory_lock.go:61,67`. Service passes `s.namespace` in
  `FindCandidates` (`service.go:145`) and therefore in
  `FindCandidatesForText` (`service.go:165-176`), which is the extraction
  port (`internal/extraction/processor.go:54-63`, called at `:216`). Write
  path passes the target `ns` (`writepath.go:81`), so an explicit
  `namespace="global"` store dedups against `global` (tested
  `namespace_test.go:112-126`).
- **TargetID re-validation**: `ExtractionDecision.TargetID` must be in the
  ns-scoped fresh top-5 (`writepath.go:338-356`), else ADD. A foreign id or
  a `global` id (when writing to a project namespace) can never be
  UPDATEd/SUPERSEDEd/NOOPed. Lock, candidates and the ADD row share `ns`
  (`:75`, `:81`, `:118`). **Exception: the SUPERSEDE row (`:205-247`) —
  blocker 1.**
- **Advisory lock** (`advisory_lock.go:25-28`): key
  `namespace:repo:<repo:md5[:8]>` — namespace included (AC-18); `repo`
  appears twice (`computeLockKey` `writepath.go:390-403` already embeds it),
  redundant but harmless (iteration-0 L5-bis).
- **By-id tools**: `GetRecord` (`crud.go:18`), `UpdateRecord` (`:48`),
  `DeprecateRecord` (`:144-146`), `Feedback` (`lifecycle.go:30`) all go
  through `getAccessible` (`service.go:79-88`): own namespace or `global`,
  else `ErrNotFound` — the same error as an unknown id (AC-19, AC-20).
  Tested (`namespace_test.go:57-96`). `Update`'s column whitelist still has
  no `namespace`, so a record's namespace is immutable through the service
  and the check-then-update has no TOCTOU. Gap: `SupersededBy` (LOW, table).
- **List** (`crud.go:169-171`): caller-supplied `Namespace` is overwritten
  with the service's own; `global` excluded (AC-17). Store applies it
  (`store.go:405-409`). Tested (`namespace_test.go:45-55`).
- **MCP layer** (`internal/mcpserver/service.go:22-45`): every handler goes
  through the seven service methods; no handler touches the store. The
  `namespace` input is passed straight into `StoreRequest`
  (`handlers.go:92`) and validated in `validate()`
  (`writepath.go:444-446`) **before** scrubbing/embedding/any write
  (`:33`), so an invalid value writes nothing (AC-21). Tested
  (`namespace_test.go:128-134`).
- **Automatic writers never set `StoreRequest.Namespace`** (AC-22):
  `cmd/claude-memory/seed.go:117-124`, `internal/evalset/run.go:90-98,
  155-163`, `internal/extraction/schema.go:119-131` — none set it; the only
  setter is `mcpserver/handlers.go:92`.
- **Composition root** (`cmd/claude-memory/main.go:106-118`): `serve`,
  `seed`, `eval-retrieval` scope from `os.Getwd()`; `hook` re-scopes from
  payload `cwd` (`hook.go:83`); `extract --run` from transcript `cwd` or
  `""` (`extract.go:138-146`); `ingest-pr` per repo through `scopeFunc`
  (`main.go:206-213`, `ingestpr.go:87-90`). `internal/memory` reads no env
  or files; `internal/namespace` imports neither `memory` nor `postgres`;
  `namespaces` is dispatched before `config.Load` (`main.go:34-37`).
- **Cleanup**: `DeleteCandidatesByTTL` sweeps all namespaces
  (`cleanup.go:30`) — AC-14 by design.
- **New resolver code** (`internal/namespace/namespace.go`): `Load` treats a
  missing file as empty (`:55-57`), rejects invalid names (`:63-70`);
  `Explain` cleans `dir` (`""` → `.`), picks the highest `specificity`
  with strict `>` so file order breaks ties (`:91-111`); exact path scores
  `len+2`, `<path>/**` scores `len+1` (`:123-128`) — AC-6 holds for the
  nested case (`TestExplainAndTieBreak`). `**` matches zero or more
  segments (`:136-157`). `ForDir` rejects an invalid override (`:162-170`).
  Symlinks are not resolved (documented limitation). Namespace values reach
  SQL only as bound parameters; `cwd` is used only for glob matching and as
  `git`'s working directory.

## Spec v0.2 ACs vs code

| AC | Code | Test | Verdict |
|---|---|---|---|
| AC-1 | field + persisted/returned everywhere (`store.go:133,141,166,285,441`; `advisory_lock.go:160`) | round-trip integration (unrun) | **Not met**: SUPERSEDE row has `''` (blocker 1); no non-empty guard |
| AC-2 | `0002_namespaces.sql:4,6` | none | by reading only (blocker 2) |
| AC-3 | `0002:8,10` | none | by reading only |
| AC-4 | `IF NOT EXISTS` ×3, `DROP DEFAULT` no-op | none | by reading only |
| AC-5 | `cmd/namespace.go:23-36`, `namespace.go:162-170` | `TestOverrideWins`, `TestResolve`, `TestMissingFileFallsBack` | met |
| AC-6 | `namespace.go:100,123-128` | `TestResolve`, `TestExplainAndTieBreak` | met |
| AC-7 | `namespace.go:55-57` | `TestMissingFileFallsBack` | met |
| AC-8 | `cmd/namespace.go:26-34` | `TestInvalidNamesRejected` (resolver only); no cmd test for broken file → Warn → `global` | met in code; Warn count is 2-3 not 1 (MEDIUM); untested at cmd layer |
| AC-10 | `main.go:115-118` | manual only (plan WI-6) | met in code; cwd assumption still unverified on the laptop |
| AC-11 | `hook.go:83` | none | code yes, **untested** (blocker 3) |
| AC-12 | `extract.go:138-146` | none | code yes, **untested**; Warn missing for `""` (MEDIUM) |
| AC-13 | `ingestpr.go:22,87-90`; `main.go:206-213` | `TestIngestOneRepoUsesScopedWriter` (one repo, path only) | code yes, **test incomplete** (blocker 3) |
| AC-14 | seed via `buildService`; eval `main.go:241`; cleanup all | none | eval ignores `MEMORY_NAMESPACE` (LOW) |
| AC-15 | `service.go:64-69,122`; SQL `ANY($N)` | `TestNamespace_SearchCoversOwnAndGlobal`; `TestNamespaceIsolation` (unrun) | met (SQL unexecuted) |
| AC-16 | `writepath.go:75,81`; `store.go:365`; `advisory_lock.go:61` | `TestNamespace_StoreWritesOwnNamespace`/`ExplicitGlobal`; isolation test (unrun) | met |
| AC-17 | `crud.go:169-171`; `store.go:405-409` | `TestNamespace_ListIsConfinedToOwnNamespace` | met |
| AC-18 | `advisory_lock.go:27` | **none** | code yes, untested (blocker 2) |
| AC-19 | `getAccessible` in crud/lifecycle | `TestNamespace_IDLookupsHideOtherNamespaces` | met; `SupersededBy` gap (LOW) |
| AC-20 | `service.go:73-75` | same test | met |
| AC-21 | `writepath.go:58-61,444-446`; `handlers.go:92` | `TestNamespace_StoreRejectsForeignNamespace` | met |
| AC-22 | no automatic setter (grep) | grep only | met |
| AC-23 | `types.go:25,76,143`; `handlers.go:48,126`; `convert.go:19` | **none** | code yes, **untested** (blocker 3) |
| AC-24 | example YAML; INSTALL 2a + Using; DEPLOY; snippet | n/a | met except INSTALL re-home SQL / `which` output (LOW) |
| AC-25 | `file.go:48-70`; `namespaces.go:35-54` | `TestInitAddAndRoundTrip` | met; glob validation missing (LOW) |
| AC-26 | `file.go:73-103`; `namespaces.go:56-64` | same | met |
| AC-27 | `cmd/namespace.go:51-59`; `namespaces.go:66-78` | `TestExplainAndTieBreak` (rule/default/fallback); no `env` case, no cmd test | met in code; `env` provenance untested |
| AC-28 | `cmd/namespace.go:65-69`; `extract.go:141-142`; `main.go:210`; `serve.go:22` | none | **partially met**: no Warn for `cwd=""`; 2-3 Warns on broken file; `serve` lacks provenance |
| AC-29 | `main_test.go:10-13` | constant stub only | **partially met**: no per-subcommand assertions |
| AC-30 | `ci.yml` | no observed run | **unverified** |

## Item 4 — hook skips migrations: failure mode on an old schema

- **New binary, schema without 0002, prompt hook:** `cmdHook` →
  `buildService(…, false)` (`main.go:164-165`) → `postgres.Open`
  (`store.go:57-75`, ping only) → `svc.Search` → `searchHybridRRF` fails
  with `column r.namespace does not exist` → `store.go:327` logs a Warn and
  degrades to `searchFullTextOnly`, which fails on the same column →
  `hookCmd` logs at Debug and returns `nil` (`hook.go:94-98`) → exit 0, no
  cards. No write, no corruption, no error surfaced to the user; the only
  symptom is "memory is silent" plus one Warn line on stderr per prompt
  until migrated. Matches spec §8 and `DEPLOY.md:198`.
- **How the window closes:** any of `serve` (Claude Code start), `seed`,
  `cleanup`, `ingest-pr`, or `extract --run` (`buildService(…, true)` at
  `extract.go:130` — the SessionEnd hook's detached child migrates, so in
  practice the first session end after the upgrade also fixes it).
  `DEPLOY.md:198` lists these correctly; `claude-memory cleanup` is the
  recommended explicit step.
- **Concurrent migration:** `ADD COLUMN` takes `ACCESS EXCLUSIVE` briefly;
  a hook query in flight waits or hits its 800 ms budget → silent. Fine.
- **Old binary on a migrated schema (rollback):** SELECTs work (column is
  simply unread); INSERTs fail (`NOT NULL`, no default) until
  `ALTER COLUMN namespace SET DEFAULT 'global'` — documented
  (`DEPLOY.md:200`). Records inserted under that default land in `global`,
  consistent with decision 1.
- **Residual risk:** none beyond "silent until migrated". The Warn at
  `store.go:327` is emitted per prompt during the window; acceptable, but
  see the MEDIUM stderr-noise finding for the resolver Warns, which persist
  after migration when the YAML is broken.

## Item 5 — migration 0002 safety

- **Statement splitting** (`store.go:28-30,86-91`): `migrationSQL = 0001 +
  ";\n" + 0002`, split on `;`, blanks skipped. `0001` ends with `;`, so the
  join yields one empty statement that `TrimSpace`/`continue` drops. `0002`
  has four statements; its only string literal is `'acme'` and neither
  the literal nor the leading `--` comments contain `;`. The last statement
  has no trailing `;` — fine under split. The comments are prepended to the
  first `ALTER TABLE`, which Postgres accepts.
- **First run on an MVP database:** `ADD COLUMN IF NOT EXISTS namespace TEXT
  NOT NULL DEFAULT 'acme'` backfills every existing row (AC-2 historic
  data); `ALTER COLUMN namespace DROP DEFAULT` removes the default so a
  bare INSERT fails (AC-2 second clause); two `CREATE INDEX IF NOT EXISTS`
  (AC-3). Fresh database: same statements on an empty table.
- **Idempotent re-run** (AC-4): `ADD COLUMN IF NOT EXISTS` → NOTICE, no
  backfill, no default re-added; `DROP DEFAULT` on a column with no default
  is a no-op without error; both `CREATE INDEX IF NOT EXISTS` no-op. Row
  count, namespaces and indexes unchanged. Correct by reading; **no test
  executes it** (blocker 2).
- **Partial failure:** if the process dies between `ADD COLUMN` and `DROP
  DEFAULT`, the next start completes the drop; in the gap only an *old*
  binary's INSERT could pick up `'acme'` (new binaries always bind
  `$24`). Acceptable.
- **Concurrency:** two `serve` processes starting together serialize on the
  `ALTER TABLE` lock; a racing `CREATE INDEX IF NOT EXISTS` can still raise
  "already exists", which `runMigrations` swallows (`store.go:92-97`).
- **Default drop vs rollback:** correct trade-off per the plan; rollback
  requires re-adding a default (`'global'`, documented).
- **Gap worth noting:** `NOT NULL` accepts `''`. Blocker 1 shows why that
  matters; a service-level non-empty guard is the right place (a `CHECK`
  would be a third migration and is out of scope).

## Item 7 — docs vs CLI behaviour

- `integration/INSTALL.md:43-79` (2a): `namespaces init acme='~/work/
  acme/**' …` matches `namespaces.go:35-54` (`NAME=GLOB`, `~` expanded
  by `namespace.go:114-119`, quotes keep the shell from expanding); "no
  database, DSN or Ollama" is true (`main.go:34-37` dispatches before
  `config.LoadFromFile`); `--default NAME` and the `global` default match
  `file.go:60-62`; the upgrade note (map `acme`) is correct. The `which`
  examples at `:74-75` omit the `\t(<provenance>)` column — LOW.
- `INSTALL.md:222-241` (Using namespaces): the resolution description
  (MCP = start dir, hook = prompt dir, extraction = session dir, ingest-pr =
  repo path), the table (`which [DIR]`, `add NAME 'GLOB' …`,
  `MEMORY_NAMESPACE` in `.claude/settings.json`, `namespace: "global"`,
  `init --force`), "most specific wins", and "work lands in `global`
  instead of failing" all match the code. Missing: re-home SQL or a pointer
  (AC-24) — LOW.
- `DEPLOY.md:196-208`: migrating subcommands list is accurate (`extract`
  means `extract --run`; the SessionEnd entry point itself opens no DB);
  order (map before starting Claude Code; `cleanup` once), rollback
  `SET DEFAULT 'global'`, re-home SQL keyed on `repo` — all correct and
  consistent with `02-plan.md` Rollout 2-9.
- `integration/claude-md-snippet.md:59-67`: resolution is automatic,
  search = current + `global`, `namespace: "global"` only for stack-generic
  facts, "do not enrich `global` records with project-specific details" —
  matches AC-24 and the `StoreInput.Namespace` schema hint
  (`types.go:53`).
- `integration/namespaces.example.yaml:4-19`: order, `**`/`~` semantics,
  fallback `global`, the `acme` backfill note — match `namespace.go`.
  Its `pet-game` rule shows the exact-plus-`/**` idiom the spec recommends.
- Spec §3 "Install" says `which` prints `acme\t(rule ~/work/acme/**)`
  — the code prints the glob as written in the file (`namespace.go:101`
  keeps `g`, not the expanded `eg`), so this is exact.

## Item 6 — regressions

Ran in this review: `go build ./...` OK; `go vet ./...` OK;
`go vet -tags integration ./...` OK; `go test ./...` all 15 packages `ok`;
`go test -race ./...` all `ok` (cmd 1.3 s, mcpserver 1.7 s, ollama 3.1 s).
No regressions in the MVP suites (hook, extraction, mcpserver, memory write
path, prcursor/prsource, scrub, transcript). The integration-tag suite
compiles but was not executed (no Docker here).

## Known, not re-reported

- `writepath.go:197` writes `superseded_by: ""` into a UUID column —
  backlog item 6; it is what currently hides blocker 1.
- `plainto_tsquery` AND semantics; `ftsquery.go` is the unwired item-5
  prototype.
- Spec §4 assumption that Claude Code starts the user-scope stdio server in
  the project directory (plan WI-6 manual check) — still unverified; the
  `serve` startup log now makes a wrong cwd visible.

## Summary for the owner

Code-level isolation is sound and the four code blockers from iteration 0
are fixed; the docs are now accurate and in the right order. Three things
stand between this and done: a one-line SUPERSEDE namespace bug (with a
guard so it cannot recur), the integration run with the two missing SQL
tests, and the server/cmd tests the spec's own Verify clauses require. The
remaining MEDIUM/LOW items (Warn hygiene, `eval` env override, `serve`
provenance, INSTALL re-home pointer, `SupersededBy` check, glob validation)
are each a few lines and can ride in the same change set.
