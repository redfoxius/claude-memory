# Specification: Claude Memory — Staleness Check & Usage Metrics

## 0. Metadata
- Spec ID: SPEC-2026-10-01-staleness-metrics
- Status: draft (open questions in §13)
- Version: 0.1
- Owner: Oleksandr Kolomoiets (user@example.com)
- Supersedes: none (extends SPEC-2026-10-01-memory-mvp and
  SPEC-2026-10-01-namespaces)
- Related: `docs/specs/README.md` backlog item 2 (input, agreed with the
  owner 2026-10-01); `docs/specs/memory-mvp/01-spec.md` (AC-30 hook budget,
  AC-31 silent failure, AC-35 TTL cleanup, AC-46 card fields are refined
  here, not replaced); `docs/specs/namespaces/01-spec.md` (every event
  carries a namespace).

## 1. Overview & Problem

Two gaps show up once the memory has been in use for a while:

- **Silent rot.** A record says "`OrderService.Cancel` retries 3 times". The
  code changed last week. The hook still injects the card and nothing tells
  Claude the code moved. Records already carry `files[]` and `commit_sha`,
  so git can tell whether those files changed since the record was written.
- **No feedback loop.** There is no way to tell whether cards help, which
  source (inline / session / PR) produces records that get used, whether
  candidates ever become active, or how often cards point at changed code.
  Tuning thresholds (`MEMORY_HOOK_SIM_THRESHOLD`, dedup bands) is guesswork.

This feature adds:
1. A **staleness hint** on retrieval (hook card, `memory_search`,
   `memory_get`): when the current checkout is the record's repo, count the
   commits since `commit_sha` that touch the record's files. A non-zero
   count marks the record `stale_hint` and the card says
   "⚠ code changed since this was recorded".
2. A **commit baseline** at write time, so automatic records have a
   `commit_sha` to compare against (today only inline `memory_store` calls
   that pass it explicitly have one; session and PR writers never set it).
3. An append-only **`events` table** (ids and enums only, no content) and a
   `claude-memory stats` report.

Staleness is a hint. It never hides, demotes or deprecates a record, and a
failed check is never an error.

## 2. Glossary

| Term | Definition |
|---|---|
| Checkout | The git working tree a process works in: toplevel dir `T`, repo name `R = basename(T)` (the same rule as today's `deriveRepo`), and `HEAD` sha `H`. Hook: from payload `cwd`. `serve`: from its working directory. `extract --run`: from the transcript `cwd`. |
| Matching record | A record whose `repo == R` (not `*`), with a valid `commit_sha` and at least one usable file after normalization (AC-3). |
| Stale count | Number of commits in `commit_sha..HEAD` touching at least one of the record's files, capped at 100. Same set as `git log --oneline <sha>..HEAD -- <files>`. |
| `stale_hint` | `true` when the stale count is ≥ 1. |
| Unchecked | No verdict: not a matching record, timeout, or any git failure. Shown exactly like "not stale" (no marker). |
| Commit baseline | The `commit_sha` stored on a record: the commit the record's facts were true at. |
| Event | One row in `events`: a timestamp, a namespace, a type, and ids/enums/numbers only. |
| Spool | `~/.local/state/claude-memory/events/spool.jsonl`: the hook's local append-only event buffer, later moved into `events` (drained). |
| Precision proxy | Share of cards injected in a window whose record later got `memory_feedback(useful)` in that window (AC-27). |

## 3. User Scenarios

### Scenario: Stale card
The owner asks about order cancellation in `billing-service`. The hook finds
"Cancel retries 3 times" (`commit_sha=a1b2c3`, `files=[internal/order/cancel.go]`).
`git rev-list --count a1b2c3..HEAD -- internal/order/cancel.go` gives 2. The card reads
`- [memory] Cancel retries 3 times (repo: billing-service, id: …) ⚠ code changed since this was recorded (2 commits)`.
Claude calls `memory_get` (which also returns `stale_hint: true,
stale_commits: 2`), checks the current code, then calls `memory_update`
with the corrected content. The update re-baselines `commit_sha` to the
current `HEAD`, so the next card has no marker.

### Scenario: Foreign repo
The same record is a card while working in `pet-game` (it lives in
`global`, `repo=billing-service`). The checkout is `pet-game`, so no git call
runs and no marker is shown.

### Scenario: Slow repository
In a huge monorepo `git rev-list` takes 400 ms. The hook's 50 ms staleness
deadline fires; the card is shown without a marker. Hook latency stays
within MVP AC-30.

### Scenario: Monthly review
`claude-memory stats --since 30d` (no Ollama needed) prints: 412 cards
injected, 37 distinct records later marked useful (precision proxy 0.18),
records created by source (inline 9, session 41, pr 63), candidate→active
promotion 22 %, stale-flag rate 7 % of checked cards (check coverage 61 %),
14 TTL deletions. The owner raises `MEMORY_HOOK_SIM_THRESHOLD`.

## 4. Assumptions & Constraints
- Single user, local laptop, Postgres over Tailscale (MVP topology B).
  Events are a relevance/tuning aid, not an audit log.
- `git` is on `PATH` wherever `claude-memory` runs. Without it, every check
  is unchecked.
- The hook is a fresh process per prompt with an 800 ms hard timeout and a
  300 ms p95 target (MVP AC-30). It opens Postgres without migrations
  (`postgres.Open`). Claude Code reads the hook's output after the process
  exits, so anything the hook does before exiting counts against the budget.
- Hook payloads carry `cwd` and `session_id`.
- Layering stays as in the MVP and namespaces: `internal/memory` declares
  ports and never runs processes, reads env vars or touches files. Concrete
  adapters (git, spool, Postgres) are built only in
  `cmd/claude-memory/main.go`.
- Only migrating subcommands (`serve`, `seed`, `cleanup`, `ingest-pr`,
  `eval-retrieval`, and the new `stats`) apply migration 0003.

## 5. Cross-Module Interactions

```
hook ──► gitlog.Head(cwd) ─► Checkout{T,R,H} ─► svc.WithNamespace(ns).WithCheckout(co)
           │                                        │ Search → SearchRecord{Files, CommitSHA}
           │                                        └► annotateStale (≤3 recs, parallel, 50 ms)
           │                                              └► CodeHistory.CommitsTouching (cached per session)
           └► stdout card (+ ⚠ suffix) ──► eventspool.Append(card_injected…)   [no DB write]

serve ──► Checkout from wd ─► memory_search / memory_get (+ stale_hint, stale_commits)
       ──► memory_store / update / deprecate / feedback ─► EventSink (postgres) best-effort
       ──► startup: eventspool.Drain → postgres.AppendEvents
extract --run ─► WithCheckout(transcript cwd) ─► Store stamps commit_sha = HEAD ─► events
ingest-pr ────► StoreRequest.CommitSHA = PR merge commit ─► events
cleanup ──────► DELETE … RETURNING → INSERT events (one statement); prune old events; drain
stats ────────► postgres only (no Ollama): drain, then aggregate SQL over events + records
```

Ports in `internal/memory/ports.go`: `CodeHistory` (git), `EventSink`
(events). Adapters: `internal/gitlog` (exec `git`, plus a cache decorator),
`internal/eventspool` (JSONL file), `internal/postgres` (`AppendEvents`,
stats queries, cleanup CTE).

## 6. Functional Requirements

### 6.1 Staleness check
- AC-1 (Event-driven): WHEN a matching record (§2) is returned by the hook,
  `memory_search` or `memory_get` and a checkout is known, the system shall
  compute its stale count with
  `git rev-list --count --max-count=100 <commit_sha>..HEAD -- <files>` run in
  `T`, and set `stale_hint = (count ≥ 1)`. Verify: a temp git repo with a
  record at commit C1 and two later commits touching one listed file gives
  count 2; commits touching only unlisted files give count 0 (not stale).
- AC-2 (Unwanted behavior): IF the record's `commit_sha` is missing or not
  `^[0-9a-f]{7,40}$`, OR no file is usable (AC-3), OR `repo` is `*` or
  differs from `R`, OR the directory is not a git checkout, OR `git` is
  missing, OR the sha is unknown locally, OR git exits non-zero, THEN the
  record is unchecked: no marker, no error, the call's result is otherwise
  unchanged. Verify: one table-test row per condition; each yields
  unchecked and the search/get still succeeds.
- AC-3 (Ubiquitous): Files shall be normalized before the check: strip a
  trailing `:N` or `:N-M` line suffix; turn an absolute path under `T` into
  a `T`-relative path; drop paths outside `T`, paths escaping `T` via `..`,
  and empty entries; keep at most 20. Verify: table test over these forms.
- AC-4 (Ubiquitous): `git` shall be run via `exec` without a shell, with
  `cmd.Dir = T`, the sha passed only after validation (AC-2), every file
  after a literal `--`, env `GIT_LITERAL_PATHSPECS=1`,
  `GIT_OPTIONAL_LOCKS=0`, and `GIT_DIR`/`GIT_WORK_TREE`/`GIT_INDEX_FILE`
  removed. Verify: adapter test asserts the argv and env; a file named
  `:(glob)**` or `--output=x` is treated literally.
- AC-5 (Event-driven): WHEN the hook injects a card for a stale record, the
  card line shall end with
  ` ⚠ code changed since this was recorded (N commits)` (`1 commit`
  singular, `100+ commits` at the cap). Nothing else on the card changes.
  This refines MVP AC-46: cards still carry only `title`, `repo`, `id`,
  plus this fixed text and an integer. Verify: `renderAdditionalContext`
  golden tests for fresh, stale (1, 2, 100+) and unchecked cards.
- AC-6 (Ubiquitous): `memory_search` items and the `memory_get` record
  shall include `stale_hint: true` and `stale_commits: N` when stale, and
  omit both otherwise. `serve` derives its checkout from its working
  directory and reads `HEAD` fresh for each call (the cache key carries
  it). At most 10 records per `memory_search` call are checked (the top
  10). Verify: server tests with a fake service/`CodeHistory`.
- AC-7 (State-driven): WHILE checking staleness, all checks of one call shall
  run in parallel under one deadline: `MEMORY_STALE_TIMEOUT_HOOK` (default
  50 ms) in the hook, `MEMORY_STALE_TIMEOUT` (default 500 ms) in `serve`.
  Checks unfinished at the deadline are unchecked and not cached; their
  processes are killed. Verify: a fake `CodeHistory` that blocks returns
  unchecked within deadline + 10 ms; hook output still has the card.
- AC-8 (Ubiquitous): Results shall be cached by key
  `(T, H, commit_sha, sorted files)`. Because `H` is in the key, a hit is
  always exact. Hook: one file per session,
  `~/.local/state/claude-memory/stale-cache/<session_id>.json` (0600; no
  file cache when `session_id` is empty or not `^[A-Za-z0-9_-]{1,64}$`).
  `serve`: in-process map for the server's lifetime. Verify: two hook runs
  in one session with an unchanged `HEAD` call `CommitsTouching` once; a
  new commit (new `H`) forces a recompute.
- AC-9 (Ubiquitous): The hook shall get `T` and `H` from one
  `git rev-parse --show-toplevel HEAD` call that replaces today's
  `deriveRepo` call, so an empty result (no cards) adds no git process. If
  `HEAD` cannot be resolved (empty repo), repo derivation falls back as
  today and every record is unchecked. Verify: hook test with a fake
  `CodeHistory` counts calls: 1 `Head`, 0 `CommitsTouching` when no card
  passes the threshold.

### 6.2 Commit baseline at write time
- AC-10 (Event-driven): WHEN the service has a checkout and stores a record
  with `source` `inline` or `session`, no explicit `commit_sha`, at least
  one file, and `repo == R`, THEN it shall stamp `commit_sha` with the
  checkout's current `HEAD` (read at write time). An explicit `commit_sha`
  always wins. `serve` has its working-directory checkout; `extract --run`
  uses the transcript `cwd`. Verify: service test with a fake
  `CodeHistory`: stamped when all conditions hold; not stamped for
  `repo="*"`, no files, other repo, or no checkout.
- AC-11 (Event-driven): WHEN `ingest-pr` stores a record extracted from a
  PR, `commit_sha` shall be the PR's merge commit from the provider (Azure
  DevOps `lastMergeCommit.commitId`, carried as `prsource.PR.MergeCommit`);
  if the provider gives none, `commit_sha` stays empty. PR records are never
  stamped from local `HEAD` (a local checkout behind the merge would flag
  the PR's own commits as changes). Verify: azuredevops client test parses
  the field; ingest-pr test asserts `StoreRequest.CommitSHA`.
- AC-12 (Event-driven): WHEN a write-path UPDATE or `memory_update` changes
  `content` or `files`, `commit_sha` shall be re-baselined: the explicit
  value (`memory_update` gains optional `commit_sha`), else the checkout
  `HEAD` under the AC-10 conditions, else unchanged. The Postgres adapter
  accepts `commit_sha` as an update column. NOOP never changes it. Verify:
  service tests per path; integration test that `Update` persists
  `commit_sha`.

### 6.3 Events table
- AC-13 (Ubiquitous): Migration `0003_events.sql` shall create `events`
  (§9) with `CREATE … IF NOT EXISTS` only, embedded and applied after 0002
  by migrating subcommands. Running it twice changes nothing. Verify:
  integration test applies all migrations twice; table and indexes exist
  once.
- AC-14 (Ubiquitous): Events shall hold no content: only the columns in §9
  (uuids, enums, numbers, booleans, timestamps, a validated session id). No
  title, content, repo, file path, note, reason or query text. Verify: a
  unit test reflects over `memory.Event` and allows only the listed fields;
  an integration test checks the column set in `information_schema`.
- AC-15 (Ubiquitous): Every event shall carry a non-empty `namespace`: for
  usage events (`card_injected`, `feedback`) the namespace the card was
  shown in or the feedback was given from; for lifecycle events the
  record's namespace. Verify: a `global` record injected in `acme`
  yields `card_injected` with `acme`; its creation yields
  `record_created` with `global`.
- AC-16 (Ubiquitous): `events` is append-only. No code path updates
  events; the only delete is the retention prune (AC-29). Verify: a unit
  test scans non-test sources of `internal/postgres`: no `UPDATE events`,
  exactly one `DELETE FROM events`.

### 6.4 What is recorded
Event types (`type`), with `via` values where relevant:

| type | when | fields set |
|---|---|---|
| `card_injected` | hook injects a card | record_id, similarity, stale (true / false / null = unchecked), stale_commits, session_id |
| `feedback` | `memory_feedback` succeeds | record_id, outcome |
| `record_created` | write path ADD, and the new row of SUPERSEDE | record_id, source, status |
| `record_updated` | write path UPDATE (source of the write); `memory_update` (source `inline`) | record_id, source |
| `record_superseded` | SUPERSEDE | record_id (old), related_id (new), source |
| `record_deprecated` | `memory_deprecate` (`via=tool`), `memory_update` to deprecated (`tool`), feedback outdated/wrong (`feedback`) | record_id, via |
| `record_promoted` | candidate→active by NOOP seen_count (`seen`), feedback useful (`feedback`), `memory_update` status (`tool`) | record_id, via |
| `record_deleted` | TTL cleanup (`via=ttl`, source `cleanup`) | record_id, source, via |

- AC-17 (Event-driven): WHEN the hook injects cards, one `card_injected`
  event per card shall be recorded (via the spool, AC-24). Verify: hook test
  with a temp spool: 2 cards → 2 lines with the right ids, similarities,
  stale values and session id.
- AC-18 (Event-driven): WHEN `memory_feedback` succeeds, a `feedback` event
  shall be recorded, plus `record_promoted(via=feedback)` or
  `record_deprecated(via=feedback)` when the status changed. Verify: service
  test with a recording `EventSink`.
- AC-19 (Event-driven): WHEN the write path commits, it shall record the
  events in the table above for ADD, UPDATE, SUPERSEDE (both events) and a
  NOOP that promotes. A `needs_judgment` response, a plain NOOP and a rolled
  back transaction record nothing. Verify: one service test per action,
  including a failing tx.
- AC-20 (Event-driven): WHEN `memory_update` or `memory_deprecate` succeeds,
  it shall record `record_updated` and, on a status change,
  `record_promoted(via=tool)` or `record_deprecated(via=tool)`. Verify:
  service tests.
- AC-21 (Event-driven): WHEN `cleanup` deletes candidates by TTL, it shall
  write one `record_deleted` event per deleted row in the same SQL statement
  as the delete (`WITH d AS (DELETE … RETURNING id, namespace) INSERT INTO
  events …`), so the printed count equals the events written. Verify:
  integration test: 3 expired candidates → output `3`, 3 events.
- AC-22 (Unwanted behavior): IF appending an event fails (table missing,
  Postgres error, unwritable spool), THEN the operation's result shall be
  unchanged and the failure logged at most once per process at Warn (hook:
  Debug only). Events are appended after the business operation commits,
  never inside its transaction. Verify: service test with a failing
  `EventSink`: `Store`/`Feedback` succeed with identical responses.

### 6.5 Hook event path (latency)
- AC-23 (Ubiquitous): The hook shall not write events to Postgres. It shall
  append all its events as JSON lines in a single `write` on a file opened
  `O_APPEND|O_CREATE` (0600, dir 0700) at the spool path. If the spool is
  over 10 MB it appends nothing (one Debug line). Verify: hook test; a
  stubbed 10 MB spool is left untouched.
- AC-24 (Event-driven): WHEN `serve` starts, `extract --run`, `cleanup` or
  `stats` runs, it shall drain the spool: rename `spool.jsonl` to
  `spool.<pid>.<unix-nanos>.draining`, insert the events of every
  `*.draining` file whose mtime is ≥ 2 s old with `ON CONFLICT (id) DO
  NOTHING` (event ids are client-generated UUIDv4), then delete the file.
  Malformed lines are skipped and counted. A failed insert keeps the file
  for the next drain. Verify: unit test (fake sink): a drain interrupted
  after inserting re-inserts nothing new on the next drain; a fresh
  `.draining` file is left for later.
- AC-25 (Ubiquitous): Staleness plus event recording shall add ≤ 50 ms p95
  to the hook, and MVP AC-30 (p95 < 300 ms, 800 ms hard cap) and AC-31
  (silent failure) still hold. Verify: manual re-measure over ~50 prompts
  in a real repo, with and without stale cards (plan WI-14).

### 6.6 `claude-memory stats`
- AC-26 (Ubiquitous): `claude-memory stats [--since 30d] [--namespace NS]`
  shall build only the Postgres store (no Ollama, no embedder), apply
  migrations, drain the spool, and print the report. `--since` accepts `Nd`
  or a Go duration (default `30d`); `--namespace` filters (default: all,
  with a per-namespace breakdown). A bad flag exits non-zero with a usage
  message. Verify: `cmd` test: dispatch with Ollama unreachable succeeds;
  `--since 7d` and `--since 36h` parse; `--since x` errors.
- AC-27 (Ubiquitous): Over window `W = [now − since, now]` the report shall
  show:
  - **cards:** `card_injected` count, distinct records injected;
    **useful** = distinct injected records with a `feedback(useful)` event
    in `W` at or after the record's first injection in `W`;
    **precision proxy** = useful / distinct records injected.
  - **feedback** counts by outcome.
  - **records by source:** `record_created` in `W` by source; plus the
    current inventory from `records` by source × status.
  - **promotion rate:** of records created in `W` with status `candidate`,
    the share with a `record_promoted` event (at any time after creation).
  - **stale-flag rate** = cards with `stale = true` / cards with
    `stale IS NOT NULL`; **check coverage** = cards with
    `stale IS NOT NULL` / all cards.
  - **lifecycle:** superseded, deprecated by `via`, TTL-deleted counts.
  Verify: integration test seeds a fixed event set and asserts every number.
- AC-28 (Ubiquitous): Ratios with a zero denominator print `n/a`; an empty
  table prints zeros. Verify: unit test of the formatter.

### 6.7 Retention
- AC-29 (Ubiquitous): `cleanup` shall delete events older than
  `MEMORY_EVENTS_RETENTION` (Go duration, default `8760h` = 365 days; `0`
  keeps everything), stale-cache files older than 7 days, and print the
  event count deleted on a second line. The prune is not itself an event.
  Verify: integration test with events at −400 d and −10 d; cmd test for
  cache-file sweep.

### 6.8 Verification and documentation
- AC-30 (Ubiquitous): New SQL (migration 0003, `AppendEvents`, cleanup CTE,
  stats queries, `commit_sha` update) shall have `integration`-tag tests
  under `./internal/postgres/...`, run green by the existing CI
  `integration` job. Verify: green CI run on the feature branch.
- AC-31 (Ubiquitous): Docs shall cover: `DEPLOY.md` upgrade (run
  `claude-memory cleanup` once to apply 0003; the hook keeps working before
  that, its events wait in the spool), the new env vars, and
  `claude-memory stats`; the CLAUDE.md snippet: on a ⚠ card, verify the
  record against current code before relying on it, then `memory_update`
  (re-baselines) or `memory_feedback(outdated)`.

## 7. Non-Functional Requirements
- **Hook latency:** AC-25. The spool append is one syscall; the extra git
  work only runs for injected cards and is capped at 50 ms.
- **Serve latency:** staleness adds ≤ 500 ms worst case per
  `memory_search`/`memory_get` call; typical < 30 ms with the cache.
- **Privacy:** events hold no content (AC-14). Event ids, record ids and
  session ids are not secrets. The spool and cache files are 0600.
- **Security:** sha and files come from the model or haiku; they are
  validated and passed as argv after `--` (AC-4, §11). No new SQL built
  from input; all values are bound parameters.
- **Storage:** about 150 rows/day at 50 prompts × 3 cards, ≈ 55 k rows/year,
  a few MB. Retention (AC-29) bounds it.
- **Logging:** never log files, titles or content from the staleness path;
  ids and counts only.

## 8. Edge Cases
- `commit_sha` not an ancestor of `HEAD` (feature branch, rebase): `sha..HEAD`
  still counts commits reachable from `HEAD` only; may over-count. Accepted:
  it is a hint.
- Shallow clone or the PR merge commit not fetched: unknown revision →
  unchecked.
- Renamed or deleted file: renames are not followed; a delete counts as a
  change (stale), which is correct.
- `files` are directories: git pathspecs accept them; any change below
  counts.
- Uncommitted working-tree changes: not seen (commits only).
- Existing records (before this feature) mostly lack `commit_sha` → unchecked.
  Check coverage in `stats` shows how much is checkable.
- `HEAD` moves during a session: the cache key includes `H`, so the next
  call recomputes.
- Record in `global` whose `repo` equals the current repo: checked like any
  other record.
- Hook before migration 0003: unaffected (it never writes `events`).
- `serve` started outside a git repo: no checkout; nothing is checked or
  stamped.
- Spool never drained (no `serve`, no `cleanup`): capped at 10 MB (AC-23).
- Two drains at once: renames are atomic, each claims different files; the
  insert is idempotent by event id.
- Clock skew between laptop processes: none (same machine); `at` is set by
  the writer (`now()` for direct inserts, the client time for spooled
  events).

## 9. Data Model

```sql
CREATE TABLE IF NOT EXISTS events (
    id            UUID PRIMARY KEY,               -- client-generated (idempotent drain)
    at            TIMESTAMPTZ NOT NULL,
    namespace     TEXT NOT NULL,
    type          VARCHAR(32) NOT NULL CHECK (type IN ('card_injected','feedback',
                    'record_created','record_updated','record_superseded',
                    'record_deprecated','record_promoted','record_deleted')),
    record_id     UUID,                           -- no FK: records may be deleted
    related_id    UUID,                           -- superseding record
    source        VARCHAR(20) CHECK (source IN ('inline','session','pr','cleanup')),
    status        VARCHAR(20) CHECK (status IN ('candidate','active','deprecated')),
    outcome       VARCHAR(20) CHECK (outcome IN ('useful','outdated','wrong')),
    via           VARCHAR(20) CHECK (via IN ('tool','feedback','seen','ttl')),
    similarity    REAL,
    stale         BOOLEAN,                        -- null = unchecked
    stale_commits SMALLINT,
    session_id    VARCHAR(64)                     -- Claude Code session id (hook only)
);
CREATE INDEX IF NOT EXISTS idx_events_at ON events(at);
CREATE INDEX IF NOT EXISTS idx_events_type_at ON events(type, at);
CREATE INDEX IF NOT EXISTS idx_events_record ON events(record_id);
```

`records`: no schema change. `commit_sha` becomes updatable (adapter
whitelist) and gets stamped (§6.2).

## 10. Interfaces
- **Ports** (`internal/memory/ports.go`):
  ```go
  type Checkout struct{ Dir, Repo, Head string }
  type CodeHistory interface {
      Head(ctx context.Context, dir string) (Checkout, error)
      CommitsTouching(ctx context.Context, co Checkout, sha string, files []string, max int) (int, error)
  }
  type EventSink interface { Append(ctx context.Context, evs ...Event) error }
  ```
  `Service.WithCheckout(Checkout) *Service`,
  `Service.WithCodeHistory(CodeHistory, timeout) *Service`,
  `Service.WithEvents(EventSink) *Service` (defaults: no checkout, no-op
  sink). `SearchRecord` gains `Files`, `CommitSHA`, `Stale *StaleHint`;
  new `Service.StaleHint(ctx, *record.Record) *StaleHint`.
- **MCP:** `memory_search` items and `memory_get` gain optional
  `stale_hint` (bool) and `stale_commits` (int). `memory_update` gains
  optional `commit_sha`.
- **Hook card:** suffix per AC-5.
- **CLI:** `claude-memory stats [--since 30d] [--namespace NS]`. `cleanup`
  prints a second line: events pruned.
- **Env:** `MEMORY_STALE_TIMEOUT_HOOK` (50ms), `MEMORY_STALE_TIMEOUT`
  (500ms), `MEMORY_EVENTS_RETENTION` (8760h).
- **Files:** `~/.local/state/claude-memory/events/spool.jsonl`,
  `…/stale-cache/<session_id>.json`.
- **prsource:** `PR.MergeCommit string`.

## 11. Untrusted Inputs
- `commit_sha` and `files` come from the model (inline) or haiku
  (session/PR). They reach only `git` argv, validated and after `--`, with
  literal pathspecs (AC-3, AC-4). They are never executed, used in SQL text,
  or written to events.
- `session_id` from the hook payload: validated (AC-8) before use in a file
  name or an event.
- Spool lines are parsed as JSON into `memory.Event`, and enums are
  validated before insert (the table CHECKs are the backstop). Bad lines are
  skipped.

## 12. Out of Scope
- Ranking, demoting or auto-deprecating stale records; showing *which*
  commits changed (Claude can run `git log` itself).
- Staleness for `memory_list`, for `repo="*"` records, or from a different
  checkout of the same repo name.
- Detecting uncommitted changes; following renames.
- Events for search queries, `memory_get` reads, NOOPs that do not promote,
  or the hook's below-threshold results.
- Dashboards, export, or remote metrics; per-repo breakdown (repo names are
  not stored in events).
- Backfilling `commit_sha` on existing records (could be a later
  `review` CLI action, backlog item 3).

## 13. Clarifications Log

| # | Question | Answer | Impacted AC |
|---|---|---|---|
| 1 | Where is git invoked? | **Decided (spec-creator):** a `CodeHistory` port declared in `internal/memory`, implemented by a new `internal/gitlog` adapter (exec `git`) built only in `main.go`; caching is a decorator in `gitlog`, so `memory` stays free of processes, files and env. | AC-1..AC-9 |
| 2 | How does the hook record events without blowing its budget? | **Decided (spec-creator):** local spool (one `O_APPEND` write, < 1 ms), drained by DB-holding subcommands. Rejected: sync INSERT (+1 Tailscale round trip on an already tight budget; fails before 0003 is applied), goroutine fire-and-forget (killed at process exit), detached child process (exec + new connection, hard to test), same transaction (the read path has none). | AC-17, AC-23..AC-25 |
| 3 | Events in the write transaction or after it? | **Decided (spec-creator):** after commit, best-effort, for every service path. Metrics must never fail or roll back a write; a lost event in a crash window is acceptable for a proxy metric. Exception: TTL cleanup writes its events in the delete statement (atomic and free). | AC-19, AC-21, AC-22 |
| 4 | Retention? | **Proposed:** 365 days, pruned by `cleanup`, configurable (`0` = forever). Volume is a few MB/year, so the limit is for hygiene, not space. Owner to confirm. | AC-29 |
| 5 | Automatic records have no `commit_sha` today (session and PR writers never set it), so the check would almost never apply. Stamp it at write time? | **Proposed, needs owner confirmation:** yes — session/inline from the checkout `HEAD` (AC-10), PR from the provider merge commit (AC-11), re-baseline on content/file updates (AC-12). Without this, the staleness half of the feature only covers inline stores that pass `commit_sha`. | AC-10..AC-12 |
| 6 | Precision proxy attribution | **Proposed:** a record counts as useful if it gets `feedback(useful)` in the window at or after its first injection; no session matching (the MCP server does not know the Claude session id). Biased low because Claude does not always call `memory_feedback`. Owner to confirm. | AC-27 |
| 7 | Stale threshold | Owner, backlog item 2: any commit touching a listed file → stale (count ≥ 1). | AC-1 |
| 8 | Hook staleness deadline | **Proposed:** 50 ms; re-measure (WI-14) and retune. | AC-7, AC-25 |

## 14. Acceptance Criteria Summary (Definition of Done)

- [ ] AC-1 — stale count via `rev-list --count` over `sha..HEAD -- files`, capped at 100
- [ ] AC-2 — missing sha/files, foreign repo, git failure → unchecked, never an error
- [ ] AC-3 — file normalization (line suffix, absolute → relative, drop outside, max 20)
- [ ] AC-4 — safe git invocation (no shell, `--`, literal pathspecs, clean env)
- [ ] AC-5 — ⚠ suffix on stale hook cards; AC-46 refined
- [ ] AC-6 — `stale_hint` / `stale_commits` on `memory_search` and `memory_get`
- [ ] AC-7 — one parallel deadline per call (hook 50 ms, serve 500 ms)
- [ ] AC-8 — cache keyed with HEAD; per-session file (hook), in-process (serve)
- [ ] AC-9 — one `git rev-parse` in the hook; no extra git without cards
- [ ] AC-10 — stamp `commit_sha` from checkout HEAD (inline/session)
- [ ] AC-11 — PR records use the provider merge commit
- [ ] AC-12 — re-baseline on content/file updates; `memory_update.commit_sha`
- [ ] AC-13 — migration 0003, idempotent
- [ ] AC-14 — no content in events
- [ ] AC-15 — every event has a namespace (usage: acting; lifecycle: record's)
- [ ] AC-16 — append-only (only the retention delete)
- [ ] AC-17 — `card_injected` per injected card
- [ ] AC-18 — feedback events (+ promoted/deprecated)
- [ ] AC-19 — write-path events; none on rollback / needs_judgment
- [ ] AC-20 — `memory_update` / `memory_deprecate` events
- [ ] AC-21 — TTL deletes evented in the same statement
- [ ] AC-22 — event failures never change an operation's result
- [ ] AC-23 — hook spools events, never writes them to Postgres; 10 MB cap
- [ ] AC-24 — idempotent rename-based drain
- [ ] AC-25 — ≤ 50 ms p95 added to the hook; MVP AC-30/31 still hold
- [ ] AC-26 — `stats` without Ollama; `--since`, `--namespace`
- [ ] AC-27 — metric definitions
- [ ] AC-28 — zero denominators → `n/a`
- [ ] AC-29 — event retention and cache sweep in `cleanup`
- [ ] AC-30 — integration tests green in CI
- [ ] AC-31 — DEPLOY.md, env vars, CLAUDE.md snippet
