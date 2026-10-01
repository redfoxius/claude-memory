// Package prsource declares the provider-neutral port ingest-pr needs
// against a PR-hosting platform (Azure DevOps, GitHub, GitLab), and the
// origin-URL detection that picks which provider a given local repo uses.
// It has zero concrete dependencies: the only implementation in MVP is
// internal/azuredevops, wired up by cmd/claude-memory/ingestpr.go.
package prsource

import (
	"context"
	"time"
)

// Provider identifies which hosting platform a repo's PRs come from.
type Provider string

const (
	ProviderAzureDevOps Provider = "azuredevops"
	ProviderGitHub      Provider = "github"
	ProviderGitLab      Provider = "gitlab"
	ProviderUnknown     Provider = "unknown"
)

// RepoRef identifies one repo scoped to a single provider. Org/Project are
// best-effort, parsed from the origin remote URL by Detect (empty if the
// form doesn't carry them, e.g. a two-segment GitHub/GitLab path where Org
// holds the owner). LocalPath and Name are filled in by the caller
// (cmd/claude-memory/ingestpr.go) once it knows which local directory this
// remote came from — Name is the canonical identifier used for the cursor
// file and the memory record's repo field.
type RepoRef struct {
	Provider  Provider
	Org       string
	Project   string
	Name      string
	LocalPath string
	Remote    string
}

// PR is a provider-neutral view of one pull request, with just enough
// detail for extraction to turn it into memory records (AC-26).
type PR struct {
	ID             string
	Title          string
	Description    string
	DiffSummary    string
	ReviewComments []string
	CompletedAt    time.Time
	URL            string
}

// Source is the port ingest-pr needs against a PR-hosting platform:
// list PRs completed since a cursor time (in completion order is NOT
// guaranteed by the port itself — callers that need ordering, per AC-26,
// sort the result), and fetch one PR's full detail including review
// comments. Declared here, by the consumer, sized to exactly what
// ingest-pr calls; satisfied in MVP only by internal/azuredevops.Client
// (AC-58 — GitHub/GitLab adapters are backlog).
type Source interface {
	ListCompleted(ctx context.Context, repo RepoRef, since time.Time) ([]PR, error)
	Get(ctx context.Context, repo RepoRef, id string) (*PR, error)
}
