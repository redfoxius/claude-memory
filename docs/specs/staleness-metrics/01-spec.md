# Specification: Claude Memory — Staleness Check & Usage Metrics

## 0. Metadata
- Spec ID: SPEC-2026-10-01-staleness-metrics
- Status: PR A (staleness) implemented and reviewed; PR B (events + stats) not started. Original: ready for implementation (all §13 questions resolved in v0.2)
- Version: 0.2
- Owner: Oleksandr Kolomoiets (user@example.com)
- Supersedes: none (extends SPEC-2026-10-01-memory-mvp and
  SPEC-2026-10-01-namespaces)
- Related: `docs/specs/README.md` backlog item 2 (input, agreed with the
  owner 2026-10-01); `docs/specs/memory-mvp/01-spec.md` (AC-30 hook budget,
  AC-31 silent failure, AC-35 TTL cleanup, AC-46 card fields are refined
  here, not replaced); `docs/specs/namespaces/01-spec.md` (every event
  carries a namespace); `03-architecture-review.md` (review of v0.1; its
  recommendations are adopted below).

## 0.1 Changes in v0.2

All changes come from `03-architecture-review.md` (iteration 0, gate
"revise before implementing"). Finding ids refer to that review (H = high,
M = medium, L = low, in table order).

| # | Decision | Review | ACs / sections |
|---|---|---|---|
| 1 | **Detector is a tree compare.** `stale_hint` comes from `git diff --quiet <sha> <H> -- <files>` (exit 1 = changed). No history walk, so squash/rebase merges, rewritten shas and change-and-revert give the right answer. `git rev-list --count --max-count=100 <sha>..<H> -- <files>` runs only when the diff says "changed", only to print the optional commit count; if it fails or times out the marker is shown without a count. | H1 | §1, §2, §3, AC-1, AC-2, AC-5, AC-6, §8, §10 |
| 2 | **Clean-tree guard on stamping.** `commit_sha` is stamped at write time only when the record's files have no uncommitted changes relative to `HEAD` (`git status --porcelain --untracked-files=all -- <files>` is empty). Otherwise it stays `NULL` (or unchanged on update). New port method `CodeHistory.Dirty`; write path only, never the hook. | H2 | AC-10, AC-12, AC-31, §8, §10 |
| 3 | **Extraction takes `repo` from the resolved checkout** (`basename` of `git rev-parse --show-toplevel`), not from `transcript.inferRepo`'s `basename(cwd)`. `inferRepo` stays as the fallback when the cwd is not a git checkout. Fixes a pre-existing MVP defect (a session started in `billing-service/src` wrote `repo = "src"`). | M3 | new AC-32, §2 |
| 4 | **Hook dependencies via a factory.** `hookCmd` receives `hookDeps{History func(sessionID string) memory.CodeHistory; Events memory.EventSink}` built in `main.go`; `main.go` never parses stdin and `hook.go` never constructs adapters. | M4 | AC-9, §10 |
| 5 | **Spool drain without poison pills.** Every row is validated in Go before insert (the only path to "skipped"); a batch rejected for data/constraint reasons (SQLSTATE class 22/23) falls back to per-row inserts and the file is quarantined as `*.failed`; only transient errors keep a `.draining` file for retry. `cleanup` reports leftover `.draining`/`.failed` files. | M5, L3 | AC-24, AC-23, §11 |
| 6 | **AC-11 scoped down honestly.** PR records carry the provider merge commit for completeness, but PR drafts rarely have `files` (nothing about the PR's changed paths reaches extraction), so staleness coverage for PR records is expected to be low and is shown by `stats` check coverage. Feeding the PR's changed paths into extraction (Azure DevOps iterations/changes) is a **separate, optional, droppable** requirement (new AC-33, plan WI-15). | M6 | AC-11, new AC-33, §13 #5, #10 |
| 7 | **Budget-aware hook deadline.** 50 ms is a ceiling, not an add-on: the stale deadline is `min(ceiling, hookStart + 300 ms − 20 ms)`. Timeouts and errors are negative-cached per session under the same `(T, H, sha, files)` key. Every git process has `WaitDelay` set. A latency baseline is measured on the current binary *before* the hook changes (plan WI-0) and re-measured after. | M7 | AC-4, AC-7, AC-8, AC-25, §7 |
| 8 | **Precision proxy with a 2 h attribution window.** A record counts as useful when a `feedback(useful)` for it occurs within 2 h after one of its `card_injected` events. The unwindowed number is printed next to it. The "lower bound" claim is dropped: the proxy is biased both ways. | M8 | §2, AC-27, §13 #6 |
| 9 | **Promotion rate from `events` only.** `events.at` is `TIMESTAMPTZ`, `records.*_at` is `TIMESTAMP`; no stats query compares the two. `records` is used only for the current inventory. | M9 | AC-27, §9 |
| 10 | **Path normalization moves into `internal/memory`** as a pure function (`memory.NormalizeFiles`). The service decides "matching" with it and hands the adapter only normalized, `T`-relative paths, so one outside path can no longer abort the whole git call. `Checkout` loses `Head`: the hook resolves `HEAD` once and pins it; `serve` reads it at the start of each call. | M9 (normalization), M10 | AC-3, AC-6, AC-9, §10 |
| 11 | **Scope trims.** Event retention is a constant (365 d), not `MEMORY_EVENTS_RETENTION`. The AC-16 source-scan test is withdrawn (append-only stays a constraint, §4). `stats --namespace` is dropped (the per-namespace breakdown answers the question). The stale-cache sweep is kept, with justification (AC-29). | §13 #4, L4 | AC-16 (withdrawn), AC-26, AC-29, §10 |
| 12 | **Two deliveries.** PR A = staleness (plan WI-0..6, 14); PR B = events + stats (WI-7..12). WI-13 (docs) is split between them. B does not depend on A's git work. | "Spec ↔ plan ↔ backlog" | plan |
| 13 | **Smaller fixes adopted.** Child env for git is a whitelist, no inherited `GIT_*` (L2). Each spool append starts with `"\n"` so a torn line never merges with the next one; `ENOENT` on drain rename/remove is "nothing to do" (L3). CHECK constraints are named (L5). Drainers are the subcommands that already migrate and run long or on a schedule (`serve`, `extract --run`, `ingest-pr`, `cleanup`); `stats` does not drain, it prints the spool backlog (L6). Status-transition events are defined exactly (L7). The cache file keeps only entries for the current `HEAD` (L9). Worktree/different-clone-name limits documented (L10). `serve` write latency stated (L11). Unborn `HEAD` parsed correctly (L1). `SearchRecord.CommitSHA` is a `string`; the 10-record cap is a named constant (L12). | L1–L12 | AC-4, AC-6, AC-8, AC-9, AC-13, AC-18, AC-20, AC-23, AC-24, AC-26, §7, §8, §9 |
| 14 | **Deferred:** re-baselining `commit_sha` on an explicit extraction NOOP (L8). Not in this feature; NOOP never changes `commit_sha`. | L8 | AC-12, §12, §13 #9 |

**ACs added:** AC-32 (extraction repo from the checkout toplevel), AC-33
(optional: PR changed paths fed to extraction).
**ACs withdrawn:** AC-16 (append-only source-scan test; the rule itself
moves to §4 as a constraint).
**ACs materially rewritten:** AC-1, AC-3, AC-5, AC-7, AC-8, AC-9, AC-10,
AC-11, AC-12, AC-24, AC-25, AC-26, AC-27, AC-29.

## 1. Overview & Problem

Two gaps show up once the memory has been in use for a while:

- **Silent rot.** A record says "`OrderService.Cancel` retries 3 times". The
  code changed last week. The hook still injects the card and nothing tells
  Claude the code moved. Records have `files[]`, and (once this feature
  stamps it) a `commit_sha`, so git can tell whether those files differ
  between the record's commit and the current `HEAD`.
- **No feedback loop.** There is no way to tell whether cards help, which
  source (inline / session / PR) produces records that get used, whether
  candidates ever become active, or how often cards point at changed code.
  Tuning thresholds (`MEMORY_HOOK_SIM_THRESHOLD`, dedup bands) is guesswork.

This feature adds:
1. A **staleness hint** on retrieval (hook card, `memory_search`,
   `memory_get`): when the current checkout is the record's repo, compare
   the record's files at `commit_sha` with the same files at `HEAD`. If any
   differs, the record is `stale_hint` and the card says
   "⚠ code changed since this was recorded", with the number of commits
   that touched those files when git can count it cheaply.
2. A **commit baseline** at write time, so automatic records have a
   `commit_sha` to compare against (today only inline `memory_store` calls
   that pass it explicitly have one; session and PR writers never set it).
   A baseline is stamped only when it is honest: the record's files are
   committed (clean relative to `HEAD`).
3. An append-only **`events` table** (ids and enums only, no content) and a
   `claude-memory stats` report.

Staleness is a hint. It never hides, demotes or deprecates a record, and a
failed check is never an error.

## 2. Glossary

| Term | Definition |
|---|---|
| Checkout | The git working tree a process works in: toplevel dir `T` and repo name `R = basename(T)` (the same rule as today's `deriveRepo`). Resolved from: hook — payload `cwd`; `serve` — its working directory; `extract --run` — the transcript `cwd`. |
| `H` | The `HEAD` commit sha of the checkout, read in the same call that uses it: once per process in the hook (pinned), at the start of each call in `serve`, at write time on the write path. `""` on an unborn `HEAD` (no checks, no stamping). |
| Usable files | `memory.NormalizeFiles(T, files)` (AC-3): `T`-relative, deduplicated, at most 20. |
| Matching record | A record whose `repo == R` (not `*`), with a valid `commit_sha` and at least one usable file. |
| Stale verdict | `git diff --quiet <commit_sha> <H> -- <usable files>`: exit 0 = fresh, exit 1 = changed, anything else = unchecked. A tree compare: rebases, squash merges and change-and-revert do not produce false positives. |
| Commit count | Optional, display only: `git rev-list --count --max-count=100 <commit_sha>..<H> -- <usable files>`, run only for a "changed" verdict. Approximate (simplified history; over-counts when `commit_sha` is not an ancestor of `H`). `0`, failure or timeout = unknown (no count shown). |
| `stale_hint` | `true` when the stale verdict is "changed". |
| Unchecked | No verdict: not a matching record, no time budget left, timeout, or any git failure. Shown exactly like "fresh" (no marker). |
| Dirty | At least one usable file has uncommitted changes relative to `HEAD` (staged, unstaged or untracked): `git status --porcelain --untracked-files=all -- <usable files>` prints anything. Used on the write path only. |
| Commit baseline | The `commit_sha` stored on a record: a commit at which the record's files had the content the record describes. |
| Event | One row in `events`: a timestamp, a namespace, a type, and ids/enums/numbers only. |
| Spool | `~/.local/state/claude-memory/events/spool.jsonl`: the hook's local append-only event buffer, later moved into `events` (drained). |
| Attribution window `A` | 2 hours (constant). A `feedback(useful)` counts for a card only if it occurs within `A` after a `card_injected` of the same record. |
| Precision proxy | Distinct records injected in the window that got `feedback(useful)` within `A` after one of their injections, divided by distinct records injected (AC-27). Biased down by missing feedback and up by unrelated `useful` feedback inside `A`; compare trends, not absolutes. |

## 3. User Scenarios

### Scenario: Stale card
The owner asks about order cancellation in `billing-service`. The hook finds
"Cancel retries 3 times" (`commit_sha=a1b2c3`, `files=[internal/order/cancel.go]`).
`git diff --quiet a1b2c3 <H> -- internal/order/cancel.go` exits 1 (changed);
`git rev-list --count --max-count=100 a1b2c3..<H> -- internal/order/cancel.go`
gives 2. The card reads
`- [memory] Cancel retries 3 times (repo: billing-service, id: …) ⚠ code changed since this was recorded (2 commits)`.
Claude calls `memory_get` (which also returns `stale_hint: true,
stale_commits: 2`), checks the current code, then calls `memory_update`
with the corrected content. The file is committed, so the update
re-baselines `commit_sha` to the current `HEAD` and the next card has no
marker.

### Scenario: Record written about uncommitted work
Claude edits `cancel.go`, then calls `memory_store` describing the new
behaviour, `files=[internal/order/cancel.go]`, before anything is committed.
`cancel.go` is dirty, so `commit_sha` stays `NULL`: the record is unchecked
rather than flagged stale by the very commit that introduces the change it
describes. Once the change is committed, Claude (per the CLAUDE.md snippet)
calls `memory_update` with the record id (or passes `commit_sha`
explicitly), which stamps the baseline.

### Scenario: Squash-merged feature branch
A record was stamped on `feature/cancel` at `f00d`. The PR is squash-merged
into `main`. On `main`, `git diff --quiet f00d <H> -- cancel.go` exits 0
because the squash commit carries the same content, so no marker is shown.
(`rev-list` would have counted every commit on `main` touching the file
since the branch point; it is never used as the detector.) If the branch
is later deleted and `f00d` is garbage-collected, the sha is unknown →
unchecked.

### Scenario: Foreign repo
The same record is a card while working in `pet-game` (it lives in
`global`, `repo=billing-service`). The checkout is `pet-game`, so no git call
runs and no marker is shown.

### Scenario: Slow prompt
The search already took 290 ms. The stale deadline is
`min(50 ms, hookStart + 300 ms − 20 ms − now)` ≤ 0, so no git process is
started; cached verdicts for this session are still used. The cards are
shown without new markers and hook latency stays within MVP AC-30.

### Scenario: Monthly review
`claude-memory stats --since 30d` (no Ollama needed) prints: 412 cards
injected, 140 distinct records; 21 marked useful within 2 h of a card
(precision proxy 0.15; 37 marked useful at any time in the window, 0.26);
records created by source (inline 9, session 41, pr 63), candidate→active
promotion 22 %, stale-flag rate 7 % of checked cards (check coverage 61 %),
14 TTL deletions, and "0 events still in the spool". The owner raises
`MEMORY_HOOK_SIM_THRESHOLD`.

## 4. Assumptions & Constraints
- Single user, local laptop, Postgres over Tailscale (MVP topology B).
  Events are a relevance/tuning aid, not an audit log.
- `git` is on `PATH` wherever `claude-memory` runs. Without it, every check
  is unchecked and nothing is stamped.
- The owner's PRs (Azure DevOps) are squash- or rebase-merged; feature
  branches are deleted after merge. The detector must be correct under
  rewritten shas (§0.1 #1).
- The hook is a fresh process per prompt with an 800 ms hard timeout and a
  300 ms p95 target (MVP AC-30). It opens Postgres without migrations
  (`postgres.Open`). Claude Code reads the hook's output after the process
  exits, so anything the hook does before exiting counts against the budget.
- Hook payloads carry `cwd` and `session_id`.
- Layering stays as in the MVP and namespaces: `internal/memory` declares
  ports and never runs processes, reads env vars or touches files. Concrete
  adapters (git, spool, Postgres) are built only in
  `cmd/claude-memory/main.go`; inner command code (including `hookCmd`)
  receives ports or factories of ports.
- Migrating subcommands (`serve`, `seed`, `cleanup`, `ingest-pr`,
  `extract --run`, `eval-retrieval`, and the new `stats`) apply migration
  0003. Of those, `serve` (startup), `extract --run`, `ingest-pr` and
  `cleanup` also drain the spool (AC-24).
- `events` is append-only: no code path updates events; the only delete is
  the retention prune (AC-29). (Was AC-16; enforced by review, not by a
  test.)

## 5. Cross-Module Interactions

```
hook ──► deps.History(sid).Resolve(cwd) ─► Checkout{T,R}, H
           │   svc.WithNamespace(ns).WithCheckout(co).WithPinnedHead(H)
           │      .WithStaleDeadline(hookStart+300ms−20ms)
           │        Search → SearchRecord{Files, CommitSHA}
           │        └► annotateStale (≤3 recs, NormalizeFiles, parallel, budget-aware)
           │              └► CodeHistory.Changed (diff --quiet; rev-list count if changed)
           │                   (gitlog.Cached: per-session file, tri-state, keyed by T,H,sha,files)
           └► stdout card (+ ⚠ suffix) ──► deps.Events.Append(card_injected…)  [spool, no DB write]

serve ──► Checkout from wd (no pinned HEAD: Head(T) read per call)
       ──► memory_search / memory_get (+ stale_hint, stale_commits)
       ──► memory_store / update / deprecate / feedback ─► EventSink (postgres) best-effort
       ──► startup: migrate, then eventspool.Drain → postgres.AppendEvents
extract --run ─► Resolve(transcript cwd) ─► repo = R for every draft (AC-32)
              ─► Store stamps commit_sha = H if files clean (Dirty) ─► events; drain
ingest-pr ────► StoreRequest.CommitSHA = PR merge commit (AC-11) ─► events; drain
                [optional WI-15: PR changed paths → extraction → draft files (AC-33)]
cleanup ──────► drain; DELETE … RETURNING → INSERT events (one statement); prune events;
                sweep stale-cache; report leftover spool files
stats ────────► postgres only (no Ollama, no drain): grouped counts over events (+ records
                inventory); ratios computed in Go; prints spool backlog
```

Ports in `internal/memory/ports.go`: `CodeHistory` (git), `EventSink`
(events). Pure helper in `internal/memory`: `NormalizeFiles`. Adapters:
`internal/gitlog` (exec `git`, plus a cache decorator),
`internal/eventspool` (JSONL file), `internal/postgres` (`AppendEvents`,
stats queries, cleanup CTE).

## 6. Functional Requirements

### 6.1 Staleness check
- AC-1 (Event-driven): WHEN a matching record (§2) is returned by the hook,
  `memory_search` or `memory_get` and a checkout with a non-empty `H` is
  known, the system shall compute its stale verdict with
  `git diff --quiet <commit_sha> <H> -- <usable files>` run in `T`
  (exit 0 = fresh, exit 1 = changed) and set `stale_hint = changed`. Only
  for a "changed" verdict it shall then try to get the commit count with
  `git rev-list --count --max-count=100 <commit_sha>..<H> -- <usable files>`
  under the same deadline; a failure, timeout or `0` leaves the count
  unknown and does not change the verdict. Verify (real temp repos, WI-2):
  two later commits touching a listed file → changed, count 2; commits
  touching only unlisted files → fresh; change then revert → fresh;
  `commit_sha` rewritten by a rebase/squash with identical file content →
  fresh; rewritten with different content → changed.
- AC-2 (Unwanted behavior): IF the record's `commit_sha` is missing or not
  `^[0-9a-f]{7,40}$`, OR no file is usable (AC-3), OR `repo` is `*` or
  differs from `R`, OR there is no checkout or `H` is empty, OR `git` is
  missing, OR the sha is unknown locally, OR `git diff` exits with anything
  other than 0 or 1, THEN the record is unchecked: no marker, no error, the
  call's result is otherwise unchanged. The first three conditions are
  decided by the service without calling the adapter. Verify: one
  table-test row per condition (fake `CodeHistory`; zero adapter calls for
  the first three); each yields unchecked and the search/get still
  succeeds.
- AC-3 (Ubiquitous): Files shall be normalized by a pure function
  `memory.NormalizeFiles(T string, files []string) []string` in
  `internal/memory` (no I/O) before any git call: strip a trailing `:N` or
  `:N-M` line suffix; turn an absolute path under `T` into a `T`-relative
  path; drop paths outside `T`, paths escaping `T` via `..`, and empty
  entries; deduplicate; keep at most 20. The adapter receives only the
  result and never re-normalizes, so one bad path can never abort the call
  for the others. Verify: table test in `internal/memory` over these forms
  (no git); adapter test with a record whose original list contained
  `/etc/passwd` and a valid file → the service passes only the valid file.
- AC-4 (Ubiquitous): `git` shall be run via `exec.CommandContext` without a
  shell, with `cmd.Dir = T`, the sha passed only after validation (AC-2),
  every file after a literal `--`, and a child environment built from a
  whitelist (`PATH`, `HOME`, `LANG`, `LC_*`, `XDG_CONFIG_HOME`) plus
  `GIT_LITERAL_PATHSPECS=1` and `GIT_OPTIONAL_LOCKS=0`; no inherited
  `GIT_*` variable reaches the child. `cmd.WaitDelay` shall be set (10 ms)
  so a killed `git` cannot hold the caller on pipe close. Verify: adapter
  test asserts argv, env (a `GIT_DIR` set in the test process is absent in
  the child) and `WaitDelay`; a file named `:(glob)**` or `--output=x` is
  treated literally.
- AC-5 (Event-driven): WHEN the hook injects a card for a stale record, the
  card line shall end with ` ⚠ code changed since this was recorded`
  followed by ` (N commits)` when the count is known (`1 commit` singular,
  `100+ commits` at the cap) and nothing when it is unknown. Nothing else on
  the card changes. This refines MVP AC-46: cards still carry only
  `title`, `repo`, `id`, plus this fixed text and an integer. Verify:
  `renderAdditionalContext` golden tests for fresh, stale (1, 2, 100+, no
  count) and unchecked cards.
- AC-6 (Ubiquitous): `memory_search` items and the `memory_get` record
  shall include `stale_hint: true` when stale, plus `stale_commits: N` when
  the count is known, and omit both otherwise. `serve` resolves its
  checkout (`T`, `R`) from its working directory once at startup and logs
  `checkout=<T>` (or `checkout=none`); the service reads `H` with
  `CodeHistory.Head(T)` at the start of each `Search`/`Get`/`Store`/`Update`
  that needs it, so the cache key always carries a `HEAD` read in the same
  call. At most `maxStaleChecksPerSearch = 10` records per `memory_search`
  call are checked (the top 10). Verify: server tests with a fake
  service/`CodeHistory`; service test: two `Search` calls with a changed
  fake `Head` use different keys.
- AC-7 (State-driven): WHILE checking staleness, all checks of one call
  shall run in parallel under one deadline. The ceiling is
  `MEMORY_STALE_TIMEOUT_HOOK` (default 50 ms) in the hook and
  `MEMORY_STALE_TIMEOUT` (default 500 ms) in `serve`. In the hook the
  deadline is budget-aware: `min(now + ceiling, hookStart + 300 ms − 20 ms)`,
  where `hookStart` is taken at the top of `cmdHook` and 300 ms is the MVP
  AC-30 p95 target; if that is not in the future, no git process is
  started (cached verdicts are still used). Checks unfinished at the
  deadline are unchecked and their processes are killed. Verify: a fake
  `CodeHistory` that blocks returns unchecked within deadline + 10 ms; a
  deadline already in the past starts zero checks but still applies cache
  hits; hook output still has the cards.
- AC-8 (Ubiquitous): Results shall be cached by key
  `(T, H, commit_sha, sorted usable files)` with a tri-state value:
  `fresh`, `stale(n)` (n = 0 when unknown), or `unchecked`. Because `H` is
  in the key, a hit is always exact for that `HEAD`. Timeouts and git
  errors are cached as `unchecked` (negative cache) — except a timeout
  whose deadline was shortened below the ceiling by the hook budget
  (AC-7), which is not cached, so a slow prompt does not suppress the
  check for the rest of the session. Hook: one file per session,
  `~/.local/state/claude-memory/stale-cache/<session_id>.json` (0600; no
  file cache — in-process map only — when `session_id` is empty or not
  `^[A-Za-z0-9_-]{1,64}$`); on write it keeps only entries whose `H`
  equals the current `H`. `serve`: in-process map for the server's
  lifetime. Verify: two hook runs in one session with an unchanged `HEAD`
  call `Changed` once; a new commit (new `H`) forces a recompute and drops
  old entries from the file; a ceiling timeout is not retried on the next
  prompt with the same `H`; a budget-shortened timeout is.
- AC-9 (Ubiquitous): The hook shall get `T`, `R` and `H` from one
  `git rev-parse --show-toplevel HEAD` call (`CodeHistory.Resolve`) that
  replaces today's `deriveRepo` call, so an empty result (no cards) adds no
  git process. On an unborn `HEAD` git prints `T` and exits 128: the
  adapter shall still take `T` from stdout line 1 when it is an absolute
  path and return `H = ""` (every record unchecked, repo still derived
  from `T`). When `cwd` is not a checkout, repo derivation falls back to
  `basename(cwd)` as today. `hookCmd` gets the adapter through
  `hookDeps.History(sessionID)` (§10). Verify: hook test with a fake
  `CodeHistory` factory counts calls: 1 `Resolve`, 0 `Head`, 0 `Changed`
  when no card passes the threshold; adapter test on an unborn repo.

### 6.2 Commit baseline at write time
- AC-10 (Event-driven): WHEN the service has a checkout and stores a record
  with `source` `inline` or `session`, no explicit `commit_sha`, at least
  one usable file, and `repo == R`, THEN it shall read `H`
  (`CodeHistory.Head`) and `CodeHistory.Dirty(T, usable files)` at write
  time, and stamp `commit_sha = H` only when `H` is non-empty and `Dirty`
  returns `false` without error. Otherwise `commit_sha` stays `NULL` and
  one Debug line is logged (ids only). An explicit `commit_sha` always
  wins. `Dirty` runs only on the write path (`serve`, `extract --run`),
  never in the hook. `serve` has its working-directory checkout;
  `extract --run` resolves the transcript `cwd`. Verify: service test with
  a fake `CodeHistory`: stamped when all conditions hold; not stamped for
  `repo="*"`, no usable files, other repo, no checkout, unborn `H`, dirty
  files, or a `Dirty` error; adapter test: a modified tracked file, a
  staged file and an untracked listed file are each dirty; an unrelated
  modified file is not.
- AC-11 (Event-driven, best-effort): WHEN `ingest-pr` stores a record
  extracted from a PR, `commit_sha` shall be the PR's merge commit from the
  provider (Azure DevOps `lastMergeCommit.commitId`, carried as
  `prsource.PR.MergeCommit`); if the provider gives none, `commit_sha`
  stays empty. PR records are never stamped from local `HEAD` (a local
  checkout behind the merge would flag the PR's own changes). **Expected
  coverage is low:** PR drafts get `files` only when haiku infers them from
  the PR description, so most PR records stay unchecked; `stats` check
  coverage shows how many. AC-33 is the optional fix. Verify:
  azuredevops client test parses the field; ingest-pr test asserts
  `StoreRequest.CommitSHA`; manual, once: for a real completed squash PR,
  `git merge-base --is-ancestor <MergeCommit> origin/main` succeeds (if
  Azure DevOps reports a preview-merge commit instead, record that in the
  plan and leave PR records unchecked).
- AC-12 (Event-driven): WHEN a write-path UPDATE or `memory_update` changes
  `content` or `files`, `commit_sha` shall be re-baselined: the explicit
  value (`memory_update` gains optional `commit_sha`), else `H` under the
  AC-10 conditions including the clean-tree guard, else unchanged (never
  cleared). The Postgres adapter accepts `commit_sha` as an update column.
  A NOOP (inline threshold or extraction decision) never changes it.
  `memory_update` with only an explicit `commit_sha` (no content/files
  change) is allowed and sets it. Verify: service tests per path, including
  "files dirty → unchanged"; integration test that `Update` persists
  `commit_sha`.

### 6.3 Events table
- AC-13 (Ubiquitous): Migration `0003_events.sql` shall create `events`
  (§9) with `CREATE … IF NOT EXISTS` only and named CHECK constraints,
  embedded and applied after 0002 by migrating subcommands. Running it
  twice changes nothing. Verify: integration test applies all migrations
  twice; table, indexes and constraints exist once.
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
- ~~AC-16~~ **Withdrawn in v0.2.** The source-scan test for `UPDATE events`
  / `DELETE FROM events` is dropped as brittle; "append-only, the only
  delete is the retention prune" remains a constraint (§4).

### 6.4 What is recorded
Event types (`type`), with `via` values where relevant:

| type | when | fields set |
|---|---|---|
| `card_injected` | hook injects a card | record_id, similarity, stale (true / false / null = unchecked), stale_commits (null when unknown), session_id |
| `feedback` | `memory_feedback` succeeds | record_id, outcome |
| `record_created` | write path ADD, and the new row of SUPERSEDE | record_id, source, status |
| `record_updated` | write path UPDATE (source of the write); `memory_update` (source `inline`), including any status change not listed below | record_id, source |
| `record_superseded` | SUPERSEDE | record_id (old), related_id (new), source |
| `record_deprecated` | status `!deprecated → deprecated` only: `memory_deprecate` (`via=tool`), `memory_update` (`tool`), feedback outdated/wrong (`feedback`) | record_id, via |
| `record_promoted` | status `candidate → active` only: NOOP seen_count (`seen`), feedback useful (`feedback`), `memory_update` (`tool`) | record_id, via |
| `record_deleted` | TTL cleanup (`via=ttl`, source `cleanup`) | record_id, source, via |

Status transitions are derived from the record's status before and after
the operation (captured inside the operation); e.g. `deprecated → active`
via `memory_update` emits `record_updated` only, and `feedback(outdated)`
on an already deprecated record emits `feedback` only.

- AC-17 (Event-driven): WHEN the hook injects cards, one `card_injected`
  event per card shall be recorded (via the spool, AC-23). Verify: hook test
  with a temp spool (`t.Setenv("HOME", …)`): 2 cards → 2 lines with the
  right ids, similarities, stale values and session id.
- AC-18 (Event-driven): WHEN `memory_feedback` succeeds, a `feedback` event
  shall be recorded, plus `record_promoted(via=feedback)` or
  `record_deprecated(via=feedback)` only for the transitions in §6.4.
  Verify: service test with a recording `EventSink`, including feedback on
  an already deprecated record (one event).
- AC-19 (Event-driven): WHEN the write path commits, it shall record the
  events in the table above for ADD, UPDATE, SUPERSEDE (both events) and a
  NOOP that promotes (derived from the `candidate && seen_count ≥ 2`
  branch). A `needs_judgment` response, a plain NOOP and a rolled back
  transaction record nothing. Verify: one service test per action,
  including a failing tx.
- AC-20 (Event-driven): WHEN `memory_update` or `memory_deprecate` succeeds,
  it shall record `record_updated` (for `memory_update`) and, for the
  transitions in §6.4, `record_promoted(via=tool)` or
  `record_deprecated(via=tool)`. Verify: service tests, including
  un-deprecate (`record_updated` only).
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
  `O_APPEND|O_CREATE` (0600, dir 0700) at the spool path. The written
  buffer starts with `"\n"` so a torn previous line can never merge with
  this one (empty lines are skipped by the drain). If the spool is over
  10 MB it appends nothing (one Debug line). Verify: hook test; a stubbed
  10 MB spool is left untouched; a spool ending in a partial line followed
  by an append yields one skipped and all new lines drained.
- AC-24 (Event-driven): WHEN `serve` starts (after migrating), or
  `extract --run`, `ingest-pr` or `cleanup` runs, it shall drain the spool:
  1. rename `spool.jsonl` to `spool.<pid>.<unix-nanos>.draining`
     (`ENOENT` = nothing to do);
  2. for every `*.draining` file whose mtime is ≥ 2 s old: parse each
     line; validate it in Go (`Event.Validate`: known type and enums, valid
     UUIDs, non-empty namespace, `session_id` regex, `at` within
     [now − 400 d, now + 1 h], `stale_commits` in 0..100). Malformed or
     invalid lines are skipped and counted — the only path to a skipped
     row;
  3. insert valid rows in batches of 500 with `ON CONFLICT (id) DO NOTHING`
     (event ids are client-generated UUIDv4). IF a batch fails with a
     data/constraint error (SQLSTATE class 22 or 23, surfaced by the
     adapter as `memory.ErrEventRejected`), THEN the rows of that batch are
     retried one by one and individually rejected rows are counted;
  4. after the pass: no rejected rows → delete the file (`ENOENT` ok);
     rejected rows → rename it to `….failed` (kept for inspection, never
     drained again); any other error (connection, timeout, cancelled
     context) → keep the `.draining` file for the next drain and stop.
  No deterministic error can leave a `.draining` file that every later
  drain retries. Verify: unit tests (fake sink): a drain interrupted after
  inserting re-inserts nothing new on the next drain; a fresh `.draining`
  file is left for later; one rejected row in 3 → 2 inserted, file renamed
  `.failed`, 1 rejected; an invalid enum line → skipped, never sent; a
  transient error keeps the file.
- AC-25 (Ubiquitous): Staleness plus event recording shall add ≤ 50 ms p95
  to the hook, and MVP AC-30 (p95 < 300 ms, 800 ms hard cap) and AC-31
  (silent failure) still hold. Verify: manual measurement over ~50 prompts
  in a real repo **on the current binary before any hook change** (plan
  WI-0, the post-backlog-item-7 baseline that does not exist yet), then the
  same after PR A (with and without stale cards) and after PR B (plan
  WI-14). If the baseline search alone is already ≥ 250 ms p95, lower the
  search cost first (plan WI-0 note); do not tune the stale ceiling to
  compensate.

### 6.6 `claude-memory stats`
- AC-26 (Ubiquitous): `claude-memory stats [--since 30d]` shall build only
  the Postgres store (no Ollama, no embedder), apply migrations, and print
  the report with a total block and one block per namespace. It does not
  drain the spool; it prints `N events still in the spool` (lines in
  `spool.jsonl` plus `*.draining` files) so the owner knows the report may
  lag. `--since` accepts `Nd` or a Go duration (default `30d`). A bad flag
  exits non-zero with a usage message. Per-namespace blocks mix "used
  from" (usage events) and "owned by" (lifecycle events) counts on
  purpose (AC-15). Verify: `cmd` test: dispatch with Ollama unreachable
  succeeds; `--since 7d` and `--since 36h` parse; `--since x` errors; the
  spool note counts a temp spool.
- AC-27 (Ubiquitous): Over window `W = [now − since, now]` the report shall
  show, computed from `events` only (no query compares `events.at` with
  any `records.*_at` column):
  - **cards:** `card_injected` count, distinct records injected;
    **useful (2 h)** = distinct records with a `feedback(useful)` event `f`
    and a `card_injected` event `c` in `W` with `c.at ≤ f.at ≤ c.at + 2 h`;
    **precision proxy** = useful (2 h) / distinct records injected;
    **useful (any)** = distinct injected records with any
    `feedback(useful)` in `W` at or after their first injection in `W`,
    printed with its ratio next to the proxy to show the attribution gap.
  - **feedback** counts by outcome.
  - **records by source:** `record_created` in `W` by source; plus the
    current inventory from `records` by source × status (no time filter).
  - **promotion rate:** of `record_created` events in `W` with
    `status = candidate`, the share whose `record_id` has a
    `record_promoted` event with `at ≥` the creation event's `at`.
  - **stale-flag rate** = cards with `stale = true` / cards with
    `stale IS NOT NULL`; **check coverage** = cards with
    `stale IS NOT NULL` / all cards.
  - **lifecycle:** superseded, deprecated by `via`, TTL-deleted counts.
  The store returns raw grouped counts; ratios and formatting are computed
  in `cmd/claude-memory`. Verify: unit test of the ratio computation on
  fixed counts; integration test seeds a fixed event set (including a
  `useful` 3 h after a card, which counts in "any" but not in "2 h") and
  asserts every raw count.
- AC-28 (Ubiquitous): Ratios with a zero denominator print `n/a`; an empty
  table prints zeros. Verify: unit test of the formatter.

### 6.7 Retention
- AC-29 (Ubiquitous): `cleanup` shall delete events older than 365 days
  (constant `eventsRetention`; no env var), delete
  `stale-cache/*.json` files older than 7 days, print the event count
  deleted on a second line, and, when any exist, a third line
  `spool: N draining, M failed files left`. The prune is not itself an
  event. The cache sweep is kept because the hook creates one cache file
  per Claude Code session and nothing else removes them (thousands per
  year otherwise); it is a glob + mtime check with no configuration.
  Verify: integration test with events at −400 d and −10 d; cmd test for
  the cache-file sweep and the spool line in a temp dir.

### 6.8 Verification and documentation
- AC-30 (Ubiquitous): New SQL (migration 0003, `AppendEvents`, cleanup CTE,
  stats queries, search rows selecting `files`/`commit_sha`, `commit_sha`
  update) shall have `integration`-tag tests under `./internal/postgres/...`,
  run green by the existing CI `integration` job. The staleness SQL ships
  with its tests in PR A, the events SQL in PR B. Verify: green CI run on
  each feature branch.
- AC-31 (Ubiquitous): Docs shall cover: `DEPLOY.md` upgrade (PR B: run
  `claude-memory cleanup` once to apply 0003; the hook keeps working
  before that, its events wait in the spool), the new env vars
  (`MEMORY_STALE_TIMEOUT_HOOK`, `MEMORY_STALE_TIMEOUT`), and
  `claude-memory stats`; the CLAUDE.md snippet: on a ⚠ card, verify the
  record against current code before relying on it, then `memory_update`
  (re-baselines) or `memory_feedback(outdated)`; when a record describes
  uncommitted work, call `memory_update` (or pass `commit_sha`) after
  committing so it gets a baseline; when a card was useful, call
  `memory_feedback(useful)` on it (the precision proxy depends on it).

### 6.9 Added in v0.2
- AC-32 (Ubiquitous): `extract --run` shall resolve the transcript `cwd`
  with `CodeHistory.Resolve` once and use `R` (the toplevel basename) as
  the `repo` of every draft it stores; `transcript.inferRepo`
  (`basename(cwd)`) is used only when the `cwd` is not a git checkout. The
  same checkout is passed to the service for stamping (AC-10). This also
  fixes the MVP behaviour where a session started in a sub-directory wrote
  that sub-directory's name as `repo`. Verify: `cmd` test with a fake
  `CodeHistory`: transcript `cwd = <T>/src` → records carry `basename(T)`;
  non-checkout `cwd` → `inferRepo` result.
- AC-33 (Optional, Event-driven; separate droppable work item): WHEN
  `ingest-pr` processes a PR, the system shall fetch the PR's changed paths
  from Azure DevOps (`az rest` on
  `…/pullRequests/{id}/iterations/{last}/changes`, the same pattern as
  `reviewComments`), carry them as `prsource.PR.ChangedFiles` (at most 20
  after AC-3-style cleanup), include them in the extraction prompt, and use
  them as a draft's `files` when haiku returns none. A failure to fetch
  them leaves the PR processed as today. Dropping this AC leaves AC-11's
  low coverage as documented. Verify: azuredevops client test on a canned
  response; extraction test: empty draft `files` + changed paths → files
  set; non-empty draft `files` kept.

## 7. Non-Functional Requirements
- **Hook latency:** AC-25. The spool append is one syscall; the extra git
  work runs only for injected cards, is bounded by
  `min(50 ms, remaining 300 ms budget − 20 ms)`, and is skipped entirely on
  a warm per-session cache. With the tree-compare detector each check is
  O(files), not O(history).
- **Serve latency:** staleness adds ≤ 500 ms worst case per
  `memory_search`/`memory_get` call (plus one `git rev-parse HEAD`, ~5 ms);
  typical < 30 ms with the cache. Each `memory_store`/`memory_update` with
  a stampable record adds `rev-parse HEAD` + `git status` (~10 ms). Each
  `memory_store`/`feedback`/`update`/`deprecate` adds one synchronous
  `AppendEvents` round trip over Tailscale after commit; acceptable (no
  write budget exists). If it ever matters, a bounded in-process queue in
  the Postgres event adapter can batch it (adapter concern, not in scope).
- **Privacy:** events hold no content (AC-14). Event ids, record ids and
  session ids are not secrets. The spool and cache files are 0600.
- **Security:** sha and files come from the model or haiku; they are
  validated and normalized and passed as argv after `--` with a whitelisted
  environment (AC-3, AC-4, §11). No new SQL built from input; all values are
  bound parameters.
- **Storage:** about 150 rows/day at 50 prompts × 3 cards, ≈ 55 k rows/year,
  a few MB. Retention (AC-29) bounds it.
- **Logging:** never log files, titles or content from the staleness or
  stamping path; ids and counts only.

## 8. Edge Cases
- `commit_sha` rewritten by rebase or squash merge (the owner's normal
  flow): the tree compare is correct; the optional commit count may
  over-count (it counts commits on `H` not reachable from the sha). The
  count is display only.
- `commit_sha` is a descendant of `H` (record stamped on a newer commit,
  read from an older branch): the diff reports "changed" when the files
  differ — correct from the reader's point of view; the count is `0` →
  shown without a count.
- Change then revert: no marker (contents equal).
- Merged side branches: the verdict is exact; the count is a simplified
  count (no `--full-history`).
- Shallow clone, garbage-collected feature-branch sha, or the PR merge
  commit not fetched: unknown revision → unchecked.
- Renamed or deleted file: renames are not followed; a delete is a change
  (stale), which is correct.
- `files` are directories: git pathspecs accept them; any change below
  counts.
- Uncommitted changes at read time: not considered (the verdict compares
  commits). Uncommitted changes at write time: no stamp (AC-10), the
  record stays unchecked until re-baselined.
- Existing records (before this feature) mostly lack `commit_sha` →
  unchecked. Check coverage in `stats` shows how much is checkable.
- PR records: usually no `files` → unchecked (AC-11) unless AC-33 ships.
- `HEAD` moves during a session: the cache key includes `H`, so the next
  call recomputes; old entries are dropped from the hook cache file.
- Unborn `HEAD` (new repo, no commits): repo is derived from `T`; nothing
  is checked or stamped.
- Same repo in a worktree or a clone with a different directory name, or a
  PR ingested from a differently named path: `R` differs from the
  record's `repo` → unchecked, silently (pre-existing for the `repo`
  filter). `stats` check coverage makes it visible. A stable identity
  (remote URL) is backlog item 4.
- Record in `global` whose `repo` equals the current repo: checked like any
  other record.
- Hook before migration 0003: unaffected (it never writes `events`).
- `serve` started outside a git repo: no checkout; nothing is checked or
  stamped; startup log says `checkout=none`.
- Spool never drained (no drainer runs): capped at 10 MB (AC-23).
- Two drains at once: renames are atomic, each claims different files; a
  loser's `ENOENT` is "nothing to do"; the insert is idempotent by event id.
- A spooled event a future binary wrote with a new enum value: invalid for
  this binary → skipped and counted, never a stuck file.
- Clock skew between laptop processes: none (same machine); `at` is set by
  the writer (`now()` for direct inserts and the cleanup CTE, the client
  time for spooled events).

## 9. Data Model

```sql
CREATE TABLE IF NOT EXISTS events (
    id            UUID PRIMARY KEY,               -- client-generated (idempotent drain)
    at            TIMESTAMPTZ NOT NULL,           -- never compared with records.*_at (TIMESTAMP)
    namespace     TEXT NOT NULL,
    type          VARCHAR(32) NOT NULL
                  CONSTRAINT events_type_check CHECK (type IN ('card_injected','feedback',
                    'record_created','record_updated','record_superseded',
                    'record_deprecated','record_promoted','record_deleted')),
    record_id     UUID,                           -- no FK: records may be deleted
    related_id    UUID,                           -- superseding record
    source        VARCHAR(20)
                  CONSTRAINT events_source_check CHECK (source IN ('inline','session','pr','cleanup')),
    status        VARCHAR(20)
                  CONSTRAINT events_status_check CHECK (status IN ('candidate','active','deprecated')),
    outcome       VARCHAR(20)
                  CONSTRAINT events_outcome_check CHECK (outcome IN ('useful','outdated','wrong')),
    via           VARCHAR(20)
                  CONSTRAINT events_via_check CHECK (via IN ('tool','feedback','seen','ttl')),
    similarity    REAL,
    stale         BOOLEAN,                        -- null = unchecked
    stale_commits SMALLINT,                       -- null = unknown
    session_id    VARCHAR(64)                     -- Claude Code session id (hook only)
);
CREATE INDEX IF NOT EXISTS idx_events_at ON events(at);
CREATE INDEX IF NOT EXISTS idx_events_type_at ON events(type, at);
CREATE INDEX IF NOT EXISTS idx_events_record ON events(record_id);
```

Named constraints let a later migration add an enum value with
`ALTER TABLE events DROP CONSTRAINT events_type_check, ADD CONSTRAINT …`.
Go-side validation (§11) is the primary check; the CHECKs are the backstop.

`records`: no schema change. `commit_sha` becomes updatable (adapter
whitelist) and gets stamped (§6.2).

## 10. Interfaces
- **Ports and helpers** (`internal/memory/ports.go`, `staleness.go`):
  ```go
  // Checkout identifies a working tree; it carries no HEAD.
  type Checkout struct{ Dir, Repo string } // Dir = toplevel T, Repo = basename(T)

  type CodeHistory interface {
      // Resolve: one `git rev-parse --show-toplevel HEAD`; head == "" on an unborn HEAD.
      Resolve(ctx context.Context, cwd string) (co Checkout, head string, err error)
      // Head: `git rev-parse HEAD` in co.Dir, for long-running callers.
      Head(ctx context.Context, dir string) (string, error)
      // Changed: diff --quiet verdict; commits = rev-list count when changed, 0 = unknown.
      Changed(ctx context.Context, co Checkout, head, sha string, files []string) (changed bool, commits int, err error)
      // Dirty: `git status --porcelain --untracked-files=all -- files` non-empty. Write path only.
      Dirty(ctx context.Context, co Checkout, files []string) (bool, error)
  }

  func NormalizeFiles(toplevel string, files []string) []string // pure, AC-3

  type EventSink interface { Append(ctx context.Context, evs ...Event) error }
  var ErrEventRejected = errors.New("event rejected") // data/constraint error, AC-24
  ```
  `files` passed to `Changed`/`Dirty` are always `NormalizeFiles` output.
  `Service.WithCheckout(Checkout) *Service` (HEAD read per call via `Head`),
  `Service.WithPinnedHead(head string) *Service` (one-shot processes: the
  hook pins the `H` from `Resolve`),
  `Service.WithCodeHistory(CodeHistory, ceiling time.Duration) *Service`,
  `Service.WithStaleDeadline(time.Time) *Service` (hook budget, AC-7),
  `Service.WithEvents(EventSink) *Service` (defaults: no checkout, no-op
  sink). `SearchRecord` gains `Files []string`, `CommitSHA string`,
  `Stale *StaleHint` (`StaleHint{Commits int}`, 0 = unknown); new
  `Service.StaleHint(ctx, *record.Record) *StaleHint`. Constant
  `maxStaleChecksPerSearch = 10`.
- **Hook composition** (`cmd/claude-memory`):
  ```go
  type hookDeps struct {
      History func(sessionID string) memory.CodeHistory // gitlog.Cached(Exec, FileCache|MapCache)
      Events  memory.EventSink                          // eventspool.Sink (PR B)
  }
  func hookCmd(ctx context.Context, cfg *config.Config, svc *memory.Service, deps hookDeps, start time.Time) error
  ```
  Both fields are built in `main.go` (`cmdHook`); `hookCmd` parses stdin,
  validates `session_id`, and calls `deps.History(sessionID)`.
- **MCP:** `memory_search` items and `memory_get` gain optional
  `stale_hint` (bool) and `stale_commits` (int). `memory_update` gains
  optional `commit_sha`.
- **Hook card:** suffix per AC-5.
- **CLI:** `claude-memory stats [--since 30d]`. `cleanup` prints a second
  line (events pruned) and, when non-zero, a third (spool leftovers).
- **Env:** `MEMORY_STALE_TIMEOUT_HOOK` (50ms ceiling),
  `MEMORY_STALE_TIMEOUT` (500ms). Retention is a constant (365 d).
- **Files:** `~/.local/state/claude-memory/events/spool.jsonl`,
  `…/events/spool.*.draining`, `…/events/spool.*.failed`,
  `…/stale-cache/<session_id>.json`.
- **prsource:** `PR.MergeCommit string`; optional (AC-33)
  `PR.ChangedFiles []string`.
- **extraction:** the checkout's repo is passed in (AC-32), e.g.
  `extraction.Config.Repo` (falls back to `tr.Repo`).

## 11. Untrusted Inputs
- `commit_sha` and `files` come from the model (inline) or haiku
  (session/PR). They reach only `git` argv, validated, normalized and after
  `--`, with literal pathspecs and a whitelisted environment (AC-3, AC-4).
  They are never executed, used in SQL text, or written to events.
- `session_id` from the hook payload: validated (AC-8) before use in a file
  name or an event.
- Spool lines are parsed as JSON into `memory.Event` and validated in Go
  (`Event.Validate`, AC-24) before insert; the named table CHECKs are the
  backstop. Bad lines are skipped and counted; rows the database still
  rejects quarantine their file as `.failed`.
- PR changed paths (AC-33, optional) come from Azure DevOps and go through
  the same normalization as any `files`.

## 12. Out of Scope
- Ranking, demoting or auto-deprecating stale records; showing *which*
  commits changed (Claude can run `git log` itself).
- Staleness for `memory_list`, for `repo="*"` records, or from a different
  checkout of the same repo under another directory name.
- Flagging records because of uncommitted changes at read time; following
  renames.
- Re-baselining `commit_sha` on an extraction NOOP (review L8; deferred).
- Events for search queries, `memory_get` reads, NOOPs that do not promote,
  or the hook's below-threshold results.
- Dashboards, export, or remote metrics; per-repo breakdown (repo names are
  not stored in events); a `stats --namespace` filter.
- A configurable event retention.
- Backfilling `commit_sha` on existing records (could be a later
  `review` CLI action, backlog item 3).

## 13. Clarifications Log

All questions are resolved as of v0.2. Answers marked "review" adopt the
recommendation of `03-architecture-review.md`.

| # | Question | Answer | Impacted AC |
|---|---|---|---|
| 1 | Where is git invoked? | **Resolved (spec-creator; confirmed by review):** a `CodeHistory` port declared in `internal/memory`, implemented by a new `internal/gitlog` adapter (exec `git`) built only in `main.go`; caching is a decorator in `gitlog`, so `memory` stays free of processes, files and env. v0.2: path normalization is a pure function in `memory` (AC-3); the hook gets the adapter through a `hookDeps` factory (§10). | AC-1..AC-9 |
| 2 | How does the hook record events without blowing its budget? | **Resolved (spec-creator; confirmed by review):** local spool (one `O_APPEND` write, < 1 ms), drained by migrating, long-running or scheduled subcommands (`serve`, `extract --run`, `ingest-pr`, `cleanup`). Rejected: sync INSERT (+1 Tailscale round trip on an already tight budget; fails before 0003 is applied), goroutine fire-and-forget (killed at process exit), detached child process (exec + new connection, hard to test), same transaction (the read path has none), pipelining the insert with the search (the insert needs the result). | AC-17, AC-23..AC-25 |
| 3 | Events in the write transaction or after it? | **Resolved (spec-creator; confirmed by review):** after commit, best-effort, for every service path. Metrics must never fail or roll back a write; a lost event in a crash window is acceptable for a proxy metric. Exception: TTL cleanup writes its events in the delete statement (atomic and free). | AC-19, AC-21, AC-22 |
| 4 | Retention? | **Resolved (review):** 365 days, pruned by `cleanup`, as a constant. A `MEMORY_EVENTS_RETENTION` env var is not added until someone needs it. | AC-29 |
| 5 | Stamp `commit_sha` at write time? | **Resolved (review):** yes, session/inline from the checkout `HEAD` **only when the record's files are clean relative to `HEAD`** (AC-10), re-baselined on content/file updates under the same guard (AC-12), with the tree-compare detector (AC-1). PR records use the provider merge commit (AC-11), with low expected coverage stated; feeding changed paths is optional AC-33. `lastMergeCommit` is verified once against a real squash PR. | AC-1, AC-10..AC-12, AC-33 |
| 6 | Precision proxy attribution | **Resolved (review):** a record counts as useful if a `feedback(useful)` for it occurs within 2 h after one of its `card_injected` events; the unwindowed "useful (any)" is printed next to it. The proxy is not a lower bound (biased down by missing feedback, up by misattribution); compare trends. The CLAUDE.md snippet asks for `memory_feedback(useful)` on useful cards. | AC-27, AC-31 |
| 7 | Stale threshold | **Resolved (owner, backlog item 2; refined by review):** any change to a listed file → stale, where "change" means the content at `H` differs from the content at `commit_sha` (tree compare), so a touch-and-revert or a rebase does not count. | AC-1 |
| 8 | Hook staleness deadline | **Resolved (review):** 50 ms is the ceiling; the effective deadline is carved from the remaining 300 ms budget (AC-7); timeouts and errors are negative-cached per session (AC-8); `WaitDelay` on git processes (AC-4). The post-item-7 baseline is measured before the hook changes (plan WI-0) and again after (WI-14). | AC-4, AC-7, AC-8, AC-25 |
| 9 | Re-baseline on an extraction NOOP? (review L8) | **Resolved: deferred.** NOOP never changes `commit_sha` in this feature. Candidate follow-up: re-baseline only on an explicit extraction decision with the clean-tree guard. | AC-12 |
| 10 | Make PR records checkable? (review M6) | **Resolved:** AC-11 stays, scoped down and best-effort; feeding changed paths is optional AC-33 in its own work item (plan WI-15), droppable without affecting anything else. | AC-11, AC-33 |
| 11 | Which subcommands drain; does `stats` drain? (review L6) | **Resolved (review):** `serve` (startup), `extract --run`, `ingest-pr`, `cleanup` drain; `stats` only reports the spool backlog. | AC-24, AC-26 |
| 12 | Delivery | **Resolved (review):** two PRs — A staleness, B events + stats (plan). | all |

## 14. Acceptance Criteria Summary (Definition of Done)

PR A (staleness):
- [ ] AC-1 — stale verdict via `git diff --quiet sha H -- files`; optional `rev-list` count (cap 100)
- [ ] AC-2 — missing sha/files, foreign repo, unborn HEAD, git failure → unchecked, never an error
- [ ] AC-3 — `memory.NormalizeFiles` (pure; line suffix, absolute → relative, drop outside, dedupe, max 20)
- [ ] AC-4 — safe git invocation (no shell, `--`, literal pathspecs, whitelisted env, `WaitDelay`)
- [ ] AC-5 — ⚠ suffix on stale hook cards, count optional; AC-46 refined
- [ ] AC-6 — `stale_hint` / `stale_commits` on `memory_search` and `memory_get`; HEAD per call in serve
- [ ] AC-7 — one parallel deadline per call; hook deadline budget-aware (ceiling 50 ms), serve 500 ms
- [ ] AC-8 — tri-state cache keyed with HEAD, negative-cached timeouts; per-session file (hook), in-process (serve)
- [ ] AC-9 — one `git rev-parse` in the hook via `hookDeps.History`; unborn HEAD handled; no extra git without cards
- [ ] AC-10 — stamp `commit_sha` from HEAD (inline/session) only when files are clean
- [ ] AC-11 — PR records use the provider merge commit (best-effort; low coverage expected)
- [ ] AC-12 — re-baseline on content/file updates under the clean guard; `memory_update.commit_sha`
- [ ] AC-25 — baseline measured before; ≤ 50 ms p95 added; MVP AC-30/31 still hold (re-checked after PR B)
- [ ] AC-30 — integration tests green in CI (staleness SQL)
- [ ] AC-31 — docs (staleness half: env vars, CLAUDE.md snippet)
- [ ] AC-32 — extraction `repo` from the checkout toplevel

PR B (events + stats):
- [ ] AC-13 — migration 0003, idempotent, named constraints
- [ ] AC-14 — no content in events
- [ ] AC-15 — every event has a namespace (usage: acting; lifecycle: record's)
- [x] ~~AC-16~~ — withdrawn (append-only is a §4 constraint)
- [ ] AC-17 — `card_injected` per injected card
- [ ] AC-18 — feedback events (+ promoted/deprecated on exact transitions)
- [ ] AC-19 — write-path events; none on rollback / needs_judgment
- [ ] AC-20 — `memory_update` / `memory_deprecate` events
- [ ] AC-21 — TTL deletes evented in the same statement
- [ ] AC-22 — event failures never change an operation's result
- [ ] AC-23 — hook spools events (leading `\n`), never writes them to Postgres; 10 MB cap
- [ ] AC-24 — idempotent rename-based drain; Go validation; per-row fallback; `.failed` quarantine
- [ ] AC-26 — `stats` without Ollama, `--since`, no drain, spool backlog line
- [ ] AC-27 — metric definitions (2 h attribution, events-only promotion rate)
- [ ] AC-28 — zero denominators → `n/a`
- [ ] AC-29 — 365 d retention constant, cache sweep, spool leftovers line in `cleanup`
- [ ] AC-30 — integration tests green in CI (events SQL)
- [ ] AC-31 — docs (events half: DEPLOY upgrade, `stats`)

Optional (separate work item, droppable):
- [ ] AC-33 — PR changed paths fed to extraction as default `files`
