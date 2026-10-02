# Installing `claude-memory` integration

> **Use `claude-memory install`** (and `claude-memory install --upgrade`
> to bring an existing install up to date); `--dry-run` shows the plan
> first. It does every step below (binary, env file, database, Ollama,
> namespaces, hooks, MCP, skills, CLAUDE.md block, and on macOS the launchd
> jobs) and `claude-memory doctor` checks the result. The manual steps
> below are the reference for what it does.

Everything under `integration/` is produced by this repo but installs
into **user-level** config (`~/.local/bin`, `~/.config/claude-memory`,
`~/.claude/`, `~/Library/LaunchAgents`, `acme/CLAUDE.md`). None of it
is applied automatically — every step below is something you run
yourself. Follow them in order.

**Check the result at any time with `claude-memory doctor`** (after step
1): a read-only health check of every step below — env file, Postgres
(connection, pgvector, schema), Ollama, MCP registration, hooks, skills,
CLAUDE.md block, namespaces and the launchd jobs. It never changes
anything, prints a `fix:` line under each failing check, exits 1 when a
check fails (`--strict`: also on warnings) and has `--json` for scripts.


## 0. Prerequisites

- The server Postgres is already deployed and reachable over Tailscale
  (`DEPLOY.md`).
- Go toolchain installed (for `make build`/`make install`).

## 1. Build and install the binary

```bash
cd /path/to/claude-memory
make install
# -> installs to $HOME/.local/bin/claude-memory
```

Confirm `$HOME/.local/bin` is on your `PATH` (or at least that the hook
wrappers below, which use an absolute path, don't need it to be).

## 2. Create the laptop env file

Run this in a regular terminal (not via Claude Code's `!` prefix — `read -s`
needs an interactive TTY). The password is typed hidden and never lands in
shell history or the chat:

```bash
mkdir -p ~/.config/claude-memory
read -rs "?APP_DB_PASSWORD: " PW && echo && umask 077 && printf \
  'MEMORY_PG_DSN=postgresql://claude_memory:%s@<POSTGRES_BIND_IP>:5432/claude_memory\nMEMORY_OLLAMA_URL=http://127.0.0.1:11434\nMEMORY_EMBED_MAX_TOKENS=2048\nMEMORY_PR_INGEST_REPOS=%s\n' \
  "$PW" "$HOME/work/acme" > ~/.config/claude-memory/env && unset PW \
  && chmod 600 ~/.config/claude-memory/env && echo "env ok"
```

Replace `<POSTGRES_BIND_IP>` with the server's Tailscale IP (`DEPLOY.md`).
(`read -rs "?prompt"` is zsh syntax; in bash use `read -rsp "prompt: " PW`.)
If the password contains `@ : / %`, percent-encode it in the DSN.
`config.LoadFromFile` refuses the file if it is group/world-readable.
Check it without revealing the password:

```bash
sed -E 's#(claude_memory:)[^@]*@#\1****@#' ~/.config/claude-memory/env
```

## 2a. Set up namespaces

Records are partitioned by **namespace** (a company, a side project, ...), so
facts from one project don't crowd out another's. This is a local,
single-user database: namespaces are for relevance, not security, and an
occasional cross-project memory is harmless. This step needs no database,
DSN or Ollama — only the binary from step 1.

Create the mapping file (`~/.config/claude-memory/namespaces.yaml`, mode
0600) with one of:

```bash
# map project areas to namespaces right away (globs: ** = any depth, ~ = home)
claude-memory namespaces init acme='~/work/acme/**' pet-game='~/src/pet-game/**'

# or start with only the shared default and add projects later
claude-memory namespaces init
```

Directories matching no mapping use `default:` — `global` unless you pass
`--default NAME` (e.g. `init --default acme` to make one project the
catch-all). Nothing is special-cased in code; any name works
(lowercase letters, digits, `-`, `_`).

**Upgrading from a pre-namespace install:** the migration backfilled
existing records to `acme`. Map your Acme directories to it or those
records stay invisible: `claude-memory namespaces add acme '<path>/**'`.

Check the result:

```bash
claude-memory namespaces which ~/work/acme/billing-service   # -> acme
claude-memory namespaces which /tmp                         # -> global (the default)
```

`integration/namespaces.example.yaml` shows the file format if you prefer to
edit it by hand.

## 3. Install and configure Ollama

Follow `integration/ollama.md` in full: `brew install ollama`, `brew
services start ollama`, `ollama pull bge-m3`, and its health check (keep-alive
is sent per request by `claude-memory`, no plist edit). Do this before step 7's verification
— the hook round-trip needs a warm model to hit its latency budget.

## 4. Register the MCP server

Follow `integration/mcp-registration.md`:

```bash
claude mcp add --scope user claude-memory -- "$HOME/.local/bin/claude-memory" serve
```

## 5. Merge the hooks into `~/.claude/settings.json`

Copy the wrapper scripts somewhere stable and executable:

```bash
mkdir -p ~/.claude/hooks/claude-memory
cp integration/hooks/user-prompt-submit.sh ~/.claude/hooks/claude-memory/
cp integration/hooks/session-end.sh ~/.claude/hooks/claude-memory/
chmod 755 ~/.claude/hooks/claude-memory/*.sh
```

Then **merge** (don't overwrite) the `hooks` block from
`integration/settings.snippet.json` into `~/.claude/settings.json`. If
`~/.claude/settings.json` already has a top-level `"hooks"` key, append
these two entries (`UserPromptSubmit`, `SessionEnd`) into its existing
arrays for those events instead of replacing the key outright — merge
by hand or with `jq -s '.[0] * .[1]'` against a throwaway copy, then
review the diff before saving over the real file.

## 6. Paste the `acme/CLAUDE.md` snippet

Open `integration/claude-md-snippet.md`, copy everything from the `---`
separator onward, and paste it into `acme/CLAUDE.md` under its own
heading. This is a manual copy — review it like any other edit to a
shared file before committing.

## 7. Copy the skills

```bash
mkdir -p ~/.claude/skills
cp -r integration/skills/remember ~/.claude/skills/
cp -r integration/skills/memory-digest ~/.claude/skills/
```

## 8. (Optional) Load the launchd jobs (PR ingest + cleanup)

Skip this to run `ingest-pr` / `cleanup` by hand (see `USAGE.md`); the
first `ingest-pr` run spends two haiku calls per PR over the last 30 days.

`claude-memory install` does this step (see the note at the top). By
hand: create `~/.local/state/claude-memory`, then write one plist per job
to `~/Library/LaunchAgents/io.github.claude-memory.<job>.plist` from
the Go template `integration/launchd/job.plist.tmpl` (fields: `Label`
`io.github.claude-memory.<job>`, `Program` the absolute path of the
installed `claude-memory`, `Args` `[<job>]`, `PATH` the directories of
`claude`, `git` and `az` plus `/usr/bin:/bin`, `Hour`/`Minute` 7:15 for
`cleanup` and 7:00 for `ingest-pr`, `LogPath`
`~/.local/state/claude-memory/<job>.log`). The jobs run the binary
directly; it reads `~/.config/claude-memory/env` itself, so there is no
wrapper script (the old `run-with-env.sh` wrapper `source`s the env file,
which breaks passwords containing `$` or `&`). Then load each one:

```bash
launchctl bootout gui/$(id -u)/io.github.claude-memory.cleanup 2>/dev/null || true
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.claude-memory.cleanup.plist
# the same for ingest-pr
launchctl list | grep io.github.claude-memory
```

Set `MEMORY_PR_INGEST_REPOS` in `~/.config/claude-memory/env` (step 2)
to the comma-separated local repo paths (or root directories) you want
`ingest-pr` to scan, if you haven't already.

## 9. Verify

Start with `claude-memory doctor`: every check should be `pass` or
`info`. The manual checks below exercise the same paths end to end.

After verifying, see `USAGE.md` for day-to-day operation.

**MCP tool list** (no Claude Code needed — talks to the binary directly):

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke-test","version":"0"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | "$HOME/.local/bin/claude-memory" serve
# Expect the 7 memory_* tools, nothing else.
```

**Hook round-trip** (`UserPromptSubmit`): store something first so there
is a match to find, then simulate the hook's stdin:

```bash
source ~/.config/claude-memory/env
"$HOME/.local/bin/claude-memory" seed --file seed/facts.yaml --dry-run # sanity-parses, doesn't write

echo '{"prompt":"what is the k8s label length limit","cwd":"'"$PWD"'"}' \
  | ~/.claude/hooks/claude-memory/user-prompt-submit.sh
# Expect a JSON {"hookSpecificOutput":{"hookEventName":"UserPromptSubmit",
# "additionalContext":"<header line>\n- [memory] <title> (repo: <repo>, id: <id>)\n..."}}
# (at most 3 cards, title/repo/id only), once seed/facts.yaml (or a real
# memory_store) has something to match. No output at all otherwise.
```

**SessionEnd backgrounding**: confirm it returns fast and never blocks:

```bash
time (echo '{"transcript_path":"/nonexistent","cwd":"'"$PWD"'","session_id":"smoke"}' \
  | ~/.claude/hooks/claude-memory/session-end.sh)
# real time should be well under 100ms; exit code 0 either way.
```

**End-to-end in Claude Code**: open a new session in any `acme/`
repo, type a prompt related to something you've stored, and confirm the
injected context card appears with only title/repo/id — this confirms
the hooks are actually wired through `~/.claude/settings.json`, not
just runnable standalone.

## Uninstall

```bash
# MCP
claude mcp remove --scope user claude-memory

# launchd
launchctl unload ~/Library/LaunchAgents/io.github.claude-memory.ingest-pr.plist
launchctl unload ~/Library/LaunchAgents/io.github.claude-memory.cleanup.plist
rm ~/Library/LaunchAgents/io.github.claude-memory.*.plist

# hooks: remove the UserPromptSubmit/SessionEnd entries you merged into
# ~/.claude/settings.json by hand, then:
rm -rf ~/.claude/hooks/claude-memory

# skills
rm -rf ~/.claude/skills/remember ~/.claude/skills/memory-digest

# CLAUDE.md: manually remove the pasted section from acme/CLAUDE.md

# binary + config (keep ~/.config/claude-memory/env and namespaces.yaml if
# you plan to reinstall later; otherwise remove them too)
rm ~/.local/bin/claude-memory ~/.local/bin/claude-memory-run-with-env.sh
```

Ollama itself (`brew services stop ollama`, `brew uninstall ollama`) is
left running by design — other tools on the laptop may depend on it;
stop it manually if you're sure nothing else uses it.

## Using namespaces

Day to day you don't think about them: the namespace is resolved
automatically from the project directory each time — by the MCP server (the
directory Claude Code was started in), the prompt hook (the prompt's
directory), session extraction (the session's directory) and `ingest-pr`
(each repo's path). Search covers the current namespace **plus `global`**;
listing, dedup and lookups by id never leave the current namespace.

| I want to... | Do this |
|---|---|
| list all namespaces and their paths | `claude-memory namespaces list [--json]` |
| see what a directory maps to | `claude-memory namespaces which [DIR]` |
| add a project / more paths | `claude-memory namespaces add NAME 'GLOB' ['GLOB'...]` |
| force a namespace for one repo | set `MEMORY_NAMESPACE` in that repo's `.claude/settings.json` under `env` (beats the file) |
| share a stack-generic fact everywhere | `memory_store` with `namespace: "global"` (e.g. a Go or Docker gotcha) |
| start over | `claude-memory namespaces init --force ...` |

The most specific matching path wins (`~/work/acme/infra/**` beats
`~/work/acme/**`). When no namespace can be chosen — no file, no match,
an unreadable file — work lands in `global` instead of failing.

## Staleness warnings

A record remembers the commit it was written at (`commit_sha`) and the files
it is about. When a card, `memory_search` result or `memory_get` result is for
the repo you are standing in and any of those files differ at the current
`HEAD`, it is marked: the hook card ends with
`⚠ code changed since this was recorded (N commits)`, and search/get carry
`stale_hint` (+ `stale_commits`). Verify the record against the code, then
`memory_update` it (a content/files change re-baselines it; or pass
`commit_sha` alone) or `memory_feedback(outdated)`.

- Needs `git` on `PATH`. No `commit_sha`, no files, another repo, or an unknown
  commit means "unchecked": no marker, no error.
- Inline and session records get `commit_sha = HEAD` only when their files
  have no uncommitted changes at write time; otherwise they stay unchecked
  until a clean `memory_update`. PR records use the PR's merge commit.
- The check is capped: `MEMORY_STALE_TIMEOUT_HOOK` (default 50ms, hook) and
  `MEMORY_STALE_TIMEOUT` (default 500ms, MCP server) in
  `~/.config/claude-memory/env`. The hook also stops at its own latency
  budget. Per-session verdicts are cached under
  `~/.local/state/claude-memory/stale-cache/`.

## Usage events and `stats`

The hook appends one `card_injected` event per card to
`~/.local/state/claude-memory/events/spool.jsonl` (a single local write, never a
database call; capped at 10 MB). `serve`, `extract --run`, `ingest-pr` and
`cleanup` drain it into the `events` table (migration 0003); a file the
database rejected is kept as `spool.*.failed`. Until the new schema is applied
(run `claude-memory migrate` once after upgrading, before restarting Claude Code sessions: several `serve` processes starting at once can race on `CREATE TABLE`)
the events simply wait in the spool. `claude-memory stats` reports on them
(`integration/USAGE.md`, "Usage stats"); events older than 365 days are
removed by `cleanup`.
