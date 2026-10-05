# Specification: Claude Memory — Management CLI & Import

## 0. Metadata
- Spec ID: SPEC-2026-10-02-mgmt-cli
- Status: v0.2 — plan review (Opus) PASS WITH FIXES; fixes applied, owner
  answered §9
- Version: 0.2
- Owner: Oleksandr Kolomoiets
- Input: `docs/specs/README.md` backlog item 3 ("management CLI (`review`) +
  import of existing knowledge"); builds on `namespaces/01-spec.md` and
  `staleness-metrics/01-spec.md` (events, stale hint).
- Owner constraint: **minimal**. Line-oriented CLI only.

## 0.1 Changes in v0.2 (plan review)
Migration 0004 re-run-safe (D2, AC-37); `import_key` also in the tx
`Create` (AC-38); validation errors are per-item skips (AC-28); home-dir
namespace from `namespaces.yaml` only (AC-22); `--namespace` applied last
(AC-1); scrubbed dry-run titles (AC-27); non-git home → repo `*` (AC-31,
AC-34); imports excluded from the promotion rate (new AC-40, D1); `SKIP`
never accepted as a decision (AC-26); EOF = abort/N (AC-8, AC-17); file
refs confined to the toplevel (AC-35); `$EDITOR` via `sh -c` (AC-10); id
prefix limits (AC-3); dry-run early dispatch (D7). Post-implementation review: AC-28 reminder prints the configured candidate TTL instead of a fixed 30 days; migrations run under an advisory lock; INSIGHTS paths are symlink-resolved before keys are built. **Withdrawn:** AC-36
(commit-by-date), AC-39 (decoder move). §9 answered: proposed defaults.

## 1. Problem

Today the only way to look at or fix memory is through the MCP tools inside
a Claude session. Candidates pile up (they are TTL-deleted after 30 days
untouched) and nobody approves them, so the candidate→active rate in
`stats` says little. Also, knowledge that already exists — Claude Code
auto-memory files and per-module `INSIGHTS.md` logs — is not in memory.

This feature adds:
1. `claude-memory ls | show | rm | edit | promote` — direct record management.
2. `claude-memory review` — a weekly ~2-minute pass over `candidate` records.
3. `claude-memory import automem | insights` — one-off import of existing
   knowledge as `candidate` records, idempotent.

Every write goes through `memory.Service` (scrubbing, embedding, dedup,
advisory lock, events). `cmd/` never writes to the store directly.

## 2. Glossary

| Term | Definition |
|---|---|
| Target namespace | `--namespace NAME` if given, else `resolveNamespace(cwd)` (`cmd/claude-memory/namespace.go`). Every command reads and writes only this namespace. |
| Short id | First 8 hex chars of a record UUID, as printed by `ls`/`review`. |
| Auto-memory file | `~/.claude/projects/<encoded-dir>/memory/<name>.md` with YAML frontmatter (`name`, `description`, `metadata.type`). `MEMORY.md` is the index, not a memory. |
| INSIGHTS entry | A top-level bullet `- YYYY-MM-DD — text…` (em dash, en dash or `-`) plus its indented continuation lines, under a `## ` section of a file named `INSIGHTS.md` (any case). |
| Import key | `<kind>:` + first 32 hex of `sha256(locator + "\0" + whitespace-collapsed raw text)`; `kind` ∈ `automem`, `insights`. Stored in `records.import_key`. |
| Locator | automem: `<encoded-dir>/<file name>`; insights: `<repo>/<toplevel-relative path of the INSIGHTS file>`. |
| Home dir | The directory an imported item belongs to: the decoded project dir (automem) or the git toplevel of the INSIGHTS file. Its namespace is `resolveNamespace(home)`. |

## 3. Key Decisions

| # | Decision | Why |
|---|---|---|
| D1 | **New source `import`** (`record.SourceImport`, `memory.EventSourceImport`). | Reusing `inline` routes imports through the inline NOOP branch (`seen_count++`, auto-promotion at 2, breaking "start as candidate") and the judgment band (write silently skipped), and without a distinct source `stats` could not keep imports out of the candidate→active promotion rate (bulk imports reviewed in one pass would distort it) nor report them separately (AC-40). `session` would ADD a duplicate on every re-run. |
| D2 | **Migration 0004** (statements in this order): `ADD CONSTRAINT events_source_check_v2 CHECK (source IN (…, 'import'))`, `DROP CONSTRAINT IF EXISTS events_source_check`, `ADD COLUMN IF NOT EXISTS records.import_key TEXT`, plain `CREATE UNIQUE INDEX IF NOT EXISTS idx_records_import_key … (namespace, import_key) WHERE import_key IS NOT NULL`. `records.source` has no CHECK. `migrationSQL` runs on every `serve`/`extract`/`ingest-pr` start, so 0004 must be a true no-op on re-run: the second ADD fails "already exists" (swallowed by the runner), the DROP is then a no-op; no DROP+ADD of one name. The new binary auto-applies 0004 at the next session start; rollback = reinstall the old binary (it ignores the column, index and v2 constraint); no down migration. | The events CHECK rejects unknown sources (`0003_events.sql:17`). The key gives a re-run no-op that survives edits and deprecation; similarity alone cannot (`FindCandidates` ignores deprecated rows, and an edited record drifts). |
| D3 | **`rm` deprecates; `rm --hard` deletes** after a y/N confirmation. | Deprecation is reversible, keeps the import key (so a re-import stays a no-op), events and `superseded_by` links; hard delete is for junk or leaked secrets. |
| D4 | CLI mutations (`rm`, `edit`, `promote`, review actions) emit events like the MCP tools: `source=inline`, `via=tool`. | The CLI is an explicit owner action with the same authority as `memory_update`/`memory_deprecate`; no new `via` value, no extra CHECK change. |
| D5 | **Decoder stays in `setup`.** `cmd` passes `setup.DecodeProjectName` (with the read-only `readOnlyFS` adapter, root `/`) to the importer as a `Decode func(name string) (string, bool)` port. (v0.1's move to `internal/claudeproj` is withdrawn.) | The importer never imports `setup`; `cmd` already does. No code move, one tested decoder. |
| D6 | Imports never use `ExtractionDecision`, never NOOP/UPDATE/SUPERSEDE: outcome is ADD (as candidate) or SKIP. | A one-off import must not change existing records. |
| D7 | `import … --dry-run` is **early-dispatched** in `run()` before `config.Load` (like `namespaces`/`doctor`): no env file, DSN, DB or Ollama needed. It reports what would be submitted, not the dedup outcome. | Usable before the setup works; dedup needs embeddings. |

## 4. User Scenarios

**Weekly review.** In `~/work/acme/billing-service`, the owner runs
`claude-memory review`. It shows `[1/7]` — the newest candidate, its stale
line, content, and 2 similar records. `a` approves it (→ active), `d` + Enter
deprecates the next ("rejected in review"), `s` skips one, `q` stops. The
summary prints `approved 1, deprecated 1, skipped 1`.

**First import.** `claude-memory import automem --dry-run` lists 6 files to
import, 2 skipped (`MEMORY.md`, `type user`), 3 in another namespace. The
real run adds 5 and skips 1 as `duplicate of 3f2a91c0`. A second run prints
`skipped (already imported)` for all 5.

**INSIGHTS.** `claude-memory import insights ~/work/pet-game
--namespace pet-game` walks three `INSIGHTS.md` files; each dated entry in
"What Works" becomes a `pattern` candidate with `files` from its
`` `src/platform/sse.ts:63-68` `` references (no `commit_sha`: unchecked
for staleness).

## 5. Requirements (EARS)

### 5.1 Common
- **AC-1** The system shall scope every subcommand in this spec to the
  target namespace. When `--namespace` is given and is not a valid name
  (`namespace.ValidName`), the command shall exit 2. `--namespace` is
  applied after `scopeSessionService` (which re-scopes to the cwd), so it
  always wins. `ls` and `show` shall not print the fallback-namespace
  warning.
- **AC-2** When `rm`, `edit`, `promote` or a review action targets a record
  whose namespace is not the target namespace (including a `global` record
  visible through the read fallback), the command shall refuse with
  "record is in namespace X; use --namespace X" and exit 1.
- **AC-3** Commands taking an id shall accept a full UUID or an 8-hex
  short id (other lengths are not prefixes; use the full UUID). A short id
  is matched only against the target namespace's records, so a `global`
  record is addressed by full UUID only. When a short id matches
  more than one record, the command shall list the matches and exit 1; when
  none, print "not found" and exit 1.
- **AC-4** The system shall perform every write through `memory.Service`
  built with events enabled; `cmd/claude-memory` shall not call the store's
  write methods.

### 5.2 `ls`, `show`
- **AC-5** `ls` shall print the target namespace's records newest first
  (`created_at` desc), one per line: short id, status, kind, source, repo,
  created date, title (truncated to 80 runes). By default it shall show
  `candidate` and `active` only; `--status S`, `--kind K`, `--repo R` filter;
  `--limit N` (default 50, 0 = all).
- **AC-6** `show ID` shall print every field of the record (kind, status,
  source, repo, namespace, tags, files, commit_sha, ticket, confidence,
  seen/used counts, timestamps, deprecation reason, superseded_by) and the
  full content, plus a stale line: `stale: N commits` / `stale` / `fresh` /
  `unchecked`, computed with `Service.StaleHint` against the cwd checkout
  (unchecked when the record's repo is not the cwd checkout).

### 5.3 `rm`
- **AC-7** `rm ID [--reason TEXT]` shall deprecate the record through
  `DeprecateRecord` (reason default `removed via CLI`). When the record is
  already deprecated, it shall print "already deprecated" and exit 0.
- **AC-8** `rm --hard ID` shall ask `delete <short id> "<title>" permanently? [y/N]`
  (`--yes` skips the prompt; EOF or empty input = N) and, on `y`, delete
  the row through a new
  `Service.DeleteRecord`, which emits `record_deleted` with `via=tool`.
- **AC-9** When another record's `superseded_by` references the record,
  `DeleteRecord` shall return `ErrReferenced` with the referencing ids and
  delete nothing; the CLI shall print them and exit 1 (in review: print
  them plus "use [d]eprecate instead").

### 5.4 `edit`
- **AC-10** `edit ID` shall write the record to a 0600 temp file as a header
  (`title:`, `tags:` comma-separated, `files:` comma-separated), a `---`
  line, then the content; open `$VISUAL`, else `$EDITOR`, else `vi`, run
  as `sh -c '<editor> "$1"' sh <path>` (so editors with arguments work and
  the path is never shell-parsed); and parse the file when the editor
  exits 0.
- **AC-11** When nothing changed, `edit` shall print "no changes" and write
  nothing. Otherwise it shall send only the changed fields through
  `UpdateRecord` (scrub, re-embed, re-baseline, `record_updated`). An
  empty `tags:` or `files:` line when the record had values shall be
  rejected ("cannot clear tags/files") because `UpdateRequest` treats an
  empty list as "unchanged".
- **AC-12** When the editor exits non-zero or the file does not parse
  (missing `---`, empty title or content), `edit` shall write nothing, keep
  the temp file, print its path, and exit 1. On success the temp file is
  removed.

### 5.5 `promote`
- **AC-13** `promote ID` shall set a `candidate` to `active` through
  `UpdateRecord{Status: active}` (emits `record_promoted`, `via=tool`). For
  an `active` record it shall print "already active" and exit 0; for a
  `deprecated` record it shall refuse and exit 1.

### 5.6 `review`
- **AC-14** `review` shall walk the target namespace's `candidate` records
  newest first. With none, it shall print "no candidates" and exit 0.
- **AC-15** For each record, review shall print `[i/N]`, short id, kind,
  repo, source, age, seen/used counts, the stale line (AC-6), tags, files,
  the first 15 content lines (`… N more lines` when truncated), and up to 3
  similar records (short id, status, similarity to 2 decimals, title).
- **AC-16** Similar records shall come from a new `Service.Similar(ctx, id,
  limit)`: `FindCandidates` with the record's stored embedding, same
  namespace and repo (or `*`), the record itself excluded; no Ollama call.
  A record without an embedding shows `similar: unavailable`.
- **AC-17** Review shall prompt
  `[a]pprove [e]dit [d]eprecate [x]delete [s]kip [v]iew [q]uit >` and read one
  line from stdin: `a` = AC-13; `e` = AC-10..12, then show the same record
  again; `d` = ask a reason (Enter = `rejected in review`), then AC-7;
  `x` = AC-8 confirmation and delete; `s` or Enter = next; `v` = print the
  full content and prompt again; `q` or EOF = stop. EOF at the `d` reason
  or `x` y/N sub-prompt aborts that action (nothing written) and stops.
  Any other input re-prompts.
- **AC-18** When an action fails, review shall print the error and prompt
  again on the same record. On exit it shall print counts of approved,
  edited, deprecated, deleted and skipped records.
- **AC-19** Review shall read plain lines from any stdin (no raw mode, no
  TUI library), so it can be scripted and tested with a reader.

### 5.7 Import — common
- **AC-20** `import automem [--projects-dir DIR]` and
  `import insights [PATH...]` shall both accept `--namespace` and `--dry-run`.
- **AC-21** When no `--namespace` is given and the cwd resolves only through
  the built-in fallback (`namespace.WhyFallback`), import shall refuse with
  "no namespace mapping for <cwd>; pass --namespace" and exit 2. Import
  shall write to `global` only with an explicit `--namespace global`.
- **AC-22** When an item's home dir resolves to a namespace other than the
  target, import shall skip it with reason `other namespace (X)`. The home
  dir's namespace comes from `namespaces.yaml` only
  (`loadNamespaces().Resolve(dir)`), ignoring `MEMORY_NAMESPACE`.
- **AC-23** Each item shall be stored through `Service.Store` with
  `Source=import`, its import key, status `candidate`, confidence 0.5, and
  tag `imported`; a stored item emits `record_created` with
  `source=import`, `status=candidate`.
- **AC-24** Inside the write transaction (after the advisory lock), when a
  record in the target namespace (any status) already has the import key,
  `Store` shall return decision `SKIP` (reason already imported) and write
  nothing: no event, no `seen_count` change.
- **AC-25** Otherwise, when the top candidate's similarity is
  ≥ `StoreSimUpdate`, `Store` shall return `SKIP` with that candidate's id
  and write nothing; else it shall ADD (the judgment band also ADDs;
  review shows the similar records).
- **AC-26** `Store` shall reject `Source=import` without an import key, an
  import key with any other source, and `ExtractionDecision` with
  `Source=import`, and an `ExtractionDecision` with action `SKIP` for any
  source (`SKIP` is an outcome, never an input). The MCP `memory_store`
  tool shall reject `source=import`.
  The service shall not stamp a `HEAD` baseline on import records.
- **AC-27** `--dry-run` shall connect to neither Postgres nor Ollama and
  write nothing. It shall print, per item, kind, repo, the **scrubbed**
  title, file count, import-key prefix and `would redact: yes|no`
  (from the scrubber), every skip with its reason, and a header saying
  dedup is not evaluated. Skip and parse reasons shall name the file and a
  fixed reason only, never file content.
- **AC-28** A real run shall print one line per item —
  `added <short id>`, `skipped (<reason>)` or `error: …` — and a summary.
  A file that does not parse, and a `Store` validation error (wrapped
  `memory.ErrInvalidRequest`, e.g. oversized content), is a per-item skip
  with a reason. Only a DB or embedding error stops import (exit 1; a
  re-run continues where it stopped, by AC-24). After a run that added
  N > 0 records it shall print
  "N candidates await `claude-memory review`; unreviewed candidates are
  deleted after <TTL> days (MEMORY_CANDIDATE_TTL)", with <TTL> from
  `cfg.CandidateTTL` (default 180 days), not a fixed number.

### 5.8 Import — auto-memory
- **AC-29** Import shall read `<projects-dir>/*/memory/*.md` (default
  `~/.claude/projects`), regular files only, non-recursive, and shall never
  import a file named `MEMORY.md` or `CLAUDE.md` (any case).
- **AC-30** From the YAML frontmatter it shall take `description` (title,
  trimmed, ≤ 160 runes), the body after the frontmatter (content; the
  description when the body is empty), and `metadata.type` (or a top-level
  `type`): `feedback`→`convention`, `project`→`decision`,
  `reference`→`pattern`. `user`, an unknown or missing type, or missing
  frontmatter/description shall be skipped with a reason. Tags:
  `imported`, `auto-memory`.
- **AC-31** The project dir name shall be decoded with
  the `Decode` port (`setup.DecodeProjectName`, existence-checked; ambiguous
  or undecodable → skip `undecodable project dir`). Repo = basename of the
  git toplevel containing the decoded dir, else `*` (retrieval matches
  `repo = $repo OR '*'`). No `files`, no `commit_sha`.

### 5.9 Import — INSIGHTS.md
- **AC-32** Each PATH may be a file or a directory (default: the cwd's git
  toplevel). A directory is walked for files named `INSIGHTS.md` (any
  case), skipping `.git`, `node_modules`, `vendor`, without following
  symlinks.
- **AC-33** Import shall parse entries (glossary) per `## ` section and map
  sections to kinds: `What Works`, `Codebase Patterns` → `pattern`;
  `What Doesn't Work`, `Recurring Errors & Fixes`, `Tool & Library Notes`
  → `gotcha`. Entries in any other section (incl. `Open Questions`,
  `Session Notes`) shall be skipped with reason `section <name>`.
- **AC-34** Title = the entry's first sentence with the date stripped
  (≤ 160 runes); content = the entry text plus a final line
  `(imported from <toplevel-relative path>, <date>)`; repo = basename of
  the toplevel, or `*` when the file is not in a git checkout (then the
  locator and the provenance line use the absolute path, and `files` is
  empty); tags `imported`, `insights`.
- **AC-35** Files = backticked tokens of the form `path` or
  `path:N[-M]` that name an existing regular file relative to the INSIGHTS
  file's directory or the toplevel **and stays inside the toplevel** after
  `filepath.Clean`/`EvalSymlinks` (anything else is dropped), stored
  toplevel-relative with the line suffix kept, deduplicated, at most 20.
  Imported records have no `commit_sha`.
- **AC-36** *Withdrawn (v0.2).* Commit-by-date baselines are dropped;
  `commit_sha` stays NULL for imports.

### 5.10 Schema, source, decoder
- **AC-37** Migration 0004 shall, in order, add
  `events_source_check_v2` (the 0003 list plus `'import'`), drop
  `events_source_check` if it exists, add `records.import_key TEXT`, and
  create the partial unique index (not `CONCURRENTLY`), with no `;` inside
  comments. Applying `migrationSQL` again (every service start) shall
  change nothing and return no error.
  `record.Source.IsValid` and `Event.Validate` shall accept `import`.
- **AC-38** `postgres.Store.Create` and the transaction store's `Create`
  (`txStoreImpl`, used by the write path) shall include `import_key` in the
  INSERT only when it is set, so non-import writes keep working on a
  database that has not run 0004. `stats` shall list `import` among the
  sources.
- **AC-39** *Withdrawn (v0.2).* The decoder stays in `internal/setup`
  (D5).
- **AC-40** `stats`' promotion rate shall count only candidates created
  with a source other than `import`; `stats` shall print imported
  candidates' promotion separately (`import promotion: promoted/created`).

## 6. Out of Scope (YAGNI)
- TUI frameworks, raw-mode keys, colours, paging; `review` is plain lines.
- `export`, backup/restore, bulk edit/approve, `ls --json`, search from the
  CLI (use `memory_search`), editing `kind`, `repo` or `namespace`,
  un-deprecate.
- Web UI, MCP changes beyond rejecting `source=import`.
- Importing `CLAUDE.md`, `MEMORY.md`, AGENTS.md, skills, transcripts, or
  any other format; continuous/scheduled sync of auto-memory.
- Keeping the original date as `created_at`; mapping auto-memory `user`
  entries; following INSIGHTS correction lines to supersede older entries.
- Moving records between namespaces; `--all-namespaces`.
- `commit_sha` baselines for imported records (commit-by-date).

## 7. Verification
- Unit (`go test ./...`): id prefix resolution; `ls` ordering/filters;
  edit file round-trip and every AC-11/12 branch with a stub editor;
  promote/rm state matrix; review driven by a scripted reader covering every
  key, EOF, failed action, summary; `Service.Store` import branches
  (key hit, similarity ≥ threshold, judgment band → ADD, validation
  errors, no HEAD baseline, events); `DeleteRecord` + `ErrReferenced`;
  automem frontmatter/type/skip table; INSIGHTS parser on a fixture with
  every section, continuation lines, dash variants, file refs;
  `Decode` port fed by `setup.DecodeProjectName` in a temp tree; MCP
  rejects `source=import`; validation error → skip, infra error → stop;
  `SKIP` as decision rejected; EOF at every sub-prompt; stats promotion
  excludes imports.
- Integration (`make test-integration`): `migrationSQL` applied twice
  without error, `events_source_check` gone and `_v2` present;
  event with `source=import` accepted; import of the same item twice →
  one row (key written through the tx `Create`); deprecate then re-import
  → still one row; hard delete of a superseded target refused; `Create`
  without import key on the new schema.
- Manual (owner's machine): `import automem --dry-run`, then a real run,
  then a second run (all `already imported`); `import insights` on
  `pet-game`; one `review` pass; `stats` shows `import` in
  created-by-source.

## 8. Risks
- **TTL cleanup** deletes unreviewed imported candidates after the candidate
  TTL (`MEMORY_CANDIDATE_TTL`, default 180 days), and a later re-import
  re-creates them (the key went with the row). Review soon after importing;
  import prints the configured TTL (AC-28); documented in DEPLOY.md.
- **Imported candidates are live immediately.** Candidates are searchable and
  injected into sessions (flagged unverified) before review, so run
  `--dry-run` first and review soon; the database is shared.
- **Similarity skip** (≥ 0.85) can hide an import that adds a nuance to an
  existing record; the skip line names the record so the owner can edit it.
- **Personal preferences** in auto-memory `feedback` files may not be team
  knowledge; they arrive as candidates and review rejects them.
- **Migration 0004** is auto-applied by the new binary at the next session
  start (no separate `migrate` step); it takes short exclusive locks on
  `events` and `records` (plain index build — the tables are small).
  Rollback = reinstall the old binary: it ignores the column, index and
  `_v2` constraint and never writes `import`. No down migration.
- **Scrubbing is pattern-based**; `--dry-run` shows `would redact` so the
  owner can inspect suspicious files before the real run.

## 9. Owner Decisions (answered 2026-10-02, proposed defaults taken)
1. Auto-memory `type: user` is skipped.
2. INSIGHTS `Open Questions` / `Session Notes` are skipped.
3. `rm --hard` asks y/N; `--yes` bypasses; EOF or an empty answer = N (a non-TTY stdin that is at EOF therefore means N; piped `y` is honored).
4. `created_at` = import time (the store sets it anyway).
5. TTL re-import is documented; import prints the configured TTL reminder.
