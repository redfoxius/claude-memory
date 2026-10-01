# Registering the `claude-memory` MCP server

`claude-memory serve` runs over stdio only (no network listener — see
`internal/mcpserver/stdio.go` / `cmd/claude-memory/serve.go`). Register it
once, at **user scope**, so it is available in every project, not just
this repo.

## Command

```bash
claude mcp add --scope user claude-memory -- "$HOME/.local/bin/claude-memory" serve
```

Verified against this machine's `claude mcp add --help` (stdio is the
default transport when none is given; `--` separates `claude mcp add`'s
own options from the server command and its arguments):

```
Usage: claude mcp add [options] <name> <commandOrUrl> [args...]
```

- `<name>` — `claude-memory`.
- `<commandOrUrl>` — the absolute path to the installed binary
  (`$HOME/.local/bin/claude-memory`, from `make install`; see `INSTALL.md`).
- `[args...]` — `serve`.
- `--scope user` — registers it for this user across all projects, not
  just the current working directory (`local`) or a shared project file
  (`project`).

No `-e KEY=value` flags are needed: `cmd/claude-memory/main.go`'s `run()`
calls `config.LoadFromFile` against `~/.config/claude-memory/env` (mode
0600, never committed) before parsing any subcommand, so `MEMORY_PG_DSN`,
`MEMORY_OLLAMA_URL`, `MEMORY_EMBED_MAX_TOKENS`, etc. are already in the
process environment by the time `serve` starts. Passing the DSN via `-e`
instead would put the Postgres password in `claude mcp list` / Claude
Code's own config file in plaintext — avoid that.

## Verifying the registration

```bash
claude mcp list
# claude-memory: stdio • /Users/<you>/.local/bin/claude-memory serve

claude mcp get claude-memory
```

## Verifying the server actually speaks MCP

With the dev stack up (laptop Ollama running, `MEMORY_PG_DSN` pointed at a
reachable Postgres), confirm the server responds over stdio by listing its
tools directly (bypassing Claude Code):

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke-test","version":"0"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | "$HOME/.local/bin/claude-memory" serve
```

Expected: a `tools/list` response listing exactly the 7 spec MCP tools
(`memory_store`, `memory_search`, `memory_get`, `memory_update`,
`memory_deprecate`, `memory_list`, `memory_feedback`) — no extra tools
(AC-19's tool surface).

## Uninstalling

```bash
claude mcp remove --scope user claude-memory
```
