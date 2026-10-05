package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/prsource"
)

// fakeRunner answers by joined argv; calls records every argv.
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

var repo = prsource.RepoRef{Provider: prsource.ProviderGitHub, Host: "github.com", Path: "acme/widgets", LocalPath: "/tmp/widgets"}

// page builds a JSON array of n PRs: the template item with numbers start..,
// all merged at `merged` and updated at `updated`.
func page(start, n int, merged, updated string) []byte {
	var items []string
	for i := 0; i < n; i++ {
		items = append(items, fmt.Sprintf(`{"number":%d,"title":"t","body":"b","html_url":"u","merged_at":%s,"updated_at":%q,"merge_commit_sha":"c%d","user":{"login":"a","type":"User"},"author_association":"MEMBER"}`,
			start+i, merged, updated, start+i))
	}
	return []byte("[" + strings.Join(items, ",") + "]")
}

func TestListCompletedFiltersSortsAndMapsFields(t *testing.T) {
	list := `[` + string(fixture(t, "pull_123.json")) + `,` + string(fixture(t, "pull_item.json")) + `,
	 {"number":7,"title":"closed unmerged","merged_at":null,"updated_at":"2026-09-22T00:00:00Z","user":{"login":"a","type":"User"},"author_association":"MEMBER"},
	 {"number":8,"title":"dependabot","merged_at":"2026-09-22T01:00:00Z","updated_at":"2026-09-22T01:00:00Z","user":{"login":"dependabot[bot]","type":"Bot"},"author_association":"NONE"},
	 {"number":5,"title":"old","merged_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","user":{"login":"a","type":"User"},"author_association":"MEMBER"}]`
	r := &fakeRunner{answers: map[string][]byte{"repos/acme/widgets/pulls -f state": []byte(list)}}
	prs, err := New(r).ListCompleted(context.Background(), repo, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 3 {
		t.Fatalf("got %d PRs: %+v", len(prs), prs)
	}
	if prs[0].ID != "101" || prs[1].ID != "123" || prs[2].ID != "8" {
		t.Errorf("not sorted by merged_at asc: %v %v %v", prs[0].ID, prs[1].ID, prs[2].ID)
	}
	if prs[1].MergeCommit != "bbbb222" || prs[1].URL != "https://github.com/acme/widgets/pull/123" || prs[1].Bot || !prs[1].Trusted {
		t.Errorf("mapping: %+v", prs[1])
	}
	if !prs[2].Bot || prs[2].Trusted {
		t.Errorf("bot PR must be flagged: %+v", prs[2])
	}
	if len(r.calls) != 1 {
		t.Errorf("an old item on page 1 must stop paging, got %d calls", len(r.calls))
	}
}

func TestArgvShape(t *testing.T) {
	r := &fakeRunner{answers: map[string][]byte{"pulls": []byte(`[]`)}}
	if _, err := New(r).ListCompleted(context.Background(), repo, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := []string{"api", "--hostname", "github.com", "-X", "GET", "repos/acme/widgets/pulls",
		"-f", "state=closed", "-f", "sort=updated", "-f", "direction=desc", "-f", "per_page=100", "-f", "page=1"}
	if got := r.calls[0]; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q", got)
	}
	for _, a := range r.calls[0] {
		if strings.Contains(strings.ToLower(a), "token") {
			t.Errorf("token in argv: %q", a)
		}
	}
}

func TestPagingContinuesOnFullPagesThenStopsOnShort(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &fakeRunner{answers: map[string][]byte{
		"-f page=1 ": page(1000, 100, `"2026-09-01T00:00:00Z"`, "2026-09-01T00:00:00Z"),
		"-f page=2 ": page(2000, 3, `"2026-08-01T00:00:00Z"`, "2026-08-01T00:00:00Z"),
	}}
	prs, err := New(r).ListCompleted(context.Background(), repo, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 103 || len(r.calls) != 2 {
		t.Errorf("prs=%d calls=%d", len(prs), len(r.calls))
	}
}

func TestPagingDeduplicatesAcrossPages(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &fakeRunner{answers: map[string][]byte{
		"-f page=1 ": page(1, 100, `"2026-09-01T00:00:00Z"`, "2026-09-01T00:00:00Z"),
		"-f page=2 ": page(100, 2, `"2026-08-01T00:00:00Z"`, "2026-08-01T00:00:00Z"), // 100 repeats
	}}
	prs, err := New(r).ListCompleted(context.Background(), repo, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 101 {
		t.Errorf("want 101 unique PRs, got %d", len(prs))
	}
}

func TestPageCapIsAnError(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &fakeRunner{answers: map[string][]byte{"repos/acme": page(1, 100, `"2026-09-01T00:00:00Z"`, "2026-09-01T00:00:00Z")}}
	_, err := New(r).ListCompleted(context.Background(), repo, since)
	if !errors.Is(err, prsource.ErrPageCap) {
		t.Fatalf("want ErrPageCap, got %v", err)
	}
	if len(r.calls) != 10 {
		t.Errorf("want 10 pages fetched, got %d", len(r.calls))
	}
}

func TestListErrorAndBadJSONFail(t *testing.T) {
	r := &fakeRunner{errs: map[string]error{"pulls": errors.New("gh: HTTP 401")}}
	if _, err := New(r).ListCompleted(context.Background(), repo, time.Now()); err == nil {
		t.Errorf("want error, got nil")
	}
	r = &fakeRunner{answers: map[string][]byte{"pulls": []byte(`{not json`)}}
	if _, err := New(r).ListCompleted(context.Background(), repo, time.Now()); err == nil {
		t.Error("bad JSON must fail")
	}
}

func TestInvalidPathNeverReachesRunner(t *testing.T) {
	r := &fakeRunner{}
	for _, p := range []string{"{owner}/{repo}", "a/b/c", "a/..", "a/b:c", "a"} {
		bad := repo
		bad.Path = p
		if _, err := New(r).ListCompleted(context.Background(), bad, time.Now()); err == nil {
			t.Errorf("path %q accepted", p)
		}
		if _, err := New(r).Get(context.Background(), bad, "1"); err == nil {
			t.Errorf("Get path %q accepted", p)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("runner was called: %v", r.calls)
	}
	if _, err := New(r).Get(context.Background(), repo, "1; rm"); err == nil {
		t.Error("non-numeric id accepted")
	}
}

func TestGetKeepsOnlyTrustedNonBotNonEmptyComments(t *testing.T) {
	r := &fakeRunner{answers: map[string][]byte{
		"repos/acme/widgets/pulls/123 ":           fixture(t, "pull_123.json"),
		"repos/acme/widgets/issues/123/comments ": fixture(t, "issue_comments.json"),
		"repos/acme/widgets/pulls/123/comments ":  fixture(t, "review_comments.json"),
	}}
	pr, err := New(r).Get(context.Background(), repo, "123")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Title != "Switch to squash merges" || pr.MergeCommit != "bbbb222" {
		t.Errorf("detail: %+v", pr)
	}
	want := []string{"Why not rebase?", "Prefer context.WithTimeout here."}
	if strings.Join(pr.ReviewComments, "|") != strings.Join(want, "|") {
		t.Errorf("comments = %q", pr.ReviewComments)
	}
}

func TestGetDegradesWhenCommentsFail(t *testing.T) {
	r := &fakeRunner{
		answers: map[string][]byte{"pulls/123 ": fixture(t, "pull_123.json")},
		errs:    map[string]error{"/comments": errors.New("boom")},
	}
	// errs are matched first, so the detail call (no "/comments") still succeeds.
	pr, err := New(r).Get(context.Background(), repo, "123")
	if err != nil || len(pr.ReviewComments) != 0 {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
}

func TestGetDetailFailureFails(t *testing.T) {
	r := &fakeRunner{errs: map[string]error{"pulls/123 ": errors.New("boom")}}
	if _, err := New(r).Get(context.Background(), repo, "123"); err == nil {
		t.Error("detail failure must fail Get")
	}
}
