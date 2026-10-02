# Claude Memory — `install` / `doctor` Implementation Plan

**Status (2026-10-02):** slice 1 (WI-S1-1..12) implemented and reviewed (`04-slice-1-review.md`, gate FAIL: 1 high + 5 medium open — migrate password-fragment leak, plan/DoD bookkeeping incl. the manual Mac `doctor` run, fenced-marker handling, settings symlink outside HOME, shell-env DSN override severity, hook identity match). WI-S1-0 manual fixtures on a Mac not done. Slice 2 (WI-S2-1..16) and slice 3 not started. Plan v0.2 for spec v0.2. The architecture review
(`03-architecture-review.md`) is applied and the owner's decisions are in
spec §0.1; every spec §13 question is resolved. Needs plan review before
implementation (SDD flow). Work proceeds **slice by slice**: slice 1 is
fully implementable without any slice-2 code.

## Spec
- `docs/specs/install-doctor/01-spec.md` (SPEC-2026-10-01-install-doctor,
  v0.2, AC-1..AC-70). AC-23/AC-24 are slice 3 (deferred); AC-46, AC-48,
  AC-55, AC-61 are deferred follow-ups (spec §12.1).

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
| **S2 — install, upgrade, uninstall** | PR 2 (may split at WI-S2-12: 2a install core, 2b jobs + uninstall + docs) | engine, prompter, platform/prereqs/binary, topology A/B + database (`bootstrap.sql`), envfile, migrate, ollama, namespaces, hooks, mcp, skills, claude-md, launchd + systemd jobs, install command + final doctor, `--upgrade` alias, uninstall, e2e, docs | WI-S2-1..WI-S2-16 | all remaining non-deferred ACs (spec §14 "Slice 2") |
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
  0600. They are reused behind a port.
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
| `cmd/claude-memory/platform.go` (new) | S1, S2 | `detectPlatform(fs, runner, paths) PlatformInfo` (S2: systemd + bus retry, package managers) |
| `cmd/claude-memory/setup_adapters.go` (new) | S1, S2 | `osFS` (read-only S1; writable/dry-run + `Lock` S2), `execRunner`, `systemClock`; S2: `ttyPrompter`, `claudeCLI`, `nsStore` |
| `cmd/claude-memory/doctor.go` (new) | S1 | flags, adapters, `setup.RunDoctor`, renderers, exit codes |
| `cmd/claude-memory/install.go`, `uninstall.go` (new) | S2 | flags, root guard, `--pg-password-stdin`, `setup.Engine` |
| `internal/config/config.go` | S1 | `ParseEnvFile`, `EnvFile` type; `LoadFromFile` on top |
| `internal/postgres/probe.go`, `schema_objects.go` (new) | S1 | `Probe` (read-only, TLS, SQLSTATE classes), `Migrate`; S2: `LocalServerEvidence` |
| `internal/ollama/prober.go` (new) | S1 | version, tags, embed dims, pull (stream) |
| `internal/setup/` (new) | S1 | `paths.go` (types), `ports.go`, `platform.go` (type), `state.go`, `redact.go`, `jsonobj.go`, `settings.go`, `mdblock.go`, `gitrepo.go`, `mcpreg.go`, `manifest.go` (read), `doctor.go`, `checks_*.go`, `report.go`, `fakes_test.go`, `guard_test.go`, `testdata/` |
| `internal/setup/` | S2 | `engine.go`, `prompt.go`, `manifest.go` (write), `lock.go`, `diff.go`, `envfile.go`, `dsn.go`, `bootstrap.go`, `hints.go`, `steps_*.go`, `jobs_launchd.go` (install half), `jobs_systemd.go`, `uninstall.go` |
| `integration/embed.go` (new, `package integration`) | S1 | `//go:embed hooks skills launchd claude-md-section.md settings.snippet.json` (S2 adds `systemd`) |
| `integration/claude-md-section.md` (new) | S1 | section body, location-neutral; `claude-md-snippet.md` points at it |
| `integration/skills/remember/SKILL.md`, `memory-digest/SKILL.md` | S1 | drop `acme/CLAUDE.md` / `acme/` references |
| `integration/launchd/*.plist` | S2 | `text/template` placeholders (binary, PATH, log dir); ProgramArguments → binary directly |
| `integration/systemd/` (new) | S2 | `claude-memory-{cleanup,ingest-pr}.{service,timer}` templates |
| `deploy/embed.go` (new, `package deploy`) | S1 | `//go:embed initdb` (S2 adds `app-role.psql` to it; S3 adds compose files + `.env.example` by name) |
| `deploy/initdb/app-role.psql` (new), `01-app-role.sh` | S2 | shared idempotent SQL; the `.sh` becomes a wrapper |
| `Makefile` | S1 | one `LDFLAGS` var for build/install/cross-build; cross-build adds darwin/amd64, linux/arm64 |
| `.github/workflows/ci.yml` | S1, S2 | integration job adds `./cmd/claude-memory/...` (S1) and `./internal/setup/...` (S2); guard test once |
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
   machinery); a separate installer copy (drift).
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
    choice matrix is 5 × 3.

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
S2:  S2-1 (engine+state+manifest write+lock) ─> S2-2 (prompter + x/term) ─> S2-3 (platform/prereqs/binary/root)
     ─> S2-4 (topology + database A/B + bootstrap.sql) ─> S2-5 (envfile) ─> S2-6 (migrate step) ─> S2-7 (ollama step)
     ─> S2-8 (namespaces) ; S2-9 (hooks) ; S2-10 (mcp) ; S2-11 (skills) ; S2-12 (claude-md)
     ─> S2-13 (launchd + systemd jobs) ─> S2-14 (install cmd + final doctor + --upgrade)
     ─> S2-15 (uninstall) ─> S2-16 (e2e + CI + docs)
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

### Slice 2 — install, upgrade, uninstall

**WI-S2-1 [S2] Engine, states, manifest write, lock.** files:
`internal/setup/engine.go`, `manifest.go` (write, merge, trim),
`lock.go`, `engine_test.go`; `osFS` writable + dry-run modes and `Lock`
(`syscall.Flock`).
- Order + `Requires`; status table; AC-6 defaults; combined plan; one
  confirmation; Apply → re-Detect (Design 12); failure isolation (AC-8);
  manifest written only on change (Design 8); exit 0/1/2/130 (cancel
  stops before the next action); `--skip`.

Satisfies AC-5..AC-9, AC-49 (write), AC-53 (`--skip`).

**WI-S2-2 [S2] Prompter.** files: `internal/setup/prompt.go`,
`prompt_test.go`, `cmd/claude-memory/setup_adapters.go` (`ttyPrompter`:
`term.IsTerminal` on fds 0 and 1, `term.ReadPassword`, Redactor
registration), `go.mod` (`golang.org/x/term`).
- Select/Confirm/Text/Secret; 3-retry rule; re-check re-runs one Detect;
  `NO_COLOR`; `CI` disables cursor progress; non-TTY without `--yes` →
  exit 2 before Detect.

Satisfies AC-10, AC-11, AC-12 (prompt half), AC-14.

**WI-S2-3 [S2] Platform (systemd), prereqs, binary, root guard.** files:
`cmd/claude-memory/platform.go` (systemd probe + bus retry, package
managers, `--jobs-backend`), `internal/setup/steps_platform.go`,
`steps_binary.go`, `hints.go`, tests; `cmd/claude-memory/install.go`
(root guard).
- Prereqs: `LookPath` for git, claude, psql, az, ollama, with the hint
  table; blocked → re-check / skip / quit.
- Binary: `Paths.Self`; refuse temp-dir/GOCACHE executables; same-file
  check; atomic copy 0755; PATH warning; darwin `xattr -p
  com.apple.quarantine` read-only probe.

Satisfies AC-15 (S2 part), AC-16, AC-17, AC-18 (hints), AC-35, AC-69.

**WI-S2-4 [S2] Topology + database (A, B) + shared bootstrap SQL.** files:
`internal/setup/steps_topology.go`, `steps_database.go`, `dsn.go`,
`bootstrap.go`, tests, `testdata/bootstrap/*`;
`deploy/initdb/app-role.psql` (new), `deploy/initdb/01-app-role.sh`
(wrapper), `deploy/embed.go` (add the file);
`internal/postgres/probe.go` (`LocalServerEvidence`);
`internal/postgres/bootstrap_integration_test.go`.
- Topology A/B; evidence-based default (AC-19); `--topology
  docker-*` → exit 2 "deferred".
- DSN builder (`url.UserPassword`, `sslmode`), classifier, re-ask on
  `auth` (AC-20); `--pg-dsn` with password → exit 2; `--pg-password-stdin`
  read first in `install.go` (AC-31).
- A: existing-DB path (AC-20 prompts) or create path → password (AC-22)
  → env file first → `bootstrap.sql` 0600 in `<StateDir>` → print the
  OS-specific command → re-check loop → delete the file when `ok`
  (Design 5). Name validation regex.
- Topology/DSN change warning (AC-26).
- Integration test (skipped without `psql`): render with sentinel values,
  run twice via `psql` as superuser, then `Migrate` as the app role; unit
  test that `01-app-role.sh` references `app-role.psql`.

Satisfies AC-19..AC-22, AC-26, AC-31, AC-65 (bootstrap part).

**WI-S2-5 [S2] Env file step.** files: `internal/setup/envfile.go`
(edit-in-place over `config.ParseEnvFile` order),
`steps_envfile.go`, `testdata/envfile/*`.
- Managed keys set in place; unknown lines preserved; `export`/quoted
  forms converted with consent; `unparseable-value` kept + reported;
  recoverable unencoded password re-encoded; perm-only repair is `chmod`
  (AC-29); 0600 in 0700.
- Golden: fresh, update, unknown kept, export converted, quoted
  unquoted, unparseable kept, base64-`/` re-encoded, idempotent.

Satisfies AC-28, AC-29.

**WI-S2-6 [S2] Migrate step.** files: `internal/setup/steps_migrate.go`.
Detect = `pg.schema` introspection; Apply = `DBProber.Migrate(ctx,
state.DSN)` with the step-state DSN. `Requires: database, envfile`;
`hooks.settings` requires it.

Satisfies AC-25.

**WI-S2-7 [S2] Ollama step.** files: `internal/setup/steps_ollama.go`.
URL resolution; the non-loopback warning with the 800 ms hook-timeout
wording (AC-33); pull with progress; the 1024-dim verify.

Satisfies AC-32 (step), AC-33.

**WI-S2-8 [S2] Namespaces step.** files: `internal/setup/steps_namespaces.go`,
`projects.go` (existence-checked decoding of `<ClaudeDir>/projects`
names), `cmd/claude-memory/setup_adapters.go` (`nsStore`).
- Absent → `Init` with `--namespace` flags or prompted mappings; valid →
  `ok` with optional "add mappings"; broken → `modified`, never written.

Satisfies AC-47.

**WI-S2-9 [S2] Hook steps.** files: `internal/setup/steps_hooks.go`,
tests.
- `hooks.scripts`: render wrappers with `CLAUDE_MEMORY_BIN` default;
  0755; record hashes.
- `hooks.settings`: `settings.Analyze` → state per Design 3 → `Merge`
  (WI-S1-7) only if `changed` → one timestamped backup → re-read + hash
  compare → atomic write keeping mode, following an in-`Home` symlink →
  record canonical entries in the manifest. AC-70 duplicates surfaced in
  the summary.

Satisfies AC-36, AC-37 (step), AC-38 (step), AC-39, AC-70 (install).

**WI-S2-10 [S2] MCP step.** files: `internal/setup/steps_mcp.go`,
`cmd/claude-memory/setup_adapters.go` (`claudeCLI`: `mcp add --scope
user` / `mcp remove --scope user` over the mutating Runner).
Detect = `mcpreg.Read` (WI-S1-9); absent → add; outdated → remove + add;
`ok` → no `claude` call.

Satisfies AC-40 (register), AC-67 (Detect half, re-asserted in the
engine tests).

**WI-S2-11 [S2] Skills step.** files: `internal/setup/steps_skills.go`,
`diff.go` (minimal LCS unified diff; no dependency). Per-file hash states
(AC-41); first-run-after-hand-install message; overwrite keeps a `.bak`
after the AC-6 Confirm.

Satisfies AC-41 (step), AC-51 (files).

**WI-S2-12 [S2] CLAUDE.md step.** files: `internal/setup/steps_claudemd.go`,
tests. Uses `mdblock` + `InGitRepo` (WI-S1-8); default target
`<ClaudeDir>/CLAUDE.md`; `--yes` rules per AC-42.

Satisfies AC-42 (step).

**WI-S2-13 [S2] Jobs: launchd install + systemd + step.** files:
`internal/setup/jobs_launchd.go` (Render/Install/Remove),
`jobs_systemd.go`, `steps_jobs.go`, `integration/launchd/*.plist`
(templates), `integration/systemd/*` (new), `integration/embed.go`
(add `systemd`), golden plists/units, tests.
- launchd: write plist → `bootout` (tolerated "not loaded") →
  `bootstrap`; legacy `run-with-env.sh` plist → `outdated` → replaced.
- systemd: units → `daemon-reload` → `enable --now`; `SystemdEnv` from
  AC-16 on every call; linger probe + hint; detection via one `systemctl
  --user show` per job.
- Backend `none` → `blocked` + printed instructions; no cron.
- Step: PATH from `LookPath` (stable shims preferred);
  `MEMORY_PR_INGEST_REPOS` via the envfile editor; Azure-only note;
  `--no-jobs`. Doctor `jobs` switches to the full JobManager set and the
  backend-differs-from-manifest warning.

Satisfies AC-16 (wiring), AC-43, AC-44 (install), AC-45.

**WI-S2-14 [S2] `install` command + final doctor + `--upgrade`.** files:
`cmd/claude-memory/install.go`, `main.go` (early dispatch),
`install_test.go`, `internal/setup/install_test.go`.
- Flag set (AC-4), deferred flag values → exit 2; adapters; `Engine.Run`;
  final in-process doctor (AC-62) + restart line per WI-S1-0;
  `--upgrade` = `--yes` (same plan asserted).
- Sentinel test (AC-30) over a full `--yes --topology remote` run with
  fakes; no-op re-run test (AC-50); hand-install fixture (AC-51 identity
  rules); dry-run golden (AC-13); `--yes` never overwriting `modified`
  (AC-12, AC-52).

Satisfies AC-1 (S2 half), AC-4 (install half), AC-12, AC-13, AC-30
(sentinel), AC-50..AC-52, AC-62.

**WI-S2-15 [S2] Uninstall.** files: `internal/setup/uninstall.go`,
`cmd/claude-memory/uninstall.go`, tests.
- Reverse recorded artifacts by kind; `modified` kept and listed; the
  binary only when it is the recorded one at `--bin-dir`; manifest
  trimmed per artifact and deleted when empty; no manifest → list only.
- End-to-end on a temp `Paths`: install → uninstall → tree equals the
  pre-install tree apart from kept config (file-tree hash); a partial
  uninstall leaves a trimmed manifest.

Satisfies AC-54, AC-56.

**WI-S2-16 [S2] Integration e2e, CI, docs.** files:
`internal/setup/install_integration_test.go`, `.github/workflows/ci.yml`,
`integration/INSTALL.md`, `DEPLOY.md` (upgrade → `install --upgrade`;
server half stays manual), `integration/bin/run-with-env.sh` (legacy
header), `integration/mcp-registration.md`, `integration/ollama.md`,
`docs/specs/README.md`.
- `install --yes --topology local` against an existing test database
  (`MEMORY_TEST_PG_ADMIN_DSN` creates it first) with `httptest` Ollama
  and fake claude/jobs adapters, temp `Paths`; second run is a no-op.
- Binary-outside-checkout test (AC-34): build to temp dir A, run with
  `cmd.Dir` = temp dir B and `HOME` = temp dir C.
- CI integration job adds `./internal/setup/...`.

Satisfies AC-34 (S2 half), AC-65 (S2 half), AC-66 (S2 half).

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

## Rollout
1. **Slice 1 merged → owner runs `claude-memory doctor`** on the current
   hand install. Nothing about the running setup changes. Expected warns:
   `run-with-env.sh` plists, legacy `$HOME` hook entries. Fix the env file
   by hand if `env.format` flags it.
2. **Slice 2 merged → owner runs `claude-memory install`** on the laptop
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
