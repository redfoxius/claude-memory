// Package mock provides test doubles for the memory service and its ports.
package mock

import (
	"context"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// Service is a fake implementation of *memory.Service for testing.
// It implements the Store, EmbeddingProvider, Scrubber, and Clock ports.
type Service struct {
	storedRecords []*record.Record
	searchResults []*memory.SearchRecord
	candidates    []*memory.Candidate
	embeddings    map[string][]float32
}

// NewMemoryService creates a new fake Service.
func NewMemoryService() *Service {
	return &Service{
		storedRecords: make([]*record.Record, 0),
		embeddings:    make(map[string][]float32),
	}
}

// Store implements the Store port for testing.
func (s *Service) Store(ctx context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error) {
	// Simulate storing a record.
	rec := &record.Record{
		ID:         "test-id-" + req.Title,
		Kind:       req.Kind,
		Title:      req.Title,
		Content:    req.Content,
		Repo:       req.Repo,
		CommitSHA:  req.CommitSHA,
		Source:     req.Source,
		Status:     record.StatusCandidate,
		Confidence: 0.8,
	}
	s.storedRecords = append(s.storedRecords, rec)

	// Return a response indicating the decision.
	decision := memory.ActionAdd
	if req.ExtractionDecision != nil {
		decision = req.ExtractionDecision.Action
	}

	return &memory.StoreResponse{
		ID:       rec.ID,
		Decision: decision,
	}, nil
}

// Search implements the memory service search for testing.
func (s *Service) Search(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResult, error) {
	return &memory.SearchResult{
		Records:  s.searchResults,
		Degraded: false,
	}, nil
}

// List implements the memory service list for testing.
func (s *Service) List(ctx context.Context, filters *memory.ListFilters) ([]*record.Record, error) {
	return s.storedRecords, nil
}

// Get implements the memory service get for testing.
func (s *Service) Get(ctx context.Context, id string) (*record.Record, error) {
	for _, r := range s.storedRecords {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, memory.ErrNotFound
}

// Update implements the memory service update for testing.
func (s *Service) Update(ctx context.Context, req *memory.UpdateRequest) (*record.Record, error) {
	for i, r := range s.storedRecords {
		if r.ID == req.ID {
			if req.Title != nil {
				r.Title = *req.Title
			}
			if req.Content != nil {
				r.Content = *req.Content
			}
			s.storedRecords[i] = r
			return r, nil
		}
	}
	return nil, memory.ErrNotFound
}

// Feedback implements the memory service feedback for testing.
func (s *Service) Feedback(ctx context.Context, req *memory.FeedbackRequest) (*memory.FeedbackResponse, error) {
	for i, r := range s.storedRecords {
		if r.ID == req.ID {
			switch req.Outcome {
			case memory.FeedbackUseful:
				r.Status = record.StatusActive
			case memory.FeedbackOutdated, memory.FeedbackWrong:
				r.Status = record.StatusDeprecated
			}
			s.storedRecords[i] = r
			return &memory.FeedbackResponse{
				ID:        r.ID,
				NewStatus: r.Status,
			}, nil
		}
	}
	return nil, memory.ErrNotFound
}

// Deprecate implements the memory service deprecate for testing.
func (s *Service) Deprecate(ctx context.Context, req *memory.DeprecateRequest) (*record.Record, error) {
	for i, r := range s.storedRecords {
		if r.ID == req.ID {
			r.Status = record.StatusDeprecated
			s.storedRecords[i] = r
			return r, nil
		}
	}
	return nil, memory.ErrNotFound
}

// FindCandidates implements finding candidate records for testing.
func (s *Service) FindCandidates(ctx context.Context, embedding []float32, namespace, repo string, limit int) ([]*memory.Candidate, error) {
	return s.candidates, nil
}

// FindCandidatesForText implements the extraction-time candidate lookup for
// testing; it returns whatever was configured via SetCandidates, ignoring
// the text/repo arguments (tests set candidates explicitly per case).
func (s *Service) FindCandidatesForText(ctx context.Context, title string, tags []string, content string, repo string) ([]*memory.Candidate, error) {
	return s.candidates, nil
}

// SetSearchResults sets the mock results for Search calls.
func (s *Service) SetSearchResults(results []*memory.SearchRecord) {
	s.searchResults = results
}

// SetCandidates sets the mock candidates for FindCandidates calls.
func (s *Service) SetCandidates(candidates []*memory.Candidate) {
	s.candidates = candidates
}

// GetStoredRecords returns all stored records for assertion.
func (s *Service) GetStoredRecords() []*record.Record {
	return s.storedRecords
}
