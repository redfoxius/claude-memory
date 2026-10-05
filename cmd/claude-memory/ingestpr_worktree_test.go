package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"claude-memory/internal/config"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// realWorktreeFixture creates root/main (a clone with one commit) and
// root/main-wt (a linked worktree of it).
func realWorktreeFixture(t *testing.T) (root, mainDir, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root = t.TempDir()
	mainDir = filepath.Join(root, "main")
	wt = filepath.Join(root, "main-wt")
	if err := os.MkdirAll(mainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, mainDir, "init", "-q")
	gitCmd(t, mainDir, "commit", "-q", "--allow-empty", "-m", "init")
	gitCmd(t, mainDir, "worktree", "add", "-q", wt)
	return
}

func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	setupBareGitDir(t, dir)
}

func writeGitlink(t *testing.T, dir, target string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+target+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverWorktreeSkippedWhenMainPresent(t *testing.T) {
	root, mainDir, wt := realWorktreeFixture(t)
	other := filepath.Join(root, "other")
	mkRepo(t, other)
	kept, skipped := discoverReposDetailed([]string{root}, gitMainWorktree)
	want := []string{mainDir, other}
	if strings.Join(kept, "|") != strings.Join(want, "|") {
		t.Errorf("kept = %v, want %v", kept, want)
	}
	if len(skipped) != 1 || skipped[0].path != wt || normPath(skipped[0].main) != normPath(mainDir) {
		t.Errorf("skipped = %+v", skipped)
	}
}

func TestDiscoverWorktreeKeptWhenMainAbsent(t *testing.T) {
	_, mainDir, wt := realWorktreeFixture(t)
	kept, skipped := discoverReposDetailed([]string{wt}, gitMainWorktree)
	if len(kept) != 1 || kept[0] != wt || len(skipped) != 0 {
		t.Errorf("kept=%v skipped=%v (main %s)", kept, skipped, mainDir)
	}
}

func TestDiscoverWorktreeFakeResolver(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "a-main")
	wt := filepath.Join(root, "b-wt")
	mkRepo(t, mainDir)
	writeGitlink(t, wt, "/x/a-main/.git/worktrees/b-wt")
	resolve := func(string) (string, error) { return mainDir, nil }
	kept, skipped := discoverReposDetailed([]string{root}, resolve)
	if len(kept) != 1 || kept[0] != mainDir || len(skipped) != 1 {
		t.Errorf("kept=%v skipped=%v", kept, skipped)
	}
}

func TestDiscoverSubmoduleGitlinkKept(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "a-main")
	sub := filepath.Join(root, "b-sub")
	mkRepo(t, mainDir)
	writeGitlink(t, sub, "../.git/modules/b-sub")
	resolve := func(string) (string, error) { return mainDir, nil }
	kept, skipped := discoverReposDetailed([]string{root}, resolve)
	if len(kept) != 2 || len(skipped) != 0 {
		t.Errorf("kept=%v skipped=%v", kept, skipped)
	}
}

func TestDiscoverNormalClonesUnaffected(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b"} {
		mkRepo(t, filepath.Join(root, n))
	}
	resolve := func(string) (string, error) { t.Fatal("resolver must not run"); return "", nil }
	kept, skipped := discoverReposDetailed([]string{root}, resolve)
	if len(kept) != 2 || len(skipped) != 0 {
		t.Errorf("kept=%v skipped=%v", kept, skipped)
	}
}

func TestIngestPRDryRunPrintsWorktreeSkip(t *testing.T) {
	root, _, _ := realWorktreeFixture(t)
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	cfg := &config.Config{PRIngestRepos: []string{root}}
	err := runIngestPR(context.Background(), cfg, nil, ingestPorts{}, true)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "main-wt: skipped (linked worktree of main)\n") {
		t.Errorf("output = %q", out)
	}
}
