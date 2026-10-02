package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"

	"claude-memory/internal/record"
)

// EventSink is the port through which usage and lifecycle events are
// recorded. Events hold ids, enums and numbers only, never content. The
// adapters are the Postgres store (service events, spool drain) and the
// JSONL spool (hook). Appending is best-effort: callers never let a failure
// change the result of the operation that produced the event.
type EventSink interface {
	Append(ctx context.Context, evs ...Event) error
}

// ErrEventRejected is returned by a sink when the store rejected an event
// with a data or constraint error: retrying the same event can never
// succeed (the spool drain quarantines such rows).
var ErrEventRejected = errors.New("event rejected")

// NopEventSink drops every event. It is the service's default sink.
type NopEventSink struct{}

// Append implements EventSink.
func (NopEventSink) Append(context.Context, ...Event) error { return nil }

// EventType is the kind of an event.
type EventType string

const (
	EventCardInjected     EventType = "card_injected"
	EventFeedback         EventType = "feedback"
	EventRecordCreated    EventType = "record_created"
	EventRecordUpdated    EventType = "record_updated"
	EventRecordSuperseded EventType = "record_superseded"
	EventRecordDeprecated EventType = "record_deprecated"
	EventRecordPromoted   EventType = "record_promoted"
	EventRecordDeleted    EventType = "record_deleted"
)

// EventSource is where the change behind a lifecycle event came from: a
// record source, or "cleanup" for TTL deletes.
type EventSource string

const (
	EventSourceInline  EventSource = EventSource(record.SourceInline)
	EventSourceSession EventSource = EventSource(record.SourceSession)
	EventSourcePR      EventSource = EventSource(record.SourcePR)
	EventSourceImport  EventSource = EventSource(record.SourceImport)
	EventSourceCleanup EventSource = "cleanup"
)

// EventVia is the mechanism behind a promotion, deprecation or deletion.
type EventVia string

const (
	ViaTool     EventVia = "tool"     // memory_update / memory_deprecate
	ViaFeedback EventVia = "feedback" // memory_feedback
	ViaSeen     EventVia = "seen"     // seen_count reached 2
	ViaTTL      EventVia = "ttl"      // cleanup
)

// Event is one usage or lifecycle fact. Its fields are the columns of the
// events table (migration 0003) and nothing else: no title, content, repo,
// file path, note, reason or query text may ever be added (AC-14).
type Event struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Namespace string    `json:"namespace"`
	Type      EventType `json:"type"`
	RecordID  string    `json:"record_id,omitempty"`
	// RelatedID is the superseding record (record_superseded only).
	RelatedID    string          `json:"related_id,omitempty"`
	Source       EventSource     `json:"source,omitempty"`
	Status       record.Status   `json:"status,omitempty"`
	Outcome      FeedbackOutcome `json:"outcome,omitempty"`
	Via          EventVia        `json:"via,omitempty"`
	Similarity   *float64        `json:"similarity,omitempty"`
	Stale        *bool           `json:"stale,omitempty"`         // nil = unchecked
	StaleCommits *int            `json:"stale_commits,omitempty"` // nil = unknown
	SessionID    string          `json:"session_id,omitempty"`    // hook only
}

// NewEvent returns an event of the given type with a fresh client-generated
// id (the drain inserts idempotently by id).
func NewEvent(at time.Time, namespace string, t EventType) Event {
	return Event{ID: uuid.New().String(), At: at.UTC(), Namespace: namespace, Type: t}
}

const (
	// eventMaxAge and eventMaxFuture bound a plausible event time.
	eventMaxAge    = 400 * 24 * time.Hour
	eventMaxFuture = time.Hour
	// maxStaleCommits is the cap of the rev-list count the adapter reports.
	maxStaleCommits = 100
)

var eventSessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Validate checks the event against the same rules as the table's CHECK
// constraints and more (UUIDs, time window, ranges). The spool drain calls it
// on every line it reads, so a deterministic reject never reaches Postgres.
func (e Event) Validate(now time.Time) error {
	if _, err := uuid.Parse(e.ID); err != nil {
		return fmt.Errorf("invalid id: %w", err)
	}
	if e.At.Before(now.Add(-eventMaxAge)) || e.At.After(now.Add(eventMaxFuture)) {
		return errors.New("at outside the accepted window")
	}
	if e.Namespace == "" {
		return errors.New("namespace is required")
	}
	switch e.Type {
	case EventCardInjected, EventFeedback, EventRecordCreated, EventRecordUpdated,
		EventRecordSuperseded, EventRecordDeprecated, EventRecordPromoted, EventRecordDeleted:
	default:
		return fmt.Errorf("unknown type %q", e.Type)
	}
	if _, err := uuid.Parse(e.RecordID); err != nil {
		return fmt.Errorf("invalid record_id: %w", err)
	}
	if e.RelatedID != "" {
		if _, err := uuid.Parse(e.RelatedID); err != nil {
			return fmt.Errorf("invalid related_id: %w", err)
		}
	}
	switch e.Source {
	case "", EventSourceInline, EventSourceSession, EventSourcePR, EventSourceImport, EventSourceCleanup:
	default:
		return fmt.Errorf("unknown source %q", e.Source)
	}
	if e.Status != "" && !e.Status.IsValid() {
		return fmt.Errorf("unknown status %q", e.Status)
	}
	switch e.Outcome {
	case "", FeedbackUseful, FeedbackOutdated, FeedbackWrong:
	default:
		return fmt.Errorf("unknown outcome %q", e.Outcome)
	}
	switch e.Via {
	case "", ViaTool, ViaFeedback, ViaSeen, ViaTTL:
	default:
		return fmt.Errorf("unknown via %q", e.Via)
	}
	if e.Similarity != nil && (math.IsNaN(*e.Similarity) || math.IsInf(*e.Similarity, 0)) {
		return errors.New("similarity is not finite")
	}
	if e.StaleCommits != nil && (*e.StaleCommits < 0 || *e.StaleCommits > maxStaleCommits) {
		return errors.New("stale_commits out of range")
	}
	if e.SessionID != "" && !eventSessionIDRe.MatchString(e.SessionID) {
		return errors.New("invalid session_id")
	}
	return nil
}

// UsefulAttributionWindow is how long after a card was injected a
// feedback(useful) on its record still counts as that card being useful
// (the "precision proxy" in `stats`).
const UsefulAttributionWindow = 2 * time.Hour

// EventCounts are the raw, grouped counts `stats` computes its ratios from.
// The store fills them from the events table (and the records inventory) for
// one namespace, or for all of them.
type EventCounts struct {
	Cards              int                       `json:"cards_injected"`
	CardRecords        int                       `json:"cards_distinct_records"`
	CardsChecked       int                       `json:"cards_checked"`       // stale IS NOT NULL
	CardsStale         int                       `json:"cards_stale"`         // stale = true
	UsefulWithin       int                       `json:"useful_within_2h"`    // distinct injected records, useful within the window
	UsefulAny          int                       `json:"useful_any"`          // distinct injected records, useful at/after injection
	Feedback           map[string]int            `json:"feedback"`            // by outcome
	CreatedBySource    map[string]int            `json:"created_by_source"`   // record_created by source
	CandidatesCreated  int                       `json:"candidates_created"`  // record_created with status candidate, source other than import
	CandidatesPromoted int                       `json:"candidates_promoted"` // of those, with a later record_promoted
	ImportCreated      int                       `json:"import_created"`      // the same pair for source import
	ImportPromoted     int                       `json:"import_promoted"`
	Superseded         int                       `json:"superseded"`
	DeprecatedByVia    map[string]int            `json:"deprecated_by_via"`
	TTLDeleted         int                       `json:"ttl_deleted"`
	Inventory          map[string]map[string]int `json:"inventory"` // records now: source -> status -> rows
}

// WithEvents returns a copy of the service that records events through sink.
func (s *Service) WithEvents(sink EventSink) *Service {
	c := *s
	c.events = sink
	return &c
}

var eventFailureOnce sync.Once

// appendEvents records evs after the operation that produced them has
// committed. A failure is logged once per process and otherwise ignored.
func (s *Service) appendEvents(ctx context.Context, evs ...Event) {
	if s.events == nil || len(evs) == 0 {
		return
	}
	if err := s.events.Append(ctx, evs...); err != nil {
		eventFailureOnce.Do(func() {
			slog.WarnContext(ctx, "recording events failed; further failures are not logged", "error", err)
		})
	}
}

// event builds an event stamped with the service clock.
func (s *Service) event(namespace string, t EventType, recordID string) Event {
	e := NewEvent(s.clock.Now(), namespace, t)
	e.RecordID = recordID
	return e
}
