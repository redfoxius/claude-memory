# Installing `claude-memory` integration

Everything under `integration/` is produced by this repo but installs
into **user-level** config (`~/.local/bin`, `~/.config/claude-memory`,
`~/.claude/`, `~/Library/LaunchAgents`, `acme/CLAUDE.md`). None of it
is applied automatically — every step below is something you run
yourself. Follow them in order.

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

```bash
mkdir -p ~/.config/claude-memory
cat > ~/.config/claude-memory/env <<'EOF'
MEMORY_PG_DSN=postgresql://claude_memory:<APP_DB_PASSWORD>@<POSTGRES_BIND_IP>:5432/claude_memory
MEMORY_OLLAMA_URL=http://127.0.0.1:11434
MEMORY_EMBED_MAX_TOKENS=2048
EOF
chmod 600 ~/.config/claude-memory/env
```

Fill in `<APP_DB_PASSWORD>` / `<POSTGRES_BIND_IP>` per `DEPLOY.md`.
`config.LoadFromFile` (`internal/config/config.go`) refuses to load this
file if it is group/world-readable, so the `chmod 600` above is not
optional.

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

## 8. Load the launchd jobs (PR ingest + cleanup)

```bash
mkdir -p ~/.local/bin ~/.local/state/claude-memory
cp integration/bin/run-with-env.sh ~/.local/bin/claude-memory-run-with-env.sh
chmod 755 ~/.local/bin/claude-memory-run-with-env.sh

for f in integration/launchd/io.github.claude-memory.ingest-pr.plist \
         integration/launchd/io.github.claude-memory.cleanup.plist; do
  dest="$HOME/Library/LaunchAgents/$(basename "$f")"
  sed "s|__HOME__|$HOME|g" "$f" > "$dest"
  launchctl unload "$dest" 2>/dev/null || true
  launchctl load "$dest"
done

launchctl list | grep io.github.claude-memory
```

Set `MEMORY_PR_INGEST_REPOS` in `~/.config/claude-memory/env` (step 2)
to the comma-separated local repo paths (or root directories) you want
`ingest-pr` to scan, if you haven't already.

## 9. Verify

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

# binary + config (keep ~/.config/claude-memory/env if you plan to
# reinstall later; otherwise remove it too)
rm ~/.local/bin/claude-memory ~/.local/bin/claude-memory-run-with-env.sh
```

Ollama itself (`brew services stop ollama`, `brew uninstall ollama`) is
left running by design — other tools on the laptop may depend on it;
stop it manually if you're sure nothing else uses it.

## Namespaces

Records are partitioned by namespace (e.g. a company vs. a side project).
Copy `integration/namespaces.example.yaml` to
`~/.config/claude-memory/namespaces.yaml` and map your project directories to
namespaces; `MEMORY_NAMESPACE` overrides it per repo. When no namespace can
be chosen (no file, no matching path, broken file) records use the shared
`global` namespace. No namespace is special: the upgrade migration backfilled
existing records to `acme`, so map your Acme directories to a
`acme` namespace or those records stay invisible. See the example file.
