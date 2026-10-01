#!/usr/bin/env bash
# launchd runs ProgramArguments directly (no shell), so it cannot source a
# KEY=VALUE env file on its own. This wrapper sources
# ~/.config/claude-memory/env (mode 0600, never committed) into its own
# environment, then execs the real claude-memory subcommand so the
# launchd job (ingest-pr, cleanup) gets MEMORY_PG_DSN / MEMORY_OLLAMA_URL /
# etc. without them ever appearing in the plist itself.
set -eu

CLAUDE_MEMORY_ENV_FILE="${CLAUDE_MEMORY_ENV_FILE:-$HOME/.config/claude-memory/env}"
CLAUDE_MEMORY_BIN="${CLAUDE_MEMORY_BIN:-$HOME/.local/bin/claude-memory}"

if [ -f "$CLAUDE_MEMORY_ENV_FILE" ]; then
  # shellcheck disable=SC1090
  source "$CLAUDE_MEMORY_ENV_FILE"
fi

exec "$CLAUDE_MEMORY_BIN" "$@"
