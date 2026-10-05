package main

import (
	"context"
	"errors"
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
	"claude-memory/internal/namespace"
	"claude-memory/internal/prcursor"
	"claude-memory/internal/prsource"
)

// scopeFunc returns the writer scoped to a namespace. The composition root
// supplies it (it knows the concrete service); nil means "use svc
// unchanged" (dry runs, tests).
type scopeFunc func(namespace string) extraction.StoreWriter

// ingestPorts are the already-constructed ports ingest-pr works against;
// main.go's cmdIngestPR is the composition root that builds them.
type ingestPorts struct {
	// Sources maps a provider to its PR source (azuredevops, github, gitlab).
	Sources map[prsource.Provider]prsource.Source
	Cursors *prcursor.Store
	// Settings resolves a repo's namespace once and returns that
	// namespace's pr_ingest section (problem != "" means it is unusable).
	// nil means no namespaces: namespace "", no section.
	Settings func(repoPath string) (ns string, p namespace.PRIngest, problem string)
	Scope    scopeFunc
	Scrubber memory.Scrubber
	// Haiku is nil in production: extraction.ProcessPR then builds the real
	// `claude -p` runner itself; tests inject a fake.
	Haiku extraction.HaikuRunner
}

// runIngestPR implements the "ingest-pr" subcommand's work (AC-26, AC-27,
// AC-28, AC-58): for each configured local repo, detect its PR provider
// from its git origin remote, list PRs completed since that repo's
// persisted cursor (or a lookback window on first run), process them
// through extraction in completion order, and persist the cursor only
// after the whole batch succeeds. One repo's failure never blocks the
// others.
func runIngestPR(ctx context.Context, cfg *config.Config, svc extraction.StoreWriter, d ingestPorts, dryRun bool) error {
	repos := discoverRepos(cfg.PRIngestRepos)
	if len(repos) == 0 {
		slog.Warn("ingest-pr: no repos configured or discovered (MEMORY_PR_INGEST_REPOS)")
		return nil
	}
	for _, repoPath := range repos {
		ingestOneRepo(ctx, svc, d, cfg, repoPath, dryRun)
	}
	return nil
}

// ingestOneRepo ingests PRs for a single local repo directory. It never
// returns an error: every failure mode is logged and causes this repo to
// be skipped, so one repo's trouble never blocks the others in the same
// run (AC-27).
func ingestOneRepo(ctx context.Context, svc extraction.StoreWriter, d ingestPorts, cfg *config.Config, repoPath string, dryRun bool) {
	name := filepath.Base(repoPath)
	// skip logs why a repo is not ingested and, in a dry run, prints it
	// (AC-28). Cursors are untouched on every skip.
	skip := func(reason string, args ...any) {
		slog.Warn("ingest-pr: skipping repo ("+reason+")", append([]any{"repo", name}, args...)...)
		if dryRun {
			fmt.Printf("%s: skipped (%s)\n", name, reason)
		}
	}

	// The namespace is resolved once and used for the pr_ingest lookup and
	// the write scope (AC-10).
	ns, settings, problem := "", namespace.PRIngest{}, ""
	if d.Settings != nil {
		ns, settings, problem = d.Settings(repoPath)
	}
	if problem != "" {
		skip("pr_ingest problem", "namespace", ns, "reason", problem)
		return
	}
	if !settings.IsEnabled() {
		slog.Info("ingest-pr: namespace opted out via pr_ingest.enabled=false; skipping repo", "repo", name, "namespace", ns)
		if dryRun {
			fmt.Printf("%s: skipped (disabled)\n", name)
		}
		return
	}

	remote, err := gitRemoteURL(ctx, repoPath)
	if err != nil {
		skip("no origin remote", "error", err)
		return
	}
	safeRemote := prsource.RedactRemote(remote) // AC-33: never log userinfo

	provider, ref, err := prsource.Detect(remote)
	if err != nil {
		skip("provider detection failed", "error", err)
		return
	}
	ref.LocalPath = repoPath
	ref.Name = name

	// AC-3: the namespace's provider replaces the detected one (the only
	// way to reach a self-hosted GitLab); Host/Path come from the origin.
	if settings.Provider != "" {
		host, path, ok := prsource.ParseRemote(remote)
		if !ok || path == "" {
			skip("invalid path", "remote", safeRemote)
			return
		}
		provider = prsource.Provider(settings.Provider)
		ref.Provider, ref.Host, ref.Path, ref.Remote = provider, host, path, safeRemote
	}

	source, ok := d.Sources[provider]
	if !ok {
		// AC-58: an unsupported or unknown provider is skipped, cursor untouched.
		skip("unsupported provider", "provider", provider, "remote", safeRemote)
		return
	}

	// AC-32: nothing derived from the remote reaches gh/glab unvalidated.
	cursorRepo := ref.Name
	if provider != prsource.ProviderAzureDevOps {
		if err := prsource.ValidAPIPath(provider, ref.Path); err != nil {
			skip("invalid path", "remote", safeRemote, "error", err)
			return
		}
		if provider == prsource.ProviderGitLab {
			if err := prsource.ValidHost(ref.Host); err != nil {
				skip("invalid path", "remote", safeRemote, "error", err)
				return
			}
		}
		// AC-4: two clones with one basename must not share a cursor.
		cursorRepo = strings.ReplaceAll(ref.Path, "/", "_")
	}

	if d.Scope != nil {
		svc = d.Scope(ns)
	}

	cur, hasCursor := d.Cursors.Load(string(provider), cursorRepo)
	since := time.Now().Add(-cfg.PRIngestLookback) // AC-28: first-run lookback default.
	if hasCursor {
		since = cur.Since
	}

	prs, err := source.ListCompleted(ctx, ref, since)
	if err != nil {
		// AC-27: auth/API failure for this repo never advances its cursor
		// and never blocks the other configured repos.
		if errors.Is(err, prsource.ErrPageCap) {
			err = fmt.Errorf("%w; move \"since\" forward in %s or shorten MEMORY_PR_INGEST_LOOKBACK",
				err, d.Cursors.File(string(provider), cursorRepo))
		}
		skip("listing PRs failed; cursor left unchanged", "error", err)
		return
	}

	if dryRun {
		fmt.Printf("%s (%s): %d PR(s) would be ingested since %s\n", ref.Name, provider, len(prs), since.Format(time.RFC3339))
		return
	}

	latest := since
	skipped := 0
	for _, pr := range prs {
		// AC-21/AC-34: bot and untrusted PRs are never fetched or extracted,
		// but the cursor passes them.
		if pr.Bot || !pr.Trusted {
			skipped++
			if pr.CompletedAt.After(latest) {
				latest = pr.CompletedAt
			}
			continue
		}
		full, err := source.Get(ctx, ref, pr.ID)
		if err != nil {
			// AC-26: abort this repo's batch without persisting a cursor,
			// so an interrupted run leaves the previous cursor intact and
			// retries the same PRs next time.
			slog.Warn("ingest-pr: fetching PR detail failed; aborting this repo's batch, cursor left unchanged",
				"repo", ref.Name, "pr_id", pr.ID, "error", err)
			return
		}

		if full.Bot || !full.Trusted {
			skipped++
		} else {
			input := extraction.PRInput{
				Title:          full.Title,
				Description:    full.Description,
				Repo:           ref.Name,
				URL:            full.URL,
				CommitSHA:      full.MergeCommit,
				ReviewComments: full.ReviewComments,
			}
			extractionCfg := extraction.Config{
				MinMessages:  cfg.ExtractMinMessages,
				CharBudget:   cfg.MaxContentChars,
				HaikuTimeout: defaultHaikuTimeout,
			}
			if _, err := extraction.ProcessPR(ctx, svc, input, extractionCfg, d.Haiku, d.Scrubber); err != nil {
				slog.Warn("ingest-pr: processing PR failed; continuing with the rest of this repo's batch",
					"repo", ref.Name, "pr_id", pr.ID, "error", err)
			}
		}

		if pr.CompletedAt.After(latest) {
			latest = pr.CompletedAt
		}
	}
	if skipped > 0 {
		slog.Info("ingest-pr: skipped bot or untrusted-author PRs", "repo", ref.Name, "count", skipped)
	}

	// The whole batch succeeded (or there was nothing to process): persist
	// the cursor now, never mid-batch (AC-26).
	if err := d.Cursors.Save(prcursor.Cursor{Provider: string(provider), Repo: cursorRepo, Since: latest}); err != nil {
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
