package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/setup"
)

func TestExecRunner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := execRunner{}

	res, err := r.Run(ctx, setup.Cmd{Argv: []string{"echo", "hello"}})
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "hello\n" {
		t.Errorf("echo: %+v, %v", res, err)
	}
	res, err = r.Run(ctx, setup.Cmd{Argv: []string{"false"}})
	if err != nil || res.ExitCode != 1 {
		t.Errorf("false: %+v, %v (want exit 1, nil error)", res, err)
	}
	if _, err := r.Run(ctx, setup.Cmd{Argv: []string{"claude-memory-no-such-program"}}); err == nil {
		t.Error("missing program: want an error")
	}
	if _, err := r.Run(ctx, setup.Cmd{}); err == nil {
		t.Error("empty argv: want an error")
	}
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := r.Run(cctx, setup.Cmd{Argv: []string{"sleep", "5"}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: err = %v, want DeadlineExceeded", err)
	}

	ro := execRunner{readOnly: true}
	if _, err := ro.Run(ctx, setup.Cmd{Argv: []string{"echo", "x"}, Mutating: true}); !errors.Is(err, setup.ErrReadOnly) {
		t.Errorf("read-only mutating: err = %v, want ErrReadOnly", err)
	}
	if res, err := ro.Run(ctx, setup.Cmd{Argv: []string{"echo", "x"}}); err != nil || res.ExitCode != 0 {
		t.Errorf("read-only non-mutating: %+v, %v", res, err)
	}
}

func TestReadOnlyFS(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var f setup.FS = readOnlyFS{}
	if b, err := f.ReadFile(p); err != nil || string(b) != "x" {
		t.Errorf("ReadFile: %q, %v", b, err)
	}
	if !f.Writable(dir) {
		t.Error("temp dir not writable per access(2)")
	}
	if f.Writable(filepath.Join(dir, "missing")) {
		t.Error("missing path reported writable")
	}
	for name, err := range map[string]error{
		"WriteFileAtomic": f.WriteFileAtomic(p, []byte("y"), 0o600),
		"MkdirAll":        f.MkdirAll(filepath.Join(dir, "d"), 0o700),
		"Remove":          f.Remove(p),
		"Chmod":           f.Chmod(p, 0o644),
	} {
		if !errors.Is(err, setup.ErrDryRun) {
			t.Errorf("%s: err = %v, want ErrDryRun", name, err)
		}
	}
	if _, err := f.Lock(filepath.Join(dir, "lock")); !errors.Is(err, setup.ErrDryRun) {
		t.Errorf("Lock: err = %v, want ErrDryRun", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "x" {
		t.Errorf("file changed: %q", b)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode changed: %#o", info.Mode().Perm())
	}
}

func TestWritableFSWriteKeepsModeAndReplaces(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	f := newWritableFS()
	// A 0o644 request must survive the process umask (CreateTemp makes 0600).
	if err := f.WriteFileAtomic(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFileAtomic(p, []byte("two"), 0o640); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Errorf("content = %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("leftover temp files: %v", ents)
	}
}

func TestWritableFSRenameFaultLeavesOriginal(t *testing.T) { // AC-9
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("injected rename failure")
	f := writableFS{rename: func(string, string) error { return boom }}
	if err := f.WriteFileAtomic(p, []byte("new"), 0o600); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want injected failure", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "original" {
		t.Errorf("original changed: %q", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("temp file left behind: %v", ents)
	}
}

func TestWritableFSMkdirRemoveChmod(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := newWritableFS()
	d := filepath.Join(dir, "a", "b")
	if err := f.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod(d, 0o750); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o750 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if err := f.Remove(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Errorf("dir still there: %v", err)
	}
}

func TestWritableFSLock(t *testing.T) { // AC-9
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg", "install.lock") // parent does not exist yet
	f := newWritableFS()
	unlock, err := f.Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open file description, so a second Lock in this process
	// conflicts like another process would.
	if _, err := f.Lock(p); err == nil {
		t.Fatal("second Lock on the same path succeeded")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Errorf("second unlock: %v (want idempotent)", err)
	}
	unlock2, err := f.Lock(p)
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	_ = unlock2()
}

// stubDB / stubOllama are non-nil probers for the builder test; none of their
// methods is ever called.
type stubDB struct{ setup.DBProber }
type stubOllama struct{ setup.OllamaProber }

func TestPortBuilders(t *testing.T) {
	t.Parallel()
	d := setupDeps{FS: readOnlyFS{}, Runner: execRunner{readOnly: true}, DB: stubDB{}, Ollama: stubOllama{}}
	dir := t.TempDir()
	p := filepath.Join(dir, "f")

	ro := readOnlyPorts(d)
	if ro.Jobs != nil {
		t.Error("ReadPorts.Jobs must be nil in 2a")
	}
	if _, ok := ro.FS.(setup.FS); ok {
		// readOnlyFS carries write methods, but they all refuse; the
		// compile-time narrowing is what the reflection test pins.
		if err := ro.FS.(setup.FS).WriteFileAtomic(p, nil, 0o600); !errors.Is(err, setup.ErrDryRun) {
			t.Errorf("read-only ports FS write: %v", err)
		}
	}

	wp := writablePorts(d, false)
	if wp.DB == nil || wp.Ollama == nil {
		t.Error("WritePorts.DB/Ollama must be set")
	}
	// Same adapter: compared by type (writableFS holds a func, so == would panic).
	if fmt.Sprintf("%T", wp.ReadPorts.FS) != fmt.Sprintf("%T", wp.FS) {
		t.Errorf("WritePorts.ReadPorts.FS is %T, want the same adapter as FS (%T)", wp.ReadPorts.FS, wp.FS)
	}
	if fmt.Sprintf("%T/%v", wp.ReadPorts.Runner, wp.ReadPorts.Runner) != fmt.Sprintf("%T/%v", wp.Runner, wp.Runner) {
		t.Errorf("WritePorts.ReadPorts.Runner is %v, want the same adapter as Runner (%v)", wp.ReadPorts.Runner, wp.Runner)
	}
	if wp.Jobs != nil {
		t.Error("WritePorts.Jobs must stay nil until the jobs step (WI-S2-13a)")
	}
	if wp.ClaudeCLI == nil {
		t.Error("WritePorts.ClaudeCLI must be set for the mcp step")
	}
	if err := wp.FS.WriteFileAtomic(p, []byte("x"), 0o600); err != nil {
		t.Errorf("writable ports write: %v", err)
	}
	if _, err := wp.Runner.Run(context.Background(), setup.Cmd{Argv: []string{"true"}, Mutating: true}); err != nil {
		t.Errorf("writable runner refused mutating: %v", err)
	}

	dry := writablePorts(d, true)
	if err := dry.FS.WriteFileAtomic(filepath.Join(dir, "g"), []byte("x"), 0o600); !errors.Is(err, setup.ErrDryRun) {
		t.Errorf("dry-run write: %v, want ErrDryRun", err)
	}
	if _, err := dry.Runner.Run(context.Background(), setup.Cmd{Argv: []string{"true"}, Mutating: true}); !errors.Is(err, setup.ErrReadOnly) {
		t.Errorf("dry-run mutating: %v, want ErrReadOnly", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "g")); !os.IsNotExist(err) {
		t.Error("dry-run wrote a file")
	}
}

// stubRunner records the commands the claudeCLI adapter issues.
type stubRunner struct {
	cmds []setup.Cmd
	res  setup.Result
	err  error
}

func (s *stubRunner) Run(_ context.Context, c setup.Cmd) (setup.Result, error) {
	s.cmds = append(s.cmds, c)
	return s.res, s.err
}
func (*stubRunner) LookPath(string) (string, error) { return "/usr/bin/claude", nil }

// WI-S2-10: the adapter builds the exact `claude mcp` argv, marks it mutating
// (so the read-only Runner refuses it), has no read side, and reports a
// failure with the CLI's own message.
func TestClaudeCLIAdapter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := &stubRunner{}
	c := claudeCLI{runner: s}
	if err := c.MCPAdd(ctx, "claude-memory", []string{"/b/claude-memory", "serve"}); err != nil {
		t.Fatal(err)
	}
	if err := c.MCPRemove(ctx, "claude-memory"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"claude", "mcp", "add", "--scope", "user", "claude-memory", "--", "/b/claude-memory", "serve"},
		{"claude", "mcp", "remove", "--scope", "user", "claude-memory"},
	}
	for i, w := range want {
		if fmt.Sprint(s.cmds[i].Argv) != fmt.Sprint(w) || !s.cmds[i].Mutating {
			t.Errorf("cmd %d = %v mutating=%v, want %v mutating", i, s.cmds[i].Argv, s.cmds[i].Mutating, w)
		}
	}
	for _, cmd := range s.cmds {
		for _, a := range cmd.Argv {
			if a == "-e" || a == "get" || a == "list" {
				t.Errorf("forbidden argument %q (AC-40, AC-67)", a)
			}
		}
	}

	s = &stubRunner{res: setup.Result{ExitCode: 1, Stderr: []byte("  boom: already exists\n")}}
	if err := (claudeCLI{runner: s}).MCPAdd(ctx, "x", []string{"/b", "serve"}); err == nil || !strings.Contains(err.Error(), "exit 1: boom: already exists") {
		t.Errorf("error = %v", err)
	}
	ro := claudeCLI{runner: execRunner{readOnly: true}}
	if err := ro.MCPAdd(ctx, "x", []string{"/b", "serve"}); !errors.Is(err, setup.ErrReadOnly) {
		t.Errorf("read-only MCPAdd = %v", err)
	}
	if err := ro.MCPRemove(ctx, "x"); !errors.Is(err, setup.ErrReadOnly) {
		t.Errorf("read-only MCPRemove = %v", err)
	}
}
