package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
