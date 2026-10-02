# install/doctor — progress and handoff (2026-10-02)

## Slice 1 manual run (WI-S1-12) — recorded
`doctor` from `master` (`cd28e43`+) on the owner's live macOS install (darwin/arm64, launchd), after the server came back:
23 checks — pass: env, perms, format, pg.connect (PG 16.15, pgvector 0.8.6, 3–4 ms), ollama (0.35.0, bge-m3, 1024 dims), git/claude/az, MCP registered, hook scripts; warn: `hooks.settings` (legacy `$HOME/...` form, both events), `skills` (modified vs embedded), `jobs` (cleanup/ingest-pr plists not installed), `pg.schema` (0002 missing — **applied during this run with `migrate`, now ok**); info: no CLAUDE.md block, no `namespaces.yaml`, no `install.json`.
First run was FAIL `pg.connect` (server `home-server` offline over Tailscale); not a code issue.
## WI-S1-0 manual fixtures — measured on the Mac (macOS 26.2, Claude Code 2.1.287)
- **`launchctl bootout gui/<uid>/<label>` for a job that is not loaded:** prints `Boot-out failed: 3: No such process`, **exit 3**. `launchctl print` for the same label: `Could not find service "<label>" in domain for user gui: 501`, **exit 113** (the slice-1 fixture). WI-S2-13a must tolerate bootout exit 3 (not only 113) when "not loaded".
- **`~/.claude/projects` names:** the absolute path with every `/` replaced by `-` (e.g. `-Users-example-user-work-acme-claude-memory`). A literal hyphen in a directory name is indistinguishable (`-Users-example-user-work-Block-strike` is `~/work/Block-strike`), so WI-S2-8's existence-checked greedy decoding is required; confirmed.
- **`$HOME` in a hook command:** `settings.json` hooks use `$HOME/.claude/hooks/claude-memory/user-prompt-submit.sh` (timeout 5) and fire correctly in this session (UserPromptSubmit context arrives), so Claude Code expands `$HOME` there. The legacy form works; replacing it is optional.
- **Settings reload for the AC-62 restart line:** not verifiable from inside a session; keep the plan default ("restart open Claude Code sessions").


## Slice 2 status (branch `feature/install-s2a-install-cmd`)
PR 2a is done on `feature/install-s2a-install-cmd`: WI-S2-8 (namespaces), WI-S2-14a (install command, dry-run) and WI-S2-14b (final doctor, exit-code rule, cross-cutting tests), each with its review fixes. Earlier: WI-S2-0..7 with the Opus reviews. PR 2b part 1 is done on `feature/install-s2b-claude-steps`: WI-S2-9 (`hooks.scripts`, `hooks.settings`) and WI-S2-10 (`mcp`, `claudeCLI` adapter), with the Opus security and conformance review fixes (token kept across a Rule-B re-plan, per-script change check, adoption of ok-but-unrecorded artifacts through the new optional `Adopter` step method, 0600 unique backups, env-only MCP difference is `modified`, `hooks.settings` also requires `hooks.scripts`; spec v0.7 §0.7). Next in PR 2b: skills, CLAUDE.md, jobs, doctor deltas, uninstall, e2e, docs.

## Owner decision (2026-10-02): 2b scope reduced
Reason: installer outgrew the product; keep it minimal; revisit if needed.
- Remaining in 2b: a launchd-only minimal `jobs` step (WI-S2-13a + trimmed `jobs`, no systemd), trimmed doctor deltas (17), docs pointing `INSTALL.md`/`DEPLOY.md` at `claude-memory install`.
- DROPPED from 2b: systemd (WI-S2-13b), uninstall (WI-S2-15), testcontainers e2e (16).
- Done: WI-S2-11 (`skills`) and WI-S2-12 (`claude-md`) with review fixes (spec v0.8 §0.8).

## Slice 2b minimal jobs (branch `feature/install-s2b-claude-steps`, uncommitted)
Done: `jobs` step (launchd only) with the trimmed WI-S2-13a: `integration/launchd/job.plist.tmpl` replaces the two `__HOME__` plists; `LaunchdJobs.Render`, `LaunchdManager.Install` (bootout tolerates exit 3/113/"No such process"/"not loaded", then bootstrap), `Inspect`/`Detect` compare with the recorded hash and the rendering (`recordedJobHashes`, `ComputeJobPATH`); `--no-jobs` and `--pr-repos` are bound; `ReadPorts.Jobs`/`WritePorts.Jobs` wired; doctor `jobs` check compares against the rendering and names `claude-memory install`; `INSTALL.md`/`DEPLOY.md` point at `install`. `JobManager.Remove` is dropped (no uninstall).
Not done (by the owner decision): systemd, uninstall, e2e, doctor deltas beyond `jobs` (no `tools.claude` recorded-PATH check, no backend-vs-manifest warning), stable-shim PATH preference (nvm/volta), a `jobs`-specific diff question for modified plists (generic overwrite Confirm only).

## Open follow-ups
- If uninstall is ever revived: skill backups (`SKILL.md.bak.claude-memory.*`) live inside `skills/<name>`, so that owned dir is not empty after removing our files and stays; an `md-block` with `CreatedFile` is removed with its file only when our block is the only content; nested skill subdirs are recorded as owned `dir` artifacts; an empty unrecorded `skills/<name>` left by a failed Apply is the same gap as for `hooks/claude-memory`.
- Uninstall (WI-S2-15) must handle an owned `hooks/claude-memory` dir that is not in the manifest: a failed `hooks.scripts` Apply (AC-8: nothing recorded) can leave the directory created but unrecorded. Treat an empty, unrecorded dir under `<ClaudeDir>/hooks/claude-memory` as removable, or record it before the first write.
- `Plan.Token` of a step planned only after its prerequisite (`afterDep`) covers just the span from that re-plan to Apply; no confirmed plan exists for it.
- MCP `outdated` (other command/args) still drops any user env on replace; the plan names the env keys. `modified` (env only) needs the overwrite Confirm.
- Engine prompt for a `modified` unparseable `namespaces.yaml` says "a backup is kept", which is misleading; it needs an engine flag per artifact.
- `LinePrompter` has no ctx: `Select`/`Text` wait for Enter after the first Ctrl-C.
- 14a review carried-over items 10-12: AC-34 child test with `MEMORY_PG_DSN` set; duplicate remote-Ollama warning in the dry-run golden.
- `manifest` and `dirs.state` doctor checks have no owning step (`unownedChecks`), so their fails are never exempt; decide in 2b whether an owner is wanted.
- Kept from earlier: ParseDBTarget refuses DSN query options other than `sslmode`; the Await-wait Select maps ErrTooManyAttempts to exit 2.
