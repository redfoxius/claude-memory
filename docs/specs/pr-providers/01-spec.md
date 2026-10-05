# Specification: Claude Memory — GitHub & GitLab PR Ingest, per-namespace `pr_ingest`

## 0. Metadata
- Spec ID: SPEC-2026-10-05-pr-providers
- Status: v0.2 — Opus plan review PASS WITH FIXES; fixes applied (§0.1)
- Owner: Oleksandr Kolomoiets (user@example.com)
- Input: `docs/specs/README.md` backlog item 4; builds on `memory-mvp`
  AC-26/27/28/58 (cursor, per-repo isolation, lookback, `PRSource` port),
  `staleness-metrics` AC-11 (merge-commit baseline) and `namespaces`.
- Owner constraints: **minimal** — no new frameworks or Go dependencies,
  no web UI. GitHub = cloud `github.com` only, `gh` is logged in (keyring,
  SSH git protocol). GitLab: no account today, adapter built anyway and
  verified against fixtures only.

## 0.1 Changes in v0.2 (Opus review)
Security: GitLab auto-detect `gitlab.com` only (D3, AC-2/3); env
stripping, stdout cap, `--hostname=H`, `auth status` gate (AC-16, AC-22);
path allowlist (AC-32), userinfo stripping (AC-33), trusted authors (AC-34,
D13). `pr_ingest` never fails `Parse` (AC-6/8). Cursor by provider + path
(D6, AC-4); cap remedy names the cursor file, de-dup by id (AC-13/17).
Scrub → cap → head-first truncation (AC-25/26). Doctor 2 s, `could not
check` (AC-29). **Cuts:** namespace roots (D8, AC-9 withdrawn, AC-31),
reviews/files/diffs (AC-15/19/24), list column (AC-8), rich dry run
(AC-28), error classes (AC-23). §9: env-only repo source.

## 1. Problem
`ingest-pr` implements only Azure DevOps; repos with a GitHub or GitLab
`origin` are skipped (`cmd/claude-memory/ingestpr.go:92`); namespaces
cannot opt out. The Azure client fetches review comments but
`extraction.PRInput` (`processor.go:47`) has no field for them, and PR text
goes to haiku unscrubbed and uncapped. This feature adds GitHub (`gh api`)
and GitLab (`glab api`) adapters; an optional per-namespace `pr_ingest`
(`enabled: false` opt-out, `provider` override) kept by every writer of
`namespaces.yaml`; scrubbed, capped PR comments for all providers; and
`doctor`/`install` rows for `gh`/`glab`.

## 2. Glossary
| Term | Definition |
|---|---|
| Completed PR | GitHub: a pull request with `merged_at` set. GitLab: a merge request with `state=merged`. Azure: `status=completed` (unchanged). Closed-unmerged / abandoned are never ingested. |
| Remote path | The origin URL path without leading `/` and trailing `.git`, all segments kept (`owner/repo`, `group/sub/project`). New `RepoRef.Path`. |
| Repo name | `basename(local path)` — the record's `repo` (all providers) and the Azure cursor key (unchanged). |
| Cursor key | Azure: `(azuredevops, repo name)` as today. GitHub/GitLab: `(effective provider, remote path with "/" → "_")`, e.g. `github__example-user_pet-game.json`. |
| PR document | The text sent to haiku: title, description, URL, comments. |
| Bot | GitHub `user.type == "Bot"` or login ending `[bot]`; GitLab username matching `^(project|group)_\d+_bot` or ending `-bot`/`[bot]`. |
| Trusted author | GitHub `author_association` ∈ {`OWNER`, `MEMBER`, `COLLABORATOR`}; GitLab: user id in the project's member list with `access_level >= 30` (Developer). |
| Effective provider | `pr_ingest.provider` of the repo's namespace when set, else the detected provider. |

## 3. Key Decisions
| # | Decision | Why |
|---|---|---|
| D1 | **Shell out to `gh api` / `glab api`** through a consumer-declared exec port (argv slice, no shell), not REST with a token. | Reuses the CLI's own login (keyring); no secret ever in our argv, env, config or logs; same pattern as `az`; no HTTP client or token storage to build. |
| D2 | **`auth.token_env` deferred**; no `auth` key at all. | One account per platform today; `gh auth switch` covers multi-account; a token env var would live in the env file/launchd plist. Revisit when a second account is real. |
| D3 | **No `host` key; no GitHub Enterprise.** GitHub always targets `github.com`. GitLab is auto-detected for `gitlab.com` only; a self-hosted GitLab is used only when its namespace sets `provider: gitlab` (the owner's explicit consent), and then targets the origin's host. | YAGNI: owner has no GHE. A hostile or typo'd `gitlab.*` remote must not make `glab` send credentials to an unexpected host. |
| D4 | Completed = **merged only** (GitHub `merged_at != null`, GitLab `state=merged`). | Matches Azure `completed`; abandoned PRs carry rejected ideas that would pollute memory. |
| D5 | **List via REST pages ordered by `updated_at` desc**, stop at the first item updated at or before the cursor; page size 100, cap 10 pages per repo per run; hitting the cap fails the repo (cursor unchanged). | A PR merged after the cursor was updated after it, so the stop is exact; failing beats silently skipping older PRs; 1 000 updated PRs per window is far above this owner's volume. |
| D6 | **Cursor**: reuse `internal/prcursor` unchanged; GitHub/GitLab keyed by effective provider + sanitized remote path, Azure keeps `(provider, repo name)`; `Since` = latest processed completion time (`merged_at`). | Two clones with the same basename (forks, `owner/app` vs `other/app`) must not share a cursor; Azure cursor files stay valid. |
| D7 | **Staleness baseline**: GitHub `merge_commit_sha` (merge, squash or last rebased commit — all on the base branch); GitLab `merge_commit_sha`, else `squash_commit_sha`, else `sha` (fast-forward). Never local `HEAD`. | Same rule as staleness AC-11; squash commits are on the base branch, so `merge-base --is-ancestor` holds. |
| D8 | **(v0.2, narrowed)** `MEMORY_PR_INGEST_REPOS` is the **only** repo source. `pr_ingest.enabled: false` opts a namespace's repos out; `provider` overrides detection. The repo's namespace is resolved once (`resolveNamespace`, incl. `MEMORY_NAMESPACE`) and used for both the opt-out and the write scope. | Minimal: no glob→root derivation; job install/doctor gating stays on the env var. |
| D9 | `pr_ingest` lives on `namespace.Rule` as a typed struct with `omitempty`; `rerenderLoss` takes its known keys from one exported list in `internal/namespace`. A bad `pr_ingest` never fails `Parse`; problems are collected and only ingest-pr/doctor act on them. | `Marshal` re-renders from the struct, so only typed fields survive. A `Parse` error makes hook/serve/extract fall back to `global` — company sessions would leak into it. |
| D10 | **Bots skipped**: bot-authored PRs are not extracted (they still advance the cursor); bot comments are dropped. | Dependabot/Renovate PRs hold no team knowledge and cost haiku calls. |
| D11 | **Doctor runs `gh auth status` / `glab auth status`** (read-only, network, own 2 s timeout) only when the tool is on PATH; rows are `info` only. | Login state is the most likely failure; the CLI's own check is authoritative; never parse its config or touch tokens. |
| D12 | Fetch failures of comments degrade to empty (as Azure); list/detail failures fail the repo. | Comments enrich extraction; they are not required. |
| D13 | **(v0.2) Trusted authors only**: ingest only PRs by trusted authors and keep only their comments. GitHub comments = issue comments + inline review comments (one call each, both carry `author_association`); no `/reviews`, `/files`, `/diffs`. | On public repos PR text comes from strangers and becomes `active` records injected into the owner's sessions (indirect prompt injection). |

## 4. User Scenarios
With `MEMORY_PR_INGEST_REPOS=~/work/acme,~/src`, the nightly job ingests
`~/src/pet-game` (`git@github.com:example-user/pet-game.git`) into `pet-game`
via `gh api` (cursor `github__example-user_pet-game.json`, `commit_sha` = squash
commit); a fork in namespace `sandbox` with `pr_ingest: {enabled: false}` is
skipped; a self-hosted GitLab repo needs `pr_ingest: {provider: gitlab}`.

## 5. Requirements (EARS)

### 5.1 Provider routing and detection
- **AC-1** `ingest-pr` shall pick the `prsource.Source` for each repo's
  provider from a provider→Source map built in the composition root
  (`azuredevops`, `github`, `gitlab`). An unknown provider shall be skipped
  with a warning, cursor untouched (MVP AC-58 behaviour for unknown only).
- **AC-2** `prsource.Detect` shall return `github` for hosts `github.com`,
  `www.github.com`, `ssh.github.com` (https, `ssh://` with or without port,
  and SCP forms) and `gitlab` for `gitlab.com` only (v0.2: other `gitlab.*`
  hosts → `unknown`). It shall fill new `RepoRef.Host` (lower-case, no
  port) and `RepoRef.Path` (remote path, all segments); existing
  `Org`/`Project`/`Name` parsing stays.
- **AC-3** When the repo's namespace sets `pr_ingest.provider`, that value
  shall replace the detected provider (the only way to reach a self-hosted
  GitLab); `Host`/`Path` still come from the origin URL. An origin whose
  path is empty shall be skipped with a warning.
- **AC-4** `ingest-pr` shall keep `RepoRef.Name = basename(local path)` for
  the record `repo` (all providers) and the Azure cursor. For GitHub/GitLab
  the cursor shall be keyed by the effective provider and `Path` with `/`
  replaced by `_`, and the adapters shall address the API only by `Path`
  (and `Host` for GitLab), never by `Name`.
- **AC-5** All providers shall keep MVP AC-26/27/28 (completion order,
  cursor after the whole batch, per-repo isolation, first-run lookback).

### 5.2 `pr_ingest` config
- **AC-6** `namespace.Rule` shall accept an optional
  `pr_ingest: {enabled: bool, provider: azuredevops|github|gitlab}` (both
  keys optional). `Parse` shall **never fail** because of `pr_ingest`: an
  unknown provider, a wrongly typed value, or differing `pr_ingest` on two
  rules of one namespace go to `Config.PRIngestProblems` (namespace,
  reason), `Resolve` keeps working, and `ingest-pr` skips that namespace's
  repos with a warning. Unknown keys inside `pr_ingest` are ignored (no
  `KnownFields`).
- **AC-7** `namespace.Marshal` shall emit `pr_ingest` exactly when set, so
  `namespaces add` and the install `namespaces` step keep it. `rerenderLoss`
  shall take known keys, incl. `pr_ingest` (depth 2) and its keys (depth 3),
  from `namespace.KnownKeys`: a valid section raises no "unknown keys"
  warning, a misspelled key still does.
- **AC-8** (v0.2, narrowed) `doctor`'s `namespaces` row shall `warn` with
  each `PRIngestProblems` entry. (`namespaces list` column cut.)
- **AC-9** *Withdrawn (v0.2)* — namespace-derived roots cut (D8).
- **AC-10** `ingest-pr` shall resolve each repo's namespace once
  (`resolveNamespace(repoPath)`, incl. `MEMORY_NAMESPACE`) and use it for
  both the `pr_ingest` lookup and the write scope. When that namespace has
  `enabled: false`, the repo shall be skipped with an info log, cursor
  untouched.
- **AC-11** With no `pr_ingest` anywhere, repo set and routing equal
  today's (plus GitHub/GitLab now ingested).

### 5.3 GitHub adapter (`internal/github`)
- **AC-12** The adapter shall run `gh api --hostname github.com -X GET
  <endpoint> -f k=v…` through its `Runner` port with the repo dir as working
  dir, stdin closed, and `GH_PROMPT_DISABLED=1`, `GH_NO_UPDATE_NOTIFIER=1`,
  `NO_COLOR=1` added to the inherited env (minus the vars in AC-22).
- **AC-13** `ListCompleted` shall page `repos/{Path}/pulls` with
  `state=closed sort=updated direction=desc per_page=100 page=N` until a page
  has < 100 items or holds an item with `updated_at <= since`; it shall keep
  items with `merged_at > since`, de-duplicated by `number` (pages shift
  while being read), sorted by `merged_at` ascending. After 10 full pages
  without reaching `since` it shall return an error naming the cap, the
  cursor file path and `MEMORY_PR_INGEST_LOOKBACK` (remedy: move the
  cursor's `since` forward or shorten the lookback).
- **AC-14** Mapping: `ID`=`number`, `CompletedAt`=`merged_at`,
  `URL`=`html_url`, `MergeCommit`=`merge_commit_sha`, new `PR.Bot` and
  `PR.Trusted` from the author (Glossary).
- **AC-15** (v0.2, narrowed) `Get` shall fetch `repos/{Path}/pulls/{n}`
  (title, body, `merge_commit_sha`, `merged_at`) and first pages
  (`per_page=100`) of `repos/{Path}/issues/{n}/comments` and
  `repos/{Path}/pulls/{n}/comments`, keeping only non-empty bodies of
  trusted, non-bot authors. No `/reviews` or `/files` calls.

### 5.4 GitLab adapter (`internal/gitlab`)
- **AC-16** The adapter shall run `glab api --hostname=<Host> <endpoint>`
  (`--hostname=` and the host as **one** argument; `Host` must match
  `^[a-z0-9.-]+$`, else skip the repo; query string built with
  `url.Values`, project id = `url.PathEscape(Path)`) through its `Runner`
  port with the repo dir, stdin closed, `NO_COLOR=1`. Before the first
  `api` call to a host other than `gitlab.com` in a run, `glab auth status
  --hostname=<Host>` shall succeed, else the repo fails.
- **AC-17** `ListCompleted` shall first read `projects/{id}/members/all?
  per_page=100` (pages, same cap) into the trusted-id set, then page
  `projects/{id}/merge_requests` with `state=merged updated_after=<since
  RFC3339> order_by=updated_at sort=desc per_page=100 page=N` until a page
  has < 100 items; keep `merged_at > since`, de-duplicated by `iid`, sorted
  ascending; same 10-page cap error as AC-13. A members-call failure fails
  the repo (fail closed).
- **AC-18** Mapping: `ID`=`iid`, `CompletedAt`=`merged_at`, `URL`=`web_url`,
  `MergeCommit` per D7, `PR.Bot` from `author.username`, `PR.Trusted` from
  `author.id` ∈ trusted set.
- **AC-19** (v0.2, narrowed) `Get` shall fetch `…/merge_requests/{iid}`
  (title, description) and the first page of `…/notes?sort=asc&per_page=100`,
  keeping `system == false` notes by trusted, non-bot authors. No `/diffs`.

### 5.5 Common adapter behaviour
- **AC-20** When a comments/notes fetch fails, `Get` shall log it and
  continue with comments empty (D12). When the list, members or PR detail
  call fails or returns unparseable JSON, the repo's batch shall fail
  (cursor unchanged), as MVP AC-26/27.
- **AC-21** Bot or untrusted PRs shall be listed (the cursor passes them)
  but get no `Get` or extraction call.
- **AC-22** The exec runner shall remove `GH_TOKEN`, `GITHUB_TOKEN`,
  `GH_DEBUG` (gh) and `GITLAB_TOKEN`, `GITLAB_ACCESS_TOKEN`, `OAUTH_TOKEN`,
  `GITLAB_HOST`, `GLAB_DEBUG` (glab) from the child env; cap stdout at
  32 MiB (overflow = error); and return errors carrying the CLI name, the
  endpoint and at most 512 bytes of scrubbed stderr. No adapter shall read,
  pass or log a token.
- **AC-23** (v0.2, narrowed) Any runner failure (missing CLI, auth, HTTP
  error, rate limit) shall surface as a repo failure (AC-20) with a remedy
  hint keyed by CLI name only (`gh auth login` / `glab auth login
  --hostname=<Host>`); no error classification.

### 5.6 Extraction input (all providers)
- **AC-24** (v0.2, narrowed) `extraction.PRInput` shall gain
  `ReviewComments []string`; `ProcessPR` shall append a `Comments:` section
  when non-empty. `ingest-pr` shall fill it for every provider (Azure:
  review comments now reach extraction).
- **AC-25** The PR document shall be built in this order: scrub each field
  (title, description, each comment) → cap (≤ 50 comments, each ≤ 1 000
  runes) → truncate the whole document **head-first** (keep the start, cut
  at a rune boundary) to `cfg.MaxContentChars`. `TruncateWithBudget` (keeps
  the tail) shall not be used for it.
- **AC-26** Adapters shall add no author or commenter identity fields
  (login, name, email, association) to `PR` or the PR document; `@mentions`
  inside bodies are out of scope. The scrubber is `scrub.NewAdapter(scrub.New())`,
  shared with the exec runner's stderr scrub. Stored records are scrubbed
  by the write path as today.
- **AC-27** `commit_sha` of PR records shall be `PR.MergeCommit` (D7); empty
  stays empty; never local `HEAD`.

### 5.7 Dry run
- **AC-28** (v0.2, narrowed) `ingest-pr --dry-run` shall keep today's
  per-repo line (works for every provider) and print one line with the
  skip reason (`disabled`, `unsupported provider`, `invalid path`,
  `pr_ingest problem`, error) for a skipped repo. No `Get`, haiku, Postgres
  or Ollama.

### 5.8 Install and doctor
- **AC-29** `doctor` shall add rows `tools.gh` and `tools.glab`: tool not on
  PATH → info "not on PATH (needed only for GitHub/GitLab repos)"; on PATH
  → run `gh auth status --hostname github.com` / `glab auth status
  --hostname=gitlab.com` (non-mutating `Cmd`, **own 2 s timeout** below the
  3 s check timeout, output redacted) → info `logged in` / `not logged in:
  <remedy>`; timeout or offline → info `could not check` (never let the
  framework's check timeout turn it into `fail`). Never `warn`/`fail`;
  never `--show-token`. Both rows map to the `prereqs` step in
  `DoctorCheckSteps`; the doctor golden is updated.
- **AC-30** `tools.az` text shall say "needed only for Azure DevOps repos"
  and stays gated on `MEMORY_PR_INGEST_REPOS` (`prRepos()`, the only repo
  source). The install `prereqs` step shall list `gh` and `glab` at
  `NoteInfo`, and `JobTools` shall include `gh` and `glab` so their dirs
  enter the job PATH when present at install time.
- **AC-31** (v0.2, narrowed) The install `jobs` notes shall read
  "ingest-pr supports Azure DevOps (az), GitHub (gh) and GitLab (glab)
  repositories; each needs its CLI logged in" and, when not installed,
  "ingest-pr is not installed: pass --pr-repos or set
  MEMORY_PR_INGEST_REPOS (Azure DevOps, GitHub or GitLab repos) to enable
  it". Install/doctor gating stays on `MEMORY_PR_INGEST_REPOS`.

### 5.9 Security (v0.2)
- **AC-32** Before any `gh`/`glab` call, every `Path` segment shall match
  `^[A-Za-z0-9_.-]+$` and be neither `.` nor `..` (so no `{`, `}`, `:`,
  `?`, `#`, `%` — gh expands `{owner}/{repo}/{branch}`, glab expands
  `:id`/`:fullpath`); GitHub needs exactly 2 segments, GitLab ≥ 2. Any
  other path shall skip the repo with a warning (`invalid path`).
- **AC-33** Every log, print and dry-run line that shows a remote shall
  show it with userinfo removed (`https://user:token@host/…` →
  `https://host/…`); this includes the existing unsupported-provider
  warning (`ingestpr.go:96-97`).
- **AC-34** Only trusted authors' PRs shall be extracted and only trusted
  authors' comments kept (D13, Glossary); untrusted PRs are counted in the
  run log, never stored.

## 6. Out of Scope (YAGNI)
- GitHub Enterprise / custom GitHub hosts; a `host` key; SSH host aliases
  for GitLab (use the real host in origin); auto-detecting self-hosted GitLab.
- `auth.token_env`, any token handling, REST clients, new Go modules.
- Bitbucket etc.; `/reviews`, `/files`, `/diffs`; namespace-derived ingest
  roots; `pr_ingest` in `namespaces list`; a richer dry run.
- Closed-unmerged or draft PRs; author allow/deny lists beyond AC-34.
- A CLI to edit `pr_ingest`; web UI; changed paths as draft `files`
  (staleness AC-33); scrubbing session transcripts before haiku;
  rate-limit back-off (a limited repo fails and retries next run).

## 7. Verification
- Unit (`go test ./...`, no network), per AC: `Detect`/path-allowlist/
  userinfo tables; github and gitlab clients via a fake `Runner` mapping
  argv → JSON fixtures in `internal/{github,gitlab}/testdata/` (paging,
  cap, de-dup, merged filter, bot/untrusted, members fail-closed, commit
  fallback, `--hostname=` single arg, `auth status` gate); exec runner env
  stripping and caps; `PRIngestProblems` with `Resolve` intact; install
  re-render keeps `pr_ingest`; routing/cursor keys; scrub → cap → head-first
  truncation; doctor rows incl. internal timeout.
- Fixtures: GitHub captured once via `gh api` (trimmed, anonymised); GitLab hand-written from the REST docs.
- Manual (owner, GitHub only): `ingest-pr --dry-run`, one real run, re-run
  no-op, `git merge-base --is-ancestor <commit_sha> origin/<default>` on a
  squash PR, `doctor` shows `tools.gh: logged in`.
- **GitLab is not verified against a live instance** (owner has no GitLab
  account): correctness rests on doc-derived fixtures only; first real use
  must start with `--dry-run`.

## 8. Risks
- **GitLab fixtures may drift** from real `glab api` output (field names,
  pagination headers ignored, members endpoint permissions on self-hosted);
  mitigated by dry-run-first and D12 degradation.
- **Indirect prompt injection from strangers**: on public GitHub repos PR
  bodies and comments are written by anyone; extracted records are
  `active` (MVP AC-29) and return in the owner's sessions. Mitigation:
  trusted-author filter for PRs and comments (AC-34), the extraction
  prompt's data-not-instructions framing, scrubbing. A trusted collaborator
  can still write misleading text — review via `claude-memory review`.
- **Teammates' text** reaches haiku (`claude -p`, as for Azure today) and
  the DB; scrubbing is pattern-based; no identity fields are added.
- **Credentials to an unexpected host**: see AC-2, AC-16, AC-22.
- **Azure change**: review comments now reach extraction (more input).
- **Old binaries** re-rendering `namespaces.yaml` drop `pr_ingest` (with
  an "unknown keys" warning and a backup); an invalid `pr_ingest` value is
  dropped on re-render by the new binary too.
- **Job PATH frozen at install** (`gh` installed later needs `install
  --upgrade`); **gh under launchd** needs keychain access — verify once.

## 9. Owner Decisions (v0.2 — taken via the review's minimal cuts)
1. Closed-unmerged PRs: **skip** (D4).
2. Repo source: **env only** (D8, v0.2); `pr_ingest` = opt-out + override.
3. Bot PRs/comments: **skip** (D10); untrusted authors skipped (D13).
4. Azure review comments into extraction: **yes**, scrub then cap (AC-24/25).
5. Doctor calls `gh auth status` (network): **yes, info only, 2 s** (D11).
6. `token_env`, GHE/`host`: **deferred** (D2, D3).
