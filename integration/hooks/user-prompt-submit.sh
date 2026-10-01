#!/usr/bin/env bash
# Claude Code UserPromptSubmit hook wrapper.
#
# Claude Code invokes this script with the hook's JSON on stdin and reads
# structured output from stdout. This wrapper is a thin pass-through to
# `claude-memory hook`, which performs the actual search/threshold logic
# (internal/memory + internal/ollama + internal/postgres, in-process).
#
# AC-31: a missing binary, a down Ollama, or an unreachable Postgres must
# never fail or delay prompt submission — always exit 0, with no stderr
# noise Claude Code would otherwise surface to the user.
set -u

CLAUDE_MEMORY_BIN="${CLAUDE_MEMORY_BIN:-$HOME/.local/bin/claude-memory}"

if [ ! -x "$CLAUDE_MEMORY_BIN" ]; then
  # Not installed yet (or PATH misconfigured): nothing to inject.
  exit 0
fi

# `claude-memory hook` itself enforces MEMORY_HOOK_TIMEOUT (default 800ms)
# internally and exits 0 silently on any error (AC-31); no need to
# duplicate that timeout here.
"$CLAUDE_MEMORY_BIN" hook

exit 0
