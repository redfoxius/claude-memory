package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/setup"
)

// pipeFile returns the read end of a pipe whose write end is returned too;
// neither is closed unless the test does (a never-closing stdin).
func pipePair(t *testing.T) (r, w *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return r, w
}

func TestTTYPrompterNonTTYNeverReadsStdin(t *testing.T) {
	t.Parallel()
	in, _ := pipePair(t) // empty, never closed
	out, _ := pipePair(t)
	red := setup.NewRedactor()
	called := false
	p := newTTYPrompterWith(context.Background(), in, out, red,
		func(int) bool { return false },
		func(int) ([]byte, error) { called = true; return nil, nil })
	if p.Interactive() {
		t.Fatal("Interactive must be false when fds are not terminals")
	}
	done := make(chan error, 1)
	go func() { _, err := p.Secret("Password"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Secret in a non-TTY session must fail")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Secret blocked on a never-closed stdin pipe (AC-11)")
	}
	if called {
		t.Error("ReadPassword called in a non-TTY session")
	}
}

func TestTTYPrompterInteractiveNeedsBothFDs(t *testing.T) {
	t.Parallel()
	in, _ := pipePair(t)
	out, _ := pipePair(t)
	for _, tc := range []struct {
		name          string
		inTTY, outTTY bool
		want          bool
	}{
		{"both", true, true, true},
		{"stdin only", true, false, false},
		{"stdout only", false, true, false},
	} {
		inFD, outFD := int(in.Fd()), int(out.Fd())
		p := newTTYPrompterWith(context.Background(), in, out, nil, func(fd int) bool {
			if fd == inFD {
				return tc.inTTY
			}
			if fd == outFD {
				return tc.outTTY
			}
			return false
		}, func(int) ([]byte, error) { return nil, nil })
		if p.Interactive() != tc.want {
			t.Errorf("%s: Interactive = %v, want %v", tc.name, p.Interactive(), tc.want)
		}
	}
}

// Contract from the architecture review: the TTY Prompter redacts questions
// and options (a step's Detail reaches the phase-2 Select), and registers a
// password before returning it.
func TestTTYPrompterRedactsAndRegisters(t *testing.T) {
	t.Parallel()
	inR, inW := pipePair(t)
	outR, outW := pipePair(t)
	if _, err := io.WriteString(inW, "1\n"); err != nil {
		t.Fatal(err)
	}
	red := setup.NewRedactor()
	const pw = "Sup3r-secret-pw"
	p := newTTYPrompterWith(context.Background(), inR, outW, red,
		func(int) bool { return true },
		func(int) ([]byte, error) { return []byte(pw), nil })

	dsn := "postgresql://claude_memory:" + pw + "@db.example:5432/claude_memory"
	if _, err := p.Select("database [outdated]: "+dsn, []string{"apply " + dsn, "keep"}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := p.Secret("Password for " + dsn)
	if err != nil || got != pw {
		t.Fatalf("Secret = (%q, %v)", got, err)
	}
	_ = outW.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, outR)
	if strings.Contains(buf.String(), pw) {
		t.Errorf("password leaked into the prompt output:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "claude_memory:***@db.example") {
		t.Errorf("DSN not masked:\n%s", buf.String())
	}
	if red.Redact("x "+pw) != "x ***" {
		t.Error("password not registered with the Redactor")
	}
}

func TestReadStdinSecret(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{"line with newline", "hunter2hunter2\n", "hunter2hunter2", ""},
		{"crlf", "hunter2hunter2\r\n", "hunter2hunter2", ""},
		{"no newline", "hunter2hunter2", "hunter2hunter2", ""},
		{"only the first line is read", "abc\ndef\n", "abc", ""},
		{"empty", "", "", "empty"},
		{"only newline", "\n", "", "empty"},
		{"too long", strings.Repeat("a", 4097), "", "longer than"},
		{"exactly max", strings.Repeat("a", 4096) + "\n", strings.Repeat("a", 4096), ""},
	}
	for _, tc := range cases {
		red := setup.NewRedactor()
		got, err := readStdinSecret(context.Background(), strings.NewReader(tc.in), red, time.Second)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: (%q, %v), want %q", tc.name, len(got), err, len(tc.want))
		}
		if len(got) >= setup.MinSecretLen && red.Redact(got) != setup.Mask {
			t.Errorf("%s: secret not registered", tc.name)
		}
	}
}

func TestReadStdinSecretTimesOutOnNeverClosedPipe(t *testing.T) {
	t.Parallel()
	r, w := pipePair(t)
	if _, err := io.WriteString(w, "no-newline-no-eof"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := readStdinSecret(context.Background(), r, nil, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "within") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestReadStdinSecretContextCancel(t *testing.T) {
	t.Parallel()
	r, _ := pipePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readStdinSecret(ctx, r, nil, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A Ctrl-C (ctx cancel) while the password read blocks returns at once with
// ErrInterrupted instead of hanging.
func TestTTYPrompterSecretCancel(t *testing.T) {
	t.Parallel()
	in, _ := pipePair(t)
	out, _ := pipePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	p := newTTYPrompterWith(ctx, in, out, nil, func(int) bool { return true },
		func(int) ([]byte, error) { <-block; return nil, nil })
	done := make(chan error, 1)
	go func() { _, err := p.Secret("Password"); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, setup.ErrInterrupted) {
			t.Errorf("err = %v, want ErrInterrupted", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Secret hung after the context was cancelled")
	}
}

func TestReadStdinSecretErrorsNeverEchoInput(t *testing.T) {
	t.Parallel()
	for _, in := range []string{strings.Repeat("S3cret", 1000), "\n"} {
		_, err := readStdinSecret(context.Background(), strings.NewReader(in), nil, time.Second)
		if err == nil || strings.Contains(err.Error(), "S3cret") {
			t.Errorf("err = %v", err)
		}
	}
}

func TestRefuseTerminalStdin(t *testing.T) {
	t.Parallel()
	if refuseTerminalStdin(true) == nil || refuseTerminalStdin(false) != nil {
		t.Error("a terminal stdin must be refused, a pipe accepted")
	}
}
