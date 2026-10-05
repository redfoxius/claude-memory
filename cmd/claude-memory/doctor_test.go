package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"claude-memory/internal/setup"
)

func TestDoctorExit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		sum    setup.DoctorSummary
		strict bool
		want   int
	}{
		{setup.DoctorSummary{Pass: 20, Info: 3}, false, 0},
		{setup.DoctorSummary{Pass: 20, Info: 3}, true, 0},
		{setup.DoctorSummary{Pass: 20, Warn: 2, Info: 1}, false, 0},
		{setup.DoctorSummary{Pass: 20, Warn: 2, Info: 1}, true, 1},
		{setup.DoctorSummary{Pass: 10, Fail: 1, Skip: 12}, false, 1},
		{setup.DoctorSummary{Pass: 10, Fail: 1, Skip: 12}, true, 1},
	}
	for _, tc := range cases {
		err := doctorExit(setup.DoctorReport{Summary: tc.sum}, tc.strict)
		if got := exitCode(err); got != tc.want {
			t.Errorf("%+v strict=%v: exit %d, want %d", tc.sum, tc.strict, got, tc.want)
		}
		if err != nil && !errors.Is(err, errQuiet) {
			t.Errorf("doctor's exit 1 must be quiet (the report says why): %v", err)
		}
	}
}

// buildBinary builds claude-memory into a temp dir (the real binary, not the
// test executable).
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary; skipped under -short")
	}
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	bin := filepath.Join(t.TempDir(), "claude-memory")
	cmd := exec.Command(goBin, "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// markerScript writes an executable shell script at path that creates
// marker when it runs.
func markerScript(t *testing.T, path, marker string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorBinaryNeverRunsMCPCommand is AC-67 (cmd half), with the AC-30
// sentinel: the built binary runs doctor against a HOME whose .claude.json
// registers a marker-creating script, whose hook scripts and settings.json
// entries are marker-creating scripts, with a fake `claude` (and a fake
// `claude-memory`) first on PATH that create markers too. After doctor and
// doctor --json, no marker exists, and the DSN password never appears.
func TestDoctorBinaryNeverRunsMCPCommand(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	markers := filepath.Join(root, "markers")
	if err := os.MkdirAll(markers, 0o755); err != nil {
		t.Fatal(err)
	}
	mark := func(name string) string { return filepath.Join(markers, name) }

	// Registered MCP server command.
	mcpCmd := filepath.Join(root, "registered", "claude-memory-sentinel")
	markerScript(t, mcpCmd, mark("mcp-command"))
	reg := map[string]any{"mcpServers": map[string]any{"claude-memory": map[string]any{
		"type": "stdio", "command": mcpCmd, "args": []string{"serve"}, "env": map[string]string{}}}}
	b, _ := json.Marshal(reg)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	// Hook scripts and settings.json entries.
	hooksDir := filepath.Join(home, ".claude", "hooks", "claude-memory")
	markerScript(t, filepath.Join(hooksDir, "user-prompt-submit.sh"), mark("hook-ups"))
	markerScript(t, filepath.Join(hooksDir, "session-end.sh"), mark("hook-se"))
	settings := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"` + filepath.Join(hooksDir, "user-prompt-submit.sh") +
		`","timeout":5}]}],"SessionEnd":[{"hooks":[{"type":"command","command":"` + filepath.Join(hooksDir, "session-end.sh") + `","timeout":5}]}]}}`
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake claude and claude-memory first on PATH; also the installed path.
	fakeBin := filepath.Join(root, "fakebin")
	markerScript(t, filepath.Join(fakeBin, "claude"), mark("claude-cli"))
	markerScript(t, filepath.Join(fakeBin, "claude-memory"), mark("claude-memory-on-path"))
	markerScript(t, filepath.Join(home, ".local", "bin", "claude-memory"), mark("installed-binary"))

	// Env file with a sentinel password and endpoints that refuse at once.
	const sentinel = "S3ntinel-pw-$@:/x"
	dsn := "postgresql://" + url.UserPassword("claude_memory", sentinel).String() + "@127.0.0.1:1/claude_memory?connect_timeout=2"
	cfgDir := filepath.Join(home, ".config", "claude-memory")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := "MEMORY_PG_DSN=" + dsn + "\nMEMORY_OLLAMA_URL=http://127.0.0.1:1\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"doctor"}, {"doctor", "--json"}, {"doctor", "--strict", "--timeout", "2s"}} {
		cmd := exec.Command(bin, args...)
		cmd.Env = []string{"HOME=" + home, "PATH=" + fakeBin + ":/usr/bin:/bin"}
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			t.Errorf("%q: err %v, want exit 1 (checks fail); output:\n%s", args, err, out)
		}
		for _, form := range []string{sentinel, url.QueryEscape(sentinel), url.PathEscape(sentinel), "S3ntinel-pw-$%40%3A%2Fx"} {
			if strings.Contains(string(out), form) {
				t.Errorf("%q: output contains the password form %q:\n%s", args, form, out)
			}
		}
		if args[len(args)-1] == "--json" {
			var doc struct {
				Schema int  `json:"schema"`
				OK     bool `json:"ok"`
				Checks []struct {
					ID, Status string
				} `json:"checks"`
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("--json output is not one JSON object: %v\n%s", err, out)
			}
			if doc.Schema != 1 || doc.OK || len(doc.Checks) != 25 {
				t.Errorf("--json: schema %d ok %v, %d checks", doc.Schema, doc.OK, len(doc.Checks))
			}
		}
	}

	entries, err := os.ReadDir(markers)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("doctor executed something it must never run: marker %s exists (AC-67)", e.Name())
	}
}
