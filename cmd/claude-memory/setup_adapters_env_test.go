package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/setup"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakecli")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecRunnerDropsEnvWithoutPrintingValues(t *testing.T) {
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "x")
	t.Setenv("KEEP_ME", "y")
	p := writeScript(t, `[ -z "$GITHUB_PERSONAL_ACCESS_TOKEN" ] && echo dropped; [ -n "$KEEP_ME" ] && echo kept`)
	res, err := execRunner{readOnly: true}.Run(context.Background(), setup.Cmd{Argv: []string{p}, DropEnv: []string{"GITHUB_PERSONAL_ACCESS_TOKEN"}})
	if err != nil || !strings.Contains(string(res.Stdout), "dropped") || !strings.Contains(string(res.Stdout), "kept") {
		t.Errorf("res=%q err=%v", res.Stdout, err)
	}
}

// A wrapper script that leaves a grandchild holding the pipes must still
// return shortly after the context deadline (the doctor row then reads
// "could not check", never a framework-timeout fail).
func TestExecRunnerForkingWrapperReturnsAfterDeadline(t *testing.T) {
	p := writeScript(t, "sleep 20 &\nsleep 20\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := execRunner{readOnly: true}.Run(ctx, setup.Cmd{Argv: []string{p}})
	if err == nil {
		t.Error("want a deadline error")
	}
	if el := time.Since(start); el > 2500*time.Millisecond {
		t.Errorf("hung for %s", el)
	}
}
