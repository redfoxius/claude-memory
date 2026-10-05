# Security

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting ("Report a vulnerability"
on the repository's Security tab). If that is not available to you, open a
regular issue that describes the problem in general terms, and leave out
exploit details, secrets, DSNs, transcripts and memory contents. This is a
personal project, so I can't promise a response time, but security reports come
first.

## Threat model in brief

claude-memory runs as your user on your machine. It stores what it learns in a
Postgres database that you operate.

- **No network listener.** The MCP server speaks stdio only. Claude Code starts
  it as a child process. Outbound, it talks to your Postgres, your Ollama
  (local by default) and the `claude`, `git`, `az`, `gh` and `glab` CLIs.
- **Database credentials** come only from the environment or from
  `~/.config/claude-memory/env`. The binary refuses that file if it is group-
  or world-readable (it must be 0600), and it never shell-sources it.
  `doctor` and `install` mask the password in their output. The server recipe
  in `DEPLOY.md` binds Postgres to loopback and a Tailscale address, uses
  `scram-sha-256` authentication, and lets `pg_hba.conf` reject everything
  outside the tailnet range.
- **Secret scrubbing is best-effort.** Session transcripts and PR text are
  redacted before they are sent to Anthropic through your own
  `claude -p --model haiku`. Every record is scrubbed again before it is
  written to Postgres. The scrubber matches known patterns: bearer tokens,
  private-key blocks, URLs with passwords, AWS access keys, GitHub tokens,
  JWTs, Azure DevOps PATs, and `secret/key/token/password = "..."`
  assignments. It is not a guarantee. A secret in an unrecognised format can
  get through, so don't paste credentials into sessions you expect to be
  remembered. Embeddings are computed by your Ollama and never leave the
  machine unless you point `MEMORY_OLLAMA_URL` elsewhere.
- **Untrusted PR authors are filtered.** On GitHub, only PRs and comments by
  `OWNER`, `MEMBER` or `COLLABORATOR` are ingested. On GitLab, only those by
  Developer-or-higher project members are ingested. Bots are skipped on both.
  Everything else is dropped before extraction. Azure DevOps content is
  treated as trusted because access is limited to the organisation's members.
  The `gh`/`glab`/`az` CLIs run with their own login. Token variables such as
  `GH_TOKEN` are removed from their environment.
- **Retrieved memory is a prompt-injection surface.** The hook and
  `memory_search` put stored text into Claude's context. Hook cards carry only
  title, repo and id, under a header that marks them as unverified hints to
  confirm with `memory_get`. Candidates are flagged as unverified. These are
  labels, not a sandbox. Anyone who can write to your database, and any text
  that reached a record through a session or a trusted PR, can influence
  Claude's behaviour in later sessions. Treat write access to the database as
  equivalent to write access to your prompts, review candidates
  (`claude-memory review`), and run `import` and `ingest-pr` with `--dry-run`
  first.
- **Namespaces are for relevance, not isolation.** All namespaces share one
  database and one role. Use separate databases if you need a hard boundary.
