# `acme/CLAUDE.md` snippet — shared semantic memory

This is the exact rule text to paste into `acme/CLAUDE.md` (the shared
file at the `acme/` root). Installing it is a manual, user-approved
step (see `INSTALL.md`) — nothing in this repo edits `acme/CLAUDE.md`
directly.

Paste the section below as-is, under its own heading, anywhere after the
existing shared conventions.

---

## Shared semantic memory (`claude-memory`)

This folder's repos share a semantic memory service (`claude-memory`,
exposed as an MCP server with `memory_store`, `memory_search`,
`memory_get`, `memory_update`, `memory_deprecate`, `memory_list`,
`memory_feedback` tools). It captures durable, repo-spanning facts —
patterns, decisions, gotchas, conventions — so they survive past a single
session.

### When to call `memory_store` (inline capture)

Call `memory_store` yourself, without asking for confirmation, when a
session surfaces one of: a non-obvious bug root cause, a user correction,
a decision among alternatives together with its reason, an infra/CI/
third-party gotcha, or a reusable snippet.

**Never call `memory_store` for:**
1. Content derivable from the code or git history itself (if `git log`
   or reading the file would tell the same story, it does not belong in
   memory).
2. Ephemeral task steps (a TODO list, a one-off debugging step, anything
   true only for the current task).
3. Content already present in that repo's own `CLAUDE.md` (do not
   duplicate a rule that is already written down locally).

### Before relying on memory (read path)

1. Call `memory_search` before designing a solution — check whether this
   repo (or a cross-repo `*` record) already has relevant prior art.
2. Verify any retrieved record against the current code before relying
   on it — a stored record can go stale; the code is always the source
   of truth.
3. Call `memory_feedback` after using a record: `useful` if it held up,
   `outdated` or `wrong` if it did not. Feedback is how records graduate
   from `candidate` to `active`, or get deprecated.

### Candidates, "unverified" records, and hook cards

- A record flagged `"unverified"` is a `candidate`-status record: seen
  only once or twice, not yet confirmed by feedback. Treat it as a lead
  to verify, not as settled fact.
- A card injected automatically by the `UserPromptSubmit` hook contains
  only `title`, `repo`, and `id` — never the record's full content. Treat
  an injected card as a pointer to look up via `memory_get`/
  `memory_search`, not as verified ground truth to act on directly.
