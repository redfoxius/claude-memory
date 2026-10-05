# Specification: Claude Memory — Namespaces

## 0. Metadata
- Spec ID: SPEC-2026-10-01-namespaces
- Status: implemented (code + docs); integration tests pass locally; review follow-ups open — see docs/specs/README.md item 1. Original: draft, re-baselined after owner decisions
- Version: 0.2
- Owner: Oleksandr Kolomoiets
- Supersedes: none (extends SPEC-2026-10-01-memory-mvp)
- Related: `docs/specs/README.md` backlog item 1 (input requirements, agreed
  with the owner 2026-10-01); `docs/specs/memory-mvp/01-spec.md` (AC-6, AC-10,
  AC-11, AC-13, AC-16, AC-31 are refined here, not replaced);
  `docs/specs/namespaces/03-architecture-review.md` (iteration 0)

### Changes in v0.2
Owner decisions taken during the architecture review (§13 #3, #6..#9)
supersede v0.1:
- The built-in fallback is the shared `global` namespace, not `work`.
  No project is special-cased in code (`namespace.Fallback` =
  `memory.DefaultNamespace` = `"global"`). Migration 0002 still backfills
  existing rows to `work`; that is historic data, not a code default.
- "Nothing reaches `global` automatically" is dropped. Writes whose context
  cannot be resolved (inline store, session extraction, PR ingest) land in
  `global`. An explicit `memory_store(namespace="global")` also works.
- Single-user local database: cross-namespace leakage is a relevance
  concern, not a security one. There is no fail-closed machinery. AC-8 is
  rewritten (degrade and warn); AC-9 is withdrawn.
- Creating `namespaces.yaml` is an installation step, done with the new
  `claude-memory namespaces init|add|which` subcommand (AC-25..AC-27).
- New: provenance and a fallback warning (AC-28), the exact-vs-`/**`
  tie-break (AC-6), injected ingest-pr scoping (AC-13), `namespace` in tool
  outputs (AC-23, unchanged but now a release blocker), command-layer tests
  with a stubbed resolver (AC-29), a CI job for the integration suite
  (AC-30), and documented SQL for re-homing records (AC-24).

## 1. Overview & Problem

The MVP stores every record in one pool. All search, dedup and listing runs
across every repo the owner has ever worked in. That is fine while all work
is Acme. Once a second project (e.g. a personal `pet-game`) uses the same
memory, two things go wrong:

- **Noise.** Acme-internal facts (conventions, ticket ids, infra names)
  appear as hook cards in unrelated projects, and side-project facts appear
  in Acme sessions. They crowd out relevant cards.
- **Wrong merges.** Dedup can UPDATE or SUPERSEDE a record from another
  project when the titles look similar.

A **namespace** is a partition key on every record. Every read and write is
scoped to it. Each process resolves its namespace automatically from the
project directory. Facts that really are generic ("pgx returns `ErrNoRows`
for…", Docker/Go/Postgres gotchas) are shared through the `global`
namespace, which every search also covers.

This is a local, single-user database. Namespaces exist for **relevance**,
not for access control. When no namespace can be chosen, work goes to
`global` rather than failing: an occasional stray card is cheap, memory
being silently off is not, and stray records can be re-homed later because
every record carries its `repo`.

## 2. Glossary

| Term | Definition |
|---|---|
| Namespace | Lowercase name matching `^[a-z0-9][a-z0-9_-]{0,62}$` (e.g. `work`, `pet-game`), stored on every record. Records in different namespaces never mix or merge. |
| Current namespace | The namespace a process (or one call within it) is scoped to after resolution (§6.2). |
| `global` | Reserved shared namespace. Every search also covers it. It is also the built-in fallback, so it receives unresolved-context writes as well as explicit `memory_store(namespace="global")` writes. |
| Built-in fallback | `global` (`namespace.Fallback`, `memory.DefaultNamespace`): the result when neither `MEMORY_NAMESPACE`, a glob, nor `default:` applies, or when the file is absent or unusable. |
| `namespaces.yaml` | Per-user mapping file `~/.config/claude-memory/namespaces.yaml`: project-path globs → namespace, plus an optional `default:`. Created at install time with `claude-memory namespaces init`. |
| Provenance | How a namespace was chosen: `env MEMORY_NAMESPACE`, `rule <glob>`, `default`, or `fallback`. |
| Re-home | Moving records from `global` to a project namespace with a documented SQL statement, keyed on `repo` (§10). |

## 3. User Scenarios

### Scenario: Install
During installation (INSTALL.md step 2a) the owner runs
`claude-memory namespaces init work='~/work/acme/**' pet-game='~/src/pet-game/**'`.
No database, DSN or Ollama is needed for this step.
`claude-memory namespaces which ~/work/acme/billing-service` prints
`work	(rule ~/work/acme/**)`.

### Scenario: Two projects, no cross-talk
The owner works in `~/work/acme/billing-service` in the morning and in
`~/src/pet-game` in the evening. Hook cards in `pet-game` never show
Acme records, and the reverse is also true. Session extraction in
`pet-game` never dedups against, updates or supersedes a Acme record.

### Scenario: Sharing a stack-generic fact
In a Acme session Claude finds a generic pgvector gotcha and stores it
with `memory_store(..., namespace="global")`. Later, a `pet-game` session
gets that record as a hook card.

### Scenario: Unmapped project
The owner opens `~/tmp/scratch`, which no glob covers and the file has no
`default:`. The session resolves to `global`: it sees `global` records, and
whatever it stores lands in `global`. `extract --run` and `ingest-pr` log
one warning that the fallback was used. Later the owner runs
`namespaces add scratch '~/tmp/scratch/**'` and, if wanted, re-homes the
records by `repo` (§10).

### Scenario: Per-repo override
A Acme repo checked out outside `~/work/acme/` sets
`"env": {"MEMORY_NAMESPACE": "work"}` in its `.claude/settings.json`.
The hook and the MCP server for that session use `work`, whatever
`namespaces.yaml` says.

### Scenario: Upgrade
Before relying on the new binary, the owner runs `namespaces init` with the
`work` mapping (or writes the file by hand from the example). Then the
first migrating subcommand (`claude-memory cleanup`, or a `serve` start)
applies migration 0002, which sets `namespace = 'work'` on every existing
record. Acme sessions then behave as before. Without the mapping,
Acme directories would resolve to `global` and see none of their
history until it is added.

## 4. Assumptions & Constraints
- Single user, single local Postgres database (as in the MVP). Namespaces
  give logical partitioning enforced by `claude-memory`, not by Postgres
  roles, and they are a relevance feature, not a security boundary.
- Claude Code passes the `env` block of `.claude/settings.json` to hooks and
  to stdio MCP servers. It starts a stdio MCP server with the project
  directory as its working directory. (Plan WI-6 verifies both.)
- Hook payloads (`UserPromptSubmit`, `SessionEnd`) carry `cwd`. Session
  transcripts (JSONL) carry `cwd` on their entries.
- Stack or language inside a namespace (Go vs JavaScript at Acme) is a
  tag. It is not a namespace and not a new field. Conventions that apply
  across a company must still show up across its stacks.
- Layering stays as in the MVP. Resolution is a pure package
  (`internal/namespace`) that the composition root (`cmd/claude-memory`)
  calls. `internal/memory` never reads env vars or files itself.
- Re-scoping per call (hook `cwd`, transcript `cwd`, each ingest-pr repo) is
  done by the composition root. Inner command code receives a scoping
  function (or an already scoped service) and never probes a port for a
  concrete type.

## 5. Cross-Module Interactions

```
              cwd / repo path                 namespace (or global on fallback)
hook ───────► resolveNamespace(dir) ───────► svc.WithNamespace(ns) ──► Search(ns + global)
extract ────► (transcript cwd)                                     ──► FindCandidates/Store(ns)
ingest-pr ──► scope(repoPath)  [func injected by main.go]          ──► FindCandidates/Store(ns)
serve ──────► (process working directory) ──► memory.New(cfg{Namespace})
                                                     │
memory_store(namespace="global") ────────────────────┴─► Store(global)  [explicit]

namespaces init|add|which ──► internal/namespace (file only; no DB, no DSN)
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
  database, every existing row shall get `namespace = 'work'` (historic
  data; not a code default). The column then becomes `TEXT NOT NULL` with
  **no** default. Verify: after the migration,
  `SELECT count(*) FROM records WHERE namespace <> 'work'` is 0, and an
  `INSERT` that leaves out `namespace` fails.
- AC-3 (Ubiquitous): The migration shall create indexes
  `idx_records_namespace_repo (namespace, repo)` and
  `idx_records_namespace_status (namespace, status)`. Verify: both appear in
  `pg_indexes`.
- AC-4 (Ubiquitous): Running the migration again on a migrated database
  shall change nothing and raise no error. Verify: run startup migrations
  twice; row count, namespaces and indexes are unchanged.

### 6.2 Namespace resolution
Resolution order for a directory `dir`. The first rule that applies wins:

1. `MEMORY_NAMESPACE` env var (explicit override; `global` is a valid value)
2. the most specific glob in `namespaces.yaml` that matches `dir`
3. `default:` in `namespaces.yaml`
4. built-in `global`

`namespaces.yaml` format:

```yaml
default: global                   # optional; what `namespaces init` writes
namespaces:
  - namespace: work
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

Specificity: a wildcard pattern scores the length of its literal prefix
before the first wildcard (`/a/b/**` scores `len("/a/b/")`). A pattern with
no wildcard scores `len(path) + 2`, so an exact path beats `<path>/**` and
any other wildcard with the same prefix. The highest score wins. On a tie,
the first pattern in the file wins.

- AC-5 (Ubiquitous): Resolution shall follow the order above. Verify:
  table tests for each step, including an env override that beats a
  matching glob, a `default:` used when no glob matches, and `global`
  when the file is missing.
- AC-6 (Ubiquitous): When several globs match, the most specific one shall
  win. Verify: with `~/work/**` → `a` and `~/work/acme/**` → `b`, the
  directory `~/work/acme/x` resolves to `b` and `~/work/other` resolves
  to `a`, whatever order the two rules appear in. With `/p/**` → `wide`
  listed before `/p` → `exact`, `/p` resolves to `exact` and `/p/x` to
  `wide`.
- AC-7 (Unwanted behavior): IF `namespaces.yaml` does not exist, THEN
  resolution shall not fail. Verify: no file → `global` (or the env value).
- AC-8 (Unwanted behavior): IF `namespaces.yaml` cannot be read or parsed,
  or holds an invalid name, THEN resolution shall log one warning and
  continue as if the file were absent (env, then `global`). IF
  `MEMORY_NAMESPACE` is set but invalid, THEN it shall be logged and
  ignored, and resolution continues with the file. No subcommand fails or
  goes silent because of either. Verify: a broken file → `global` with a
  Warn line; `MEMORY_NAMESPACE=Bad!` with a matching glob → the glob's
  namespace.
- AC-9 — *withdrawn in v0.2.* (v0.1: "`global` can never be the resolved
  namespace".) `global` is now the fallback and a valid
  `MEMORY_NAMESPACE`/`default:` value.

### 6.3 Where resolution applies
- AC-10 (Ubiquitous): `serve` (MCP, stdio) shall resolve once at startup
  from its working directory and scope every tool call to that namespace.
  Verify: an MCP server started in a `pet-game` directory returns no
  `work` records from `memory_search` or `memory_list`.
- AC-11 (Ubiquitous): `hook` shall resolve from the payload `cwd` on every
  call. Verify: with a stubbed resolver, two hook calls with different
  `cwd` values search `[ns(cwd1), global]` and `[ns(cwd2), global]`.
- AC-12 (Ubiquitous): `extract --run` shall resolve from the session's
  `cwd` as recorded in the transcript, and with `dir=""` when the
  transcript has none. Verify: a transcript whose `cwd` is under
  `~/src/pet-game` produces records with `namespace = 'pet-game'`; one
  with no `cwd` and no env/default produces `global` records.
- AC-13 (Ubiquitous): `ingest-pr` shall resolve separately for each repo in
  `MEMORY_PR_INGEST_REPOS`, from that repo's local path, and shall write
  that repo's PR records into the resolved namespace. The re-scoping is a
  scoping function `scope(repoPath) StoreWriter` built in the composition
  root and passed in; `ingestOneRepo` calls it once per repo and does no
  type assertion on the service. Verify: a `cmd` test with a fake `scope`
  shows it is called with each repo's path and that each repo's records
  reach the writer returned for that path (two repos → two writers, no
  cross-over).
- AC-14 (Ubiquitous): `seed` shall write into the namespace resolved from
  its working directory or `MEMORY_NAMESPACE`. `eval-retrieval` shall use a
  dedicated `eval` namespace unless `MEMORY_NAMESPACE` is set, so fixtures
  never land in a project namespace or `global`. `cleanup` (TTL) is a
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
  current namespace. `global` is not included unless it is the current
  namespace. Verify: `memory_list()` from `a` never returns `b` or
  `global` rows.
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
  `namespace` argument. The only accepted explicit value is `"global"`.
  Leaving it out (or passing `""`) writes to the current namespace (which
  may itself be `global`). Any other value is rejected with a validation
  error, and nothing is written. Verify: `namespace="global"` → row in
  `global`; `namespace="pet-game"` from `work` → error, no row.
- AC-22 (Ubiquitous): No automatic writer (session extraction, PR ingest,
  seed, the hook) shall set `StoreRequest.Namespace`. Automatic writes go to
  the current namespace, which is `global` when the context resolves to the
  fallback (unmapped directory, no `cwd`, unusable file). Verify: a grep or
  code-path test finds no non-MCP caller that sets
  `StoreRequest.Namespace`; extraction from a mapped `cwd` produces 0
  `global` rows, and from an unmapped `cwd` produces `global` rows.
- AC-23 (Ubiquitous): MCP tool results shall include the record's
  `namespace`: each `memory_search` item, the `memory_get` and
  `memory_list` records, and `memory_store` (the namespace written), so
  Claude can tell shared records from project records before enriching or
  deprecating them. The hook card format stays the same. Verify: server
  tests assert the field on all four tools.

### 6.6 The `namespaces` subcommand (installation)
`claude-memory namespaces` manages `namespaces.yaml` only. It is dispatched
before config loading, so it needs no database, DSN or Ollama.

- AC-25 (Ubiquitous): `namespaces init [--default NAME] [--force]
  [NAME=GLOB ...]` shall create the file (parent dir 0700, file 0600,
  atomic write) with `default:` = `NAME` or `global`, and one rule per
  mapping. It shall refuse to overwrite an existing file without `--force`
  and reject invalid names or a mapping not shaped `NAME=GLOB`. Verify:
  round-trip test; second `init` without `--force` errors; mode is 0600.
- AC-26 (Ubiquitous): `namespaces add NAME GLOB [GLOB ...]` shall append
  globs to `NAME` (creating the namespace, or the file, if missing) without
  duplicating existing globs. It loads before saving, so a broken file is
  reported, not overwritten. Verify: adding the same glob twice keeps one
  copy; a broken file makes `add` fail and leaves the file unchanged.
- AC-27 (Ubiquitous): `namespaces which [DIR]` (default: current directory)
  shall print the resolved namespace and its provenance (`env
  MEMORY_NAMESPACE`, `rule <glob>`, `default`, or `fallback`), using the
  same resolution as the other subcommands. Verify: one case per
  provenance.
- AC-28 (Event-driven): WHEN a background writer (`extract --run` for a
  session, `ingest-pr` for a repo) resolves its namespace through the
  built-in fallback, it shall log exactly one Warn line naming the
  directory and suggesting `namespaces add`. `serve` shall log its resolved
  namespace and provenance at startup. Verify: a test or manual run with
  an unmapped directory shows one Warn line per session or repo.

### 6.7 Verification infrastructure
- AC-29 (Ubiquitous): Command-layer tests shall not read the developer's
  real `namespaces.yaml` or `MEMORY_NAMESPACE`: `resolveNamespace` is
  stubbed for the `cmd/claude-memory` package, and AC-11..AC-13 have
  per-subcommand tests that assert the namespace each one uses.
- AC-30 (Ubiquitous): A GitHub Actions workflow shall run `go vet` and
  `go test -race ./...` and, in a separate job on a Docker-capable runner,
  `go vet -tags integration ./...` and
  `go test -tags integration ./internal/postgres/...` on every push and
  pull request. Verify: a green run of both jobs on the feature branch.

### 6.8 Documentation
- AC-24 (Ubiquitous): Documentation shall cover:
  - `integration/namespaces.example.yaml`: the format and the resolution
    order (fallback `global`).
  - `integration/INSTALL.md`: step 2a (`namespaces init`), the "Using
    namespaces" section (`which`, `add`, `MEMORY_NAMESPACE`, explicit
    `global`), and the re-home SQL.
  - `DEPLOY.md`: the upgrade order (map `work` before relying on the new
    binary; run `claude-memory cleanup` once to migrate, because the hook
    skips migrations), rollback with default `'global'`, and the re-home
    SQL.
  - The CLAUDE.md capture-rule snippet: when `namespace="global"` is
    appropriate (stack-generic facts only, never company-specific ones), and
    not to add project-specific detail to an existing `global` record.

## 7. Non-Functional Requirements
- Hook latency: resolution (read and parse one small YAML file, glob match)
  adds < 5 ms p95. This is within the MVP AC-30 budget.
- Security: namespace values are passed to SQL only as bound parameters
  (`= $N`, `= ANY($N)`). Cross-namespace visibility is a relevance issue in
  a single-user local database; no fail-closed behaviour is required.
  `namespaces.yaml` holds no secrets; the subcommand writes it 0600 anyway
  under a 0700 directory.
- Logging: the resolved namespace may be logged (Info at `serve` startup,
  Warn on fallback for background writers). Record content may not.

## 8. Edge Cases
- `cwd` is empty or missing from the payload or transcript → resolve with
  `dir=""`. Only the env var, `default:` and `global` can then apply.
- No `namespaces.yaml` → env, then `global`.
- Unreadable or unparsable `namespaces.yaml`, or an invalid name in it →
  one Warn, then as if absent (AC-8).
- Invalid `MEMORY_NAMESPACE` → logged and ignored (AC-8).
- `cwd` is a symlink to a mapped path → no match, because symlinks are not
  resolved (known limitation; set `MEMORY_NAMESPACE` or map both paths).
- The same pattern is mapped to two namespaces → the first one in the file
  wins (tie rule).
- `~/work/acme` and `~/work/acme/**` map to different namespaces →
  the exact path wins for `~/work/acme` itself (AC-6).
- A glob with a bad pattern (e.g. an unclosed `[`) never matches. `add` and
  `init` should reject it up front (plan, non-blocking).
- `memory_store(namespace="global")` from a service whose own namespace is
  `global` → writes to `global`, the same as omitting it.
- An unmapped repo in `ingest-pr` → its PR records go to `global`, with a
  Warn (AC-28). Re-home them with the SQL in §10 after adding a mapping.
- New binary installed, migration 0002 not yet applied → the hook (which
  skips migrations) fails its query and stays silent. Run
  `claude-memory cleanup` once after upgrading.

## 9. Data Model
`records.namespace TEXT NOT NULL` (no default), with indexes
`(namespace, repo)` and `(namespace, status)`. No other schema change.

## 10. Interfaces
- `memory_store`: new optional input `namespace` (`"global"` only); the
  output gains `namespace`.
- `memory_search` / `memory_get` / `memory_list`: the output records gain
  `namespace`. No new inputs.
- CLI: `claude-memory namespaces init [--default NAME] [--force]
  [NAME=GLOB ...]`, `namespaces add NAME GLOB [GLOB ...]`,
  `namespaces which [DIR]` (prints `<namespace>\t(<provenance>)`).
- Env: `MEMORY_NAMESPACE`. File: `~/.config/claude-memory/namespaces.yaml`.
- Re-home (documented SQL, run by hand after a backup):

  ```sql
  -- review first
  SELECT id, title, repo FROM records WHERE namespace = 'global' AND repo = 'billing-service';
  UPDATE records SET namespace = 'work'
   WHERE namespace = 'global' AND repo IN ('billing-service', 'catalog-service');
  ```

  Records moved this way were never deduplicated against the target
  namespace; near-duplicates may need a manual `memory_deprecate`.

## 11. Untrusted Inputs
- `namespace` on `memory_store` comes from the model. It is validated
  against the single allowed value.
- `cwd` in hook payloads and transcripts is used only for glob matching
  (and as `git`'s working directory in `ingest-pr`). It is never executed
  and never used in SQL.
- `namespaces` subcommand arguments come from the owner's shell; names are
  validated, globs are stored as given.

## 12. Out of Scope
- A separate Postgres database or role per namespace (hard isolation; same
  code, a different DSN per namespace). Possible later.
- Fail-closed handling of unresolvable namespaces (decided against, §13 #3).
- A `namespaces rehome` command or an MCP tool for moving records between
  namespaces; promoting records into `global` (re-store them explicitly).
  The documented SQL in §10 is in scope.
- Listing or browsing `global` through `memory_list` from another namespace.
- A dedicated `lang` field. Use tags.
- Per-namespace `pr_ingest` config (backlog item 4 adds it to
  `namespaces.yaml`).
- Resolving symlinks or the git toplevel during glob matching.

## 13. Clarifications Log

All questions are resolved.

| # | Question | Answer | Impacted AC |
|---|---|---|---|
| 1 | Resolution order and file format | Owner, 2026-10-01: env > most specific glob > `default:` > built-in fallback. File at `~/.config/claude-memory/namespaces.yaml`. Fallback changed to `global` by #6. | AC-5..AC-7 |
| 2 | `global` semantics | Owner, 2026-10-01: search = current + `global`. v0.1 "writes only explicit; never automatic" is superseded by #7. | AC-15, AC-21, AC-22 |
| 3 | What happens on a broken config or `MEMORY_NAMESPACE=global`? | **Resolved**, owner, 2026-10-01 (review): no fail-closed. A broken file is logged and treated as absent; an invalid env value is ignored; `global` is a valid value. v0.1's spec-creator proposal (fail closed) is withdrawn. | AC-8, AC-9 (withdrawn) |
| 4 | Can a non-`global` namespace mutate `global` records by id? | **Resolved**: yes (AC-20); confirmed by the review. Precondition: tool outputs show `namespace` (AC-23) so the model knows a record is shared, and the CLAUDE.md snippet says not to add project detail to a `global` record. | AC-19, AC-20, AC-23 |
| 5 | How is "most specific" defined? | **Resolved**: longest literal prefix before the first wildcard; an exact path scores `len+2`, so it beats `<path>/**`; ties go to file order. (v0.1's rule did not hold for `/a/b` vs `/a/b/**`; fixed.) | AC-6 |
| 6 | Which namespace is used when none can be chosen? | **Resolved**, owner, 2026-10-01: `global`. No project is special-cased in code. 0002 still backfills legacy rows to `work` as historic data. | AC-2, AC-5, AC-7, AC-8 |
| 7 | May anything reach `global` without an explicit argument? | **Resolved**, owner: yes. Unresolved-context writes (inline, session, PR) land in `global`; explicit `namespace="global"` also works. Recovery is the re-home SQL. | AC-22, AC-24, AC-28 |
| 8 | Is cross-namespace leakage a security issue? | **Resolved**, owner: no. Local single-user database; leakage is a relevance concern. Visibility (provenance, Warn line) instead of gating. | §1, §7, AC-8, AC-27, AC-28 |
| 9 | How is `namespaces.yaml` created? | **Resolved**, owner: at installation, with `claude-memory namespaces init|add|which` (INSTALL.md step 2a, "Using namespaces", DEPLOY.md upgrade note). | AC-24..AC-27 |

## 14. Acceptance Criteria Summary (Definition of Done)

- [ ] AC-1 — every record carries a non-empty namespace
- [ ] AC-2 — migration backfills `work` (historic), column NOT NULL without default
- [ ] AC-3 — `(namespace, repo)` and `(namespace, status)` indexes
- [ ] AC-4 — migration is idempotent
- [ ] AC-5 — resolution order env > glob > `default:` > `global`
- [ ] AC-6 — most specific glob wins regardless of order; exact path beats `<path>/**`
- [ ] AC-7 — missing `namespaces.yaml` is not an error (→ `global`)
- [ ] AC-8 — broken file / invalid env: warn and degrade, never fail
- AC-9 — withdrawn in v0.2
- [ ] AC-10 — `serve` scoped by its working directory
- [ ] AC-11 — `hook` scoped by payload `cwd`
- [ ] AC-12 — `extract` scoped by transcript `cwd` (`""` when absent)
- [ ] AC-13 — `ingest-pr` scoped per repo path via an injected scope function
- [ ] AC-14 — `seed` resolved, `eval-retrieval` in `eval`, `cleanup` covers all namespaces
- [ ] AC-15 — search = current + `global` only
- [ ] AC-16 — dedup candidates only from the target namespace
- [ ] AC-17 — list confined to the current namespace
- [ ] AC-18 — advisory-lock key includes the namespace
- [ ] AC-19 — by-id ops on another namespace's record → not-found
- [ ] AC-20 — `global` records reachable by id from any namespace
- [ ] AC-21 — `memory_store(namespace="global")` is the only explicit override
- [ ] AC-22 — automatic writers never set the namespace; unresolved → `global` by resolution
- [ ] AC-23 — search/get/list/store outputs expose `namespace`
- [ ] AC-24 — example YAML, INSTALL.md, DEPLOY.md (order, rollback, re-home SQL), CLAUDE.md snippet
- [ ] AC-25 — `namespaces init`
- [ ] AC-26 — `namespaces add`
- [ ] AC-27 — `namespaces which` prints namespace + provenance
- [ ] AC-28 — one Warn per background write context on fallback; `serve` logs its namespace
- [ ] AC-29 — `cmd` tests stub the resolver; per-subcommand namespace tests
- [ ] AC-30 — CI runs unit and `integration`-tag suites
