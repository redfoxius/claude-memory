package memory

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"claude-memory/internal/record"
)

func TestNormalizeFiles(t *testing.T) {
	const dir = "/work/repo"
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"plain", []string{"a/b.go"}, []string{"a/b.go"}},
		{"line suffix", []string{"a/b.go:42", "c.go:10-20"}, []string{"a/b.go", "c.go"}},
		{"colon in name kept", []string{"a:b.go", "x:y-z"}, []string{"a:b.go", "x:y-z"}},
		{"absolute under dir", []string{"/work/repo/pkg/x.go:7"}, []string{"pkg/x.go"}},
		{"absolute outside", []string{"/etc/passwd", "ok.go"}, []string{"ok.go"}},
		{"escape", []string{"../x.go", "a/../../y.go", "a/../z.go"}, []string{"z.go"}},
		{"empty and dot", []string{"", "  ", "."}, []string{}},
		{"dedupe", []string{"a.go", "./a.go", "a.go:3"}, []string{"a.go"}},
	}
	for _, c := range cases {
		if got := NormalizeFiles(dir, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	many := make([]string, 30)
	for i := range many {
		many[i] = "f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go"
	}
	if got := NormalizeFiles(dir, many); len(got) != maxStaleFiles {
		t.Errorf("cap: got %d files, want %d", len(got), maxStaleFiles)
	}
}

type fakeHistory struct {
	headCalls    atomic.Int32
	changedCalls atomic.Int32
	head         string
	dirtyCalls   atomic.Int32
	dirty        bool
	dirtyErr     error
	changedFn    func(ctx context.Context, sha string, files []string) (bool, int, error)
}

func (f *fakeHistory) Resolve(context.Context, string) (Checkout, string, bool, error) {
	return Checkout{}, "", false, nil
}
func (f *fakeHistory) Head(context.Context, string) (string, error) {
	f.headCalls.Add(1)
	return f.head, nil
}
func (f *fakeHistory) Changed(ctx context.Context, _, _, sha string, files []string) (bool, int, error) {
	f.changedCalls.Add(1)
	return f.changedFn(ctx, sha, files)
}
func (f *fakeHistory) Dirty(context.Context, string, []string) (bool, error) {
	f.dirtyCalls.Add(1)
	return f.dirty, f.dirtyErr
}

const goodSHA = "abcdef1234567"

func staleSvc(h CodeHistory, ceiling time.Duration) *Service {
	return New(&mockStore{}, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg()).
		WithCheckout(Checkout{Dir: "/work/repo", Repo: "repo"}).
		WithCodeHistory(h, ceiling)
}

func rec(repo, sha string, files ...string) *SearchRecord {
	return &SearchRecord{ID: "id", Repo: repo, CommitSHA: sha, Files: files}
}

func TestAnnotateStale_RowsAndNoAdapterCalls(t *testing.T) {
	changed := func(context.Context, string, []string) (bool, int, error) { return true, 2, nil }

	t.Run("changed", func(t *testing.T) {
		f := &fakeHistory{head: "H1", changedFn: changed}
		r := rec("repo", goodSHA, "a.go")
		staleSvc(f, time.Second).annotateStale(context.Background(), []*SearchRecord{r}, 0)
		if r.Stale == nil || r.Stale.Commits != 2 {
			t.Fatalf("Stale = %+v, want commits 2", r.Stale)
		}
	})

	t.Run("fresh", func(t *testing.T) {
		f := &fakeHistory{head: "H1", changedFn: func(context.Context, string, []string) (bool, int, error) { return false, 0, nil }}
		r := rec("repo", goodSHA, "a.go")
		staleSvc(f, time.Second).annotateStale(context.Background(), []*SearchRecord{r}, 0)
		if r.Stale != nil {
			t.Fatalf("Stale = %+v, want nil", r.Stale)
		}
	})

	t.Run("adapter error is unchecked", func(t *testing.T) {
		f := &fakeHistory{head: "H1", changedFn: func(context.Context, string, []string) (bool, int, error) { return true, 3, errors.New("boom") }}
		r := rec("repo", goodSHA, "a.go")
		staleSvc(f, time.Second).annotateStale(context.Background(), []*SearchRecord{r}, 0)
		if r.Stale != nil {
			t.Fatalf("an errored check must not flag: %+v", r.Stale)
		}
	})

	// The first three conditions are decided by the service: zero adapter calls.
	for name, r := range map[string]*SearchRecord{
		"no sha":       rec("repo", "", "a.go"),
		"bad sha":      rec("repo", "not-a-sha", "a.go"),
		"short sha":    rec("repo", "abc12", "a.go"),
		"no files":     rec("repo", goodSHA),
		"only outside": rec("repo", goodSHA, "/etc/passwd", "../x"),
		"star repo":    rec("*", goodSHA, "a.go"),
		"foreign repo": rec("other", goodSHA, "a.go"),
		"empty repo":   rec("", goodSHA, "a.go"),
	} {
		f := &fakeHistory{head: "H1", changedFn: changed}
		staleSvc(f, time.Second).annotateStale(context.Background(), []*SearchRecord{r}, 0)
		if f.changedCalls.Load() != 0 || f.headCalls.Load() != 0 || r.Stale != nil {
			t.Errorf("%s: changed=%d head=%d stale=%v, want no adapter calls and no flag", name, f.changedCalls.Load(), f.headCalls.Load(), r.Stale)
		}
	}

	t.Run("no checkout or history", func(t *testing.T) {
		svc := New(&mockStore{}, &mockEmbeddingProvider{}, &mockScrubber{}, &mockClock{}, writepathCfg())
		r := rec("repo", goodSHA, "a.go")
		svc.annotateStale(context.Background(), []*SearchRecord{r}, 0)
		if r.Stale != nil {
			t.Error("flagged without a checkout")
		}
	})

	t.Run("empty HEAD (unborn) is unchecked", func(t *testing.T) {
		f := &fakeHistory{head: "", changedFn: changed}
		r := rec("repo", goodSHA, "a.go")
		staleSvc(f, time.Second).annotateStale(context.Background(), []*SearchRecord{r}, 0)
		if f.changedCalls.Load() != 0 || r.Stale != nil {
			t.Error("checked against an empty HEAD")
		}
	})
}

func TestAnnotateStale_OnlyTopTenAndOneHeadRead(t *testing.T) {
	f := &fakeHistory{head: "H1", changedFn: func(context.Context, string, []string) (bool, int, error) { return true, 1, nil }}
	recs := make([]*SearchRecord, 15)
	for i := range recs {
		recs[i] = rec("repo", goodSHA, "a.go")
	}
	staleSvc(f, time.Second).annotateStale(context.Background(), recs, 0)
	if got := f.changedCalls.Load(); got != maxStaleChecksPerSearch {
		t.Errorf("Changed calls = %d, want %d", got, maxStaleChecksPerSearch)
	}
	if f.headCalls.Load() != 1 {
		t.Errorf("Head calls = %d, want 1", f.headCalls.Load())
	}
	if recs[10].Stale != nil {
		t.Error("record beyond the top 10 was checked")
	}
}

func TestAnnotateStale_PinnedHeadSkipsHeadRead(t *testing.T) {
	f := &fakeHistory{head: "H1", changedFn: func(context.Context, string, []string) (bool, int, error) { return true, 1, nil }}
	r := rec("repo", goodSHA, "a.go")
	staleSvc(f, time.Second).WithPinnedHead("PIN").annotateStale(context.Background(), []*SearchRecord{r}, 0)
	if f.headCalls.Load() != 0 || r.Stale == nil {
		t.Errorf("head calls = %d, stale = %v", f.headCalls.Load(), r.Stale)
	}
}

func TestAnnotateStale_DeadlineBoundsBlockingChecks(t *testing.T) {
	// An adapter that ignores its context entirely.
	block := make(chan struct{})
	defer close(block)
	f := &fakeHistory{head: "H1", changedFn: func(context.Context, string, []string) (bool, int, error) {
		<-block
		return true, 1, nil
	}}
	r := rec("repo", goodSHA, "a.go")
	start := time.Now()
	staleSvc(f, 30*time.Millisecond).annotateStale(context.Background(), []*SearchRecord{r}, 0)
	if el := time.Since(start); el > 150*time.Millisecond {
		t.Errorf("annotateStale took %v, want ~deadline+10ms", el)
	}
	if r.Stale != nil {
		t.Error("flagged despite the check not finishing")
	}
}

func TestAnnotateStale_HookBudgetDeadlineIsAnUpperBound(t *testing.T) {
	var sawDeadline time.Time
	f := &fakeHistory{head: "H1", changedFn: func(ctx context.Context, _ string, _ []string) (bool, int, error) {
		sawDeadline, _ = ctx.Deadline()
		return false, 0, nil
	}}
	budget := time.Now().Add(20 * time.Millisecond)
	svc := staleSvc(f, time.Second).WithStaleDeadline(budget)
	svc.annotateStale(context.Background(), []*SearchRecord{rec("repo", goodSHA, "a.go")}, 0)
	if sawDeadline.After(budget) {
		t.Errorf("check deadline %v is after the hook budget %v", sawDeadline, budget)
	}

	// Budget already spent: the adapter is still consulted (so a cache can
	// answer) but with an expired context.
	f2 := &fakeHistory{head: "H1", changedFn: func(ctx context.Context, _ string, _ []string) (bool, int, error) {
		if ctx.Err() == nil {
			t.Error("expected an already-expired context")
		}
		return false, 0, ctx.Err()
	}}
	r := rec("repo", goodSHA, "a.go")
	staleSvc(f2, time.Second).WithStaleDeadline(time.Now().Add(-time.Second)).annotateStale(context.Background(), []*SearchRecord{r}, 0)
	if r.Stale != nil {
		t.Error("flagged on an expired budget")
	}
}

func TestStaleHint_SingleRecordAndHeadChangeChangesKey(t *testing.T) {
	f := &fakeHistory{head: "H1"}
	f.changedFn = func(context.Context, string, []string) (bool, int, error) { return true, 0, nil }
	svc := staleSvc(f, time.Second)
	sha := goodSHA
	r := &record.Record{Repo: "repo", CommitSHA: &sha, Files: []string{"a.go"}}

	if h := svc.StaleHint(context.Background(), r); h == nil || h.Commits != 0 {
		t.Fatalf("StaleHint = %+v, want stale with unknown count", h)
	}
	r2 := &record.Record{Repo: "other", CommitSHA: &sha, Files: []string{"a.go"}}
	if svc.StaleHint(context.Background(), r2) != nil {
		t.Error("foreign repo flagged")
	}
	if f.headCalls.Load() != 1 {
		t.Errorf("Head calls = %d, want 1 (foreign repo makes none)", f.headCalls.Load())
	}
}

// A spent budget must still deliver results that are instantly available
// (cache hits) — the grace timer must not fire before they are collected.
func TestAnnotateStale_SpentBudgetStillDeliversReadyResults(t *testing.T) {
	f := &fakeHistory{head: "H1", changedFn: func(ctx context.Context, _ string, _ []string) (bool, int, error) {
		return true, 4, nil // as a cache hit would, ignoring the expired ctx
	}}
	recs := make([]*SearchRecord, 20)
	for i := range recs {
		recs[i] = rec("repo", goodSHA, "a.go")
	}
	svc := staleSvc(f, time.Second).WithStaleDeadline(time.Now().Add(-time.Second))
	for i := 0; i < 50; i++ {
		for _, r := range recs {
			r.Stale = nil
		}
		svc.annotateStale(context.Background(), recs, 0)
		for j := 0; j < maxStaleChecksPerSearch; j++ {
			if recs[j].Stale == nil {
				t.Fatalf("iteration %d: result %d dropped although its verdict was ready", i, j)
			}
		}
	}
}

// Results below the caller's similarity floor never reach git.
func TestAnnotateStale_BelowMinSimilarityNotChecked(t *testing.T) {
	f := &fakeHistory{head: "H1", changedFn: func(context.Context, string, []string) (bool, int, error) { return true, 1, nil }}
	low, high := rec("repo", goodSHA, "a.go"), rec("repo", goodSHA, "a.go")
	low.Similarity, high.Similarity = 0.2, 0.8
	staleSvc(f, time.Second).annotateStale(context.Background(), []*SearchRecord{low, high}, 0.5)
	if f.changedCalls.Load() != 1 || low.Stale != nil || high.Stale == nil {
		t.Errorf("changed calls=%d low=%v high=%v, want 1 call flagging only high", f.changedCalls.Load(), low.Stale, high.Stale)
	}

	f2 := &fakeHistory{head: "H1", changedFn: f.changedFn}
	staleSvc(f2, time.Second).annotateStale(context.Background(), []*SearchRecord{low}, 0.5)
	if f2.changedCalls.Load() != 0 || f2.headCalls.Load() != 0 {
		t.Errorf("nothing eligible, yet head=%d changed=%d", f2.headCalls.Load(), f2.changedCalls.Load())
	}
}

// An unborn HEAD that was pinned (empty) is not re-read from git.
func TestPinnedEmptyHeadIsNotReRead(t *testing.T) {
	f := &fakeHistory{head: "H1", changedFn: func(context.Context, string, []string) (bool, int, error) { return true, 1, nil }}
	r := rec("repo", goodSHA, "a.go")
	staleSvc(f, time.Second).WithPinnedHead("").annotateStale(context.Background(), []*SearchRecord{r}, 0)
	if f.headCalls.Load() != 0 || f.changedCalls.Load() != 0 || r.Stale != nil {
		t.Errorf("head=%d changed=%d stale=%v", f.headCalls.Load(), f.changedCalls.Load(), r.Stale)
	}
}
