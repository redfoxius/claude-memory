package memory

import (
	"context"
	"fmt"

	"claude-memory/internal/record"
)

// Get retrieves a single record by ID.
// Returns ErrNotFound if the record does not exist (AC-10).
func (s *Service) GetRecord(ctx context.Context, id string) (*record.Record, error) {
	if id == "" {
		return nil, fmt.Errorf("id is required")
	}

	rec, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get record: %w", err)
	}

	return rec, nil
}

// UpdateRecord modifies fields of an existing record.
// If title or content changes, the embedding is recomputed (AC-8).
// Returns ErrNotFound if the record does not exist (AC-10).
func (s *Service) UpdateRecord(ctx context.Context, req *UpdateRequest) (*record.Record, error) {
	if req == nil {
		return nil, fmt.Errorf("update request is required")
	}

	if req.ID == "" {
		return nil, fmt.Errorf("id is required")
	}

	// If title or content is being changed, we need to recompute the embedding (AC-8).
	// Scrub the new content/title for secrets before embedding.
	updates := make(map[string]interface{})

	if req.Title != nil {
		scrubbed, _ := s.scrubber.Scrub(*req.Title)
		updates["title"] = scrubbed
	}

	if req.Content != nil {
		scrubbed, _ := s.scrubber.Scrub(*req.Content)
		updates["content"] = scrubbed

		// If content changed, re-embed (AC-8).
		// The Store implementation will handle the embedding computation.
		updates["content_changed"] = true
	}

	if len(req.Tags) > 0 {
		updates["tags"] = req.Tags
	}

	if len(req.Files) > 0 {
		updates["files"] = req.Files
	}

	if req.Ticket != nil {
		updates["ticket"] = *req.Ticket
	}

	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if req.Confidence != nil {
		updates["confidence"] = *req.Confidence
	}

	rec, err := s.store.Update(ctx, req.ID, updates)
	if err != nil {
		return nil, fmt.Errorf("update record: %w", err)
	}

	return rec, nil
}

// DeprecateRecord transitions a record to deprecated status.
// Sets the deprecation reason and optionally links to a superseding record.
// Returns ErrNotFound if the record does not exist (AC-10).
func (s *Service) DeprecateRecord(ctx context.Context, req *DeprecateRequest) (*record.Record, error) {
	if req == nil {
		return nil, fmt.Errorf("deprecate request is required")
	}

	if req.ID == "" {
		return nil, fmt.Errorf("id is required")
	}

	if req.Reason == "" {
		return nil, fmt.Errorf("reason is required for deprecation")
	}

	updates := map[string]interface{}{
		"status":              record.StatusDeprecated,
		"deprecation_reason": req.Reason,
	}

	if req.SupersededBy != nil {
		updates["superseded_by"] = *req.SupersededBy
	}

	rec, err := s.store.Update(ctx, req.ID, updates)
	if err != nil {
		return nil, fmt.Errorf("deprecate record: %w", err)
	}

	return rec, nil
}

// ListRecords returns all records matching the given optional filters.
// If no filters are provided, returns all records.
// Filters apply with AND logic (AC-11).
func (s *Service) ListRecords(ctx context.Context, filters ListFilters) ([]*record.Record, error) {
	recs, err := s.store.List(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("list records: %w", err)
	}

	return recs, nil
}
