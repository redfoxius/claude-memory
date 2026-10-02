# CLAUDE.md section — shared semantic memory

The rule text for Claude Code lives in
[`claude-md-section.md`](claude-md-section.md), worded so it fits any
CLAUDE.md: your user-level `~/.claude/CLAUDE.md` (the default) or a shared
file such as a workspace-root `CLAUDE.md`. The binary embeds that file, and
`claude-memory install` (slice 2) writes it between
`<!-- BEGIN claude-memory -->` / `<!-- END claude-memory -->` markers.

To install it by hand, paste the whole of `claude-md-section.md`, as-is and
under its own heading, into the CLAUDE.md you chose. Review it like any other
edit to a shared file before committing.
