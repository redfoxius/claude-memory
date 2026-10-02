package memory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/record"
)

// Test doubles for the port interfaces.

type mockTxStore struct {
	store *mockStore
}

func (m *mockTxStore) AcquireLock(ctx context.Context, namespace, repo string, titleHash string) error {
	m.store.lockNamespace = namespace
	if m.store.AcquireLockFunc != nil {
		return m.store.AcquireLockFunc(ctx, repo, titleHash)
	}
	return nil
}

func (m *mockTxStore) FindCandidates(ctx context.Context, embedding []float32, namespace, repo string, limit int) ([]*Candidate, error) {
	m.store.candidatesNamespace = namespace
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
	CreateFunc                func(ctx context.Context, r *record.Record) (*record.Record, error)
	GetFunc                   func(ctx context.Context, id string) (*record.Record, error)
	UpdateFunc                func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error)
	SearchFunc                func(ctx context.Context, query string, embedding []float32, repo string, options SearchOptions) (*SearchResult, error)
	FindCandidatesFunc        func(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error)
	ListFunc                  func(ctx context.Context, filters ListFilters) ([]*record.Record, error)
	WithTxFunc                func(ctx context.Context, fn func(tx TxStore) error) error
	AcquireLockFunc           func(ctx context.Context, repo string, titleHash string) error
	DeleteCandidatesByTTLFunc func(ctx context.Context, ttlDays int) (int, error)
	DeleteFunc                func(ctx context.Context, id string) error

	// Recorded by the mocks so namespace tests can assert scoping.
	lockNamespace       string
	candidatesNamespace string
	searchOpts          SearchOptions
	listFilters         ListFilters
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
	m.searchOpts = options
	if m.SearchFunc != nil {
		return m.SearchFunc(ctx, query, embedding, repo, options)
	}
	return &SearchResult{Records: []*SearchRecord{}}, nil
}

func (m *mockStore) FindCandidates(ctx context.Context, embedding []float32, namespace, repo string, limit int) ([]*Candidate, error) {
	if m.FindCandidatesFunc != nil {
		return m.FindCandidatesFunc(ctx, embedding, repo, limit)
	}
	return []*Candidate{}, nil
}

func (m *mockStore) List(ctx context.Context, filters ListFilters) ([]*record.Record, error) {
	m.listFilters = filters
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

func (m *mockStore) AcquireLock(ctx context.Context, namespace, repo string, titleHash string) error {
	if m.AcquireLockFunc != nil {
		return m.AcquireLockFunc(ctx, repo, titleHash)
	}
	return nil
}

func (m *mockStore) Delete(ctx context.Context, id string) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, id)
	}
	return nil
}

func (m *mockStore) DeleteCandidatesByTTL(ctx context.Context, ttlDays int) (int, error) {
	if m.DeleteCandidatesByTTLFunc != nil {
		return m.DeleteCandidatesByTTLFunc(ctx, ttlDays)
	}
	return 0, nil
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
		MaxContentChars:  20000,
		StoreSimUpdate:   0.92,
		StoreSimAsk:      0.80,
		EmbedMaxTokens:   2048,
		HookSimThreshold: 0.75,
		HookTimeout:      800 * time.Millisecond,
		CandidateTTL:     180 * 24 * time.Hour,
	}

	t.Run("get existing record", func(t *testing.T) {
		expectedRecord := &record.Record{
			Namespace: DefaultNamespace,
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
			Namespace:         DefaultNamespace,
			ID:                "rec1",
			Status:            record.StatusDeprecated,
			DeprecationReason: ptrString("Record is outdated"),
			CreatedAt:         time.Now(),
		}

		store := &mockStore{
			GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
				return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive}, nil
			},
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
	cfg := &config.Config{MaxContentChars: 20000, EmbedMaxTokens: 2048}

	t.Run("update with new content", func(t *testing.T) {
		existingRec := &record.Record{
			Namespace: DefaultNamespace,
			ID:        "rec1",
			Title:     "Original Title",
			Content:   "Original content",
			Tags:      []string{"tag1", "tag2"},
			Status:    record.StatusActive,
		}
		updatedRec := &record.Record{
			Namespace: DefaultNamespace,
			ID:        "rec1",
			Title:     "Original Title",
			Content:   "Updated content",
			Status:    record.StatusActive,
		}

		store := &mockStore{
			GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
				if id == "rec1" {
					return existingRec, nil
				}
				return nil, ErrNotFound
			},
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

	t.Run("not found", func(t *testing.T) {
		store := &mockStore{
			GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
				return nil, ErrNotFound
			},
		}

		svc := New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, cfg)

		newContent := "doesn't matter"
		_, err := svc.UpdateRecord(context.Background(), &UpdateRequest{ID: "missing", Content: &newContent})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("AC-10: expected ErrNotFound, got %v", err)
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

// TestService_Search_PassesEmbeddingToStore is a regression test for the
// hybrid-search bug where Search computed a query embedding but a bug
// elsewhere (postgres hybrid_search.go's CTE alias error) silently
// degraded every search to full-text-only. This test pins the contract at
// the memory.Service layer: when the embedding provider succeeds, Search
// must hand the resulting non-nil embedding to Store.Search rather than
// dropping it (which would force every search into the degraded,
// Similarity-always-0 full-text-only path).
func TestService_Search_PassesEmbeddingToStore(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000}

	wantEmbedding := make([]float32, 1024)
	for i := range wantEmbedding {
		wantEmbedding[i] = float32(i) / 1024.0
	}

	var gotEmbedding []float32
	var searchCalled bool

	store := &mockStore{
		SearchFunc: func(ctx context.Context, query string, embedding []float32, repo string, options SearchOptions) (*SearchResult, error) {
			searchCalled = true
			gotEmbedding = embedding
			return &SearchResult{Records: []*SearchRecord{}}, nil
		},
	}

	embedProvider := &mockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string, maxTokens int) ([]float32, error) {
			return wantEmbedding, nil
		},
	}

	svc := New(store, embedProvider, &mockScrubber{}, &mockClock{}, cfg)

	_, err := svc.Search(context.Background(), &SearchRequest{Query: "some paraphrase query"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !searchCalled {
		t.Fatal("expected Store.Search to be called")
	}

	if gotEmbedding == nil {
		t.Fatal("expected Store.Search to receive a non-nil embedding when the embedding provider succeeds")
	}

	if len(gotEmbedding) != len(wantEmbedding) {
		t.Fatalf("expected embedding of length %d, got %d", len(wantEmbedding), len(gotEmbedding))
	}

	for i := range wantEmbedding {
		if gotEmbedding[i] != wantEmbedding[i] {
			t.Fatalf("embedding mismatch at index %d: want %f, got %f", i, wantEmbedding[i], gotEmbedding[i])
		}
	}
}

// TestService_UpdateRecord_ContentChangeRecomputesEmbedding encodes AC-8:
// "memory_update shall re-compute the record's embedding whenever title or
// content changes." It uses an UpdateFunc that enforces the same column
// whitelist the real postgres.Store.Update applies (internal/postgres/store.go,
// allowedColumns) before ever touching a connection, so this also catches a
// non-whitelisted key leaking into the updates map without needing
// Postgres/testcontainers.
func TestService_UpdateRecord_ContentChangeRecomputesEmbedding(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000, EmbedMaxTokens: 2048}

	wantEmbedding := make([]float32, 1024)
	for i := range wantEmbedding {
		wantEmbedding[i] = float32(i) / 1024.0
	}

	allowedColumns := map[string]bool{
		"title": true, "content": true, "tags": true, "files": true,
		"ticket": true, "status": true, "confidence": true,
		"deprecation_reason": true, "superseded_by": true,
		"seen_count": true, "used_count": true, "last_used_at": true,
		"embedding": true,
	}

	existingRec := &record.Record{
		Namespace: DefaultNamespace,
		ID:        "rec1",
		Title:     "Existing Title",
		Content:   "Existing content",
		Tags:      []string{"tagA", "tagB"},
		Status:    record.StatusActive,
	}

	var capturedUpdates map[string]interface{}
	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			if id == "rec1" {
				return existingRec, nil
			}
			return nil, ErrNotFound
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			for k := range updates {
				if !allowedColumns[k] {
					return nil, fmt.Errorf("update: column %q not allowed", k)
				}
			}
			capturedUpdates = updates
			content, _ := updates["content"].(string)
			return &record.Record{ID: id, Namespace: DefaultNamespace, Content: content, Status: record.StatusActive}, nil
		},
	}

	embedCalled := false
	embedProvider := &mockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string, maxTokens int) ([]float32, error) {
			embedCalled = true
			return wantEmbedding, nil
		},
	}

	svc := New(store, embedProvider, &mockScrubber{}, &mockClock{}, cfg)

	newContent := "Updated content with new facts"
	_, err := svc.UpdateRecord(context.Background(), &UpdateRequest{ID: "rec1", Content: &newContent})
	if err != nil {
		t.Fatalf("UpdateRecord failed (likely a non-whitelisted key leaking into the updates map): %v", err)
	}

	if !embedCalled {
		t.Error("AC-8: expected UpdateRecord to call the embedding provider when content changes")
	}
	if _, ok := capturedUpdates["embedding"]; !ok {
		t.Errorf("AC-8: expected the updates map to include a recomputed 'embedding', got keys: %v", keysOf(capturedUpdates))
	}
}

// TestService_UpdateRecord_ContentOnlyEmbedsExistingTitleAndTags encodes
// AC-55: the embedding input is the record's title + tags + content, so a
// content-only update must still embed the record's existing (unchanged)
// title and tags, not just the new content, otherwise the recomputed
// embedding would drift away from what the record is actually titled/tagged.
func TestService_UpdateRecord_ContentOnlyEmbedsExistingTitleAndTags(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000, EmbedMaxTokens: 2048}

	existingRec := &record.Record{
		Namespace: DefaultNamespace,
		ID:        "rec1",
		Title:     "Existing Title",
		Content:   "Existing content",
		Tags:      []string{"tagA", "tagB"},
		Status:    record.StatusActive,
	}

	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return existingRec, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive}, nil
		},
	}

	var gotEmbedInput string
	embedProvider := &mockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string, maxTokens int) ([]float32, error) {
			gotEmbedInput = text
			return make([]float32, 1024), nil
		},
	}

	svc := New(store, embedProvider, &mockScrubber{}, &mockClock{}, cfg)

	newContent := "Brand new content"
	_, err := svc.UpdateRecord(context.Background(), &UpdateRequest{ID: "rec1", Content: &newContent})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantEmbedInput := composeEmbedInput(existingRec.Title, existingRec.Tags, newContent)
	if gotEmbedInput != wantEmbedInput {
		t.Errorf("AC-55: expected embed input %q (existing title+tags + new content), got %q", wantEmbedInput, gotEmbedInput)
	}
}

// TestService_UpdateRecord_TagsOnlyRecomputesEmbedding encodes AC-55: tags
// are part of the embedding input, so a tags-only update (title/content
// unchanged) must still re-embed using the existing title+content with the
// new tags.
func TestService_UpdateRecord_TagsOnlyRecomputesEmbedding(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000, EmbedMaxTokens: 2048}

	existingRec := &record.Record{
		Namespace: DefaultNamespace,
		ID:        "rec1",
		Title:     "Existing Title",
		Content:   "Existing content",
		Tags:      []string{"tagA"},
		Status:    record.StatusActive,
	}

	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return existingRec, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive}, nil
		},
	}

	embedCalled := false
	var gotEmbedInput string
	embedProvider := &mockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string, maxTokens int) ([]float32, error) {
			embedCalled = true
			gotEmbedInput = text
			return make([]float32, 1024), nil
		},
	}

	svc := New(store, embedProvider, &mockScrubber{}, &mockClock{}, cfg)

	newTags := []string{"tagA", "tagC"}
	_, err := svc.UpdateRecord(context.Background(), &UpdateRequest{ID: "rec1", Tags: newTags})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !embedCalled {
		t.Fatal("AC-55: expected a tags-only update to re-embed")
	}

	wantEmbedInput := composeEmbedInput(existingRec.Title, newTags, existingRec.Content)
	if gotEmbedInput != wantEmbedInput {
		t.Errorf("AC-55: expected embed input %q (existing title+content + new tags), got %q", wantEmbedInput, gotEmbedInput)
	}
}

// TestService_UpdateRecord_NoEmbedInputChangeSkipsEmbed is a negative
// counterpart to AC-8/AC-55: updating a field outside the embedding input
// (e.g. ticket) must not call the embedding provider at all.
func TestService_UpdateRecord_NoEmbedInputChangeSkipsEmbed(t *testing.T) {
	cfg := &config.Config{MaxContentChars: 20000, EmbedMaxTokens: 2048}

	existingRec := &record.Record{
		Namespace: DefaultNamespace,
		ID:        "rec1",
		Title:     "Existing Title",
		Content:   "Existing content",
		Tags:      []string{"tagA"},
		Status:    record.StatusActive,
	}

	store := &mockStore{
		GetFunc: func(ctx context.Context, id string) (*record.Record, error) {
			return existingRec, nil
		},
		UpdateFunc: func(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
			if _, ok := updates["embedding"]; ok {
				t.Error("did not expect 'embedding' in the updates map for a ticket-only update")
			}
			return &record.Record{ID: id, Namespace: DefaultNamespace, Status: record.StatusActive}, nil
		},
	}

	embedCalled := false
	embedProvider := &mockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string, maxTokens int) ([]float32, error) {
			embedCalled = true
			return make([]float32, 1024), nil
		},
	}

	svc := New(store, embedProvider, &mockScrubber{}, &mockClock{}, cfg)

	newTicket := "JIRA-123"
	_, err := svc.UpdateRecord(context.Background(), &UpdateRequest{ID: "rec1", Ticket: &newTicket})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if embedCalled {
		t.Error("expected a ticket-only update to NOT call the embedding provider")
	}
}

func keysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Helper function to create a string pointer.
func ptrString(s string) *string {
	return &s
}
