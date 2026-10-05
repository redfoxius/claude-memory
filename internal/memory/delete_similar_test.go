package memory

import (
	"context"
	"errors"
	"testing"

	"claude-memory/internal/record"
)

func TestDeleteRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes and emits record_deleted via tool", func(t *testing.T) {
		sink := &recSink{}
		var deleted string
		st := recStore(record.StatusActive, "work")
		st.DeleteFunc = func(_ context.Context, id string) error { deleted = id; return nil }
		if err := eventSvc(st, sink).DeleteRecord(ctx, "r"); err != nil {
			t.Fatal(err)
		}
		if deleted != "r" {
			t.Errorf("deleted = %q", deleted)
		}
		eqStrings(t, sink.types(), []string{"record_deleted/tool"})
		if sink.evs[0].Source != "" {
			t.Errorf("source = %q, want none", sink.evs[0].Source)
		}
	})

	t.Run("referenced: error passes through, no event", func(t *testing.T) {
		sink := &recSink{}
		st := recStore(record.StatusActive, "work")
		st.DeleteFunc = func(context.Context, string) error { return &ErrReferenced{IDs: []string{"x"}} }
		err := eventSvc(st, sink).DeleteRecord(ctx, "r")
		var ref *ErrReferenced
		if !errors.As(err, &ref) || len(ref.IDs) != 1 {
			t.Fatalf("err = %v", err)
		}
		eqStrings(t, sink.types(), nil)
	})

	t.Run("other namespace is not found", func(t *testing.T) {
		st := recStore(record.StatusActive, "other")
		st.DeleteFunc = func(context.Context, string) error { t.Fatal("Delete called"); return nil }
		if err := eventSvc(st, &recSink{}).DeleteRecord(ctx, "r"); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestSimilar(t *testing.T) {
	ctx := context.Background()
	withEmb := func(emb []float32) *mockStore {
		st := recStore(record.StatusCandidate, "work")
		st.GetFunc = func(_ context.Context, id string) (*record.Record, error) {
			return &record.Record{ID: id, Namespace: "work", Repo: "svc", Embedding: emb}, nil
		}
		return st
	}

	t.Run("excludes the record itself and caps at limit", func(t *testing.T) {
		st := withEmb([]float32{1})
		var gotLimit int
		st.FindCandidatesFunc = func(_ context.Context, _ []float32, repo string, limit int) ([]*Candidate, error) {
			gotLimit = limit
			return []*Candidate{{ID: "a"}, {ID: "r"}, {ID: "b"}, {ID: "c"}}, nil
		}
		got, err := eventSvc(st, &recSink{}).Similar(ctx, "r", 2)
		if err != nil {
			t.Fatal(err)
		}
		if gotLimit != 3 {
			t.Errorf("store limit = %d, want 3", gotLimit)
		}
		if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("no embedding", func(t *testing.T) {
		if _, err := eventSvc(withEmb(nil), &recSink{}).Similar(ctx, "r", 3); !errors.Is(err, ErrNoEmbedding) {
			t.Errorf("err = %v, want ErrNoEmbedding", err)
		}
	})

	t.Run("embedding provider is not called", func(t *testing.T) {
		st := withEmb([]float32{1})
		svc := New(st, &mockEmbeddingProvider{EmbedFunc: func(context.Context, string, int) ([]float32, error) {
			t.Fatal("Embed called")
			return nil, nil
		}}, &mockScrubber{}, &mockClock{}, writepathCfg()).WithNamespace("work")
		if _, err := svc.Similar(ctx, "r", 3); err != nil {
			t.Fatal(err)
		}
	})
}
