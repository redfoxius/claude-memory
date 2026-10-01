package memory

import (
	"context"
	"fmt"
	"log/slog"

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
// Per AC-55, the embedding input is the record's title + tags + content, so
// a change to any one of those three fields invalidates the stored
// embedding — UpdateRecord therefore fetches the existing record first (AC-10:
// ErrNotFound if it does not exist), merges the requested changes over it,
// and whenever title, tags, or content changed, recomputes the embedding
// from the *merged* title+tags+content via the same composeEmbedInput
// helper the write path uses (AC-8, AC-55). Changed title/content are
// scrubbed for secrets before embedding or persisting (AC-38, AC-39). If the
// embedding provider fails, the update is aborted before any store call is
// made — no partial update (AC-57).
func (s *Service) UpdateRecord(ctx context.Context, req *UpdateRequest) (*record.Record, error) {
	if req == nil {
		return nil, fmt.Errorf("update request is required")
	}

	if req.ID == "" {
		return nil, fmt.Errorf("id is required")
	}

	// Fetch the existing record so a partial update can still recompute the
	// embedding from the full (merged) title+tags+content (AC-55).
	existing, err := s.store.Get(ctx, req.ID)
	if err != nil {
		return nil, fmt.Errorf("get record: %w", err)
	}

	updates := make(map[string]interface{})

	titleChanged := req.Title != nil
	contentChanged := req.Content != nil
	tagsChanged := len(req.Tags) > 0

	// Start from the existing record's fields, then overlay whatever the
	// request changes, so the embedding input always reflects the merged
	// record rather than just the fields this call happened to touch.
	mergedTitle := existing.Title
	mergedContent := existing.Content
	mergedTags := existing.Tags

	var titleRedacted, contentRedacted bool

	if titleChanged {
		mergedTitle, titleRedacted = s.scrubber.Scrub(*req.Title)
		updates["title"] = mergedTitle
	}

	if contentChanged {
		mergedContent, contentRedacted = s.scrubber.Scrub(*req.Content)
		updates["content"] = mergedContent
	}

	if titleRedacted || contentRedacted {
		slog.InfoContext(ctx, "secrets redacted in update request",
			"title_redacted", titleRedacted,
			"content_redacted", contentRedacted)
	}

	if tagsChanged {
		mergedTags = req.Tags
		updates["tags"] = mergedTags
	}

	// If title, tags, or content changed, recompute the embedding from the
	// merged title+tags+content (AC-8, AC-55). A provider failure aborts the
	// update before s.store.Update is ever called, so there is no partial
	// write (AC-57).
	if titleChanged || contentChanged || tagsChanged {
		embedInput := composeEmbedInput(mergedTitle, mergedTags, mergedContent)

		embedding, embedErr := s.embeddingProvider.Embed(ctx, embedInput, s.cfg.EmbedMaxTokens)
		if embedErr != nil {
			return nil, fmt.Errorf("embedding provider unavailable: %w", embedErr)
		}

		updates["embedding"] = embedding
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
