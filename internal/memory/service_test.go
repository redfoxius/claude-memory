package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/record"
)

// Test doubles for the port interfaces.

type mockTxStore struct {
	store *mockStore
}

func (m *mockTxStore) AcquireLock(ctx context.Context, repo string, titleHash string) error {
	if m.store.AcquireLockFunc != nil {
		return m.store.AcquireLockFunc(ctx, repo, titleHash)
	}
	return nil
}

func (m *mockTxStore) FindCandidates(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
	if m.store.FindCandidatesFunc != nil {
		return m.store.FindCandidatesFunc(ctx, embedding, repo, limit)
	}
	return []*Candidate{}, nil
}

func (m *mockTxStore) Get(ctx context.Context, id string) (*record.Record, error) {
	if m.store.GetFunc != nil {
		return m.store.GetFunc(ctx, id)
	}
	return nil, ErrNotFound
}

func (m *mockTxStore) Create(ctx context.Context, r *record.Record) (*record.Record, error) {
	if m.store.CreateFunc != nil {
		return m.store.CreateFunc(ctx, r)
	}
	return nil, errors.New("mock: Create not implemented")
}

func (m *mockTxStore) Update(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
	if m.store.UpdateFunc != nil {
		return m.store.UpdateFunc(ctx, id, updates)
	}
	return nil, errors.New("mock: Update not implemented")
}

type mockStore struct {
	CreateFunc          func(ctx context.Context, r *record.Record) (*record.Record, error)
	GetFunc             func(ctx context.Context, id string) (*record.Record, error)
	UpdateFunc          func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error)
	SearchFunc          func(ctx context.Context, query string, embedding []float32, repo string, options SearchOptions) (*SearchResult, error)
	FindCandidatesFunc  func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error)
	ListFunc            func(ctx context.Context, filters ListFilters) ([]*record.Record, error)
	WithTxFunc          func(ctx context.Context, fn func(tx TxStore) error) error
	AcquireLockFunc     func(ctx context.Context, repo string, titleHash string) error
	DeleteCandidatesByTTLFunc func(ctx context.Context, ttlDays int) error
}

func (m *mockStore) Create(ctx context.Context, r *record.Record) (*record.Record, error) {
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, r)
	}
	return nil, errors.New("mock: Create not implemented")
}

func (m *mockStore) Get(ctx context.Context, id string) (*record.Record, error) {
	if m.GetFunc != nil {
		return m.GetFunc(ctx, id)
	}
	return nil, ErrNotFound
}

func (m *mockStore) Update(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
	if m.UpdateFunc != nil {
		return m.UpdateFunc(ctx, id, updates)
	}
	return nil, errors.New("mock: Update not implemented")
}

func (m *mockStore) Search(ctx context.Context, query string, embedding []float32, repo string, options SearchOptions) (*SearchResult, error) {
	if m.SearchFunc != nil {
		return m.SearchFunc(ctx, query, embedding, repo, options)
	}
	return &SearchResult{Records: []*SearchRecord{}}, nil
}

func (m *mockStore) FindCandidates(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
	if m.FindCandidatesFunc != nil {
		return m.FindCandidatesFunc(ctx, embedding, repo, limit)
	}
	return []*Candidate{}, nil
}

func (m *mockStore) List(ctx context.Context, filters ListFilters) ([]*record.Record, error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, filters)
	}
	return []*record.Record{}, nil
}

func (m *mockStore) WithTx(ctx context.Context, fn func(tx TxStore) error) error {
	if m.WithTxFunc != nil {
		return m.WithTxFunc(ctx, fn)
	}
	// Default: provide a default TxStore implementation that delegates to the mock's methods
	tx := &mockTxStore{
		store: m,
	}
	return fn(tx)
}

func (m *mockStore) AcquireLock(ctx context.Context, repo string, titleHash string) error {
	if m.AcquireLockFunc != nil {
		return m.AcquireLockFunc(ctx, repo, titleHash)
	}
	return nil
}

func (m *mockStore) DeleteCandidatesByTTL(ctx context.Context, ttlDays int) error {
	if m.DeleteCandidatesByTTLFunc != nil {
		return m.DeleteCandidatesByTTLFunc(ctx, ttlDays)
	}
	return nil
}

type mockEmbeddingProvider struct {
	EmbedFunc func(ctx context.Context, text string, maxTokens int) ([]float32, error)
}

func (m *mockEmbeddingProvider) Embed(ctx context.Context, text string, maxTokens int) ([]float32, error) {
	if m.EmbedFunc != nil {
		return m.EmbedFunc(ctx, text, maxTokens)
	}
	return make([]float32, 1024), nil
}

type mockScrubber struct {
	ScrubFunc func(text string) (redacted string, wasRedacted bool)
}

func (m *mockScrubber) Scrub(text string) (redacted string, wasRedacted bool) {
	if m.ScrubFunc != nil {
		return m.ScrubFunc(text)
	}
	return text, false
}

type mockClock struct {
	NowFunc func() time.Time
}

func (m *mockClock) Now() time.Time {
	if m.NowFunc != nil {
		return m.NowFunc()
	}
	return time.Now().UTC()
}

func TestService_GetRecord(t *testing.T) {
	cfg := &config.Config{
		MaxContentChars:    20000,
		StoreSimUpdate:     0.92,
		StoreSimAsk:        0.80,
		EmbedMaxTokens:     2048,
		HookSimThreshold:   0.75,
		HookTimeout:        800 * time.Millisecond,
		CandidateTTL:       180 * 24 * time.Hour,
	}

	t.Run("get existing record", func(t *testing.T) {
		expectedRecord := &record.Record{
			ID:        "test-id-1",
			Kind:      record.KindPattern,
			Title:     "Test Pattern",
			Content:   "Test content",
			Repo:      "test-repo",
			Status:    record.StatusActive,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		store := &mockStore{
			GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
				if id == "test-id-1" {
					return expectedRecord, nil
				}
				return nil, ErrNotFound
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		result, err := svc.GetRecord(context.Background(), "test-id-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.ID != expectedRecord.ID {
			t.Errorf("expected ID %s, got %s", expectedRecord.ID, result.ID)
		}
	})

	t.Run("get nonexistent record", func(t *testing.T) {
		store := &mockStore{
			GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
				return nil, ErrNotFound
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		_, err := svc.GetRecord(context.Background(), "nonexistent")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("get with empty id", func(t *testing.T) {
		store := &mockStore{}
		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		_, err := svc.GetRecord(context.Background(), "")
		if err == nil {
			t.Error("expected error for empty id")
		}
	})
}

func TestService_ListRecords(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000}

	records := []*record.Record{
		{
			ID:        "rec1",
			Kind:      record.KindPattern,
			Title:     "Pattern 1",
			Repo:      "repo1",
			Status:    record.StatusActive,
			CreatedAt: time.Now(),
		},
		{
			ID:        "rec2",
			Kind:      record.KindDecision,
			Title:     "Decision 1",
			Repo:      "repo2",
			Status:    record.StatusCandidate,
			CreatedAt: time.Now(),
		},
	}

	t.Run("list all records", func(t *testing.T) {
		store := &mockStore{
			ListFunc: func(ctx context.Context, filters ListFilters) ([]*record.Record, error) {
				return records, nil
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		result, err := svc.ListRecords(context.Background(), ListFilters{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != len(records) {
			t.Errorf("expected %d records, got %d", len(records), len(result))
		}
	})

	t.Run("list with repo filter", func(t *testing.T) {
		store := &mockStore{
			ListFunc: func(ctx context.Context, filters ListFilters) ([]*record.Record, error) {
				if filters.Repo != nil && *filters.Repo == "repo1" {
					return []*record.Record{records[0]}, nil
				}
				return []*record.Record{}, nil
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		repo := "repo1"
		result, err := svc.ListRecords(context.Background(), ListFilters{Repo: &repo})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 1 {
			t.Errorf("expected 1 record, got %d", len(result))
		}
	})
}

func TestService_DeprecateRecord(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000}

	t.Run("deprecate existing record", func(t *testing.T) {
		deprecatedRec := &record.Record{
			ID:                   "rec1",
			Status:               record.StatusDeprecated,
			DeprecationReason:    ptrString("Record is outdated"),
			CreatedAt:            time.Now(),
		}

		store := &mockStore{
			UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
				if id == "rec1" {
					return deprecatedRec, nil
				}
				return nil, ErrNotFound
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		req := &DeprecateRequest{
			ID:     "rec1",
			Reason: "Record is outdated",
		}

		result, err := svc.DeprecateRecord(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Status != record.StatusDeprecated {
			t.Errorf("expected deprecated status, got %s", result.Status)
		}
	})

	t.Run("deprecate nonexistent record", func(t *testing.T) {
		store := &mockStore{
			UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
				return nil, ErrNotFound
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		req := &DeprecateRequest{
			ID:     "nonexistent",
			Reason: "Outdated",
		}

		_, err := svc.DeprecateRecord(context.Background(), req)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("deprecate with empty reason", func(t *testing.T) {
		store := &mockStore{}
		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		req := &DeprecateRequest{
			ID:     "rec1",
			Reason: "",
		}

		_, err := svc.DeprecateRecord(context.Background(), req)
		if err == nil {
			t.Error("expected error for empty reason")
		}
	})
}

func TestService_UpdateRecord(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000}

	t.Run("update with new content", func(t *testing.T) {
		updatedRec := &record.Record{
			ID:      "rec1",
			Title:   "Updated Title",
			Content: "Updated content",
			Status:  record.StatusActive,
		}

		store := &mockStore{
			UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
				if id == "rec1" {
					return updatedRec, nil
				}
				return nil, ErrNotFound
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		newContent := "Updated content"
		req := &UpdateRequest{
			ID:      "rec1",
			Content: &newContent,
		}

		result, err := svc.UpdateRecord(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Content != newContent {
			t.Errorf("expected content %q, got %q", newContent, result.Content)
		}
	})
}

func TestService_FindCandidates(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000}

	candidates := []*Candidate{
		{ID: "cand1", Title: "Candidate 1", Score: 0.95},
		{ID: "cand2", Title: "Candidate 2", Score: 0.85},
	}

	t.Run("find candidates", func(t *testing.T) {
		store := &mockStore{
			FindCandidatesFunc: func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
				return candidates, nil
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		result, err := svc.FindCandidates(context.Background(), make([]float32, 1024), "test-repo", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != len(candidates) {
			t.Errorf("expected %d candidates, got %d", len(candidates), len(result))
		}
	})
}

// Helper function to create a string pointer.
func ptrString(s string) *string {
	return &s
}
