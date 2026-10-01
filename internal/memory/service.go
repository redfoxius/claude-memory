package memory

import (
	"context"
	"errors"
	"fmt"

	"claude-memory/internal/config"
	"claude-memory/internal/record"
)

// Service orchestrates the memory management logic: search, dedup/merge, write-path decisions.
// It depends only on interfaces (ports) it declares in this package, never on concrete
// implementations (postgres, ollama) — those are wired in the composition root.
type Service struct {
	store              Store
	embeddingProvider  EmbeddingProvider
	scrubber           Scrubber
	clock              Clock
	cfg                *config.Config
}

// New constructs a Service with all required dependencies.
// All adapters must implement the port interfaces declared in ports.go.
func New(
	store Store,
	embeddingProvider EmbeddingProvider,
	scrubber Scrubber,
	clock Clock,
	cfg *config.Config,
) *Service {
	return &Service{
		store:             store,
		embeddingProvider: embeddingProvider,
		scrubber:          scrubber,
		clock:             clock,
		cfg:               cfg,
	}
}

// Search performs a hybrid semantic + full-text search using the query text,
// optionally including a pre-computed embedding for the query (for efficiency
// when the embedding was already computed upstream).
// If no embedding is provided, Search computes one from the query text.
// Search respects the configured thresholds and returns results ranked by fused score.
// If the embedding provider is unavailable, Search degrades to full-text-only (AC-7).
// Deprecated records are excluded by default; candidates are flagged as unverified.
func (s *Service) Search(ctx context.Context, req *SearchRequest) (*SearchResult, error) {
	if req == nil {
		return nil, errors.New("search request is required")
	}

	// Compute or use provided embedding.
	// If embedding fails, embedding will be nil and Store will use full-text-only ranking.
	embedding := req.Embedding
	if embedding == nil {
		embedding = s.embedQueryText(ctx, req.Query)
		// Note: if embedQueryText returns nil, the Store will degrade to full-text-only (AC-7).
	}

	opts := SearchOptions{
		Kind:                 req.Kind,
		Tags:                 req.Tags,
		Limit:                req.Limit,
		IncludeDeprecated:    false,
	}
	if opts.Limit == 0 {
		opts.Limit = 5
	}

	// Delegate to the store for hybrid search.
	result, err := s.store.Search(ctx, req.Query, embedding, req.Repo, opts)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	return result, nil
}

// FindCandidates fetches the top-N nearest records in the same repo or repo="*",
// used during the write path to check for dedup before deciding to ADD/UPDATE/SUPERSEDE/NOOP.
// This is a read-only, internal service method (not an MCP tool).
// It uses the same fused-score ranking as Search and returns only {id, title, score}.
func (s *Service) FindCandidates(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
	if limit <= 0 {
		limit = 5
	}
	candidates, err := s.store.FindCandidates(ctx, embedding, repo, limit)
	if err != nil {
		return nil, fmt.Errorf("find candidates: %w", err)
	}
	return candidates, nil
}

// Store persists a new record, applying the dedup/merge logic.
// Implemented in writepath.go (WI-3b).
// See writepath.go for full documentation.

// Get retrieves a record by ID.
// Implemented in WI-3a (crud.go).
func (s *Service) Get(ctx context.Context, id string) (*record.Record, error) {
	if id == "" {
		return nil, errors.New("id is required")
	}
	rec, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get: %w", err)
	}
	return rec, nil
}

// Update modifies fields of an existing record.
// If title or content changes, the embedding is recomputed (AC-8).
// Implemented in WI-3a (crud.go).
func (s *Service) Update(ctx context.Context, req *UpdateRequest) (*record.Record, error) {
	return nil, errors.New("not implemented (WI-3a, handled separately)")
}

// Deprecate transitions a record to deprecated status with a reason.
// Implemented in WI-3a (crud.go).
func (s *Service) Deprecate(ctx context.Context, req *DeprecateRequest) (*record.Record, error) {
	return nil, errors.New("not implemented (WI-3a, handled separately)")
}

// List returns all records matching optional filters (repo, kind, status).
// Implemented in WI-3a (crud.go).
func (s *Service) List(ctx context.Context, filters ListFilters) ([]*record.Record, error) {
	recs, err := s.store.List(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	return recs, nil
}

// Feedback records user feedback (useful/outdated/wrong) and applies lifecycle transitions.
// Implemented in lifecycle.go (WI-3c).
// See lifecycle.go for full documentation.

// SearchRequest is the input to Search.
type SearchRequest struct {
	Query     string
	Embedding []float32  // Optional; if provided, used instead of computing from Query.
	Repo      string
	Kind      *record.Kind
	Tags      []string
	Limit     int // Default 5.
}
