package memory

import (
	"context"
	"errors"
	"testing"

	"claude-memory/internal/record"
)

const headSHA = "0123456789abcdef0123456789abcdef01234567"

func baselineReq(repo string, files ...string) *StoreRequest {
	r := baseStoreRequest()
	r.Repo = repo
	r.Files = files
	return r
}

func storeAndGetCreated(t *testing.T, svc *Service, store *mockStore, req *StoreRequest) *record.Record {
	t.Helper()
	var created *record.Record
	store.CreateFunc = func(ctx context.Context, r *record.Record) (*record.Record, error) {
		created = r
		return r, nil
	}
	if _, err := svc.Store(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	return created
}

func TestBaseline_StampedOnlyWhenAllConditionsHold(t *testing.T) {
	co := Checkout{Dir: "/w/repo", Repo: "repo"}
	cases := []struct {
		name  string
		hist  *fakeHistory
		req   *StoreRequest
		pin   string
		noCo  bool
		stamp bool
	}{
		{"clean inline", &fakeHistory{head: headSHA}, baselineReq("repo", "a.go"), "", false, true},
		{"session source", &fakeHistory{head: headSHA}, func() *StoreRequest {
			r := baselineReq("repo", "a.go")
			r.Source = record.SourceSession
			return r
		}(), "", false, true},
		{"pinned head", &fakeHistory{head: "ignored"}, baselineReq("repo", "a.go"), headSHA, false, true},
		{"star repo", &fakeHistory{head: headSHA}, baselineReq("*", "a.go"), "", false, false},
		{"other repo", &fakeHistory{head: headSHA}, baselineReq("other", "a.go"), "", false, false},
		{"no files", &fakeHistory{head: headSHA}, baselineReq("repo"), "", false, false},
		{"only outside files", &fakeHistory{head: headSHA}, baselineReq("repo", "/etc/passwd"), "", false, false},
		{"no checkout", &fakeHistory{head: headSHA}, baselineReq("repo", "a.go"), "", true, false},
		{"unborn head", &fakeHistory{head: ""}, baselineReq("repo", "a.go"), "", false, false},
		{"dirty files", &fakeHistory{head: headSHA, dirty: true}, baselineReq("repo", "a.go"), "", false, false},
		{"dirty error", &fakeHistory{head: headSHA, dirtyErr: errors.New("x")}, baselineReq("repo", "a.go"), "", false, false},
		{"pr source never stamped", &fakeHistory{head: headSHA}, func() *StoreRequest {
			r := baselineReq("repo", "a.go")
			r.Source = record.SourcePR
			return r
		}(), "", false, false},
	}
	for _, c := range cases {
		store := &mockStore{}
		svc := nsService(store, "global").WithCodeHistory(c.hist, 0)
		if !c.noCo {
			svc = svc.WithCheckout(co)
		}
		if c.pin != "" {
			svc = svc.WithPinnedHead(c.pin)
		}
		got := storeAndGetCreated(t, svc, store, c.req)
		stamped := got.CommitSHA != nil
		if stamped != c.stamp {
			t.Errorf("%s: stamped=%v, want %v", c.name, stamped, c.stamp)
		}
		if stamped && *got.CommitSHA != headSHA {
			t.Errorf("%s: commit_sha = %q, want %q", c.name, *got.CommitSHA, headSHA)
		}
	}
}

func TestBaseline_ExplicitCommitSHAWins(t *testing.T) {
	explicit := "deadbeefdeadbeef"
	store := &mockStore{}
	h := &fakeHistory{head: headSHA}
	svc := nsService(store, "global").WithCodeHistory(h, 0).WithCheckout(Checkout{Dir: "/w/repo", Repo: "repo"})
	req := baselineReq("repo", "a.go")
	req.CommitSHA = &explicit
	got := storeAndGetCreated(t, svc, store, req)
	if got.CommitSHA == nil || *got.CommitSHA != explicit {
		t.Errorf("commit_sha = %v, want explicit", got.CommitSHA)
	}
	if h.dirtyCalls.Load() != 0 {
		t.Error("git consulted although an explicit commit_sha was given")
	}
}

func TestBaseline_NoGitInsideTransaction(t *testing.T) {
	// The baseline is computed before WithTx (no git under the advisory lock).
	var inTx bool
	h := &fakeHistory{head: headSHA}
	store := &mockStore{WithTxFunc: func(ctx context.Context, fn func(tx TxStore) error) error {
		inTx = true
		defer func() { inTx = false }()
		return fn(&mockTxStore{store: &mockStore{CreateFunc: func(ctx context.Context, r *record.Record) (*record.Record, error) { return r, nil }}})
	}}
	hh := &txCheckHistory{fakeHistory: h, inTx: &inTx, t: t}
	svc := nsService(store, "global").WithCodeHistory(hh, 0).WithCheckout(Checkout{Dir: "/w/repo", Repo: "repo"})
	if _, err := svc.Store(context.Background(), baselineReq("repo", "a.go")); err != nil {
		t.Fatal(err)
	}
}

type txCheckHistory struct {
	*fakeHistory
	inTx *bool
	t    *testing.T
}

func (h *txCheckHistory) Dirty(ctx context.Context, d string, f []string) (bool, error) {
	if *h.inTx {
		h.t.Error("git (Dirty) ran inside the write transaction")
	}
	return h.fakeHistory.Dirty(ctx, d, f)
}

func TestUpdateRecord_Baseline(t *testing.T) {
	existing := &record.Record{ID: "r1", Namespace: "global", Repo: "repo", Files: []string{"a.go"}, Title: "t", Content: "c"}
	run := func(t *testing.T, h *fakeHistory, req *UpdateRequest) map[string]interface{} {
		var got map[string]interface{}
		store := &mockStore{
			GetFunc: func(context.Context, string) (*record.Record, error) { return existing, nil },
			UpdateFunc: func(_ context.Context, id string, u map[string]interface{}) (*record.Record, error) {
				got = u
				return existing, nil
			},
		}
		svc := nsService(store, "global").WithCodeHistory(h, 0).WithCheckout(Checkout{Dir: "/w/repo", Repo: "repo"})
		if _, err := svc.UpdateRecord(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		return got
	}
	newContent := "new"

	got := run(t, &fakeHistory{head: headSHA}, &UpdateRequest{ID: "r1", Content: &newContent})
	if got["commit_sha"] != headSHA {
		t.Errorf("content change on clean files: commit_sha = %v, want re-baseline", got["commit_sha"])
	}

	got = run(t, &fakeHistory{head: headSHA, dirty: true}, &UpdateRequest{ID: "r1", Content: &newContent})
	if _, ok := got["commit_sha"]; ok {
		t.Error("dirty files must leave commit_sha unchanged")
	}

	title := "only title"
	got = run(t, &fakeHistory{head: headSHA}, &UpdateRequest{ID: "r1", Title: &title})
	if _, ok := got["commit_sha"]; ok {
		t.Error("a title-only update must not re-baseline")
	}

	explicit := "cafebabecafebabe"
	got = run(t, &fakeHistory{head: headSHA, dirty: true}, &UpdateRequest{ID: "r1", CommitSHA: &explicit})
	if got["commit_sha"] != explicit {
		t.Errorf("explicit commit_sha (allowed alone, even when dirty) = %v", got["commit_sha"])
	}

	bad := "not a sha"
	svc := nsService(&mockStore{GetFunc: func(context.Context, string) (*record.Record, error) { return existing, nil }}, "global")
	if _, err := svc.UpdateRecord(context.Background(), &UpdateRequest{ID: "r1", CommitSHA: &bad}); err == nil {
		t.Error("invalid commit_sha accepted")
	}
}
