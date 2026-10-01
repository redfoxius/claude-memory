//go:build integration
// +build integration

package postgres

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// These tests drive memory.Service end-to-end against real Postgres + pgvector
// (the unit tests use mocks; the adapter tests call the store directly). They
// exist because write-path bugs — an invalid column in an UPDATE map, a
// missing namespace on a SUPERSEDE row, an empty string for a UUID column —
// only fail against the real schema.

// fixedEmbedder returns a predetermined vector chosen by the title the write
// path puts first in the embedding input, so tests control cosine similarity.
type fixedEmbedder struct {
	byTitle map[string][]float32
	calls   int
}

func (f *fixedEmbedder) Embed(_ context.Context, text string, _ int) ([]float32, error) {
	f.calls++
	best := ""
	for title := range f.byTitle {
		if strings.HasPrefix(text, title) && len(title) > len(best) {
			best = title // longest prefix wins ("Cache TTL" vs "Cache TTL (revised)")
		}
	}
	if best != "" {
		return f.byTitle[best], nil
	}
	return unitVec(2, 0), nil // default: orthogonal to everything below
}

// unitVec returns a 1024-d unit vector: cos(angle) on axis 0 and sin(angle)
// on axis `axis`, so its cosine similarity with unitVec(_, 0) is exactly
// cos(angle) — i.e. sim when called as vecWithSim.
func unitVec(axis int, sim float64) []float32 {
	v := make([]float32, 1024)
	v[0] = float32(sim)
	v[axis] = float32(math.Sqrt(1 - sim*sim))
	return v
}

type passScrubber struct{}

func (passScrubber) Scrub(s string) (string, bool) { return s, false }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

func newTestService(t *testing.T, store memory.Store, emb memory.EmbeddingProvider) *memory.Service {
	t.Helper()
	return memory.New(store, emb, passScrubber{}, realClock{}, &config.Config{
		StoreSimAsk:     0.65,
		StoreSimUpdate:  0.85,
		MaxContentChars: 20000,
		EmbedMaxTokens:  2048,
		Namespace:       testNS,
	})
}

func newServiceStore(t *testing.T) (*Store, func()) {
	t.Helper()
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	store, err := New(ctx, dsn)
	if err != nil {
		cleanup()
		t.Fatalf("new store: %v", err)
	}
	return store, func() { store.Close(); cleanup() }
}

func storeReq(title, content string, src record.Source) *memory.StoreRequest {
	return &memory.StoreRequest{
		Kind: record.KindGotcha, Title: title, Content: content, Repo: "svc-repo", Source: src,
	}
}

func mustGet(t *testing.T, s *Store, id string) *record.Record {
	t.Helper()
	r, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return r
}

// Inline NOOP: a near-identical re-store bumps seen_count; the second repeat
// promotes the candidate to active (AC-15, AC-34).
func TestServiceNoopPromotesCandidateAtSecondRepeat(t *testing.T) {
	store, done := newServiceStore(t)
	defer done()
	emb := &fixedEmbedder{byTitle: map[string][]float32{"Retry policy": unitVec(1, 1)}}
	svc := newTestService(t, store, emb)
	ctx := context.Background()

	first, err := svc.Store(ctx, storeReq("Retry policy", "retries three times", record.SourceInline))
	if err != nil || first.Decision != memory.ActionAdd {
		t.Fatalf("first store: %+v %v", first, err)
	}
	if r := mustGet(t, store, first.ID); r.Status != record.StatusCandidate || r.SeenCount != 0 || r.Namespace != testNS {
		t.Fatalf("after ADD: status=%s seen=%d ns=%s", r.Status, r.SeenCount, r.Namespace)
	}

	second, err := svc.Store(ctx, storeReq("Retry policy", "retries three times", record.SourceInline))
	if err != nil || second.Decision != memory.ActionNoop || second.ID != first.ID {
		t.Fatalf("second store: %+v %v", second, err)
	}
	if r := mustGet(t, store, first.ID); r.SeenCount != 1 || r.Status != record.StatusCandidate {
		t.Fatalf("after 1st NOOP: seen=%d status=%s, want 1/candidate", r.SeenCount, r.Status)
	}

	if _, err := svc.Store(ctx, storeReq("Retry policy", "retries three times", record.SourceInline)); err != nil {
		t.Fatal(err)
	}
	if r := mustGet(t, store, first.ID); r.SeenCount != 2 || r.Status != record.StatusActive {
		t.Fatalf("after 2nd NOOP: seen=%d status=%s, want 2/active", r.SeenCount, r.Status)
	}
}

// ExtractionDecision UPDATE rewrites title/content/embedding/files in place,
// keeps status, and the full-text index follows the new content.
func TestServiceExtractionUpdateRewritesRecord(t *testing.T) {
	store, done := newServiceStore(t)
	defer done()
	emb := &fixedEmbedder{byTitle: map[string][]float32{
		"Cache TTL": unitVec(1, 1), "Cache TTL (revised)": unitVec(1, 0.99),
	}}
	svc := newTestService(t, store, emb)
	ctx := context.Background()

	added, err := svc.Store(ctx, storeReq("Cache TTL", "ttl is one hour zebracorn", record.SourceSession))
	if err != nil {
		t.Fatal(err)
	}

	req := storeReq("Cache TTL (revised)", "ttl is five minutes quokkafin", record.SourceSession)
	req.Files = []string{"cache/ttl.go"}
	req.ExtractionDecision = &memory.ExtractionDecision{Action: memory.ActionUpdate, TargetID: &added.ID}
	resp, err := svc.Store(ctx, req)
	if err != nil || resp.Decision != memory.ActionUpdate || resp.ID != added.ID {
		t.Fatalf("update store: %+v %v", resp, err)
	}

	r := mustGet(t, store, added.ID)
	if r.Title != "Cache TTL (revised)" || !strings.Contains(r.Content, "quokkafin") || len(r.Files) != 1 {
		t.Errorf("record not rewritten: %+v", r)
	}
	if r.Status != record.StatusCandidate {
		t.Errorf("status changed to %s", r.Status)
	}

	nsOpt := memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5}
	if res, _ := store.Search(ctx, "quokkafin", nil, "svc-repo", nsOpt); len(res.Records) != 1 {
		t.Errorf("new content not full-text searchable: %d hits", len(res.Records))
	}
	if res, _ := store.Search(ctx, "zebracorn", nil, "svc-repo", nsOpt); len(res.Records) != 0 {
		t.Errorf("old content still searchable: %d hits", len(res.Records))
	}
	if sim := cosine(r.Embedding, unitVec(1, 0.99)); sim < 0.999 {
		t.Errorf("embedding not recomputed (cosine to new vector %.3f)", sim)
	}
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// SUPERSEDE: old row deprecated with superseded_by -> new row, new row is
// active (PR source) and lives in the right namespace.
func TestServiceSupersedeDeprecatesOldAndCreatesNew(t *testing.T) {
	store, done := newServiceStore(t)
	defer done()
	emb := &fixedEmbedder{byTitle: map[string][]float32{"Old rule": unitVec(1, 1), "New rule": unitVec(1, 0.95)}}
	svc := newTestService(t, store, emb)
	ctx := context.Background()

	old, err := svc.Store(ctx, storeReq("Old rule", "do it the old way", record.SourceSession))
	if err != nil {
		t.Fatal(err)
	}

	req := storeReq("New rule", "do it the new way", record.SourcePR)
	req.ExtractionDecision = &memory.ExtractionDecision{Action: memory.ActionSupersede, TargetID: &old.ID}
	resp, err := svc.Store(ctx, req)
	if err != nil || resp.Decision != memory.ActionSupersede || resp.ID == old.ID {
		t.Fatalf("supersede: %+v %v", resp, err)
	}

	oldRec, newRec := mustGet(t, store, old.ID), mustGet(t, store, resp.ID)
	if oldRec.Status != record.StatusDeprecated || oldRec.DeprecationReason == nil || oldRec.SupersededBy == nil || *oldRec.SupersededBy != resp.ID {
		t.Errorf("old record: status=%s reason=%v superseded_by=%v", oldRec.Status, oldRec.DeprecationReason, oldRec.SupersededBy)
	}
	if newRec.Status != record.StatusActive || newRec.Namespace != testNS || newRec.Title != "New rule" {
		t.Errorf("new record: status=%s ns=%q title=%q", newRec.Status, newRec.Namespace, newRec.Title)
	}

	// the superseding fact is findable; the deprecated one is not
	res, err := store.Search(ctx, "rule", unitVec(1, 0.95), "svc-repo", memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.Records[0].ID != resp.ID {
		t.Errorf("search after supersede: %d records, want only the new one", len(res.Records))
	}
}

// failCreateTx makes the transaction's Create fail, to prove SUPERSEDE rolls
// back as one unit.
type failCreateTx struct{ memory.TxStore }

func (failCreateTx) Create(context.Context, *record.Record) (*record.Record, error) {
	return nil, errors.New("injected create failure")
}

type failingStore struct{ *Store }

func (f failingStore) WithTx(ctx context.Context, fn func(tx memory.TxStore) error) error {
	return f.Store.WithTx(ctx, func(tx memory.TxStore) error { return fn(failCreateTx{tx}) })
}

func TestServiceSupersedeRollsBackOnFailure(t *testing.T) {
	store, done := newServiceStore(t)
	defer done()
	emb := &fixedEmbedder{byTitle: map[string][]float32{"Old rule": unitVec(1, 1)}}
	ctx := context.Background()

	old, err := newTestService(t, store, emb).Store(ctx, storeReq("Old rule", "old way", record.SourceSession))
	if err != nil {
		t.Fatal(err)
	}

	req := storeReq("New rule", "new way", record.SourcePR)
	req.ExtractionDecision = &memory.ExtractionDecision{Action: memory.ActionSupersede, TargetID: &old.ID}
	if _, err := newTestService(t, failingStore{store}, emb).Store(ctx, req); err == nil {
		t.Fatal("expected the injected failure to surface")
	}

	oldRec := mustGet(t, store, old.ID)
	if oldRec.Status == record.StatusDeprecated || oldRec.SupersededBy != nil || oldRec.DeprecationReason != nil {
		t.Errorf("old record not rolled back: status=%s superseded_by=%v", oldRec.Status, oldRec.SupersededBy)
	}
	all, err := store.List(ctx, memory.ListFilters{})
	if err != nil || len(all) != 1 {
		t.Errorf("rows after rollback = %d (err %v), want 1", len(all), err)
	}
}

// UpdateRecord with a content change re-embeds and the full-text index
// reflects the new content (AC-8, AC-55).
func TestServiceUpdateRecordContentReembedsAndReindexes(t *testing.T) {
	store, done := newServiceStore(t)
	defer done()
	emb := &fixedEmbedder{byTitle: map[string][]float32{"Pool size": unitVec(1, 1)}}
	svc := newTestService(t, store, emb)
	ctx := context.Background()

	added, err := svc.Store(ctx, storeReq("Pool size", "pool is walrusold", record.SourceInline))
	if err != nil {
		t.Fatal(err)
	}
	before := emb.calls

	newContent := "pool is okapinew"
	emb.byTitle["Pool size"] = unitVec(1, 0.3) // what re-embedding must now produce
	rec, err := svc.UpdateRecord(ctx, &memory.UpdateRequest{ID: added.ID, Content: &newContent})
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if emb.calls != before+1 {
		t.Errorf("embedder calls %d -> %d, want exactly one re-embed", before, emb.calls)
	}
	if !strings.Contains(rec.Content, "okapinew") {
		t.Errorf("content = %q", rec.Content)
	}
	got := mustGet(t, store, added.ID)
	if sim := cosine(got.Embedding, unitVec(1, 0.3)); sim < 0.999 {
		t.Errorf("stored embedding is stale (cosine to recomputed vector %.3f)", sim)
	}

	nsOpt := memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5}
	if res, _ := store.Search(ctx, "okapinew", nil, "svc-repo", nsOpt); len(res.Records) != 1 {
		t.Errorf("new content not searchable")
	}
	if res, _ := store.Search(ctx, "walrusold", nil, "svc-repo", nsOpt); len(res.Records) != 0 {
		t.Errorf("old content still searchable")
	}
}

// A different fact in the same repo is simply ADDed (below the ask band) and
// does not touch the neighbour; a candidate in the judgment band is returned
// to the caller without writing.
func TestServiceAddVersusJudgmentBand(t *testing.T) {
	store, done := newServiceStore(t)
	defer done()
	emb := &fixedEmbedder{byTitle: map[string][]float32{
		"Base fact": unitVec(1, 1),
		"Far fact":  unitVec(2, 0.2),  // cosine to Base: 0.2*0 ... orthogonal-ish (<0.65)
		"Near fact": unitVec(1, 0.75), // in the 0.65..0.85 judgment band vs Base
	}}
	svc := newTestService(t, store, emb)
	ctx := context.Background()

	base, err := svc.Store(ctx, storeReq("Base fact", "base", record.SourceInline))
	if err != nil {
		t.Fatal(err)
	}

	far, err := svc.Store(ctx, storeReq("Far fact", "far", record.SourceInline))
	if err != nil || far.Decision != memory.ActionAdd || far.ID == "" || far.ID == base.ID {
		t.Fatalf("far fact: %+v %v", far, err)
	}

	near, err := svc.Store(ctx, storeReq("Near fact", "near", record.SourceInline))
	if err != nil {
		t.Fatal(err)
	}
	if near.ID != "" || len(near.CandidatesConsidered) == 0 {
		t.Fatalf("judgment band should return candidates without writing: %+v", near)
	}
	all, _ := store.List(ctx, memory.ListFilters{})
	if len(all) != 2 {
		t.Errorf("rows = %d, want 2 (nothing written in the judgment band)", len(all))
	}
}
