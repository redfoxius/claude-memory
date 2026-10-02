# Using `claude-memory` after install

Day-to-day operation on the laptop once `INSTALL.md` is done. Server-side
operations (backups, restore, upgrades) are in `../DEPLOY.md`.

## Check that it works

Open a **new** Claude Code session (hooks and MCP servers are read at
session start) in any repo and ask something you know is stored, e.g.
"which branch do I open the PR into" from `billing-service`. The prompt gets a
short "Possible relevant memories" block with `title / repo / id` lines.
No block means nothing scored above `MEMORY_HOOK_SIM_THRESHOLD` (0.50) — or
something is down; check with:

```bash
claude mcp list | grep claude-memory          # ✔ Connected
ollama ps                                     # bge-m3 … UNTIL Forever (after first use)
echo '{"prompt":"which branch do I open the PR into","cwd":"'"$PWD"'"}' \
  | ~/.claude/hooks/claude-memory/user-prompt-submit.sh
```

The hook is silent on any error by design (it must never block a prompt).
For details: `MEMORY_DEBUG=1 claude-memory hook < input.json`.

## What gets captured, and how

| Path | When | Needs |
|---|---|---|
| Hook cards (read) | every prompt | hooks in `~/.claude/settings.json` |
| Session extraction | on `SessionEnd`, sessions with file edits or > 20 messages | hooks; uses `claude -p` (haiku) |
| `/remember` | when you ask | skill in `~/.claude/skills/` |
| Inline capture by Claude | during work, without asking | the rules from `claude-md-snippet.md` pasted into the shared `CLAUDE.md` — **without them Claude does not store or search on its own** |
| PR ingest | manual or launchd | `az` logged in, `MEMORY_PR_INGEST_REPOS` |

`/memory-digest` summarizes what memory holds for a repo.

## PR ingest (run manually)

Each PR costs two haiku calls; the first run looks back 30 days
(`MEMORY_PR_INGEST_LOOKBACK`). Run it when you have quota to spare:

```bash
claude-memory ingest-pr --dry-run          # what would be ingested, no haiku, no writes
claude-memory ingest-pr                    # real run; cursors in ~/.local/state/claude-memory

# one repo only
MEMORY_PR_INGEST_REPOS=$HOME/work/acme/billing-service claude-memory ingest-pr --dry-run
```

Only Azure DevOps remotes are ingested today; GitHub/GitLab repos are
skipped with a warning. Cursors only advance after a repo's full batch
succeeds, so re-running after a failure is safe.

To schedule it daily instead, load the launchd job from `INSTALL.md` step 8.

## Cleanup

Candidates nobody used or re-saw for 180 days (`MEMORY_CANDIDATE_TTL`) are
deleted; `active` and `deprecated` records are never touched.

```bash
claude-memory cleanup
```

It prints the number of deleted candidates, then the number of usage events
pruned (events older than 365 days), and a third line
`spool: N draining, M failed files left` only when spool files remain (see
Usage stats). It also moves any pending hook events into the database and
removes per-session staleness caches older than 7 days.

## Usage stats

The hook, `memory_feedback` and every write record small usage events (record
ids, enums and numbers only, never text) in the `events` table. The hook only
appends them to `~/.local/state/claude-memory/events/spool.jsonl`; `serve`,
`extract`, `ingest-pr` and `cleanup` move the spool into Postgres.

```bash
claude-memory stats [--since 30d] [--json]   # window: Nd or a Go duration, e.g. 36h
```

One block for all namespaces and one per namespace: cards injected and
distinct records, the precision proxy (records marked `useful` within 2 h of a
card / distinct records injected) next to the same ratio without the 2 h limit,
feedback by outcome, records created by source and the current inventory,
candidate-to-active promotion rate (imported candidates are left out and shown
on their own `import promotion` line), stale-flag rate and check coverage (cards
whose staleness could be checked), superseded/deprecated/TTL-deleted counts.
A ratio with no data prints `n/a`. The last line, `N events still in the
spool`, tells you the report may lag. The proxy depends on Claude calling
`memory_feedback(useful)`; compare trends, not absolute numbers.

## Managing records from the terminal

Look at and fix memory without a Claude session. Every command works on one
namespace: the current directory's, or `--namespace NAME` (which always wins).
Writes go through the same path as the MCP tools (scrubbing, re-embedding,
events), so `edit` needs Ollama; `ls`, `show`, `rm` and `promote` do not.

```bash
claude-memory ls [--status S] [--kind K] [--repo R] [--limit N]
claude-memory show ID
claude-memory rm ID [--reason TEXT]            # deprecates (reversible)
claude-memory rm ID --hard [--yes]             # deletes the row; asks y/N first
claude-memory edit ID                          # opens $VISUAL, else $EDITOR, else vi
claude-memory promote ID                       # candidate -> active
claude-memory review                           # weekly pass over candidates
```

- **ids** are the full UUID or the 8-hex short id that `ls` prints. A short id
  is matched only inside the namespace; a `global` record needs its full UUID.
  `rm`, `edit`, `promote` and `review` refuse a record of another namespace
  (including `global`): pass `--namespace global` for those.
- `ls` shows `candidate` and `active` records, newest first (50 by default,
  `--limit 0` = all); `--status deprecated` shows the retired ones.
- `show` prints every field, the content and a stale line: `stale: N commits`,
  `stale`, `fresh` or `unchecked` (unchecked unless the record's repo is the
  current directory's git checkout and it has a baseline commit and files).
- `rm --hard` asks `delete <id> "<title>" permanently? [y/N]`; EOF and an empty
  answer mean no, `--yes` skips the question. It refuses a record that another
  record's `superseded_by` points at; deprecate it instead.
- `edit` shows `title:`, `tags:`, `files:` (comma-separated), a `---` line and the
  content. Nothing changed means nothing written; an empty `tags:`/`files:` line
  cannot clear them. If the editor fails or the file does not parse, nothing is
  written and the temp file is kept (its path is printed).
- `review` walks candidates newest first. Per record it shows age, counts, the
  stale line, tags, files, the first 15 content lines and up to 3 similar
  records, then asks `[a]pprove [e]dit [d]eprecate [x]delete [s]kip [v]iew
  [q]uit`: `a` promotes, `d` asks a reason (Enter = `rejected in review`), `x`
  asks y/N, `s` or Enter skips, `v` prints the whole content, `q` or EOF stops.
  EOF at the reason or y/N prompt aborts that action. It prints a summary of
  the counts at the end. It reads plain lines, so it can be scripted.

## Seeding and correcting records

```bash
claude-memory seed --file seed/facts.yaml --dry-run   # parse only
claude-memory seed --file seed/facts.yaml             # store via the normal write path
```

Re-running `seed` is safe: near-identical facts resolve to NOOP. A fact whose
closest existing record scores 0.65–0.85 is reported as
`ASK (needs manual judgment, not stored)` — store it from a Claude session
with `memory_store` + `decision: {"action": "ADD"}` (or UPDATE with
`target_id`). Fix an existing record with `memory_update`, retire it with
`memory_deprecate`.

## Config reference

`~/.config/claude-memory/env` (must be `chmod 600`, otherwise it is refused):

| Variable | Default | Meaning |
|---|---|---|
| `MEMORY_PG_DSN` | — (required) | `postgresql://claude_memory:<pw>@<server-tailscale-ip>:5432/claude_memory` |
| `MEMORY_OLLAMA_URL` | `http://127.0.0.1:11434` | local Ollama |
| `MEMORY_OLLAMA_MODEL` | `bge-m3` | embedding model |
| `MEMORY_EMBED_MAX_TOKENS` | `2048` | embedding input cap (Ollama `num_batch`) |
| `MEMORY_HOOK_SIM_THRESHOLD` | `0.50` | min cosine similarity for a hook card |
| `MEMORY_STORE_SIM_ASK` / `_UPDATE` | `0.65` / `0.85` | inline dedup bands |
| `MEMORY_HOOK_TIMEOUT` | `800ms` | hard hook timeout |
| `MEMORY_EXTRACT_MIN_MESSAGES` | `20` | session extraction gate |
| `MEMORY_CANDIDATE_TTL` | `180d` | cleanup age for unused candidates |
| `MEMORY_PR_INGEST_REPOS` | — | comma-separated repo dirs or roots |
| `MEMORY_PR_INGEST_LOOKBACK` | `30d` | first-run window per repo |

## Troubleshooting

| Symptom | Check |
|---|---|
| No cards ever | `claude mcp list`; Tailscale up; `psql` to the DSN works; prompt really matches something (`memory_search` from a session) |
| First prompt after reboot slow | Ollama loads the model on first use; warm it: `curl -s localhost:11434/api/embed -d '{"model":"bge-m3","input":"warmup","keep_alive":-1}'` |
| `env file … permissions` error | `chmod 600 ~/.config/claude-memory/env` |
| Away from home network | Hook stays silent, `memory_store` returns an "unavailable" error (no offline queue yet) |
| Undo the hooks | restore `~/.claude/settings.json.bak-claude-memory` or remove the `hooks` entries; see `INSTALL.md` → Uninstall |
