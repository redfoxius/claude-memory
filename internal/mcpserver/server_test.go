package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

// newTestClient connects a client to a Server backed by svc over the
// go-sdk's in-memory (net.Pipe) transport, and returns the client session
// plus a cleanup func.
func newTestClient(t *testing.T, svc Service, opts ...Option) *mcp.ClientSession {
	t.Helper()

	srv := New(svc, zerolog.New(io.Discard), opts...)

	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()

	if _, err := srv.mcp.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// callTool invokes name with args and decodes the structured result into
// an Out value, failing the test if the call returned a tool error.
func callTool[Out any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) Out {
	t.Helper()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool returned error: %s", name, toolErrorText(res))
	}

	var out Out
	decodeStructured(t, res, &out)
	return out
}

// callToolExpectError invokes name with args and returns the tool's error
// text, failing the test if the call unexpectedly succeeded.
func callToolExpectError(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("%s: expected a tool error, got success", name)
	}
	return toolErrorText(res)
}

func toolErrorText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return ""
	}
	return tc.Text
}

// decodeStructured round-trips res.StructuredContent (an untyped value
// after JSON transport) back into a concrete Go struct via JSON.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
}

func TestMemorySearch_HappyPath(t *testing.T) {
	svc := &fakeService{
		searchFn: func(_ context.Context, req *memory.SearchRequest) (*memory.SearchResult, error) {
			if req.Query != "idempotency key gotcha" {
				t.Errorf("unexpected query: %q", req.Query)
			}
			return &memory.SearchResult{
				Degraded: false,
				Records: []*memory.SearchRecord{
					{
						ID:         "rec-1",
						Kind:       record.KindGotcha,
						Title:      "idempotency key gotcha",
						Repo:       "inventory-service",
						Tags:       []string{"webhook"},
						Status:     record.StatusActive,
						Confidence: 0.75,
						Score:      0.9,
						Similarity: 0.88,
						Unverified: false,
					},
				},
			}, nil
		},
	}

	cs := newTestClient(t, svc)
	out := callTool[SearchOutput](t, cs, "memory_search", map[string]any{
		"query": "idempotency key gotcha",
	})

	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.Results))
	}
	r := out.Results[0]
	if r.ID != "rec-1" || r.Repo != "inventory-service" || r.Title != "idempotency key gotcha" {
		t.Errorf("unexpected result shape: %+v", r)
	}
	if r.Unverified {
		t.Errorf("expected active record to not be flagged unverified")
	}
	if out.Degraded {
		t.Errorf("expected Degraded=false")
	}
}

func TestMemoryStore_HappyPath(t *testing.T) {
	svc := &fakeService{
		storeFn: func(_ context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error) {
			if req.Repo != "billing-service" {
				t.Errorf("unexpected repo: %q", req.Repo)
			}
			return &memory.StoreResponse{ID: "new-id", Decision: memory.ActionAdd}, nil
		},
	}

	cs := newTestClient(t, svc)
	out := callTool[StoreOutput](t, cs, "memory_store", map[string]any{
		"kind":    "gotcha",
		"title":   "nil SDK gotcha",
		"content": "the SDK returns a zero-value struct instead of an error",
		"repo":    "billing-service",
	})

	if out.Status != "stored" || out.ID != "new-id" || out.Decision != "ADD" {
		t.Errorf("unexpected store output: %+v", out)
	}
}

func TestMemoryStore_NeedsJudgmentRoundTrip(t *testing.T) {
	var gotDecision *memory.ExtractionDecision

	svc := &fakeService{
		storeFn: func(_ context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error) {
			if req.ExtractionDecision == nil {
				// First call: top candidate lands in the 0.80-0.92 band.
				return &memory.StoreResponse{
					ID:       "",
					Decision: memory.ActionAdd,
					CandidatesConsidered: []*memory.Candidate{
						{ID: "existing-1", Title: "similar existing fact", Similarity: 0.85},
					},
				}, nil
			}
			// Follow-up call: the caller resolved the judgment via Decision.
			gotDecision = req.ExtractionDecision
			return &memory.StoreResponse{ID: "existing-1", Decision: req.ExtractionDecision.Action}, nil
		},
	}

	cs := newTestClient(t, svc)

	first := callTool[StoreOutput](t, cs, "memory_store", map[string]any{
		"kind":    "pattern",
		"title":   "near-duplicate fact",
		"content": "this closely matches an existing record",
		"repo":    "billing-service",
	})
	if first.Status != "needs_judgment" {
		t.Fatalf("expected needs_judgment, got %+v", first)
	}
	if len(first.CandidatesConsidered) != 1 || first.CandidatesConsidered[0].ID != "existing-1" {
		t.Fatalf("expected candidate existing-1, got %+v", first.CandidatesConsidered)
	}
	if first.ID != "" {
		t.Errorf("expected no record id on needs_judgment response, got %q", first.ID)
	}

	second := callTool[StoreOutput](t, cs, "memory_store", map[string]any{
		"kind":    "pattern",
		"title":   "near-duplicate fact",
		"content": "this closely matches an existing record",
		"repo":    "billing-service",
		"decision": map[string]any{
			"action":    "UPDATE",
			"target_id": "existing-1",
		},
	})
	if second.Status != "stored" || second.ID != "existing-1" || second.Decision != "UPDATE" {
		t.Fatalf("unexpected follow-up store output: %+v", second)
	}
	if gotDecision == nil || gotDecision.Action != memory.ActionUpdate || gotDecision.TargetID == nil || *gotDecision.TargetID != "existing-1" {
		t.Fatalf("decision not forwarded to service: %+v", gotDecision)
	}
}

func TestMemoryUpdate_HappyPath(t *testing.T) {
	svc := &fakeService{
		updateFn: func(_ context.Context, req *memory.UpdateRequest) (*record.Record, error) {
			if req.ID != "rec-1" {
				t.Errorf("unexpected id: %q", req.ID)
			}
			rec := sampleRecord("rec-1")
			rec.Title = *req.Title
			return rec, nil
		},
	}

	cs := newTestClient(t, svc)
	out := callTool[RecordOutput](t, cs, "memory_update", map[string]any{
		"id":    "rec-1",
		"title": "updated title",
	})
	if out.ID != "rec-1" || out.Title != "updated title" {
		t.Errorf("unexpected update output: %+v", out)
	}
}

func TestMemoryUpdate_NotFound(t *testing.T) {
	svc := &fakeService{
		updateFn: func(_ context.Context, _ *memory.UpdateRequest) (*record.Record, error) {
			return nil, memory.ErrNotFound
		},
	}

	cs := newTestClient(t, svc)
	text := callToolExpectError(t, cs, "memory_update", map[string]any{"id": "missing"})
	if !strings.Contains(text, "not found") {
		t.Errorf("expected 'not found' in error text, got %q", text)
	}
}

func TestMemoryDeprecate_HappyPath(t *testing.T) {
	svc := &fakeService{
		deprecateFn: func(_ context.Context, req *memory.DeprecateRequest) (*record.Record, error) {
			rec := sampleRecord(req.ID)
			rec.Status = record.StatusDeprecated
			rec.DeprecationReason = &req.Reason
			return rec, nil
		},
	}

	cs := newTestClient(t, svc)
	out := callTool[RecordOutput](t, cs, "memory_deprecate", map[string]any{
		"id":     "rec-1",
		"reason": "no longer accurate",
	})
	if out.Status != "deprecated" || out.DeprecationReason != "no longer accurate" {
		t.Errorf("unexpected deprecate output: %+v", out)
	}
}

func TestMemoryDeprecate_NotFound(t *testing.T) {
	svc := &fakeService{
		deprecateFn: func(_ context.Context, _ *memory.DeprecateRequest) (*record.Record, error) {
			return nil, memory.ErrNotFound
		},
	}

	cs := newTestClient(t, svc)
	text := callToolExpectError(t, cs, "memory_deprecate", map[string]any{
		"id":     "missing",
		"reason": "x",
	})
	if !strings.Contains(text, "not found") {
		t.Errorf("expected 'not found' in error text, got %q", text)
	}
}

func TestMemoryGet_HappyPath(t *testing.T) {
	svc := &fakeService{
		getFn: func(_ context.Context, id string) (*record.Record, error) {
			return sampleRecord(id), nil
		},
	}

	cs := newTestClient(t, svc)
	out := callTool[RecordOutput](t, cs, "memory_get", map[string]any{"id": "rec-1"})
	if out.ID != "rec-1" || out.Content != "sample content" {
		t.Errorf("unexpected get output: %+v", out)
	}
}

func TestMemoryGet_NotFound(t *testing.T) {
	svc := &fakeService{
		getFn: func(_ context.Context, _ string) (*record.Record, error) {
			return nil, memory.ErrNotFound
		},
	}

	cs := newTestClient(t, svc)
	text := callToolExpectError(t, cs, "memory_get", map[string]any{"id": "missing"})
	if !strings.Contains(text, "not found") {
		t.Errorf("expected 'not found' in error text, got %q", text)
	}
}

func TestMemoryList_HappyPath(t *testing.T) {
	svc := &fakeService{
		listFn: func(_ context.Context, filters memory.ListFilters) ([]*record.Record, error) {
			if filters.Repo == nil || *filters.Repo != "billing-service" {
				t.Errorf("unexpected repo filter: %+v", filters.Repo)
			}
			return []*record.Record{sampleRecord("rec-1"), sampleRecord("rec-2")}, nil
		},
	}

	cs := newTestClient(t, svc)
	out := callTool[ListOutput](t, cs, "memory_list", map[string]any{"repo": "billing-service"})
	if len(out.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(out.Records))
	}
}

func TestMemoryFeedback_HappyPath(t *testing.T) {
	svc := &fakeService{
		feedbackFn: func(_ context.Context, req *memory.FeedbackRequest) (*memory.FeedbackResponse, error) {
			if req.Outcome != memory.FeedbackUseful {
				t.Errorf("unexpected outcome: %q", req.Outcome)
			}
			return &memory.FeedbackResponse{ID: req.ID, NewStatus: record.StatusActive}, nil
		},
	}

	cs := newTestClient(t, svc)
	out := callTool[FeedbackOutput](t, cs, "memory_feedback", map[string]any{
		"id":      "rec-1",
		"outcome": "useful",
	})
	if out.ID != "rec-1" || out.NewStatus != "active" {
		t.Errorf("unexpected feedback output: %+v", out)
	}
}

func TestMemoryFeedback_NotFound(t *testing.T) {
	svc := &fakeService{
		feedbackFn: func(_ context.Context, _ *memory.FeedbackRequest) (*memory.FeedbackResponse, error) {
			return nil, memory.ErrNotFound
		},
	}

	cs := newTestClient(t, svc)
	text := callToolExpectError(t, cs, "memory_feedback", map[string]any{
		"id":      "missing",
		"outcome": "wrong",
	})
	if !strings.Contains(text, "not found") {
		t.Errorf("expected 'not found' in error text, got %q", text)
	}
}

// --- staleness and namespace fields (spec: AC-6, AC-12 input, namespaces AC-23) ---

func TestMemorySearch_StaleAndNamespaceFields(t *testing.T) {
	svc := &fakeService{
		searchFn: func(context.Context, *memory.SearchRequest) (*memory.SearchResult, error) {
			return &memory.SearchResult{Records: []*memory.SearchRecord{
				{ID: "stale-n", Namespace: "pet-game", Stale: &memory.StaleHint{Commits: 3}},
				{ID: "stale-unknown", Namespace: "global", Stale: &memory.StaleHint{}},
				{ID: "fresh", Namespace: "pet-game"},
			}}, nil
		},
	}
	cs := newTestClient(t, svc)
	raw := callTool[map[string]any](t, cs, "memory_search", map[string]any{"query": "q"})
	results := raw["results"].([]any)
	byID := map[string]map[string]any{}
	for _, r := range results {
		m := r.(map[string]any)
		byID[m["id"].(string)] = m
	}

	if byID["stale-n"]["stale_hint"] != true || byID["stale-n"]["stale_commits"] != float64(3) {
		t.Errorf("stale-n: %v", byID["stale-n"])
	}
	if byID["stale-unknown"]["stale_hint"] != true {
		t.Errorf("stale-unknown: %v", byID["stale-unknown"])
	}
	if _, ok := byID["stale-unknown"]["stale_commits"]; ok {
		t.Error("stale_commits must be omitted when the count is unknown")
	}
	for _, k := range []string{"stale_hint", "stale_commits"} {
		if _, ok := byID["fresh"][k]; ok {
			t.Errorf("fresh record has %s", k)
		}
	}
	for id, ns := range map[string]string{"stale-n": "pet-game", "stale-unknown": "global", "fresh": "pet-game"} {
		if byID[id]["namespace"] != ns {
			t.Errorf("%s namespace = %v, want %s", id, byID[id]["namespace"], ns)
		}
	}
}

func TestMemoryGet_StaleHintAndNamespace(t *testing.T) {
	svc := &fakeService{
		getFn: func(_ context.Context, id string) (*record.Record, error) {
			r := sampleRecord(id)
			r.Namespace = "pet-game"
			return r, nil
		},
		staleHint: func(r *record.Record) *memory.StaleHint {
			if r.ID == "stale" {
				return &memory.StaleHint{Commits: 1}
			}
			return nil
		},
	}
	cs := newTestClient(t, svc)

	stale := callTool[RecordOutput](t, cs, "memory_get", map[string]any{"id": "stale"})
	if !stale.StaleHint || stale.StaleCommits != 1 || stale.Namespace != "pet-game" {
		t.Errorf("stale get: %+v", stale)
	}
	fresh := callTool[RecordOutput](t, cs, "memory_get", map[string]any{"id": "fresh"})
	if fresh.StaleHint || fresh.StaleCommits != 0 {
		t.Errorf("fresh get flagged: %+v", fresh)
	}
}

func TestMemoryUpdate_CommitSHAPassesThrough(t *testing.T) {
	var got *string
	svc := &fakeService{
		updateFn: func(_ context.Context, req *memory.UpdateRequest) (*record.Record, error) {
			got = req.CommitSHA
			return sampleRecord(req.ID), nil
		},
	}
	cs := newTestClient(t, svc)
	callTool[RecordOutput](t, cs, "memory_update", map[string]any{"id": "rec-1", "commit_sha": "abcdef1234567"})
	if got == nil || *got != "abcdef1234567" {
		t.Errorf("CommitSHA = %v", got)
	}
}

func TestMemoryStore_NamespaceInAndOut(t *testing.T) {
	var gotNS string
	svc := &fakeService{
		storeFn: func(_ context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error) {
			gotNS = req.Namespace
			return &memory.StoreResponse{ID: "new", Namespace: "global", Decision: memory.ActionAdd}, nil
		},
	}
	cs := newTestClient(t, svc)
	out := callTool[StoreOutput](t, cs, "memory_store", map[string]any{
		"kind": "gotcha", "title": "t", "content": "c", "namespace": "global",
	})
	if gotNS != "global" || out.Namespace != "global" {
		t.Errorf("namespace in=%q out=%q", gotNS, out.Namespace)
	}
}

func TestMemoryStore_RejectsSourceImport(t *testing.T) {
	svc := &fakeService{
		storeFn: func(context.Context, *memory.StoreRequest) (*memory.StoreResponse, error) {
			t.Fatal("Store must not be called for source=import")
			return nil, nil
		},
	}
	cs := newTestClient(t, svc)
	msg := callToolExpectError(t, cs, "memory_store", map[string]any{
		"kind": "gotcha", "title": "t", "content": "c", "source": "import",
	})
	if !strings.Contains(msg, "import") {
		t.Errorf("error = %q", msg)
	}
}
