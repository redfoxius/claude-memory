# Claude Memory — GitHub & GitLab PR Ingest — Plan

**Status:** v0.2, implemented (WI-1..WI-9 plus review fixes) — Opus plan review PASS WITH FIXES, fixes
applied. Spec: `docs/specs/pr-providers/01-spec.md` (v0.2, AC-1..AC-34;
AC-9 withdrawn). v0.2 delta: security hardening in WI-1/2/3/4/7, lenient
`pr_ingest` parsing (WI-6), cursor re-key and env-only repos (WI-7),
scrub → cap → head-first (WI-5), doctor timeout (WI-8); cuts: roots,
reviews/files/diffs, list column, rich dry run, error classes.

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
- `internal/prcursor/cursor.go` — `(provider, repo)` JSON cursor, reused.
- `internal/namespace/namespace.go:28,41,64` — `Rule`, `Config`, `Parse`
  (yaml.v3, unknown keys ignored); `file.go:21` `Marshal` re-renders from
  the struct (drops unknown keys); `list.go:6` `Entry`.
- `internal/setup/steps_namespaces.go:131,181-237` — install step parses,
  edits, `Marshal`s; `rerenderLoss` hard-codes known keys
  (`default`,`namespaces` / `namespace`,`paths`).
- `internal/setup/checks_misc.go:70-79` `checkToolAz`; `:134-170`
  `checkJobs` gating on `prRepos()` (`doctor.go:343`); `doctor.go:493`
  check list.
- `internal/setup/jobs_launchd.go:48` `JobTools`; `steps_jobs.go:195,197`
  the two ingest-pr notes; `steps_prereqs.go:28` `az` prereq;
  `install.go:61` `DoctorCheckSteps`.
- `cmd/claude-memory/ingestpr.go:96-97` — logs the raw remote (may hold
  userinfo); `internal/scrub/adapter.go:11` `NewAdapter`.

## Architectural Constraints
- `internal/github`, `internal/gitlab` mirror `internal/azuredevops`: each
  declares its own `Runner` port, imports only `prsource` (+ stdlib),
  never `cmd`, `setup`, `memory` or `namespace`.
- One shared concrete runner `internal/cliexec.Runner{Bin, Env, Drop,
  Scrub}` (argv slice, `Dir`, stdin `nil`, env minus `Drop`, stdout ≤
  32 MiB, stderr ≤ 512 B through `Scrub`, a func wrapping
  `scrub.NewAdapter(scrub.New())`). Built only in `main.go`.
- `ingest-pr` receives `map[prsource.Provider]prsource.Source` and a
  `prIngestConfig` port (per-namespace settings + problems); no adapter is
  constructed outside `main.go`.
- No new Go modules; tests use fake Runners + JSON fixtures, no network.

## Work Items

### WI-1 — `prsource`: Host/Path, detection, allowlist, userinfo (AC-2..AC-4, AC-14, AC-18, AC-32, AC-33)
- `RepoRef.Host`, `RepoRef.Path`; `PR.Bot`, `PR.Trusted bool`.
- `Detect`: github for `github.com`/`www.github.com`/`ssh.github.com`;
  gitlab for `gitlab.com` only (`gitlab.*` → unknown); strip port; `Path`
  = full path for every provider (Azure keeps its own parse).
- `ParseRemote(remote) (host, path, ok)` for the override path (AC-3).
- `ValidAPIPath(provider, path) error`: segments `^[A-Za-z0-9_.-]+$`, not
  `.`/`..`; github exactly 2, gitlab ≥ 2 (AC-32). `ValidHost` =
  `^[a-z0-9.-]+$` (AC-16).
- `RedactRemote(remote) string`: drops URL userinfo; SCP `git@` stays
  (no secret) (AC-33).
- `IsGitHubBot(login, typ)`, `IsGitLabBot(username)`,
  `GitHubTrusted(association)`.
- Tests: detect (`:443`, nested groups, `gitlab.example.com` → unknown),
  allowlist (`{owner}`, `:id`, `..`, `%2F`, segment counts), redaction, bots.

### WI-2 — `internal/cliexec` runner (AC-12, AC-16, AC-22, AC-23)
- `Runner{Bin string; Env, Drop []string; Scrub func(string) string}`;
  `Run(ctx, dir, args)`: env = `os.Environ()` minus `Drop` plus `Env`;
  stdin nil; stdout through a 32 MiB `LimitedReader` (+1 byte probe →
  overflow error); error = `<bin> <endpoint>: <exit>: <scrubbed stderr ≤
  512 B>; try <remedy>` with the remedy keyed by `Bin` only. No `Classify`.
- `main.go` drops: gh → `GH_TOKEN`, `GITHUB_TOKEN`, `GH_DEBUG`; glab →
  `GITLAB_TOKEN`, `GITLAB_ACCESS_TOKEN`, `OAUTH_TOKEN`, `GITLAB_HOST`,
  `GLAB_DEBUG`. Scrub = `scrub.NewAdapter(scrub.New())` wrapped as a func.
- Tests (re-exec helper): env drop/add, stdout overflow, stderr cap +
  scrub, missing binary.

### WI-3 — `internal/github` client (AC-12..AC-15, AC-20, AC-21, AC-34)
- `New(r Runner)`; `ListCompleted`: page loop per AC-13 (stop rule, 10-page
  cap error carrying a `CursorHint` the caller fills with the cursor file
  path), de-dup by `number`, `merged_at > since`, sort asc; `Bot`,
  `Trusted` from `user.type`/`login`/`author_association`.
- `Get`: PR detail (error → fail); `issues/{n}/comments` and
  `pulls/{n}/comments` (each error → log, empty); keep trusted non-bot
  non-empty bodies only; no identity fields.
- Args `api --hostname github.com -X GET repos/<Path>/… -f k=v`; `Path`
  pre-validated by WI-1 (checked again in `New`'s calls, defence in depth).
- Fixtures (`testdata/`, hand-written from the GitHub REST docs (acme/widgets), not captured):
  pulls pages (merged, closed-unmerged, bot, `NONE` author, a duplicate),
  one PR, issue comments, review comments.
- Tests: fake Runner keyed by argv; stop, short page, cap error, de-dup,
  filters, ordering, comment filtering, degradation, argv shape.

### WI-4 — `internal/gitlab` client (AC-16..AC-21, AC-34)
- `New(r Runner)`; args `api --hostname=<Host> <endpoint>` (one arg);
  endpoint `projects/<PathEscape(Path)>/…?` + `url.Values`.
- Per run per non-`gitlab.com` host: `auth status --hostname=<Host>` once
  (cached in the client), failure → repo error.
- `ListCompleted`: `members/all` pages → trusted ids (`access_level >=
  30`; failure → error); MR pages + cap + de-dup by `iid`; `MergeCommit` =
  merge → squash → sha.
- `Get`: MR detail; first `notes` page (`system==false`, trusted, non-bot).
- Fixtures hand-written from GitLab REST docs: members, MR pages, a
  squash MR (null merge sha), a ff MR, notes (system/bot/guest/dev).
- Tests: as WI-3 + escaping, `--hostname=` single arg, auth gate (once,
  not for gitlab.com), members fail-closed, commit fallback. Not verified
  against a live GitLab (doc comment).

### WI-5 — Extraction PR document (AC-24..AC-27)
- `PRInput.ReviewComments []string` (no diff field); `ProcessPR` takes a
  `memory.Scrubber` param (nil = none, tests).
- `renderPRDocument(pr, scrub, budget)`: scrub title, description and each
  comment → cap (50 comments, 1 000 runes each) → assemble (`Comments:`
  section) → `truncateHead(doc, budget)` (keeps the start, rune-safe; new
  helper, `TruncateWithBudget` untouched).
- Tests: scrub before cap, caps, rune-safe head-first cut, captured
  prompt, `CommitSHA` passthrough.

### WI-6 — `namespace` `pr_ingest` schema (AC-6, AC-7, AC-8)
- `type PRIngest struct { Enabled *bool; Provider string }` (yaml
  `omitempty`) with a custom `UnmarshalYAML(*yaml.Node) error` that
  **never returns an error**: reads known keys, records bad values in an
  unexported `problem` field; `Rule.PRIngest *PRIngest`.
- `Parse`: after unmarshal, collect rule problems + conflicting duplicates
  into `Config.PRIngestProblems []PRIngestProblem{Namespace, Reason}`;
  never an error for `pr_ingest`; no `KnownFields`.
- `KnownKeys` (top / rule / pr_ingest sets); `Config.PRIngestFor(ns)
  (PRIngest, problem string)`.
- `setup.rerenderLoss`: use `namespace.KnownKeys`, add depth-3 check.
- doctor `checkNamespaces`: `warn` listing `PRIngestProblems` (AC-8).
- Tests: bad provider / `enabled: maybe` / conflict → problems with
  `Resolve` intact; round-trip via Marshal, `namespaces add`, install step
  (no lossy note; misspelled key → note); doctor row.

### WI-7 — `ingest-pr` wiring (AC-1, AC-3..AC-5, AC-10, AC-11, AC-21, AC-28, AC-32..AC-34)
- `runIngestPR(ctx, cfg, svc, cursors, sources map[Provider]Source, nsCfg
  prIngestConfig, scope, dryRun)`; `prIngestConfig{ For(ns) (PRIngest,
  problem string) }` backed by `loadNamespaces()`; repos only from
  `cfg.PRIngestRepos` (unchanged `discoverRepos`).
- Per repo: `ns := resolveNamespace(path)` once → problem? skip → disabled?
  skip → Detect → override → `ValidHost`/`ValidAPIPath` → source lookup →
  cursor key (`azuredevops`: `ref.Name`; else `strings.ReplaceAll(Path,
  "/", "_")` under the effective provider) → batch; bot/untrusted PRs
  advance `latest` without `Get`; `scope(ns)` reuses the same `ns`
  (`scopeFunc` takes the namespace, not the path).
- All remote logs/prints via `RedactRemote` (incl. `:96-97`); page-cap
  error gets the cursor file path appended.
- Dry run: existing line + one `skipped (<reason>)` line.
- `main.go`: two `cliexec.Runner`s (WI-2), map `{azuredevops, github,
  gitlab}`, scrubber into `ProcessPR`.
- Tests: routing (unknown, `gitlab.example.com` ± override), disabled,
  problem namespace, one resolution for scope, cursor keys of two `app`
  clones, untrusted/bot advance cursor, redacted logs, dry-run, parity.

### WI-8 — Doctor + install (AC-29, AC-30, AC-31)
- `checkToolGh`/`checkToolGlab`: LookPath; then a non-mutating `Cmd`
  (`gh auth status --hostname github.com` / `glab auth status
  --hostname=gitlab.com`) under its own `context.WithTimeout(2s)`; exit 0 →
  `logged in`; non-zero → `not logged in: <remedy>`; deadline/offline →
  `could not check`; always `StatusInfo`; output via `d.redact`. Register
  after `tools.az`; add both to `DoctorCheckSteps` → `prereqs`; update the
  doctor golden.
- `checkToolAz` wording, gating unchanged (`prRepos()`); `prereqTools` +
  `gh`, `glab` (`NoteInfo`); `JobTools` + `gh`, `glab`; hints `CompGh`,
  `CompGlab`.
- `steps_jobs.go:195,197` note texts (AC-31); no gating change.
- Tests: absent / logged in / not logged in / timeout → `could not check`.

### WI-9 — Docs (all ACs, documentation only)
- `integration/INSTALL.md`, `USAGE.md`, `namespaces.example.yaml`
  (commented `pr_ingest` opt-out/override example), `DEPLOY.md`: gh/glab
  login, trusted-author rule, self-hosted GitLab needs `provider: gitlab`,
  GitLab unverified note, old-binary re-render warning.
- `docs/specs/README.md` item 4 status; mark MVP AC-58 "GitHub/GitLab
  implemented".

## Order & dependencies
One PR (S–M): WI-1 → {WI-2, WI-5, WI-6} → {WI-3, WI-4} → WI-7 → WI-8 →
WI-9. WI-5 and WI-6 are independent of the adapters; WI-3/WI-4 parallel.

## AC coverage
| AC | WI |
|---|---|
| 1, 5, 10, 11, 28 | 7 |
| 2, 3, 4 | 1 (3, 4 applied in 7) |
| 6, 7, 8 | 6 |
| 12–15 | 3 (12 env in 2/7) |
| 16–19 | 4 (16 env in 2/7) |
| 20, 21 | 3, 4 (21 skip in 7) |
| 22, 23 | 2 |
| 24–27 | 5 (24 filling in 7) |
| 29–31 | 8 |
| 32, 33 | 1 (applied in 7) |
| 34 | 3, 4 (skip in 7) |
| 9 | withdrawn |

## Verification
- `go vet ./... && go test ./...` green; no test touches the network or a
  real `gh`/`glab` (fake Runners; cliexec tests re-exec the test binary).
- Manual (spec §7), GitHub only: dry-run, real run, re-run no-op,
  `merge-base --is-ancestor` on a squash PR, `doctor` rows, job run under
  launchd once. GitLab: fixtures only — state so in the PR description.
- Review gates per SDD: plan review ×2 models before WI-1.

## Risks (plan-level)
- A never-failing `UnmarshalYAML` must also absorb wrong node kinds
  (scalar/sequence for `pr_ingest`); test them explicitly — any error
  there would push hook/serve sessions to `global`.
- `ProcessPR` signature change: nil-scrubber default limits test churn.
- `gh api -X GET -f` → query string: assert argv; confirm via `--dry-run`.
- GitLab `updated_after` + desc order and `members/all` access levels rely
  on documented semantics only.
- `scopeFunc` (path → namespace) touches existing ingest-pr tests.
