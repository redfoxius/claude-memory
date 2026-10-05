# Claude Memory — GitHub & GitLab PR Ingest — Plan

**Status:** v0.1, not started — awaiting plan review ×2. Spec:
`docs/specs/pr-providers/01-spec.md` (SPEC-2026-10-05-pr-providers v0.1,
AC-1..AC-31). Owner decisions §9 open (proposed defaults assumed).

## Context (file:line — fact)
- `internal/prsource/prsource.go:30,41,62` — `RepoRef`, `PR`, `Source`
  (ListCompleted/Get). No Host/Path/Bot fields.
- `internal/prsource/detect.go:38-41` — github only `github.com`; gitlab
  `gitlab.com`/`gitlab.*`; `parseTwoSegmentRef` (`:106`) keeps 2 segments
  (GitLab subgroups lost).
- `internal/azuredevops/client.go:27,39` — `Runner` port + `CLIRunner`
  (argv slice, stderr in error); comments fetched in `Get` (`:161`).
- `cmd/claude-memory/ingestpr.go:43,85,92,132` — repos from
  `cfg.PRIngestRepos` via `discoverRepos` (`:165`); `ref.Name =
  basename`; non-Azure skipped; `PRInput` gets no comments/diff.
- `cmd/claude-memory/main.go:468-499` — `cmdIngestPR` composition root
  (one `azuredevops.New(nil)`, scope via `resolveNamespace`).
- `internal/extraction/processor.go:47,129-139` — `PRInput`; `ProcessPR`
  builds the document unscrubbed and uncapped (`CharBudget` unused here);
  `prompt.go:126,138` — `TruncateWithBudget`, `SanitizeTranscriptText`.
- `internal/prcursor/cursor.go` — `(provider, repo)` JSON cursor; reused
  unchanged.
- `internal/namespace/namespace.go:28,41,64` — `Rule`, `Config`, `Parse`
  (yaml.v3, unknown keys ignored); `file.go:21` `Marshal` re-renders from
  the struct (drops unknown keys); `list.go:6` `Entry`.
- `internal/setup/steps_namespaces.go:131,181-237` — install step parses,
  edits, `Marshal`s; `rerenderLoss` hard-codes known keys
  (`default`,`namespaces` / `namespace`,`paths`).
- `cmd/claude-memory/namespaces.go:98-140` — `namespaces list` text/JSON.
- `internal/setup/checks_misc.go:70-79` `checkToolAz`; `:134-170`
  `checkJobs` gating on `prRepos()` (`doctor.go:343`); `doctor.go:493`
  check list.
- `internal/setup/jobs_launchd.go:48` `JobTools`; `steps_jobs.go:118,195`
  install gating and the "Azure DevOps repositories only" note;
  `steps_prereqs.go:28` `az` prereq.

## Architectural Constraints
- `internal/github`, `internal/gitlab` mirror `internal/azuredevops`: each
  declares its own `Runner` port, imports only `prsource` (+ stdlib),
  never `cmd`, `setup`, `memory` or `namespace`.
- One shared concrete runner `internal/cliexec.Runner{Bin, Env}` (argv
  slice, `Dir`, stdin `nil`, stderr capped 512 B and passed through an
  injected `Scrub func(string) string`). Built only in `main.go`.
- `ingest-pr` receives `map[prsource.Provider]prsource.Source` and a
  `prIngestConfig` port (per-namespace settings + roots); no adapter is
  constructed outside `main.go`.
- No new Go modules; tests use fake Runners + JSON fixtures, no network.

## Work Items

### WI-1 — `prsource`: Host/Path, GitHub hosts, Bot flag (AC-2, AC-3, AC-4, AC-14, AC-18)
- `RepoRef.Host`, `RepoRef.Path`; `PR.Bot bool`.
- `Detect`: github for `github.com`/`www.github.com`/`ssh.github.com`;
  strip port from host; `Path` = cleaned full path for every provider
  (Azure keeps its own parse; Path still set).
- `ParseRemote(remote) (host, path string, ok bool)` exported for the
  override path (AC-3).
- `IsGitHubBot(login, typ string)`, `IsGitLabBot(username string)`.
- Tests: extend `detect_test.go` table (ssh `:443`, nested GitLab groups,
  ports, `.git`, trailing `/`); bot tables.

### WI-2 — `internal/cliexec` runner (AC-12, AC-16, AC-22, AC-23)
- `Runner{Bin string; Env []string; Scrub func(string) string}`;
  `Run(ctx, dir, args) ([]byte, error)`: `exec.CommandContext`, env =
  `os.Environ()` + `Env`, stdin nil; error = `<bin> <endpoint>: <exit>:
  <scrubbed stderr ≤512 B>`; `exec.ErrNotFound` wrapped as
  `ErrCLIMissing`.
- `Classify(stderr)` → `ErrNotLoggedIn` / `ErrRateLimited` / `ErrNotFound`
  sentinels for remedy hints (AC-23).
- Tests: a test-helper binary (`os.Args[0]` re-exec pattern) emitting
  stdout/stderr/exit codes; env contains the added vars; stderr cap +
  scrub; missing binary.

### WI-3 — `internal/github` client (AC-12..AC-15, AC-20, AC-21)
- `New(r Runner)`; `ListCompleted`: page loop per AC-13 (stop rule, 10-page
  cap error), filter `merged_at > since`, sort asc, map AC-14 incl. `Bot`.
- `Get`: PR detail (error → fail); comments, reviews, files (error → log,
  empty); drop bot comments and empty bodies; diff summary lines.
- Args always `api --hostname github.com -X GET repos/<Path>/… -f k=v`;
  Path validated (`owner/repo`, no `..`, no leading `-`).
- Fixtures `internal/github/testdata/*.json`: `pulls_page1.json` (100
  items incl. closed-unmerged, bot), `pulls_page2_short.json`,
  `pull_123.json`, `comments.json`, `reviews.json`, `files.json` — captured
  once via `gh api` from a public repo, trimmed + anonymised.
- Tests: fake Runner keyed by joined argv; stop on old item, short page,
  cap error, merged filter, ordering, bot flag, partial-fetch degradation,
  argv shape (no token, `--hostname`).

### WI-4 — `internal/gitlab` client (AC-16..AC-21)
- `New(r Runner)`; endpoint `projects/<PathEscape(Path)>/merge_requests?`
  + `url.Values`; `--hostname <Host>`; page loop + cap; `MergeCommit` =
  merge → squash → sha.
- `Get`: MR detail; `notes` (`system==false`, non-bot); `diffs` paths.
- Fixtures hand-written from GitLab REST docs: `mrs_page1.json`,
  `mrs_page2_short.json`, `mr_7.json` (squash, null merge sha),
  `mr_8_ff.json`, `notes.json` (system + bot + user), `diffs.json`.
- Tests: as WI-3 plus nested-group escaping and commit fallback order.
  Doc comment states: not verified against a live GitLab.

### WI-5 — Extraction PR document (AC-24..AC-27)
- `PRInput.DiffSummary`, `PRInput.ReviewComments`; `extraction.Config`
  gains nothing new — `ProcessPR` takes a `Scrubber` (existing
  `memory.Scrubber` port) via a new `PRDeps`/param (nil = no scrub, tests).
- `renderPRDocument(pr, budget)`: sections, caps (100 lines, 50 comments,
  1 000 runes each), `TruncateWithBudget(cfg.CharBudget)`, then scrub.
- No logins: adapters never put authors into `PR`; render test asserts
  fixture logins absent.
- Tests: rendering golden (substring), caps, scrub applied before runner
  sees the prompt (fake runner captures prompt), `CommitSHA` passthrough.

### WI-6 — `namespace` `pr_ingest` schema (AC-6, AC-7, AC-8)
- `type PRIngest struct { Enabled *bool \`yaml:"enabled,omitempty"\`;
  Provider string \`yaml:"provider,omitempty"\` }`;
  `Rule.PRIngest *PRIngest \`yaml:"pr_ingest,omitempty"\``.
- `Parse`: validate provider; decode each rule's `pr_ingest` node with
  `KnownFields(true)` (via a `yaml.Node` pass) → unknown-key error;
  conflicting duplicates → error.
- `KnownKeys` (top / rule / pr_ingest key sets); `Config.PRIngestFor(ns)
  (PRIngest, bool)`; `Config.PRIngestRoots(home) (roots, warnings)` (AC-9
  derivation).
- `setup.rerenderLoss`: use `namespace.KnownKeys`, add depth-3 check under
  `pr_ingest`.
- `namespace.Entry.PRIngest *PRIngest \`json:"pr_ingest,omitempty"\``;
  `namespaces list` column `PR INGEST`.
- Tests: Parse table; Marshal round-trip byte-compare with `pr_ingest`;
  `namespaces add` keeps the section; install `namespaces` step on a file
  with `pr_ingest` → section kept, no lossy note; misspelled key → note;
  list text/JSON.

### WI-7 — `ingest-pr` wiring (AC-1, AC-3..AC-5, AC-9..AC-11, AC-21, AC-28)
- `runIngestPR(ctx, cfg, svc, cursors, sources map[Provider]Source,
  nsCfg prIngestConfig, scope, dryRun)`; `prIngestConfig` = consumer port
  `{ Roots() []string; ForRepo(path) (ns string, p namespace.PRIngest) }`
  backed by `loadNamespaces()` in `main.go`.
- Repo set: `discoverRepos(union(env, nsCfg.Roots()))`, de-dup by
  `filepath.Clean(abs)`.
- Per repo: Detect → override (AC-3) → `enabled:false` skip (AC-10) →
  source lookup (AC-1) → existing cursor/batch loop; skip `Get`/extraction
  for `pr.Bot` but advance `latest`; fill `DiffSummary`/`ReviewComments`.
- Dry run prints AC-28 line (provider `detected|override`, namespace, PR
  count + bot count, or skip reason).
- `main.go`: `cliexec.Runner` for `gh` (env `GH_PROMPT_DISABLED=1`,
  `GH_NO_UPDATE_NOTIFIER=1`, `NO_COLOR=1`) and `glab` (`NO_COLOR=1`), scrub =
  `scrub.New().Scrub`; map `{azuredevops, github, gitlab}`; pass scrubber
  into `ProcessPR`.
- Tests (`ingestpr_test.go`): provider map routing incl. unknown; override;
  disabled namespace listed in env; union + de-dup; glob root warning; bot
  PR advances cursor without `Get`; no-section behaviour equals today;
  two repos → two namespaces (closes namespaces follow-up); dry-run output.

### WI-8 — Doctor + install (AC-29, AC-30, AC-31)
- `checkToolGh`, `checkToolGlab` (LookPath; then non-mutating `Cmd`
  `gh auth status --hostname github.com` / `glab auth status`; exit 0 →
  `logged in`; else `not logged in` + remedy; output via `d.redact`); always
  `StatusInfo`. Register after `tools.az`; map both to `prereqs` in
  `install.go:75`.
- `checkToolAz` wording; `prereqTools` + `gh`, `glab` (`NoteInfo`);
  `JobTools` + `gh`, `glab`; hints `CompGh`, `CompGlab` (brew `gh`/`glab`).
- `prIngestEnabled()` helper (env set OR yaml has an enabled namespace,
  read through the setup `ReadFS`) used by `steps_jobs.go:118` and
  `checkJobs`; new note text (AC-31).
- Tests: doctor rows with fake Runner (absent / logged in / not logged in /
  timeout); jobs install gating by yaml only; golden note text update.

### WI-9 — Docs (all ACs, documentation only)
- `integration/INSTALL.md`, `USAGE.md`, `namespaces.example.yaml`
  (commented `pr_ingest` example), `DEPLOY.md`: gh/glab login, opt-in
  section, GitLab unverified note, old-binary re-render warning.
- `docs/specs/README.md` item 4 status; mark MVP AC-58 "GitHub/GitLab
  implemented".

## Order & dependencies
One PR (S–M): WI-1 → {WI-2, WI-5, WI-6} → {WI-3, WI-4} → WI-7 → WI-8 →
WI-9. WI-5 and WI-6 are independent of the adapters; WI-3/WI-4 parallel.

## AC coverage
| AC | WI |
|---|---|
| 1, 5, 9–11, 28 | 7 (9 derivation in 6) |
| 2, 3, 4 | 1 (3, 4 applied in 7) |
| 6, 7, 8 | 6 |
| 12–15 | 3 (12 env in 2/7) |
| 16–19 | 4 (16 env in 2/7) |
| 20, 21 | 3, 4 (21 skip in 7) |
| 22, 23 | 2 |
| 24–27 | 5 (24 filling in 7) |
| 29–31 | 8 |

## Verification
- `go vet ./... && go test ./...` green; no test touches the network or a
  real `gh`/`glab` (fake Runners; cliexec tests re-exec the test binary).
- Manual (spec §7), GitHub only: dry-run, real run, re-run no-op,
  `merge-base --is-ancestor` on a squash PR, `doctor` rows, job run under
  launchd once. GitLab: fixtures only — state so in the PR description.
- Review gates per SDD: plan review ×2 models before WI-1.

## Risks (plan-level)
- `yaml.v3` `KnownFields` applies per decoder: the `pr_ingest` strictness
  needs a node-level decode; keep the rest of the file lenient (forward
  compat for other keys).
- `ProcessPR` signature change ripples into tests; keep a nil-scrubber
  default to limit churn.
- `gh api -X GET -f` turns fields into a query string — assert argv in
  tests, confirm once manually with `--dry-run`.
- GitLab `updated_after` + desc order relies on documented semantics only.
