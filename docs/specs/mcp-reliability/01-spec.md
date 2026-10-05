# Specification: Claude Memory — MCP & Hook Reliability Counters

## 0. Metadata
- Spec ID: SPEC-2026-10-05-mcp-reliability
- Status: v0.2 — Opus plan review PASS WITH FIXES; fixes applied (§9)
- Owner: Oleksandr Kolomoiets (user@example.com)
- Input: owner request (paraphrased): "how many times we searched in the
  current session, how many records we tried to store, how many were
  actually stored and how many attempts failed (e.g. DB unavailable) — we
  fail softly, so how do we know the real numbers?" Builds on
  `staleness-metrics` (events table, spool, `stats`) and `mgmt-cli` (0004
  migration pattern).
- Owner constraints: **minimal** — only search/store counters, outcomes,
  error classes, hook failures, DB-down windows. No latency, no per-tool
  breakdown beyond search/store, no new commands, no new Go modules.

## 1. Problem
`memory_search`/`memory_store` failures are visible only in serve's
zerolog stream; hook failures (`cmd/claude-memory/hook.go:121,156`,
`main.go:400`) are logged at Debug and vanish. Service events
(`record_created` …) are written straight to Postgres after commit, so they
exist only for successful writes and a DB outage leaves no trace. `stats`
therefore cannot say how many calls happened, how many failed, or why.
This feature records one content-free reliability event per search/store
call through the hook's file spool (`internal/eventspool`), which works
while Postgres is down, and adds a "Reliability" section to `stats`.

## 2. Glossary
| Term | Definition |
|---|---|
| Reliability event | An event of type `search_called` or `store_attempted`. Ids, enums, time, namespace and session id only. |
| Origin (`via`) | `mcp` (serve's tool handler) or `hook` (UserPromptSubmit hook). Stored in the existing `via` column. |
| Outcome | `search_called`: `ok` \| `degraded` (embedding down, FTS-only result) \| `error`. `store_attempted`: `added` \| `updated` \| `superseded` \| `noop` \| `needs_judgment` \| `error`. Stored in the existing `outcome` column. |
| Error class | `db_unavailable` \| `embedding_unavailable` \| `invalid_request` \| `timeout` \| `internal`. Set iff outcome = `error`. New column `error_class`. Never error text. |
| Serve session | One `serve` process (one Claude Code session's MCP server). Id = random UUIDv4 generated at process start. |
| Hook session | Claude Code's `session_id` from hook stdin (already stored on `card_injected`). |
| DB-down hour | A UTC clock hour containing ≥ 1 reliability event with class `db_unavailable`. |
| Spool-resident | A valid event still in `spool.jsonl` or a `*.draining` file (not `*.failed`). |

## 3. Key Decisions
| # | Decision | Why |
|---|---|---|
| D1 | **Serve writes reliability events to the spool, always**; service lifecycle events stay on the direct-DB sink unchanged. No "DB first, spool on failure" fallback. | One write path per event = no duplicates and no ordering logic (stats aggregate by `at`, order is irrelevant); spool append needs no DB, so outages are recorded. |
| D2 | **Event id = UUIDv4 at emission** (`memory.NewEvent`); the drain's `ON CONFLICT (id) DO NOTHING` makes re-drains idempotent. | Same contract as hook `card_injected`; no transaction is involved. |
| D3 | **Reuse `outcome` (new values), `via` (new values `mcp`/`hook`), `session_id`; add one nullable column `error_class`** with its own CHECK. | `outcome`/`via` semantically fit; error class has no existing column and overloading one would mix enums across types. `record_id` stays NULL for reliability events. |
| D4 | **Session id**: serve = random UUIDv4 per process, never derived from host, user, path or PID; hook = Claude's `session_id` (already regex-validated, `main.go:564`). | No PII; a serve process ≈ one Claude Code session, so "current session" is answerable without MCP exposing Claude's id. |
| D5 | **`at` = emission time** (client clock), never drain time. DB-down = DB-down hours (Glossary) from `db_unavailable` events. | Spool events arrive late; `at` keeps the window honest. Hour buckets are one `date_trunc` query — no interval stitching. |
| D6 | **10 MB spool cap unchanged; past it events are dropped and not counted.** `stats` warns when the live spool is over the cap. No drop counter. The hook now appends on **every** prompt (today only with cards) and shares the cap with `card_injected`, so a full spool also drops staleness metrics; serve starts and nightly cleanup drain it. | ~300 B/event ⇒ ~30 k events ≈ months of normal use with no drain; a lock-free counter is extra machinery the owner rejected. Loss is stated, not hidden. |
| D7 | **Emit synchronously after the result is built, ignore errors** (serve: one Warn per process; hook: Debug). The hook merges its reliability event into its existing single `Append` with the card events. | `Sink.Append` is stat + open + one write + close (no fsync) ≈ tens of µs; one write per hook run preserves the "single syscall" spool contract. |
| D8 | **Hook never needs migration 0005**: it only appends to the spool (`postgres.Open` path unchanged). Every drainer (`serve`, `extract`, `ingest-pr`, `cleanup`) opens via `postgres.New`, so 0005 is applied before any drain inserts. | The owner's hook runs on every prompt; it must keep working on any schema. |
| D9 | **`stats` counts DB events + spool-resident reliability events whose ids are not yet in the table** (one `id = ANY($1)` query). | Serve's events reach Postgres only at the next drain (serve start, extract, nightly jobs); without this the current session would read 0. Exact, no double count. |
| D10 | **Store outcomes are counted at the MCP handler, not in `Service.Store`.** `extract`/`ingest-pr`/`import` writes are not counted. `skipped` is dropped (import-only, unreachable from MCP). | The request is about the MCP server; the handler alone sees `needs_judgment` and handler-level validation errors. |

## 4. User Scenarios
Mid-session `stats --since 1d` shows 14 MCP searches (1 degraded), 3
store attempts (2 added, 1 needs_judgment), 212 hook searches with 5
`db_unavailable` failures, 1 DB-down hour and the latest serve session —
although serve's events are still in the spool.

## 5. Requirements (EARS)

### 5.1 Event model
- **AC-1** `memory.EventType` shall gain `search_called` and
  `store_attempted`; `Event` shall gain `ErrorClass` (json
  `error_class,omitempty`). No other field is added; no query, title,
  content, repo, path or error text may be recorded (staleness AC-14).
- **AC-2** `Event.Validate` shall require `record_id` (UUID) for the eight
  existing types only, and for `search_called`/`store_attempted` require:
  `record_id`, `related_id`, `source`, `status` empty; `via` ∈ {`mcp`,
  `hook`}; `outcome` in that type's set (Glossary); `error_class` set and
  valid iff `outcome = error`; `similarity`, `stale`, `stale_commits` nil.
  Existing types shall reject a non-empty `error_class` and the new
  outcome/via values.
- **AC-3** (v0.2) `memory` shall provide `ClassifyError(err, dbDown
  func(error) bool) ErrorClass`, checked in this order: `dbDown(err)` →
  `db_unavailable` (first, so a `*pgconn.ConnectError` wrapping a deadline
  is never `timeout`); `ErrInvalidRequest` or a handler validation error →
  `invalid_request`; new sentinel `ErrEmbeddingUnavailable` (wrapped by
  `Store` (`writepath.go:53`) and `FindCandidatesForText`
  (`service.go:184`) where the embedder fails, `%w: %w`, message text
  unchanged) → `embedding_unavailable`; `context.DeadlineExceeded` →
  `timeout`; else `internal`. Searches never produce
  `embedding_unavailable`: `Search` degrades instead of failing (outcome
  `degraded`).
- **AC-4** `postgres` shall provide `IsUnavailable(err) bool`: true for
  `*pgconn.ConnectError` (incl. one wrapping a deadline), dial errors,
  `net.Error`, SQLSTATE class `08`, `57P01..57P03`, and
  `puddle.ErrClosedPool` (puddle/v2 becomes a direct require — already in
  the module graph). Connection drops mid-query not matching these land in
  `internal` (accepted drift). Wired into `ClassifyError` only in `main.go`.

### 5.2 Schema (migration 0005)
- **AC-5** `0005_reliability.sql` shall, idempotently under the 0004
  pattern (add new-named constraint, then `DROP CONSTRAINT IF EXISTS` the
  old name; no `;` in comments): replace `events_type_check` with
  `events_type_check_v2` (+ `search_called`, `store_attempted`),
  `events_outcome_check` with `events_outcome_check_v2` (+ `ok`,
  `degraded`, `error`, `added`, `updated`, `superseded`, `noop`,
  `needs_judgment`), `events_via_check` with `events_via_check_v2` (+
  `mcp`, `hook`), and `ADD COLUMN IF NOT EXISTS error_class VARCHAR(24)
  CONSTRAINT events_error_class_check CHECK (error_class IN (…five…))`.
- **AC-6** 0005 shall be registered in `store.go` (`migrationSQL`),
  `schemaMigrations` (object: `column("events","error_class")`) and
  `MigrationIDs`; re-running it twice and running it concurrently from two
  processes shall leave the schema unchanged and return no error.
- **AC-7** (v0.2) `Store.Append` shall always insert `error_class` (NULL
  when empty) — a fixed 15-column list; no dynamic column list (a drain
  against a pre-0005 schema is unreachable, AC-16).

### 5.3 Serve (MCP) instrumentation
- **AC-8** `serve` shall generate one serve session id at start and pass
  to `mcpserver.New` a spool sink (`eventspool.Sink{Dir: spoolDir()}`), the
  session id and the classifier. `buildServiceWithEvents` and its direct-DB
  service events are unchanged.
- **AC-9** After `memory_search` has built its result or error, the handler
  shall append one `search_called` (`via=mcp`, namespace = the service's,
  outcome `ok`/`degraded`/`error` + class). Handler-level validation errors
  (invalid kind) count as `error`/`invalid_request`.
- **AC-10** After `memory_store` has built its result or error, the handler
  shall append one `store_attempted` (`via=mcp`, namespace = response
  namespace, else the service's — never the caller's `in.Namespace`) with
  outcome taken from the **handler's branch**: the judgment-band branch
  (`handlers.go:143`, whose response still carries `Decision=ADD`) →
  `needs_judgment`; the stored branch maps `ADD`→`added`,
  `UPDATE`→`updated`, `SUPERSEDE`→`superseded`, `NOOP`→`noop`, any other
  decision (e.g. `SKIP`) → `error`/`internal`, never `added`; any error →
  `error` + class (incl. handler validation → `invalid_request`).
- **AC-11** An append failure shall never change the tool result, its
  error, or its latency beyond the append itself; it shall be logged at
  Warn once per process. A nil sink disables emission (tests, other tools).
- **AC-12** No other tool (`memory_get/list/update/deprecate/feedback`)
  shall emit reliability events.

### 5.4 Hook instrumentation
- **AC-13** `hook` shall decode stdin before building the service (pure
  local work; output and exit code unchanged), so every failure after a
  successful decode can be attributed to a namespace (`resolveNamespace(cwd)`)
  and session.
- **AC-14** Each hook run that decoded stdin shall emit exactly one
  `search_called` (`via=hook`, hook session id when valid): `error` +
  class when service build fails (**always `db_unavailable`** — the only
  network step of `buildService` is `postgres.Open`'s ping, even when the
  800 ms hook deadline cut it) or search fails (`ClassifyError`), `degraded` when the result is degraded, else `ok`
  (regardless of card count). It shall go into the same single `Append`
  as the card events, after stdout is written; with no cards it is the only
  event. A stdin decode failure emits nothing (no namespace).
- **AC-15** Hook output, exit code and timeouts shall be unchanged; a spool
  failure stays at Debug.

### 5.5 Drain and compatibility
- **AC-16** (v0.2) The drain shall accept the new events unchanged (it
  already calls `Validate`). A drain against a pre-0005 schema is
  unreachable: every drainer opens via `postgres.New`, which applies 0005
  first or fails (D8). (Were it reached, `42703` is class 42 = transient
  and would stall the drain, not quarantine.)
- **AC-17** With a new binary, the hook shall work against a pre-0005
  database (no migration on the hook path, D8); an old binary shall keep
  writing and reading a 0005 database (constraints are supersets, the new
  column is nullable and never named by old INSERTs).

### 5.6 `stats` Reliability section
- **AC-18** `StatsCounts` shall fill a new `EventCounts.Reliability`:
  `search` by via → outcome → count; `store` by outcome → count;
  `failures` by class → count (both types, both vias); `db_down_hours`
  (count) with `first_down`/`last_down` (nullable times); all for `at >=
  since` and the namespace filter.
- **AC-19** `runStats` shall read spool-resident events (live + `*.draining`
  files, `Validate`d, reliability types only, `at >= since`, namespace
  filter), drop those whose ids already exist in `events` (`id =
  ANY($1::uuid[])`), and add them to the counts; it shall report
  `reliability_from_spool` (that number). A namespace present only in the
  spool shall get its own block. It shall not drain or modify the spool.
- **AC-20** The text report shall print per block, after `lifecycle:`:
  `searches (mcp):`, `searches (hook):` (outcome counts), `store attempts:`
  (outcome counts), `failures:` (class counts), `failure rate:` as
  `ratio{errors, attempts}` for mcp search, hook search and store (`n/a`
  when 0 attempts), `db-down hours:` (count, first–last or `n/a`).
- **AC-21** The total block shall add `latest serve session:` — the serve
  session id (first 8 chars) with the latest `at` among `via=mcp` events in
  the window (DB and spool-resident), its first/last `at`, searches, store
  attempts, failures; `n/a` when none. (v0.2) Its counts are the **merge**
  of DB rows for that `session_id` and its spool-resident rows whose ids
  are not in the DB — never one source replacing the other. JSON:
  `latest_serve_session` (null when none).
- **AC-22** When the live spool file is over its cap, the report shall add
  `spool is full: new events are being dropped and not counted` (JSON
  `spool_full: true`).
- **AC-23** `--json` shall carry every new field; existing fields keep
  their names and values.

## 6. Out of Scope (YAGNI)
- Latency/duration metrics, percentiles; per-tool counters beyond
  search/store; counts for `extract`/`ingest-pr`/`import` writes.
- New commands or flags (no `--session`); a dashboard; alerting.
- A drop counter for the full spool; raising the cap; periodic drains in
  serve.
- `stats` without a database (spool-only mode); error text or stack in
  events; retry of failed calls.
- Recording serve start failures (DB down at start ⇒ no MCP server; only
  the hook records the outage).

## 7. Verification
- Unit (`go test ./...`): `Validate` table (new types, per-type outcome
  sets, class iff error, old types reject new values); `ClassifyError`
  table (incl. a `ConnectError` wrapping `context.DeadlineExceeded` →
  `db_unavailable`); `IsUnavailable` with synthetic `pgconn` errors; handler tests with
  a fake sink and fake service (each outcome, errors by class, append
  error does not change result); hook tests (one event per run, merged
  with cards, build-failure path, decode failure emits nothing);
  `runStats` with a fake reader + spool fixtures (dedup by id, cap
  warning, n/a, latest session); text and JSON golden.
- Integration (Postgres): 0005 twice and concurrently; insert each new
  value; `StatsCounts` reliability queries; `id = ANY` dedup; latest
  serve session split between DB and spool.
- Manual (owner): stop Postgres, run 3 prompts and 2 MCP searches, start
  Postgres, `stats --since 1h` shows them as failures with
  `db_unavailable` and 1 DB-down hour.

## 8. Risks
- **Spool cap loss** (D6): after ~30 k undrained events new ones vanish;
  `stats` says so (AC-22) but cannot say how many.
- **Old-binary drain** (rollback, or a second old binary at another
  path): old `Validate` skips new types → file removed, Debug-logged only;
  spooled reliability events lost. Accepted.
- **Whole-session outage invisible to MCP counters**: serve exits at start
  when Postgres is down; only the hook records that outage (USAGE note).
- **0005 DDL at every start**: four more `ALTER TABLE events` per process
  start take ACCESS EXCLUSIVE before failing `42710` (5 s
  `lock_timeout`); a slow concurrent `stats` join can make a serve start
  fail. Same pattern as 0004; `DO` guards impossible (runner splits on
  `;`).
- `StatsNamespaces` also lists reliability-only namespaces (fine).
- **Clock skew**: `at` is the client clock; `Validate`'s window applies.
- **Classification drift**: an unrecognised driver error lands in
  `internal`, under-counting `db_unavailable`; AC-4 table tests pin known
  shapes.

## 9. Owner Decisions / Changelog
- v0.1 (2026-10-05): owner accepted spool events, enums only, minimal scope.
- v0.2 (2026-10-05, Opus plan review PASS WITH FIXES): classifier order
  dbDown-first, hook build failure = `db_unavailable` (AC-3/4/14);
  fixed 15-column `Append`, AC-16 restated (AC-7/16); latest-session
  DB+spool merge (AC-21); spool-only namespaces get a block (AC-19);
  reliability events require nil similarity/stale fields (AC-2); store
  outcome from the handler branch, unknown decision → `error`/`internal`,
  no caller namespace (AC-10); searches never `embedding_unavailable`;
  Risks/D6 notes added.
- Owner decisions (v0.2): `extract`/`ingest-pr` store attempts **not
  counted** (D10); `degraded` search outcome **kept**; spool-only `stats`
  mode **out of scope**.
