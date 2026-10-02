# Specification: Claude Memory — `install` / `doctor` (interactive, re-runnable setup)

## 0. Metadata
- Spec ID: SPEC-2026-10-01-install-doctor
- Status: v0.7 (small WI-S2-9/10 review changes, §0.7; v0.6 one AC-62 wording change, §0.6; otherwise v0.5). Slice 1 is implemented and merged, with the review 04
  fixes in. Slice 2 is re-planned after the slice-2 plan review
  (`05-slice-2-plan-review.md`, v0.3 edits in §0.3) and its iteration-2
  and iteration-3 re-reviews (`06-slice-2-plan-rereview.md`, v0.4 edits
  in §0.4, v0.5 edits in §0.5). Slice 3 is not implemented.
  Implementation proceeds **slice by slice** (§0.2).
- Version: 0.6 (v0.5 → v0.6 in §0.6; v0.1 → v0.2 changes in §0.1; v0.2 → v0.3 in §0.3; v0.3 →
  v0.4 in §0.4; v0.4 → v0.5 in §0.5)
- Owner: Oleksandr Kolomoiets (user@example.com)
- Supersedes: none. Replaces the manual procedure in `integration/INSTALL.md`
  steps 1–9 and the laptop half of `DEPLOY.md` (they stay as reference).
- Related: `docs/specs/README.md` backlog item 8 (owner request,
  2026-10-01); backlog item 7 (hook skips migrations);
  `docs/specs/namespaces/01-spec.md` AC-25..AC-27 (`namespaces init|add|which`);
  `docs/specs/memory-mvp/01-spec.md` AC-30/AC-31 (hook latency, hook never
  fails), AC-44 (laptop-local Ollama).

### 0.1 Changes in v0.2

Owner decisions taken on the architecture review (2026-10-02):

| # | Decision | Where it lands |
|---|---|---|
| D1 | **Scope cut into slices** as the reviewer proposed. **Slice 1 (S1)** = `doctor` + shared plumbing: `go:embed` asset packages, `version`/ldflags, `migrate`, the DEPLOY.md env-file format fix, early dispatch before the DSN is needed, `internal/setup` ports + `Paths` + redaction, the order-preserving `settings.json` merge library, the CLAUDE.md marker-block library. **Slice 2 (S2)** = interactive `install` for topology **B** (remote Postgres, e.g. Tailscale) and topology **A** (local Postgres with an *existing* database/role given by DSN, or bootstrap through a printed command), launchd and systemd `--user` timers, `--upgrade` = `install --yes`, `uninstall` driven by `install.json`. **Deferred** with reasons (§12.1): Docker bundles / topology C, the cron backend, `--purge-*`, hand-install adoption heuristics, the doctor latency probe, `--only`, plus the reviewer's smaller cuts (seed step, in-process admin bootstrap, backup rotation). Topology C stays in the spec as **deferred slice 3 (S3)**, design kept in compact form (§6.5.1), because the owner originally asked for a Docker choice. | §0.2, every AC label, §6.5.1, §12.1, §14 |
| D2 | **Local bootstrap never runs sudo.** For topology A the wizard writes `bootstrap.sql` (mode 0600, real values including the generated password: `CREATE ROLE`, `CREATE DATABASE`, `CREATE EXTENSION vector`, grants) into the state dir, **prints** the one command to run it (`sudo -u postgres psql … -f - < <file>` on Linux, `psql … -f <file>` on macOS/Homebrew), then re-detects. The SQL body is **shared** with `deploy/initdb/` as one embedded psql-variable file (`deploy/initdb/app-role.psql`); this resolves the v0.1 contradiction between AC-21 ("one file") and plan Design 5 ("two copies + equivalence test"). Generated passwords are URL-safe. | AC-21, AC-22, §8 |
| D3 | **BLOCKER fix.** `claude mcp get` and `claude mcp list` health-check servers by **spawning** them (for us: `claude-memory serve` → `postgres.New` → migrations). They are never called. Registration is read from `$CLAUDE_CONFIG_DIR/.claude.json` (default `~/.claude.json`) → `mcpServers["claude-memory"]`. Only `claude mcp add|remove --scope user` write. The doctor read-only guarantee holds; new AC-67 tests that doctor and every Detect never execute the registered command. | §4, AC-40, AC-57, AC-58, AC-67, §10 `ClaudeCLI` |
| D4 | **settings.json merge model** rewritten: `json.Decoder` + `UseNumber` + `InputOffset` raw slices (untouched subtrees re-emitted byte-for-byte), indentation style, key order and unknown keys preserved; no-op = **no semantic change** (not byte-identical); our entry is `outdated` (and repaired) only when it equals the entry the manifest recorded, otherwise a differing entry of ours is `modified` → kept and reported as drift, never overwritten under `--yes`; duplicate keys refused; `settings.local.json` and the project's `.claude/settings*.json` are inspected read-only and doctor warns on duplicates. | AC-37, AC-38, AC-70, §8 |
| D5 | **Plan/spec inconsistencies resolved:** a `Paths` + `Env` value type built in `main.go` (no `os.Getenv` in `internal/setup`; tests pass explicit values, no `t.Setenv`) — AC-68; `PlatformInfo` is data, not a port; `Verify` is dropped — the engine re-runs Detect; choices reduced to `apply | keep | skip` (+ one `Confirm` for `modified`, `--reconfigure` flag); `migrate` dispatches **after** config load (AC-1 vs AC-3); doctor probes run in parallel under a per-check `--timeout` and a whole-run `--deadline`; `systemctl --user` without a session bus is retried with the user's bus address, and with no usable backend the jobs step prints instructions (no silent cron fallback); secrets edges (redactor minimum length, `--pg-password-stdin` read first, URL-safe passwords); the manifest is trimmed by `uninstall` and deleted when empty; doctor flags `run-with-env.sh` plists; the `remember`/`memory-digest` skills lose their `acme/CLAUDE.md` citations; the remote-Ollama warning names the hook's 800 ms timeout. | AC-1..AC-3, AC-5, AC-6, AC-11, AC-16, AC-30, AC-31, AC-33, AC-54, AC-58, AC-59, AC-66, AC-68, §10 |
| D6 | **§13 answered**: all six recommendations accepted, plus the reviewer's three new questions (slice plan: yes; `--upgrade` ≡ `install --yes`: yes; topology C: deferred slice 3). | §13 |

Other review findings applied (not owner decisions): `sslmode` prompt for B
(AC-20); SQLSTATE-based error classifier (AC-20); topology-A default needs a
local socket or process, not only an open TCP port (AC-19); root guard
(AC-69); refusal to self-install a `go run` temp binary (AC-35);
existence-checked `~/.claude/projects` decoding (AC-47); doctor exit code 3
and stable JSON keys (AC-59, AC-60); quoted-whole-value env finding
(AC-27); `.git` as file or directory (AC-42); FakeRunner matcher and the
widened argv deny list (AC-18, AC-63); test cwd outside the repo (AC-34);
manifest records the jobs backend and the effective Claude config dir
(AC-49); scenario check count fixed (§3).

**ACs added:** AC-67 (MCP detection never executes the registered
command), AC-68 (`Paths`/`Env` injection), AC-69 (root guard), AC-70
(other settings files scanned for duplicate hooks).
**ACs withdrawn from S1/S2 (deferred, text kept):** AC-23, AC-24 (→ S3),
AC-46, AC-48, AC-55, AC-61 (→ §12.1). **Rewritten:** AC-21, AC-37, AC-40,
AC-52. No AC number is reused.

### 0.2 Slices

Every AC and plan work item carries one label:

| Label | Meaning | Ships as |
|---|---|---|
| **[S1]** | Slice 1 — `doctor` + plumbing | PR 1 (the owner runs `doctor` on the live hand install the day it merges) |
| **[S2]** | Slice 2 — `install` (A/B, launchd, systemd), `--upgrade`, `uninstall` | PR 2 |
| **[S3]** | Slice 3 — Docker topology C (deferred; re-planned before work starts) | not scheduled |
| **[D]** | Deferred follow-up (§12.1); text kept for the design record | not scheduled |

An AC split across slices says which half lands where, e.g. `[S1 library /
S2 step]`.

### 0.3 Changes in v0.3 (slice-2 plan review, 2026-10-02)

Only the slice-2 text changes. Each edit closes a
`05-slice-2-plan-review.md` finding. Owner confirmation is pending for the
rows marked *(owner Q)*. They are written with the planner's recommended
default so implementation is not blocked (plan v0.3, "Open questions").

| AC / § | Change | Finding |
|---|---|---|
| AC-5, §10 `Step` | Engine phases gain **Configure** (input questions, after choices, before Plan). Detect returns per-artifact states. Choices are per artifact. Apply returns `StepResult{Artifacts, Removed, Notes, Diffs, Await}`. A step succeeds when every artifact it chose to apply re-detects `ok` (a kept `modified` artifact is drift, not failure). | #1, #2, B-1 |
| AC-7 | Order: `envfile` **before** `database` (the env file is written by one step only and holds the generated password before `bootstrap.sql` exists). | #3 |
| AC-20 | The DSN builder percent-encodes every byte outside RFC 3986 unreserved in user and password (not `url.UserPassword`, which leaves `$` etc.). | B-2 |
| AC-21 | `--yes` behaviour with no database is defined. `bootstrap.sql` renders only passwords in `[A-Za-z0-9_.~-]` *(owner Q)*. | #1, B-3 |
| AC-36 | Doctor and Detect compare hook scripts with the **rendered** script. An unrecorded file equal to the raw embedded script is `outdated`. | #4 |
| AC-44 | launchd Detect compares with the manifest-recorded hash and the rendered plist (moved `--bin-dir` / schedule / PATH → `outdated`). | #6 |
| AC-54 | The binary is removed from its **recorded** path (no `--bin-dir`). `env-key`/`dir` are retained kinds, dropped from the manifest without reversal. A `settings.json` created by install is removed only when it unmerges to `{}`. The verify step excludes kept files, backups and `install.lock`. | #5 |
| AC-62 | A doctor `fail` from a check whose step the user skipped or kept (or that this build does not install) is printed but does not make `install` exit 1 *(owner Q)*. | B-6 |
| §10 | `Namespaces` port dropped (namespaces are written through `FS`). `Paths.EphemeralDirs` added. `detectPlatform` takes the uid. | #8, B-5 |

### 0.4 Changes in v0.4 (slice-2 plan re-review, iteration 2, 2026-10-02)

Slice-2 text only; each edit closes a `06-slice-2-plan-rereview.md`
finding. No new owner question; the five v0.3 defaults stand.

| AC / § | Change | Finding |
|---|---|---|
| AC-5 | Shared state is seeded from flags/`Env`/env file/manifest by each owning step **before** Detect; Configure only overrides; an unset value leaves its env key untouched. Step 5 re-Detects the steps that **read** a changed value (not "the steps after"). Step 4 includes the blocked-step `re-check / skip / quit`. | H1, H2, M1 |
| AC-21 | A leftover `bootstrap.sql` while the probe says auth/nodb/no `vector` = `blocked: awaiting user action`, not a failure. Verify uses testcontainers `Exec` by default. | M2, L2 |
| AC-28 | Managed keys are written only when their value is known in this run; otherwise the line is untouched. | H1 |
| AC-35 | The binary path is sticky: explicit `--bin-dir`, else the manifest-recorded path, else `~/.local/bin`; `doctor` uses the same rule. `GOTMPDIR` added to the refusal. | H3, L4 |
| AC-51 | For hook scripts "embedded version" = the rendered script (AC-36). | O5 |
| AC-54 | Owned `dir` artifacts under the Claude config dir are removed when empty; `<ConfigDir>`/`<StateDir>`/`<BinDir>` are retained; tree-equality excludes them and unrecorded parents. | M3 |
| AC-62 | "Kept" removed from the exit-code exemption: only user-skipped (or not-yet-installed) steps are exempt. | O4 |
| §10 | Read/write port halves (`ReadFS`, `DBProbe`, `OllamaProbe`, `JobDetector`), `JobSpec` with `Unit`, `JobDetector.Detect` takes the recorded hashes, `Seeder`, `EphemeralDirs` + `GOTMPDIR`. | M4, M5, L7, H1, L4 |

### 0.5 Changes in v0.5 (slice-2 plan re-review, iteration 3, 2026-10-02)

Slice-2 text only; each edit closes a `06-slice-2-plan-rereview.md`
§ Iteration 3 finding. No new owner question; all earlier defaults stand.

| AC / § | Change | Finding |
|---|---|---|
| AC-5 | Seed order flag → env file → `Env` (only when the file has none; a differing shell value is a drift note) → the owner's documented default. Under `--yes` a missing value takes the owner's default and fails only when there is none. Unset → set counts as a change for step 5. An `Await` names the artifacts the user action must fix; the re-check passes on those, then the step's other artifacts are applied with their defaults. | N3, N10, N1 |
| AC-6 | Names the AC-5 step-5 exception (an env key the user answered in this run is applied, even when `modified`). | N9 |
| AC-21 | `bootstrap.sql` is deleted through the transient `bootstrap-file` artifact (`ok` when there is no file or while pending, `outdated` once the database is `ok`, never recorded in the manifest). | N1 |
| AC-28 | A key is written when its owner set it in this run, including a documented default or a generated value. | N3 |
| AC-50 | Verify adds: a differing shell `MEMORY_PG_DSN` does not make a no-op run write. | N10 |
| §10 | `Detection` (with `Remedy`) listed; `Seed` returns `([]Note, error)`; `Await{Instructions, Artifacts}`. | N4, N6, N1 |


### 0.6 Changes in v0.6 (WI-S2-14b review, 2026-10-02)

Small and reversible: restore the old AC-62 sentence to undo it.

| AC / § | Change | Finding |
|---|---|---|
| AC-62 | The restart line is printed only when the run applied a change, and reads "restart Claude Code sessions to load the changes". | 14b review #2 |

### 0.7 Changes in v0.7 (WI-S2-9/10 review, 2026-10-02)

Small and reversible: restore the old wording of each AC to undo it.

| AC / § | Change | Finding |
|---|---|---|
| AC-7 | `hooks.settings` requires `hooks.scripts` as well as `migrate`, so no entry points at a script that was not installed. | 2b-1 conformance F1 |
| AC-39 | The settings backup is mode 0600 (was: the original's mode) and never overwrites an existing backup (a `.N` suffix is added when the name exists). | 2b-1 security A8 |
| AC-40 | An entry with our command and args but extra `env` is `modified` (kept unless the overwrite is confirmed), not `outdated`; replacing it drops that env, and the plan names the keys. | 2b-1 conformance F4 |
| AC-51 | Already-correct artifacts that are `ok` but not in the manifest are recorded on the first run (adoption), without any write to the artifact. | 2b-1 conformance F2 |

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
fix or work around (each re-verified in the architecture review):

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
  superuser (pgvector is not a trusted extension). The Docker server stack
  creates the extension in `deploy/initdb/01-app-role.sh`. A fresh local
  Postgres has no equivalent step, so `postgres.New` fails with
  `permission denied to create extension`. Once a superuser has created
  it, the full migration set is idempotent as the app role.
- **Scheduled jobs have no PATH, and the wrapper corrupts passwords.**
  launchd runs with `/usr/bin:/bin:/usr/sbin:/sbin`. `ingest-pr` executes
  `az` and `git`, and session extraction executes `claude`, which are
  usually in `/opt/homebrew/bin` or `~/.local/bin`. `run-with-env.sh`
  `source`s the env file: a `$` or `&` in the password makes bash expand
  or execute parts of it, and because `LoadFromFile` only sets unset keys,
  a partially expanded value wins over the file.
- **The binary has no version** (no ldflags, no `version` subcommand), so
  neither an upgrade nor doctor can say what is installed.
- **Assets exist only in a repo checkout.** `integration/` and `deploy/` are
  not in the binary. A copied or released binary cannot install hooks or
  skills.

This feature adds four subcommands:

- **`claude-memory doctor`** [S1]: a read-only health check with
  remediation hints, exit codes and `--json`.
- **`claude-memory migrate`** [S1]: applies the schema explicitly. It is
  needed because the hook skips migrations.
- **`claude-memory install`** [S2]: an interactive wizard (or
  non-interactive with `--yes` and flags) that detects the platform, asks
  for a topology, and runs an ordered list of idempotent steps. Before
  applying anything it shows each step's state, and every step can be
  skipped.
- **`claude-memory uninstall`** [S2]: reverses exactly what `install`
  recorded.

## 2. Glossary

| Term | Definition |
|---|---|
| Slice | A delivery unit (§0.2): S1, S2, S3 or D. |
| Step | One unit of installation (e.g. `hooks.settings`) with three phases: **Detect → Plan → Apply**; after Apply the engine runs Detect again and the step succeeds only if it reports `ok`. |
| Step state | The result of Detect: `absent`, `ok`, `outdated` (we installed it, the user did not change it, the embedded version differs), `modified` (it differs from what we recorded, or it is ours by identity but we have no record of writing it in this form), `blocked` (a prerequisite is missing or the platform cannot run it; the detail says which). v0.1's `foreign` and `unsupported` are folded in: a byte-identical unrecorded artifact is `ok` (and recorded), a differing one is `modified`; unsupported is `blocked` with that reason. |
| Choice | What the user picks per step, applied per artifact (v0.3: a step's artifacts, e.g. its two hook events, get their own defaults; `apply` on a step applies its absent/outdated artifacts and asks the overwrite `Confirm` per modified one): `apply` (install, refresh or repair to our version), `keep`, `skip`. Overwriting a `modified` artifact additionally needs an explicit `Confirm`. Re-asking a step's questions is the `--reconfigure` flag, not a choice. |
| Topology | Where Postgres and Ollama run (§6.5): **A** local Postgres, **B** remote Postgres + local or remote Ollama, **C** Postgres in Docker (S3, deferred). |
| Paths / Env | Value types computed once in `main.go` from `HOME`, `CLAUDE_CONFIG_DIR`, flags, `os.Getuid()` and an allow-listed snapshot of environment variables, and injected into every step and check (AC-68). |
| PlatformInfo | Detected OS, arch, OS version, WSL flag, jobs backend and package managers; plain data handed to the engine and doctor. |
| Manifest | `~/.config/claude-memory/install.json`. It records every artifact `install` created: kind, path or identity, sha256 as written (and, for hook entries, the canonical entry), binary version and time. |
| Embedded assets | Hook scripts, skills, the CLAUDE.md section, job templates and the bootstrap SQL, compiled into the binary with `go:embed`. |
| Managed block | Text between `<!-- BEGIN claude-memory -->` / `<!-- END claude-memory -->` in a Markdown file. |
| Our hook entry | A `settings.json` hook whose `command` matches `…/claude-memory/user-prompt-submit.sh` or `…/claude-memory/session-end.sh`, or contains `claude-memory hook` / `claude-memory extract`. Identity only; whether we may change it is decided by the manifest (AC-37). |
| MCP registration | `mcpServers["claude-memory"]` in Claude Code's `.claude.json` (user scope), read directly, written only through `claude mcp add|remove --scope user`. |
| Check | One doctor probe with an id, a status (`pass`/`fail`/`warn`/`info`/`skip`), detail and a remedy. |

## 3. User Scenarios

### Scenario: Health check of today's hand install [S1]
The owner builds slice 1 and runs `claude-memory doctor` on the Mac set up
by hand from INSTALL.md. No env-file fix is needed for doctor to start.
Doctor prints 23 checks, e.g. `21 pass · 2 warn`: `jobs` warns that both
plists run `integration/bin/run-with-env.sh` (remedy: "re-install the jobs
with `claude-memory install` once slice 2 ships; until then see
INSTALL.md"), and `hooks.settings` warns that the entries use the legacy
`$HOME/...` form. The MCP check reads `~/.claude.json`; it never starts
the server.

### Scenario: First install on a Mac, remote Postgres (topology B, today's setup) [S2]
The owner downloads or builds the binary and runs `./claude-memory install`.
The wizard prints `macOS 15 arm64 · launchd · brew found`. It finds that
Ollama is running and that `claude` and `git` are on PATH, then asks for a
topology. The owner picks **B**. The wizard asks for host, port, database,
user, TLS mode (default `prefer`; "Tailscale already encrypts the link"),
then a password with no echo. It connects, finds `vector` installed, and
runs migrations. It pulls `bge-m3` after a y/N prompt. It asks for project
mappings for `namespaces.yaml`, suggesting existing parent directories of
recent `~/.claude/projects` entries. Next it shows a plan table with the
settings.json change (`+2 hook entries, 0 removed, backup →
settings.json.bak.claude-memory.20261001T101500Z`) and the jobs it will
load. After **Apply** it runs doctor in-process and prints `23 checks:
22 pass · 1 warn (ollama.embed: 1.8 s, model was cold)`, then "restart
Claude Code sessions to load the hooks".

### Scenario: Re-run after a manual change [S2]
Months later the owner runs `claude-memory install` again. The status table
shows every step `ok` except `skills.remember: modified (you edited
SKILL.md)` and `jobs.ingest-pr: absent`. The defaults are keep for the
modified skill (the diff is shown on request; overwriting needs a second
explicit confirmation) and apply for the missing job. No hook is
duplicated and `settings.json` is untouched (no backup, no rewrite, even
though it is tab-indented).

### Scenario: Upgrade after a new binary [S2]
`make install` (or a new download) followed by `claude-memory install
--upgrade`, which is exactly `install --yes`. Migrations are applied when
the schema is behind. Every `outdated` artifact is refreshed; nothing
`modified` is changed — those are listed as `warn: drift` with the hint to
run `claude-memory install` interactively. MCP is re-registered only if
the binary path changed, jobs are reloaded only if their rendered unit
changed, then doctor runs. No questions are asked.

### Scenario: Fresh Linux box, local Postgres (topology A) [S2]
Ubuntu 24.04. Detect reports `postgres: absent`, `pgvector: absent`,
`ollama: absent`. The wizard prints the package commands (`sudo apt
install postgresql-16 postgresql-16-pgvector` via PGDG, the Ollama
installer URL) and offers "re-check / skip / quit". It never runs them.
After the owner installs them and picks re-check, the database step asks
"use an existing database (enter its credentials)" or "create the role and
database". The owner picks create. The wizard generates a URL-safe
password, writes it into the env file, writes
`~/.local/state/claude-memory/bootstrap.sql` (0600) and prints:

```
sudo -u postgres psql -v ON_ERROR_STOP=1 -d postgres -f - < ~/.local/state/claude-memory/bootstrap.sql
```

It waits at "re-check / skip / quit". After the owner runs the command
and picks re-check, the app DSN connects with `vector` present, the file
is deleted, and the wizard continues with `migrate`. Jobs use systemd user
timers, with a hint about `loginctl enable-linger`.

### Scenario: Linux over ssh without a user session bus [S2]
On a server reached over `ssh` with lingering enabled, `systemctl --user`
fails with `Failed to connect to bus`. The detector retries with
`XDG_RUNTIME_DIR=/run/user/<uid>` and the bus address and finds the user
instance. Where no user instance exists at all (containers, some WSL), the
`jobs` step is `blocked: no systemd user instance`; it prints the unit
files' location and the manual commands, and does not fall back to cron.

### Scenario: Postgres in Docker (topology C) [S3, deferred]
Kept for the design record in §6.5.1. `--topology docker-local` /
`docker-server` exit 2 in S2 with "topology C is deferred (spec §6.5.1);
use B against your Docker server, or A".

### Scenario: Health check in CI or a script [S1]
`claude-memory doctor --json | jq '.summary'`. The command exits 1 when a
check fails, 0 otherwise, 3 when it cannot start (e.g. `HOME` unset). It
never prompts, writes or starts the MCP server, and redacts the password
from all output.

### Scenario: Uninstall [S2]
`claude-memory uninstall` lists what the manifest recorded: hook entries,
scripts, skills, MCP registration, jobs and the CLAUDE.md block. The user
confirms once. Each item is reversed and removed from the manifest; the
manifest is deleted when it is empty. Files the user modified are listed
separately with default **keep** (and stay in the manifest). The env file,
`namespaces.yaml` and all database data are kept.

## 4. Assumptions & Constraints
- Single user, user-level install. The installer never invokes `sudo`, and
  it never installs system packages, including via `brew`, which needs no
  sudo. For packages and privileged database setup it only prints
  commands, then re-detects (§13 #3, D2). Running as root is refused
  unless `--allow-root` (AC-69).
- Supported: **macOS** (arm64, amd64) and **Linux** (amd64, arm64; WSL2 is
  Linux). **Windows, BSDs and others are out of scope.** `install` exits 2
  with a message pointing at `integration/INSTALL.md`. `doctor` still runs
  its platform-independent checks.
- Claude Code reads hooks from `~/.claude/settings.json` (or
  `$CLAUDE_CONFIG_DIR/settings.json` when that variable is set) and runs a
  hook `command` through a shell, so `$HOME` in a command expands.
  `CLAUDE_CONFIG_DIR` relocates `.claude.json` too (verified: without it
  the file is `~/.claude.json`, with it `$CLAUDE_CONFIG_DIR/.claude.json`).
- **MCP registration** (D3): `claude mcp add|remove --scope user` are the
  only writers (`add` defaults to scope `local`, so `--scope user` is
  load-bearing). **`claude mcp get` and `claude mcp list` are never run**:
  they health-check each server by spawning its command, which for us
  starts `claude-memory serve`, opens the database and applies migrations.
  Detection reads `.claude.json` → `mcpServers["claude-memory"]` =
  `{type, command, args, env}` read-only. `.claude.json` belongs to Claude
  Code and is **never written** by us.
- Layering stays as it is. `internal/setup` holds the step engine, every
  step, the doctor checks and the pure merge libraries, and depends only
  on ports it declares itself plus the `Paths`/`Env`/`PlatformInfo`
  values. Concrete adapters (os, exec, pgx, http, terminal) are
  constructed only in `cmd/claude-memory`. `internal/setup` never imports
  `os/exec`, `net/http`, pgx or `golang.org/x/term`, and never calls
  `os.Getenv`, `os.UserHomeDir`, `os.Getuid` or `os.Executable` (AC-68).
- No new heavy dependency. The prompts are line-oriented, using stdlib
  `bufio` and `fmt`. The one addition is **`golang.org/x/term`** (S2), for
  `ReadPassword` (no echo) and `IsTerminal`. It is maintained by the Go
  team, and its only dependency `golang.org/x/sys` is already in the
  module graph. A TUI framework (bubbletea, survey, promptui) was
  rejected: a large dependency tree for about 20 prompts, raw-mode
  rendering that breaks over dumb terminals, `ssh -T`, CI logs and piped
  stdin, and line prompts are trivially fakeable in tests (§10
  `Prompter`).
- Server-side automation is limited to generating files and printing
  commands. The wizard never opens a remote shell.

## 5. Cross-Module Interactions

```
cmd/claude-memory/main.go  (composition root)
   │  early dispatch (no DSN needed): doctor, version [S1]; install, uninstall [S2]
   │  after config load:              migrate [S1] and the existing subcommands
   │  builds Paths + Env (AC-68) and PlatformInfo (detectPlatform, a function, not a port)
   ├─ adapters: osFS (read-only | dry-run | writable), execRunner (read-only | mutating),
   │            ttyPrompter (x/term) [S2], systemClock, pgProber (internal/postgres),
   │            ollamaProber (internal/ollama), claudeCLI (mcp add/remove only) [S2],
   │            launchd | systemd JobManager [S2]   (v0.3: no nsStore; namespaces via FS)
   ▼
internal/setup
   ├─ libraries [S1]: jsonobj + settings merge, mdblock, redact, mcpreg (.claude.json reader),
   │                  manifest (read [S1], write [S2]), diff
   ├─ doctor [S1]: check registry, parallel runner, text | --json report
   ├─ engine [S2]: for each Step: Detect → table → choice → Plan → one confirm → Apply → re-Detect
   └─ assets ◄── integration (package integration: //go:embed hooks skills launchd systemd claude-md-section.md ...)
             ◄── deploy      (package deploy: //go:embed initdb/app-role.psql ... ; compose bundle [S3])

internal/config   + ParseEnvFile(path) (pure parse + perm report; LoadFromFile uses it) [S1]
internal/postgres + Probe (read-only), Migrate [S1]
internal/ollama   + Prober (version, tags, embed dims [S1]; pull [S2])
internal/namespace  (Parse reused; + Marshal, Config.Add exported, pure) [S2]
```

## 6. Functional Requirements

### 6.1 Command surface and dispatch
- AC-1 [S1 doctor, version / S2 install, uninstall] (Ubiquitous):
  `main.run` shall dispatch `doctor` and `version` (S1) and `install` and
  `uninstall` (S2) before `config.LoadFromFile` and `config.Load`, as it
  already does for `namespaces`. They shall work with no env file, a 0644
  env file, or no `MEMORY_PG_DSN`. `migrate` is dispatched **after**
  config load, because it needs the DSN and the env file's 0600 check
  (AC-3). Verify: a `cmd` test with `HOME` set to an empty temp dir (on
  the child process's environment, not `t.Setenv`) runs `doctor --json`
  (S1) and `install --dry-run --yes --topology remote` (S2) without the
  error `MEMORY_PG_DSN is required`.
- AC-2 [S1] (Ubiquitous): `claude-memory version` shall print the module
  version, VCS revision and dirty flag, read from `-ldflags -X
  main.version` when set and otherwise from `debug.ReadBuildInfo`; when
  both are empty it prints `dev`. The Makefile defines `LDFLAGS` once
  (`git describe --tags --always --dirty`) and uses it in `build`,
  `install` and `cross-build`. Verify: unit test of the formatter; `make
  build && bin/claude-memory version` shows a revision.
- AC-3 [S1] (Ubiquitous): `claude-memory migrate` shall load the config
  like the other post-config subcommands, open the store with
  `postgres.New` (which applies the idempotent migrations), print `schema
  up to date (migrations: 0001,0002)` and exit 0. On failure it exits 1
  with a redacted error. Verify: integration test against a fresh
  database; a second run is a no-op.
- AC-4 [S1 doctor flags / S2 install, uninstall flags] (Ubiquitous):
  - `doctor` [S1]: `--json`, `--strict`, `--timeout D` (per check, default
    3 s), `--deadline D` (whole run, default 10 s).
  - `install` [S2]: `--yes`, `--upgrade` (alias of `--yes`, AC-52),
    `--dry-run`, `--reconfigure`, `--topology local|remote`, `--skip
    STEP[,STEP]`, `--bin-dir DIR` (default `~/.local/bin`), `--ollama-url
    URL`, `--pg-dsn DSN` (rejected if it contains a password, AC-31),
    `--pg-sslmode prefer|require|disable`, `--pg-password-stdin`,
    `--claude-md PATH`, `--namespace NAME=GLOB` (repeatable), `--pr-repos
    PATHS`, `--no-jobs`, `--jobs-backend launchd|systemd|none`,
    `--no-doctor`, `--allow-root`.
  - `uninstall` [S2]: `--yes`, `--dry-run`, `--allow-root`.
  - Deferred flag values exit 2 with a pointer to §12.1:
    `--topology docker-local|docker-server` (S3), `--only`, `--purge-*`,
    `--seed-file`, `doctor --latency`, `--jobs-backend cron`.

  Unknown flags exit 2. Verify: flag parsing table test.

### 6.2 Engine (internal/setup) [S2]
- AC-5 [S2] (Ubiquitous) — **revised in v0.3**: Every step shall
  implement `Detect` (returning a step state plus **one state per
  artifact**, e.g. per hook event, per skill file, per job), `Plan`
  (actions, diffs, notes) and `Apply` (returning `StepResult{Artifacts,
  Removed, Notes, Diffs, Await}`), and optionally `Configure` (its input
  questions). Detect, Configure and Plan get read-only ports only. There
  is no separate `Verify`: after Apply the engine runs the step's Detect
  again and marks the step failed unless every artifact it chose to
  apply reports `ok`. A kept `modified` artifact is reported as drift and
  is not a failure. The engine shall:
  1. (v0.4) let each step that owns a shared value (binary path,
     topology, DSN, Ollama URL/model, PR repos, …) seed it from flags,
     `Env`, the env file and the manifest, then run Detect for all steps.
     Each value has one owner; Configure may only override it. (v0.5)
     The source order is flag → env file → `Env` (used only when the env
     file has no value for that key; a shell value that differs from
     the file's is reported as drift and not used) → the owner's
     documented default (e.g. AC-32's Ollama URL and model). A value
     with no source and no default stays unset, and the env-file key it
     feeds is left untouched;
  2. print a status table (step, state, default choice, one line of
     detail);
  3. collect choices (per artifact; Choice in §2);
  4. run Configure in step order; (v0.4) in the same walk, a step that
     is `blocked` by an external condition (missing tool, no jobs
     backend, Ollama absent) is offered `re-check / skip / quit`
     interactively (AC-10, AC-18), and stays blocked under `--yes`.
     (v0.5) Under `--yes`, a missing value takes its owner's documented
     or detected default (AC-19, AC-21 create-path defaults) and the step
     fails with the flag name (AC-12) only when there is none;
  5. (v0.4) re-Detect every step that **reads** a shared value Configure
     changed (v0.5: including unset → set) (wherever it sits in the order, e.g. `envfile` after a DSN
     change), plus every step whose block was resolved and its
     dependents. An artifact whose state changed takes its AC-6 default,
     except that an env key whose value the user answered in this run is
     applied;
  6. show the combined plan and ask for one confirmation;
  7. Apply in order. Before applying a step whose prerequisite was
     applied in this run, Detect it again. After each Apply, re-Detect
     the step.

  A step may pause for a user action (`Await`: printed instructions, then
  `re-check / skip / quit`, AC-10). (v0.5) The pause names the artifacts
  the user action must turn `ok`; the re-check passes when those
  re-detect `ok`, and the engine then applies the step's remaining
  non-`ok` artifacts once with their AC-6 defaults (e.g. deleting
  `bootstrap.sql`, AC-21). Under `--yes` it ends `blocked` with
  the instructions as its remedy. Verify: an engine test with fake steps
  asserts:
  - the call order;
  - that no Apply happens before confirmation;
  - both re-Detect rules (v0.4: a DSN change re-detects the earlier
    `envfile`);
  - (v0.4) a no-op run seeds every shared value without Configure and
    writes nothing; an unset value leaves its env line byte-identical;
  - that a step whose post-Apply Detect leaves an applied artifact non-`ok`
    is reported failed;
  - that a step with one `modified` and one `absent` artifact under
    `--yes` applies the absent one and succeeds with a drift note.
- AC-6 [S2] (Ubiquitous): Default choices shall be: `absent → apply`, `ok
  → keep`, `outdated → apply`, `modified → keep`, `blocked → skip`.
  Choosing `apply` for a `modified` artifact requires a second explicit
  `Confirm("overwrite your modified <x>? a backup is kept")`, which is
  never auto-answered (AC-12). (v0.5) One exception, from AC-5 step 5:
  interactively, an env key whose value the user answered in this run's
  Configure is applied even when it is `modified`, without the second
  `Confirm`; the answer is the consent, the AC-26 warning was shown
  before it, and the combined plan still shows the diff before the one
  confirmation. Under `--yes` there is no exception. Verify: table test
  over the 5 states × 3 choices, plus the exception row.
- AC-7 [S2] (Ubiquitous): The steps are ordered, and a step whose
  prerequisite step was skipped or failed shall be `blocked` with that
  reason, not attempted. The order is (v0.3: `envfile` before
  `database`): `platform`, `binary`, `prereqs`,
  `topology`, `envfile`, `database`, `migrate`, `ollama`, `namespaces`,
  `hooks.scripts`, `hooks.settings`, `mcp`, `skills`, `claude-md`, `jobs`,
  `doctor`. `hooks.settings` requires `migrate` and `hooks.scripts`, so a failed
  migration never leaves a wired hook against an empty schema (v0.7: and no
  entry points at a missing script). Verify: a failing
  `database` blocks `migrate` and `hooks.settings`, and leaves `skills`
  unaffected.
- AC-8 [S2] (Unwanted): IF a step's Apply fails, THEN the engine shall
  stop that step, record nothing in the manifest for it, continue with the
  independent steps, and exit 1 with a summary listing each failed step
  and its remedy. Verify: fake step returns an error; the summary and
  exit code are asserted.
- AC-9 [S2] (Ubiquitous): Every file write shall be atomic: a temp file in
  the same directory, fsync, rename, keeping the existing mode or using
  the step's mode. The manifest is written after a step completes **only
  when that step changed the artifact set or a recorded hash**
  (`updated_at` moves only then), so an interrupted run (Ctrl-C → exit
  130) leaves every artifact either old or new with the manifest
  matching, and a no-op run writes nothing (AC-50). A process-scoped
  `flock` on `~/.config/claude-memory/install.lock` (never a PID file; it
  is released on exit, so a crash leaves no stale lock) shall make a
  second concurrent `install`/`uninstall` exit 2. Verify: the
  fault-injecting FS fails the rename and the original is intact; the
  second process fails to acquire the lock.

### 6.3 Interaction modes [S2]
- AC-10 [S2] (Ubiquitous): Prompts shall be line-oriented. A selection
  shows numbered options with a default (Enter accepts it). A
  confirmation is `[y/N]` or `[Y/n]`. Text prompts show their default.
  Invalid input re-asks, up to 3 times, then aborts that step as skipped.
  A blocked step's "re-check" re-runs only that step's Detect, not the
  whole table. Verify: `Prompter` fake transcripts.
- AC-11 [S2] (State-driven): WHILE stdin is not a TTY and `--yes` is not
  given, `install`/`uninstall` shall exit 2 before Detect with
  `non-interactive session: pass --yes and the needed flags`. Stdin is
  read in a non-TTY session **only** for `--pg-password-stdin`, which is
  read first, before Detect and before any other input (AC-31);
  otherwise a non-TTY run never reads stdin. Verify: test with a non-TTY
  prompter and an empty, never-closed stdin pipe completes.
- AC-12 [S2] (Ubiquitous): With `--yes` (or `--upgrade`), every choice
  takes its AC-6 default and every question takes its flag, `Env` or
  detected value. A required value that is missing (e.g. the DSN for
  topology B) shall fail that step with the flag name needed; it never
  falls back to prompting. `--yes` never overwrites `modified` artifacts
  (they are reported as `warn: drift`), never writes an explicitly given
  `--claude-md` path inside a git repository (AC-42), and never answers a
  `Confirm` for an overwrite. Verify: non-interactive run on a temp
  `Paths` with fakes.
- AC-13 [S2] (Ubiquitous): `--dry-run` shall run Detect and Plan and print
  the plan, including unified diffs for every text file that would change
  (secrets redacted; a new DSN line shows as
  `MEMORY_PG_DSN=postgresql://claude_memory:***@host:5432/claude_memory`).
  It performs no mutating action. This is enforced structurally: the
  engine receives a read-only `Runner` and an FS whose write methods
  return `ErrDryRun`. Verify: a dry-run test asserts zero FS writes and
  zero mutating commands, and that the output matches a golden file.
- AC-14 [S2] (Ubiquitous): `install` output shall use plain text, with
  color only when stdout is a TTY and `NO_COLOR` is unset. Each Apply
  prints `✓ step — detail` / `✗ step — error · fix: …`. Single-line
  progress (cursor movement) is used only when stdout is a TTY and `CI` is
  unset. Verify: golden output with color off.

### 6.4 Platform detection
- AC-15 [S1] (Ubiquitous): `detectPlatform` (a function in `cmd`, its
  result a `PlatformInfo` value) shall report the OS and arch
  (`runtime.GOOS/GOARCH`), the OS version (`sw_vers -productVersion` /
  `/etc/os-release`), WSL (`/proc/sys/kernel/osrelease` contains
  `microsoft`, case-insensitive: WSL1 kernels say `Microsoft`), the jobs
  backend (AC-16; S1 detects launchd only, S2 adds systemd), and (S2,
  for the hint table) the package managers present (`brew`, `apt-get`,
  `dnf`, `pacman`; `brew` absent is `info`, never `blocked`). Verify: the detector is tested with
  the fake FS and Runner for macOS, Ubuntu, Fedora, WSL1, WSL2,
  ssh-with-lingering-user-instance, and Windows.
- AC-16 [S2] (Ubiquitous): The jobs backend shall be launchd on darwin. On
  linux it is systemd user units when `systemctl --user
  show-environment` exits 0; if it fails and `/run/user/<uid>/bus`
  exists, the probe is retried with `XDG_RUNTIME_DIR=/run/user/<uid>` and
  `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus` in `Cmd.Env`
  (ssh sessions into a lingering user instance), and those variables are
  used for every later `systemctl --user` call. Otherwise the backend is
  `none`: the `jobs` step is `blocked: no systemd user instance` and
  prints where the units would go and the manual commands. **There is no
  silent cron fallback** (cron is deferred, §12.1). `--jobs-backend`
  overrides detection; the backend is recorded in the manifest and doctor
  warns when the detected backend differs from the recorded one.
  Verify: detector table test incl. the no-bus-then-retry row.
- AC-17 [S2] (Unwanted): IF the OS is not darwin or linux, THEN `install`
  shall exit 2 with `unsupported OS <goos>: see integration/INSTALL.md
  for manual steps` before any prompt. Verify: detector fake returns
  `windows`.
- AC-18 [S1 guard / S2 hints] (Ubiquitous): Package hints shall come from
  a table keyed by (package manager, component), covering Postgres 16 +
  pgvector, Ollama, git, the `claude` CLI, `az` and `libpq`/`psql`. They
  are only printed, never executed. An interactive `blocked` step offers
  `re-check / skip / quit`. Verify (S1, extended in S2): a guard test over
  the fake Runner's call log of every test in `internal/setup` asserts no
  call whose `filepath.Base(argv[0])` is in {`sh`, `bash`, `zsh`, `dash`,
  `env`, `sudo`, `doas`, `brew`, `apt`, `apt-get`, `dnf`, `yum`,
  `pacman`, `curl`, `wget`, `ssh`, `scp`, `rsync`, `psql`, `docker`}, and
  no `claude` call other than `claude mcp add --scope user …` /
  `claude mcp remove --scope user …`.

### 6.5 Topologies and the database [S2]
- AC-19 [S2] (Ubiquitous): The `topology` step shall offer **A** `local`
  (Postgres + pgvector on this machine; Ollama local or a URL) and **B**
  `remote` (Postgres elsewhere, e.g. over Tailscale; Ollama local or a
  URL). The default comes from detection: an existing env DSN → its host's
  class (loopback/socket → A, else B); else A only when `127.0.0.1:5432`
  accepts TCP **and** a local socket (`/var/run/postgresql/.s.PGSQL.5432`,
  `/tmp/.s.PGSQL.5432`) or a local `postgres` process exists (a forwarded
  port alone is not evidence); else B. The choice is stored in the
  manifest, and on a re-run the step is `ok` (keep) unless
  `--reconfigure`. Verify: detection table test.
- AC-20 [S2] (Ubiquitous): For B (and for A with an existing database),
  the wizard shall ask for host, port (5432), database (`claude_memory`),
  user (`claude_memory`), TLS mode (`prefer` default / `require` /
  `disable`, with the note "Tailscale already encrypts the link"; written
  as `?sslmode=`) and the password (no echo). It builds the DSN by
  percent-encoding every byte of user, password and (v0.4) database name
  outside RFC 3986 *unreserved* (`A-Za-z0-9-._~`), so `@ : / ? # $ & + ; =` and spaces are
  all encoded and the written env line never trips the AC-27
  `unparseable-value` rule (v0.3: `url.UserPassword` leaves `$` and other
  sub-delims unencoded). It then probes the connection with a 5 s timeout.
  A failure is classified from `*pgconn.PgError` SQLSTATE and net errors:
  `28P01`/`28000` → `auth failed` (re-ask password, up to 3 times; also
  shown when the role does not exist, because Postgres does not
  distinguish), `3D000` → `database missing`, a `pg_hba` rejection
  (`28000` with "no pg_hba.conf entry") → `pg_hba reject`,
  `net.Error`/`context.DeadlineExceeded` → `timeout/unreachable` (hint:
  `tailscale status`, firewall, DEPLOY.md). Verify: DSN builder table test
  incl. special characters and sslmode; classifier test on pgx error
  fixtures.
- AC-21 [S2] (Ubiquitous) — **rewritten in v0.2 (D2)**: For A, the
  `database` step shall offer (1) **use an existing database**: the AC-20
  prompts with host defaulting to `localhost`; or (2) **create the role
  and database**. In both cases Detect probes the app DSN for "connects"
  and "`vector` installed in the target database".
  - When the role/database must be created, or the database lacks
    `vector`, the step generates a password (AC-22; for an existing role
    the user's own password is used), writes the env file first (AC-28),
    then writes `<StateDir>/bootstrap.sql` (directory 0700, file 0600)
    with the **real** values and **prints** the command to run it:
    - Linux: `sudo -u postgres psql -v ON_ERROR_STOP=1 -d postgres -f -
      < <StateDir>/bootstrap.sql` — the redirect is opened by the user's
      own shell, so the `postgres` OS user needs no read access to the
      0600 file or the user's home;
    - macOS/Homebrew (the installing user is the superuser): `psql -v
      ON_ERROR_STOP=1 -d postgres -f <StateDir>/bootstrap.sql`.
  - It then waits at `re-check / skip / quit` (AC-10) and re-runs Detect.
    When the database is `ok`, `bootstrap.sql` is deleted (v0.5: via
    the step's transient `bootstrap-file` artifact, which is `ok` when
    there is no file or while the bootstrap is pending, `outdated` once
    the database is `ok` and the file still exists, and is never
    recorded in the manifest). If it is still not
    `ok`, the step reports what is missing; for "auth failed after
    bootstrap" the detail says the role already existed with another
    password ("enter that password, or change it yourself with `ALTER
    ROLE`"). The wizard never runs `sudo` or `psql`.
  - **One SQL source** (resolves v0.1 AC-21 vs plan Design 5): the
    statements live in one embedded psql script,
    `deploy/initdb/app-role.psql`, written with psql variables
    (`:"app_user"`, `:"app_db"`, `:'app_pw'`) and idempotent: role and
    database are created via `SELECT format('CREATE ROLE %I LOGIN
    PASSWORD %L', …) WHERE NOT EXISTS (…) \gexec` and the same for `CREATE
    DATABASE … OWNER` (`CREATE DATABASE` cannot run inside `DO`), then
    `REVOKE ALL ON DATABASE … FROM PUBLIC`, `\connect :"app_db"`, `CREATE
    EXTENSION IF NOT EXISTS vector`, `REVOKE CREATE ON SCHEMA public FROM
    PUBLIC`, `GRANT ALL ON SCHEMA public TO :"app_user"`.
    `deploy/initdb/01-app-role.sh` becomes a thin wrapper that runs it
    with `psql -v app_user=… -v app_db=… -v app_pw=… -f app-role.psql`
    (the `.psql` extension keeps the Docker entrypoint from running it a
    second time on its own). The installer's `bootstrap.sql` is three
    `\set` lines with the real values followed by the embedded body
    verbatim. Role and database names are validated against
    `^[a-z_][a-z0-9_]{0,62}$` before rendering; the generated password
    alphabet needs no quoting. **v0.3:** only a password matching
    `^[A-Za-z0-9_.~-]{8,256}$` is ever rendered into `bootstrap.sql`.
    Generated passwords always match. For any other password (e.g. a
    user-typed one for an existing role without `vector`) the create
    path is `blocked` with "this password cannot be written into
    bootstrap.sql safely: let the installer generate one, or run
    deploy/initdb/app-role.psql yourself", because psql meta-command
    arguments treat backslashes and backquotes specially.
  - **`--yes` (v0.3).** Topology A with no DSN from `--pg-dsn`, `Env` or
    the env file takes the create path with the defaults (`localhost`,
    5432, `claude_memory`/`claude_memory`, `sslmode=disable` on
    loopback, generated password): `envfile` writes the DSN, `database`
    writes `bootstrap.sql`, prints the command, and ends `blocked:
    awaiting user action`. Dependent steps are blocked, independent
    ones run, and `install` exits 1. The next `install --yes` re-detects
    and continues. With a DSN, Detect decides: `ok` → keep; `nodb` or no
    `vector` → the create path with that DSN's password (alphabet rule
    above); `auth`/unreachable → the step fails (no re-ask under `--yes`).
    Topology B with no DSN fails the step with the flag name (AC-12).
  - **Pending bootstrap (v0.4).** A role that does not exist yet probes
    as `auth` (`28P01`). So while `<StateDir>/bootstrap.sql` exists and
    the probe says `auth`, `database missing` or no `vector`, the step is
    `blocked: awaiting user action` with the command printed again (exit
    1 under `--yes`; nothing regenerated), not a failure. The detail also
    names the "role already existed with another password" case. When
    the probe is `ok` and the file still exists, the run deletes it.
  - Verify: golden `bootstrap.sql` (sentinel values); integration test
    (tag `integration`; v0.4: by default `psql` runs **inside** the
    testcontainers pgvector container via `Exec`; with
    `MEMORY_TEST_PG_ADMIN_DSN` and host `psql`, on the host; skipped when
    neither is available) runs the rendered file twice as the
    superuser on a throwaway database and role; afterwards the app role
    can run `postgres.New`; a unit test runs `--yes` twice before the
    command is run (both `blocked`, same password) and once after (file
    deleted); a test asserts the `.sh` wrapper references
    the `.psql` file; the call-log guard asserts no `sudo`/`psql` argv.
- AC-22 [S2] (Ubiquitous): Generated passwords shall be 32 bytes from
  `crypto/rand`, base64url-encoded with no padding (alphabet
  `[A-Za-z0-9_-]`: URL-, shell- and SQL-literal-safe). Verify: unit test
  of alphabet and length.
- AC-25 [S2] (Ubiquitous): The `migrate` step shall call the same code
  path as `claude-memory migrate` (AC-3) through `DBProber.Migrate(ctx,
  dsn)`, with the DSN from the step state (what the `topology`/`database`
  steps resolved and `envfile` wrote), never from the process environment
  loaded at startup. Its Detect is the `pg.schema` introspection (Design
  6 of the plan): every embedded migration's objects present → `ok`, so a
  no-op run applies nothing. Verify: engine test asserts that `migrate`
  precedes `hooks.settings` and uses the step-state DSN; integration
  test.
- AC-26 [S2] (Unwanted): IF the user reconfigures the topology or DSN on a
  re-run, THEN the wizard shall warn that existing records stay in the
  old database. It prints the `pg_dump`/`pg_restore` hint from DEPLOY.md
  and does not migrate data. Verify: prompter transcript shows the
  warning before the new DSN is written.

#### 6.5.1 Topology C — Postgres in Docker [S3, deferred]

Kept so slice 3 starts from a design, not from nothing. Re-plan before
work starts; the reviewer suggests C2 may shrink to "write `deploy/.env`
with generated passwords and print DEPLOY.md's steps", because the server
already runs `deploy/` from a clone.

- AC-23 [S3] (Ubiquitous): For C1 (`docker-local`), write a local-profile
  bundle to `<ShareDir>/docker/`: image `pgvector/pgvector:pg16`, compose
  project `claude-memory-local`; port `127.0.0.1:${PORT}:5432` (default
  55432, busy port → ask); named volume `claude-memory-pgdata` (no host
  path, no chown/sudo); the shared `initdb` (AC-21); a `pg_hba.conf` with
  exactly three lines — `local all postgres trust` (initdb over the
  socket), `host all postgres 127.0.0.1/32 trust` (the `pg_isready`
  healthcheck), `host claude_memory claude_memory 0.0.0.0/0
  scram-sha-256` — and reject for everything else; `.env` at 0600 with
  generated passwords. Requires `docker compose version`; after
  confirmation runs exactly `docker compose -p claude-memory-local -f
  <dir>/docker-compose.yml up -d`, then polls health for up to 60 s.
  Bridge networking, not host networking (unreliable on Docker Desktop
  for macOS). Container env holds `POSTGRES_PASSWORD`/`APP_DB_PASSWORD`,
  readable via `docker inspect` by the `docker` group: accepted for a
  single-user tool; `docker secrets` is the upgrade path. Verify: golden
  bundle; exact argv; Docker unavailable → `blocked`.
- AC-24 [S3] (Ubiquitous): For C2 (`docker-server`), render the server
  bundle into a chosen directory (default `./claude-memory-server`,
  refusing a non-empty directory unless it is our bundle): `deploy/`
  files byte-identical, a 0600 `.env` with `POSTGRES_BIND_IP` (asked) and
  generated passwords, and `SERVER-STEPS.txt` rendered from DEPLOY.md.
  Print the steps; never run `ssh`, `scp`, `rsync` or `docker`; then
  continue as B with the DSN prefilled. Verify: golden bundle; call-log
  guard.

### 6.6 Env file and secrets
- AC-27 [S1] (Ubiquitous): `config.ParseEnvFile(path)` shall return the
  parsed `KEY=VALUE` pairs in order, the file mode, and format findings:
  `export-prefix`, `quoted-whole-value` (quotes that wrap the entire
  value; convertible), `unparseable-value` (anything else the plain
  format cannot represent safely, e.g. `$(...)`, backslashes, unbalanced
  quotes; never converted), `duplicate`, `no-equals`,
  `group-world-readable`. It does not touch the process environment.
  `LoadFromFile` shall be reimplemented on top of it with unchanged
  behavior. Verify: existing config tests stay green; new table tests
  cover each finding.
- AC-28 [S2] (Ubiquitous): The `envfile` step shall write
  `<ConfigDir>/env` (dir 0700, file 0600) in plain `KEY=VALUE` format
  with no quotes and no `export`. It sets the managed keys
  (`MEMORY_PG_DSN`, `MEMORY_OLLAMA_URL`, `MEMORY_OLLAMA_MODEL`,
  `MEMORY_EMBED_MAX_TOKENS`, and `MEMORY_PR_INGEST_REPOS` when given) in
  place. (v0.4) A managed key is written only when its owner set its
  value in this run (from a flag, the env file, `Env`, the manifest, a
  prompt, or — v0.5 — the owner's documented default or a generated
  value, e.g. the AC-32 Ollama defaults or the AC-21 `--yes` create-path
  DSN); otherwise its line is left untouched, e.g. `MEMORY_PR_INGEST_REPOS` in
  a build without the `jobs` step, or `MEMORY_EMBED_MAX_TOKENS`, which is
  never prompted. Every other line, comment and unknown key is preserved in its
  order. A file with `export-prefix` / `quoted-whole-value` findings is
  `modified` and, with consent (interactive only), rewritten to the plain
  form; a line with `unparseable-value` is left untouched and reported.
  When the DSN's password is recoverable from a `user:pw@` form but not
  URL-encoded (e.g. a base64 password with `/`), the rewrite re-encodes
  it. Verify: golden tests (fresh, update-in-place, unknown keys
  preserved, `export` form converted, quoted value unquoted, unparseable
  kept, base64 `/` re-encoded, idempotent second run = no write).
- AC-29 [S2] (Unwanted): IF the env file exists with group/world
  permission bits, THEN Detect shall report `outdated: mode 0644, the
  binary refuses to load it`, and the default choice is `apply` (chmod
  0600 without changing content). Verify: temp `Paths` test.
- AC-30 [S1 redactor / S2 sentinel over install] (Ubiquitous): The DB
  password shall travel only from the no-echo prompt,
  `--pg-password-stdin` or an existing env file, into the env file and
  (topology A) the temporary `bootstrap.sql`. It shall never appear in
  command argv, in stdout/stderr (including the dry-run diff), the
  manifest, a settings.json backup, `claude mcp` arguments, job units or
  slog output. All output passes through one `Redactor` sink that:
  - always masks `scheme://user:…@` userinfo by pattern;
  - masks registered secret literals (and their `url.QueryEscape` /
    `PathEscape` forms) only when the secret is **≥ 8 characters**, so a
    short password cannot mangle unrelated text (a shorter password is
    still masked inside DSNs by the pattern);
  - is in place **before** a secret is read: the prompt adapter registers
    the secret as it returns it, before any error path can print.

  Verify: unit tests for the min-length rule and pattern; a sentinel test
  runs doctor (S1) and every step (S2) with password `S3ntinel-pw-$@:/x`
  and greps all captured output, written files other than the env file,
  `bootstrap.sql` and the env-file backup `env.bak.claude-memory.<ts>`
  (a 0600 copy of the previous env file that install keeps when it
  overwrites a hand-edited one; it holds the old password by design), and
  the Runner argv for the sentinel and its percent-encoded form.
- AC-31 [S2] (Ubiquitous): `--pg-dsn` containing a password shall be
  rejected (exit 2: `pass the password via the prompt or
  --pg-password-stdin, not argv`). `MEMORY_PG_DSN` in `Env` is accepted
  for `--yes`. With `--pg-password-stdin`, `install` reads exactly one
  line (≤ 4 KiB, 5 s deadline, trailing newline stripped, any other
  newline rejected) **before** Detect and before checking for a TTY;
  without it, stdin is never read in a non-TTY session (AC-11). Verify:
  flag tests; a stdin that never closes still finishes within the
  deadline.

Accepted exposures (stated, not fixed): `MEMORY_PG_DSN` is in the
environment of every `claude-memory` process and readable via
`/proc/<pid>/environ` by the same uid; the env file is plaintext at 0600.
Both are acceptable for a single-user tool.

### 6.7 Ollama
- AC-32 [S1 prober / S2 step] (Ubiquitous): The `ollama` step shall probe
  `GET /api/version` and `GET /api/tags` at the configured URL (default
  `http://127.0.0.1:11434`, or `--ollama-url`, or one asked for). When
  the model (`bge-m3` unless `MEMORY_OLLAMA_MODEL` is set) is missing, it
  offers to pull it via `POST /api/pull` (streamed progress, so it also
  works against a remote Ollama). It then verifies with one `/api/embed`
  call that returns exactly 1024 dimensions, because the schema is
  `VECTOR(1024)`. Ollama absent → `blocked`, with the brew/Linux install
  hint (printed only). Verify: prober tests against `httptest.Server`;
  wrong dimension → step fails with `model returns N dims, schema needs
  1024`.
- AC-33 [S2] (Event-driven): WHEN the Ollama URL is not loopback, the
  wizard shall warn, and ask for confirmation, with this content: prompts
  are sent over the network for embedding; **the prompt hook gives up
  after `MEMORY_HOOK_TIMEOUT` (800 ms by default), and a slow remote
  Ollama — e.g. on CPU, where DEPLOY.md measured 0.23 s for 15 tokens and
  2.3 s for 150 — makes the hook time out and inject nothing at all**;
  `memory_search` via MCP still works. Doctor's `ollama.embed > 500 ms`
  warn is the watchdog. Under `--yes` the warning is printed and the URL
  accepted. Verify: prompter transcript contains `800 ms` and `inject
  nothing`.

### 6.8 Claude Code integration
- AC-34 [S1 embed / S2 outside-checkout run] (Ubiquitous): Assets shall
  be embedded. Two new Go packages, `integration` (`integration/embed.go`)
  and `deploy` (`deploy/embed.go`), expose `embed.FS` values with the hook
  scripts, skills, the CLAUDE.md section, launchd and systemd templates
  and `deploy/initdb/app-role.psql`; the S3 compose bundle and the
  dotfile `.env.example` (which must be named explicitly; a directory
  pattern skips dotfiles) are added in S3. `install` and `doctor` use
  only these and never read the repo working tree. Verify (S1): a test
  lists every path the code references and asserts it is in the FS.
  Verify (S2): a test builds the binary to a temp dir, then runs `install
  --dry-run --yes --topology remote` with `cmd.Dir` set to another temp
  dir outside the repo and `HOME` to a third, with `MEMORY_PG_DSN` in the
  child's env; the plan lists all assets.
- AC-35 [S2] (Ubiquitous): The `binary` step shall install the running
  executable (`Paths.Self`: `os.Executable`, symlinks resolved, computed
  in `main.go`) to `--bin-dir` at mode 0755 by atomic copy and rename. If
  the running binary already is the target, the step is `ok`. **v0.4:
  the target is sticky:** an explicit `--bin-dir`, else the binary path
  the manifest recorded, else `~/.local/bin`. `doctor` (which has no
  `--bin-dir`) uses the same rule for every check that names the binary
  (hook scripts, MCP command, jobs, `binary.version`), so after `install
  --bin-dir X` neither a plain `install` re-run nor `doctor` falls back
  to `~/.local/bin`. A new explicit `--bin-dir Y` installs to Y, records
  Y and leaves the old file with an info note. It refuses
  (exit 2, "build to a stable path: `make install`") when `Paths.Self` is
  under `os.TempDir()`, `GOTMPDIR` (v0.4) or `GOCACHE` (a `go run` binary). It warns when
  `--bin-dir` is not on PATH, and on darwin when the file carries
  `com.apple.quarantine` (hint: `xattr -d com.apple.quarantine <path>`).
  Two facts, stated so nobody adds machinery: replacing the file by
  rename is safe while the old binary runs (Claude Code's `serve`, a job)
  because the old inode lives on; Go ad-hoc signs darwin/arm64 binaries,
  so a copied binary runs without `codesign`. Templates (hook scripts,
  jobs, MCP) render the target path. Verify: temp `Paths` test; same-file
  case; temp-dir refusal.
- AC-36 [S2] (Ubiquitous): The `hooks.scripts` step shall write
  `user-prompt-submit.sh` and `session-end.sh` to
  `<ClaudeDir>/hooks/claude-memory/` at mode 0755, with the binary path
  rendered as the default of `CLAUDE_MEMORY_BIN`. Their sha256 goes in
  the manifest. **v0.3:** "the embedded version" for hook scripts means
  the embedded script **rendered** with the binary path (one shared
  render function used by both `doctor` and Detect). An unrecorded
  script that is byte-identical to the raw embedded script (a manual
  install of this version, `$HOME/.local/bin` default) is `outdated` in
  Detect, because its content is exactly ours. Doctor reports it `ok
  (manual install)`. Verify: temp `Paths`; hash recorded; doctor right
  after install reports `pass` for `hooks.scripts`, and a second
  `--upgrade` writes nothing.
- AC-37 [S1 library / S2 step] (Ubiquitous) — **rewritten in v0.2 (D4)**:
  The `hooks.settings` step shall ensure exactly one of our entries under
  `hooks.UserPromptSubmit` and `hooks.SessionEnd` in
  `<ClaudeDir>/settings.json`. The desired entry is
  `{"hooks":[{"type":"command","command":"<abs path to script>","timeout":5}]}`.
  - **Codec.** Parsing uses `json.Decoder` with `UseNumber()` and records
    `InputOffset()` around every value, so each scalar and each untouched
    object/array is kept as its exact input bytes. Output re-emits
    untouched subtrees byte-for-byte; only the event arrays we change are
    re-encoded. Key order, unknown keys, number spellings
    (`12345678901234567890`) and string escapes (`"café \/ x"`,
    `\uXXXX`) survive. A duplicate key at any level is refused (last-wins
    re-encoding would silently drop one).
  - **Indentation.** When a write is needed, the indentation unit is
    detected from the first indented line (tab or N spaces) and used for
    the re-encoded arrays; a file with no indented line uses 2 spaces.
    A trailing newline is preserved or added.
  - **No-op = no semantic change.** Detect compares the parsed hook
    arrays. When exactly one of our entries per event is present and it
    is the desired one, the step is `ok` and nothing is written and no
    backup is made, regardless of formatting (tabs, 4 spaces, escapes).
  - **Our entries and what we may change** ("marked" = recorded): the
    manifest records the canonical JSON of each entry install wrote. For
    each event:
    - our entry equal to desired → `ok`;
    - our entry equal to the manifest's recorded entry but not to desired
      (e.g. the script path moved with `--bin-dir`) → `outdated` → apply
      replaces it in place, preserving its group's other keys (e.g. a
      `matcher`): the inner `hooks` element is edited, not the group;
    - our entry (by identity, §2) that matches neither — a user-raised
      `timeout`, the legacy `$HOME/...` form from
      `integration/settings.snippet.json`, an unrecorded hand install —
      → `modified`: kept by default, reported as `warn: drift` under
      `--yes`/`--upgrade`, replaced only after the AC-6 overwrite
      `Confirm`;
    - several of our entries for one event → `modified` (duplicates);
      collapsing them to one needs the same `Confirm`;
    - absent → apply adds the desired entry; the file or the `hooks` key
      is created when missing.
  - No marker key is added inside hook entries: Claude Code validates the
    settings schema, and an unknown key could invalidate the user's file.
    Identity + manifest record is the mark.
  - Every other hook entry and event is untouched.

  Verify: golden tests under `testdata/settings/` for `missing`,
  `empty-object`, `no-hooks-key`, `other-events`,
  `other-hooks-same-event`, `ours-identical`, `ours-recorded-outdated`,
  `ours-legacy-$HOME` (modified), `ours-user-timeout` (modified, kept
  under `--yes`), `ours-duplicated`, `ours-under-matcher`,
  `unknown-top-level-keys-order`, `tab-indented`, `four-space`,
  `unicode-escapes-unchanged-noop`, `big-number-unchanged`,
  `duplicate-key` (refuse); each runs twice (second run = no write) and,
  for S2, through uninstall.
- AC-38 [S1 library / S2 step] (Unwanted): IF `settings.json` is not
  valid strict JSON (comments, trailing commas, truncated), has a
  duplicate key, or `hooks` / an event value has an unexpected type, or
  it is a symlink whose target lies outside `Paths.Home`, THEN the step
  shall fail with `refusing to edit <path>: <reason at line:col>; fix it
  or merge integration/settings.snippet.json by hand` and leave the file
  untouched. A symlink inside `Home` is followed: the target is edited and
  backed up next to itself. Verify: golden refusal cases; the file hash is
  unchanged.
- AC-39 [S2] (Ubiquitous): Before any write to `settings.json`, the step
  shall copy the original to `settings.json.bak.claude-memory.<UTC
  yyyymmddThhmmssZ>` at mode 0600, never over an existing backup (v0.7; one backup per write; pruning old
  backups is deferred, §12.1). Right before the rename it shall re-read
  the file and abort if its hash changed since Detect, in case Claude Code
  wrote it meanwhile. A half-applied `settings.json` cannot exist (single
  rename). Verify: backup exists and matches the original; a
  concurrent-change fake makes the step fail with "changed during
  install, re-run".
- AC-40 [S1 detect / S2 register] (Ubiquitous) — **rewritten in v0.2
  (D3)**: MCP registration shall be **detected by reading** `Paths.
  ClaudeJSON` (`$CLAUDE_CONFIG_DIR/.claude.json`, default `~/.claude.json`)
  read-only, strict JSON, tolerant of a missing file or key: top-level
  `mcpServers["claude-memory"]` with `command == <installed bin>` and
  `args == ["serve"]` → `ok`; present with another `command` or `args` →
  `outdated`; the right command and args with extra `env` → `modified`
  (v0.7: kept unless the overwrite is confirmed); missing → `absent`. A `claude-memory` entry under
  `projects.<path>.mcpServers` (local scope, which overrides user scope in
  that project) is reported as `warn` by doctor. **`claude mcp get` and
  `claude mcp list` are never executed** (they spawn the server). The
  `mcp` step (S2) registers with `claude mcp add --scope user
  claude-memory -- <bin> serve`, and for `outdated` runs `claude mcp
  remove --scope user claude-memory` first. It never passes `-e` and never
  writes `.claude.json`. Without `claude` on PATH, the step is `blocked`
  and prints the command. Verify: reader tests against the WI-0
  `.claude.json` fixtures (absent file, no key, ok, other command,
  local-scope shadow); Runner fake asserts the `add`/`remove` argv for
  absent/outdated and no `claude` call for `ok`; AC-67.
- AC-41 [S1 doctor states / S2 step] (Ubiquitous): The `skills` step shall
  copy each embedded skill directory (`remember`, `memory-digest`) to
  `<ClaudeDir>/skills/<name>/` and record per-file sha256. On a re-run, an
  installed file whose hash equals the manifest hash and differs from the
  embedded one is `outdated` and refreshed. An installed file that is
  byte-identical to the embedded one but not recorded is `ok` and
  recorded. Any other difference (edited after install, or a hand install
  of an older version) is `modified`: keep by default, and offer `show
  diff / overwrite (backup .bak) / keep`. The first run after a hand
  install says so explicitly ("files from a manual install that differ
  from this version are kept; choose overwrite to adopt ours"); a table
  of known historical hashes is deferred (§12.1). In S1, doctor compares
  against the manifest when present, else against the embedded version.
  Verify: temp `Paths` tests for each state.
- AC-42 [S1 library / S2 step] (Ubiquitous): The `claude-md` step shall
  insert or refresh the memory section between `<!-- BEGIN claude-memory
  -->` and `<!-- END claude-memory -->` in the target file, leaving all
  text outside the markers unchanged. The target is
  `<ClaudeDir>/CLAUDE.md` by default, or `--claude-md PATH` (e.g.
  `acme/CLAUDE.md`, §13 #1). The section text lives in its own
  embedded file, `integration/claude-md-section.md`, worded
  location-neutrally. The step shows a diff and requires consent
  interactively. **`--yes` writes the default user-level file
  unconditionally** (even when `Home` itself is a git repo, e.g.
  dotfiles — it is the user's own file); **it never writes an explicitly
  given `--claude-md` path inside a git repository** (detected by walking
  up for `.git` as a file or a directory, so worktrees and submodules
  count; no `git` exec), and reports it as skipped with the reason.
  Unbalanced or duplicated markers → refuse. A pre-existing hand-pasted
  section (heading `## Shared semantic memory (\`claude-memory\`)` without
  markers) is `modified` with the hint to delete it before installing the
  block. Verify: golden tests (insert at end, refresh between markers,
  idempotent, unbalanced refused, hand-pasted detected, `--yes` + git
  path skipped, `--yes` + default target in a git `Home` written).

### 6.9 Scheduled jobs
- AC-43 [S2] (Ubiquitous): The `jobs` step shall install two jobs:
  `cleanup` (daily 07:15 local) and `ingest-pr` (daily 07:00 local).
  `ingest-pr` is installed only when `MEMORY_PR_INGEST_REPOS` is set or
  given; the prompt notes that only Azure DevOps is supported today
  (backlog 4). The jobs invoke the binary directly (no
  `run-with-env.sh`; the binary loads the env file itself) and set
  `PATH` explicitly to the directories that contain `claude`, `git` and
  `az` as found at install time (preferring a stable shim directory such
  as `~/.volta/bin` or a `current` symlink over a versioned `nvm` path
  when detectable), plus `/usr/bin:/bin`. Logs go to
  `<StateDir>/<job>.log`. Verify: golden unit files per backend.
- AC-44 [S1 doctor / S2 install] (Ubiquitous): On launchd, plists shall be
  written to `<LaunchAgentsDir>` with the existing labels
  `io.github.claude-memory.{cleanup,ingest-pr}` (§13 #6), so
  hand-installed jobs are recognized rather than duplicated. Plists are
  rendered with absolute paths and an `EnvironmentVariables` PATH (no
  `__HOME__` placeholders). Jobs are loaded with `launchctl bootout
  gui/<uid>/<label>` (tolerating "not loaded" per the WI-0 fixture)
  followed by `launchctl bootstrap gui/<uid> <plist>`, and detected with
  one `launchctl print gui/<uid>/<label>` per job plus a read of the plist.
  A plist under our label whose `ProgramArguments[0]` is
  `…/run-with-env.sh` is a recognized legacy form → `outdated` → apply
  replaces it; doctor (S1) already reports it (AC-58 `jobs`). **v0.3:**
  Detect compares the plist with the manifest-recorded hash and with the
  plist rendered for the current `JobSpec` (binary path, arguments,
  schedule, PATH). Recorded, unedited and different from the rendering
  (e.g. a moved `--bin-dir`) → `outdated`, so `--yes`/`--upgrade`
  re-renders and reloads it. Unrecorded and different, or edited after
  install → `modified`. Verify: Runner fake argv; golden plists; legacy
  plist fixture → `outdated`; recorded plist with an old binary path →
  `outdated` → reloaded under `--yes`.
- AC-45 [S2] (Ubiquitous): On systemd, the step shall write
  `<SystemdUserDir>/claude-memory-{cleanup,ingest-pr}.{service,timer}`
  (`Type=oneshot`, `Environment=PATH=…`, `OnCalendar`,
  `Persistent=true`, `RandomizedDelaySec=10m`), then run `systemctl
  --user daemon-reload` and `systemctl --user enable --now <timer>`, with
  the bus environment from AC-16 when it was needed. Detection uses one
  `systemctl --user show <timer> -p UnitFileState,ActiveState` per job.
  When `loginctl show-user <user> -p Linger` reports `no`, the step prints
  the `loginctl enable-linger` hint and does not run it. Verify: Runner
  fake argv; golden units.
- AC-46 [D] (Ubiquitous): *Deferred (§12.1).* With cron, the step would
  read `crontab -l`, replace or append a managed block `# BEGIN
  claude-memory` … `# END claude-memory` with one `PATH=`-setting line per
  job, preserve all other lines, and write with `crontab -` only when the
  content changed; install and doctor would warn when no `cron`/`crond`
  process runs.

### 6.10 Namespaces and seed
- AC-47 [S2] (Ubiquitous): The `namespaces` step shall call
  `namespace.Init` when the file is absent (and `namespace.Add` for added
  mappings), through a port. Interactively it offers to add `NAME=GLOB`
  mappings. As suggestions it shows up to 5 distinct parent directories
  derived from `<ClaudeDir>/projects/*` names. The encoding (`/` → `-`) is
  lossy, so a name is decoded by splitting on `-` and greedily joining
  segments, accepting only a decoding in which every prefix exists
  (`Stat`); ambiguous or non-existent decodings are not shown. It
  finishes with `namespaces which` for each suggested path. An existing
  valid file is `ok`; an unparseable one is `modified`, reported with the
  parse error and never overwritten. Verify: temp `Paths` test incl. a
  hyphenated directory; broken file untouched.
- AC-48 [D] (Optional): *Deferred (§12.1).* A `seed` step offered only
  with `--seed-file` or `./seed/facts.yaml`, default `skip`, never part of
  `--yes` without `--seed-file`.

### 6.11 Re-runnability, manifest, upgrade, uninstall [S2]
- AC-49 [S1 read / S2 write] (Ubiquitous): The manifest (`install.json`,
  0600) shall record `schema: 1`, `binary_version`, `platform`,
  `topology`, `jobs_backend`, `claude_config_dir` (effective
  `Paths.ClaudeDir`), `installed_at`, `updated_at`, and per artifact
  `{step, kind (file|dir|settings-hook|mcp|launchd|systemd|md-block|
  env-key), path or identity, sha256 as written, version}`; a
  `settings-hook` artifact also stores the canonical entry JSON (AC-37).
  It never stores secrets. A re-run that sees a different effective
  Claude config dir warns before Detect. A corrupt manifest is treated as
  absent, backed up as `install.json.corrupt.<ts>`, and reported.
  Verify: schema test; sentinel test (AC-30).
- AC-50 [S2] (Ubiquitous): A re-run with nothing changed shall perform
  zero writes (including the manifest) and zero mutating commands, and
  shall print a status table with every step `ok`. Verify: two
  consecutive `--yes` runs on a temp `Paths` with fakes; the second run's
  FS write count and mutating Runner count are 0. (v0.5) Also with a
  shell `MEMORY_PG_DSN` that differs from the env file's: 0 writes and
  one drift note (AC-5 step 1).
- AC-51 [S2 identity rules; heuristics D] (Ubiquitous): Without a
  manifest, Detect shall not create duplicates of a hand install done per
  `integration/INSTALL.md`: our hook entries are recognized by identity
  (AC-37), launchd jobs by label (AC-44), MCP by name (AC-40); files
  byte-identical to the embedded version are `ok` and recorded (v0.4: for
  hook scripts the embedded version is the **rendered** script, AC-36; a
  hook script equal to the raw embedded script is `outdated` and
  replaced); anything else is `modified` (kept unless the user confirms
  an overwrite).
  Further adoption heuristics are deferred (§12.1). Verify: fixture HOME
  replicating INSTALL.md steps 2–8; no duplicate hook entry or job after
  `--yes`.
- AC-52 [S2] (Ubiquitous) — **rewritten in v0.2 (D6 #8)**: `install
  --upgrade` shall be exactly `install --yes`: one code path, the AC-6
  defaults (`outdated → apply`, `modified → keep`). It therefore installs
  the binary when invoked from a different path, migrates when the schema
  is behind, refreshes `outdated` artifacts, reloads changed jobs,
  re-registers MCP only when the command changed, and runs doctor.
  `modified` artifacts are listed as `warn: drift` with exit code still 0.
  Verify: manifest with older hashes → refreshed; user-edited skill and
  user-raised hook timeout → kept and listed; `--upgrade` and `--yes`
  produce identical plans for the same fixture.
- AC-53 [S2 `--skip`; `--only` D] (Ubiquitous): `--skip` shall remove the
  named steps from consideration; a step requiring a skipped step is
  `blocked` with that reason. `--only` is deferred (§12.1). Verify:
  `--skip jobs,claude-md` touches neither.
- AC-54 [S2] (Ubiquitous): `uninstall` shall reverse the manifest's
  artifacts in reverse order:
  - remove our recorded hook entries (deleting an event key whose array
    becomes empty, and the `hooks` key only if the manifest records that
    install created it), with the backup and refusal rules of AC-38/AC-39;
    a hook entry now `modified` is kept and listed;
  - `claude mcp remove --scope user claude-memory`, only when
    `.claude.json` still shows our command;
  - unload and delete jobs (`launchctl bootout` / `systemctl --user
    disable --now` + `daemon-reload`);
  - remove the CLAUDE.md block (markers included);
  - delete hook scripts and skills whose hash still matches the
    manifest, listing any that differ (default keep);
  - remove the installed binary last, from the path the manifest
    recorded and only when its hash still matches (v0.3: no `--bin-dir`
    flag on `uninstall`; a repo build running `uninstall` is skipped);
    unlinking the running executable is safe on darwin and linux.

  Each reversed artifact is removed from the manifest as it goes; kept
  ones stay. **v0.3:** `env-key` and `dir` artifacts are *retained*: never
  reversed (the env file and our directories are kept), but dropped from
  the manifest. When every other artifact has been reversed, the manifest
  is deleted, so a later `doctor` or `install` does not see a stale
  topology or artifacts that do not exist. A `settings.json` that install
  created (recorded on its hook artifacts) is deleted only when the
  unmerge leaves `{}`. Settings backups, `*.bak` files and `install.lock`
  are left in place. The env file, `namespaces.yaml`, `<StateDir>` and all
  DB data are kept. **v0.4 `dir` artifacts:** only directories install
  created are recorded. Those under the Claude config dir
  (`hooks/claude-memory`, `skills/<name>`) are removed after their
  files, only when empty. `<ConfigDir>`, `<StateDir>` and `<BinDir>` are
  retained (dropped from the manifest, never removed). Parent
  directories created on the way are not recorded and stay. Verify:
  - golden after-uninstall `settings.json` equals the pre-install golden
    for the "other hooks present" case;
  - Runner argv;
  - manifest absent after a full uninstall, trimmed after a partial one;
  - an install → uninstall run on a temp `Paths` leaves a tree equal to
    the pre-install tree, excluding the kept files above and (v0.4) the
    retained directories and unrecorded parents when empty;
    `hooks/claude-memory` and `skills/<name>` are gone.
- AC-55 [D] (Ubiquitous): *Deferred (§12.1).* `--purge-config` (also
  delete env file, `namespaces.yaml`, manifest, state dir) and
  `--purge-data` (C1 only, `docker compose -p claude-memory-local down -v`
  after the user types `delete`; never touches B or C2 data).
- AC-56 [S2] (Ubiquitous): `uninstall` shall remove nothing that is not in
  the manifest. With no manifest, it lists what it detects and what to
  remove by hand, and exits 0. Verify: temp `Paths` with unrecorded
  artifacts only; FS write count 0.

### 6.12 `doctor` [S1]
- AC-57 [S1] (Ubiquitous): `doctor` shall be read-only. It never prompts,
  never applies migrations (it uses `postgres.Open`), never pulls models,
  never writes files, and **never executes the registered MCP command or
  any `claude mcp` subcommand** (AC-67). It runs with a read-only Runner
  and an FS without write access. Verify: a doctor test asserts zero
  writes and zero mutating commands across all checks; AC-67.
- AC-58 [S1] (Ubiquitous): `doctor` shall run these 23 checks, each with
  its status on failure and a one-line remedy (in S1 the remedy names a
  manual action or `claude-memory migrate`; from S2 it can name
  `claude-memory install`).

  | id | checks | failure status |
  |---|---|---|
  | `binary.version` | version, path, `--bin-dir` on PATH; another `claude-memory` earlier on PATH with a different version | info / warn |
  | `env.file` | exists | fail |
  | `env.perms` | mode has no group/world bits | fail |
  | `env.format` | AC-27 findings; required keys parse; a DSN that does not parse (e.g. base64 password with `/`) gets the remedy "URL-encode the password" | fail if DSN unusable, else warn |
  | `pg.connect` | DSN parses, connect + ping, classified error (AC-20); detail includes whether the connection uses TLS (`pg_stat_ssl`) | fail |
  | `pg.latency` | ping RTT | warn > 100 ms |
  | `pg.vector` | extension installed (+ version) | fail |
  | `pg.schema` | `records` exists, objects of every embedded migration present (0002 `namespace`); unknown newer objects → info | fail if missing; warn if behind → `claude-memory migrate` |
  | `ollama.reachable` | `/api/version` | fail |
  | `ollama.model` | model in `/api/tags` | fail |
  | `ollama.embed` | one embed, 1024 dims, latency | fail on dims; warn > 500 ms (the hook budget, AC-33) |
  | `tools.git` | `git` on PATH | warn (staleness off) |
  | `tools.claude` | `claude` on PATH (and, S2, on the recorded job PATH) | warn (no session extraction / MCP CLI) |
  | `tools.az` | `az` on PATH when PR repos configured | warn |
  | `mcp.registered` | `.claude.json` → `mcpServers["claude-memory"]` (AC-40), file read only; local-scope shadow entry | fail if absent; warn if command differs or shadowed |
  | `hooks.scripts` | present, executable, hash vs manifest (else vs embedded) | fail missing; warn modified/outdated |
  | `hooks.settings` | valid strict JSON; exactly one of our entries per event; its command path exists (`$HOME` expanded for the legacy form); legacy/drifted entries; duplicates in `settings.local.json` / project settings (AC-70) | fail; warn duplicates/drift |
  | `skills` | present; hash vs manifest (else vs embedded) | warn |
  | `claude-md` | managed block present in the recorded target (default target when no manifest) | info |
  | `namespaces` | file parses; resolution + provenance for cwd | warn on parse error; info |
  | `jobs` | launchd: plist present, loaded (`launchctl print`), last exit status; **warn** when `ProgramArguments[0]` is not the installed binary (e.g. `run-with-env.sh`) and when the job PATH lacks the directories of `claude`, `git`, `az`; S2 adds systemd and the backend-differs-from-manifest warning; on linux in S1 → `info: not checked yet` | warn |
  | `dirs.state` | `<StateDir>` writable (checked with `access(2)`, no write) | warn |
  | `manifest` | present; artifacts it lists exist | info (absent: "hand install") / warn |

  Checks that depend on a failed check are `skip` with `because <id>
  failed`. Verify: one test per check id with fakes, covering pass and
  each failure branch.
- AC-59 [S1] (Ubiquitous): Exit codes: 0 when no check is `fail` (and,
  with `--strict`, no `warn`), 1 otherwise, 2 for usage errors, 3 when
  doctor cannot start (`HOME` unset or relative). Independent checks run
  **concurrently**; each is bounded by `--timeout` (default 3 s) and the
  whole run by `--deadline` (default 10 s), after which unfinished checks
  report `fail: timed out`. A dependent check starts when its
  prerequisite finishes, so the worst case is the longest dependency
  chain (`pg.connect` → `pg.schema`, 6 s), not the sum. Verify: fake
  hanging probers; the wall-clock bound is asserted with context
  deadlines; a healthy-fake run completes in well under 1 s.
- AC-60 [S1] (Ubiquitous): `doctor --json` shall print one JSON object:
  `{"schema":1,"version":"…","platform":{"os","arch","jobs","config_dir",
  "bin"},"ok":bool,"summary":{"pass":n,"fail":n,"warn":n,"info":n,
  "skip":n},"checks":[{"id","title","status","detail","remedy",
  "duration_ms"}]}`. Every key is always present (`remedy: ""` when
  none), checks are in table order, no ANSI codes. The DSN appears only
  redacted. Verify: golden JSON with durations zeroed and `detail`
  excluded from comparison except for a few checks; sentinel test.
- AC-61 [D] (Optional): *Deferred (§12.1).* `doctor --latency[=N]` would
  run the installed `user-prompt-submit.sh` N times (default 20, max 100)
  with `cwd` = a temp dir and `session_id` = `doctor.probe` (which fails
  the hook's session-id pattern, so no stale-cache file is written),
  report p50/p95/max against the 300 ms budget with run 1 as `cold`, only
  after `env.*`, `pg.connect` and `ollama.embed` passed, and say that
  each run embeds one prompt.
- AC-62 [S2] (Ubiquitous): `install` shall run doctor's check registry
  in-process as its final step (unless `--no-doctor`) and print its
  summary, followed by "restart Claude Code sessions to load the changes"
  when this run applied a change (v0.6; was: always, "to load the hooks",
  unless WI-0 showed that a running session reloads `settings.json`; WI-0
  could not verify that, and running MCP and hook processes also read the
  binary, env file and namespaces at start). If
  doctor reports a fail, `install` exits 1. **v0.3 exception (narrowed
  in v0.4):** a `fail` from a check whose owning step the user skipped
  (`--skip`, choice `skip`, or `skip` at a blocked step's prompt), that
  is blocked by such a skipped step, or that this build does not install
  is printed as `fail (not installed: <step> skipped)` and does not
  change the exit code. A step whose artifacts were **kept** is not
  exempt: e.g. a kept hand-edited env file whose DSN fails `pg.connect`
  exits 1. A step left blocked under `--yes` is not exempt either.
  `doctor` run on its own is unaffected. Verify: engine test, incl.
  `--skip mcp` → `mcp.registered` fail printed, exit 0; kept `modified`
  env file + `pg.connect` fail → exit 1.

### 6.13 Verification infrastructure and documentation
- AC-63 [S1] (Ubiquitous): `internal/setup` tests shall use only fakes
  and explicit `Paths` built from `t.TempDir()` (never `t.Setenv`, so
  tests can run in parallel). They must never call real `brew`, `apt`,
  `launchctl`, `systemctl`, `docker`, `claude`, `psql` or `ollama`. This
  is enforced in two ways. A guard test asserts that `internal/setup` does
  not import `os/exec`, `net/http`, pgx or `golang.org/x/term` (via `go
  list -deps`; skipped under `testing.Short()` and run once in CI), and
  the fake Runner — scripted by `Script(match func(Cmd) bool, Result)`
  — fails the test on any unmatched command and records every call for
  the AC-18 deny-list guard. Verify: CI unit job.
- AC-64 [S1 libraries / S2 steps] (Ubiquitous): JSON, Markdown, env-file,
  `bootstrap.sql` and unit-file renders/merges shall have golden tests
  under `internal/setup/testdata/` with an `-update` flag. Every golden
  merge case also runs twice to assert idempotence. Verify: CI unit job.
- AC-65 [S1 migrate + pg checks / S2 e2e] (Ubiquitous): Integration tests
  (tag `integration`) shall cover `migrate` (AC-3) and the `pg.*` doctor
  checks (S1), and in S2 the `bootstrap.sql` run (AC-21) and an
  end-to-end `install --yes --topology local` against an existing test
  database (`MEMORY_TEST_PG_ADMIN_DSN` or testcontainers), with an
  `httptest` Ollama and fake Claude/jobs adapters. CI's integration job
  shall add `./internal/setup/...` and `./cmd/claude-memory/...`.
  Verify: green CI.
- AC-66 [S1 DEPLOY.md + skills wording / S2 docs] (Ubiquitous):
  Documentation changes:
  - S1: `DEPLOY.md`'s laptop env snippet is fixed to plain `KEY=VALUE`
    (no `export`, no quotes), and its password recipes switch to a
    URL-safe form (`openssl rand -hex 32`);
  - S1: `integration/skills/remember/SKILL.md` and
    `integration/skills/memory-digest/SKILL.md` drop their references to
    `acme/CLAUDE.md` / `acme/` repos and refer to "the memory
    section of your CLAUDE.md", matching the location-neutral
    `claude-md-section.md`;
  - S2: `integration/INSTALL.md` starts with `claude-memory install` /
    `doctor` / `uninstall`, and the manual steps move under "Manual
    install (reference)";
  - S2: `integration/bin/run-with-env.sh` is marked legacy, kept only so
    that old plists keep working until `install` replaces them;
  - S2: `DEPLOY.md`'s upgrade section points to `install --upgrade`; its
    server half stays manual (topology C is S3);
  - S1 and S2: `docs/specs/README.md` status is updated.

  Verify: review.

### 6.14 Added in v0.2
- AC-67 [S1] (Ubiquitous): Neither `doctor` nor any step's Detect shall
  execute the registered MCP server command or `claude mcp get|list`.
  Verify: (1) unit: a `.claude.json` fixture registers a sentinel command;
  doctor and every Detect run with a FakeRunner that fails on any call
  whose argv[0] is that command or whose argv contains `mcp get`/`mcp
  list`; (2) `cmd` test (S1): a built binary runs `doctor` with `HOME`
  pointing at a temp dir whose `.claude.json` registers a script that
  creates a marker file, and with a fake `claude` first on PATH that also
  creates a marker when invoked; after doctor, neither marker exists.
- AC-68 [S1] (Ubiquitous): `internal/setup` shall receive every path and
  environment input as values: `Paths` (§10) and `Env` (an allow-listed
  snapshot: `MEMORY_*`, `NO_COLOR`, `CI`, `PATH`, `XDG_RUNTIME_DIR`),
  both built once in `main.go` from `HOME`, `CLAUDE_CONFIG_DIR`,
  `--bin-dir`, `os.Getuid()`, `os.Executable()` and `os.Getwd()`. The
  package shall not call `os.Getenv`, `os.LookupEnv`, `os.Environ`,
  `os.UserHomeDir`, `os.Getuid` or `os.Executable`. `HOME` unset or
  relative → exit 2 (`install`) / 3 (`doctor`) before Detect. Verify:
  the guard test greps the package's non-test sources for those calls;
  a `Paths` builder table test (with and without `CLAUDE_CONFIG_DIR`,
  which also moves `.claude.json`).
- AC-69 [S2] (Unwanted): IF `install` or `uninstall` runs with effective
  uid 0 and without `--allow-root`, THEN it shall exit 2 with `refusing
  to install for root; run as your user (or pass --allow-root)`. Doctor
  is not affected. Verify: `cmd` test with an injected euid.
- AC-70 [S1 doctor / S2 install] (Ubiquitous): Doctor and the
  `hooks.settings` Detect shall parse read-only, when present,
  `<ClaudeDir>/settings.local.json`, `<Cwd>/.claude/settings.json` and
  `<Cwd>/.claude/settings.local.json`, and report `warn: duplicate
  claude-memory hook in <file>` for any of our entries there (it would
  fire the hook twice). They are never edited; the install summary
  repeats the warning with the file path. Verify: golden case
  `local-settings-duplicate`; FS write count 0.

## 7. Non-Functional Requirements
- **Safety**: No sudo. No package installs. No ssh. No writes to
  `.claude.json` and no execution of registered MCP servers. Every edit to
  a user-owned file outside our own directories (`settings.json`,
  `CLAUDE.md`, the env file) is shown as a diff, needs consent (or is an
  AC-6/AC-12 `--yes` default that never touches `modified` content), makes
  a backup or is atomic, and is idempotent.
- **Secrets**: AC-30, AC-31. The env file is 0600 inside a 0700
  directory; `bootstrap.sql` is 0600 in a 0700 directory and deleted once
  the database verifies. Accepted exposures are listed under §6.6.
- **Performance**: an all-`ok` re-run finishes in < 3 s, excluding
  network timeouts. `doctor` takes < 2 s on a healthy setup (probes run
  concurrently; no Node-based `claude` CLI call). Nothing here touches the
  hook hot path.
- **Portability**: darwin/linux, amd64/arm64. Builds via `make
  cross-build` (extended with darwin/amd64 and linux/arm64, same
  `LDFLAGS`).
- **Dependencies**: `golang.org/x/term` only (§4), added in S2.

## 8. Edge Cases
- `settings.json` is a symlink (dotfiles repo): edit the target, keep the
  link, back up next to the target. Target outside `Home` or not writable
  → refuse (AC-38).
- `settings.json` holds our entry under a `matcher` group → it is our
  entry; a replacement edits the inner `hooks` element and keeps the
  group's other keys. Our entry with a different `timeout` → `modified`
  unless the manifest recorded exactly that entry (AC-37); never reverted
  by `--yes`.
- `settings.json` is tab- or 4-space-indented, or uses `\uXXXX` escapes,
  and already has our entries → `ok`, no write (AC-37).
- Our hook entry also present in `settings.local.json` or a project's
  `.claude/settings*.json` → warn (AC-70).
- `$CLAUDE_CONFIG_DIR` is set → `ClaudeDir` and `ClaudeJSON` follow it.
  Doctor prints the effective directory; a re-run that sees a different
  one than the manifest warns (AC-49).
- `HOME` is unset or relative → exit 2 (install) / 3 (doctor) before
  Detect.
- Running as root → exit 2 unless `--allow-root` (AC-69). Running a `go
  run` binary → the `binary` step refuses (AC-35).
- The password contains `$`, `@`, `:`, `/`, `#` or spaces → URL-encoded in
  the DSN, and the env file is never shell-sourced by our jobs (AC-43).
  It contains a newline → rejected at the prompt. An existing env file
  with an unencoded base64 password → `env.format` remedy, re-encoded by
  the `envfile` rewrite (AC-28).
- The role already exists with a different password when
  `bootstrap.sql` runs → creation is skipped (idempotent), re-check still
  fails `auth`; the step says so (AC-21).
- `bootstrap.sql` left behind by an aborted run → overwritten on the next
  run; doctor's `dirs.state` reports `info: bootstrap.sql present (holds a
  password; delete it once the database works)`.
- No systemd user session bus (ssh) → retried with the user's bus address;
  no user instance at all (containers, some WSL) → `jobs` blocked with
  printed instructions, no cron fallback (AC-16).
- Running `install` with an older embedded asset than the manifest's
  version (downgrade) → the wizard warns `binary is older than installed
  assets` and defaults to `keep` for those artifacts.
- launchd plist present but not loaded (e.g. after a manual `launchctl
  bootout`) → `outdated` → `apply` loads it.
- The manifest is corrupt → treated as absent, backed up, warned (AC-49).
- `.claude.json` absent, unparseable or without `mcpServers` → `mcp`
  `absent` (unparseable additionally `warn` in doctor); never written by
  us.
- Ctrl-C during a prompt → no step applied yet, exit 130. During Apply →
  AC-9.
- The CLAUDE.md target given by `--claude-md` is inside a git repo → note
  that the file is probably shared or committed; `--yes` never writes it
  (AC-42).
- An `ollama pull` is interrupted → re-run resumes (Ollama's own
  behavior). The step is `absent` until the model is listed.
- The DB is reachable but the schema was created by a newer binary
  (unknown columns) → `pg.schema` `info`, not fail.

## 9. Data Model
No database change. New local files:
- `~/.config/claude-memory/install.json` (0600): manifest, AC-49 [S2
  writes, S1 reads].
- `~/.config/claude-memory/install.lock`: process-scoped `flock` target
  [S2].
- `~/.local/state/claude-memory/bootstrap.sql` (0600, temporary): AC-21
  [S2].
- Backups `settings.json.bak.claude-memory.<ts>` (one per write),
  `*.bak` for overwritten user-modified assets [S2].
- [S3] `~/.local/share/claude-memory/docker/` (C1 bundle, `.env` 0600).
- Changed repo files: `deploy/initdb/app-role.psql` (new, shared SQL),
  `deploy/initdb/01-app-role.sh` (wrapper) [S2].

## 10. Interfaces
Ports declared in `internal/setup` (consumer-side). Adapters live in
`cmd/claude-memory` or in their own packages, and are constructed only in
`cmd/claude-memory`. Values (`Paths`, `Env`, `PlatformInfo`) are plain
data built in `main.go`.

```go
// Values (AC-68). Built once in main.go; tests construct them directly.
type Paths struct {
    Home            string // $HOME, absolute
    ClaudeDir       string // $CLAUDE_CONFIG_DIR, else Home/.claude
    ClaudeJSON      string // $CLAUDE_CONFIG_DIR/.claude.json, else Home/.claude.json
    ConfigDir       string // Home/.config/claude-memory (env, namespaces.yaml, install.json, install.lock)
    StateDir        string // Home/.local/state/claude-memory (job logs, bootstrap.sql)
    ShareDir        string // Home/.local/share/claude-memory ([S3] docker bundle)
    BinDir          string // --bin-dir, else Home/.local/bin
    LaunchAgentsDir string // Home/Library/LaunchAgents (darwin)
    SystemdUserDir  string // Home/.config/systemd/user (linux)
    Cwd             string // for project settings (AC-70) and `namespaces which`
    Self            string // os.Executable, symlinks resolved
    UID             int
    EphemeralDirs   []string // [S2, v0.3] os.TempDir(), GOTMPDIR when set (v0.4), GOCACHE (else UserCacheDir/go-build): AC-35 refusal
}
type Env map[string]string // allow-listed snapshot (AC-68)

type PlatformInfo struct {  // data, not a port; detectPlatform lives in cmd
    OS, Arch, OSVersion string
    WSL                 bool
    JobsBackend         string   // launchd | systemd | none
    JobsBackendReason   string
    SystemdEnv          []string // XDG_RUNTIME_DIR / DBUS_SESSION_BUS_ADDRESS when needed (AC-16)
    PackageManagers     []string
}

// Ports.
type Prompter interface {                                       // [S2]
    Select(q string, opts []string, def int) (int, error)
    Confirm(q string, def bool) (bool, error)
    Text(q, def string, validate func(string) error) (string, error)
    Secret(q string) (string, error)          // no echo (x/term); registers with the Redactor
    Interactive() bool                         // stdin+stdout are TTYs
}
type FS interface {                            // paths are absolute  [S1 read half, S2 write half]
    ReadFile(p string) ([]byte, error); Stat(p string) (fs.FileInfo, error)
    Lstat(p string) (fs.FileInfo, error); ReadDir(p string) ([]fs.DirEntry, error)
    EvalSymlinks(p string) (string, error); Writable(p string) bool // access(2)
    WriteFileAtomic(p string, b []byte, mode fs.FileMode) error
    MkdirAll(p string, mode fs.FileMode) error; Remove(p string) error
    Chmod(p string, mode fs.FileMode) error
    Lock(p string) (unlock func() error, err error) // process-scoped flock (AC-9)
}                                              // read-only / dry-run adapters: writes return ErrDryRun
type Runner interface {                        // never a shell; argv only  [S1]
    Run(ctx context.Context, c Cmd) (Result, error) // Cmd{Argv, Dir, Env, Stdin, Mutating bool}
    LookPath(name string) (string, error)
}                                              // read-only runner rejects Mutating
type Clock interface{ Now() time.Time }        // [S1]
type DBProber interface {                      // [S1]
    Probe(ctx context.Context, dsn string) (DBStatus, error) // ping RTT, TLS, vector ext+ver, schema objects; read-only (postgres.Open)
    Migrate(ctx context.Context, dsn string) error           // postgres.New + Close (used by the migrate step [S2])
    LocalServerEvidence(ctx context.Context) (bool, string)   // TCP 127.0.0.1:5432 + socket/process (AC-19) [S2]
}                                              // Bootstrap(adminDSN) removed in v0.2 (in-process admin bootstrap deferred)
type OllamaProber interface {                  // [S1; Pull used from S2]
    Version(ctx context.Context, url string) (string, error)
    HasModel(ctx context.Context, url, model string) (bool, error)
    Pull(ctx context.Context, url, model string, progress func(done, total int64)) error
    EmbedDims(ctx context.Context, url, model string) (dims int, latency time.Duration, err error)
}
type JobSpec struct {                          // [S2, v0.4]
    Name          string   // "cleanup" | "ingest-pr"
    Label         string   // launchd label io.github.claude-memory.<name>
    Unit          string   // systemd unit base name claude-memory-<name>
    Program       string   // absolute path of the installed binary (AC-35 sticky path)
    Args          []string
    Hour, Minute  int
    PATH, LogPath string
}
type JobDetector interface {                   // [S2, v0.4] read half, in ReadPorts
    Render(j JobSpec) (map[string][]byte, error)   // path → content
    // recorded: unit/plist path → sha256 from the manifest (nil = no manifest), AC-44
    Detect(ctx context.Context, j JobSpec, recorded map[string]string) (State, string, error)
}
type JobManager interface {                    // launchd | systemd [S2] | none; in WritePorts
    JobDetector
    Install(ctx context.Context, j JobSpec) error; Remove(ctx context.Context, j JobSpec) error
}
// v0.4 read/write halves: ReadFS (the read methods of FS; FS embeds it),
// DBProbe (Probe + LocalServerEvidence; DBProber adds Migrate),
// OllamaProbe (Version + HasModel + EmbedDims; OllamaProber adds Pull).
// ReadPorts holds only the read halves, so Detect/Seed/Configure/Plan
// cannot write, migrate, pull or install; mutating commands are refused
// at runtime by the read-only Runner.
type ClaudeCLI interface {                     // [S2] writers only. No MCPGet: detection reads
    MCPAdd(ctx context.Context, name string, argv []string) error  // Paths.ClaudeJSON (AC-40, AC-67)
    MCPRemove(ctx context.Context, name string) error
}
// v0.3: no Namespaces port. The step reads with FS + namespace.Parse and
// writes namespace.Marshal output with FS.WriteFileAtomic (dry-run safe).
type Step interface {                          // [S2, v0.3] no Verify: the engine re-runs Detect after Apply
    ID() string; Title() string; Requires() []string
    Detect(ctx context.Context, rp ReadPorts, st *RunState) Detection          // per-artifact states
    Plan(ctx context.Context, rp ReadPorts, st *RunState, ch Choices) (Plan, error) // actions, diffs, notes
    Apply(ctx context.Context, wp WritePorts, st *RunState, p Plan) (StepResult, error)
}
type Seeder interface {                        // [S2, v0.4] optional: owner of shared values, runs before any Detect
    Seed(ctx context.Context, rp ReadPorts, st *RunState) ([]Note, error) // v0.5: error = invalid flag value → exit 2
}
type Detection struct {                        // [S2, v0.5 listed]
    State State; Detail string
    BlockedBy string                           // step id, when blocked only by a prerequisite
    Remedy    string                           // external block (missing tool, no jobs backend, Ollama absent): keys re-check/skip/quit
    Artifacts []ArtifactState; Notes []Note
}
type Await struct {                            // [S2, v0.5] pause for a user action (AC-5, AC-21)
    Instructions []string
    Artifacts    []string                      // artifact IDs the action must turn ok; the re-check passes on these
}
type Configurer interface {                    // [S2, v0.3] optional: input questions; may only override its own values
    Configure(ctx context.Context, rp ReadPorts, ui Prompter, st *RunState) error
}
// StepResult{Artifacts, Removed, Notes, Diffs, Await}; RunState is typed, one writer per field (plan Design 16).
type Check struct {                            // [S1]
    ID, Title string; Requires []string
    Run func(ctx context.Context) (Status, string /*detail*/, string /*remedy*/)
}
```

CLI: §6.1 AC-4. Doctor JSON: AC-60. Manifest: AC-49.

## 11. Untrusted Inputs
- `settings.json` (and the other settings files of AC-70), `CLAUDE.md`,
  `.claude.json`, the env file and the manifest are user- or
  tool-editable. They are parsed strictly, and the code refuses rather
  than guessing (AC-37, AC-38, AC-42, edge cases). Every path taken from
  them is validated to be under `Paths.Home` before it is written or
  deleted.
- `.claude.json` is read only to compare `command`/`args`; its values are
  never executed or interpolated into argv.
- Output of `launchctl print` and `systemctl --user show` is parsed
  loosely and only drives the state shown to the user. It is never
  executed or interpolated into argv.
- User-typed host, port, DB name, user and namespace globs go through
  `net/url` (DSN), the role/database name pattern (AC-21) and
  `namespace.ValidName` (names). They reach argv only as separate
  elements, never through a shell.
- `~/.claude/projects/*` directory names are only decoded into path
  suggestions (existence-checked) and shown. They are not read further.

## 12. Out of Scope
- Windows; BSDs; system-wide (root) installs; installing system
  packages or running sudo; editing `pg_hba`/UFW/sysctl on a server;
  running anything over ssh.
- Publishing release binaries (§13 #2 follow-up PR), self-update /
  downloading new versions.
- Migrating data between databases when the topology changes (only a
  printed hint).
- Configuring PR providers beyond today's Azure DevOps (backlog 4); a
  TUI; localization.
- Managing Ollama or Postgres services (start, stop, upgrade).

### 12.1 Deferred follow-ups (D1)

None of these changes a port or the engine; each is a new `Step`,
`JobManager`, flag or check when it is picked up.

| Item | ACs | Reason for deferring |
|---|---|---|
| Docker topology C (C1 local bundle + compose up, C2 server bundle) — **slice 3** | AC-23, AC-24 | The owner runs topology B against an existing Docker server set up from `deploy/`; C1/C2 generators are the largest share of new code. Design kept in §6.5.1; re-plan when a second machine or user appears. |
| cron backend | AC-46 | No current user without launchd or a systemd user instance; `crontab` on PATH does not prove a running daemon. Slice 2 prints instructions instead. |
| `uninstall --purge-config` / `--purge-data` | AC-55 | Destructive, rarely needed, and `--purge-data` only applies to C1. Manual `rm` of the listed paths is printed instead. |
| Hand-install adoption heuristics (known historical hashes) | AC-51 (beyond identity rules), AC-41 | Identity rules already prevent duplicates; the cost is one interactive overwrite per differing file on the owner's first run. |
| `doctor --latency` probe | AC-61 | Not free (one embed + one hybrid query per run) and not needed to install or diagnose; backlog 7's p95 can be measured by hand. |
| `install --only` | AC-53 (half) | `--skip` plus per-step choices cover the need; `--only` adds prerequisite edge cases. |
| `seed` step | AC-48 | Repo-checkout-only convenience; `claude-memory seed` exists. |
| In-process admin bootstrap (admin DSN) | v0.1 AC-21 branch 1 | Works only on Homebrew; the printed `bootstrap.sql` command covers macOS and Linux without holding an admin credential. |
| Backup rotation (keep newest 5) | AC-39 | Backups are made only on real writes, which are rare after the model-level no-op. |
| Prebuilt release binaries | §13 #2 | Follow-up PR after slice 1 is in use; must pass the same `LDFLAGS`. |

## 13. Owner Questions — resolved (2026-10-02)

| # | Question | Decision | Impacted AC |
|---|---|---|---|
| 1 | Where should the CLAUDE.md section go by default? | **Resolved — user-level `~/.claude/CLAUDE.md`** (`<ClaudeDir>/CLAUDE.md`) by default; `--claude-md acme/CLAUDE.md` for a shared file. `--yes` never writes an explicitly given file inside a git repo. The section and both skills become location-neutral. | AC-42, AC-66 |
| 2 | Publish prebuilt binaries? | **Resolved — yes, as a follow-up PR** after slice 1 is in use; the release workflow uses the Makefile `LDFLAGS`. Downloaded darwin binaries carry quarantine; the AC-35 hint covers it. | AC-2, AC-35, §12.1 |
| 3 | May the wizard run `brew install` itself? | **Resolved — no; hints only.** Re-check re-runs only the blocked step's Detect to keep it cheap. | AC-10, AC-18 |
| 4 | Is `golang.org/x/term` acceptable? | **Resolved — yes** (added in S2). | §4, AC-30 |
| 5 | Allow a remote Ollama URL? | **Resolved — allowed with a warning and confirmation**; the warning names the hook's 800 ms timeout and the "inject nothing" consequence. | AC-33 |
| 6 | Keep launchd labels `io.github.claude-memory.*`? | **Resolved — keep.** | AC-44 |
| 7 | (review) Is the slice plan acceptable — doctor + plumbing first, then install for B and A-with-existing-DB, docker/cron/purge later? | **Resolved — yes** (D1). | §0.2, §12.1 |
| 8 | (review) May `install --upgrade` be the same as `install --yes`? | **Resolved — yes**: an alias, one code path. | AC-4, AC-52 |
| 9 | (review) Is C2 worth more than writing `.env` and printing steps? | **Resolved — topology C is deferred to slice 3**; its scope (incl. this question) is re-decided when slice 3 is planned. | §6.5.1 |

## 14. Acceptance Criteria Summary (Definition of Done)

Slice 1 — doctor + plumbing:
- [ ] AC-1 [S1 half] — doctor/version dispatched before config load; migrate after
- [ ] AC-2 [S1] — `version` + single `LDFLAGS` in Makefile
- [ ] AC-3 [S1] — `migrate` subcommand
- [ ] AC-4 [S1 half] — doctor flags
- [ ] AC-15 [S1] — platform detection as data (launchd backend)
- [ ] AC-18 [S1 guard] — argv deny-list guard
- [ ] AC-27 [S1] — `config.ParseEnvFile` with format findings
- [ ] AC-30 [S1 redactor] — redactor (min length, userinfo pattern) + doctor sentinel
- [ ] AC-32 [S1 prober] — Ollama prober
- [ ] AC-34 [S1 embed] — `integration` / `deploy` embed packages
- [ ] AC-37 [S1 library] — settings.json codec + merge library
- [ ] AC-38 [S1 library] — refusal rules
- [ ] AC-40 [S1 detect] — MCP registration read from `.claude.json`
- [ ] AC-41 [S1 doctor states] — skill hash states in doctor
- [ ] AC-42 [S1 library] — Markdown managed-block library
- [ ] AC-44 [S1 doctor] — launchd job detection incl. run-with-env warning
- [ ] AC-49 [S1 read] — manifest schema + reader
- [ ] AC-57 [S1] — doctor read-only
- [ ] AC-58 [S1] — doctor check table (23 checks)
- [ ] AC-59 [S1] — exit codes, parallel probes, timeout + deadline
- [ ] AC-60 [S1] — doctor `--json` schema
- [ ] AC-63 [S1] — fakes, explicit Paths, import guard
- [ ] AC-64 [S1 libraries] — golden tests, run twice
- [ ] AC-65 [S1 half] — integration: migrate + pg checks
- [ ] AC-66 [S1 half] — DEPLOY.md env fix + URL-safe passwords; skills reworded
- [ ] AC-67 [S1] — MCP command never executed by doctor/Detect
- [ ] AC-68 [S1] — `Paths`/`Env` injection, no `os.Getenv` in setup
- [ ] AC-70 [S1 doctor] — duplicate hooks in other settings files

Slice 2 — install, upgrade, uninstall:
- [ ] AC-1 [S2 half] — install/uninstall dispatched before config load
- [ ] AC-4 [S2 half] — install/uninstall flags; deferred values exit 2
- [ ] AC-5 — Detect → table → choice → plan → confirm → Apply → re-Detect
- [ ] AC-6 — default choice per state; overwrite Confirm
- [ ] AC-7 — step order and prerequisite blocking
- [ ] AC-8 — failure isolation, exit 1 with remedies
- [ ] AC-9 — atomic writes, conditional manifest write, flock
- [ ] AC-10 — line-oriented prompts; cheap re-check
- [ ] AC-11 — non-TTY without `--yes` exits 2; stdin read only for `--pg-password-stdin`
- [ ] AC-12 — `--yes` defaults; never overwrites `modified`
- [ ] AC-13 — `--dry-run` structurally write-free, diffs shown
- [ ] AC-14 — plain/colored output rules
- [ ] AC-16 — jobs backend launchd/systemd (bus retry)/none; no cron fallback
- [ ] AC-17 — unsupported OS exits 2
- [ ] AC-18 [S2 hints] — package hints only
- [ ] AC-19 — topology A/B with evidence-based default
- [ ] AC-20 — DSN prompt incl. sslmode, URL-encoded, SQLSTATE classifier
- [ ] AC-21 — topology A: existing DB, or `bootstrap.sql` + printed command; SQL shared with initdb
- [ ] AC-22 — URL-safe generated passwords
- [ ] AC-25 — migrate step with step-state DSN, before hooks
- [ ] AC-26 — topology/DSN change warns, no data move
- [ ] AC-28 — env file written plain, in place, unknown keys preserved
- [ ] AC-29 — wrong env perms detected and repaired
- [ ] AC-30 [S2 sentinel] — password never leaves env file/`bootstrap.sql`
- [ ] AC-31 — `--pg-dsn` with password rejected; stdin ordering
- [ ] AC-32 [S2 step] — Ollama step, pull, 1024-dim check
- [ ] AC-33 — remote Ollama warning (800 ms hook timeout)
- [ ] AC-34 [S2 run] — works outside a checkout
- [ ] AC-35 — binary self-install; `go run` refusal; PATH/quarantine hints
- [ ] AC-36 — hook scripts installed with rendered path
- [ ] AC-37 [S2 step] — settings.json step on the S1 library
- [ ] AC-38 [S2 step] — refusal at the step
- [ ] AC-39 — settings.json backup + concurrent-change abort
- [ ] AC-40 [S2 register] — `claude mcp add/remove --scope user` only
- [ ] AC-41 [S2 step] — skills with hash states
- [ ] AC-42 [S2 step] — CLAUDE.md step, consent, `--yes` rules
- [ ] AC-43 — jobs call binary directly with explicit PATH
- [ ] AC-44 [S2 install] — launchd bootstrap/bootout, existing labels
- [ ] AC-45 — systemd user timers, linger hint
- [ ] AC-47 — namespaces init/add, existence-checked suggestions
- [ ] AC-49 [S2 write] — manifest written, jobs backend + config dir recorded
- [ ] AC-50 — no-op re-run performs zero writes
- [ ] AC-51 [S2 identity] — hand installs never duplicated
- [ ] AC-52 — `--upgrade` ≡ `--yes`
- [ ] AC-53 [S2 `--skip`] — `--skip`
- [ ] AC-54 — uninstall reverses and trims the manifest
- [ ] AC-56 — uninstall without manifest removes nothing
- [ ] AC-62 — install ends with doctor
- [ ] AC-64 [S2 steps] — golden tests for step renders
- [ ] AC-65 [S2 half] — integration: bootstrap.sql + e2e local
- [ ] AC-66 [S2 half] — INSTALL.md / run-with-env.sh / DEPLOY.md upgrade
- [ ] AC-69 — root guard
- [ ] AC-70 [S2 install] — duplicate warning in install summary

Slice 3 / deferred (not part of the definition of done for this spec's
first two PRs):
- [ ] AC-23 [S3] — docker-local bundle
- [ ] AC-24 [S3] — docker-server bundle
- [ ] AC-46 [D] — cron managed block
- [ ] AC-48 [D] — seed step
- [ ] AC-55 [D] — purge flags
- [ ] AC-61 [D] — hook latency probe
