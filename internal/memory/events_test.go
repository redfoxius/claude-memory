package memory

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"claude-memory/internal/record"
)

// recSink records appended events; err makes Append fail.
type recSink struct {
	evs []Event
	err error
}

func (r *recSink) Append(_ context.Context, evs ...Event) error {
	if r.err != nil {
		return r.err
	}
	r.evs = append(r.evs, evs...)
	return nil
}

// types lists the recorded event types (with via for transitions) for compact asserts.
func (r *recSink) types() []string {
	var out []string
	for _, e := range r.evs {
		s := string(e.Type)
		if e.Via != "" {
			s += "/" + string(e.Via)
		}
		out = append(out, s)
	}
	return out
}

func eqStrings(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func eventSvc(store Store, sink EventSink) *Service {
	return New(store, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg()).
		WithNamespace("acme").WithEvents(sink)
}

// AC-14: Event holds ids, enums and numbers only.
func TestEventHasOnlyAllowedFields(t *testing.T) {
	allowed := []string{"ID", "At", "Namespace", "Type", "RecordID", "RelatedID", "Source",
		"Status", "Outcome", "Via", "Similarity", "Stale", "StaleCommits", "SessionID", "ErrorClass"}
	var got []string
	rt := reflect.TypeOf(Event{})
	for i := 0; i < rt.NumField(); i++ {
		got = append(got, rt.Field(i).Name)
	}
	sort.Strings(got)
	sort.Strings(allowed)
	if !reflect.DeepEqual(got, allowed) {
		t.Errorf("Event fields = %v, want exactly %v", got, allowed)
	}
}

func TestEventValidate(t *testing.T) {
	now := time.Now()
	ok := func() Event {
		e := NewEvent(now, "acme", EventCardInjected)
		e.RecordID = uuid.New().String()
		return e
	}
	if err := ok().Validate(now); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	sim := 0.7
	n101, n100 := 101, 100
	cases := map[string]func(e *Event){
		"bad id":           func(e *Event) { e.ID = "x" },
		"too old":          func(e *Event) { e.At = now.Add(-401 * 24 * time.Hour) },
		"too far future":   func(e *Event) { e.At = now.Add(2 * time.Hour) },
		"no namespace":     func(e *Event) { e.Namespace = "" },
		"unknown type":     func(e *Event) { e.Type = "nope" },
		"no record id":     func(e *Event) { e.RecordID = "" },
		"bad related id":   func(e *Event) { e.RelatedID = "x" },
		"unknown source":   func(e *Event) { e.Source = "x" },
		"unknown status":   func(e *Event) { e.Status = "x" },
		"unknown outcome":  func(e *Event) { e.Outcome = "x" },
		"unknown via":      func(e *Event) { e.Via = "x" },
		"commits too high": func(e *Event) { e.StaleCommits = &n101 },
		"bad session id":   func(e *Event) { e.SessionID = "a/b" },
	}
	for name, mut := range cases {
		e := ok()
		mut(&e)
		if e.Validate(now) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	e := ok()
	e.Similarity, e.StaleCommits, e.SessionID, e.Via, e.Source = &sim, &n100, "sess_1-A", ViaSeen, EventSourceCleanup
	if err := e.Validate(now); err != nil {
		t.Errorf("valid edge values rejected: %v", err)
	}
}

// AC-19: ADD records record_created; the namespace is the written one.
func TestStoreEvents_Add(t *testing.T) {
	store := &mockStore{CreateFunc: func(_ context.Context, r *record.Record) (*record.Record, error) { return r, nil }}
	sink := &recSink{}
	req := baseStoreRequest()
	req.Namespace = record.GlobalNamespace
	if _, err := eventSvc(store, sink).Store(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, sink.types(), []string{"record_created"})
	e := sink.evs[0]
	if e.Namespace != record.GlobalNamespace || e.Source != EventSourceInline || e.Status != record.StatusCandidate || e.RecordID == "" {
		t.Errorf("event = %+v", e)
	}
}

func evDecisionStore(candID string, rec *record.Record) *mockStore {
	return &mockStore{
		FindCandidatesFunc: func(context.Context, []float32, string, int) ([]*Candidate, error) {
			return []*Candidate{{ID: candID, Similarity: 0.5}}, nil
		},
		GetFunc:    func(context.Context, string) (*record.Record, error) { return rec, nil },
		CreateFunc: func(_ context.Context, r *record.Record) (*record.Record, error) { return r, nil },
		UpdateFunc: func(_ context.Context, id string, _ map[string]interface{}) (*record.Record, error) {
			return &record.Record{ID: id}, nil
		},
	}
}

func TestStoreEvents_UpdateSupersedeNoop(t *testing.T) {
	target := uuid.New().String()
	run := func(t *testing.T, action WriteAction, cur *record.Record, src record.Source) *recSink {
		t.Helper()
		sink := &recSink{}
		req := baseStoreRequest()
		req.Source = src
		req.ExtractionDecision = &ExtractionDecision{Action: action, TargetID: &target}
		if _, err := eventSvc(evDecisionStore(target, cur), sink).Store(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		return sink
	}

	s := run(t, ActionUpdate, nil, record.SourceSession)
	eqStrings(t, s.types(), []string{"record_updated"})
	if s.evs[0].Source != EventSourceSession || s.evs[0].RecordID != target {
		t.Errorf("update event = %+v", s.evs[0])
	}

	s = run(t, ActionSupersede, nil, record.SourcePR)
	eqStrings(t, s.types(), []string{"record_superseded", "record_created"})
	if s.evs[0].RecordID != target || s.evs[0].RelatedID != s.evs[1].RecordID || s.evs[1].Status != record.StatusActive {
		t.Errorf("supersede events = %+v", s.evs)
	}

	// NOOP promotes only candidate && seen_count >= 2 after the increment.
	s = run(t, ActionNoop, &record.Record{Status: record.StatusCandidate, SeenCount: 1}, record.SourceSession)
	eqStrings(t, s.types(), []string{"record_promoted/seen"})
	s = run(t, ActionNoop, &record.Record{Status: record.StatusCandidate, SeenCount: 0}, record.SourceSession)
	eqStrings(t, s.types(), nil)
	s = run(t, ActionNoop, &record.Record{Status: record.StatusActive, SeenCount: 5}, record.SourceSession)
	eqStrings(t, s.types(), nil)
}

// AC-19: rollback and needs_judgment record nothing.
func TestStoreEvents_RollbackAndJudgmentRecordNothing(t *testing.T) {
	sink := &recSink{}
	store := &mockStore{
		CreateFunc: func(context.Context, *record.Record) (*record.Record, error) { return nil, errors.New("boom") },
	}
	if _, err := eventSvc(store, sink).Store(context.Background(), baseStoreRequest()); err == nil {
		t.Fatal("expected the write to fail")
	}
	eqStrings(t, sink.types(), nil)

	store = &mockStore{FindCandidatesFunc: func(context.Context, []float32, string, int) ([]*Candidate, error) {
		return []*Candidate{{ID: "c", Similarity: 0.7}}, nil // inside the ask..update range
	}}
	if _, err := eventSvc(store, sink).Store(context.Background(), baseStoreRequest()); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, sink.types(), nil)
}

// AC-22: a failing sink changes no response.
func TestStoreEvents_FailingSinkIdenticalResponses(t *testing.T) {
	mk := func() *mockStore {
		return &mockStore{CreateFunc: func(_ context.Context, r *record.Record) (*record.Record, error) { r.ID = "fixed"; return r, nil }}
	}
	good, err := eventSvc(mk(), &recSink{}).Store(context.Background(), baseStoreRequest())
	if err != nil {
		t.Fatal(err)
	}
	bad, err := eventSvc(mk(), &recSink{err: errors.New("table missing")}).Store(context.Background(), baseStoreRequest())
	if err != nil {
		t.Fatalf("Store failed with a failing sink: %v", err)
	}
	if good.Decision != bad.Decision || good.Namespace != bad.Namespace || len(good.CandidatesConsidered) != len(bad.CandidatesConsidered) {
		t.Errorf("responses differ: %+v vs %+v", good, bad)
	}
}

func recStore(status record.Status, ns string) *mockStore {
	return &mockStore{
		GetFunc: func(_ context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: ns, Status: status}, nil
		},
		UpdateFunc: func(_ context.Context, id string, u map[string]interface{}) (*record.Record, error) {
			st := status
			if v, ok := u["status"].(record.Status); ok {
				st = v
			}
			return &record.Record{ID: id, Namespace: ns, Status: st}, nil
		},
	}
}

// AC-18: feedback events and the transitions that count.
func TestFeedbackEvents(t *testing.T) {
	cases := []struct {
		name    string
		before  record.Status
		outcome FeedbackOutcome
		want    []string
	}{
		{"useful on candidate", record.StatusCandidate, FeedbackUseful, []string{"feedback", "record_promoted/feedback"}},
		{"useful on active", record.StatusActive, FeedbackUseful, []string{"feedback"}},
		{"useful on deprecated", record.StatusDeprecated, FeedbackUseful, []string{"feedback"}},
		{"outdated on active", record.StatusActive, FeedbackOutdated, []string{"feedback", "record_deprecated/feedback"}},
		{"wrong on candidate", record.StatusCandidate, FeedbackWrong, []string{"feedback", "record_deprecated/feedback"}},
		{"outdated on deprecated", record.StatusDeprecated, FeedbackOutdated, []string{"feedback"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recSink{}
			// The record lives in "global"; feedback is given from "acme".
			svc := eventSvc(recStore(tc.before, record.GlobalNamespace), sink)
			if _, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "r", Outcome: tc.outcome}); err != nil {
				t.Fatal(err)
			}
			eqStrings(t, sink.types(), tc.want)
			if sink.evs[0].Namespace != "acme" || sink.evs[0].Outcome != tc.outcome {
				t.Errorf("feedback event = %+v", sink.evs[0])
			}
			if len(sink.evs) > 1 && sink.evs[1].Namespace != record.GlobalNamespace {
				t.Errorf("lifecycle event namespace = %q, want the record's", sink.evs[1].Namespace)
			}
		})
	}

	// A failing sink changes nothing; a failed update records nothing.
	resp, err := eventSvc(recStore(record.StatusActive, "acme"), &recSink{err: errors.New("x")}).
		Feedback(context.Background(), &FeedbackRequest{ID: "r", Outcome: FeedbackUseful})
	if err != nil || resp.NewStatus != record.StatusActive {
		t.Errorf("failing sink: %v %+v", err, resp)
	}
	sink := &recSink{}
	failing := recStore(record.StatusActive, "acme")
	failing.UpdateFunc = func(context.Context, string, map[string]interface{}) (*record.Record, error) {
		return nil, errors.New("db")
	}
	if _, err := eventSvc(failing, sink).Feedback(context.Background(), &FeedbackRequest{ID: "r", Outcome: FeedbackUseful}); err == nil {
		t.Fatal("expected error")
	}
	eqStrings(t, sink.types(), nil)
}

// AC-20: memory_update / memory_deprecate events.
func TestUpdateAndDeprecateEvents(t *testing.T) {
	st := func(s record.Status) *record.Status { return &s }
	ctx := context.Background()
	cases := []struct {
		name   string
		before record.Status
		status *record.Status
		want   []string
	}{
		{"content only", record.StatusActive, nil, []string{"record_updated"}},
		{"promote", record.StatusCandidate, st(record.StatusActive), []string{"record_updated", "record_promoted/tool"}},
		{"deprecate", record.StatusActive, st(record.StatusDeprecated), []string{"record_updated", "record_deprecated/tool"}},
		{"un-deprecate", record.StatusDeprecated, st(record.StatusActive), []string{"record_updated"}},
		{"deprecated stays", record.StatusDeprecated, st(record.StatusDeprecated), []string{"record_updated"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recSink{}
			svc := eventSvc(recStore(tc.before, "acme"), sink)
			if _, err := svc.UpdateRecord(ctx, &UpdateRequest{ID: "r", Status: tc.status, Ticket: ptrString("T-1")}); err != nil {
				t.Fatal(err)
			}
			eqStrings(t, sink.types(), tc.want)
			if sink.evs[0].Source != EventSourceInline {
				t.Errorf("source = %q", sink.evs[0].Source)
			}
		})
	}

	sink := &recSink{}
	svc := eventSvc(recStore(record.StatusActive, "acme"), sink)
	if _, err := svc.DeprecateRecord(ctx, &DeprecateRequest{ID: "r", Reason: "old"}); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, sink.types(), []string{"record_deprecated/tool"})

	sink = &recSink{}
	svc = eventSvc(recStore(record.StatusDeprecated, "acme"), sink)
	if _, err := svc.DeprecateRecord(ctx, &DeprecateRequest{ID: "r", Reason: "old"}); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, sink.types(), nil)

	// A failing sink never fails the operation.
	svc = eventSvc(recStore(record.StatusActive, "acme"), &recSink{err: errors.New("x")})
	if _, err := svc.DeprecateRecord(ctx, &DeprecateRequest{ID: "r", Reason: "old"}); err != nil {
		t.Errorf("DeprecateRecord with a failing sink: %v", err)
	}
}

// A service with no sink (the default) works and records nothing.
func TestNoSinkIsFine(t *testing.T) {
	svc := New(recStore(record.StatusActive, "acme"), &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg()).
		WithNamespace("acme")
	if _, err := svc.Feedback(context.Background(), &FeedbackRequest{ID: "r", Outcome: FeedbackUseful}); err != nil {
		t.Fatal(err)
	}
}

func TestEventValidateAcceptsImportSource(t *testing.T) {
	e := NewEvent(time.Now(), "acme", EventRecordCreated)
	e.RecordID = uuid.New().String()
	e.Source, e.Status = EventSourceImport, record.StatusCandidate
	if err := e.Validate(time.Now()); err != nil {
		t.Errorf("Validate: %v", err)
	}
}
