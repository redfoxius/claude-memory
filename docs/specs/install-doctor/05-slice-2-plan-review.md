# Plan review — install/doctor, slice 2 (pre-implementation), iteration 1

- **Target:** `01-spec.md` v0.2 §14 [S2] + `02-plan.md` v0.2 WI-S2-1..16, against master `cd28e43` (slice 1 + its review fixes merged).
- **Runs:** two independent `plan-reviewer` passes — Sonnet 5.5 (A) and Opus 5.5 (B). Verdicts **disagree**: A "ready with risks", B "NOT READY" (1 blocker, 5 high). Per the SDD rule the disagreement is itself a finding: A treated the engine's interaction/data model as a per-item risk, B as a blocker. Resolution: **treat as blocker** — the plan is revised (v0.3) before any implementer runs.
- **Gate:** FAIL until plan v0.3 closes the items below.

## Agreed by both (must be fixed in the plan)

| # | Sev | Finding |
|---|---|---|
| 1 | BLOCKER | Engine interaction + data model undefined: `Step` has no shared typed state (topology → DSN/password → envfile → migrate, AC-25), no place for prompts (AC-20 DSN re-ask, AC-21 re-check loop, AC-41 diff/overwrite choice), no rule to re-Detect dependents after a prerequisite's Apply, and no defined `--yes` behaviour when no database exists (AC-21). |
| 2 | HIGH | No output channel: `Step` returns only `([]Artifact, error)`, so notes/warnings/diffs (AC-12 drift, AC-13 dry-run diffs, AC-35 PATH/quarantine, AC-41 hand-install message, AC-45 linger hint, AC-70 duplicates) have no owner. Add `StepResult{Artifacts, Notes, Diffs}`; move `diff.go` into WI-S2-1 (needed by S2-5/9/11/12). |
| 3 | HIGH | Env file has up to three writers (database step per AC-21, envfile step, jobs step for `MEMORY_PR_INGEST_REPOS` per AC-43 vs AC-28) and S2-5 is ordered after S2-4. Define one writer (the envfile editor), build it before S2-4, and assign `env-key` ownership. |
| 4 | HIGH | Doctor/install disagree on hook scripts: install renders the binary path into the wrappers, doctor compares the raw embedded bytes (`checks_claude.go:171`) → `outdated` forever, `--upgrade` rewrites every run, AC-50 breaks. Shared `RenderHookScript(asset, binPath)` used by both. |
| 5 | MED | Uninstall manifest never empties (`env-key`/`dir` kept by policy but stay recorded) — contradicts AC-54 "manifest absent after full uninstall". Also: created-by-install `settings.json`, `install.lock`, `.bak` files vs the "tree equals pre-install" test. AC-54 mentions `--bin-dir`, AC-4 does not list it. |
| 6 | MED | Launchd `Inspect` ignores the manifest/rendered plist → moved `--bin-dir` or schedule change reads as `modified` and is never reloaded under `--yes` (AC-52). |
| 7 | MED | Oversized WI-S2-4 (and S2-13, S2-14). Split. |
| 8 | MED | Dry-run safety rests on "Apply is never called": `internal/namespace` writes via `os` directly, `readOnlyFS.Lock` returns `ErrDryRun`, `buildSetupDeps` builds only read-only adapters (install needs a second, writable builder + a read-only one for the final doctor). Extend the AST guard (FS verbs, `net.Dial*`, sub-packages) **before** S2 code lands. |
| 9 | LOW | Fold open slice-1 LOWs in before S2 writes user files: CRLF mixed newlines in `jsonEmitter.indent` (hits S2-9), `dsnPasswords` via `url.Parse` (hits AC-30 sentinel), doctor goldens compare `detail`. |
| 10 | LOW | WI-S1-0 / WI-S1-12 manual Mac checks still open (`bootout` "not loaded" exit status, settings reload for the AC-62 restart line, `~/.claude/projects` hyphen encoding, `$HOME` in hook commands) — gate S2-8, S2-13, AC-62. |
| 11 | HOLES | AC-13/AC-14 have no implementation owner; AC-49 (config-dir-changed warning, `BackupCorruptManifest` wiring), AC-69 for `uninstall`, AC-1/AC-4 for uninstall, AC-58 S2 deltas (`tools.claude` on job PATH, remedies naming `install`), AC-64 [S2], spec §8 downgrade warning. |
| 12 | LOW | Plan has no "Skills Implementer Will Need" (`golang-architecture`; no conflict found). Plan/status line stale (`02-plan.md:3`). |

## Reviewer B only (verify, then adopt)

| # | Sev | Finding |
|---|---|---|
| B-1 | HIGH | **Per-artifact choice vs step aggregate (H2):** `aggregateHookStates` + `DefaultChoice` make one `modified` event win, so under `--yes` an absent sibling event is never added and the step re-Detects non-`ok` (AC-5 failure). Applies to hooks.settings, hooks.scripts, skills (per file), jobs (two). Per-artifact choices, or non-destructive apply with success = "no absent/outdated left". |
| B-2 | HIGH | **DSN builder leaves `$` unencoded** (`url.UserPassword`) → `shellExpansionRe` classifies the written env file as `unparseable-value` → installer's own env file is flagged by doctor. Percent-encode everything outside URL-unreserved; Redactor registers that form. |
| B-3 | HIGH (security) | **psql quoting in `bootstrap.sql`:** user-typed password (`'`, `\`, backtick) rendered into `\set app_pw '…'` can break the script or reach psql backtick evaluation under sudo. Render only generated base64url passwords, or define escaping + tests. |
| B-4 | MED | `config.EnvFile` has no line-level API (`entries` unexported, comments dropped) — S2-5 needs a new exported API; `internal/config` missing from its files. |
| B-5 | MED | `go run` refusal needs TempDir/GOCACHE not in `Paths`/`Env` (AC-68 forbids env reads in setup) — compute in `main`. `detectPlatform` needs a UID. 5 s stdin deadline needs goroutine+timer. |
| B-6 | MED | Integration CI uses testcontainers, not `MEMORY_TEST_PG_ADMIN_DSN`; the bootstrap test needs `psql` + admin DSN. AC-62 (doctor fail → exit 1) conflicts with `--skip mcp`. Two plist consumers (`sed __HOME__` in INSTALL.md vs templates). `JobSpec.Label` is launchd-only. |

## Reviewer A only

- Slice-1 libraries already present (no work): `SaveManifest`, `LocalServerEvidence`, `deploy/embed.go` embeds `initdb/` recursively (`app-role.psql` needs no embed change), `x/term` v0.45.0 already in `go.sum`.
- Makefile `test-integration` also needs `./internal/setup/...` (S2-16).

## PR split — reviewers disagree (owner-level choice)

- **A:** 2a = S2-1..12 + install command; 2b = jobs + uninstall + docs (plan's own seam).
- **B:** 2a = core, topology B, **no edits to Claude files**; 2b = settings.json/hooks/MCP/skills/CLAUDE.md + jobs + uninstall + rollback, so that user-file edits never ship without `uninstall`.
- Recommendation: **B's split** (rollback ships with the first user-file write; 2a is still a runnable `install --dry-run`/topology-B command).

## Pre-work (small PR on slice 1, before S2)

Guard widening, CRLF newline fix + `crlf` golden, `dsnPasswords` via `url.Parse`, shared `RenderHookScript` + doctor use, plan status line, WI-S1-0/S1-12 Mac runs (owner).
