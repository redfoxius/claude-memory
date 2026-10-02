package setup

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testBin = "/Users/owner/.local/bin/claude-memory"

// Reader tests against the WI-S1-0 .claude.json shapes (AC-40).
func TestReadMCPRegistration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture     string // "" = no file
		state       State
		present     bool
		parseErr    bool
		shadows     []string
		detailHas   string
		detailHasnt string
	}{
		{fixture: "", state: StateAbsent, detailHas: "does not exist"},
		{fixture: "no-mcpservers", state: StateAbsent, detailHas: "no claude-memory server"},
		{fixture: "ok", state: StateOK, present: true},
		{fixture: "no-type-no-env", state: StateOK, present: true},
		{fixture: "other-command", state: StateOutdated, present: true, detailHas: "command /Users/owner/go/bin/claude-memory"},
		{fixture: "other-args", state: StateOutdated, present: true, detailHas: `args ["serve" "--verbose"]`},
		{fixture: "env-set", state: StateOutdated, present: true, detailHas: "env sets MEMORY_PG_DSN", detailHasnt: "S3ntinel"},
		{fixture: "local-shadow", state: StateOK, present: true, shadows: []string{"/Users/owner/acme"}},
		{fixture: "unparseable", state: StateAbsent, parseErr: true, detailHas: "unreadable"},
		{fixture: "mcpservers-not-object", state: StateAbsent, parseErr: true},
		{fixture: "malformed-entry", state: StateOutdated, present: true, detailHas: "expected shape"},
	}
	for _, tc := range cases {
		name := tc.fixture
		if name == "" {
			name = "absent-file"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := testPaths(t)
			fsys := NewFakeFS(t, filepath.Dir(p.Home))
			if tc.fixture != "" {
				writeTestFile(t, p.ClaudeJSON, readTestdata(t, filepath.Join("testdata", "fixtures", "claude-json", tc.fixture+".json")), 0o600)
			}
			reg, err := ReadMCPRegistration(fsys, p)
			if err != nil {
				t.Fatal(err)
			}
			if reg.Present != tc.present || (reg.ParseError != nil) != tc.parseErr || reg.FileExists != (tc.fixture != "") {
				t.Errorf("reg = %+v", reg)
			}
			if !slices.Equal(reg.LocalShadows, tc.shadows) {
				t.Errorf("shadows = %v, want %v", reg.LocalShadows, tc.shadows)
			}
			st, detail := reg.State(testBin)
			if st != tc.state {
				t.Errorf("state = %s (%s), want %s", st, detail, tc.state)
			}
			if tc.detailHas != "" && !strings.Contains(detail, tc.detailHas) {
				t.Errorf("detail %q lacks %q", detail, tc.detailHas)
			}
			if tc.detailHasnt != "" && strings.Contains(detail, tc.detailHasnt) {
				t.Errorf("detail %q leaks %q", detail, tc.detailHasnt)
			}
			if len(fsys.Writes()) != 0 {
				t.Errorf(".claude.json reader wrote: %v", fsys.Writes())
			}
		})
	}
}

// CLAUDE_CONFIG_DIR relocates .claude.json: the reader follows
// Paths.ClaudeJSON, never Home/.claude.json on its own.
func TestReadMCPRegistrationFollowsClaudeJSONPath(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	writeTestFile(t, filepath.Join(p.Home, ".claude.json"), readTestdata(t, "testdata/fixtures/claude-json/ok.json"), 0o600)
	p.ClaudeJSON = filepath.Join(filepath.Dir(p.Home), "cfgdir", ".claude.json")
	reg, err := ReadMCPRegistration(fsys, p)
	if err != nil || reg.FileExists || reg.Present {
		t.Errorf("reg = %+v, %v; want the relocated (missing) file", reg, err)
	}
}

// AC-67 (library half): detection never executes anything. The reader
// takes no Runner at all; this test pins that a sentinel registration plus
// a Runner that fails on the sentinel, `mcp get` and `mcp list` sees zero
// calls when registration is read and its state computed.
func TestMCPDetectionNeverExecutes(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	fsys.ReadOnly = true
	writeTestFile(t, p.ClaudeJSON, readTestdata(t, "testdata/fixtures/claude-json/sentinel.json"), 0o600)
	runner := NewFakeRunner(t).
		Deny(Argv0("ac67-sentinel-must-never-run"), "AC-67: the registered MCP command was executed").
		Deny(func(c Cmd) bool { return len(c.Argv) > 0 && filepath.Base(c.Argv[0]) == "claude" }, "AC-67: claude was executed")
	runner.ReadOnly = true

	reg, err := ReadMCPRegistration(fsys, p)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := reg.State(testBin); st != StateOutdated || reg.Server.Command != "/tmp/ac67-sentinel-must-never-run" {
		t.Errorf("state %s, server %+v", st, reg.Server)
	}
	if n := len(runner.Calls()); n != 0 {
		t.Errorf("runner calls = %d, want 0", n)
	}
	if len(fsys.Writes()) != 0 {
		t.Errorf("writes = %v", fsys.Writes())
	}
}
