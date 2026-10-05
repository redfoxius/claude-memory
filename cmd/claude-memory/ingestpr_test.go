package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/extraction"
	"github.com/redfoxius/claude-memory/internal/memory/mock"
	"github.com/redfoxius/claude-memory/internal/namespace"
	"github.com/redfoxius/claude-memory/internal/prcursor"
	"github.com/redfoxius/claude-memory/internal/prsource"
)

// emptyHaikuRunner stands in for extraction's real `claude -p` subprocess
// runner in tests: it always returns an empty extraction result, keeping
// these tests fast and free of any real CLI invocation.
type emptyHaikuRunner struct{}

func (emptyHaikuRunner) Run(ctx context.Context, prompt string) ([]byte, error) {
	return []byte(`[]`), nil
}

// fakeSource is a scripted prsource.Source for testing ingestOneRepo
// without a real `az` CLI.
type fakeSource struct {
	listResult []prsource.PR
	listErr    error
	getResults map[string]*prsource.PR
	getErr     error
	listCalls  int
	getCalls   []string
	refs       []prsource.RepoRef
}

func (f *fakeSource) ListCompleted(ctx context.Context, repo prsource.RepoRef, since time.Time) ([]prsource.PR, error) {
	f.listCalls++
	f.refs = append(f.refs, repo)
	return f.listResult, f.listErr
}

func (f *fakeSource) Get(ctx context.Context, repo prsource.RepoRef, id string) (*prsource.PR, error) {
	f.getCalls = append(f.getCalls, id)
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResults[id], nil
}

// setupBareGitDir creates a directory with a bare ".git" entry — enough
// for discoverRepos' isGitRepo check, which never shells out to git.
func setupBareGitDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatalf("failed to create .git dir: %v", err)
	}
}

// setupGitRepoWithRemote creates a real git repository (via `git init` +
// `git remote add origin`) so that ingestOneRepo's own `git remote get-url
// origin` call succeeds and prsource.Detect sees a real remote URL.
func setupGitRepoWithRemote(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", remote)
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// azurePorts wires one fake source as the Azure DevOps provider.
func azurePorts(src prsource.Source, store *prcursor.Store) ingestPorts {
	return ingestPorts{
		Sources: map[prsource.Provider]prsource.Source{prsource.ProviderAzureDevOps: src},
		Cursors: store,
		Haiku:   emptyHaikuRunner{},
	}
}

func TestDiscoverReposDirectRepo(t *testing.T) {
	dir := t.TempDir()
	setupBareGitDir(t, dir)

	got := discoverRepos([]string{dir})
	if len(got) != 1 || got[0] != dir {
		t.Errorf("expected [%s], got %v", dir, got)
	}
}

func TestDiscoverReposScansOneLevelDeep(t *testing.T) {
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	notARepo := filepath.Join(root, "not-a-repo")
	for _, d := range []string{repoA, repoB, notARepo} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
	}
	setupBareGitDir(t, repoA)
	setupBareGitDir(t, repoB)

	got := discoverRepos([]string{root})
	if len(got) != 2 {
		t.Fatalf("expected 2 discovered repos, got %d: %v", len(got), got)
	}
}

func TestDiscoverReposSkipsUnreadableRoot(t *testing.T) {
	got := discoverRepos([]string{"/nonexistent/path/for/testing"})
	if len(got) != 0 {
		t.Errorf("expected no repos from an unreadable root, got %v", got)
	}
}

// --- ingestOneRepo: AC-26 (cursor only after a full successful batch),
// AC-27 (one repo's list failure never advances its cursor), AC-28
// (missing cursor falls back to the lookback window), AC-58 (unsupported
// provider is skipped without touching list/get or the cursor).

const azureRemote = "git@ssh.dev.azure.com:v3/acme/Marketplace/test-repo"

func TestIngestOneRepoFirstRunUsesLookbackAndPersistsCursor(t *testing.T) {
	cursorStore := prcursor.NewStore(t.TempDir())

	completedAt := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		listResult: []prsource.PR{{ID: "1", Title: "t", CompletedAt: completedAt, URL: "https://example/1", Trusted: true}},
		getResults: map[string]*prsource.PR{"1": {ID: "1", Title: "t", Description: "d", CompletedAt: completedAt, Trusted: true}},
	}

	svc := mock.NewMemoryService()
	cfg := testConfig()

	repoPath := setupGitRepoWithRemote(t, azureRemote)
	ingestOneRepo(context.Background(), svc, azurePorts(src, cursorStore), cfg, repoPath, false)

	if src.listCalls != 1 {
		t.Errorf("expected ListCompleted called once, got %d", src.listCalls)
	}
	if len(src.getCalls) != 1 {
		t.Errorf("expected Get called once, got %d", len(src.getCalls))
	}

	cur, ok := cursorStore.Load("azuredevops", filepath.Base(repoPath))
	if !ok {
		t.Fatal("expected a cursor to be persisted after a successful batch")
	}
	if !cur.Since.Equal(completedAt) {
		t.Errorf("expected cursor Since %v, got %v", completedAt, cur.Since)
	}
}

func TestIngestOneRepoListFailureLeavesCursorUnchanged(t *testing.T) {
	cursorStore := prcursor.NewStore(t.TempDir())
	repoPath := setupGitRepoWithRemote(t, azureRemote)
	repoName := filepath.Base(repoPath)

	existing := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := cursorStore.Save(prcursor.Cursor{Provider: "azuredevops", Repo: repoName, Since: existing}); err != nil {
		t.Fatalf("failed to seed cursor: %v", err)
	}

	src := &fakeSource{listErr: errors.New("az: auth failed")}
	svc := mock.NewMemoryService()
	cfg := testConfig()

	ingestOneRepo(context.Background(), svc, azurePorts(src, cursorStore), cfg, repoPath, false)

	cur, ok := cursorStore.Load("azuredevops", repoName)
	if !ok {
		t.Fatal("expected the existing cursor to still be present")
	}
	if !cur.Since.Equal(existing) {
		t.Errorf("expected cursor to remain unchanged at %v, got %v", existing, cur.Since)
	}
}

func TestIngestOneRepoGetFailureAbortsBatchWithoutAdvancingCursor(t *testing.T) {
	cursorStore := prcursor.NewStore(t.TempDir())
	repoPath := setupGitRepoWithRemote(t, azureRemote)
	repoName := filepath.Base(repoPath)

	existing := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := cursorStore.Save(prcursor.Cursor{Provider: "azuredevops", Repo: repoName, Since: existing}); err != nil {
		t.Fatalf("failed to seed cursor: %v", err)
	}

	src := &fakeSource{
		listResult: []prsource.PR{{ID: "1", Title: "t", CompletedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Trusted: true}},
		getErr:     errors.New("az: pr show failed"),
	}
	svc := mock.NewMemoryService()
	cfg := testConfig()

	ingestOneRepo(context.Background(), svc, azurePorts(src, cursorStore), cfg, repoPath, false)

	cur, ok := cursorStore.Load("azuredevops", repoName)
	if !ok || !cur.Since.Equal(existing) {
		t.Errorf("expected cursor to remain at %v (interrupted batch), got ok=%v since=%v", existing, ok, cur.Since)
	}
}

func TestIngestOneRepoDryRunDoesNotWriteOrCallStore(t *testing.T) {
	cursorStore := prcursor.NewStore(t.TempDir())
	repoPath := setupGitRepoWithRemote(t, azureRemote)
	repoName := filepath.Base(repoPath)

	src := &fakeSource{
		listResult: []prsource.PR{{ID: "1", Title: "t", CompletedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Trusted: true}},
	}
	cfg := testConfig()

	// svc is nil: dry-run must never touch it.
	ingestOneRepo(context.Background(), nil, azurePorts(src, cursorStore), cfg, repoPath, true)

	if len(src.getCalls) != 0 {
		t.Errorf("expected no Get calls in dry-run mode, got %d", len(src.getCalls))
	}
	if _, ok := cursorStore.Load("azuredevops", repoName); ok {
		t.Error("expected no cursor to be persisted in dry-run mode")
	}
}

func TestIngestOneRepoUnsupportedProviderIsSkipped(t *testing.T) {
	cursorStore := prcursor.NewStore(t.TempDir())
	repoPath := setupGitRepoWithRemote(t, "git@github.com:someowner/some-repo.git")
	repoName := filepath.Base(repoPath)

	src := &fakeSource{}
	svc := mock.NewMemoryService()
	cfg := testConfig()

	ingestOneRepo(context.Background(), svc, azurePorts(src, cursorStore), cfg, repoPath, false)

	if src.listCalls != 0 {
		t.Errorf("expected ListCompleted never called for an unsupported provider, got %d calls", src.listCalls)
	}
	if _, ok := cursorStore.Load("azuredevops", repoName); ok {
		t.Error("expected no cursor to be written for an unsupported provider")
	}
}

func testConfig() *config.Config {
	return &config.Config{
		ExtractMinMessages: 20,
		MaxContentChars:    20000,
		PRIngestLookback:   30 * 24 * time.Hour,
	}
}

// AC-13/AC-10: each repo is ingested through the writer the composition
// root's scope function returns for the namespace resolved once for it.
func TestIngestOneRepoUsesScopedWriter(t *testing.T) {
	cursorStore := prcursor.NewStore(t.TempDir())
	completedAt := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		listResult: []prsource.PR{{ID: "1", Title: "t", CompletedAt: completedAt, Trusted: true}},
		getResults: map[string]*prsource.PR{"1": {ID: "1", Title: "t", Description: "d", CompletedAt: completedAt, Trusted: true}},
	}
	unscoped := mock.NewMemoryService()
	scoped := mock.NewMemoryService()
	var gotNS string
	d := azurePorts(src, cursorStore)
	d.Scope = func(ns string) extraction.StoreWriter { gotNS = ns; return scoped }
	d.Settings = func(string) (string, namespace.PRIngest, string) { return "team", namespace.PRIngest{}, "" }

	repoPath := setupGitRepoWithRemote(t, azureRemote)
	ingestOneRepo(context.Background(), unscoped, d, testConfig(), repoPath, false)

	if gotNS != "team" {
		t.Errorf("scope called with %q, want the resolved namespace", gotNS)
	}
}
