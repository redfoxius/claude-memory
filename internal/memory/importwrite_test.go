package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"claude-memory/internal/record"
)

func importReq() *StoreRequest {
	return &StoreRequest{
		Kind: record.KindPattern, Title: "Imported title", Content: "Imported content",
		Repo: "billing-service", Source: record.SourceImport, ImportKey: "automem:abc",
		Tags: []string{"imported", "auto-memory"},
	}
}

// importStore returns a store whose Create records what it was given.
func importStore(created *[]*record.Record) *mockStore {
	return &mockStore{
		CreateFunc: func(_ context.Context, r *record.Record) (*record.Record, error) {
			*created = append(*created, r)
			return r, nil
		},
	}
}

func TestStoreImport_AddsCandidateWithKeyAndEvent(t *testing.T) {
	var created []*record.Record
	sink := &recSink{}
	svc := eventSvc(importStore(&created), sink)

	resp, err := svc.Store(context.Background(), importReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != ActionAdd || resp.ID == "" || len(created) != 1 {
		t.Fatalf("resp = %+v, created %d", resp, len(created))
	}
	r := created[0]
	if r.Status != record.StatusCandidate || r.Source != record.SourceImport || r.Confidence != 0.5 {
		t.Errorf("record = %+v", r)
	}
	if r.ImportKey == nil || *r.ImportKey != "automem:abc" {
		t.Errorf("ImportKey = %v", r.ImportKey)
	}
	if r.CommitSHA != nil {
		t.Errorf("import must not stamp a baseline, got %v", *r.CommitSHA)
	}
	eqStrings(t, sink.types(), []string{"record_created"})
	if e := sink.evs[0]; e.Source != EventSourceImport || e.Status != record.StatusCandidate {
		t.Errorf("event = %+v", e)
	}
}

func TestStoreImport_KeyHitSkipsWithoutWriteOrEvent(t *testing.T) {
	var created []*record.Record
	st := importStore(&created)
	st.ImportKeyExistsFunc = func(_ context.Context, ns, key string) (bool, error) {
		if ns != "acme" || key != "automem:abc" {
			t.Errorf("ImportKeyExists(%q, %q)", ns, key)
		}
		return true, nil
	}
	sink := &recSink{}
	resp, err := eventSvc(st, sink).Store(context.Background(), importReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != ActionSkip || resp.SkipReason != "already imported" || len(created) != 0 || len(sink.evs) != 0 {
		t.Errorf("resp = %+v, created %d, events %v", resp, len(created), sink.types())
	}
}

func TestStoreImport_SimilarityBoundaries(t *testing.T) {
	cases := []struct {
		name string
		sim  float64
		want WriteAction
	}{
		{"at update threshold skips", 0.85, ActionSkip},
		{"judgment band adds", 0.80, ActionAdd},
		{"below band adds", 0.50, ActionAdd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var created []*record.Record
			st := importStore(&created)
			st.FindCandidatesFunc = func(context.Context, []float32, string, int) ([]*Candidate, error) {
				return []*Candidate{{ID: "cand-1", Similarity: tc.sim}}, nil
			}
			resp, err := eventSvc(st, &recSink{}).Store(context.Background(), importReq())
			if err != nil {
				t.Fatal(err)
			}
			if resp.Decision != tc.want {
				t.Fatalf("decision = %s, want %s", resp.Decision, tc.want)
			}
			if tc.want == ActionSkip && (resp.ID != "cand-1" || resp.SkipReason != "duplicate of cand-1" || len(created) != 0) {
				t.Errorf("skip resp = %+v, created %d", resp, len(created))
			}
			if tc.want == ActionAdd && len(created) != 1 {
				t.Errorf("created %d", len(created))
			}
		})
	}
}

func TestStoreImport_InfraErrorsAreNotInvalidRequest(t *testing.T) {
	st := importStore(new([]*record.Record))
	st.ImportKeyExistsFunc = func(context.Context, string, string) (bool, error) { return false, errors.New("db down") }
	_, err := eventSvc(st, &recSink{}).Store(context.Background(), importReq())
	if err == nil || errors.Is(err, ErrInvalidRequest) {
		t.Errorf("err = %v", err)
	}
}

func TestStoreValidation_WrapsErrInvalidRequest(t *testing.T) {
	skip := &ExtractionDecision{Action: ActionSkip}
	add := &ExtractionDecision{Action: ActionAdd}
	cases := map[string]func(r *StoreRequest){
		"import without key":     func(r *StoreRequest) { r.ImportKey = "" },
		"key with inline source": func(r *StoreRequest) { r.Source = record.SourceInline },
		"import with decision":   func(r *StoreRequest) { r.ExtractionDecision = add },
		"SKIP decision, import":  func(r *StoreRequest) { r.ExtractionDecision = skip },
		"SKIP decision, inline":  func(r *StoreRequest) { r.Source, r.ImportKey, r.ExtractionDecision = record.SourceInline, "", skip },
		"SKIP decision, session": func(r *StoreRequest) { r.Source, r.ImportKey, r.ExtractionDecision = record.SourceSession, "", skip },
		"SKIP decision, pr":      func(r *StoreRequest) { r.Source, r.ImportKey, r.ExtractionDecision = record.SourcePR, "", skip },
		"oversized content":      func(r *StoreRequest) { r.Content = strings.Repeat("x", 20001) },
		"missing title":          func(r *StoreRequest) { r.Title = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			st := importStore(new([]*record.Record))
			req := importReq()
			mutate(req)
			_, err := eventSvc(st, &recSink{}).Store(context.Background(), req)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestStoreImport_UniqueViolationRaceIsSkip(t *testing.T) {
	st := &mockStore{CreateFunc: func(context.Context, *record.Record) (*record.Record, error) {
		return nil, fmt.Errorf("insert record: %w", ErrImportKeyExists)
	}}
	sink := &recSink{}
	resp, err := eventSvc(st, sink).Store(context.Background(), importReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != ActionSkip || resp.SkipReason != "already imported" || len(sink.evs) != 0 {
		t.Errorf("resp = %+v, events %v", resp, sink.types())
	}
}
