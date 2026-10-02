package setup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Engine-level tests of the Claude integration steps (WI-S2-9, WI-S2-10) over
// the full 2a+2b rig: hooks.scripts, hooks.settings and mcp.

// A converging first run wires everything, records what uninstall will need
// (Design 23) and leaves a green final doctor.
func TestFullRunClaudeIntegration(t *testing.T) {
	r := newFullRig(t)
	res := r.run(r.inputs())
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	for _, id := range []string{"hooks.scripts", "hooks.settings", "mcp"} {
		if o := outcomeOf(res, id); o != OutcomeApplied {
			t.Errorf("%s: %q, want applied", id, o)
		}
	}
	bin := r.p.InstalledBinary()
	if got := r.claude.Calls(); !slices.Equal(got, []string{"add claude-memory -- " + bin + " serve"}) {
		t.Errorf("claude calls %v", got)
	}
	for _, c := range r.runner.Calls() {
		if slices.Contains(c.Argv, "get") || slices.Contains(c.Argv, "list") || (len(c.Argv) > 0 && c.Argv[0] == "claude") {
			t.Errorf("unexpected command through the runner: %v", c.Argv)
		}
	}
	for _, name := range []string{"user-prompt-submit.sh", "session-end.sh"} {
		info, err := os.Stat(filepath.Join(r.p.HookScriptsDir(), name))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("%s: %v %v", name, info, err)
		}
	}
	man, err := LoadManifest(r.fs, r.p)
	if err != nil || !man.Present() {
		t.Fatalf("manifest: %+v %v", man, err)
	}
	m := man.Manifest
	if d, ok := m.Lookup(KindDir, r.p.HookScriptsDir(), ""); !ok || d.Step != "hooks.scripts" {
		t.Errorf("owned dir artifact missing: %+v", m.Artifacts)
	}
	for _, name := range []string{"user-prompt-submit.sh", "session-end.sh"} {
		a, ok := m.Lookup(KindFile, filepath.Join(r.p.HookScriptsDir(), name), "")
		if !ok || a.SHA256 == "" || a.Step != "hooks.scripts" {
			t.Errorf("script artifact %s: %+v", name, a)
		}
	}
	rec := m.RecordedHooks(r.p.SettingsJSON())
	want := recordedFor(DesiredHooks(r.p.HookScriptsDir()))
	if len(rec) != 2 || rec[EventUserPromptSubmit] != want[EventUserPromptSubmit] || rec[EventSessionEnd] != want[EventSessionEnd] {
		t.Errorf("recorded hooks %v", rec)
	}
	if !m.CreatedHooksKey(r.p.SettingsJSON()) {
		t.Error("CreatedContainer not recorded")
	}
	for _, a := range m.Find(KindSettingsHook) {
		if !a.CreatedFile {
			t.Errorf("CreatedFile not recorded on %+v", a)
		}
	}
	if _, ok := m.Lookup(KindMCP, r.p.ClaudeJSON, MCPServerName); !ok {
		t.Errorf("mcp artifact missing: %+v", m.Artifacts)
	}
	for _, id := range []string{"mcp.registered", "hooks.scripts", "hooks.settings"} {
		if strings.Contains(r.out.String(), "FAIL  "+id) || strings.Contains(r.out.String(), "WARN  "+id) {
			t.Errorf("final doctor flags %s:\n%s", id, r.out)
		}
	}
}

// AC-7: a migration that does not happen blocks hooks.settings (never a wired
// hook against an empty schema), and leaves hooks.scripts and mcp alone.
func TestHooksSettingsBlockedWhenMigrateSkipped(t *testing.T) {
	r := newFullRig(t)
	res := r.run(r.inputs(func(in *Inputs) { in.Skip = []string{"migrate"} }))
	if o := outcomeOf(res, "hooks.settings"); o != OutcomeBlocked {
		t.Errorf("hooks.settings: %q, want blocked\n%s", o, r.out)
	}
	if _, err := os.Stat(r.p.SettingsJSON()); err == nil {
		t.Error("settings.json was written although migrate did not run")
	}
	for _, id := range []string{"hooks.scripts", "mcp"} {
		if o := outcomeOf(res, id); o != OutcomeApplied {
			t.Errorf("%s: %q, want applied", id, o)
		}
	}
	if res.ExitCode != ExitOK {
		t.Errorf("exit %d: a user skip is a soft block\n%s", res.ExitCode, r.out)
	}
}

// AC-37 / AC-52: --yes keeps a legacy $HOME entry (drift note, exit 0, no
// write); the repeated run is a no-op.
func TestYesKeepsLegacyHookEntry(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	legacy := `{
  "hooks": {
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "$HOME/.claude/hooks/claude-memory/user-prompt-submit.sh", "timeout": 5}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "$HOME/.claude/hooks/claude-memory/session-end.sh", "timeout": 5}]}]
  }
}
`
	if err := os.WriteFile(r.p.SettingsJSON(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	writes := r.nonLockWrites()
	res := r.run(r.inputs())
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	if n := r.nonLockWrites() - writes; n != 0 {
		t.Errorf("%d writes under --yes over a modified entry: %v", n, r.fs.Writes()[writes:])
	}
	if b, _ := os.ReadFile(r.p.SettingsJSON()); string(b) != legacy {
		t.Error("the legacy entries were overwritten under --yes")
	}
	drift := false
	for _, o := range res.Outcomes {
		for _, n := range o.Notes {
			if o.StepID == "hooks.settings" && strings.Contains(n.Text, "drift: hooks.settings/UserPromptSubmit") {
				drift = true
			}
		}
	}
	if !drift || !strings.Contains(r.out.String(), "modified  keep") {
		t.Errorf("no drift note / modified row (drift=%v):\n%s", drift, r.out)
	}
}

// A modified hook script is kept under --yes (drift), outdated content is not.
func TestYesKeepsModifiedHookScript(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	p := filepath.Join(r.p.HookScriptsDir(), "session-end.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n# mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writes := r.nonLockWrites()
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	if r.nonLockWrites() != writes {
		t.Errorf("writes over a modified script: %v", r.fs.Writes()[writes:])
	}
	if b, _ := os.ReadFile(p); string(b) != "#!/bin/sh\n# mine\n" {
		t.Error("modified script overwritten under --yes")
	}
}

// An outdated MCP registration (the binary moved) is removed and re-added; the
// dry-run changes nothing and calls nothing.
func TestMCPOutdatedThroughEngine(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	r.claude.path = r.p.ClaudeJSON
	if err := os.WriteFile(r.p.ClaudeJSON, []byte(`{"mcpServers":{"claude-memory":{"type":"stdio","command":"/old/claude-memory","args":["serve"],"env":{}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := len(r.claude.Calls())
	if res := r.run(r.inputs(func(in *Inputs) { in.DryRun = true })); res.ExitCode != ExitOK || len(r.claude.Calls()) != calls {
		t.Fatalf("dry-run: exit %d, claude calls %v", res.ExitCode, r.claude.Calls()[calls:])
	}
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	got := r.claude.Calls()[calls:]
	if len(got) != 2 || got[0] != "remove claude-memory" || !strings.HasPrefix(got[1], "add claude-memory -- ") {
		t.Errorf("claude calls %v", got)
	}
}

// No `claude` on PATH: the mcp step is blocked with the command to run by hand
// (hard under --yes, exit 1) and the rest of the install goes on.
func TestMCPNoClaudeBlockedThroughEngine(t *testing.T) {
	r := newFullRig(t)
	r.runner.paths = map[string]string{"git": "/usr/bin/git", "psql": "/usr/bin/psql", "ollama": "/usr/bin/ollama"}
	res := r.run(r.inputs())
	if o := outcomeOf(res, "mcp"); o != OutcomeBlocked {
		t.Fatalf("mcp: %q\n%s", o, r.out)
	}
	if res.ExitCode != ExitFailed {
		t.Errorf("exit %d, want 1", res.ExitCode)
	}
	if !strings.Contains(r.out.String(), "claude mcp add --scope user claude-memory -- "+r.p.InstalledBinary()+" serve") {
		t.Errorf("the command to run by hand is not printed:\n%s", r.out)
	}
	if o := outcomeOf(res, "hooks.settings"); o != OutcomeApplied {
		t.Errorf("hooks.settings: %q", o)
	}
	if len(r.claude.Calls()) != 0 {
		t.Errorf("claude calls %v", r.claude.Calls())
	}
}
