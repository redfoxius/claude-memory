---
name: remember
description: Explicitly capture a durable fact (pattern, decision, gotcha, or convention) into shared semantic memory via memory_store, when the user says "remember this", "store this in memory", or similar — skip this skill for anything the inline-capture exclusions in the memory section of your CLAUDE.md rule out (derivable from code/git history, ephemeral task steps, or already in the current repo's own CLAUDE.md).
---

# /remember

Use when the user explicitly asks to remember, save, or store something
for later ("remember this", "note this down for next time", "store this
decision"). This is the user-triggered counterpart to the automatic
inline-capture rule in the memory section of your CLAUDE.md — that rule
covers *Claude's own* judgment calls; this skill covers an explicit ask.

## Steps

1. **Check the three exclusions first** (same as the inline-capture rule
   in the memory section of your CLAUDE.md): do not store it if it is (a)
   derivable from the code or git history, (b) an ephemeral task step, or
   (c) already written in this repo's own `CLAUDE.md`. If any apply, tell the user
   why and skip the store — don't store it anyway just because they
   asked.
2. **Classify the fact** into one `kind`: `pattern`, `decision`,
   `gotcha`, or `convention`.
3. **Pick the scope**: a specific `repo` name if it's local to this
   repo, or `"*"` if it applies across all your projects (e.g. a
   cross-cutting convention).
4. **Call `memory_store`** with a short `title`, the full `content` in
   markdown, the chosen `kind`/`repo`, and any relevant `tags`/`files`.
   No confirmation parameter is required or expected by the tool — call
   it directly.
5. Confirm back to the user in one line what was stored and under which
   `repo`/`kind`, so they can correct it if misclassified.
