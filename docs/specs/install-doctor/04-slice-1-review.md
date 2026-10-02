# Implementation Review — install/doctor, slice 1 (doctor + plumbing), iteration 1

- **Reviewer:** architecture-reviewer (post-implementation pass on slice 1
  only; slice 2 (`install`/`uninstall`) is not implemented and not reviewed,
  except where a slice-1 library it will reuse has a defect).
- **Target:** branch `claude/task-rb9lio`, fixed range `4499f1c..4e4ffe0`
  (code only, `:!docs`), against `01-spec.md` v0.2 (the `[S1]` ACs of §14),
  `02-plan.md` v0.2 (WI-S1-0..WI-S1-12) and `03-architecture-review.md`
  (iteration 0). Line numbers are at `4e4ffe0`. The working tree was
  identical to `4e4ffe0` before and after this review (`git status
  --porcelain` empty at both ends); every probe below was injected with
  `go test -overlay` from the scratchpad or run against the built binary in
  temp HOMEs, so nothing in the tree was written.
- **Verified locally (this review ran them):** `go build ./...`, `go vet
  ./...`, `go vet -tags integration ./...`, `go test -race -count=1 ./...`
  (20 packages `ok`), the `integration`-tagged suite with
  `MEMORY_TEST_PG_ADMIN_DSN` against the local Postgres 16.14 + pgvector
  0.6.0 (`./internal/postgres/... ./cmd/claude-memory/... ./internal/setup/...`
  all `ok`), `go test -short ./internal/setup` (guard skipped, rest green),
  `make build` (`claude-memory 4e4ffe0 (revision 4e4ffe08af9d)`),
  `GOOS=darwin GOARCH=arm64 go build` (ok). The built binary ran `doctor`,
  `doctor --json`, `doctor --strict`, `version` and `migrate` against 20
  temp HOMEs (healthy hand install, empty, `export`-form env, quoted DSN,
  DSN with an unencoded `/`, 0644 env, env-is-a-directory, BOM+CRLF env,
  database without pgvector, wrong SCRAM password (raw and %-encoded, with
  `$ ( ) + *` in it), 4-character password with an unencoded `@`, right
  password, 768-dim and 900 ms Ollama, hanging Postgres + hanging Ollama,
  duplicate + legacy + stray hook entries with `settings.local.json` and a
  project `.claude/settings.json` duplicate, `settings.json` symlinked
  outside HOME, `CLAUDE_CONFIG_DIR` relocation (absolute and relative),
  `.claude.json` registering a marker-creating script with marker-creating
  hook scripts and a marker-creating fake `claude` first on PATH, `HOME`
  unset and relative). Fake Ollama servers were Python `http.server`
  instances; the hang sink accepted TCP and never answered. Four overlay
  probe tests fuzzed the settings codec (31 inputs), the Markdown block
  library (8 inputs), the redactor and `RunChecks`. **Not runnable here:**
  anything launchd (no macOS), so AC-44 and the darwin half of `jobs` are
  reviewed from the code and the unit fixtures; the owner's manual run on
  the live Mac (WI-S1-12) is still pending.
- **Gate:** **FAIL** — 1 high, 5 medium, 9 low, 6 info. The slice is in
  very good shape: every iteration-0 blocker and high that belongs to slice
  1 is resolved in code and tested, the doctor is read-only by construction
  *and* by observation (byte-identical HOME snapshot after three runs; no
  marker created by any registered command), the settings codec keeps
  big integers, escapes, key order and indentation byte-for-byte, exit
  codes and the JSON schema are exactly as specified, and the hanging-
  endpoint bounds are `max`, not `sum` (1.01 s with `--timeout 1s`). The
  gate fails on one spec-visible secret leak that slice 1 itself
  introduces: `claude-memory migrate` prints a fragment of the database
  password for exactly the DSN shape the spec calls out (a base64 password
  with `/`), because `cmdMigrate` bypasses the sanitizing path the probe
  already has. It is a three-line fix. The DoD bookkeeping (plan status,
  the recorded manual run) is the second item before slice 1 is marked
  done.

## Blockers (before slice 1 is marked done)

1. **HIGH — `migrate` leaks a password fragment on an unparseable DSN
   (AC-3 "exits 1 with a redacted error", AC-30).**
   `cmd/claude-memory/migrate.go:33-40` calls `postgres.New` directly and
   redacts with its own `redactWithDSN` (`:43-49`), which registers the
   password literal and applies the userinfo pattern. pgx's
   `ParseConfigError` for a URL that `net/url` rejects embeds the
   `net/url` message, and that message quotes the text it mis-parsed as a
   port. Verified with the built binary and
   `MEMORY_PG_DSN=postgresql://claude_memory:ab/cd+Efgh12345==@127.0.0.1:54329/rev_ok`:

   ```
   error: migrate: parse dsn: cannot parse `postgresql://claude_memory:***@127.0.0.1:54329/rev_ok`: failed to parse as URL (invalid port ":ab" after host)
   ```

   `ab` is the password up to its first `/`. Neither redactor form matches
   a fragment, so no redactor can fix this after the fact; the adapter
   already knows it: `internal/postgres/probe.go:184-189` (`parseError`)
   withholds the detail whenever the DSN carries a password, and
   `Prober.Migrate` (`probe.go:105-124`) uses it. Overlay probe: `Prober{}.
   Migrate` on the same DSN returns `migrate: parse dsn: the DSN does not
   parse (details withheld: they may quote the password); URL-encode any /
   ? # @ : % in the password`. Doctor's `env.format` on the same HOME is
   also clean (it never reaches pgx). `TestMigrateErrorIsRedacted`
   (`migrate_test.go:50-58`) and `TestMigrateErrorNeverContainsDSN`
   (`probe_test.go:242-255`) both use DSNs that parse, so neither sees it.
   **Fix:** `cmdMigrate` calls `postgres.Prober{}.Migrate(ctx, cfg.PGDSN)`
   and drops `migrateDSN`/`redactWithDSN`/`dsnPassword` from `cmd` (one
   code path, which is also what AC-25 requires of the slice-2 step). Add
   the `ab/cd+…==` DSN to `TestMigrateErrorNeverContainsDSN` and a
   `runChild` row in `dispatch_test.go` asserting the output lacks `:ab`.

2. **MEDIUM — Definition-of-Done bookkeeping.** `02-plan.md:3` still says
   "Status: not started"; WI-S1-12's manual item ("run on the owner's
   current setup and paste the output here", expected `jobs` warn on
   `run-with-env.sh` and `hooks.settings` warn on the legacy `$HOME` form)
   is unrecorded; spec §14's slice-1 boxes are unticked.
   `docs/specs/README.md:8,133-140` already says "slice 1 implemented", so
   the two documents disagree. This review cannot do the Mac run (no
   launchd here). **Fix:** run `bin/claude-memory doctor` on the live
   install, paste the (redacted) output under WI-S1-12, update the plan
   status line, tick §14 S1.

## Findings (severity ranked)

| Sev | file:line | Rule / AC | Claim | Recommendation |
|---|---|---|---|---|
| HIGH | `cmd/claude-memory/migrate.go:33-49` | AC-3, AC-30 | `migrate` prints a password fragment for a DSN whose password holds `/` (verified: `invalid port ":ab"`). | Blocker 1. |
| MEDIUM | `docs/specs/install-doctor/02-plan.md:3`; WI-S1-12; spec §14 | DoD | Plan status stale; manual owner run unrecorded. | Blocker 2. |
| MEDIUM | `internal/setup/mdblock.go:52-64,75-105,171-212` | AC-42 (library) | Marker lines inside a fenced code block count as the block. Overlay probe: a CLAUDE.md that *documents* the markers in a ```` ```md ```` fence (and has no real block) is reported `modified` by doctor, `UpsertMDBlock` rewrites the example *inside the fence*, and `RemoveMDBlock` deletes the fence's content (`"…```md\n```\n…"`). A fence holding only the BEGIN line next to a real block is refused as "2 begin and 1 end markers" (safe but wrong). Slice 1 impact is a wrong `claude-md` state; the library ships now and slice 2 writes with it. | Track fences in `mdLines`/`FindMDBlock` (a line matching ``^\s*(```|~~~)`` toggles "in fence"; marker lines inside are ordinary text). Add goldens `fenced-markers-only` (→ absent, insert at end) and `fenced-begin-plus-block` (→ ok/refresh). |
| MEDIUM | `internal/setup/settings.go:541-559`; `internal/setup/checks_claude.go:194-197` | AC-38 vs AC-57/AC-58 `hooks.settings` | Doctor reports **FAIL** `refusing to edit …/settings.json: symlink target … is outside <HOME>` (verified) for a `settings.json` symlinked to a file outside HOME — a working Claude Code setup (dotfiles kept outside `$HOME`, e.g. `/opt/dotfiles`, `/Volumes/…`). Doctor edits nothing, so the AC-38 refusal is the wrong rule here; the exit code becomes 1 and the hooks are not analyzed at all. | In doctor, read through any symlink (a `ReadSettingsFile` option, or `followOutsideHome bool`) and analyze the target; report the outside target as `warn: … is a symlink outside HOME; claude-memory install will refuse to edit it (AC-38)`. Keep the refusal for the slice-2 write path. |
| MEDIUM | `internal/setup/doctor.go:294-304`; `internal/setup/checks_env.go:104-128` | AC-58 `env.format` ("fail if DSN unusable") | `setting()` lets the shell environment win, mirroring `LoadFromFile`. The owner's realistic case: a shell profile still `source`s the old `export`-form env file (DEPLOY.md's previous instruction), so `MEMORY_PG_DSN` is set in the shell, the file's line is `export MEMORY_PG_DSN="…"` (ignored by the binary), and doctor reports `env.format: warn` and `pg.connect: pass` against the shell's DSN while hooks, the MCP server and the jobs — which never see the shell — have no DSN at all. Verified: with the DSN in the environment the `export`-form file downgrades from FAIL to WARN. The one defect slice 1 exists to catch becomes exit 0. | When `src == "environment"` and the env file either lacks `MEMORY_PG_DSN` or carries a finding on it, report `fail` (not warn) with "hooks, the MCP server and scheduled jobs do not see your shell's MEMORY_PG_DSN; the file's line is not read". Also name the source in `pg.connect`'s detail. |
| MEDIUM | `internal/setup/settings.go:95-107` | §2 "Our hook entry", AC-37 identity | `IsOurHookCommand` matches by substring: `strings.Contains(c, "claude-memory hook")`. Overlay probe: a user entry `echo 'not claude-memory hook'` is classified ours → `modified` → doctor warns, and slice 2 with the overwrite `Confirm` would replace it. Pathological today, but identity is the only mark we have (no marker key, by design), so it must be tight. | Match the first shell word: `(^|[\s'"])(\S*/)?claude-memory\s+(hook|extract)\b`, i.e. `claude-memory` must be the program, not a word in an argument. Add the negative case to `TestIsOurHookCommand`. |
| LOW | `internal/setup/jsonobj.go:505-515,485-503` | AC-37 "Indentation" | A CRLF `settings.json` comes back with mixed line endings after a merge: untouched bytes keep `\r\n`, every emitted line uses `\n` (verified: `…"echo hi"}]}],\n    "UserPromptSubmit": [\n …\n    ]\r\n  }\r\n}\r\n`). Valid JSON; Claude Code reads it; a dotfiles diff is noisy. Slice-2 impact only. | Detect the newline like `mdblock.newlineOf` and use it in `jsonEmitter.indent` and for the final newline. Add a `crlf` golden. |
| LOW | `internal/setup/redact.go:24`; `internal/setup/doctor.go:324-343` | AC-30 pattern edges | (1) `postgres://u:p@ss@h/db` → `postgres://u:***@ss@h/db`: a password shorter than `MinSecretLen` that contains an unencoded `@` (pgx accepts it: `net/url` splits on the *last* `@`) is only half masked by the pattern. (2) `postgres://h:5432/db?application_name=a@b` → `postgres://h:***@b`: a password-less DSN with `@` in the query is over-masked, and `dsnPasswords` registers `5432/db?x=a` as a secret. (3) `dsnPasswords("host=h password='se cret'")` yields `se` (cut at the space). No slice-1 output path prints the raw DSN (verified: pgx connect errors carry user/db/host only), so none of these leaked in practice. | `dsnPasswords`: parse with `url.Parse` and take `u.User.Password()` (falls back to the current split only when parsing fails); for key/value DSNs use a quote-aware scanner. Pattern: make the password part `[^@\s]*(@[^@\s/]*)*` or anchor on the last `@` before the first `/`. |
| LOW | `internal/config/config.go:355-410` | AC-27 | A UTF-8 BOM makes the first key `"﻿MEMORY_PG_DSN"` → `unparseable-value: invalid key` (verified), and `env.format`'s remedy then says "add MEMORY_PG_DSN=…" although the line exists. CRLF files are fine (`TrimSpace`). | Strip a leading BOM in `parseEnv` (or report `bom` by name with the remedy "remove the byte-order mark"). |
| LOW | `internal/setup/checks_env.go:161-191` | AC-58 `env.format` | A DSN wrapped in quotes that comes from the *environment* (no finding available) is reported as `scheme "postgres" is not postgres:// or postgresql://` because `:175` trims the quotes out of the message, hiding the cause. From the env file the `quoted-whole-value` finding supplies the right remedy. | Say `scheme %q` with the quotes kept, or special-case a leading quote: "the value is wrapped in quotes". |
| LOW | `internal/setup/guard_test.go:22-28,32-36,65-116` | AC-63, AC-68 | Guard strength, read-only check: `TestGuardImports` uses `go list -deps`, which is transitive, so a helper package importing `os/exec`, `net/http`, pgx or `x/term` **is** caught. `TestGuardNoAmbientOSCalls` globs only `*.go` in the package directory, so a future sub-package of `internal/setup` calling `os.Getenv` would slip; and the ban list covers ambient *environment* only — `os.ReadFile`, `os.Stat`, `os.WriteFile`, `net.Dial` bypassing the FS/Runner ports would pass both guards. No violation today (zero `os.` selectors in non-test setup sources; `net` is used for `net.ParseIP` only, `checks_ollama.go:6,22`). | Walk `./...` under the package in the AST guard; extend `bannedOSCalls` with the FS verbs (`Open`, `OpenFile`, `Create`, `ReadFile`, `WriteFile`, `ReadDir`, `Stat`, `Lstat`, `Mkdir*`, `Remove*`, `Rename`, `Chmod`, `Symlink`) and ban `net.Dial*`/`net.Listen*`; the only `os` identifiers setup needs are errors and `FileMode` constants. |
| LOW | `internal/setup/doctor_test.go:895-896`; `testdata/doctor/*.golden.json` | AC-60 Verify ("`detail` excluded from comparison except for a few checks") | The goldens compare every `detail` (with `$ROOT` normalized). Stable, but any wording change in any check rewrites both goldens, which weakens them as a *schema* guard. | Zero/strip `detail` for all but a chosen few (e.g. `env.file`, `mcp.registered`) before `checkGolden`, or keep the full goldens and add a separate key-set assertion (already present at `:898-919`) as the schema contract — then say so in the test. |
| LOW | `internal/setup/doctor.go:151-178` | AC-59 | When `--deadline` fires, dependents of an unfinished check report `fail: timed out waiting for pg.connect` (verified: 7 fails for two hanging endpoints with `--deadline 1s`, vs 2 fails + 5 skips with `--timeout 1s`). Spec: "unfinished checks report `fail: timed out`"; a dependent that never started is arguably `skip` ("because pg.connect timed out"), and the inflated fail count is noise in `--json` consumers. | Report dependents of a timed-out prerequisite as `skip`, keep `fail` for the check that was actually running. Both readings satisfy the text; pick one and state it in AC-59. |
| LOW | `internal/config/config.go:225-255` | AC-27 ("unchanged behavior") | `LoadFromFile` no longer sets `export KEY` (as the key `export KEY`) nor keys that fail `envKeyRe`; both reached `os.Setenv` before. Harmless (nothing read those names) and arguably better, but it is a behaviour change the AC says there is none of. `TestLoadFromFileSkipsExportAndKeepsFirstValue` enshrines the new behaviour. | Note it in AC-27 ("…unchanged for well-formed files; `export` lines and invalid keys are now ignored instead of polluting the environment"). |
| LOW | `internal/setup/checks_claude.go:231-249` | AC-58 `hooks.settings` | The warn detail is one unbounded line: with duplicates, a legacy entry, a stray event, an AC-70 duplicate and an unreadable project file it ran to ~600 characters (verified). `joinDetail` caps extra items only when `fails` exist. | Cap `warns` with `joinDetail`'s `+N more` too (or print one bullet per issue in the text renderer). |
| INFO | `cmd/claude-memory/setup_adapters.go:55`; `cmd/claude-memory/extract.go:113` | §4 (Windows out of scope) | `GOOS=windows go build` fails; it already failed before this range (`Setsid`), `syscall.Access` adds a second site. Spec §4 says doctor "still runs its platform-independent checks" on an unsupported OS — it cannot be built for Windows at all. Out of scope by §4; stated so nobody is surprised. | Either drop the sentence from §4 or guard the two sites with build tags when Windows ever matters. |
| INFO | `internal/setup/doctor.go:180-211` | AC-59 goroutines | A check that ignores its context keeps exactly one goroutine alive until it returns (overlay probe: baseline 2 → 3 right after `RunChecks` returned → 2 once the 1.5 s sleeper finished); a context-honouring check ends at `ccancel`. The outer slot goroutine never blocks (`ch` is buffered). No permanent leak; the process exits anyway. | None. |
| INFO | `internal/setup/mdblock.go:75-85` | AC-42 | `<!-- BEGIN claude-memory --><!-- END claude-memory -->` on one line is not a marker (whole-line rule) → a second block is inserted below it. Consistent with the stated rule; noted for the goldens. | Optionally refuse when a marker string appears anywhere that is not a whole line. |
| INFO | `internal/setup/mcpreg.go:151-181`; `checks_claude.go:74-124` | AC-40 | `mcp.registered` compares against `<BinDir>/claude-memory`; a registration pointing at a repo build (`…/bin/claude-memory`) is `outdated → warn` with the remove+add remedy. Correct per AC-40; the owner's INSTALL.md registration uses `$HOME/.local/bin/claude-memory` (`INSTALL.md:101`), so the §3 scenario holds. | None. |
| INFO | `.github/workflows/ci.yml:27`; `Makefile:29` | AC-65 | The integration job adds `./cmd/claude-memory/...` (S1); `./internal/setup/...` is S2 per the plan. The unit job runs without `-short`, so `TestGuardImports` runs once in CI as planned. | None. |
| INFO | `cmd/claude-memory/main.go:206-240` | composition root | `buildSetupDeps` is the only constructor of setup adapters; `detectPlatform` runs one read-only `sw_vers` on darwin. `migrate.go` constructs through `postgres.New` instead of the `Prober` adapter (blocker 1 is the consequence). | Blocker 1 removes the deviation. |

## Status of iteration-0 findings that belong to slice 1

| # | Iteration-0 item | Status | Evidence |
|---|---|---|---|
| BLOCKER | `mcp.registered` / Detect must not run `claude mcp get\|list` | **RESOLVED** | `mcpreg.go:84-141` reads `Paths.ClaudeJSON` through the FS port, takes no Runner; `ports.go:202-205` `ClaudeCLI` has only `MCPAdd`/`MCPRemove`; `guard_test.go:132-153` denies `mcp get`/`mcp list` for *any* argv[0] and any `claude` call other than `mcp add\|remove --scope user`; the deny list is asserted on every `FakeRunner.Run` and again at cleanup (`fakes_test.go:76,125`). Unit: `TestDoctorNeverRunsRegisteredMCPCommand`, `TestMCPDetectionNeverExecutes`; cmd: `TestDoctorBinaryNeverRunsMCPCommand` (fake `claude` and fake `claude-memory` first on PATH). This review: `.claude.json` → marker script, hook scripts → marker scripts, `~/.local/bin/claude-memory` → marker script, fake `claude` on PATH; after `doctor`, `--json`, `--strict`: **zero markers**. |
| HIGH #1 | scope cut into slices | **RESOLVED** | Only WI-S1-0..12 landed; `doctor --latency` exits 2 with the §12.1 pointer (verified); no engine, prompter, x/term (`go.mod` unchanged). |
| HIGH #2 | topology A bootstrap | n/a (S2) | `deploy/embed.go:1-21` embeds `initdb` + `.env.example` (dotfile named explicitly, as the review required); `app-role.psql` is S2. |
| HIGH #3 | settings.json merge model | **RESOLVED (library)** | `UseNumber` + `InputOffset` raw slices (`jsonobj.go:172-264`); duplicate keys refused at any level, including an escaped duplicate `"a"`/`"a"` and a nested one (overlay probe); model-level no-op (`timeout: 5.0`/`5e0` → `ok`, no write; `tab-indented-noop`, `four-space`, `unicode-escapes-unchanged-noop`, `big-number-unchanged` goldens); `123456789012345678901234567890`, `1.0000000000000000001`, `1e400`, `-0`, `"café \/ x"` all byte-preserved through a merge (probe); `outdated` only when equal to the manifest's recorded canonical entry, else `modified` + Drift (`settings.go:290-310`; `ours-user-timeout`, `ours-legacy-home` goldens kept under merge); in-place replacement keeps a group's `matcher` and sibling entries (probe); AC-70 scan of `settings.local.json` and `<cwd>/.claude/settings*.json` (verified live). Residual: CRLF mixed newlines (LOW), fenced markers are mdblock (MEDIUM). |
| HIGH #4 | `Paths`/`Env` values, no `Verify`, `PlatformInfo` as data | **RESOLVED** | `paths.go`, `platform.go` (data), `buildPaths` pure over `getenv` (`cmd/paths.go:19-57`, table test with/without `CLAUDE_CONFIG_DIR`); zero `os.` selectors in non-test setup sources; no `t.Setenv` in setup tests (grep); `Step` has no `Verify` (`01-spec.md:1286`, not yet implemented). `CLAUDE_CONFIG_DIR` relocates `settings.json`, hooks dir and `.claude.json` (verified: 19 pass with a relocated dir; relative value → exit 3). |
| M-dispatch | `migrate` after config load; doctor/version before | **RESOLVED** | `main.go:77-95,134-135`; `TestEarlyDispatch` (0644 env file: `version` works, `doctor` reports `FAIL env.perms`, `migrate` refuses). Verified live. |
| M-doctor-time | concurrent probes, `--timeout` + `--deadline` | **RESOLVED** | `RunChecks` (`doctor.go:120-232`): two hanging endpoints → wall 1.01 s with `--timeout 1s --deadline 3s`, 1.01 s with `--deadline 1s`, 3.02 s with defaults; healthy fakes < 1 s (`TestDoctorHealthy`). |
| M-secrets (1),(3) | ≥ 8-char literal rule; base64 `/` remedy | **RESOLVED** | `redact.go:16` `MinSecretLen = 8`; `env.format` → "URL-encode the password (/ → %2F …) or use a URL-safe one (openssl rand -hex 32)" (verified); DEPLOY.md switched to `-hex 32` with the explanation (`DEPLOY.md:44-47`). (2) stdin order and (4) accepted exposures are spec/S2. |
| M-jobs-doctor | `jobs` warns on `run-with-env.sh` and missing PATH dirs | **RESOLVED in code (read-only here)** | `jobs_launchd.go:135,147-160` (`Legacy` by base-name suffix — the hand install's `claude-memory-run-with-env.sh` matches; `MissingPathDirs` from `LookPath(claude\|git\|az)`); `checkJobs` remedy names slice 2; fixtures `legacy-cleanup.plist`, `launchctl-print-loaded.txt`, exit 113 "Could not find service" for not loaded. No launchd here. |
| M-CLAUDE.md (3) | skills reworded | **RESOLVED** | `remember/SKILL.md`, `memory-digest/SKILL.md` no longer cite `acme/`; `TestAssetsAreLocationNeutral`. |
| M-test-mechanics | matcher-scripted FakeRunner; base-name deny list; `go list` once | **RESOLVED** | `fakes_test.go:82-106` (`Script`, `ScriptError`, `Deny`, unmatched → test failure); `guard_test.go:122-126` list is the AC-18 set, matched on `filepath.Base`; `TestGuardImports` skips under `-short`, CI runs without it. |
| M-remote | `sslmode`; doctor reports TLS | **RESOLVED (doctor half)** | `probe.go:223-233` `pg_stat_ssl`; `pg.connect` detail says `TLS`/`no TLS` (verified "no TLS"). Prompt is S2. |
| M-test-validity (1) | test cwd outside the repo | **RESOLVED** | `runChild` sets `cmd.Dir = t.TempDir()` (`dispatch_test.go:21`); `TestDoctorBinaryNeverRunsMCPCommand` builds the real binary and runs from the temp root. |
| M-platform | WSL case-insensitive | **RESOLVED** | `platform.go:41`; rows "WSL1 (capital M)", "WSL2", "Fedora without PRETTY_NAME", "linux without os-release", "Windows", "macOS, sw_vers fails" in `TestDetectPlatform`. |
| L-version | single `LDFLAGS`, `dev` fallback | **RESOLVED** | `Makefile:9-10,16,47-50` (build, cross-build incl. darwin/amd64 + linux/arm64); `version.go:22-47`; `make build && bin/claude-memory version` → `claude-memory 4e4ffe0 (revision 4e4ffe08af9d)`. |
| L-JSON | all keys, `config_dir`, `bin`, `schema: 1` | **RESOLVED** | `report.go:28-96`; `TestDoctorGoldenJSON` asserts every key; verified `--json` on stdout only, nothing on stderr. |
| Doctor exit 3 | `HOME` unset/relative | **RESOLVED** | `main.go:90-93`; verified: unset → 3, `HOME=rel/x` → 3, `CLAUDE_CONFIG_DIR=rel` → 3. |

## Read-only guarantee (AC-57, AC-67)

- **By construction.** `readOnlyFS` returns `ErrDryRun` from every write
  method (`setup_adapters.go:57-61`); `execRunner{readOnly: true}` returns
  `ErrReadOnly` for `Mutating` (`:81-83`); the Postgres probe opens with
  `pgx.ConnectConfig`, never `postgres.New`, and sets
  `default_transaction_read_only=on` and `application_name=claude-memory-probe`
  (`probe.go:62-64`); `TestProbeIntegrationMakesNoWrites` asserts it on a
  real server. Ollama: `GET /api/version`, `GET /api/tags`, one
  `POST /api/embed` with `keep_alive: -1` like the embedder (`prober.go:181`),
  never `/api/pull`. The only command doctor can run is `launchctl print`
  (darwin) plus `sw_vers` in `detectPlatform`; `LookPath` is a PATH walk.
  Every `internal/setup` test runs the FakeFS in `ReadOnly` mode and asserts
  `len(Writes()) == 0` and `MutatingCalls() == 0` at cleanup
  (`doctor_test.go:176-183`).
- **By observation.** Snapshot of the healthy HOME (`find -printf '%p %s
  %T@ %m'` + `sha256sum` of every file) before and after `doctor`,
  `doctor --json` and `doctor --strict`: **identical** (sizes, mtimes, modes,
  hashes). No marker file appeared from the registered MCP command, the
  hook scripts, the installed binary or the fake `claude`, across text,
  JSON and strict runs. The `--json` run on an empty HOME wrote nothing to
  stderr.
- **Residual.** `Writable()` is `access(2)` (`setup_adapters.go:55`) — no
  write. `dirs.state` on a missing directory walks up to the first existing
  parent and `access`es that (`checks_misc.go:179-190`). Correct.

## Secrets walk-through (AC-30)

- The DSN password is registered with the Redactor in `prepare()`
  (`doctor.go:283-287`) before any check runs, from the env file or the
  `Env` snapshot, as written and percent-decoded; `Register` adds the
  `QueryEscape`, `PathEscape` and userinfo forms and sorts longest-first
  (`redact.go:44-64`). Every renderer redacts each string and then writes
  through a redacting writer (`report.go:11-14`, `main.go:237-238`).
- Verified clean: wrong SCRAM password `Wr0ng.pa$$(w)ord+*` raw and as
  `%24%24%28w%29…` — no form of it in text or JSON output (pgx's
  `28P01` message names only the user); the predecessor's `.claude.json`
  `env` holding a DSN — key names shown, values never
  (`mcpreg.go:174-176`); `describeDSN` never emits the password
  (`checks_env.go:161-191`); `parseError` withholds pgx/net-url detail
  whenever a password is present (`probe.go:184-189`) — verified for the
  `/`-in-password DSN through `doctor` **and** `Prober.Migrate`.
- Leak: `cmdMigrate` (blocker 1). Edges without an observed leak: LOW rows
  on the userinfo pattern and `dsnPasswords`.
- Redactor robustness: `strings.ReplaceAll` is literal, so `$ ( ) + * [ ]`
  in a secret are safe (probe: `Wr0ngXpaYYZwZord` untouched); a multi-line
  secret ≥ 8 characters is masked as a literal; the pattern deliberately
  stops at whitespace.

## settings.json codec and merge (AC-37, AC-38)

31-input fuzz through `AnalyzeSettings` → `MergeSettings` → re-parse →
second merge → `UnmergeSettings`, asserting: output strict and
re-parseable by the library's own parser; second merge `changed == false`
and byte-identical; every non-`hooks` top-level key semantically equal;
every original hook group still present; unmerge of what was added
semantically equals the input when the file had no entry of ours.

- **Refused (correctly):** BOM, duplicate key (plain, `a`-escaped,
  nested in a hook entry), trailing garbage, a second top-level value,
  `"hooks": null`, `"hooks.UserPromptSubmit": null`, top-level string /
  number / array, nesting deeper than `encoding/json`'s 10 000 (`exceeded
  max depth`, position reported). 9 000-deep objects parse and re-emit in
  0.15 s.
- **Preserved:** big integers, long fractions, `1e400`, `-0`, `é`,
  `\/`, a surrogate pair, `\u0000`, an escaped `"hooks"` key (decoded
  key matched, raw key bytes kept), empty keys, tabs, 4 spaces, a
  single-line file (expanded only where we insert), a `matcher` group with a
  foreign sibling (replaced entry in place, siblings and `matcher` kept).
- **No-op is semantic:** `"timeout": 5.0` and `5e0` are `ok` with
  `changed == false`; a CRLF file with our entries present is untouched.
- **Round trip:** every `absent` input returned to its semantic original
  after unmerge, including `CreatedHooksKey` removing a `hooks` object the
  merge created.
- Defects: mixed newlines on CRLF input (LOW); substring identity (MEDIUM);
  `event-null` and `null-hooks` are refusals where Claude Code itself would
  probably tolerate `null` — acceptable (refuse rather than guess).

## Markdown block library (AC-42)

Goldens cover insert-at-end, insert without trailing newline, refresh,
idempotent, remove, unbalanced, duplicated, out-of-order, inline marker,
hand-pasted, edited and CRLF, each run twice. Probes: CRLF round trip
exact; markers with trailing spaces or indentation are accepted (whole-line
after `TrimSpace`); a file without an EOF newline is handled; an empty
block refreshes; mixed `\n`/`\r\n` input gets the CRLF style for new text
(`newlineOf` picks CRLF when any is present). Defect: fenced code blocks
(MEDIUM). `InGitRepo` accepts `.git` as file or directory with a ceiling for
tests (`gitrepo.go:18-37`); tested.

## Concurrency, timeouts, exit codes, JSON

- `RunChecks`: one goroutine per check waits on its prerequisites' `done`
  channels or `runCtx.Done()`; the check body runs in an inner goroutine
  with a buffered result channel, so a timed-out body can never block the
  slot; panics are recovered into `fail: internal error`; an empty status
  is a `fail`; a `Requires` naming a later or unknown check is an internal
  error (`TestRunChecksScheduling`). `d.dbStatus` is written only by
  `pg.connect` and read only by checks that `Require` it after `<-done`
  (happens-before via channel close); `-race` is green across 20 packages.
- Exit codes (verified on the binary): healthy 0; `--strict` with warns
  1; fails 1; `--bogus`, positional arg, `--timeout 0`, `--latency`,
  `version x` → 2; `HOME` unset/relative, `CLAUDE_CONFIG_DIR` relative → 3.
  Doctor's exit 1 is quiet (`errQuiet`, `main.go:41-46`); exit 3 prints
  `error: doctor cannot start: …`.
- JSON: `{schema, version, platform{os,arch,jobs,config_dir,bin}, ok,
  summary{pass,fail,warn,info,skip}, checks[{id,title,status,detail,remedy,
  duration_ms}]}`, every key always present, `remedy: ""` when none,
  checks in table order, no ANSI, stdout only (`report.go`;
  `TestDoctorGoldenJSON`; `TestDoctorBinaryNeverRunsMCPCommand` parses it).
  The `empty-home` and `healthy` goldens are the stability contract; see
  the LOW on `detail` in goldens.

## Guard test strength (AC-18, AC-63, AC-68)

- Imports: `go list -deps -f {{.ImportPath}} .` is transitive, so **an
  import cannot slip through a helper package** for the banned set
  (`os/exec`, `net/http`, `database/sql`, `github.com/jackc/`,
  `golang.org/x/term`); the test fails if the package itself is missing
  from the output. Verified by reading; not bypassable with the overlay
  (the subprocess does not see `-overlay`).
- Ambient calls: AST walk over `*.go` in the package dir, catching the
  selector even when not called (`f := os.Getenv`), and dot/blank imports.
  Directory-local and environment-only (LOW above).
- Argv deny list: checked on every `FakeRunner.Run` and at cleanup; base
  name, so `/opt/homebrew/bin/brew` is caught; `claude` allowed only as
  `mcp add|remove --scope user …`; `mcp get|list` denied for any program
  (`npx … mcp list` tested).

## Hexagonal boundaries

- `internal/setup` declares every port (`ports.go`) and depends on
  `internal/config` (pure `ParseEnvData`) and `internal/namespace` (pure
  `Parse`, new in this range, `namespace.go:60-72`) only; `internal/postgres`
  and `internal/ollama` import `internal/setup` for the port types
  (adapter → consumer direction, correct). Adapters are constructed only in
  `cmd/claude-memory/main.go:buildSetupDeps` — with one deviation,
  `cmd/claude-memory/migrate.go` calling `postgres.New` directly, which is
  also the leak (blocker 1). Checks never print (`report.go` renders).
  `detectPlatform` is a function over the FS and Runner ports, returning
  data. `Paths`/`Env` are values; `Env` is allow-listed (`MEMORY_*`,
  `NO_COLOR`, `CI`, `PATH`, `XDG_RUNTIME_DIR`; `TestBuildEnvAllowList`).
- `LaunchdJobs` implements only the read half; the `JobManager` interface
  is declared for slice 2 and nothing claims to implement it yet. Fine.

## Platform detection (AC-15, read-only)

`runtime.GOOS/GOARCH`; darwin: `sw_vers -productVersion` through the Runner
with a 2 s bound, `"macOS"` when it fails; linux: `PRETTY_NAME`, else
`NAME VERSION_ID`, else `Linux` (quotes via `strconv.Unquote` with a trim
fallback); WSL from `/proc/sys/kernel/osrelease` case-insensitively; jobs
backend launchd on darwin, `none` on linux with the slice-2 reason;
unsupported OS keeps running (`checkJobs` → info). Rows for macOS (with and
without `sw_vers`), Ubuntu, Fedora, WSL1, WSL2, no `os-release`, Windows.
The "ssh with lingering user instance" row is slice 2 (systemd). macOS
paths: `LaunchAgentsDir = $HOME/Library/LaunchAgents`, `/var` →
`/private/var` handled in `underDir` via `EvalSymlinks(dir)`
(`settings.go:577-589`).

## Docs accuracy (AC-66, S1 half)

- `DEPLOY.md:93-115`: laptop env snippet is plain `KEY=VALUE`, with the
  explicit "no `export`, no quotes" paragraph and the `doctor` verification
  step; password recipes are `openssl rand -hex 32` with the `/` rationale
  (`:44-47`, `:256-258`); the `psql` check no longer `source`s the file.
  Correct and consistent with `config.ParseEnvFile`.
- `integration/INSTALL.md:9-16,160-163`: points at `doctor` after step 1
  and before the manual verify; the manual steps are unchanged (that
  restructuring is S2 per AC-66).
- `integration/claude-md-snippet.md` now points at the embedded
  `claude-md-section.md`; skills reworded; `docs/specs/README.md:8,131-140`
  says slice 1 is implemented. `02-plan.md:3` contradicts it (blocker 2).

## Spec v0.2 slice-1 ACs vs code

| AC | Code | Test | Verdict |
|---|---|---|---|
| AC-1 [S1] | `main.go:77-95,134` | `TestEarlyDispatch` (empty HOME, 0644 env; child process env, no `t.Setenv`) | met (verified live) |
| AC-2 | `version.go`; `Makefile:9-10,16,47-50` | `TestVersionFormatter`; `make build && version` → revision | met |
| AC-3 | `migrate.go`; `postgres.New` | `TestMigrateAndDoctorPGChecks` (twice, no-op) | **partially met**: redacted-error clause violated for an unparseable DSN (blocker 1) |
| AC-4 [doctor] | `doctor.go:30-55` | `TestParseDoctorFlags`, `TestEarlyDispatch` (`--latency` → 2) | met |
| AC-15 | `cmd/platform.go` | `TestDetectPlatform` (7 rows) | met (S1 rows) |
| AC-18 [guard] | `guard_test.go:122-164`; `fakes_test.go:125` | `TestDeniedCall`; every setup test | met |
| AC-27 | `config.go:300-445` | `TestParseEnvFile` (each kind), `TestLoadFromFileSkipsExportAndKeepsFirstValue` | met; "unchanged behavior" is slightly untrue (LOW) |
| AC-30 [redactor + doctor sentinel] | `redact.go`; `doctor.go:283-287`; `report.go` | `TestRedactor*` (5), `TestDoctorSentinelPassword` (3 cases, raw report too), `TestDoctorBinaryNeverRunsMCPCommand` sentinel | met for doctor; **`migrate` bypasses it** (blocker 1) |
| AC-32 [prober] | `ollama/prober.go` | 11 `httptest` tests (dims, 404, timeout, bad bodies, pull stream/cancel) | met |
| AC-34 [embed] | `integration/embed.go`, `deploy/embed.go` | `TestReferencedAssetsAreEmbedded` (×2), `TestEmbeddedMatchesWorkingTree`, `TestInitDBFullyEmbedded`, `TestDoctorCheckTable` | met |
| AC-37 [library] | `jsonobj.go`, `settings.go` | `TestSettingsGolden` (every spec case + `-overwrite` variants, each twice), `TestCanonicalJSON`, `TestParseJSONKeepsRawBytes`, `TestJSONEditSplicesAndAppends` | met; CRLF LOW, identity MEDIUM |
| AC-38 [library] | `settings.go:164-197,541-573` | `TestSettingsRefusals` (comments, trailing comma, hooks-is-array, truncated, duplicate key, trailing data, top-level array, group/event types), `TestReadSettingsFileSymlinks` | met for the write path; doctor false FAIL on an outside symlink (MEDIUM) |
| AC-40 [detect] | `mcpreg.go` | `TestReadMCPRegistration` (11 fixtures), `…FollowsClaudeJSONPath`, `TestMCPDetectionNeverExecutes` | met |
| AC-41 [doctor states] | `checks_claude.go:56-70,265-310` | branches `skills: not installed/edited`, `hooks.scripts: edited/recorded older version` | met |
| AC-42 [library] | `mdblock.go`, `gitrepo.go` | `TestMDBlockGolden` (12 cases, twice), `TestMDBlockRefusals`, `TestInGitRepo` | met; fenced markers (MEDIUM) |
| AC-44 [doctor] | `jobs_launchd.go` | `TestLaunchdInspect` (fixtures incl. legacy, foreign, unparseable, not loaded), `TestDoctorHealthyDarwin`, `jobs:` branches | met in code; not runnable here |
| AC-49 [read] | `manifest.go` | `TestManifestRoundTrip`, `…CorruptAndUnsupported`, `…RefusesSecrets`; `manifest:` branches | met |
| AC-57 | read-only adapters; `probe.go:62-64` | zero-writes/zero-mutating cleanup in every doctor test; `TestProbeIntegrationMakesNoWrites`; snapshot diff (this review) | met |
| AC-58 | `doctor.go:346-372`; `checks_*.go` | `TestDoctorCheckTable`, `TestDoctorBranches` (≈60 rows, remedy asserted for every non-pass) | met; `env.format` environment-override downgrade (MEDIUM) |
| AC-59 | `doctor.go:120-232`; `main.go:41-72` | `TestDoctorTimeouts`, `TestRunChecksScheduling`, `TestDoctorExit`, `TestDoctorWithoutHomeExits3`; live timing 1.01 s / 3.02 s | met; dependents-on-deadline wording (LOW) |
| AC-60 | `report.go` | `TestDoctorGoldenJSON`; `TestDoctorBinaryNeverRunsMCPCommand` parses 23 checks | met; goldens include `detail` (LOW) |
| AC-63 | `fakes_test.go`, `guard_test.go` | `TestGuardImports`, `TestGuardNoAmbientOSCalls`, fake self-tests; no `t.Setenv` in setup tests (grep) | met; breadth LOW |
| AC-64 [libraries] | `golden_test.go` (`-update`) | settings and mdblock goldens run twice | met |
| AC-65 [S1 half] | `doctor_integration_test.go`, `probe_integration_test.go` | `TestMigrateAndDoctorPGChecks`, `TestDoctorPGWrongDatabase`, 6 `TestProbeIntegration*` — green here against the local server; CI adds `./cmd/claude-memory/...` | met |
| AC-66 [S1 half] | `DEPLOY.md`, both `SKILL.md`, `docs/specs/README.md` | `TestAssetsAreLocationNeutral`; review | met; plan status stale (blocker 2) |
| AC-67 | `mcpreg.go` (no Runner); `guard_test.go:140-144` | unit + cmd tests; marker run (this review) | met |
| AC-68 | `paths.go`; `cmd/paths.go` | `TestBuildPaths` (incl. `CLAUDE_CONFIG_DIR` moving `.claude.json`), `TestBuildEnvAllowList`, guard | met |
| AC-70 [doctor] | `settings.go:650-718`; `checks_claude.go:234-240` | `TestScanOtherSettings`; two `hooks.settings` branches; live run with `settings.local.json` + project file | met |

## Empirically verified vs read-only

- **Empirical (ran here):** build, vet, race unit suite, integration suite
  against a real pgvector server; `make build` + `version`; `migrate`
  twice; doctor on 20 HOMEs in text/JSON/strict; exit codes 0/1/2/3 and
  all usage errors; `CLAUDE_CONFIG_DIR` relocation; read-only snapshot
  diff; AC-67 markers; sentinel-password grep on every output; hanging
  endpoints timed; 768-dim and slow Ollama; missing pgvector; wrong SCRAM
  password; database missing (`3D000`); env `export`/quoted/`/`-in-password
  /0644/directory/BOM; duplicate+legacy+stray hooks with AC-70 files;
  outside-HOME symlink; settings codec fuzz (31), mdblock probes (8),
  redactor probes, `RunChecks` goroutine count, `Prober.Migrate` vs
  `cmdMigrate` on the same bad DSN.
- **Read-only (code and fixtures only):** everything launchd (`jobs` on
  darwin, `launchctl print` exit 113, `sw_vers`); macOS path handling;
  guard transitivity (`go list -deps` semantics); pgx error-message shapes
  other than the ones observed (`28P01`, `3D000`, URL parse); CI workflow
  behaviour; the owner's live hand install.

## Known, not re-reported

- Windows does not build (pre-existing `extract.go:113`; §4 out of scope).
- Slice-2 code present in the range but unused by doctor (`WriteSettingsFile`,
  `UnmergeSettings`, `SaveManifest`, `BackupCorruptManifest`, `Pull`,
  `LocalServerEvidence`): tested in isolation, not reviewed for S2 ACs.
- `plainto_tsquery`, namespaces review items, `serve` cwd assumption
  (unchanged by this range).

## Summary for the owner

Slice 1 does what the spec says and proves it the way the plan asked: the
MCP registration is a file read with a deny-listed fake Runner behind every
test and a marker test on the real binary; the doctor leaves `$HOME`
byte-identical; the settings codec survives everything I threw at it
except a cosmetic CRLF mix; probes run concurrently inside the stated
bounds; exit codes and the JSON document are exactly AC-59/AC-60. Fix
before marking slice 1 done: route `claude-memory migrate` through
`postgres.Prober{}.Migrate` so a `/`-in-password DSN cannot echo `":ab"`
(blocker 1), and record the Mac run plus the plan status (blocker 2). Worth
doing in the same change set because slice 2 will build on them: ignore
markers inside fenced code blocks, let doctor read a `settings.json`
symlinked outside HOME instead of failing it, make the shell-environment
DSN override a `fail` when the file's own line is unusable, and tighten
hook identity to the first argv word.
