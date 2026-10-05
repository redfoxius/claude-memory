package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

type recSink struct {
	evs []memory.Event
	err error
}

func (r *recSink) Append(_ context.Context, evs ...memory.Event) error {
	r.evs = append(r.evs, evs...)
	return r.err
}

var errDown = errors.New("pg down")

func testClassify(err error) memory.ErrorClass {
	return memory.ClassifyError(err, func(e error) bool { return errors.Is(e, errDown) })
}

func storeArgs() map[string]any {
	return map[string]any{"kind": "pattern", "title": "t", "content": "c", "repo": "r", "namespace": "caller-ns"}
}

func oneEvent(t *testing.T, sink *recSink, typ memory.EventType, outcome memory.FeedbackOutcome, class memory.ErrorClass) memory.Event {
	t.Helper()
	if len(sink.evs) != 1 {
		t.Fatalf("events = %d, want 1: %+v", len(sink.evs), sink.evs)
	}
	e := sink.evs[0]
	if e.Type != typ || e.Outcome != outcome || e.ErrorClass != class || e.Via != memory.ViaMCP || e.SessionID != "sess-1" {
		t.Errorf("event = %+v, want %s/%s/%s via mcp session sess-1", e, typ, outcome, class)
	}
	if err := e.Validate(e.At); err != nil {
		t.Errorf("event does not validate: %v", err)
	}
	return e
}

func TestSearchEmitsOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		res     *memory.SearchResult
		err     error
		outcome memory.FeedbackOutcome
		class   memory.ErrorClass
		wantErr bool
	}{
		{"ok", map[string]any{"query": "q"}, &memory.SearchResult{}, nil, memory.OutcomeOK, "", false},
		{"degraded", map[string]any{"query": "q"}, &memory.SearchResult{Degraded: true}, nil, memory.OutcomeDegraded, "", false},
		{"db down", map[string]any{"query": "q"}, nil, fmt.Errorf("x: %w", errDown), memory.OutcomeError, memory.ErrClassDBUnavailable, true},
		{"timeout", map[string]any{"query": "q"}, nil, context.DeadlineExceeded, memory.OutcomeError, memory.ErrClassTimeout, true},
		{"internal", map[string]any{"query": "q"}, nil, errors.New("boom"), memory.OutcomeError, memory.ErrClassInternal, true},
		{"invalid kind", map[string]any{"query": "q", "kind": "nope"}, nil, nil, memory.OutcomeError, memory.ErrClassInvalidRequest, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recSink{}
			svc := &fakeService{searchFn: func(context.Context, *memory.SearchRequest) (*memory.SearchResult, error) { return tc.res, tc.err }}
			cs := newTestClient(t, svc, WithEvents(sink, "sess-1", testClassify))
			if tc.wantErr {
				callToolExpectError(t, cs, "memory_search", tc.args)
			} else {
				callTool[SearchOutput](t, cs, "memory_search", tc.args)
			}
			e := oneEvent(t, sink, memory.EventSearchCalled, tc.outcome, tc.class)
			if e.Namespace != "fake-ns" {
				t.Errorf("namespace = %q", e.Namespace)
			}
		})
	}
}

func TestStoreEmitsOutcomes(t *testing.T) {
	judgment := &memory.StoreResponse{Namespace: "resp-ns", Decision: memory.ActionAdd,
		CandidatesConsidered: []*memory.Candidate{{ID: "c1", Title: "x", Similarity: 0.85}}}
	cases := []struct {
		name    string
		args    map[string]any
		resp    *memory.StoreResponse
		err     error
		outcome memory.FeedbackOutcome
		class   memory.ErrorClass
		ns      string
		wantErr bool
	}{
		{"added", storeArgs(), &memory.StoreResponse{ID: "1", Namespace: "resp-ns", Decision: memory.ActionAdd}, nil, memory.OutcomeAdded, "", "resp-ns", false},
		{"updated", storeArgs(), &memory.StoreResponse{ID: "1", Decision: memory.ActionUpdate}, nil, memory.OutcomeUpdated, "", "fake-ns", false},
		{"superseded", storeArgs(), &memory.StoreResponse{ID: "1", Decision: memory.ActionSupersede}, nil, memory.OutcomeSuperseded, "", "fake-ns", false},
		{"noop", storeArgs(), &memory.StoreResponse{ID: "1", Decision: memory.ActionNoop}, nil, memory.OutcomeNoop, "", "fake-ns", false},
		// The judgment response still carries Decision=ADD: it must not count as added.
		{"needs judgment", storeArgs(), judgment, nil, memory.OutcomeNeedsJudgment, "", "resp-ns", false},
		{"unexpected decision", storeArgs(), &memory.StoreResponse{ID: "1", Decision: memory.ActionSkip}, nil, memory.OutcomeError, memory.ErrClassInternal, "fake-ns", false},
		{"embedding", storeArgs(), nil, fmt.Errorf("%w: x", memory.ErrEmbeddingUnavailable), memory.OutcomeError, memory.ErrClassEmbeddingUnavailable, "fake-ns", true},
		{"db down", storeArgs(), nil, fmt.Errorf("x: %w", errDown), memory.OutcomeError, memory.ErrClassDBUnavailable, "fake-ns", true},
		{"service validation", storeArgs(), nil, fmt.Errorf("store validation: %w", memory.ErrInvalidRequest), memory.OutcomeError, memory.ErrClassInvalidRequest, "fake-ns", true},
		{"invalid kind", map[string]any{"kind": "nope", "title": "t", "content": "c"}, nil, nil, memory.OutcomeError, memory.ErrClassInvalidRequest, "fake-ns", true},
		{"reserved source", map[string]any{"kind": "pattern", "title": "t", "content": "c", "source": "import"}, nil, nil, memory.OutcomeError, memory.ErrClassInvalidRequest, "fake-ns", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recSink{}
			svc := &fakeService{storeFn: func(context.Context, *memory.StoreRequest) (*memory.StoreResponse, error) { return tc.resp, tc.err }}
			cs := newTestClient(t, svc, WithEvents(sink, "sess-1", testClassify))
			if tc.wantErr {
				callToolExpectError(t, cs, "memory_store", tc.args)
			} else {
				callTool[StoreOutput](t, cs, "memory_store", tc.args)
			}
			e := oneEvent(t, sink, memory.EventStoreAttempted, tc.outcome, tc.class)
			// Never the caller's namespace ("caller-ns").
			if e.Namespace != tc.ns {
				t.Errorf("namespace = %q, want %q", e.Namespace, tc.ns)
			}
		})
	}
}

// A failing sink changes neither the result nor the error.
func TestFailingSinkDoesNotChangeResults(t *testing.T) {
	svc := &fakeService{
		searchFn: func(context.Context, *memory.SearchRequest) (*memory.SearchResult, error) {
			return &memory.SearchResult{}, nil
		},
		storeFn: func(context.Context, *memory.StoreRequest) (*memory.StoreResponse, error) {
			return &memory.StoreResponse{ID: "1", Decision: memory.ActionAdd}, nil
		},
	}
	sink := &recSink{err: errors.New("disk full")}
	cs := newTestClient(t, svc, WithEvents(sink, "sess-1", testClassify))
	callTool[SearchOutput](t, cs, "memory_search", map[string]any{"query": "q"})
	callTool[SearchOutput](t, cs, "memory_search", map[string]any{"query": "q"})
	out := callTool[StoreOutput](t, cs, "memory_store", storeArgs())
	if out.Status != "stored" || out.ID != "1" {
		t.Errorf("store output = %+v", out)
	}
	if len(sink.evs) != 3 {
		t.Errorf("events attempted = %d, want 3", len(sink.evs))
	}
}

// Without WithEvents nothing is recorded, and no other tool emits.
func TestOtherToolsAndNilSinkEmitNothing(t *testing.T) {
	svc := &fakeService{
		searchFn: func(context.Context, *memory.SearchRequest) (*memory.SearchResult, error) {
			return &memory.SearchResult{}, nil
		},
		getFn:  func(context.Context, string) (*record.Record, error) { return sampleRecord("r1"), nil },
		listFn: func(context.Context, memory.ListFilters) ([]*record.Record, error) { return nil, nil },
	}
	callTool[SearchOutput](t, newTestClient(t, svc), "memory_search", map[string]any{"query": "q"})

	sink := &recSink{}
	cs := newTestClient(t, svc, WithEvents(sink, "sess-1", testClassify))
	callTool[RecordOutput](t, cs, "memory_get", map[string]any{"id": "r1"})
	callTool[ListOutput](t, cs, "memory_list", map[string]any{})
	if len(sink.evs) != 0 {
		t.Errorf("get/list emitted %d events", len(sink.evs))
	}
}
