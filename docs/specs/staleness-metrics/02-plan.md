# Claude Memory — Staleness Check & Usage Metrics

**Status:** PR A (staleness) implemented and reviewed
(`04-pr-a-review.md`); its blockers are fixed. PR B (events + stats) not
started. Plan revised for spec v0.2 after `03-architecture-review.md`.

**PR A implementation status:** WI-1..6 and WI-13a DONE (unit-tested; the SQL
changes are covered by integration tests that pass on a local Postgres+pgvector). WI-15 not
built (optional). Still manual/open: **WI-0** latency baseline (build
`40a7fb7` for the "before" numbers), **WI-14** re-measure, and the
**AC-11** check `git merge-base --is-ancestor <MergeCommit> origin/main`
on a real completed squash PR (result decides whether PR records can ever
be checked). Known deviation from AC-12: a write-path UPDATE whose request
has no `files` never re-baselines (the candidate carries no files and the
baseline is computed before the transaction); `memory_update` does.

## Spec
- `docs/specs/staleness-metrics/01-spec.md` (SPEC-2026-10-01-staleness-metrics,
  v0.2, AC-1..AC-33; AC-16 withdrawn; AC-33 optional). All §13 questions
  are resolved; see spec §0.1 for the v0.2 change log.

## Delivery: two PRs

| PR | Scope | Work Items | ACs |
|---|---|---|---|
| **A — staleness** | detector, cache, hook/serve surface, write-time baseline, extraction repo fix | WI-0..WI-6, WI-14, WI-13 (staleness half) | AC-1..AC-12, AC-25, AC-30 (staleness SQL), AC-31 (half), AC-32 |
| **B — events + stats** | migration 0003, service events, spool, `stats`, `cleanup` | WI-7..WI-12, WI-13 (events half), WI-14 re-check | AC-13..AC-15, AC-17..AC-24, AC-26..AC-29, AC-30 (events SQL), AC-31 (half) |
| optional | PR changed paths → extraction | WI-15 | AC-33 |

B does not depend on A's git work and can be developed in parallel; the
only touch points are `hook.go` (B adds `hookDeps.Events` and the spool
append after A's card rendering) and the `card_injected` stale fields (B
reads the verdict A computes; if B lands first, it writes `stale = null`).
A is what the owner feels; ship it first.

## Context
- Namespaces are shipped (`7e54d2f`); review baseline `40a7fb7`. Every
  record has `namespace`; `Service.WithNamespace`; migrations 0001, 0002
  embedded in `internal/postgres/store.go` and concatenated into
  `migrationSQL`.
- The hook opens Postgres with `postgres.Open` (no migrations), runs
  `deriveRepo` (`git rev-parse --show-toplevel`) then `svc.Search`, renders
  ≤ 3 cards (`renderAdditionalContext`, title/repo/id). Measured 215–343 ms
  **before** migrations were skipped (backlog item 7); no measurement
  exists on the current binary — WI-0 takes it. MVP AC-30 target 300 ms
  p95.
- `cmdHook` (`main.go:160-174`) builds the service; `hookCmd` parses stdin
  (`hook.go:70-77`). `hook_test.go:81-123` swaps `os.Stdin`, so a new
  `session_id` payload field fits the harness unchanged.
- `records.files` and `records.commit_sha` exist, but:
  - `SearchRecord` and the hybrid/full-text queries do not select them.
  - Only `memory_store` (inline) can set `commit_sha`; extraction
    (`ConvertDraftToStoreRequest`) and PR ingest never do.
  - `postgres.Store.Update` does not whitelist `commit_sha`.
  - `records.*_at` are `TIMESTAMP` (no zone); `events.at` will be
    `TIMESTAMPTZ` — never compared (spec AC-27).
- Session extraction's `repo` comes from `transcript.inferRepo`
  (`basename(cwd)`, `parse.go:301-310`), not the git toplevel — fixed by
  AC-32 in WI-6.
- PR extraction gets no changed paths (`PRInput` has title/description/URL
  only; `prsource.PR.DiffSummary` is never set) — hence AC-11's low
  coverage and optional WI-15.
- `DeleteCandidatesByTTL` returns only a count. `cleanupCmd` uses the store
  directly through the consumer-side `ttlDeleter` interface.
- `stateDir()` (`cmd/claude-memory/extract.go:172-175`) =
  `$HOME/.local/state/claude-memory`; tests set `HOME` with `t.Setenv`.
- `extract --run` and `ingest-pr` already call `buildService(…, migrate=true)`.
- CI (`.github/workflows/ci.yml`): `unit` job, and `integration` job running
  `go test -tags integration ./internal/postgres/...`. Integration tests
  cannot run in the dev container (no Docker).

## Scope
- In scope: AC-1..AC-15, AC-17..AC-32; AC-33 optional (WI-15).
- Withdrawn: AC-16 (scan test); the append-only rule is a spec §4
  constraint checked in review.
- Out of scope (spec §12): ranking/demoting stale records, `memory_list`
  staleness, `repo="*"` records, read-time uncommitted changes, rename
  following, NOOP re-baselining, search/read events, per-repo metrics,
  `stats --namespace`, configurable retention, backfilling `commit_sha`.

## Modules Touched
PR A:
- `internal/memory/` — ports (`Checkout{Dir,Repo}`, `CodeHistory{Resolve,
  Head, Changed, Dirty}`, `StaleHint`), `NormalizeFiles`, `staleness.go`,
  stamping in the write path and `UpdateRecord`; `mock/` regenerated
- `internal/gitlog/` (new) — exec adapter + cache decorator (map,
  per-session file, tri-state)
- `internal/postgres/` — search selects `files`/`commit_sha`; `commit_sha`
  update column (+ integration tests)
- `internal/mcpserver/` — `stale_hint`/`stale_commits`,
  `memory_update.commit_sha`, `StaleHint` in the consumer interface
- `internal/prsource/`, `internal/azuredevops/` — `PR.MergeCommit`
- `internal/extraction/` — repo from the checkout (AC-32); `CommitSHA` for PRs
- `internal/config/` — `MEMORY_STALE_TIMEOUT_HOOK`, `MEMORY_STALE_TIMEOUT`
- `cmd/claude-memory/` — `main.go` (`hookDeps`, `buildCodeHistory`),
  `hook.go`, `serve.go`, `extract.go`, `ingestpr.go`

PR B:
- `internal/memory/` — `EventSink`, `Event` (+ `Validate`),
  `ErrEventRejected`, `events.go`; events in `writepath.go`,
  `lifecycle.go`, `crud.go`
- `internal/eventspool/` (new) — JSONL spool `Sink` and `Drain`
- `internal/postgres/` — migration 0003, `AppendEvents`, cleanup CTE,
  `PruneEvents`, stats count queries
- `cmd/claude-memory/` — `stats.go` (new), `cleanup.go`, `hook.go`
  (events), `serve.go`, `extract.go`, `ingestpr.go` (drains), `main.go`
- Docs (both PRs): `DEPLOY.md`, `integration/INSTALL.md`,
  `integration/claude-md-snippet.md`, `docs/specs/README.md`

## Architectural Constraints
- `internal/memory` never runs `git`, reads files or env. It sees git only
  through `CodeHistory` and events only through `EventSink`; path
  normalization is pure code in `memory`.
- Adapters (`gitlog`, `eventspool`, `postgres`) are constructed only in
  `cmd/claude-memory/main.go`. The hook receives a
  `hookDeps{History func(sessionID) memory.CodeHistory; Events
  memory.EventSink}` built there; `main.go` never parses stdin, `hook.go`
  never constructs adapters.
- `Checkout` carries no `HEAD`. One-shot processes pin the `H` they
  resolved (`WithPinnedHead`); `serve` reads it per call.
- `stats` and `cleanup` build only the Postgres store (no Ollama).
- The hook never writes events to Postgres, never migrates, never runs
  `git status`.
- Events: ids/enums/numbers only; namespace always set; appended after
  commit, best-effort; failures never surface to tool callers; table is
  append-only (only the retention prune deletes).
- Staleness is best-effort under a deadline; every failure is "unchecked".
- New SQL uses bound parameters only; no stats query compares
  `events.at` with `records.*_at`.

## Design Decisions
1. **Git behind a port; tree compare as the detector.** `memory.CodeHistory
   {Resolve, Head, Changed, Dirty}`; `gitlog.Exec` implements it with
   `exec.CommandContext`: `rev-parse --show-toplevel HEAD` (Resolve),
   `rev-parse HEAD` (Head), `diff --quiet <sha> <H> -- <files>` then, only
   on exit 1, `rev-list --count --max-count=100 <sha>..<H> -- <files>`
   (Changed), `status --porcelain --untracked-files=all -- <files>`
   (Dirty). `gitlog.Cached(inner, store)` wraps `Changed` only; `store` is
   a map (serve) or a per-session JSON file (hook). Caching is an adapter
   concern; `memory` unit tests use a plain fake. (Spec §13 #1, #7.)
2. **Honest baselines.** Stamp only when `Dirty` is false; otherwise
   `NULL`/unchanged. `NULL` is "unchecked", a pre-change sha is a false ⚠.
   (Spec §13 #5.)
3. **Budget-aware hook deadline.** `min(now + 50 ms, hookStart + 300 ms −
   20 ms)`; tri-state negative cache; `WaitDelay = 10 ms`. (Spec §13 #8.)
4. **Hook events via spool.** One `O_APPEND` write per hook run (leading
   `\n`); drained by `serve` startup, `extract --run`, `ingest-pr`,
   `cleanup`. Drain validates in Go, falls back per row on class 22/23
   errors, quarantines to `.failed`, retries only on transient errors.
   (Spec §13 #2, #11.)
5. **Service events post-commit, best-effort.** `Service` holds an
   `EventSink` (default no-op). The write path collects events inside the
   tx closure and appends them only after `WithTx` returns nil. TTL deletes
   are evented in SQL (CTE). Status events derive from before/after
   transitions. (Spec §13 #3.)
6. **Stats: raw counts in SQL, ratios in Go.** The store returns grouped
   counts; `cmd/claude-memory` computes the AC-27 ratios (2 h attribution
   window, events-only promotion rate) and formats them. The report type
   lives in `cmd` with a consumer-side `statsReader` interface, so the
   ratio logic is unit-testable without Docker. (Spec §13 #6.)
7. **Retention** 365 d constant via `cleanup`. (Spec §13 #4.)

## Work Items

```
PR A:  00 (baseline) ─────────────────────────────────────────┐
       01 ─┬─> 02 ─> 03 ─┬─> 04 (hook) ───────────────────────┴─> 14 (measure)
           │             └─> 05 (mcp/serve)
           └─> 06 (stamping + extraction repo; needs 02)            13a (docs)
       15 (optional, after 06; droppable)

PR B:  08a (event types) ─┬─> 07 (postgres) ─┬─> 08 (service events)
                          │                  ├─> 10 (stats) ─┐
                          │                  └─> 11 (cleanup)├─> 12 (integration) ─> 13b (docs)
                          └─> 09 (spool + drains + hook events)┘                    ─> 14 (re-check)
```

### PR A — staleness

0. **Latency baseline (manual, before any hook change).** ~50 prompts in a
   real Acme repo on the current binary; record hook wall time p50/p95
   in this plan. If p95 is already ≥ 250 ms, open a follow-up to reduce
   the search cost (`Limit`, embedding call) before tuning anything in this
   feature.

   Satisfies AC-25 (baseline half).

1. **Ports, domain types, normalization.** files:
   `internal/memory/ports.go`, `internal/memory/staleness.go` (new),
   `internal/memory/service.go`, `internal/memory/mock/`.
   - `Checkout{Dir, Repo}`, `CodeHistory{Resolve, Head, Changed, Dirty}`,
     `StaleHint{Commits int}` (0 = unknown), `maxStaleChecksPerSearch = 10`.
   - `NormalizeFiles(T, files)`: pure; `:N`/`:N-M` suffix, absolute →
     relative, drop outside/`..`/empty, dedupe, cap 20. Table test, no git.
   - `Service.WithCheckout`, `WithPinnedHead`, `WithCodeHistory(h,
     ceiling)`, `WithStaleDeadline`.
   - `SearchRecord` gains `Files []string`, `CommitSHA string`,
     `Stale *StaleHint`.

   Satisfies AC-3, AC-2 (matching rules as types/helpers).

2. **`internal/gitlog` adapter.** files: `internal/gitlog/gitlog.go`,
   `cache.go`, `gitlog_test.go`.
   - `Resolve(cwd)`: one `rev-parse --show-toplevel HEAD`; parse stdout
     line 1 as `T` even on exit 128 (unborn → `head = ""`); `Repo =
     basename(T)`.
   - `Head(dir)`: `rev-parse HEAD`.
   - `Changed`: validate sha regex; `diff --quiet <sha> <H> -- files` (0 =
     fresh, 1 = changed, else error); on changed, `rev-list --count
     --max-count=100 <sha>..<H> -- files` (failure → commits 0, no error).
   - `Dirty`: `status --porcelain --untracked-files=all -- files` non-empty.
   - Common exec: no shell, `cmd.Dir = T`, `--` before files, env
     whitelist (`PATH`, `HOME`, `LANG`, `LC_*`, `XDG_CONFIG_HOME`) +
     `GIT_LITERAL_PATHSPECS=1`, `GIT_OPTIONAL_LOCKS=0`; `WaitDelay = 10 ms`.
   - `Cached(inner, store, ceiling)`: wraps `Changed`; key
     `T|H|sha|sorted(files)`; tri-state value `fresh | stale(n) |
     unchecked`; caches errors and timeouts as `unchecked`, except a
     timeout whose context had less than `ceiling` left when the call
     started (budget-shortened, AC-8), which is not cached. An
     already-expired context returns cache hits and an uncached error for
     misses, without exec.
     `MapCache`; `FileCache(path)` (load once, corrupt → empty; write back
     atomically 0600 only on new entries, keeping only entries with the
     current `H`).
   - Tests on real temp repos (helper sets `GIT_AUTHOR_*`/`GIT_COMMITTER_*`
     and `-c init.defaultBranch=main`): 2 commits on a listed file →
     changed/2; unlisted only → fresh; change+revert → fresh; rebased
     (rewritten) sha with same content → fresh; count cap 100+; unknown
     sha → error; unborn HEAD → `T` and `head = ""`; literal pathspec
     `:(glob)**` and `--output=x`; inherited `GIT_DIR` absent in child;
     `Dirty` for modified/staged/untracked listed file and not for an
     unlisted one; cache hit, miss on new `H`, old-`H` entries dropped,
     negative caching rules.

   Satisfies AC-1 (adapter), AC-4, AC-8 (adapter), AC-9 (adapter), AC-10
   (`Dirty`).

3. **Service staleness.** files: `internal/memory/staleness.go`,
   `search.go`/`service.go`, `crud.go`,
   `internal/postgres/hybrid_search.go`, `store.go` (search selects
   `r.files, r.commit_sha`), `internal/postgres/store_integration_test.go`.
   - `annotateStale(ctx, recs, max)`: matching decision in the service
     (repo, sha regex, `NormalizeFiles` non-empty, checkout and non-empty
     `H`) — no adapter call for non-matching records; `H` = pinned or
     `Head(T)` once per call; checks in goroutines under
     `min(now + ceiling, staleDeadline)`; past deadline → only cache hits
     (the `Cached` decorator is consulted with an already-expired context
     and returns hits without exec). Set `Stale` only for changed.
   - `Search` annotates the top `min(len, maxStaleChecksPerSearch)`;
     `StaleHint(ctx, rec)` for `memory_get`.
   - Tests (fake `CodeHistory`): each AC-2 row, zero adapter calls for
     non-matching; blocking fake returns within deadline + 10 ms; past
     deadline starts no checks; `Search` succeeds when every check fails;
     changed fake `Head` between two calls → different keys.
   - Integration: search rows return `files` and `commit_sha`.

   Satisfies AC-1, AC-2, AC-6 (service), AC-7, AC-30 (search SQL).

4. **Hook.** files: `cmd/claude-memory/hook.go`, `hook_test.go`,
   `main.go` (`cmdHook`, `hookDeps`, `buildCodeHistory`),
   `internal/config/config.go`.
   - `hookStart := time.Now()` at the top of `cmdHook`;
     `hookCmd(ctx, cfg, svc, deps, hookStart)`.
   - `hookDeps.History(sid)` = `gitlog.Cached(gitlog.Exec{},
     FileCache(stale-cache/<sid>.json))`, or `MapCache` when `sid` fails
     `^[A-Za-z0-9_-]{1,64}$`. `Events` field added in WI-9.
   - Payload gains `session_id`. Replace `deriveRepo` with
     `History.Resolve(cwd)` (fallback `basename(cwd)` when not a checkout).
   - `svc.WithNamespace(ns).WithCheckout(co).WithPinnedHead(H)
     .WithCodeHistory(h, cfg.StaleTimeoutHook)
     .WithStaleDeadline(hookStart + 300 ms − 20 ms)`.
   - `card` gains stale state; `renderAdditionalContext` appends the AC-5
     suffix (`1 commit`, `N commits`, `100+ commits`, no count).
   - `config`: `StaleTimeoutHook` (50 ms), `StaleTimeout` (500 ms).
   - Tests: golden rendering (fresh, 1, 2, 100+, no count, unchecked);
     fake factory: 1 `Resolve`, 0 `Head`, 0 `Changed` when nothing passes
     the threshold; budget already spent → no `Changed`, cards rendered.

   Satisfies AC-5, AC-7 (hook budget), AC-8 (hook), AC-9.

5. **MCP surface and serve.** files: `internal/mcpserver/{types,handlers,
   service}.go`, `server_test.go`, `fake_service_test.go`,
   `cmd/claude-memory/serve.go`, `main.go`.
   - `SearchResultItem` and `RecordOutput` gain `stale_hint,omitempty`,
     `stale_commits,omitempty` (commits omitted when unknown);
     `handleGet` calls `svc.StaleHint`.
   - `UpdateInput.CommitSHA` → `UpdateRequest.CommitSHA`.
   - `serve`: `Resolve(os.Getwd())` at startup → `WithCheckout(co)` (no
     pinned head; none if not a repo); log `checkout=<T>` / `none`;
     `gitlog.Cached(Exec, MapCache)`; ceiling `MEMORY_STALE_TIMEOUT`.
   - Tests: fields present when stale, `stale_commits` absent when
     unknown, both absent otherwise; `commit_sha` passes through.

   Satisfies AC-6, AC-12 (tool input).

6. **Commit baseline (stamping) and extraction repo.** files:
   `internal/memory/writepath.go`, `crud.go`,
   `internal/postgres/store.go` (whitelist `commit_sha`) +
   integration test, `internal/prsource/prsource.go` (`PR.MergeCommit`),
   `internal/azuredevops/client.go` (`lastMergeCommit.commitId`),
   `internal/extraction/{schema,processor}.go` (repo from config;
   `CommitSHA` for PR), `cmd/claude-memory/extract.go`,
   `cmd/claude-memory/ingestpr.go`.
   - Write path: when `CommitSHA == nil`, source ∈ {inline, session},
     usable files non-empty, checkout set, `req.Repo == co.Repo` → read
     `H`; stamp only if `H != ""` and `Dirty(T, files)` is `false` without
     error; else leave `NULL` and Debug-log the record id. Applies to ADD,
     SUPERSEDE (new row) and UPDATE (re-baseline; else unchanged).
   - `UpdateRecord`: explicit `CommitSHA` (allowed alone), else stamp under
     the same rule when `content` or `files` change.
   - `extract --run` (AC-32): `Resolve(tr.Cwd)` once; pass `co.Repo` into
     extraction as the repo for every draft (`extraction.Config.Repo`,
     fallback `tr.Repo` from `inferRepo`); `WithCheckout(co)` for stamping.
     This fixes the pre-existing MVP defect where a session started in a
     sub-directory wrote that sub-directory as `repo`.
   - PR: `StoreRequest.CommitSHA = PR.MergeCommit` (never local `HEAD`).
     Manual check once: `git merge-base --is-ancestor <MergeCommit>
     origin/main` on a real completed squash PR; record the result here.
   - Tests: each AC-10 condition incl. dirty and `Dirty` error; re-baseline
     on update, unchanged when dirty; NOOP leaves `commit_sha` unchanged;
     PR request carries the merge commit; transcript `cwd = T/src` →
     `repo = basename(T)`; non-checkout cwd → `inferRepo`; integration:
     `Update` persists `commit_sha`.

   Satisfies AC-10, AC-11, AC-12, AC-32, AC-30 (`commit_sha` update SQL).

13a. **Docs (staleness half).** files: `integration/INSTALL.md`,
    `integration/claude-md-snippet.md`, `DEPLOY.md` (env vars),
    `docs/specs/README.md` (index row).
    - Env vars `MEMORY_STALE_TIMEOUT_HOOK`, `MEMORY_STALE_TIMEOUT`.
    - Snippet: on a ⚠ card verify against current code, then
      `memory_update` (re-baselines) or `memory_feedback(outdated)`; pass
      `commit_sha` and `files` on `memory_store` when known; after
      committing work a record describes, call `memory_update` (or pass
      `commit_sha`) so it gets a baseline; call `memory_feedback(useful)`
      when a card helped.

    Satisfies AC-31 (staleness half).

14. **Latency re-measure (manual).** Same protocol as WI-0: ~50 prompts with
    ≥ 1 stale card and ~50 without, after PR A; record p50/p95 next to the
    WI-0 baseline; retune `MEMORY_STALE_TIMEOUT_HOOK` only if the added
    p95 exceeds 50 ms. Re-check once after PR B (spool append).

    Satisfies AC-25.

15. **Optional: PR changed paths (droppable).** files:
    `internal/azuredevops/client.go` (`az rest …/pullRequests/{id}/
    iterations` → last iteration → `/changes`), `internal/prsource/
    prsource.go` (`PR.ChangedFiles`), `internal/extraction/
    {schema,processor}.go` (paths in `PRInput` and the prompt; default an
    empty draft `files` to them, normalized, cap 20).
    - Fetch failure → PR processed as today (Warn once).
    - Tests: canned `az rest` responses; draft `files` defaulting rules.
    - Can be dropped or deferred without touching any other WI; without
      it, PR records stay mostly unchecked (AC-11).

    Satisfies AC-33.

### PR B — events + stats

8a. **Event types.** files: `internal/memory/ports.go`,
    `internal/memory/events.go` (new), `internal/memory/service.go`,
    `internal/memory/mock/`.
    - `EventSink`, `NopEventSink`, `ErrEventRejected`, `Event` (fields =
      spec §9 columns; typed enums `EventType`, `EventVia`, …),
      `Event.Validate(now)`; `Service.WithEvents`.
    - Reflection test: `Event` has only the allowed fields; `Validate`
      table test.

    Satisfies AC-14 (type half), AC-15 (type).

7. **Postgres: migration 0003, events, stats SQL.** files:
   `internal/postgres/migrations/0003_events.sql`, `store.go` (embed,
   append to `migrationSQL`), `events.go` (new), `stats.go` (new).
   - 0003 per spec §9 with named CHECK constraints.
   - `AppendEvents(ctx, evs...)`: one multi-row `INSERT … ON CONFLICT (id)
     DO NOTHING`; a `*pgconn.PgError` with SQLSTATE class 22/23 is wrapped
     as `memory.ErrEventRejected`; everything else returned as is.
   - `DeleteCandidatesByTTL`: `WITH d AS (DELETE … RETURNING id, namespace)
     INSERT INTO events (…) SELECT gen_random_uuid(), now(), d.namespace,
     'record_deleted', d.id, 'cleanup', 'ttl' FROM d` — count from the
     INSERT (pg ≥ 13; the pgvector image is pg16).
   - `PruneEvents(ctx, before time.Time) (int, error)`.
   - Stats count queries (raw grouped counts, all `at >= $1`, grouped by
     namespace): card counts and distinct records; useful within 2 h and
     useful any (self-join of `events` on `record_id`); feedback by
     outcome; created by source; candidate created / promoted pairs
     (`e2.at >= e1.at`, events only); stale tri-state counts; lifecycle
     counts; inventory from `records` by source × status (no time filter).
     Returned in a plain struct consumed by `cmd` (`statsReader`).

   Satisfies AC-13, AC-21, AC-27 (SQL), AC-29 (SQL).

8. **Service events.** files: `internal/memory/writepath.go`,
   `lifecycle.go`, `crud.go`, `events.go`, tests.
   - Write path collects events in the tx closure, appends after commit:
     ADD → `record_created`; UPDATE → `record_updated`; SUPERSEDE →
     `record_superseded` + `record_created`; NOOP promotion →
     `record_promoted(seen)` from the `candidate && seen_count ≥ 2` branch.
     Namespace = the written namespace.
   - `Feedback` / `UpdateRecord` / `DeprecateRecord` capture
     `before.Status` and derive events from the transition (spec §6.4):
     promoted only `candidate → active`, deprecated only
     `!deprecated → deprecated`, otherwise `record_updated` only (update)
     or nothing extra (feedback). `feedback` namespace = service's;
     lifecycle = record's.
   - `appendEvents` helper: on error, Warn once per process (`sync.Once`).
   - Tests with a recording sink: each action; rollback → none;
     `needs_judgment` → none; failing sink → identical responses;
     un-deprecate; feedback on deprecated record.

   Satisfies AC-15, AC-18, AC-19, AC-20, AC-22.

9. **`internal/eventspool`, drains, hook events.** files:
   `internal/eventspool/spool.go`, `spool_test.go`,
   `cmd/claude-memory/{hook,serve,extract,ingestpr,main}.go`.
   - `Sink{Dir}` implements `memory.EventSink`: marshal all events into one
     buffer starting with `"\n"`, one `write` with `O_APPEND|O_CREATE`
     0600 (dir 0700); skip when size > 10 MB.
   - `Drain(ctx, dir, sink) (DrainResult{Inserted, Skipped, Rejected},
     error)`: rename to `spool.<pid>.<nanos>.draining` (`ENOENT` ok);
     process `*.draining` with mtime ≥ 2 s; skip empty/malformed lines and
     `Validate` failures; batches of 500; on `ErrEventRejected` retry the
     batch per row; after the pass delete (no rejects) or rename to
     `.failed` (rejects); any other error → keep the file, stop.
     `Pending(dir) (lines, draining, failed int)` for `stats`/`cleanup`.
   - Wiring in `main.go`: `hookDeps.Events = eventspool.Sink{…}`; hook
     appends `card_injected` (stale tri-state, commits null when unknown,
     session id) after writing stdout; drains in `serve` (after migrate,
     at startup), `extract --run`, `ingest-pr` (each best-effort, Debug on
     error); `serve`, `extract --run`, `ingest-pr` get
     `WithEvents(postgres store)`.
   - Tests: round trip; torn last line + append → 1 skipped, new lines
     intact; malformed and invalid-enum lines skipped, never sent;
     interrupted drain re-run inserts nothing new (fake sink dedups by id);
     fresh `.draining` left; 1 rejected of 3 → 2 inserted, `.failed`, 1
     rejected; transient error keeps the file; concurrent `ENOENT`; size
     cap; hook test: 2 cards → 2 spool lines (temp `HOME`), output
     unchanged when the spool dir is unwritable.

   Satisfies AC-17, AC-23, AC-24.

10. **`stats` subcommand.** files: `cmd/claude-memory/stats.go` (new),
    `stats_test.go`, `main.go` (dispatch, help text).
    - Flag `--since` (`Nd` or Go duration, default `30d`).
      `buildPostgresStore(ctx, cfg, true)` only; no drain; counts via
      consumer-side `statsReader`; ratios (precision proxy 2 h, useful
      any, promotion rate, stale-flag rate, check coverage) computed in Go;
      total block + one block per namespace; final line
      `N events still in the spool` from `eventspool.Pending`.
    - Tests: flag parsing; ratio computation on fixed counts; formatter
      incl. `n/a` and empty data; dispatch builds no embedder (fake
      builder seam); spool backlog line.

    Satisfies AC-26, AC-27 (ratios, presentation), AC-28.

11. **`cleanup` additions.** files: `cmd/claude-memory/cleanup.go`,
    `main.go`.
    - Drain spool first; TTL delete (now evented, WI-7);
      `PruneEvents(now − eventsRetention)` with the constant
      `eventsRetention = 365 * 24 * time.Hour`; sweep `stale-cache/*.json`
      older than 7 d; print the pruned count on the second line and
      `spool: N draining, M failed files left` when non-zero.
    - Tests: `ttlDeleter` fake extended; cache sweep and spool line in a
      temp dir.

    Satisfies AC-29 (cmd half), AC-24 (cleanup drain, leftovers).

12. **Integration tests (events).** file:
    `internal/postgres/store_integration_test.go` (tag `integration`).
    - 0003 applied twice; `information_schema` column set and named
      constraints (AC-13, AC-14).
    - `AppendEvents` idempotent by id; a bad enum →
      `errors.Is(err, memory.ErrEventRejected)`.
    - Cleanup CTE: 3 expired candidates → count 3, 3 `record_deleted`
      events with the right namespaces (AC-21).
    - `PruneEvents` (−400 d removed, −10 d kept).
    - Stats counts over a seeded event set, including a `useful` 3 h after
      a card (counts in "any", not in "2 h") and a candidate promoted
      later.
    - Run green in the CI `integration` job (no workflow change).

    Satisfies AC-30 (events SQL; and AC-13, AC-21, AC-27, AC-29 on real
    SQL).

13b. **Docs (events half).** files: `DEPLOY.md`, `integration/INSTALL.md`.
    - Upgrade: run `claude-memory cleanup` once to apply 0003; the hook
      keeps working and spools meanwhile.
    - `claude-memory stats`; spool, `.failed` and cache paths; what the
      `cleanup` third line means.

    Satisfies AC-31 (events half).

## Test Strategy
- **Unit (fakes; run in CI and the dev container):**
  - `internal/gitlog`: real temp git repos (no Docker), deterministic git
    identity and default branch; the verified case table from the review
    (change+revert, rewritten sha, unborn HEAD, outside path, literal
    pathspecs).
  - `internal/memory`: `NormalizeFiles` table; fake `CodeHistory`
    (canned `changed/commits/err` per key, a blocking variant, a
    `Dirty` switch) and a recording `EventSink` (staleness rules,
    deadlines, stamping and clean guard, every event path and transition,
    failure isolation).
  - `internal/eventspool`: temp dirs; fake sink with id dedup and
    injectable `ErrEventRejected` / transient errors.
  - `internal/mcpserver`: output fields and `commit_sha` input.
  - `cmd/claude-memory`: hook via `hookDeps` fakes (call counts, budget),
    golden card rendering, spool lines under `t.Setenv("HOME")`;
    extraction repo (AC-32); `stats` flags, ratios and formatter;
    `cleanup` sweep and leftovers line. Resolver stub from `TestMain`
    stays.
- **Integration (tag `integration`, testcontainers pgvector):** WI-3/WI-6
  (PR A) and WI-12 (PR B), only under `./internal/postgres/...` so the
  existing CI job runs them. They cannot run in the dev container; compile
  them with `go vet -tags integration ./...`.
- **Manual:** WI-0 baseline; stale card in a real repo, `memory_get` shows
  the hint, `memory_update` clears it; record stored about uncommitted
  work has no `commit_sha`, gets one after commit + `memory_update`;
  squash-merged branch record shows no ⚠ on `main`; `lastMergeCommit` on
  `main` (WI-6); `stats --since 1d` after a few prompts; hook p95 (WI-14).

## Risks
- **Little to check at first.** Existing records rarely have `commit_sha`;
  the clean-tree guard further withholds baselines for records written
  about uncommitted work; PR records rarely have `files`. Mitigation:
  WI-6 stamping, the CLAUDE.md snippet's "update after commit" line,
  optional WI-15; `stats` check coverage makes the gap visible.
- **False positives.** Formatting-only changes to a listed file still
  flag. Rebases, squash merges and reverts no longer do (tree compare).
  Mitigation: it is a hint; the card asks Claude to verify; `memory_update`
  re-baselines.
- **Approximate counts.** The commit count over-counts after rewrites and
  is simplified across merges. It is display only and optional.
- **False negatives.** Renames, wrong or too-narrow `files`, a different
  clone/worktree name, PR merge commit not fetched locally (or a
  preview-merge sha) → unchecked or "fresh".
- **Hook budget.** Baseline unknown until WI-0. Mitigation: no DB write for
  events, one git process when no card, deadline carved from the remaining
  budget, tri-state per-session cache, `WaitDelay`; WI-14 measures against
  WI-0.
- **Spool growth or loss.** If nothing drains, the spool caps at 10 MB and
  then drops events; torn lines cost at most one event (leading `\n`);
  poison rows go to `.failed` instead of blocking the drain; a drain/append
  race is avoided by the 2 s mtime rule, not by locks.
- **Precision proxy bias.** Biased down (missing feedback) and up
  (unrelated `useful` within 2 h); the "any" number shows the attribution
  gap. Compare trends, not absolutes.
- **Integration tests unrun locally.** SQL is verified only in CI; ratio
  logic is kept in Go so it is tested locally.
- **`git` env surprises.** A hook running inside another git process could
  inherit `GIT_*` variables; the adapter passes none (whitelist, AC-4).

## Rollout
PR A:
1. WI-0 baseline recorded in this plan.
2. Finish WI-1..WI-6, WI-13a; CI `unit` and `integration` green;
   `go test -race ./...` locally.
3. Close Claude Code sessions; `deploy/backup.sh`; install the binary (no
   migration in PR A).
4. Smoke: a committed record whose file then changes → ⚠ card;
   `memory_get` shows `stale_hint`; `memory_update` clears it; a record
   stored about uncommitted work has no `commit_sha`.
5. WI-14 re-measure; tune `MEMORY_STALE_TIMEOUT_HOOK` only if needed.
6. Rollback: the old binary ignores stamped `commit_sha` values and cache
   files; optionally delete `~/.local/state/claude-memory/stale-cache`.

PR B:
1. Finish WI-8a, WI-7..WI-12, WI-13b; CI green; `go test -race ./...`.
2. Close Claude Code sessions; `deploy/backup.sh`; install the binary.
3. Run `claude-memory cleanup` once: applies 0003, drains the (empty or
   pre-0003) spool, prints `0` deletions and `0` pruned.
4. `claude-memory stats --since 1d` shows the injected cards and a zero
   spool backlog after the next drain.
5. WI-14 re-check (spool append).
6. Rollback: the old binary ignores `events` and the spool. Optionally
   `DROP TABLE events` and delete `~/.local/state/claude-memory/events`.

## Verification
- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`,
  `go test -race ./...` green after each PR.
- CI `unit` and `integration` jobs green on each PR.
- Every AC-1..AC-15, AC-17..AC-32 appears in at least one Work Item's
  `Satisfies` line and in at least one named test or manual check; AC-33
  only if WI-15 is taken; AC-16 is withdrawn.
