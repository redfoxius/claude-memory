package memory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func relEvent(t EventType, via EventVia, outcome FeedbackOutcome, class ErrorClass) Event {
	e := NewEvent(time.Now(), "acme", t)
	e.Via, e.Outcome, e.ErrorClass = via, outcome, class
	return e
}

func TestValidateReliability(t *testing.T) {
	now := time.Now()
	f, b, n := 0.5, true, 3
	withField := func(mut func(*Event)) Event {
		e := relEvent(EventSearchCalled, ViaMCP, OutcomeOK, "")
		mut(&e)
		return e
	}
	cases := []struct {
		name string
		e    Event
		ok   bool
	}{
		{"search ok mcp", relEvent(EventSearchCalled, ViaMCP, OutcomeOK, ""), true},
		{"search degraded hook", relEvent(EventSearchCalled, ViaHook, OutcomeDegraded, ""), true},
		{"search error db", relEvent(EventSearchCalled, ViaHook, OutcomeError, ErrClassDBUnavailable), true},
		{"store added", relEvent(EventStoreAttempted, ViaMCP, OutcomeAdded, ""), true},
		{"store updated", relEvent(EventStoreAttempted, ViaMCP, OutcomeUpdated, ""), true},
		{"store superseded", relEvent(EventStoreAttempted, ViaMCP, OutcomeSuperseded, ""), true},
		{"store noop", relEvent(EventStoreAttempted, ViaMCP, OutcomeNoop, ""), true},
		{"store needs_judgment", relEvent(EventStoreAttempted, ViaMCP, OutcomeNeedsJudgment, ""), true},
		{"store error timeout", relEvent(EventStoreAttempted, ViaMCP, OutcomeError, ErrClassTimeout), true},
		{"search with store outcome", relEvent(EventSearchCalled, ViaMCP, OutcomeAdded, ""), false},
		{"store with search outcome", relEvent(EventStoreAttempted, ViaMCP, OutcomeOK, ""), false},
		{"store degraded", relEvent(EventStoreAttempted, ViaMCP, OutcomeDegraded, ""), false},
		{"feedback outcome", relEvent(EventSearchCalled, ViaMCP, FeedbackUseful, ""), false},
		{"empty outcome", relEvent(EventSearchCalled, ViaMCP, "", ""), false},
		{"error without class", relEvent(EventSearchCalled, ViaMCP, OutcomeError, ""), false},
		{"error bad class", relEvent(EventSearchCalled, ViaMCP, OutcomeError, "boom"), false},
		{"class without error", relEvent(EventSearchCalled, ViaMCP, OutcomeOK, ErrClassInternal), false},
		{"via tool", relEvent(EventSearchCalled, ViaTool, OutcomeOK, ""), false},
		{"via empty", relEvent(EventSearchCalled, "", OutcomeOK, ""), false},
		{"record_id set", withField(func(e *Event) { e.RecordID = uuid.New().String() }), false},
		{"related_id set", withField(func(e *Event) { e.RelatedID = uuid.New().String() }), false},
		{"source set", withField(func(e *Event) { e.Source = EventSourceInline }), false},
		{"status set", withField(func(e *Event) { e.Status = "active" }), false},
		{"similarity set", withField(func(e *Event) { e.Similarity = &f }), false},
		{"stale set", withField(func(e *Event) { e.Stale = &b }), false},
		{"stale_commits set", withField(func(e *Event) { e.StaleCommits = &n }), false},
		{"session ok", withField(func(e *Event) { e.SessionID = uuid.New().String() }), true},
		{"session bad", withField(func(e *Event) { e.SessionID = "../x" }), false},
	}
	for _, tc := range cases {
		err := tc.e.Validate(now)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// Existing types reject the new outcome, via and error_class values.
func TestValidateOldTypesRejectReliabilityValues(t *testing.T) {
	now := time.Now()
	mk := func(mut func(*Event)) Event {
		e := NewEvent(now, "acme", EventFeedback)
		e.RecordID = uuid.New().String()
		mut(&e)
		return e
	}
	for name, e := range map[string]Event{
		"outcome ok":   mk(func(e *Event) { e.Outcome = OutcomeOK }),
		"via mcp":      mk(func(e *Event) { e.Via = ViaMCP }),
		"via hook":     mk(func(e *Event) { e.Via = ViaHook }),
		"error_class":  mk(func(e *Event) { e.ErrorClass = ErrClassInternal }),
		"outcome noop": mk(func(e *Event) { e.Outcome = OutcomeNoop }),
	} {
		if err := e.Validate(now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := mk(func(e *Event) { e.Outcome = FeedbackUseful }).Validate(now); err != nil {
		t.Errorf("feedback useful rejected: %v", err)
	}
}

func TestClassifyError(t *testing.T) {
	down := errors.New("pg down")
	isDown := func(err error) bool { return errors.Is(err, down) }
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"db down", fmt.Errorf("search: %w", down), ErrClassDBUnavailable},
		// A dbDown match wins over a wrapped deadline.
		{"db down wrapping deadline", fmt.Errorf("%w: %w", down, context.DeadlineExceeded), ErrClassDBUnavailable},
		{"invalid request", fmt.Errorf("store validation: %w", ErrInvalidRequest), ErrClassInvalidRequest},
		{"embedding", fmt.Errorf("%w: %w", ErrEmbeddingUnavailable, errors.New("ollama refused")), ErrClassEmbeddingUnavailable},
		// The embedder's own network failure is not a database outage.
		{"embedding wrapping a db-looking error", fmt.Errorf("%w: %w", ErrEmbeddingUnavailable, down), ErrClassEmbeddingUnavailable},
		{"embedding deadline", fmt.Errorf("%w: %w", ErrEmbeddingUnavailable, context.DeadlineExceeded), ErrClassEmbeddingUnavailable},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), ErrClassTimeout},
		{"other", errors.New("boom"), ErrClassInternal},
		{"canceled (callers skip the event)", context.Canceled, ErrClassInternal},
	}
	for _, tc := range cases {
		if got := ClassifyError(tc.err, isDown); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := ClassifyError(down, nil); got != ErrClassInternal {
		t.Errorf("nil dbDown: %q", got)
	}
}

func TestIsCanceled(t *testing.T) {
	if !IsCanceled(fmt.Errorf("x: %w", context.Canceled)) || IsCanceled(context.DeadlineExceeded) || IsCanceled(nil) {
		t.Error("IsCanceled wrong")
	}
}

// The embed wrap keeps the message text unchanged.
func TestEmbeddingWrapMessage(t *testing.T) {
	err := fmt.Errorf("%w: %w", ErrEmbeddingUnavailable, errors.New("conn refused"))
	if err.Error() != "embedding provider unavailable: conn refused" {
		t.Errorf("message = %q", err.Error())
	}
}
