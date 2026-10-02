package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"claude-memory/internal/setup"
)

// Adapters for the internal/setup ports (spec §10). They are constructed
// only in main.go (the composition root). Slice 1 has the read-only FS
// (doctor) and the exec Runner; slice 2 adds the writable/dry-run FS with
// Lock, the TTY prompter, the claude CLI and the namespaces store.

// setupDeps is what main.go hands to the doctor (and, in slice 2, install
// and uninstall) commands: the values and the constructed adapters.
type setupDeps struct {
	Paths    setup.Paths
	Env      setup.Env
	Platform setup.PlatformInfo
	FS       setup.FS
	Runner   setup.Runner
	Clock    setup.Clock
	Redactor *setup.Redactor
	Stdout   io.Writer // redacting (AC-30)
	Stderr   io.Writer // redacting (AC-30)
}

// ---- FS -------------------------------------------------------------------

// readOnlyFS is the doctor's FS: the read half of setup.FS over the real
// filesystem; every write method returns setup.ErrDryRun (AC-57).
type readOnlyFS struct{}

var _ setup.FS = readOnlyFS{}

func (readOnlyFS) ReadFile(p string) ([]byte, error)       { return os.ReadFile(p) }
func (readOnlyFS) Stat(p string) (fs.FileInfo, error)      { return os.Stat(p) }
func (readOnlyFS) Lstat(p string) (fs.FileInfo, error)     { return os.Lstat(p) }
func (readOnlyFS) ReadDir(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) }
func (readOnlyFS) EvalSymlinks(p string) (string, error)   { return filepath.EvalSymlinks(p) }

// Writable checks write permission with access(2); it writes nothing.
func (readOnlyFS) Writable(p string) bool { return syscall.Access(p, 0x2 /* W_OK */) == nil }

func (readOnlyFS) WriteFileAtomic(string, []byte, fs.FileMode) error { return setup.ErrDryRun }
func (readOnlyFS) MkdirAll(string, fs.FileMode) error                { return setup.ErrDryRun }
func (readOnlyFS) Remove(string) error                               { return setup.ErrDryRun }
func (readOnlyFS) Chmod(string, fs.FileMode) error                   { return setup.ErrDryRun }
func (readOnlyFS) Lock(string) (func() error, error)                 { return nil, setup.ErrDryRun }

// ---- Runner ---------------------------------------------------------------

// execRunner runs argv commands with os/exec, never through a shell. With
// readOnly set it refuses every Cmd marked Mutating (doctor, Detect,
// --dry-run).
type execRunner struct {
	readOnly bool
}

var _ setup.Runner = execRunner{}

// maxCmdOutput bounds the captured stdout/stderr of one command.
const maxCmdOutput = 1 << 20

func (r execRunner) Run(ctx context.Context, c setup.Cmd) (setup.Result, error) {
	if len(c.Argv) == 0 {
		return setup.Result{}, errors.New("run: empty argv")
	}
	if r.readOnly && c.Mutating {
		return setup.Result{}, setup.ErrReadOnly
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stdout, stderr limitedBuffer
	stdout.max, stderr.max = maxCmdOutput, maxCmdOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A child that leaves a grandchild holding the pipes must not hang us
	// past the context.
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	res := setup.Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && ctx.Err() == nil {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, err
	}
	return res, nil
}

func (execRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// limitedBuffer keeps at most max bytes and silently drops the rest.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// ---- Clock ----------------------------------------------------------------

// systemClock (main.go) also implements setup.Clock.
var _ setup.Clock = (*systemClock)(nil)
