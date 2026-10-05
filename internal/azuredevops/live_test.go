//go:build live

package azuredevops

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/redfoxius/claude-memory/internal/prsource"
)

// TestLiveReviewComments is opt-in (go test -tags live): it needs a logged-in
// az and AZURE_LIVE_REPO_DIR (a local clone) + AZURE_LIVE_PR_ID. Read-only;
// logs only the number of comments, never their text.
func TestLiveReviewComments(t *testing.T) {
	dir, id := os.Getenv("AZURE_LIVE_REPO_DIR"), os.Getenv("AZURE_LIVE_PR_ID")
	if dir == "" || id == "" {
		t.Skip("AZURE_LIVE_REPO_DIR and AZURE_LIVE_PR_ID not set")
	}
	remote, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatalf("git remote: %v", err)
	}
	_, repo, err := prsource.Detect(string(remote))
	if err != nil {
		t.Fatalf("detect repo: %v", err)
	}
	repo.LocalPath = dir
	if repo.Org == "" || repo.Project == "" {
		t.Fatalf("origin is not an Azure DevOps remote")
	}
	c := New(nil)
	if _, err := c.reviewComments(context.Background(), repo, id); err != nil {
		t.Fatalf("reviewComments: %v", err)
	}
	pr, err := c.Get(context.Background(), repo, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Logf("PR %s: %d review comments", id, len(pr.ReviewComments))
}
