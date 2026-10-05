// Package gitlab implements prsource.Source for GitLab through the `glab`
// CLI (`glab api`), reusing the CLI's own login: no token is read, passed or
// logged here.
//
// NOT VERIFIED AGAINST A LIVE GITLAB: the owner has no GitLab account, so
// this adapter is covered only by fixtures written from the GitLab REST API
// documentation. Start any first real use with `ingest-pr --dry-run`.
//
// gitlab.com is reached by auto-detection; a self-hosted host only through an
// explicit namespace `pr_ingest.provider: gitlab`, and then `glab auth
// status` must succeed for that host before the first API call. Only
// Developer-or-higher project members' MRs and notes are used (prompt
// injection from strangers).
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/redfoxius/claude-memory/internal/prsource"
)

const (
	perPage        = 100
	maxPages       = 10
	minAccessLevel = 30 // Developer
	defaultHost    = "gitlab.com"
)

// Runner is the port gitlab needs to invoke the `glab` CLI, declared here by
// the consumer; the argv is always a slice, never a shell string.
type Runner interface {
	Run(ctx context.Context, dir string, args []string) ([]byte, error)
}

// Client implements prsource.Source against GitLab.
type Client struct {
	runner  Runner
	authed  map[string]bool         // hosts whose `auth status` passed this run
	members map[string]map[int]bool // project key -> trusted user ids
}

var _ prsource.Source = (*Client)(nil)

// New constructs a Client around r.
func New(r Runner) *Client {
	return &Client{runner: r, authed: map[string]bool{}, members: map[string]map[int]bool{}}
}

type author struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

type mergeRequest struct {
	IID            int        `json:"iid"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	WebURL         string     `json:"web_url"`
	MergedAt       *time.Time `json:"merged_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	SquashSHA      string     `json:"squash_commit_sha"`
	SHA            string     `json:"sha"`
	Author         author     `json:"author"`
}

type note struct {
	Body   string `json:"body"`
	System bool   `json:"system"`
	Author author `json:"author"`
}

type member struct {
	ID          int `json:"id"`
	AccessLevel int `json:"access_level"`
}

// check validates what reaches glab (path allowlist, host) and, for a host
// other than gitlab.com, the one-time `auth status` gate.
func (c *Client) check(ctx context.Context, repo prsource.RepoRef) error {
	if err := prsource.ValidAPIPath(prsource.ProviderGitLab, repo.Path); err != nil {
		return err
	}
	if err := prsource.ValidHost(repo.Host); err != nil {
		return err
	}
	if repo.Host == defaultHost || c.authed[repo.Host] {
		return nil
	}
	if _, err := c.runner.Run(ctx, repo.LocalPath, []string{"auth", "status", "--hostname=" + repo.Host}); err != nil {
		return fmt.Errorf("glab is not logged in to %s: %w", repo.Host, err)
	}
	c.authed[repo.Host] = true
	return nil
}

// api runs `glab api --hostname=<Host> <endpoint>?<query>`.
func (c *Client) api(ctx context.Context, repo prsource.RepoRef, suffix string, q url.Values) ([]byte, error) {
	endpoint := "projects/" + url.PathEscape(repo.Path) + suffix
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	out, err := c.runner.Run(ctx, repo.LocalPath, []string{"api", "--hostname=" + repo.Host, endpoint})
	if err != nil {
		return nil, fmt.Errorf("glab api %s: %w", endpoint, err)
	}
	return out, nil
}

func pageQuery(page int, extra ...string) url.Values {
	q := url.Values{"per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q
}

// trustedMembers returns the ids of project members with at least Developer
// access, cached per project for the run. A failure is an error (fail closed).
func (c *Client) trustedMembers(ctx context.Context, repo prsource.RepoRef) (map[int]bool, error) {
	key := repo.Host + "/" + repo.Path
	if m, ok := c.members[key]; ok {
		return m, nil
	}
	ids := map[int]bool{}
	for page := 1; ; page++ {
		if page > maxPages {
			// Not ErrPageCap: the cursor remedy does not apply here.
			return nil, fmt.Errorf("project %s has more than %d members; the trusted-author check cannot complete, so it is skipped", repo.Path, maxPages*perPage)
		}
		out, err := c.api(ctx, repo, "/members/all", pageQuery(page))
		if err != nil {
			return nil, err
		}
		var ms []member
		if err := json.Unmarshal(out, &ms); err != nil {
			return nil, fmt.Errorf("parse members of %s: %w", repo.Path, err)
		}
		for _, m := range ms {
			if m.AccessLevel >= minAccessLevel {
				ids[m.ID] = true
			}
		}
		if len(ms) < perPage {
			break
		}
	}
	c.members[key] = ids
	return ids, nil
}

func (c *Client) toPR(m mergeRequest, trusted map[int]bool) prsource.PR {
	commit := m.MergeCommitSHA
	if commit == "" {
		commit = m.SquashSHA
	}
	if commit == "" {
		commit = m.SHA // fast-forward merge: the MR head is on the base branch
	}
	pr := prsource.PR{
		ID:          strconv.Itoa(m.IID),
		Title:       m.Title,
		Description: m.Description,
		URL:         m.WebURL,
		MergeCommit: commit,
		Bot:         prsource.IsGitLabBot(m.Author.Username),
		Trusted:     trusted[m.Author.ID],
	}
	if m.MergedAt != nil {
		pr.CompletedAt = *m.MergedAt
	}
	return pr
}

// ListCompleted lists merged MRs with merged_at after since, oldest first.
// It first reads the project's trusted members (failure fails the call), then
// pages merged MRs by updated_at descending until a short page; reaching the
// page cap is an error wrapping prsource.ErrPageCap.
func (c *Client) ListCompleted(ctx context.Context, repo prsource.RepoRef, since time.Time) ([]prsource.PR, error) {
	if err := c.check(ctx, repo); err != nil {
		return nil, err
	}
	trusted, err := c.trustedMembers(ctx, repo)
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	var prs []prsource.PR
	for page := 1; page <= maxPages; page++ {
		out, err := c.api(ctx, repo, "/merge_requests", pageQuery(page,
			"state", "merged", "updated_after", since.UTC().Format(time.RFC3339),
			"order_by", "updated_at", "sort", "desc"))
		if err != nil {
			return nil, err
		}
		var items []mergeRequest
		if err := json.Unmarshal(out, &items); err != nil {
			return nil, fmt.Errorf("parse merge requests page %d of %s: %w", page, repo.Path, err)
		}
		for _, it := range items {
			if it.MergedAt == nil || !it.MergedAt.After(since) || seen[it.IID] {
				continue
			}
			seen[it.IID] = true
			prs = append(prs, c.toPR(it, trusted))
		}
		if len(items) < perPage {
			sort.Slice(prs, func(i, j int) bool { return prs[i].CompletedAt.Before(prs[j].CompletedAt) })
			return prs, nil
		}
	}
	return nil, fmt.Errorf("merge requests of %s: %w", repo.Path, prsource.ErrPageCap)
}

// Get fetches one MR's detail (failure fails the call) and the first page of
// its notes from trusted, non-bot authors, system notes excluded (a notes
// failure degrades to no comments).
func (c *Client) Get(ctx context.Context, repo prsource.RepoRef, id string) (*prsource.PR, error) {
	if err := c.check(ctx, repo); err != nil {
		return nil, err
	}
	if _, err := strconv.Atoi(id); err != nil {
		return nil, fmt.Errorf("invalid MR id %q", id)
	}
	trusted, err := c.trustedMembers(ctx, repo)
	if err != nil {
		return nil, err
	}
	out, err := c.api(ctx, repo, "/merge_requests/"+id, nil)
	if err != nil {
		return nil, err
	}
	var m mergeRequest
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parse merge request %s of %s: %w", id, repo.Path, err)
	}
	pr := c.toPR(m, trusted)

	out, err = c.api(ctx, repo, "/merge_requests/"+id+"/notes", url.Values{"sort": {"asc"}, "per_page": {strconv.Itoa(perPage)}})
	var notes []note
	if err == nil {
		err = json.Unmarshal(out, &notes)
	}
	if err != nil {
		slog.Warn("gitlab: fetching notes failed; continuing without them", "repo", repo.Path, "mr", id, "error", err)
		return &pr, nil
	}
	for _, n := range notes {
		if n.System || n.Body == "" || !trusted[n.Author.ID] || prsource.IsGitLabBot(n.Author.Username) {
			continue
		}
		pr.ReviewComments = append(pr.ReviewComments, n.Body)
	}
	return &pr, nil
}
