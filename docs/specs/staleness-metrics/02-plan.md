# Claude Memory — Staleness Check & Usage Metrics

**Status:** not started.

## Spec
- `docs/specs/staleness-metrics/01-spec.md` (SPEC-2026-10-01-staleness-metrics,
  v0.1, AC-1..AC-31). Open owner questions: spec §13 #4 (retention), #5
  (stamping `commit_sha`), #6 (precision attribution), #8 (hook deadline).
  The plan assumes the proposed answers; WI-6 is the only item that depends
  on #5 and can be dropped without touching the others.

## Context
- Namespaces are shipped (`7e54d2f` is HEAD of the line): every record has
  `namespace`; `Service.WithNamespace`; migrations 0001, 0002 embedded in
  `internal/postgres/store.go` and concatenated into `migrationSQL`.
- The hook opens Postgres with `postgres.Open` (no migrations), runs
  `deriveRepo` (`git rev-parse --show-toplevel`) then `svc.Search`, renders
  ≤ 3 cards (`renderAdditionalContext`, title/repo/id). Measured 215–343 ms
  before migrations were skipped; AC-30 (MVP) target 300 ms p95.
- `records.files` and `records.commit_sha` exist, but:
  - `SearchRecord` and the hybrid/full-text queries do not select them.
  - Only `memory_store` (inline) can set `commit_sha`; extraction
    (`ConvertDraftToStoreRequest`) and PR ingest never do.
  - `postgres.Store.Update` does not whitelist `commit_sha`.
- `DeleteCandidatesByTTL` returns only a count. `cleanupCmd` uses the store
  directly through the consumer-side `ttlDeleter` interface.
- `stateDir()` (`cmd/claude-memory/extract.go`) = `~/.local/state/claude-memory`.
- CI (`.github/workflows/ci.yml`): `unit` job, and `integration` job running
  `go test -tags integration ./internal/postgres/...`. Integration tests
  cannot run in the dev container (no Docker).

## Scope
- In scope: AC-1..AC-31.
- Out of scope (spec §12): ranking/demoting stale records, `memory_list`
  staleness, `repo="*"` records, uncommitted changes, rename following,
  search/read events, per-repo metrics, backfilling `commit_sha`.

## Modules Touched
- `internal/memory/` — ports (`CodeHistory`, `Checkout`, `EventSink`,
  `Event`, `StaleHint`), `staleness.go`, `events.go`, stamping in the write
  path and `UpdateRecord`, events in `writepath.go`, `lifecycle.go`,
  `crud.go`; `mock/` regenerated
- `internal/gitlog/` (new) — exec adapter + cache decorator (memory map,
  per-session file)
- `internal/eventspool/` (new) — JSONL spool `Append` and `Drain`
- `internal/postgres/` — migration 0003, `AppendEvents`, search selects
  `files`/`commit_sha`, `commit_sha` update column, cleanup CTE,
  `PruneEvents`, `Stats`
- `internal/mcpserver/` — `stale_hint`/`stale_commits` outputs,
  `memory_update.commit_sha`, `StaleHint` in the consumer interface
- `internal/prsource/`, `internal/azuredevops/` — `PR.MergeCommit`
- `internal/extraction/` — pass `CommitSHA` through for PRs
- `internal/config/` — three env vars
- `cmd/claude-memory/` — `main.go` wiring, `hook.go`, `stats.go` (new),
  `cleanup.go`, `extract.go`, `ingestpr.go`, `serve.go`
- Docs: `DEPLOY.md`, `integration/INSTALL.md`, `integration/claude-md-snippet.md`

## Architectural Constraints
- `internal/memory` never runs `git`, reads files or env. It sees git only
  through `CodeHistory` and events only through `EventSink`.
- Adapters (`gitlog`, `eventspool`, `postgres`) are constructed only in
  `cmd/claude-memory/main.go` (`buildService`, `buildPostgresStore`, and a
  new `buildCodeHistory(sessionID)`); inner command code receives ports.
- `stats` and `cleanup` build only the Postgres store (no Ollama).
- The hook never writes events to Postgres and never migrates.
- Events: ids/enums/numbers only; namespace always set; appended after
  commit, best-effort; failures never surface to tool callers.
- Staleness is best-effort under a deadline; every failure is "unchecked".
- New SQL uses bound parameters only.

## Design Decisions (spec §13 #1–#3)
1. **Git behind a port.** `memory.CodeHistory{Head, CommitsTouching}`;
   `gitlog.Exec` implements it with `exec.CommandContext` (`rev-parse
   --show-toplevel HEAD`, `rev-list --count --max-count=100 <sha>..HEAD --
   <files>`). `gitlog.Cached(inner, store)` wraps it; `store` is a
   `map` (serve) or a per-session JSON file (hook). Caching is an adapter
   concern, so `memory` has no cache logic and unit tests use a plain fake.
2. **Hook events via spool.** One `O_APPEND` write per hook run; drained by
   `serve` startup, `extract --run`, `cleanup`, `stats`. Rejected options
   and reasons are in spec §13 #2.
3. **Service events post-commit, best-effort.** `Service` holds an
   `EventSink` (default no-op). The write path collects events inside the
   tx closure into a slice and appends them only after `WithTx` returns nil.
   TTL deletes are evented in SQL (CTE) for atomicity.
4. **Retention** 365 d via `cleanup` (`MEMORY_EVENTS_RETENTION`).

## Work Items

```
01 ─┬─> 02 ─> 03 ─┬─> 04 (hook) ─> 14 (measure)
    │             └─> 05 (mcp/serve)
    ├─> 06 (stamping; needs 02)
    ├─> 07 (postgres) ─┬─> 08 (service events)
    │                  ├─> 10 (stats) ─┐
    │                  └─> 11 (cleanup)├─> 12 (integration) ─> 13 (docs)
    └─> 09 (spool) ────────────────────┘
```

1. **Ports and domain types.** files: `internal/memory/ports.go`,
   `internal/memory/events.go` (new), `internal/memory/service.go`,
   `internal/memory/mock/`.
   - `Checkout`, `CodeHistory`, `StaleHint{Commits int}`, `EventSink`,
     `Event` (fields = spec §9 columns; typed enums `EventType`, `EventVia`),
     `NopEventSink`.
   - `Service.WithCheckout`, `WithCodeHistory(h, timeout)`, `WithEvents`.
   - `SearchRecord` gains `Files []string`, `CommitSHA *string`,
     `Stale *StaleHint`.
   - Reflection test: `Event` has only the allowed fields (AC-14).

   Satisfies AC-14 (type half), AC-15 (type).

2. **`internal/gitlog` adapter.** files: `internal/gitlog/gitlog.go`,
   `cache.go`, `gitlog_test.go`.
   - `Head(ctx, dir)`: one `git rev-parse --show-toplevel HEAD`; repo name
     = `basename(toplevel)`; empty repo → `Head == ""`.
   - `CommitsTouching`: validate sha regex, normalize files (AC-3), argv
     with `--`, env `GIT_LITERAL_PATHSPECS=1`, `GIT_OPTIONAL_LOCKS=0`,
     strip `GIT_DIR`/`GIT_WORK_TREE`/`GIT_INDEX_FILE`; parse the count.
   - `Cached`: key `T|H|sha|sorted(files)`; `MapCache`; `FileCache(path)`
     (load once, write back atomically 0600 only on new entries; corrupt
     file → start empty).
   - Tests use real temp repos (`git init`, commits) — `git` exists in CI
     and the dev container: counts 0/2/cap, unknown sha → error, `:N`
     suffix, absolute and outside paths, literal pathspec `:(glob)**`,
     cache hit/miss on new HEAD.

   Satisfies AC-1, AC-3, AC-4, AC-8 (adapter half), AC-9 (adapter).

3. **Service staleness.** files: `internal/memory/staleness.go` (new),
   `search.go`/`service.go`, `crud.go`, `internal/postgres/hybrid_search.go`,
   `store.go` (search selects `r.files, r.commit_sha`).
   - `annotateStale(ctx, recs, max)`: skip non-matching records (AC-2);
     run checks in goroutines under `context.WithTimeout(timeout)`; set
     `Stale` only for count ≥ 1; errors and timeouts → nil.
   - `Search` annotates the top `min(len, 10)` when a checkout and
     `CodeHistory` are set. `StaleHint(ctx, rec)` for `memory_get`.
   - Tests with a fake `CodeHistory`: each AC-2 row; parallel deadline
     (blocking fake returns within timeout + 10 ms); `Search` still
     succeeds when every check fails.

   Satisfies AC-1, AC-2, AC-6 (service), AC-7.

4. **Hook.** files: `cmd/claude-memory/hook.go`, `hook_test.go`,
   `main.go` (`cmdHook`).
   - Payload gains `session_id`. Replace `deriveRepo` with
     `history.Head(cwd)` (fallback to `basename(cwd)` on error, as today).
   - `svc.WithNamespace(ns).WithCheckout(co)` with the hook timeout
     `MEMORY_STALE_TIMEOUT_HOOK`; `CodeHistory` built in `main.go` as
     `gitlog.Cached(gitlog.Exec{}, FileCache(stale-cache/<sid>.json))`
     (`MapCache` when the session id is invalid).
   - `card` gains `StaleCommits int`; `renderAdditionalContext` appends the
     AC-5 suffix (`1 commit`, `N commits`, `100+ commits`).
   - After writing stdout: `events.Append(card_injected…)` through a
     spool `EventSink` built in `main.go` (one line per card, one write).
   - Tests: golden card rendering; one `Head` and zero `CommitsTouching`
     when nothing passes the threshold; spool lines for 2 cards; hook
     output unchanged when the spool dir is unwritable.

   Satisfies AC-5, AC-8 (hook), AC-9, AC-17, AC-23 (hook half).

5. **MCP surface and serve.** files: `internal/mcpserver/{types,handlers,
   service}.go`, `server_test.go`, `fake_service_test.go`,
   `cmd/claude-memory/serve.go`, `main.go`.
   - `SearchResultItem` and `RecordOutput` gain `stale_hint,omitempty`,
     `stale_commits,omitempty`; `handleGet` calls `svc.StaleHint`.
   - `UpdateInput.CommitSHA` → `UpdateRequest.CommitSHA`.
   - `serve`: checkout from `os.Getwd()` via `CodeHistory.Head` at startup
     (none if not a repo); `gitlog.Cached(Exec, MapCache)`; timeout
     `MEMORY_STALE_TIMEOUT`; `WithEvents(postgres store)`; drain spool at
     startup (WI-9).
   - Service reads `HEAD` fresh per call through `Head(co.Dir)` (cheap,
     ~5 ms) so a long session sees new commits.
   - Tests: fields present when stale, absent otherwise; `commit_sha`
     passes through.

   Satisfies AC-6, AC-12 (tool input).

6. **Commit baseline (stamping).** Depends on owner answer §13 #5.
   files: `internal/memory/writepath.go`, `crud.go`,
   `internal/postgres/store.go` (whitelist `commit_sha`),
   `internal/prsource/prsource.go` (`PR.MergeCommit`),
   `internal/azuredevops/client.go` (`lastMergeCommit.commitId`),
   `internal/extraction/{schema,processor}.go` (carry `CommitSHA` for PR),
   `cmd/claude-memory/extract.go` (`WithCheckout(Head(transcript cwd))`).
   - Write path: when `CommitSHA == nil`, source ∈ {inline, session},
     files non-empty, checkout set and `req.Repo == co.Repo` → stamp fresh
     `Head`. Applies to ADD, SUPERSEDE (new row) and UPDATE (re-baseline).
   - `UpdateRecord`: explicit `CommitSHA`, else stamp under the same rule
     when `content` or `files` change.
   - Tests: each condition in AC-10; PR request carries the merge commit;
     NOOP leaves `commit_sha` unchanged.

   Satisfies AC-10, AC-11, AC-12.

7. **Postgres: migration 0003, events, stats SQL.** files:
   `internal/postgres/migrations/0003_events.sql`, `store.go` (embed,
   append to `migrationSQL`), `events.go` (new), `stats.go` (new).
   - `AppendEvents(ctx, evs...)`: one multi-row `INSERT … ON CONFLICT (id)
     DO NOTHING`.
   - `DeleteCandidatesByTTL`: `WITH d AS (DELETE … RETURNING id, namespace)
     INSERT INTO events (…) SELECT gen_random_uuid(), now(), d.namespace,
     'record_deleted', d.id, 'cleanup', 'ttl' FROM d` — count from the
     INSERT (pg ≥ 13 has `gen_random_uuid()` built in; pgvector image is
     pg16).
   - `PruneEvents(ctx, before time.Time) (int, error)`.
   - `Stats(ctx, since time.Time, ns *string) (*memory.StatsReport, error)`:
     one query per section (AC-27), all filtered by `at >= $1` and optional
     namespace; inventory from `records`.
   - Unit test (no DB): source scan for `UPDATE events` / `DELETE FROM
     events` (AC-16).

   Satisfies AC-13, AC-16, AC-21, AC-27 (SQL), AC-29 (SQL).

8. **Service events.** files: `internal/memory/writepath.go`,
   `lifecycle.go`, `crud.go`, `events.go`, tests.
   - Write path collects events in the tx closure, appends after commit:
     ADD → `record_created`; UPDATE → `record_updated`; SUPERSEDE →
     `record_superseded` + `record_created`; NOOP promotion →
     `record_promoted(seen)`. Namespace = the written namespace.
   - `Feedback` → `feedback` (+ `record_promoted(feedback)` /
     `record_deprecated(feedback)`); namespace = service's namespace for
     `feedback`, record's for lifecycle events.
   - `UpdateRecord` / `DeprecateRecord` → AC-20 events.
   - `appendEvents` helper: on error, Warn once per process (`sync.Once`).
   - Tests with a recording sink: each action; rollback → none;
     `needs_judgment` → none; failing sink → identical responses.

   Satisfies AC-15, AC-18, AC-19, AC-20, AC-22.

9. **`internal/eventspool`.** files: `internal/eventspool/spool.go`,
   `spool_test.go`.
   - `Sink{Dir}` implements `memory.EventSink`: marshal all events, one
     `write` with `O_APPEND|O_CREATE` 0600 (dir 0700); skip when size
     > 10 MB.
   - `Drain(ctx, dir, sink memory.EventSink) (inserted, skipped int, err)`:
     rename to `spool.<pid>.<nanos>.draining`; process `*.draining` with
     mtime ≥ 2 s old; validate enums; batch `Append` (500 per batch);
     delete on success, keep on error.
   - Tests: round trip; malformed line skipped; interrupted drain re-run
     inserts nothing new (fake sink dedups by id, as `ON CONFLICT` does);
     fresh `.draining` file left; size cap.

   Satisfies AC-23, AC-24.

10. **`stats` subcommand.** files: `cmd/claude-memory/stats.go` (new),
    `stats_test.go`, `main.go` (dispatch, help text).
    - Flags `--since` (`Nd` or Go duration, default `30d`),
      `--namespace`. `buildPostgresStore(ctx, cfg, true)` only; drain
      spool; `Stats`; print a plain-text report (per-namespace blocks when
      no filter).
    - Consumer-side interface `statsReader` (like `ttlDeleter`).
    - Tests: flag parsing; formatter with a fixed `StatsReport` (incl.
      `n/a`); dispatch builds no embedder (fake builder seam).

    Satisfies AC-26, AC-27 (presentation), AC-28.

11. **`cleanup` additions.** files: `cmd/claude-memory/cleanup.go`,
    `main.go`, `internal/config/config.go`.
    - Drain spool first; TTL delete (now evented, WI-7); `PruneEvents`
      when `MEMORY_EVENTS_RETENTION > 0`; sweep `stale-cache/*.json` older
      than 7 d; print the second line.
    - `config`: `StaleTimeoutHook` (50 ms), `StaleTimeout` (500 ms),
      `EventsRetention` (8760 h).
    - Tests: `ttlDeleter` fake extended; cache sweep in a temp dir.

    Satisfies AC-29 (cmd half), AC-24 (cleanup drain).

12. **Integration tests.** file:
    `internal/postgres/store_integration_test.go` (tag `integration`).
    - 0003 applied twice; `information_schema` column set (AC-13, AC-14).
    - `AppendEvents` idempotent by id; CHECK rejects a bad enum.
    - Cleanup CTE: 3 expired candidates → count 3, 3 `record_deleted`
      events with the right namespaces (AC-21).
    - `PruneEvents` (−400 d removed, −10 d kept).
    - `Update` persists `commit_sha`; search rows return `files` and
      `commit_sha`.
    - `Stats` over a seeded event set: every AC-27 number, with and
      without `--namespace`.
    - Run green in the CI `integration` job (path already covers
      `./internal/postgres/...`; no workflow change needed).

    Satisfies AC-30 (and AC-13, AC-21, AC-27, AC-29 on real SQL).

13. **Docs.** files: `DEPLOY.md`, `integration/INSTALL.md`,
    `integration/claude-md-snippet.md`, `docs/specs/README.md` (index row).
    - Upgrade: run `claude-memory cleanup` once to apply 0003; the hook
      keeps working and spools meanwhile.
    - Env vars; `claude-memory stats`; spool and cache paths.
    - Snippet: on a ⚠ card, verify against current code, then
      `memory_update` (re-baselines) or `memory_feedback(outdated)`; pass
      `commit_sha` and `files` on `memory_store` when known.

    Satisfies AC-31.

14. **Latency re-measure (manual).** ~50 prompts in a real Acme repo
    with ≥ 1 stale card, and ~50 without; record hook wall time p50/p95;
    retune `MEMORY_STALE_TIMEOUT_HOOK` if needed. Record results in this
    plan.

    Satisfies AC-25.

## Test Strategy
- **Unit (fakes; run in CI and the dev container):**
  - `internal/gitlog`: real temp git repos (no Docker needed).
  - `internal/memory`: fake `CodeHistory` and recording `EventSink`
    (staleness rules, deadline, stamping, every event path, failure
    isolation).
  - `internal/eventspool`: temp dirs.
  - `internal/mcpserver`: output fields and `commit_sha` input.
  - `cmd/claude-memory`: hook rendering, git call counts, spool lines;
    `stats` flags/formatter; `cleanup` sweep. Resolver stub from
    `TestMain` stays.
- **Integration (tag `integration`, testcontainers pgvector):** WI-12, only
  under `./internal/postgres/...` so the existing CI job runs them. They
  cannot run in the dev container; compile them with
  `go vet -tags integration ./...`.
- **Manual:** stale card in a real repo, `memory_get` shows the hint,
  `memory_update` clears it; `stats --since 1d` after a few prompts; hook
  p95 (WI-14).

## Risks
- **Little to check at first.** Existing records rarely have `commit_sha`;
  without WI-6 most automatic records never get one. Mitigation: WI-6
  stamping; `stats` check coverage makes the gap visible.
- **False positives.** Formatting-only commits, or `sha` on another branch,
  flag records whose facts still hold. Mitigation: it is a hint; the card
  asks Claude to verify, not to discard; `memory_update` re-baselines.
- **False negatives.** Renames, uncommitted changes, wrong or too-narrow
  `files`, PR merge commit not fetched locally → unchecked or "fresh".
- **Hook budget.** Already near 300 ms p95. Mitigation: no DB write for
  events, one git process when no card, parallel checks under 50 ms,
  per-session cache; WI-14 measures.
- **Spool growth or loss.** If nothing drains, the spool caps at 10 MB and
  then drops events; a drain/append race is avoided by the 2 s mtime rule,
  not by locks (an event written > 2 s after rename could be lost —
  practically impossible for a single write right after `open`).
- **Precision proxy bias.** Claude does not always call `memory_feedback`,
  so the ratio is a lower bound; compare trends, not absolutes.
- **Integration tests unrun locally.** SQL is verified only in CI.
- **`git` env surprises.** A hook running inside another git process could
  inherit `GIT_DIR`; the adapter strips it (AC-4).

## Rollout
1. Finish WI-1..WI-13; CI `unit` and `integration` green;
   `go test -race ./...` locally.
2. Close Claude Code sessions; `deploy/backup.sh`.
3. Install the new binary.
4. Run `claude-memory cleanup` once: applies 0003, drains (empty) spool,
   prints `0` deletions and `0` pruned.
5. Smoke: a prompt in a repo with a recorded record whose file changed →
   ⚠ card; `memory_get` shows `stale_hint`; `memory_update` clears it.
6. `claude-memory stats --since 1d` shows the injected cards.
7. WI-14 latency re-measure; tune `MEMORY_STALE_TIMEOUT_HOOK`.
8. Rollback: the old binary ignores `events`, the spool and cache files,
   and the stamped `commit_sha` values. Optionally
   `DROP TABLE events` and delete `~/.local/state/claude-memory/{events,stale-cache}`.

## Verification
- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`,
  `go test -race ./...` green.
- CI `unit` and `integration` jobs green.
- Every AC-1..AC-31 appears in at least one Work Item's `Satisfies` line
  and in at least one named test or manual check.
