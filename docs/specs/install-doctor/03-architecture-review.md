# Architecture Review — `install` / `doctor` (pre-implementation, iteration 0)

- **Reviewer:** architecture-reviewer, skills: golang-architecture, security.
- **Target:** `01-spec.md` v0.1 (SPEC-2026-10-01-install-doctor, AC-1..AC-66),
  `02-plan.md` v0.1 (WI-0..WI-30, four PRs), backlog item 8 in
  `docs/specs/README.md:130-168`, reviewed against the code and assets they
  touch at `64f14c3` (nothing of the feature exists yet; every code citation
  is the current baseline).
- **Verified locally** (Linux container, Go 1.25.0, Postgres 16.14 +
  pgvector 0.6.0 on `/tmp:54329`, Claude Code CLI 2.1.287):
  `go vet ./...`, `go build ./...` and the `integration`-tagged
  `./internal/postgres/...` suite green against the local Postgres. The
  spec's §1 claims were each re-run, not read: `config.LoadFromFile`
  against DEPLOY.md's `export KEY="…"` form; `source` of a `KEY=VALUE` file
  with `$`, `&` and a space in the value; `CREATE EXTENSION vector` as a
  non-superuser database owner on a fresh database, then `IF NOT EXISTS`
  as the same role after a superuser created it, then both migration files
  twice as the app role; `schema_migrations` row count; `CREATE DATABASE`
  inside `DO`; `go:embed` of a parent directory, of a directory containing
  dotfiles, and of `*`; `json.Decoder.Token` on large integers and escaped
  strings with and without `UseNumber`/`InputOffset`; `systemctl --user`
  without a session bus; and, with `CLAUDE_CONFIG_DIR` pointed at a scratch
  directory, `claude mcp add --scope user`, `claude mcp get`, `claude mcp
  list` and `claude mcp remove` (timed). No launchd here, so AC-44 is
  reviewed on paper.
- **Gate:** **REVISE BEFORE IMPLEMENTING** — 1 blocker, 4 high, 13 medium,
  14 low. The layering (ports declared by `internal/setup`, adapters in
  `main.go`, mutating-vs-read-only as data) is the right shape and the
  secrets handling is unusually careful. The blocker is that the one
  external command the spec classifies as read-only (`claude mcp get`) is
  not: it spawns the registered server, which here means `claude-memory
  serve` → `postgres.New` → migrations, from inside `doctor`. The high
  findings are the size of the feature relative to the owner's need, a
  Linux bootstrap path that cannot work as written, the settings.json
  merge model, and an unresolved contradiction about how `internal/setup`
  learns `$HOME`. All are fixable in the spec; none requires a different
  architecture.

## What the spec gets right (so it is not re-argued below)

- The §1 defects are real. Verified: DEPLOY.md's `export MEMORY_PG_DSN="…"`
  line produces the environment key `export MEMORY_PG_DSN` with the quotes
  kept in the value, `MEMORY_PG_DSN` stays empty and `config.Load` fails
  with `MEMORY_PG_DSN is required but not set` (`config.go:248-259`,
  `DEPLOY.md:94`). `run()` loads the env file and `config.Load()` before
  any dispatch except `namespaces` (`main.go:36-50`). `0001_init.sql:4`
  fails for the app role on a fresh database with `permission denied to
  create extension "vector" / HINT: Must be superuser` (pgvector 0.6.0 is
  not a trusted extension), and after a superuser creates it the app role's
  re-run is a `NOTICE … skipping`, so the full migration set is idempotent
  as the app role. `schema_migrations` has 0 rows after both migrations
  (`0001_init.sql:7-10` creates it; nothing writes it). No `ldflags`,
  `ReadBuildInfo` or `version` anywhere (`Makefile:7-9`). `go:embed
  ../../assets` from `cmd/` is `invalid pattern syntax`, and a directory
  pattern excludes `.env.example` while `all:` or the explicit name
  includes it (`deploy/.env.example` is tracked; `ls` hides it).
- `run-with-env.sh:15` is worse than "redundant": `source` of
  `MEMORY_PG_DSN=postgresql://u:p$4ss&w0rd@h:5432/db` gives `unbound
  variable`, tries to run `w0rd@h:5432/db` as a command and leaves the
  variable unset, and because `LoadFromFile` only sets a key when it is
  unset (`config.go:257-258`), any *partially* expanded value the shell did
  produce wins over the file. AC-43's "call the binary directly" is the
  right fix.
- `claude mcp add` defaults to scope `local` (verified `--help`), so the
  explicit `--scope user` in AC-40 is load-bearing. `claude mcp get <absent>`
  prints `No MCP server named "<name>". Run \`claude mcp add\` to add one.`
  and exits 1; `claude mcp list` exits 0 with `No MCP servers configured`
  when empty. These are the WI-0 fixtures; they are recorded here.
- `CLAUDE_CONFIG_DIR` relocates `.claude.json` as well as `settings.json`:
  `claude mcp add --scope user` wrote `$CLAUDE_CONFIG_DIR/.claude.json`
  (`mcpServers.<name> = {type:"stdio", command, args, env:{}}`) and a
  `backups/` directory next to it; `~/.claude.json` was untouched. §8's
  "all `~/.claude` paths follow it" must include `.claude.json`.
- Hook latency probe safety (AC-61): `session_id = doctor.probe` fails
  `sessionIDRe` (`main.go:264`) so the hook uses `MapCache` and writes no
  stale-cache file; the `Search` path has no writes (`used_count` is only
  touched in `lifecycle.go:53-55` from `Feedback`). The probe is read-only
  as claimed, though not free (one Ollama embed and one hybrid query per
  run).

## Findings

| Sev | where | Rule | Claim | Recommendation |
|---|---|---|---|---|
| BLOCKER | `01-spec.md:524-532` (AC-40 Detect via `claude mcp get`), `:669-673` (AC-57 doctor never applies migrations), `:694` (AC-58 `mcp.registered` runs `claude mcp get`), `:170-174` (§4), `02-plan.md:276-282` (WI-7 "read-only command") | read-only guarantee | Verified with claude 2.1.287: `claude mcp get <name>` **health-checks the server by spawning its command** (`--help`: "approved servers are health-checked"; the output for a `/bin/true` server is `Status: × Failed to connect / CONNECTION_CLOSED`). For our registration the spawned command is `<bin> serve`, i.e. `cmdServe` → `buildService(ctx, cfg, true)` → `postgres.New` → `runMigrations` (`main.go:151-157,88-96`, `store.go:39-52`), plus an Ollama client and a tailnet round trip, every time `doctor` or `install`'s `mcp` step runs Detect. That breaks AC-57 ("never applies migrations", "uses `postgres.Open`"), the "read-only Runner" cannot catch it (the argv is labelled `Mutating: false`), and it costs 0.7–0.9 s here for a trivial server, far more over Tailscale. `claude mcp list` health-checks every server the same way. | Never run `claude mcp get|list` from `doctor` or from Detect. Read the registration from `$CLAUDE_CONFIG_DIR/.claude.json` (default `~/.claude.json`), key `mcpServers["claude-memory"]` → `{type, command, args}`, read-only, strict JSON, tolerant of a missing key; `ok` when `command == <bin>` and `args == ["serve"]`, `outdated` when `command` differs, `absent` otherwise. Keep `claude mcp add --scope user` / `claude mcp remove --scope user` as the only writers (they are what the spec already mandates; §4's "never edited directly" is about writes). Add a fixture of the `.claude.json` shape above and the `get`/`list` outputs to WI-0. Update AC-40, AC-58, AC-57's verification and the `ClaudeCLI` port (drop `MCPGet`; add nothing — the FS port reads the file). |
| HIGH | `01-spec.md` §6 (66 ACs), §10 (9 ports), `02-plan.md:13-20` (4 PRs), `:156-170` (31 WIs); backlog `README.md:168` ("Rough size: M–L") | scope vs the owner's need | The non-test code today is ~8.8 k lines. The plan adds an engine with four phases and five choice kinds, nine ports and their fakes, three job backends, four topologies with two generated compose bundles, an ordered JSON codec, an LCS differ, a crontab merger, a Markdown block merger, an env-file editor, a manifest with eleven artifact kinds, hand-install adoption, a lock file, `--only/--skip`, `--upgrade`, `uninstall` with two purge modes, 24 doctor checks and a latency probe. That is 6–8 k lines plus tests — roughly doubling the codebase for an installer whose only user runs topology B on one Mac. The backlog's "M–L" is honest; the spec is XL. Risk is not that it cannot be built but that PR B (WI-9..21, 13 work items, ~30 ACs) is a month of review for a feature whose value arrives the first time the owner runs it. | Ship the first slice described in "A cut-down first slice" below: doctor + plumbing, then `install` for the two topologies the owner actually has (B, and A against an *existing* database), macOS + Linux/systemd, without docker, cron, bootstrap-via-admin, purge, adoption heuristics or the latency probe. Fold `--upgrade` into `install --yes` (same defaults: `outdated → repair`, `modified → keep`). Treat C2 as "write `.env` + print DEPLOY.md's steps" at most: the bundle AC-24 describes is `cp -r deploy/`. Re-plan C1/C2 only if a second machine or user appears. Keep the architecture; cut the surface. |
| HIGH | `01-spec.md:117-127` (Linux scenario: admin DSN `postgresql:///postgres`, hint `sudo -u postgres`), `:352-365` (AC-21), `02-plan.md:131-139` (Design 5), `:342-348` (WI-12 "password shown as `<generated, see env file>`") | topology A correctness | Two defects. (1) `postgresql:///postgres` connects as the **OS user** over the socket (pgx `defaultSettings`: `user = user.Current()`, host = `/var/run/postgresql` on Debian). On every apt/dnf install only the `postgres` OS user is peer-mapped to a superuser and the `postgres` role has no password, so the "bootstrap through an admin connection" branch fails for the wizard's user, and the wizard never runs `sudo` (§4). The branch works only on Homebrew, where the installing user is the superuser. (2) The printed-SQL fallback hides the password as `<generated, see env file>`, so the user must open a 0600 file and paste a 43-character secret into `psql` by hand — the opposite of "the user never has to copy it". Also AC-21 says the SQL "is the same text as `deploy/initdb/01-app-role.sh` … kept in one embedded file used by both" while plan Design 5 keeps the shell script as is (bare `CREATE ROLE`, `initdb/01-app-role.sh:10`) and adds an equivalence test. Verified: `CREATE DATABASE` cannot run inside `DO`, a `DO … IF NOT EXISTS … CREATE ROLE` block is idempotent. | Make the manual path the primary path on Linux and the only path in the first slice: write `~/.config/claude-memory/bootstrap.sql` (0600, real role name, database, **real password**, `IF NOT EXISTS`/`DO` form, `CREATE DATABASE` guarded by a `\gexec` or run from Go), print exactly `sudo -u postgres psql -v ON_ERROR_STOP=1 -d postgres < ~/.config/claude-memory/bootstrap.sql` (the redirect is opened by the *user's* shell, so the `postgres` OS user needs no read access to a 0600 file), wait for re-check, and delete the file when `database` verifies. Keep the in-process admin bootstrap as an option that asks for an admin DSN (with a no-echo password prompt), defaulting to `postgresql:///postgres` only on darwin. Fix AC-21 to match Design 5 (two copies, one catalog-equivalence test), or generate the heredoc body from the embedded SQL — pick one and state it in both documents. |
| HIGH | `01-spec.md:494-509` (AC-37 merge model), `:255-258` (AC-6 `outdated → repair`), `:783-788` (§8 `matcher`/`timeout` → `outdated`), `02-plan.md:111-119` (Design 2 "json.RawMessage leaves", "byte-identical"), `:391-405` (WI-17) | safety of the settings.json merge | (1) `json.Decoder.Token` does not yield raw leaves: without `UseNumber` a `12345678901234567890` becomes `1.2345678901234567e+19` and re-encodes as `12345678901234567000`; with or without it, `"café \/ x"` re-encodes as `"café / x"` (verified). Design 2's "`json.RawMessage` leaves" is only achievable by slicing the input with `dec.InputOffset()` between tokens, which the plan does not say. (2) "Write nothing when the result is byte-identical to the input" is the wrong no-op test: a 4-space- or tab-indented file, or one with `\uXXXX` escapes, is *always* rewritten on the first run (whole-file re-indent → a noisy diff in a dotfiles repo), and a semantically unchanged merge still produces a backup. (3) AC-6 maps `outdated → repair` and §8 says an entry of ours "with a different `timeout`" is `outdated`; under `--yes`/`--upgrade` a timeout the owner deliberately raised is silently reverted. Without a manifest hash for the entry, "ours but different" cannot be told from "ours, user-edited". (4) Hooks can also live in `settings.local.json` and in a project's `.claude/settings.json`; an entry there plus ours in the user file fires the hook twice, and neither install nor doctor looks. | (1) Specify the codec: `UseNumber()` + `InputOffset()` raw slices for every scalar, ordered keys, and re-emit untouched subtrees byte-for-byte; re-encode only the arrays we insert into. (2) Define no-op at the model level: Detect compares the *parsed* hook arrays; when our entries are already present exactly once per event, the step is `ok` and nothing is written, regardless of formatting. When a write is needed, detect the file's indentation (first indented line) and reuse it. (3) Hook-entry states: byte-equal to desired → `ok`; equal to the manifest's recorded entry but not to desired → `outdated`; present with our command but different and not in the manifest → `modified` → keep (consent needed, never under `--yes`). The legacy `$HOME/…` form from `settings.snippet.json:8` is `modified`, not `outdated`; doctor expands `$HOME` when checking that the command path exists. (4) Doctor `hooks.settings` also parses `settings.local.json` and `<cwd>/.claude/settings*.json` read-only and warns on a second entry of ours. Add golden cases `tab-indented`, `four-space`, `unicode-escapes-unchanged-noop`, `local-settings-duplicate`. |
| HIGH | `02-plan.md:83-91` ("`internal/setup` may use `os` only for `os.ErrNotExist`"), `:533-536` ("a real temp HOME (`t.Setenv("HOME", t.TempDir())`) behind the FS port"), `01-spec.md:833-887` (§10 has no paths/platform input), `:880-886` (Step with `Plan`, `Apply`, `Verify`), `:853` (`Platform` port) | engine shape / composition | (1) The plan forbids `internal/setup` from reading the environment and at the same time tests it by setting `HOME`. Every step needs `~/.claude`, `~/.config/claude-memory`, `~/.local/{bin,state,share}`, `~/Library/LaunchAgents`, `$CLAUDE_CONFIG_DIR`, the uid for `gui/<uid>`; §10 declares no type that carries them, so each step would either call `os.Getenv` (banned) or take ad-hoc constructor arguments. (2) `Verify(ctx) error` is `Detect(ctx) == ok` for every step in the spec; a separate method is a second implementation to keep in sync and a second fake to script. (3) `Platform` is a port with one method returning a struct; detection runs once, before the engine, and its result is data every step reads. (4) Five choices × seven states is a 35-cell matrix the prompter, engine and `--yes` logic all encode; `reconfigure` is only meaningful for `topology`, `envfile` and `namespaces`. | (1) Add `type Paths struct { Home, ConfigDir, ClaudeDir, StateDir, ShareDir, BinDir, LaunchAgentsDir string; UID int }` computed in `main.go` from `HOME`, `CLAUDE_CONFIG_DIR`, `--bin-dir` and `os.Getuid()`, injected into every step; tests build it from `t.TempDir()` (no `t.Setenv` needed, and the race detector is happier). (2) Drop `Verify` from the `Step` interface; the engine re-runs `Detect` after `Apply` and fails the step unless it reports `ok`. (3) Make `PlatformInfo` an input value, not a port; the detector is a function in `cmd` tested with the fake FS/Runner. (4) Reduce choices to `apply | keep | skip` plus one explicit `Confirm("overwrite your modified <x>?")` for `modified`; `reconfigure` becomes a `--reconfigure` flag (or re-asking when a required input is missing). Update AC-5, AC-6, §10. |
| MEDIUM | `01-spec.md:216-222` (AC-1 dispatches `migrate` **before** config load) vs `:229-234` (AC-3 `migrate` "shall load the config like the other subcommands") and `02-plan.md:29-33` ("`migrate` needs config, so it goes after config load") | spec/plan consistency | The same subcommand is placed on both sides of `config.LoadFromFile` in the same document. Also unstated: `install` writes the env file in step `envfile` and must run `migrate` with the DSN it just wrote, so it cannot rely on the process environment loaded at startup. | AC-1: dispatch `install`, `uninstall`, `doctor`, `version` early; `migrate` stays after config load (it genuinely needs the DSN and the 0600 check). State in AC-25 that the `migrate` step calls `DBProber.Migrate(ctx, dsn)` with the DSN from the step state, never from `os.Getenv`. |
| MEDIUM | `01-spec.md:317-321` (AC-16 `systemctl --user show-environment` exit 0 → systemd), `:579-587` (AC-45), `:805-806` (§8) | platform detection correctness | `systemctl --user` talks to the user bus via `$XDG_RUNTIME_DIR`/`$DBUS_SESSION_BUS_ADDRESS`. Verified here with `XDG_RUNTIME_DIR` unset: `Failed to connect to bus: No medium found`, exit 1 — the same thing happens over `ssh` into a box whose user instance *is* running (lingering), so the detector would fall back to cron on a systemd machine and install a different backend than a later run from a desktop session. | If `/run/user/<uid>/bus` exists, run the probe with `XDG_RUNTIME_DIR=/run/user/<uid>` and `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus` in `Cmd.Env`; only then fall back to cron. Add `--jobs-backend launchd|systemd|cron|none` as an override, record the backend in the manifest, and have doctor warn when the detected backend differs from the recorded one. Add the "ssh, lingering user instance" row to the AC-15 detector table. |
| MEDIUM | `01-spec.md:708-713` (AC-59 "whole run finishes within 10 s when every endpoint hangs", `--timeout` default 3 s), `:674-706` (AC-58 table), `:774-776` (§7 "< 2 s on a healthy setup") | doctor time bounds | With sequential checks, `pg.connect` (3 s) + `ollama.reachable` (3 s) + `mcp.registered` via the CLI (3 s, see BLOCKER) + `jobs` via `launchctl`/`systemctl` (3 s) already exceeds 10 s, and dependent checks are only skipped *after* their prerequisite times out. On a healthy setup the Node-based `claude` CLI alone measured 0.66–0.86 s here. | Run independent probes concurrently (DB, Ollama, tools, files) under one `--timeout` each and a `--deadline` for the whole run (default 10 s); make `mcp.registered` a file read (BLOCKER) and `jobs` a single `launchctl print` / `systemctl --user show` per job. Then the 10 s bound is `max`, not `sum`, and the < 2 s target is realistic. |
| MEDIUM | `01-spec.md:596-604` (AC-47 suggestions "decoded from Claude Code's path-encoding"), `02-plan.md:377-380` (WI-16 `projects.go`) | correctness of a convenience | Claude Code names `~/.claude/projects/<dir>` by replacing `/` with `-` in the cwd (this session's own scratch dir is `-home-user-claude-memory`). The encoding is lossy: `-home-user-claude-memory` is `/home/user/claude-memory` or `/home/user/claude/memory`, and every hyphenated directory name collides. A wrong suggestion written into `namespaces.yaml` silently scopes memory to a path that does not exist. | Disambiguate by existence: split on `-`, greedily join segments and `Stat` each candidate prefix, accept only a decoding whose every prefix exists; otherwise show nothing for that entry. Suggest the *parent* only when the decoded path exists. Or drop the suggestions from the first slice and keep `namespaces which`. |
| MEDIUM | `01-spec.md:438-452` (AC-30, AC-31), `:287-290` (AC-11 "never block reading stdin"), `:235-241` (`--pg-password-stdin`), `:366-368` (AC-22), `DEPLOY.md:44-45` (`openssl rand -base64 32`), `:369-380` (AC-23 `.env`) | secrets handling | Four gaps, none severe alone. (1) A `Redactor` that replaces the password literal everywhere in output mangles unrelated text when the password is short or a common substring (`pw`, `123`); AC-30 sets no minimum. (2) `--pg-password-stdin` *requires* reading stdin, while AC-11 says a non-TTY run "shall never block reading stdin"; the order (read the one line first, bounded, then check `--yes`) is unstated. (3) DEPLOY.md recommends `openssl rand -base64 32`, which emits `+`, `/` and `=`; a `/` in the userinfo of a URL DSN is parsed as the start of the path. The owner's current env file may hold exactly that, and doctor's `env.format` has no remedy for it. (4) Both compose bundles put `POSTGRES_PASSWORD`/`APP_DB_PASSWORD` into the container environment (`docker-compose.yml:23,29`), readable via `docker inspect` by anyone in the `docker` group; and `MEMORY_PG_DSN` sits in the environment of every `claude-memory` process (`config.go:258`), readable via `/proc/<pid>/environ` by the same uid. Both are acceptable for a single-user tool but §7 does not say so. | (1) Register a secret for literal redaction only when it is ≥ 8 characters, and always redact `scheme://user:…@` userinfo by pattern. (2) Specify: with `--pg-password-stdin`, read exactly one line (≤ 4 KiB, 5 s deadline) before anything else; without it, never read stdin when it is not a TTY. (3) `pg.connect`'s "DSN parses" failure gets the remedy "URL-encode the password (`claude-memory install --only envfile` re-encodes it)"; AC-28's rewrite re-encodes a password it can recover from a `user:pw@` form. (4) State both exposures in §7 as accepted; mention `docker secrets` as the C1/C2 upgrade path if it ever matters. |
| MEDIUM | `01-spec.md:639-666` (AC-54 keeps the manifest, AC-55 deletes it only with `--purge-config`), `:650` ("remove the installed binary last"), `:619-623` (AC-50 zero writes) vs `:272-279` (AC-9 manifest rewritten after each step) | uninstall / manifest semantics | (1) After a plain `uninstall` the manifest still lists every removed artifact; the next `doctor` warns "artifacts it lists do not exist" and the next `install` sees a topology/DSN "already chosen" for a machine with nothing installed. (2) AC-9's "manifest rewritten after each completed step" and AC-50's "zero writes on a no-op re-run" conflict unless the rewrite is conditional; `updated_at` alone would make every run a write. (3) Removing the binary last while it is the running executable is fine on macOS/Linux (unlink of an open file), but the spec should say so and should not try to `Remove` when `os.Executable` is not under `--bin-dir` (a repo build running `uninstall`). | (1) `uninstall` removes artifacts from the manifest as they are reversed and deletes the manifest when it is empty; `--purge-config` additionally removes env, `namespaces.yaml`, state dir. (2) Write the manifest only when the artifact set or a recorded hash changed; `updated_at` moves only then. (3) Say it in AC-54; skip the binary when it is not the one recorded. |
| MEDIUM | `01-spec.md:569-578` (AC-44 legacy plist → `outdated` → repaired by `install`), `:674-706` (AC-58 `jobs` check: "loaded/enabled; last exit status") | doctor coverage of a known defect | The owner's live plists point at `run-with-env.sh` (`integration/launchd/*.plist:16`), whose `source` is the bug verified above. PR A's doctor is meant to run against exactly that setup ("Rollout 1"), but the `jobs` check only reports loaded/enabled, so the one job defect the spec already knows about is invisible until PR C. | Add to `jobs`: `warn` when a plist/unit's `ProgramArguments[0]`/`ExecStart` is not the installed binary (remedy `install --only jobs`, or until PR C "see AC-43"), and `warn` when the job's `EnvironmentVariables.PATH`/`Environment=PATH` lacks the directories of `claude`, `git`, `az`. Both are file reads. |
| MEDIUM | `01-spec.md:541-556` (AC-42 `--yes` sentence), `02-plan.md:430-436` (WI-21 "walk up for `.git`"), `integration/claude-md-snippet.md:13-15` ("This folder's repos …"), `integration/skills/remember/SKILL.md:3,11` (`acme/CLAUDE.md`) | CLAUDE.md step + §13 #1 | (1) "Under `--yes`, it writes only to the default user-level file, and only when `--claude-md` was not given for a path inside a git repository" reads two ways (does `--yes` write the default file when `$HOME` itself is a git repo — dotfiles — or not?). (2) `.git` is a *file* in worktrees and submodules; "walk up for `.git`" must accept both. (3) The location-neutral rewording §13 #1 calls for is needed in the `remember` skill too, which cites `acme/CLAUDE.md` twice; otherwise the installed skill contradicts the installed CLAUDE.md block. | (1) Rewrite: "`--yes` writes the default user-level file unconditionally (even inside a git repo, since `~/.claude/CLAUDE.md` is the user's own); it never writes an explicitly given `--claude-md` path that is inside a git repository." (2) Accept `.git` as file or dir. (3) Add `integration/skills/remember/SKILL.md` (and `memory-digest`) to WI-3's rewording list and to AC-66. |
| MEDIUM | `01-spec.md:328-332` (AC-18 deny list), `:737-743` (AC-63 "`go list -deps`"), `02-plan.md:252-265` (WI-6 FakeRunner "fails on any unscripted argv") | test strategy mechanics | Good strategy; three mechanics need stating. (1) A FakeRunner keyed by exact argv is brittle for commands whose argv includes paths from `t.TempDir()`; scripting by `argv[0]` + a matcher is needed or every test re-scripts paths. (2) `go list -deps` inside a `go test` run spawns the toolchain (~1 s, needs `GOFLAGS`/module cache in CI; fine on `ubuntu-latest`, slow under `-race` loops). (3) The AC-18 guard greps `argv[0]` for `sh`/`curl`; `bash`, `zsh`, `env`, `sudo -u`, `brew` via absolute path (`/opt/homebrew/bin/brew`) slip through. | (1) `FakeRunner.Script(match func(Cmd) bool, result)` plus an "unmatched argv fails" default; record calls for the guard. (2) Keep `go list -deps` but mark the test `testing.Short()`-skippable and run it once in CI, not per package iteration. (3) Guard on `filepath.Base(argv[0])` against `{sh,bash,zsh,dash,env,sudo,doas,brew,apt,apt-get,dnf,yum,pacman,curl,wget,ssh,scp,rsync}`; for `docker` allow only the exact AC-23 argv. |
| MEDIUM | `01-spec.md:88-99` (scenario B), `:343-351` (AC-20 host/port/db/user/password only) | remote topology completeness | AC-20 builds the DSN from five fields; there is no `sslmode`. Over Tailscale that is fine (the spec's assumption), but "remote" in the topology list is any host, and pgx's default `sslmode=prefer` silently falls back to cleartext when the server has no TLS — the password itself is safe under SCRAM, the data is not. | Add an optional sixth prompt `TLS: prefer (default) / require / disable`, written as `?sslmode=` on the DSN, with a one-line note that Tailscale already encrypts the link. Doctor `pg.connect` reports the negotiated `ssl` (`SHOW ssl` / `pg_stat_ssl`) as `info`. Cheap, and it keeps "remote" honest. |
| MEDIUM | `01-spec.md:216-222` (AC-1 "Verify: `cmd` test with `HOME` set to an empty temp dir runs `doctor --json`"), `:472-480` (AC-34 "copies it to a temp dir outside the repo"), `02-plan.md:517-519` | test validity | (1) AC-34's test proves nothing about reading the working tree unless the process's `cwd` is also outside the repo (a relative `integration/…` read would still succeed from the repo root). (2) `os.Executable` under `go run` is `/tmp/go-build…/exe/claude-memory`; the `binary` step would happily install that temp file, with an empty version. | (1) Set `cmd.Dir` to the temp dir and `HOME` to another temp dir in that test. (2) The `binary` step refuses (exit 2, "run `make install` or build to a stable path") when `os.Executable` resolves under `os.TempDir()`/`GOCACHE`, or when `version` is empty and `--bin-dir` is given. |
| MEDIUM | `01-spec.md:623-628` (AC-51 "differing ones become `modified`"), `:533-540` (AC-41) | adoption heuristics | A hand install copied `remember/SKILL.md` at commit X. The first `install` after an embedded change has no manifest hash, so the file is "present, differs from ours, not recorded" → `modified` → keep, forever under `--yes`/`--upgrade`. The owner's own setup would never receive a skill update without an interactive `overwrite`. | Acceptable if said out loud: "the first run after a hand install is interactive and offers `show diff / overwrite` for each differing file; from then on hashes are recorded". Make the first-run message say so. Alternatively ship the pre-feature `integration/` hashes as a small embedded table of "known historical versions" so a byte-match to any of them counts as `outdated`; cheap (two skills, two scripts) and it makes the owner's upgrade non-interactive. |
| LOW | `01-spec.md:223-228` (AC-2), `Makefile:7-9,54-59` | version | `debug.ReadBuildInfo` carries `vcs.revision` only for `go build` of a main package inside a git checkout with `git` on PATH (`-buildvcs=auto`); `go test` binaries and `go install pkg@version` have none. Fine as a fallback, but the Makefile ldflags are what makes it deterministic — and `cross-build` must get them too. | Put `LDFLAGS` in one Make variable used by `build`, `install` and `cross-build`; `version` prints `dev` when both sources are empty. |
| LOW | `01-spec.md:317-321` (AC-16 cron when `crontab` is on PATH) | cron backend | `crontab` on PATH does not mean a cron daemon runs (containers, WSL without systemd, minimal cloud images). The block is written and never fires; doctor's `jobs` sees the block and says `ok`. | Detect a running `cron`/`crond` process (read `/proc/*/comm` or `pgrep -x cron`) and downgrade to `warn: cron daemon not running` in both install and doctor. |
| LOW | `01-spec.md:721-730` (AC-61) | latency probe | The probe runs the real hook: one Ollama embed and one hybrid query per run (N ≤ 200 → up to 200 embeds), plus `git rev-parse` in the temp cwd. Read-only (verified), not free; and if `env.perms` fails the hook exits 0 silently with no work done, so the measured time is a lie. | `hook.latency` is `skip` unless `env.*`, `pg.connect` and `ollama.embed` passed; cap N at 100; print "each run embeds one prompt" in the detail. |
| LOW | `01-spec.md:338-342` (AC-19 default A when `127.0.0.1:5432` accepts TCP) | topology default | A port forwarded by `ssh -L`, a Docker-published port or another Postgres-speaking service makes the default A for a machine with no local server. Only a default, but the next prompt (admin DSN) then points the wrong way. | Default A only when the TCP probe *and* a local socket directory (`/var/run/postgresql`, `/tmp/.s.PGSQL.5432`) or a local `postgres`/`brew services` process exist; otherwise B. |
| LOW | `01-spec.md:714-720` (AC-60) | JSON stability | `remedy` may be empty, `platform` omits the effective config dir, `detail` is free text that tests will golden. | Always emit every key (`remedy: ""`), add `platform.config_dir`, `platform.bin`, and keep `detail` out of golden comparisons except for a few checks; version the schema (`schema: 1`) as planned. |
| LOW | `01-spec.md:88-99` (scenario: "14 pass · 1 warn"), `:674-706` (24 checks) | nit | The scenario's count does not match the table. | Fix on the v0.2 pass. |
| LOW | `01-spec.md:481-488` (AC-35) | binary step | Replacing `~/.local/bin/claude-memory` while Claude Code's `serve` and a launchd job may be running: rename is safe (old inode lives on). On darwin/arm64 Go ad-hoc signs the binary, so a *copied* binary runs; a *downloaded* one carries quarantine (the spec's hint covers it). Worth one sentence so a future reader does not add `codesign`. | State both facts in AC-35. |
| LOW | `01-spec.md:160-163` (§4 single user) | root guard | `sudo claude-memory install` would install into `/root` and register MCP for root. | Exit 2 when `os.Geteuid() == 0` unless `--allow-root`. |
| LOW | `01-spec.md:559-568` (AC-43 PATH "as found at install time") | jobs PATH | `claude` installed via a version manager (`nvm`, `volta`) lives under a versioned directory that changes on upgrade; the captured PATH goes stale silently. | Also add the directories' *parents' stable shims* when detectable (`~/.volta/bin`, `~/.nvm/current/bin` symlink) and have doctor `tools.claude` re-check with the *job's* PATH, not the shell's. |
| LOW | `01-spec.md:494-509` (AC-37), Claude Code behaviour | hook activation | Whether a running Claude Code session picks up a changed `settings.json` without restart is unverified; the final message should not promise it. | WI-0: check; the install summary prints "restart Claude Code sessions to load the hooks" unless WI-0 shows a reload. |
| LOW | `01-spec.md:272-279` (AC-9 `flock` on `install.lock`) | lock | Fine on local filesystems. `flock` on an NFS home is advisory-at-best; a stale lock after a crash is harmless because `flock` releases on process exit. | Say "process-scoped `flock`; never a PID file". |
| LOW | `01-spec.md:390-405` (AC-24 C2) | value | The bundle is `deploy/` byte-for-byte plus `.env` and a rendered steps file; the owner's server already runs it from a clone (`DEPLOY.md:30-34`). | See HIGH #1: reduce C2 to "write `deploy/.env` with generated passwords + print the steps", or defer. |
| LOW | `01-spec.md:369-389` (AC-23 C1 `pg_hba.conf` "allows only `claude_memory`") | docker-local | The healthcheck is `pg_isready -U postgres … -h 127.0.0.1` (`docker-compose.yml:63`), which needs a `host … 127.0.0.1/32` line, and `initdb` runs as the superuser over the socket. The sentence is right in spirit; the golden file must keep both lines. | State the three `pg_hba` lines explicitly in AC-23. |
| LOW | `01-spec.md:424-433` (AC-28 "rewritten to the plain form … with consent") | env file | A `KEY="value with spaces"` line loses its quotes in the plain form and `LoadFromFile` would keep them if left; the conversion must *strip* quotes only when they wrap the whole value, and must never touch a value it does not understand (`$(...)`, backslashes). | Add `quoted-whole-value → unquoted`, `anything else → finding + keep` to AC-27's finding kinds and the golden cases. |
| LOW | `02-plan.md:372-375` (WI-15 pull progress "single updating line on a TTY") | output | Fine; note `NO_COLOR` does not disable cursor movement, and `CI=true` should. | Single-line progress only when stdout is a TTY and `CI` is unset. |

## The §13 owner questions, with a recommendation each

- **#1 CLAUDE.md default location.** Agree: user-level `~/.claude/CLAUDE.md`
  by default, `--claude-md` for a shared file. Two additions: the
  location-neutral rewording must also cover `integration/skills/remember/
  SKILL.md:3,11`, which cites `acme/CLAUDE.md` twice (M-CLAUDE.md), and
  the AC-42 `--yes` sentence needs the unambiguous form given above.
- **#2 Prebuilt binaries.** Agree, as a follow-up and not before the first
  slice has been used. Note for that PR: the release workflow must pass the
  same `LDFLAGS` as the Makefile (otherwise `version` is empty on the one
  artifact people download), and a downloaded darwin binary carries
  `com.apple.quarantine` — the AC-35 hint already covers it, nothing else is
  needed (Go ad-hoc signs darwin/arm64 binaries).
- **#3 Running `brew` from the wizard.** Agree: hints only. Keep the
  "re-check" loop cheap — re-run only the blocked step's Detect, not the
  whole table — or the owner will feel the friction the recommendation
  worries about.
- **#4 `golang.org/x/term`.** Agree. It is one package, its only dependency
  is already in the graph, and `IsTerminal` is needed anyway for AC-11 and
  AC-14. The `stty` alternative would also violate the package's own
  "never a shell" rule.
- **#5 Remote Ollama.** Agree: allow with warning and confirmation. Make the
  warning quantitative, because the consequence is not "slower" but
  "silent": DEPLOY.md's measured CPU numbers (0.23 s for 15 tokens, 2.3 s
  for 150) exceed the hook's 800 ms `MEMORY_HOOK_TIMEOUT` for an ordinary
  prompt, and the hook then exits 0 with **no cards at all** (`hook.go:
  145-150`). The warning should say "with a remote CPU Ollama the prompt
  hook will usually time out and inject nothing; `memory_search` still
  works", and doctor's `ollama.embed > 500 ms` warn is the right watchdog.
- **#6 launchd labels.** Agree: keep `io.github.claude-memory.*`. A
  rename buys nothing and costs an upgrade migration.
- **Three questions the spec should add.** (#7) Is the first slice below
  acceptable, i.e. doctor + install for topologies B and A-with-existing-DB
  first, docker/cron/uninstall-purge later? (#8) May `install --upgrade` be
  the same thing as `install --yes` (one code path, one set of defaults)?
  (#9) Given the server already runs `deploy/` from a clone, is C2 worth
  anything beyond writing `.env` and printing DEPLOY.md's steps?

## A cut-down first slice (recommendation for HIGH #1)

Two PRs, each useful on its own, together ~2.5–3.5 k lines including tests
(against 6–8 k for the full plan):

**Slice 1 — doctor + plumbing** (= PR A minus the latency probe and minus
systemd/cron job detection): early dispatch for `doctor`/`version`/
`install`/`uninstall`; `version` + Makefile `LDFLAGS`; `migrate`;
`config.ParseEnvFile`; `integration`/`deploy` embed packages (dotfile named
explicitly); `internal/setup` with `Paths`, `FS`, `Runner`, `Clock`,
`DBProber`, `OllamaProber`, the `Redactor`, the fakes and the import guard;
doctor checks `binary.version`, `env.*`, `pg.*`, `ollama.*`, `tools.*`,
`mcp.registered` (file read, BLOCKER), `hooks.*`, `skills`, `claude-md`,
`namespaces`, `jobs` (launchd plist/`launchctl print` + the
run-with-env warning), `dirs.state`, `manifest`; text + `--json`; the
DEPLOY.md env fix. The owner runs it on the live setup the day it merges.

**Slice 2 — install for the owner's topologies**: the engine with
`Detect → plan → one confirmation → Apply → re-Detect`, states
`absent | ok | outdated | modified | blocked`, choices `apply | keep |
skip`; manifest; `--yes`, `--dry-run`, `--skip`; topologies **B** and **A
against an existing database** (the `database` step probes, and when
`vector` is missing writes `bootstrap.sql` and prints the one `sudo -u
postgres psql <` command, HIGH #2); steps `envfile`, `migrate`, `ollama`
(probe + pull + 1024-dim check), `namespaces`, `hooks.scripts`,
`hooks.settings` (the merge per HIGH #3), `mcp` (file-read Detect, CLI
Apply), `skills`, `claude-md`, `jobs` for launchd and systemd (PATH
captured, direct binary), final in-process doctor; `uninstall` that
reverses the manifest without purge flags. `install --upgrade` is an alias
of `install --yes`.

**Deferred until asked for**: C1/C2 docker bundles (AC-23/24), cron
backend (AC-46), in-process admin bootstrap (AC-21 branch 1), `--only`,
`--purge-*`, `foreign` adoption beyond "byte-identical → ok", the seed step,
the latency probe (AC-61), hand-install heuristics (AC-51 beyond the
identity rules), the install lock (a plain `O_EXCL` lock file suffices for
one user), the five-backup rotation (one timestamped backup is enough).

Nothing in the deferred list changes the ports or the engine; each is a new
`Step`/`JobManager` or a flag.

## Safety walk-throughs

### settings.json (HIGH #3, plus what is already right)

- *Atomic write, same directory, rename:* right; Claude Code readers never
  see a torn file. *Backup before write:* right; keep it, but only when a
  write happens (model-level no-op first).
- *Hash re-check right before rename:* narrows the TOCTOU window to
  microseconds; acceptable. Claude Code also keeps its own `backups/`
  directory for `.claude.json` (observed), not for `settings.json`, so our
  backup is the only one.
- *Comments / trailing commas:* refusing is right. Verified that
  `settings.json` is read as strict JSON by the merge model; whether Claude
  Code tolerates JSONC is irrelevant because we never write what we could
  not parse.
- *Duplicate keys:* a `Token`-based model sees both; the spec must say
  "duplicate key at any level → refuse" (last-wins re-encoding would
  silently drop one).
- *Symlinked file (§8):* edit the target, back up next to it — right; add
  "refuse if the symlink leaves `$HOME`" (§11's rule generalized).
- *Our entry under a `matcher` group:* treat as ours — right; but then the
  replacement must preserve the group's other keys, i.e. edit the inner
  `hooks` array, not the group.

### Secrets

- The design (no password flags, `--pg-password-stdin`, env file as the
  only sink, single redacting output sink, sentinel test) is the right one
  and better than most installers. The findings are the four edges in
  M-secrets. One more: `--dry-run` prints the env-file diff; a *new* DSN
  line must appear as `MEMORY_PG_DSN=postgresql://claude_memory:***@host:
  5432/claude_memory`, and the Redactor must be in place *before* the
  password is read (register the secret, then prompt), or the first error
  message after the prompt can leak it.

### Topologies

- **B** is complete once `sslmode` is optional (M-remote). The error
  classifier (AC-20) should key on SQLSTATE `28P01`/`28000`/`3D000` from
  `*pgconn.PgError` and on `net.Error`/`context.DeadlineExceeded` for
  "unreachable"; verified `3D000` for a missing database here.
- **A** works on Homebrew as written and not on apt/dnf (HIGH #2). The
  `bootstrap.sql` + stdin-redirect form fixes Linux without sudo in the
  wizard and without copying the password by hand.
- **C1** is sound (bridge + loopback publish + named volume; host
  networking is correctly rejected for a laptop); the `pg_hba` must keep
  the healthcheck and socket lines (L-docker-local). `down -v` behind a
  typed `delete` is right.
- **C2** is `cp -r deploy/` + `.env` + a steps file (L-C2); defer.
- **Remote Ollama** is allowed correctly; the warning must mention the
  hook's 800 ms timeout (#5 above).

### Doctor

- Read-only by construction holds *except* for the BLOCKER; with the
  file-read detection it holds everywhere (`postgres.Open` for `pg.*`,
  `GET` only for Ollama, `access(2)` for `dirs.state`, `launchctl print` /
  `systemctl --user show` for jobs).
- Exit codes (0/1/2, `--strict` promotes `warn`) are standard. Add exit 3
  for "could not even start" (bad `HOME`) so scripts can tell "unhealthy"
  from "misconfigured".
- JSON (AC-60) is stable enough with the L-JSON additions.
- Platform detection: WSL match must be case-insensitive (`Microsoft` in
  WSL1 kernels, `microsoft` in WSL2); systemd detection per M-systemd.

### Scenario gaps (not in §3/§8)

- *Linux without a systemd user session but with systemd* — M-systemd.
- *macOS without brew* — covered by hints (table falls back to URLs);
  state that `brew` absent is `info`, not `blocked`.
- *WSL* — covered; add "cron daemon not running" (L-cron).
- *Non-TTY* — AC-11 covers; M-secrets fixes the stdin ordering.
- *Ctrl-C mid-Apply* — AC-9 is right (per-step manifest, atomic writes);
  add "a half-applied `hooks.settings` cannot exist (single rename)" and
  "an interrupted `ollama pull` resumes" (already in §8).
- *Partial failure* — AC-7/AC-8 isolate correctly; the important property
  (verified from `Requires`) is that `hooks.settings` requires `migrate`,
  so a failed migration never leaves a wired hook against an empty schema.
- *Running as root / `sudo`* — L-root.
- *Running from `go run`* — M-test-validity.
- *Two Claude Code config dirs* (`CLAUDE_CONFIG_DIR` set in some shells
  only) — doctor prints the effective dir (§8); install should record it in
  the manifest and warn when a re-run sees a different one.

## Spec ↔ plan ↔ backlog

- **Backlog fidelity.** Every bullet of item 8 is covered: OS/arch
  detection with a clear unsupported message, service-manager choice,
  package hints, the three topologies, every listed step individually
  skippable, re-runnable with a status table and per-step repair,
  `uninstall`, `doctor` with the listed checks, `--json`, exit codes,
  `install` ending with doctor, the optional latency probe. The spec adds
  `migrate` and `version` (both needed), `--upgrade` (mergeable into
  `--yes`), the manifest (needed for uninstall), hand-install adoption, the
  lock, and the two docker bundles as full generators. The "open design
  points" (platforms first, Docker safety, DSN secrets, how much of
  DEPLOY.md to automate) are all answered; the DEPLOY.md answer ("generate,
  never ssh") is right, and the generated part can be much smaller.
- **Inconsistencies to fix in v0.2:** AC-1 vs AC-3 (`migrate` placement,
  M-dispatch); AC-21 vs plan Design 5 (bootstrap SQL sharing, HIGH #2);
  AC-40/AC-58 vs AC-57 (`claude mcp get`, BLOCKER); AC-9 vs AC-50
  (manifest rewrite vs zero writes, M-uninstall); AC-6 `outdated → repair`
  vs §8's `timeout`-differs example (HIGH #3); plan's `t.Setenv("HOME")`
  vs the no-`os` rule (HIGH #4); §10 `FS` lacks the `Lock` the plan adds
  (WI-9); scenario "14 pass" vs 24 checks; AC-42's `--yes` sentence.
- **Plan quality.** The WI graph is correct (A → B → C ‖ D), the risk table
  names the right risks, and "doctor first" is the right rollout. WI-0 is
  the most valuable item in the plan; three of its five questions are now
  answered above (`claude mcp get` shape and exit code, `CLAUDE_CONFIG_DIR`
  relocates `.claude.json`, `mcp add` default scope) and two remain for the
  owner's Mac (`launchctl print` loaded/unloaded output, hook `$HOME`
  expansion and whether a live session reloads hooks).

## Blockers before implementation starts (spec v0.2)

1. **BLOCKER** — `mcp.registered` and the `mcp` step's Detect read
   `$CLAUDE_CONFIG_DIR/.claude.json` instead of running `claude mcp get`;
   `claude mcp add|remove --scope user` remain the only writers. Rewrite
   AC-40, AC-58, AC-57's verification, the `ClaudeCLI` port and WI-7/WI-19.
2. **HIGH #1** — adopt the first slice (or an explicit alternative) and
   restructure the PR table; fold `--upgrade` into `--yes`; downgrade C2.
3. **HIGH #2** — topology A on Linux: `bootstrap.sql` (0600, real values)
   + `sudo -u postgres psql … < file` as the primary path; reconcile AC-21
   with plan Design 5.
4. **HIGH #3** — settings.json: `UseNumber` + `InputOffset` raw leaves,
   model-level no-op, indentation preserved, `modified` vs `outdated` for
   our own entry, duplicate-key refusal, other settings files scanned by
   doctor.
5. **HIGH #4** — `Paths` value injected from `main.go`; drop `Verify`;
   `PlatformInfo` as input; three choices.

Recommended in the same v0.2 pass, not blocking: M-dispatch, M-systemd,
M-doctor-time (concurrent probes), M-secrets (min length, stdin order,
base64 `/` remedy), M-uninstall (manifest trimming), M-jobs-doctor
(run-with-env warning in PR A), M-CLAUDE.md (wording incl. skills),
M-test-mechanics (FakeRunner matcher, guard list), M-remote (`sslmode`).
