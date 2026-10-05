package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/redfoxius/claude-memory/internal/record"
)

// Feedback records user feedback on a record and applies lifecycle transitions.
// - feedback(useful) or seen_count >= 2 promotes candidate→active (AC-12, AC-34).
// - feedback(outdated|wrong) deprecates with reason (AC-36).
// Returns the updated record or ErrNotFound if the record does not exist (AC-10).
func (s *Service) Feedback(ctx context.Context, req *FeedbackRequest) (*FeedbackResponse, error) {
	if req == nil {
		return nil, errors.New("feedback request is required")
	}

	if req.ID == "" {
		return nil, errors.New("id is required")
	}

	if req.Outcome == "" {
		return nil, errors.New("outcome is required")
	}

	// Fetch the current record.
	rec, err := s.getAccessible(ctx, req.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("fetch record for feedback: %w", err)
	}

	// Determine the new status based on outcome and current status.
	var newStatus record.Status

	switch req.Outcome {
	case FeedbackUseful:
		// Useful feedback: promote candidate→active (AC-34).
		if rec.Status == record.StatusCandidate {
			newStatus = record.StatusActive
			slog.InfoContext(ctx, "promoting candidate to active via feedback",
				"id", rec.ID, "outcome", FeedbackUseful)
		} else {
			// Already active or deprecated; no change.
			newStatus = rec.Status
		}

		// Increment used_count.
		updates := map[string]interface{}{
			"used_count":   rec.UsedCount + 1,
			"status":       newStatus,
			"last_used_at": s.clock.Now(),
		}

		updatedRec, updateErr := s.store.Update(ctx, req.ID, updates)
		if updateErr != nil {
			return nil, fmt.Errorf("update record after feedback: %w", updateErr)
		}

		fb := s.event(s.namespace, EventFeedback, rec.ID)
		fb.Outcome = req.Outcome
		evs := []Event{fb}
		if rec.Status == record.StatusCandidate {
			evs = append(evs, s.transitionEvent(rec.Namespace, EventRecordPromoted, rec.ID, ViaFeedback))
		}
		s.appendEvents(ctx, evs...)

		return &FeedbackResponse{
			ID:        updatedRec.ID,
			NewStatus: updatedRec.Status,
		}, nil

	case FeedbackOutdated, FeedbackWrong:
		// Outdated or wrong: deprecate with reason (AC-36).
		reason := formatDeprecationReason(req.Outcome, req.Note)

		updates := map[string]interface{}{
			"status":             record.StatusDeprecated,
			"deprecation_reason": reason,
		}

		updatedRec, updateErr := s.store.Update(ctx, req.ID, updates)
		if updateErr != nil {
			return nil, fmt.Errorf("deprecate record after feedback: %w", updateErr)
		}

		slog.InfoContext(ctx, "deprecated record via feedback",
			"id", rec.ID, "outcome", req.Outcome, "reason", reason)

		fb := s.event(s.namespace, EventFeedback, rec.ID)
		fb.Outcome = req.Outcome
		evs := []Event{fb}
		if rec.Status != record.StatusDeprecated {
			evs = append(evs, s.transitionEvent(rec.Namespace, EventRecordDeprecated, rec.ID, ViaFeedback))
		}
		s.appendEvents(ctx, evs...)

		return &FeedbackResponse{
			ID:        updatedRec.ID,
			NewStatus: updatedRec.Status,
		}, nil

	default:
		return nil, fmt.Errorf("unknown feedback outcome: %s", req.Outcome)
	}
}

// formatDeprecationReason creates a deprecation reason string from feedback outcome and note.
func formatDeprecationReason(outcome FeedbackOutcome, note *string) string {
	switch outcome {
	case FeedbackOutdated:
		if note != nil && *note != "" {
			return fmt.Sprintf("Marked as outdated: %s", *note)
		}
		return "Marked as outdated"
	case FeedbackWrong:
		if note != nil && *note != "" {
			return fmt.Sprintf("Marked as wrong: %s", *note)
		}
		return "Marked as wrong"
	default:
		return "Deprecated via feedback"
	}
}

// transitionEvent builds a record_promoted / record_deprecated event.
func (s *Service) transitionEvent(ns string, t EventType, id string, via EventVia) Event {
	ev := s.event(ns, t, id)
	ev.Via = via
	return ev
}
