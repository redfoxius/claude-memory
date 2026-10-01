# Claude Memory — `install` / `doctor` Implementation Plan

**Status:** not started. Plan v0.1 for spec v0.1. Needs plan review ×2
before implementation (SDD flow). The owner questions in spec §13 come
with recommendations, and the plan assumes those recommendations. A
different answer changes only the WIs named in the "If the owner decides
otherwise" table at the end.

## Spec
- `docs/specs/install-doctor/01-spec.md` (SPEC-2026-10-01-install-doctor,
  v0.1, AC-1..AC-66; AC-48 and AC-61 optional).

## Delivery: four PRs

| PR | Scope | Work Items | ACs |
|---|---|---|---|
| **A — foundations + doctor** | early dispatch, `version`, `migrate`, `config.ParseEnvFile`, embedded assets, `internal/setup` ports + redactor + engine skeleton, all doctor checks | WI-0..WI-8 | AC-1..AC-3, AC-27, AC-34 (embed half), AC-57..AC-61, AC-63 (guard), AC-66 (DEPLOY.md env fix) |
| **B — install: core local steps** | engine, prompter, manifest, binary, prereqs, topology A/B, database bootstrap, envfile, migrate, ollama, namespaces, hooks scripts + settings merge, MCP, skills, CLAUDE.md, final doctor | WI-9..WI-21 | AC-4..AC-15, AC-17..AC-22, AC-25..AC-26, AC-28..AC-42, AC-47..AC-53, AC-62, AC-64 |
| **C — jobs + uninstall + upgrade** | launchd/systemd/cron JobManagers, `jobs` step, `uninstall`, `--upgrade` polish | WI-22..WI-26 | AC-16, AC-43..AC-46, AC-52 (jobs half), AC-54..AC-56 |
| **D — docker topologies + docs** | C1 local bundle + compose up, C2 server bundle, INSTALL/DEPLOY/README rewrite, integration e2e | WI-27..WI-30 | AC-23, AC-24, AC-65, AC-66 |

Doctor ships first because it is useful on its own against today's
hand-installed setup. It also checks the step engine's detection logic,
since detection and checks share probes. B depends on A. C and D both
depend on B and can proceed in parallel; they touch different step files,
and both touch `steps.go` only for registration.

## Context
- `cmd/claude-memory/main.go:run()` dispatches only `namespaces` before
  `config.LoadFromFile` + `config.Load()`, and `config.Load` fails without
  `MEMORY_PG_DSN`. The new subcommands go in that early branch (AC-1).
  `migrate` needs config, so it goes after config load and still
  runs before `flag.Parse`.
- `config.LoadFromFile` splits each line on `=`: `export ` prefixes and
  quotes are not handled. DEPLOY.md documents the `export` form.
- `postgres.New` runs `migrationSQL` (0001 + 0002, concatenated and split
  on `;`); `postgres.Open` doesn't. 0001 does `CREATE EXTENSION IF NOT
  EXISTS vector`, which needs a superuser if the extension is missing.
  `schema_migrations` exists but nothing writes to it, so schema state
  must be detected by introspection.
- `deploy/initdb/01-app-role.sh` holds the role/db/extension SQL inside a
  bash heredoc with psql variables. It is not idempotent (bare `CREATE
  ROLE`), which is fine for initdb because it runs once.
- External binaries in use: `git` (gitlog, ingest-pr), `claude`
  (extraction/haiku.go), `az` (azuredevops). The launchd jobs inherit no
  PATH.
- `integration/` and `deploy/` contain no Go packages. `go:embed` cannot
  reference parent directories, so each needs a tiny package of its own.
- `namespace.Init/Add/Save` use `os` directly and write atomically at
  0600. They are reused behind a port.
- CI runs `go test ./...` and an integration job covering only
  `./internal/postgres/...`.

## Scope
In scope is everything in spec §6. Out of scope: spec §12, a release
workflow (§13 #2 follow-up), and renaming launchd labels.

## Modules Touched

| Path | Change |
|---|---|
| `cmd/claude-memory/main.go` | early dispatch; `version`, `migrate`; adapter construction for setup |
| `cmd/claude-memory/version.go` (new) | `version` var, `ReadBuildInfo` fallback |
| `cmd/claude-memory/install.go`, `doctor.go`, `uninstall.go` (new) | flag parsing → `setup.Engine` / `setup.Doctor` with adapters |
| `cmd/claude-memory/setup_adapters.go` (new) | `osFS`, `execRunner`, `ttyPrompter`, `claudeCLI`, `platformDetector`, `nsStore` |
| `internal/config/config.go` | `ParseEnvFile`, `EnvFile` type; `LoadFromFile` on top |
| `internal/postgres/probe.go` (new) | `Probe` (ping RTT, ext version, schema objects), `Bootstrap`, `Migrate` |
| `internal/postgres/bootstrap.sql` (new, embedded) | idempotent role/db/ext/grants SQL (shared with initdb) |
| `internal/ollama/prober.go` (new) | version, tags, pull (stream), embed dims |
| `internal/setup/` (new) | `ports.go`, `engine.go`, `state.go`, `manifest.go`, `redact.go`, `prompt.go` (line prompter over io.Reader/Writer + Secret func), `jsonobj.go` (ordered JSON), `settings.go`, `envfile.go`, `mdblock.go`, `cronblock.go`, `diff.go`, `steps_*.go`, `doctor.go`, `checks_*.go`, `jobs_launchd.go`, `jobs_systemd.go`, `jobs_cron.go`, `bundle.go`, `testdata/` |
| `integration/embed.go` (new, `package integration`) | `//go:embed hooks skills launchd claude-md-section.md settings.snippet.json` |
| `integration/claude-md-section.md` (new) | section body, location-neutral wording; `claude-md-snippet.md` points at it |
| `integration/systemd/` (new) | `claude-memory-{cleanup,ingest-pr}.{service,timer}` templates |
| `integration/launchd/*.plist` | `text/template` placeholders (binary, PATH, log dir) replacing `__HOME__`; ProgramArguments → binary directly |
| `deploy/embed.go` (new, `package deploy`) | `//go:embed docker-compose.yml pg_hba.conf initdb backup.sh systemd .env.example local` |
| `deploy/local/` (new) | `docker-compose.yml`, `pg_hba.conf` for C1 |
| `deploy/initdb/01-app-role.sh` | uses the shared idempotent SQL (or is generated from it; WI-4) |
| `Makefile` | `LDFLAGS=-X main.version=$(shell git describe --tags --always --dirty)`; cross-build adds darwin/amd64, linux/arm64 |
| `.github/workflows/ci.yml` | integration job adds `./internal/setup/... ./cmd/claude-memory/...` |
| `go.mod` | `golang.org/x/term` |
| `integration/INSTALL.md`, `DEPLOY.md`, `integration/bin/run-with-env.sh`, `docs/specs/README.md` | docs (WI-30) |

## Architectural Constraints
- **Ports are declared by the consumer.** `internal/setup` declares
  every interface (spec §10). Adapters live in `cmd/claude-memory`
  (`os`, `exec`, `x/term`), `internal/postgres` (`Probe`, `Bootstrap`,
  `Migrate`) and `internal/ollama` (`Prober`). Only `main.go` and
  `install.go`/`doctor.go`/`uninstall.go` construct them.
- **`internal/setup` imports none of `os/exec`, `net/http`, pgx,
  `golang.org/x/term`.** It may use `os` only for `os.ErrNotExist`, and
  `io/fs` types. A test enforces this with `go list -deps` (AC-63).
- **Mutating vs read-only is part of the data.** `Cmd.Mutating` is set
  by the step code. `execRunner{readOnly: true}` returns `ErrReadOnly`
  for a mutating command, and the FS adapter in dry-run or doctor mode
  returns `ErrDryRun` from every write method. Doctor and `--dry-run`
  are therefore write-free by construction, not by review.
- **Steps never print.** They return `Detail`, actions and errors. The
  engine renders the output, and the `Redactor` is applied at the single
  output sink, in both text and JSON.
- **One check registry.** Doctor checks and step Detect share probe
  helpers (`probeDB`, `probeOllama`, `readSettings`, ...), so `install`
  and `doctor` cannot disagree.
- **No shell.** Every external command is argv. Templates render into
  files, never into a shell string.

## Design Decisions
1. **Stdlib line prompts + `x/term`, not a TUI.** This is spec §4. The
   prompter takes `io.Reader`/`io.Writer` plus an injected `secret
   func() (string, error)`. The TTY adapter supplies
   `term.ReadPassword`, and tests supply a scripted transcript.
2. **Order-preserving JSON via `json.Decoder.Token`.** A ~150-line
   `jsonobj` type (ordered keys, `json.RawMessage` leaves) is enough to
   keep key order and unknown values. Re-encoding uses
   `json.MarshalIndent` on the leaves and 2-space indentation. Strict
   JSON only: Claude Code's `settings.json` is strict JSON, so comments
   lead to a refusal (AC-38). Rejected options: a `map[string]any`
   round-trip (reorders keys and makes for noisy diffs), and a third-party
   JSON5/JSONC library (adds a dependency and could silently drop
   comments).
3. **Hook identity is by command path suffix.** It is
   `…/claude-memory/user-prompt-submit.sh` / `…/session-end.sh`, plus
   commands containing `claude-memory hook|extract`. This matches hand
   installs that used `$HOME/...` and lets an absolute-path install
   replace them. The installed command uses the absolute path. `$HOME`
   would also work, but the absolute path makes doctor's
   "command path exists" check exact.
4. **Jobs call the binary directly.** The binary already loads the env
   file through the 0600 check, so `run-with-env.sh` adds nothing except
   bash expansion bugs. PATH is captured at install time from
   `LookPath(claude|git|az)`.
5. **Bootstrap SQL lives in one embedded file.** It is idempotent: a `DO`
   block creates the role only if it is missing (and `ALTER ROLE …
   PASSWORD` only when we generated a new password). `CREATE DATABASE`
   cannot run inside `DO`, so the Go side checks `pg_database` first.
   Then come `CREATE EXTENSION IF NOT EXISTS vector`, the REVOKEs and
   the GRANTs. `initdb/01-app-role.sh` keeps its current shape, and a
   test asserts that both produce the same catalog state (WI-4).
   Rejected: generating the shell script from the SQL, which is too much
   machinery for 6 statements.
6. **Schema detection by introspection.** `Probe` checks the
   `information_schema.columns` / `pg_indexes` objects introduced by
   each embedded migration (a Go table next to the migrations: `0001 →
   records.embedding, idx_records_embedding_hnsw`, `0002 →
   records.namespace`). A migration test fails if a new migration file
   has no table entry.
7. **The C1 compose uses bridge + loopback publish + named volume** (spec
   AC-23). It is a separate template, `deploy/local/`, because the server
   template's host networking and host paths are deliberate for the
   tailnet and wrong for a laptop.
8. **The manifest is written per step** (AC-9). The engine owns it, and
   steps return `[]Artifact` from Apply.
9. **Golden tests with `-update`** live under
   `internal/setup/testdata/<area>/<case>.{in,golden}`. Each case runs
   twice (idempotence) and, where it applies, through uninstall.

## Work Items

```
PR A: 00 ─> 01 (dispatch/version/migrate) ─┬─> 02 (ParseEnvFile)
                                           ├─> 03 (embed pkgs) 
      04 (pg Probe/Bootstrap/Migrate) ─────┤
      05 (ollama Prober) ──────────────────┤
      06 (setup ports, redactor, fakes, guard) ─> 07 (doctor checks) ─> 08 (doctor cmd + json)
PR B: 09 (engine+state+manifest+lock) ─> 10 (prompter) ─> 11 (platform/prereqs/binary)
      ─> 12 (topology + database A/B) ─> 13 (envfile) ─> 14 (migrate step) ─> 15 (ollama step)
      ─> 16 (namespaces) ; 17 (jsonobj+settings merge) ─> 18 (hooks steps) ; 19 (mcp) ; 20 (skills) ; 21 (claude-md)
      ─> final doctor wiring + install cmd
PR C: 22 (launchd) ; 23 (systemd) ; 24 (cron) ─> 25 (jobs step) ─> 26 (uninstall + upgrade)
PR D: 27 (C1 bundle + compose) ; 28 (C2 bundle) ─> 29 (integration e2e + CI) ─> 30 (docs)
```

### PR A — foundations + doctor

0. **Verify assumptions (manual, recorded here).** On the owner's
   machine with the current `claude --version`, check:
   - `claude mcp get claude-memory`: output shape and exit code when the
     server is absent;
   - hook `command` shell expansion;
   - whether `CLAUDE_CONFIG_DIR` relocates `settings.json` and skills;
   - `launchctl print gui/$UID/io.github.claude-memory.cleanup`
     output, loaded vs not;
   - the `~/.claude/projects` directory encoding.

   Save the outputs as test fixtures under
   `internal/setup/testdata/fixtures/`. Satisfies spec §4 assumptions.

1. **Early dispatch, `version`, `migrate`.** files: `cmd/claude-memory/main.go`,
   `version.go`, `main_test.go`, `Makefile`.
   - In `run()`, dispatch `install|uninstall|doctor|version` before
     config load. `migrate` loads config and calls `postgres.New`, then
     prints the AC-3 line.
   - `version`: `var version = ""`; if it is empty, fall back to
     `debug.ReadBuildInfo()` (`vcs.revision`, `vcs.modified`).
   - Makefile `LDFLAGS`; cross-build targets.
   - Tests: `HOME=t.TempDir()`, so `doctor --json` returns no
     config-load error, and the version formatter is checked.

   Satisfies AC-1, AC-2, AC-3 (unit half).

2. **`config.ParseEnvFile`.** files: `internal/config/config.go`,
   `config_test.go`.
   - `type EnvFile struct{ Values map[string]string; Order []string;
     Mode fs.FileMode; Findings []Finding }` (finding kinds:
     `export-prefix`, `quoted`, `duplicate`, `no-equals`, `group-world-readable`).
   - `LoadFromFile` = `ParseEnvFile` + the existing perm error +
     `Setenv`-if-unset. Behavior stays identical, and the existing tests
     stay green.

   Satisfies AC-27.

3. **Embedded asset packages.** files: `integration/embed.go`,
   `integration/claude-md-section.md`, `integration/claude-md-snippet.md`
   (points at the section), `integration/launchd/*.plist`
   (`text/template`), `integration/systemd/*` (new), `deploy/embed.go`,
   `deploy/local/*` (C1 templates; filled in WI-27).
   - Embed `.env.example` explicitly by name.
   - Test: every file `install` references exists in the FS; templates
     parse; rendering with sample data yields golden output.

   Satisfies AC-34 (embed half).

4. **Postgres probe, bootstrap, migrate adapters.** files:
   `internal/postgres/probe.go`, `bootstrap.sql`, `schema_objects.go`,
   `probe_integration_test.go`.
   - `Probe(ctx, dsn) (DBStatus, error)`: `pgxpool.ParseConfig`, connect
     with a timeout, then ping RTT, `SELECT extversion FROM pg_extension
     WHERE extname='vector'`, and the schema-object table (Design 6).
     Returns `ErrClass` (`unreachable|auth|nodb|hba|other`) from pgconn
     error codes (`28P01`, `3D000`, `28000`) and net errors.
   - `Bootstrap(ctx, adminDSN, spec)`: the idempotent SQL, using
     `pgx.Identifier{}.Sanitize()` for names and a bound parameter for
     the password. A second run is a no-op.
   - `Migrate(ctx, dsn)` = `New` + `Close`.
   - Integration tests (tag, `MEMORY_TEST_PG_ADMIN_DSN`/testcontainers):
     bootstrap twice; the app role then passes `Migrate`; `Probe` reports
     all objects; a wrong password gives `auth`; a missing db gives
     `nodb`. Also a test comparing the catalog after `initdb` script SQL
     vs `bootstrap.sql`, as role/db/ext/ACL rows (Design 5).
   - Unit test: every `migrations/*.sql` has a schema-object entry.

   Satisfies AC-21 (adapter), AC-3, AC-58 (`pg.*` data).

5. **Ollama prober.** files: `internal/ollama/prober.go`, `prober_test.go`.
   - `/api/version`, `/api/tags`, `POST /api/pull` (NDJSON progress, with
     the `status:"success"` terminator), and one `/api/embed` →
     `len(embeddings[0])`.
   - Tested with `httptest.Server`, covering wrong dims, 404 model and
     a timeout.

   Satisfies AC-32 (adapter).

6. **`internal/setup` ports, redactor, fakes, guard.** files:
   `internal/setup/ports.go`, `redact.go`, `redact_test.go`,
   `fakes_test.go` (FakeRunner with a scripted argv → Result map that
   fails on any unscripted argv and records calls; FakePrompter
   transcript; FakeClock; fault-injecting FS wrapper over a real temp
   dir), `guard_test.go`.
   - `Redactor`: registers secrets at runtime (the literal and its
     `url.QueryEscape`/`PathEscape` forms) and masks `scheme://user:…@`
     userinfo by regex.
   - `guard_test.go`: `go list -deps ./internal/setup` contains none of
     the forbidden imports; the FakeRunner call log never has argv[0] in
     the AC-18/AC-24 deny list.

   Satisfies AC-63, AC-30 (redactor), AC-18 (guard).

7. **Doctor checks.** files: `internal/setup/doctor.go`,
   `checks_env.go`, `checks_db.go`, `checks_ollama.go`,
   `checks_claude.go` (mcp, hooks, skills, claude-md), `checks_misc.go`
   (binary, tools, namespaces, jobs, dirs, manifest), `checks_latency.go`,
   `doctor_test.go`, `testdata/doctor/*.golden.json`.
   - The registry is an ordered `[]Check{ID, Title, Requires, Run}`.
     `Run` returns `Status`, detail and remedy. Each check has its own
     context deadline (`--timeout`). The requirement graph turns
     dependents into `skip`.
   - Hook settings parsing reuses `jsonobj` read-only. This pulls the
     `jsonobj` parser (WI-17's read half) forward into PR A.
   - Until PR C, the `jobs` check reads the plist/unit/cron entry
     through a read-only `JobManager.Detect`, with launchd only. systemd
     and cron detection arrive in WI-23/24.
   - Latency: run the installed wrapper N times through the Runner, as
     a read-only command: with `session_id` `doctor.probe` it writes no
     cache file and, once events exist, no events. Compute p50/p95/max with nearest-rank;
     report the cold run separately.
   - Tests: one per check id × branch; a hanging prober with a fake
     clock → bounded; sentinel password never in output.

   Satisfies AC-57, AC-58, AC-59, AC-61.

8. **`doctor` command + JSON + DEPLOY.md env fix.** files:
   `cmd/claude-memory/doctor.go`, `setup_adapters.go` (read-only FS, exec
   runner, platform detector), `DEPLOY.md` (laptop env snippet →
   `KEY=VALUE`).
   - Text renderer: aligned `status id detail`, plus a `fix:` line for
     non-pass checks and a summary line.
   - JSON per AC-60; exit codes per AC-59.
   - Manual: run on the owner's current setup and paste the output here.
     The expectation is all pass or warn; any fail found is a real
     finding.

   Satisfies AC-59, AC-60, AC-66 (DEPLOY env fix).

### PR B — install: core local steps

9. **Engine, states, manifest, lock.** files: `internal/setup/engine.go`,
   `state.go`, `manifest.go`, `lock.go` (flock via an FS port method
   `Lock(path)`; the adapter uses `syscall.Flock`), `engine_test.go`.
   - Order and `Requires`; status table; default choices (AC-6);
     combined plan; one confirmation; Apply → Verify. After each
     successful step, `manifest.Merge(stepArtifacts)` + atomic write.
     Failure isolation (AC-8). Exit codes 0/1/2/130 (signal handling in
     the cmd adapter: on cancel, the engine stops before the next action).
   - `--only/--skip` filtering (AC-53).
   - A corrupt manifest is backed up and treated as absent.

   Satisfies AC-5..AC-9, AC-49, AC-53.

10. **Prompter.** files: `internal/setup/prompt.go`, `prompt_test.go`,
    `cmd/claude-memory/setup_adapters.go` (`ttyPrompter`:
    `term.IsTerminal` on fds 0 and 1, `term.ReadPassword`).
    - Select/Confirm/Text/Secret; 3-retry rule; `NO_COLOR`; non-TTY
      without `--yes` → engine exits 2 before Detect.

    Satisfies AC-10, AC-11, AC-12 (prompt half), AC-14.

11. **Platform, prereqs, binary steps.** files: `steps_platform.go`,
    `steps_binary.go`, `hints.go` (package hint table), tests.
    - Platform detection per AC-15/AC-16/AC-17 (WSL via
      `/proc/sys/kernel/osrelease`).
    - Prereqs: `LookPath` for git, claude, psql, docker, az, ollama, with
      the hint table. Blocked steps offer re-check / skip / quit.
    - Binary: `os.Executable` + `EvalSymlinks` (via the FS port); a
      same-file check; atomic copy at 0755; a PATH warning; on darwin,
      an `xattr -p com.apple.quarantine` read-only probe.

    Satisfies AC-15..AC-18, AC-35.

12. **Topology + database (A, B).** files: `steps_topology.go`,
    `steps_database.go`, `dsn.go`, tests.
    - Topology detection (AC-19). Stored in the manifest.
    - B: prompts, the `url.UserPassword` DSN builder, `Probe` with
      classification and re-ask on `auth` (AC-20).
    - A: probe the app DSN. If it is missing or lacks the extension,
      ask for the admin DSN (in memory) → `Bootstrap` with a generated
      password (AC-22), or print `bootstrap.sql` with the values filled
      in and the password shown as `<generated, see env file>`, then wait
      for re-check. The password is written to the env file in WI-13
      before the SQL is printed, so the user never has to copy it.
    - Topology/DSN change warning (AC-26).
    - `--pg-dsn` with a password → exit 2 (AC-31); `--pg-password-stdin`.

    Satisfies AC-19..AC-22, AC-26, AC-31.

13. **Env file step.** files: `envfile.go` (edit-in-place over
    `config.ParseEnvFile` order), `steps_envfile.go`,
    `testdata/envfile/*`.
    - Managed keys are set in place; unknown lines are preserved; the
      `export` form is converted with consent; a perm-only repair is
      `chmod` (AC-29); writes are at 0600 and the directory is 0700.
    - Golden: fresh, update, unknown kept, export converted, idempotent.

    Satisfies AC-28, AC-29.

14. **Migrate step.** files: `steps_migrate.go`. Calls
    `DBProber.Migrate`. It is always `Requires: database, envfile`, and
    `hooks.settings` requires it.

    Satisfies AC-25.

15. **Ollama step.** files: `steps_ollama.go`. URL resolution, the
    non-loopback warning (AC-33), pull with progress (the engine renders
    a single updating line on a TTY, or percent lines otherwise), the
    1024-dim verify.

    Satisfies AC-32, AC-33.

16. **Namespaces step (+ optional seed).** files: `steps_namespaces.go`,
    `projects.go` (decode `~/.claude/projects` names per the WI-0
    fixture), `steps_seed.go`, `cmd/claude-memory/setup_adapters.go`
    (`nsStore` wrapping `internal/namespace`).
    - Absent → `Init` with the `--namespace` flags or prompted mappings.
      Present and valid → ok, with an optional "add mappings". Broken →
      `modified`, never written.
    - Seed: offered per AC-48 and run by calling the existing seed code
      path. This needs a small refactor in `seed.go`: extract
      `runSeed(ctx, svc, path, dry)` and pass a service built by the
      adapter. The setup package depends only on a `Seeder` port.

    Satisfies AC-47, AC-48.

17. **Ordered JSON + settings merge.** files: `jsonobj.go`, `settings.go`,
    `settings_test.go`, `testdata/settings/` cases:
    `missing`, `empty-object`, `no-hooks-key`, `other-events`,
    `other-hooks-same-event`, `ours-identical`, `ours-legacy-$HOME`,
    `ours-different-timeout`, `ours-duplicated`, `ours-under-matcher`,
    `unknown-top-level-keys-order`, `nested-unicode-escapes`, `comments`
    (refuse), `trailing-comma` (refuse), `hooks-is-array` (refuse),
    `truncated` (refuse), `symlinked`. Each case has
    `.in.json`, `.install.golden.json` and `.uninstall.golden.json`, and
    runs twice.
    - Merge and unmerge are pure functions over bytes, with the
      result plus a change summary (`added`, `replaced`, `deduped`,
      `removed`).

    Satisfies AC-37, AC-38, AC-54 (settings half), AC-64.

18. **Hook steps.** files: `steps_hooks.go`.
    - `hooks.scripts`: render the wrappers with `CLAUDE_MEMORY_BIN`
      defaulting to the bin path; 0755; record hashes.
    - `hooks.settings`: read → merge → if changed: backup (keep 5, sorted
      by timestamp), re-read and compare hash, atomic write keeping mode
      and following a symlink to its target. `$CLAUDE_CONFIG_DIR` is
      honored.

    Satisfies AC-36, AC-39.

19. **MCP step.** files: `steps_mcp.go`, `cmd/claude-memory/setup_adapters.go`
    (`claudeCLI` over the Runner). The parser for `claude mcp get` is
    tested against the WI-0 fixtures. States are absent / ok /
    outdated / `outdated?` (unparseable output, default keep).

    Satisfies AC-40.

20. **Skills step.** files: `steps_skills.go`. Per-file hash states
    (AC-41); diff via `diff.go` (a minimal LCS unified diff of a few
    dozen lines; no dependency); overwrite keeps a `.bak`.

    Satisfies AC-41.

21. **CLAUDE.md step + install command + final doctor.** files:
    `mdblock.go`, `steps_claudemd.go`, `testdata/mdblock/*`,
    `cmd/claude-memory/install.go`.
    - Managed block insert/refresh/remove; refuse unbalanced or
      duplicated markers; detect a foreign hand-pasted heading; detect
      "inside a git repo" via the FS (walk up for `.git`), with no
      `git` exec; the `--yes` rules.
    - `install.go`: the flag set (AC-4); build adapters; `Engine.Run`;
      the final doctor step (AC-62); `--upgrade` mode = `--yes` + choice
      overrides (`outdated → repair`, `modified → keep` + warn).
    - Sentinel test (AC-30) over a full `--yes --topology remote` run
      with fakes.
    - No-op re-run test (AC-50) and hand-install adoption fixture (AC-51).

    Satisfies AC-4, AC-12, AC-13, AC-30, AC-42, AC-50..AC-52, AC-62.

### PR C — jobs + uninstall + upgrade

22. **launchd JobManager.** files: `jobs_launchd.go`, tests, golden plists.
    Render the plists from templates. Detect via `launchctl print
    gui/<uid>/<label>` (loaded or not) plus file hash. A legacy plist
    with `run-with-env.sh` counts as `outdated`. Install: write the
    plist, then `bootout` (exit status for "not loaded" tolerated, per
    the WI-0 fixture), then `bootstrap`. Remove: `bootout` + delete.

    Satisfies AC-44.

23. **systemd JobManager.** files: `jobs_systemd.go`, tests, golden units.
    Includes the linger probe and hint.

    Satisfies AC-45.

24. **cron JobManager.** files: `jobs_cron.go`, `cronblock.go`,
    `testdata/cron/*`.

    Satisfies AC-46.

25. **Jobs step.** files: `steps_jobs.go`. Backend from platform; PATH
    from `LookPath`; `MEMORY_PR_INGEST_REPOS` prompt (written through the
    envfile editor, so WI-13 is reused); the Azure-only note; `--no-jobs`.
    The doctor `jobs` check switches to the full JobManager set.

    Satisfies AC-16 (wiring), AC-43.

26. **Uninstall + upgrade polish.** files: `uninstall.go` (setup),
    `cmd/claude-memory/uninstall.go`, tests.
    - Reverse the manifest artifacts by kind; modified files default to
      keep; purge flags; typed `delete` for `--purge-data`; no manifest
      → list only.
    - End-to-end on a temp HOME: install → uninstall → the tree equals
      the pre-install tree apart from kept config (an assertion over a
      file-tree hash).
    - Upgrade test: a manifest from an "older" asset set → refreshed;
      user-modified kept.

    Satisfies AC-52, AC-54..AC-56.

### PR D — docker topologies + docs

27. **C1 local bundle.** files: `deploy/local/docker-compose.yml`,
    `deploy/local/pg_hba.conf`, `bundle.go`, `steps_database.go` (C1
    branch), golden bundle.
    - Checks: `docker compose version` and `docker info` (both read-only);
      port-free check via a TCP dial port method on the FS/Net adapter
      (`Dialer` port, if one is needed; otherwise via `DBProber`).
    - `up -d` is exactly the AC-23 argv (mutating). Health is polled
      with `docker inspect -f '{{.State.Health.Status}}'
      claude-memory-local-postgres` (read-only) every 2 s, up to 60 s.

    Satisfies AC-23.

28. **C2 server bundle.** files: `bundle.go`, `templates/SERVER-STEPS.txt.tmpl`
    (embedded with deploy), golden bundle.
    - A non-empty target is refused unless the manifest says it is ours.
      `.env` is 0600; files are byte-identical to `deploy/`. The steps
      are printed, the wizard waits for Enter or s, and the DSN is
      prefilled → B.

    Satisfies AC-24.

29. **Integration e2e + CI.** files:
    `internal/setup/install_integration_test.go`,
    `.github/workflows/ci.yml`.
    - The `local` topology runs against `MEMORY_TEST_PG_ADMIN_DSN` or
      testcontainers (bootstrap → migrate → doctor pg checks pass), with
      an `httptest` Ollama returning 1024-dim vectors and fake
      claude/jobs adapters, all in a temp HOME. A second run is a no-op.
    - The binary-outside-checkout test (AC-34): `go build` to a temp dir,
      then run `install --dry-run --yes --topology remote` there.

    Satisfies AC-34, AC-65.

30. **Docs.** files: `integration/INSTALL.md` (new top section; manual
    steps under "Manual install (reference)"), `DEPLOY.md` (server half
    → `install --topology docker-server`; upgrade section →
    `install --upgrade`), `integration/bin/run-with-env.sh` (legacy
    header), `integration/mcp-registration.md`, `integration/ollama.md`
    (pointers), `docs/specs/README.md` (status row + backlog item 8
    link).

    Satisfies AC-66.

## Test Strategy
- **Unit, the default for everything.** `internal/setup` runs against
  FakeRunner (scripted, strict), FakePrompter (transcripts), FakeClock,
  and a real temp HOME (`t.Setenv("HOME", t.TempDir())`) behind the FS
  port, with a fault-injecting wrapper for rename/write failures. No
  real `brew`/`apt`/`launchctl`/`systemctl`/`crontab`/`docker`/`claude`/
  `ollama` call is possible, because the FakeRunner fails on unscripted
  argv and the guard test bans `os/exec` in the package.
- **Golden files** cover settings.json, CLAUDE.md, env file, crontab,
  plists, systemd units, bundles, dry-run output and doctor JSON. The
  `-update` flag regenerates them. Each merge case runs twice and,
  where it applies, through uninstall.
- **Property-style checks**: no-op re-run ⇒ 0 writes / 0 mutating
  commands (AC-50); `--dry-run` and doctor ⇒ 0 writes (AC-13, AC-57);
  the sentinel password never appears outside the env file and bundle
  `.env` (AC-30).
- **Adapters**: the Postgres probe, bootstrap and migrate tests use the
  `integration` tag against `MEMORY_TEST_PG_ADMIN_DSN`
  (`postgres://postgres@localhost:5432/postgres` locally) or
  testcontainers in CI. The Ollama prober is tested against
  `httptest`. The exec runner is tested against `/bin/echo` and `false`
  only, and the TTY prompter is not unit-tested; it is a thin `x/term`
  wrapper covered by the manual run.
- **cmd layer**: flag parsing, early dispatch (no DSN needed), exit
  codes, and the binary-outside-checkout test.
- **Manual (recorded in this plan)**:
  - WI-0 fixtures;
  - doctor on the current setup (WI-8);
  - a full interactive install on a clean macOS user account
    (topology B against the real server), and a re-run that must show
    all ok;
  - `--upgrade` after a rebuild;
  - uninstall;
  - an Ubuntu VM or container with systemd user units (topology A);
  - `doctor --latency=50` p95, which also re-measures backlog 7.

## Risks

| Risk | Mitigation |
|---|---|
| Corrupting `~/.claude/settings.json` (shared with Claude Code and the user's other hooks) | Strict parse or refuse; order-preserving model; golden suite; backup ×5; hash re-check before rename; atomic write; no-op when unchanged. |
| Claude Code CLI output or config layout changes (`claude mcp get`, `CLAUDE_CONFIG_DIR`) | WI-0 fixtures; loose parsing that defaults to `keep` when unsure; doctor reports `outdated?` instead of failing. |
| Password leakage via argv, logs, diffs or errors | No password flags; single redacting output sink; sentinel test across every step and doctor; admin DSN in memory only. |
| Duplicate hooks or jobs on machines installed by hand | Identity rules (Design 3, existing launchd labels); adoption fixture (AC-51). |
| Launchd semantics differ across macOS versions (`bootstrap` vs `load`) | `bootout`/`bootstrap` work on macOS 11+, which is the only supported range. The exit status for "not loaded" is tolerated per WI-0. Doctor's `jobs` check verifies. |
| systemd user timers don't run when the user is logged out | Linger hint; doctor `jobs` warns when Linger=no. |
| Bootstrap SQL drifts from `initdb/01-app-role.sh` | Catalog-equivalence integration test (WI-4). |
| Scope creep (M–L size) | Four PRs, each shippable. Doctor alone already pays off. Docker topologies are last and can slip. |
| Embedding assets bloats the binary | The assets are ~40 KB of text, negligible. |
| Hook regressions | The hook code path is untouched. Only the wrapper template's default bin path is rendered, and a golden test diffs it against today's script. |

## Rollout
1. **PR A merged → owner runs `claude-memory doctor`** on the current
   hand install. Findings are fixed by hand or wait for PR B. Nothing
   about the running setup changes; `run-with-env.sh` and the plists are
   untouched.
2. **PR B merged → owner runs `claude-memory install`** on the laptop
   (topology B). Expect adoption of the hand-installed artifacts
   (AC-51), the env file left as-is (if already plain) or offered for
   conversion, and zero duplicate hooks. Then a re-run, which must show
   all ok with 0 writes.
3. **PR C merged → `install --upgrade`** replaces the legacy plists
   (with `run-with-env.sh`) by direct-binary plists with PATH. The
   owner checks `doctor` `jobs` and the next morning's job logs.
4. **PR D merged →** optional rehearsal of `--topology docker-server`
   into a scratch dir, diffed against the live server's
   `/opt/claude-memory/deploy`, which should be identical apart from
   `.env`. Docs switch to the installer.
5. Rollback at any stage: `claude-memory uninstall` (keeps config and
   data), then the manual INSTALL.md reference steps, or the previous
   binary. Settings backups give a byte-level restore.

## Verification
- `go vet ./... && go test -race ./...` green; integration job green
  with the added packages.
- The manual checklist from the Test Strategy, with outputs pasted into a
  `04-implementation-report.md` at the end.
- Spec §14 checkboxes ticked per PR.

## If the owner decides otherwise (spec §13)

| Question | Alternative answer | Plan impact |
|---|---|---|
| #1 CLAUDE.md target | `acme/CLAUDE.md` default | WI-21: change the default path prompt; `--yes` never writes it (rule unchanged). |
| #2 releases | no releases | none (`make install` path stays primary). |
| #3 run brew | allowed with confirmation | WI-11: add a `brew install`/`brew services start` mutating action for brew only; the guard deny list drops `brew`. |
| #4 x/term | rejected | WI-10: `stty -echo` via Runner on darwin/linux (mutating=false, TTY only); no-echo becomes best effort. |
| #5 remote Ollama | disallowed | WI-15: reject non-loopback URLs; AC-33 becomes an error. |
| #6 launchd labels | rename | WI-22: add a legacy-label detection, then bootout old + bootstrap new on `--upgrade`. |
