package cliexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain doubles as the child process: with CLIEXEC_HELPER set the test
// binary behaves as a fake CLI instead of running tests.
func TestMain(m *testing.M) {
	switch os.Getenv("CLIEXEC_HELPER") {
	case "env":
		for _, k := range []string{"GITLAB_TOKEN", "GH_DEBUG", "ADDED", "KEEP"} {
			fmt.Printf("%s=%s\n", k, os.Getenv(k))
		}
		os.Exit(0)
	case "big":
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 40; i++ {
			fmt.Print(chunk)
		}
		os.Exit(0)
	case "fail":
		fmt.Fprint(os.Stderr, "token ghp_SECRET boom "+strings.Repeat("y", 2000))
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func helper(t *testing.T, mode string, r Runner) Runner {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r.Bin = self
	r.Env = append(r.Env, "CLIEXEC_HELPER="+mode)
	return r
}

func TestEnvDroppedAndAdded(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "secret")
	t.Setenv("GH_DEBUG", "api")
	t.Setenv("KEEP", "yes")
	r := helper(t, "env", Runner{Env: []string{"ADDED=1"}, Drop: []string{"GITLAB_TOKEN", "GH_DEBUG"}})
	out, err := r.Run(context.Background(), "", []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Contains(got, "secret") || !strings.Contains(got, "GITLAB_TOKEN=\n") || !strings.Contains(got, "GH_DEBUG=\n") {
		t.Errorf("tokens not stripped:\n%s", got)
	}
	if !strings.Contains(got, "ADDED=1") || !strings.Contains(got, "KEEP=yes") {
		t.Errorf("env not preserved/added:\n%s", got)
	}
}

func TestStdoutCap(t *testing.T) {
	_, err := helper(t, "big", Runner{}).Run(context.Background(), "", []string{"api"})
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("want ErrOutputTooLarge, got %v", err)
	}
}

func TestStderrCappedAndScrubbed(t *testing.T) {
	r := helper(t, "fail", Runner{Scrub: func(s string) string { return strings.ReplaceAll(s, "ghp_SECRET", "[REDACTED]") }})
	_, err := r.Run(context.Background(), "", []string{"api", "x"})
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "ghp_SECRET") || !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("stderr not scrubbed: %s", msg)
	}
	if len(msg) > 900 {
		t.Errorf("stderr not capped, len %d", len(msg))
	}
}

func TestMissingBinary(t *testing.T) {
	_, err := Runner{Bin: "definitely-not-a-real-cli-xyz"}.Run(context.Background(), "", []string{"api"})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("got %v", err)
	}
}
