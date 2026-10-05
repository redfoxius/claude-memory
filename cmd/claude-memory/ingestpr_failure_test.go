package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/memory/mock"
	"github.com/redfoxius/claude-memory/internal/prcursor"
	"github.com/redfoxius/claude-memory/internal/prsource"
)

// failingTitleRunner fails the extraction call for any PR whose prompt
// contains one of the failing titles and returns "[]" otherwise.
type failingTitleRunner struct{ failTitles []string }

func (f failingTitleRunner) Run(ctx context.Context, prompt string) ([]byte, error) {
	for _, t := range f.failTitles {
		if strings.Contains(prompt, t) {
			return nil, errors.New("claude: boom")
		}
	}
	return []byte(`[]`), nil
}

// Three PRs completed on days 1, 2, 3; the PR with title "bad" is #2.
func failureBatchSource() *fakeSource {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	mk := func(id, title string, d int) *prsource.PR {
		return &prsource.PR{ID: id, Title: title, Description: "d", CompletedAt: day(d), Trusted: true}
	}
	return &fakeSource{
		listResult: []prsource.PR{*mk("1", "good-one", 1), *mk("2", "bad", 2), *mk("3", "good-three", 3)},
		getResults: map[string]*prsource.PR{"1": mk("1", "good-one", 1), "2": mk("2", "bad", 2), "3": mk("3", "good-three", 3)},
	}
}

func TestIngestOneRepoExtractionFailureCursorHandling(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	seed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		failTitles   []string
		priorFailed  string // seeded failed_pr
		priorCount   int
		wantSince    time.Time
		wantFailedPR string
		wantFailures int
		wantGets     []string
	}{
		{"all succeed advances to the end", nil, "", 0, day(3), "", 0, []string{"1", "2", "3"}},
		{"failure mid-batch keeps cursor before the failed PR", []string{"bad"}, "", 0, day(1), "2", 1, []string{"1", "2"}},
		{"second consecutive failure counts up", []string{"bad"}, "2", 1, day(1), "2", 2, []string{"1", "2"}},
		{"third consecutive failure skips the PR and continues", []string{"bad"}, "2", 2, day(3), "", 0, []string{"1", "2", "3"}},
		{"failure of a different PR restarts the count", []string{"bad"}, "9", 2, day(1), "2", 1, []string{"1", "2"}},
		{"failure of the first PR keeps the previous cursor", []string{"good-one"}, "", 0, seed, "1", 1, []string{"1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := prcursor.NewStore(t.TempDir())
			repoPath := setupGitRepoWithRemote(t, azureRemote)
			name := filepath.Base(repoPath)
			if err := store.Save(prcursor.Cursor{Provider: "azuredevops", Repo: name, Since: seed, FailedPR: tc.priorFailed, Failures: tc.priorCount}); err != nil {
				t.Fatal(err)
			}
			src := failureBatchSource()
			d := azurePorts(src, store)
			d.Haiku = failingTitleRunner{failTitles: tc.failTitles}

			ingestOneRepo(context.Background(), mock.NewMemoryService(), d, testConfig(), repoPath, false)

			cur, ok := store.Load("azuredevops", name)
			if !ok {
				t.Fatal("cursor missing")
			}
			if !cur.Since.Equal(tc.wantSince) {
				t.Errorf("Since=%v want %v", cur.Since, tc.wantSince)
			}
			if cur.FailedPR != tc.wantFailedPR || cur.Failures != tc.wantFailures {
				t.Errorf("failed=%q/%d want %q/%d", cur.FailedPR, cur.Failures, tc.wantFailedPR, tc.wantFailures)
			}
			if strings.Join(src.getCalls, ",") != strings.Join(tc.wantGets, ",") {
				t.Errorf("Get calls %v want %v", src.getCalls, tc.wantGets)
			}
		})
	}
}

// A PR that legitimately yields nothing ("[]") is a skip: the cursor passes it.
func TestIngestOneRepoLegitimateSkipAdvances(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	repoPath := setupGitRepoWithRemote(t, azureRemote)
	if err := store.Save(prcursor.Cursor{Provider: "azuredevops", Repo: filepath.Base(repoPath), Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	src := failureBatchSource()
	ingestOneRepo(context.Background(), mock.NewMemoryService(), azurePorts(src, store), testConfig(), repoPath, false)
	cur, _ := store.Load("azuredevops", filepath.Base(repoPath))
	if !cur.Since.Equal(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)) || cur.FailedPR != "" {
		t.Errorf("unexpected cursor %+v", cur)
	}
}
