# Claude Memory — `install` / `doctor` Implementation Plan

**Status (2026-10-02):** slice 1 (WI-S1-1..12) implemented and merged
(master `cd28e43`); the `04-slice-1-review.md` high/medium findings are
fixed in `79fb743` and `cd28e43`; its open LOWs that slice 2 depends on
are folded into WI-S2-0. WI-S1-0 / WI-S1-12 manual Mac runs are still
open (owner) and gate WI-S2-9, WI-S2-8, WI-S2-13a and AC-62's restart line (WI-S2-14b).
**Plan v0.5 (slice 2 only) for spec v0.5** answers the iteration-3
re-review (`06-slice-2-plan-rereview.md` § Iteration 3, gate READY WITH
RISKS) on top of v0.4, which answered iteration 2 (same file), and v0.3,
which answered iteration 1 (`05-slice-2-plan-review.md`). Slice 2 is
not started. `implementer` may start once the owner accepts the stated
risks (§ Stated risks for the implementer). Slice 3 is not scheduled. Work proceeds slice by slice, and slice 2
ships as pre-work + PR 2a + PR 2b (Delivery).

## Spec
- `docs/specs/install-doctor/01-spec.md` (SPEC-2026-10-01-install-doctor,
  v0.5, AC-1..AC-70). AC-23/AC-24 are slice 3 (deferred); AC-46, AC-48,
  AC-55, AC-61 are deferred follow-ups (spec §12.1).
- Review inputs: `03-architecture-review.md` (v0.1), `04-slice-1-review.md`
  (slice 1), `05-slice-2-plan-review.md` (slice-2 plan, iteration 1),
  `06-slice-2-plan-rereview.md` (iterations 2 and 3).

## Changes from plan v0.4

Delta only; each row closes an iteration-3 finding
(`06-slice-2-plan-rereview.md` § Iteration 3). No new owner decision.

| Finding | Resolution | Where |
|---|---|---|
| N1 Await re-check never passes | `Await.Artifacts []string`: re-check passes when those artifacts re-detect `ok`. The engine then re-Plans/Applies the step's other artifacts once with AC-6 defaults (deletes `bootstrap.sql`). `OnOK`/`OnOKDesc` removed. `bootstrap-file` is `ok` when no file exists (never `absent`), `ok` while pending, `outdated` only when the DB is `ok`, and never recorded in the manifest. | Design 17, 19; WI-S2-1c, WI-S2-4b; spec AC-5, AC-21 |
| N2 `ReadFS` vs slice-1 helpers | WI-S2-1a narrows `LoadManifest`, `InGitRepo`, `ReadMCPRegistration`, `ReadSettingsFile`/`ReadSettingsFileFollow`/`readSettingsFile`/`underDir`, `ScanOtherSettings` and `LaunchdJobs.FS` to `ReadFS`. `SaveManifest`, `BackupCorruptManifest`, `WriteSettingsFile` and `recheckSettings` keep `FS`. | Design 20; WI-S2-1a |
| N3 defaults vs "unset" | Seed falls back to the owner's documented default (`Source=default`): Ollama URL/model (AC-32). Generated values have `Source=generated`. Both are written. Rule A: unset→set is a change. Configure under `--yes`: a missing value takes the owner's default and fails only when there is none. | Design 15.3, 15.4, 16; WI-S2-7; spec AC-5, AC-28 |
| N4 `Detection.Remedy` | Field added. | Design 17; spec §10 |
| N5 `BinDir` users | `filepath.Dir(ResolveBinPath(…))` for the doctor not-on-PATH check, the install PATH note and the retained `<BinDir>` dir artifact. | WI-S2-0, WI-S2-3; Design 23 |
| N6 Seed notes / exit | `Seed` returns `([]Note, error)`. An error is a usage error from a flag source → exit 2 before Detect. | Design 15.1, 17, 24; WI-S2-1c; spec §10 |
| N7 | WI-S2-14b Satisfies adds AC-64 (no-file steps). | WI-S2-14b |
| N8 | `preflight.go` moves into WI-S2-1c. WI-S2-1b only renders its warnings. | WI-S2-1b, WI-S2-1c |
| N9 | AC-6 names the rule-A exception. | spec AC-6 |
| N10 stale shell DSN | Seed order for env-backed fields is flag → env file → `Env` (only when the file has no value) → default. A differing `Env` value is a drift note, not a source. | Design 16; WI-S2-4b, WI-S2-5, WI-S2-7; spec AC-5, AC-50 |

## Changes from plan v0.3

Delta only; each row closes a `06-slice-2-plan-rereview.md` finding.

| Finding | Resolution | Where |
|---|---|---|
| H1 one-writer vs skipped Configure | New **Seed** pass (phase 1a): every field-owning step sets its fields from `Inputs`/`Env`/`Prior` before any Detect. Configure may only override. Preflight no longer seeds. An **unset** field means its env key is left untouched. | Design 15, 16; spec AC-5, AC-28 |
| H2 positional rule A | Rule A re-Detects the **readers** (Design 16 column) of each changed field, plus steps whose block was resolved. A re-detected artifact whose state changed takes the AC-6 default; an env key whose field the user answered in this run's Configure is `apply`. | Design 15.4; spec AC-5 step 5 |
| H3 `--bin-dir` not sticky | `ResolveBinPath`: explicit `--bin-dir` → manifest-recorded binary path → `Paths.InstalledBinary()`. Doctor uses it (no flag). | Design 16; WI-S2-0, WI-S2-3; spec AC-35 |
| M4 compile-time claim | `ReadPorts` holds narrowed interfaces (`ReadFS`, `DBProbe`, `OllamaProbe`, `JobDetector`); `Migrate`/`Pull`/writes/`Install` exist only in `WritePorts`. Mutating commands stay a runtime guard (stated). | Design 20; WI-S2-1a |
| M3 `dir` artifacts | `dir` is recorded only for directories install created. Under `<ClaudeDir>` (`hooks/claude-memory`, `skills/<name>`) they are reversed (removed when empty). `<ConfigDir>`, `<StateDir>`, `<BinDir>` are retained. Tree-equality excludes the retained dirs and parents `MkdirAll` created. | Design 23; WI-S2-15; spec AC-54 |
| M2 pending bootstrap read as auth | A `<StateDir>/bootstrap.sql` that exists while the probe says `auth`/`nodb`/no `vector` → `blocked: awaiting user action` (command reprinted), not a fail. DB `ok` + leftover file → a `bootstrap-file` artifact `outdated` → removed. | Design 19; WI-S2-4b; spec AC-21 |
| M1 pre-Apply blocked prompt | Blocked-step prompt runs in the **Configure** phase in step order (re-check / skip / quit). A resolved block feeds rule A. | Design 15.3, 19; WI-S2-3; spec AC-5 |
| M5 recorded job hash | `JobManager.Detect(ctx, j, recorded map[string]string)` (unit path → sha256 from the manifest; nil without one). | WI-S2-13a; spec §10 |
| M6 2a→2b leak | In 2a `WritePorts.ClaudeCLI` and `WritePorts.Jobs` are nil (composition-root decision), no 2a step reads them; `PRRepos`/`JobPATH` stay unset, so `MEMORY_PR_INGEST_REPOS` is untouched. | Delivery; WI-S2-14a |
| WI-S2-1a oversized | Split: 1a = `RunState` + ports split + writable FS + lock; new 1c = engine phases + manifest write. | Work Items |
| `Await.OnOK []Action` | `Action` is display data. `Await.OnOK` is `func(ctx, WritePorts) error` (+ `OnOKDesc`). Durable cleanup is Detect-driven (M2). | Design 17, 19 |
| `created` naming | New field `Artifact.CreatedFile` (`created_file`); `CreatedContainer` (`manifest.go:56`) keeps its meaning. | Design 23; WI-S2-9 |
| WI-S2-14b exemption | Exemption covers only skipped / not-registered / blocked-by-skipped steps. **Kept** artifacts' fails count (exit 1). | WI-S2-14b; spec AC-62 |
| AC-51 vs AC-36 | AC-51: "embedded version" = rendered for hook scripts; raw embedded script = `outdated`. | spec AC-51; WI-S2-9 |
| L1 pull progress | Port already has a callback (`ports.go:168`); missing piece is the sink. `WritePorts.Progress` (renderer-owned) is passed to `Pull`. | Design 17; WI-S2-7 |
| L2 | AC-21 Verify → testcontainers `Exec` default. | spec AC-21 |
| L4 | `EphemeralDirs` adds `GOTMPDIR`. | WI-S2-3; spec §10, AC-35 |
| L5 | Modules table signature updated. | Modules Touched |
| L6 | AC-26: topology (4a) and database (4b), shared helper in 4a. | Design 22; WI-S2-4a/4b |
| L7 | Spec §10 gains `JobSpec`; `ports.go:177` comment fixed in WI-S2-13a. | spec §10; WI-S2-13a |
| L8 | `dsn.go` (new, `pctEncode` only) in WI-S2-0 files. | WI-S2-0 |
| L9 | Order S2-4a → S2-5 → S2-4b. | Work Items graph |
| L10 | Delivery 2a adds AC-27. | Delivery |
| L11 | AC-64 named in step WIs' Satisfies. | Work Items |
| L12 | DB name percent-encoded; host/port/db/sslmode validated on every path. | Design 24; WI-S2-4a |

## Changes from plan v0.2

Slice 1 text is unchanged. Each row closes a `05-slice-2-plan-review.md`
finding (`#n` = agreed table, `B-n` = reviewer B only, `A` = reviewer A
only).

| Finding | Resolution | Where |
|---|---|---|
| #1 BLOCKER engine model | Typed `RunState` with one writer per field. New `Configure` phase between choices and Plan. `Await` for "pause for a user action, then re-check". Two re-Detect rules (after Configure; before applying a step whose prerequisite was applied in this run). `--yes` behaviour with no database is defined. | Design 15–19, 22; WI-S2-1a; spec AC-5, AC-21 |
| #2 output channel | `StepResult{Artifacts, Removed, Notes, Diffs, Await}`. `Plan` returns `Plan{Actions, Diffs, Notes}`. `diff.go` moves into WI-S2-1b. | Design 17; WI-S2-1a/1b |
| #3 env file writers | The `envfile` step is the **only** writer of the env file and owns every `env-key` artifact. `database` and `jobs` put values into `RunState` during Configure. The editor gets an exported line-level API in `internal/config` and is built before the database work (WI-S2-5, now ahead of WI-S2-4b). Spec AC-7 order: `envfile` before `database`. | Design 16; WI-S2-5; spec AC-7, AC-21 |
| #4 hook script compare | Shared `RenderHookScript(asset, binPath)`. Doctor and `hooks.scripts` Detect both compare against it. | WI-S2-0; spec AC-36 |
| #5 uninstall manifest | `env-key` and `dir` artifacts are *retained* kinds: uninstall drops them from the manifest without reversing them. The binary is removed from its **recorded** path (`--bin-dir` is removed from AC-54). A `settings.json` that install created is deleted only when unmerge leaves `{}`. Backups, `install.lock` and kept config are excluded from the tree-equality test. | Design 23; WI-S2-15; spec AC-54 |
| #6 launchd Detect | Detect compares with the manifest-recorded plist hash and the *rendered* plist (program, args, schedule, PATH). Recorded and unedited but differing from the rendering → `outdated` (reloaded under `--yes`). | WI-S2-13a; spec AC-44 |
| #7 oversized WIs | S2-1 → 1a/1b; S2-4 → 4a/4b; S2-13 → 13a/13b; S2-14 → 14a/14b; doctor S2 deltas → new S2-17. | Work Items |
| #8 dry-run safety | Separate read-only and writable adapter builders. Detect/Configure/Plan get only read-only ports by type, and Apply gets the writable ones. Dry-run never calls `Lock`. `namespaces` writes through the FS port (no `os` writes from `internal/namespace` on the install path). The guard widening lands in WI-S2-0 before any S2 code. | Design 20, 21; WI-S2-0, WI-S2-8, WI-S2-14a |
| #9 slice-1 LOWs | CRLF in `jsonEmitter.indent`, `dsnPasswords` via `url.Parse`, doctor goldens that strip `detail`, env BOM. | WI-S2-0 (BOM: WI-S2-5) |
| #10 manual Mac checks | Listed as gates on WI-S2-9 (hook `$HOME`, unknown keys), WI-S2-13a (`bootout` status), WI-S2-14b (restart line), WI-S2-8 (hyphen decoding). Not blocking 2a. | Verification |
| #11 holes | Owners: AC-13/AC-14 → WI-S2-1b + WI-S2-14a; AC-49 (dir-changed warning, corrupt-backup wiring, recorded fields) → WI-S2-1b; AC-58 S2 deltas → WI-S2-17; AC-64 [S2] → every step WI + WI-S2-16; AC-69 → WI-S2-3 (install) + WI-S2-15 (uninstall); AC-1/AC-4 uninstall → WI-S2-15; spec §8 downgrade → WI-S2-1b. | Work Items |
| #12 skills/status | "Skills Implementer Will Need" added; status line updated. | — |
| B-1 per-artifact choice | `Detection.Artifacts []ArtifactState`. Choices are per artifact. `MergeSettings` takes a per-event overwrite set. A step succeeds when every artifact it chose to apply re-Detects `ok`; kept `modified` artifacts are drift notes. | Design 18; WI-S2-1a, WI-S2-9; spec AC-5 |
| B-2 `$` in DSN | `dsn.go` percent-encodes every byte outside RFC 3986 unreserved in user and password (not `url.UserPassword`). The Redactor also registers that form. | WI-S2-4a, WI-S2-0; spec AC-20 |
| B-3 psql quoting | `bootstrap.sql` renders only passwords in `[A-Za-z0-9_.~-]` (generated ones always qualify). Otherwise the create path refuses with a hint. Golden + refusal tests. | WI-S2-4b; spec AC-21; owner Q1 |
| B-4 env API | `config.EnvDoc` (exported, line-level, comments kept). `internal/config` is in WI-S2-5's files. | WI-S2-5 |
| B-5 cmd inputs | `Paths.EphemeralDirs` (`os.TempDir()`, `GOCACHE` / `UserCacheDir()/go-build`) is computed in `main`. `detectPlatform` takes the uid. The 5 s stdin deadline is a goroutine+timer in the cmd adapter. | WI-S2-3, WI-S2-14a; spec §10 |
| B-6 test infra | The bootstrap integration test runs `psql` **inside** the testcontainers pgvector container (`Exec`), or host `psql` with `MEMORY_TEST_PG_ADMIN_DSN`, and skips when neither is available. AC-62 vs `--skip`: fails of checks owned by user-skipped or kept steps do not set the exit code. Single plist source: Go templates; the `__HOME__` plists are deleted. `JobSpec` gets `Unit` beside `Label`. | WI-S2-4b, WI-S2-13a/b, WI-S2-14b, WI-S2-16; spec AC-62 |
| A (Makefile) | `test-integration` adds `./internal/setup/...`. | WI-S2-16 |
| PR split | Reviewer B's split: 2a = core + topology B/A, no Claude-file edits, runnable `install` / `install --dry-run`; 2b = Claude integration + jobs + uninstall + e2e/docs. | Delivery |

### Rejected or corrected findings
- **#6 (partly wrong).** A schedule change does not read as `modified`.
  `LaunchdJobs.Inspect` never compares `Hour`/`Minute`
  (`internal/setup/jobs_launchd.go:147-160`), so it reads as `ok`, which
  is worse. A moved `--bin-dir` does read as `modified` (`:152`
  `s.Program != j.Program`). The fix is adopted for both cases.
- **B-1 (partly wrong).** The merge library is already non-destructive.
  `MergeSettings` adds absent events, replaces outdated ones and keeps
  modified ones when `overwriteModified` is false
  (`internal/setup/settings.go:391-438`). The defect is the step-level
  aggregate (`settings.go:344-370`, `aggregateHookStates`) combined with
  the step-level `DefaultChoice` (`state.go:47-56`), plus the single
  `overwriteModified bool`. The fix is per-artifact choices plus a
  per-event overwrite set, and the library rewrite is not needed.
- **B-2 (narrower than stated).** `shellExpansionRe` is `\$[A-Za-z_({]`
  (`internal/config/config.go:318`), so the review's own sentinel `$@`
  (encoded as `$%40`, see `doctor_test.go:748`) does not trigger it.
  Passwords such as `pa$word` do trigger it. The fix is adopted.
- **Reviewer A "slice-1 libraries present"** is confirmed:
  `SaveManifest` (`manifest.go:212`), `BackupCorruptManifest`
  (`manifest.go:225`), `golang.org/x/term v0.45.0` in `go.sum:194` (a
  `require` line is still needed in `go.mod`).

## Changes from plan v0.1
- Four PRs (A–D) → **two slices** (spec §0.2). Old PR A ≈ slice 1 minus the
  latency probe, plus the settings.json and Markdown-block libraries and
  the `.claude.json` reader. Old PRs B + C ≈ slice 2 minus cron, purge,
  `--only`, seed and in-process admin bootstrap. Old PR D → slice 3
  (docker) and slice-2 docs.
- Work items renumbered (`WI-S1-n`, `WI-S2-n`); the old → new map is at the
  end. Withdrawn items are listed there, not silently dropped.
- Design decisions changed: 2 (JSON codec), 5 (bootstrap SQL), new 10–13
  (`Paths`/`Env`, MCP detection, no `Verify`, parallel doctor).

## Delivery

| Slice | Ships as | Scope | Work items | ACs |
|---|---|---|---|---|
| **S1 — doctor + plumbing** | PR 1 (may split at the WI-S1-6 / WI-S1-7 seam if review size demands: 1a plumbing, 1b libraries + doctor) | early dispatch, `version`, `migrate`, `ParseEnvFile`, embed packages, skill rewording, pg/ollama probers, `internal/setup` ports + `Paths`/`Env` + redactor + fakes + guard, settings.json codec/merge library, Markdown block library, `.claude.json` reader, manifest reader, 23 doctor checks, `doctor` command, DEPLOY.md env fix | WI-S1-0..WI-S1-12 | AC-1/AC-4 (doctor halves), AC-2, AC-3, AC-15, AC-18 (guard), AC-27, AC-30 (redactor), AC-32 (prober), AC-34 (embed), AC-37/AC-38 (library), AC-40 (detect), AC-41 (doctor), AC-42 (library), AC-44 (doctor), AC-49 (read), AC-57..AC-60, AC-63, AC-64 (libraries), AC-65 (pg half), AC-66 (S1 half), AC-67, AC-68, AC-70 (doctor) |
| **S2-pre — slice-1 hardening** | small PR on slice 1, before any S2 code | guard widening, CRLF emitter, `dsnPasswords` via `url.Parse` + percent-encoded Redactor form, shared `RenderHookScript` (doctor half), doctor goldens without `detail` | WI-S2-0 | AC-30 (redactor edges), AC-36 (doctor compare), AC-37 (CRLF), AC-60, AC-63, AC-68 |
| **S2a — install core** | PR 2a | engine + renderer + diff, prompter, platform/prereqs/binary/root guard, env-file editor + `envfile`, topology + DSN, `bootstrap.sql` + `database`, migrate, ollama, namespaces, `install` command (writable/read-only builders, `--dry-run`, `--upgrade`), final doctor. **No step edits a Claude Code file** (`settings.json`, hook scripts, `.claude.json`, skills, CLAUDE.md) and no job is loaded. `install` and `install --dry-run` are runnable end to end for topology B and A. v0.4: `WritePorts.ClaudeCLI`, `WritePorts.Jobs` and `ReadPorts.Jobs` are **nil** in 2a (no 2a step reads them; the 2a registry test asserts it), and `PRRepos`/`JobPATH` are never set, so `MEMORY_PR_INGEST_REPOS` is left untouched. | WI-S2-1a, 1c, 1b, 2, 3, 4a, 5, 4b, 6, 7, 8, 14a, 14b | AC-1 (install), AC-4 (install), AC-5..AC-14, AC-15 (S2), AC-16 (detect), AC-17, AC-18 (hints), AC-19..AC-22, AC-25, AC-26, AC-27 (BOM), AC-28..AC-33, AC-34 (S2), AC-35, AC-47, AC-49 (write), AC-50, AC-52, AC-53, AC-62, AC-64 (2a steps), AC-65 (bootstrap), AC-69 (install) |
| **S2b — Claude integration, jobs, uninstall** | PR 2b | hooks.scripts + hooks.settings, mcp, skills, claude-md, launchd install + systemd + jobs step, doctor S2 deltas, `uninstall`, e2e, CI, docs. Every user-file writer ships with its `uninstall` reversal in the same PR. | WI-S2-9, 10, 11, 12, 13a, 13b, 17, 15, 16 | AC-1/AC-4 (uninstall), AC-16 (wiring), AC-36..AC-45, AC-51, AC-54, AC-56, AC-58 (S2), AC-64 (2b steps), AC-65 (e2e), AC-66 (S2), AC-69 (uninstall), AC-70 (install) |
| **S3 — docker topology C** | not scheduled | C1 bundle + compose up, C2 bundle | re-plan first | AC-23, AC-24 |
| **D — follow-ups** | not scheduled | cron, purge, adoption heuristics, latency probe, `--only`, seed, admin bootstrap, backup rotation, release binaries | — | AC-46, AC-48, AC-55, AC-61, parts of AC-39/41/51/53 |

Doctor ships first because it is useful on its own against today's hand
install (it flags the `run-with-env.sh` plists and legacy hook entries),
and it exercises the probes and libraries that slice 2's Detect reuses.
S2 depends on S1.

## Context
- `cmd/claude-memory/main.go:run()` dispatches only `namespaces` before
  `config.LoadFromFile` + `config.Load()`, and `config.Load` fails without
  `MEMORY_PG_DSN`. `doctor`/`version` (S1) and `install`/`uninstall` (S2)
  go in that early branch. `migrate` needs the DSN and the 0600 check, so
  it goes **after** config load (spec AC-1/AC-3, consistent now).
- `config.LoadFromFile` splits each line on `=`: `export ` prefixes and
  quotes are not handled. DEPLOY.md documents the `export` form and
  recommends `openssl rand -base64 32` passwords (may contain `/`).
- `postgres.New` runs `migrationSQL` (0001 + 0002); `postgres.Open`
  doesn't. 0001 does `CREATE EXTENSION IF NOT EXISTS vector`, which needs
  a superuser if the extension is missing; after a superuser created it,
  the app role's re-run is a no-op. `schema_migrations` exists but nothing
  writes to it, so schema state is detected by introspection.
- `deploy/initdb/01-app-role.sh` holds the role/db/extension SQL inside a
  bash heredoc with psql variables; it is not idempotent (bare `CREATE
  ROLE`).
- `claude mcp get|list` spawn the registered server (verified, claude
  2.1.287) — never used. Registration lives in `.claude.json`
  (`$CLAUDE_CONFIG_DIR/.claude.json` or `~/.claude.json`) as
  `mcpServers.<name> = {type:"stdio", command, args, env:{}}`.
- `systemctl --user` fails with `Failed to connect to bus` when
  `XDG_RUNTIME_DIR` is unset (ssh), even with a lingering user instance.
- External binaries in use: `git` (gitlog, ingest-pr), `claude`
  (extraction/haiku.go), `az` (azuredevops). The launchd jobs inherit no
  PATH, and `run-with-env.sh` corrupts passwords containing `$`/`&`.
- `integration/` and `deploy/` contain no Go packages; `go:embed` cannot
  reference parent directories, so each gets a tiny package. A directory
  pattern skips dotfiles.
- `integration/skills/remember/SKILL.md` (lines 3, 11, 16, 25) and
  `memory-digest/SKILL.md` (lines 3, 27) cite `acme/CLAUDE.md` /
  `acme/` repos.
- `namespace.Init/Add/Save` use `os` directly and write atomically at
  0600 (`internal/namespace/file.go:20-82`). v0.3: the install path does
  not call them. It uses `namespace.Parse` + a new exported `Marshal`
  through the FS port (Design 20), so dry-run stays write-free by
  construction.
- `config.EnvFile` exposes only `Values`/`Order`/`Mode`/`Findings`. Its
  line entries are unexported and comments are dropped
  (`internal/config/config.go:295-310`), so the env editor needs a new
  exported line-level API (WI-S2-5).
- The CI integration job uses testcontainers. `MEMORY_TEST_PG_ADMIN_DSN`
  is an optional override, and the `probe_integration_test.go` tests
  skip without it (`internal/postgres/probe_integration_test.go:34-36`).
- CI runs `go test ./...` and an integration job covering only
  `./internal/postgres/...`.

## Scope
In scope: spec §6 ACs labelled [S1] and [S2]. Out of scope: spec §12,
slice 3, the §12.1 follow-ups, a release workflow (§13 #2 follow-up PR),
renaming launchd labels.

## Modules Touched

| Path | Slice | Change |
|---|---|---|
| `cmd/claude-memory/main.go` | S1, S2 | early dispatch; `version`, `migrate`; `Paths`/`Env` construction |
| `cmd/claude-memory/version.go` (new) | S1 | `version` var, `ReadBuildInfo` fallback, `dev` |
| `cmd/claude-memory/migrate.go` (new) | S1 | `cmdMigrate` |
| `cmd/claude-memory/paths.go` (new) | S1 | `buildPaths(getenv, flags, uid, exe, cwd)`, `buildEnv` (allow-list) |
| `cmd/claude-memory/platform.go` (new) | S1, S2 | `detectPlatform(ctx, fs, runner, goos, goarch)` (S1); S2 adds `uid`: `detectPlatform(ctx, fs, runner, goos, goarch, uid) PlatformInfo` (systemd + bus retry, package managers) |
| `cmd/claude-memory/setup_adapters.go` (new) | S1, S2 | `readOnlyFS` (S1), `writableFS` + `Lock` (S2), `execRunner{readOnly}`, `systemClock`; S2: `ttyPrompter`, `stdinSecret` (one line, 4 KiB, 5 s goroutine+timer), `claudeCLI`. No `nsStore` (v0.3: namespaces go through the FS port, WI-S2-8). |
| `cmd/claude-memory/main.go` | S2 | `buildSetupDeps` split into `buildSetupValues` (Paths incl. `EphemeralDirs`, Env, Platform with uid, Redactor), `readOnlyPorts()` and `writablePorts()` (Design 20) |
| `cmd/claude-memory/doctor.go` (new) | S1 | flags, adapters, `setup.RunDoctor`, renderers, exit codes |
| `cmd/claude-memory/install.go`, `uninstall.go` (new) | S2 | flags, root guard, `--pg-password-stdin`, `setup.Engine` |
| `internal/config/config.go` | S1 | `ParseEnvFile`, `EnvFile` type; `LoadFromFile` on top |
| `internal/postgres/probe.go`, `schema_objects.go` (new) | S1 | `Probe` (read-only, TLS, SQLSTATE classes), `Migrate`; S2: `LocalServerEvidence` |
| `internal/ollama/prober.go` (new) | S1 | version, tags, embed dims, pull (stream) |
| `internal/setup/` (new) | S1 | `paths.go` (types), `ports.go`, `platform.go` (type), `state.go`, `redact.go`, `jsonobj.go`, `settings.go`, `mdblock.go`, `gitrepo.go`, `mcpreg.go`, `manifest.go` (read), `doctor.go`, `checks_*.go`, `report.go`, `fakes_test.go`, `guard_test.go`, `testdata/` |
| `internal/setup/` | S2 | `engine.go` (phases), `runstate.go` (`RunState`, `Detection`, `Plan`, `StepResult`, `Await`), `binpath.go` (`ResolveBinPath`, S2-0), `render.go` (status table, plan, diffs, ✓/✗ lines), `prompt.go`, `manifest.go` (write, merge, trim, retained kinds), `preflight.go` (corrupt backup, config-dir and downgrade warnings), `diff.go`, `envfile.go`, `dsn.go`, `bootstrap.go`, `hints.go`, `hookscript.go` (`RenderHookScript`, S2-0), `steps_*.go`, `jobs_launchd.go` (install half), `jobs_systemd.go`, `jobs_render.go`, `uninstall.go`; `settings.go` (`MergeSettings` per-event overwrite set); `jsonobj.go` (CRLF, S2-0); `checks_claude.go`, `checks_misc.go` (S2-0 render compare; S2-17 deltas) |
| `internal/config/envdoc.go` (new) | S2 | exported line-level editor `EnvDoc` (keeps comments, blank lines, unknown keys, order); BOM strip in `parseEnv` |
| `internal/namespace/file.go` | S2 | export `Marshal(*Config) ([]byte, error)` (header + YAML, what `Save` writes) and `(*Config).Add`; `Save`/`Init`/`Add` keep their behaviour for the `namespaces` CLI |
| `internal/setup/guard_test.go` | S2-0 | AST guard walks sub-packages; bans `os` FS verbs and `net.Dial*`/`net.Listen*` |
| `internal/setup/doctor.go`, `redact.go` | S2-0 | `dsnPasswords` via `url.Parse`; the Redactor registers the full percent-encoded userinfo form |
| `integration/embed.go` (new, `package integration`) | S1 | `//go:embed hooks skills launchd claude-md-section.md settings.snippet.json` (S2 adds `systemd`) |
| `integration/claude-md-section.md` (new) | S1 | section body, location-neutral; `claude-md-snippet.md` points at it |
| `integration/skills/remember/SKILL.md`, `memory-digest/SKILL.md` | S1 | drop `acme/CLAUDE.md` / `acme/` references |
| `integration/launchd/*.plist` | S2 (2b) | **replaced** by one `text/template` `integration/launchd/job.plist.tmpl` (binary, args, schedule, PATH, log path). The `__HOME__` plists are deleted, and INSTALL.md's manual reference prints the rendered plist via `install --dry-run` (single plist source, B-6). |
| `integration/systemd/` (new) | S2 (2b) | `job.service.tmpl`, `job.timer.tmpl` |
| `deploy/embed.go` (new, `package deploy`) | S1 | `//go:embed initdb` (S2 adds `app-role.psql` to it; S3 adds compose files + `.env.example` by name) |
| `deploy/initdb/app-role.psql` (new), `01-app-role.sh` | S2 | shared idempotent SQL; the `.sh` becomes a wrapper |
| `Makefile` | S1 | one `LDFLAGS` var for build/install/cross-build; cross-build adds darwin/amd64, linux/arm64 |
| `.github/workflows/ci.yml`, `Makefile` (`test-integration`) | S1, S2 | integration job adds `./cmd/claude-memory/...` (S1) and `./internal/setup/...` (S2, also in `make test-integration`); guard test once |
| `go.mod` | S2 | `golang.org/x/term` |
| `DEPLOY.md` | S1, S2 | S1: laptop env snippet → `KEY=VALUE`, `openssl rand -hex 32`; S2: upgrade → `install --upgrade` |
| `integration/INSTALL.md`, `integration/bin/run-with-env.sh`, `integration/mcp-registration.md`, `integration/ollama.md`, `docs/specs/README.md` | S2 (README also S1) | docs |

## Architectural Constraints
- **Ports are declared by the consumer.** `internal/setup` declares every
  interface (spec §10). Adapters live in `cmd/claude-memory` (`os`,
  `exec`, `x/term`), `internal/postgres` (`Probe`, `Migrate`) and
  `internal/ollama` (`Prober`). Only `cmd/claude-memory` constructs them.
- **Values, not ambient state (AC-68).** `Paths`, `Env` and
  `PlatformInfo` are built once in `main.go` and passed in.
  `internal/setup` never calls `os.Getenv`/`LookupEnv`/`Environ`/
  `UserHomeDir`/`Getuid`/`Executable`; it uses `os` only for
  `os.ErrNotExist` and `io/fs` types. Tests construct `Paths` from
  `t.TempDir()`; no `t.Setenv`, so tests are `t.Parallel()`-safe.
- **`internal/setup` imports none of `os/exec`, `net/http`, pgx,
  `golang.org/x/term`.** A guard test enforces imports (`go list -deps`)
  and greps non-test sources for the banned `os.*` calls (AC-63, AC-68).
- **Mutating vs read-only is part of the data.** `Cmd.Mutating` is set by
  the step code. `execRunner{readOnly: true}` returns `ErrReadOnly` for a
  mutating command, and the FS adapter in dry-run or doctor mode returns
  `ErrDryRun` from every write method. Doctor and `--dry-run` are
  write-free by construction. "Read-only" also means "never spawns
  something that writes": no `claude mcp get|list` (AC-67).
- **Steps and checks never print.** They return state, detail, actions,
  artifacts and errors. The renderer prints through the single redacting
  sink, in text and JSON.
- **One probe set.** Doctor checks and step Detect share probe helpers
  (`probeDB`, `probeOllama`, `readSettings`, `readMCPRegistration`,
  `readPlist`), so `install` and `doctor` cannot disagree. A step's
  success criterion is its own Detect returning `ok`.
- **No shell.** Every external command is argv. Templates render into
  files, never into a shell string. The only shell command a user sees is
  printed text (`bootstrap.sql` run, package hints).

## Relevant INSIGHTS.md Gotchas
- The repo has no `INSIGHTS.md` and no repo-level `CLAUDE.md` (checked at
  `cd28e43`). The parent `acme/CLAUDE.md` conventions apply: Russian or
  English artifacts; agents don't commit.

## Skills Implementer Will Need
- **golang-architecture.** Used for every S2 work item.
  - Ports stay consumer-declared in `internal/setup/ports.go`.
  - `ReadPorts` and `WritePorts` are built only in
    `cmd/claude-memory/main.go`, the single composition root (Design 20).
  - The engine is synchronous and starts no goroutines. The 5 s stdin
    deadline goroutine lives in the cmd adapter, tied to a context
    ("Concurrency Ownership").
  - `internal/namespace` gains pure `Marshal`/`Add`, so the setup package
    never reaches `os` through it.
  - `internal/config.EnvDoc` is a pure editor over bytes (no I/O).
  - No conflict found with the plan.
- **security.** Used for WI-S2-0, 2, 4a, 4b, 5, 14b and 15: secrets
  handling (AC-30/31), percent-encoding (Design 24), the `bootstrap.sql`
  alphabet rule, no shell, argv only. No conflict. The skill's
  Gin/JWT/HTTP sections do not apply (no server code is touched).

## Design Decisions
1. **Stdlib line prompts + `x/term`, not a TUI** (spec §4, S2). The
   prompter takes `io.Reader`/`io.Writer` plus an injected `secret func()
   (string, error)`. The TTY adapter supplies `term.ReadPassword` and
   registers the secret with the Redactor before returning it; tests
   supply a scripted transcript.
2. **settings.json codec: raw slices, not re-encoding (revised).**
   `jsonobj` walks the input with `json.Decoder` (`UseNumber()`) and
   records `InputOffset()` before and after each value, building a tree of
   ordered keys whose leaves and untouched subtrees are `[]byte` slices of
   the original input. Writing re-emits untouched slices verbatim and
   re-encodes only the event arrays the merge changed, using the
   indentation unit detected from the first indented line (tab or N
   spaces; 2 if none). Duplicate keys at any level are a parse error.
   No-op is decided on the parsed model ("our entries present exactly
   once and equal to desired"), so formatting never triggers a write.
   Merge/unmerge are pure functions `([]byte, desired, recorded) →
   ([]byte, Summary, changed bool, error)`; the read-only analysis
   (`Analyze`) used by doctor and Detect is the same code. Rejected: a
   `map[string]any` round-trip (reorders keys, loses number precision and
   escapes), `json.RawMessage` via `Unmarshal` (no order), a JSON5/JSONC
   library (dependency; could silently drop comments).
3. **Hook identity by command path suffix, mutability by manifest.**
   Identity: `…/claude-memory/user-prompt-submit.sh` / `…/session-end.sh`,
   plus commands containing `claude-memory hook|extract` — this
   recognizes hand installs with `$HOME/...`. We only *replace* an entry
   that equals the canonical JSON recorded in the manifest (`outdated`);
   any other entry of ours is `modified` (drift) and needs the overwrite
   Confirm, which `--yes` never gives. No marker key inside entries
   (Claude Code validates the settings schema). The installed command
   uses the absolute path, so doctor's "command path exists" is exact;
   doctor expands `$HOME` for legacy entries.
4. **Jobs call the binary directly.** The binary already loads the env
   file through the 0600 check, so `run-with-env.sh` adds nothing except
   bash expansion bugs. PATH is captured at install time from
   `LookPath(claude|git|az)`, preferring stable shim directories.
5. **Bootstrap SQL: one psql-variable file shared with initdb
   (revised).** `deploy/initdb/app-role.psql` uses `:"app_user"`,
   `:"app_db"`, `:'app_pw'`; idempotency comes from `SELECT format(…)
   WHERE NOT EXISTS (…) \gexec` for `CREATE ROLE` and `CREATE DATABASE`
   (the latter cannot run in `DO`; psql does not interpolate variables
   inside `$$` bodies, which rules out `DO` for the role too), then
   `\connect`, `CREATE EXTENSION IF NOT EXISTS vector`, REVOKEs and
   GRANTs. `01-app-role.sh` becomes `psql -v … -f
   /docker-entrypoint-initdb.d/app-role.psql` (the `.psql` extension is
   not run by the Postgres image's entrypoint on its own). The installer
   renders `bootstrap.sql` = three `\set` lines with real values + the
   embedded body verbatim, writes it 0600 to `<StateDir>`, prints the
   `sudo -u postgres psql … -f - < file` (Linux) / `psql … -f file`
   (macOS) command, and never executes it. This replaces v0.1's "two
   copies + catalog-equivalence test" and v0.1's in-process `Bootstrap`
   (deferred). Rejected: generating the shell heredoc from Go (extra
   machinery); a separate installer copy (drift). *(v0.3: only passwords in
   `[A-Za-z0-9_.~-]` are rendered, B-3; see WI-S2-4b.)*
6. **Schema detection by introspection.** `Probe` checks the
   `information_schema.columns` / `pg_indexes` objects introduced by each
   embedded migration (a Go table next to the migrations: `0001 →
   records.embedding, idx_records_embedding_hnsw`, `0002 →
   records.namespace`). A unit test fails if a migration file has no
   table entry.
7. **(S3) The C1 compose uses bridge + loopback publish + named volume**,
   a separate template from the server's host-networked one. Kept for
   slice 3.
8. **The manifest is written only on change** (AC-9, AC-50). The engine
   owns it; steps return `[]Artifact` from Apply; the engine merges and
   writes only when the artifact set or a hash changed. `uninstall` trims
   it and deletes it when empty.
9. **Golden tests with `-update`** live under
   `internal/setup/testdata/<area>/<case>.{in,golden}`. Each case runs
   twice (idempotence) and, from S2, through uninstall where it applies.
10. **`Paths` + `Env` values (new).** See constraints. `buildPaths` is a
    pure function over `getenv func(string) string` and the flag values,
    tested by a table in `cmd`.
11. **MCP detection by file read (new, BLOCKER fix).** `mcpreg.Read(fs,
    paths)` parses `Paths.ClaudeJSON` strictly, read-only, and returns
    `{present, command, args, localScopeShadows []string}`. The
    `ClaudeCLI` port has only `MCPAdd`/`MCPRemove`. AC-67 tests prove
    nothing spawns the registered command.
12. **No `Verify` method (new).** The engine re-runs `Detect` after
    `Apply`; success = `ok`. One implementation per step, one fake.
13. **Parallel doctor (new).** The registry is an ordered
    `[]Check{ID, Title, Requires, Run}`. A runner starts every check whose
    prerequisites finished, each under `context.WithTimeout(--timeout)`,
    all under `--deadline`; results are reported in table order. Checks
    are one read each (file read, one HTTP GET, one `launchctl print`).
14. **Choices `apply | keep | skip` (new).** Plus a separate overwrite
    `Confirm` for `modified`; `--reconfigure` re-asks inputs. The state ×
    choice matrix is 5 × 3. *(v0.3: choices are made per artifact, see
    Design 18. Designs 8 and 12 are refined by Designs 17 and 19.)*

### Slice-2 engine design (v0.3, closes review #1, #2, #3, #8, B-1)

15. **Phases.** `Engine.Run` is synchronous. It starts no goroutines;
    the doctor runner keeps its own (Design 13). The phases are:
    0. **Preflight** (`preflight.go`). Take the lock (writable mode only,
       Design 21). Load the manifest. If it is corrupt, back it up in
       writable mode with `BackupCorruptManifest` (`manifest.go:225`); in
       dry-run only report it. Warn when `manifest.claude_config_dir` ≠
       `Paths.ClaudeDir` (AC-49). Warn on a downgrade (spec §8): when the
       manifest's `binary_version` and the running version both parse as
       semver (`vX.Y.Z[-n-gSHA]`) and the manifest's is newer, set
       `RunState.Downgrade` so every `outdated` artifact defaults to
       `keep`. `dev` or an unparseable version never warns. Preflight
       fills only `Inputs` and `Prior` (manifest + env-file `EnvDoc`); it
       sets no step-owned field (v0.4, H1).
    1. **Seed, then Detect all.** (a) **Seed** (v0.4, H1): in AC-7 order,
       every step implementing `Seeder` sets the `RunState` fields it owns
       (Design 16) from `Inputs`, `Env` and `Prior`, with read-only ports.
       Seed runs once per run; a re-Detect never re-seeds. A field the
       owner cannot derive and that has no documented default stays
       **unset** (zero value plus `Set=false`). (v0.5, N6) `Seed`
       returns `([]Note, error)`: notes (e.g. an invalid env-file DSN,
       a differing shell value) are printed before the status table; an
       error means a flag value is invalid and exits 2 before any
       Detect.
       This runs before any Detect, because `envfile` precedes `database`
       in AC-7 yet reads `DB`. (b) **Detect** all steps in AC-7 order with
       read-only ports, then print the status table (AC-5).
    2. **Choices** per artifact (Design 18). Interactive mode asks per
       step, with a per-artifact overwrite `Confirm` for `modified`.
       `--yes` takes the defaults.
    3. **Configure** (new) in step order, for every step that implements
       `Configurer` and whose choice set contains an `apply`, or that runs
       under `--reconfigure`, or whose required input is missing. It asks
       questions (or reads flags/`Env` under `--yes`; v0.5, N3: a missing
       value takes the owner's documented or detected default — AC-19
       topology evidence, AC-21 local create-path defaults with a
       generated password — and fails the step with the flag name,
       AC-12, only when there is none, e.g. topology `remote` with no
       DSN), may probe
       read-only (the AC-20 auth re-ask loop runs here), and may only
       **override** its own `RunState` fields that Seed set or left unset
       (Design 16). **Blocked-step prompt (v0.4, M1):** in the same
       step-order walk, before a step's Configurer, a step whose Detection
       is `blocked` by an external condition (`BlockedBy` empty, a
       `Remedy` present: AC-18 prereq hints, `jobs` backend `none`,
       Ollama absent) is offered `re-check / skip / quit` interactively
       (AC-10, AC-18). **re-check** runs only that step's Detect; when it
       is no longer `blocked`, its choices take the AC-6 defaults and the
       step counts as *resolved* for rule A. **skip** keeps it `blocked:
       skipped by you`. **quit** exits 130 before any write. Under `--yes`
       no prompt: the step stays blocked (default `skip`, AC-6) and its
       dependents are blocked.
    4. **Re-Detect rule A (v0.4, H2: by reader, not by position).** After
       Configure, re-run Detect for (a) every step listed as a **reader**
       (Design 16) of a field whose value Configure changed (v0.5, N3:
       unset → set counts as a change, as does set → a different
       value), and (b) every
       resolved step and every step whose `BlockedBy` names it. Envfile is
       a reader of `DB`, so a DSN `--reconfigure` re-detects it although it
       precedes `database`. Choices: an artifact whose state did not
       change keeps its choice. An artifact whose state changed takes the
       AC-6 default for the new state, with one exception: interactively,
       an `env-key` whose source field the user answered in this run's
       Configure is `apply` (that answer is the consent; the AC-26 warning
       was shown before it; spec AC-6 names this exception, v0.5 N9).
       Otherwise a re-Detect never escalates to an
       overwrite, so an artifact that became `modified` gets `keep` (under
       `--yes` always). Re-detected rows are printed again. On a no-op run
       nothing is configured or resolved, so no re-Detect runs (AC-50).
    5. **Plan** every step. The combined plan, with diffs and notes, is
       printed, then one confirmation is asked (AC-5). `--dry-run` stops
       here with exit 0 (AC-13).
    6. **Apply** in order with writable ports. **Re-Detect rule B:**
       before applying step k, if any step in k's transitive `Requires`
       was applied in this run, Detect k again and re-Plan it with the
       same choices. An artifact that is now `ok` drops out, and a newly
       `modified` one is kept and reported. After Apply, Detect k again to
       check success (Design 18). Handle `Await` (Design 19). Merge and
       write the manifest when the artifact set or a hash changed
       (Design 8, AC-9). Ctrl-C stops before the next action (exit 130).
    7. **Final doctor** (AC-62, WI-S2-14b) and the summary.
16. **Typed shared state, one writer per field.** `RunState` is a struct
    in `runstate.go`. It is not a map, and no step reads another step's
    internals.

    | Field | Writer: owner step (Seed source → Configure override) | Readers (rule A) |
    |---|---|---|
    | `Inputs` (parsed flags incl. `BinDirExplicit`, `Env` snapshot) | cmd, before Run | all |
    | `Prior` (manifest, env-file `EnvDoc`) | engine preflight | all |
    | `Downgrade`, `ConfigDirChanged` | engine preflight | all, renderer |
    | `BinPath` | `binary` (Seed: `ResolveBinPath` — explicit `--bin-dir` → the manifest-recorded `binary` artifact path → `Paths.InstalledBinary()`; no Configure) | binary, hooks.scripts, mcp, jobs |
    | `Topology` (`local`/`remote`) | `topology` (Seed: manifest `topology`, else unset → Detect proposes the AC-19 default; Configure) | database, manifest |
    | `DB` (`DBTarget{Host, Port, Name, User, SSLMode, Mode existing/create, Source}` + unexported password; `DSN()` built by `dsn.go`) | `database` (Seed: `--pg-dsn` + stdin password, else the env-file DSN, else `Env` `MEMORY_PG_DSN` (v0.5, N10); Configure; `--yes` local create path → `Source=generated`) | envfile, database, migrate |
    | `Ollama` (`URL`, `Model`, `EmbedMaxTokens`) | `ollama` (Seed: flags, env file, `Env`, else the AC-32 defaults `http://127.0.0.1:11434` / `bge-m3` with `Source=default` (v0.5, N3); `EmbedMaxTokens` only from the env file or `Env`, no default, never prompted; Configure: URL/model) | envfile, ollama |
    | `NSRules` | `namespaces` (Configure) | — |
    | `PRRepos` | `jobs` [2b] (Seed: flag, env file, `Env`; Configure) | envfile, jobs |
    | `ClaudeMDTarget` | `claude-md` (Seed: manifest; Configure) | claude-md |
    | `JobPATH` | `jobs` [2b] (Seed: `LookPath(claude|git|az)`) | jobs, doctor `tools.claude` |
    | `Applied`, `Results` | engine | engine, renderer |

    Each field still has exactly one writer, its owner step, in two phases
    (Seed, then an optional Configure override). Every field carries
    `Set bool` and `Source` (`flag`/`env`/`envfile`/`manifest`/
    `default`/`generated`/`prompt`). **Seed order for env-backed fields
    (v0.5, N10):** flag → env file → `Env` (only when the env file has no
    usable value for the key; this adopts a shell-only DSN, which doctor
    fails today, `checks_env.go:114-118`) → the owner's documented
    default. A shell value that differs from the env file's is a drift
    note (`warn: your shell's MEMORY_PG_DSN differs from <env>; hooks,
    MCP and jobs use the file`) and never a source, so a stale shell
    export cannot rewrite the file on a no-op `--yes` run. **Default and
    generated values are set** (v0.5, N3) and their keys are written like
    any other; the next run seeds them from the env file, so AC-50
    holds. **Unset means "not managed in this run":**
    `envfile` leaves the matching key untouched (no artifact, no
    write), e.g. `MEMORY_PR_INGEST_REPOS` in 2a, where `jobs` does not
    exist, a hand-added key on a run without `--pr-repos`, and
    `MEMORY_EMBED_MAX_TOKENS` when neither `Env` nor the file has it (the
    binary's default 2048 applies).
    `ResolveBinPath(p Paths, m *Manifest, explicit bool) string` lives in
    `binpath.go` and is shared with doctor (WI-S2-0), so `install` and
    `doctor` agree on the binary path after `install --bin-dir X`.

    The env file has exactly one writer, the `envfile` step. It owns every
    `env-key` artifact, and its desired key set is computed in **Plan**
    from `DB`, `Ollama`, `PRRepos` and the prior file, so it sees every
    Configure result. This is why spec AC-7 (v0.3) puts `envfile` before
    `database`. The env file holding the generated password is written
    before `bootstrap.sql` (AC-21) without the database step touching the
    env file. The password lives only in `DBTarget` (unexported), the
    Redactor (registered at the prompt or stdin read, AC-30), the env file
    and `bootstrap.sql`.
17. **Step shape and output channel** (replaces spec §10 `Step`; spec v0.3
    updates it):
    ```go
    type Step interface {
        ID() string; Title() string; Requires() []string
        Detect(ctx context.Context, rc ReadPorts, st *RunState) Detection
        Plan(ctx context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error)
        Apply(ctx context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error)
    }
    type Seeder interface { // optional, v0.4: owners of RunState fields (Design 16)
        Seed(ctx context.Context, rc ReadPorts, st *RunState) ([]Note, error) // v0.5, N6: error = invalid flag → exit 2
    }
    type Configurer interface { // optional
        Configure(ctx context.Context, rc ReadPorts, ui Prompter, st *RunState) error
    }
    type Detection struct {
        State     State            // worst of Artifacts (for the table only)
        Detail    string
        BlockedBy string           // step id when blocked only by a pending prerequisite
        Remedy    string           // v0.5, N4: set when blocked by an external condition (BlockedBy empty); keys the Configure-phase blocked-step prompt (Design 15.3)
        Artifacts []ArtifactState  // {ID, State, Detail}; ID e.g. "hooks.settings/UserPromptSubmit"
        Notes     []Note
    }
    type Choices map[string]Choice      // artifact ID → apply|keep|skip (apply on modified = confirmed overwrite)
    type Plan struct { Actions []Action; Diffs []Diff; Notes []Note }
    type StepResult struct {
        Artifacts []Artifact   // recorded or updated in the manifest
        Removed   []ArtifactKey // dropped from the manifest
        Notes     []Note        // {Level: warn|info, Text}: drift, PATH, quarantine, linger, AC-70 duplicates, hand-install message
        Diffs     []Diff        // unified diffs actually applied (redacted by the renderer)
        Await     *Await
    }
    // v0.4: Action is display data for the combined plan; a step's Apply
    // executes its own Plan (it switches on its own actions).
    type Action struct { Artifact, Verb, Path, Desc string } // Verb: write|chmod|remove|run|migrate|pull|register|load
    type Await struct { // v0.5, N1: OnOK/OnOKDesc removed
        Instructions []string
        Artifacts    []string // artifact IDs the user action must turn ok, e.g. {"database/database"}
    }
    type ProgressSink func(step, label string, done, total int64) // v0.4, L1: in WritePorts, owned by the renderer
    ```
    v0.5 (N1): there is no `OnOK` callback. Follow-up work after a user
    action (deleting `bootstrap.sql`) is a Detect-visible artifact that
    the engine applies with its AC-6 default once the awaited artifacts
    are `ok`, in this run or the next one (Design 19).
    Progress (L1): `OllamaProber.Pull` already takes a `progress` callback
    (`internal/setup/ports.go:168`); what was missing is the sink. The
    `ollama` Apply forwards it to `WritePorts.Progress`, which the
    renderer implements (single-line on a TTY with `CI` unset, AC-14;
    otherwise one start and one end line).
    Steps and checks never print. The renderer (WI-S2-1b) prints `Notes`
    and `Diffs` through the redacting sink. `diff.go` (a minimal LCS
    unified diff with no dependency) is in WI-S2-1b, because env file,
    settings, skills, CLAUDE.md and job units all use it. A step with
    `BlockedBy` set whose prerequisite is chosen `apply` gets the choice
    `apply (after <dep>)`. Rule B then re-detects it, and if it is still
    blocked it is reported `blocked` (AC-7).
18. **Per-artifact choices (B-1).** The table shows one row per step, with
    the worst state. Defaults come from `DefaultChoice` per artifact
    (`state.go:47`), with `outdated → keep` when `Downgrade` is set.
    Interactive `apply` on a step applies its `absent` and `outdated`
    artifacts and asks the overwrite `Confirm` for each `modified` one
    (AC-6). `keep`/`skip` cover all of the step's artifacts. Artifacts by
    step: hooks.scripts = 2 files, hooks.settings = 2 events, skills = one
    per file, jobs = 2 jobs, envfile = the file format (export/quote
    consent) + one per managed key that is set (Design 16) + mode;
    database = the database + `bootstrap-file` (v0.4, M2; v0.5, N1: a
    transient artifact, never recorded in the manifest); every other
    step = 1.
    **Success** after Apply: every artifact chosen `apply` re-Detects
    `ok`. A kept `modified` artifact is not a failure. It becomes a `warn:
    drift` note, and the exit code stays 0 (AC-52). `MergeSettings` takes
    `overwrite map[string]bool` (per event) instead of
    `overwriteModified bool` (`settings.go:391`).
    `aggregateHookStates` stays only for doctor's one-line detail.
19. **Pause for a user action (`Await`).** Apply may return
    `StepResult.Await` (Design 17). This is used today only by `database`
    (create path: print the `bootstrap.sql` command). *(v0.4, M1: a step
    that is already `blocked` before Apply — AC-18 prereq hints, `jobs`
    backend `none`, Ollama absent — never reaches Apply, because its
    default choice is `skip`. Its `re-check / skip / quit` prompt is the
    Configure-phase blocked-step prompt, Design 15.3.)*
    - Interactive: print the instructions, then `Select(re-check / skip /
      quit)`. **re-check** runs only that step's Detect (AC-10). (v0.5,
      N1) It passes when every artifact in `Await.Artifacts` re-detects
      `ok`; the other artifacts' states do not matter for the re-check.
      The engine then gives each remaining non-`ok` artifact of the step
      its AC-6 default (`bootstrap-file` `outdated` → `apply`), re-Plans
      and Applies it once with the writable ports, and re-Detects for
      the success rule (Design 18). An `Await` returned by that second
      Apply is a step failure (no loop). A re-check that does not pass
      prints the detail and offers the prompt again. **skip** marks the step
      `blocked: skipped by you`, so its dependents are blocked (AC-7).
      **quit** = cancel, exit 130 (nothing further is applied; artifacts
      already applied are recorded).
    - `--yes` / non-interactive: no wait. The step ends `blocked: awaiting
      user action` with the instructions as its remedy, dependents are
      blocked, independent steps continue, and the exit code is 1. The
      next `install --yes` re-detects and finishes.
    - **`--yes` with no database (AC-21, review #1).** Topology `remote`
      with no `--pg-dsn`/`MEMORY_PG_DSN`/env-file DSN fails `database`
      with "pass --pg-dsn (and --pg-password-stdin)" (AC-12). Topology
      `local` with no DSN takes the **create** path with defaults
      (`localhost:5432`, `claude_memory`/`claude_memory`, a generated
      password, `sslmode=disable` for loopback). `envfile` writes the DSN,
      `database` writes `bootstrap.sql` and ends `Await` → exit 1 with the
      command printed. Topology `local` with a DSN whose database answers
      `nodb`, or that lacks `vector`, takes the create path with that DSN's
      password, subject to the alphabet rule (WI-S2-4b). An `auth` failure
      under `--yes` fails the step: the password is never re-asked.
    - **Pending bootstrap (v0.4, M2).** A missing role probes as `auth`
      (`28P01`/`28000`, `internal/setup/ports.go:112`), so on the second
      `--yes` run before the user ran the command, "auth → fail" would
      misreport it. Rule: when `<StateDir>/bootstrap.sql` exists and the
      probe class is `auth` or `nodb`, or `vector` is missing, Detect
      reports `blocked: awaiting user action` with the command reprinted
      (detail also names the AC-21 "role already existed with another
      password" case). Exit 1, no re-render, no new password (DB is seeded
      from the env file). When the probe is `ok` and the file still
      exists, the `database` step's second artifact, `bootstrap-file`, is
      `outdated` → apply removes it. `auth` with no `bootstrap.sql` still
      fails the step.
    - **`bootstrap-file` states (v0.5, N1).** No file → `ok` (never
      `absent`, so a no-op run proposes nothing). File present while the
      `database` artifact is pending (above) → `ok` (the file is still
      needed). File present and `database` `ok` → `outdated`. It is never
      recorded in the manifest (`StepResult.Artifacts` omits it), so
      uninstall ignores it; doctor keeps reporting a leftover file via
      `dirs.state` (`info`, `internal/setup/checks_misc.go:172-199`).
20. **Read-only vs writable ports, typed (v0.4, M4: narrowed).**
    v0.3 claimed "a write in Detect does not compile", which was false:
    `ReadPorts.FS` had type `FS` (with `WriteFileAtomic`/`MkdirAll`/
    `Remove`/`Lock`), `DBProber` has `Migrate`, and `OllamaProber` has
    `Pull` (an HTTP mutation with no runtime guard). v0.4 splits the
    consumer-declared interfaces in `ports.go`:
    - `ReadFS` = the read half of `FS` (`ReadFile`, `Stat`, `Lstat`,
      `ReadDir`, `EvalSymlinks`, `Writable`); `FS` embeds `ReadFS` and
      adds the writes and `Lock`.
    - `DBProbe` = `Probe` + `LocalServerEvidence`; `DBProber` embeds it
      and adds `Migrate`.
    - `OllamaProbe` = `Version` + `HasModel` + `EmbedDims`;
      `OllamaProber` embeds it and adds `Pull`.
    - `JobDetector` = `Render` + `Detect`; `JobManager` embeds it and adds
      `Install`/`Remove`.

    `ReadPorts{FS ReadFS, Runner (read-only adapter), DB DBProbe, Ollama
    OllamaProbe, Jobs JobDetector, Clock, Paths, Env, Platform, Assets}`
    and `WritePorts{ReadPorts, FS FS (writable), Runner (mutating
    allowed), DB DBProber, Ollama OllamaProber, Jobs JobManager,
    ClaudeCLI, Progress ProgressSink}`. Detect, Seed, Configure and Plan
    receive only `ReadPorts`, so a file write, a migration, a model pull,
    a job install or an MCP add from them **does not compile**. The one
    remaining runtime guard is `Runner`: mutation is a `Cmd.Mutating`
    flag, so a mutating command from Detect is refused by the read-only
    adapter (`ErrReadOnly`) and caught by the call-log tests, not by the
    compiler. The doctor keeps its `DoctorDeps` and may narrow to the
    same read interfaces (optional, WI-S2-1a). (v0.5, N2) The slice-1
    read helpers that Detect/Seed call take the full `FS` today and so
    would not accept a `ReadFS`; WI-S2-1a narrows their parameter to
    `ReadFS`: `LoadManifest` (`manifest.go:174`), `InGitRepo`
    (`gitrepo.go:18`), `ReadMCPRegistration` (`mcpreg.go:86`),
    `ReadSettingsFile`/`ReadSettingsFileFollow`/`readSettingsFile`/
    `underDir` (`settings.go:550/557/561/601`), `ScanOtherSettings`
    (`settings.go:707`) and the `LaunchdJobs.FS` field
    (`jobs_launchd.go:61`). The writers `SaveManifest` (`:212`),
    `BackupCorruptManifest` (`:225`), `WriteSettingsFile`
    (`settings.go:629`) and its `recheckSettings` (`:653`) keep `FS`.
    Callers that hold an `FS` still compile, since `FS` embeds `ReadFS`. Under `--dry-run`
    the engine is built with `WritePorts` whose FS and Runner are the
    read-only adapters, and the engine never reaches Apply anyway. That
    gives two independent layers. `cmd` builds both: `readOnlyPorts()`
    (doctor, Detect, final doctor) and `writablePorts()` (install Apply,
    uninstall). There is still exactly one place that constructs
    adapters (`main.go`, golang-architecture "Composition Root").
    `internal/namespace` writes with `os` directly
    (`internal/namespace/file.go:20-44`), so the `namespaces` step does
    not call `namespace.Init`/`Add`. It reads with `FS.ReadFile` +
    `namespace.Parse` (`namespace.go:64`), edits the `*Config`, renders
    with the new `namespace.Marshal`, and writes with
    `WritePorts.FS.WriteFileAtomic`. The spec §10 `Namespaces` port is
    dropped.
21. **Dry-run and the lock.** The engine calls `FS.Lock` only in writable
    mode. `install --dry-run`, `uninstall --dry-run` and doctor never lock,
    because they never write and a concurrent writer only makes their
    output stale. `readOnlyFS.Lock` keeps returning `ErrDryRun`
    (`cmd/claude-memory/setup_adapters.go:61`), so a stray Lock call in
    dry-run fails loudly. Lock contention in writable mode exits 2 (AC-9).
22. **Configure-time prompts by owner** (closes the "no place for
    prompts" half of #1): AC-19 topology and the AC-26 warning on a
    topology change → `topology` (WI-S2-4a, which also owns the shared
    `warnDataStays` helper). AC-20 DSN fields and the 3× auth re-ask, the
    AC-21 existing/create choice and the AC-26 warning on a DSN change →
    `database` (WI-S2-4b). AC-33 remote-Ollama warning and
    confirm, and the pull y/N → `ollama` (the pull itself runs in Apply).
    AC-47 mappings → `namespaces`. AC-43 PR repos → `jobs`. AC-42 target
    → `claude-md`. The AC-41 "show diff / overwrite / keep" and the AC-28
    export-conversion consent are per-artifact choices (Design 18), not
    Configure.
23. **Retained artifact kinds and manifest emptying (#5).** `env-key` and
    `dir` are *retained*: install records them so doctor and re-runs know
    what is managed. `uninstall` never reverses them, because the env file,
    `namespaces.yaml` and our directories are kept by AC-54, but it does
    drop them from the manifest. "Full uninstall" means every
    non-retained artifact was reversed. The manifest is then deleted, even
    though retained kinds were listed. A kept `modified` artifact keeps the
    manifest alive with only the kept entries. `install.lock` is left in
    place (deleting a flock target while another process may open it is a
    race, and the file is empty). Settings backups are left in place (they
    are the byte-level rollback). If install created `settings.json`
    (recorded as **`CreatedFile`** / `created_file: true` on the
    `settings-hook` artifacts — a new field beside the existing
    `CreatedContainer`, `internal/setup/manifest.go:56`, which keeps its
    meaning "install created the `hooks` object"), uninstall deletes it
    only when the unmerge result is exactly `{}` modulo whitespace.
    **`dir` artifacts (v0.4, M3).** A `dir` artifact is recorded only
    for a directory install itself created (it did not exist at Detect).
    Two classes, decided by path:
    - *owned*, under `<ClaudeDir>`: `hooks/claude-memory`,
      `skills/<name>`. Not retained: uninstall removes each after its
      files, only when empty (a non-empty one is kept and listed, and its
      artifact stays in the manifest).
    - *retained*: `<ConfigDir>`, `<StateDir>`, `<BinDir>` (v0.5, N5:
      `<BinDir>` = `filepath.Dir(RunState.BinPath)`, not
      `Paths.BinDir`) (shared with
      other tools, and holding the kept env file, `namespaces.yaml`, job
      logs). Dropped from the manifest, never removed.

    Parents that `MkdirAll` created on the way (`~/.config`, `~/.local`,
    `<ClaudeDir>/hooks`, `<ClaudeDir>/skills`, `<ClaudeDir>` itself) are
    not recorded and are left in place.
24. **DSN encoding (B-2).** `dsn.go` builds `postgresql://` +
    `pctEncode(user) ":" pctEncode(password) "@" host ":" port "/"
    pctEncode(db) "?sslmode=…"` (v0.4, L12: the database name is encoded
    too). Validation runs on **every** source of a `DBTarget` (prompt,
    `--pg-dsn`, `Env`, env file), in `dsn.go`: host per below, port
    1–65535, database name non-empty, `sslmode` in
    `disable|allow|prefer|require|verify-ca|verify-full`; a value that
    fails is never written: from a flag → `Seed` returns an error → exit
    2 (v0.5, N6); from `Env`/env file →
    Seed leaves `DB` unset with a note (the step then needs input:
    Configure asks, `--yes` fails with the flag name; v0.5: an invalid
    value that is present is not "missing", so no Design 15.3 default
    replaces it); at a prompt → re-ask.
    The stricter `^[a-z_][a-z0-9_]{0,62}$` applies to role/database
    names only on the create path (`bootstrap.sql`, WI-S2-4b). `pctEncode` escapes every byte outside RFC 3986
    *unreserved* (`A-Za-z0-9-._~`), so `$ & ' ( ) * + , ; = : @ / ? # %`
    and spaces are all `%XX`. `url.UserPassword` leaves sub-delims such as
    `$` unescaped, and `config.classifyValue` (`config.go:439`) then flags
    the installer's own file. A host is accepted as a hostname, an IPv4
    address or a bracketed IPv6 address, and nothing else. The Redactor
    registers this form beside its existing ones (WI-S2-0). Test:
    round-trip through `url.Parse` → `User.Password()` equals the input,
    and `config.ParseEnvData` of the written line yields no finding, for a
    table that includes the `S3ntinel-pw-$@:/x` sentinel and `pa$word`.

## Work Items

```
S1:  S1-0 (fixtures) ─> S1-1 (dispatch/version/migrate) ─┬─> S1-2 (ParseEnvFile)
                                                         ├─> S1-3 (embed pkgs + rewording)
     S1-4 (pg Probe/Migrate) ─────────────────────────────┤
     S1-5 (ollama Prober) ────────────────────────────────┤
     S1-6 (setup values, ports, redactor, fakes, guard) ──┼─> S1-7 (settings codec+merge)
                                                         ├─> S1-8 (md block + git detect)
                                                         ├─> S1-9 (.claude.json reader)
                                                         └─> S1-10 (manifest reader + launchd detect)
                                       S1-7..S1-10 ─> S1-11 (doctor checks) ─> S1-12 (doctor cmd + json + DEPLOY.md)
S2-pre: S2-0 (guard, CRLF, dsnPasswords, RenderHookScript, goldens)            [own small PR]
S2a:  S2-1a (RunState, ports split, writable FS, lock) ─> S2-1c (engine phases, manifest write) ─┬─> S2-1b (renderer, diff, preflight warnings)
                                                                                                 └─> S2-2 (prompter + x/term)
      S2-2 ─> S2-3 (platform/prereqs/binary/root) ─> S2-4a (topology + DSN builder) ─> S2-5 (EnvDoc + envfile step, needs DB.DSN())
      S2-5 ─> S2-4b (bootstrap.sql + database step) ─> S2-6 (migrate) ─> S2-7 (ollama) ─> S2-8 (namespaces)
      S2-1b + S2-3..S2-8 ─> S2-14a (install cmd, builders, --dry-run, --upgrade) ─> S2-14b (final doctor + cross-cutting tests)
S2b:  S2-9 (hooks) ; S2-10 (mcp) ; S2-11 (skills) ; S2-12 (claude-md)        [each needs S2-14a]
      S2-13a (launchd install) ─> S2-13b (systemd + jobs step) ─> S2-17 (doctor S2 deltas)
      S2-9..S2-13b ─> S2-15 (uninstall) ─> S2-16 (e2e + CI + docs)
```

### Slice 1 — doctor + plumbing

**WI-S1-0 [S1] Record fixtures.** files:
`internal/setup/testdata/fixtures/`.
- Already verified by the review (claude 2.1.287), saved as fixtures:
  `.claude.json` with and without `mcpServers.claude-memory` (`{type:
  "stdio", command, args, env:{}}`), with a local-scope entry under
  `projects.<path>.mcpServers`; `CLAUDE_CONFIG_DIR` relocates
  `.claude.json`; `claude mcp add` defaults to scope `local`.
- Still to check on the owner's Mac: `launchctl print
  gui/$UID/io.github.claude-memory.cleanup` loaded vs not (and the
  `bootout` "not loaded" exit status); whether a hook `command` with
  `$HOME` expands; whether a running Claude Code session reloads
  `settings.json` (drives the AC-62 restart line); the
  `~/.claude/projects` encoding of a hyphenated path; whether Claude Code
  rejects unknown keys in a hook entry (confirms Design 3's "no marker
  key").

Satisfies spec §4 assumptions.

**WI-S1-1 [S1] Early dispatch, `version`, `migrate`, `Paths`.** files:
`cmd/claude-memory/main.go`, `version.go`, `migrate.go`, `paths.go`,
`paths_test.go`, `main_test.go`, `Makefile`.
- In `run()`, dispatch `doctor|version` before config load (S2 adds
  `install|uninstall`). `migrate` runs after config load: `postgres.New`
  + `Close`, prints the AC-3 line.
- `version`: `var version = ""`; else `debug.ReadBuildInfo()`
  (`vcs.revision`, `vcs.modified`); else `dev`.
- `buildPaths`/`buildEnv` (AC-68) incl. `CLAUDE_CONFIG_DIR` moving
  `.claude.json`; `HOME` unset/relative → error used for exit 3/2.
- Makefile: one `LDFLAGS` used by `build`, `install`, `cross-build`;
  cross-build adds darwin/amd64, linux/arm64.
- Tests: a child process with `HOME=<tempdir>` in its env (no
  `t.Setenv`) runs `doctor --json` without a config-load error (lands
  with WI-S1-12; stubbed here); version formatter; `buildPaths` table.

Satisfies AC-1 (S1 half), AC-2, AC-3 (unit half), AC-68 (builder).

**WI-S1-2 [S1] `config.ParseEnvFile`.** files: `internal/config/config.go`,
`config_test.go`.
- `type EnvFile struct{ Values map[string]string; Order []string; Mode
  fs.FileMode; Findings []Finding }`; finding kinds `export-prefix`,
  `quoted-whole-value`, `unparseable-value`, `duplicate`, `no-equals`,
  `group-world-readable`.
- `LoadFromFile` = `ParseEnvFile` + the existing perm error +
  `Setenv`-if-unset. Behavior identical; existing tests green.

Satisfies AC-27.

**WI-S1-3 [S1] Embedded asset packages + location-neutral wording.**
files: `integration/embed.go`, `integration/embed_test.go`,
`integration/claude-md-section.md` (new), `integration/claude-md-snippet.md`
(points at the section), `integration/skills/remember/SKILL.md`,
`integration/skills/memory-digest/SKILL.md`, `deploy/embed.go`.
- `integration`: hooks, skills, launchd, `claude-md-section.md`,
  `settings.snippet.json`. `deploy`: `initdb` (S2 adds `app-role.psql`;
  S3 adds compose files and `.env.example` by explicit name).
- Reword both skills: "the memory section of your CLAUDE.md" instead of
  `acme/CLAUDE.md`; "all your projects" instead of "all `acme/`
  repos".
- Test: every asset path referenced by `internal/setup` exists in the
  FS.

Satisfies AC-34 (embed half), AC-66 (skills wording).

**WI-S1-4 [S1] Postgres probe + migrate adapters.** files:
`internal/postgres/probe.go`, `schema_objects.go`, `probe_test.go`,
`probe_integration_test.go`.
- `Probe(ctx, dsn) (DBStatus, error)`: `pgxpool.ParseConfig`, connect
  with a timeout (no migrations — `Open` path), ping RTT, TLS in use
  (`pg_stat_ssl`), `SELECT extversion FROM pg_extension WHERE
  extname='vector'`, the schema-object table (Design 6). Error class
  (`unreachable|auth|nodb|hba|other`) from `*pgconn.PgError` SQLSTATE
  (`28P01`, `28000` + hba text, `3D000`) and `net.Error`/deadline.
- `Migrate(ctx, dsn)` = `New` + `Close`.
- Integration tests (tag; `MEMORY_TEST_PG_ADMIN_DSN`): `Probe` on a
  migrated DB reports all objects; wrong password → `auth`; missing DB →
  `nodb`; `Migrate` twice is a no-op.
- Unit test: every `migrations/*.sql` has a schema-object entry.

Satisfies AC-3 (integration), AC-58 (`pg.*` data), AC-65 (S1 half).

**WI-S1-5 [S1] Ollama prober.** files: `internal/ollama/prober.go`,
`prober_test.go`.
- `/api/version`, `/api/tags`, one `/api/embed` → `len(embeddings[0])` +
  latency; `POST /api/pull` (NDJSON progress, `status:"success"`
  terminator) implemented and tested here, first used in S2.
- `httptest.Server` tests: wrong dims, 404 model, timeout, pull stream.

Satisfies AC-32 (prober).

**WI-S1-6 [S1] `internal/setup` values, ports, redactor, fakes, guard.**
files: `internal/setup/paths.go` (`Paths`, `Env`), `platform.go`
(`PlatformInfo`), `ports.go`, `state.go` (`Status`, `State`),
`redact.go`, `redact_test.go`, `fakes_test.go`, `guard_test.go`;
`cmd/claude-memory/setup_adapters.go` (read-only `osFS`, `execRunner`,
`systemClock`), `cmd/claude-memory/platform.go` (`detectPlatform`, S1:
OS/arch/version/WSL case-insensitive, launchd backend), `platform_test.go`.
- `Redactor`: always masks `scheme://user:…@` userinfo; literal secrets
  (plus `QueryEscape`/`PathEscape` forms) only when ≥ 8 characters;
  `Register` callable before the secret is used.
- Fakes: `FakeRunner.Script(match func(Cmd) bool, Result)`, fails on
  unmatched commands, records calls; `FakeFS` = real temp dir behind the
  port with fault injection; `FakeClock`; (S2: `FakePrompter`).
- `guard_test.go`: `go list -deps ./internal/setup` excludes `os/exec`,
  `net/http`, pgx, `x/term` (skipped under `-short`; CI runs it once); a
  source grep for `os.Getenv|LookupEnv|Environ|UserHomeDir|Getuid|
  Executable`; a helper that asserts every recorded FakeRunner call
  against the AC-18 deny list, used by all setup tests.

Satisfies AC-15, AC-18 (guard), AC-30 (redactor), AC-63, AC-68.

**WI-S1-7 [S1] settings.json codec + merge library.** files:
`internal/setup/jsonobj.go`, `jsonobj_test.go`, `settings.go`,
`settings_test.go`, `testdata/settings/*`.
- `jsonobj` per Design 2 (raw slices, `UseNumber`, `InputOffset`,
  duplicate-key refusal, indentation detection).
- `settings.Analyze(b) → per-event {ours []Entry, others int}` (read-only,
  used by doctor); `Merge(b, desired, recorded)` and `Unmerge(b,
  recorded)` pure functions returning bytes + `Summary{added, replaced,
  deduped, removed, drift}` + `changed`.
- Golden cases (each run twice; no-op cases assert `changed == false`):
  `missing`, `empty-object`, `no-hooks-key`, `other-events`,
  `other-hooks-same-event`, `ours-identical`, `ours-recorded-outdated`,
  `ours-legacy-$HOME`, `ours-user-timeout`, `ours-duplicated`,
  `ours-under-matcher`, `unknown-top-level-keys-order`, `tab-indented`,
  `four-space`, `unicode-escapes-unchanged-noop`, `big-number-unchanged`,
  `local-settings-duplicate` (Analyze), refusals `comments`,
  `trailing-comma`, `hooks-is-array`, `truncated`, `duplicate-key`.
  Each has `.in.json`, `.merge.golden.json`, `.unmerge.golden.json`.

Satisfies AC-37 (library), AC-38 (library), AC-64 (part), AC-70 (parser).

**WI-S1-8 [S1] Markdown managed-block library + git detection.** files:
`internal/setup/mdblock.go`, `mdblock_test.go`, `gitrepo.go`,
`testdata/mdblock/*`.
- `Upsert(b, section) (out, changed, err)`, `Remove(b)`, `Find(b)`;
  refuse unbalanced/duplicated markers; detect a hand-pasted heading
  without markers.
- `InGitRepo(fs, path)`: walk up for `.git` as file or directory.
- Golden: insert at end, refresh, idempotent, remove, unbalanced,
  duplicated, hand-pasted.

Satisfies AC-42 (library), AC-64 (part).

**WI-S1-9 [S1] `.claude.json` MCP reader.** files:
`internal/setup/mcpreg.go`, `mcpreg_test.go`.
- `Read(fs, paths) (Registration, error)` per Design 11, against the
  WI-S1-0 fixtures: absent file, no `mcpServers`, ok, other command,
  other args, local-scope shadow, unparseable.
- No Runner involvement at all.

Satisfies AC-40 (detect).

**WI-S1-10 [S1] Manifest reader + launchd read-only detection.** files:
`internal/setup/manifest.go` (types, schema, `Load`; corrupt → absent +
reported), `manifest_test.go`, `jobs_launchd.go` (`Detect` only: plist
read, `ProgramArguments[0]`, `EnvironmentVariables.PATH`, one read-only
`launchctl print gui/<uid>/<label>`), `jobs_launchd_test.go`.
- Legacy plist (`run-with-env.sh`) fixture → `outdated`; PATH missing
  `claude`/`git`/`az` directories → drift detail.

Satisfies AC-44 (doctor), AC-49 (read).

**WI-S1-11 [S1] Doctor checks.** files: `internal/setup/doctor.go`
(registry + parallel runner, Design 13), `checks_env.go`, `checks_db.go`,
`checks_ollama.go`, `checks_claude.go` (mcp, hooks.scripts,
hooks.settings incl. AC-70 files, skills, claude-md), `checks_misc.go`
(binary, tools, namespaces, jobs, dirs.state incl. leftover
`bootstrap.sql`, manifest), `doctor_test.go`,
`testdata/doctor/*.golden.json`.
- 23 checks per AC-58; dependents `skip`; skills/hook scripts compared
  against the manifest when present, else the embedded assets; on linux
  `jobs` is `info: not checked yet` until WI-S2-13.
- Tests: one per check id × branch; hanging probers → bounded by
  `--timeout`/`--deadline`; healthy fakes finish fast; sentinel password
  never in output; FS write count 0 and mutating Runner count 0;
  **AC-67 unit test**: `.claude.json` fixture registers a sentinel
  command; the FakeRunner fails on that argv[0] or any `mcp get`/`mcp
  list`.

Satisfies AC-41 (doctor), AC-57, AC-58, AC-59 (logic), AC-67 (unit),
AC-70 (doctor).

**WI-S1-12 [S1] `doctor` command + JSON + DEPLOY.md.** files:
`cmd/claude-memory/doctor.go`, `doctor_test.go`, `DEPLOY.md`,
`docs/specs/README.md`, `.github/workflows/ci.yml`.
- Flags `--json`, `--strict`, `--timeout`, `--deadline`; text renderer
  (aligned `status id detail`, a `fix:` line per non-pass, a summary);
  JSON per AC-60 (all keys always present, `platform.config_dir`,
  `platform.bin`); exit 0/1/2/3.
- **AC-67 cmd test**: build the binary; temp `HOME` with a `.claude.json`
  registering a marker-creating script; a fake `claude` first on PATH
  that also creates a marker; run `doctor`; assert no marker.
- DEPLOY.md: laptop env snippet → plain `KEY=VALUE`; password recipes →
  `openssl rand -hex 32`.
- CI: integration job adds `./cmd/claude-memory/...`.
- Manual: run on the owner's current setup and paste the output here.
  Expected: pass/warn only, with `jobs` warning about `run-with-env.sh`
  and `hooks.settings` reporting the legacy `$HOME` form; any fail is a
  real finding.

Satisfies AC-1 (verify), AC-4 (doctor flags), AC-59, AC-60, AC-66 (S1
half), AC-67 (cmd).

### Slice 2 — install, upgrade, uninstall (v0.3)

IDs are stable from v0.2 where the item survives. Split items get
letters, and new items get new numbers (map at the end). Every step WI
adds its own goldens under `testdata/<area>/`, each run twice (AC-64 [S2]),
and its own rows in the cross-cutting suites of WI-S2-14b (sentinel,
no-op, dry-run). v0.4 (L11): a step WI that renders a text file names
`AC-64 (goldens)` in its Satisfies line; steps that render no file
(platform, prereqs, binary, topology, migrate, ollama, mcp) cover their
AC-64 share with the run-twice idempotence row in WI-S2-14b.

#### Pre-work (own PR on slice 1)

**WI-S2-0 [S2-pre] Slice-1 hardening before S2 writes user files.**
files: `internal/setup/guard_test.go`, `jsonobj.go`, `settings_test.go`,
`testdata/settings/crlf.*`, `doctor.go`, `redact.go`, `redact_test.go`,
`hookscript.go` (new), `hookscript_test.go`, `checks_claude.go`,
`doctor_test.go`, `testdata/doctor/*.golden.json`; v0.4: `dsn.go` (new,
`pctEncode` only; WI-S2-4a adds the builder), `dsn_test.go`,
`binpath.go` (new), `binpath_test.go`, `checks_misc.go`,
`jobs_launchd.go` (`DefaultJobSpecs(p, binPath)`).
- Guard (`guard_test.go:65-116`):
  - Walk every non-test `.go` file under `internal/setup/...`, not only
    `*.go` in the package directory.
  - Add the FS verbs `Open`, `OpenFile`, `Create`, `CreateTemp`,
    `ReadFile`, `WriteFile`, `ReadDir`, `Stat`, `Lstat`, `Mkdir*`,
    `Remove*`, `Rename`, `Chmod`, `Chown`, `Symlink`, `Link`,
    `Truncate` to `bannedOSCalls`.
  - Ban `net.Dial*` and `net.Listen*`.
  - Allowed `os` identifiers: `ErrNotExist`, `ErrExist`, `ErrPermission`,
    `FileMode` and the `Mode*` constants.
- CRLF: `jsonEmitter.indent` and the final newline use the input's
  newline, detected like `mdblock.newlineOf` (`mdblock.go:170`). Add a
  `crlf` golden (merge + unmerge, twice).
- `dsnPasswords` (`doctor.go:324`): use `url.Parse` →
  `User.Password()`, and fall back to the current split only when parsing
  fails. Key/value DSNs get a quote-aware scanner. The userinfo pattern
  (`redact.go:24`) anchors on the last `@` before the first `/`. The
  `Redactor.Register` forms (`redact.go:48-53`) add the full
  percent-encoded form from Design 24 (shared `pctEncode`, which moves to
  `dsn.go` here).
- `RenderHookScript(asset []byte, binPath string) ([]byte, error)`
  replaces the default in `CLAUDE_MEMORY_BIN="${CLAUDE_MEMORY_BIN:-…}"`
  (the line `hookBinRe` matches, `checks_claude.go:130`). It errors when
  the line is missing, and `binPath` must be absolute and contain no `"`,
  `$`, `` ` `` or `\`. Doctor `checkHookScripts` (`checks_claude.go:171`)
  compares against `RenderHookScript(embedded, binPath)`.
- **Sticky binary path (v0.4, H3).** `ResolveBinPath(p, m, explicit)`
  (Design 16): explicit `--bin-dir` → the manifest's `binary` `file`
  artifact path (`Step: "binary"`, `Kind: file`) → `Paths.InstalledBinary()`. Doctor has no `--bin-dir`
  (`cmd/claude-memory/doctor.go:30-55`, `buildSetupDeps(ctx, "")` at
  `main.go:90`), so it calls it with `explicit=false` and the manifest it
  already loads. Every doctor use of `Paths.InstalledBinary()` switches
  to that value: `checkMCP` (`checks_claude.go:76`), `checkHookScripts`,
  `binary.version`, and `jobs` via `DefaultJobSpecs(p, binPath)`
  (`jobs_launchd.go:52`). (v0.5, N5) The `binary` not-on-PATH check
  (`checks_misc.go:39-41`) tests `filepath.Dir(binPath)` instead of
  `Paths.BinDir`. Test: a manifest recording
  `/opt/x/claude-memory` + matching hook scripts, MCP and plists → doctor
  reports no binary-path warning, and with `/opt/x` on `PATH` no
  not-on-PATH info.
  A file byte-equal to the **raw** embedded script (a manual install of
  this version) is `ok` in doctor with the detail "manual install"
  (unchanged behaviour), and `outdated` in install Detect (spec AC-36
  v0.3).
- Doctor goldens: strip `detail` from every check except `env.file`,
  `mcp.registered` and `manifest` before comparison. The key-set
  assertion (`doctor_test.go:898-919`) is the schema contract, and a
  comment says so.

Satisfies AC-30 (redactor edges), AC-35 (doctor half of the sticky
path), AC-36 (doctor compare), AC-37 (CRLF), AC-60 (golden scope),
AC-63, AC-68.

#### PR 2a — install core

**WI-S2-1a [S2a] `RunState`, ports split, writable FS, lock** (v0.4:
split from the engine). files: `internal/setup/runstate.go` (`RunState`
with `Set`/`Source` per field, `Detection`, `Choices`, `Plan`,
`Action`, `StepResult`, `Await`, `ProgressSink`, `Step`, `Seeder`,
`Configurer`), `ports.go` (`ReadFS`/`FS`, `DBProbe`/`DBProber`,
`OllamaProbe`/`OllamaProber`, `JobDetector`/`JobManager` with the
recorded-hash `Detect`, `ReadPorts`, `WritePorts`; Design 20),
`lock.go`, `fakes_test.go` (fakes satisfy the narrowed interfaces);
v0.5 (N2): `manifest.go` (`LoadManifest`), `gitrepo.go` (`InGitRepo`),
`mcpreg.go` (`ReadMCPRegistration`), `settings.go` (`ReadSettingsFile`,
`ReadSettingsFileFollow`, `readSettingsFile`, `underDir`,
`ScanOtherSettings`), `jobs_launchd.go` (`LaunchdJobs.FS`) narrowed to
`ReadFS` per Design 20 (writers keep `FS`);
`cmd/claude-memory/setup_adapters.go` (`writableFS`: `WriteFileAtomic`
temp+fsync+rename keeping mode, `MkdirAll`, `Remove`, `Chmod`, `Lock`
via `syscall.Flock` LOCK_EX|LOCK_NB), `setup_adapters_test.go`.
- A reflection test asserts the `ReadPorts` field types are exactly
  `ReadFS`/`DBProbe`/`OllamaProbe`/`JobDetector`, so a later widening
  fails a test (the compiler then enforces the rest).
- Tests: fault-injecting rename leaves the original intact (AC-9); a
  second `Lock` on the same path fails; `writableFS` keeps the mode.

Satisfies AC-9 (atomic write, lock), AC-13 (structural half), AC-68.

**WI-S2-1c [S2a] Engine phases + manifest write.** files:
`internal/setup/engine.go`, `preflight.go` (v0.5, N8: moved from
WI-S2-1b — lock, manifest load, corrupt backup, config-dir and
downgrade detection; returns its warnings as `Notes`), `preflight_test.go`,
`manifest.go` (merge, write-on-change,
trim, retained and owned `dir` kinds, `CreatedFile` field),
`engine_test.go`, `fakes_test.go` (`FakeStep`, `FakePrompter`).
- Phases 0–6 per Design 15 (incl. Seed, the Configure-phase
  blocked-step prompt and rule A by reader). Per-artifact choices and
  the success rule per Design 18. `Await` handling per Design 19.
- `Requires` and AC-7 order (v0.3: `envfile` before `database`).
  `BlockedBy` → `apply (after <dep>)`. `--skip` (AC-53). Failure
  isolation: nothing is recorded for a failed step (AC-8). The manifest
  is written only on change (Design 8), with `topology`, `jobs_backend`,
  `claude_config_dir` and `binary_version` recorded (AC-49). Exit codes
  0/1/2/130.
- Tests (fake steps): call order; no Apply before the confirm; re-Detect
  rule A and rule B; a `modified` + `absent` artifact pair under `--yes`
  → the absent one applied, the modified one kept, step success, drift
  note, exit 0 (B-1 regression); `Await` re-check/skip/quit; `Await`
  under `--yes` → exit 1 with dependents blocked; a failing step records
  nothing; manifest bytes unchanged on a no-op run; the lock held → exit
  2; dry-run never calls `Lock` (the fake FS fails on `Lock`).
- v0.4 tests: (H1) a no-op run with fakes — Seed fills `DB`/`Topology`/
  `Ollama` from `Prior`, no Configure runs, `envfile` Detect is `ok`, 0
  writes; an unset `PRRepos` leaves a hand-added `MEMORY_PR_INGEST_REPOS`
  line byte-identical. (H2) a `--reconfigure` that changes `DB` in
  `database` Configure re-detects `envfile` (earlier in order) and the
  DSN key becomes `apply`; a field nobody reads re-detects nothing.
  (M1) a fake step `blocked` with a remedy: interactive re-check →
  resolved → its dependents re-detected; skip → dependents blocked;
  quit → 130 with 0 writes; `--yes` → no prompt, blocked, exit 1.
- v0.5 tests: (N1) a fake step returning `Await{Artifacts: {"s/a"}}`
  with a second artifact `s/f` that turns `outdated` once `s/a` is `ok`:
  re-check passes on `s/a` alone, `s/f` is applied once with its default,
  the step succeeds, `s/f` is not in the manifest; a second `Await` from
  that follow-up Apply → step failed. (N3) a fresh `--yes` run where an
  owner's Seed leaves a field unset and its `--yes` Configure sets a
  default → the reader is re-detected and its key is `apply`. (N6) Seed
  error → exit 2 before any Detect; Seed notes printed before the table.
  Preflight (N8): corrupt manifest backed up once (writable) or only
  reported (dry-run); config-dir and downgrade notes; downgrade →
  `outdated → keep`.

Satisfies AC-5, AC-6, AC-7, AC-8, AC-9 (conditional manifest write),
AC-10 (blocked re-check), AC-12 (engine), AC-18 (blocked prompt),
AC-49 (write, corrupt backup, dir-changed detection), AC-50 (engine
half), AC-53 (`--skip`), spec §8 downgrade (detection).

**WI-S2-1b [S2a] Renderer, diff, preflight warnings.** files:
`internal/setup/render.go`, `diff.go`, `render_test.go`,
`diff_test.go`, `testdata/render/*`. (v0.5, N8: `preflight.go` moved to
WI-S2-1c, which the engine needs first; 1b only renders its notes.)
- Status table, combined plan with unified diffs (redacted; the DSN line
  shows `***`), `✓ step — detail` / `✗ step — error · fix: …`, `Notes`
  grouped as warn/info, the final summary. Color only when stdout is a
  TTY and `NO_COLOR` is unset. Single-line progress only on a TTY with
  `CI` unset (AC-14).
- `diff.go`: a minimal LCS unified diff with 3 context lines and no
  dependency.
- Preflight notes (from WI-S2-1c's `preflight.go`): corrupt-manifest
  backup or report, the changed-config-dir warning before Detect
  (AC-49), the downgrade warning (spec §8), rendered with the Seed notes
  before the status table.
- Goldens: status table, plan with diffs, dry-run output, apply lines,
  color-off, preflight notes.

Satisfies AC-13 (rendering half), AC-14, AC-49 (warning rendering),
spec §8 downgrade (warning rendering).

**WI-S2-2 [S2a] Prompter.** files: `internal/setup/prompt.go`,
`prompt_test.go`, `cmd/claude-memory/setup_adapters.go` (`ttyPrompter`:
`term.IsTerminal` on fds 0 and 1, `term.ReadPassword`, Redactor
registration before return; `stdinSecret`: one line ≤ 4 KiB, a newline
inside → error, read in a goroutine with a 5 s `time.Timer` and a
context, the goroutine left blocked on a never-closing pipe is
acceptable because the process exits), `go.mod` (`golang.org/x/term`
require).
- Select/Confirm/Text/Secret; the 3-retry rule; non-TTY without `--yes` →
  exit 2 before Detect; `--pg-password-stdin` read before Detect and
  before the TTY check (AC-31).

Satisfies AC-10, AC-11, AC-31 (stdin half).

**WI-S2-3 [S2a] Platform (systemd detect), prereqs, binary, root guard.**
files: `cmd/claude-memory/platform.go` (`detectPlatform(ctx, fs,
runner, goos, goarch, uid)`: systemd probe + bus retry with
`/run/user/<uid>`, package managers, `--jobs-backend` override),
`platform_test.go`, `paths.go` (`EphemeralDirs`: `os.TempDir()`,
`$GOTMPDIR` when set (v0.4, L4: `go run` builds there), `$GOCACHE` else
`os.UserCacheDir()/go-build`, computed in `main`; `BinDirExplicit` in
the install inputs),
`internal/setup/paths.go` (`EphemeralDirs []string`),
`steps_platform.go`, `steps_prereqs.go`, `steps_binary.go`, `hints.go`,
tests; `cmd/claude-memory/install.go` (root guard: `os.Geteuid()==0`
without `--allow-root` → exit 2; euid injected for the test).
- Prereqs: `LookPath` for git, claude, psql, az, ollama, with the hint
  table. A blocked prereq gets the Configure-phase blocked-step prompt
  (re-check / skip / quit, Design 15.3; v0.4, M1), not `Await`. Its
  Detection carries the hint as `Remedy` and an empty `BlockedBy`.
- Binary: refuses when `Paths.Self` is under an `EphemeralDirs` entry
  (exit 2); same-file check; atomic copy at 0755; `BinPath` set in
  **Seed** via `ResolveBinPath` (v0.4, H3: a plain re-run after `install
  --bin-dir X` targets X, not `~/.local/bin`; an explicit new `--bin-dir
  Y` installs to Y, records Y, and leaves the old recorded file in place
  with an info note naming it); notes for not-on-PATH (v0.5, N5:
  `filepath.Dir(RunState.BinPath)`, not `Paths.BinDir`) and darwin
  quarantine (`xattr -p`, read-only). The retained `dir` artifact for
  the binary's directory uses the same value (Design 23).
- Detector table test incl. the no-bus-then-retry row (AC-16), Windows →
  exit 2 (AC-17).
- v0.4 tests: `install --bin-dir X` then `install --yes` → second run
  is a no-op (AC-50) and writes nothing under `~/.local/bin`; `Self` under
  a `GOTMPDIR` dir → exit 2.

Satisfies AC-15 (S2 part), AC-16 (detection), AC-17, AC-18 (hints),
AC-35, AC-69 (install).

**WI-S2-5 [S2a] Env-file editor + `envfile` step (single writer).**
Moved ahead of WI-S2-4b; v0.4 (L9): after WI-S2-4a, because Plan
calls `DB.DSN()` from `dsn.go`. files: `internal/config/envdoc.go` (new),
`envdoc_test.go`, `config.go` (BOM strip in `parseEnv`);
`internal/setup/envfile.go`, `steps_envfile.go`, tests,
`testdata/envfile/*`.
- `config.EnvDoc` is a pure, exported, line-level model:
  `ParseEnvDoc([]byte) *EnvDoc` keeps every line (comments, blanks,
  unknown keys, duplicates) with its parse kind.
  - `Get(key)`.
  - `Set(key, value)` replaces the first assignment in place, or appends.
  - `Normalize(key)` drops `export ` and whole-value quotes.
  - `Findings()` reuses the AC-27 classifier.
  - `Bytes()` keeps the newline style and adds a trailing newline.
  - `ParseEnvFile`/`LoadFromFile` behaviour is unchanged, except that a
    leading UTF-8 BOM is stripped (slice-1 review LOW).
- `envfile` step:
  - Desired keys come from `RunState` (Design 16): `DB.DSN()`,
    `Ollama.URL`/`Model`/`EmbedMaxTokens`, and `PRRepos`. Detect and
    Plan both read them (Seed has run before any Detect). **A field that
    is not `Set` yields no artifact and no write for its key** (v0.4,
    H1): the line, if any, stays byte-identical. A field `Set` with
    `Source=default` or `generated` (v0.5, N3) is written like any
    other.
  - Envfile is a rule-A reader of `DB`, `Ollama` and `PRRepos`, so a
    Configure change re-detects it (v0.4, H2).
  - Artifacts are the format (export/quote consent, interactive only),
    each managed key (`env-key`, retained) and the mode (AC-29 `chmod`
    only).
  - An `unparseable-value` line is kept and reported.
  - A recoverable unencoded password is re-encoded with `pctEncode`.
  - Writes are 0600 in a 0700 directory.
- Goldens: fresh, update in place, unknown kept, export converted, quoted
  unquoted, unparseable kept, base64 `/` re-encoded, `$` password encoded
  (no `unparseable-value` on re-parse), BOM, CRLF, idempotent; v0.4:
  unset `PRRepos` with a hand-added `MEMORY_PR_INGEST_REPOS` → line kept,
  no write.

Satisfies AC-27 (BOM), AC-28, AC-29, AC-64 (goldens).

**WI-S2-4a [S2a] Topology + DSN builder.** files:
`internal/setup/steps_topology.go`, `dsn.go` (`pctEncode` from WI-S2-0;
adds `BuildDSN`, `DBTarget.DSN()`, `ParseDBTarget`, validation of host,
port, db name and sslmode on every source, db name encoded — v0.4,
L12), `dsn_test.go`, `steps_topology_test.go`, `warn.go`
(`warnDataStays`, the AC-26 text, shared with WI-S2-4b);
`internal/postgres/probe.go` (`LocalServerEvidence`: TCP
127.0.0.1:5432 + socket path or `postgres` process via the read-only
Runner).
- Topology A/B with the AC-19 evidence default. The choice is recorded in
  the manifest, and a re-run is `ok` unless `--reconfigure`.
  `--topology docker-*` → exit 2 "deferred".
- DSN builder per Design 24, with a table test (special characters, the
  sentinel, `pa$word`, IPv6, sslmode).
- `--pg-dsn` containing a password → exit 2 (AC-31).
- Seed: `Topology` from the manifest (Design 16).
- AC-26 (topology half): on a topology change, `warnDataStays` is shown
  in Configure before the new value reaches `RunState`. The DSN half is
  WI-S2-4b (v0.4, L6).
- DSN table adds a database name with `%`, `/`, `?`, space (encoded)
  and invalid port/sslmode rows from flag, `Env` and env file.

Satisfies AC-19, AC-20 (builder), AC-26 (topology half), AC-31 (flag
half).

**WI-S2-4b [S2a] `bootstrap.sql` + `database` step.** files:
`deploy/initdb/app-role.psql` (new), `deploy/initdb/01-app-role.sh`
(wrapper), `deploy/embed.go` (constant `InitDBAppRolePSQL`),
`deploy/embed_test.go`; `internal/setup/bootstrap.go`,
`steps_database.go`, tests, `testdata/bootstrap/*`;
`internal/postgres/bootstrap_integration_test.go`.
- `app-role.psql` per Design 5. The `.sh` wrapper runs it with `psql -v`.
- Render: three `\set` lines followed by the body verbatim. Names must
  match `^[a-z_][a-z0-9_]{0,62}$`. **The password must match
  `^[A-Za-z0-9_.~-]{8,256}$`** (B-3), and generated passwords always do.
  Any other password is never rendered. The create path refuses with
  "this password cannot be written into bootstrap.sql safely: let the
  installer generate one, or run deploy/initdb/app-role.psql yourself
  with `psql -v app_pw=…`", and the step is `blocked`.
- Seed: `DB` from `--pg-dsn` + stdin password, else the env-file DSN,
  else `Env` (v0.5, N10; Design 16), via `ParseDBTarget`. A shell DSN
  that differs from the env file's is a Seed note only.
- Configure: existing/create (AC-21), the AC-20 prompts with a 3× auth
  re-ask (probe, 5 s), the classifier messages, and the AC-26 DSN-change
  warning via `warnDataStays` (v0.4, L6).
- Apply (create): write `bootstrap.sql` 0600 in 0700 `<StateDir>`. This
  `Requires: envfile`, so the env file with the password exists first.
  Then `Await{Instructions: <OS-specific command>, Artifacts:
  {"database/database"}}` (v0.5, N1). Once the re-check sees the
  database `ok`, the engine applies `bootstrap-file` (`outdated` →
  delete) per Design 19. The "auth failed after bootstrap" detail follows
  AC-21.
- `--yes` per Design 19, including the **pending-bootstrap rule** (v0.4,
  M2): `bootstrap.sql` present + `auth`/`nodb`/no `vector` → `blocked:
  awaiting user action` (command reprinted, exit 1, nothing
  regenerated); DB `ok` + file present → artifact `bootstrap-file`
  `outdated` → removed.
- Tests:
  - Unit: golden `bootstrap.sql` (sentinel values); a refusal for `'`,
    `\`, `` ` ``, `$`, space and newline in the password; a test that
    `01-app-role.sh` references `app-role.psql`; the call-log guard (no
    `psql`/`sudo` argv).
  - Integration (tag `integration`): with testcontainers, copy the
    rendered file into the pgvector container and run `psql -v
    ON_ERROR_STOP=1 -U postgres -f` there via `Exec`, twice. Otherwise,
    with `MEMORY_TEST_PG_ADMIN_DSN` and `psql` on the host, run it there.
    Skip when neither is available. Afterwards `postgres.New` succeeds as
    the app role (B-6).
  - v0.4 unit: two consecutive `--yes --topology local` runs with no DSN
    and a fake DB answering `auth` → both end `blocked: awaiting user
    action`, exit 1, same password in env file and `bootstrap.sql`; then
    the fake answers `ok` → third run removes `bootstrap.sql`, exit 0.
  - v0.5 unit: (N1) interactive create path, the fake DB turns `ok`
    before re-check → re-check passes, `bootstrap.sql` deleted in the
    same run, step `ok`, manifest has no `bootstrap-file` entry; no
    file → `bootstrap-file` `ok`; pending → `ok`. (N10) env file DSN X,
    `Env` DSN Y, `--yes` with the DB `ok` → 0 writes, one drift note;
    no env-file DSN, `Env` DSN Y → the key is written with Y.

Satisfies AC-20 (prompts, re-ask, classifier messages), AC-21, AC-22,
AC-26 (DSN half), AC-64 (goldens), AC-65 (bootstrap part).

**WI-S2-6 [S2a] Migrate step.** files: `internal/setup/steps_migrate.go`,
test. Detect = `pg.schema` introspection with `RunState.DB.DSN()`;
Apply = `DBProber.Migrate`. `Requires: database, envfile`. The engine
test asserts it precedes `hooks.settings` (with a fake hooks step in
2a) and uses the step-state DSN, not `Env`.

Satisfies AC-25.

**WI-S2-7 [S2a] Ollama step.** files: `internal/setup/steps_ollama.go`,
test. Seed: `Ollama` from flags, env file, `Env` (v0.5, N10 order),
else the AC-32 defaults with `Source=default` (v0.5, N3), so Detect can
probe on a fresh `--yes` run and `envfile` writes both keys
(`EmbedMaxTokens` only from the env file/`Env`, no default). Test: a
fresh `--yes` run with no Ollama input probes `http://127.0.0.1:11434`
for `bge-m3` and the env file gains both keys; the second run is a
no-op. Configure: URL resolution, the AC-33 warning
and confirm, and the pull y/N. Apply: `WritePorts.Ollama.Pull` with its
`progress` callback forwarded to `WritePorts.Progress` (v0.4, L1: live,
not via `Notes`), then the 1024-dim verify. Test: the fake Pull's
callbacks reach a fake sink in order.

Satisfies AC-32 (step), AC-33.

**WI-S2-8 [S2a] Namespaces step.** files: `internal/namespace/file.go`
(export `Marshal`, `(*Config).Add`; `Save` uses `Marshal`),
`namespace_test.go`; `internal/setup/steps_namespaces.go`, `projects.go`
(existence-checked decoding), tests.
- Writes go through the FS port (Design 20). Absent → create from the
  `--namespace` flags or the prompted mappings. Valid → `ok`, with
  optional mappings added. Unparseable → `modified`, never written.
- Gate: WI-S1-0 hyphen-encoding check (owner Mac). The decoder is
  existence-checked either way, so a wrong guess only hides a
  suggestion.

Satisfies AC-47, AC-64 (goldens).

**WI-S2-14a [S2a] `install` command, builders, `--dry-run`, `--upgrade`.**
files: `cmd/claude-memory/install.go`, `install_test.go`, `main.go`
(early dispatch of `install`; `buildSetupValues` + `readOnlyPorts` +
`writablePorts`, Design 20), `internal/setup/install.go` (step registry
for 2a).
- The AC-4 install flag set. Deferred flag values → exit 2. Unknown
  flags → exit 2. `--upgrade` ≡ `--yes` (same plan asserted). `HOME`
  unset/relative → exit 2.
- `--dry-run` builds the engine on `readOnlyPorts` for every phase.
- 2a composition (v0.4, M6): the composition root sets
  `WritePorts.ClaudeCLI`, `WritePorts.Jobs` and `ReadPorts.Jobs` to
  **nil** in 2a. `LaunchdJobs` has only `Detect`/`Inspect` until
  WI-S2-13a and does not satisfy `JobDetector`/`JobManager`; doctor
  keeps using it directly (`checks_misc.go:139`). `claudeCLI` arrives in
  WI-S2-10. A registry test asserts that the 2a registry has no `mcp`
  or `jobs` step, so no 2a code path dereferences a nil port. 2b fills
  the three ports in the same PR that registers their steps.
- Tests:
  - `cmd` child process with `HOME`=temp and no DSN runs `install
    --dry-run --yes --topology remote` without `MEMORY_PG_DSN is
    required` (AC-1).
  - Binary-outside-checkout run (AC-34 S2).
  - Dry-run golden plus 0 writes and 0 mutating commands (AC-13).

Satisfies AC-1 (install), AC-4 (install), AC-13, AC-34 (S2), AC-52.

**WI-S2-14b [S2a] Final doctor + cross-cutting install tests.** files:
`internal/setup/install.go` (final doctor step), `install_test.go`.
- In-process doctor with the read-only ports (AC-62).
- Exit-code rule (spec AC-62 v0.4): a `fail` from a check whose owning
  step the **user** skipped (`--skip`, an interactive `skip` choice or
  blocked-step `skip`), is blocked by such a skipped prerequisite, or is
  not registered
  in this build (2a has no Claude steps) is printed as `fail (not
  installed: <step> skipped)` and does not set exit 1. **Kept is not
  exempt** (v0.4): a `fail` from a check whose step's artifacts were
  kept — e.g. a kept `modified` env file whose DSN fails `pg.connect`,
  or a kept database — counts and exits 1. The check → step map lives
  next to the registry. A step left blocked under `--yes` (default
  `skip`, not the user's choice) is not exempt either. Test: kept
  `modified` envfile + `pg.connect` fail → exit 1; `--skip mcp` →
  `mcp.registered` fail printed, exit 0.
- Restart line per WI-S1-0 (gate; default: print it).
- Cross-cutting suites, extended by each 2b WI:
  - AC-30 sentinel over a full `--yes --topology remote` run with fakes.
  - AC-50 no-op re-run: 0 writes, 0 mutating commands, manifest
    untouched.
  - AC-12/AC-52: `--yes` never overwrites `modified`; `--upgrade` and
    `--yes` produce identical plans.

Satisfies AC-12, AC-30 (sentinel), AC-50, AC-52, AC-62, AC-64 (v0.5,
N7: the run-twice idempotence row for the no-file steps platform,
prereqs, binary, topology, migrate, ollama, mcp).

#### PR 2b — Claude integration, jobs, uninstall

**WI-S2-9 [S2b] Hook steps.** files: `internal/setup/steps_hooks.go`,
`settings.go` (`MergeSettings(b, desired, recorded, overwrite
map[string]bool)`), `settings_test.go`, tests.
- `hooks.scripts`: two file artifacts rendered with `RenderHookScript(…,
  RunState.BinPath)`, mode 0755, hashes recorded.
- `hooks.settings`: one artifact per event (Design 18), then `Merge` only
  if `changed`, one timestamped backup, re-read + hash compare, and an
  atomic write keeping the mode (following an in-`Home` symlink). The
  canonical entries are recorded, plus `CreatedFile` when the file was
  absent and `CreatedContainer` when the `hooks` object was (Design 23).
  AC-70 duplicates become `Notes`.
- Hook scripts vs AC-51 (v0.4): "byte-identical to the embedded
  version" means the **rendered** script (`RenderHookScript(asset,
  BinPath)`) → `ok` and recorded; the **raw** embedded script → `outdated`
  (our content, replaced without an overwrite Confirm); anything else →
  `modified`. Spec AC-51 now says so (it contradicted AC-36 v0.3).
- `dir` artifacts: `<ClaudeDir>/hooks/claude-memory` recorded as an
  owned `dir` only when this step created it (Design 23).
- Gate: WI-S1-0 checks for `$HOME` expansion and unknown keys.

Satisfies AC-36, AC-37 (step), AC-38 (step), AC-39, AC-51 (hooks),
AC-64 (goldens), AC-70 (install).

**WI-S2-10 [S2b] MCP step.** files: `internal/setup/steps_mcp.go`,
`cmd/claude-memory/setup_adapters.go` (`claudeCLI`). Detect =
`ReadMCPRegistration` against `RunState.BinPath`; absent → add; outdated
→ remove + add; `ok` → no `claude` call; no `claude` on PATH → `blocked`
with the printed command.

Satisfies AC-40 (register), AC-51 (MCP), AC-67 (Detect half).

**WI-S2-11 [S2b] Skills step.** files: `internal/setup/steps_skills.go`,
tests. One artifact per file. The diff is shown on request
(`show diff / overwrite (backup .bak) / keep`), and the hand-install
message is a `Note`. Each `<ClaudeDir>/skills/<name>` it creates is an
owned `dir` artifact (Design 23).

Satisfies AC-41 (step), AC-51 (files), AC-64 (goldens).

**WI-S2-12 [S2b] CLAUDE.md step.** files: `internal/setup/steps_claudemd.go`,
tests. Uses `mdblock` (fence-aware since `cd28e43`) + `InGitRepo`; the
target is chosen in Configure; `--yes` rules per AC-42.

Satisfies AC-42 (step), AC-64 (goldens).

**WI-S2-13a [S2b] launchd install half + templates.** files:
`integration/launchd/job.plist.tmpl` (new; the two `__HOME__` plists are
deleted), `integration/embed.go`, `integration/embed_test.go`,
`internal/setup/jobs_render.go`, `jobs_launchd.go`
(Render/Install/Remove; `Inspect` rewritten), `ports.go` (`JobSpec`:
`Label` for launchd, `Unit` for systemd, `Name` shared; drop "or systemd
unit base name" from the `Label` comment, `ports.go:177` — v0.4, L7),
`checks_misc.go` (doctor passes the manifest hashes), golden plists,
tests.
- **Recorded hash route (v0.4, M5):** `JobDetector.Detect(ctx, j
  JobSpec, recorded map[string]string)` — unit/plist path → sha256 from
  the manifest's `launchd`/`systemd` artifacts; nil when there is no
  manifest. The `jobs` step and doctor (`checks_misc.go:139`) build the
  map from `Prior`/the loaded manifest with one helper
  (`recordedJobHashes(m)`), so both compare against the same record.
- `Inspect` (#6) compares the file's hash with the manifest-recorded
  hash and the **rendered** plist:
  - equal to the rendering → `ok` (if loaded);
  - recorded and unedited but differing from the rendering (moved
    `--bin-dir`, a schedule or PATH change) → `outdated`, so `--yes`
    re-renders and reloads it;
  - the legacy wrapper → `outdated`;
  - unrecorded and different, or recorded and edited → `modified`;
  - present but not loaded → `outdated`.
- Install: write the plist, `bootout` (exit status tolerated per WI-S1-0;
  default: treat any non-zero exit whose stderr says "not loaded" / "No
  such process" or exit 3 / 113 as tolerated), then `bootstrap`.
- Gate: WI-S1-0 `bootout` status (owner Mac).

Satisfies AC-44 (install), AC-51 (jobs), AC-64 (goldens).

**WI-S2-13b [S2b] systemd + `jobs` step.** files:
`internal/setup/jobs_systemd.go`, `integration/systemd/*.tmpl`,
`steps_jobs.go`, golden units, tests.
- systemd: write the units, `daemon-reload`, `enable --now`, with
  `SystemdEnv` on every call; linger probe + hint `Note`; detection via
  one `systemctl --user show` per job + the recorded/rendered compare,
  as in 13a.
- Step: two job artifacts. Configure sets `PRRepos` and `JobPATH`
  (stable shims preferred). `MEMORY_PR_INGEST_REPOS` reaches the env
  file only through `envfile` (Design 16). Azure-only note; `--no-jobs`.
  Backend `none` → `blocked` + instructions (the Configure-phase
  blocked-step prompt, Design 15.3). The backend is recorded.
- Seed: `PRRepos` from flag, `Env`, env file; `JobPATH` from `LookPath`
  (Design 16). This WI also wires `ReadPorts.Jobs`/`WritePorts.Jobs`
  in `writablePorts()`/`readOnlyPorts()` (nil in 2a, M6).

Satisfies AC-16 (wiring), AC-43, AC-45, AC-64 (goldens).

**WI-S2-17 [S2b] Doctor slice-2 deltas.** files:
`internal/setup/checks_misc.go`, `checks_claude.go`, `doctor_test.go`,
goldens.
- `jobs` uses the full JobManager set on linux, plus a warning when the
  detected backend differs from the manifest's.
- `tools.claude` also checks the recorded job PATH.
- Remedies name `claude-memory install` where slice 1 named a manual
  step.
- `jobs` compares against the rendered unit (13a/13b).

Satisfies AC-16 (doctor warn), AC-58 (S2 deltas).

**WI-S2-15 [S2b] Uninstall.** files: `internal/setup/uninstall.go`,
`cmd/claude-memory/uninstall.go`, `main.go` (early dispatch), tests.
- Flags `--yes`, `--dry-run`, `--allow-root` (AC-4); root guard (AC-69);
  non-TTY rule (AC-11); lock (writable only).
- Reverses recorded artifacts in reverse order, by kind (AC-54).
  `modified` artifacts are kept and listed. The binary is removed from
  its **recorded** path only when the hash matches. Retained kinds
  (`env-key`, retained `dir`) are dropped from the manifest without
  reversal; owned `dir` artifacts (`<ClaudeDir>/hooks/claude-memory`,
  `<ClaudeDir>/skills/<name>`) are removed after their files, only when
  empty (Design 23, v0.4 M3). `settings.json` that install created
  (`CreatedFile`) is removed only when unmerge leaves `{}` (Design 23).
  No manifest → list only, 0 writes (AC-56).
- E2E on a temp `Paths`: install → uninstall → the tree equals the
  pre-install tree, excluding `<ConfigDir>/env`, `namespaces.yaml`,
  `install.lock`, `<StateDir>`, and `*.bak.claude-memory.*` / `*.bak`
  backups, **and (v0.4) the retained directories `<ConfigDir>`,
  `<StateDir>`, `<BinDir>` plus the unrecorded parents `MkdirAll`
  created (`~/.config`, `~/.local`, `~/.local/state`, `<ClaudeDir>`,
  `<ClaudeDir>/hooks`, `<ClaudeDir>/skills`) when they are empty**;
  `<ClaudeDir>/hooks/claude-memory` and `<ClaudeDir>/skills/<name>`
  must be gone; manifest absent. A partial uninstall (one skill edited) →
  trimmed manifest holding only that skill. Every 2b golden case also
  runs through uninstall (Design 9).

Satisfies AC-1 (uninstall), AC-4 (uninstall), AC-54, AC-56, AC-69
(uninstall).

**WI-S2-16 [S2b] Integration e2e, CI, docs.** files:
`internal/setup/install_integration_test.go`, `.github/workflows/ci.yml`,
`Makefile` (`test-integration` adds `./internal/setup/...`),
`integration/INSTALL.md` (manual plist reference → `install --dry-run`
output), `DEPLOY.md` (upgrade → `install --upgrade`; server half stays
manual), `integration/bin/run-with-env.sh` (legacy header),
`integration/mcp-registration.md`, `integration/ollama.md`,
`docs/specs/README.md`.
- `install --yes --topology local` against an existing test database:
  testcontainers by default, `MEMORY_TEST_PG_ADMIN_DSN` when set (creates
  the role and DB through the admin connection first). Uses an `httptest`
  Ollama, fake claude/jobs adapters and a temp `Paths`. The second run is
  a no-op.
- AC-51 fixture: a HOME replicating INSTALL.md steps 2–8 → no duplicate
  hook entry or job after `--yes`.

Satisfies AC-51, AC-64 (S2 aggregate), AC-65 (S2 e2e), AC-66 (S2).

## Test Strategy
- **Unit, the default for everything.** `internal/setup` runs against
  FakeRunner (matcher-scripted, strict, call-logged), FakePrompter
  (transcripts, S2), FakeClock, and a real temp dir behind the FS port
  with fault injection, all addressed through explicit `Paths` values
  (no `t.Setenv`; tests are parallel). No real `brew`/`apt`/`launchctl`/
  `systemctl`/`docker`/`claude`/`psql`/`ollama` call is possible: the
  FakeRunner fails on unmatched commands and the guard bans `os/exec` in
  the package.
- **Golden files** cover settings.json, CLAUDE.md, env file,
  `bootstrap.sql`, plists, systemd units, dry-run output and doctor JSON.
  `-update` regenerates them. Each merge case runs twice and, from S2,
  through uninstall.
- **Property-style checks**: no-op re-run ⇒ 0 writes / 0 mutating
  commands (AC-50); `--dry-run` and doctor ⇒ 0 writes (AC-13, AC-57);
  doctor/Detect ⇒ the registered MCP command and `claude mcp get|list`
  never run (AC-67); the sentinel password never appears outside the env
  file and `bootstrap.sql` (AC-30).
- **Adapters**: Postgres probe/migrate (S1) and the `bootstrap.sql` run
  (S2, needs `psql`) use the `integration` tag against
  `MEMORY_TEST_PG_ADMIN_DSN` or testcontainers. The Ollama prober is
  tested against `httptest`. The exec runner is tested against
  `/bin/echo` and `false` only; the TTY prompter is a thin `x/term`
  wrapper covered by the manual run.
- **cmd layer**: flag parsing, early dispatch (no DSN needed), `Paths`
  builder, exit codes, AC-67 marker test (S1), root guard and
  binary-outside-checkout (S2).
- **Manual (recorded in this plan)**:
  - S1: WI-S1-0 fixtures; doctor on the current hand install (WI-S1-12).
  - S2: a full interactive install on the Mac (topology B against the
    real server) and a re-run that must show all ok with 0 writes;
    `--upgrade` after a rebuild; uninstall; an Ubuntu VM or container
    with a systemd user instance (topology A, both the existing-DB and
    the `bootstrap.sql` path), including one run over `ssh` without
    `XDG_RUNTIME_DIR`.
  - S2a (before 2a merges): `install --dry-run` on the Mac against the
    live hand install (expect no Claude-file rows, env file `ok` or
    `modified` for the export form), then a real 2a `install` for
    topology B (binary, env file, migrate, ollama, namespaces only), with
    the final doctor reporting the Claude checks as `fail (not installed:
    …)` and exit 0 (spec AC-62 v0.3).
  - Gates from WI-S1-0 / WI-S1-12 (owner Mac; review #10): the hook
    `$HOME` expansion and unknown keys gate WI-S2-9. The `bootout` "not
    loaded" exit status gates WI-S2-13a. The settings reload gates the
    WI-S2-14b restart line. The `~/.claude/projects` hyphen encoding gates
    WI-S2-8 suggestions. None of them blocks starting PR 2a. Each gated
    item has a stated default to use until the run is done.

## Risks

| Risk | Mitigation |
|---|---|
| Corrupting `~/.claude/settings.json` (shared with Claude Code and the user's other hooks) | Raw-slice codec; strict parse or refuse (incl. duplicate keys); model-level no-op; manifest-gated replacement; one backup per write; hash re-check before rename; atomic write; golden suite incl. tabs/escapes/big numbers. |
| Doctor or Detect starting the MCP server (and migrating) | No `claude mcp get|list` anywhere; `.claude.json` file read; AC-67 unit + cmd tests; deny-list guard over every call log. |
| Clobbering deliberate user edits under `--yes`/`--upgrade` | `modified` is never overwritten without an interactive Confirm; drift reported as warn. |
| Claude Code config layout changes (`.claude.json` shape, `CLAUDE_CONFIG_DIR`) | WI-S1-0 fixtures; tolerant reader (missing key → `absent`, unparseable → warn); writes only via the CLI. |
| Password leakage via argv, logs, diffs, errors or the bootstrap file | No password flags; redactor registered before reading; ≥ 8-char literal rule + userinfo pattern; sentinel test; `bootstrap.sql` 0600 in 0700, deleted on success, flagged by doctor if left. |
| Bootstrap SQL drifts from `initdb` | One shared `app-role.psql`; the `.sh` is a wrapper (test asserts the reference). |
| Duplicate hooks or jobs on machines installed by hand | Identity rules (Design 3, existing launchd labels); AC-51 fixture; AC-70 scan of other settings files. |
| launchd semantics differ across macOS versions | `bootout`/`bootstrap` on macOS 11+; "not loaded" status per WI-S1-0; doctor verifies. |
| systemd misdetected over ssh / no user instance | Bus-address retry (AC-16); `--jobs-backend` override; `none` prints instructions; doctor warns on backend change. |
| systemd user timers don't run when logged out | Linger hint; doctor `jobs` warns when Linger=no. |
| Scope creep | Two slices, each shippable; docker, cron, purge, latency probe, `--only`, seed deferred with reasons (spec §12.1). |
| Hook regressions | The hook code path is untouched; only the wrapper template's default bin path is rendered, and a golden test diffs it against today's script. |
| Engine state/ordering bugs (v0.3: Configure phase, two re-Detect rules, `Await`; v0.4: Seed pass, rule A by reader, blocked-step prompt) | Typed `RunState` with one owner per field, `Set`/`Source` flags (Design 16); fake-step engine tests for Seed on a no-op run, rule A by reader, both re-Detect rules, the blocked-step prompt, `Await` under TTY and `--yes`, and the B-1 regression (WI-S2-1c). |
| `bootstrap.sql` script injection via a user-typed password (B-3) | Only `[A-Za-z0-9_.~-]` passwords are rendered; refusal tests for quote, backslash, backtick, `$`, space, newline (WI-S2-4b). |
| The installer's own env file flagged by doctor (B-2) | Full percent-encoding (Design 24); envfile golden re-parses the written file with zero findings. |
| A user-file write shipping without its rollback | PR split: every Claude-file writer lands in 2b together with `uninstall` (Delivery). |

## Stated risks for the implementer

v0.5. Iteration 3 (READY WITH RISKS) findings N4–N10 are all fixed in
the text, so none is carried as an open risk. Residual risks in the
v0.5 resolutions themselves:

| Risk | Where | What to watch |
|---|---|---|
| The `Await` follow-up pass (re-Plan/Apply of the remaining artifacts) is a third Apply path beside the normal Apply and rule B | Design 19; WI-S2-1c | It must go through the same success rule, manifest write and Ctrl-C handling as a normal Apply. A second `Await` in it is a failure, not a new prompt. |
| `Source=default` keys are now written, so the first `install` over a hand install whose env file relies on the binary's Ollama defaults adds `MEMORY_OLLAMA_URL`/`MEMORY_OLLAMA_MODEL` lines | Design 16; WI-S2-7 | These show as `absent → apply` in the plan diff. When the AC-51 hand-install fixture's env file lacks them, it must expect them on run 1 and a no-op on run 2. |
| Seed order flag → env file → `Env` differs from the binary's own precedence (shell over file, `internal/config/config.go:251`) | Design 16 | Intentional: the file is what hooks, MCP and jobs read. The drift note is the only signal; doctor's shell-only FAIL (`checks_env.go:114-118`) is unchanged. |
| Narrowing slice-1 helpers to `ReadFS` touches merged, reviewed code | WI-S2-1a | Signature-only change; the slice-1 tests must pass unchanged. |

## Rollout
1. **Slice 1 merged → owner runs `claude-memory doctor`** on the current
   hand install. Nothing about the running setup changes. Expected warns:
   `run-with-env.sh` plists, legacy `$HOME` hook entries. Fix the env file
   by hand if `env.format` flags it.
2. **PR 2a merged → owner runs `claude-memory install --dry-run`, then
   `install`** (topology B). Only the binary, env file (export form
   converted with consent), schema and `namespaces.yaml` change; Claude
   Code files and jobs are untouched.
   **PR 2b merged → owner runs `claude-memory install`** on the laptop
   (topology B). Expect: hook entries in the legacy form reported as
   `modified` → the owner confirms the overwrite once (interactive);
   plists replaced (`outdated`); skills differing from the embedded
   version offered for overwrite; zero duplicates. Then a re-run, which
   must show all ok with 0 writes, and `install --upgrade` after the next
   rebuild.
3. Slice 3 / follow-ups only on demand (spec §12.1).
4. Rollback at any stage: `claude-memory uninstall` (keeps config and
   data), then the manual INSTALL.md reference steps, or the previous
   binary. The settings backup gives a byte-level restore.

## Verification
- `go vet ./... && go test -race ./...` green; integration job green with
  the added packages.
- The manual checklist from the Test Strategy, outputs pasted into
  `04-implementation-report.md` per slice.
- Spec §14 checkboxes ticked per slice.
- Slice 2: the plan re-review (`plan-reviewer`, both models) on v0.4
  passes before WI-S2-0 starts. Each of 2a and 2b gets its own
  implementation report and review. The WI-S2-0 pre-work PR is merged
  before 2a's first commit.

## Work item map (v0.2 → v0.3, slice 2)

| v0.2 | v0.3 | PR | Note |
|---|---|---|---|
| — | WI-S2-0 | pre | new: guard, CRLF, `dsnPasswords`, `RenderHookScript`, goldens (review #4, #8, #9) |
| WI-S2-1 | WI-S2-1a (state, ports split, writable FS, lock) + WI-S2-1c (engine, manifest; v0.4 split) + WI-S2-1b (renderer, `diff.go`, preflight) | 2a | `diff.go` moved from S2-11; AC-13/14/49/§8 owners |
| WI-S2-2 | WI-S2-2 | 2a | + stdin secret adapter (5 s goroutine+timer) |
| WI-S2-3 | WI-S2-3 | 2a | + `EphemeralDirs`, uid into `detectPlatform`, prereqs file split |
| WI-S2-5 | WI-S2-5 | 2a | moved **before** S2-4b; + `config.EnvDoc`; single env writer |
| WI-S2-4 | WI-S2-4a (topology + DSN) + WI-S2-4b (`bootstrap.sql` + database) | 2a | full percent-encoding; password alphabet; testcontainers `Exec` |
| WI-S2-6, 7 | WI-S2-6, 7 | 2a | Configure-phase prompts |
| WI-S2-8 | WI-S2-8 | 2a | FS-port writes; `namespace.Marshal`; no `nsStore` |
| WI-S2-14 | WI-S2-14a (cmd, builders, dry-run, upgrade) + WI-S2-14b (final doctor, cross-cutting tests) | 2a | AC-62 exit-code rule |
| WI-S2-9 | WI-S2-9 | 2b | per-event overwrite set; `created` flag |
| WI-S2-10, 11, 12 | WI-S2-10, 11, 12 | 2b | `diff.go` no longer in S2-11 |
| WI-S2-13 | WI-S2-13a (launchd + templates) + WI-S2-13b (systemd + jobs step) | 2b | Detect vs recorded/rendered; single plist source; `JobSpec.Unit` |
| — | WI-S2-17 | 2b | new: doctor S2 deltas (AC-58, AC-16 warn) |
| WI-S2-15 | WI-S2-15 | 2b | + uninstall dispatch/flags/root guard; retained kinds |
| WI-S2-16 | WI-S2-16 | 2b | + Makefile, AC-51 fixture, testcontainers default |

## Work item map (v0.1 → v0.2)

| v0.1 | v0.2 | Note |
|---|---|---|
| WI-0 | WI-S1-0 | three of five questions answered by the review; `.claude.json` fixtures added |
| WI-1 | WI-S1-1 | + `Paths` builder; `migrate` after config load |
| WI-2 | WI-S1-2 | + `quoted-whole-value`, `unparseable-value` |
| WI-3 | WI-S1-3 (embed, wording) + WI-S2-13 (launchd/systemd templates) | C1 templates → S3 |
| WI-4 | WI-S1-4 (probe, migrate) + WI-S2-4 (shared SQL) | `Bootstrap(adminDSN)` **withdrawn** (deferred) |
| WI-5 | WI-S1-5 | — |
| WI-6 | WI-S1-6 | + `Paths`/`Env`, matcher FakeRunner, wider deny list, os-call grep |
| WI-7 | WI-S1-7..WI-S1-11 | split: settings library, md library, `.claude.json` reader, manifest + launchd detect, checks; latency check **withdrawn** (deferred) |
| WI-8 | WI-S1-12 | + AC-67 cmd test, URL-safe password docs |
| WI-9 | WI-S2-1 | no `Verify`; conditional manifest write; `--only` **withdrawn** |
| WI-10 | WI-S2-2 | — |
| WI-11 | WI-S2-3 | + root guard, `go run` refusal, systemd bus retry |
| WI-12 | WI-S2-4 | admin-DSN bootstrap **withdrawn**; `bootstrap.sql` + printed command; sslmode |
| WI-13 | WI-S2-5 | — |
| WI-14 | WI-S2-6 | step-state DSN; Detect by introspection |
| WI-15 | WI-S2-7 | warning names the 800 ms hook timeout |
| WI-16 | WI-S2-8 | seed **withdrawn** (deferred); existence-checked decoding |
| WI-17 | WI-S1-7 | moved to slice 1 |
| WI-18 | WI-S2-9 | one backup per write (rotation deferred) |
| WI-19 | WI-S1-9 (detect) + WI-S2-10 (register) | `claude mcp get` parser **withdrawn** (BLOCKER) |
| WI-20 | WI-S2-11 | — |
| WI-21 | WI-S1-8 (library) + WI-S2-12 (step) + WI-S2-14 (install cmd) | — |
| WI-22 | WI-S1-10 (detect) + WI-S2-13 (install) | — |
| WI-23 | WI-S2-13 | + bus retry, `none` backend |
| WI-24 | — | **withdrawn** (cron deferred, spec AC-46) |
| WI-25 | WI-S2-13 | — |
| WI-26 | WI-S2-15 (uninstall) + WI-S2-14 (`--upgrade` alias) | purge **withdrawn** (deferred) |
| WI-27 | — | **slice 3** (spec AC-23) |
| WI-28 | — | **slice 3** (spec AC-24) |
| WI-29 | WI-S2-16 | e2e uses an existing DB, not docker |
| WI-30 | WI-S1-12 (DEPLOY.md env) + WI-S2-16 (rest) | DEPLOY.md server half stays manual |

## Owner decisions (spec §13) — all resolved

All six recommendations accepted, plus review questions #7–#9 (slices
yes; `--upgrade` ≡ `--yes`; topology C deferred to slice 3). The v0.1
"if the owner decides otherwise" table no longer applies; the release
workflow (§13 #2) is a separate follow-up PR that must reuse the Makefile
`LDFLAGS`.

## Open questions for the owner (v0.3)

These do not block. Each one is written into spec v0.3 / this plan with
the recommended default. If the answer differs, only the named AC and WI
change.

1. **User-typed passwords in `bootstrap.sql` (AC-21, B-3).** Should the
   create path refuse a password outside `[A-Za-z0-9_.~-]`, or should we
   define and test psql meta-command escaping (doubling `'`, escaping `\`,
   neutralising backquotes)? *Recommended: refuse* (generated passwords
   always pass; escaping psql meta-command arguments is easy to get
   subtly wrong, and the payload runs under `sudo -u postgres`).
2. **Final doctor exit code vs skipped steps (AC-62, B-6).** Should fails
   from checks whose step you skipped (or that PR 2a does not install
   yet) be printed but not make `install` exit 1? *(v0.4: "kept" removed
   from the exemption — a kept broken env file or database still exits 1.)* *Recommended:
   yes* (otherwise `--skip mcp` and every 2a install always exit 1).
   Standalone `doctor` is unchanged.
3. **`install --yes --topology local` with no DSN (AC-21).**
   *Recommended:* take the create path with defaults, write the env file
   and `bootstrap.sql`, print the command, exit 1, and finish on the next
   `--yes` run. Alternative: fail at once with "pass --pg-dsn".
4. **Step order `envfile` before `database` (AC-7).** *Recommended:
   accept.* This is what lets one step own the env file while AC-21 still
   writes the password before `bootstrap.sql`. The only visible effect:
   on the create path the env file names a database that does not exist
   yet until you run the printed command.
5. **Does PR 2a ship a real `install`, or only `--dry-run`?**
   *Recommended: real install.* 2a writes only the binary, the env file,
   the schema and `namespaces.yaml`, and touches no Claude Code file and
   no job. All of it is recorded in the manifest, so 2b's `uninstall`
   reverses the binary, and the rest is config that AC-54 keeps anyway.
