# Retrieval Evaluation Results

## Summary

- Seed records stored: 11/11
- Total query cases: 34
- Paraphrase cases: 14, hit@3: 14 (recall: 100.00%)
- Exact identifier cases: 5, hit@3: 5 (recall: 100.00%)
- Long-prompt identifier cases: 7, hit@3: 7 (recall: 100.00%) — PASS
- Negative cases: 6, top Similarity below hook threshold 0.50: 6 (precision: 100.00%)

## Similarity Distributions

**Positive (paraphrase + identifier) similarity scores** (Similarity of the RRF rank-1 record, which is not necessarily the expected one or the max-similarity one; the hook filters each record by its own Similarity):
- Min: 0.4336, Max: 0.7302, Mean: 0.6212
- P50: 0.6191, P95: 0.7227
**Negative (unrelated) similarity scores:**
- Min: 0.2743, Max: 0.4568, Mean: 0.3761
- P50: 0.4191, P95: 0.4568
**Near-duplicate-store similarity scores:**
- Min: 0.8004, Max: 0.8825, Mean: 0.8415 (n=2)

## Threshold Recommendations

**Hook similarity threshold (AC-32):** configured default 0.5000
- Negative Max: 0.4568, Positive P10: 0.4629
- Derived hook threshold: 0.4598 (midpoint of negative max and positive P10, clamped to [0.05, 0.95])

**Store thresholds (AC-15):** configured defaults ADD below 0.65, ASK 0.65-0.85, NOOP/UPDATE at/above 0.85
- Derived ASK floor: 0.4598 (same signal as the hook threshold above)
- Near-duplicate-store Similarity observed: min 0.8004 (n=2)
- Derived NOOP/UPDATE floor: 0.8004 (lowest observed near-duplicate similarity, clamped at/above the ASK floor)
Justification: derived from this run's measured positive/negative/near-duplicate Similarity distributions (see Similarity Distributions above); configured defaults are shown for comparison only, never overwritten automatically.

## Store Latency (AC-48)

- Min: 42.836292ms
- Max: 196.34925ms
- Mean: 123.775833ms
- P50: 122.328167ms
- P95: 196.34925ms
- Budget (AC-48): < 3s at p95 ✓ (measured: 196.34925ms)

## Detailed Case Results

**✓ paraphrase_001** (paraphrase)
- Query: Where should I open pull requests in billing-service?
- Repo scope: "billing-service"
- Top result (RRF rank 1): billing-service: Pull requests target master, not develop
- Similarity: 0.6962, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6962, Score 0.0328
  - #1 [active] Similarity 0.6962, Score 0.0328 — billing-service: Pull requests target master, not develop (expected)
  - #2 [active] Similarity 0.5200, Score 0.0323 — REFUNDED/REFUNDED is a terminal status in billing-service
  - #3 [candidate] Similarity 0.4440, Score 0.0159 — Azure DevOps work item retry logic: never use bare exponential backoff
**✓ paraphrase_002** (paraphrase)
- Query: How does the Kubernetes build pipeline handle branch names that are too long?
- Repo scope: ""
- Top result (RRF rank 1): Kubernetes label value 63-byte limit breaks long branch names
- Similarity: 0.7211, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.7211, Score 0.0328
  - #1 [active] Similarity 0.7211, Score 0.0328 — Kubernetes label value 63-byte limit breaks long branch names (expected)
  - #2 [candidate] Similarity 0.4593, Score 0.0161 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
  - #3 [active] Similarity 0.4534, Score 0.0159 — Tailscale CGNAT range is 100.64.0.0/10 for all installations
**✓ paraphrase_003** (paraphrase)
- Query: What happens when a payment is refunded in billing-service?
- Repo scope: "billing-service"
- Top result (RRF rank 1): REFUNDED/REFUNDED is a terminal status in billing-service
- Similarity: 0.6842, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6842, Score 0.0328
  - #1 [active] Similarity 0.6842, Score 0.0328 — REFUNDED/REFUNDED is a terminal status in billing-service (expected)
  - #2 [active] Similarity 0.5674, Score 0.0323 — billing-service: Pull requests target master, not develop
  - #3 [candidate] Similarity 0.4567, Score 0.0159 — Azure DevOps work item retry logic: never use bare exponential backoff
**✓ exact_identifier_001** (exact_identifier)
- Query: pg_advisory_xact_lock
- Repo scope: ""
- Top result (RRF rank 1): pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
- Similarity: 0.6996, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6996, Score 0.0328
  - #1 [candidate] Similarity 0.6996, Score 0.0328 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock (expected)
  - #2 [active] Similarity 0.4671, Score 0.0161 — Tailscale CGNAT range is 100.64.0.0/10 for all installations
  - #3 [candidate] Similarity 0.4225, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ exact_identifier_002** (exact_identifier)
- Query: num_batch truncation Ollama bge-m3
- Repo scope: ""
- Top result (RRF rank 1): Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
- Similarity: 0.7047, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.7047, Score 0.0328
  - #1 [candidate] Similarity 0.7047, Score 0.0328 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag (expected)
  - #2 [active] Similarity 0.5185, Score 0.0323 — Semantic memory: Ollama runs on the laptop, not the server
  - #3 [active] Similarity 0.3785, Score 0.0159 — Kubernetes label value 63-byte limit breaks long branch names
**✓ exact_identifier_003** (exact_identifier)
- Query: 100.64.0.0/10 Tailscale CGNAT
- Repo scope: ""
- Top result (RRF rank 1): Tailscale CGNAT range is 100.64.0.0/10 for all installations
- Similarity: 0.7176, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.7176, Score 0.0328
  - #1 [active] Similarity 0.7176, Score 0.0328 — Tailscale CGNAT range is 100.64.0.0/10 for all installations (expected)
  - #2 [candidate] Similarity 0.4086, Score 0.0161 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
  - #3 [active] Similarity 0.3791, Score 0.0159 — Kubernetes label value 63-byte limit breaks long branch names
**✓ exact_identifier_004** (exact_identifier)
- Query: bearer token Authorization scrubbing pattern
- Repo scope: ""
- Top result (RRF rank 1): Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
- Similarity: 0.5897, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.5897, Score 0.0328
  - #1 [candidate] Similarity 0.5897, Score 0.0328 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens (expected)
  - #2 [active] Similarity 0.4425, Score 0.0161 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [candidate] Similarity 0.4340, Score 0.0159 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
**✓ paraphrase_004** (paraphrase)
- Query: Why does the memory service run locally instead of on the server?
- Repo scope: ""
- Top result (RRF rank 1): Semantic memory: Ollama runs on the laptop, not the server
- Similarity: 0.5943, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.5943, Score 0.0328
  - #1 [active] Similarity 0.5943, Score 0.0328 — Semantic memory: Ollama runs on the laptop, not the server (expected)
  - #2 [active] Similarity 0.4112, Score 0.0318 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [candidate] Similarity 0.4158, Score 0.0313 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ paraphrase_005** (paraphrase)
- Query: How should I handle retries against Azure DevOps API endpoints?
- Repo scope: ""
- Top result (RRF rank 1): Azure DevOps work item retry logic: never use bare exponential backoff
- Similarity: 0.6732, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6732, Score 0.0328
  - #1 [candidate] Similarity 0.6732, Score 0.0328 — Azure DevOps work item retry logic: never use bare exponential backoff (expected)
  - #2 [active] Similarity 0.4940, Score 0.0323 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [candidate] Similarity 0.4569, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ paraphrase_006** (paraphrase)
- Query: What format are Claude Code session transcripts stored in?
- Repo scope: ""
- Top result (RRF rank 1): Claude Code session transcripts use JSON-lines format with message type field
- Similarity: 0.7227, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.7227, Score 0.0328
  - #1 [candidate] Similarity 0.7227, Score 0.0328 — Claude Code session transcripts use JSON-lines format with message type field (expected)
  - #2 [active] Similarity 0.4435, Score 0.0323 — Semantic memory: Ollama runs on the laptop, not the server
  - #3 [candidate] Similarity 0.3983, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ negative_001** (negative)
- Query: What is the best pizza topping for a Thursday dinner?
- Repo scope: ""
- Top result (RRF rank 1): Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
- Similarity: 0.3074, Score: 0.0164
**✓ negative_002** (negative)
- Query: How do I knit a sweater with Python?
- Repo scope: ""
- Top result (RRF rank 1): Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
- Similarity: 0.4191, Score: 0.0164
**✓ negative_003** (negative)
- Query: What is the capital of France?
- Repo scope: ""
- Top result (RRF rank 1): Semantic memory: Ollama runs on the laptop, not the server
- Similarity: 0.2743, Score: 0.0164
**✓ near_duplicate_001** (near_duplicate_store)
- Query: 
- Top result (RRF rank 1): billing-service: Pull requests target master, not develop
- Similarity: 0.8004, Score: 0.0164
- Decision: ASK (judgment range, not written)
**✓ near_duplicate_002** (near_duplicate_store)
- Query: 
- Top result (RRF rank 1): Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
- Similarity: 0.8825, Score: 0.0164
- Decision: NOOP
**✓ exact_identifier_005** (exact_identifier)
- Query: RRF Reciprocal Rank Fusion 1.0 / (60 + rank)
- Repo scope: ""
- Top result (RRF rank 1): RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection
- Similarity: 0.6168, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6168, Score 0.0328
  - #1 [active] Similarity 0.6168, Score 0.0328 — RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection (expected)
  - #2 [active] Similarity 0.4240, Score 0.0161 — Tailscale CGNAT range is 100.64.0.0/10 for all installations
  - #3 [candidate] Similarity 0.4154, Score 0.0159 — Azure DevOps work item retry logic: never use bare exponential backoff
**✓ long_identifier_001** (long_prompt_identifier)
- Query: I'm writing a Go helper that serializes concurrent writes for the same record and I'm wondering which lock call is safe with pooled connections, something like pg_advisory_xact_lock versus the session-level variant, what should I do here
- Repo scope: ""
- Top result (RRF rank 1): pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
- Similarity: 0.7017, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.7017, Score 0.0328
  - #1 [candidate] Similarity 0.7017, Score 0.0328 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock (expected)
  - #2 [active] Similarity 0.5025, Score 0.0161 — Tailscale CGNAT range is 100.64.0.0/10 for all installations
  - #3 [candidate] Similarity 0.4985, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ long_identifier_002** (long_prompt_identifier)
- Query: our embedding calls seem to silently drop the tail of long documents even though the model supports a much larger context window, could num_batch be the culprit in this setup
- Repo scope: ""
- Top result (RRF rank 1): Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
- Similarity: 0.6000, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6000, Score 0.0328
  - #1 [candidate] Similarity 0.6000, Score 0.0328 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag (expected)
  - #2 [active] Similarity 0.4743, Score 0.0161 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [active] Similarity 0.4609, Score 0.0159 — Semantic memory: Ollama runs on the laptop, not the server
**✓ long_identifier_003** (long_prompt_identifier)
- Query: for the firewall rules on the database host which source range do I need to allow so that every machine on the tailnet can connect, I vaguely remember 100.64.0.0/10 but I am not sure it applies to all of them
- Repo scope: ""
- Top result (RRF rank 1): Tailscale CGNAT range is 100.64.0.0/10 for all installations
- Similarity: 0.7302, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.7302, Score 0.0328
  - #1 [active] Similarity 0.7302, Score 0.0328 — Tailscale CGNAT range is 100.64.0.0/10 for all installations (expected)
  - #2 [active] Similarity 0.4940, Score 0.0161 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [active] Similarity 0.4632, Score 0.0159 — RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection
**✓ long_identifier_004** (long_prompt_identifier)
- Query: in the dedup path where we check candidates before inserting, where exactly should the lock acquisition live, inside db.WithTx or can it sit outside the transaction block
- Repo scope: ""
- Top result (RRF rank 1): pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
- Similarity: 0.6063, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6063, Score 0.0328
  - #1 [candidate] Similarity 0.6063, Score 0.0328 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock (expected)
  - #2 [candidate] Similarity 0.4379, Score 0.0161 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
  - #3 [active] Similarity 0.3927, Score 0.0159 — Kubernetes label value 63-byte limit breaks long branch names
**✓ paraphrase_007** (paraphrase)
- Query: Why must user-supplied search text never be spliced into the statement and what do we pass instead?
- Repo scope: ""
- Top result (RRF rank 1): RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection
- Similarity: 0.5152, Score: 0.0325
- Hit@3: true
- Expected record: rank 1, Similarity 0.5152, Score 0.0325
  - #1 [active] Similarity 0.5152, Score 0.0325 — RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection (expected)
  - #2 [candidate] Similarity 0.5215, Score 0.0323 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
  - #3 [candidate] Similarity 0.4253, Score 0.0315 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
**✓ paraphrase_008** (paraphrase)
- Query: Which network block should the database listener trust when only our private mesh VPN peers may reach it?
- Repo scope: ""
- Top result (RRF rank 1): Tailscale CGNAT range is 100.64.0.0/10 for all installations
- Similarity: 0.4629, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.4629, Score 0.0328
  - #1 [active] Similarity 0.4629, Score 0.0328 — Tailscale CGNAT range is 100.64.0.0/10 for all installations (expected)
  - #2 [candidate] Similarity 0.4314, Score 0.0320 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
  - #3 [candidate] Similarity 0.4112, Score 0.0310 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
**✓ paraphrase_009** (paraphrase)
- Query: How do we stop passwords and credentials from leaking into what gets saved long term?
- Repo scope: ""
- Top result (RRF rank 1): Kubernetes label value 63-byte limit breaks long branch names
- Similarity: 0.4336, Score: 0.0318
- Hit@3: true
- Expected record: rank 2, Similarity 0.6485, Score 0.0164
  - #1 [active] Similarity 0.4336, Score 0.0318 — Kubernetes label value 63-byte limit breaks long branch names
  - #2 [candidate] Similarity 0.6485, Score 0.0164 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens (expected)
  - #3 [candidate] Similarity 0.4445, Score 0.0161 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
**✓ paraphrase_010** (paraphrase)
- Query: Why is the embedding model hosted on my notebook and not on the home machine?
- Repo scope: ""
- Top result (RRF rank 1): Semantic memory: Ollama runs on the laptop, not the server
- Similarity: 0.5848, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.5848, Score 0.0328
  - #1 [active] Similarity 0.5848, Score 0.0328 — Semantic memory: Ollama runs on the laptop, not the server (expected)
  - #2 [candidate] Similarity 0.4556, Score 0.0323 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
  - #3 [active] Similarity 0.3974, Score 0.0159 — Kubernetes label value 63-byte limit breaks long branch names
**✓ confusable_001** (paraphrase)
- Query: Which machine should run the embedding model for the synchronous hook, and why?
- Repo scope: ""
- Top result (RRF rank 1): Semantic memory: Ollama runs on the laptop, not the server
- Similarity: 0.4557, Score: 0.0325
- Hit@3: true
- Expected record: rank 1, Similarity 0.4557, Score 0.0325
  - #1 [active] Similarity 0.4557, Score 0.0325 — Semantic memory: Ollama runs on the laptop, not the server (expected)
  - #2 [candidate] Similarity 0.4780, Score 0.0325 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
  - #3 [active] Similarity 0.3600, Score 0.0310 — Kubernetes label value 63-byte limit breaks long branch names
**✓ confusable_002** (paraphrase)
- Query: Why are my long inputs to the embedding model cut off before the end?
- Repo scope: ""
- Top result (RRF rank 1): Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
- Similarity: 0.6106, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6106, Score 0.0328
  - #1 [candidate] Similarity 0.6106, Score 0.0328 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag (expected)
  - #2 [active] Similarity 0.5026, Score 0.0161 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [candidate] Similarity 0.4673, Score 0.0159 — Azure DevOps work item retry logic: never use bare exponential backoff
**✓ confusable_003** (paraphrase)
- Query: How do I keep two concurrent writers from inserting the same duplicate record at once?
- Repo scope: ""
- Top result (RRF rank 1): pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
- Similarity: 0.5106, Score: 0.0325
- Hit@3: true
- Expected record: rank 1, Similarity 0.5106, Score 0.0325
  - #1 [candidate] Similarity 0.5106, Score 0.0325 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock (expected)
  - #2 [active] Similarity 0.5086, Score 0.0320 — RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection
  - #3 [candidate] Similarity 0.5237, Score 0.0164 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ confusable_004** (paraphrase)
- Query: How is the combined vector and keyword ranking score computed for search results?
- Repo scope: ""
- Top result (RRF rank 1): RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection
- Similarity: 0.6191, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6191, Score 0.0328
  - #1 [active] Similarity 0.6191, Score 0.0328 — RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection (expected)
  - #2 [active] Similarity 0.3791, Score 0.0161 — Kubernetes label value 63-byte limit breaks long branch names
  - #3 [candidate] Similarity 0.3616, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ negative_004** (negative)
- Query: How do I configure an nginx reverse proxy with upstream load balancing and SSL termination?
- Repo scope: ""
- Top result (RRF rank 1): Azure DevOps work item retry logic: never use bare exponential backoff
- Similarity: 0.4568, Score: 0.0328
**✓ negative_005** (negative)
- Query: How do I clean up subscriptions in a React useEffect cleanup function?
- Repo scope: ""
- Top result (RRF rank 1): pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
- Similarity: 0.4334, Score: 0.0325
**✓ negative_006** (negative)
- Query: How does Terraform state locking work with an S3 backend and DynamoDB?
- Repo scope: ""
- Top result (RRF rank 1): Azure DevOps work item retry logic: never use bare exponential backoff
- Similarity: 0.3656, Score: 0.0315
**✓ long_identifier_005** (long_prompt_identifier)
- Query: we are reviewing how the service persists notes from chat sessions and someone asked whether we redact things like AKIA access keys or ghp_ tokens before anything touches the database, where is that policy defined
- Repo scope: ""
- Top result (RRF rank 1): Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
- Similarity: 0.6473, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6473, Score 0.0328
  - #1 [candidate] Similarity 0.6473, Score 0.0328 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens (expected)
  - #2 [candidate] Similarity 0.5094, Score 0.0161 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
  - #3 [active] Similarity 0.4371, Score 0.0159 — RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection
**✓ long_identifier_006** (long_prompt_identifier)
- Query: when I paste the sql for the hybrid search into a review the reviewer keeps asking why we use pgx.Query with $1 placeholders instead of building the string by hand, can you remind me of the reasoning
- Repo scope: ""
- Top result (RRF rank 1): RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection
- Similarity: 0.5924, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.5924, Score 0.0328
  - #1 [active] Similarity 0.5924, Score 0.0328 — RRF (Reciprocal Rank Fusion) ranking requires parameterized queries to avoid SQL injection (expected)
  - #2 [candidate] Similarity 0.4997, Score 0.0161 — pgx advisory locks: always use transaction scope with pg_advisory_xact_lock
  - #3 [candidate] Similarity 0.4720, Score 0.0159 — Secret scrubbing before persistence: redact JWTs, API keys, bearer tokens
**✓ long_identifier_007** (long_prompt_identifier)
- Query: the pipeline for the billing repo failed again during the Setup k8s step with an error complaining about metadata.labels being longer than allowed, and I have no idea what is going wrong with the branch
- Repo scope: ""
- Top result (RRF rank 1): Kubernetes label value 63-byte limit breaks long branch names
- Similarity: 0.6614, Score: 0.0328
- Hit@3: true
- Expected record: rank 1, Similarity 0.6614, Score 0.0328
  - #1 [active] Similarity 0.6614, Score 0.0328 — Kubernetes label value 63-byte limit breaks long branch names (expected)
  - #2 [candidate] Similarity 0.4576, Score 0.0323 — Ollama num_batch truncation: input silently cut at 2048 tokens without the flag
  - #3 [active] Similarity 0.4497, Score 0.0159 — Tailscale CGNAT range is 100.64.0.0/10 for all installations
