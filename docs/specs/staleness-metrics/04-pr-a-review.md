# Implementation Review — staleness (PR A, iteration 1)

- **Reviewer:** architecture-reviewer (post-implementation pass on the
  staleness half only; the events/stats half, PR B, is not implemented and
  not reviewed).
- **Target:** branch `claude/task-rb9lio`, fixed range `40a7fb7..e897439`
  (code only, `:!docs`), against `01-spec.md` v0.2 (AC-1..AC-12, AC-25
  baseline half, AC-30/AC-31 staleness parts, AC-32), `02-plan.md`
  (WI-1..WI-6, WI-13a) and `03-architecture-review.md` (iteration 0). Line
  numbers are at `e897439`. At review time the working tree was identical to
  `e897439` (`git status --porcelain` and `git diff e897439 --stat` both
  empty), so no PR B file influenced any result below.
- **Verified locally (this review ran them):** `go build ./...`,
  `go vet ./...`, `go vet -tags integration ./...`, `go test -race ./...` —
  all green, 17 packages `ok`, no failures. Three throwaway probe tests were
  run with `go test -overlay` (files injected from the scratchpad, the tree
  untouched) to confirm the AC-7 and AC-9 findings; their outcomes are
  quoted where used. The `integration`-tag suite compiles but cannot run
  here (no Docker); SQL claims are from reading the statements. Git claims
  were checked against the package's own real-repo tests on git 2.43.
- **Gate:** **FAIL** — 0 high, 6 medium, 9 low. The detector, the clean-tree
  guard, the env/pathspec hardening, the tri-state cache and the MCP surface
  are implemented as specified and well tested; every iteration-0 blocker
  that belongs to PR A (HIGH #1, HIGH #2, M3, M4, M6 scope-down) is fixed in
  code. The gate fails on (a) two small but spec-visible defects in the
  hook's budget behaviour (AC-9: git runs for results that never become
  cards; AC-7: cached verdicts are discarded, 500/500 times, when the budget
  is already spent), (b) `extract --run` pinning HEAD for the whole
  extraction, which re-opens the iteration-0 HIGH #2 false-positive window,
  and (c) the AC-25 baseline (WI-0) and several tests the spec's Verify
  clauses name not existing yet.

## Blockers (before PR A is marked done)

1. **MEDIUM — hook: `Changed` runs for results that never become cards
   (AC-9).** `internal/memory/service.go:142` annotates every search row
   before `hookCmd` applies `HookSimThreshold`
   (`cmd/claude-memory/hook.go:151-161`). A prompt whose top-3 results all
   fall below the threshold still spawns up to three `git diff` processes
   (plus a cache-file read and write) and outputs nothing. Spec AC-9: "an
   empty result (no cards) adds no git process"; Verify: "0 `Changed` when
   no card passes the threshold". `TestHook_NoCardNoChangedCalls`
   (`hook_test.go:432-442`) asserts `resolve=1 head=0` and conspicuously
   not `changedCalls`; the overlay probe with that exact setup printed
   `resolve=1 head=0 changed=1`.
   **Fix:** add `SearchRequest.MinSimilarity float64` and have
   `annotateStale` skip rows with `Similarity < req.MinSimilarity` (the hook
   passes `cfg.HookSimThreshold`; `serve` passes 0), or export
   `Service.AnnotateStale(ctx, recs)` and call it from the hook after the
   threshold filter. Add `h.changedCalls != 0` to the existing test.

2. **MEDIUM — service: a spent budget discards cache hits (AC-7).**
   `internal/memory/staleness.go:246`:
   `grace := time.NewTimer(time.Until(deadline) + 10*time.Millisecond)`.
   When the hook budget is already in the past (spec §3 "Slow prompt"),
   `time.Until(deadline)` is negative, the timer fires immediately, and the
   `select` at `:248-257` returns before any goroutine can deliver the
   `Cached` hit. Spec AC-7: "if that is not in the future, no git process
   is started (cached verdicts are still used)"; plan WI-3: "past deadline →
   only cache hits". Overlay probe (adapter answers instantly with
   `changed=true`, deadline −1 s): hits dropped 500/500. The two existing
   tests cannot see it: `TestAnnotateStale_HookBudgetDeadlineIsAnUpperBound`
   (`staleness_test.go:209-221`) uses an adapter that returns `ctx.Err()`,
   and `TestHook_BudgetAlreadySpentStillRendersCards` (`hook_test.go:460-484`)
   asserts only that nothing is flagged.
   **Fix:** `grace := time.NewTimer(max(time.Until(deadline), 0) + 10*time.Millisecond)`
   (a cache lookup, including the first `FileCache` read, is sub-millisecond;
   10 ms is ample). Add the probe as a test: instant adapter + past deadline
   → `Stale` set.

3. **MEDIUM — `extract --run` pins HEAD for the whole extraction and
   builds the adapter itself.** `cmd/claude-memory/extract.go:150-157`
   constructs `gitlog.Exec{}` inside `runExtract`, resolves once, and
   `WithPinnedHead(head)`. Two problems. (a) Spec §2 defines `H` as read
   "at write time on the write path" (pinned only in the hook); AC-10 says
   "read `H` … at write time". Extraction runs detached at `SessionEnd` for
   seconds to minutes (haiku calls per draft) — exactly when the owner
   commits. If a commit lands between `Resolve` and a draft's `Store`,
   `Dirty` (which is relative to the *current* index/HEAD) says clean and
   the record is stamped with the *pre-commit* `H`; the first hook check
   then flags it — the iteration-0 HIGH #2 false positive, re-introduced
   through pinning. (b) `02-plan.md` "Architectural Constraints":
   "Adapters … are constructed only in `cmd/claude-memory/main.go`"; spec
   §4 says the same. `extract.go` is package `main` but it is the inner
   command, the analogue of `hook.go`, which the plan went out of its way
   (M4, `hookDeps`) to keep adapter-free. The direct consequence is that
   AC-32's Verify ("`cmd` test with a fake `CodeHistory`: transcript
   `cwd = <T>/src` → records carry `basename(T)`; non-checkout `cwd` →
   `inferRepo` result") cannot be written — and is not.
   **Fix:** drop the `WithPinnedHead` call (the service then reads `Head`
   once per `Store`, one `rev-parse` ≈ 5 ms next to a multi-second haiku
   call); pass the adapter in — `runExtract(cfg, path, history
   memory.CodeHistory)` with `cmdExtract` in `main.go` supplying
   `gitlog.Exec{}` (or a package-level `newCodeHistory` variable stubbed in
   `TestMain` like `resolveNamespace`) — and add the two AC-32 cmd tests
   with a counting fake.

4. **MEDIUM — manual items the Definition of Done requires are not
   recorded.** `02-plan.md:3` still reads "Status: not started"; WI-0 has no
   p50/p95 (`:170-176`), WI-14 none; the AC-11 one-time check
   (`git merge-base --is-ancestor <MergeCommit> origin/main`, `:306-308`)
   has no result. Spec AC-25 requires the baseline "on the current binary
   before any hook change" — the hook change has landed, but the
   measurement is still possible on a binary built from `40a7fb7`. Without
   it the "≤ 50 ms p95 added" claim cannot be made and
   `MEMORY_STALE_TIMEOUT_HOOK` cannot be tuned honestly.
   **Fix:** build `40a7fb7`, run the ~50-prompt protocol, record p50/p95 in
   WI-0; repeat on `e897439` (with and without stale cards) for WI-14;
   record the `merge-base` result; update the plan status line.

5. **MEDIUM — tests the plan's WI-6 and the spec's Verify clauses name do
   not exist.** `internal/memory/baseline_test.go` covers ADD
   (`:33-82`), explicit-wins (`:84-98`), no-git-in-tx (`:100-114`) and
   `UpdateRecord` (`:129-175`). Missing: write-path UPDATE re-baselines when
   `req.Files` is set and clean, and leaves `commit_sha` unchanged when
   dirty (`writepath.go:184-193`); SUPERSEDE stamps the new row
   (`:239-241`); NOOP never touches `commit_sha` (`:279-309`;
   `grep commit_sha internal/memory/writepath_test.go` is empty). Plus the
   AC-9 assertion (blocker 1), the AC-7 cache-hit test (blocker 2) and the
   AC-32 cmd tests (blocker 3).
   **Fix:** three table rows in `baseline_test.go` driving `Store` through
   `mockTxStore` with an `ExtractionDecision` for UPDATE/SUPERSEDE/NOOP and
   asserting the `updates` map / created record.

## Findings (severity ranked)

| Sev | file:line | Rule / AC | Claim | Recommendation |
|---|---|---|---|---|
| MEDIUM | `internal/memory/service.go:142`; `cmd/claude-memory/hook.go:143-161`; `hook_test.go:432-442` | AC-9, AC-25 | Staleness is annotated on all search rows before the hook's similarity filter; below-threshold results cost git processes and cache writes and produce no card. Probe: `changed=1` with no card. | Blocker 1. |
| MEDIUM | `internal/memory/staleness.go:246-257` | AC-7 | Negative grace duration when the budget is already spent → return before cache hits arrive (500/500 dropped). | Blocker 2. |
| MEDIUM | `cmd/claude-memory/extract.go:150-157` | AC-10, spec §2 (`H` at write time), §4 / plan constraints (adapters in `main.go`), AC-32 Verify | HEAD pinned for a long-running extraction (pre-commit baseline after a mid-extraction commit); adapter constructed outside the composition root; no seam for the AC-32 cmd test. | Blocker 3. |
| MEDIUM | `02-plan.md:3,170-176,306-308,330-335` | AC-25 (baseline half), AC-11 manual check | No WI-0 baseline, no WI-14 numbers, no `merge-base` result; plan status stale. | Blocker 4. |
| MEDIUM | `internal/memory/baseline_test.go`; `writepath_test.go` | AC-12 Verify, plan WI-6 tests | Write-path UPDATE/SUPERSEDE/NOOP baseline behaviour untested. | Blocker 5. |
| MEDIUM | `internal/memory/writepath.go:184-193` | AC-12 | A write-path UPDATE whose `req.Files` is empty never re-baselines, even when `content` changed and the existing record has clean files: `baselineSHA` is computed from `req.Files` before the tx (`:63-70`), and `Candidate` (`ports.go:148-159`) carries no `Files`, so the target's own files are unknown until inside the lock. Spec: "WHEN a write-path UPDATE … changes `content` or `files`, `commit_sha` shall be re-baselined … else `H` under the AC-10 conditions". `UpdateRecord` does it right (`crud.go:117-125` uses `existing.Files`). Session-extraction UPDATEs often carry no files, so this is the common path. | Either (preferred) after `WithTx` returns nil with `decision == ActionUpdate && len(req.Files) == 0`, `Get` the record and, if `baselineSHA(existing.Files) != ""`, issue one best-effort `store.Update(commit_sha)` outside the lock; or state the deviation in AC-12 ("content-only extraction UPDATEs keep their baseline"). Test either way. |
| LOW | `internal/memory/staleness.go:174`; `cmd/claude-memory/hook.go:129`; `extract.go:155` | AC-9 ("unborn HEAD … every record unchecked, no extra git") | `pinnedHead == ""` means "not pinned", so on an unborn HEAD the hook pins `""` and `currentHead` runs a second `git rev-parse HEAD`. Probe: `resolve=1 head=1`. `extract.go:155` already works around it (`if head != ""`). | `pinnedHead *string` or a `headPinned bool` set by `WithPinnedHead`; `currentHead` returns `""` when pinned-empty. Assert `headCalls == 0` in a hook test with `head: ""`. |
| LOW | `internal/gitlog/cache.go:38-40,198-203,212-215` | AC-8 negative-cache rule | "Budget-shortened" is inferred from `time.Until(ctx.Deadline()) < ceiling − 5 ms` at call time. The service computes the deadline at `staleness.go:224`, then reads HEAD (pinned → instant; `serve` → one exec) and schedules goroutines; any delay > 5 ms (loaded laptop, `-race`) turns a genuine ceiling timeout into "not cached" (the doomed walk M7 warned about is retried every prompt — benign direction, but exactly what the rule exists to prevent), and a hook budget within 5 ms of the ceiling is negative-cached as a git timeout. For the real ceiling (50 ms) the cut-off is 45 ms. | Make it explicit rather than inferred: the service knows which bound it chose (`staleDeadlineFor`) and can mark the context (`ctx = context.WithValue(ctx, budgetShortenedKey{}, true)` read by `Cached`), or widen the slack to a fraction of the ceiling (e.g. 20 %) and document the trade-off. |
| LOW | `internal/memory/crud.go:112-116`; `internal/postgres/store_integration_test.go:1365-1372` | AC-12 | `memory_update` with `commit_sha: ""` is accepted and writes `''` (not `NULL`) into `records.commit_sha` (`0001_init.sql:20`, `TEXT`). The spec allows an explicit value and "never cleared" for the automatic path; clearing is undefined. `''` is treated as unchecked everywhere (`shaRe`) and `recordToOutput` omits it, so it is harmless, but it creates two "no baseline" representations and the integration test enshrines it. | Either reject `""` (`invalid commit_sha`) or bind `NULL` (`updates["commit_sha"] = (*string)(nil)`) and assert `NULL` in the integration test. Also: `StoreRequest.CommitSHA` (`writepath.go:442-470`) and `PR.MergeCommit` (`ingestpr.go:137`) are not validated against the sha regex — junk is stored and silently unchecked; validate in `validate()` and drop a non-matching merge commit. |
| LOW | `internal/memory/staleness.go:290-308` | §7 serve latency, robustness | `baselineSHA` runs `Head` and `Dirty` under the raw tool/extraction context with no deadline; a hung `git status` (slow FS) blocks `memory_store`/`extract --run` indefinitely. The read path always bounds git; the write path never does. | `wctx, cancel := context.WithTimeout(ctx, s.staleCeiling)` (500 ms in `serve`) around both calls; failure → no stamp, as today. |
| LOW | `internal/gitlog/cache.go:115-127,130-151` | AC-8 hook cache, latency | `Put` flushes the whole file per entry: three stale cards → three `CreateTemp`+`write`+`chmod`+`rename` cycles per prompt. Two hook processes for the same `session_id` (queued prompts) each load, add, prune and rename — last writer wins and the other's entries are lost (recomputed next prompt). Atomic rename means no torn file; no corruption, no wrong verdict (key carries `H`). | Acceptable as is. Optional: coalesce flushes (dirty flag + one `Flush()` called by `Cached` at the end of the call, or merge the on-disk map before writing). |
| LOW | `internal/memory/staleness_test.go:224-241` | AC-6 Verify ("two `Search` calls with a changed fake `Head` use different keys") | `TestStaleHint_SingleRecordAndHeadChangeChangesKey` never changes the fake's head and the fake ignores the `head` argument (`:62`); the service→adapter pass-through of the per-call `HEAD` is unasserted. The adapter half (new `H` → recompute) is tested in `gitlog` (`TestCached_HitsMissesAndHeadKey`). | Record `head` in `fakeHistory.Changed`; call `StaleHint` twice with `f.head = "H1"` then `"H2"` and assert both values were passed. |
| LOW | `02-plan.md:3`; `docs/specs/README.md:7` | docs consistency | Plan says "not started"; README says "PR A (staleness) implemented". | Update the plan status with the WI-0/WI-14 numbers (blocker 4). |
| LOW | `internal/gitlog/gitlog.go:99-108`; `internal/memory/staleness.go:75-81` | §8 edge case (undocumented) | `--show-toplevel` returns the symlink-resolved path; a Claude Code `cwd` through a symlink makes every *absolute* entry in `files` "outside `T`" and dropped (relative entries are unaffected). Pre-existing for `deriveRepo`'s repo name. | Document in §8; optionally `filepath.EvalSymlinks` the absolute file before `Rel`. |
| LOW | `cmd/claude-memory/hook.go:126` | AC-30 (hook budget) | `Resolve` runs under the full 800 ms hook context with no own bound — identical to the old `deriveRepo` (`40a7fb7` `hook.go:142-156`), so no regression; noted because it is now the one git call that is *not* under the stale deadline. | Leave; or `context.WithTimeout(ctx, 100 ms)` so a hung git still leaves time for the search. |
| INFO | `internal/gitlog/cache.go:44-48` | cache key | Files are joined with `"\n"` inside a `\x00`-separated key; a path containing a newline could collide with two paths. Pathological; `NormalizeFiles` does not reject it. | Join with `\x00` too, or ignore. |
| INFO | `internal/gitlog/gitlog_test.go:209-235` | AC-4 Verify ("asserts argv") | Env, `WaitDelay` and literal-pathspec *behaviour* are asserted; the `--` position in argv is not asserted directly (it is exercised by `TestChanged_LiteralPathspecs`). The 100+ cap is tested at the renderer, not at the adapter (would need 100 commits). | Fine as is. |

## Status of iteration-0 blockers that belong to PR A

| # | Iteration-0 item | Status | Evidence |
|---|---|---|---|
| HIGH #1 | detector = tree compare; `rev-list` only for the count | **RESOLVED** | `gitlog.go:131-153`: `diff --quiet sha head -- files` (0/1/else), `rev-list --count --max-count=100` only on exit 1, failure → count 0 and verdict stands. Tests: 2 commits → changed/2; unlisted → fresh; change+revert → fresh; squash-rewritten sha, same content → fresh, later change → changed (`gitlog_test.go:56-125`). |
| HIGH #2 | clean-tree guard on stamping; `CodeHistory.Dirty` | **RESOLVED in code; re-opened by pinning in `extract --run`** | `staleness.go:290-308` (`Dirty` false without error, `H` non-empty); `Dirty` = `status --porcelain --untracked-files=all -- files` (`gitlog.go:157-169`), tested for modified/staged/untracked/unrelated (`gitlog_test.go:176-207`); never called from the hook (`hook_test.go:423` asserts `dirty=0`). Pinned `H` in `extract.go:155-157` re-opens the window (blocker 3). |
| M3 | extraction `repo` from the checkout toplevel | **RESOLVED in code; cmd test missing** | `extraction.Config.Repo` (`processor.go:24-28,117-121`), set from `Resolve` in `extract.go:153,168`; `TestProcessSession_ConfigRepoOverridesTranscriptRepo` covers precedence at the extraction layer only (blocker 3). |
| M4 | hook composition via `hookDeps` factory | **RESOLVED** | `hook.go:34-38,108,125`; `main.go:187-188,260-274` (`buildCodeHistory`, `sessionIDRe`); `hook.go` constructs nothing, `main.go` parses nothing. |
| M6 | AC-11 scoped down; `MergeCommit` plumbed | **RESOLVED in code; manual check unrecorded** | `prsource.go:49-52`, `client.go:79-82,126,175`, `ingestpr.go:137`, `processor.go:52-54,152,207-209`; tests `TestMergeCommitParsedFromListAndShow`, `TestProcessPR_CarriesMergeCommitAsCommitSHA`; PR source never stamped from local HEAD (`writepath.go:67`, `baseline_test.go:58-62`). `merge-base` check not recorded (blocker 4). |
| M7 | budget-aware deadline, negative cache, `WaitDelay` | **RESOLVED, with two defects** | `staleness.go:185-191` (`min(now+ceiling, staleDeadline)`), `hook.go:131` (`hookStart+300−20 ms`), `cache.go:153-218` (tri-state, negative cache, budget-shortened exception), `gitlog.go:25,67` (`WaitDelay` 10 ms). Defects: blockers 1–2, LOW on the shortened heuristic. |
| M9 | `NormalizeFiles` in `memory` | **RESOLVED** | `staleness.go:67-123`, pure; table test incl. `/etc/passwd`, `..`, `:N-M`, dedupe, cap 20 (`staleness_test.go:14-43`); service passes only normalized files (`:168,216,236`). |
| M10 | `Checkout` without `Head`; `H` per call in `serve` | **RESOLVED** | `Checkout{Dir, Repo}` (`staleness.go:28-31`); `serve` has no pinned head (`main.go:159-168`), `currentHead` reads `Head` per `Search`/`Get`/`Store`/`Update` (`staleness.go:173-182`); `TestAnnotateStale_OnlyTopTenAndOneHeadRead` asserts one `Head` per call. |
| L1 | unborn HEAD parsing | **RESOLVED** | `gitlog.go:103-113` takes line 1 when absolute even on exit 128; `TestResolve` unborn case. |
| L2 | env whitelist, no inherited `GIT_*` | **RESOLVED** | `gitlog.go:46-55`; `TestCommandHardening` sets `GIT_DIR=/nonexistent` and asserts it is absent and `Head` still resolves. |
| L9 | cache file keeps only current-`H` entries | **RESOLVED** | `cache.go:120-125`; `TestFileCache_PersistsDropsOldHeadsAndSurvivesCorruption`. |
| L12 | `CommitSHA string`, named cap constant | **RESOLVED** | `ports.go:130`, `staleness.go:18`. |

## Git usage re-check (adapter, `internal/gitlog`)

Checked clean unless noted.

- **Detector.** `diff --quiet <sha> <H> -- files` with both shas validated by
  `^[0-9a-f]{7,40}$` before argv (`gitlog.go:132`); exit 0 fresh, 1 changed,
  anything else `ErrUnchecked` with the code in the message (no stderr
  captured, so no path can leak into logs). `rev-list --count
  --max-count=100 <sha>..<H> -- files` only after exit 1; any failure or
  non-zero exit → `(true, 0, nil)` — verdict stands, count unknown
  (`:147-152`). `maxCount` 100 → renderer prints `100+` (`hook.go:70-71`).
- **Unborn HEAD.** `rev-parse --show-toplevel HEAD` prints `T` on stdout and
  exits 128; `run` returns stdout with the exit code (`:85-88`), `Resolve`
  takes line 1 when absolute (`:103-107`) and only accepts line 2 as `H`
  when `code == 0` and it matches the sha regex (`:110-112`). Not a checkout
  → empty stdout → `ok=false`; nonexistent `cwd` → `chdir` error → `err`
  (hook falls back to `basename(cwd)`, `hook.go:123,126`). `Head` on unborn
  → `""` (`:122-124`). Tested (`TestResolve`).
- **Literal pathspecs.** `GIT_LITERAL_PATHSPECS=1` always set (`:47`); every
  file after `--` (`:135,147,161`); `:(glob)**`, `--output=x`, `*.go`
  verified to match nothing and create nothing (`gitlog_test.go:127-144`).
- **Env whitelist.** `PATH`, `HOME`, `LANG`, `XDG_CONFIG_HOME`, `LC_*` plus
  the two overrides (`:46-55`) — exactly AC-4. `GIT_OPTIONAL_LOCKS=0` keeps
  `status` from writing the index. `HOME` keeps `safe.directory` working.
- **WaitDelay / kill.** `exec.CommandContext` (SIGKILL on `ctx.Done`) and
  `WaitDelay = 10 ms` on every command (`:64-67`); `run` distinguishes a
  context end from an exit code (`:82-84`), so a killed git is reported as
  `ctx.Err()`, which `Cached` classifies as a timeout (`cache.go:212`).
- **Negative cache.** Errors and ceiling timeouts → `unchecked` cached;
  timeout with a budget-shortened context → not cached; expired context →
  hits only, miss returns an uncached error without exec (`cache.go:194-216`).
  All four rules tested (`TestCached_NegativeCachingRules`). The
  shortened-vs-ceiling inference is heuristic (LOW above); for the real
  50 ms ceiling the cut-off is 45 ms.
- **Cache key.** `T \x00 H \x00 sha \x00 sorted(files)` (`cache.go:44-48`);
  `H` in the key makes every hit exact for that HEAD; order-insensitive
  (tested). `keyHead` splits on the same separator (`:51-57`).
- **FileCache concurrency.** `sync.Mutex` around load/get/put (`:107-127`);
  lazy load, corrupt → empty (`:93-105`); write = `CreateTemp` in the same
  dir, `Chmod 0600`, `Rename` (`:138-150`), directory `MkdirAll 0700`
  (`:131`). Two processes on one session file: no torn reads, last rename
  wins, lost entries are recomputed (LOW above). Per-entry flush (LOW).
- **`Dirty`.** `status --porcelain --untracked-files=all -- files`, non-zero
  exit → error → no stamp (`:157-169`); the hook never calls it
  (`cache.go:178-180` passes through; `hook_test.go:423`).

## Hook walk-through (`cmd/claude-memory/hook.go`, `main.go`)

- **Latency.** `hookStart` is taken at the top of `cmdHook` (`main.go:174`),
  before config/DB. `deriveRepo` (one `rev-parse --show-toplevel`) is
  replaced by `Resolve` (one `rev-parse --show-toplevel HEAD`,
  `hook.go:125-126`): still one process, now also yielding `H`, so a prompt
  with no cards costs exactly what it did before — *except* for blocker 1
  (below-threshold results are checked). Added per prompt with stale
  candidates: one lazy JSON read, 0–3 `git diff` (+ `rev-list` on changed)
  in parallel under `min(now + 50 ms, hookStart + 280 ms)`
  (`staleness.go:185-191,224-225`; `hook.go:131`), 1–3 atomic cache writes,
  no DB write. `TestHook_StaleCardRendered_OneResolveZeroHead` asserts the
  adapter saw a deadline ≤ 60 ms out.
- **Deadline math.** `WithStaleDeadline(hookStart.Add(300 ms − 20 ms))`
  (`hook.go:27-30,131`) and `staleDeadlineFor = min(now+ceiling,
  staleDeadline)`; the goroutines' `cctx` is `WithDeadline(ctx, deadline)`,
  so the 800 ms hook context is also honoured. Correct. Past deadline →
  `cctx` already expired → `Cached` serves hits only — but the grace timer
  defeats it (blocker 2).
- **Goroutines / grace.** `results` is buffered to `len(jobs)`
  (`staleness.go:233`), so a late goroutine never blocks; `defer cancel()`
  ends every well-behaved check at return; the exec adapter honours
  cancellation (kill + `WaitDelay`), so nothing outlives
  deadline + 10 ms + 10 ms in `serve`; in the hook the process exits. No
  write to `recs` happens after the loop returns (no data race; `-race`
  green). Fine.
- **Fail-silent.** `Resolve` error or `ok=false` → `basename(cwd)` and no
  staleness (`hook.go:123-133`); `Changed` error → unflagged
  (`staleness.go:237-239`); cache write failure ignored (`cache.go:130-151`);
  `hookCmd` returns `nil` on every path. `TestHook_NotACheckoutStillShowsCardsUnflagged`
  covers the non-checkout case. AC-31 (MVP) holds.
- **`session_id` in a file name.** `sessionIDRe = ^[A-Za-z0-9_-]{1,64}$`
  (`main.go:260`) excludes `/`, `.`, and empties; invalid → `MapCache`
  (`:267-273`), which in a one-shot process is "no cache", as specified.
  `filepath.Join(stateDir(), "stale-cache", sid+".json")` cannot escape.
- **State dir.** `stale-cache/` created `0700`, file `0600` (chmod before
  rename); `TestFileCache_…` asserts `0600`. `HOME` unset → `MkdirAll`
  fails → silent no-op.

## Baseline stamping re-check (`internal/memory`)

- **Store (ADD / SUPERSEDE).** `baselineSHA` runs before `WithTx`
  (`writepath.go:63-70`), i.e. never under the advisory lock
  (`TestBaseline_NoGitInsideTransaction`); conditions: history + checkout,
  `repo ∉ {"", "*"}`, `repo == co.Repo`, ≥ 1 usable file, `H` non-empty and
  sha-shaped, `Dirty == false` without error (`staleness.go:290-308`);
  source ∈ {inline, session} (`:67`); explicit `req.CommitSHA` wins and
  skips git (`TestBaseline_ExplicitCommitSHAWins`). ADD `:134-136`,
  SUPERSEDE `:239-241` both use the computed value. One Debug line on
  no-stamp with `dirty`/`error` only (no files, no ids — the record has no
  id yet; acceptable).
- **Store (UPDATE).** `req.Files` present → `files` and the computed
  `commit_sha` (clean) or unchanged (dirty); `req.Files` empty → only an
  explicit `req.CommitSHA` is applied (`:184-193`). Content-only UPDATEs
  never re-baseline (MEDIUM above). NOOP touches only `seen_count`/`status`
  (`:279-309`) — correct, untested.
- **`UpdateRecord`.** Explicit `commit_sha` (validated unless `""`, allowed
  alone, even when dirty) → set; else content or files changed →
  `baselineSHA(existing.Repo, req.Files ∨ existing.Files)`; title-only →
  untouched (`crud.go:110-127`; all four cases tested,
  `TestUpdateRecord_Baseline`). `""` writes `''` (LOW).
- **Namespaces.** Stamping and checking ignore namespace by design: a
  `global` record with `repo == R` is checked (spec §8); `UpdateRecord`
  still goes through `getAccessible` (`crud.go:48`), so a foreign-namespace
  id cannot be re-baselined; the cache key needs no namespace (`sha`,
  `files`, `H` determine the verdict). Correct.
- **PR records.** `SourcePR` is excluded from `baselineSHA` (`writepath.go:67`)
  and carries `MergeCommit` through `PRInput.CommitSHA` → `processDrafts`
  (`processor.go:152,207-209`). Correct.

## Hexagonal boundaries

- `internal/memory` runs no process, reads no env or file: git only via
  `CodeHistory` (`staleness.go:36-54`); `NormalizeFiles` is pure;
  `gitlog` imports `memory`, never the reverse. `mcpserver.Service` grows
  `StaleHint` (`service.go:32-34`), `extraction.StoreWriter` is unchanged.
- Adapter construction: `main.go:162` (`serve`), `:260-274` (hook factory) —
  composition root, as required. **`cmd/claude-memory/extract.go:150`**
  constructs `gitlog.Exec{}` inside `runExtract` (blocker 3). It is the
  same package as `main.go`, so the compiler does not care; the plan's
  constraint is about the *inner command functions* receiving ports (that is
  why M4 introduced `hookDeps`), and the practical cost is the missing
  AC-32 seam. `ingestpr.go` adds a field only. Verdict: one deviation,
  cheap to fix.

## Regressions (item 6)

Run in this review on the working tree (identical to `e897439`): `go build
./...` OK; `go vet ./...` OK; `go vet -tags integration ./...` OK;
`go test -race ./...` all 17 packages `ok`. No PR B files were present, so
no failure could be attributed to them; none occurred. The MVP and
namespaces suites (hook, extraction, mcpserver, memory write path,
transcript, prsource/prcursor, scrub) are unchanged in outcome. The
`integration` suite compiles (`-tags integration` vet) but was not executed
(no Docker).

## Integration SQL read-through (item 7)

- **Search selects `files`/`commit_sha`.** Hybrid RRF final `SELECT`
  (`internal/postgres/hybrid_search.go:99-112`) adds `r.files, r.commit_sha`
  between `r.namespace` and `r.tags`; `Scan` order matches
  (`:137-145`: `&files []string`, `&commitSHA *string`). Full-text-only
  (`:216-224`, `:248-256`) likewise. `files TEXT[] DEFAULT '{}'` and
  `commit_sha TEXT` nullable (`0001_init.sql:19-20`): pgx scans a NULL array
  into a nil slice and NULL text into a nil `*string`; `derefString`
  (`:286-291`) maps it to `""`. The degrade path (`store.go:321-333`) uses
  the same two queries, so `files`/`commit_sha` arrive on both legs.
- **Update whitelist.** `commit_sha` added to both `Store.Update`
  (`store.go:204`) and `txStoreImpl.Update` (`advisory_lock.go:195`); the
  value is bound as a parameter (`col+" = $N"`), never spliced.
- **Test.** `TestSearchReturnsFilesAndCommitSHA_UpdateRebaselines`
  (`store_integration_test.go:1327-1373`) covers hybrid, full-text-only,
  `Update(commit_sha)` and the `''` case, under the existing CI
  `integration` job (`ci.yml:19-27`, `-race -count=1`). Not executed here;
  a green run should be recorded in the plan (AC-30).

## Spec v0.2 ACs (PR A) vs code

| AC | Code | Test | Verdict |
|---|---|---|---|
| AC-1 | `gitlog.go:131-153`; service `staleness.go:202-282` | `TestChanged*` (changed/2, unlisted, revert, squash-rewritten same/different content, unknown sha); service rows | met |
| AC-2 | `staleInputs` `staleness.go:161-170` (repo/sha/files decided without the adapter); adapter errors → unflagged `:237-239` | `TestAnnotateStale_RowsAndNoAdapterCalls` (8 rows + no checkout + unborn), `gitlog` unknown/invalid sha | met |
| AC-3 | `NormalizeFiles` `staleness.go:67-123` | `TestNormalizeFiles` (suffix, abs→rel, outside, `..`, empty, dedupe, cap) | met |
| AC-4 | `gitlog.go:46-69` | `TestCommandHardening` (env, `WaitDelay`, hostile `GIT_DIR`), `TestChanged_LiteralPathspecs` | met (argv `--` asserted behaviourally) |
| AC-5 | `hook.go:62-75,94-99` | `TestRenderAdditionalContext_StaleSuffixGolden` (fresh, 1, 2, 100+, unknown) | met |
| AC-6 | `types.go:32-35,148-150`; `handlers.go:55-59,220-224`; `serve.go:22-26`; `main.go:159-168`; `Head` per call `staleness.go:173-182` | `TestMemorySearch_StaleAndNamespaceFields`, `TestMemoryGet_StaleHintAndNamespace`, `TestAnnotateStale_OnlyTopTenAndOneHeadRead` | met; "changed `Head` → different keys" not asserted at the service (LOW) |
| AC-7 | `staleness.go:185-191,224-257`; `hook.go:27-30,131`; config 50/500 ms | `TestAnnotateStale_DeadlineBoundsBlockingChecks`, `…HookBudgetDeadlineIsAnUpperBound`, `TestHook_BudgetAlreadySpentStillRendersCards` | **partially met**: cached verdicts are dropped on a spent budget (blocker 2) |
| AC-8 | `cache.go` (tri-state, key with `H`, negative rules, file per session 0600, current-`H` only, `MapCache` for invalid id `main.go:267-273`) | `TestCached_*`, `TestFileCache_*` | met |
| AC-9 | `hook.go:119-133`; `gitlog.go:95-114`; `hookDeps` | `TestHook_StaleCardRendered_OneResolveZeroHead`, `TestHook_NoCardNoChangedCalls` (no `Changed` assertion), `TestResolve` unborn | **not met**: `Changed` runs for below-threshold results (blocker 1); unborn → extra `Head` (LOW) |
| AC-10 | `writepath.go:63-70`; `baselineSHA` | `TestBaseline_StampedOnlyWhenAllConditionsHold` (12 rows), `…ExplicitCommitSHAWins`, `…NoGitInsideTransaction`; adapter `TestDirty` | met; `extract --run` pinning weakens it (blocker 3) |
| AC-11 | `client.go:79-82,126,175`; `prsource.go:52`; `ingestpr.go:137`; `processor.go:52-54,207-209` | `TestMergeCommitParsedFromListAndShow`, `TestProcessPR_CarriesMergeCommitAsCommitSHA`, PR-never-stamped row | met in code; manual `merge-base` check unrecorded (blocker 4) |
| AC-12 | `crud.go:110-127`; `writepath.go:184-193,239-241`; whitelist `store.go:204`, `advisory_lock.go:195`; `types.go:98`, `handlers.go:175` | `TestUpdateRecord_Baseline`, `TestMemoryUpdate_CommitSHAPassesThrough`, integration `Update` | **partially met**: content-only write-path UPDATE never re-baselines (MEDIUM); write-path UPDATE/SUPERSEDE/NOOP untested (blocker 5); `""` clears to `''` (LOW) |
| AC-25 (baseline half) | — | manual | **not done** (blocker 4) |
| AC-30 (staleness SQL) | `hybrid_search.go`, `store.go:204`, `advisory_lock.go:195` | `TestSearchReturnsFilesAndCommitSHA_UpdateRebaselines` | met in code; CI run unverified |
| AC-31 (staleness half) | `DEPLOY.md:97-98`; `INSTALL.md:243-263`; `claude-md-snippet.md:69-78` (+ existing `useful` line `:45`) | n/a | met |
| AC-32 | `extract.go:141-168`; `processor.go:24-28,117-121` | `TestProcessSession_ConfigRepoOverridesTranscriptRepo` (extraction layer; the `cfgRepo=""` case asserts nothing) | met in code; **cmd test with fake `CodeHistory` missing**, fallback branch unasserted (blocker 3) |

## Known, not re-reported

- `plainto_tsquery` AND semantics (backlog item 5); `ftsquery.go` prototype.
- Namespaces review open items (SUPERSEDE namespace fix landed earlier on this
  branch; integration suite never observed green; `serve` cwd assumption).
  The `serve` cwd assumption is now load-bearing for `checkout=<T>` too —
  the new startup log line makes a wrong cwd visible.
- Worktree / differently named clone → `R` mismatch → unchecked (spec §8,
  documented).
- `transcript.Parse` runs twice in `extract --run` (once in `runExtract`,
  once in `ProcessSession`) — pre-existing.

## Summary for the owner

The hard parts are right: the detector is the tree compare with the count
as decoration, baselines are stamped only on clean files and never under
the lock, git is invoked with a whitelisted environment, literal pathspecs
and a kill-with-`WaitDelay` context, the cache is tri-state and keyed by
HEAD, and `memory_search`/`memory_get`/cards carry the hint exactly as
specified. Five things stand between this and done, all small: stop
checking results the hook will not show (one condition), stop discarding
cache hits when the budget is gone (one `max(…, 0)`), unpin HEAD in
`extract --run` and inject its adapter so the AC-32 test can exist, write
the WI-6 write-path tests, and take the WI-0/WI-14 latency baseline and the
AC-11 `merge-base` check that the Definition of Done requires. The MEDIUM on
content-only UPDATEs is a spec/implementation choice to make explicit; the
LOWs (pinned-empty HEAD, shortened-budget heuristic, `''` vs `NULL`,
unbounded `Dirty`, per-entry flushes) can ride in the same change set.
