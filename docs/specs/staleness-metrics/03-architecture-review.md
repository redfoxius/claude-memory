# Architecture Review — staleness check & usage metrics (pre-implementation, iteration 0)

- **Reviewer:** architecture-reviewer, skills: golang-architecture, security.
- **Target:** `01-spec.md` v0.1 (SPEC-2026-10-01-staleness-metrics, AC-1..AC-31),
  `02-plan.md` (WI-1..WI-14), backlog item 2 in `docs/specs/README.md:35-51`,
  reviewed against the code they will touch at `40a7fb7` (nothing of the
  feature is implemented yet; every code citation is the current baseline).
- **Verified locally:** `go build ./...`, `go vet ./...`,
  `go vet -tags integration ./...` green at `40a7fb7`. No Docker daemon here,
  so no SQL was executed. Every git claim below was checked against real
  `git 2.43` in temp repositories (unborn HEAD, `--count --max-count`,
  non-ancestor sha, change+revert, merge simplification, shallow clone,
  unknown sha, literal pathspecs, absolute path outside the tree).
- **Gate:** **REVISE BEFORE IMPLEMENTING** — 0 blocker, 2 high, 9 medium,
  12 low. The layering, the spool and the event model are sound. The two
  high findings are about *what the staleness signal will say in the
  owner's real workflow* (squash-merged feature branches, uncommitted
  work): as specified, the first weeks of ⚠ markers will be mostly false,
  which is the one outcome that makes the model learn to ignore the marker.
  Both are fixable in the spec with one extra git command each.

## Findings

| Sev | where | Rule | Claim | Recommendation |
|---|---|---|---|---|
| HIGH | `01-spec.md:50,130-136` (AC-1 `sha..HEAD`), `:361-363` (§8 "may over-count. Accepted"), `:190-197` (AC-10 stamps the checkout `HEAD`) | correctness of the signal | AC-10 stamps the `HEAD` the model is working on, which in the owner's flow is a **feature branch**; every PR in `billing-service` is squash/rebase-merged (Azure DevOps). After the merge the stamped sha is not an ancestor of `main`'s `HEAD`, so `sha..HEAD` counts *every* commit on `main` that touches the files since the branch point — verified: a rewritten `C1` gave count 1 for a file never touched on `main` and 8 for one touched 8 times, while `git diff --quiet C1R HEAD -- file` correctly reported "identical". Same for a record stamped on `main` and read from an older feature branch. Once the branch is deleted and gc'd the sha becomes unknown → unchecked. Net effect: records written during feature work turn ⚠ (often `100+ commits`) right after their own PR merges, or go dark. `rev-list` also counts a change-and-revert as 3 (diff: 0) and, without `--full-history`, reports a *simplified* count (2 vs 6 on a merged side branch). | Make the **boolean** come from a tree comparison and the **count** from history: run `git diff --quiet <sha> HEAD -- <files>` (exit 1 = changed; no history walk, immune to rebases, squashes and reverts); only when it says changed, run `rev-list --count --max-count=100 <sha>..HEAD -- <files>` for the number, and if that times out still show `⚠ code changed since this was recorded` without a count. Port: `CodeHistory.Changed(ctx, co, sha, files) (changed bool, commits int, err)` (commits = -1 when unknown). Update AC-1, §2 "Stale count", AC-5 (count optional), §8. Keep the backlog's `git log` as the description of *what commits* the model can look at, not as the detector. |
| HIGH | `01-spec.md:190-197` (AC-10), `:205-211` (AC-12), `02-plan.md:184-189` (WI-6) | correctness of the baseline | The dominant inline flow is: Claude edits `cancel.go`, then calls `memory_store` describing the *new* behaviour, files `[cancel.go]`, **before anyone commits**. AC-10 stamps `commit_sha = HEAD`, which predates the change the record describes. The next commit touches `cancel.go`, so the very first check flags the record stale — a systematic false positive on every record written about uncommitted work. Session extraction at `SessionEnd` has the same problem whenever the user commits after the session (`extract.go:127-167` runs detached, but `HEAD` is still pre-commit). A wrong baseline is worse than none: `NULL` is honest ("unchecked"); a pre-change sha is a lie the hook will repeat on every prompt until someone runs `memory_update`. | Add a **clean-tree guard** to AC-10/AC-12: stamp only when the record's files have no uncommitted changes relative to `HEAD` (`git status --porcelain --untracked-files=all -- <files>` is empty); otherwise leave `commit_sha` unchanged/`NULL` and log at Debug. Port: `CodeHistory.Dirty(ctx, co, files) (bool, error)`; one extra process on the *write* path only (`serve`, `extract --run`), never in the hook. Document in the CLAUDE.md snippet: "pass `commit_sha` explicitly after committing, or call `memory_update` once the change is committed". Add the dirty case to WI-6's test list. |
| MEDIUM | `internal/transcript/parse.go:301-310` (`inferRepo` = `basename(cwd)`), `extraction/processor.go:109` (`tr.Repo`), `01-spec.md:48-49` (R = `basename(toplevel)`) | spec assumption vs code | The spec's glossary assumes a record's `repo` and the checkout's `R` are derived the same way. They are not: the hook uses `git rev-parse --show-toplevel` (`hook.go:142-156`), session extraction uses the transcript `cwd`'s basename. A session started in `billing-service/src` writes `repo = "src"`. Today that already hides those records from the hook's `repo = $2 OR repo = '*'` filter; with this feature AC-10's `repo == R` guard means they are also never stamped and never checked. | In `runExtract` (`extract.go:138-146`) resolve the checkout once (`history.Head(tr.Cwd)`) and pass `co.Repo` into extraction as the repo for every draft (add `Repo string` to `extraction.Config` or a `ProcessSession` parameter; `inferRepo` stays as the fallback when there is no checkout). Add a test: transcript `cwd` in a subdirectory → records carry the toplevel basename. This fixes a pre-existing MVP defect as a side effect; say so in the plan. |
| MEDIUM | `02-plan.md:62-63,148-150` (`buildCodeHistory(sessionID)` in `main.go`), `hook.go:70-77` (`session_id` is read from stdin *inside* `hookCmd`) | composition root | The plan builds the per-session `FileCache(stale-cache/<sid>.json)` in `main.go`, but `main.go` never sees the hook payload; `cmdHook` (`main.go:160-174`) builds the service, then `hookCmd` parses stdin. As written, either `main.go` parses stdin (duplicating `hookCmd`) or `hookCmd` constructs the adapter (violating the "adapters only in main.go" constraint, `02-plan.md:61-63`). | Inject a factory, not an instance: `hookCmd(ctx, cfg, svc, deps hookDeps)` with `hookDeps{ History func(sessionID string) memory.CodeHistory; Events memory.EventSink }`, both built in `main.go` (`History` = `gitlog.Cached(gitlog.Exec{}, FileCache(path))` or `MapCache` for an invalid id). Tests pass fakes and assert `Head` once / `Changed` zero times (AC-9). Also note `hook_test.go:81-123` swaps `os.Stdin`; the new payload field `session_id` fits that harness unchanged. |
| MEDIUM | `01-spec.md:284-292` (AC-24 "a failed insert keeps the file for the next drain"), `02-plan.md:235-239` (500-row batches) | failure mode: poison pill | A deterministic failure (one row rejected by a `CHECK`, a `.draining` file from a future binary with a new enum, a truncated line that still parses but has an invalid UUID) fails the whole multi-row `INSERT`, the file is kept, and every later drain fails the same way forever — while new `.draining` files pile up behind it and the spool keeps being renamed into more of them. Nothing surfaces: drain errors are best-effort by AC-22. | (a) Validate every row in Go before the batch (enums, UUID, non-empty namespace, `at` sane) and drop bad rows as "skipped" — the spec says this (§11) but AC-24 should make it the *only* path to a skipped row. (b) On a batch error whose SQLSTATE is `23xxx`/`22xxx` (constraint/data), fall back to per-row inserts and quarantine the file as `spool.<...>.failed` after the pass; keep-for-retry only on connection-class errors. (c) `cleanup` prints the number of `.draining`/`.failed` files left so a stuck drain is visible. Add a test: one bad row in 3 → 2 inserted, file removed, 1 skipped. |
| MEDIUM | `01-spec.md:198-204` (AC-11), `internal/prsource/prsource.go:45` (`DiffSummary` never set), `azuredevops/client.go:72-79,163-170` (no file list), `extraction/processor.go:41-47,127` (`PRInput` has no files; prompt gets title/description/URL only) | AC-11 adds a sha the check cannot use | A record is "matching" only with ≥ 1 usable file (`01-spec.md:49`). PR drafts get `files` only when haiku infers them from the PR *description*; nothing about the PR's changed paths reaches the prompt. So most PR records will have a merge commit and no files → unchecked. AC-11 as specified is plumbing without payoff. | Either scope AC-11 down ("PR records carry the merge commit for future use; staleness coverage for PR records is expected to be low — `stats` check coverage shows it") or make it pay: fetch the PR's changed paths (`az rest … /pullRequests/{id}/iterations/{n}/changes`, the same `az rest` pattern as `reviewComments`, `client.go:189-203`), put them in `PRInput.ChangedFiles`, include them in the extraction prompt and default a draft's empty `files` to them (capped at 20, AC-3). Second option is S; decide explicitly in §13. Also verify once on a real completed squash PR that `lastMergeCommit.commitId` is on `main` (`git merge-base --is-ancestor <sha> origin/main`) — if ADO reports the preview-merge commit instead, every PR sha is unknown locally and silently unchecked. |
| MEDIUM | `01-spec.md:167-172` (AC-7: fixed 50 ms), `:173-180` (AC-8: timeouts "not cached"), `02-plan.md:16-19` (215–343 ms measured *before* backlog item 7; no post-fix baseline) | hook budget | Two issues. (1) The deadline is a fixed add-on: a prompt already at 280 ms gets +50 ms of git regardless, so AC-25's "≤ 50 ms p95 added" and AC-30's 300 ms p95 cannot both hold unless the search is already comfortably under 250 ms — and the post-item-7 baseline has never been measured. (2) `--max-count=100` bounds the *output*, not the *walk*: for a non-ancestor sha (HIGH #1) `rev-list` walks the whole reachable history before concluding there are no more matches. Because a timeout is never cached and `H` does not change between prompts, the same doomed walk is spawned and killed on every prompt of the session. | (1) Derive the stale deadline from the remaining hook budget: `min(MEMORY_STALE_TIMEOUT_HOOK, hookDeadline − now − 20 ms)`, and measure the baseline *before* WI-4 (make it WI-0: 50 prompts on the current binary). (2) Cache timeouts and errors under the same `(T, H, sha, files)` key with a tri-state value (`fresh / stale(n) / unchecked`); since `H` is in the key, a cached "unchecked" is exact for the same `HEAD` and costs nothing to invalidate. With the `diff --quiet` detector the walk disappears anyway; keep (2) for `rev-list` counts. Also set `cmd.WaitDelay` (Go ≥ 1.20) so a killed `git` cannot hold the hook on pipe close, and `--max-count` can stay as belt-and-braces. |
| MEDIUM | `01-spec.md:306-321` (AC-27 precision proxy), `:476` (§13 #6 "lower bound") | metric validity | "Useful = `feedback(useful)` in `W` at or after the record's first injection in `W`" attributes to cards any `useful` the model gives after a `memory_search` or `memory_get` that had nothing to do with a card, for up to 30 days. The proxy is therefore *not* a lower bound: it is biased **down** by missing feedback and **up** by misattribution, and the two do not cancel. Over a 30-day window with ~400 cards the second term dominates for records that are both searched and injected. | Attribute by time, not by window: a record counts as useful if a `feedback(useful)` for it occurs within `A` after one of its `card_injected` events (`A` = 2 h is generous for one working session; make it a constant, not a flag). Print both `useful_within_2h / distinct_injected` (the proxy) and `useful_any / distinct_injected` so the owner sees the attribution gap. Keep §13 #6's wording but drop "lower bound". |
| MEDIUM | `01-spec.md:389-411` (§9 `at TIMESTAMPTZ`) vs `internal/postgres/migrations/0001_init.sql:30-32` (`records.*_at TIMESTAMP` without zone), AC-27 promotion rate "(at any time after creation)", inventory from `records` | SQL correctness, untestable here | Any stats query that compares `events.at` with `records.created_at` compares `timestamptz` with `timestamp`; Postgres casts the latter using the *session* `TimeZone`, and pgx writes `time.Now().UTC()` into `TIMESTAMP` as a zone-less wall clock. On a laptop in Europe/Kyiv the comparison is off by the UTC offset. This only shows in CI (no Docker here), and only if the fixture crosses the offset. | Compute every event-derived number from `events` alone: promotion rate = `record_created(status=candidate)` in `W` joined to a later `record_promoted` on `record_id` (`e2.at >= e1.at`). Use `records` only for the inventory (no time comparison). State this in AC-27 and WI-7 so the SQL never joins the two tables on time. |
| MEDIUM | `02-plan.md:116-118` (AC-3 normalization in the adapter) vs `:132-134` (service "skips non-matching records (AC-2)"); `01-spec.md:49,144-147` | responsibility split | "No usable file after normalization" is part of the *matching* decision the service makes, but normalization needs `T` and lives in the adapter. Either the service calls the adapter to find out it has nothing to do (an exec for a no-op), or the adapter returns a sentinel the service must know about. Verified that a single bad path aborts the whole git call (`fatal: /etc/passwd is outside repository`), so dropping unusable paths before exec is not optional. | Normalization is pure string/path logic with no I/O: put `memory.NormalizeFiles(T string, files []string) []string` in `internal/memory` (or a tiny `internal/pathspec` package both import). The service decides "matching" with it; the adapter receives already-normalized, non-empty, `T`-relative paths and only execs. The `:N` suffix, absolute→relative, `..` escape and cap-20 table test then runs without git. |
| MEDIUM | `01-spec.md:161-166` (AC-6 "reads `HEAD` fresh for each call") vs `:418-423` (§10 `Checkout{Dir, Repo, Head}` passed once via `WithCheckout`), `02-plan.md:167-172` | interface design | `Checkout.Head` is both a constructor input and something the service is told to refresh per call; a `Checkout` held by a long-running `serve` is stale by definition, and the hook's one-shot `Checkout` is not. Two lifetimes in one struct invite the bug where a cached `H` is used as the key while `HEAD` moved. | `WithCheckout(dir, repo string)` (no `Head`), and `CodeHistory.Head(ctx, dir) (sha string, err)` resolved by the service at the start of each `Search`/`Store`/`Update` that needs it (hook: once per process — same cost as today's `deriveRepo`; serve: ~5 ms per call). The cache key is then always built from a `HEAD` read in the same call. Rename the port method that returns toplevel+repo to `Resolve(ctx, cwd) (Checkout, error)` to keep AC-9's single `rev-parse --show-toplevel HEAD` in the hook (it can return both). |
| LOW | `01-spec.md:181-187` (AC-9 "if `HEAD` cannot be resolved … falls back as today") | adapter detail | Verified: on an unborn repo `git rev-parse --show-toplevel HEAD` **prints the toplevel on stdout and then exits 128**. An adapter that discards stdout on non-zero exit loses `T` and falls back to `basename(cwd)` — fine for the repo name, but it must not treat the exit as "not a git checkout" for other purposes. | Parse stdout line 1 even on exit 128 when it is an absolute path; `Head == ""` means "no checks, no stamping". Add the unborn-repo case to WI-2's tests. |
| LOW | `01-spec.md:148-153` (AC-4 env list) | security / env hygiene | Stripping only `GIT_DIR`/`GIT_WORK_TREE`/`GIT_INDEX_FILE` leaves `GIT_COMMON_DIR`, `GIT_OBJECT_DIRECTORY`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `GIT_NAMESPACE`, `GIT_CEILING_DIRECTORIES`, `GIT_CONFIG_*` — any of which redirect what "HEAD" and "sha" mean if the hook is spawned from inside another git process (plan Risks `:340-341` names exactly that scenario). | Build the child env from a whitelist (`PATH`, `HOME`, `LANG`, `LC_*`, `XDG_CONFIG_HOME`) plus the two overrides; `HOME` is needed for `safe.directory`. Drop the per-variable strip list from AC-4 in favour of "no inherited `GIT_*`". |
| LOW | `01-spec.md:279-283` (AC-23 one `write`), `02-plan.md:333-336` | spool robustness | A torn write (crash, disk full) leaves a partial last line; the next hook's append starts right after it, so one torn line swallows the next good one too. | Prefix each append with `"\n"`: an empty line is skipped for free by the drain, and a torn line can never merge with the next record. Also tolerate `ENOENT` on `rename`/`remove` in the drain (two drains at once is an expected race, `01-spec.md:381-383`). |
| LOW | `01-spec.md:230-233` (AC-16 source-scan test) | test value | A test that greps non-test Go sources for `UPDATE events` is brittle (string formatting, aliases) and guards against something a reviewer sees in any diff. | Drop AC-16's test; keep the sentence "append-only; the only delete is the retention prune" as a constraint. Saves a WI-7 item. |
| LOW | `01-spec.md:389-411` (§9 unnamed `CHECK (type IN (…))`, `CREATE … IF NOT EXISTS` only) | schema evolution | Adding an event type or `via` later needs `ALTER TABLE … DROP CONSTRAINT`, which needs the constraint's name; unnamed CHECKs get generated names that differ per database. `runMigrations` (`store.go:82-100`) splits on `;` and swallows any "already exists" — fine for 0003 as written. | Name the constraints (`CONSTRAINT events_type_check CHECK (…)`, same for `source`, `status`, `outcome`, `via`). Alternatively keep only the Go-side validation (§11) and no CHECKs; the table is a tuning aid, not an audit log (§4). |
| LOW | `01-spec.md:299-305` (AC-26 `stats` drains), `:104-120` (§5: `eval-retrieval`, `ingest-pr` migrate but do not drain) | consistency / least surprise | A report command that writes to the database is unusual; and the drain list is arbitrary (`ingest-pr` runs daily under launchd — a natural drain point — but is excluded). The `extract --run` drain is in AC-24 and nowhere in a Work Item's file list. | Drain in every long-running or scheduled subcommand that already migrated (`serve`, `extract --run`, `ingest-pr`, `cleanup`) and make `stats` print a one-line note "N events still in the spool" instead of draining. Add the `extract.go` drain to WI-9. |
| LOW | `01-spec.md:238-247` (table: `record_promoted via=tool`), `internal/memory/lifecycle.go:70-90` (feedback deprecates regardless of current status) | event semantics | Undefined: `memory_update status=active` on a *deprecated* record (un-deprecate) — promoted? `feedback(outdated)` on an already-deprecated record re-writes the reason; AC-18 says "when the status changed", so the service must compare before/after, which `Feedback` does not do today. | Define: `record_promoted` only for `candidate → active`; `record_deprecated` only for `!deprecated → deprecated`; any other status change emits `record_updated` only. Have `Feedback`/`UpdateRecord` capture `before.Status` and derive events from the transition. |
| LOW | `01-spec.md:205-211` (AC-12 "NOOP never changes it") | missed signal | A session/PR NOOP is haiku confirming, against the current code, that the stored fact still holds — the one moment the baseline could be *advanced* to clear a true-but-no-longer-relevant ⚠ without anyone calling `memory_update`. The inline threshold NOOP (`writepath.go:364-370`, similarity only) is not such evidence. | Re-baseline on NOOP **only** when `req.ExtractionDecision != nil` (explicit decision) and the clean-tree guard passes; keep inline threshold NOOP as "unchanged". Optional; note it in §13 either way. |
| LOW | `02-plan.md:148-150` (`stale-cache/<sid>.json` grows per prompt), `01-spec.md:326-330` (7-day sweep) | hygiene | Entries for old `H` values are dead weight after every commit; the file is loaded and re-written each prompt. | On write, keep only entries whose `H` equals the current `HEAD` (plus the new ones). The 7-day sweep stays. |
| LOW | `cmd/claude-memory/ingestpr.go:84-85` (`ref.Name = basename(repoPath)`), `01-spec.md:49` | foreign-repo detection | PR records match the hook's `R` only when the ingest path's basename equals the user's working clone basename; a worktree (`basename` = worktree dir) or a differently named clone never matches → unchecked, silently. Pre-existing for the `repo` filter; the staleness feature inherits it. | Accept, but document in §8 ("same repo, different directory name → unchecked") and have `stats` check coverage make it visible. Longer term the remote URL is the stable identity (backlog item 4). |
| LOW | `01-spec.md:271-276` (AC-22 events after commit, synchronous in `serve`) | serve latency | Each `memory_store`/`feedback`/`update` gains one Tailscale round trip for `AppendEvents` before the tool returns. Acceptable (§7 has no serve budget for writes), but worth stating. | State it in §7; if it bites, batch through a bounded in-process queue flushed every 2 s and on shutdown — still an adapter concern, not the hook's goroutine problem (§13 #2). |
| LOW | `02-plan.md:71` ("spec §13 #1–#3" lists four decisions), `:99-101` vs `01-spec.md:429` (`SearchRecord.CommitSHA *string` — a search row never needs a pointer), `01-spec.md:166` ("top 10" checks) vs hook's 3 cards | nits | Numbering drift; `*string` where `""` suffices; the 10-record check cap in `memory_search` is fine but should be named as a constant, not buried in AC-6. | Fix on the v0.2 pass. |

## The open questions (§13 #4–#8), with a recommendation each

- **#4 Retention 365 days.** Agree. Volume is ~55 k rows/year (§7), the prune
  is one indexed `DELETE`. Recommend a constant rather than
  `MEMORY_EVENTS_RETENTION` for now (backlog says "S each"; an env var is a
  doc line, a test and a config field). Add it when someone asks.
- **#5 Stamp `commit_sha` at write time.** Yes — without it the staleness
  half covers only inline stores that pass a sha, and the spec is right to
  surface that the backlog's "records already carry `commit_sha`" is false
  for 95 % of records. But stamp **only under the clean-tree guard**
  (HIGH #2) and detect with a **tree diff** (HIGH #1); without both, the
  stamped baselines produce false ⚠ on the first commit and after every
  squash merge. PR: use `lastMergeCommit.commitId`, verified once against a
  real completed squash PR, and decide whether changed paths are fed to
  extraction (M6) — otherwise AC-11 is inert for staleness.
- **#6 Precision attribution.** Adopt with a 2-hour attribution window after
  a `card_injected` for that record, and print the unwindowed number next
  to it (M8). Stop calling it a lower bound. The real fix is behavioural:
  the CLAUDE.md snippet (AC-31) should say "when a card was useful, call
  `memory_feedback(useful)` on it" — the metric is only as good as that
  habit.
- **#7 Threshold "any commit → stale".** Agree, with the detector being
  "content differs", so a touch-and-revert or a rebase does not count.
- **#8 Hook deadline 50 ms.** Fine as the *ceiling*; make it budget-aware
  and cache unchecked results (M7). Measure the post-item-7 baseline
  before WI-4, not after WI-13; if the search alone is already ≥ 250 ms
  p95, the right first move is lowering `Limit` or the embedding call, not
  tuning this deadline.
- **#1–#3 (decided).** Agree with all three: `CodeHistory` as a `memory`
  port with the exec and cache in `internal/gitlog`; the spool over a sync
  `INSERT` (the hook has no post-exit time, and the pre-0003 window would
  otherwise lose events); events after commit, with the TTL CTE as the one
  atomic exception. The rejected alternatives in §13 #2 are the right list;
  one more that was not considered and is also wrong: pipelining the
  `INSERT` with the search query — impossible, the insert needs the result.

## Hook latency walk-through

Today's hook (`main.go:160-174` → `hook.go:70-137`): config file + env,
`postgres.Open` (pool + ping over Tailscale), namespace YAML, one
`git rev-parse` (`hook.go:142-156`), Ollama embed, one hybrid query, encode.
The feature adds, per prompt: read one small JSON cache file; 0–3 `git`
processes in parallel (measured ~5 ms each on a small repo; the `diff
--quiet` form is O(files), the `rev-list` form is O(history) in the worst
case — M7); one atomic cache write when there are new entries; one `stat`
and one `O_APPEND` write for the spool. With a warm cache the add is
single-digit ms; on a miss it is bounded by the stale deadline. That is
consistent with AC-25 **provided** the deadline is carved from the remaining
budget rather than added to it (M7), and provided WI-14 measures against a
baseline taken on the current binary (none exists after backlog item 7).

Failure modes checked against the spec:
- *Spool corruption:* torn line → one or two events lost (L3 fixes the
  second); unparseable JSON → skipped and counted (AC-24). Fine.
- *Concurrent hooks appending:* `O_APPEND` on a local regular file with one
  `write` per process is atomic in practice on Linux/macOS for this size;
  two Claude Code sessions in parallel are the realistic maximum. Fine.
- *Concurrent drains:* `rename` is atomic, the loser gets `ENOENT` and must
  treat it as "nothing to do" (L3). Both may process the same `.draining`
  file: idempotent by `id`. Fine.
- *Disk cap:* 10 MB with silent drop is acceptable for a tuning aid; the
  `serve` startup drain means it is reached only if the owner never starts
  Claude Code, in which case there are no cards either.
- *Old schema:* the hook never touches `events` and the extra columns it
  selects (`files`, `commit_sha`) exist since 0001. Correct. `serve`
  migrates before its drain. Correct. The one gap is the poison-pill file
  (M5).
- *Deadline kill:* `exec.CommandContext` kills `git`, but `Wait` blocks until
  the stdout pipe closes; set `WaitDelay` (M7).

## Git semantics walk-through (verified)

| Case | `rev-list --count sha..HEAD -- files` | `diff --quiet sha HEAD -- files` | Verdict |
|---|---|---|---|
| 2 commits touch a listed file | 2 | changed | both right |
| commits touch only unlisted files | 0 | same | both right |
| change then revert | 3 (false ⚠) | same | diff right |
| merged side branch | 2 (simplified; 6 with `--full-history`) | changed | count is approximate |
| stamped sha rewritten (rebase/squash) | over-count (1 for an untouched file, 8 for a touched one) | correct | **diff right, rev-list wrong** |
| sha unknown / shallow clone | `fatal: bad revision` / `Invalid revision range`, exit 128 | `fatal`, exit 128 | unchecked, as AC-2 says |
| literal pathspec `:(glob)**`, `--output=x` after `--` | treated as paths with `GIT_LITERAL_PATHSPECS=1` | same | AC-4 holds |
| absolute path outside `T` | `fatal: … is outside repository` — **whole call fails** | same | AC-3 must drop it *before* exec (M9) |
| unborn `HEAD` | — | — | `rev-parse` prints `T` then exits 128 (L1) |
| worktree | `--show-toplevel` is the worktree dir; `basename` ≠ repo name | — | unchecked by the `repo == R` rule; pre-existing for `deriveRepo` (L10) |
| monorepo, record from a sub-directory | paths are `T`-relative pathspecs; `cmd.Dir = T` | same | fine once M3 makes `repo` the toplevel basename |

## Events, schema, idempotency

- Schema (§9) is the right shape: ids/enums/numbers only, no FK to
  `records` (deletes must not cascade into metrics), client UUIDs for the
  spool. `session_id` is stored only for `card_injected`, as a validated
  opaque token (AC-8 regex). `namespace` is an identifier, not content;
  AC-14's reflection test is cheap and worth keeping.
- Dual namespace semantics (acting for usage events, owning for lifecycle,
  AC-15) is coherent and `stats --namespace X` stays self-consistent: a
  `global` record injected in `X` contributes `card_injected(X)` and
  `feedback(X)` to `X`, and its `record_created(global)` /
  `record_promoted(global)` both to `global`. Say in AC-27 that
  per-namespace blocks mix "used from" and "owned by" counts on purpose.
- Idempotent import holds: UUIDv4 by the writer, `ON CONFLICT (id) DO
  NOTHING`, the hook never inserts directly, so a spooled event can never
  collide with a direct one. Duplicates across two drains of the same
  `.draining` file are absorbed. Retention delete is the only `DELETE`.
- Write path: collecting events in the `WithTx` closure
  (`writepath.go:73-296`) and appending after `err == nil` (`:298`) gives
  AC-19 for free, including `needs_judgment` (`:304-311`, returns before
  the append) and rollback. `record_promoted(seen)` must be derived from the
  `currentRec.Status == candidate && newSeenCount >= 2` branch
  (`:280-285`), not from the update map.
- TTL CTE (`store.go:486-506` → `WITH d AS (DELETE … RETURNING id,
  namespace) INSERT INTO events … SELECT … FROM d`): count from the INSERT
  equals rows deleted; `gen_random_uuid()` is built in on pg ≥ 13. The
  `ttlDeleter` seam (`cleanup.go:17-19`) is unchanged. Correct and atomic.

## Ports / adapter boundaries

- `internal/memory` stays free of processes, files and env: git only via
  `CodeHistory`, events only via `EventSink`, time via `Clock`. `gitlog`,
  `eventspool` and `postgres` import `memory` for the port types, never the
  reverse. `mcpserver.Service` (`service.go:21-43`) grows `StaleHint`;
  `extraction.StoreWriter` (`processor.go:54-64`) is unchanged. Good.
- Two boundary slips to fix on paper before coding: the hook's adapter
  needs the session id that only the hook sees (M4 — factory port), and
  normalization sits on the wrong side of the port (M9).
- `Service.WithCodeHistory(h, timeout)`: a timeout is policy, fine in the
  service; but prefer `WithCheckout(dir, repo)` without `Head` (M10).
- `serve`'s checkout = `os.Getwd()` (`main.go:115-118`, `serve.go:20-31`)
  is still the unverified assumption from the namespaces review (M6 there).
  It is now load-bearing for AC-6 and AC-12 too. The `repo == R` guard
  makes a wrong cwd *safe* (no stamp, no check) but *inert*; the startup log
  line already prints the namespace — add `checkout=<T>@<H>`.

## Spec ↔ plan ↔ backlog

- **Backlog fidelity.** Everything in item 2 is covered: the three retrieval
  surfaces, `stale_hint` + count, the ⚠ text, the latency cap and
  per-session cache, foreign-repo/missing-sha → no flag; the `events` table
  with the listed event kinds and `stats` with all four metrics; "no
  content". The backlog's `git log --oneline sha..HEAD -- files` is the
  *idea*; HIGH #1 argues the detector must be a tree diff to mean what the
  owner wants ("if any listed file changed since the record was written").
- **Beyond the backlog** (each defensible, together they make "S each" into
  M+L): WI-6 stamping and re-baselining (`memory_update.commit_sha`,
  write-path UPDATE), `lastMergeCommit` plumbing, `MEMORY_EVENTS_RETENTION`
  + the 7-day cache sweep, `stats --namespace`, the AC-16 scan test, the
  AC-14 reflection test, `record_promoted` as its own event. Keep WI-6 (the
  feature is inert without it), `record_promoted` (needed for the promotion
  rate) and `memory_update.commit_sha` (the model's way to re-baseline).
  Cut or defer: AC-16's test (L4), the retention env var (#4), and
  `--namespace` as a flag (the per-namespace breakdown already answers the
  question). Ship as two PRs: **A** staleness (WI-1..6, 14) and **B**
  events + stats (WI-7..12); B does not depend on A's git work, and A is
  what the owner feels.
- **Inconsistencies to fix in v0.2:** AC-6 "fresh `HEAD`" vs §10
  `Checkout.Head` (M10); AC-2/AC-3 split across WI-2/WI-3 (M9); AC-24's
  `extract --run` drain not in any WI (L6); §4 migrating list vs AC-24 drain
  list (L6); `02-plan.md:71` numbering (L12); AC-10's `repo == R` relies on
  `transcript.inferRepo` behaving like `rev-parse` (M3); AC-11's merge
  commit vs no changed files (M6); §13 #6 "lower bound" (M8). Plan WI-7's
  `Stats` signature returns a `memory.StatsReport` — put the report type in
  `memory` only if the service ever computes it; otherwise it is a
  `cmd`-side type returned by a consumer-declared `statsReader`
  (`02-plan.md:251` already says consumer-side; keep the type there too).

## Testability without Docker

- `internal/gitlog` against real temp repos: right call; `git` exists on
  ubuntu runners and here. Set `GIT_AUTHOR_*`/`GIT_COMMITTER_*` and
  `-c init.defaultBranch=main` in the helper so tests do not depend on the
  developer's global config. Add the unborn-HEAD, rewritten-sha,
  change-and-revert and outside-path rows from the table above.
- `internal/memory` with a fake `CodeHistory` (returning canned
  `changed/commits/err` per key, plus a blocking variant) and a recording
  `EventSink`: covers AC-2, AC-7, AC-10 guard, AC-12, AC-18..AC-22 fully.
- `cmd/claude-memory`: `hookCmd` needs the `hookDeps` seam (M4) so the test
  can count `Head`/`Changed` calls and read the spool from a `t.TempDir()`
  `HOME` (`stateDir()` reads `$HOME`, `extract.go:172-175`; use
  `t.Setenv`). Golden tests for the ⚠ suffix (`1 commit`, `2 commits`,
  `100+ commits`, no count).
- Postgres: unit-test the *ratios and formatting* in Go by having the store
  return raw grouped counts (`type×outcome×namespace`, `stale` tri-state
  counts, created/promoted pairs) and computing AC-27/AC-28 in `cmd`; only
  the `GROUP BY` queries, the CTE, `PruneEvents`, `AppendEvents` idempotency
  and the 0003 column set need the CI `integration` job
  (`.github/workflows/ci.yml:19-27` already runs `./internal/postgres/...`).
  That keeps the part most likely to be wrong (ratio edge cases, `n/a`)
  runnable here.

## Known, not re-reported

- `plainto_tsquery` AND semantics (backlog item 5) — the hook's full-text
  leg is still inert, so card volume (and thus `card_injected` counts) is
  lower than it will be after item 5; `stats` trends across that change
  will show a step.
- `serve` cwd assumption and `global`-fallback writers (namespaces review
  M6, L1) — unchanged.

## Blockers before implementation starts (spec v0.2)

1. **HIGH #1** — detector: `git diff --quiet <sha> HEAD -- <files>` for the
   boolean, `rev-list --count` (optional, capped, cached) for the number;
   rewrite AC-1, §2, AC-5, §8.
2. **HIGH #2** — AC-10/AC-12: stamp only when the record's files are clean
   relative to `HEAD`; add `CodeHistory.Dirty`; CLAUDE.md snippet line.
3. **M3** — extraction takes `repo` from the resolved checkout (toplevel
   basename), not `basename(cwd)`.
4. **M4** — hook composition via a `hookDeps` factory port; no adapter
   construction in `hook.go`, no stdin parsing in `main.go`.
5. **M5** — drain: validate rows in Go, per-row fallback and quarantine on
   constraint errors; never a permanently stuck `.draining` file.
6. **M6** — decide AC-11's fate: scope it down honestly or feed changed
   paths to PR extraction; verify `lastMergeCommit` lands on `main`.

Recommended in the same v0.2 pass, not blocking: M7 (budget-aware deadline,
negative cache, `WaitDelay`, baseline measurement first), M8 (attribution
window), M9 (normalization in `memory`), M10 (`Checkout` without `Head`),
and the scope trims (L4, #4 constant, `--namespace`). Split delivery into
staleness (A) and events/stats (B).
