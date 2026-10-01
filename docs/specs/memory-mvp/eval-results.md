# Retrieval Evaluation Results

## Summary

- Seed records stored: 11/11
- Total query cases: 16
- Paraphrase cases: 6, hit@3: 6 (recall: 100.00%)
- Exact identifier cases: 5, hit@3: 5 (recall: 100.00%)
- Negative cases: 3, top Similarity below hook threshold 0.50: 3 (precision: 100.00%)

## Similarity Distributions

**Positive (paraphrase + identifier) similarity scores:**
- Min: 0.5897, Max: 0.7227, Mean: 0.6676
- P50: 0.6913, P95: 0.7227
**Negative (unrelated) similarity scores:**
- Min: 0.2783, Max: 0.4191, Mean: 0.3349
- P50: 0.3074, P95: 0.4191
**Near-duplicate-store similarity scores:**
- Min: 0.7900, Max: 0.8825, Mean: 0.8362 (n=2)

## Threshold Recommendations

**Hook similarity threshold (AC-32):** configured default 0.5000
- Negative Max: 0.4191, Positive P10: 0.5966
- Derived hook threshold: 0.5078 (midpoint of negative max and positive P10, clamped to [0.05, 0.95])

**Store thresholds (AC-15):** configured defaults ADD below 0.65, ASK 0.65-0.85, NOOP/UPDATE at/above 0.85
- Derived ASK floor: 0.5078 (same signal as the hook threshold above)
- Near-duplicate-store Similarity observed: min 0.7900 (n=2)
- Derived NOOP/UPDATE floor: 0.7900 (lowest observed near-duplicate similarity, clamped at/above the ASK floor)
Justification: derived from this run's measured positive/negative/near-duplicate Similarity distributions (see Similarity Distributions above); configured defaults are shown for comparison only, never overwritten automatically.

## Store Latency (AC-48)

- Min: 27.043083ms
- Max: 159.900542ms
- Mean: 63.847541ms
- P50: 59.689917ms
- P95: 159.900542ms
- Budget (AC-48): < 3s at p95 ✓ (measured: 159.900542ms)

## Detailed Case Results

**✓ paraphrase_001** (paraphrase)
- Query: Where should I open pull requests in the billing-service service?
- Repo scope: "billing-service"
- Top result: billing-service: Pull requests target master, not develop
- Similarity: 0.6913, Score: 0.0164
- Hit@3: true
- Expected record: rank 1, Similarity 0.6913, Score 0.0164
  - #1 [active] Similarity 0.6913, Score 0.0164 — billing-service: Pull requests target master, not develop (expected)
  - #2 [active] Similarity 0.4979, Score 0.0161 — DECLINED/DECLINED is a terminal status in billing-service
  - #3 [candidate] Similarity 0.4498, Score 0.0159 — Azure DevOps work item retry logic: never use bare exponential backoff
**✓ paraphrase_002** (paraphrase)
- Query: How does the Kubernetes build pipeline handle branch names that are too long?
- Repo scope: ""
- Top result: Kubernetes label value 63-byte limit breaks long branch names
- Similarity: 0.7222, Score: 0.0164
- Hit@3: true
- Expected record: rank 1, Similarity 0.7222, Score 0.0164
  - #1 [active] Similarity 0.7222, Score 0.0164 — Kubernetes label value 63-byte limit breaks long branch names (expected)
  - #2 [candidate] Similarity 0.4593, Score 0.0161 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
  - #3 [active] Similarity 0.4534, Score 0.0159 — Tailscale CGNAT range is 100.64.0.0/10 for all installations
**✓ paraphrase_003** (paraphrase)
- Query: What happens when an order return is declined in billing-service?
- Repo scope: "billing-service"
- Top result: DECLINED/DECLINED is a terminal status in billing-service
- Similarity: 0.6095, Score: 0.0164
- Hit@3: true
- Expected record: rank 1, Similarity 0.6095, Score 0.0164
  - #1 [active] Similarity 0.6095, Score 0.0164 — DECLINED/DECLINED is a terminal status in billing-service (expected)
  - #2 [active] Similarity 0.5966, Score 0.0161 — billing-service: Pull requests target master, not develop
  - #3 [candidate] Similarity 0.4462, Score 0.0159 — Azure DevOps work item retry logic: never use bare exponential backoff
**✓ exact_identifier_001** (exact_identifier)
- Query: pg_advisory_xact_lock
- Repo scope: ""
- Top result: pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
- Similarity: 0.6996, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6996, Score 0.0328
  - #1 [candidate] Similarity 0.6996, Score 0.0328 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock (expected)
  - #2 [active] Similarity 0.4671, Score 0.0161 — Tailscale CGNAT range is 100.64.0.0/10 for all installations
  - #3 [candidate] Similarity 0.4225, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ exact_identifier_002** (exact_identifier)
- Query: num_batch truncation Ollama bge-m3
- Repo scope: ""
- Top result: Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
- Similarity: 0.7047, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.7047, Score 0.0328
  - #1 [candidate] Similarity 0.7047, Score 0.0328 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag (expected)
  - #2 [active] Similarity 0.5143, Score 0.0161 — Semantic memory: Ollama runs on the laptop, not the server
  - #3 [active] Similarity 0.3807, Score 0.0159 — Kubernetes label value 63-byte limit breaks long branch names
**✓ exact_identifier_003** (exact_identifier)
- Query: 100.64.0.0/10 Tailscale CGNAT
- Repo scope: ""
- Top result: Tailscale CGNAT range is 100.64.0.0/10 for all installations
- Similarity: 0.7176, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.7176, Score 0.0328
  - #1 [active] Similarity 0.7176, Score 0.0328 — Tailscale CGNAT range is 100.64.0.0/10 for all installations (expected)
  - #2 [candidate] Similarity 0.4086, Score 0.0161 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
  - #3 [active] Similarity 0.3792, Score 0.0159 — Kubernetes label value 63-byte limit breaks long branch names
**✓ exact_identifier_004** (exact_identifier)
- Query: bearer token Authorization scrubbing pattern
- Repo scope: ""
- Top result: Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
- Similarity: 0.5897, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.5897, Score 0.0328
  - #1 [candidate] Similarity 0.5897, Score 0.0328 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens (expected)
  - #2 [active] Similarity 0.4439, Score 0.0161 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [candidate] Similarity 0.4340, Score 0.0159 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
**✓ paraphrase_004** (paraphrase)
- Query: Why does the memory service run locally instead of on the server?
- Repo scope: ""
- Top result: Semantic memory: Ollama runs on the laptop, not the server
- Similarity: 0.5966, Score: 0.0164
- Hit@3: true
- Expected record: rank 1, Similarity 0.5966, Score 0.0164
  - #1 [active] Similarity 0.5966, Score 0.0164 — Semantic memory: Ollama runs on the laptop, not the server (expected)
  - #2 [candidate] Similarity 0.4269, Score 0.0161 — Azure DevOps work item retry logic: never use bare exponential backoff
  - #3 [candidate] Similarity 0.4158, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ paraphrase_005** (paraphrase)
- Query: How should I handle retries against Azure DevOps API endpoints?
- Repo scope: ""
- Top result: Azure DevOps work item retry logic: never use bare exponential backoff
- Similarity: 0.6732, Score: 0.0164
- Hit@3: true
- Expected record: rank 1, Similarity 0.6732, Score 0.0164
  - #1 [candidate] Similarity 0.6732, Score 0.0164 — Azure DevOps work item retry logic: never use bare exponential backoff (expected)
  - #2 [active] Similarity 0.4952, Score 0.0161 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [candidate] Similarity 0.4569, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ paraphrase_006** (paraphrase)
- Query: What format are Claude Code session transcripts stored in?
- Repo scope: ""
- Top result: Claude Code session transcripts use JSON-lines format with message type field
- Similarity: 0.7227, Score: 0.0164
- Hit@3: true
- Expected record: rank 1, Similarity 0.7227, Score 0.0164
  - #1 [candidate] Similarity 0.7227, Score 0.0164 — Claude Code session transcripts use JSON-lines format with message type field (expected)
  - #2 [active] Similarity 0.4447, Score 0.0161 — Semantic memory: Ollama runs on the laptop, not the server
  - #3 [candidate] Similarity 0.3983, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ negative_001** (negative)
- Query: What is the best pizza topping for a Thursday dinner?
- Repo scope: ""
- Top result: Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
- Similarity: 0.3074, Score: 0.0164
**✓ negative_002** (negative)
- Query: How do I knit a sweater with Python?
- Repo scope: ""
- Top result: Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
- Similarity: 0.4191, Score: 0.0164
**✓ negative_003** (negative)
- Query: What is the capital of France?
- Repo scope: ""
- Top result: Semantic memory: Ollama runs on the laptop, not the server
- Similarity: 0.2783, Score: 0.0164
**✓ near_duplicate_001** (near_duplicate_store)
- Query: 
- Top result: billing-service: Pull requests target master, not develop
- Similarity: 0.7900, Score: 0.0164
- Decision: ASK (judgment range, not written)
**✓ near_duplicate_002** (near_duplicate_store)
- Query: 
- Top result: Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
- Similarity: 0.8825, Score: 0.0164
- Decision: NOOP
**✓ exact_identifier_005** (exact_identifier)
- Query: RRF Reciprocal Rank Fusion 1.0 / (60 + rank)
- Repo scope: ""
- Top result: RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection
- Similarity: 0.6168, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6168, Score 0.0328
  - #1 [active] Similarity 0.6168, Score 0.0328 — RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection (expected)
  - #2 [active] Similarity 0.4240, Score 0.0161 — Tailscale CGNAT range is 100.64.0.0/10 for all installations
  - #3 [candidate] Similarity 0.4154, Score 0.0159 — Azure DevOps work item retry logic: never use bare exponential backoff
