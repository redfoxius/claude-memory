# Review: backlog items 5 (full-text for natural-language queries) and 6 (service-level integration tests)

Reviewed: commits `b3e5da1..HEAD` (`3d1ed25`, `bf9065f`, `f4bded3`) on branch `claude/task-rb9lio`, 2026-10-01.
Method: code reading plus empirical verification against a local PostgreSQL 16.14 + pgvector 0.6.0
(`MEMORY_TEST_PG_ADMIN_DSN`, one throwaway database per test), psql probes, a 5000-row EXPLAIN ANALYZE,
and mutation testing on a copy of the repo under the scratchpad (the working tree was not modified).

## Gate

| Item | Verdict | Reason |
|---|---|---|
| 6 — service-level integration tests | **PASS** | Every backlog bullet is covered; all 14 compiling mutants (incl. the three historical bugs) were killed; 7 consecutive runs (one with `-race`) showed no flakiness. |
| 5 — full-text for natural-language queries | **FAIL — do not mark DONE** | Two high-severity defects: (H1) the Go-side tokenizer disagrees with Postgres' parser, so dotted/slashed identifiers (`db.WithTx`, `pg_hba.conf`, `100.64.0.0/10`, `OrderService.Cancel`) never match — a regression versus the old `plainto_tsquery`, and 2 of the 4 acceptance cases pass only via ordinary words; (H2) the 32-term cap truncates by position, so an identifier buried past the 32nd distinct word of a hook prompt is dropped — the exact scenario the item exists for. The "no paraphrase regression" half is unverified (the README admits this) and the mechanism makes a regression plausible. |
| Build / vet / tests | PASS | `go build ./...`, `go vet ./...`, `go vet -tags integration ./...`, `go test -race ./...` all clean; `go test -tags integration -count=1 ./internal/postgres/...` 28/28 pass in 3.8 s; `make test-integration` passes (10 s wall). |

Recommended action: keep item 6 as DONE; revert the README line for item 5 to "implemented, not accepted",
fix H1 and H2, re-run the acceptance test, then run `claude-memory eval-retrieval` against real Ollama
before declaring the paraphrase half.

## Findings, by severity

### H1 — Query tokenizer diverges from Postgres' text parser: dotted/slashed identifiers can never match (regression)

`internal/postgres/ftsquery.go:56-58` splits the query on every rune that is not a letter, digit or `_`,
then joins the pieces with `|`. The index side (`tsvector.go`, `to_tsvector('simple', …)`) uses Postgres'
default parser, which keeps dotted and slashed tokens whole (`host`, `file`, `url_path` token types).

Empirically verified (psql + scratch copy, eval seed set loaded):

| Stored text | Index lexemes | Query string built by `buildFTSQueries` | Matches? | Old `plainto_tsquery` on the same input |
|---|---|---|---|---|
| `` `db.WithTx()` `` | `'db.withtx'` | `db \| withtx` | **no** | yes (seed 3) |
| `` `pg_hba.conf` `` | `'pg_hba.conf'` | `pg_hba \| conf` → `'pg' <-> 'hba' \| 'conf'` | **no** | yes (seed 7) |
| `100.64.0.0/10` | `'100.64.0.0'`, `'/10'` | `100 \| 64 \| 10` | **no** | yes (seed 7) |
| `OrderService.Cancel` | `'orderservice.cancel'` | `orderservice \| cancel` | **no** | yes |
| `DECLINED/DECLINED` | `'declined/declined'` | `declined` | **no** | yes (seed 2) |
| `acme/CLAUDE.md` | `'acme/claude.md'` | `acme \| claude \| md` | wrong record (seed 6, via "claude") | seed 0 |
| `pg_advisory_xact_lock` | `'pg' 'advisory' 'xact' 'lock'` (positions 1-4) | phrase `'pg' <-> 'advisory' <-> 'xact' <-> 'lock'` | yes | yes |
| `num_batch`, `ERR_GATEWAY_TIMEOUT`, `ErrNoRows` | split on `_` / single lexeme | phrase / single | yes | yes |

Consequences for the acceptance claim (see §4 below): `long_identifier_003` (`100.64.0.0/10`) and
`long_identifier_004` (`db.WithTx`) are found **only** through ordinary words (`host`, `range`, `all`, `dedup`,
`lock`, `transaction`, …). Removing the identifier from those two queries does not change the result; the
identifier alone returns nothing. `exact_identifier_003` is likewise carried by `tailscale | cgnat`.
This is a functional regression for short identifier queries that previously worked (`pg_hba.conf`, `db.WithTx`
alone → now zero results in the full-text-only path).

Fix (either):
- Let Postgres tokenize: pass the raw text and build the OR query in SQL from the parser's own lexemes, e.g.
  `to_tsquery('simple', (SELECT string_agg(quote_literal(l), ' | ') FROM unnest(tsvector_to_array(to_tsvector('simple', $3))) l))`
  (quote each lexeme; `url_path` lexemes can contain `&`/`(`), or use `websearch_to_tsquery('simple', <whitespace tokens joined with ' OR '>)`
  after stripping `"` and a leading `-` (never raises a syntax error, same parser as the index). Keep the Go side for
  stopword removal, dedup and identifier detection on raw whitespace tokens (trim only end punctuation).
- Add tsvector-side cases to `TestBuildFTSQueries`/`TestFullTextNaturalLanguagePrompts` for `a.b`, `a/b`, `a-b`,
  `1.2.3.4/10`, so the two tokenizers are pinned to agree.

### H2 — `maxFTSTerms = 32` drops identifiers that appear after the 32nd distinct term

`ftsquery.go:78-80` stops collecting terms at 32 in input order. The hook sends the whole prompt
(`cmd/claude-memory/hook.go:138`), and real prompts routinely exceed 32 distinct non-stopwords.
Verified: 38 filler words + `could num_batch be the culprit` → `num_batch` is not in the query,
full-text-only returns seeds 1/7/3 at score 0.0038 (one shared generic word each); expected seed 4 absent;
hybrid with flat vectors likewise misses it. The identifier the feature exists to find is the first thing cut.

Fix: collect identifier-like terms first (never cap them, or reserve slots), cap only ordinary terms;
or raise the cap (ts_rank cost is linear in terms; 64-128 is cheap). Add a unit test with the identifier at position > 32.

### M1 — The relative floor sheds rare content-only words whenever any query word hits a title (A weight = 5 × C weight)

`hybrid_search.go:107` / `:238`: a non-identifier match survives only if `ts_rank ≥ 0.25 × MAX(ts_rank)`.
`ts_rank` with the default weights gives a single matched word 0.608 at weight A (title), 0.243 at B (tags),
0.122 at C (content), each divided by the number of query lexemes (verified: 0.6079 / 0.3040 / 0.1013 for 1/2/6 terms,
so the "scales with term count" comment in `ftsquery.go:8-13` is correct and the relative floor does neutralize it).
But one title word is worth five content words, and `'simple'` has no IDF, so a rare lowercase word that is only in
a record's content is shed by a generic word in another record's title (ratio 0.2 < 0.25). Verified on the seed set:

| Query (repo-scoped correctly) | Result |
|---|---|
| `does hashtext work` | seed 5 only (`work` in its title); seed 3 (`hashtext` in content) **shed** |
| `can a deadlock happen` → `… on the server` | seed 3 found → **gone**; seed 8 (`server` in title) only |
| `how does erp behave` → `… on the server` | seed 2 #1 → #2 behind seed 8 |
| `is the wireguard tunnel involved` → `… in the laptop setup` | seed 7 #1 → #2 behind seed 8 |

The identifier override cannot help here: `hashtext`, `deadlock`, `erp`, `wireguard`, `tailscale`, `ollama`, `pgx`
are not identifier-like. In the hybrid path the shed record also loses its FTS boost while the title-word record
gains one (see M2), e.g. `does hashtext work on the server` hybrid → seed 8, seed 5, seed 7, seed 1, seed 10; seed 3 absent.

Fix options: compute the floor on a weight-neutral rank (`ts_rank('{1,1,1,1}', …)`) while ordering by the weighted
rank; or lower the floor to ~0.15 so a single content word survives a single title word (noise then also survives,
which is what RRF's rank compression is for); or treat words absent from a small common-word list as "rare" (poor
man's IDF). Whatever is chosen, add a test for "rare content word vs generic title word".

### M2 — Paraphrase regression risk is real in mechanism and unverified in practice

`vector_ranks` (`hybrid_search.go:83-90`) ranks **every** in-scope row (no LIMIT), so in RRF any record that passes the
FTS floor gets `1/(60+fts_rank)` on top of a vector term that every record has. Consequence: a record at FTS rank 1
with vector rank N scores `1/61 + 1/(60+N)`, which beats a record at vector rank 1 with no FTS hit (`1/61`) for every N.
Before this change, FTS contributed nothing to natural-language prompts (AND semantics), so paraphrase ranking was
vector-only; now any record sharing one generic title word with the prompt is boosted above purely semantic matches.

Synthetic experiment (seed set, expected record given cosine 0.99, the other ten cosine 0.90…0.63):
all six `paraphrase_*` cases keep the expected record at rank 1 **because each expected record is also FTS rank 1**
(the paraphrases share title words such as "pull requests", "transcripts", "Azure DevOps"). The noise records at FTS
rank 2-3 jump from vector ranks 2-11 to within 0.0008 of the top (0.0320 vs 0.0328) and displace the vector-rank-2/3
records — e.g. `paraphrase_005`: seeds 1 and 9 take #2/#3 on `azure`/`devops`/`api`. On this 11-record eval set a
paraphrase whose expected record shares no title word with the query does not exist, so the eval cannot detect the
regression class this change introduces. The README's "**Not verified** … needs eval-retrieval against real Ollama" is
honest and must be resolved before DONE. Mitigation if it regresses: count non-identifier FTS hits only within the top-K
FTS ranks, or weight them (e.g. 0.5) in the fusion.

### M3 — Digits-only tokens are "identifiers" and bypass the floor

`ftsquery.go:34` (`unicode.IsDigit(r)` → identifier). `10`, `30`, `63`, `2048`, years, ports are treated as rare and are
always kept. Verified: appending `build 2048` to a k8s prompt pulls seed 4 (Ollama `num_batch`, "2048 tokens") into
the results. Numbers are common in technical content; consider requiring at least one letter or `_` for the digit rule,
or excluding pure numbers of ≤ 4 digits. (`isIdentifierLike` is otherwise sane: `iPhone`, `GitHub`, `PostgreSQL`,
`macOS`, `v2`, `ErrNoRows` → identifier; `TODO`, `HTTP` → not.)

### L1 — Apostrophe fragments become query terms

`I'm`, `don't`, `we're`, `we'll`, `I've` → `don | doesn | re | ll | ve` (verified). `re` matches the parser's
`'re'` lexeme from `re-run`/`re-embed`; others are harmless dilution. Add them (and `am`, `all`, `them`, `here`, `like`,
`same`, `much`, `even`) to the stopword list, or drop fragments produced by splitting on `'`.

### L2 — `TestLongPromptIdentifierCasesAreWellFormed` certifies by substring, not by FTS

`internal/evalset/fixtures_test.go:109` checks `strings.Contains(record text, token)`, which is true for `100.64.0.0/10`
and `db.WithTx` even though neither can match through the tsquery (H1). The test therefore certifies two cases that do not
test what they claim. Make it tokenizer-aware (or move the check into the integration test by asserting the identifier
alone finds the record).

### L3 — Harness nits (`store_integration_test.go:1388-1413`)

- Works as documented (used throughout this review). Database names are hex-derived (safe); `DROP … WITH (FORCE)` needs PG ≥ 13 (fine for the documented pg16).
- If `url.Parse(adminDSN)` fails after `CREATE DATABASE`, the database leaks (no cleanup returned yet); parse before creating.
- Only URL-form DSNs are accepted (keyword/value DSNs pass `pgx.ParseConfig` but fail `url.Parse` with a clear message) — document in the Makefile comment.
- `New()` runs `CREATE EXTENSION vector`, so the admin role needs that privilege; the Makefile comment says "pgvector-enabled", which is sufficient.
- One admin connection is held per test for its duration — fine at 28 tests.

### L4 — Minor

- `isIdentifierLike` is evaluated on the first-seen casing of a term (`ftsquery.go:75`): `errnorows … ErrNoRows` → not an identifier. Negligible.
- `fixtures_test.go:107` uses the deprecated `strings.Title` (vet is silent; `golang.org/x/text/cases` or a manual check).
- The `fts_scored` CTE is referenced twice and is correctly materialized once (plan shows one `CTE fts_scored`, two `CTE Scan`s).

## What was verified empirically vs. read-only

Verified (local Postgres 16.14 / pgvector 0.6.0):
- Build, vet (both tag sets), `go test -race ./...`, full integration suite, `make test-integration` (with `-race`), 5× repeat of the service/FTS tests: all green, no flakes.
- tsquery safety: no SQL error or degraded fallback for unicode (`naïve café 日本語 Ωmega`), digits-only, `_`-only, double underscores (`'foo' <-> 'bar'`), 5000-char tokens (NOTICE "word is too long", dropped), 10 000 repeated words, SQL/tsquery operator payloads (stripped by the tokenizer), stopword-only prompts (NULL params: `to_tsquery(NULL)` is NULL, `@@`/`ts_rank` NULL → no rows, no error). `$3::text`/`$4::text` casts resolve the overloaded `to_tsquery` correctly with pgx NULLs. Every letter/digit/underscore-only term re-parses without syntax errors (operators cannot be produced from that alphabet); underscores yield phrase queries, which do match adjacent positions.
- ts_rank scaling and weights (0.608 / n; A:B:C = 5:2:1), the relative floor's neutrality to term count, and M1's shed behaviour.
- H1 (tokenizer divergence, old vs new behaviour per identifier), H2 (cap), M3 (digits), L1 (fragments), the "what drives the hit" breakdown for all four `long_identifier` cases, and the synthetic paraphrase experiment (M2).
- Performance, 5000 rows (1666 in scope, 4500 in namespace): hybrid 54 ms (vector sort over all in-scope rows ≈ 20 ms + two hash joins; pre-existing shape), full-text-only 9 ms, selective one-term 2.4 ms. GIN (`idx_records_tsvector_gin`) is used when the tsquery is selective (`erp`, phrase identifiers) and skipped in favour of `idx_records_namespace_repo` when the OR query matches most rows — correct planner behaviour, not a defect. For a generic 7-term prompt 1666 rows matched and 524 (31 %) passed the floor.
- Item 6 mutation testing (copy of repo): M1 `updated_at` in NOOP map, M1b in UPDATE map, M2 `superseded_by: ""`, M3 supersede row without namespace, M3b wrong namespace, M4 UpdateRecord skips re-embed on content change, M5 `WithTx` commits despite error, M6 promotion at 3, M7/M7b stale tsvector (tx and non-tx Update), M8 `superseded_by` never set, M9 UPDATE keeps stale embedding, M10 UPDATE drops files, M11 SUPERSEDE leaves status — **all killed** by the intended test. The three historical bugs are caught by `TestServiceNoopPromotesCandidateAtSecondRepeat`, `TestServiceSupersedeDeprecatesOldAndCreatesNew` (both the store guard and the namespace assertion fire).
- Isolation: one database per test; `fixedEmbedder` picks the longest matching title prefix, so Go map iteration order cannot change its output (no flake source); `calls` is only touched single-threaded.

Read-only (not executed):
- `claude-memory eval-retrieval` against real Ollama/bge-m3 (the only way to settle "no paraphrase regression").
- The Docker/testcontainers branch of `startPostgresContainer` (no Docker here; unchanged code).
- CI (`.github/workflows/ci.yml` already runs the integration job with Docker; unchanged by these commits).

## Item 5 acceptance — what is and is not proven

| Claim (README §5) | Status |
|---|---|
| OR-semantics tsquery, bound parameters, no injection path | Proven (reading + probes). |
| Identifier-term matches always count | Proven for `_`/camelCase/digit tokens; **false for dotted/slashed identifiers** (H1). |
| Relative floor instead of absolute | Proven correct w.r.t. term-count scaling; sheds rare content words (M1). |
| `long_prompt_identifier` recall@3 = 100 % (4 cases) | `TestLongPromptIdentifierRecall` passes, but cases 003 and 004 are found through ordinary words, not their identifier (verified by removing the identifier: same result; identifier alone: no result). Only 001 and 002 exercise the mechanism. Not a valid acceptance for the identifier half. |
| Identifiers after 32 distinct words | Not covered; fails (H2). |
| No paraphrase regression | Not verified (README says so). Synthetic experiment: no regression on the six eval paraphrases, with the caveat that each of them shares a title word with its answer. |

## Item 6 coverage — backlog bullets vs tests (`internal/postgres/service_integration_test.go`)

| Bullet | Test | Assertions strong enough? |
|---|---|---|
| inline NOOP: `seen_count++`, candidate→active at 2 | `TestServiceNoopPromotesCandidateAtSecondRepeat` (:107) | Yes: seen 0/candidate → 1/candidate → 2/active; same ID; decision NOOP. Killed M1, M6. |
| ExtractionDecision UPDATE | `TestServiceExtractionUpdateRewritesRecord` (:140) | Yes: title, content, files, status unchanged, FTS new/old content, embedding recomputed (cosine ≥ 0.999). Killed M1b, M7, M9, M10. |
| SUPERSEDE: old deprecated + `superseded_by`, new active, namespace | `TestServiceSupersedeDeprecatesOldAndCreatesNew` (:194) | Yes, plus search excludes the deprecated row. Killed M2, M3, M3b, M8, M11. |
| SUPERSEDE: one transaction, rollback on failure | `TestServiceSupersedeRollsBackOnFailure` (:245) | Yes (injected `Create` failure; old row untouched; row count 1). Killed M5. Only the Create step is failure-injected; the second `Update` (superseded_by) is not, but it runs in the same `WithTx`. |
| `UpdateRecord` content change re-embedded + FTS reflects | `TestServiceUpdateRecordContentReembedsAndReindexes` (:274) | Yes: exactly one embed call, stored vector matches, FTS new/old. Killed M4, M7b. |
| (extra) judgment band / ADD | `TestServiceAddVersusJudgmentBand` (:316) | Reasonable; comment "orthogonal-ish" for cosine 0.2 is loose but the numbers are right. |

## Blockers before item 5 can be marked DONE

1. Fix H1 (query-side tokenization must agree with the index parser) and H2 (identifier terms must survive the cap); extend `TestBuildFTSQueries` / `TestFullTextNaturalLanguagePrompts` accordingly, and make the four `long_identifier` cases prove the identifier (identifier alone must find the record).
2. Decide on M1/M3 (floor weight-neutrality, digits rule) with tests.
3. Run `claude-memory eval-retrieval` against real Ollama and record paraphrase/identifier recall in the README before using the word DONE.
