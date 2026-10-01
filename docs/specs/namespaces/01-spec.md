# Specification: Claude Memory — Namespaces

## 0. Metadata
- Spec ID: SPEC-2026-10-01-namespaces
- Status: draft
- Version: 0.1
- Owner: Oleksandr Kolomoiets (user@example.com)
- Supersedes: none (extends SPEC-2026-10-01-memory-mvp)
- Related: `docs/specs/README.md` backlog item 1 (input requirements, agreed
  with the owner 2026-10-01); `docs/specs/memory-mvp/01-spec.md` (AC-6, AC-10,
  AC-11, AC-13, AC-16, AC-31 are refined here, not replaced)

## 1. Overview & Problem

The MVP stores every record in one pool. All search, dedup and listing runs
across every repo the owner has ever worked in. That is fine while all work
is Acme. Once a second project (e.g. a personal `pet-game`) uses the same
memory, two things go wrong:

- **Leakage.** Acme-internal facts (conventions, ticket ids, infra names)
  appear as hook cards in unrelated projects, and side-project facts appear
  in Acme sessions.
- **Wrong merges.** Dedup can UPDATE or SUPERSEDE a record from another
  project when the titles look similar.

A **namespace** is a hard partition key on every record. Every read and
write is scoped to it. Each process resolves its namespace automatically
from the project directory. Facts that really are generic ("pgx returns
`ErrNoRows` for…", Docker/Go/Postgres gotchas) can be shared through a
`global` namespace. Writing to `global` must always be explicit, so company
content never leaks there by accident.

## 2. Glossary

| Term | Definition |
|---|---|
| Namespace | Lowercase name matching `^[a-z0-9][a-z0-9_-]{0,62}$` (e.g. `acme`, `pet-game`), stored on every record. Records in different namespaces never mix or merge. |
| Current namespace | The namespace a process (or one call within it) is scoped to after resolution (§6.2). |
| `global` | Reserved shared namespace. Every search also covers it. A record is written to it only through an explicit `memory_store(namespace="global")`. |
| `namespaces.yaml` | Per-user mapping file `~/.config/claude-memory/namespaces.yaml`: project-path globs → namespace, plus an optional `default:`. |
| Built-in fallback | `acme`: the namespace existing records are backfilled to, and the result when nothing else applies. |

## 3. User Scenarios

### Scenario: Two projects, no cross-talk
The owner works in `~/work/acme/billing-service` in the morning and in
`~/src/pet-game` in the evening. Hook cards in `pet-game` never show
Acme records, and the reverse is also true. Session extraction in
`pet-game` never dedups against, updates or supersedes a Acme record.

### Scenario: Sharing a stack-generic fact
In a Acme session Claude finds a generic pgvector gotcha and stores it
with `memory_store(..., namespace="global")`. Later, a `pet-game` session
gets that record as a hook card.

### Scenario: Per-repo override
A Acme repo checked out outside `~/work/acme/` sets
`"env": {"MEMORY_NAMESPACE": "acme"}` in its `.claude/settings.json`.
The hook and the MCP server for that session use `acme`, whatever
`namespaces.yaml` says.

### Scenario: Upgrade
On first start after the upgrade, migration 0002 runs. Every existing
record now has `namespace = 'acme'`, and Acme sessions behave exactly
as before.

## 4. Assumptions & Constraints
- Single user, single Postgres database (as in the MVP). Namespaces give
  logical isolation enforced by `claude-memory`, not by Postgres roles.
- Claude Code passes the `env` block of `.claude/settings.json` to hooks and
  to stdio MCP servers. It starts a stdio MCP server with the project
  directory as its working directory. (WI-6 verifies both.)
- Hook payloads (`UserPromptSubmit`, `SessionEnd`) carry `cwd`. Session
  transcripts (JSONL) carry `cwd` on their entries.
- Stack or language inside a namespace (Go vs JavaScript at Acme) is a
  tag. It is not a namespace and not a new field. Conventions that apply
  across a company must still show up across its stacks.
- Layering stays as in the MVP. Resolution is a pure package
  (`internal/namespace`) that the composition root
  (`cmd/claude-memory`) calls. `internal/memory` never reads env vars or
  files itself.

## 5. Cross-Module Interactions

```
              cwd / repo path                 namespace
hook ───────► resolveNamespace(dir) ───────► svc.WithNamespace(ns) ──► Search(ns + global)
extract ────► (transcript cwd)                                     ──► FindCandidates/Store(ns)
ingest-pr ──► (each MEMORY_PR_INGEST_REPOS path)                   ──► FindCandidates/Store(ns)
serve ──────► (process working directory) ──► memory.New(cfg{Namespace})
                                                     │
memory_store(namespace="global") ────────────────────┴─► Store(global)  [explicit only]
```

`resolveNamespace` reads `MEMORY_NAMESPACE` and `namespaces.yaml`. The
service hands the namespace to every `Store` port call. Postgres filters
on it in SQL.

## 6. Functional Requirements

### 6.1 Data model & migration
- AC-1 (Ubiquitous): Every record shall carry a non-empty `namespace`. The
  record is persisted with it and returned by get, search and list.
  Verify: a stored record read back by id has the namespace of the service
  that wrote it.
- AC-2 (Event-driven): WHEN migration `0002_namespaces.sql` runs on an MVP
  database, every existing row shall get `namespace = 'acme'`. The
  column then becomes `TEXT NOT NULL` with **no** default. Verify: after the
  migration, `SELECT count(*) FROM records WHERE namespace <> 'acme'`
  is 0, and an `INSERT` that leaves out `namespace` fails.
- AC-3 (Ubiquitous): The migration shall create indexes
  `idx_records_namespace_repo (namespace, repo)` and
  `idx_records_namespace_status (namespace, status)`. Verify: both appear in
  `pg_indexes`.
- AC-4 (Ubiquitous): Running the migration again on a migrated database
  shall change nothing and raise no error. Verify: run startup migrations
  twice; row count, namespaces and indexes are unchanged.

### 6.2 Namespace resolution
Resolution order for a directory `dir`. The first rule that applies wins:

1. `MEMORY_NAMESPACE` env var (explicit override)
2. the most specific glob in `namespaces.yaml` that matches `dir`
3. `default:` in `namespaces.yaml`
4. built-in `acme`

`namespaces.yaml` format:

```yaml
default: acme                  # optional
namespaces:
  - namespace: acme
    paths: ["~/work/acme/**"]
  - namespace: pet-game
    paths: ["~/src/pet-game", "~/src/pet-game/**"]
```

Glob rules:
- A leading `~` is expanded to `$HOME`. Paths are cleaned before matching.
- `**` matches zero or more path segments.
- Every other segment is matched with `filepath.Match` (`*`, `?`, `[…]`
  match inside one segment only).
- A pattern with no wildcard matches that exact directory only. To cover a
  whole tree, list it together with `/**`.

Specificity: a pattern scores the length of its literal prefix before the
first wildcard. An exact path scores higher than any wildcard pattern with
the same prefix. The highest score wins. On a tie, the first pattern in the
file wins.

- AC-5 (Ubiquitous): Resolution shall follow the order above. Verify:
  table tests for each step, including an env override that beats a
  matching glob, a `default:` used when no glob matches, and `acme`
  when the file is missing.
- AC-6 (Ubiquitous): When several globs match, the most specific one shall
  win. Verify: with `~/work/**` → `a` and `~/work/acme/**` → `b`, the
  directory `~/work/acme/x` resolves to `b` and `~/work/other` resolves
  to `a`, whatever order the two rules appear in.
- AC-7 (Unwanted behavior): IF `namespaces.yaml` does not exist, THEN
  resolution shall not fail. Verify: no file → `acme` (or the env
  value).
- AC-8 (Unwanted behavior): IF `namespaces.yaml` cannot be read or parsed,
  or holds an invalid name, or `MEMORY_NAMESPACE` is invalid, THEN the
  process shall not read or write any namespace it guessed:
  - The hook returns silently with no card (MVP AC-31).
  - `extract` and `ingest-pr` log the error and write nothing.
  - `serve` and `seed` exit with a clear error.

  Verify: one test per subcommand with a broken file.
- AC-9 (Unwanted behavior): IF the resolved namespace would be `global`,
  whether from `MEMORY_NAMESPACE`, a glob or `default:`, THEN resolution
  shall be rejected and handled as in AC-8. Writes to `global` must come
  only from an explicit `memory_store` argument (AC-17). Verify:
  `MEMORY_NAMESPACE=global` → hook silent, `extract` writes nothing.

### 6.3 Where resolution applies
- AC-10 (Ubiquitous): `serve` (MCP, stdio) shall resolve once at startup
  from its working directory and scope every tool call to that namespace.
  Verify: an MCP server started in a `pet-game` directory returns no
  `acme` records from `memory_search` or `memory_list`.
- AC-11 (Ubiquitous): `hook` shall resolve from the payload `cwd` on every
  call. Verify: two hook calls with `cwd` values in different namespaces
  search different namespaces.
- AC-12 (Ubiquitous): `extract --run` shall resolve from the session's
  `cwd` as recorded in the transcript. Verify: a transcript whose `cwd` is
  under `~/src/pet-game` produces records with `namespace = 'pet-game'`.
- AC-13 (Ubiquitous): `ingest-pr` shall resolve separately for each repo in
  `MEMORY_PR_INGEST_REPOS`, from that repo's local path, and shall write
  that repo's PR records into the resolved namespace. Verify: two repos
  mapped to two namespaces in one run produce records in both namespaces.
  None of them lands in the other repo's namespace.
- AC-14 (Ubiquitous): `seed` shall write into the namespace resolved from
  its working directory or `MEMORY_NAMESPACE`. `cleanup` (TTL) is a
  lifecycle sweep and covers every namespace.

### 6.4 Isolation rules
- AC-15 (Ubiquitous): `memory_search` and the hook shall return records only
  from the current namespace and `global`. Verify: identical records in
  `a`, `b` and `global`, searched from `a`, return the `a` and `global`
  records and never the `b` one.
- AC-16 (Ubiquitous): Dedup candidates (`FindCandidates`, in and outside
  the write transaction) shall come only from the namespace being written
  to. A write therefore never NOOPs into, UPDATEs or SUPERSEDEs a record of
  another namespace, and that includes `global` when the write targets
  another namespace. An `ExtractionDecision.TargetID` outside the candidate
  set is ignored, as in the MVP. Verify: storing an exact duplicate of a
  `b` record from `a` gives ADD, and both rows exist.
- AC-17 (Ubiquitous): `memory_list` shall return only records of the
  current namespace. `global` is not included. Verify: `memory_list()` from
  `a` never returns `b` or `global` rows.
- AC-18 (Ubiquitous): The advisory-lock key shall include the namespace
  (`namespace:repo:titleHash`). Verify: concurrent near-duplicate writes in
  the same namespace give one row. The same writes in two namespaces give
  one row each and do not wait on each other.
- AC-19 (Unwanted behavior): IF `memory_get`, `memory_update`,
  `memory_deprecate` or `memory_feedback` targets an id whose record is in
  another namespace (not current, not `global`), THEN the tool shall return
  the same not-found error as for an unknown id (MVP AC-10) and change
  nothing. Verify: a `b` id used from `a` → not-found, and the row is
  unchanged.
- AC-20 (Ubiquitous): `global` records can be read, updated, deprecated and
  given feedback from any namespace.

### 6.5 The `global` namespace
- AC-21 (Ubiquitous): `memory_store` shall accept an optional
  `namespace` argument. The only accepted value is `"global"`. Leaving it
  out (or passing `""`) writes to the current namespace. Any other value
  is rejected with a validation error, and nothing is written. Verify:
  `namespace="global"` → row in `global`; `namespace="pet-game"` from
  `acme` → error, no row.
- AC-22 (Ubiquitous): Nothing shall reach `global` automatically. Session
  extraction, PR ingest, seed and the hook never set the namespace on a
  write request. Verify: a code-path test or grep finds no non-MCP caller
  that sets `StoreRequest.Namespace`. Extraction output in any namespace
  produces 0 `global` rows.
- AC-23 (Ubiquitous): MCP tool results (`memory_search`, `memory_get`,
  `memory_list`, `memory_store`) shall include each record's `namespace`,
  so Claude can tell shared records from project records. The hook card
  format stays the same.

### 6.6 Documentation
- AC-24 (Ubiquitous): `integration/namespaces.example.yaml` shall document
  the format and the resolution order. `integration/INSTALL.md` and
  `DEPLOY.md` shall describe setup, the upgrade/backfill and the
  `MEMORY_NAMESPACE` override. The CLAUDE.md capture-rule snippet shall say
  when `namespace="global"` is appropriate (stack-generic facts only, never
  company-specific ones).

## 7. Non-Functional Requirements
- Hook latency: resolution (read and parse one small YAML file, glob match)
  adds < 5 ms p95. This is within the MVP AC-30 budget.
- Security: namespace values are passed to SQL only as bound parameters
  (`= $N`, `= ANY($N)`). `namespaces.yaml` holds no secrets, so it has no
  0600 requirement, unlike `~/.config/claude-memory/env`.
- Logging: the resolved namespace may be logged at debug level. Record
  content may not.

## 8. Edge Cases
- `cwd` is empty or missing from the payload or transcript → resolve with
  `dir=""`. Only the env var, `default:` and `acme` can then apply.
- `cwd` is a symlink to a mapped path → no match, because symlinks are not
  resolved (known limitation; set `MEMORY_NAMESPACE` or map both paths).
- The same pattern is mapped to two namespaces → the first one in the file
  wins (tie rule).
- `memory_store(namespace="global")` from a service whose own namespace is
  `global` cannot happen, because AC-9 forbids resolving to `global`.

## 9. Data Model
`records.namespace TEXT NOT NULL` (no default), with indexes
`(namespace, repo)` and `(namespace, status)`. No other schema change.

## 10. Interfaces
- `memory_store`: new optional input `namespace` (`"global"` only); the
  output gains `namespace`.
- `memory_search` / `memory_get` / `memory_list`: the output records gain
  `namespace`. No new inputs.
- Env: `MEMORY_NAMESPACE`. File: `~/.config/claude-memory/namespaces.yaml`.

## 11. Untrusted Inputs
- `namespace` on `memory_store` comes from the model. It is validated
  against the single allowed value.
- `cwd` in hook payloads and transcripts is used only for glob matching.
  It is never executed and never used in SQL.

## 12. Out of Scope
- A separate Postgres database or role per namespace (hard isolation; same
  code, a different DSN per namespace). Possible later.
- Moving or copying records between namespaces, and promoting records into
  `global` (re-store them explicitly instead).
- Listing or browsing `global` through `memory_list`.
- A dedicated `lang` field. Use tags.
- Per-namespace `pr_ingest` config (backlog item 4 adds it to
  `namespaces.yaml`).
- Resolving symlinks or the git toplevel during glob matching.

## 13. Clarifications Log

| # | Question | Answer | Impacted AC |
|---|---|---|---|
| 1 | Resolution order and file format | Owner, 2026-10-01: env > most specific glob > `default:` > `acme`. File at `~/.config/claude-memory/namespaces.yaml`. | AC-5..AC-7 |
| 2 | `global` semantics | Owner, 2026-10-01: search = current + `global`; writes only explicit; never automatic. | AC-15, AC-21, AC-22 |
| 3 | What happens on a broken config or `MEMORY_NAMESPACE=global`? | Spec-creator decision: fail closed (silent hook, no write) rather than fall back to `acme`. Falling back would put a side project's facts into Acme. [NEEDS CLARIFICATION: confirm with the owner] | AC-8, AC-9 |
| 4 | Can a non-`global` namespace mutate `global` records by id? | Spec-creator decision: yes (AC-20). `global` is shared, and deprecating stale shared facts must stay possible from anywhere. | AC-19, AC-20 |
| 5 | How is "most specific" defined? | Longest literal prefix before the first wildcard; an exact path beats a wildcard; ties go to file order. | AC-6 |

## 14. Acceptance Criteria Summary (Definition of Done)

- [ ] AC-1 — every record carries a non-empty namespace
- [ ] AC-2 — migration backfills `acme`, column NOT NULL without default
- [ ] AC-3 — `(namespace, repo)` and `(namespace, status)` indexes
- [ ] AC-4 — migration is idempotent
- [ ] AC-5 — resolution order env > glob > `default:` > `acme`
- [ ] AC-6 — most specific glob wins regardless of order
- [ ] AC-7 — missing `namespaces.yaml` is not an error
- [ ] AC-8 — broken config or invalid env fails closed per subcommand
- [ ] AC-9 — `global` can never be the resolved namespace
- [ ] AC-10 — `serve` scoped by its working directory
- [ ] AC-11 — `hook` scoped by payload `cwd`
- [ ] AC-12 — `extract` scoped by transcript `cwd`
- [ ] AC-13 — `ingest-pr` scoped per repo path
- [ ] AC-14 — `seed` resolved, `cleanup` covers all namespaces
- [ ] AC-15 — search = current + `global` only
- [ ] AC-16 — dedup candidates only from the target namespace
- [ ] AC-17 — list confined to the current namespace
- [ ] AC-18 — advisory-lock key includes the namespace
- [ ] AC-19 — by-id ops on another namespace's record → not-found
- [ ] AC-20 — `global` records reachable by id from any namespace
- [ ] AC-21 — `memory_store(namespace="global")` is the only override
- [ ] AC-22 — no automatic writes to `global`
- [ ] AC-23 — tool outputs expose `namespace`
- [ ] AC-24 — example YAML, INSTALL.md, DEPLOY.md, CLAUDE.md snippet updated
