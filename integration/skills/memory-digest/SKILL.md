---
name: memory-digest
description: Summarize what shared semantic memory holds for a repo (or across all your projects) via memory_list and memory_search — use when the user asks "what does memory know about X", "show me what we've learned about this repo", or "/memory-digest".
---

# /memory-digest

Use when the user wants an overview of what `claude-memory` has
accumulated — for the current repo, a named repo, or everything tagged
`"*"` (cross-repo).

## Steps

1. Call `memory_list` filtered by the relevant `repo` (default: the
   current repo plus `"*"`), optionally narrowed by `kind` or `status`
   if the user asked for e.g. "just gotchas" or "just active facts".
2. Group the results by `kind` (`pattern`/`decision`/`gotcha`/
   `convention`), and within each group put `active` records first,
   `candidate` ("unverified") records after, clearly labeled as
   unverified. Omit `deprecated` records unless the user asks to see
   them too.
3. For each record, show `title`, `status`, and (if present) `tags` —
   not the full `content` body; this is a digest, not a dump. If the
   user wants the full text of a specific one, call `memory_get` for
   that id on request.
4. If the user is about to act on a `candidate`/unverified record,
   remind them (per the read-path rule in the memory section of your
   CLAUDE.md) to verify it against the current code before relying on
   it, and to call `memory_feedback` afterward.
5. If `memory_list` returns nothing for the requested scope, say so
   plainly rather than inventing content.
