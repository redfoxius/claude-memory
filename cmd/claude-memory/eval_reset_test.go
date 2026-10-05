package main

import (
	"context"
	"testing"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

type fakeFixtures struct {
	ns      string
	recs    map[string]*record.Record
	deleted []string
}

func (f *fakeFixtures) Namespace() string { return f.ns }

// ListRecords mimics Service.ListRecords: only the service's own namespace.
func (f *fakeFixtures) ListRecords(_ context.Context, _ memory.ListFilters) ([]*record.Record, error) {
	var out []*record.Record
	for _, r := range f.recs {
		if r.Namespace == f.ns {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeFixtures) DeleteRecord(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.recs, id)
	return nil
}

func TestResetEvalFixtures_OnlyEvalNamespace(t *testing.T) {
	f := &fakeFixtures{ns: "eval", recs: map[string]*record.Record{
		"a": {ID: "a", Namespace: "eval"},
		"b": {ID: "b", Namespace: "eval"},
		"c": {ID: "c", Namespace: "work"},
	}}
	n, err := resetEvalFixtures(context.Background(), f)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2 nil", n, err)
	}
	if _, ok := f.recs["c"]; !ok || len(f.recs) != 1 {
		t.Errorf("non-eval record touched: %v", f.recs)
	}
}

func TestResetEvalFixtures_RefusesOtherNamespace(t *testing.T) {
	f := &fakeFixtures{ns: "work", recs: map[string]*record.Record{"a": {ID: "a", Namespace: "work"}}}
	n, err := resetEvalFixtures(context.Background(), f)
	if err == nil || n != 0 || len(f.deleted) != 0 {
		t.Fatalf("n=%d err=%v deleted=%v, want refusal and no deletes", n, err, f.deleted)
	}
}
