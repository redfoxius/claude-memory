# Specification: Claude Memory — `install` / `doctor` (interactive, re-runnable setup)

## 0. Metadata
- Spec ID: SPEC-2026-10-01-install-doctor
- Status: draft (design decisions taken; owner questions in §13)
- Version: 0.1
- Owner: Oleksandr Kolomoiets (user@example.com)
- Supersedes: none. Replaces the manual procedure in `integration/INSTALL.md`
  steps 1–9 and the laptop half of `DEPLOY.md` (they stay as reference).
- Related: `docs/specs/README.md` backlog item 8 (owner request,
  2026-10-01); backlog item 7 (hook skips migrations);
  `docs/specs/namespaces/01-spec.md` AC-25..AC-27 (`namespaces init|add|which`);
  `docs/specs/memory-mvp/01-spec.md` AC-30/AC-31 (hook latency, hook never
  fails), AC-44 (laptop-local Ollama).

## 1. Overview & Problem

Installing `claude-memory` today takes about ten manual steps spread over
`integration/INSTALL.md` and `DEPLOY.md`: build, an env file with a
hand-written DSN, `chmod 600`, Ollama install and model pull, `claude mcp
add`, copying hook scripts, merging JSON into `~/.claude/settings.json` by
hand, pasting a CLAUDE.md section, copying skills, `sed`-templating launchd
plists, then a verification recipe. Only `namespaces init` is a command.
Each step can fail silently, and repeating any of them by hand risks
duplicate hooks or jobs.

Inspecting the current tree shows defects that a guided installer has to
fix or work around:

- **Env file format is inconsistent.** `INSTALL.md` writes `KEY=VALUE`.
  `DEPLOY.md` "Laptop Side Configuration" writes `export KEY="VALUE"`.
  `config.LoadFromFile` splits on the first `=`, so the DEPLOY.md form
  produces the key `export MEMORY_PG_DSN` with a quoted value. The DSN
  stays unset, and every subcommand except `namespaces` fails with
  `MEMORY_PG_DSN is required`.
- **Every subcommand except `namespaces` needs a DSN.** `run()` calls
  `config.LoadFromFile` and `config.Load()` before dispatch. A 0644 env
  file, or no env file at all, makes the binary exit 1 before any
  subcommand runs. Neither `install` nor `doctor` could run in that state.
- **The hook never migrates** (backlog 7). After a new binary is installed,
  something must apply migrations before the hook relies on the schema.
- **Creating the extension needs a superuser.** `0001_init.sql` runs
  `CREATE EXTENSION IF NOT EXISTS vector`, and the app role is not a
  superuser. The Docker server stack creates the extension in
  `deploy/initdb/01-app-role.sh`. A fresh local Postgres has no equivalent
  step, so `postgres.New` fails with `permission denied to create
  extension`.
- **Scheduled jobs have no PATH.** launchd runs with
  `/usr/bin:/bin:/usr/sbin:/sbin`. `ingest-pr` executes `az` and `git`, and
  session extraction executes `claude`, which are usually in
  `/opt/homebrew/bin` or `~/.local/bin`. `run-with-env.sh` sources the env
  file, which is redundant because the binary loads it itself. It also
  makes bash expand `$` inside a password.
- **The binary has no version** (no ldflags, no `version` subcommand), so
  neither an upgrade nor doctor can say what is installed.
- **Assets exist only in a repo checkout.** `integration/` and `deploy/` are
  not in the binary. A copied or released binary cannot install hooks or
  skills.

This feature adds four subcommands:

- **`claude-memory install`**: an interactive wizard (or non-interactive
  with flags) that detects the platform, asks for a topology, and runs an
  ordered list of idempotent steps. Before applying anything it shows each
  step's state, and every step can be skipped.
- **`claude-memory doctor`**: a read-only health check with remediation
  hints, exit codes and `--json`.
- **`claude-memory uninstall`**: reverses exactly what `install` recorded.
- **`claude-memory migrate`**: applies the schema explicitly. It is needed
  because the hook skips migrations.

## 2. Glossary

| Term | Definition |
|---|---|
| Step | One unit of installation (e.g. `hooks.settings`) with four phases: **Detect → Plan → Apply → Verify**. |
| Step state | The result of Detect: `absent`, `ok`, `outdated` (installed by us, unmodified, embedded version differs), `modified` (installed by us, then edited by the user), `foreign` (present but not recorded by us, e.g. a hand install), `blocked` (a prerequisite is missing), `unsupported` (not available on this platform). |
| Choice | What the user picks per step: `install`, `keep`, `repair` (restore our version), `reconfigure` (ask the questions again), `skip`. |
| Topology | Where Postgres and Ollama run (§6.5): **A** all local, **B** remote Postgres + local Ollama, **C** Postgres in Docker (local, or a bundle for a server). |
| Manifest | `~/.config/claude-memory/install.json`. It records every artifact `install` created: kind, path or identity, sha256 as written, binary version and time. |
| Embedded assets | Hook scripts, skills, the CLAUDE.md section, job templates and the compose bundle, compiled into the binary with `go:embed`. |
| Managed block | Text between `<!-- BEGIN claude-memory -->` / `<!-- END claude-memory -->` (Markdown) or `# BEGIN claude-memory` / `# END claude-memory` (crontab). |
| Our hook entry | A `settings.json` hook whose `command` matches `…/claude-memory/user-prompt-submit.sh` or `…/claude-memory/session-end.sh`, or contains `claude-memory hook` / `claude-memory extract`. |
| Check | One doctor probe with an id, a status (`pass`/`fail`/`warn`/`info`/`skip`), detail and a remedy. |

## 3. User Scenarios

### Scenario: First install on a Mac, remote Postgres (topology B, today's setup)
The owner downloads or builds the binary and runs `./claude-memory install`.
The wizard prints `macOS 15 arm64 · launchd · brew found`. It finds that
Ollama is running and that `claude` and `git` are on PATH, then asks for a
topology. The owner picks **B**. The wizard asks for host, port, database, user, and then a password
with no echo. It connects, finds `vector` installed, and runs migrations.
It pulls `bge-m3` after a y/N prompt. It asks for project mappings for
`namespaces.yaml`, and suggests the parents of recent `~/.claude/projects`
directories. Next it shows a plan table with the settings.json diff
(`+2 hook entries, 0 removed, backup → settings.json.bak.claude-memory.20261001T101500Z`)
and the jobs it will load. After **Apply** it runs `doctor` and prints
`14 pass · 1 warn (ollama.latency: first embed 1.8s, model was cold)`.

### Scenario: Re-run after a manual change
Months later the owner runs `claude-memory install` again. The status table
shows every step `ok` except `skills.remember: modified (you edited
SKILL.md)` and `jobs.ingest-pr: absent`. The defaults are
keep/keep/…/**keep** for the modified skill (the diff is shown on request)
and **install** for the missing job. No hook is duplicated and
`settings.json` is untouched (no backup, no rewrite).

### Scenario: Upgrade after a new binary
`make install` (or a new download) followed by `claude-memory install
--upgrade`. The command applies migrations. It refreshes every asset that
is `outdated` and unmodified, and changes nothing that is `modified`; those
are listed with the instruction `install --only skills` to resolve them. It
re-registers MCP only if the binary path changed, reloads jobs whose
rendered unit changed, then runs doctor. It asks no questions.

### Scenario: Fresh Linux box, everything local (topology A)
Ubuntu 24.04, no Postgres. Detect reports `postgres: absent`, `pgvector:
absent`, `ollama: absent`. The wizard prints the package commands
(`sudo apt install postgresql-16 postgresql-16-pgvector` via PGDG, the
Ollama installer URL) and offers "re-check / skip / quit". It never runs
them. After the owner installs them and picks re-check, the database
bootstrap step asks for an admin connection (default: local socket
`postgresql:///postgres`, the hint is `sudo -u postgres` for peer auth). It
can either create the role, database and extension with a generated
password, or print the SQL. Jobs use systemd user timers, with a hint
about `loginctl enable-linger`.

### Scenario: Postgres in Docker on a server (topology C, server)
The owner picks **C → server**. The wizard asks for the server's Tailscale
IP. It writes a bundle directory `./claude-memory-server/` containing
`docker-compose.yml`, `pg_hba.conf`, `initdb/`, a `.env` (mode 0600) with
generated `POSTGRES_PASSWORD`/`APP_DB_PASSWORD`, `backup.sh`, the systemd
backup units and `SERVER-STEPS.txt`. That file holds the exact `scp -r`,
`ssh`, `sudo` sysctl/ufw/mkdir/chown, `docker compose up -d` and
backup-timer commands from DEPLOY.md. The wizard prints them and waits at
"press Enter when the server is up (or s to skip)". It then builds the DSN
from the bind IP and the generated app password and continues as in
topology B. It never runs `ssh` or `scp`.

### Scenario: Postgres in Docker on this machine (topology C, local)
The wizard checks that `docker compose version` works. It writes the
local-profile bundle (loopback-published port, named volume, §6.5) to
`~/.local/share/claude-memory/docker/`, shows the `docker compose … up -d`
command and runs it after confirmation. It then waits for the container
to be healthy (up to 60 s) and continues.

### Scenario: Health check in CI or a script
`claude-memory doctor --json | jq '.summary'`. The command exits 1 when a
check fails, 0 otherwise. It never prompts or writes, and redacts the
password from all output.

### Scenario: Uninstall
`claude-memory uninstall` lists what the manifest recorded: hook entries,
scripts, skills, MCP registration, jobs and the CLAUDE.md block. The user
confirms once. Each item is reversed. Files the user modified are listed
separately with default **keep**. The env file, `namespaces.yaml` and all
database data are kept unless `--purge-config` / `--purge-data` is given.

## 4. Assumptions & Constraints
- Single user, user-level install. The installer never invokes `sudo`, and
  it never installs system packages, including via `brew`, which needs no
  sudo. For packages it only prints commands, then re-detects (§13 #3).
- Supported: **macOS** (arm64, amd64) and **Linux** (amd64, arm64; WSL2 is
  Linux). **Windows, BSDs and others are out of scope.** `install` exits 2
  with a message pointing at `integration/INSTALL.md`. `doctor` still runs
  its platform-independent checks.
- Claude Code reads hooks from `~/.claude/settings.json` (or
  `$CLAUDE_CONFIG_DIR/settings.json` when that variable is set) and runs a
  hook `command` through a shell, so `$HOME` in a command expands. The
  `claude mcp add|get|remove --scope user` CLI is the supported way to
  register MCP servers. `~/.claude.json` belongs to Claude Code and is
  never edited directly. Plan WI-0 verifies these against the installed
  `claude --version`.
- Layering stays as it is. `internal/setup` holds the step engine and every
  step and depends only on ports that it declares itself. Concrete
  adapters (os, exec, pgx, http, terminal) are constructed only in
  `cmd/claude-memory/main.go`. `internal/setup` never imports `os/exec`,
  `net/http` or pgx.
- No new heavy dependency. The prompts are line-oriented, using stdlib
  `bufio` and `fmt`. The one addition is **`golang.org/x/term`**, for
  `ReadPassword` (no echo) and `IsTerminal`. It is maintained by the Go
  team, and its only dependency `golang.org/x/sys` is already in the
  module graph. A TUI framework (bubbletea, survey, promptui) was
  rejected for three reasons. It adds a large dependency tree for about
  20 prompts. Its raw-mode rendering breaks over dumb terminals, `ssh -T`,
  CI logs and piped stdin. And line prompts are trivially fakeable in
  tests (§10 `Prompter`).
- The server-side automation is limited to generating files and printing
  commands. The wizard never opens a remote shell.

## 5. Cross-Module Interactions

```
cmd/claude-memory/main.go  (composition root; dispatches install/doctor/uninstall/migrate/version
        │                   BEFORE config.LoadFromFile/config.Load)
        ├─ adapters: osFS, execRunner(readOnly|mutating), ttyPrompter(x/term), systemClock,
        │            platformDetector, pgProber(internal/postgres), ollamaProber(internal/ollama),
        │            claudeCLI, launchd|systemd|cron JobManager, namespace store
        ▼
internal/setup  ── Engine: for each Step: Detect → (status table) → Choice → Plan → Apply → Verify
        │           Manifest (install.json) · Redactor · settings.json merger · env-file editor
        ├── assets ◄── integration (package integration: //go:embed hooks skills launchd systemd ...)
        │         ◄── deploy      (package deploy: //go:embed docker-compose.yml pg_hba.conf initdb .env.example ...)
        └── doctor checks (same ports, read-only runner) ─► report (text | --json)

internal/config   + ParseEnvFile(path) (pure parse + perm report; LoadFromFile uses it)
internal/postgres + Probe (read-only: ping, extension, schema objects) · Bootstrap SQL (shared with initdb)
internal/ollama   + Prober (version, tags, pull, embed dims)
internal/namespace  (Init/Add/Load reused as-is)
```

## 6. Functional Requirements

### 6.1 Command surface and dispatch
- AC-1 (Ubiquitous): `main.run` shall dispatch `install`, `uninstall`,
  `doctor`, `migrate` and `version` before `config.LoadFromFile` and
  `config.Load`, as it already does for `namespaces`. They shall work
  with no env file, a 0644 env file, or no `MEMORY_PG_DSN`. Verify: a
  `cmd` test with `HOME` set to an empty temp dir runs `doctor --json`
  and `install --dry-run --yes --topology remote` without the error
  `MEMORY_PG_DSN is required`.
- AC-2 (Ubiquitous): `claude-memory version` shall print the module
  version, VCS revision and dirty flag, read from `-ldflags -X
  main.version` when set and otherwise from `debug.ReadBuildInfo`. `make
  build`/`install`/`cross-build` shall set the ldflags from `git describe
  --tags --always --dirty`. Verify: unit test of the formatter; `make
  build && bin/claude-memory version` shows a revision.
- AC-3 (Ubiquitous): `claude-memory migrate` shall load the config like
  the other subcommands, open the store with `postgres.New` (which applies
  the idempotent migrations), print `schema up to date (migrations:
  0001,0002)` and exit 0. On failure it exits 1 with a redacted error.
  Verify: integration test against a fresh database; a second run is a
  no-op.
- AC-4 (Ubiquitous): `install` flags shall be `--yes`, `--dry-run`,
  `--upgrade`, `--topology local|remote|docker-local|docker-server`,
  `--only STEP[,STEP]`, `--skip STEP[,STEP]`, `--bin-dir DIR` (default
  `~/.local/bin`), `--ollama-url URL`, `--pg-dsn DSN` (rejected if it
  contains a password), `--pg-password-stdin`, `--claude-md PATH`,
  `--namespace NAME=GLOB` (repeatable), `--pr-repos PATHS`, `--no-jobs`,
  `--seed-file PATH`, `--no-doctor`. `uninstall`: `--yes`, `--dry-run`, `--purge-config`,
  `--purge-data`. `doctor`: `--json`, `--strict`, `--latency[=N]`
  (default N=20), `--timeout D`. Unknown flags exit 2. Verify: flag
  parsing table test.

### 6.2 Engine (internal/setup)
- AC-5 (Ubiquitous): Every step shall implement `Detect(ctx) (State,
  Detail)`, `Plan(ctx, Choice) ([]Action, error)`, `Apply(ctx, []Action)
  error` and `Verify(ctx) error`. The engine shall run Detect for all
  steps first, print a status table (step, state, default choice, one
  line of detail), collect choices, show the combined plan, ask for one
  confirmation, then Apply and Verify each step in order. Verify: an
  engine test with fake steps asserts call order and that no Apply
  happens before confirmation.
- AC-6 (Ubiquitous): Default choices shall be: `absent → install`, `ok →
  keep`, `outdated → repair`, `modified → keep`, `foreign → keep`
  (adopting it into the manifest when it is byte-identical to ours),
  `blocked`/`unsupported → skip`. Verify: table test.
- AC-7 (Ubiquitous): The steps are ordered, and a step whose prerequisite
  step was skipped or failed shall be `blocked` with that reason, not
  attempted. The order is: `platform`, `binary`, `prereqs`, `topology`,
  `database` (provision/bootstrap), `envfile`, `migrate`, `ollama`,
  `namespaces`, `hooks.scripts`, `hooks.settings`, `mcp`, `skills`,
  `claude-md`, `jobs`, `seed` (optional), `doctor`. Verify: a failing
  `database` blocks `migrate` and leaves `hooks.settings` unaffected.
- AC-8 (Unwanted): IF a step's Apply fails, THEN the engine shall stop
  that step, record nothing in the manifest for it, continue with the
  independent steps, and exit 1 with a summary listing each failed step
  and its remedy. Verify: fake step returns an error; the summary and
  exit code are asserted.
- AC-9 (Ubiquitous): Every file write shall be atomic: a temp file in the
  same directory, fsync, rename, keeping the existing mode or using the
  step's mode. The manifest shall be rewritten after each completed step,
  so an interrupted run (Ctrl-C → exit 130) leaves every artifact either
  old or new, with the manifest matching. A lock file
  (`~/.config/claude-memory/install.lock`, `flock`) shall make a second
  concurrent `install`/`uninstall` exit 2. Verify: the fault-injecting FS
  fails the rename and the original is intact; the second process
  fails to acquire the lock.

### 6.3 Interaction modes
- AC-10 (Ubiquitous): Prompts shall be line-oriented. A selection shows
  numbered options with a default (Enter accepts it). A confirmation is
  `[y/N]` or `[Y/n]`. Text prompts show their default. Invalid input
  re-asks, up to 3 times, then aborts that step as skipped. Verify:
  `Prompter` fake transcripts.
- AC-11 (State-driven): WHILE stdin is not a TTY and `--yes` is not given,
  `install`/`uninstall` shall exit 2 before Detect with `non-interactive
  session: pass --yes and the needed flags`, and shall never block
  reading stdin. Verify: test with a non-TTY prompter.
- AC-12 (Ubiquitous): With `--yes`, every choice takes its §6.2 default
  and every question takes its flag, env or detected value. A required
  value that is missing (e.g. the DSN for topology B) shall fail that
  step with the flag name needed. It shall never fall back to prompting.
  `--yes` never overwrites `modified` artifacts. Verify: non-interactive
  run on a temp HOME with fakes.
- AC-13 (Ubiquitous): `--dry-run` shall run Detect and Plan and print the
  plan, including unified diffs for every text file that would change
  (secrets redacted). It performs no mutating action. This is enforced
  structurally: the engine receives a read-only `Runner` and an FS whose
  write methods return `ErrDryRun`. Verify: a dry-run test asserts zero FS
  writes and zero mutating commands, and that the output matches a
  golden file.
- AC-14 (Ubiquitous): `install` output shall use plain text, with color
  only when stdout is a TTY and `NO_COLOR` is unset. Each Apply prints
  `✓ step — detail` / `✗ step — error · fix: …`. Verify: golden output
  with color off.

### 6.4 Platform detection
- AC-15 (Ubiquitous): The `platform` step shall report the OS and arch
  (`runtime.GOOS/GOARCH`), the OS version (`sw_vers -productVersion` /
  `/etc/os-release`), WSL (`/proc/sys/kernel/osrelease` contains
  `microsoft`), the job backend, and the package managers present (`brew`,
  `apt-get`, `dnf`, `pacman`). Verify: the detector is tested with a fake
  FS and Runner for macOS, Ubuntu, Fedora, WSL-without-systemd, and
  Windows.
- AC-16 (Ubiquitous): The job backend shall be launchd on darwin. On
  linux it is systemd user units when `systemctl --user show-environment`
  exits 0, else cron when `crontab` is on PATH, else `none`, in which
  case `jobs` is `unsupported` and the commands are printed. Verify:
  detector table test.
- AC-17 (Unwanted): IF the OS is not darwin or linux, THEN `install`
  shall exit 2 with `unsupported OS <goos>: see integration/INSTALL.md
  for manual steps` before any prompt. Verify: detector fake returns
  `windows`.
- AC-18 (Ubiquitous): Package hints shall come from a table keyed by
  (package manager, component), covering Postgres 16 + pgvector, Ollama,
  git, the `claude` CLI, docker, `az` and `libpq`/`psql`. They are only
  printed, never executed. An interactive `blocked` step offers
  `re-check / skip / quit`. Verify: no `Runner` call with argv[0] in
  {`sudo`,`brew`,`apt-get`,`dnf`,`pacman`,`sh`,`curl`} in any test of
  any step (guard test over the fake's call log).

### 6.5 Topologies and the database
- AC-19 (Ubiquitous): The `topology` step shall offer A `local`
  (Postgres + pgvector and Ollama on this machine), B `remote` (Postgres
  elsewhere, e.g. over Tailscale; Ollama local or a URL), C1
  `docker-local` and C2 `docker-server`. The default comes from
  detection: an existing env DSN → its host's class; `127.0.0.1:5432`
  accepting TCP → A; else `docker` available → C1; else B. The choice is
  stored in the manifest, and on a re-run it defaults to `keep`. Verify:
  detection table test.
- AC-20 (Ubiquitous): For B, the wizard shall ask for host, port (5432),
  database (`claude_memory`), user (`claude_memory`) and the password
  (no echo). It builds the DSN with `net/url` (`url.UserPassword`), so
  characters such as `@ : / ? #` in the password are encoded. It then
  probes the connection with a 5 s timeout. A failure is classified as
  `timeout/unreachable` (hint: `tailscale status`, firewall, DEPLOY.md),
  `auth failed` (re-ask password, up to 3 times), `database missing`, or
  `pg_hba reject`. Verify: DSN builder table test incl. special
  characters; classifier test on pgx error fixtures.
- AC-21 (Ubiquitous): For A, the `database` step shall detect whether
  the app DSN connects and whether `vector` is installed in the target
  database. If either is missing, it offers (1) bootstrap through an
  admin connection, or (2) print the bootstrap SQL to run manually
  (`sudo -u postgres psql` on Linux, plain `psql postgres` for brew). The
  admin DSN defaults to `postgresql:///postgres`, is held only in memory
  and is never written or logged. The bootstrap SQL is the same text as
  `deploy/initdb/01-app-role.sh` (role with a generated password, owned
  database, `REVOKE ALL … FROM PUBLIC`, `CREATE EXTENSION vector`,
  schema grants), kept in one embedded file used by both. It is
  idempotent (`IF NOT EXISTS` / `DO` blocks), so a re-run does not fail.
  Verify: integration test (`MEMORY_TEST_PG_ADMIN_DSN`) bootstraps a
  throwaway database twice; afterwards the app role can run
  `postgres.New`.
- AC-22 (Ubiquitous): Generated passwords shall be 32 bytes from
  `crypto/rand`, base64url-encoded with no padding (URL- and shell-safe).
  Verify: unit test of alphabet and length.
- AC-23 (Ubiquitous): For C1 (`docker-local`), the wizard shall write a
  local-profile bundle to `~/.local/share/claude-memory/docker/` with
  these properties:
  - image `pgvector/pgvector:pg16`, compose project `claude-memory-local`;
  - port published as `127.0.0.1:${PORT}:5432`, default PORT 55432 so it
    does not collide with a native Postgres;
  - a named volume `claude-memory-pgdata` (no host path, no chown/sudo);
  - the same `initdb` script;
  - a `pg_hba.conf` that allows only `claude_memory` on database
    `claude_memory` via scram-sha-256 and rejects everything else (the
    port is reachable only on loopback);
  - `.env` at 0600 with generated passwords.

  The wizard requires `docker compose version` to succeed. After a
  confirmation it shows and runs exactly `docker compose -p
  claude-memory-local -f <dir>/docker-compose.yml up -d`, then polls the
  container health for up to 60 s. Host networking is not used, because
  Docker Desktop on macOS does not support it reliably and no tailnet
  source address has to be preserved locally. Verify: golden bundle
  files; a Runner fake asserts the exact argv; with Docker unavailable
  the step is `blocked`.
- AC-24 (Ubiquitous): For C2 (`docker-server`), the wizard shall render
  the server bundle into a chosen directory (default
  `./claude-memory-server`, refusing a non-empty directory unless it is
  our bundle). The bundle contains:
  - `deploy/docker-compose.yml`, `pg_hba.conf`, `initdb/`, `backup.sh`
    and `systemd/*`, byte-identical to the embedded `deploy/` copies;
  - a `.env` at 0600 with `POSTGRES_BIND_IP` (asked) and generated
    passwords;
  - `SERVER-STEPS.txt`, rendered from the DEPLOY.md server prerequisites
    and installation commands, with the bundle path and IP substituted.

  The wizard shall print those steps and shall not run `ssh`, `scp`,
  `rsync` or any `docker` command for C2. It then continues as B with
  the DSN prefilled (host = bind IP, app password from the bundle).
  Verify: golden bundle; the call-log guard asserts no
  `ssh`/`scp`/`rsync`/`docker` argv.
- AC-25 (Ubiquitous): The `migrate` step shall call the same code path as
  `claude-memory migrate` (AC-3), so the schema exists before the hook,
  which skips migrations (backlog 7), is wired. `install --upgrade`
  always runs it. Verify: engine test asserts that `migrate` precedes
  `hooks.settings`; integration test.
- AC-26 (Unwanted): IF the user reconfigures the topology or DSN on a
  re-run, THEN the wizard shall warn that existing records stay in the
  old database. It prints the `pg_dump`/`pg_restore` hint from DEPLOY.md
  and does not migrate data. Verify: prompter transcript shows the
  warning before the new DSN is written.

### 6.6 Env file and secrets
- AC-27 (Ubiquitous): `config.ParseEnvFile(path)` shall return the
  parsed `KEY=VALUE` pairs, the file mode, and format findings
  (`export ` prefix, surrounding quotes, duplicate keys, lines without
  `=`). It does not touch the process environment. `LoadFromFile` shall
  be reimplemented on top of it with unchanged behavior. Verify: existing
  config tests stay green; new table tests cover the findings.
- AC-28 (Ubiquitous): The `envfile` step shall write
  `~/.config/claude-memory/env` (dir 0700, file 0600) in plain
  `KEY=VALUE` format with no quotes and no `export`. It sets the managed
  keys (`MEMORY_PG_DSN`, `MEMORY_OLLAMA_URL`, `MEMORY_OLLAMA_MODEL`,
  `MEMORY_EMBED_MAX_TOKENS`, and `MEMORY_PR_INGEST_REPOS` when given) in
  place. Every other line, comment and unknown key is preserved in its
  order. A file in DEPLOY.md's `export KEY="…"` form is detected
  (`modified`) and, with consent, rewritten to the plain form. Verify:
  golden tests (fresh, update-in-place, unknown keys preserved, `export`
  form converted, idempotent second run = no write).
- AC-29 (Unwanted): IF the env file exists with group/world permission
  bits, THEN Detect shall report `modified: mode 0644, the binary refuses
  to load it`, and the default choice is `repair` (chmod 0600 without
  changing content). Verify: temp HOME test.
- AC-30 (Ubiquitous): The DB password shall travel only from the no-echo
  prompt, `--pg-password-stdin` or an existing env file, into the env
  file (and, for C, the bundle `.env`). It shall never appear in command
  argv, in stdout/stderr (including the dry-run diff), the manifest, a
  settings.json backup, `claude mcp` arguments, job units or slog
  output. Errors pass through a `Redactor` that replaces the password
  literal and any `user:…@` userinfo with `***`. Verify: a sentinel test
  runs every step and doctor with password `S3ntinel-pw-$@:/x` and
  greps all captured output, written files outside the env file and
  bundle `.env`, and the Runner argv for the sentinel and its
  percent-encoded form.
- AC-31 (Ubiquitous): `--pg-dsn` containing a password shall be rejected
  (exit 2: `pass the password via the prompt or --pg-password-stdin, not
  argv`). `MEMORY_PG_DSN` in the environment is accepted for `--yes`.
  Verify: flag test.

### 6.7 Ollama
- AC-32 (Ubiquitous): The `ollama` step shall probe `GET /api/version`
  and `GET /api/tags` at the configured URL (default
  `http://127.0.0.1:11434`, or `--ollama-url`, or one asked for). When
  the model (`bge-m3` unless `MEMORY_OLLAMA_MODEL` is set) is missing, it
  offers to pull it via `POST /api/pull` (streamed progress, so it also
  works against a remote Ollama). It then verifies with one `/api/embed`
  call that returns exactly 1024 dimensions, because the schema is
  `VECTOR(1024)`. Ollama absent → `blocked`, with the brew/Linux install
  hint (printed only). Verify: prober tests against `httptest.Server`;
  wrong dimension → step fails with `model returns N dims, schema needs
  1024`.
- AC-33 (Event-driven): WHEN the Ollama URL is not loopback, the wizard
  shall warn that prompts are sent over the network for embedding and
  that the latency budget assumes a local GPU (DEPLOY.md AC-44), and
  shall ask for confirmation. Verify: prompter transcript.

### 6.8 Claude Code integration
- AC-34 (Ubiquitous): Assets shall be embedded. Two new Go packages,
  `integration` (`integration/embed.go`) and `deploy` (`deploy/embed.go`),
  expose `embed.FS` values with the hook scripts, skills, the CLAUDE.md
  section, launchd and systemd templates, and the compose bundle,
  including the dotfile `.env.example`, which must be named explicitly.
  `install` uses only these and never reads the repo working tree.
  Verify: a test builds the binary, copies it to a temp dir outside the
  repo, and runs `install --dry-run --yes --topology remote` with
  `MEMORY_PG_DSN` set, so the plan lists all assets.
- AC-35 (Ubiquitous): The `binary` step shall install the running
  executable (`os.Executable`, symlinks resolved) to `--bin-dir` at mode
  0755 by atomic copy and rename, which is safe while the old binary
  runs. If the running binary already is the target, the step is `ok`.
  It warns when `--bin-dir` is not on PATH, and on darwin when the file
  carries `com.apple.quarantine` (hint: `xattr -d com.apple.quarantine
  <path>`). Templates (hook scripts, jobs, MCP) render this absolute path.
  Verify: temp HOME test; same-file case.
- AC-36 (Ubiquitous): The `hooks.scripts` step shall write
  `user-prompt-submit.sh` and `session-end.sh` to
  `~/.claude/hooks/claude-memory/` at mode 0755, with the binary path
  rendered as the default of `CLAUDE_MEMORY_BIN`. Their sha256 goes in
  the manifest. Verify: temp HOME; hash recorded.
- AC-37 (Ubiquitous): The `hooks.settings` step shall merge the two hook
  entries into `settings.json` under `hooks.UserPromptSubmit` and
  `hooks.SessionEnd`. Each entry is `{"hooks":[{"type":"command",
  "command":"<abs path to script>","timeout":5}]}`. The merge shall:
  - parse with an order-preserving JSON object model, so top-level and
    nested key order and all unknown keys and values survive (the output
    is re-indented with 2 spaces and a trailing newline);
  - leave every other hook entry and event untouched;
  - add an entry only when no "our hook entry" (§2) exists for that
    event; replace an existing entry of ours only when it differs from
    the desired one, and only after consent;
  - collapse duplicates of our entry to one, with consent;
  - create the file or the `hooks` key when absent;
  - write nothing when the result is byte-identical to the input.
  Verify: golden tests for each of these cases, run twice to prove
  idempotence.
- AC-38 (Unwanted): IF `settings.json` is not valid strict JSON (comments,
  trailing commas, truncated), or `hooks` / an event value has an
  unexpected type, THEN the step shall fail with `refusing to edit
  <path>: <parse error at line:col>; fix it or merge
  integration/settings.snippet.json by hand` and leave the file
  untouched. Verify: golden refusal cases; the file hash is unchanged.
- AC-39 (Ubiquitous): Before any write to `settings.json`, the step shall
  copy the original to `settings.json.bak.claude-memory.<UTC
  yyyymmddThhmmssZ>` with the same mode and keep the newest 5 such
  backups. Right before the rename it shall re-read the file and abort
  if its hash changed since Detect, in case Claude Code wrote it
  meanwhile. Verify: backup exists and matches the original; a
  concurrent-change fake makes the step fail with "changed during
  install, re-run".
- AC-40 (Ubiquitous): The `mcp` step shall detect `claude` on PATH. It
  reads the current registration with `claude mcp get claude-memory`;
  exit 0 plus a command line containing `<bin> serve` means `ok`, a
  different command means `outdated`. It registers with `claude mcp add
  --scope user claude-memory -- <bin> serve`, and for `outdated` runs
  `claude mcp remove --scope user claude-memory` first. It never passes
  `-e`, and never edits `~/.claude.json`. Without `claude`, the step is
  `blocked` and prints the command. Verify: Runner fake asserts argv for
  absent/ok/outdated; parser tolerant of output variations (fixtures).
- AC-41 (Ubiquitous): The `skills` step shall copy each embedded skill
  directory (`remember`, `memory-digest`) to `~/.claude/skills/<name>/`
  and record per-file sha256. On a re-run, an installed file whose hash
  equals the manifest hash and differs from the embedded one is
  `outdated` and refreshed. An installed file whose hash differs from
  the manifest is `modified`: keep by default, and offer `show diff /
  overwrite (backup .bak) / keep`. A same-named directory not in the
  manifest is `foreign`. Verify: temp HOME tests for each state.
- AC-42 (Ubiquitous): The `claude-md` step shall insert or refresh the
  memory section between `<!-- BEGIN claude-memory -->` and `<!-- END
  claude-memory -->` in the target file, leaving all text outside the
  markers unchanged. The target is `~/.claude/CLAUDE.md` by default, or
  `--claude-md PATH` (§13 #1). The section text lives in its own
  embedded file, `integration/claude-md-section.md`, worded
  location-neutrally. The step shows a diff and requires explicit
  consent. Under `--yes`, it writes only to the default user-level file,
  and only when `--claude-md` was not given for a path inside a git
  repository, so a shared committed file is never edited unattended.
  Unbalanced or duplicated markers → refuse. A pre-existing
  hand-pasted section (heading `## Shared semantic memory
  (\`claude-memory\`)` without markers) is reported as `foreign` with
  the hint to delete it before installing the block. Verify: golden
  tests (insert at end, refresh between markers, idempotent, unbalanced
  refused, foreign detected).

### 6.9 Scheduled jobs
- AC-43 (Ubiquitous): The `jobs` step shall install two jobs:
  `cleanup` (daily 07:15 local) and `ingest-pr` (daily 07:00 local).
  `ingest-pr` is installed only when `MEMORY_PR_INGEST_REPOS` is set or
  given; the prompt notes that only Azure DevOps is supported today
  (backlog 4). The jobs invoke the binary directly (no
  `run-with-env.sh`; the binary loads the env file itself) and set
  `PATH` explicitly to the directories that contain `claude`, `git` and
  `az` as found at install time, plus `/usr/bin:/bin`. Logs go to
  `~/.local/state/claude-memory/<job>.log`. Verify: golden unit files
  per backend.
- AC-44 (Ubiquitous): On launchd, plists shall be written to
  `~/Library/LaunchAgents/` with the existing labels
  `io.github.claude-memory.{cleanup,ingest-pr}`, so hand-installed jobs
  are recognized rather than duplicated. Plists are rendered with absolute
  paths and an `EnvironmentVariables` PATH (no `__HOME__` placeholders). Jobs are loaded with `launchctl
  bootout gui/<uid>/<label>` (ignore "not loaded") followed by `launchctl
  bootstrap gui/<uid> <plist>`, and detected with `launchctl print
  gui/<uid>/<label>`. Verify: Runner fake argv; golden plists; an
  existing hand-installed plist pointing at `run-with-env.sh` is
  `outdated` → repaired.
- AC-45 (Ubiquitous): On systemd, the step shall write
  `~/.config/systemd/user/claude-memory-{cleanup,ingest-pr}.{service,timer}`
  (`Type=oneshot`, `Environment=PATH=…`, `OnCalendar`,
  `Persistent=true`, `RandomizedDelaySec=10m`), then run `systemctl
  --user daemon-reload` and `systemctl --user enable --now <timer>`.
  Detection uses `systemctl --user is-enabled|is-active`. When
  `loginctl show-user $USER -p Linger` reports `no`, the step prints the
  `loginctl enable-linger` hint and does not run it. Verify: Runner fake
  argv; golden units.
- AC-46 (Ubiquitous): With cron, the step shall read `crontab -l`
  (treating "no crontab" as empty) and replace or append a managed block
  `# BEGIN claude-memory` … `# END claude-memory` containing one line per
  job with `PATH=` set. All other lines are preserved. It writes with
  `crontab -` only when the content changed. Verify: golden crontab
  tests (empty, foreign lines kept, block refreshed, idempotent).

### 6.10 Namespaces and seed
- AC-47 (Ubiquitous): The `namespaces` step shall call `namespace.Init`
  when the file is absent (and `namespace.Add` for added mappings),
  through a port. Interactively it offers to add `NAME=GLOB` mappings. As
  suggestions it shows up to 5 distinct parent directories derived from
  `~/.claude/projects/*` entries, decoded from Claude Code's
  path-encoding, without listing file contents. It finishes with
  `namespaces which` for each suggested path. An existing valid file is
  `ok`; an unparseable one is `modified`, reported with the parse error
  and never overwritten. Verify: temp HOME test; broken file untouched.
- AC-48 (Optional): The `seed` step shall be offered only when a seed
  file is given (`--seed-file`) or `./seed/facts.yaml` exists in the
  current directory (a repo checkout). It runs the `seed` code path,
  defaults to `skip`, and is never part of `--yes` unless
  `--seed-file` is given. Verify: step is `unsupported` without a file.

### 6.11 Re-runnability, manifest, upgrade, uninstall
- AC-49 (Ubiquitous): The manifest (`install.json`, 0600) shall record
  `schema: 1`, `binary_version`, `platform`, `topology`, `installed_at`,
  `updated_at`, and per artifact `{step, kind (file|dir|settings-hook|mcp|
  launchd|systemd|cron-block|md-block|env-key|docker-bundle|compose-project),
  path or identity, sha256 as written, version}`. It never stores
  secrets. Verify: schema test; sentinel test (AC-30).
- AC-50 (Ubiquitous): A re-run with nothing changed shall perform zero
  writes and zero mutating commands, and shall print a status table with
  every step `ok`. Verify: two consecutive `--yes` runs on a temp HOME
  with fakes; the second run's FS write count and mutating Runner count
  are 0.
- AC-51 (Ubiquitous): Without a manifest, Detect shall recognize a hand
  install done per `integration/INSTALL.md`: identical files and our hook
  entries become `foreign` and are adopted into the manifest after
  confirmation, while differing ones become `modified`. No duplicate
  hook entry or job is created. Verify: fixture HOME replicating
  INSTALL.md steps 2–8.
- AC-52 (Ubiquitous): `install --upgrade` shall be non-interactive. It
  runs `binary` (when invoked from a different path), `migrate`, then
  refreshes `outdated` artifacts and reloads changed jobs, re-checks MCP,
  and runs `doctor`. `modified` artifacts are listed and kept, with the
  exit code still 0 and a `warn` line. Verify: manifest with older hashes
  → refreshed; user-edited skill → kept and listed.
- AC-53 (Ubiquitous): `--only` / `--skip` shall restrict the steps the
  engine considers. Prerequisites are Detected but not applied, and a
  step whose prerequisite is not `ok` is `blocked`. Verify: `--only
  hooks.settings` touches only `settings.json`.
- AC-54 (Ubiquitous): `uninstall` shall reverse the manifest's artifacts
  in reverse order:
  - remove our hook entries (deleting an event key whose array becomes
    empty, and the `hooks` key only if the manifest records that install
    created it), with the same backup and refusal rules as AC-38/AC-39;
  - `claude mcp remove --scope user claude-memory`;
  - unload and delete jobs (launchctl bootout / systemctl disable --now
    + daemon-reload / remove the cron block);
  - remove the CLAUDE.md block (markers included);
  - delete hook scripts and skills whose hash still matches the
    manifest, listing any that differ (default keep);
  - remove the installed binary last.

  The env file, `namespaces.yaml`, `~/.local/state/claude-memory` and
  all DB data are kept. Verify: golden after-uninstall `settings.json`
  equals the pre-install golden for the "other hooks present" case;
  Runner argv.
- AC-55 (Ubiquitous): `uninstall --purge-config` shall additionally
  delete the env file, `namespaces.yaml`, the manifest and the state
  dir. `--purge-data` applies only to C1. It runs `docker compose -p
  claude-memory-local down -v` after the user types the word `delete`;
  `--yes` alone is not enough. It shall never touch B or C2 databases;
  instead it prints that data on the server is kept. Verify: prompter
  transcript; `--yes --purge-data` without typed confirmation exits 2.
- AC-56 (Ubiquitous): `uninstall` shall remove nothing that is not in the
  manifest. With no manifest, it lists what it detects and what to
  remove by hand, and exits 0. Verify: temp HOME with foreign artifacts
  only; FS write count 0.

### 6.12 `doctor`
- AC-57 (Ubiquitous): `doctor` shall be read-only. It never prompts,
  never applies migrations (it uses `postgres.Open`), never pulls models
  and never writes files. It runs with a read-only Runner and an FS
  without write access. Verify: a doctor test asserts zero writes and zero
  mutating commands across all checks.
- AC-58 (Ubiquitous): `doctor` shall run these checks, each with its
  status on failure and a one-line remedy. The remedy is usually
  `claude-memory install --only <id>` or a command.

  | id | checks | failure status |
  |---|---|---|
  | `binary.version` | version, path, `--bin-dir` on PATH; another `claude-memory` earlier on PATH with a different version | info / warn |
  | `env.file` | exists | fail |
  | `env.perms` | mode has no group/world bits | fail |
  | `env.format` | `export`/quotes/duplicates (AC-27); required keys parse | fail if DSN unusable, else warn |
  | `pg.connect` | DSN parses, connect + ping ≤ 3 s, classified error (AC-20) | fail |
  | `pg.latency` | ping RTT | warn > 100 ms |
  | `pg.vector` | extension installed (+ version) | fail |
  | `pg.schema` | `records` exists, columns of every embedded migration present (0002 `namespace`) | fail if missing; warn if behind → `claude-memory migrate` |
  | `ollama.reachable` | `/api/version` | fail |
  | `ollama.model` | model in `/api/tags` | fail |
  | `ollama.embed` | one embed, 1024 dims, latency | fail on dims; warn > 500 ms |
  | `tools.git` | `git` on PATH | warn (staleness off) |
  | `tools.claude` | `claude` on PATH | warn (no session extraction / MCP CLI) |
  | `tools.az` | `az` on PATH when PR repos configured | warn |
  | `mcp.registered` | `claude mcp get` lists `<bin> serve` | fail if absent; warn if path differs |
  | `hooks.scripts` | present, executable, hash vs manifest | fail missing; warn modified |
  | `hooks.settings` | valid JSON; exactly one of our entries per event; command path exists | fail; warn duplicates |
  | `skills` | present; hash vs manifest | warn |
  | `claude-md` | managed block present in the recorded target | info |
  | `namespaces` | file parses; resolution + provenance for cwd | warn on parse error; info |
  | `jobs` | loaded/enabled per backend; last exit status when the backend reports it | warn |
  | `dirs.state` | `~/.local/state/claude-memory` writable (checked with `access(2)`, no write) | warn |
  | `manifest` | present; artifacts it lists exist | info / warn |
  | `hook.latency` | only with `--latency[=N]` (AC-61) | warn if p95 > 300 ms |

  Checks that depend on a failed check are `skip` with `because <id>
  failed`. Verify: one test per check id with fakes, covering pass and
  each failure branch.
- AC-59 (Ubiquitous): Exit codes: 0 when no check is `fail` (and, with
  `--strict`, no `warn`), 1 otherwise, 2 for usage errors. Each check is
  bounded by `--timeout` (default 3 s). Without `--latency`, the whole
  run finishes within 10 s when every endpoint hangs. Verify: fake
  hanging prober; wall-clock bound asserted with a fake clock and
  context deadlines.
- AC-60 (Ubiquitous): `doctor --json` shall print one JSON object:
  `{"schema":1,"version":"…","platform":{"os","arch","jobs"},"ok":bool,
  "summary":{"pass":n,"fail":n,"warn":n,"info":n,"skip":n},
  "checks":[{"id","title","status","detail","remedy","duration_ms"}]}`,
  with checks in the table order and no ANSI codes. The DSN appears
  only redacted. Verify: golden JSON with durations zeroed; sentinel
  test.
- AC-61 (Optional): `doctor --latency[=N]` shall run the installed
  `user-prompt-submit.sh` N times (default 20, max 200) with synthetic
  prompts from a fixed list. It uses `cwd` = a temp dir and
  `session_id` = `doctor.probe`. The `.` fails the hook's session-id
  pattern, so no stale-cache file is written. Once usage events
  (staleness-metrics PR B) exist, the hook shall not emit events for
  this session id. The check reports p50/p95/max wall time of the whole
  wrapper against the 300 ms AC-30 budget. Run 1 is reported separately
  as `cold`. Verify: Runner fake with scripted durations → percentile
  math; manual run on the real setup recorded in the plan.
- AC-62 (Ubiquitous): `install` shall run doctor's check registry
  in-process as its final step (unless `--no-doctor`) and print its
  summary. If doctor reports a fail, `install` exits 1. Verify: engine
  test.

### 6.13 Verification infrastructure and documentation
- AC-63 (Ubiquitous): `internal/setup` tests shall use only fakes or a
  temp `HOME` (`t.Setenv("HOME", t.TempDir())`). They must never call
  real `brew`, `apt`, `launchctl`, `systemctl`, `crontab`, `docker`,
  `claude` or `ollama`. This is enforced in two ways. A test asserts that
  `internal/setup` does not import `os/exec`, `net/http` or pgx (via
  `go list -deps`), and the fake Runner fails the test on any unscripted
  argv. Verify: CI unit job.
- AC-64 (Ubiquitous): JSON, Markdown, crontab, env-file and unit-file
  merges shall have golden tests under `internal/setup/testdata/` with an
  `-update` flag. Every golden case also runs twice to assert
  idempotence. Verify: CI unit job.
- AC-65 (Ubiquitous): Integration tests (tag `integration`) shall cover
  bootstrap (AC-21), migrate (AC-3), the `pg.*` doctor checks, and an
  end-to-end `install --yes --topology local` against the test Postgres
  (`MEMORY_TEST_PG_ADMIN_DSN` or testcontainers), with an `httptest`
  Ollama and fake Claude/jobs adapters. CI's integration job shall add
  `./internal/setup/...` and `./cmd/claude-memory/...`. Verify: green CI.
- AC-66 (Ubiquitous): Documentation changes:
  - `integration/INSTALL.md` starts with `claude-memory install` /
    `doctor` / `uninstall`, and the manual steps move under "Manual
    install (reference)";
  - `DEPLOY.md`'s laptop env snippet is fixed to plain `KEY=VALUE`, with
    the server half pointing to `install --topology docker-server`;
  - `integration/bin/run-with-env.sh` is marked legacy, kept only so that
    old plists keep working until `install --upgrade` replaces them;
  - `docs/specs/README.md` status is updated.

  Verify: review.

## 7. Non-Functional Requirements
- **Safety**: No sudo. No package installs. No ssh. No edits to
  `~/.claude.json`. Every edit to a user-owned file outside our own
  directories (`settings.json`, `CLAUDE.md`, crontab, env file) is shown
  as a diff, needs consent, makes a backup or is atomic, and is
  idempotent.
- **Secrets**: AC-30. The env file and bundle `.env` are 0600 inside a
  0700 directory. The admin DSN is held in memory only.
- **Performance**: an all-`ok` re-run finishes in < 3 s, excluding
  network timeouts. `doctor` without `--latency` takes < 2 s on a
  healthy setup. Nothing here touches the hook hot path. The only change
  to it is the optional `doctor.probe` exclusion, and that comes later.
- **Portability**: darwin/linux, amd64/arm64. Builds via `make
  cross-build` (extended with darwin/amd64 and linux/arm64).
- **Dependencies**: `golang.org/x/term` only (§4).

## 8. Edge Cases
- `settings.json` is a symlink (dotfiles repo): edit the target, keep the
  link, back up next to the target. If the target is not writable,
  refuse.
- `settings.json` holds our entry under a `matcher` group or with a
  different `timeout` → treated as our entry; differs → `outdated`,
  shown as a diff.
- `$CLAUDE_CONFIG_DIR` is set → all `~/.claude` paths follow it. Doctor
  prints the effective directory.
- `HOME` is unset or relative → exit 2 before Detect.
- The password contains `$`, `@`, `:`, `/`, `#` or spaces → URL-encoded in
  the DSN, and the env file is never shell-sourced by our jobs (AC-43).
  It contains a newline → rejected at the prompt.
- Port 55432 is busy for C1 → Detect finds it (TCP dial) and asks for
  another port.
- Docker is installed but the daemon is down, or the user is not in the
  `docker` group → C1 `blocked` with a hint. sudo is never used.
- Running `install` from inside the repo with an older embedded asset
  than the manifest's version (downgrade) → assets `outdated` the other
  way. The wizard warns `binary is older than installed assets` and
  defaults to `keep`.
- launchd plist present but not loaded (e.g. after a manual `launchctl
  bootout`) → `outdated` → `repair` loads it.
- systemd user instance missing (containers, some WSL) → cron fallback
  (AC-16).
- The manifest is corrupt → treat it as absent (detection still works,
  AC-51), back it up as `install.json.corrupt.<ts>`, warn.
- `claude mcp get` output format changes → parse loosely. If unsure, say
  `outdated?`, offer re-registration and default to `keep`.
- Ctrl-C during a prompt → no step applied yet, exit 130. During Apply →
  AC-9.
- The CLAUDE.md target is inside a git repo → note that the file is
  probably shared or committed. `--yes` never writes it (AC-42).
- An `ollama pull` is interrupted → re-run resumes (Ollama's own
  behavior). The step is `absent` until the model is listed.
- The DB is reachable but the schema was created by a newer binary
  (unknown columns) → `pg.schema` `info`, not fail.

## 9. Data Model
No database change. New local files:
- `~/.config/claude-memory/install.json` (0600): manifest, AC-49.
- `~/.config/claude-memory/install.lock`: flock target.
- `~/.local/share/claude-memory/docker/` (C1 bundle, `.env` 0600).
- Backups `settings.json.bak.claude-memory.<ts>` (newest 5), `*.bak` for
  overwritten user-modified assets.

## 10. Interfaces
Ports declared in `internal/setup` (consumer-side). Adapters live in
`cmd/claude-memory` or in their own packages, and are constructed only in
`main.go`.

```go
type Prompter interface {
    Select(q string, opts []string, def int) (int, error)
    Confirm(q string, def bool) (bool, error)
    Text(q, def string, validate func(string) error) (string, error)
    Secret(q string) (string, error)          // no echo (x/term)
    Interactive() bool                         // stdin+stdout are TTYs
}
type FS interface {                            // paths are absolute
    ReadFile(p string) ([]byte, error); Stat(p string) (fs.FileInfo, error)
    Lstat(p string) (fs.FileInfo, error); ReadDir(p string) ([]fs.DirEntry, error)
    WriteFileAtomic(p string, b []byte, mode fs.FileMode) error
    MkdirAll(p string, mode fs.FileMode) error; Remove(p string) error
    Chmod(p string, mode fs.FileMode) error; Writable(p string) bool
}
type Runner interface {                        // never a shell; argv only
    Run(ctx context.Context, c Cmd) (Result, error) // Cmd{Argv, Dir, Env, Stdin, Mutating bool}
    LookPath(name string) (string, error)
}                                              // read-only runner rejects Mutating
type Clock interface{ Now() time.Time }
type Platform interface{ Detect(ctx context.Context) (PlatformInfo, error) }
type DBProber interface {
    Probe(ctx context.Context, dsn string) (DBStatus, error) // ping RTT, vector ext+ver, schema objects
    Bootstrap(ctx context.Context, adminDSN string, b BootstrapSpec) error
    Migrate(ctx context.Context, dsn string) error
}
type OllamaProber interface {
    Version(ctx context.Context, url string) (string, error)
    HasModel(ctx context.Context, url, model string) (bool, error)
    Pull(ctx context.Context, url, model string, progress func(done, total int64)) error
    EmbedDims(ctx context.Context, url, model string) (dims int, latency time.Duration, err error)
}
type JobManager interface {                    // launchd | systemd | cron | none
    Render(j JobSpec) (map[string][]byte, error)   // path → content
    Detect(ctx context.Context, j JobSpec) (State, string, error)
    Install(ctx context.Context, j JobSpec) error; Remove(ctx context.Context, j JobSpec) error
}
type ClaudeCLI interface {
    MCPGet(ctx context.Context, name string) (cmdline string, found bool, err error)
    MCPAdd(ctx context.Context, name string, argv []string) error
    MCPRemove(ctx context.Context, name string) error
}
type Namespaces interface {
    Load(path string) (*namespace.Config, error)
    Init(path, def string, rules []namespace.Rule) error
    Add(path, name string, globs ...string) error
}
type Step interface {
    ID() string; Title() string; Requires() []string
    Detect(ctx context.Context) (State, string)
    Plan(ctx context.Context, c Choice) ([]Action, error)
    Apply(ctx context.Context, a []Action) error
    Verify(ctx context.Context) error
}
```

CLI: §6.1 AC-4. Doctor JSON: AC-60. Manifest: AC-49.

## 11. Untrusted Inputs
- `settings.json`, `CLAUDE.md`, the crontab, the env file and the
  manifest are user-editable. They are parsed strictly, and the code
  refuses rather than guessing (AC-38, AC-42, AC-46, edge cases). Every
  path taken from them is validated to be under `$HOME` before it is
  written or deleted.
- Output of `claude mcp get`, `launchctl print`, `systemctl` and
  `crontab -l` is parsed loosely and only drives the state shown to the
  user. It is never executed or interpolated into argv.
- User-typed host, port, DB name, user and namespace globs go through
  `net/url` (DSN) and `namespace.ValidName` (names). They reach argv
  only as separate elements, never through a shell.
- `~/.claude/projects/*` directory names are only decoded into path
  suggestions and shown. They are not read further.

## 12. Out of Scope
- Windows; BSDs; system-wide (root) installs; installing system
  packages or running sudo; editing `pg_hba`/UFW/sysctl on a server;
  running anything over ssh.
- Publishing release binaries (§13 #2), self-update / downloading new
  versions.
- Migrating data between databases when the topology changes (only a
  printed hint).
- Configuring PR providers beyond today's Azure DevOps (backlog 4); a
  TUI; localization.
- Managing Ollama or Postgres services (start, stop, upgrade).

## 13. Open Questions for the Owner (with recommendations)

| # | Question | Recommendation | Impacted AC |
|---|---|---|---|
| 1 | Where should the CLAUDE.md section go by default: user-level `~/.claude/CLAUDE.md` (all projects) or the shared `acme/CLAUDE.md` as today? | **User-level by default.** Namespaces made the tool multi-project, and a user-level file is not shared or committed. Offer `--claude-md acme/CLAUDE.md` for teams. The section text becomes location-neutral. | AC-42 |
| 2 | Should the repo publish prebuilt binaries (tag-triggered GitHub release: darwin arm64/amd64, linux amd64/arm64) so `install` works without Go? | **Yes, as a small follow-up PR after this feature.** `go:embed` already makes a copied binary self-sufficient; until then `make install` + `claude-memory install` works. | AC-34, AC-35 |
| 3 | May the wizard run `brew install` / `brew services start ollama` itself after confirmation (no sudo needed)? | **No for v1, hints only.** One uniform rule ("we never install packages"), no long-running third-party installers inside the wizard, nothing to fake. Revisit if the owner finds re-check friction annoying. | AC-18 |
| 4 | Is the `golang.org/x/term` dependency acceptable (no-echo password, TTY detection)? | **Yes.** It is Go-team maintained, `x/sys` is already in the graph, and the alternative (`stty -echo` via exec) is fragile and untestable. | §4, AC-30 |
| 5 | Allow a remote Ollama URL (embeddings over the tailnet) at all? | **Allow it with a warning and confirmation.** It is useful for a laptop without a GPU. Doctor's `ollama.embed` latency warn makes the cost visible. | AC-33 |
| 6 | Keep the launchd labels `io.github.claude-memory.*` now that the tool is multi-project? | **Keep them** so hand installs are adopted, not duplicated. A rename can be a later `--upgrade` migration (bootout old, bootstrap new). | AC-44 |

## 14. Acceptance Criteria Summary (Definition of Done)

- [ ] AC-1 — install/uninstall/doctor/migrate/version dispatched before config load
- [ ] AC-2 — `version` + ldflags in Makefile
- [ ] AC-3 — `migrate` subcommand
- [ ] AC-4 — flag surface; passwords never via argv
- [ ] AC-5 — Detect → table → choices → plan → confirm → Apply → Verify
- [ ] AC-6 — default choice per state
- [ ] AC-7 — step order and prerequisite blocking
- [ ] AC-8 — failure isolation, exit 1 with remedies
- [ ] AC-9 — atomic writes, per-step manifest, install lock, Ctrl-C safety
- [ ] AC-10 — line-oriented prompts with defaults
- [ ] AC-11 — non-TTY without `--yes` exits 2, never blocks
- [ ] AC-12 — `--yes` uses defaults/flags; never prompts; never overwrites `modified`
- [ ] AC-13 — `--dry-run` structurally write-free, diffs shown
- [ ] AC-14 — plain/colored output rules
- [ ] AC-15 — platform detection incl. WSL and package managers
- [ ] AC-16 — job backend selection launchd/systemd/cron/none
- [ ] AC-17 — unsupported OS exits 2
- [ ] AC-18 — package hints only; no sudo/brew/apt/curl|sh execution
- [ ] AC-19 — topology choice A/B/C1/C2 with detected default
- [ ] AC-20 — remote DSN prompt, URL-encoded, classified connect errors
- [ ] AC-21 — local bootstrap via admin DSN or printed SQL (shared with initdb)
- [ ] AC-22 — generated passwords
- [ ] AC-23 — docker-local bundle (loopback, named volume, port 55432) + compose up
- [ ] AC-24 — docker-server bundle + printed steps; no ssh/scp/docker
- [ ] AC-25 — migrate step before hooks; always on `--upgrade`
- [ ] AC-26 — topology/DSN change warns, no data move
- [ ] AC-27 — `config.ParseEnvFile` with format findings
- [ ] AC-28 — env file written plain, in place, unknown keys preserved
- [ ] AC-29 — wrong env perms detected and repaired
- [ ] AC-30 — password never leaves env file/bundle `.env` (sentinel test)
- [ ] AC-31 — `--pg-dsn` with password rejected
- [ ] AC-32 — Ollama probe, pull via API, 1024-dim check
- [ ] AC-33 — non-loopback Ollama warning
- [ ] AC-34 — assets embedded; works outside a checkout
- [ ] AC-35 — binary self-install, PATH and quarantine hints
- [ ] AC-36 — hook scripts installed with rendered path
- [ ] AC-37 — order-preserving idempotent settings.json merge
- [ ] AC-38 — refuse unparseable/unexpected settings.json
- [ ] AC-39 — settings.json backup (5 kept) + concurrent-change abort
- [ ] AC-40 — MCP via `claude mcp` CLI only
- [ ] AC-41 — skills with hash states
- [ ] AC-42 — CLAUDE.md managed block, consent, `--yes` rules
- [ ] AC-43 — jobs call binary directly with explicit PATH
- [ ] AC-44 — launchd bootstrap/bootout, existing labels
- [ ] AC-45 — systemd user timers, linger hint
- [ ] AC-46 — cron managed block
- [ ] AC-47 — namespaces init/add via port, suggestions
- [ ] AC-48 — optional seed step
- [ ] AC-49 — manifest schema, no secrets
- [ ] AC-50 — no-op re-run performs zero writes
- [ ] AC-51 — hand installs adopted, never duplicated
- [ ] AC-52 — `--upgrade` non-interactive refresh
- [ ] AC-53 — `--only` / `--skip`
- [ ] AC-54 — uninstall reverses manifest exactly
- [ ] AC-55 — purge flags; typed confirmation for data
- [ ] AC-56 — uninstall without manifest removes nothing
- [ ] AC-57 — doctor read-only
- [ ] AC-58 — doctor check table
- [ ] AC-59 — doctor exit codes and time bounds
- [ ] AC-60 — doctor `--json` schema
- [ ] AC-61 — optional hook latency probe (`doctor.probe`)
- [ ] AC-62 — install ends with doctor
- [ ] AC-63 — no real external commands in tests; import guard
- [ ] AC-64 — golden merge tests, run twice
- [ ] AC-65 — integration tests + CI paths
- [ ] AC-66 — INSTALL.md / DEPLOY.md / README updates
