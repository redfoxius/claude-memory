# Specification: Claude Memory — Shared Semantic Memory for Claude Code (MVP)

## 0. Metadata
- Spec ID: SPEC-2026-10-01-memory-mvp
- Status: implemented and verified (07-verification.md); operational ACs outstanding
- Version: 0.5
- Owner: Oleksandr Kolomoiets (user@example.com)
- Supersedes: none
- Related: shared conventions `acme/CLAUDE.md`; no prior spec/plan exists (greenfield repo); design decisions captured here were agreed in a prior discussion with the owner, not sourced from a separate document; v0.2 revises the deployment topology (laptop-local Ollama + `claude-memory`, server reduced to Postgres-only) based on the owner's AC-44 smoke-test measurements taken 2026-10-01

## 1. Overview & Problem

Claude Code sessions across the `acme/` repos (and, longer-term, other
repos) repeatedly rediscover the same facts: a non-obvious bug root cause,
a convention a repo's own `CLAUDE.md` doesn't mention, an infra/CI
gotcha, a decision made among alternatives, a reusable snippet. Today that
knowledge either lives nowhere (lost at session end) or is manually copied
into a `CLAUDE.md`/`INSIGHTS.md` that nobody reliably updates. A session
in `catalog-service` has no way to learn from a fact discovered last week in
`billing-service`.

`claude-memory` is a shared semantic memory service: a single-user (MVP)
Go server exposing Model Context Protocol (MCP) tools that let any Claude
Code session, in any repo, store a discovered fact and retrieve relevant
facts from any other repo, ranked by a hybrid (semantic + keyword) search.
Capture is fully automatic from three sources (inline tool calls, a
session-end background extraction, a periodic PR ingest) — no manual
confirmation step, since a single developer reviewing every candidate
would defeat the purpose of ambient capture. Read access is likewise
ambient: a `UserPromptSubmit` hook silently surfaces 1–3 relevant records
before Claude starts reasoning, never blocking or slowing the prompt.

## 2. Glossary

| Term | Definition |
|---|---|
| MCP | Model Context Protocol — the tool-calling protocol Claude Code uses to reach external tools/servers; this spec uses `modelcontextprotocol/go-sdk`. **(v0.2)** The MVP transport is stdio, with `claude-memory` registered as a local, user-level MCP server on the laptop; Streamable HTTP remains available as an optional future transport, not required by any AC (see §12). |
| Record | One stored unit of memory: a pattern, decision, gotcha, or convention (see §9). |
| Hybrid search | A single ranked result list fusing a pgvector cosine-similarity (semantic) search and a Postgres `tsvector` full-text (keyword) search, e.g. via Reciprocal Rank Fusion (RRF), so an exact identifier/error-code match is not lost to a purely semantic ranking. |
| Embedding | A 1024-dimension vector produced by the `bge-m3` model (served by Ollama) representing a record's or query's semantic content. |
| Candidate / active / deprecated | The three lifecycle states of a record (see §6.9). |
| Inline capture | Claude, inside a live session, calling `memory_store` on its own initiative per a rule in `acme/CLAUDE.md`. |
| Session extraction | A background process, launched by a `SessionEnd` hook, that asks `claude -p` (haiku) to extract 0–3 candidate records from a finished session's transcript. |
| PR ingest | A periodic laptop-side job that reads recently-completed Azure DevOps PRs and extracts pattern/decision records from them. |
| Dedup/merge | The decision process run before every write: compare against the nearest existing records and choose ADD, UPDATE, SUPERSEDE, or NOOP (see §6.4). |
| Tailnet | The Tailscale private network (CGNAT range `100.64.0.0/10`) connecting the laptop and the home server. **(v0.2)** Only Postgres on the server is reachable over it, bound to the server's Tailscale IP with password authentication — `claude-memory` itself has no network listener in the MVP (see §4; AC-41 withdrawn, replaced by AC-54). |
| Advisory lock | A Postgres session/transaction-scoped lock (`pg_advisory_xact_lock`) used here to serialize concurrent writes that could otherwise race past each other's dedup check. |

## 3. User Scenarios

### Scenario: Cross-repo paraphrased retrieval
A developer starts a session in `catalog-service` and asks Claude to design a
retry policy for an outbound HTTP call. The `UserPromptSubmit` hook
silently searches memory with the prompt text; a record stored months
earlier from `inventory-service` ("idempotency key gotcha with Azure
DevOps webhook retries") is semantically close enough to be injected as a
short card. Claude sees it before proposing a design.

### Scenario: Inline capture during an active session
While debugging in `billing-service`, Claude discovers that a `nil` pointer
panic was caused by a third-party SDK returning a zero-value struct
instead of an error on a specific failure mode. Per the `acme/CLAUDE.md`
capture rule, Claude calls `memory_store` directly, without asking the
user, recording the gotcha.

### Scenario: Session-end background extraction
A session in `admin-dashboard` ends after 40 messages and several file
edits. The `SessionEnd` hook launches `claude-memory extract
<transcript_path>` in the background and returns control to Claude Code
immediately. Minutes later, the background process asks haiku to review
the transcript; it proposes one candidate record (a convention about how
that repo's dashboard handles optimistic UI updates), which goes through
the dedup/merge decision and is stored as a `candidate`.

### Scenario: Daily PR ingest
Once a day, `claude-memory ingest-pr` runs on the laptop via `launchd`. It
reads, per configured repo, all Azure DevOps PRs completed since the last
recorded cursor, asks haiku to extract pattern/decision records from each
PR's title/description/comments, and stores them as `active` records with
higher initial confidence than inline/session captures.

### Scenario: Deprecating an outdated fact
A later session gives `memory_feedback(id, "outdated")` on a record that
no longer reflects the current code. The record transitions to
`deprecated` with a reason; subsequent `memory_search` calls exclude it
by default.

## 4. Assumptions & Constraints

**Assumptions**
- Single user (the repo owner) for the entire MVP; no multi-tenant
  isolation. **(changed in v0.2)** Authentication is Postgres password
  auth plus Tailscale network-level restriction (see Constraints) rather
  than an MCP-layer bearer token — AC-41 is withdrawn, replaced by AC-54.
- The home Mac Mini server, Tailscale, Docker CE (`data-root
  /mnt/data/docker`), and the existing production workloads (game server,
  Node/PM2 app, MongoDB, Redis, Caddy) are already running and must not be
  reconfigured or disrupted by this deployment. **(changed in v0.2)** The
  server's only `claude-memory`-related workload is now the Postgres +
  pgvector container — Ollama and the `claude-memory` process no longer
  run there.
- **(changed in v0.2)** `bge-m3` via Ollama runs natively on the laptop
  (Homebrew, Metal-accelerated), not on the server. This follows directly
  from the AC-44 smoke test: the server's AVX-only CPU measured
  12–60× slower than the laptop's GPU-accelerated Ollama for this model,
  and linear in input length (~17 ms/token) — unacceptable for a
  synchronous read-path hook.
- The laptop (macOS, arm64) has the Azure CLI (`az`) already authenticated
  against the relevant Azure DevOps organization for PR ingest.
- `claude -p` with the haiku model is available on the laptop for all
  LLM-based extraction/dedup-judgment calls; no LLM call ever runs on the
  server.
- Initial manual seeding (10–20 known facts, e.g. the k8s label 63-byte
  branch-name limit, "billing-service PRs target master", "DECLINED/DECLINED is
  terminal in billing-service") is a one-time operational task performed via
  `memory_store` (or an equivalent seed CLI built on the same code path)
  after deployment — not a separate feature; it exercises the normal write
  path and is not itself a new requirement beyond AC-1/AC-16/AC-53.

**Constraints**
- No LLM runs on the server; all extraction/dedup-judgment calls to
  haiku happen from the laptop via `claude -p`.
- **(v0.2)** Postgres is the only `claude-memory` workload on the server,
  reachable only over Tailscale: bound to the server's Tailscale IP
  (never `0.0.0.0`, never a public interface), `scram-sha-256` password
  authentication, and `pg_hba.conf` restricted to the Tailscale CGNAT
  range `100.64.0.0/10`. The DSN (host, port, database, user, password)
  lives in a local laptop config/env file that is never committed to any
  repo.
- **(v0.2)** Postgres-level TLS (`sslmode=require`) is not mandated for
  the MVP: Tailscale's WireGuard tunnel already encrypts the laptop↔server
  link, so an additional TLS layer on top is judged redundant for this
  single-user, single-link topology (recorded decision — see §13 log
  entry). `sslmode=require` remains available as an optional future
  hardening step (e.g. if a second tailnet device is added) but no AC in
  this spec depends on it.
- **(v0.2)** Ollama and the `claude-memory` MCP server run natively on
  the laptop (not containerized, not on the server): Ollama via
  Homebrew/`brew services` with `OLLAMA_KEEP_ALIVE=-1`; `claude-memory`
  registered as a user-level MCP server over stdio in Claude Code. The
  `UserPromptSubmit` hook, `extract`, and `ingest-pr` commands call the
  same in-process service/library or the local Ollama HTTP API directly —
  there is no laptop→server MCP-over-HTTP hop in the MVP. A
  `--transport http` mode may remain available as a future/optional
  capability, but no AC in this spec requires it (see §12). Caddy on the
  server is not touched.
- **(v0.2)** The server's container resource ceiling is now just
  Postgres: 512 MiB RAM / 1.0 CPU — chosen to leave ample headroom for
  the pre-existing workloads on the 7.2 GiB host, now that Ollama's
  former 1.5 GiB/2.0-CPU ceiling no longer applies there.
- Embedding generation sits behind an `EmbeddingProvider` interface;
  Ollama/`bge-m3` (now laptop-local, per the above) is the only
  implementation shipped in the MVP. The previously-documented llama.cpp
  (no-AVX2 build) fallback — originally scoped for the server — is moot
  under the v0.2 topology, since no embedding runs on the server at all;
  it remains out of scope (see §12).
- Go, `modelcontextprotocol/go-sdk`, `pgx`, Postgres 16 + `pgvector` +
  `tsvector`. Package layering follows the `golang-architecture` skill:
  domain packages declare the ports they need (e.g. an `EmbeddingProvider`,
  a `Store` interface) rather than importing `pgx`/Ollama's HTTP client
  directly; concrete adapters are wired in one composition root
  (`cmd/claude-memory/main.go`).

## 5. Cross-Module Interactions

**(v0.2)** Only one flow crosses a machine boundary: every
`claude-memory` read/write reaches across the Tailscale link to Postgres
on the home server. Everything else — the `UserPromptSubmit` hook,
inline `memory_store` calls, session extraction, and PR ingest — runs
entirely on the laptop, calling the local Ollama (embeddings) and
`claude -p` (haiku extraction/dedup judgment) in-process or over
localhost. There is no longer a laptop→server MCP-over-HTTP hop.

### 5.1 Live session: read path + inline write path

```mermaid
sequenceDiagram
    participant Dev as Developer
    participant CC as Claude Code session (laptop)
    participant Mem as claude-memory (local, stdio)
    participant Oll as Ollama (local, bge-m3)
    participant PG as Postgres + pgvector (server, via Tailscale)

    Dev->>CC: submit prompt
    CC->>Mem: UserPromptSubmit event (prompt text)
    Mem->>Oll: embed(prompt, up to 2048 tokens)
    Oll-->>Mem: query vector
    Mem->>PG: hybrid query (vector + tsvector, fused rank) [timeout 800ms]
    alt PG reachable within timeout
        PG-->>Mem: ranked records
        alt top result >= similarity threshold
            Mem-->>CC: inject 1-3 cards (title, repo, id only)
        else below threshold
            Mem-->>CC: no injection (silent)
        end
    else timeout, Ollama down, or PG unreachable
        Mem-->>CC: silent skip, no error surfaced
    end
    CC->>CC: Claude decides a fact is worth capturing (inline rule)
    CC->>Mem: memory_store(record)
    Mem->>Mem: secret scrub
    Mem->>PG: advisory lock + fetch top-5 nearest (same repo or "*")
    Mem->>Mem: dedup/merge decision (ADD / UPDATE / SUPERSEDE / NOOP)
    opt decision is ADD or UPDATE
        Mem->>Oll: embed(content, up to 2048 tokens)
        Oll-->>Mem: content vector
    end
    alt PG reachable
        Mem->>PG: persist decision result (commit, release lock)
        Mem-->>CC: ack (id, decision taken)
    else PG unreachable
        Mem-->>CC: clear error (no local queue in MVP)
    end
```

**Failure contract**: if local Ollama is down, or Postgres is unreachable
over Tailscale, or the read-path exceeds its timeout, the hook fails
silently (AC-31) — it never blocks or errors the prompt. If Postgres is
unreachable during a write, or the embedding provider fails during a
write, the write returns a clear error with no local retry queue in the
MVP (AC-43, AC-57); during a read, the system degrades to full-text-only
ranking rather than failing the whole query (AC-7).

### 5.2 Background capture: session extraction + PR ingest

```mermaid
flowchart TD
    subgraph Laptop[Laptop - macOS]
        SE(["SessionEnd hook"]) -->|detached, non-blocking| EX[["claude-memory extract <transcript>"]]
        EX --> RT[Read transcript file]
        RT -->|missing/unparseable| LOGEX[Log failure, exit 0, zero records]
        RT -->|valid| GATE{Code changed OR\nmessages > N?}
        GATE -->|no| SKIP[Skip extraction, zero records]
        GATE -->|yes| HAI1[["claude -p haiku: extract candidates"]]
        HAI1 -->|invalid JSON / timeout / error| DISCARD1[Discard, log, zero records]
        HAI1 -->|strict JSON, 0-3 candidates| SCRUB1[Secret scrub]

        CRON(["launchd daily timer"]) --> ING[["claude-memory ingest-pr"]]
        ING --> CUR{Per-repo cursor exists?}
        CUR -->|no / unreadable| LOOKBACK[Default lookback window]
        CUR -->|yes| SINCE[PRs completed since cursor]
        LOOKBACK --> AZ
        SINCE --> AZ[["az CLI -> Azure DevOps REST API"]]
        AZ -->|auth/API failure for a repo| LOGPR[Log failure, leave that\nrepo's cursor unchanged, continue]
        AZ -->|success| HAI2[["claude -p haiku: extract candidates"]]
        HAI2 --> SCRUB2[Secret scrub]
        SCRUB2 --> CURWRITE[Persist cursor after\nrepo batch succeeds]
    end

    SCRUB1 --> WRITE[["memory_store write path (local, see 5.1)"]]
    SCRUB2 --> WRITE
    WRITE --> PG[(Postgres + pgvector, via Tailscale)]
```

**Failure contract**: a malformed/missing transcript, a failed/timed-out
haiku call, or non-conforming JSON output each independently result in
zero stored records and a logged failure — never a partial or garbage
write (AC-26, AC-27). A PR-ingest failure for one repo never blocks or
corrupts the cursor of another repo, and never advances a failed repo's
own cursor past the point of failure (AC-29). **(v0.2)** If Postgres is
unreachable over Tailscale when either background path reaches the write
step, the write returns a clear error with no local retry queue (AC-57)
— the candidate is lost for that run, not silently dropped without a
log entry.

## 6. Functional Requirements

### 6.1 Data model & validation
- AC-1 (Ubiquitous): The system shall persist every memory record with
  the fields `id` (UUID), `kind` (`pattern`|`decision`|`gotcha`|`convention`),
  `title`, `content` (markdown), `repo` (a specific repo name or `*` for
  cross-repo), `files[]`, `commit_sha` (optional), `ticket` (optional),
  `tags[]`, `status` (`candidate`|`active`|`deprecated`),
  `deprecation_reason` (optional), `superseded_by` (optional record id),
  `source` (`inline`|`session`|`pr`), `confidence` (0.0–1.0), `seen_count`,
  `used_count`, `created_at`, `updated_at`, `last_used_at`, and a
  1024-dimension `embedding` vector. Verify: reading back a freshly
  stored record returns all fields populated per this schema.
- AC-2 (Unwanted behavior): IF a write sets `kind`, `status`, or `source`
  to a value outside its enumerated set, THEN the system shall reject the
  write with a validation error and shall not persist a partial record.
  Verify: `memory_store`/`memory_update` with `kind="foo"` returns an
  error and no row is created.
- AC-3 (Unwanted behavior): IF `content` exceeds a configured maximum
  size (default 20,000 characters, `MEMORY_MAX_CONTENT_CHARS`), THEN the
  system shall reject the write with a validation error rather than
  silently truncating it or sending an oversized payload to the embedding
  provider. Verify: storing a record with 50,000 characters of content
  returns an error and no row is created. [NEEDS CLARIFICATION: confirm
  the exact character ceiling — 20,000 is a spec-creator default, not a
  value the owner specified.]

### 6.2 Search & retrieval (`memory_search`)
- AC-4 (Ubiquitous): `memory_search` shall exclude `deprecated` records
  by default and shall rank `candidate` records lower than `active`
  records, flagging each `candidate` result as `"unverified"`. Verify:
  searching a term matching both a `candidate` and an `active` record
  returns the `active` one first, with the `candidate` one flagged.
- AC-5 (Ubiquitous): `memory_search` shall rank results by a fused score
  combining pgvector cosine similarity and Postgres `tsvector` full-text
  match (e.g. Reciprocal Rank Fusion), so a query containing an exact
  identifier/error code/function name surfaces the matching record even
  when it is not the top semantic match. Verify: searching for a literal
  error code stored in a record's content returns that record even when
  a semantically-closer but keyword-unrelated record also exists.
- AC-6 (Ubiquitous): `memory_search` shall include both the queried
  repo's own records and `repo="*"` records in its ranked results, so a
  cross-repo convention is retrievable from any repo via a paraphrased
  query. Verify: a record stored with `repo="*"` is returned by a
  paraphrased query issued from a different repo context.
- AC-7 (Unwanted behavior): IF the embedding provider is unreachable
  during a search, THEN the system shall degrade to a full-text-only
  (`tsvector`) ranking rather than failing the request outright. Verify:
  with Ollama stopped, `memory_search` still returns keyword-matching
  results instead of an error.

### 6.3 Other MCP tools
- AC-8 (Ubiquitous): `memory_update` shall re-compute the record's
  embedding whenever `title` or `content` changes. Verify: updating
  `content` changes the stored `embedding` value.
- AC-9 (Event-driven): WHEN `memory_deprecate(id, reason)` is called, the
  system shall set that record's `status` to `deprecated`, persist the
  `deprecation_reason`, and set `superseded_by` if a replacement id is
  given. Verify: calling `memory_deprecate` then `memory_get` shows the
  updated status and reason.
- AC-10 (Unwanted behavior): IF `memory_get`, `memory_update`,
  `memory_deprecate`, or `memory_feedback` is called with an id that does
  not exist, THEN the system shall return a not-found error rather than a
  silent no-op. Verify: calling any of these four tools with a random
  UUID returns a not-found error.
- AC-11 (Ubiquitous): `memory_list` shall filter by any combination of
  `repo`, `kind`, and `status` when provided, and shall return all
  matching records when none are provided. Verify: listing with
  `repo="billing-service", status="active"` returns only matching rows.
- AC-12 (Event-driven): WHEN `memory_feedback(id, outcome, note?)` is
  called with `outcome` in `useful`|`outdated`|`wrong`, the system shall
  record the feedback and apply the corresponding lifecycle transition
  (§6.9). Verify: `memory_feedback(id, "useful")` on a `candidate` moves
  it to `active` per AC-36.

### 6.4 Write path: dedup/merge
- AC-13 (Event-driven): WHEN any write path (`memory_store`, session
  extraction, PR ingest) is about to persist new content, the system
  shall first fetch the top-5 nearest existing records scoped to the same
  `repo` or `repo="*"`, ranked by the same fused score as search, before
  deciding how to write. Verify: a write to an empty table always
  resolves to ADD with zero candidates considered; a write matching an
  existing record considers it among the top-5.
- AC-14 (Ubiquitous): For session-extraction and PR-ingest write paths,
  the ADD/UPDATE/SUPERSEDE/NOOP decision shall be made by the haiku call
  that also performs extraction, given the candidate content and its
  top-5 nearest existing records. Verify: a PR-ingest run whose extracted
  fact closely matches an existing active record results in UPDATE or
  NOOP, not a duplicate ADD.
- AC-15 (Ubiquitous) **(thresholds set in v0.5)**: For the inline
  `memory_store` write path, the decision shall follow a threshold rule on
  the top candidate's **cosine similarity** (not the fused RRF rank score):
  at or above `MEMORY_STORE_SIM_UPDATE` (default 0.85), treat as the same
  fact (NOOP with `seen_count++`, or UPDATE if the new content materially
  enriches it); between `MEMORY_STORE_SIM_ASK` (default 0.65) and 0.85,
  return the top candidates to the calling Claude session so it can judge
  ADD vs. UPDATE using its own context; below 0.65, ADD. Verify: storing
  near-verbatim content twice in a row results in NOOP, not two rows.
- AC-16 (Unwanted behavior): IF two writes targeting the same `repo` and
  a near-identical normalized title are in flight concurrently, THEN the
  system shall serialize their dedup decision via a Postgres advisory
  transaction lock keyed on `(repo, normalized-title-hash)`, so neither
  write can commit without seeing the other's effect. Verify: firing two
  near-duplicate `memory_store` calls concurrently results in exactly one
  stored record (plus a NOOP/UPDATE on the second), never two ADDs.
- AC-17 (Ubiquitous): A SUPERSEDE decision shall, in one transaction, set
  the old record's `status` to `deprecated` with `superseded_by` pointing
  at the new record's id, and persist the new record. Verify: after a
  SUPERSEDE, the old record is `deprecated` and its `superseded_by`
  resolves to the new record.
- AC-18 (Event-driven): WHEN an extraction or inline judgment determines
  that new content contradicts an existing `active` record, the system
  shall apply SUPERSEDE (old record deprecated, reason recorded) rather
  than ADD. Verify: storing a fact that directly contradicts an existing
  active record results in the old record becoming `deprecated` and the
  new one `active`/`candidate` per its source.

### 6.5 Inline capture (source A)
- AC-19 (Optional feature): WHERE a session identifies a non-obvious bug
  root cause, a user correction, a decision among alternatives with its
  reason, an infra/CI/third-party gotcha, or a reusable snippet, the
  `memory_store` tool shall accept the write without requiring any
  confirmation parameter or user-approval step. Verify: the `memory_store`
  tool contract has no required confirmation field; a call succeeds
  without one.
- AC-20 (Ubiquitous): The `acme/CLAUDE.md` inline-capture rule shall
  instruct Claude not to call `memory_store` for content derivable from
  code or git history, ephemeral task steps, or content already present
  in that repo's own `CLAUDE.md`. Verify: inspection of the rule's text in
  `acme/CLAUDE.md` lists these three exclusions (this is a
  documentation-level, not server-enforced, control — see §12).

### 6.6 Session extraction (source B)
- AC-21 (Event-driven): WHEN a Claude Code `SessionEnd` hook fires, the
  system shall launch `claude-memory extract <transcript_path>` as a
  detached background process and return control to Claude Code within
  100 ms, without waiting for extraction to complete. Verify: the hook
  script's own exit happens before the background process's completion,
  measurably under 100 ms of hook-added latency.
- AC-22 (State-driven): WHILE evaluating a finished session for
  extraction, the system shall only proceed if the session contains at
  least one code change (file write/edit/diff) OR more than
  `MEMORY_EXTRACT_MIN_MESSAGES` messages (default 20); otherwise it shall
  skip extraction with zero records produced. Verify: a 5-message
  session with no file edits produces zero records.
- AC-23 (Ubiquitous): Extraction shall invoke `claude -p` with the haiku
  model against the transcript, expecting a strict JSON array of 0–3
  candidate records; an empty array is a valid, normal result. Verify: a
  session transcript with no new knowledge yields zero stored records
  without being treated as an error.
- AC-24 (Unwanted behavior): IF the transcript file is missing,
  truncated, or not valid against its expected format, THEN extraction
  shall log the failure and exit without storing any record. Verify:
  pointing `extract` at a nonexistent or corrupted transcript path
  results in zero records and a logged error, not a crash or partial
  write.
- AC-25 (Unwanted behavior): IF the `claude -p` subprocess fails, times
  out, or returns output that fails strict JSON parsing, THEN extraction
  shall discard that output, log the failure, and store zero records.
  Verify: simulating a non-JSON haiku response results in zero stored
  records and a logged parse failure.

### 6.7 PR ingest (source C)
- AC-26 (Ubiquitous): `claude-memory ingest-pr` shall, per configured
  repo, query Azure DevOps (via the `az` CLI) for PRs completed since
  that repo's persisted cursor, process them in completion order, and
  persist the cursor only after that repo's batch is fully processed
  successfully. Verify: an interrupted run before completion leaves the
  previous cursor value intact (no partial advance).
- AC-27 (Unwanted behavior): IF the Azure DevOps API call or `az` CLI
  auth fails for a given repo, THEN ingestion shall log the failure,
  leave that repo's cursor unchanged, and continue processing the other
  configured repos. Verify: one repo configured with invalid credentials
  does not prevent other repos' PRs from being ingested in the same run.
- AC-28 (Unwanted behavior): IF a repo's ingest cursor is missing or
  unreadable (e.g. first run), THEN ingestion shall default to a
  configurable lookback window (default 30 days, `MEMORY_PR_INGEST_LOOKBACK`)
  rather than failing, and shall persist a fresh cursor after that run.
  Verify: deleting a repo's cursor file and re-running ingests only PRs
  completed in the last 30 days, then writes a new cursor. [NEEDS
  CLARIFICATION: 30 days is a spec-creator default; confirm desired
  first-run lookback.]
- AC-58 (Ubiquitous) **(new in v0.4)**: `claude-memory ingest-pr` shall
  take its repos from a configured list of local repo directories
  (`MEMORY_PR_INGEST_REPOS`, or root directories scanned for git repos),
  determine each repo's PR provider from its `origin` remote (Azure DevOps:
  `dev.azure.com` / `*.visualstudio.com`; GitHub; GitLab) behind a
  provider-neutral `PRSource` port, and keep the cursor per provider + repo.
  In this version only the Azure DevOps provider is implemented. Verify: a
  repo whose origin is GitHub or GitLab (or unknown) is skipped with a logged
  "provider not supported" warning, its cursor untouched, and the remaining
  repos are still ingested in the same run.
- AC-29 (Optional feature): WHERE a record's `source` is `pr`, the system
  shall set its initial `status` to `active` and initial `confidence` to
  0.75; WHERE `source` is `inline` or `session`, initial `status` shall
  be `candidate` and initial `confidence` 0.5. Verify: a PR-ingested
  record is immediately retrievable as `active` without needing
  `memory_feedback` or a `seen_count` threshold first.

### 6.8 Read path (`UserPromptSubmit` hook)
- AC-30 (Event-driven): **(changed in v0.2 — topology no longer has a
  laptop→server network hop on this path)** WHEN a `UserPromptSubmit`
  event fires, the `claude-memory` hook (local, in-process/stdio) shall
  call `memory_search` with the prompt text, with a hard timeout of
  800 ms covering the local embedding call plus the Postgres round-trip
  over Tailscale, such that total hook-added latency stays under 300 ms
  at p95 on the home network when Postgres is reachable; the 800 ms hard
  timeout is retained as the ceiling for a remote/slow-network case.
  Verify: measuring hook wall-clock time across repeated prompts on the
  home network shows p95 < 300 ms, and it never exceeds 800 ms even on a
  slow/remote link. [NEEDS CLARIFICATION: 300 ms is a spec-creator
  default proposed as reasonable now that embedding is local — confirm
  or retune once measured end-to-end on the real link.]
- AC-31 (Unwanted behavior): **(reworded in v0.2 — failure modes changed
  from "remote server unreachable" to "local Ollama down / Postgres
  unreachable over Tailscale", same silent-skip behavior)** IF the local
  Ollama embedding call fails or is not running, OR Postgres is
  unreachable over Tailscale (e.g. the laptop is off the home
  network/tailnet, or the server is down), OR the search exceeds its
  timeout, THEN the hook shall silently skip (no card injected, no error
  surfaced, hook exits 0) and shall never block or fail the prompt
  submission. Verify: stopping local Ollama, or disconnecting Tailscale,
  still allows a prompt to submit normally with no visible error.
- AC-32 (Event-driven) **(threshold set in v0.5)**: WHEN a search
  result's cosine similarity to the prompt is at or above
  `MEMORY_HOOK_SIM_THRESHOLD` (default 0.50), the hook shall inject 1–3
  cards containing only `title`, `repo`, and `id`; results below the
  threshold shall not be injected. Verify: a weakly-related result below
  0.50 produces no injected card; a strongly-related one does, with only
  those three fields visible.
- AC-33 (Ubiquitous): The `acme/CLAUDE.md` read-path rule shall
  instruct Claude to call `memory_search` before designing a solution,
  verify any retrieved record against current code before relying on it,
  and call `memory_feedback` after using a record. Verify: inspection of
  the rule's text in `acme/CLAUDE.md` confirms all three instructions
  (documentation-level control, not server-enforced).

### 6.9 Lifecycle
- AC-34 (Event-driven): WHEN `memory_feedback(id, "useful")` is received,
  or WHEN a `candidate` record's `seen_count` reaches 2, the system shall
  transition that record's `status` to `active`. Verify: a `candidate`
  record reaches `active` either via one `useful` feedback call or via
  two NOOP "seen again" events, whichever comes first.
- AC-35 (State-driven): WHILE a `candidate` record has received no
  feedback and no `seen_count` increment for `MEMORY_CANDIDATE_TTL`
  (default 180 days), a daily cleanup job shall hard-delete it. Verify: a
  `candidate` record with `last_used_at`/`updated_at` older than 180 days
  and no intervening activity is absent after the next cleanup run.
- AC-36 (Event-driven): WHEN `memory_feedback(id, "outdated"|"wrong")` is
  received, or WHEN a SUPERSEDE decision targets a record, the system
  shall transition that record's `status` to `deprecated` with a
  recorded reason. Verify: `memory_feedback(id, "wrong")` results in
  `status=deprecated` with a reason referencing the feedback.
- AC-37 (Ubiquitous): The TTL cleanup job shall never delete a record
  whose `status` is `active` or `deprecated`, regardless of age. Verify:
  an `active` record older than 180 days with no recent activity survives
  a cleanup run.

### 6.10 Safety: secrets, PII, auth
- AC-38 (Ubiquitous): Before persisting any `memory_store`/`memory_update`
  write, from any capture source, the system shall scan `content` and
  `title` for common secret patterns (bearer/API tokens, passwords, JWTs,
  database/connection strings, `*_SECRET`/`*_KEY`-style assignments).
  Verify: storing content containing a recognizable AWS-style access key
  triggers the scrub path before the write commits.
- AC-39 (Unwanted behavior): IF a secret pattern is detected, THEN the
  system shall redact the matched span with a placeholder before
  embedding or persisting it, and shall record that a redaction occurred
  (e.g. a scrub-event log entry), rather than silently storing the
  original text. Verify: a stored record's `content` contains the
  placeholder, never the original secret value; a log entry confirms the
  redaction.
- AC-40 (Ubiquitous): The system shall not persist raw customer PII; the
  haiku extraction prompt shall instruct the model to avoid including
  it, and the server shall not log full request/response bodies
  (prompt/content text) at its default (info) log level. Verify: default
  server logs for a store/search call contain no raw record content, only
  ids/metadata.
- AC-41 — **Withdrawn (v0.2): MCP-layer bearer-token HTTP auth no longer
  applies.** `claude-memory` has no network listener in the MVP (a local
  stdio process only, registered user-level in Claude Code); the
  authentication/authorization boundary moved to Postgres itself. See
  AC-54 for the replacement requirement.
- AC-54 (Unwanted behavior) **(new in v0.2)**: IF a Postgres connection
  attempt does not present valid `scram-sha-256` credentials, OR
  originates from outside the Tailscale CGNAT range (`100.64.0.0/10`),
  THEN `pg_hba.conf` shall reject the connection before any query runs.
  Verify: connecting with a wrong password is rejected; connecting with
  correct credentials from a non-tailnet address is rejected; connecting
  with correct credentials from a tailnet address succeeds.

### 6.11 Embedding provider
- AC-42 (Ubiquitous): Embedding generation shall sit behind an
  `EmbeddingProvider` interface declared by the package that consumes it;
  Ollama (`bge-m3`), running natively on the laptop **(moved from the
  server in v0.2 — see §4)**, shall be the only implementation shipped in
  the MVP. Verify: the composition root wires one concrete
  `EmbeddingProvider` implementation; no domain package imports an
  Ollama-specific type directly.
- AC-43 (Unwanted behavior): IF the embedding provider is unreachable or
  errors during a `memory_store`/`memory_update` write that requires a
  new embedding, THEN the write shall fail with a clear error rather than
  persisting a record with a missing or zero-value embedding. Verify:
  stopping Ollama and attempting a new `memory_store` returns an error
  and no row is created.
- AC-44 (Event-driven): WHEN the embedding provider is first deployed,
  the system shall support running a smoke-test embedding call and
  recording its latency, so the owner can confirm acceptable performance
  before relying on it for the live hooks in production.
  **Measured baseline (smoke test completed 2026-10-01), which directly
  motivated moving Ollama off the server in v0.2:**
  - Mac Mini 2012 server (AVX-only, no AVX2, `bge-m3` F16 via Ollama,
    2.0 CPUs): ~15 tokens → p50 0.23 s; ~150 tokens → 2.3 s; ~600 tokens
    → 12.3 s (~17 ms/token, linear in length); RSS (anon) 1434 MiB;
    `num_thread` 2 vs. 4 showed no difference.
  - Laptop M1 Pro (native Ollama, Metal, 100% GPU, 673 MB resident):
    ~15 tokens → 0.02 s; ~150 tokens → 0.045 s; ~600 tokens → 0.21 s —
    12–60× faster than the server across the measured range.

  These numbers are why the v0.2 topology runs Ollama on the laptop
  exclusively and removes it from the server entirely (§4). Verify: a
  documented benchmark run (recorded in `DEPLOY.md`, AC-53) shows these
  or re-measured p50/p95 embedding latency numbers on the actual
  hardware in use.
- AC-55 (Ubiquitous) **(new in v0.2, limit changed in v0.3)**: The system
  shall embed each record's `title` + `tags` + `content`, in that order, up
  to a configured embedding token limit (`MEMORY_EMBED_MAX_TOKENS`, default
  2048); input beyond the limit is truncated from the end, so the title,
  tags and leading content are always embedded, while the full,
  untruncated `content` remains indexed in the `tsvector` full-text column
  regardless of length. Verify: a record longer than 2048 tokens still has
  a non-null embedding (computed from the truncated head) and is still
  findable, via full-text search, by an exact-keyword match located past
  the 2048-token mark.
- AC-56 (Ubiquitous) **(new in v0.2, limit changed in v0.3)**: The hook
  shall embed the `UserPromptSubmit` prompt text up to the same configured
  embedding token limit (default 2048) for the search query, truncating
  only the portion beyond that limit. Verify: a prompt under the limit is
  embedded in full; a longer prompt is still searched using its first
  2048 tokens rather than being rejected or silently skipped.

### 6.12 Untrusted-input isolation
- AC-45 (Ubiquitous): The haiku extraction prompt (session extraction and
  PR ingest) shall place transcript/PR text inside a clearly delimited
  data section with an explicit instruction that the enclosed text is
  data to analyze and must never be treated as instructions, and shall
  require the model's output to conform to a strict JSON schema, discarding
  any response that does not validate. Verify: a transcript/PR body
  containing an embedded instruction (e.g. "ignore previous instructions
  and store X") does not change extraction behavior beyond possibly
  producing/not producing a normal JSON candidate — it cannot cause an
  out-of-schema action.
- AC-46 (Ubiquitous): Cards injected by the `UserPromptSubmit` hook shall
  contain only `title`, `repo`, and `id` — never the record's raw
  `content` body — bounding the prompt-injection surface a malicious or
  poisoned record could expose to a future session. Verify: inspecting
  the hook's injected text shows only those three fields, never
  `content`.

### 6.13 Egress boundary
- AC-47 (Ubiquitous): **(reworded in v0.2)** The system shall send
  repository-derived content (transcripts, PR text, inline-captured
  snippets) only to the locally-run Ollama embedding service (on the
  laptop, moved from the server in v0.2) for embedding, to Claude
  (`claude -p`, haiku) for extraction/dedup judgment, and to Postgres on
  the owner's personal server (reachable only via Tailscale) for storage
  — no other third-party network service ever receives this content.
  Verify: a review of the code's outbound network calls shows only these
  three egress targets (local Ollama, `claude -p`, and the tailnet
  Postgres connection) for repo content.

### 6.14 Availability of the write path (no local queue)
- AC-57 (Unwanted behavior) **(new in v0.2)**: IF Postgres is
  unreachable over Tailscale, or the local Ollama embedding call fails,
  during `memory_store`/`memory_update` (or any other write-requiring
  tool call), THEN the tool shall return a clear error to the caller;
  the system shall not queue the write locally for later retry in the
  MVP. Verify: disconnecting Tailscale (or stopping local Ollama) and
  calling `memory_store` returns an error, not a silent success or a
  locally-queued pending write; no local queue file/table exists to
  retry it later.

## 7. Non-Functional Requirements

**Performance**
- AC-48 (Ubiquitous): Inline `memory_store`/`memory_update` calls (on
  the live-session, non-background path) shall complete end-to-end
  (secret scrub + dedup fetch + embedding + persistence) within 3 seconds
  at p95 on the home network. Verify: timing repeated inline store calls
  shows p95 < 3 s. (Hook read-path latency is covered by AC-30.)

**Availability**
- AC-49 (Ubiquitous): **(reshaped in v0.2 — server now runs only
  Postgres)** The `postgres` container shall run within its configured
  memory/CPU limit (512 MiB RAM / 1.0 CPU), and the pre-existing host
  workloads (game server, Node/PM2 app, MongoDB, Redis, Caddy) shall show
  no measurable degradation after it is deployed. Ollama and
  `claude-memory` no longer run on the server at all in the MVP, so their
  former 1.5 GiB/2.0-CPU and 128 MiB/0.5-CPU ceilings no longer apply.
  Verify: `docker stats` over a 24-hour window shows no OOM-kill or
  sustained CPU-throttle event on the `postgres` container, and the game
  server's own uptime/latency is unaffected.
- AC-50 (Unwanted behavior): **(reshaped in v0.2)** IF the `postgres`
  container crashes or the server reboots, THEN the `unless-stopped`
  restart policy and its healthcheck shall bring it back to a healthy
  state without manual intervention beyond the server itself booting.
  (Ollama and `claude-memory` run natively on the laptop in v0.2, not as
  server containers — their own laptop-side auto-start, via `brew
  services` and the user-level Claude Code MCP registration respectively,
  is a laptop configuration detail, not a server-availability AC.)
  Verify: `docker compose kill postgres` followed by `docker compose ps`
  shows it auto-restart and reach `healthy`.
- AC-51 (Ubiquitous): The system shall perform a daily `pg_dump` of the
  `claude-memory` database to `/mnt/data/backups/claude-memory`, retaining
  the last 14 daily backups and pruning older ones. Verify: after 15+
  days of operation, exactly 14 dated backup files are present.

**Security**
- AC-52 (Ubiquitous): **(reshaped in v0.2 — no more `claude-memory`/
  Ollama network surface on the server)** `postgres` shall bind only to
  the server's Tailscale IP (never `0.0.0.0`, never a public interface),
  with `pg_hba.conf` restricted to the Tailscale CGNAT range
  `100.64.0.0/10` and `scram-sha-256` password auth required (AC-54).
  `claude-memory` has no network listener in the MVP (local stdio process
  only), so it has no port to publish or restrict, and `tailscale
  serve`/HTTPS is no longer part of this topology. Verify: `netstat`/`ss`
  on the server shows `postgres` bound only to its Tailscale interface,
  never `0.0.0.0` or a public interface; a connection attempt from
  outside the tailnet is refused at the network layer or by
  `pg_hba.conf`. (Auth/secret/PII requirements: AC-38 through AC-40,
  AC-54; AC-41 is withdrawn — see §6.10.)

**Accessibility / localization**
- N/A: `claude-memory` has no UI in the MVP (CLI + MCP tool surface
  only); written artifacts (commit messages, docs) follow the existing
  `acme/CLAUDE.md` English/Russian convention, which is an
  organizational convention, not a feature of this spec.

**Operational documentation**
- AC-53 (Ubiquitous): The deployment shall be documented in a `DEPLOY.md`
  covering ports, volumes, resource limits, the upgrade procedure, and
  the restore-from-backup procedure. Verify: `DEPLOY.md` exists with
  those five sections, and following its restore procedure against a
  backup file succeeds in a test run.

## 8. Edge Cases (index)

| AC-ID or `accepted: no handling` | Trigger/condition | Category (1–6) |
|---|---|---|
| AC-3 | Oversized `content`/`title` on write | 6 |
| AC-7 | Embedding provider down during search | 5 |
| AC-10 | Tool called with a nonexistent record id | 6 |
| AC-16 | Two near-duplicate writes race concurrently | 6 |
| AC-18 | New fact contradicts an existing active record | 2 |
| AC-24 | Transcript file missing/truncated/malformed | 6 |
| AC-25 | haiku subprocess fails, times out, or returns non-JSON | 5, 6 |
| AC-27 | Azure DevOps API/`az` CLI auth failure for one repo | 5 |
| AC-28 | PR-ingest cursor missing/unreadable on first run | 6 |
| AC-31 | `claude-memory` server unreachable/timeout during hook read | 5 |
| AC-35 | `candidate` record untouched for 180 days | 2 |
| AC-37 | TTL cleanup must never touch `active`/`deprecated` | 2 |
| AC-39 | Secret pattern detected in write content | 6 |
| AC-54 (replaces withdrawn AC-41) | Connection attempt with wrong password or from outside the tailnet CGNAT range | 4, 6 |
| AC-43 / AC-57 | Embedding provider or Postgres down during write (no local retry queue) | 5, 6 |
| AC-45 | Transcript/PR text contains an embedded instruction | 6 |
| AC-49 / AC-50 | Postgres container resource overrun or crash/reboot affecting host services | 4 |
| Resolved (v0.2) via AC-44 | AVX-only CPU makes `bge-m3`/Ollama latency unacceptable on the server | 5 (measured 2026-10-01: server is 12–60× slower than the laptop for `bge-m3`, ~17 ms/token linear — see AC-44; resolved by moving Ollama off the server entirely rather than by building the llama.cpp fallback, which remains intentionally unbuilt — `EmbeddingProvider` exists so it could still be added later; see §12) |
| `accepted: no handling` | A malicious/poisoned record (e.g. from a compromised PR) influences a future session via its injected card | 6 (mitigated, not eliminated, by AC-46's title/repo/id-only card; full mitigation needs review tooling that is out of scope for a single-user MVP) |

## 9. Data Model

**Record** (the only persisted domain entity)
- `id`: UUID, primary key.
- `kind`: enum `pattern` | `decision` | `gotcha` | `convention`.
- `title`: short text.
- `content`: markdown text, bounded by `MEMORY_MAX_CONTENT_CHARS`.
- `repo`: text — a specific repo name (e.g. `billing-service`) or `*` for a
  cross-repo fact.
- `files`: text array — paths the record references (optional).
- `commit_sha`: text, optional.
- `ticket`: text, optional (e.g. a Jira/Azure DevOps key).
- `tags`: text array.
- `status`: enum `candidate` | `active` | `deprecated` (see §6.9 for
  transitions).
- `deprecation_reason`: text, set only when `status = deprecated`.
- `superseded_by`: UUID, optional, references another `Record.id`.
- `source`: enum `inline` | `session` | `pr`.
- `confidence`: float 0.0–1.0, set at creation per source (AC-29),
  adjustable via `memory_update`.
- `seen_count`, `used_count`: integers, incremented by NOOP decisions and
  `memory_feedback` usage respectively.
- `created_at`, `updated_at`, `last_used_at`: timestamps.
- `embedding`: `vector(1024)` (pgvector), recomputed on any `title`/
  `content` change (AC-8).

Lifecycle: `candidate → active` (AC-34), `candidate → (hard-deleted)`
after TTL (AC-35), `active → deprecated` (AC-36); `deprecated` is
terminal in this MVP (no documented un-deprecate path — reintroducing a
fact goes through a new ADD).

**Auxiliary, non-database state**: the PR-ingest cursor (last-ingested
PR/timestamp per repo) is persisted as local laptop-side state (e.g. a
JSON file under the `claude-memory` config directory), not a database
row, since `ingest-pr` runs on the laptop and writes via the same local
`claude-memory` write path used by `memory_store` (AC-26, AC-28), which
itself reaches Postgres over Tailscale (**v0.2** — previously phrased as
"talks to the server through the normal MCP write path", before the
server stopped running an MCP listener).

## 10. Interfaces (MCP tool contracts)

Shapes only — fields, direction, optionality. No wire-format/struct code.

**`memory_search`**
- Input: `query` (string, required), `repo` (string, optional — omitted
  means "current repo + `*`"), `kind` (enum, optional), `tags` (string
  array, optional), `limit` (integer, optional, default 5).
- Output: list of `{ id, kind, title, repo, tags, status, confidence,
  score, unverified: bool }`, excluding `deprecated` by default (AC-4),
  ordered by fused score (AC-5).

**`memory_store`**
- Input: `kind`, `title`, `content` (required); `repo`, `files`,
  `commit_sha`, `ticket`, `tags`, `source` (optional, server may infer a
  default), `confidence` (optional override).
- Output: `{ id, decision: ADD|UPDATE|SUPERSEDE|NOOP, candidates_considered:
  [{id, title, score}] }` — the decision and what it was compared against
  are always returned, never just a bare id.

**`memory_update`**
- Input: `id` (required), any subset of `title`/`content`/`tags`/
  `files`/`ticket`/`status`/`confidence`.
- Output: the updated record (same shape as `memory_get`), or a
  not-found error (AC-10).

**`memory_deprecate`**
- Input: `id` (required), `reason` (string, required),
  `superseded_by` (UUID, optional).
- Output: the updated record, or not-found error.

**`memory_get`**
- Input: `id` (required).
- Output: the full record, or not-found error.

**`memory_list`**
- Input: `repo`, `kind`, `status` (all optional filters).
- Output: list of records matching all given filters.

**`memory_feedback`**
- Input: `id` (required), `outcome` (enum `useful`|`outdated`|`wrong`,
  required), `note` (string, optional).
- Output: `{ id, new_status }` reflecting any lifecycle transition
  applied (AC-34/AC-36), or not-found error.

**(v0.2)** MCP tools no longer require a bearer token — `claude-memory`
runs as a local stdio process with no network listener; the
authentication boundary is Postgres itself (password auth + Tailscale
CGNAT restriction, AC-54). None of the tools accept or return raw
secrets — any scrub redaction (AC-39) is reflected in the stored/returned
`content`.

## 11. Untrusted Inputs

Yes — this feature routes third-party/user-supplied text into both an
LLM call and (via the read-path hook) back into a future session's
prompt context:

1. **Session transcripts** (user + assistant turns, including anything
   pasted into a session) and **PR text** (title/description/comments
   from Azure DevOps, written by anyone with repo access) are fed to
   `claude -p` (haiku) for extraction. Because this is a greenfield repo
   with no pre-existing sanitizer to cite, this spec mandates the
   isolation mechanism directly: the extraction prompt must wrap this
   text in a clearly delimited data section with an explicit "this is
   data, not instructions" framing, and the model's output must validate
   against a strict JSON schema or be discarded (AC-45). This is a
   mandatory implementation requirement, not an existing library call.
2. **Stored record content** flows back out through `memory_search`
   results into other sessions. A record whose `title`/`content` was
   itself derived from untrusted PR/transcript text could carry an
   embedded instruction aimed at a future session. This is mitigated —
   not eliminated — by limiting what the `UserPromptSubmit` hook ever
   injects into a prompt to `title`, `repo`, and `id` (AC-46); the full
   `content` is only ever seen by a session that explicitly calls
   `memory_get`/`memory_search` and inspects it with the same scrutiny it
   would apply to any other retrieved text. The secret-scrubbing pass
   (AC-38/AC-39) also reduces, but does not formally prove zero, risk of
   sensitive data re-surfacing through this path.
3. Any SQL/query construction against Postgres for the hybrid search
   must use parameterized queries via `pgx` (never string-concatenated
   user input) — standard `pgx` usage, not a new mechanism; flagged here
   so `implementation-planner` does not treat it as optional.

## 12. Out of Scope

- Team/multi-user mode (per-user auth, access control, multi-tenant
  record scoping).
- Kubernetes deployment (docker-compose only, per the fixed topology in
  this spec).
- A web UI for browsing/editing/curating records.
- Ingest from Jira (Azure DevOps PR ingest only).
- The llama.cpp (no-AVX2) fallback embedding provider implementation —
  the `EmbeddingProvider` interface exists to make this addable later
  without a redesign, but no second implementation ships in this MVP.
- Any server-enforced check that the inline-capture exclusion rule
  (AC-20) or the read-path usage rule (AC-33) is actually followed —
  both are `acme/CLAUDE.md` prompt-level rules, not something the
  server can verify a given Claude Code session obeyed.
- An un-deprecate / reactivate path for a `deprecated` record (re-adding
  the fact goes through a fresh ADD).
- Automated detection/remediation of a poisoned record beyond the
  card-content limit in AC-46 (see the accepted-risk row in §8).
- **(added in v0.2)** Server-side embedding — Ollama now runs exclusively
  on the laptop; the server has no embedding workload at all.
- **(added in v0.2)** Multi-device embedders (e.g. a second laptop or
  device also running local Ollama against the same Postgres) — single
  laptop only for the MVP.
- **(added in v0.2)** A local write queue / offline retry for
  `memory_store`/`memory_update` when Postgres is unreachable (AC-57) —
  not built in the MVP; a failed write must be retried by the caller once
  connectivity returns.
- **(added in v0.2)** `tailscale serve`/HTTPS exposure of `claude-memory`,
  and a required `--transport http` mode — the MVP is stdio-only; an HTTP
  transport may be added later without changing this spec's ACs.
- **(added in v0.2)** Postgres-level TLS (`sslmode=require`) enforcement
  — judged redundant given Tailscale's WireGuard encryption for this
  single-link MVP topology (see §4); may be enabled later without an AC
  change.

## 13. Clarifications Log

| # | Category (1–6) | Question | Answer / [NEEDS CLARIFICATION] | Impacted AC-ID(s) |
|---|---|---|---|---|
| 1 | 2 | Maximum allowed `content` size per record? | [NEEDS CLARIFICATION: spec-creator default 20,000 chars, not owner-specified] | AC-3 |
| 2 | 6 | Exact similarity thresholds for the inline `memory_store` dedup rule? | Resolved v0.5 (see row 11): 0.65 / 0.85 on cosine similarity. | AC-15 |
| 3 | 5 | First-run PR-ingest lookback window when no cursor exists? | [NEEDS CLARIFICATION: spec-creator default 30 days] | AC-28 |
| 4 | 3 | Similarity threshold for injecting a card on the read path? | Resolved v0.5 (see row 11): 0.50 on cosine similarity. | AC-32 |
| 5 | 1/2/3/4/5/6 | All other functional scope, data model, UX flow, NFR, integration, and edge-case decisions | Fully decided by the owner in prior discussion (see request); recorded directly into §6/§7 without re-asking, per explicit instruction | AC-1–AC-2, AC-4–AC-14, AC-16–AC-27, AC-29–AC-31, AC-33–AC-53 |
| 6 | 5 | Was server-side embedding latency on the AVX-only Mac Mini acceptable for production use? | Measured via the AC-44 smoke test on 2026-10-01: server ~17 ms/token (linear), 12–60× slower than the laptop's Metal-accelerated Ollama. Owner decided (v0.2 topology) to move Ollama and the `claude-memory` MCP process off the server entirely onto the laptop, reducing the server to Postgres-only, reachable solely over Tailscale with password auth replacing the former bearer-token MCP auth. | AC-30 (changed), AC-31 (changed), AC-41 (withdrawn), AC-42 (changed), AC-44 (changed), AC-47 (changed), AC-49 (changed), AC-50 (changed), AC-52 (changed), AC-54 (new), AC-55 (new), AC-56 (new), AC-57 (new) |
| 7 | 4 | What hook-added p95 latency target applies now that embedding is local rather than over the former laptop→server MCP hop? | [NEEDS CLARIFICATION: spec-creator default 300 ms p95, proposed as reasonable given the removed network hop; confirm or retune once measured end-to-end on the real Tailscale link] (2026-10-01) | AC-30 |
| 8 | 4 | Should the new Postgres-over-Tailscale connection require `sslmode=require`, given WireGuard already encrypts the link? | Decided (spec-creator judgment, recorded 2026-10-01 in §4 Constraints): not mandated for the MVP — an additional TLS layer is redundant for this single-user, single-link topology; `sslmode=require` remains available as an optional future hardening step, with no AC depending on it. | None (design decision, not a testable AC) |
| 9 | 4 | What embedding token limit applies, given Ollama's real behavior? | Measured 2026-10-01 (Ollama 0.35, M1 Pro): Ollama silently truncates at `num_batch` (default 2048), keeping leading tokens; 2048 tokens 0.6s, 4096 3.4s, 8192 8.0s. 8192 would break AC-48 (< 3s). Owner decided: configurable `MEMORY_EMBED_MAX_TOKENS`, default 2048, passed as `num_ctx`/`num_batch`; full content always in the full-text index. (v0.3) | AC-55 (changed), AC-56 (changed) |
| 10 | 3 | Where do PRs come from when namespaces span several platforms? | Owner decided 2026-10-01: provider is auto-detected from the repo's git `origin`; MVP ships a provider-neutral `PRSource` port with Azure DevOps only; GitHub/GitLab adapters, per-namespace `pr_ingest` config and `token_env` multi-account auth are backlog (after namespaces). (v0.4) | AC-58 (new) |
| 11 | 6 | What thresholds fit real `bge-m3` scores? | Measured 2026-10-01 (eval harness, real bge-m3 + pgvector, 11 records / 16 queries): relevant hits 0.45–0.72 (median 0.67), unrelated 0.28–0.42, near-duplicates 0.79 and 0.88. The original 0.75 hook / 0.80–0.92 store defaults would never inject a card and would store duplicates. Owner set defaults: hook 0.50, store ask 0.65, store update/NOOP 0.85 — all compared against cosine similarity, never the fused RRF score; still config-driven; revisit with a larger eval set and usage metrics. (v0.5) | AC-15 (changed), AC-32 (changed) |

No blocking questions were raised: every open point above — including
the v0.2 topology items — is either a tunable numeric default or a
recorded design decision that does not change the spec's fundamental
intent (ambient, automatic, cross-repo memory), so none required
`AskUserQuestion` before drafting this revision.

## 14. Acceptance Criteria Summary (Definition of Done)

- [ ] AC-1 — full record schema persisted
- [ ] AC-2 — enum validation rejects invalid `kind`/`status`/`source`
- [ ] AC-3 — oversized content rejected
- [ ] AC-4 — search excludes deprecated by default, flags candidates
- [ ] AC-5 — hybrid fused ranking surfaces exact-identifier matches
- [ ] AC-6 — cross-repo (`*`) records included in search scope
- [ ] AC-7 — search degrades to full-text-only if embedding provider down
- [ ] AC-8 — `memory_update` re-embeds on content/title change
- [ ] AC-9 — `memory_deprecate` sets status/reason/superseded_by
- [ ] AC-10 — not-found error for unknown ids across get/update/deprecate/feedback
- [ ] AC-11 — `memory_list` filters by repo/kind/status
- [ ] AC-12 — `memory_feedback` records outcome and applies lifecycle transition
- [ ] AC-13 — top-5 nearest fetched before every write
- [ ] AC-14 — haiku-driven ADD/UPDATE/SUPERSEDE/NOOP decision for extraction paths
- [ ] AC-15 — threshold-based decision rule for inline `memory_store`
- [ ] AC-16 — advisory lock serializes concurrent near-duplicate writes
- [ ] AC-17 — SUPERSEDE is atomic (old deprecated + superseded_by, new persisted)
- [ ] AC-18 — contradicting fact triggers SUPERSEDE
- [ ] AC-19 — inline `memory_store` requires no confirmation step
- [ ] AC-20 — CLAUDE.md capture rule documents exclusions
- [ ] AC-21 — `SessionEnd` hook launches background extraction without delaying exit
- [ ] AC-22 — extraction gated on code-change-or-message-count
- [ ] AC-23 — haiku returns strict JSON 0–3 candidates; empty is normal
- [ ] AC-24 — malformed/missing transcript logged, zero records
- [ ] AC-25 — haiku failure/invalid JSON discarded, zero records
- [ ] AC-26 — PR ingest persists cursor only after successful batch
- [ ] AC-27 — one repo's ADO failure doesn't block others or corrupt its cursor
- [ ] AC-28 — missing cursor defaults to lookback window
- [ ] AC-29 — PR-sourced records start active/0.75; inline/session start candidate/0.5
- [ ] AC-30 — hook read-path latency budget (<300ms p95 local, 800ms hard timeout) (v0.2)
- [ ] AC-31 — local-Ollama/Postgres-unreachable hook fails silently, never blocks prompt (v0.2)
- [ ] AC-32 — cards injected only above similarity threshold
- [ ] AC-33 — CLAUDE.md read-path rule documents search/verify/feedback steps
- [ ] AC-34 — candidate→active transition (feedback or seen_count>=2)
- [ ] AC-35 — candidate TTL hard-delete after 180 days of inactivity
- [ ] AC-36 — active→deprecated transition (feedback or SUPERSEDE)
- [ ] AC-37 — TTL never deletes active/deprecated
- [ ] AC-38 — secret scrub runs before every write
- [ ] AC-39 — detected secret redacted + redaction recorded
- [ ] AC-40 — no raw PII persisted; no full bodies logged at info level
- [x] AC-41 — **Withdrawn (v0.2)**: MCP bearer-token HTTP auth no longer applies; replaced by AC-54
- [ ] AC-42 — EmbeddingProvider interface, Ollama-only MVP implementation, now laptop-local (v0.2)
- [ ] AC-43 — write fails clearly if embedding provider down
- [ ] AC-44 — AVX smoke test completed; measured baseline recorded, motivated v0.2 topology move
- [ ] AC-45 — extraction prompt isolates untrusted text, enforces strict JSON schema
- [ ] AC-46 — hook cards limited to title/repo/id
- [ ] AC-47 — repo content only ever sent to local Ollama + haiku + tailnet Postgres, no other third party (v0.2)
- [ ] AC-48 — inline store latency budget (<3s p95)
- [ ] AC-49 — postgres container within resource limits (server is now Postgres-only), host workloads unaffected (v0.2)
- [ ] AC-50 — postgres auto-recovery after crash/reboot via restart policy + healthcheck (v0.2)
- [ ] AC-51 — daily backups, 14-day retention
- [ ] AC-52 — postgres tailnet-only + password auth; claude-memory has no network listener (v0.2)
- [ ] AC-53 — DEPLOY.md documents ports/volumes/limits/upgrade/restore
- [ ] AC-54 — Postgres rejects connections lacking valid password auth or outside the tailnet CGNAT range (new, v0.2)
- [ ] AC-55 — record embedding uses title+tags+content up to the configured 2048-token limit; full content always full-text indexed (new v0.2, limit v0.3)
- [ ] AC-56 — hook embeds the prompt up to the configured 2048-token limit (new v0.2, limit v0.3)
- [ ] AC-57 — write path returns a clear error (no local queue) when Postgres/Ollama is unreachable (new, v0.2)
- [ ] AC-58 — ingest-pr detects provider from git origin behind a PRSource port; Azure only, others skipped with a warning (new, v0.4)
