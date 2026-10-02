# Claude Memory — Management CLI & Import — Plan

**Status:** v0.2, not started — Opus plan review PASS WITH FIXES, fixes
applied. Spec: `docs/specs/mgmt-cli/01-spec.md` (SPEC-2026-10-02-mgmt-cli
v0.2, AC-1..AC-40; AC-36, AC-39 withdrawn). WI-8 withdrawn; WI-12 added.

## Context (file:line — fact)
- `internal/memory/writepath.go:26` — `Service.Store`: scrub → embed → tx
  {lock, `FindCandidates` top-5, decide, write} → events after commit.
  Inline NOOP bumps `seen_count` and promotes at 2 (`:309`); inline
  judgment band returns without writing (`:101-108`); `baselineSHA` only for
  inline/session (`:67`).
- `internal/memory/crud.go` — `UpdateRecord` (scrub, re-embed, re-baseline,
  `record_updated` source inline + `record_promoted`/`deprecated` via tool),
  `DeprecateRecord`, `ListRecords` (own namespace only). No delete.
- `internal/memory/service.go:83` — `accessible` = own namespace or `global`.
- `internal/memory/events.go:56-61` — `EventSource` inline/session/pr/cleanup;
  `Validate` whitelists them (`:131`).
- `internal/postgres/migrations/0003_events.sql:17` — named
  `events_source_check`; `0001_init.sql:26` — `records.source` has no CHECK.
  Migrations are concatenated in `store.go:33` and split on `;` (`:89`),
  errors containing "already exists" are swallowed (`:96-99`), and
  `migrationSQL` runs at every `serve`/`extract`/`ingest-pr` start — so 0004
  must be plain statements (no `DO $$`, no `;` in comments) and a true
  no-op on re-run.
- `internal/postgres/store.go:106` `Store.Create` and
  `advisory_lock.go:130` `txStoreImpl.Create` (the write path's) — two
  INSERTs; `schema_objects.go` lists expected objects per migration;
  `probe_test.go:57` asserts `MigrationIDs()`.
- `internal/postgres/stats.go:87-96` — candidates created/promoted (no
  source filter); `cmd/claude-memory/stats.go:144` `PromotionRate`.
- `internal/postgres/store.go:353` — `FindCandidates` filters
  `status IN ('candidate','active')`; `Get` loads `embedding`; `List`
  orders `created_at DESC` (`:436`).
- `internal/mcpserver/handlers.go:79-84` — `memory_store` accepts any
  `record.Source.IsValid` value.
- `internal/setup/projects.go:35` — `DecodeProjectName(fsys ReadFS, root,
  name)`; `cmd` already has the read-only `readOnlyFS` adapter.
- `cmd/claude-memory/main.go:121-146` — dispatch switch;
  `buildServiceWithEvents` (`:182`); `scopeSessionService`
  (`extract.go:188`) wires checkout + `CodeHistory` for a cwd.
- `cmd/claude-memory/namespace.go:23,51` — `resolveNamespace`,
  `explainNamespace` (returns `namespace.WhyFallback`).
- `cmd/claude-memory/stats.go:191` — `statsSources` order list.

## Architectural Constraints
- `internal/memory` stays I/O-free; new store calls go through the `Store`
  / `TxStore` ports; `internal/memory/mock` updated with them.
- New package `internal/importer` parses and maps only; it declares its own
  consumer-side ports (file reading, namespace-of-dir, git) and never
  imports `cmd`, `setup`, `postgres` or `gitlog`; the decoder reaches it
  as a `Decode func(name string) (string, bool)` built in `cmd`.
- Adapters are built only in `cmd/claude-memory/main.go`; subcommand files
  receive interfaces, so tests drive them with fakes and a scripted stdin.
- All SQL uses bound parameters.

## Work Items

### WI-1 — Source `import` + migration 0004 (AC-37, AC-38)
- `record.SourceImport`; `IsValid` accepts it. `memory.EventSourceImport`;
  `Event.Validate` accepts it.
- `internal/postgres/migrations/0004_mgmt_import.sql`, statements in this
  order (no `;` inside comments):
  1. `ALTER TABLE events ADD CONSTRAINT events_source_check_v2 CHECK
     (source IN ('inline','session','pr','cleanup','import'))`
  2. `ALTER TABLE events DROP CONSTRAINT IF EXISTS events_source_check`
  3. `ALTER TABLE records ADD COLUMN IF NOT EXISTS import_key TEXT`
  4. `CREATE UNIQUE INDEX IF NOT EXISTS idx_records_import_key ON
     records(namespace, import_key) WHERE import_key IS NOT NULL` (plain,
     not `CONCURRENTLY`).
  Re-run: 1 fails "already exists" (swallowed), 2–4 no-ops.
- `//go:embed` + append to `migrationSQL` with the `";
"` join;
  `schema_objects.go`: 0004 entry (column `records.import_key`, index
  `idx_records_import_key`); `probe_test.go:57` `MigrationIDs` → `0001..0004`.
- `record.Record.ImportKey *string`; **both** `Store.Create` (`store.go:106`)
  and `txStoreImpl.Create` (`advisory_lock.go:130`) add the column only
  when non-nil (shared helper building the column/arg lists).
  `Get`/`List` do not select it.
- Docs note: the new binary auto-applies 0004 at the next session start;
  rollback = reinstall the old binary; no down migration.
- Tests: `record`/`events` unit; integration: `migrationSQL` applied twice
  (no error; `_v2` present, old constraint gone), insert event with
  `source='import'`, `Create` and tx `Create` with and without key.

### WI-2 — Import branch of the write path (AC-23..AC-26)
- `StoreRequest.ImportKey string`; `ActionSkip WriteAction = "SKIP"`;
  `StoreResponse.SkipReason string` (`already imported` /
  `duplicate of <id>`), `StoreResponse.ID` = the duplicate's id on a
  similarity skip.
- `validate`: import ⇔ key; reject `ExtractionDecision` with import; reject
  `ExtractionDecision.Action == SKIP` for every source. All `validate`
  errors wrap a new sentinel `memory.ErrInvalidRequest` (AC-28).
- New `TxStore.ImportKeyExists(ctx, namespace, key) (bool, error)` +
  postgres impl (`SELECT EXISTS … WHERE namespace=$1 AND import_key=$2`, any
  status).
- In the tx, for `Source=import`: after the lock, key exists → SKIP; then
  `FindCandidates`; top similarity ≥ `StoreSimUpdate` → SKIP; else ADD as
  candidate with `ImportKey` set. No events on SKIP. Baseline: unchanged
  code already skips `baselineSHA` for non-inline/session — add a test.
- `mcpserver/handlers.go`: reject `source=import` explicitly.
- Tests (mock store): key hit, sim ≥ threshold, judgment band → ADD,
  below band → ADD, events, validation errors (`errors.Is
  ErrInvalidRequest`), `SKIP` decision rejected, no HEAD stamping, MCP
  rejection. Integration: same item twice → one row; deprecate then
  re-import → SKIP.

### WI-3 — `DeleteRecord` + `Similar` (AC-8, AC-9, AC-16)
- `Store.Delete(ctx, id) error` (port + postgres): `DELETE … WHERE id=$1`;
  first `SELECT id FROM records WHERE superseded_by=$1` → non-empty returns
  `memory.ErrReferenced{IDs}`; 0 rows → `ErrNotFound`. Both in one tx.
- `Service.DeleteRecord(ctx, id)`: `getAccessible`, delete, append
  `record_deleted` (`via=tool`, no source).
- `Service.Similar(ctx, id, limit)`: `getAccessible`; nil embedding →
  `ErrNoEmbedding`; `store.FindCandidates(rec.Embedding, rec.Namespace,
  rec.Repo, limit+1)` minus the record itself.
- Tests: unit (mock) for both; integration: delete referenced → refused,
  delete plain → gone + event.

### WI-4 — CLI scaffolding: namespace, ids, dispatch (AC-1..AC-4)
- `cmd/claude-memory/manage.go`: `mgmtDeps{Svc mgmtService; Stdin
  io.Reader; Out, Err io.Writer; Editor func(path string) error; Now
  func() time.Time}` where `mgmtService` is a consumer-side interface over
  the `memory.Service` methods used (List/Get/Update/Deprecate/Delete/
  Similar/StaleHint/Store/Namespace).
- `--namespace` flag (validated by `namespace.ValidName`) →
  `svc.WithNamespace`, applied **after** `scopeSessionService` (which
  re-scopes to the cwd); default = cwd. `ls`/`show` do not call
  `warnIfFallback`.
- `resolveID(ctx, svc, arg)`: full UUID → `GetRecord`; exactly 8 hex →
  `ListRecords` (own namespace, all statuses) prefix match; ambiguity/none
  errors. Global records only by full UUID (documented in usage).
- `ownNamespace(rec, svc)` guard for mutating commands (AC-2).
- Service built with `buildServiceWithEvents` + `scopeSessionService(cwd)`
  for stale lines. Dispatch: `ls`, `show`, `rm`, `edit`, `promote`,
  `review`, `import` in `main.go` + usage string.
- Tests: prefix resolution table, namespace guard, invalid `--namespace` →
  exit 2.

### WI-5 — `ls`, `show`, `rm`, `promote` (AC-5..AC-9, AC-13)
- `ls`: `ListRecords` with filters; default drops deprecated; `--limit`;
  fixed-width text.
- `show`: all fields + content + stale line (`StaleHint` result →
  `stale: N commits|stale|fresh|unchecked`; unchecked when `Checkout()`
  repo ≠ record repo).
- `rm`: deprecate (AC-7); `--hard` + confirmation / `--yes` →
  `DeleteRecord`; EOF/empty = N; `ErrReferenced` printed (AC-9).
- `promote`: state matrix (AC-13) via `UpdateRecord{Status}`.
- Tests: fake service; output goldens kept small (substring asserts).

### WI-6 — `edit` (AC-10..AC-12)
- `renderEditFile(rec) []byte` / `parseEditFile([]byte) (editFields, error)`
  — pure, round-trip tested.
- Temp file `os.CreateTemp` 0600; editor = `$VISUAL` → `$EDITOR` → `vi`,
  run as `sh -c '<editor> "$1"' sh <path>` with the tty's stdio
  (injected as `Editor` func in tests). Diff vs record → `UpdateRequest`
  with only changed fields;
  clear-list rejection; keep temp file on failure.
- Tests: stub editor rewriting the file; unchanged, each field changed,
  parse errors, non-zero exit, clear tags rejected.

### WI-7 — `review` (AC-14..AC-19)
- `cmd/claude-memory/review.go`: `ListRecords{Status: candidate}` (already
  newest first); per record render (AC-15) + `Similar(…, 3)`; prompt loop
  over `bufio.Scanner(Stdin)`; actions reuse WI-5/WI-6 functions; counters;
  EOF = quit; EOF at the `d` reason / `x` y/N sub-prompt = abort action and
  quit; `x` on `ErrReferenced` adds "use [d]eprecate instead".
- Tests: scripted reader `"a\n\nd\n\nx\ny\nq\n"` etc. over a fake service:
  every key, unknown input re-prompt, failing action stays, `e` re-shows,
  `v` full content, summary line, empty list, EOF at each sub-prompt.

### WI-8 — *Withdrawn (v0.2)*
Decoder move dropped (spec D5, AC-39 withdrawn); `cmd` wires
`setup.DecodeProjectName` into the importer in WI-10.

### WI-9 — `internal/importer` parsers (AC-29, AC-30, AC-33..AC-35)
- `Item{Kind, Title, Content, Repo, Home string; Files []string; Tags
  []string; Key string; Origin string}`, `Skip{Origin, Reason}` — reasons
  are fixed strings, never file content.
- `automem.go`: `ParseAutoMemory(fileName string, data []byte) (Item,
  *Skip)` — `gopkg.in/yaml.v3` frontmatter, type→kind map, title/content
  rules, index-file exclusion.
- `insights.go`: `ParseInsights(data []byte) ([]Entry, []Skip)` — section
  tracking, bullet + continuation, dash variants, first-sentence title,
  backticked file tokens (raw; resolved in WI-10).
- `key.go`: `ImportKey(kind, locator, raw string) string`.
- Tests: table tests on synthetic fixtures under `internal/importer/
  testdata/` (no real memory content).

### WI-10 — Import discovery + run (AC-20..AC-22, AC-27, AC-28, AC-31, AC-32, AC-34, AC-35)
- `importer.Discover*` with consumer ports: `FS` (ReadDir/ReadFile/Lstat/
  EvalSymlinks), `NamespaceOf func(dir) string`, `Toplevel func(dir)
  (string, bool)`, `Decode func(name string) (string, bool)`. Returns
  `[]Item`, `[]Skip` with namespace filtering (AC-22), file resolution
  confined to the toplevel (AC-35), repo rules incl. `*` for non-git homes
  (AC-31, AC-34).
- `cmd` wiring: `NamespaceOf` = `loadNamespaces().Resolve(dir)` (yaml only,
  no `MEMORY_NAMESPACE`); `Decode` = `setup.DecodeProjectName(readOnlyFS{},
  "/", name)`; `Toplevel` = `gitlog.Exec.Resolve`.
- `cmd/claude-memory/import.go`: flags; target-namespace rule incl. the
  fallback refusal (AC-21, via `explainNamespace`). `import` is
  **early-dispatched** in `run()` before `config.Load`; `--dry-run`
  returns there (prints the plan with `scrub.New()`-scrubbed titles and the
  redaction flag; builds no service, needs no env/DSN — AC-27); a real run
  then loads config and calls `buildServiceWithEvents` →
  `WithNamespace(target)` → `Store` per item: `ErrInvalidRequest` → skip
  with reason, other errors → stop, exit 1 (AC-28); summary + "N candidates
  await `claude-memory review` within 30 days".
- Tests: discovery over `t.TempDir()` trees with fake toplevel/namespace
  (incl. `../` and symlink refs escaping the toplevel, non-git home → `*`);
  dry-run with no env file/DSN opens nothing; run loop with fake service
  (added / validation skip / infra stop / reminder line).

### WI-12 — Promotion rate excludes imports (AC-40)
- `internal/postgres/stats.go:87-96`: count candidates created/promoted
  with `source <> 'import'` (events `record_created.source`), plus the
  same pair for `source = 'import'` → new `EventCounts.ImportCreated`,
  `ImportPromoted`.
- `cmd/claude-memory/stats.go`: add `import` to `statsSources`;
  `PromotionRate` unchanged formula over the filtered counts; new line
  `import promotion: promoted/created` (+ JSON field).
- Tests: stats unit (formatting) + integration count query with mixed
  sources. Lands in PR 1 if PR 2 has not added `import` yet (the filter is
  harmless before 0004).

### WI-11 — Docs (all ACs, documentation only)
- `DEPLOY.md` / `integration/INSTALL.md`: the new commands; 0004 is
  auto-applied at the next session start (no manual `migrate`); rollback =
  reinstall the old binary (AC-38); TTL re-import risk and the 30-day
  reminder; run `--dry-run` first; id prefix limits.
- `docs/specs/README.md` item 3 status.

## Order & dependencies
Two PRs:
- **PR 1 (management):** WI-3 → WI-4 → {WI-5, WI-6} → WI-7, plus WI-12;
  WI-11 (management half). No migration.
- **PR 2 (import):** WI-1 → WI-2 → WI-9 ∥ → WI-10; WI-11 (import half).
WI-9 (pure parsers) can start any time.

## AC coverage
| AC | WI |
|---|---|
| 1–4 | 4 |
| 5–9, 13 | 5 (8, 9 also 3) |
| 10–12 | 6 |
| 14–19 | 7 (16 also 3) |
| 20–22, 27, 28 | 10 |
| 23–26 | 2 |
| 29, 30, 33–35 | 9 (34, 35 resolution in 10) |
| 31, 32 | 10 |
| 37, 38 | 1 |
| 40 | 12 |
| 36, 39 | withdrawn |

## Verification
- `go vet ./... && go test ./...` green; `make test-integration` green on a
  local Postgres+pgvector (WI-1/2/3 integration cases).
- Manual on the owner's machine (spec §7): dry-run, real run, re-run
  no-op, INSIGHTS import, one review pass, `stats`.
- Review gates per SDD: plan review ×2 models before WI-1.

## Risks (plan-level)
- `Store` grows a third source-specific branch; keep the import branch in
  its own function (`storeImport`) to avoid tangling inline/session logic.
- Editor needs the real tty; `review`'s `e` must hand stdio over and resume
  the scanner — stub-editor test only; verify once manually.
- Two `Create` INSERTs with a conditional column — one shared helper;
  covered by the WI-1 integration cases.
