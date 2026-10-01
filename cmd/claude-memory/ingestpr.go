package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/extraction"
	"claude-memory/internal/memory"
	"claude-memory/internal/prcursor"
	"claude-memory/internal/prsource"
)

// runIngestPR implements the "ingest-pr" subcommand's work (AC-26, AC-27,
// AC-28, AC-58): for each configured local repo, detect its PR provider
// from its git origin remote, list PRs completed since that repo's
// persisted cursor (or a lookback window on first run), process them
// through extraction in completion order, and persist the cursor only
// after the whole batch succeeds. One repo's failure never blocks the
// others. svc, cursorStore, and client are already-constructed ports —
// main.go's cmdIngestPR is the composition root that builds them (a
// *memory.Service via buildService when dryRun is false, a
// *prcursor.Store, and an *azuredevops.Client).
func runIngestPR(
	ctx context.Context,
	cfg *config.Config,
	svc extraction.StoreWriter,
	cursorStore *prcursor.Store,
	client prsource.Source,
	dryRun bool,
) error {
	repos := discoverRepos(cfg.PRIngestRepos)
	if len(repos) == 0 {
		slog.Warn("ingest-pr: no repos configured or discovered (MEMORY_PR_INGEST_REPOS)")
		return nil
	}

	for _, repoPath := range repos {
		// nil runner -> extraction.ProcessPR constructs the real `claude -p`
		// haiku runner itself; tests inject a fake runner instead.
		ingestOneRepo(ctx, svc, client, cursorStore, cfg, repoPath, dryRun, nil)
	}

	return nil
}

// ingestOneRepo ingests PRs for a single local repo directory. It never
// returns an error: every failure mode is logged and causes this repo to
// be skipped, so one repo's trouble never blocks the others in the same
// run (AC-27).
func ingestOneRepo(
	ctx context.Context,
	svc extraction.StoreWriter,
	client prsource.Source,
	cursorStore *prcursor.Store,
	cfg *config.Config,
	repoPath string,
	dryRun bool,
	haikuRunner extraction.HaikuRunner,
) {
	remote, err := gitRemoteURL(ctx, repoPath)
	if err != nil {
		slog.Warn("ingest-pr: could not determine git origin remote; skipping repo", "repo", repoPath, "error", err)
		return
	}

	provider, ref, err := prsource.Detect(remote)
	if err != nil {
		slog.Warn("ingest-pr: provider detection failed; skipping repo", "repo", repoPath, "error", err)
		return
	}
	ref.LocalPath = repoPath
	ref.Name = filepath.Base(repoPath)

	// Each repo is ingested into the namespace its path maps to.
	if sc, ok := svc.(interface{ WithNamespace(string) *memory.Service }); ok {
		svc = sc.WithNamespace(resolveNamespace(repoPath))
	}

	if provider != prsource.ProviderAzureDevOps {
		// AC-58: unsupported provider (GitHub, GitLab, or unknown) is
		// skipped with a warning; its cursor is left untouched, and the
		// rest of this run's repos still proceed.
		slog.Warn("ingest-pr: provider not supported, skipping repo",
			"repo", ref.Name, "provider", provider, "remote", remote)
		return
	}

	cur, hasCursor := cursorStore.Load(string(provider), ref.Name)
	since := time.Now().Add(-cfg.PRIngestLookback) // AC-28: first-run lookback default.
	if hasCursor {
		since = cur.Since
	}

	prs, err := client.ListCompleted(ctx, ref, since)
	if err != nil {
		// AC-27: auth/API failure for this repo never advances its cursor
		// and never blocks the other configured repos.
		slog.Warn("ingest-pr: listing PRs failed; cursor left unchanged", "repo", ref.Name, "error", err)
		return
	}

	if dryRun {
		fmt.Printf("%s (%s): %d PR(s) would be ingested since %s\n", ref.Name, provider, len(prs), since.Format(time.RFC3339))
		return
	}

	latest := since
	for _, pr := range prs {
		full, err := client.Get(ctx, ref, pr.ID)
		if err != nil {
			// AC-26: abort this repo's batch without persisting a cursor,
			// so an interrupted run leaves the previous cursor intact and
			// retries the same PRs next time.
			slog.Warn("ingest-pr: fetching PR detail failed; aborting this repo's batch, cursor left unchanged",
				"repo", ref.Name, "pr_id", pr.ID, "error", err)
			return
		}

		input := extraction.PRInput{
			Title:       full.Title,
			Description: full.Description,
			Repo:        ref.Name,
			URL:         full.URL,
		}
		extractionCfg := extraction.Config{
			MinMessages:  cfg.ExtractMinMessages,
			CharBudget:   cfg.MaxContentChars,
			HaikuTimeout: defaultHaikuTimeout,
		}

		if _, err := extraction.ProcessPR(ctx, svc, input, extractionCfg, haikuRunner); err != nil {
			slog.Warn("ingest-pr: processing PR failed; continuing with the rest of this repo's batch",
				"repo", ref.Name, "pr_id", pr.ID, "error", err)
		}

		if pr.CompletedAt.After(latest) {
			latest = pr.CompletedAt
		}
	}

	// The whole batch succeeded (or there was nothing to process): persist
	// the cursor now, never mid-batch (AC-26).
	if err := cursorStore.Save(prcursor.Cursor{Provider: string(provider), Repo: ref.Name, Since: latest}); err != nil {
		slog.Error("ingest-pr: failed to persist cursor", "repo", ref.Name, "error", err)
	}
}

// discoverRepos resolves the configured repo roots into concrete git repo
// directories: a root that is itself a git repo is used as-is; a root that
// isn't is scanned one level deep for subdirectories that are git repos.
func discoverRepos(roots []string) []string {
	var repos []string
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if isGitRepo(root) {
			repos = append(repos, root)
			continue
		}

		entries, err := os.ReadDir(root)
		if err != nil {
			slog.Warn("ingest-pr: cannot read configured repo root", "dir", root, "error", err)
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sub := filepath.Join(root, e.Name())
			if isGitRepo(sub) {
				repos = append(repos, sub)
			}
		}
	}
	return repos
}

// isGitRepo reports whether dir looks like a git working tree: it has a
// .git entry, either a directory (a normal clone) or a file (a worktree/
// submodule gitlink).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitRemoteURL runs `git remote get-url origin` with its working directory
// set to repoPath.
func gitRemoteURL(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
