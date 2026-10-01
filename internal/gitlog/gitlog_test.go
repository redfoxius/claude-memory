package gitlog

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/memory"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, msg string) string {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	return dir
}

func TestChanged(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "1")
	write(t, dir, "b.go", "1")
	c0 := commit(t, dir, "c0")

	write(t, dir, "a.go", "2")
	commit(t, dir, "c1")
	write(t, dir, "a.go", "3")
	h := commit(t, dir, "c2")

	g := Exec{}
	ctx := context.Background()

	changed, n, err := g.Changed(ctx, dir, h, c0, []string{"a.go"})
	if err != nil || !changed || n != 2 {
		t.Errorf("a.go: changed=%v n=%d err=%v, want true/2", changed, n, err)
	}

	changed, _, err = g.Changed(ctx, dir, h, c0, []string{"b.go"})
	if err != nil || changed {
		t.Errorf("unlisted-only: changed=%v err=%v, want fresh", changed, err)
	}

	// change then revert: tree identical -> fresh although commits touched the file.
	write(t, dir, "b.go", "2")
	commit(t, dir, "c3")
	write(t, dir, "b.go", "1")
	h2 := commit(t, dir, "c4")
	changed, _, err = g.Changed(ctx, dir, h2, c0, []string{"b.go"})
	if err != nil || changed {
		t.Errorf("change+revert: changed=%v err=%v, want fresh", changed, err)
	}

	// unknown sha -> error, never "stale"
	if changed, _, err = g.Changed(ctx, dir, h2, strings.Repeat("a", 40), []string{"a.go"}); err == nil || changed {
		t.Errorf("unknown sha: changed=%v err=%v, want error", changed, err)
	}

	// invalid sha is rejected before git runs
	if _, _, err = g.Changed(ctx, dir, h2, "--output=x", []string{"a.go"}); !errors.Is(err, ErrUnchecked) {
		t.Errorf("invalid sha err = %v", err)
	}
}

func TestChanged_RewrittenShaWithSameContentIsFresh(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "1")
	commit(t, dir, "base")
	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "a.go", "2")
	feat := commit(t, dir, "feature change")
	git(t, dir, "checkout", "-q", "main")
	git(t, dir, "merge", "--squash", "feature")
	head := commit(t, dir, "squash")

	// feature's sha is not an ancestor of main, yet content is identical.
	changed, _, err := Exec{}.Changed(context.Background(), dir, head, feat, []string{"a.go"})
	if err != nil || changed {
		t.Errorf("squash-merged: changed=%v err=%v, want fresh", changed, err)
	}

	// different content at HEAD -> changed
	write(t, dir, "a.go", "9")
	head2 := commit(t, dir, "later")
	changed, _, err = Exec{}.Changed(context.Background(), dir, head2, feat, []string{"a.go"})
	if err != nil || !changed {
		t.Errorf("later change: changed=%v err=%v, want changed", changed, err)
	}
}

func TestChanged_LiteralPathspecs(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "1")
	c0 := commit(t, dir, "c0")
	write(t, dir, "a.go", "2")
	h := commit(t, dir, "c1")

	// Pathspec magic and option-looking names are literal: they match no file.
	for _, f := range []string{":(glob)**", "--output=x", "*.go"} {
		changed, _, err := Exec{}.Changed(context.Background(), dir, h, c0, []string{f})
		if err != nil || changed {
			t.Errorf("%q: changed=%v err=%v, want fresh (literal, matches nothing)", f, changed, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Error("--output=x was interpreted as an option")
	}
}

func TestResolve(t *testing.T) {
	dir := newRepo(t)
	g := Exec{}
	ctx := context.Background()

	// unborn HEAD: checkout known, head empty
	co, head, ok, err := g.Resolve(ctx, dir)
	if err != nil || !ok || co.Dir != dir || co.Repo != filepath.Base(dir) || head != "" {
		t.Errorf("unborn: co=%+v head=%q ok=%v err=%v", co, head, ok, err)
	}

	write(t, dir, "sub/a.go", "1")
	h := commit(t, dir, "c0")
	co, head, ok, err = g.Resolve(ctx, filepath.Join(dir, "sub"))
	if err != nil || !ok || co.Dir != dir || head != h {
		t.Errorf("subdir: co=%+v head=%q ok=%v err=%v", co, head, ok, err)
	}
	if got, _ := g.Head(ctx, dir); got != h {
		t.Errorf("Head = %q, want %q", got, h)
	}

	// not a checkout
	if _, _, ok, err = g.Resolve(ctx, t.TempDir()); err != nil || ok {
		t.Errorf("non-repo: ok=%v err=%v", ok, err)
	}
	if _, _, ok, _ = g.Resolve(ctx, ""); ok {
		t.Error("empty cwd resolved")
	}
}

func TestDirty(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "1")
	write(t, dir, "other.go", "1")
	commit(t, dir, "c0")
	g := Exec{}
	ctx := context.Background()

	if d, err := g.Dirty(ctx, dir, []string{"a.go"}); err != nil || d {
		t.Errorf("clean: dirty=%v err=%v", d, err)
	}

	write(t, dir, "a.go", "2") // modified tracked
	if d, _ := g.Dirty(ctx, dir, []string{"a.go"}); !d {
		t.Error("modified file not dirty")
	}
	git(t, dir, "add", "a.go") // staged
	if d, _ := g.Dirty(ctx, dir, []string{"a.go"}); !d {
		t.Error("staged file not dirty")
	}
	commit(t, dir, "c1")

	write(t, dir, "new.go", "x") // untracked listed file
	if d, _ := g.Dirty(ctx, dir, []string{"new.go"}); !d {
		t.Error("untracked listed file not dirty")
	}

	write(t, dir, "other.go", "2") // unrelated modification
	if d, _ := g.Dirty(ctx, dir, []string{"a.go"}); d {
		t.Error("unrelated modified file made a.go dirty")
	}
}

func TestCommandHardening(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "1")
	h := commit(t, dir, "c0")

	t.Setenv("GIT_DIR", "/nonexistent")
	t.Setenv("GIT_WORK_TREE", "/nonexistent")
	t.Setenv("LC_ALL", "C")
	cmd := Exec{}.command(context.Background(), "/tmp", "status")
	if cmd.WaitDelay != waitDelay {
		t.Errorf("WaitDelay = %v", cmd.WaitDelay)
	}
	env := strings.Join(cmd.Env, "\n")
	if strings.Contains(env, "GIT_DIR") || strings.Contains(env, "GIT_WORK_TREE") {
		t.Error("inherited GIT_* variable leaked into the child environment")
	}
	for _, want := range []string{"GIT_LITERAL_PATHSPECS=1", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %s", want)
		}
	}

	// A GIT_DIR in the parent must not redirect git away from the repo.
	if got, _ := (Exec{}).Head(context.Background(), dir); got != h {
		t.Errorf("Head with hostile GIT_DIR = %q, want %q", got, h)
	}
}

// --- cache ---

type countingHistory struct {
	calls   int
	changed bool
	n       int
	err     error
	delay   time.Duration
}

func (c *countingHistory) Resolve(context.Context, string) (memory.Checkout, string, bool, error) {
	return memory.Checkout{}, "", false, nil
}
func (c *countingHistory) Head(context.Context, string) (string, error) { return "", nil }
func (c *countingHistory) Dirty(context.Context, string, []string) (bool, error) {
	return false, nil
}
func (c *countingHistory) Changed(ctx context.Context, _, _, _ string, _ []string) (bool, int, error) {
	c.calls++
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return false, 0, ctx.Err()
		}
	}
	return c.changed, c.n, c.err
}

func TestCached_HitsMissesAndHeadKey(t *testing.T) {
	inner := &countingHistory{changed: true, n: 3}
	c := Cached(inner, NewMapCache(), time.Second)
	ctx := context.Background()
	files := []string{"b.go", "a.go"}

	for i := 0; i < 3; i++ {
		changed, n, err := c.Changed(ctx, "/d", "H1", "abcdef1", files)
		if err != nil || !changed || n != 3 {
			t.Fatalf("call %d: %v %d %v", i, changed, n, err)
		}
	}
	if inner.calls != 1 {
		t.Errorf("inner calls = %d, want 1 (cached)", inner.calls)
	}
	// file order does not matter; new HEAD recomputes
	c.Changed(ctx, "/d", "H1", "abcdef1", []string{"a.go", "b.go"})
	if inner.calls != 1 {
		t.Error("file order changed the key")
	}
	c.Changed(ctx, "/d", "H2", "abcdef1", files)
	if inner.calls != 2 {
		t.Errorf("new HEAD did not recompute (calls=%d)", inner.calls)
	}
}

func TestCached_NegativeCachingRules(t *testing.T) {
	ctx := context.Background()

	// git error -> unchecked, cached
	inner := &countingHistory{err: ErrUnchecked}
	c := Cached(inner, NewMapCache(), 50*time.Millisecond)
	c.Changed(ctx, "/d", "H", "abcdef1", []string{"a"})
	if _, _, err := c.Changed(ctx, "/d", "H", "abcdef1", []string{"a"}); !errors.Is(err, ErrUnchecked) || inner.calls != 1 {
		t.Errorf("error not negative-cached: err=%v calls=%d", err, inner.calls)
	}

	// ceiling timeout (full budget available) -> cached as unchecked
	inner = &countingHistory{delay: time.Second}
	c = Cached(inner, NewMapCache(), 30*time.Millisecond)
	tctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	c.Changed(tctx, "/d", "H", "abcdef1", []string{"a"})
	cancel()
	c.Changed(ctx, "/d", "H", "abcdef1", []string{"a"})
	if inner.calls != 1 {
		t.Errorf("ceiling timeout not cached: calls=%d", inner.calls)
	}

	// budget-shortened timeout (less than ceiling left) -> NOT cached
	inner = &countingHistory{delay: time.Second}
	c = Cached(inner, NewMapCache(), 500*time.Millisecond)
	tctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
	c.Changed(tctx, "/d", "H", "abcdef1", []string{"a"})
	cancel()
	inner.delay = 0
	c.Changed(ctx, "/d", "H", "abcdef1", []string{"a"})
	if inner.calls != 2 {
		t.Errorf("budget-shortened timeout was cached: calls=%d", inner.calls)
	}

	// expired context: hits served, misses return an error without calling inner
	inner = &countingHistory{changed: true, n: 1}
	c = Cached(inner, NewMapCache(), time.Second)
	c.Changed(ctx, "/d", "H", "abcdef1", []string{"hit"})
	ectx, cancel := context.WithCancel(ctx)
	cancel()
	if ch, _, err := c.Changed(ectx, "/d", "H", "abcdef1", []string{"hit"}); err != nil || !ch {
		t.Errorf("expired ctx should still serve a hit: %v %v", ch, err)
	}
	before := inner.calls
	if _, _, err := c.Changed(ectx, "/d", "H", "abcdef1", []string{"miss"}); err == nil || inner.calls != before {
		t.Errorf("expired ctx miss: err=%v inner calls %d->%d", err, before, inner.calls)
	}
}

func TestFileCache_PersistsDropsOldHeadsAndSurvivesCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale-cache", "sess.json")
	ctx := context.Background()

	inner := &countingHistory{changed: true, n: 2}
	Cached(inner, NewFileCache(path), time.Second).Changed(ctx, "/d", "H1", "abcdef1", []string{"a"})

	// a second process (new FileCache) reads the file
	inner2 := &countingHistory{}
	if ch, n, _ := Cached(inner2, NewFileCache(path), time.Second).Changed(ctx, "/d", "H1", "abcdef1", []string{"a"}); !ch || n != 2 || inner2.calls != 0 {
		t.Errorf("file cache miss: %v %d calls=%d", ch, n, inner2.calls)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("cache file: %v %v", st, err)
	}

	// new HEAD drops old entries from the file
	Cached(&countingHistory{}, NewFileCache(path), time.Second).Changed(ctx, "/d", "H2", "abcdef1", []string{"a"})
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "H1") {
		t.Errorf("old-HEAD entry kept: %s", b)
	}

	// corrupt file reads as empty and is rewritten
	os.WriteFile(path, []byte("{not json"), 0o600)
	inner3 := &countingHistory{changed: false}
	Cached(inner3, NewFileCache(path), time.Second).Changed(ctx, "/d", "H2", "abcdef1", []string{"a"})
	if inner3.calls != 1 {
		t.Errorf("corrupt cache: calls=%d", inner3.calls)
	}
}
