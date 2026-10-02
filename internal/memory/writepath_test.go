package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"claude-memory/internal/config"
	"claude-memory/internal/record"
)

// writepathCfg returns a config matching the spec v0.5 defaults (AC-15):
// 0.65 (ask) / 0.85 (update/NOOP) cosine-similarity thresholds.
func writepathCfg() *config.Config {
	return &config.Config{
		MaxContentChars: 20000,
		StoreSimAsk:     0.65,
		StoreSimUpdate:  0.85,
		EmbedMaxTokens:  2048,
	}
}

func baseStoreRequest() *StoreRequest {
	return &StoreRequest{
		Kind:    record.KindGotcha,
		Title:   "Some title",
		Content: "Some content",
		Repo:    "billing-service",
		Source:  record.SourceInline,
	}
}

// --- AC-15: inline threshold boundaries (exactly 0.65 / 0.85, Similarity not Score) ---

func TestStore_Inline_BelowAskThreshold_ADD(t *testing.T) {
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: "cand-1", Title: "Existing", Similarity: 0.64999, Score: 0.9}}, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			r.ID = "new-id"
			return r, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	resp, err := svc.Store(context.Background(), baseStoreRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != ActionAdd {
		t.Errorf("similarity 0.64999 (just below ask threshold): decision = %s, want ADD", resp.Decision)
	}
	if resp.ID != "new-id" {
		t.Errorf("expected a new record to be created, got id %q", resp.ID)
	}
}

func TestStore_Inline_ExactlyAtAskThreshold_ReturnsCandidatesWithoutWriting(t *testing.T) {
	var createCalled, updateCalled bool
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: "cand-1", Title: "Existing", Similarity: 0.65, Score: 0.1}}, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			createCalled = true
			return r, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			updateCalled = true
			return nil, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	resp, err := svc.Store(context.Background(), baseStoreRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "" {
		t.Errorf("exactly at ask threshold (0.65): expected no record written (empty ID), got %q", resp.ID)
	}
	if len(resp.CandidatesConsidered) != 1 || resp.CandidatesConsidered[0].ID != "cand-1" {
		t.Errorf("expected the judgment-range candidates to be returned, got %+v", resp.CandidatesConsidered)
	}
	if createCalled || updateCalled {
		t.Error("exactly at ask threshold must not write anything; caller is expected to judge and re-call")
	}
}

// Regression: the inline follow-up call carrying the caller's decision used
// to hit the judgment-range early exit again, so it could never be stored.
func TestStore_Inline_JudgmentRangeWithExplicitDecision_Writes(t *testing.T) {
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: "cand-1", Title: "Existing", Similarity: 0.70, Score: 0.1}}, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			r.ID = "new-id"
			return r, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	req := baseStoreRequest()
	req.ExtractionDecision = &ExtractionDecision{Action: ActionAdd}
	resp, err := svc.Store(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != ActionAdd || resp.ID != "new-id" {
		t.Errorf("judgment range + explicit ADD: got decision %s id %q, want ADD new-id", resp.Decision, resp.ID)
	}
}

func TestStore_Inline_JustBelowUpdateThreshold_StillAskRange(t *testing.T) {
	var createCalled bool
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: "cand-1", Title: "Existing", Similarity: 0.84999}}, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			createCalled = true
			return r, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	resp, err := svc.Store(context.Background(), baseStoreRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "" || createCalled {
		t.Errorf("similarity 0.84999 is still inside the ask range and must not write; got ID=%q", resp.ID)
	}
}

func TestStore_Inline_ExactlyAtUpdateThreshold_NOOP(t *testing.T) {
	var updateCalls []map[string]interface{}
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: "cand-1", Title: "Existing", Similarity: 0.85, Score: 0.01}}, nil
		},
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive, SeenCount: 3}, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			updateCalls = append(updateCalls, updates)
			return &record.Record{ID: id}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	resp, err := svc.Store(context.Background(), baseStoreRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != ActionNoop {
		t.Errorf("exactly at update threshold (0.85): decision = %s, want NOOP", resp.Decision)
	}
	if resp.ID != "cand-1" {
		t.Errorf("NOOP should target the existing candidate, got ID=%q", resp.ID)
	}
	if len(updateCalls) != 1 {
		t.Fatalf("expected exactly one Update call for NOOP, got %d", len(updateCalls))
	}
	if sc, _ := updateCalls[0]["seen_count"].(int); sc != 4 {
		t.Errorf("expected seen_count incremented to 4, got %v", updateCalls[0]["seen_count"])
	}
	// The Store adapter owns updated_at and rejects it as an update
	// column (postgres Update whitelist); passing it made every real NOOP
	// fail with `column "updated_at" not allowed`.
	if _, has := updateCalls[0]["updated_at"]; has {
		t.Errorf("NOOP update must not include updated_at (adapter-owned), got %v", updateCalls[0])
	}
}

// TestStore_Inline_DecisionUsesSimilarityNotScore is a regression test
// pinning AC-15's explicit requirement that the dedup threshold compares
// against Candidate.Similarity (cosine similarity), never Candidate.Score
// (the fused RRF rank score, ordering-only).
func TestStore_Inline_DecisionUsesSimilarityNotScore(t *testing.T) {
	// High fused Score but low Similarity: must be treated as unrelated (ADD).
	t.Run("high score, low similarity => ADD", func(t *testing.T) {
		var createCalled bool
		store := &mockStore{
			FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
				return []*Candidate{{ID: "cand-1", Score: 0.99, Similarity: 0.1}}, nil
			},
			CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
				createCalled = true
				r.ID = "new-id"
				return r, nil
			},
		}
		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

		resp, err := svc.Store(context.Background(), baseStoreRequest())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Decision != ActionAdd || !createCalled {
			t.Errorf("expected ADD when Similarity is low regardless of Score, got decision=%s", resp.Decision)
		}
	})

	// Low fused Score but high Similarity: must still resolve to NOOP.
	t.Run("low score, high similarity => NOOP", func(t *testing.T) {
		store := &mockStore{
			FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
				return []*Candidate{{ID: "cand-1", Score: 0.001, Similarity: 0.95}}, nil
			},
			GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
				return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive, SeenCount: 0}, nil
			},
			UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
				return &record.Record{ID: id}, nil
			},
		}
		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

		resp, err := svc.Store(context.Background(), baseStoreRequest())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Decision != ActionNoop {
			t.Errorf("expected NOOP when Similarity is high regardless of Score, got decision=%s", resp.Decision)
		}
	})
}

// --- AC-14: session/pr ExtractionDecision target validated against fresh top-5 ---

func TestStore_ExtractionDecision_TargetNotInFreshTop5_FallsBackToADD(t *testing.T) {
	staleID := "stale-id-not-offered-anymore"
	var createCalled bool
	var updateCalledWithStaleID bool

	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			// The fresh top-5 no longer contains staleID (e.g. it was deleted
			// or deprecated between the extraction-time lookup and this write).
			return []*Candidate{{ID: "cand-1", Title: "Something else", Similarity: 0.5}}, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			createCalled = true
			r.ID = "new-id"
			return r, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			if id == staleID {
				updateCalledWithStaleID = true
			}
			return &record.Record{ID: id}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	req := baseStoreRequest()
	req.Source = record.SourceSession
	req.ExtractionDecision = &ExtractionDecision{Action: ActionUpdate, TargetID: &staleID}

	resp, err := svc.Store(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != ActionAdd {
		t.Errorf("expected fallback to ADD when target id is not in the fresh top-5, got %s", resp.Decision)
	}
	if !createCalled {
		t.Error("expected a new record to be created via the ADD fallback")
	}
	if updateCalledWithStaleID {
		t.Error("must never call Update against a target id that isn't in the fresh top-5")
	}
}

// --- AC-17/AC-18: SUPERSEDE atomicity ---

func TestStore_Supersede_WritesBothRowsInOneTx(t *testing.T) {
	oldID := "old-record-id"
	type updateCall struct {
		id      string
		updates map[string]interface{}
	}
	var createCalls []*record.Record
	var updateCalls []updateCall

	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: oldID, Title: "Old fact", Similarity: 0.9}}, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			r.ID = "new-record-id"
			createCalls = append(createCalls, r)
			return r, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			updateCalls = append(updateCalls, updateCall{id: id, updates: updates})
			return &record.Record{ID: id}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	req := baseStoreRequest()
	req.Source = record.SourceSession
	req.ExtractionDecision = &ExtractionDecision{Action: ActionSupersede, TargetID: &oldID}

	resp, err := svc.Store(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != ActionSupersede {
		t.Fatalf("expected SUPERSEDE decision, got %s", resp.Decision)
	}
	if len(createCalls) != 1 {
		t.Fatalf("expected exactly one new record created, got %d", len(createCalls))
	}
	if len(updateCalls) != 2 {
		t.Fatalf("expected exactly two Update calls (deprecate old + set superseded_by), got %d", len(updateCalls))
	}

	deprecateCall := updateCalls[0]
	if deprecateCall.id != oldID {
		t.Errorf("first update should target the old record, got id=%q", deprecateCall.id)
	}
	if status, _ := deprecateCall.updates["status"].(record.Status); status != record.StatusDeprecated {
		t.Errorf("expected old record status=deprecated, got %v", deprecateCall.updates["status"])
	}
	if _, ok := deprecateCall.updates["deprecation_reason"]; !ok {
		t.Error("expected a deprecation_reason to be recorded on SUPERSEDE")
	}
	if _, ok := deprecateCall.updates["superseded_by"]; ok {
		t.Error("superseded_by must only be set after the new record exists (an empty string is not a valid UUID)")
	}
	if createCalls[0].Namespace != DefaultNamespace {
		t.Errorf("superseding record namespace = %q, want %q (an unscoped row is unreachable)", createCalls[0].Namespace, DefaultNamespace)
	}

	linkCall := updateCalls[1]
	if linkCall.id != oldID {
		t.Errorf("second update should also target the old record, got id=%q", linkCall.id)
	}
	if sb, _ := linkCall.updates["superseded_by"].(string); sb != "new-record-id" {
		t.Errorf("expected superseded_by=new-record-id, got %v", linkCall.updates["superseded_by"])
	}
	if resp.ID != "new-record-id" {
		t.Errorf("expected response ID to be the new record, got %q", resp.ID)
	}
}

// TestStore_Supersede_RollsBackOnError pins AC-17's atomicity: if any part
// of the SUPERSEDE sequence fails inside WithTx, Store must report the
// whole write as failed rather than returning a partial success.
func TestStore_Supersede_RollsBackOnError(t *testing.T) {
	oldID := "old-record-id"
	updateCallCount := 0

	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: oldID, Title: "Old fact", Similarity: 0.9}}, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			r.ID = "new-record-id"
			return r, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			updateCallCount++
			if updateCallCount == 2 {
				// Simulate a DB failure while linking superseded_by.
				return nil, errors.New("simulated connection loss")
			}
			return &record.Record{ID: id}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	req := baseStoreRequest()
	req.Source = record.SourceSession
	req.ExtractionDecision = &ExtractionDecision{Action: ActionSupersede, TargetID: &oldID}

	resp, err := svc.Store(context.Background(), req)
	if err == nil {
		t.Fatal("expected Store to report an error when the SUPERSEDE sequence fails partway through")
	}
	if resp != nil {
		t.Errorf("expected a nil response on a failed/rolled-back write, got %+v", resp)
	}
}

// --- AC-57: Postgres/Ollama unavailable during write => clear error, no write ---

func TestStore_EmbeddingProviderUnavailable_NoWriteAttempted(t *testing.T) {
	store := &mockStore{
		WithTxFunc: func(ctx context.Context, fn func(tx TxStore) error) error {
			t.Fatal("WithTx must not be invoked when the embedding provider is unavailable (AC-57)")
			return nil
		},
	}
	embed := &mockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string, maxTokens int) ([]float32, error) {
			return nil, errors.New("ollama unreachable")
		},
	}
	svc := New(store, embed, &mockScrubber{}, &mockClock{}, writepathCfg())

	resp, err := svc.Store(context.Background(), baseStoreRequest())
	if err == nil {
		t.Fatal("expected an error when the embedding provider is unavailable")
	}
	if resp != nil {
		t.Errorf("expected a nil response, got %+v", resp)
	}
}

func TestStore_PostgresUnavailable_ReturnsClearError(t *testing.T) {
	store := &mockStore{
		WithTxFunc: func(ctx context.Context, fn func(tx TxStore) error) error {
			return errors.New("dial tcp: connection refused")
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	resp, err := svc.Store(context.Background(), baseStoreRequest())
	if err == nil {
		t.Fatal("expected an error when Postgres is unreachable")
	}
	if resp != nil {
		t.Errorf("expected a nil response, got %+v", resp)
	}
}

// --- AC-38/AC-39/AC-55: scrubbing before embed/persist, embed input order ---

func TestStore_ScrubbingAppliedBeforeEmbedAndPersist(t *testing.T) {
	const secret = "AKIAISECRETMARKER123"
	const redacted = "***REDACTED***"

	var embedInputSeen string
	var persistedContent string

	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return nil, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			persistedContent = r.Content
			r.ID = "new-id"
			return r, nil
		},
	}
	embed := &mockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string, maxTokens int) ([]float32, error) {
			embedInputSeen = text
			return make([]float32, 1024), nil
		},
	}
	scrubber := &mockScrubber{
		ScrubFunc: func(text string) (string, bool) {
			if strings.Contains(text, secret) {
				return strings.ReplaceAll(text, secret, redacted), true
			}
			return text, false
		},
	}
	svc := New(store, embed, scrubber, &mockClock{}, writepathCfg())

	req := baseStoreRequest()
	req.Content = "here is a secret: " + secret

	if _, err := svc.Store(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(embedInputSeen, secret) {
		t.Errorf("embedding input still contains the raw secret: %q", embedInputSeen)
	}
	if !strings.Contains(embedInputSeen, redacted) {
		t.Errorf("expected embedding input to contain the redaction placeholder, got %q", embedInputSeen)
	}
	if strings.Contains(persistedContent, secret) {
		t.Errorf("persisted content still contains the raw secret: %q", persistedContent)
	}
}

// TestStore_EmbedInputOrder pins AC-55: embedding input is composed as
// title, then tags, then content, in that order.
func TestStore_EmbedInputOrder(t *testing.T) {
	var embedInputSeen string
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return nil, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			r.ID = "new-id"
			return r, nil
		},
	}
	embed := &mockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string, maxTokens int) ([]float32, error) {
			embedInputSeen = text
			return make([]float32, 1024), nil
		},
	}
	svc := New(store, embed, &mockScrubber{}, &mockClock{}, writepathCfg())

	req := baseStoreRequest()
	req.Title = "ZZZTITLEMARKER"
	req.Tags = []string{"YYYTAGMARKER"}
	req.Content = "XXXCONTENTMARKER"

	if _, err := svc.Store(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	titleIdx := strings.Index(embedInputSeen, "ZZZTITLEMARKER")
	tagIdx := strings.Index(embedInputSeen, "YYYTAGMARKER")
	contentIdx := strings.Index(embedInputSeen, "XXXCONTENTMARKER")

	if titleIdx < 0 || tagIdx < 0 || contentIdx < 0 {
		t.Fatalf("expected title/tags/content all present in embed input, got %q", embedInputSeen)
	}
	if titleIdx >= tagIdx || tagIdx >= contentIdx {
		t.Errorf("expected embed input order title < tags < content, got title@%d tags@%d content@%d (%q)",
			titleIdx, tagIdx, contentIdx, embedInputSeen)
	}
}

// --- AC-34: seen_count >= 2 promotes candidate -> active via NOOP ---

func TestStore_NoopPromotesCandidateToActiveAtSeenCountTwo(t *testing.T) {
	var lastUpdates map[string]interface{}
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: "cand-1", Similarity: 0.9}}, nil
		},
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusCandidate, SeenCount: 1}, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			lastUpdates = updates
			return &record.Record{ID: id}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	resp, err := svc.Store(context.Background(), baseStoreRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != ActionNoop {
		t.Fatalf("expected NOOP, got %s", resp.Decision)
	}
	if sc, _ := lastUpdates["seen_count"].(int); sc != 2 {
		t.Fatalf("expected seen_count=2, got %v", lastUpdates["seen_count"])
	}
	if status, _ := lastUpdates["status"].(record.Status); status != record.StatusActive {
		t.Errorf("expected candidate to be promoted to active at seen_count=2 (AC-34), got status=%v", lastUpdates["status"])
	}
}

func TestStore_NoopDoesNotPromoteBelowSeenCountTwo(t *testing.T) {
	var lastUpdates map[string]interface{}
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return []*Candidate{{ID: "cand-1", Similarity: 0.9}}, nil
		},
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusCandidate, SeenCount: 0}, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			lastUpdates = updates
			return &record.Record{ID: id}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	if _, err := svc.Store(context.Background(), baseStoreRequest()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc, _ := lastUpdates["seen_count"].(int); sc != 1 {
		t.Fatalf("expected seen_count=1, got %v", lastUpdates["seen_count"])
	}
	if _, promoted := lastUpdates["status"]; promoted {
		t.Errorf("seen_count=1 must not promote candidate to active yet, got status update=%v", lastUpdates["status"])
	}
}

// --- AC-29: PR source starts active/0.75, inline/session start candidate/0.5 ---

func TestStore_PRSourceStartsActiveWithHigherConfidence(t *testing.T) {
	var created *record.Record
	store := &mockStore{
		FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
			return nil, nil
		},
		CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) {
			created = r
			r.ID = "new-id"
			return r, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())

	req := baseStoreRequest()
	req.Source = record.SourcePR

	if _, err := svc.Store(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.Status != record.StatusActive {
		t.Errorf("expected PR-sourced record to start active, got %s", created.Status)
	}
	if created.Confidence != 0.75 {
		t.Errorf("expected PR-sourced record confidence 0.75, got %v", created.Confidence)
	}
}
