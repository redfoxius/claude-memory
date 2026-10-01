#!/usr/bin/env bash
# Claude Code SessionEnd hook wrapper.
#
# Claude Code invokes this script with the hook's JSON (including
# transcript_path) on stdin. This wrapper is a thin pass-through to
# `claude-memory extract`, which itself launches a fully detached
# background re-exec of itself (`extract --run <transcript_path>`) and
# returns immediately without waiting for extraction to finish (AC-21).
#
# This script must return immediately: it must never block session-end,
# and it must never fail it (no non-zero exit, no surfaced error).
set -u

CLAUDE_MEMORY_BIN="${CLAUDE_MEMORY_BIN:-$HOME/.local/bin/claude-memory}"

if [ ! -x "$CLAUDE_MEMORY_BIN" ]; then
  exit 0
fi

"$CLAUDE_MEMORY_BIN" extract

exit 0
