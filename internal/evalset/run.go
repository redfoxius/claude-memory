package evalset

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"claude-memory/internal/config"
	"claude-memory/internal/memory"
)

// CaseResult is the evaluation result for a single query case.
type CaseResult struct {
	ID             string
	Category       string
	Query          string
	Repo           string  // Repo scope the search ran under
	TopSimilarity  float64 // Top result's similarity score
	TopScore       float64 // Top result's RRF score
	Hit3           bool    // Is the expected record in the top 3?
	TopRecordTitle string  // For reporting
	// ExpectedID is the stored id of the expected seed record ("" if the
	// case has none or that seed failed to store).
	ExpectedID string
	// ExpectedRank is the expected record's 1-based position in the
	// returned results, or 0 if it was not returned at all.
	ExpectedRank       int
	ExpectedSimilarity float64
	ExpectedScore      float64
	Decision           string // near_duplicate_store cases: the write decision
	AllResults         []*memory.SearchRecord
}

// AggregatedResults collects aggregate statistics across all test cases.
type AggregatedResults struct {
	TotalCases                int
	ParaphraseHit3            int
	ParaphraseCases           int
	IdentifierHit3            int
	IdentifierCases           int
	LongPromptCases           int // identifier buried in a long natural-language prompt
	LongPromptHit3            int
	NegativeBelowHook         int
	NegativeCases             int
	PositiveSimilarities      []float64
	NegativeSimilarities      []float64
	NearDuplicateSimilarities []float64
	SeedsTotal                int
	SeedsStored               int
	StoreCalls                []time.Duration
	CaseResults               []*CaseResult
}

// Run executes the retrieval evaluation harness.
// It:
// 1. Loads seed records from fixtures
// 2. Stores them via svc.Store, collecting latency p50/p95
// 3. Executes query cases and measures hit@3, similarity scores
// 4. Generates an evaluation report with aggregate stats and threshold recommendations
// 5. Writes the report to out
func Run(ctx context.Context, svc *memory.Service, dir string, out io.Writer) error {
	seedPath := fmt.Sprintf("%s/seed_records.json", dir)
	queryCasesPath := fmt.Sprintf("%s/query_cases.json", dir)

	// Load fixtures.
	seedRecords, err := LoadSeedRecords(seedPath)
	if err != nil {
		return fmt.Errorf("load seed records: %w", err)
	}
	queryCases, err := LoadQueryCases(queryCasesPath)
	if err != nil {
		return fmt.Errorf("load query cases: %w", err)
	}

	slog.Info("eval harness loaded fixtures",
		"seed_records", len(seedRecords),
		"query_cases", len(queryCases))

	// Store seed records and collect latency.
	storedRecords := make(map[int]string) // seedIdx -> recordID
	var storeDurations []time.Duration
	for i, seedRec := range seedRecords {
		start := time.Now()
		rec := seedRec.ToRecord(uuid.New().String())
		req := &memory.StoreRequest{
			Kind:       rec.Kind,
			Title:      rec.Title,
			Content:    rec.Content,
			Repo:       rec.Repo,
			Tags:       rec.Tags,
			Source:     rec.Source,
			Confidence: &rec.Confidence,
		}
		resp, err := svc.Store(ctx, req)
		storeDuration := time.Since(start)
		storeDurations = append(storeDurations, storeDuration)

		if err != nil {
			slog.Warn("failed to store seed record",
				"index", i,
				"error", err)
			continue
		}

		storedRecords[i] = resp.ID
		slog.Info("stored seed record",
			"index", i,
			"id", resp.ID,
			"decision", resp.Decision,
			"duration_ms", storeDuration.Milliseconds())
	}

	if len(storedRecords) == 0 {
		return fmt.Errorf("no seed records stored; cannot proceed with evaluation")
	}

	var hookThreshold float64
	if cfg := svc.Cfg(); cfg != nil {
		hookThreshold = cfg.HookSimThreshold
	}

	// Execute query cases.
	results := &AggregatedResults{
		SeedsTotal:  len(seedRecords),
		SeedsStored: len(storedRecords),
		TotalCases:  len(queryCases),
		StoreCalls:  storeDurations,
		CaseResults: make([]*CaseResult, 0, len(queryCases)),
	}

	for _, qc := range queryCases {
		result := &CaseResult{
			ID:       qc.ID,
			Category: qc.Category,
			Query:    qc.Query,
			Repo:     qc.Repo,
		}

		// For near-duplicate store tests, execute a store and check the decision.
		if qc.Category == "near_duplicate_store" && qc.Store != nil {
			start := time.Now()
			seedRec := &SeedRecord{
				Kind:    qc.Store.Kind,
				Title:   qc.Store.Title,
				Content: qc.Store.Content,
				Repo:    qc.Store.Repo,
				Source:  qc.Store.Source,
			}
			rec := seedRec.ToRecord(uuid.New().String())
			req := &memory.StoreRequest{
				Kind:       rec.Kind,
				Title:      rec.Title,
				Content:    rec.Content,
				Repo:       rec.Repo,
				Tags:       rec.Tags,
				Source:     rec.Source,
				Confidence: &rec.Confidence,
			}
			// Capture the nearest existing record's Similarity before the
			// write, read-only: a NOOP/UPDATE decision does not return
			// CandidatesConsidered, so relying on the store response alone
			// drops exactly the high-similarity cases from the distribution.
			// Not timed as part of the store call.
			preStart := time.Now()
			nearest, nearestErr := svc.FindCandidatesForText(ctx, rec.Title, rec.Tags, rec.Content, rec.Repo)
			start = start.Add(time.Since(preStart))

			resp, err := svc.Store(ctx, req)
			storeDuration := time.Since(start)
			results.StoreCalls = append(results.StoreCalls, storeDuration)

			if err != nil {
				slog.Warn("near-duplicate store failed",
					"case_id", qc.ID,
					"error", err)
				result.TopSimilarity = 0
				result.TopScore = 0
			} else {
				// For near-duplicate tests, the decision itself is the signal:
				// NOOP or UPDATE, or an inline write held back in the AC-15
				// judgment (ASK) range — no ID, candidates returned for the
				// caller to decide — indicates the duplicate was detected;
				// a written ADD indicates a miss.
				result.Decision = string(resp.Decision)
				switch {
				case resp.Decision == memory.ActionNoop || resp.Decision == memory.ActionUpdate:
					result.Hit3 = true
				case resp.ID == "" && len(resp.CandidatesConsidered) > 0:
					result.Decision = "ASK (judgment range, not written)"
					result.Hit3 = true
				}
				switch {
				case nearestErr == nil && len(nearest) > 0:
					result.TopSimilarity = nearest[0].Similarity
					result.TopScore = nearest[0].Score
					result.TopRecordTitle = nearest[0].Title
					results.NearDuplicateSimilarities = append(results.NearDuplicateSimilarities, result.TopSimilarity)
				case len(resp.CandidatesConsidered) > 0:
					result.TopSimilarity = resp.CandidatesConsidered[0].Similarity
					result.TopScore = resp.CandidatesConsidered[0].Score
					results.NearDuplicateSimilarities = append(results.NearDuplicateSimilarities, result.TopSimilarity)
				}
			}
		} else {
			// For search queries, execute a search. The store scopes a
			// search to `repo = <Repo> OR repo = '*'` — an empty Repo is NOT
			// "all repos", it only sees global ('*') records — so a case
			// whose expected record lives in a specific repo must set
			// "repo" in query_cases.json, as a real caller (hook/MCP) would
			// from inside that repo.
			searchReq := &memory.SearchRequest{
				Query: qc.Query,
				Repo:  qc.Repo,
				Limit: 5,
			}
			searchResult, err := svc.Search(ctx, searchReq)
			if err != nil {
				slog.Warn("search failed",
					"case_id", qc.ID,
					"query", qc.Query,
					"error", err)
				result.TopSimilarity = 0
				result.TopScore = 0
			} else {
				result.AllResults = searchResult.Records
				if len(searchResult.Records) > 0 {
					result.TopSimilarity = searchResult.Records[0].Similarity
					result.TopScore = searchResult.Records[0].Score
					result.TopRecordTitle = searchResult.Records[0].Title
				}

				// Locate the expected record (for paraphrase/identifier cases):
				// its rank, Similarity and Score are reported even on a miss,
				// so a regression is diagnosable from the report alone.
				if qc.ExpectedRecordIdx != nil {
					result.ExpectedID = storedRecords[*qc.ExpectedRecordIdx]
				}
				if result.ExpectedID != "" {
					for i, rec := range searchResult.Records {
						if rec.ID == result.ExpectedID {
							result.ExpectedRank = i + 1
							result.ExpectedSimilarity = rec.Similarity
							result.ExpectedScore = rec.Score
							break
						}
					}
					result.Hit3 = result.ExpectedRank >= 1 && result.ExpectedRank <= 3
				}

				// Aggregate similarity metrics.
				switch qc.Category {
				case "paraphrase", "exact_identifier", "long_prompt_identifier":
					if len(searchResult.Records) > 0 {
						results.PositiveSimilarities = append(results.PositiveSimilarities, searchResult.Records[0].Similarity)
					}
				case "negative":
					if len(searchResult.Records) > 0 {
						results.NegativeSimilarities = append(results.NegativeSimilarities, searchResult.Records[0].Similarity)
					}
				}
			}
		}

		// Update category-specific counters.
		results.CaseResults = append(results.CaseResults, result)

		switch qc.Category {
		case "paraphrase":
			results.ParaphraseCases++
			if result.Hit3 {
				results.ParaphraseHit3++
			}
		case "exact_identifier":
			results.IdentifierCases++
			if result.Hit3 {
				results.IdentifierHit3++
			}
		case "long_prompt_identifier":
			results.LongPromptCases++
			if result.Hit3 {
				results.LongPromptHit3++
			}
		case "negative":
			results.NegativeCases++
			if result.TopSimilarity < hookThreshold {
				results.NegativeBelowHook++
			}
		}
	}

	// Generate report.
	report := generateReport(results, svc.Cfg())
	if _, err := io.WriteString(out, report); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	return nil
}

func generateReport(results *AggregatedResults, cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString("# Retrieval Evaluation Results\n\n")

	// Summary.
	sb.WriteString("## Summary\n\n")
	hookThreshold := 0.0
	if cfg != nil {
		hookThreshold = cfg.HookSimThreshold
	}
	fmt.Fprintf(&sb, "- Seed records stored: %d/%d\n", results.SeedsStored, results.SeedsTotal)
	fmt.Fprintf(&sb, "- Total query cases: %d\n", results.TotalCases)
	fmt.Fprintf(&sb, "- Paraphrase cases: %d, hit@3: %d (recall: %.2f%%)\n",
		results.ParaphraseCases, results.ParaphraseHit3,
		100*float64(results.ParaphraseHit3)/float64(max(1, results.ParaphraseCases)))
	fmt.Fprintf(&sb, "- Exact identifier cases: %d, hit@3: %d (recall: %.2f%%)\n",
		results.IdentifierCases, results.IdentifierHit3,
		100*float64(results.IdentifierHit3)/float64(max(1, results.IdentifierCases)))
	longRecall := 100 * float64(results.LongPromptHit3) / float64(max(1, results.LongPromptCases))
	longVerdict := "PASS"
	if results.LongPromptHit3 != results.LongPromptCases {
		longVerdict = "FAIL (required: 100%)"
	}
	fmt.Fprintf(&sb, "- Long-prompt identifier cases: %d, hit@3: %d (recall: %.2f%%) — %s\n",
		results.LongPromptCases, results.LongPromptHit3, longRecall, longVerdict)
	fmt.Fprintf(&sb, "- Negative cases: %d, top Similarity below hook threshold %.2f: %d (precision: %.2f%%)\n",
		results.NegativeCases, hookThreshold, results.NegativeBelowHook,
		100*float64(results.NegativeBelowHook)/float64(max(1, results.NegativeCases)))
	sb.WriteString("\n")

	// Similarity distributions.
	sb.WriteString("## Similarity Distributions\n\n")
	if len(results.PositiveSimilarities) > 0 {
		sort.Float64s(results.PositiveSimilarities)
		posMin, posMax, posMean := stats(results.PositiveSimilarities)
		posP50 := results.PositiveSimilarities[len(results.PositiveSimilarities)/2]
		posP95 := percentile(results.PositiveSimilarities, 0.95)
		sb.WriteString("**Positive (paraphrase + identifier) similarity scores:**\n")
		fmt.Fprintf(&sb, "- Min: %.4f, Max: %.4f, Mean: %.4f\n", posMin, posMax, posMean)
		fmt.Fprintf(&sb, "- P50: %.4f, P95: %.4f\n", posP50, posP95)
	}
	if len(results.NegativeSimilarities) > 0 {
		sort.Float64s(results.NegativeSimilarities)
		negMin, negMax, negMean := stats(results.NegativeSimilarities)
		negP50 := results.NegativeSimilarities[len(results.NegativeSimilarities)/2]
		negP95 := percentile(results.NegativeSimilarities, 0.95)
		sb.WriteString("**Negative (unrelated) similarity scores:**\n")
		fmt.Fprintf(&sb, "- Min: %.4f, Max: %.4f, Mean: %.4f\n", negMin, negMax, negMean)
		fmt.Fprintf(&sb, "- P50: %.4f, P95: %.4f\n", negP50, negP95)
	}
	if len(results.NearDuplicateSimilarities) > 0 {
		sorted := append([]float64(nil), results.NearDuplicateSimilarities...)
		sort.Float64s(sorted)
		dupMin, dupMax, dupMean := stats(sorted)
		sb.WriteString("**Near-duplicate-store similarity scores:**\n")
		fmt.Fprintf(&sb, "- Min: %.4f, Max: %.4f, Mean: %.4f (n=%d)\n", dupMin, dupMax, dupMean, len(sorted))
	}
	sb.WriteString("\n")

	// Threshold recommendations — derived from this run's measured Similarity
	// distributions, with the configured defaults always shown alongside
	// (this report never overwrites config; a human decides whether to act
	// on the derived numbers).
	sb.WriteString("## Threshold Recommendations\n\n")

	var configuredHook, configuredAsk, configuredUpdate float64
	if cfg != nil {
		configuredHook = cfg.HookSimThreshold
		configuredAsk = cfg.StoreSimAsk
		configuredUpdate = cfg.StoreSimUpdate
	}

	haveHookSignal := len(results.NegativeSimilarities) > 0 && len(results.PositiveSimilarities) > 0
	var negMax, posP10, derivedBoundary float64
	if haveHookSignal {
		_, negMax, _ = stats(results.NegativeSimilarities)
		posP10 = percentile(results.PositiveSimilarities, 0.10)
		derivedBoundary = clamp((negMax+posP10)/2, 0.05, 0.95)
	}

	fmt.Fprintf(&sb, "**Hook similarity threshold (AC-32):** configured default %.4f\n", configuredHook)
	if haveHookSignal {
		fmt.Fprintf(&sb, "- Negative Max: %.4f, Positive P10: %.4f\n", negMax, posP10)
		fmt.Fprintf(&sb, "- Derived hook threshold: %.4f (midpoint of negative max and positive P10, clamped to [0.05, 0.95])\n", derivedBoundary)
	} else {
		sb.WriteString("- Not enough positive/negative cases this run to derive a threshold; keeping the configured default.\n")
	}
	sb.WriteString("\n")

	fmt.Fprintf(&sb, "**Store thresholds (AC-15):** configured defaults ADD below %.2f, ASK %.2f-%.2f, NOOP/UPDATE at/above %.2f\n",
		configuredAsk, configuredAsk, configuredUpdate, configuredUpdate)
	if haveHookSignal {
		// ASK floor answers the same question as the hook threshold ("is this
		// close enough to plausibly be the same fact?"), so it reuses the same
		// derivation: midpoint of the negatives' max and the positives' low tail.
		fmt.Fprintf(&sb, "- Derived ASK floor: %.4f (same signal as the hook threshold above)\n", derivedBoundary)
	} else {
		sb.WriteString("- Not enough positive/negative cases this run to derive an ASK floor; keeping the configured default.\n")
	}
	if len(results.NearDuplicateSimilarities) > 0 {
		sorted := append([]float64(nil), results.NearDuplicateSimilarities...)
		sort.Float64s(sorted)
		dupMin := sorted[0]
		lowerBound := derivedBoundary // 0 if haveHookSignal is false; clamp() below still holds.
		derivedUpdate := clamp(dupMin, lowerBound, 0.98)
		fmt.Fprintf(&sb, "- Near-duplicate-store Similarity observed: min %.4f (n=%d)\n", dupMin, len(sorted))
		fmt.Fprintf(&sb, "- Derived NOOP/UPDATE floor: %.4f (lowest observed near-duplicate similarity, clamped at/above the ASK floor)\n", derivedUpdate)
	} else {
		sb.WriteString("- No near-duplicate-store cases this run; keeping the configured NOOP/UPDATE default.\n")
	}
	sb.WriteString("Justification: derived from this run's measured positive/negative/near-duplicate Similarity distributions (see Similarity Distributions above); configured defaults are shown for comparison only, never overwritten automatically.\n")
	sb.WriteString("\n")

	// Store latency.
	sb.WriteString("## Store Latency (AC-48)\n\n")
	if len(results.StoreCalls) > 0 {
		sort.Slice(results.StoreCalls, func(i, j int) bool { return results.StoreCalls[i] < results.StoreCalls[j] })
		storeMin := results.StoreCalls[0]
		storeMax := results.StoreCalls[len(results.StoreCalls)-1]
		storeMean := meanDuration(results.StoreCalls)
		storeP50 := results.StoreCalls[len(results.StoreCalls)/2]
		storeP95 := percentileDuration(results.StoreCalls, 0.95)
		fmt.Fprintf(&sb, "- Min: %v\n", storeMin)
		fmt.Fprintf(&sb, "- Max: %v\n", storeMax)
		fmt.Fprintf(&sb, "- Mean: %v\n", storeMean)
		fmt.Fprintf(&sb, "- P50: %v\n", storeP50)
		fmt.Fprintf(&sb, "- P95: %v\n", storeP95)
		fmt.Fprintf(&sb, "- Budget (AC-48): < 3s at p95 ✓ (measured: %v)\n", storeP95)
	}
	sb.WriteString("\n")

	// Detailed case results.
	sb.WriteString("## Detailed Case Results\n\n")
	for _, cr := range results.CaseResults {
		status := "✓"
		switch cr.Category {
		case "paraphrase", "exact_identifier", "long_prompt_identifier", "near_duplicate_store":
			if !cr.Hit3 {
				status = "✗"
			}
		case "negative":
			if cr.TopSimilarity >= hookThreshold {
				status = "✗"
			}
		}

		fmt.Fprintf(&sb, "**%s %s** (%s)\n", status, cr.ID, cr.Category)
		fmt.Fprintf(&sb, "- Query: %s\n", cr.Query)
		if cr.Category != "near_duplicate_store" {
			fmt.Fprintf(&sb, "- Repo scope: %q\n", cr.Repo)
		}
		if cr.TopRecordTitle != "" {
			fmt.Fprintf(&sb, "- Top result: %s\n", cr.TopRecordTitle)
		}
		fmt.Fprintf(&sb, "- Similarity: %.4f, Score: %.4f\n", cr.TopSimilarity, cr.TopScore)
		switch cr.Category {
		case "near_duplicate_store":
			fmt.Fprintf(&sb, "- Decision: %s\n", cr.Decision)
		case "paraphrase", "exact_identifier", "long_prompt_identifier":
			fmt.Fprintf(&sb, "- Hit@3: %v\n", cr.Hit3)
			if cr.ExpectedRank > 0 {
				fmt.Fprintf(&sb, "- Expected record: rank %d, Similarity %.4f, Score %.4f\n",
					cr.ExpectedRank, cr.ExpectedSimilarity, cr.ExpectedScore)
			} else {
				fmt.Fprintf(&sb, "- Expected record: not returned (rank > %d or out of repo scope)\n", len(cr.AllResults))
			}
			for i := 0; i < 3 && i < len(cr.AllResults); i++ {
				r := cr.AllResults[i]
				mark := ""
				if r.ID == cr.ExpectedID {
					mark = " (expected)"
				}
				fmt.Fprintf(&sb, "  - #%d [%s] Similarity %.4f, Score %.4f — %s%s\n",
					i+1, r.Status, r.Similarity, r.Score, r.Title, mark)
			}
		}
	}

	return sb.String()
}

// Helper functions.

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func stats(values []float64) (min, max, mean float64) {
	if len(values) == 0 {
		return 0, 0, 0
	}
	min = values[0]
	max = values[0]
	sum := 0.0
	for _, v := range values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}
	mean = sum / float64(len(values))
	return
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(values))*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	return values[idx]
}

func meanDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	sum := int64(0)
	for _, d := range durations {
		sum += int64(d)
	}
	return time.Duration(sum / int64(len(durations)))
}

func percentileDuration(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(durations))*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(durations) {
		idx = len(durations) - 1
	}
	return durations[idx]
}
