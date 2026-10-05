package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/eventspool"
	"claude-memory/internal/memory"
)

type countingSink struct {
	calls [][]memory.Event
}

func (c *countingSink) Append(_ context.Context, evs ...memory.Event) error {
	c.calls = append(c.calls, evs)
	return nil
}

func failingSearchSvc(err error) *memory.Service {
	store := &fakeHookStore{
		searchFn: func(context.Context, string, []float32, string, memory.SearchOptions) (*memory.SearchResult, error) {
			return nil, err
		},
	}
	return memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, hookTestCfg())
}

func lastEvent(t *testing.T, sink *countingSink) memory.Event {
	t.Helper()
	if len(sink.calls) != 1 {
		t.Fatalf("Append calls = %d, want 1", len(sink.calls))
	}
	evs := sink.calls[0]
	e := evs[len(evs)-1]
	if e.Type != memory.EventSearchCalled || e.Via != memory.ViaHook {
		t.Fatalf("last event = %+v", e)
	}
	if err := e.Validate(time.Now()); err != nil {
		t.Errorf("event does not validate: %v", err)
	}
	return e
}

// One search_called per run, in the same single Append as the card events.
func TestHook_OneReliabilityEventPerRunInOneAppend(t *testing.T) {
	rec := func(id string) *memory.SearchRecord { return searchRecord(id, "t", "repo", "c", 0.9, 0.5, false) }
	for _, n := range []int{0, 1, 3} {
		var recs []*memory.SearchRecord
		for i := 0; i < n; i++ {
			recs = append(recs, rec(fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1)))
		}
		sink := &countingSink{}
		_, err := runHookCmdDeps(t, context.Background(), hookTestCfg(), hookEventsSvc(recs),
			`{"prompt":"p","cwd":"/tmp","session_id":"sess-1"}`, hookDeps{Events: sink})
		if err != nil {
			t.Fatal(err)
		}
		e := lastEvent(t, sink)
		if len(sink.calls[0]) != n+1 || e.Outcome != memory.OutcomeOK || e.ErrorClass != "" || e.SessionID != "sess-1" {
			t.Errorf("%d cards: events = %+v", n, sink.calls[0])
		}
	}
}

func TestHook_DegradedOutcome(t *testing.T) {
	store := &fakeHookStore{
		searchFn: func(context.Context, string, []float32, string, memory.SearchOptions) (*memory.SearchResult, error) {
			return &memory.SearchResult{Degraded: true}, nil
		},
	}
	svc := memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, hookTestCfg())
	sink := &countingSink{}
	if _, err := runHookCmdDeps(t, context.Background(), hookTestCfg(), svc, `{"prompt":"p","cwd":"/tmp"}`, hookDeps{Events: sink}); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, sink); e.Outcome != memory.OutcomeDegraded {
		t.Errorf("outcome = %q", e.Outcome)
	}
}

// A failed search records its class, writes no output and exits cleanly.
func TestHook_SearchErrorClass(t *testing.T) {
	down := errors.New("pg down")
	cases := []struct {
		err   error
		class memory.ErrorClass
	}{
		{fmt.Errorf("x: %w", down), memory.ErrClassDBUnavailable},
		{context.DeadlineExceeded, memory.ErrClassTimeout},
		{errors.New("boom"), memory.ErrClassInternal},
	}
	for _, tc := range cases {
		sink := &countingSink{}
		deps := hookDeps{Events: sink, Classify: func(err error) memory.ErrorClass {
			return memory.ClassifyError(err, func(e error) bool { return errors.Is(e, down) })
		}}
		out, err := runHookCmdDeps(t, context.Background(), hookTestCfg(), failingSearchSvc(tc.err), `{"prompt":"p","cwd":"/tmp"}`, deps)
		if err != nil || out != "" {
			t.Fatalf("out %q err %v", out, err)
		}
		e := lastEvent(t, sink)
		if e.Outcome != memory.OutcomeError || e.ErrorClass != tc.class || len(sink.calls[0]) != 1 {
			t.Errorf("err %v: events = %+v", tc.err, sink.calls[0])
		}
	}
}

// Any service-build failure is db_unavailable, attributed to the namespace
// and session of the decoded stdin.
func TestHook_BuildFailureIsDBUnavailable(t *testing.T) {
	sink := &countingSink{}
	build := func(context.Context) (*memory.Service, func(), error) {
		return nil, nil, errors.New("create postgres store: ping database: context deadline exceeded")
	}
	out, err := runHookBuild(t, context.Background(), hookTestCfg(), `{"prompt":"p","cwd":"/tmp","session_id":"sess-9"}`, build, hookDeps{Events: sink})
	if err != nil || out != "" {
		t.Fatalf("out %q err %v", out, err)
	}
	e := lastEvent(t, sink)
	if e.Outcome != memory.OutcomeError || e.ErrorClass != memory.ErrClassDBUnavailable || e.SessionID != "sess-9" || e.Namespace == "" {
		t.Errorf("event = %+v", e)
	}
}

// A stdin decode failure emits nothing and never builds the service.
func TestHook_DecodeFailureEmitsNothing(t *testing.T) {
	sink := &countingSink{}
	built := false
	build := func(context.Context) (*memory.Service, func(), error) { built = true; return nil, nil, errors.New("x") }
	out, err := runHookBuild(t, context.Background(), hookTestCfg(), `{not json`, build, hookDeps{Events: sink})
	if err != nil || out != "" || len(sink.calls) != 0 || built {
		t.Errorf("out %q err %v calls %d built %v", out, err, len(sink.calls), built)
	}
}

// With a real spool the build-failure event lands as one valid line.
func TestHook_BuildFailureSpoolLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	build := func(context.Context) (*memory.Service, func(), error) { return nil, nil, errors.New("down") }
	if _, err := runHookBuild(t, context.Background(), hookTestCfg(), `{"prompt":"p","cwd":"/tmp"}`, build, hookDeps{Events: eventspool.Sink{Dir: dir}}); err != nil {
		t.Fatal(err)
	}
	evs := spoolEvents(t, dir)
	if len(evs) != 1 || evs[0].ErrorClass != memory.ErrClassDBUnavailable || strings.Contains(fmt.Sprint(evs[0]), "down") {
		t.Errorf("events = %+v", evs)
	}
}

// A cancelled search is not a service failure: no reliability event.
func TestHook_CanceledSearchEmitsNothing(t *testing.T) {
	sink := &countingSink{}
	out, err := runHookCmdDeps(t, context.Background(), hookTestCfg(), failingSearchSvc(context.Canceled),
		`{"prompt":"p","cwd":"/tmp","session_id":"sess-1"}`, hookDeps{Events: sink})
	if err != nil || out != "" || len(sink.calls) != 0 {
		t.Errorf("out %q err %v calls %d", out, err, len(sink.calls))
	}
}

// The build-failure path resolves the namespace quietly: nothing on the
// logger (stderr) even with a broken namespaces.yaml and an invalid
// MEMORY_NAMESPACE; stdout and the exit result are unchanged.
func TestHook_BuildFailureResolvesNamespaceQuietly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "claude-memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(namespacesFile(), []byte("rules: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMORY_NAMESPACE", "Bad Name!")

	old := resolveNamespaceQuiet
	resolveNamespaceQuiet = quietResolveNamespace
	t.Cleanup(func() { resolveNamespaceQuiet = old })

	var logs bytes.Buffer
	oldLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(oldLog) })

	sink := &countingSink{}
	build := func(context.Context) (*memory.Service, func(), error) { return nil, nil, errors.New("ping database") }
	out, err := runHookBuild(t, context.Background(), hookTestCfg(), `{"prompt":"p","cwd":"/tmp","session_id":"sess-9"}`, build, hookDeps{Events: sink})
	if err != nil || out != "" {
		t.Fatalf("out %q err %v", out, err)
	}
	if strings.Contains(logs.String(), "WARN") {
		t.Errorf("warning emitted: %s", logs.String())
	}
	if e := lastEvent(t, sink); e.ErrorClass != memory.ErrClassDBUnavailable {
		t.Errorf("event = %+v", e)
	}
}
