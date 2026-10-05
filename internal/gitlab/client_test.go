package gitlab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/prsource"
)

// fakeRunner answers by the joined argv (a substring match); calls records
// every argv.
type fakeRunner struct {
	answers map[string][]byte
	errs    map[string]error
	calls   [][]string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args []string) ([]byte, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args, " ") + " "
	for k, err := range f.errs {
		if strings.Contains(key, k) {
			return nil, err
		}
	}
	for k, v := range f.answers {
		if strings.Contains(key, k) {
			return v, nil
		}
	}
	return nil, fmt.Errorf("unexpected call: %s", key)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var repo = prsource.RepoRef{Provider: prsource.ProviderGitLab, Host: "gitlab.com", Path: "grp/sub/proj", LocalPath: "/tmp/proj"}

const proj = "projects/grp%2Fsub%2Fproj"

func baseRunner(t *testing.T) *fakeRunner {
	return &fakeRunner{answers: map[string][]byte{
		proj + "/members/all?":           fixture(t, "members.json"),
		proj + "/merge_requests?":        fixture(t, "mrs_page.json"),
		proj + "/merge_requests/7 ":      fixture(t, "mr_7.json"),
		proj + "/merge_requests/7/notes": fixture(t, "notes.json"),
	}}
}

func TestListCompletedMapsFiltersAndSorts(t *testing.T) {
	r := baseRunner(t)
	prs, err := New(r).ListCompleted(context.Background(), repo, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	byID := map[string]prsource.PR{}
	for _, p := range prs {
		ids = append(ids, p.ID)
		byID[p.ID] = p
	}
	if strings.Join(ids, ",") != "7,8,6,5,4" {
		t.Fatalf("ids = %v (old MR must be filtered, order merged_at asc)", ids)
	}
	// Commit fallback order: merge -> squash -> sha.
	if byID["6"].MergeCommit != "mc666" || byID["7"].MergeCommit != "sq777" || byID["8"].MergeCommit != "head888" {
		t.Errorf("commit selection: 6=%q 7=%q 8=%q", byID["6"].MergeCommit, byID["7"].MergeCommit, byID["8"].MergeCommit)
	}
	if !byID["7"].Trusted || !byID["8"].Trusted {
		t.Error("Developer+ authors must be trusted")
	}
	if byID["5"].Trusted {
		t.Error("Guest author must not be trusted")
	}
	if !byID["4"].Bot {
		t.Error("bot MR must be flagged")
	}
	if byID["7"].URL == "" || byID["7"].Title != "Squash MR" {
		t.Errorf("mapping: %+v", byID["7"])
	}
}

func TestArgvShapeHostnameIsOneArgument(t *testing.T) {
	r := baseRunner(t)
	if _, err := New(r).ListCompleted(context.Background(), repo, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if c[0] != "api" || c[1] != "--hostname=gitlab.com" || len(c) != 3 {
			t.Errorf("argv = %q", c)
		}
	}
	mr := r.calls[1][2]
	for _, want := range []string{proj + "/merge_requests?", "state=merged", "updated_after=", "order_by=updated_at", "sort=desc", "per_page=100", "page=1"} {
		if !strings.Contains(mr, want) {
			t.Errorf("endpoint %q lacks %q", mr, want)
		}
	}
	if strings.Contains(mr, "grp/sub") {
		t.Errorf("project path must be escaped: %q", mr)
	}
}

func TestAuthStatusGateForSelfHostedOnlyOnce(t *testing.T) {
	r := &fakeRunner{answers: map[string][]byte{
		"auth status":     []byte("ok"),
		"/members/all":    []byte(`[]`),
		"/merge_requests": []byte(`[]`),
	}}
	c := New(r)
	self := repo
	self.Host = "git.example.org"
	for i := 0; i < 2; i++ {
		if _, err := c.ListCompleted(context.Background(), self, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, call := range r.calls {
		if call[0] == "auth" {
			n++
			if len(call) != 3 || call[2] != "--hostname=git.example.org" {
				t.Errorf("auth argv = %q", call)
			}
		}
	}
	if n != 1 {
		t.Errorf("auth status ran %d times, want once", n)
	}
	if r.calls[0][0] != "auth" {
		t.Error("auth status must precede the first api call")
	}
}

func TestAuthStatusFailureFailsRepoAndNoAPICall(t *testing.T) {
	r := &fakeRunner{errs: map[string]error{"auth status": errors.New("not logged in")}}
	self := repo
	self.Host = "git.example.org"
	if _, err := New(r).ListCompleted(context.Background(), self, time.Now()); err == nil {
		t.Fatal("want error")
	}
	for _, c := range r.calls {
		if c[0] == "api" {
			t.Errorf("api called despite failed auth gate: %q", c)
		}
	}
}

func TestGitLabComSkipsAuthGate(t *testing.T) {
	r := baseRunner(t)
	if _, err := New(r).ListCompleted(context.Background(), repo, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if c[0] == "auth" {
			t.Error("gitlab.com must not run auth status")
		}
	}
}

func TestMembersFailureFailsClosed(t *testing.T) {
	r := baseRunner(t)
	r.errs = map[string]error{"/members/all": errors.New("403")}
	if _, err := New(r).ListCompleted(context.Background(), repo, time.Now()); err == nil {
		t.Fatal("members failure must fail the repo")
	}
	for _, c := range r.calls {
		if strings.Contains(c[len(c)-1], "merge_requests") {
			t.Error("MRs fetched without a members list")
		}
	}
}

func TestInvalidPathOrHostNeverReachesRunner(t *testing.T) {
	r := &fakeRunner{}
	for _, mut := range []func(*prsource.RepoRef){
		func(x *prsource.RepoRef) { x.Path = "a/:id" },
		func(x *prsource.RepoRef) { x.Path = "a/{b}" },
		func(x *prsource.RepoRef) { x.Path = "a/../b" },
		func(x *prsource.RepoRef) { x.Path = "solo" },
		func(x *prsource.RepoRef) { x.Host = "evil.com --hostname=x" },
		func(x *prsource.RepoRef) { x.Host = "" },
	} {
		bad := repo
		mut(&bad)
		if _, err := New(r).ListCompleted(context.Background(), bad, time.Now()); err == nil {
			t.Errorf("accepted %+v", bad)
		}
		if _, err := New(r).Get(context.Background(), bad, "1"); err == nil {
			t.Errorf("Get accepted %+v", bad)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("runner called: %v", r.calls)
	}
}

func TestPageCapAndDedup(t *testing.T) {
	var items []string
	for i := 0; i < 100; i++ {
		items = append(items, fmt.Sprintf(`{"iid":%d,"merged_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z","merge_commit_sha":"c","author":{"id":1,"username":"alice"}}`, i))
	}
	full := []byte("[" + strings.Join(items, ",") + "]")
	r := &fakeRunner{answers: map[string][]byte{"/members/all": fixture(t, "members.json"), "/merge_requests?": full}}
	_, err := New(r).ListCompleted(context.Background(), repo, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, prsource.ErrPageCap) {
		t.Fatalf("want ErrPageCap, got %v", err)
	}
	// Dedup: page 2 repeats page 1 and is short.
	r = &fakeRunner{answers: map[string][]byte{"/members/all": fixture(t, "members.json"),
		"&page=1&": full, "&page=2&": []byte(`[` + items[0] + `]`)}}
	prs, err := New(r).ListCompleted(context.Background(), repo, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(prs) != 100 {
		t.Errorf("dedup: %d PRs, err %v", len(prs), err)
	}
}

func TestGetKeepsTrustedNonBotNonSystemNotes(t *testing.T) {
	r := baseRunner(t)
	pr, err := New(r).Get(context.Background(), repo, "7")
	if err != nil {
		t.Fatal(err)
	}
	if pr.MergeCommit != "sq777" || len(pr.ReviewComments) != 1 || pr.ReviewComments[0] != "Why squash?" {
		t.Errorf("pr = %+v", pr)
	}
}

func TestGetNotesFailureDegradesDetailFailureFails(t *testing.T) {
	r := baseRunner(t)
	r.errs = map[string]error{"/notes?": errors.New("boom")}
	pr, err := New(r).Get(context.Background(), repo, "7")
	if err != nil || len(pr.ReviewComments) != 0 {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	r = baseRunner(t)
	r.errs = map[string]error{"/merge_requests/7 ": errors.New("boom")}
	if _, err := New(r).Get(context.Background(), repo, "7"); err == nil {
		t.Error("detail failure must fail")
	}
	if _, err := New(r).Get(context.Background(), repo, "7/../x"); err == nil {
		t.Error("non-numeric id accepted")
	}
}

func TestMembersOverCapHasOwnErrorNotPageCap(t *testing.T) {
	var items []string
	for i := 0; i < 100; i++ {
		items = append(items, `{"id":1,"username":"a","access_level":30}`)
	}
	r := &fakeRunner{answers: map[string][]byte{"/members/all": []byte("[" + strings.Join(items, ",") + "]")}}
	_, err := New(r).ListCompleted(context.Background(), repo, time.Now())
	if err == nil || errors.Is(err, prsource.ErrPageCap) || !strings.Contains(err.Error(), "members") {
		t.Fatalf("err = %v", err)
	}
}
