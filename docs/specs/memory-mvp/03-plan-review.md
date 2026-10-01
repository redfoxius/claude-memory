# Plan Review — memory-mvp

- **Spec:** `01-spec.md` v0.2 (AC-1..AC-57, AC-41 withdrawn)
- **Plan:** `02-plan.md` (revision after review, 2026-10-01)
- **Runs:** A — session default model (Opus); B — Sonnet. Both read-only.
- **Verdict (both runs):** READY WITH RISKS → all must-fix items applied to
  the plan, see "Resolution". One spec-level decision is still open.

## Agreement between runs

| Finding | A | B | Resolution in 02-plan.md |
|---|---|---|---|
| Wrong citation `01-spec.md:735-737` for parameterized queries | ✓ | ✓ | → `01-spec.md:847-850` |
| AC-48 claimed by WI-15 but never tested there | ✓ | ✓ | removed from WI-15; service overhead check in WI-3b, real-stack p95 in WI-07 |
| AC-3 tested in WI-02 but claimed only by WI-01 | ✓ | ✓ | WI-02 now owns AC-3; WI-01 marked "config values only" |
| `security/SKILL.md:79` "require TLS" silently not followed | ✓ | ✓ | explicit "conscious deviation" bullet in Architectural Constraints |

## Disagreements (found by one run only)

| Finding | Run | Assessment | Resolution |
|---|---|---|---|
| WI-03 too coarse (18 ACs, one acceptance block) | B | valid — sizing risk for implementer | split into 3a (ports/search/CRUD), 3b (write path), 3c (lifecycle) |
| AC-7 degrade-decision owner unclear (WI-05 text points to WI-03, WI-03 didn't cite it) | A | valid | WI-3a owns decision, WI-05 owns query |
| AC-21 owner ambiguous between WI-12 (Go) and WI-16 (`session-end.sh`) | A | valid — spec Verify text measures the hook script | shared: WI-12 Go-side detach/gating, WI-16 script exit < 100ms |
| JWT / bearer patterns not in cited skill lines | A | valid | WI-04 adds both patterns explicitly |
| WI-16 `depends on` omits WI-08 while DAG shows it | A | valid | dependency added |
| Ollama `truncate: true` keeping *leading* content is unverified | A | valid — checked empirically | verified; **uncovered a bigger issue**, see below |

Run B missed five items Run A found; Run A did not flag the WI-03 sizing
that B did. Neither run produced a finding the other contradicted.

## New finding from verifying run A's Ollama assumption (2026-10-01)

Measured on the laptop (Ollama 0.35, M1 Pro):
- truncation keeps leading tokens — AC-55's ordering (`title + tags +
  content`) works as intended;
- **effective cap is `num_batch` (default 2048), not 8192** — without
  `options.num_batch` every input is silently cut at 2048 tokens;
- cost: 2048 tokens 0.6s · 4096 3.4s · 8192 8.0s.

8192 tokens per write (8.0s) breaks AC-48 (< 3s p95). **Open spec
decision:** set `MEMORY_EMBED_MAX_TOKENS` default to 2048 and reword
AC-55/AC-56 from "8192-token model limit" to "configured embedding
token limit (default 2048)". Full content stays in the full-text index
either way. Plan WI-06 already passes the value as `num_ctx`/`num_batch`.

## Remaining non-blocking notes
- AC-30 300ms and the AC-15/AC-32 thresholds stay `[NEEDS CLARIFICATION]`
  defaults, tuned via WI-07.
