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

## Open follow-ups
- Uninstall (WI-S2-15) must handle an owned `hooks/claude-memory` dir that is not in the manifest: a failed `hooks.scripts` Apply (AC-8: nothing recorded) can leave the directory created but unrecorded. Treat an empty, unrecorded dir under `<ClaudeDir>/hooks/claude-memory` as removable, or record it before the first write.
- `Adopter` is generic: skills, claude-md and jobs (WI-S2-11..13) should implement it, returning only artifacts that are `ok` now, never a directory the step did not create.
- `Plan.Token` of a step planned only after its prerequisite (`afterDep`) covers just the span from that re-plan to Apply; no confirmed plan exists for it.
- MCP `outdated` (other command/args) still drops any user env on replace; the plan names the env keys. `modified` (env only) needs the overwrite Confirm.
- Engine prompt for a `modified` unparseable `namespaces.yaml` says "a backup is kept", which is misleading; it needs an engine flag per artifact.
- `LinePrompter` has no ctx: `Select`/`Text` wait for Enter after the first Ctrl-C.
- `--claude-md`, `--no-jobs`, `--pr-repos` exit 2 in 2a; 2b must undo that (`deferredInstallFlags`).
- WI-S2-13a must tolerate `launchctl bootout` exit 3 (not loaded), see the WI-S1-0 fixtures above.
- 14a review carried-over items 10-12: AC-34 child test with `MEMORY_PG_DSN` set; duplicate remote-Ollama warning in the dry-run golden.
- `manifest` and `dirs.state` doctor checks have no owning step (`unownedChecks`), so their fails are never exempt; decide in 2b whether an owner is wanted.
- Kept from earlier: ParseDBTarget refuses DSN query options other than `sslmode`; the Await-wait Select maps ErrTooManyAttempts to exit 2.
