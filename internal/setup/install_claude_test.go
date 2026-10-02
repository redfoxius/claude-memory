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

// A1 (AC-39): on a fresh install migrate is applied in the same run, so the
// engine re-plans hooks.settings (Rule B) after the user's confirmation. The
// Token of the confirmed plan must survive that, or a settings.json written
// meanwhile (Claude Code) would be merged over unseen.
func TestHooksSettingsChangeAfterConfirmationAbortsAcrossRuleB(t *testing.T) {
	r := newFullRig(t)
	theirs := "{\"model\": \"opus\"}\n"
	r.tamper = func() {
		if err := os.MkdirAll(r.p.ClaudeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.p.SettingsJSON(), []byte(theirs), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := r.run(r.inputs())
	if o := outcomeOf(res, "migrate"); o != OutcomeApplied {
		t.Fatalf("migrate: %q (Rule B needs it applied in this run)\n%s", o, r.out)
	}
	if o := outcomeOf(res, "hooks.settings"); o != OutcomeFailed || res.ExitCode != ExitFailed {
		t.Fatalf("hooks.settings: %q, exit %d, want failed/1\n%s", o, res.ExitCode, r.out)
	}
	if !strings.Contains(r.out.String(), "changed during install, re-run") {
		t.Errorf("no re-run message:\n%s", r.out)
	}
	if b, _ := os.ReadFile(r.p.SettingsJSON()); string(b) != theirs {
		t.Errorf("the concurrent settings.json was overwritten:\n%s", b)
	}
}

// AC-7: a failing database blocks migrate and hooks.settings; independent
// steps (hooks.scripts) still run.
func TestFailingDatabaseBlocksMigrateAndHookSettings(t *testing.T) {
	r := newFullRig(t)
	r.db.armed.Store(true)
	res := r.run(r.inputs())
	for _, id := range []string{"migrate", "hooks.settings"} {
		if o := outcomeOf(res, id); o != OutcomeBlocked && o != OutcomeFailed {
			t.Errorf("%s: %q, want blocked or failed\n%s", id, o, r.out)
		}
	}
	if o := outcomeOf(res, "hooks.settings"); o != OutcomeBlocked {
		t.Errorf("hooks.settings: %q, want blocked", o)
	}
	if _, err := os.Stat(r.p.SettingsJSON()); err == nil {
		t.Error("settings.json was written")
	}
	if o := outcomeOf(res, "hooks.scripts"); o != OutcomeApplied {
		t.Errorf("hooks.scripts: %q, want applied (independent)", o)
	}
	if res.ExitCode != ExitFailed {
		t.Errorf("exit %d, want 1", res.ExitCode)
	}
}

// F2 / AC-51 / AC-50: a converged install whose manifest is gone (a hand
// install) is adopted on the first run: the hook scripts, entries and MCP
// registration are recorded, nothing of them is rewritten and `claude` is not
// called; the run after that writes nothing and leaves the manifest alone.
func TestAdoptHandInstalledArtifacts(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	if err := os.Remove(r.p.Manifest()); err != nil {
		t.Fatal(err)
	}
	claudeCalls := len(r.claude.Calls())
	mark := len(r.fs.Writes())
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("adopting run: exit %d\n%s", res.ExitCode, r.out)
	}
	for _, w := range r.fs.Writes()[mark:] {
		if strings.Contains(w, "/.claude/") || strings.Contains(w, ".claude.json") {
			t.Errorf("an ok Claude artifact was rewritten: %s", w)
		}
	}
	if len(r.claude.Calls()) != claudeCalls {
		t.Errorf("claude called: %v", r.claude.Calls()[claudeCalls:])
	}
	man, err := LoadManifest(r.fs, r.p)
	if err != nil || !man.Present() {
		t.Fatalf("manifest: %v", err)
	}
	m := man.Manifest
	for _, name := range []string{"user-prompt-submit.sh", "session-end.sh"} {
		if _, ok := m.Lookup(KindFile, filepath.Join(r.p.HookScriptsDir(), name), ""); !ok {
			t.Errorf("script %s not adopted", name)
		}
	}
	if rec := m.RecordedHooks(r.p.SettingsJSON()); len(rec) != 2 {
		t.Errorf("settings entries not adopted: %v", rec)
	}
	if a, ok := m.Lookup(KindMCP, r.p.ClaudeJSON, MCPServerName); !ok || a.Entry != r.p.InstalledBinary()+" serve" {
		t.Errorf("mcp not adopted: %+v", a)
	}
	if _, ok := m.Lookup(KindDir, r.p.HookScriptsDir(), ""); ok {
		t.Error("an adopted install must not claim the directory")
	}

	// AC-50 after adoption.
	before, _ := os.ReadFile(r.p.Manifest())
	writes := r.nonLockWrites()
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("re-run: exit %d", res.ExitCode)
	}
	if n := r.nonLockWrites() - writes; n != 0 {
		t.Errorf("re-run after adoption wrote %d times: %v", n, r.fs.Writes()[writes:])
	}
	if after, _ := os.ReadFile(r.p.Manifest()); string(after) != string(before) {
		t.Error("manifest changed on the re-run after adoption")
	}
}

// Adoption writes nothing on --dry-run.
func TestAdoptNotOnDryRun(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	if err := os.Remove(r.p.Manifest()); err != nil {
		t.Fatal(err)
	}
	writes := r.nonLockWrites()
	if res := r.run(r.inputs(func(in *Inputs) { in.DryRun = true })); res.ExitCode != ExitOK {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if r.nonLockWrites() != writes {
		t.Errorf("dry-run wrote: %v", r.fs.Writes()[writes:])
	}
	if _, err := os.Stat(r.p.Manifest()); err == nil {
		t.Error("dry-run recreated the manifest")
	}
}
