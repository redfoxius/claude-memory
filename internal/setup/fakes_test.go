package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test doubles for the ports (plan WI-S1-6). Every internal/setup test uses
// these and explicit Paths built from realTempDir(t); no test calls t.Setenv
// or touches the real HOME, so tests can run in parallel (AC-63).

// testPaths returns a Paths whose every directory lies under one fresh temp
// dir (nothing is created except the root itself).
func testPaths(t testing.TB) Paths {
	t.Helper()
	root := realTempDir(t)
	home := filepath.Join(root, "home")
	return Paths{
		Home:            home,
		ClaudeDir:       filepath.Join(home, ".claude"),
		ClaudeJSON:      filepath.Join(home, ".claude.json"),
		ConfigDir:       filepath.Join(home, ".config", "claude-memory"),
		StateDir:        filepath.Join(home, ".local", "state", "claude-memory"),
		ShareDir:        filepath.Join(home, ".local", "share", "claude-memory"),
		BinDir:          filepath.Join(home, ".local", "bin"),
		LaunchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		SystemdUserDir:  filepath.Join(home, ".config", "systemd", "user"),
		Cwd:             filepath.Join(root, "work"),
		Self:            filepath.Join(home, ".local", "bin", "claude-memory"),
		UID:             501,
	}
}

// ---- FakeRunner -----------------------------------------------------------

// FakeRunner is a strict, matcher-scripted Runner (AC-63). A command that no
// script matches fails the test; every call is recorded, and every call is
// checked against the AC-18 deny list (deniedCall in guard_test.go) both when
// it is made and again at test cleanup.
type FakeRunner struct {
	t        testing.TB
	ReadOnly bool // behave like the read-only adapter: Mutating cmds → ErrReadOnly

	mu      sync.Mutex
	scripts []fakeScript
	denies  []fakeDeny
	paths   map[string]string
	calls   []Cmd
}

type fakeScript struct {
	match func(Cmd) bool
	res   Result
	err   error
}

type fakeDeny struct {
	match  func(Cmd) bool
	reason string
}

// NewFakeRunner returns a FakeRunner bound to t. At cleanup it re-asserts the
// deny list over every recorded call.
func NewFakeRunner(t testing.TB) *FakeRunner {
	t.Helper()
	r := &FakeRunner{t: t, paths: map[string]string{}}
	t.Cleanup(func() { assertNoDeniedCalls(t, r.Calls()) })
	return r
}

// Script answers every Cmd matching match with res. Scripts are tried in
// the order they were added; the first match wins.
func (r *FakeRunner) Script(match func(Cmd) bool, res Result) *FakeRunner {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts = append(r.scripts, fakeScript{match: match, res: res})
	return r
}

// ScriptError makes every Cmd matching match fail to run with err (e.g. a
// context deadline or "not found").
func (r *FakeRunner) ScriptError(match func(Cmd) bool, err error) *FakeRunner {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scripts = append(r.scripts, fakeScript{match: match, err: err})
	return r
}

// Deny fails the test when a Cmd matching match is run, with reason (e.g.
// AC-67: the registered MCP command must never be executed). Deny rules are
// checked before scripts.
func (r *FakeRunner) Deny(match func(Cmd) bool, reason string) *FakeRunner {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.denies = append(r.denies, fakeDeny{match: match, reason: reason})
	return r
}

// SetPath makes LookPath(name) return full.
func (r *FakeRunner) SetPath(name, full string) *FakeRunner {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths[name] = full
	return r
}

// Run implements Runner.
func (r *FakeRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	r.t.Helper()
	r.mu.Lock()
	r.calls = append(r.calls, cloneCmd(c))
	denies := slices.Clone(r.denies)
	scripts := slices.Clone(r.scripts)
	r.mu.Unlock()

	if reason := deniedCall(c); reason != "" {
		r.t.Errorf("FakeRunner: denied command %q: %s", c.Argv, reason)
		return Result{}, fmt.Errorf("denied command %q: %s", c.Argv, reason)
	}
	for _, d := range denies {
		if d.match(c) {
			r.t.Errorf("FakeRunner: denied command %q: %s", c.Argv, d.reason)
			return Result{}, fmt.Errorf("denied command %q: %s", c.Argv, d.reason)
		}
	}
	if r.ReadOnly && c.Mutating {
		return Result{}, ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	for _, s := range scripts {
		if s.match(c) {
			return s.res, s.err
		}
	}
	r.t.Errorf("FakeRunner: unscripted command %q", c.Argv)
	return Result{}, fmt.Errorf("unscripted command %q", c.Argv)
}

// LookPath implements Runner.
func (r *FakeRunner) LookPath(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.paths[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("%s: executable file not found in $PATH: %w", name, fs.ErrNotExist)
}

// Calls returns a copy of every recorded call, in order.
func (r *FakeRunner) Calls() []Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Cmd, len(r.calls))
	for i, c := range r.calls {
		out[i] = cloneCmd(c)
	}
	return out
}

// MutatingCalls counts the recorded calls with Mutating set.
func (r *FakeRunner) MutatingCalls() int {
	n := 0
	for _, c := range r.Calls() {
		if c.Mutating {
			n++
		}
	}
	return n
}

func cloneCmd(c Cmd) Cmd {
	c.Argv = slices.Clone(c.Argv)
	c.Env = slices.Clone(c.Env)
	c.Stdin = slices.Clone(c.Stdin)
	return c
}

// ArgvEq matches a Cmd whose argv equals argv exactly.
func ArgvEq(argv ...string) func(Cmd) bool {
	return func(c Cmd) bool { return slices.Equal(c.Argv, argv) }
}

// ArgvPrefix matches a Cmd whose argv starts with prefix.
func ArgvPrefix(prefix ...string) func(Cmd) bool {
	return func(c Cmd) bool { return len(c.Argv) >= len(prefix) && slices.Equal(c.Argv[:len(prefix)], prefix) }
}

// Argv0 matches a Cmd whose program (by base name) is name.
func Argv0(name string) func(Cmd) bool {
	return func(c Cmd) bool { return len(c.Argv) > 0 && filepath.Base(c.Argv[0]) == name }
}

// ---- FakeFS ---------------------------------------------------------------

// FSOp names an FS operation for fault injection and write accounting.
type FSOp string

// FS operations FakeFS can fail.
const (
	OpRead   FSOp = "read" // ReadFile, Stat, Lstat, ReadDir, EvalSymlinks
	OpWrite  FSOp = "write"
	OpRename FSOp = "rename" // the rename step of WriteFileAtomic
	OpMkdir  FSOp = "mkdir"
	OpRemove FSOp = "remove"
	OpChmod  FSOp = "chmod"
	OpLock   FSOp = "lock"
)

// FakeFS is the FS port over a real temp directory (plan WI-S1-6): reads and
// writes hit the disk under Root, so tests see real modes and real atomic
// renames. It refuses any path outside Root (a test can never touch the real
// HOME), counts writes, can be put in read-only mode (writes → ErrDryRun, as
// the doctor and dry-run adapters do), and injects faults per operation.
type FakeFS struct {
	t    testing.TB
	Root string
	// ReadOnly makes every write method return ErrDryRun (and count nothing).
	ReadOnly bool
	// Fail, when set, is consulted before each operation; a non-nil result is
	// returned instead of performing it.
	Fail func(op FSOp, path string) error

	mu     sync.Mutex
	writes []string // "<op> <path>" of every performed or attempted write
	locks  map[string]bool
}

// NewFakeFS returns a FakeFS rooted at root (typically Paths.Home's parent
// from testPaths, or realTempDir(t)).
func NewFakeFS(t testing.TB, root string) *FakeFS {
	t.Helper()
	return &FakeFS{t: t, Root: filepath.Clean(root), locks: map[string]bool{}}
}

// Writes returns every attempted write, as "<op> <path>", in order.
// Read-only mode records attempts too, so "doctor performs zero writes" is
// asserted as len(Writes()) == 0.
func (f *FakeFS) Writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

func (f *FakeFS) check(op FSOp, p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("FakeFS: %s %q: path is not absolute", op, p)
	}
	clean := filepath.Clean(p)
	if clean != f.Root && !strings.HasPrefix(clean, f.Root+string(filepath.Separator)) {
		f.t.Errorf("FakeFS: %s %q outside the test root %q", op, p, f.Root)
		return fmt.Errorf("FakeFS: %s %q outside the test root: %w", op, p, fs.ErrPermission)
	}
	if f.Fail != nil {
		if err := f.Fail(op, clean); err != nil {
			return err
		}
	}
	return nil
}

func (f *FakeFS) write(op FSOp, p string) error {
	f.mu.Lock()
	f.writes = append(f.writes, string(op)+" "+p)
	f.mu.Unlock()
	if f.ReadOnly {
		return ErrDryRun
	}
	return f.check(op, p)
}

// ReadFile implements FS.
func (f *FakeFS) ReadFile(p string) ([]byte, error) {
	if err := f.check(OpRead, p); err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Stat implements FS.
func (f *FakeFS) Stat(p string) (fs.FileInfo, error) {
	if err := f.check(OpRead, p); err != nil {
		return nil, err
	}
	return os.Stat(p)
}

// Lstat implements FS.
func (f *FakeFS) Lstat(p string) (fs.FileInfo, error) {
	if err := f.check(OpRead, p); err != nil {
		return nil, err
	}
	return os.Lstat(p)
}

// ReadDir implements FS.
func (f *FakeFS) ReadDir(p string) ([]fs.DirEntry, error) {
	if err := f.check(OpRead, p); err != nil {
		return nil, err
	}
	return os.ReadDir(p)
}

// EvalSymlinks implements FS.
func (f *FakeFS) EvalSymlinks(p string) (string, error) {
	if err := f.check(OpRead, p); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// Writable implements FS with the owner write bit of the existing path (the
// tests run as a normal user; access(2) itself is the adapter's business).
func (f *FakeFS) Writable(p string) bool {
	if f.check(OpRead, p) != nil {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.Mode().Perm()&0o200 != 0
}

// WriteFileAtomic implements FS: temp file in the same dir, then rename.
func (f *FakeFS) WriteFileAtomic(p string, b []byte, mode fs.FileMode) error {
	if err := f.write(OpWrite, p); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	if err := f.check(OpRename, p); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// MkdirAll implements FS.
func (f *FakeFS) MkdirAll(p string, mode fs.FileMode) error {
	if err := f.write(OpMkdir, p); err != nil {
		return err
	}
	return os.MkdirAll(p, mode)
}

// Remove implements FS.
func (f *FakeFS) Remove(p string) error {
	if err := f.write(OpRemove, p); err != nil {
		return err
	}
	return os.Remove(p)
}

// Chmod implements FS.
func (f *FakeFS) Chmod(p string, mode fs.FileMode) error {
	if err := f.write(OpChmod, p); err != nil {
		return err
	}
	return os.Chmod(p, mode)
}

// Lock implements FS with an in-process lock table: a second Lock on the
// same path fails until the first is released.
func (f *FakeFS) Lock(p string) (func() error, error) {
	if err := f.write(OpLock, p); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locks[p] {
		return nil, fmt.Errorf("lock %s: already held", p)
	}
	f.locks[p] = true
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.locks, p)
		return nil
	}, nil
}

// ---- FakeClock ------------------------------------------------------------

// FakeClock is a Clock that only moves when told to.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock returns a clock fixed at now.
func NewFakeClock(now time.Time) *FakeClock { return &FakeClock{now: now} }

// Now implements Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ---- interface conformance and self-tests --------------------------------

var (
	_ Runner = (*FakeRunner)(nil)
	_ FS     = (*FakeFS)(nil)
	_ Clock  = (*FakeClock)(nil)
)

func TestFakeRunnerScriptsAndRecords(t *testing.T) {
	t.Parallel()
	r := NewFakeRunner(t).
		Script(ArgvEq("launchctl", "print", "gui/501/x"), Result{ExitCode: 113, Stderr: []byte("not found")}).
		Script(Argv0("git"), Result{Stdout: []byte("ok")})
	r.SetPath("git", "/usr/bin/git")

	res, err := r.Run(context.Background(), Cmd{Argv: []string{"launchctl", "print", "gui/501/x"}})
	if err != nil || res.ExitCode != 113 {
		t.Fatalf("launchctl: %+v, %v", res, err)
	}
	res, err = r.Run(context.Background(), Cmd{Argv: []string{"/usr/bin/git", "--version"}})
	if err != nil || string(res.Stdout) != "ok" {
		t.Fatalf("git: %+v, %v", res, err)
	}
	if p, err := r.LookPath("git"); err != nil || p != "/usr/bin/git" {
		t.Errorf("LookPath(git) = %q, %v", p, err)
	}
	if _, err := r.LookPath("az"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("LookPath(az) err = %v, want fs.ErrNotExist", err)
	}
	if got := len(r.Calls()); got != 2 {
		t.Errorf("recorded %d calls, want 2", got)
	}
}

func TestFakeRunnerReadOnlyRejectsMutating(t *testing.T) {
	t.Parallel()
	r := NewFakeRunner(t).Script(Argv0("launchctl"), Result{})
	r.ReadOnly = true
	_, err := r.Run(context.Background(), Cmd{Argv: []string{"launchctl", "bootstrap", "gui/501", "/x.plist"}, Mutating: true})
	if !errors.Is(err, ErrReadOnly) {
		t.Errorf("err = %v, want ErrReadOnly", err)
	}
	if r.MutatingCalls() != 1 {
		t.Errorf("MutatingCalls = %d, want 1 (attempts are recorded)", r.MutatingCalls())
	}
}

func TestFakeFSWritesFaultsAndReadOnly(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	f := NewFakeFS(t, root)
	dir := filepath.Join(root, "a", "b")
	if err := f.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "file")
	if err := f.WriteFileAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat(p)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v, %v", info, err)
	}

	// A failing rename leaves the original intact (AC-9).
	f.Fail = func(op FSOp, path string) error {
		if op == OpRename {
			return errors.New("injected rename failure")
		}
		return nil
	}
	if err := f.WriteFileAtomic(p, []byte("two"), 0o600); err == nil {
		t.Fatal("expected injected failure")
	}
	if b, _ := f.ReadFile(p); string(b) != "one" {
		t.Errorf("content after failed rename = %q, want %q", b, "one")
	}
	f.Fail = nil

	ro := NewFakeFS(t, root)
	ro.ReadOnly = true
	if err := ro.WriteFileAtomic(p, []byte("three"), 0o600); !errors.Is(err, ErrDryRun) {
		t.Errorf("read-only write err = %v, want ErrDryRun", err)
	}
	if err := ro.Remove(p); !errors.Is(err, ErrDryRun) {
		t.Errorf("read-only remove err = %v, want ErrDryRun", err)
	}
	if len(ro.Writes()) != 2 {
		t.Errorf("read-only Writes() = %v, want the 2 attempts", ro.Writes())
	}
	if b, _ := ro.ReadFile(p); string(b) != "one" {
		t.Errorf("read-only FS changed the file: %q", b)
	}

	unlock, err := f.Lock(filepath.Join(root, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Lock(filepath.Join(root, "lock")); err == nil {
		t.Error("second Lock succeeded")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Lock(filepath.Join(root, "lock")); err != nil {
		t.Errorf("Lock after unlock: %v", err)
	}
}

func TestFakeClock(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 1, 10, 15, 0, 0, time.UTC)
	c := NewFakeClock(start)
	c.Advance(90 * time.Second)
	if got := c.Now(); !got.Equal(start.Add(90 * time.Second)) {
		t.Errorf("Now = %v", got)
	}
}

func TestTestPathsAreUnderOneRoot(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	root := filepath.Dir(p.Home)
	for _, d := range []string{p.ClaudeDir, p.ClaudeJSON, p.ConfigDir, p.StateDir, p.ShareDir, p.BinDir,
		p.LaunchAgentsDir, p.SystemdUserDir, p.Cwd, p.Self, p.EnvFile(), p.Manifest(), p.SettingsJSON()} {
		if !strings.HasPrefix(d, root+string(filepath.Separator)) {
			t.Errorf("%q not under %q", d, root)
		}
	}
}

// realTempDir is realTempDir(t) with symlinks resolved: on macOS /var is a link
// to /private/var, and code that resolves symlinks must see the same path
// the test built its fixtures under.
func realTempDir(t testing.TB) string {
	t.Helper()
	d := t.TempDir()
	if r, err := filepath.EvalSymlinks(d); err == nil {
		return r
	}
	return d
}
