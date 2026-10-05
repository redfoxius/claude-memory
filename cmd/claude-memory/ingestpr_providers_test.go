package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/extraction"
	"github.com/redfoxius/claude-memory/internal/memory/mock"
	"github.com/redfoxius/claude-memory/internal/namespace"
	"github.com/redfoxius/claude-memory/internal/prcursor"
	"github.com/redfoxius/claude-memory/internal/prsource"
)

var t0 = time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

func goodPR(id string) prsource.PR {
	return prsource.PR{ID: id, Title: "t" + id, CompletedAt: t0, Trusted: true}
}

func sourcePorts(store *prcursor.Store, provider prsource.Provider, src prsource.Source) ingestPorts {
	return ingestPorts{
		Sources: map[prsource.Provider]prsource.Source{provider: src},
		Cursors: store,
		Haiku:   emptyHaikuRunner{},
	}
}

func listing(prs ...prsource.PR) *fakeSource {
	src := &fakeSource{listResult: prs, getResults: map[string]*prsource.PR{}}
	for i := range prs {
		p := prs[i]
		src.getResults[p.ID] = &p
	}
	return src
}

// captureLogs routes slog output into a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// captureStdout returns what f prints to os.Stdout.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestIngestGitHubRoutesAndKeysCursorByRemotePath(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	src := listing(goodPR("5"))
	repo := setupGitRepoWithRemote(t, "git@github.com:example-user/pet-game.git")

	ingestOneRepo(context.Background(), mock.NewMemoryService(), sourcePorts(store, prsource.ProviderGitHub, src), testConfig(), repo, false)

	if len(src.refs) != 1 || src.refs[0].Host != "github.com" || src.refs[0].Path != "example-user/pet-game" {
		t.Fatalf("refs = %+v", src.refs)
	}
	if len(src.getCalls) != 1 {
		t.Errorf("Get calls = %v", src.getCalls)
	}
	if _, ok := store.Load("github", cursorKey(prsource.ProviderGitHub, "github.com", "example-user/pet-game")); !ok {
		t.Error("cursor must be keyed by provider + remote path with / -> _")
	}
	if _, ok := store.Load("github", filepath.Base(repo)); ok {
		t.Error("cursor must not use the local basename for GitHub")
	}
}

func TestIngestTwoClonesWithSameBasenameGetSeparateCursors(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	for _, remote := range []string{"git@github.com:alice/app.git", "git@github.com:bob/app.git"} {
		ingestOneRepo(context.Background(), mock.NewMemoryService(), sourcePorts(store, prsource.ProviderGitHub, listing(goodPR("1"))), testConfig(), setupGitRepoWithRemote(t, remote), false)
	}
	for _, key := range []string{cursorKey(prsource.ProviderGitHub, "github.com", "alice/app"), cursorKey(prsource.ProviderGitHub, "github.com", "bob/app")} {
		if _, ok := store.Load("github", key); !ok {
			t.Errorf("missing cursor %s", key)
		}
	}
}

func TestIngestGitLabComAndSelfHostedNeedsOverride(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	src := listing()
	d := sourcePorts(store, prsource.ProviderGitLab, src)

	ingestOneRepo(context.Background(), mock.NewMemoryService(), d, testConfig(), setupGitRepoWithRemote(t, "https://gitlab.com/group/sub/proj.git"), false)
	if len(src.refs) != 1 || src.refs[0].Path != "group/sub/proj" || src.refs[0].Host != "gitlab.com" {
		t.Fatalf("gitlab.com refs = %+v", src.refs)
	}
	if _, ok := store.Load("gitlab", cursorKey(prsource.ProviderGitLab, "gitlab.com", "group/sub/proj")); !ok {
		t.Error("gitlab cursor key")
	}

	// A self-hosted host is never auto-detected.
	src.refs = nil
	self := setupGitRepoWithRemote(t, "https://git.example.org/team/proj.git")
	ingestOneRepo(context.Background(), mock.NewMemoryService(), d, testConfig(), self, false)
	if src.listCalls != 1 {
		t.Fatalf("self-hosted without override must not reach the source (calls %d)", src.listCalls)
	}

	// With the namespace override it is, against the origin's host.
	d.Settings = func(string) (string, namespace.PRIngest, string) {
		return "corp", namespace.PRIngest{Provider: "gitlab"}, ""
	}
	ingestOneRepo(context.Background(), mock.NewMemoryService(), d, testConfig(), self, false)
	if len(src.refs) != 1 || src.refs[0].Host != "git.example.org" || src.refs[0].Path != "team/proj" || src.refs[0].Provider != prsource.ProviderGitLab {
		t.Fatalf("override refs = %+v", src.refs)
	}
}

func TestIngestOverrideToUnknownProviderIsSkipped(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	src := listing()
	d := sourcePorts(store, prsource.ProviderGitHub, src)
	d.Settings = func(string) (string, namespace.PRIngest, string) {
		return "x", namespace.PRIngest{Provider: "gitlab"}, "" // no gitlab source registered
	}
	ingestOneRepo(context.Background(), mock.NewMemoryService(), d, testConfig(), setupGitRepoWithRemote(t, "git@github.com:a/b.git"), false)
	if src.listCalls != 0 {
		t.Error("source for the wrong provider was used")
	}
}

func TestIngestDisabledNamespaceAndProblemAreSkipped(t *testing.T) {
	off := false
	for name, settings := range map[string]func(string) (string, namespace.PRIngest, string){
		"disabled": func(string) (string, namespace.PRIngest, string) {
			return "sandbox", namespace.PRIngest{Enabled: &off}, ""
		},
		"problem": func(string) (string, namespace.PRIngest, string) {
			return "sandbox", namespace.PRIngest{}, "pr_ingest.provider \"x\" is unknown"
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := prcursor.NewStore(t.TempDir())
			src := listing(goodPR("1"))
			d := sourcePorts(store, prsource.ProviderGitHub, src)
			d.Settings = settings
			out := captureStdout(t, func() {
				ingestOneRepo(context.Background(), nil, d, testConfig(), setupGitRepoWithRemote(t, "git@github.com:a/b.git"), true)
			})
			if src.listCalls != 0 {
				t.Error("skipped repo reached the source")
			}
			if !strings.Contains(out, "skipped (") {
				t.Errorf("dry run must print the skip reason, got %q", out)
			}
		})
	}
}

func TestIngestInvalidPathIsSkippedBeforeAnyCall(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/a/b/c.git",     // GitHub needs exactly 2 segments
		"https://github.com/a/%7Bowner%7D", // encoded braces: decoded by url.Parse
		"git@github.com:a/b:c.git",
		"https://github.com/a/..",
	} {
		store := prcursor.NewStore(t.TempDir())
		src := listing(goodPR("1"))
		out := captureStdout(t, func() {
			ingestOneRepo(context.Background(), nil, sourcePorts(store, prsource.ProviderGitHub, src), testConfig(), setupGitRepoWithRemote(t, remote), true)
		})
		if src.listCalls != 0 || !strings.Contains(out, "skipped (invalid path)") {
			t.Errorf("%s: calls=%d out=%q", remote, src.listCalls, out)
		}
	}
}

func TestIngestBotAndUntrustedPRsAdvanceCursorWithoutGet(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	later := t0.Add(48 * time.Hour)
	src := listing(
		prsource.PR{ID: "1", CompletedAt: t0, Bot: true, Trusted: false},
		prsource.PR{ID: "2", CompletedAt: later, Trusted: false},
	)
	ingestOneRepo(context.Background(), mock.NewMemoryService(), sourcePorts(store, prsource.ProviderGitHub, src), testConfig(), setupGitRepoWithRemote(t, "git@github.com:a/b.git"), false)
	if len(src.getCalls) != 0 {
		t.Errorf("Get called for bot/untrusted PRs: %v", src.getCalls)
	}
	cur, ok := store.Load("github", cursorKey(prsource.ProviderGitHub, "github.com", "a/b"))
	if !ok || !cur.Since.Equal(later) {
		t.Errorf("cursor must pass skipped PRs: %+v ok=%v", cur, ok)
	}
}

type capturingHaiku struct{ prompts []string }

func (c *capturingHaiku) Run(_ context.Context, prompt string) ([]byte, error) {
	c.prompts = append(c.prompts, prompt)
	return []byte(`[]`), nil
}

type tokenScrubber struct{}

func (tokenScrubber) Scrub(s string) (string, bool) {
	return strings.ReplaceAll(s, "ghp_ABC123", "[REDACTED]"), strings.Contains(s, "ghp_ABC123")
}

func TestIngestPassesScrubbedCommentsToExtraction(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	src := listing()
	src.listResult = []prsource.PR{goodPR("9")}
	src.getResults["9"] = &prsource.PR{ID: "9", Title: "T", Description: "body ghp_ABC123", Trusted: true, CompletedAt: t0,
		ReviewComments: []string{"please use ctx", "leaked ghp_ABC123"}}
	h := &capturingHaiku{}
	d := sourcePorts(store, prsource.ProviderAzureDevOps, src)
	d.Haiku, d.Scrubber = h, tokenScrubber{}
	ingestOneRepo(context.Background(), mock.NewMemoryService(), d, testConfig(), setupGitRepoWithRemote(t, azureRemote), false)
	if len(h.prompts) == 0 {
		t.Fatal("extraction not run")
	}
	p := h.prompts[0]
	if strings.Contains(p, "ghp_ABC123") || !strings.Contains(p, "please use ctx") || !strings.Contains(p, "[REDACTED]") {
		t.Errorf("prompt:\n%s", p)
	}
}

func TestIngestNeverLogsRemoteUserinfo(t *testing.T) {
	logs := captureLogs(t)
	store := prcursor.NewStore(t.TempDir())
	src := listing()
	// Unsupported provider (bitbucket) and an unsupported-provider source map.
	ingestOneRepo(context.Background(), mock.NewMemoryService(), sourcePorts(store, prsource.ProviderGitHub, src), testConfig(),
		setupGitRepoWithRemote(t, "https://user:s3cr3tpass@bitbucket.org/o/r.git"), false)
	ingestOneRepo(context.Background(), mock.NewMemoryService(), sourcePorts(store, prsource.ProviderAzureDevOps, src), testConfig(),
		setupGitRepoWithRemote(t, "https://user:s3cr3tpass@github.com/o/r.git"), false)
	out := logs.String()
	if strings.Contains(out, "s3cr3tpass") || strings.Contains(out, "user:") {
		t.Errorf("userinfo leaked into logs:\n%s", out)
	}
	if !strings.Contains(out, "bitbucket.org/o/r.git") {
		t.Errorf("remote should still be logged (without userinfo):\n%s", out)
	}
}

func TestIngestPageCapErrorNamesCursorFile(t *testing.T) {
	logs := captureLogs(t)
	store := prcursor.NewStore(t.TempDir())
	src := &fakeSource{listErr: errors.Join(prsource.ErrPageCap)}
	ingestOneRepo(context.Background(), mock.NewMemoryService(), sourcePorts(store, prsource.ProviderGitHub, src), testConfig(), setupGitRepoWithRemote(t, "git@github.com:a/b.git"), false)
	want := store.File("github", cursorKey(prsource.ProviderGitHub, "github.com", "a/b"))
	if !strings.Contains(logs.String(), want) || !strings.Contains(logs.String(), "MEMORY_PR_INGEST_LOOKBACK") {
		t.Errorf("remedy must name the cursor file %s:\n%s", want, logs)
	}
}

func TestRunIngestPRAcrossRepos(t *testing.T) {
	root := t.TempDir()
	mk := func(name, remote string) {
		dir := filepath.Join(root, name)
		_ = os.MkdirAll(dir, 0o700)
		runGit(t, dir, "init")
		runGit(t, dir, "remote", "add", "origin", remote)
	}
	mk("one", "git@github.com:a/one.git")
	mk("two", "git@github.com:b/two.git")
	store := prcursor.NewStore(t.TempDir())
	src := listing()
	scoped := map[string]int{}
	d := sourcePorts(store, prsource.ProviderGitHub, src)
	d.Settings = func(p string) (string, namespace.PRIngest, string) {
		return "ns-" + filepath.Base(p), namespace.PRIngest{}, ""
	}
	d.Scope = func(ns string) extraction.StoreWriter { scoped[ns]++; return mock.NewMemoryService() }
	cfg := testConfig()
	cfg.PRIngestRepos = []string{root}
	if err := runIngestPR(context.Background(), cfg, mock.NewMemoryService(), d, false); err != nil {
		t.Fatal(err)
	}
	if scoped["ns-one"] != 1 || scoped["ns-two"] != 1 {
		t.Errorf("each repo must be written under its own namespace: %v", scoped)
	}
}

func TestCursorKeyIsCollisionFree(t *testing.T) {
	gh, gl := prsource.ProviderGitHub, prsource.ProviderGitLab
	keys := map[string]string{}
	for _, c := range []struct {
		p          prsource.Provider
		host, path string
	}{
		{gh, "github.com", "a_b/c"}, {gh, "github.com", "a/b_c"}, {gh, "github.com", "a_/b"}, {gh, "github.com", "a/_b"},
		{gl, "gitlab.com", "x/y"}, {gl, "gitlab.corp", "x/y"}, {gl, "gitlab.com", "x_y/z"}, {gl, "gitlab.com", "x/y_z"},
	} {
		k := cursorKey(c.p, c.host, c.path)
		id := string(c.p) + c.host + c.path
		if prev, dup := keys[k]; dup {
			t.Errorf("collision: %q and %q -> %s", prev, id, k)
		}
		keys[k] = id
		if strings.ContainsAny(k, "/\\: ") {
			t.Errorf("unsafe key %q", k)
		}
	}
	if cursorKey(gh, "github.com", "a/b") != cursorKey(gh, "www.github.com", "a/b") {
		t.Error("GitHub key must not depend on the host")
	}
}

func TestIngestGitHubOverrideOnNonGitHubHostIsSkipped(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	src := listing(goodPR("1"))
	d := sourcePorts(store, prsource.ProviderGitHub, src)
	d.Settings = func(string) (string, namespace.PRIngest, string) {
		return "x", namespace.PRIngest{Provider: "github"}, ""
	}
	out := captureStdout(t, func() {
		ingestOneRepo(context.Background(), nil, d, testConfig(), setupGitRepoWithRemote(t, "https://git.example.org/OWNER/repo.git"), true)
	})
	if src.listCalls != 0 || !strings.Contains(out, "skipped (unsupported provider)") {
		t.Errorf("calls=%d out=%q", src.listCalls, out)
	}
}

func TestDryRunCountExcludesBotAndUntrusted(t *testing.T) {
	store := prcursor.NewStore(t.TempDir())
	src := listing(goodPR("1"), prsource.PR{ID: "2", Bot: true}, prsource.PR{ID: "3"})
	out := captureStdout(t, func() {
		ingestOneRepo(context.Background(), nil, sourcePorts(store, prsource.ProviderGitHub, src), testConfig(), setupGitRepoWithRemote(t, "git@github.com:a/b.git"), true)
	})
	if !strings.Contains(out, "1 PR(s) would be ingested, 2 skipped (bot/untrusted)") {
		t.Errorf("out = %q", out)
	}
}
