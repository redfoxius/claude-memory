package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/extraction"
	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/memory/mock"
	"github.com/redfoxius/claude-memory/internal/namespace"
	"github.com/redfoxius/claude-memory/internal/prcursor"
	"github.com/redfoxius/claude-memory/internal/prsource"
)

// seqHaikuRunner answers each call from a script, repeating the last answer.
type seqHaikuRunner struct {
	mu   sync.Mutex
	outs []string
	n    int
}

func (r *seqHaikuRunner) Run(context.Context, string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.n
	if i >= len(r.outs) {
		i = len(r.outs) - 1
	}
	r.n++
	return []byte(r.outs[i]), nil
}

// Two repos mapped to two namespaces are written through two different
// scoped writers; neither writer sees the other repo's records.
func TestIngestOneRepo_TwoReposRouteToTwoNamespaceWriters(t *testing.T) {
	completedAt := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{
		listResult: []prsource.PR{{ID: "1", Title: "t", CompletedAt: completedAt, Trusted: true}},
		getResults: map[string]*prsource.PR{"1": {ID: "1", Title: "t", Description: "d", CompletedAt: completedAt, Trusted: true}},
	}
	repoA := setupGitRepoWithRemote(t, azureRemote)
	repoB := setupGitRepoWithRemote(t, azureRemote)
	nsOf := map[string]string{repoA: "ns-a", repoB: "ns-b"}

	writers := map[string]*mock.Service{"ns-a": mock.NewMemoryService(), "ns-b": mock.NewMemoryService()}
	var scoped []string
	d := azurePorts(src, prcursor.NewStore(t.TempDir()))
	d.Haiku = &seqHaikuRunner{outs: []string{
		// extraction call, then decision call, once per repo
		`[{"kind":"convention","title":"A convention","content":"From a PR."}]`,
		`{"action":"ADD"}`,
		`[{"kind":"convention","title":"B convention","content":"From a PR."}]`,
		`{"action":"ADD"}`,
	}}
	d.Settings = func(p string) (string, namespace.PRIngest, string) { return nsOf[p], namespace.PRIngest{}, "" }
	d.Scope = func(ns string) extraction.StoreWriter { scoped = append(scoped, ns); return writers[ns] }

	unscoped := mock.NewMemoryService()
	for _, repo := range []string{repoA, repoB} {
		ingestOneRepo(context.Background(), unscoped, d, testConfig(), repo, false)
	}

	if strings.Join(scoped, ",") != "ns-a,ns-b" {
		t.Errorf("scope calls = %v, want [ns-a ns-b]", scoped)
	}
	for ns, w := range writers {
		if got := len(w.GetStoredRecords()); got != 1 {
			t.Errorf("writer %s stored %d records, want 1", ns, got)
		}
	}
	if got := len(unscoped.GetStoredRecords()); got != 0 {
		t.Errorf("unscoped writer stored %d records, want 0", got)
	}
}

// eval-retrieval keeps its synthetic fixtures out of real namespaces, unless
// MEMORY_NAMESPACE is set explicitly.
func TestEvalScope(t *testing.T) {
	base := memory.New(&fakeHookStore{}, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, &config.Config{Namespace: "from-env"})

	t.Setenv(config.NamespaceEnv, "")
	if got := evalScope(base).Namespace(); got != evalNamespace {
		t.Errorf("no env: namespace = %q, want %q", got, evalNamespace)
	}
	t.Setenv(config.NamespaceEnv, "from-env")
	if got := evalScope(base).Namespace(); got != "from-env" {
		t.Errorf("with env: namespace = %q, want from-env", got)
	}
}

// The hook scopes its search to the namespace of the prompt's cwd, not of the
// hook process's own directory.
func TestHook_SearchScopedToPayloadCwdNamespace(t *testing.T) {
	old := resolveNamespace
	resolveNamespace = func(dir string) string {
		if dir == "/work/pet-game" {
			return "pet-game"
		}
		return "other"
	}
	t.Cleanup(func() { resolveNamespace = old })

	cfg := hookTestCfg()
	var got []string
	store := &fakeHookStore{
		searchFn: func(_ context.Context, _ string, _ []float32, _ string, opts memory.SearchOptions) (*memory.SearchResult, error) {
			got = opts.Namespaces
			return &memory.SearchResult{}, nil
		},
	}
	svc := memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, cfg)
	if _, err := runHookCmd(t, context.Background(), cfg, svc, `{"prompt":"p","cwd":"/work/pet-game"}`); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "pet-game" || got[1] != "global" {
		t.Errorf("search namespaces = %v, want [pet-game global]", got)
	}
}

func TestCmdNamespaces_RejectsInvalidGlob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := cmdNamespaces([]string{"init", "x=/a/["}); err == nil || !strings.Contains(err.Error(), "invalid path glob") {
		t.Errorf("init err = %v", err)
	}
	if err := cmdNamespaces([]string{"add", "x", "/a/["}); err == nil || !strings.Contains(err.Error(), "invalid path glob") {
		t.Errorf("add err = %v", err)
	}
}
