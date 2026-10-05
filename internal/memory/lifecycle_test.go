package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/record"
)

func lifecycleCfg() *config.Config {
	return &config.Config{MaxContentChars: 20000}
}

// --- AC-12/AC-34: feedback(useful) or seen_count>=2 promotes candidate->active ---

func TestFeedback_Useful_PromotesCandidateToActive(t *testing.T) {
	var capturedUpdates map[string]interface{}
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusCandidate, UsedCount: 0}, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			capturedUpdates = updates
			status, _ := updates["status"].(record.Status)
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: status}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, lifecycleCfg())

	resp, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "rec1", Outcome: FeedbackUseful})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.NewStatus != record.StatusActive {
		t.Errorf("expected candidate -> active on useful feedback, got %s", resp.NewStatus)
	}
	if uc, _ := capturedUpdates["used_count"].(int); uc != 1 {
		t.Errorf("expected used_count incremented to 1, got %v", capturedUpdates["used_count"])
	}
}

func TestFeedback_Useful_OnActiveRecord_NoStatusChange(t *testing.T) {
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive, UsedCount: 5}, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			status, _ := updates["status"].(record.Status)
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: status}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, lifecycleCfg())

	resp, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "rec1", Outcome: FeedbackUseful})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.NewStatus != record.StatusActive {
		t.Errorf("expected an already-active record to remain active, got %s", resp.NewStatus)
	}
}

// --- AC-36: feedback(outdated|wrong) deprecates with reason ---

func TestFeedback_Outdated_DeprecatesWithReason(t *testing.T) {
	var capturedUpdates map[string]interface{}
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive}, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			capturedUpdates = updates
			status, _ := updates["status"].(record.Status)
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: status}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, lifecycleCfg())

	note := "no longer true after the refactor"
	resp, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "rec1", Outcome: FeedbackOutdated, Note: &note})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.NewStatus != record.StatusDeprecated {
		t.Errorf("expected deprecated status, got %s", resp.NewStatus)
	}
	reason, _ := capturedUpdates["deprecation_reason"].(string)
	if reason == "" {
		t.Error("expected a deprecation_reason to be recorded")
	}
}

func TestFeedback_Wrong_DeprecatesWithReason(t *testing.T) {
	var capturedUpdates map[string]interface{}
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive}, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			capturedUpdates = updates
			status, _ := updates["status"].(record.Status)
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: status}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, lifecycleCfg())

	resp, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "rec1", Outcome: FeedbackWrong})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.NewStatus != record.StatusDeprecated {
		t.Errorf("expected deprecated status, got %s", resp.NewStatus)
	}
	if _, ok := capturedUpdates["deprecation_reason"]; !ok {
		t.Error("expected a deprecation_reason to be recorded even without a note")
	}
}

// --- AC-10: not-found error ---

func TestFeedback_NotFound(t *testing.T) {
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return nil, ErrNotFound
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, lifecycleCfg())

	_, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "missing", Outcome: FeedbackUseful})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFeedback_UnknownOutcome_ReturnsError(t *testing.T) {
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive}, nil
		},
	}
	svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, lifecycleCfg())

	_, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "rec1", Outcome: FeedbackOutcome("bogus")})
	if err == nil {
		t.Error("expected an error for an unknown feedback outcome")
	}
}

func TestFeedback_MissingIDOrOutcome(t *testing.T) {
	svc := New(&mockStore{}, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, lifecycleCfg())

	if _, err := svc.Feedback(context.Background(), &FeedbackRequest{Outcome: FeedbackUseful}); err == nil {
		t.Error("expected an error for a missing id")
	}
	if _, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "rec1"}); err == nil {
		t.Error("expected an error for a missing outcome")
	}
}
