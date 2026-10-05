// Package azuredevops is the only prsource.Source implementation in MVP
// (AC-58): it wraps the `az` CLI (azure-devops extension), never shells out
// via a concatenated string — every invocation passes args as a slice
// (A05 / Command Injection).
package azuredevops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redfoxius/claude-memory/internal/prsource"
)

// azureDevOpsResource is the Azure AD application id of Azure DevOps. `az rest`
// cannot derive the token resource from a dev.azure.com URL: without an
// explicit --resource it warns, sends no token, and Azure DevOps answers with
// an HTML sign-in page instead of JSON.
const azureDevOpsResource = "499b84ac-1321-427f-aa17-267ca6975798"

// maxBodyPrefix caps how much of a non-JSON response body is echoed in errors.
const maxBodyPrefix = 80

// Runner is the port azuredevops needs to invoke the `az` CLI, declared
// here by the consumer so tests can fake a run without shelling out to a
// real az binary. args must already be a fully-formed argument slice
// (never built by string-concatenating repo-derived text) — the
// implementation below satisfies that by construction, passing field
// values as separate slice elements.
type Runner interface {
	Run(ctx context.Context, dir string, args []string) ([]byte, error)
}

// CLIRunner implements Runner by invoking the real `az` binary.
type CLIRunner struct{}

// Run invokes `az <args...>` with its working directory set to dir (so the
// azure-devops extension's `--detect true` can pick up org/project from
// that directory's git config), capturing stdout. args is passed straight
// to exec.Command as a slice — never through a shell, never
// string-concatenated (A05).
func (CLIRunner) Run(ctx context.Context, dir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "az", args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// stderr may contain auth/config detail but never a secret (az CLI
		// doesn't echo the PAT/token it authenticates with); still, cap
		// what we surface to the caller's error message.
		return nil, fmt.Errorf("az %v: %w (stderr: %s)", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Client implements prsource.Source against Azure DevOps via the az CLI.
type Client struct {
	runner Runner
}

// New constructs a Client. A nil runner defaults to CLIRunner{} (the real
// az binary); tests pass a fake.
func New(runner Runner) *Client {
	if runner == nil {
		runner = CLIRunner{}
	}
	return &Client{runner: runner}
}

// prItem is the subset of `az repos pr list` / `az repos pr show` JSON
// output fields this client uses.
type prItem struct {
	PullRequestID int    `json:"pullRequestId"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	ClosedDate    string `json:"closedDate"`
	Status        string `json:"status"`
	URL           string `json:"url"`
	// LastMergeCommit is the commit the PR was merged as (completed PRs).
	LastMergeCommit struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeCommit"`
}

// apiName is the repository name for Azure API calls: the one parsed from
// the origin URL, not the local clone directory's name.
func apiName(repo prsource.RepoRef) string {
	if repo.RemoteName != "" {
		return repo.RemoteName
	}
	return repo.Name
}

// ListCompleted lists PRs completed after since, scoped to repo, in
// completion order (oldest first), via `az repos pr list --status
// completed --detect true --repository <name>` run with its working
// directory set to repo.LocalPath so the azure-devops extension's git
// auto-detection resolves org/project (AC-26).
func (c *Client) ListCompleted(ctx context.Context, repo prsource.RepoRef, since time.Time) ([]prsource.PR, error) {
	args := []string{
		"repos", "pr", "list",
		"--detect", "true",
		"--repository", apiName(repo),
		"--status", "completed",
		"--output", "json",
	}

	out, err := c.runner.Run(ctx, repo.LocalPath, args)
	if err != nil {
		return nil, fmt.Errorf("az repos pr list: %w", err)
	}

	var items []prItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parse az repos pr list output: %w", err)
	}

	prs := make([]prsource.PR, 0, len(items))
	for _, it := range items {
		completedAt, perr := time.Parse(time.RFC3339, it.ClosedDate)
		if perr != nil {
			slog.Warn("azuredevops: skipping PR with unparseable closedDate",
				"repo", repo.Name, "pr_id", it.PullRequestID, "closed_date", it.ClosedDate)
			continue
		}
		if !completedAt.After(since) {
			continue
		}
		prs = append(prs, prsource.PR{
			ID:          strconv.Itoa(it.PullRequestID),
			Title:       it.Title,
			Description: it.Description,
			CompletedAt: completedAt,
			URL:         it.URL,
			MergeCommit: it.LastMergeCommit.CommitID,
			Trusted:     true, // Azure DevOps access is already limited to the org's members.
		})
	}

	// Process in completion order (AC-26).
	sort.Slice(prs, func(i, j int) bool { return prs[i].CompletedAt.Before(prs[j].CompletedAt) })

	return prs, nil
}

// Get fetches one PR's full detail via `az repos pr show --id <id>
// --detect true`, plus a best-effort fetch of its review-thread comments.
// A review-comments fetch failure is logged and degrades to an empty list
// rather than failing the whole PR (review comments enrich extraction,
// they aren't required for it).
func (c *Client) Get(ctx context.Context, repo prsource.RepoRef, id string) (*prsource.PR, error) {
	args := []string{
		"repos", "pr", "show",
		"--id", id,
		"--detect", "true",
		"--output", "json",
	}

	out, err := c.runner.Run(ctx, repo.LocalPath, args)
	if err != nil {
		return nil, fmt.Errorf("az repos pr show: %w", err)
	}

	var item prItem
	if err := json.Unmarshal(out, &item); err != nil {
		return nil, fmt.Errorf("parse az repos pr show output: %w", err)
	}

	completedAt, _ := time.Parse(time.RFC3339, item.ClosedDate)

	comments, err := c.reviewComments(ctx, repo, id)
	if err != nil {
		slog.Warn("azuredevops: fetching review comments failed; continuing without them",
			"repo", repo.Name, "pr_id", id, "error", err)
		comments = nil
	}

	return &prsource.PR{
		ID:             id,
		Title:          item.Title,
		Description:    item.Description,
		ReviewComments: comments,
		CompletedAt:    completedAt,
		URL:            item.URL,
		MergeCommit:    item.LastMergeCommit.CommitID,
		Trusted:        true,
	}, nil
}

// threadsResponse is the subset of the Git Pull Request Threads REST API
// response this client reads.
type threadsResponse struct {
	Value []struct {
		IsDeleted bool `json:"isDeleted"`
		Comments  []struct {
			CommentType string `json:"commentType"`
			Content     string `json:"content"`
		} `json:"comments"`
	} `json:"value"`
}

// reviewComments fetches PR review-thread comment text via `az rest`
// against the Git Pull Request Threads API. It requires repo.Org (parsed
// by prsource.Detect from the origin remote); if Org is unknown, it
// returns an empty list rather than guessing an API URL.
func (c *Client) reviewComments(ctx context.Context, repo prsource.RepoRef, id string) ([]string, error) {
	if repo.Org == "" || repo.Project == "" {
		return nil, nil
	}

	url := fmt.Sprintf(
		"https://dev.azure.com/%s/%s/_apis/git/repositories/%s/pullRequests/%s/threads?api-version=7.1",
		repo.Org, repo.Project, apiName(repo), id,
	)
	args := []string{"rest", "--method", "get", "--url", url, "--resource", azureDevOpsResource, "--output", "json"}

	out, err := c.runner.Run(ctx, repo.LocalPath, args)
	if err != nil {
		return nil, fmt.Errorf("az rest (pr threads): %w", err)
	}

	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 || trimmed[0] == '<' {
		prefix := string(trimmed)
		if len(prefix) > maxBodyPrefix {
			prefix = prefix[:maxBodyPrefix]
		}
		return nil, fmt.Errorf("az rest (pr threads) returned a non-JSON response (likely an HTML sign-in page: az did not obtain a token); "+
			"check with `az account get-access-token --resource %s` (body prefix: %q)", azureDevOpsResource, strings.TrimSpace(prefix))
	}

	var resp threadsResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse pr threads response: %w", err)
	}

	var comments []string
	for _, thread := range resp.Value {
		if thread.IsDeleted {
			continue
		}
		for _, c := range thread.Comments {
			if c.CommentType != "" && c.CommentType != "text" {
				continue // skip system-generated comments (e.g. "policy update").
			}
			if c.Content != "" {
				comments = append(comments, c.Content)
			}
		}
	}
	return comments, nil
}
