# Egress & Security Audit (WI-18)

Reviews every outbound network/subprocess call site across `internal/`
and `cmd/claude-memory/` and checks it against AC-47: repository-derived
content (transcripts, PR text, inline-captured snippets) must go only to
(1) the locally-run Ollama embedding service, (2) `claude -p` (haiku) for
extraction/dedup judgment, and (3) the owner's Postgres over Tailscale —
no other third-party network service.

Method: `grep -rn` across `internal/` and `cmd/` for `net/http`,
`exec.Command`/`exec.CommandContext`, pgx connection construction, and any
literal URL, then manually read each hit's call site. No
`net.Listen`/`http.ListenAndServe`/`http.Serve` exists anywhere in the
tree — confirmed separately — so there is no inbound network surface
either (consistent with AC-19/AC-41: stdio-only MCP server, no listener).

## Outbound call sites

| File:Line | Destination | What leaves | Repo-derived content? |
|---|---|---|---|
| `internal/ollama/embedder.go:61` (`http.NewRequestWithContext(ctx, "POST", baseURL+"/api/embed", ...)`) | Ollama, `MEMORY_OLLAMA_URL` (default `http://127.0.0.1:11434`, loopback-only by default config) | Embedding input: `title + tags + content` for a write, or the raw prompt text for the `UserPromptSubmit` hook search — truncated to `MEMORY_EMBED_MAX_TOKENS` | Yes — this is egress target (1) in AC-47. |
| `cmd/claude-memory/main.go:82-87` / `cmd/claude-memory/eval.go:46-50` | Same Ollama target as above; these just construct the shared `*http.Client`/`ollama.Embedder` used by every subcommand (`serve`, `hook`, `extract --run`, `ingest-pr`, `cleanup`, `eval-retrieval`) via the composition root | Same as above | Yes — same target, not a new one. |
| `internal/postgres/store.go:38` (`pgxpool.NewWithConfig(ctx, config)`, parsed from `dsn`) | Postgres, `MEMORY_PG_DSN` (intended: the owner's server, reachable only over Tailscale, per `DEPLOY.md`) | Full record rows (title, content, tags, embedding, etc.) on every `Create`/`Update`/`Search`/`List`/`FindCandidates` call | Yes — egress target (3) in AC-47. |
| `internal/extraction/haiku.go:40` (`exec.CommandContext(runCtx, "claude", "-p", "--model", "haiku", "--output-format", "json")`, prompt piped on stdin) | The `claude` CLI subprocess, which itself talks to Anthropic's API | The delimited extraction prompt: transcript text (session extraction) or PR title/description/diff summary (PR ingest), inside the AC-45 delimited "data, not instructions" section | Yes — egress target (2) in AC-47 (indirectly, via the `claude` CLI's own network call, not a call this codebase makes directly). |
| `internal/azuredevops/client.go:40` (`exec.CommandContext(ctx, "az", args...)`) | The `az` CLI subprocess, which itself talks to the Azure DevOps REST API | Arguments only: repo identifiers, PR id, `--detect true`/working directory for org/project context — **no repo content is sent to `az`**; PR text flows the other direction (`az` returns title/description/diff/comments to us, which then goes into the haiku extraction prompt above) | No repo content sent *to* `az`; it is a read-only source, not a content sink. |
| `cmd/claude-memory/hook.go:100` (`exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")`) | Local `git` subprocess only — no network call | The hook's own `cwd`, to derive a repo name | No egress (local process, no network). |
| `cmd/claude-memory/ingestpr.go:205` (`exec.CommandContext(ctx, "git", "remote", "get-url", "origin")`) | Local `git` subprocess only — no network call | A configured repo path, to read its origin URL for provider detection | No egress (local process, no network). |
| `cmd/claude-memory/extract.go:107` (`exec.Command(executable, args...)`) | Self re-exec (`claude-memory extract --run <path>`), fully local, `Setsid`-detached | Nothing beyond its own argv/env; no network call is made by this line itself | No egress (local process spawn). |

## No listener / no other egress

- No `net.Listen`, `http.ListenAndServe`, or `http.Serve` anywhere in
  `internal/` or `cmd/` — the MCP server (`internal/mcpserver`,
  `cmd/claude-memory/serve.go`) is stdio-only, matching AC-19/AC-41.
- No other `net/http` client construction, no other `exec.Command`/
  `exec.CommandContext` call site, and no hardcoded URL literal besides
  `/api/embed` (appended to the configured `MEMORY_OLLAMA_URL`) exist in
  the reviewed tree.
- Secrets (`MEMORY_PG_DSN`) are read from env/`~/.config/claude-memory/env`
  only (`internal/config/config.go`) and never appear in any of the
  outbound calls above as a logged value (`internal/postgres/store.go`
  never logs `dsn`; `cmd/claude-memory/*.go` subcommands never print
  `cfg.PGDSN`).

## Conclusion (AC-47)

Every outbound call site that carries repository-derived content goes to
exactly one of the three destinations AC-47 names: local Ollama, `claude
-p` (haiku), or the owner's tailnet Postgres. The `az` CLI is used
read-only (it supplies PR text to us; it never receives repo content from
us) and local `git`/self-re-exec calls make no network call at all. No
other destination was found. No deviation flagged.
