# claude-memory

Shared, curated semantic memory for [Claude Code](https://docs.claude.com/en/docs/claude-code).
A single Go binary stores facts (patterns, decisions, gotchas, conventions) in
Postgres with pgvector, embeds them locally with Ollama (`bge-m3`), and gives
them back to Claude in two ways: an MCP server (stdio) with seven `memory_*`
tools, and a synchronous `UserPromptSubmit` hook that injects up to three short
"possible relevant memory" cards into each prompt within an 800 ms budget.
Knowledge comes from Claude itself during work, from finished sessions, and from
merged pull requests.

## What is different about it

None of these ideas is new on its own. The combination is what this project is about:

- **Curated lifecycle, not an append-only log.** New facts start as
  `candidate` and become `active` when they are seen again, marked useful, or
  approved in `claude-memory review` (a terminal pass over candidates). Every
  write is decided against its nearest neighbours: ADD below 0.65 cosine
  similarity, ASK (returned to the caller for judgment, not stored) between
  0.65 and 0.85, NOOP/UPDATE above. Replaced facts are deprecated with
  `superseded_by`; unreviewed candidates expire after 180 days.
- **Strict namespaces.** Each record belongs to a namespace (`work`,
  `pet-game`, ...) resolved from the project directory through
  `~/.config/claude-memory/namespaces.yaml`. Search covers the current
  namespace plus `global`; dedup, listing and by-id lookups never cross
  namespaces. Nothing is written to `global` automatically.
- **Staleness against git.** A record remembers its `files` and the
  `commit_sha` it was written at. When it is returned inside that repo and
  those files changed since, the card says
  `code changed since this was recorded (N commits)`.
- **PR ingest.** Merged PRs from Azure DevOps (`az`), GitHub (`gh`) and GitLab
  (`glab`) are turned into records by up to two haiku calls per PR, with the
  merge commit as the staleness baseline. Unlike session facts, PR-derived
  records are stored as `active` (confidence 0.75) without a review step. On GitHub and GitLab only
  trusted authors are used (GitHub `OWNER`/`MEMBER`/`COLLABORATOR`, GitLab
  Developer or higher, no bots).
- **Measured retrieval.** Hybrid search: vector similarity and Postgres
  full-text fused with Reciprocal Rank Fusion. An eval harness
  (`make eval`) reports hit@3, negatives below the hook threshold and
  derived thresholds; the report is tracked in git.
- **Idempotent setup.** `claude-memory install` is re-runnable (records what it
  wrote in an install manifest, backs up and merges `settings.json`, never
  overwrites edited files without asking) and `claude-memory doctor` is a
  read-only health check with a `fix:` line per failing check.

### Retrieval eval, honestly

Latest run ([eval-results.md](docs/specs/memory-mvp/eval-results.md), real
Ollama `bge-m3` + Postgres): 34 queries against 11 seed records. Paraphrase
hit@3 14/14, exact identifier 5/5, identifier buried in a long prompt 7/7,
negatives (unrelated questions) 6/6 below the 0.50 hook threshold. Store
latency p95 196 ms. The set is small and hand-written by the author, so read
it as a regression test, not a benchmark. Margins are thin: two expected
records score just under 0.50 and would be filtered out by the hook even
though search ranks them first.

## Honest comparison

This is not the easiest memory tool for Claude Code. Claude Code ships native
auto memory, and there are mature, simpler-to-install projects such as
[thedotmack/claude-mem](https://github.com/thedotmack/claude-mem),
[rohitg00/agentmemory](https://github.com/rohitg00/agentmemory),
[doobidoo/mcp-memory-service](https://github.com/doobidoo/mcp-memory-service),
[mem0](https://github.com/mem0ai/mem0) and
[Hindsight](https://github.com/vectorize-io/hindsight).

The trade-offs here make sense if you want memory **shared** across machines or
people on **your own Postgres**, **curated** (review, dedup, deprecation,
staleness) rather than accumulated, and fed by **PR history** as well as
sessions. They do not make sense if you work alone and want zero setup: you
need a Postgres with pgvector, Ollama, and about fifteen minutes.

## Architecture

```
 laptop                                                 server (or this machine)
 ┌────────────────────────────────────────────┐        ┌──────────────────────┐
 │ Claude Code                                │        │ Postgres 16+         │
 │   ├─ UserPromptSubmit hook ─┐              │        │   + pgvector         │
 │   ├─ SessionEnd hook ───────┤              │  SQL   │   records, events    │
 │   └─ MCP (stdio) ───────────┤              │◄──────►│   HNSW + GIN (FTS)   │
 │                             ▼              │        └──────────────────────┘
 │                   claude-memory (Go) ──────┼──► Ollama bge-m3 (local, :11434)
 │                     │            │         │
 │   claude -p --model haiku   az / gh / glab │
 │   (session + PR extraction) (PR ingest)    │
 │                                            │
 │   launchd: ingest-pr 07:00, cleanup 07:15  │
 └────────────────────────────────────────────┘
```

### What leaves your machine

Embeddings are computed by your local Ollama. Session transcripts (for
extraction at `SessionEnd`) and PR text (for `ingest-pr`) are sent to
Anthropic through your own logged-in `claude -p --model haiku`, after
best-effort, pattern-based secret scrubbing. Records are written to the
Postgres you configure. Details: [integration/USAGE.md](integration/USAGE.md).

## Requirements

- Go 1.25+ (from `go.mod`), to build.
- Postgres 16+ with the `pgvector` extension.
- Ollama with the embedding model: `ollama pull bge-m3`.
- Claude Code CLI (`claude`), logged in; `git` on `PATH`.
- Optional, for PR ingest: `az`, `gh` and/or `glab`, logged in.
- macOS is the tested platform. Linux builds and passes unit and integration
  tests in CI, and `install` runs there, but the scheduled jobs are
  launchd-only today (on Linux, run `ingest-pr`/`cleanup` by hand or from your
  own timer).

## Quick start

The documented, tested topology is Postgres on a separate server reachable over
a private network (Tailscale), with Ollama and the binary on the laptop.
[DEPLOY.md](DEPLOY.md) covers the server side. Note that
`deploy/docker-compose.yml` uses `network_mode: host` and binds Postgres to a
Tailscale IP, so it is a server recipe for Linux, not an all-local Docker
Desktop setup; a local compose recipe is not provided yet.

```bash
git clone https://github.com/redfoxius/claude-memory.git && cd claude-memory
make install                          # builds and copies to ~/.local/bin/claude-memory
# or: go install github.com/redfoxius/claude-memory/cmd/claude-memory@latest

brew install ollama && brew services start ollama && ollama pull bge-m3

claude-memory install --dry-run       # show the plan, change nothing
claude-memory install                 # interactive; --topology local|remote
claude-memory doctor                  # every check should be pass or info
```

`install` sets up the env file (mode 0600, DSN and Ollama URL), the database
role and schema, the Ollama model, namespaces, MCP registration, hooks,
skills, a marked block in `~/.claude/CLAUDE.md` and, on macOS, the launchd
jobs. With a Postgres on the same machine (`--topology local`) it writes a
`bootstrap.sql` (role, database, `vector` extension) and prints the `psql`
command for you to run; it never runs `sudo` or `psql` itself. For a remote
server, create the role there with `deploy/initdb/app-role.psql`. After a new
build, `claude-memory install --upgrade`. The manual equivalent of every step
is in [integration/INSTALL.md](integration/INSTALL.md).

Open a new Claude Code session afterwards: hooks and MCP servers are read at
session start.

## Usage

MCP tools (used by Claude, guided by the CLAUDE.md block): `memory_search`,
`memory_store`, `memory_update`, `memory_deprecate`, `memory_get`,
`memory_list`, `memory_feedback`. Skills: `/remember`, `/memory-digest`.

```bash
claude-memory namespaces init work='~/work/acme/**' pet-game='~/src/pet-game/**'
claude-memory namespaces which ~/work/acme/billing-service   # -> work
claude-memory review                    # walk candidates: approve/edit/deprecate/delete
claude-memory ls | show ID | edit ID | promote ID | rm ID [--hard]
claude-memory stats [--since 30d] [--json]   # cards injected, precision proxy, failures
claude-memory import automem --dry-run  # Claude Code auto-memory files -> candidates
claude-memory import insights [PATH...] # INSIGHTS.md bullets -> candidates
claude-memory ingest-pr --dry-run       # list PRs that would be ingested; no haiku, no writes
claude-memory ingest-pr                 # repos from MEMORY_PR_INGEST_REPOS
claude-memory cleanup                   # expire old candidates, prune events
claude-memory eval-retrieval --output=/tmp/eval.md   # needs Ollama + Postgres
claude-memory seed --file seed/facts.yaml --dry-run
claude-memory migrate | version
```

Configuration lives in `~/.config/claude-memory/env` (`KEY=VALUE`, read by the
binary, refused if group- or world-readable). The main keys: `MEMORY_PG_DSN`
(required), `MEMORY_OLLAMA_URL`, `MEMORY_OLLAMA_MODEL`,
`MEMORY_HOOK_SIM_THRESHOLD`, `MEMORY_HOOK_TIMEOUT`, `MEMORY_CANDIDATE_TTL`,
`MEMORY_PR_INGEST_REPOS`, `MEMORY_NAMESPACE`. Full table and defaults:
[integration/USAGE.md](integration/USAGE.md#config-reference).

## Uninstall

There is no `uninstall` command yet. `install` records what it created in
`~/.config/claude-memory/install.json`; to remove it by hand on macOS:

```bash
claude mcp remove --scope user claude-memory
for job in ingest-pr cleanup; do
  launchctl bootout gui/$(id -u)/io.github.claude-memory.$job 2>/dev/null
  rm -f ~/Library/LaunchAgents/io.github.claude-memory.$job.plist
done
# ~/.claude/settings.json: delete the UserPromptSubmit and SessionEnd entries
# whose command is under ~/.claude/hooks/claude-memory/ (install left a
# settings.json.bak.claude-memory.<timestamp> backup next to it)
rm -rf ~/.claude/hooks/claude-memory ~/.claude/skills/remember ~/.claude/skills/memory-digest
# ~/.claude/CLAUDE.md (or the file given to --claude-md): delete the lines from
# <!-- BEGIN claude-memory --> to <!-- END claude-memory -->
rm -f ~/.local/bin/claude-memory
rm -rf ~/.config/claude-memory          # env (DSN + password), namespaces.yaml, install.json
rm -rf ~/.local/state/claude-memory     # job logs, PR cursors, event spool, caches, bootstrap.sql
# database, as a Postgres superuser: DROP DATABASE claude_memory; DROP ROLE claude_memory;
```

Ollama and the `bge-m3` model are left in place; remove them yourself if
nothing else uses them. If `CLAUDE_CONFIG_DIR` is set, read it instead of
`~/.claude`.

## Development

```bash
make test               # go test -race ./...
make test-integration   # needs Docker (testcontainers) or MEMORY_TEST_PG_ADMIN_DSN
make eval               # needs Ollama + bge-m3 and the Postgres from your env file
make lint               # golangci-lint
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## Status and roadmap

Used daily by the author on macOS. Built spec-first: every feature has a spec,
plan, reviews and a verification report under [docs/specs/](docs/specs/)
(index and backlog in [docs/specs/README.md](docs/specs/README.md); some
status lines there lag behind the code).

Works: MCP server, prompt hook, session extraction, PR ingest (Azure DevOps,
GitHub, GitLab), namespaces, staleness hints, review/management CLI, import,
usage and reliability stats, `install`/`doctor`.

Known gaps:

- No all-local Docker Compose recipe; the shipped compose file is a
  Tailscale-bound server setup.
- No `uninstall` command (manual steps above).
- Scheduled jobs are launchd-only (no systemd timers or cron yet).
- The eval set is small (34 hand-written queries) and thresholds are tuned on it.
- The GitLab adapter is tested only against fixtures written from the API
  docs; GitHub Enterprise and auto-detection of self-hosted GitLab are not
  supported.
- PR-derived records go live as `active` without review, so they reach prompts
  immediately; text from untrusted authors is dropped on GitHub and GitLab, and
  Azure DevOps authors are all treated as trusted.
- No offline write queue: with Postgres unreachable the hook stays silent and
  `memory_store` returns an error.

## License

Apache License 2.0, see [LICENSE](LICENSE).
