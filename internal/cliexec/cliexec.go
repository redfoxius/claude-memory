// Package cliexec is the one concrete exec adapter behind the Runner ports
// that internal/github and internal/gitlab declare (consumer-declared ports;
// built only in cmd/claude-memory/main.go). It runs a CLI with an argv slice
// (never a shell), a sanitised environment, a capped stdout and a capped,
// scrubbed stderr in errors.
package cliexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	// MaxStdout caps what one call may print; overflow is an error.
	MaxStdout = 32 << 20
	// maxStderr caps the stderr text carried in an error; stderrRead is how
	// much is read before scrubbing, so a secret cut at the cap cannot
	// escape the scrubber's patterns.
	maxStderr  = 512
	stderrRead = 4096
)

// ErrOutputTooLarge is returned when stdout exceeds MaxStdout.
var ErrOutputTooLarge = errors.New("cli output exceeds the 32 MiB cap")

// Runner runs Bin with the given argv.
type Runner struct {
	Bin string
	// Env holds KEY=VALUE entries added to the inherited environment.
	Env []string
	// Drop lists variable names removed from the inherited environment
	// (tokens and debug switches a child must not see or echo).
	Drop []string
	// Scrub, when set, redacts secrets from the stderr text in errors.
	Scrub func(string) string
}

// Run executes `Bin args...` in dir with stdin closed and returns stdout.
// The error carries the CLI name, the subcommand (args[0]), at most 512
// bytes of scrubbed stderr and a remedy keyed by the CLI name only; the
// caller adds the endpoint.
func (r Runner) Run(ctx context.Context, dir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Dir = dir
	cmd.Env = r.environ(os.Environ())

	stdout := &capWriter{max: MaxStdout}
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &limitedBuffer{buf: &stderr, max: stderrRead}

	sub := ""
	if len(args) > 0 {
		sub = " " + args[0]
	}
	if err := cmd.Run(); err != nil {
		if stdout.over {
			return nil, fmt.Errorf("%s%s: %w", r.Bin, sub, ErrOutputTooLarge)
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%s is not installed or not on PATH: %w", r.Bin, err)
		}
		msg := strings.TrimSpace(stderr.String())
		if r.Scrub != nil {
			msg = r.Scrub(msg)
		}
		if len(msg) > maxStderr {
			msg = strings.ToValidUTF8(msg[:maxStderr], "") + "..."
		}
		return nil, fmt.Errorf("%s%s: %w (stderr: %s); try `%s auth login`", r.Bin, sub, err, msg, r.Bin)
	}
	return stdout.buf.Bytes(), nil
}

// environ returns base minus the stripped names, plus r.Env.
func (r Runner) environ(base []string) []string {
	drop := map[string]bool{}
	for _, k := range r.Drop {
		drop[k] = true
	}
	for _, kv := range r.Env {
		drop[strings.SplitN(kv, "=", 2)[0]] = true
	}
	out := make([]string, 0, len(base)+len(r.Env))
	for _, kv := range base {
		if !drop[strings.SplitN(kv, "=", 2)[0]] {
			out = append(out, kv)
		}
	}
	return append(out, r.Env...)
}

// capWriter collects up to max bytes and fails the write beyond it, which
// stops the child (exec closes the pipe on a copy error).
type capWriter struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		w.over = true
		return 0, ErrOutputTooLarge
	}
	return w.buf.Write(p)
}

// limitedBuffer keeps only the first max bytes and swallows the rest.
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.buf.Write(p[:room])
	}
	return len(p), nil
}
