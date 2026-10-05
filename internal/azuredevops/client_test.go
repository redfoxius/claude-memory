package azuredevops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/prsource"
)

// fakeRunner is a scripted Runner for testing: it maps a key derived from
// the az subcommand (args[0]+args[1], e.g. "repos pr") to a canned
// response, so a test can fail one az call while leaving others working.
type fakeRunner struct {
	responses map[string]fakeResponse
	calls     [][]string
}

type fakeResponse struct {
	output []byte
	err    error
}

func (f *fakeRunner) Run(ctx context.Context, dir string, args []string) ([]byte, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args[:min(2, len(args))], " ")
	if r, ok := f.responses[key]; ok {
		return r.output, r.err
	}
	return nil, errors.New("fakeRunner: no scripted response for " + key)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestListCompletedFiltersAndOrdersBySince(t *testing.T) {
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"repos pr": {output: []byte(`[
			{"pullRequestId": 3, "title": "newest", "description": "d3", "closedDate": "2026-09-20T10:00:00Z", "status": "completed", "url": "https://example/3"},
			{"pullRequestId": 1, "title": "oldest-after-since", "description": "d1", "closedDate": "2026-09-01T10:00:00Z", "status": "completed", "url": "https://example/1"},
			{"pullRequestId": 2, "title": "too-old", "description": "d2", "closedDate": "2026-08-01T10:00:00Z", "status": "completed", "url": "https://example/2"}
		]`)},
	}}

	client := New(runner)
	since := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	repo := prsource.RepoRef{Name: "billing-service", LocalPath: "/repos/billing-service"}

	prs, err := client.ListCompleted(context.Background(), repo, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// PR 2 (too-old) must be excluded; PR 1 and 3 remain, oldest first.
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %d: %+v", len(prs), prs)
	}
	if prs[0].ID != "1" || prs[1].ID != "3" {
		t.Errorf("expected completion order [1, 3], got [%s, %s]", prs[0].ID, prs[1].ID)
	}
}

func TestListCompletedPropagatesAzFailure(t *testing.T) {
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"repos pr": {err: errors.New("az: auth failed")},
	}}

	client := New(runner)
	repo := prsource.RepoRef{Name: "billing-service", LocalPath: "/repos/billing-service"}

	_, err := client.ListCompleted(context.Background(), repo, time.Time{})
	if err == nil {
		t.Fatal("expected error when az repos pr list fails")
	}
}

func TestGetDegradesGracefullyWithoutOrgProject(t *testing.T) {
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"repos pr": {output: []byte(`{"pullRequestId": 1, "title": "t", "description": "d", "closedDate": "2026-09-01T10:00:00Z", "url": "https://example/1"}`)},
	}}

	client := New(runner)
	// No Org/Project set: reviewComments should degrade to nil, not error.
	repo := prsource.RepoRef{Name: "billing-service", LocalPath: "/repos/billing-service"}

	pr, err := client.Get(context.Background(), repo, "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr.Title != "t" || pr.ReviewComments != nil {
		t.Errorf("unexpected PR: %+v", pr)
	}
}

func TestGetFetchesReviewComments(t *testing.T) {
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"repos pr": {output: []byte(`{"pullRequestId": 1, "title": "t", "description": "d", "closedDate": "2026-09-01T10:00:00Z", "url": "https://example/1"}`)},
		"rest --method": {output: []byte(`{"value": [
			{"isDeleted": false, "comments": [{"commentType": "text", "content": "looks good"}]},
			{"isDeleted": true, "comments": [{"commentType": "text", "content": "should not appear"}]},
			{"isDeleted": false, "comments": [{"commentType": "system", "content": "policy update"}]}
		]}`)},
	}}

	client := New(runner)
	repo := prsource.RepoRef{Org: "acme", Project: "Marketplace", Name: "billing-service", LocalPath: "/repos/billing-service"}

	pr, err := client.Get(context.Background(), repo, "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.ReviewComments) != 1 || pr.ReviewComments[0] != "looks good" {
		t.Errorf("expected exactly one non-deleted text comment, got %v", pr.ReviewComments)
	}
}

func TestMergeCommitParsedFromListAndShow(t *testing.T) {
	list := `[{"pullRequestId":7,"title":"t","description":"d","closedDate":"2026-09-20T10:00:00Z","status":"completed","url":"u","lastMergeCommit":{"commitId":"abcdef1234567"}},
	         {"pullRequestId":8,"title":"t2","description":"d","closedDate":"2026-09-21T10:00:00Z","status":"completed","url":"u"}]`
	c := New(&fakeRunner{responses: map[string]fakeResponse{"repos pr": {output: []byte(list)}}})
	prs, err := c.ListCompleted(context.Background(), prsource.RepoRef{Name: "r", LocalPath: "/x"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 || prs[0].MergeCommit != "abcdef1234567" || prs[1].MergeCommit != "" {
		t.Errorf("list merge commits: %+v", prs)
	}

	show := `{"pullRequestId":7,"title":"t","description":"d","closedDate":"2026-09-20T10:00:00Z","url":"u","lastMergeCommit":{"commitId":"abcdef1234567"}}`
	c = New(&fakeRunner{responses: map[string]fakeResponse{"repos pr": {output: []byte(show)}}})
	pr, err := c.Get(context.Background(), prsource.RepoRef{Name: "r", LocalPath: "/x"}, "7")
	if err != nil || pr.MergeCommit != "abcdef1234567" {
		t.Errorf("show: %+v %v", pr, err)
	}
}

func TestAzureCallsUseRemoteRepoNameNotLocalBasename(t *testing.T) {
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"repos pr":      {output: []byte(`[]`)},
		"rest --method": {output: []byte(`{"value":[]}`)},
	}}
	repo := prsource.RepoRef{Name: "local-clone-dir", RemoteName: "real-repo", Org: "o", Project: "p", LocalPath: "/x"}
	c := New(runner)
	if _, err := c.ListCompleted(context.Background(), repo, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.reviewComments(context.Background(), repo, "5"); err != nil {
		t.Fatal(err)
	}
	all := ""
	for _, c := range runner.calls {
		all += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(all, "--repository real-repo") || !strings.Contains(all, "/repositories/real-repo/") || strings.Contains(all, "local-clone-dir") {
		t.Errorf("calls:\n%s", all)
	}
}
