# Claude Memory — MVP (Shared Semantic Memory for Claude Code)

**Status:** not started

## Spec
- `docs/specs/memory-mvp/01-spec.md` (SPEC-2026-10-01-memory-mvp, v0.3,
  AC-1..AC-57; AC-41 withdrawn)

## Context
Greenfield repo (`acme/claude-memory`); only `docs/specs/` exists today,
no `go.mod`. **Topology B (spec v0.2):**
- **Laptop (macOS arm64)** runs everything that computes: native Ollama +
  `bge-m3` (Homebrew, `brew services`, Metal GPU), the `claude-memory` MCP
  server over **stdio only**, and the `hook` / `extract` / `ingest-pr` /
  `cleanup` / `seed` subcommands. All subcommands call the `internal/memory`
  service **in-process** — there is no laptop→server MCP hop and no MCP
  client package.
- **Server (home Mac Mini, Ubuntu 26.04)** runs only Postgres 16 + pgvector
  in Docker (512M / 1.0 CPU), bound to its Tailscale IP, scram-sha-256,
  `pg_hba` limited to `100.64.0.0/10`, plus daily `pg_dump`.
- AC-44 smoke test is done (spec §AC-44): server CPU ~17 ms/token, M1 Pro
  Metal 0.02–0.21 s for 15–600 tokens — the reason for topology B. No text
  truncation for latency; only the configured 2048-token embedding limit applies (AC-55/56, spec v0.3).

**Interface note (implementation-approach decision, not a spec change):**
AC-13 (every write fetches top-5 nearest before deciding) and AC-14 (for
session/PR paths, the *haiku call itself* makes the ADD/UPDATE/SUPERSEDE/
NOOP decision, given those same top-5) require extraction to see the top-5
candidates *before* invoking haiku. Since extraction runs in-process, this
is a **service method**, not a new MCP tool:
- `memory.Service.FindCandidates(ctx, content, repo) -> [{id, title, score}]`
  — read-only, same fused-score fetch the write path runs internally.
- `memory.Service.Store` accepts an optional `ExtractionDecision{Action,
  TargetID}` honored only when `source` ∈ `{session, pr}`; the service still
  re-fetches top-5 under the AC-16 advisory lock and applies the decision
  atomically (AC-17) — haiku's decision is advisory input, not a bypass.
The MCP tool surface stays exactly the spec's §10 set.

## Scope
- In scope: all active ACs (AC-1..AC-57 except withdrawn AC-41) — data
  model/validation, hybrid search, the 7 spec MCP tools over stdio,
  inline/session/PR capture, dedup/merge with advisory locking,
  lifecycle/TTL, secret scrubbing, laptop-local `EmbeddingProvider`/Ollama
  adapter, `UserPromptSubmit` + `SessionEnd` hooks, PR ingest, server
  Postgres compose + Tailscale-only binding + scram auth + backup +
  DEPLOY.md, seed data, `acme/CLAUDE.md` rule text, `/remember` and
  `/memory-digest` skills.
- Out of scope (per spec §12): multi-user/multi-tenant, Kubernetes, a web
  UI, Jira ingest, the llama.cpp fallback `EmbeddingProvider`, server-side
  embedding, multi-device embedders, a local write queue (AC-57 → clear
  error instead), HTTP transport / `tailscale serve`, mandatory Postgres
  TLS, server-side enforcement of the CLAUDE.md prompt rules, an
  un-deprecate path, poisoned-record remediation beyond AC-46.

## Modules Touched (new repo — target layout)
- `cmd/claude-memory/` — composition root + subcommands (`serve`, `hook`,
  `extract`, `ingest-pr`, `cleanup`, `seed`, `eval-retrieval`)
- `internal/config/` — env-driven config (all tunables below)
- `internal/record/` — domain: `Record`, enums, validation
- `internal/memory/` — application service: ports, search/write-path
  orchestration, dedup rules, lifecycle; `internal/memory/mock/` fakes
- `internal/scrub/` — secret-pattern redaction (pure)
- `internal/postgres/` — pgx `Store` adapter, migrations, hybrid search,
  advisory lock, TTL query
- `internal/ollama/` — `EmbeddingProvider` adapter (laptop-local Ollama)
- `internal/mcpserver/` — `modelcontextprotocol/go-sdk` tool wiring, stdio
  transport only
- `internal/transcript/`, `internal/extraction/` — session transcript
  parsing + haiku extraction (shared by extract & ingest-pr)
- `internal/azuredevops/`, `internal/prcursor/` — PR ingest adapter + local
  cursor file
- `internal/evalset/` + `testdata/evalset/` — retrieval eval fixtures
- `deploy/` — server-only: `docker-compose.yml` (postgres), `pg_hba.conf`,
  `backup.sh`, `.env.example`
- `DEPLOY.md` (root)
- `integration/` — artifacts that *install into* user-level config, never
  edited directly by the implementer (see Work Item 16)
- `seed/facts.yaml` — one-time seed data

## Architectural Constraints
- Domain/application packages never import a concrete infra package
  directly; ports are interfaces declared by the consumer
  (`~/.claude/skills/golang-architecture/SKILL.md:94-110`, CRITICAL).
- Group `internal/` by bounded concern/dependency, not by technical layer;
  no grab-bag `utils`/`common`/`models` package
  (`~/.claude/skills/golang-architecture/SKILL.md:75-90,112-130`).
- All concrete adapters constructed only in `cmd/claude-memory/main.go`
  (`~/.claude/skills/golang-architecture/SKILL.md:132-152`).
- Any port-crossing call takes `context.Context` first; background work
  (extraction launch, cleanup) is a port, not an inline `go func()` with no
  lifetime owner (`~/.claude/skills/golang-architecture/SKILL.md:169-210`).
- Unit tests use fakes against self-declared interfaces; real-dependency
  tests (testcontainers Postgres) are reserved for adapter packages
  (`~/.claude/skills/golang-architecture/SKILL.md:212-225`).
- Parameterized queries only for the hybrid search SQL — spec itself flags
  this as mandatory, not optional (`01-spec.md:847-850`, §11 point 3).
- Never build a shell command by string-concatenating repo-derived/PR text
  for the `az` CLI call; pass args as a slice
  (`.claude/skills/security/SKILL.md:129-138`, A05 Command Injection).
- Postgres DSN (with password) comes only from a local, never-committed env
  file (e.g. `~/.config/claude-memory/env`, mode 0600) or the environment;
  never from repo files, never logged
  (`.claude/skills/security/SKILL.md:199-211`, A09; AC-40, AC-54).
- Never log secrets/PATs/tokens or full request/response bodies at default
  level — redact before logging (`.claude/skills/security/SKILL.md:199-211`,
  A09; also AC-40).
- Server Postgres published only on the Tailscale IP (never `0.0.0.0`/
  public), scram-sha-256, `pg_hba` restricted to `100.64.0.0/10`
  (`.claude/skills/security/SKILL.md:79-81`, A02; spec AC-52, AC-54).
- **Conscious deviation from `security/SKILL.md:79` ("require TLS")**:
  Postgres TLS (`sslmode=require`) is NOT enforced in MVP. Reason, recorded
  in the spec (`01-spec.md:130-136`, §13 Clarifications): the only allowed
  path is the Tailscale WireGuard tunnel, which already encrypts and
  authenticates peers; `pg_hba` rejects every non-tailnet source. Revisit
  if Postgres is ever reachable outside the tailnet.

## Relevant INSIGHTS.md Gotchas
- None — `claude-memory` is a brand-new repo with no `INSIGHTS.md` yet
  (checked: none exists at the repo root or under `acme/`). The
  implementer should create one via the `engineering-insights` skill once
  real findings emerge (e.g. actual AVX-only `bge-m3` latency numbers,
  tuned thresholds) — there is nothing to read today.

## Skills Implementer Will Need
- `golang-architecture` — every Work Item under `cmd/`/`internal/`; binding
  for package boundaries, ports-declared-by-consumer, composition root,
  concurrency ownership (background extraction launch, cleanup job).
- `security` — Work Items 01 (DSN/secret config loading), 04 (scrub),
  11/12/13 (untrusted transcript/PR text into haiku, `az` CLI invocation),
  15 (Tailscale-only binding, scram, `pg_hba`, secrets in `.env`), 16
  (CLAUDE.md rule text, hook scripts), 18 (egress audit) — OWASP
  A02/A04/A05/A07/A09 all directly apply.
- `mermaid-diagram` — optional, only if DEPLOY.md's topology benefits from
  a diagram beyond the spec's own §5 diagrams.
- `engineering-insights` — recommended at the end of implementation (not a
  Work Item) to capture AVX/`bge-m3` latency findings and any threshold
  retuning into a fresh `INSIGHTS.md`.

## Work Items

Dependency graph (owned paths are non-overlapping where no edge exists,
so `run-plan` can dispatch those in parallel):

```
01 ─> 02 ─> 3a ─┬─> 3b ─┐
01 ─> 04        ├─> 3c ─┴─> "03" (all of 3a/3b/3c) below
                ├─> 05 (postgres)
                ├─> 06 (ollama)
                └─> 14 (cleanup, needs 05)

03,04,05,06 ─> 08 (mcp stdio) ─> 16 (integration, also needs 10,12,13)
03,05,06    ─> 07 (eval)
03,05,06    ─> 10 (hook)
03,04,05,06 ─> 11 (extraction) ─┬─> 12 (extract CLI)
                                └─> 13 (ingest-pr)
05          ─> 15 (server deploy) ─> 17 (seed, also needs 03,06)
06,11,13,05 ─> 18 (egress audit)
```
WI-09 is withdrawn (no MCP client in topology B); numbering is kept stable.

1. **Scaffolding, composition root skeleton, config** — files:
   `go.mod`, `cmd/claude-memory/main.go` (subcommand dispatch stub),
   `internal/config/config.go`, `Makefile`, `.golangci.yml`, `.gitignore`.
   depends on: none. acceptance: `go build ./...` succeeds; config loads
   `MEMORY_MAX_CONTENT_CHARS` (default 20000), `MEMORY_STORE_SIM_UPDATE`
   (default 0.92), `MEMORY_STORE_SIM_ASK` (default 0.80),
   `MEMORY_PR_INGEST_LOOKBACK` (default 30d),
   `MEMORY_HOOK_SIM_THRESHOLD` (default 0.75),
   `MEMORY_EXTRACT_MIN_MESSAGES` (default 20),
   `MEMORY_CANDIDATE_TTL` (default 180d), `MEMORY_HOOK_TIMEOUT` (default
   800ms), `MEMORY_EMBED_MAX_TOKENS` (default 2048, passed to Ollama as `num_ctx`/`num_batch`),
   `MEMORY_PG_DSN` (no default; required), `MEMORY_OLLAMA_URL` (default
   `http://127.0.0.1:11434`) — each documented with its default in
   `internal/config/config.go` godoc. Config is read from the environment,
   optionally pre-loaded from `~/.config/claude-memory/env` (refuse to load
   if the file is group/world-readable); the DSN is never logged.
   satisfies (config values only — enforcement lives in WI-02/03/10/13):
   AC-3, AC-15, AC-28, AC-30, AC-32 (all NEEDS-CLARIFICATION
   defaults made config-driven, not hardcoded).

2. **Domain record package** — files: `internal/record/record.go`,
   `internal/record/validate.go`, `internal/record/record_test.go`.
   depends on: 01 (consumes `MaxContentChars` as a parameter, not an
   import — domain stays zero-dependency per golang-architecture).
   acceptance: unit tests cover enum rejection and oversized-content
   rejection, no partial row ever implied (pure validation, no I/O).
   satisfies: AC-1 (schema fields as Go struct), AC-2, AC-3 (enforcement +
   test; WI-01 only supplies the configurable limit).

3. **Application service + ports (`internal/memory`)** — files:
   `internal/memory/ports.go` (`Store`, `EmbeddingProvider`, `Scrubber`,
   `Clock` interfaces + request/response DTOs reused by `mcpserver` and the
   in-process subcommands; `FindCandidates` + `ExtractionDecision` per the
   Interface Note),
   `internal/memory/service.go`, `internal/memory/mock/*.go`.
   Split into three sequential sub-items (same package, one review
   checkpoint each); all tests use fakes for `Store`/`EmbeddingProvider`/
   `Scrubber`/`Clock` only.

   **3a. Ports + search + CRUD** — files: `ports.go`, `service.go`,
   `search.go`, `crud.go`, `mock/*.go`. depends on: 02. acceptance:
   deprecated excluded by default, candidates ranked lower and flagged
   (AC-4); embedding-provider failure on search degrades to full-text-only
   results with a `degraded` flag — this WI owns the decision, WI-05 owns
   the query (AC-7); `memory_update` re-embeds (AC-8); deprecate (AC-9);
   not-found errors for get/update/deprecate/feedback (AC-10); list filters
   (AC-11). satisfies: AC-4, AC-7, AC-8, AC-9, AC-10, AC-11, AC-42
   (interface declared here, Ollama impl in WI-06).

   **3b. Write path: dedup / merge / atomicity** — files: `writepath.go`,
   `embedinput.go`. depends on: 3a. acceptance: top-5 fetched before every
   write decision (AC-13); session/PR paths honor `ExtractionDecision`
   (AC-14); inline 0.80/0.92 threshold branches (AC-15); advisory-lock
   acquired before the decision and released after commit (AC-16);
   SUPERSEDE in one transaction (AC-17); a contradicting fact resolves to
   SUPERSEDE of the old active record (AC-18); NOOP bumps `seen_count`;
   `pr` source starts `active` with higher confidence (AC-29); embedding
   input composed as `title + tags + content` in that order so model-side
   truncation keeps title/tags, while full `content` always goes to the
   full-text column (AC-55); one embedding per write reused for candidate
   fetch and persistence; Postgres- or Ollama-unreachable on
   `Store`/`Update` → typed error, no partial write, no queue (AC-57);
   service-level latency of one `Store` with fakes adds < 50ms overhead
   (AC-48 budget minus embedding/DB, real-stack p95 measured in WI-07).
   satisfies: AC-13, AC-14, AC-15, AC-16, AC-17, AC-18, AC-29, AC-48,
   AC-55, AC-57.

   **3c. Feedback + lifecycle transitions** — files: `lifecycle.go`.
   depends on: 3a. acceptance: `feedback(useful)` and `seen_count ≥ 2`
   promote candidate→active (AC-12, AC-34); `feedback(outdated|wrong)`
   deprecates with reason (AC-36). satisfies: AC-12, AC-34, AC-36.

4. **Secret scrubber adapter** — files: `internal/scrub/scrub.go`,
   `internal/scrub/patterns.go`, `internal/scrub/scrub_test.go`.
   depends on: 01 (pattern list config, if made overridable) — otherwise
   independent of 02/03, can run in parallel with them.
   acceptance: unit tests cover the patterns listed in
   `.claude/skills/security/SKILL.md:246-260` (AWS-style key, PATs,
   Postgres connection string, private keys, GitHub tokens, generic
   `secret|key|token|password=` assignments) **plus two patterns the skill
   does not list and this WI must add itself**: JWT shape
   (`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`) and
   `Authorization: Bearer <token>` headers (AC-38 requires JWT); each redacts to a
   placeholder and returns a redaction-occurred flag, never silently
   passes through. satisfies: AC-38, AC-39, AC-40 (content-shape half;
   logging half covered in WI-08).

5. **Postgres store adapter** — files: `internal/postgres/store.go`,
   `internal/postgres/migrations/0001_init.sql` (schema incl.
   `vector(1024)`, `tsvector` generated column, GIN/ivfflat indexes),
   `internal/postgres/hybrid_search.go` (RRF fused query, parameterized),
   `internal/postgres/advisory_lock.go`, `internal/postgres/ttl.go`,
   `internal/postgres/store_integration_test.go` (testcontainers).
   depends on: 03 (implements its `Store` port), 02 (persists `Record`).
   acceptance: testcontainers suite against `pgvector/pgvector:pg16` image
   covers: full round-trip of all AC-1 fields; exact-identifier match
   surfaced via fused rank even when semantically weaker (AC-5); `repo="*"`
   included in scope (AC-6); full-text-only fallback query path exists and
   is exercised with embedding stubbed out (AC-7, degrade decision lives
   in WI-03, query exists here); two concurrent near-duplicate writes via
   `pg_advisory_xact_lock(hashtext(repo||normalized_title))` resolve to
   exactly one row (AC-16); SUPERSEDE as one transaction (AC-17).
   satisfies: AC-1, AC-5, AC-6, AC-7 (query only; decision in WI-3a),
   AC-16, AC-17.

6. **Ollama embedding adapter** — files: `internal/ollama/embedder.go`,
   `internal/ollama/embedder_test.go` (HTTP fake), integration smoke test
   gated behind a build tag/env flag for the real server.
   depends on: 03 (implements `EmbeddingProvider`).
   acceptance: returns 1024-dim vector; errors (not zero-vector) on
   unreachable/non-200; calls `/api/embed` with `truncate: true` **and**
   `options: {num_ctx: N, num_batch: N}` where N = `MEMORY_EMBED_MAX_TOKENS`,
   so longer input is cut by the model, never rejected (AC-55/56).
   *Verified 2026-10-01 on Ollama 0.35 / M1 Pro:* truncation keeps the
   leading tokens (same head + different tail → cosine 1.0); the effective
   cap is `num_batch` (default 2048), not the model's 8192 context —
   without `num_batch` input is silently cut at 2048 tokens; `truncate:
   false` returns "input length exceeds the context length". Cost: 2048
   tokens 0.6s, 4096 3.4s, 8192 8.0s; a `bench` helper records p50/p95 over N calls (re-runs the
   AC-44 baseline on the laptop). satisfies: AC-42 (impl), AC-43, AC-44,
   AC-55, AC-56.

7. **Retrieval eval harness & threshold tuning** — files:
   `internal/evalset/fixtures.go`, `testdata/evalset/*.json` (paraphrase
   cases + exact-identifier/error-code cases, ≥15 pairs), `cmd/claude-memory/eval.go`
   (`claude-memory eval-retrieval` subcommand), results written to
   `docs/specs/memory-mvp/eval-results.md`.
   depends on: 03, 05, 06 (needs real Postgres + laptop Ollama; a local
   `pgvector/pgvector:pg16` container is fine). acceptance: running the eval against the dev stack reports
   whether the paraphrase set clears the 0.75 hook threshold and the
   0.80/0.92 store thresholds separate "same fact" from "judge-needed"
   from "new fact" on real `bge-m3` scores; results feed back into WI-01's
   config defaults (values stay config-driven — no code change needed to
   retune); the same harness measures real-stack `Store` p95 latency
   (laptop Ollama + tailnet Postgres) against AC-48's < 3s budget and
   records it in `eval-results.md`.
   satisfies: AC-48 (real-stack measurement), AC-5 (exact-identifier verification with real
   scores), AC-15, AC-32 (empirical confirmation of the two NEEDS-
   CLARIFICATION thresholds).

8. **MCP server (stdio, tool wiring)** — files:
   `internal/mcpserver/tools.go` (the 7 spec tools, no extra tools),
   `internal/mcpserver/stdio.go`, `cmd/claude-memory/serve.go`
   (stdio only; no network listener), logging setup (zerolog to stderr —
   stdout is the MCP channel — no raw content/body at info level).
   depends on: 03, 04, 05, 06. acceptance: in-process integration test over
   stdio exercises every tool's happy path + not-found path; `memory_store`
   contract requires no confirmation field (AC-19); default-level log
   capture during a store/search call contains no raw content, only
   ids/metadata (AC-40); nothing is ever written to stdout except MCP
   frames. satisfies: AC-19, AC-40 (logging half).

9. **Withdrawn (topology B)** — the MCP client helper is not needed: hook,
   extract and ingest-pr call `internal/memory` in-process. AC-41 (its
   only auth concern) is withdrawn in spec v0.2; its replacement AC-54 is
   covered by WI-15.

10. **Hook CLI (`UserPromptSubmit`)** — files: `cmd/claude-memory/hook.go`.
    depends on: 03, 05, 06. acceptance: with fake `Store`/`EmbeddingProvider`
    returning above/below-threshold results, output card contains only
    `title`/`repo`/`id`; full prompt is passed as the query (model-side
    truncation only, AC-56); Ollama-down and Postgres-unreachable fakes each
    produce silent exit 0 with no card and no error text (AC-31); a
    wall-clock test confirms the `MEMORY_HOOK_TIMEOUT` (800ms) ceiling;
    latency budget p95 < 300ms is measured on the real stack in
    Verification. satisfies: AC-30, AC-31, AC-32, AC-46, AC-56.

11. **Extraction core + transcript parsing** — files:
    `internal/transcript/parse.go` (Claude Code transcript format,
    code-change/message-count detection), `internal/extraction/prompt.go`
    (delimited untrusted-data section + explicit "data not instructions"
    framing), `internal/extraction/schema.go` (strict JSON schema +
    validation/discard), `internal/extraction/haiku.go` (`claude -p`
    subprocess wrapper with timeout), plus the in-process
    `Service.FindCandidates` call (Interface Note) feeding candidates into
    the haiku prompt so it can emit the ADD/UPDATE/SUPERSEDE/NOOP decision
    (AC-14).
    depends on: 03, 04, 05, 06. acceptance: unit tests (fake subprocess) cover:
    missing/truncated transcript → zero records + logged failure;
    non-JSON/timeout haiku output → discarded + logged, zero records;
    an embedded "ignore previous instructions" string in fixture
    transcript/PR text does not escape the delimited section or produce
    an out-of-schema action. satisfies: AC-23, AC-24, AC-25, AC-45
    (AC-14's decision-making is exercised here; its persistence is WI-03).

12. **Extract CLI (`SessionEnd`)** — files: `cmd/claude-memory/extract.go`.
    depends on: 11. acceptance: process launches detached and returns
    control measurably under 100ms (measured via a stub that sleeps in
    the background branch); gating logic skips extraction for a <20-
    message, no-file-edit fixture transcript. satisfies: AC-21, AC-22.

13. **Ingest-pr CLI** — files: `cmd/claude-memory/ingestpr.go`,
    `internal/azuredevops/client.go` (`az` CLI wrapper, args passed as a
    slice — never shell-concatenated, per security skill A05),
    `internal/prcursor/cursor.go` (per-repo JSON cursor file).
    depends on: 11. acceptance: cursor persisted only after a repo's full
    batch succeeds (interrupting mid-batch leaves the old cursor);
    invalid-credentials fixture for one repo doesn't block/corrupt other
    configured repos' runs; missing/unreadable cursor falls back to the
    configured lookback window and writes a fresh cursor after the run.
    satisfies: AC-26, AC-27, AC-28.

14. **Lifecycle/cleanup job** — files: `cmd/claude-memory/cleanup.go`
    (daily job entry point calling `internal/memory` + `internal/postgres`
    TTL query).
    depends on: 03, 05. acceptance: a `candidate` fixture row untouched
    for >180 days is hard-deleted by a cleanup run; an `active` or
    `deprecated` fixture row of the same age survives regardless of
    activity. satisfies: AC-35, AC-37.

15. **Server deploy (Postgres only)** — files: `deploy/docker-compose.yml`
    (single service `postgres`, image `pgvector/pgvector:pg16`,
    `mem_limit: 512m`, `cpus: 1.0`, `restart: unless-stopped`,
    `pg_isready` healthcheck; `network_mode: host` with
    `listen_addresses=127.0.0.1,${POSTGRES_BIND_IP}` where `.env` sets the
    server's Tailscale IP — currently `100.64.0.10` (published ports were
    dropped 2026-10-01: docker-proxy rewrote the client source to 172.x and
    pg_hba rejected the laptop); host sysctl `net.ipv4.ip_nonlocal_bind=1`;
    data bind-mounted from SSD `/var/lib/claude-memory/pgdata`; `POSTGRES_INITDB_ARGS=--auth=scram-sha-256`),
    `deploy/pg_hba.conf` (`host all all 100.64.0.0/10 scram-sha-256`, plus
    the docker bridge range needed for local `pg_dump`; everything else
    rejected), `deploy/backup.sh` (daily `pg_dump` to
    `/mnt/data/backups/claude-memory`, 14-day prune, systemd-timer snippet),
    `deploy/.env.example` (no real secrets), `DEPLOY.md` (prereqs, port,
    volumes, limits, Tailscale-only binding, password rotation, upgrade,
    restore-from-backup, AC-44 measured baseline, laptop DSN setup).
    depends on: 05 (migrations). acceptance: `docker compose kill postgres`
    → auto-restart to healthy (AC-50); a restore drill against a real
    backup file succeeds following `DEPLOY.md` (AC-51, AC-53); `ss -tlnp`
    on the host shows 5432 only on the Tailscale IP, never `0.0.0.0`
    (AC-52); connecting without/with a wrong password, and from a non-
    tailnet source, are both rejected (AC-54); `docker stats` stays within
    512M/1.0 CPU (AC-49). satisfies: AC-44 (documented baseline),
    AC-49, AC-50, AC-51, AC-52, AC-53, AC-54.

16. **Integration artifacts** (produced *inside* this repo under
    `integration/`; installing them into user-level config is a separate,
    explicit session-level step — never a direct edit by the implementer)
    — files: `integration/claude-md-snippet.md` (the exact
    inline-capture-exclusions + read-path-rule text for `acme/CLAUDE.md`),
    `integration/hooks/user-prompt-submit.sh` + `session-end.sh` (thin
    wrappers invoking `claude-memory hook`/`extract`, matching Claude Code's
    hook stdin/stdout JSON contract), `integration/settings.snippet.json`
    (the `hooks` block to merge into `~/.claude/settings.json`),
    `integration/mcp-registration.md` (user-level MCP server entry,
    stdio transport, `claude-memory serve`, config via
    `~/.config/claude-memory/env`),
    `integration/ollama.md` (`brew install ollama`, `brew services start
    ollama`, setting `OLLAMA_KEEP_ALIVE=-1` for the brew service — the
    default brew service unloads the model after 5 min, observed
    2026-10-01 — `ollama
    pull bge-m3`, health check),
    `integration/launchd/io.github.claude-memory.ingest-pr.plist` and
    `...cleanup.plist` (daily runs on the laptop),
    `integration/skills/remember/SKILL.md`, `integration/skills/memory-
    digest/SKILL.md`, `integration/INSTALL.md` (ordered install steps
    naming exactly which files move where, performed by the user/session,
    not scripted auto-edits of `~/.claude/*`).
    depends on: 08, 10, 12, 13. acceptance: `INSTALL.md`'s steps, followed
    manually, result in a working hook round-trip against a running dev
    stack; the CLAUDE.md snippet text contains the three AC-20 exclusions
    and the three AC-33 instructions verbatim; `session-end.sh` backgrounds
    `claude-memory extract` (`nohup … &`, stdin/stdout detached) and itself
    exits in < 100ms, measured with a stub `extract` that sleeps 10s
    (AC-21 — shared with WI-12, which owns the Go-side detach/gating).
    satisfies: AC-20, AC-21, AC-33.

17. **Seed records** — files: `seed/facts.yaml` (10–20 known facts incl.
    the k8s 63-byte branch-name limit, "billing-service PRs target master",
    "DECLINED/DECLINED is terminal in billing-service"), `cmd/claude-memory/seed.go`
    (`claude-memory seed --file seed/facts.yaml`, calls the normal
    `memory_store` write path — no bypass).
    depends on: 03, 06, 15 (needs the deployed server Postgres + laptop
    Ollama; seeds in-process through `memory.Service`).
    acceptance: running `claude-memory seed` against the deployed stack
    stores all fixture facts and each is retrievable via `memory_search`;
    a second run of the same file resolves to NOOP/UPDATE, not duplicate
    ADDs (exercises AC-16's lock on a real run). satisfies: AC-1, AC-16,
    AC-53 (exercises the restore/seed story referenced in DEPLOY.md).

18. **Egress & security audit** (cross-cutting verification, not new
    production code) — review of outbound network calls across
    `internal/ollama`, `internal/extraction` (haiku subprocess only),
    `internal/azuredevops`, `internal/postgres`; produces
    `docs/specs/memory-mvp/egress-audit.md` listing every outbound call
    site and its destination.
    depends on: 05, 06, 11, 13. acceptance: the audit confirms
    repository-derived content (transcripts, PR text, inline snippets)
    goes only to laptop-local Ollama, `claude -p` haiku, and the owner's
    Postgres over the tailnet — no other destination appears in the
    reviewed call sites. satisfies: AC-47.

## Test Strategy
- **Unit (fakes)**: `internal/record`, `internal/memory` (against its own
  `Store`/`EmbeddingProvider`/`Scrubber`/`Clock` fakes in
  `internal/memory/mock`), `internal/scrub`, `internal/transcript`,
  `internal/extraction` (fake subprocess), `internal/prcursor`,
  `cmd/claude-memory` hook (fake service ports). No real Postgres/Ollama/`az`/`claude`
  in this tier — per golang-architecture's testability rule.
- **Integration (testcontainers)**: `internal/postgres` against
  `pgvector/pgvector:pg16` — schema migration, hybrid RRF query, advisory
  lock race (two goroutines racing a near-duplicate write), SUPERSEDE
  transaction atomicity, TTL query boundary (`active`/`deprecated` never
  deleted). `internal/mcpserver` integration test spins up the real
  go-sdk server (stdio, in-process pipes) against a fake
  `memory.Service` or the real one backed by testcontainers Postgres +
  a stubbed `EmbeddingProvider`.
- **Retrieval eval set** (Work Item 7): a small fixture corpus of
  paraphrase pairs (same fact, reworded) and exact-identifier pairs (a
  literal error code/function name) run against the real `bge-m3`
  embeddings once the dev stack is up, to confirm or retune the
  0.75/0.80/0.92 defaults — thresholds stay config values, never
  hardcoded, so retuning needs no code change.
- **Manual/hardware-dependent** (documented in `DEPLOY.md`, not automatable
  by the implementer): AC-49's 24-hour `docker stats` window for postgres,
  AC-30/AC-48's p95 latency measurement laptop→tailnet Postgres on the
  real home network. AC-44's baseline is already measured (spec v0.2).

## Verification
- `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...
  -race` all green.
- Full testcontainers + eval-harness suite green (Work Items 5, 7, 8).
- AC-N traceability pass: every active AC-1..AC-57 (AC-41 withdrawn)
  appears in at least one Work
  Item's `satisfies:` list above and in at least one test named in that
  item's acceptance criteria.
- End-to-end dry run against the dev/staging compose stack: inline
  `memory_store` → `memory_search` (paraphrase hit) → `memory_feedback`
  (useful → active) → `memory_deprecate` → `memory_list` filter — all via
  real MCP calls, not fakes.
- `docker compose kill postgres` + `docker compose ps` on the server shows
  auto-recovery to healthy (AC-50).
- Postgres auth negative tests: no password, wrong password, and a
  non-tailnet source are all rejected (AC-54).
- With laptop Ollama stopped (`brew services stop ollama`) and, separately,
  with Tailscale down: hook exits 0 silently; `memory_store` returns a
  clear error and writes nothing (AC-31, AC-57).
- Concurrent near-duplicate `memory_store` load test against the real
  deployed stack resolves to exactly one row (AC-16).
- Backup/restore drill against `deploy/backup.sh`'s actual output file,
  following `DEPLOY.md`'s restore section verbatim (AC-51, AC-53).
- `ss -tlnp` on the server confirms 5432 listens only on the Tailscale IP;
  a connection to the server's LAN/public IP on 5432 fails (AC-52).
- `UserPromptSubmit` and `SessionEnd` hook latency measured over repeated
  real prompts/session-ends: hook-added p95 < 300ms on the home network
  (AC-30), background extraction launch overhead < 100ms (AC-21).
- Review `docs/specs/memory-mvp/egress-audit.md` (Work Item 18) confirms
  no egress target beyond laptop Ollama, `claude -p` haiku and tailnet
  Postgres (AC-47).
- `INSTALL.md` followed manually end-to-end on the laptop: hook fires,
  card injected, inline capture round-trips — confirms Work Item 16's
  artifacts are actually consumable, not just present.
