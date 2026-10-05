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

	rec, err := s.getAccessible(ctx, id)
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
	existing, err := s.getAccessible(ctx, req.ID)
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

	// Commit baseline: an explicit value wins; otherwise a content/files
	// change re-baselines when the files are clean (see baselineSHA).
	if req.CommitSHA != nil {
		if *req.CommitSHA != "" && !ValidCommitSHA(*req.CommitSHA) {
			return nil, fmt.Errorf("invalid commit_sha %q", *req.CommitSHA)
		}
		if *req.CommitSHA == "" {
			updates["commit_sha"] = nil // clear the baseline
		} else {
			updates["commit_sha"] = *req.CommitSHA
		}
	} else if contentChanged || len(req.Files) > 0 {
		files := existing.Files
		if len(req.Files) > 0 {
			files = req.Files
		}
		if sha := s.baselineSHA(ctx, existing.Repo, files); sha != "" {
			updates["commit_sha"] = sha
		}
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

	// memory_update is always an inline-source update; a status change adds
	// promoted / deprecated only for the transitions that count (spec 6.4):
	// any other status change (e.g. un-deprecate) is just the update.
	upd := s.event(existing.Namespace, EventRecordUpdated, rec.ID)
	upd.Source = EventSourceInline
	evs := []Event{upd}
	if existing.Status == record.StatusCandidate && rec.Status == record.StatusActive {
		evs = append(evs, s.transitionEvent(existing.Namespace, EventRecordPromoted, rec.ID, ViaTool))
	}
	if existing.Status != record.StatusDeprecated && rec.Status == record.StatusDeprecated {
		evs = append(evs, s.transitionEvent(existing.Namespace, EventRecordDeprecated, rec.ID, ViaTool))
	}
	s.appendEvents(ctx, evs...)

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

	before, err := s.getAccessible(ctx, req.ID)
	if err != nil {
		return nil, fmt.Errorf("deprecate record: %w", err)
	}

	updates := map[string]interface{}{
		"status":              record.StatusDeprecated,
		"deprecation_reason": req.Reason,
	}

	if req.SupersededBy != nil {
		if _, err := s.getAccessible(ctx, *req.SupersededBy); err != nil {
			return nil, fmt.Errorf("deprecate record: superseded_by: %w", err)
		}
		updates["superseded_by"] = *req.SupersededBy
	}

	rec, err := s.store.Update(ctx, req.ID, updates)
	if err != nil {
		return nil, fmt.Errorf("deprecate record: %w", err)
	}

	if before.Status != record.StatusDeprecated {
		s.appendEvents(ctx, s.transitionEvent(before.Namespace, EventRecordDeprecated, rec.ID, ViaTool))
	}

	return rec, nil
}

// ListRecords returns all records matching the given optional filters.
// If no filters are provided, returns all records.
// Filters apply with AND logic (AC-11).
func (s *Service) ListRecords(ctx context.Context, filters ListFilters) ([]*record.Record, error) {
	// Listing is always confined to the service's own namespace.
	ns := s.namespace
	filters.Namespace = &ns
	recs, err := s.store.List(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("list records: %w", err)
	}

	return recs, nil
}

// DeleteRecord hard-deletes a record and emits record_deleted (via=tool). It
// returns *ErrReferenced when another record's superseded_by points at it.
func (s *Service) DeleteRecord(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}
	rec, err := s.getAccessible(ctx, id)
	if err != nil {
		return fmt.Errorf("delete record: %w", err)
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete record: %w", err)
	}
	s.appendEvents(ctx, s.transitionEvent(rec.Namespace, EventRecordDeleted, id, ViaTool))
	return nil
}

// Similar returns up to limit records nearest to the record's stored embedding
// (same namespace and repo, or repo "*"), the record itself excluded. It does
// not call the embedding provider. A record without an embedding returns
// ErrNoEmbedding.
func (s *Service) Similar(ctx context.Context, id string, limit int) ([]*Candidate, error) {
	rec, err := s.getAccessible(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("similar: %w", err)
	}
	if len(rec.Embedding) == 0 {
		return nil, ErrNoEmbedding
	}
	cands, err := s.store.FindCandidates(ctx, rec.Embedding, rec.Namespace, rec.Repo, limit+1)
	if err != nil {
		return nil, fmt.Errorf("similar: %w", err)
	}
	out := make([]*Candidate, 0, len(cands))
	for _, c := range cands {
		if c.ID != rec.ID && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}
