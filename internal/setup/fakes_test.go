package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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

// ---- FakeLog, FakeStep, FakePrompter, FakeReporter (WI-S2-1c) -------------

// FakeLog is a shared, ordered call log ("Seed envfile", "Detect envfile",
// "Apply envfile", ...) for engine tests.
type FakeLog struct {
	mu    sync.Mutex
	calls []string
}

// Add appends one entry.
func (l *FakeLog) Add(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, fmt.Sprintf(format, a...))
}

// Calls returns a copy of the log.
func (l *FakeLog) Calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// Count counts entries equal to s.
func (l *FakeLog) Count(s string) int {
	n := 0
	for _, c := range l.Calls() {
		if c == s {
			n++
		}
	}
	return n
}

// Index returns the index of the first entry equal to s, or -1.
func (l *FakeLog) Index(s string) int { return slices.Index(l.Calls(), s) }

// FakeStep is a scriptable Step; it always implements Seeder, Configurer and
// MissingInputter, with nil hooks meaning "do nothing", and logs every call.
type FakeStep struct {
	StepID, StepTitle string
	Req               []string
	Log               *FakeLog

	// DetectF gets the 1-based Detect call number for this step.
	DetectF    func(n int, st *RunState) Detection
	PlanF      func(st *RunState, ch Choices) (Plan, error)
	ApplyF     func(n int, wc WritePorts, st *RunState, p Plan) (StepResult, error)
	SeedF      func(st *RunState) ([]Note, error)
	ConfigureF func(ui Prompter, st *RunState) error
	MissingF   func(st *RunState) bool

	detects, applies int
}

var (
	_ Step            = (*FakeStep)(nil)
	_ Seeder          = (*FakeStep)(nil)
	_ Configurer      = (*FakeStep)(nil)
	_ MissingInputter = (*FakeStep)(nil)
)

// ID implements Step.
func (f *FakeStep) ID() string { return f.StepID }

// Title implements Step.
func (f *FakeStep) Title() string {
	if f.StepTitle != "" {
		return f.StepTitle
	}
	return f.StepID
}

// Requires implements Step.
func (f *FakeStep) Requires() []string { return f.Req }

// Detect implements Step.
func (f *FakeStep) Detect(_ context.Context, _ ReadPorts, st *RunState) Detection {
	f.detects++
	f.Log.Add("Detect %s", f.StepID)
	if f.DetectF == nil {
		return Detection{State: StateOK, Artifacts: []ArtifactState{{ID: f.StepID, State: StateOK}}}
	}
	return f.DetectF(f.detects, st)
}

// Plan implements Step; the default plan lists every artifact chosen apply.
func (f *FakeStep) Plan(_ context.Context, _ ReadPorts, st *RunState, ch Choices) (Plan, error) {
	f.Log.Add("Plan %s", f.StepID)
	if f.PlanF != nil {
		return f.PlanF(st, ch)
	}
	var p Plan
	for _, id := range applyIDs(ch) {
		p.Actions = append(p.Actions, Action{Artifact: id, Verb: "write", Desc: id})
	}
	return p, nil
}

// Apply implements Step.
func (f *FakeStep) Apply(_ context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	f.applies++
	f.Log.Add("Apply %s", f.StepID)
	if f.ApplyF == nil {
		return StepResult{}, nil
	}
	return f.ApplyF(f.applies, wc, st, p)
}

// Seed implements Seeder.
func (f *FakeStep) Seed(_ context.Context, _ ReadPorts, st *RunState) ([]Note, error) {
	f.Log.Add("Seed %s", f.StepID)
	if f.SeedF == nil {
		return nil, nil
	}
	return f.SeedF(st)
}

// Configure implements Configurer.
func (f *FakeStep) Configure(_ context.Context, _ ReadPorts, ui Prompter, st *RunState) error {
	f.Log.Add("Configure %s", f.StepID)
	if f.ConfigureF == nil {
		return nil
	}
	return f.ConfigureF(ui, st)
}

// MissingInput implements MissingInputter.
func (f *FakeStep) MissingInput(st *RunState) bool { return f.MissingF != nil && f.MissingF(st) }

// FakeWorld holds artifact states that FakeStep hooks read and write.
type FakeWorld struct {
	mu sync.Mutex
	m  map[string]State
}

// NewFakeWorld returns a world with the given artifact states.
func NewFakeWorld(init map[string]State) *FakeWorld {
	w := &FakeWorld{m: map[string]State{}}
	maps.Copy(w.m, init)
	return w
}

// Get returns the state of id.
func (w *FakeWorld) Get(id string) State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.m[id]
}

// Set sets the state of id.
func (w *FakeWorld) Set(id string, s State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.m[id] = s
}

// DetectFor returns a DetectF reporting the world's state of ids.
func (w *FakeWorld) DetectFor(ids ...string) func(int, *RunState) Detection {
	return func(int, *RunState) Detection {
		d := Detection{State: StateOK}
		for _, id := range ids {
			s := w.Get(id)
			d.Artifacts = append(d.Artifacts, ArtifactState{ID: id, State: s, Detail: string(s)})
			d.State = worseState(d.State, s)
		}
		return d
	}
}

// ApplyOK returns an ApplyF that marks every planned artifact ok.
func (w *FakeWorld) ApplyOK() func(int, WritePorts, *RunState, Plan) (StepResult, error) {
	return func(_ int, _ WritePorts, _ *RunState, p Plan) (StepResult, error) {
		var res StepResult
		for _, a := range p.Actions {
			w.Set(a.Artifact, StateOK)
			res.Artifacts = append(res.Artifacts, Artifact{Step: strings.SplitN(a.Artifact, "/", 2)[0], Kind: KindFile, Path: "/x/" + a.Artifact, Version: "v1.0.0"})
		}
		return res, nil
	}
}

func worseState(a, b State) State {
	rank := map[State]int{StateOK: 0, StateOutdated: 1, StateModified: 2, StateAbsent: 3, StateBlocked: 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// FakePrompter is a strict, scripted Prompter: every call must match the next
// expectation (kind and a substring of the question), or the test fails. An
// answer of -1 for a Select accepts the default. Unconsumed expectations
// fail at cleanup.
type FakePrompter struct {
	t           testing.TB
	interactive bool
	mu          sync.Mutex
	script      []promptExpect
	calls       []string
}

type promptExpect struct {
	kind, q string
	idx     int
	yes     bool
	text    string
	err     error // returned instead of an answer (e.g. ErrInterrupted)
}

// NewFakePrompter returns a prompter bound to t; with interactive false any
// prompt is a test failure.
func NewFakePrompter(t testing.TB, interactive bool) *FakePrompter {
	t.Helper()
	p := &FakePrompter{t: t, interactive: interactive}
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.script) > 0 {
			t.Errorf("FakePrompter: %d expected prompt(s) never asked, first: %s %q", len(p.script), p.script[0].kind, p.script[0].q)
		}
	})
	return p
}

// ExpectSelect scripts a Select whose question contains q, answered idx.
func (p *FakePrompter) ExpectSelect(q string, idx int) *FakePrompter {
	p.script = append(p.script, promptExpect{kind: "select", q: q, idx: idx})
	return p
}

// ExpectConfirm scripts a Confirm whose question contains q.
func (p *FakePrompter) ExpectConfirm(q string, yes bool) *FakePrompter {
	p.script = append(p.script, promptExpect{kind: "confirm", q: q, yes: yes})
	return p
}

// ExpectSelectErr scripts a Select whose question contains q, failing with err.
func (p *FakePrompter) ExpectSelectErr(q string, err error) *FakePrompter {
	p.script = append(p.script, promptExpect{kind: "select", q: q, err: err})
	return p
}

// ExpectConfirmErr scripts a Confirm whose question contains q, failing with err.
func (p *FakePrompter) ExpectConfirmErr(q string, err error) *FakePrompter {
	p.script = append(p.script, promptExpect{kind: "confirm", q: q, err: err})
	return p
}

// Calls returns the asked prompts as "<kind> <question>".
func (p *FakePrompter) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

func (p *FakePrompter) next(kind, q string) (promptExpect, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, kind+" "+q)
	if !p.interactive {
		p.t.Errorf("FakePrompter: %s %q asked in a non-interactive session", kind, q)
		return promptExpect{}, false
	}
	if len(p.script) == 0 {
		p.t.Errorf("FakePrompter: unexpected %s %q", kind, q)
		return promptExpect{}, false
	}
	e := p.script[0]
	p.script = p.script[1:]
	if e.kind != kind || !strings.Contains(q, e.q) {
		p.t.Errorf("FakePrompter: got %s %q, want %s containing %q", kind, q, e.kind, e.q)
		return promptExpect{}, false
	}
	return e, true
}

// Select implements Prompter.
func (p *FakePrompter) Select(q string, opts []string, def int) (int, error) {
	e, ok := p.next("select", q)
	if !ok {
		return 0, ErrInterrupted
	}
	if e.err != nil {
		return 0, e.err
	}
	if e.idx < 0 {
		return def, nil
	}
	if e.idx >= len(opts) {
		p.t.Errorf("FakePrompter: select %q answer %d out of %d options", q, e.idx, len(opts))
		return 0, ErrInterrupted
	}
	return e.idx, nil
}

// Confirm implements Prompter.
func (p *FakePrompter) Confirm(q string, def bool) (bool, error) {
	e, ok := p.next("confirm", q)
	if !ok {
		return false, ErrInterrupted
	}
	if e.err != nil {
		return false, e.err
	}
	return e.yes, nil
}

// Text implements Prompter (never scripted in the engine tests).
func (p *FakePrompter) Text(q, def string, _ func(string) error) (string, error) {
	p.next("text", q)
	return def, ErrInterrupted
}

// Secret implements Prompter (never scripted in the engine tests).
func (p *FakePrompter) Secret(q string) (string, error) {
	p.next("secret", q)
	return "", ErrInterrupted
}

// Interactive implements Prompter.
func (p *FakePrompter) Interactive() bool { return p.interactive }

var _ Prompter = (*FakePrompter)(nil)

// FakeReporter records everything the engine reports.
type FakeReporter struct {
	Events   []string
	Notes_   []Note
	Tables   [][]StatusRow
	Plans    []CombinedPlan
	Awaits   map[string][]string
	Outcomes []StepOutcome
}

var _ Reporter = (*FakeReporter)(nil)

// Notes implements Reporter.
func (r *FakeReporter) Notes(ns []Note) {
	r.Events = append(r.Events, "notes")
	r.Notes_ = append(r.Notes_, ns...)
}

// Table implements Reporter.
func (r *FakeReporter) Table(rows []StatusRow) {
	r.Events = append(r.Events, "table")
	r.Tables = append(r.Tables, rows)
}

// Plan implements Reporter.
func (r *FakeReporter) Plan(cp CombinedPlan) {
	r.Events = append(r.Events, "plan")
	r.Plans = append(r.Plans, cp)
}

// Await implements Reporter.
func (r *FakeReporter) Await(id string, ins []string) {
	r.Events = append(r.Events, "await "+id)
	if r.Awaits == nil {
		r.Awaits = map[string][]string{}
	}
	r.Awaits[id] = ins
}

// StepDone implements Reporter.
func (r *FakeReporter) StepDone(o StepOutcome) {
	r.Events = append(r.Events, "done "+o.StepID)
	r.Outcomes = append(r.Outcomes, o)
}
