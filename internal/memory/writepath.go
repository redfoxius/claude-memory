package memory

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"claude-memory/internal/record"
)

// Store persists a new record, applying the dedup/merge logic:
// 1. Scrub secrets from title and content (AC-38, AC-39).
// 2. Compute embedding from title+tags+content (AC-55).
// 3. Fetch top-5 nearest candidates (AC-13).
// 4. For inline sources, decide ADD/UPDATE/SUPERSEDE/NOOP based on thresholds (AC-15).
// 5. For session/pr sources, honor the extraction decision if provided (AC-14).
// 6. Persist under an advisory lock to prevent races (AC-16).
// 7. For SUPERSEDE, set the old record as deprecated in one transaction (AC-17).
//
// Returns a StoreResponse with the decision taken and candidates considered,
// or an error if Postgres/Ollama is unreachable (AC-57).
func (s *Service) Store(ctx context.Context, req *StoreRequest) (*StoreResponse, error) {
	if req == nil {
		return nil, errors.New("store request is required")
	}

	// Validate the request.
	if err := req.validate(s.cfg.MaxContentChars); err != nil {
		return nil, fmt.Errorf("store validation: %w", err)
	}

	// Scrub secrets from title and content before embedding and persistence (AC-38, AC-39).
	scrubbedTitle, titleRedacted := s.scrubber.Scrub(req.Title)
	scrubbedContent, contentRedacted := s.scrubber.Scrub(req.Content)

	if titleRedacted || contentRedacted {
		slog.InfoContext(ctx, "secrets redacted in store request",
			"title_redacted", titleRedacted,
			"content_redacted", contentRedacted)
	}

	// Compose embedding input: title + "\n" + tags + "\n" + content (AC-55).
	embedInput := composeEmbedInput(scrubbedTitle, req.Tags, scrubbedContent)

	// Compute embedding. If embedding provider fails, return a clear error (AC-57).
	embedding, err := s.embeddingProvider.Embed(ctx, embedInput, s.cfg.EmbedMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEmbeddingUnavailable, err)
	}

	// Resolve the target namespace: the service's own, or "global" when the
	// caller explicitly asks for it (never automatic).
	ns := s.namespace
	if req.Namespace == record.GlobalNamespace {
		ns = record.GlobalNamespace
	}

	// Imports have their own, simpler branch: ADD as candidate or SKIP.
	if req.Source == record.SourceImport {
		return s.storeImport(ctx, req, ns, scrubbedTitle, scrubbedContent, embedding)
	}

	// Commit baseline (staleness): an explicit commit_sha wins; otherwise
	// inline/session records get HEAD when their files are clean. Computed
	// before the transaction so no git process runs under the advisory lock.
	commitSHA := req.CommitSHA
	if commitSHA == nil && (req.Source == record.SourceInline || req.Source == record.SourceSession) {
		if sha := s.baselineSHA(ctx, req.Repo, req.Files); sha != "" {
			commitSHA = &sha
		}
	}

	// Compute the advisory lock key: repo + hash(normalized title) (AC-16).
	lockKey := computeLockKey(req.Repo, scrubbedTitle)

	// Fetch top-5 candidates in a transaction (AC-13, AC-16, AC-17).
	var decision WriteAction
	var targetID *string
	var recordID string // Track the ID of the record created/updated by this Store call.
	var candidates []*Candidate
	var inJudgmentRange bool
	// Events are collected inside the transaction and appended only after it
	// commits, so a rollback records nothing (AC-19, AC-22).
	var evs []Event

	err = s.store.WithTx(ctx, func(tx TxStore) error {
		// Acquire advisory lock before fetching candidates (AC-16).
		if err := tx.AcquireLock(ctx, ns, req.Repo, lockKey); err != nil {
			return fmt.Errorf("acquire lock: %w", err)
		}

		// Fetch top-5 candidates (AC-13).
		var fetchErr error
		candidates, fetchErr = tx.FindCandidates(ctx, embedding, ns, req.Repo, 5)
		if fetchErr != nil {
			return fmt.Errorf("find candidates: %w", fetchErr)
		}

		// Check if inline source is in the StoreSimAsk-StoreSimUpdate judgment range (AC-15).
		// An explicit decision is the caller's answer to an earlier
		// needs_judgment response, so it skips the judgment-range exit.
		if req.Source == record.SourceInline && req.ExtractionDecision == nil && len(candidates) > 0 {
			topCandidate := candidates[0]
			if topCandidate.Similarity >= s.cfg.StoreSimAsk && topCandidate.Similarity < s.cfg.StoreSimUpdate {
				// In judgment range; signal caller to decide via ExtractionDecision on next call.
				inJudgmentRange = true
				return nil // Exit early without writing.
			}
		}

		// Decide the action: ADD/UPDATE/SUPERSEDE/NOOP.
		decision, targetID, err = s.decideWriteAction(ctx, req, candidates, scrubbedTitle)
		if err != nil {
			return fmt.Errorf("decide write action: %w", err)
		}

		// Persist the record(s) based on the decision.
		switch decision {
		case ActionAdd:
			// Create a new record.
			newRec := record.New(
				uuid.New().String(),
				req.Kind,
				scrubbedTitle,
				scrubbedContent,
				req.Repo,
				req.Source,
				defaultConfidenceForSource(req.Source, req.Confidence),
			)

			newRec.Namespace = ns

			// Set optional fields.
			if len(req.Files) > 0 {
				newRec.Files = req.Files
			}
			if commitSHA != nil {
				newRec.CommitSHA = commitSHA
			}
			if req.Ticket != nil {
				newRec.Ticket = req.Ticket
			}
			if len(req.Tags) > 0 {
				newRec.Tags = req.Tags
			}

			// Set initial status based on source (AC-29).
			if req.Source == record.SourcePR {
				newRec.Status = record.StatusActive
				newRec.Confidence = 0.75 // PR source has higher initial confidence.
			} else {
				newRec.Status = record.StatusCandidate
				newRec.Confidence = 0.5 // Inline and session sources start as candidate.
			}

			// Set embedding.
			newRec.Embedding = embedding
			newRec.UpdatedAt = s.clock.Now()

			// Persist the new record.
			_, persistErr := tx.Create(ctx, newRec)
			if persistErr != nil {
				return fmt.Errorf("create record: %w", persistErr)
			}

			recordID = newRec.ID
			evs = append(evs, s.createdEvent(ns, newRec.ID, req.Source, newRec.Status))

		case ActionUpdate:
			// Update the existing record with new content/metadata.
			// The target was identified during the decision phase.
			if targetID == nil {
				return errors.New("update decision missing target id")
			}

			// updated_at is not passed here: the Store adapter always stamps
			// it itself and rejects it as an update column, so including it
			// failed every UPDATE/SUPERSEDE/NOOP against real Postgres.
			updates := map[string]interface{}{
				"title":     scrubbedTitle,
				"content":   scrubbedContent,
				"embedding": embedding,
			}

			if len(req.Tags) > 0 {
				updates["tags"] = req.Tags
			}
			if len(req.Files) > 0 {
				updates["files"] = req.Files
				// New files re-baseline the record; without them the
				// baseline is unchanged.
				if commitSHA != nil {
					updates["commit_sha"] = *commitSHA
				}
			} else if req.CommitSHA != nil {
				updates["commit_sha"] = *req.CommitSHA
			}

			// Update without changing status; the old record stays as-is.
			_, updateErr := tx.Update(ctx, *targetID, updates)
			if updateErr != nil {
				return fmt.Errorf("update record: %w", updateErr)
			}

			recordID = *targetID
			ev := s.event(ns, EventRecordUpdated, *targetID)
			ev.Source = EventSource(req.Source)
			evs = append(evs, ev)

		case ActionSupersede:
			// Deprecate the old record and create a new one.
			if targetID == nil {
				return errors.New("supersede decision missing target id")
			}

			// Deprecate the old record.
			reason := fmt.Sprintf("Superseded by newer fact: %s", scrubbedTitle)
			deprecateUpdates := map[string]interface{}{
				"status":             record.StatusDeprecated,
				"deprecation_reason": reason,
				// superseded_by is set below, once the new record's ID exists
				// (an empty string is not a valid UUID for that column).
			}

			_, deprecateErr := tx.Update(ctx, *targetID, deprecateUpdates)
			if deprecateErr != nil {
				return fmt.Errorf("deprecate old record: %w", deprecateErr)
			}

			// Create the new record.
			newRec := record.New(
				uuid.New().String(),
				req.Kind,
				scrubbedTitle,
				scrubbedContent,
				req.Repo,
				req.Source,
				defaultConfidenceForSource(req.Source, req.Confidence),
			)
			newRec.Namespace = ns

			// Set optional fields.
			if len(req.Files) > 0 {
				newRec.Files = req.Files
			}
			if commitSHA != nil {
				newRec.CommitSHA = commitSHA
			}
			if req.Ticket != nil {
				newRec.Ticket = req.Ticket
			}
			if len(req.Tags) > 0 {
				newRec.Tags = req.Tags
			}

			// Set initial status based on source (AC-29).
			if req.Source == record.SourcePR {
				newRec.Status = record.StatusActive
				newRec.Confidence = 0.75
			} else {
				newRec.Status = record.StatusCandidate
				newRec.Confidence = 0.5
			}

			// Set embedding.
			newRec.Embedding = embedding
			newRec.UpdatedAt = s.clock.Now()

			// Persist the new record.
			createdRec, createErr := tx.Create(ctx, newRec)
			if createErr != nil {
				return fmt.Errorf("create superseding record: %w", createErr)
			}

			// Update the old record's superseded_by field now that we have the new ID.
			updateOldRec := map[string]interface{}{
				"superseded_by": createdRec.ID,
			}
			_, updateOldErr := tx.Update(ctx, *targetID, updateOldRec)
			if updateOldErr != nil {
				return fmt.Errorf("set superseded_by on old record: %w", updateOldErr)
			}

			recordID = createdRec.ID
			sup := s.event(ns, EventRecordSuperseded, *targetID)
			sup.RelatedID = createdRec.ID
			sup.Source = EventSource(req.Source)
			evs = append(evs, sup, s.createdEvent(ns, createdRec.ID, req.Source, newRec.Status))

		case ActionNoop:
			// Increment seen_count on the existing record (AC-15).
			// If seen_count reaches 2, promote candidate→active (AC-34).
			if targetID == nil {
				return errors.New("noop decision missing target id")
			}

			// Fetch the current record to get the current seen_count.
			currentRec, getErr := tx.Get(ctx, *targetID)
			if getErr != nil {
				return fmt.Errorf("get record for noop: %w", getErr)
			}

			newSeenCount := currentRec.SeenCount + 1
			updates := map[string]interface{}{
				"seen_count": newSeenCount,
			}

			// Promote candidate→active if seen_count >= 2 (AC-34).
			if currentRec.Status == record.StatusCandidate && newSeenCount >= 2 {
				updates["status"] = record.StatusActive
				ev := s.event(ns, EventRecordPromoted, *targetID)
				ev.Via = ViaSeen
				evs = append(evs, ev)
				slog.InfoContext(ctx, "promoting candidate to active via seen_count threshold",
					"id", *targetID, "seen_count", newSeenCount)
			}

			_, updateErr := tx.Update(ctx, *targetID, updates)
			if updateErr != nil {
				return fmt.Errorf("increment seen_count: %w", updateErr)
			}

			recordID = *targetID
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("write transaction failed: %w", err)
	}

	// The transaction committed: record its events (a NOOP that did not
	// promote has none).
	s.appendEvents(ctx, evs...)

	// If the inline source was in judgment range, return candidates without writing.
	// The caller should re-call Store with an ExtractionDecision to proceed (AC-15).
	if inJudgmentRange {
		return &StoreResponse{
			Namespace:            ns,
			ID:                   "",        // No record created; caller will decide.
			Decision:             ActionAdd, // Default decision (caller may override).
			CandidatesConsidered: candidates,
		}, nil
	}

	return &StoreResponse{
		Namespace:            ns,
		ID:                   recordID,
		Decision:             decision,
		CandidatesConsidered: candidates,
	}, nil
}

// createdEvent builds the record_created event of a new row.
func (s *Service) createdEvent(ns, id string, src record.Source, status record.Status) Event {
	ev := s.event(ns, EventRecordCreated, id)
	ev.Source = EventSource(src)
	ev.Status = status
	return ev
}

// decideWriteAction determines whether to ADD/UPDATE/SUPERSEDE/NOOP based on
// the source and top candidates. For inline sources in the
// StoreSimAsk-StoreSimUpdate judgment range, this returns nil error but
// signals via the decision that candidates were returned for the caller to
// judge (caller should then re-call with ExtractionDecision).
func (s *Service) decideWriteAction(
	ctx context.Context,
	req *StoreRequest,
	candidates []*Candidate,
	normalizedTitle string,
) (WriteAction, *string, error) {
	// If no candidates, it's always ADD.
	if len(candidates) == 0 {
		return ActionAdd, nil, nil
	}

	// An explicit decision wins for every source: session/pr extraction
	// (AC-14) and an inline caller resolving a needs_judgment response (AC-15).
	// Targets are re-validated against the fresh top-5.
	if req.ExtractionDecision != nil {
		// Validate that the target ID is still in the fresh top-5.
		if req.ExtractionDecision.TargetID != nil {
			found := false
			for _, cand := range candidates {
				if cand.ID == *req.ExtractionDecision.TargetID {
					found = true
					break
				}
			}
			if !found {
				// Target ID is no longer in top-5; fall back to ADD.
				slog.WarnContext(ctx, "extraction target id not in fresh top-5; falling back to ADD",
					"target_id", *req.ExtractionDecision.TargetID)
				return ActionAdd, nil, nil
			}
		}
		return req.ExtractionDecision.Action, req.ExtractionDecision.TargetID, nil
	}

	topCandidate := candidates[0]

	// For inline sources: threshold-based decision (AC-15).
	if req.Source == record.SourceInline {
		if topCandidate.Similarity >= s.cfg.StoreSimUpdate {
			// >= StoreSimUpdate: NOOP or UPDATE depending on content enrichment.
			// For simplicity, use NOOP (increment seen_count) if content is identical,
			// or UPDATE if new content adds information.
			// Since we can't reliably detect "enrichment" without more context,
			// we default to NOOP. The caller can override by passing ExtractionDecision.
			return ActionNoop, &topCandidate.ID, nil
		} else if topCandidate.Similarity >= s.cfg.StoreSimAsk {
			// Between StoreSimAsk and StoreSimUpdate: return candidates to the
			// caller for judgment.
			// Return ADD as the default decision but signal via candidates that
			// the caller should review and potentially override with ExtractionDecision.
			// We don't write anything here; the caller decides.
			// Signal this by returning a special marker, but since WriteAction doesn't
			// have a "ASK" variant, we return ActionAdd with a special flag.
			// The presence of candidates in StoreResponse signals the caller to decide.
			slog.InfoContext(ctx, "inline store: candidates in judgment range, returning for caller decision",
				"similarity", topCandidate.Similarity, "threshold_ask", s.cfg.StoreSimAsk)
			return ActionAdd, nil, nil
		}
		// < StoreSimAsk: ADD.
		return ActionAdd, nil, nil
	}

	// No explicit extraction decision provided; default to ADD for session/pr sources.
	return ActionAdd, nil, nil
}

// computeLockKey produces a deterministic lock key from repo and normalized title.
// Per AC-16: repo + hash(normalized_title_hash).
func computeLockKey(repo string, title string) string {
	// Normalize the title: lowercase, collapse whitespace, trim.
	normalized := strings.ToLower(strings.TrimSpace(title))
	normalized = strings.Join(strings.Fields(normalized), " ")

	// Hash the normalized title.
	hash := md5.Sum([]byte(normalized))

	// Combine repo and hash into a lock key.
	// The lock key is used by postgres advisory_lock; it should be reasonably short.
	return fmt.Sprintf("%s:%x", repo, hash[:8]) // Use first 8 bytes of hash.
}

// defaultConfidenceForSource returns the initial confidence for a record
// based on its source, or uses the provided override if given (AC-29).
func defaultConfidenceForSource(source record.Source, override *float64) float64 {
	if override != nil {
		return *override
	}

	switch source {
	case record.SourcePR:
		return 0.75 // Higher confidence for PR-sourced records.
	case record.SourceInline, record.SourceSession:
		return 0.5 // Lower confidence for inline/session sources.
	default:
		return 0.5
	}
}

// invalidRequest builds a validation error that wraps ErrInvalidRequest.
func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, args...)...)
}

// validate checks that the StoreRequest is valid. Every error wraps
// ErrInvalidRequest.
func (req *StoreRequest) validate(maxContentChars int) error {
	if req.Kind == "" || !req.Kind.IsValid() {
		return invalidRequest("invalid or missing kind")
	}

	if req.Title == "" {
		return invalidRequest("title is required")
	}

	if req.Content == "" {
		return invalidRequest("content is required")
	}

	if len(req.Content) > maxContentChars {
		return invalidRequest("content exceeds maximum size (%d > %d)", len(req.Content), maxContentChars)
	}

	if req.Repo == "" {
		return invalidRequest("repo is required")
	}

	if req.Namespace != "" && req.Namespace != record.GlobalNamespace {
		return invalidRequest("namespace may only be %q (or empty for the current namespace)", record.GlobalNamespace)
	}

	if req.Source == "" || !req.Source.IsValid() {
		return invalidRequest("invalid or missing source")
	}

	if (req.Source == record.SourceImport) != (req.ImportKey != "") {
		return invalidRequest("import key is required for, and only valid with, source import")
	}

	if req.ExtractionDecision != nil {
		if req.Source == record.SourceImport {
			return invalidRequest("an import takes no extraction decision")
		}
		if req.ExtractionDecision.Action == ActionSkip {
			return invalidRequest("SKIP is an outcome, not a decision")
		}
	}

	return nil
}
