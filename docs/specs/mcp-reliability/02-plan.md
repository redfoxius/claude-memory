# Claude Memory — MCP & Hook Reliability Counters — Plan

**Status:** v0.2, not started — Opus plan review PASS WITH FIXES, fixes
applied. Spec: `docs/specs/mcp-reliability/01-spec.md` (v0.2,
AC-1..AC-23). v0.2 delta: classifier order + hook build = db_unavailable
(WI-1/4), fixed-column `Append` + test list (WI-2), handler-branch store
outcome (WI-3), DB+spool session merge and spool-only namespaces (WI-5/6).

## Context (file:line — fact)
- `internal/memory/events.go:77,114,130,206` — `Event` (14 cols),
  `Validate` (`record_id` required for all types), `appendEvents` warns once.
- `internal/eventspool/spool.go:25,42,93` — 10 MB cap, one-write `Append`,
  `Drain` (rename + 2 s settle, `Validate` per line).
- `internal/postgres/events.go:23` — fixed 14-column `Append`, `ON
  CONFLICT (id)`; `migrations/0003` named type/outcome/via checks; `0004`
  new-name-then-drop; `store.go:33,45,62,112` concat, `New` vs `Open`,
  locked runner; `schema_objects.go:67,131`; `stats.go:40` `StatsCounts`.
- `internal/mcpserver/handlers.go:15,71,143` — search, store, judgment
  branch; `server.go:21` `New(svc, logger)`.
- `internal/memory/writepath.go:53,348`, `service.go:184` — embed wraps;
  judgment response carries `ADD`; `search.go:15` degrades, no error.
- `cmd/claude-memory/main.go:201,368,393,407` — events service, serve,
  hook, spool sink; `hook.go:116-210`; `stats.go:85,170,213`.

## Architectural Constraints
- `mcpserver` takes a `memory.EventSink` + `classify func(error)
  memory.ErrorClass`; never imports `eventspool`/`postgres`.
- `memory.ClassifyError` takes `dbDown func(error) bool`; only `main.go`
  passes `postgres.IsUnavailable` (memory never imports pgx).
- Spool/sink/session id constructed only in `main.go`.
- No new Go modules; no new subcommands or flags.

## Work Items

### WI-1 — Event model, sentinels, classifier (AC-1..AC-3)
- `events.go`: two types; outcome/via constants (keep `Event.Outcome`
  JSON unchanged); `ErrorClass` + 5 constants; `Event.ErrorClass`.
- `Validate`: split into record types vs reliability types per AC-2
  (reliability: similarity/stale/stale_commits nil).
- `ports.go`: `ErrEmbeddingUnavailable` (text "embedding provider
  unavailable"); wrap only at `writepath.go:53` and `service.go:184` with
  `%w: %w` so messages stay identical (no `search.go` change).
- `ClassifyError(err, dbDown)` per AC-3, `dbDown` checked first.
- Tests: `Validate` table (every new value; old types reject new ones;
  class iff error; non-nil similarity rejected); `ClassifyError` table
  incl. ConnectError-wrapping-deadline → `db_unavailable`; existing
  message assertions still pass.

### WI-2 — Migration 0005 + store (AC-4..AC-7)
- `0005_reliability.sql`: three `ADD CONSTRAINT …_v2` + `DROP CONSTRAINT
  IF EXISTS` old pairs, then `ADD COLUMN IF NOT EXISTS error_class …
  CONSTRAINT events_error_class_check CHECK (…)`; header comment as 0004,
  no `;` in comments.
- `store.go` embed + concat; `schema_objects.go` entry
  `{ID:"0005", Objects: column("events","error_class")}`; probe test's
  `MigrationIDs` expectation.
- `events.go` `Append`: fixed 15-column list, `error_class` always
  (`nullable`) (AC-7); `eventColumns = 15`.
- `IsUnavailable(err)` in a new `postgres/errors.go` (AC-4);
  `puddle.ErrClosedPool` (puddle/v2 moved to a direct require).
- Tests: integration — 0005 applied twice, concurrent `New` ×2, each new
  enum value inserts, invalid class rejected (class 23); update
  `events_integration_test.go:66-80` (column list + constraint names
  `events_type_check`/`events_outcome_check`/`events_via_check` → `_v2`)
  and `:82` (`MigrationIDs`), `doctor_integration_test.go:147`
  (`migrations: 0001,…,0005`), `probe_test.go:57`; unit — `IsUnavailable`
  table (`*pgconn.ConnectError` incl. wrapping a deadline, `*net.OpError`,
  `08006`, `57P01`, closed pool; `42P01` → false, `context.Canceled` →
  false).

### WI-3 — MCP handler instrumentation (AC-8..AC-12)
- `mcpserver.New(svc, logger, opts ...Option)` with `WithEvents(sink,
  sessionID, classify)` (keeps existing call sites/tests compiling).
- `record(ctx, ev)` helper: builds `memory.NewEvent(now, ns, type)`, sets
  via/outcome/class/session, `sink.Append`, Warn once per server on error.
- `handleSearch`: one deferred-style emission covering validation error,
  service error, `Degraded`, ok. `handleStore`: outcome set in each
  branch (judgment branch `:143` → `needs_judgment`; stored branch maps
  the decision, unknown → `error`/`internal`); namespace = response's,
  else `svc.Namespace()`, never `in.Namespace`. Other handlers untouched.
- `main.go` `cmdServe`/`serveCmd`: `uuid.NewString()` once, spool sink,
  `func(err) memory.ErrorClass { return memory.ClassifyError(err,
  postgres.IsUnavailable) }`.
- Tests: fake sink + fake service per outcome; validation errors →
  `invalid_request`; failing sink → identical result and error; nil sink →
  no emission; non-search/store tools emit nothing.

### WI-4 — Hook instrumentation (AC-13..AC-15)
- `cmdHook`: decode stdin first (move decode out of `hookCmd` into a
  `readHookInput` used by both), then `buildService`; on build failure
  append one `search_called` error event with class `db_unavailable`
  (always, AC-14) to the spool and return nil.
- `hookCmd`: compute outcome (`ok`/`degraded`/`error`+class); build card
  events + the one `search_called`; single `Append` after stdout (or alone
  on the no-card and error paths). `cardEvents` unchanged.
- Tests: one reliability event per run (0, 1, 3 cards), merged into one
  `Append` call; search error → class; build failure path; decode failure
  → no events; stdout bytes identical to today's golden.

### WI-5 — Stats queries (AC-18)
- `memory.ReliabilityCounts{Search map[string]map[string]int; Store,
  Failures map[string]int; DBDownHours int; FirstDown, LastDown
  *time.Time}` on `EventCounts` (json `reliability`).
- `StatsCounts`: three `countBy`-style queries over `type IN
  ('search_called','store_attempted')` (group by via+outcome / outcome /
  error_class) + one `count(DISTINCT date_trunc('hour', at))`, `min`,
  `max` for `error_class = 'db_unavailable'`.
- `StatsLatestServeSession(ctx, since) (id string, at time.Time, error)`
  (latest `session_id` among `via='mcp'`) and `StatsSessionCounts(ctx,
  since, sessionID)` (its first/last `at`, searches, stores, failures) on
  the store — added to the `statsReader` port; WI-6 merges with spool.
- `EventIDsExist(ctx, ids []string) (map[string]bool, error)` (`id =
  ANY($1::uuid[])`).
- Tests: integration — fixtures across namespaces/vias/outcomes, window
  and namespace filters, DB-down hours across two hours, session counts.

### WI-6 — Stats spool merge + output (AC-19..AC-23)
- `eventspool.Scan(dir) ([]memory.Event, full bool)`: live + `*.draining`
  lines, `Validate`d, read-only; `full` = live file size > cap (cap const
  shared with `Sink`).
- `runStats`: scan, keep reliability types in window, drop ids returned by
  `EventIDsExist`, fold into total and per-namespace `Reliability`; a
  namespace seen only in the spool gets a new block (zero DB counts).
  Latest serve session = newest `at` across DB and remaining spool rows;
  its counts = `StatsSessionCounts` + spool rows of that session (merge,
  never replace). Set `ReliabilityFromSpool`, `SpoolFull`.
- Text: lines per AC-20/21/22 via `joinCounts` and `ratio`; JSON fields per
  AC-23. Update the stats golden.
- Tests: fake reader + temp spool dir (dedup, window, namespace, spool-
  only namespace block, full flag, n/a ratios, latest session newer in
  spool, one session split between DB and spool → summed), text + JSON
  golden.

### WI-7 — Docs (documentation only)
- `integration/USAGE.md` stats section: Reliability lines, spool lag and
  cap semantics (shared with `card_injected`), DB-down hours definition,
  "MCP counters cannot see an outage spanning a whole session (serve
  exits at start; only the hook records it)"; `docs/specs/README.md`.

## Order & dependencies
One PR (S): WI-1 → WI-2 → {WI-3, WI-4, WI-5} → WI-6 → WI-7. WI-3/4/5
are independent after WI-2.

## AC coverage
| AC | WI |
|---|---|
| 1, 2, 3 | 1 |
| 4, 5, 6, 7 | 2 |
| 8–12 | 3 |
| 13, 14, 15 | 4 |
| 16, 17 | 2 (drain unchanged; hook side in 4) |
| 18 | 5 |
| 19–23 | 6 (21 queries in 5) |

## Verification
- `go vet ./... && go test ./...` green; integration tests behind the
  existing Postgres tag.
- Manual (spec §7): Postgres stopped → prompts + MCP searches → restart →
  `stats --since 1h` shows `db_unavailable` failures, 1 DB-down hour,
  `reliability_from_spool > 0` before the next drain, 0 after.
- Review gates per SDD: plan review ×2 models before WI-1.

## Risks (plan-level)
- `Event.Outcome` is typed `FeedbackOutcome`: widening it must not change
  feedback JSON or `Validate` for `feedback` events (WI-1 table test).
- `mcpserver.New` signature: options keep the 1-arg form compiling.
- Hook reorder: `buildService` ping and stdin read now sequential in a
  different order; total hook budget unchanged (decode is µs).
- `id = ANY($1::uuid[])` with ~30 k ids at worst: one round trip, PK index.
- 0005 adds four `ALTER TABLE events` per process start (ACCESS
  EXCLUSIVE until `42710`, 5 s `lock_timeout`); a slow `stats` join can
  fail a serve start — same as 0004, accepted.

## Rollback
- Binary rollback is safe: 0005 only widens CHECKs and adds a nullable
  column; old INSERTs and stats queries ignore it. No down-migration.
- Cost: an old drainer (incl. a second old binary at another path) skips
  and deletes spooled reliability events, Debug-logged only (spec §8);
  accepted. To drop the feature's data: `DELETE FROM events WHERE type IN
  ('search_called','store_attempted')`.
