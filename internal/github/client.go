// Package github implements prsource.Source for github.com through the `gh`
// CLI (`gh api`), reusing the CLI's own login: no token is read, passed or
// logged here. GitHub Enterprise is out of scope — the host is always
// github.com. Only trusted authors' PRs and comments (OWNER, MEMBER,
// COLLABORATOR; no bots) are used, since PR text from strangers would
// otherwise become memory records (prompt injection).
//
// The tests use hand-written fixtures (not captured from a live repo); live
// verification against GitHub is the owner's manual step (`ingest-pr
// --dry-run`, then one real run).
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"claude-memory/internal/prsource"
)

const (
	perPage  = 100
	maxPages = 10
)

// Runner is the port github needs to invoke the `gh` CLI, declared here by
// the consumer; the argv is always a slice, never a shell string.
type Runner interface {
	Run(ctx context.Context, dir string, args []string) ([]byte, error)
}

// Client implements prsource.Source against github.com.
type Client struct {
	runner Runner
}

var _ prsource.Source = (*Client)(nil)

// New constructs a Client around r.
func New(r Runner) *Client { return &Client{runner: r} }

type user struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type pull struct {
	Number            int        `json:"number"`
	Title             string     `json:"title"`
	Body              string     `json:"body"`
	HTMLURL           string     `json:"html_url"`
	MergedAt          *time.Time `json:"merged_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	MergeCommitSHA    string     `json:"merge_commit_sha"`
	User              user       `json:"user"`
	AuthorAssociation string     `json:"author_association"`
}

type comment struct {
	Body              string `json:"body"`
	User              user   `json:"user"`
	AuthorAssociation string `json:"author_association"`
}

func (p pull) toPR() prsource.PR {
	out := prsource.PR{
		ID:          strconv.Itoa(p.Number),
		Title:       p.Title,
		Description: p.Body,
		URL:         p.HTMLURL,
		MergeCommit: p.MergeCommitSHA,
		Bot:         prsource.IsGitHubBot(p.User.Login, p.User.Type),
		Trusted:     prsource.GitHubTrusted(p.AuthorAssociation),
	}
	if p.MergedAt != nil {
		out.CompletedAt = *p.MergedAt
	}
	return out
}

// api runs `gh api --hostname github.com -X GET <endpoint> -f k=v...`.
// fields are "k=v" strings passed as separate argv elements.
func (c *Client) api(ctx context.Context, dir, endpoint string, fields ...string) ([]byte, error) {
	args := []string{"api", "--hostname", "github.com", "-X", "GET", endpoint}
	for _, f := range fields {
		args = append(args, "-f", f)
	}
	out, err := c.runner.Run(ctx, dir, args)
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %w", endpoint, err)
	}
	return out, nil
}

// ListCompleted lists merged PRs with merged_at after since, oldest first.
// It pages closed PRs by updated_at descending and stops at the first page
// that is short or holds an item updated at or before since (a PR merged
// after the cursor was updated after it). Reaching the page cap first is an
// error wrapping prsource.ErrPageCap.
func (c *Client) ListCompleted(ctx context.Context, repo prsource.RepoRef, since time.Time) ([]prsource.PR, error) {
	if err := prsource.ValidAPIPath(prsource.ProviderGitHub, repo.Path); err != nil {
		return nil, err
	}
	endpoint := "repos/" + repo.Path + "/pulls"
	seen := map[int]bool{}
	var prs []prsource.PR
	for page := 1; page <= maxPages; page++ {
		out, err := c.api(ctx, repo.LocalPath, endpoint,
			"state=closed", "sort=updated", "direction=desc",
			"per_page="+strconv.Itoa(perPage), "page="+strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		var items []pull
		if err := json.Unmarshal(out, &items); err != nil {
			return nil, fmt.Errorf("parse %s page %d: %w", endpoint, page, err)
		}
		reached := false
		for _, it := range items {
			if !it.UpdatedAt.After(since) {
				reached = true
			}
			if it.MergedAt == nil || !it.MergedAt.After(since) || seen[it.Number] {
				continue
			}
			seen[it.Number] = true
			prs = append(prs, it.toPR())
		}
		if reached || len(items) < perPage {
			sort.Slice(prs, func(i, j int) bool { return prs[i].CompletedAt.Before(prs[j].CompletedAt) })
			return prs, nil
		}
	}
	return nil, fmt.Errorf("%s: %w", endpoint, prsource.ErrPageCap)
}

// Get fetches one PR's detail (failure fails the call) plus its issue and
// inline review comments from trusted, non-bot authors (failure of either
// degrades to no comments).
func (c *Client) Get(ctx context.Context, repo prsource.RepoRef, id string) (*prsource.PR, error) {
	if err := prsource.ValidAPIPath(prsource.ProviderGitHub, repo.Path); err != nil {
		return nil, err
	}
	if _, err := strconv.Atoi(id); err != nil {
		return nil, fmt.Errorf("invalid PR id %q", id)
	}
	base := "repos/" + repo.Path
	out, err := c.api(ctx, repo.LocalPath, base+"/pulls/"+id)
	if err != nil {
		return nil, err
	}
	var p pull
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("parse %s/pulls/%s: %w", base, id, err)
	}
	pr := p.toPR()

	for _, ep := range []string{base + "/issues/" + id + "/comments", base + "/pulls/" + id + "/comments"} {
		out, err := c.api(ctx, repo.LocalPath, ep, "per_page="+strconv.Itoa(perPage))
		if err != nil {
			slog.Warn("github: fetching comments failed; continuing without them", "repo", repo.Path, "pr", id, "error", err)
			continue
		}
		var cs []comment
		if err := json.Unmarshal(out, &cs); err != nil {
			slog.Warn("github: parsing comments failed; continuing without them", "repo", repo.Path, "pr", id, "error", err)
			continue
		}
		for _, cm := range cs {
			if cm.Body == "" || !prsource.GitHubTrusted(cm.AuthorAssociation) || prsource.IsGitHubBot(cm.User.Login, cm.User.Type) {
				continue
			}
			pr.ReviewComments = append(pr.ReviewComments, cm.Body)
		}
	}
	return &pr, nil
}
