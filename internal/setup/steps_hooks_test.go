package setup

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redfoxius/claude-memory/integration"
)

// Tests for WI-S2-9: hooks.scripts and hooks.settings (AC-36..AC-39, AC-51,
// AC-64, AC-70). FakeFS over a temp root; nothing touches the real HOME.

type hk struct{ *s23 }

func newHK(t *testing.T) *hk { return &hk{newS23(t)} }

func (h *hk) rp() ReadPorts {
	r := h.s23.rp()
	r.Assets = integration.FS
	return r
}

func (h *hk) wp() WritePorts {
	return WritePorts{ReadPorts: h.rp(), FS: h.fs, Runner: h.runner}
}

func (h *hk) state(m *Manifest) *RunState {
	st := NewRunState(Inputs{})
	st.BinPath.Set(h.p.InstalledBinary(), SourceDefault)
	st.Prior.Manifest = m
	return st
}

type hkRun struct {
	det  Detection
	plan Plan
	res  StepResult
	ran  bool // Apply was called
	err  error
}

// run is the engine's Detect -> Plan -> Apply for one step. choose maps an
// artifact id to a choice that replaces the AC-6 default (an overwrite
// Confirm answered yes).
func (h *hk) run(step Step, st *RunState, choose map[string]Choice) hkRun {
	h.t.Helper()
	ctx := context.Background()
	var r hkRun
	r.det = step.Detect(ctx, h.rp(), st)
	if r.det.State == StateBlocked {
		return r
	}
	ch := Choices{}
	for _, a := range r.det.Artifacts {
		ch[a.ID] = DefaultChoice(a.State)
		if c, ok := choose[a.ID]; ok {
			ch[a.ID] = c
		}
	}
	var err error
	if r.plan, err = step.Plan(ctx, h.rp(), st, ch); err != nil {
		r.err = err
		return r
	}
	if hasApply(ch) {
		r.ran = true
		r.res, r.err = step.Apply(ctx, h.wp(), st, r.plan)
	}
	return r
}

// manifestOf folds step results into a manifest like the engine's persist.
func manifestOf(res ...StepResult) *Manifest {
	m := &Manifest{}
	for _, r := range res {
		for _, a := range r.Artifacts {
			m.Upsert(a)
		}
	}
	return m
}

func (h *hk) assertNoWrites(before int) {
	h.t.Helper()
	if w := h.fs.Writes(); len(w) != before {
		h.t.Errorf("unexpected writes: %v", w[before:])
	}
}

func applyAll(h *hk, ids ...string) map[string]Choice {
	out := map[string]Choice{}
	for _, id := range ids {
		out[id] = ChoiceApply
	}
	return out
}

// ---- hooks.scripts ------------------------------------------------------------

func renderedScript(t *testing.T, name, bin string) []byte {
	t.Helper()
	raw, err := fs.ReadFile(integration.FS, "hooks/"+name)
	if err != nil {
		t.Fatal(err)
	}
	b, err := RenderHookScript(raw, bin)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHookScriptsFreshAndRerun(t *testing.T) {
	h := newHK(t)
	step := HooksScriptsStep{Version: "v1.0.0"}
	st := h.state(nil)

	r := h.run(step, st, nil)
	if r.err != nil || r.det.State != StateAbsent || len(r.res.Artifacts) != 3 {
		t.Fatalf("fresh: det %s err %v artifacts %+v", r.det.State, r.err, r.res.Artifacts)
	}
	var dirs, files int
	for _, a := range r.res.Artifacts {
		switch a.Kind {
		case KindDir:
			dirs++
			if a.Path != h.p.HookScriptsDir() {
				t.Errorf("dir artifact %s", a.Path)
			}
		case KindFile:
			files++
			want := sha256Hex(renderedScript(t, filepath.Base(a.Path), h.p.InstalledBinary()))
			if a.SHA256 != want || a.Step != "hooks.scripts" {
				t.Errorf("file artifact %+v, want hash %s", a, want)
			}
		}
	}
	if dirs != 1 || files != 2 {
		t.Errorf("artifacts: %d dirs, %d files", dirs, files)
	}
	for _, name := range []string{HookScriptUserPromptSubmit, HookScriptSessionEnd} {
		p := filepath.Join(h.p.HookScriptsDir(), name)
		b, _ := os.ReadFile(p)
		if !bytes.Equal(b, renderedScript(t, name, h.p.InstalledBinary())) {
			t.Errorf("%s is not the rendered script", name)
		}
		if info, _ := os.Stat(p); info.Mode().Perm() != 0o755 {
			t.Errorf("%s mode %v, want 0755", name, info.Mode().Perm())
		}
	}

	// Run twice (AC-64, AC-50): ok, no apply, no write.
	n := len(h.fs.Writes())
	st2 := h.state(manifestOf(r.res))
	r2 := h.run(step, st2, nil)
	if r2.det.State != StateOK || r2.ran {
		t.Errorf("second run: %s ran=%v", r2.det.State, r2.ran)
	}
	h.assertNoWrites(n)
}

// AC-36 / AC-51: rendered => ok, raw embedded => outdated, recorded older =>
// outdated, anything else => modified; a wrong mode is outdated.
func TestHookScriptsStates(t *testing.T) {
	name := HookScriptSessionEnd
	raw, _ := fs.ReadFile(integration.FS, "hooks/"+name)
	cases := []struct {
		name     string
		content  func(h *hk) []byte
		mode     fs.FileMode
		recorded func(h *hk) string
		want     State
	}{
		{"rendered, unrecorded", func(h *hk) []byte { return renderedScript(t, name, h.p.InstalledBinary()) }, 0o755, nil, StateOK},
		{"raw embedded (manual install)", func(*hk) []byte { return raw }, 0o755, nil, StateOutdated},
		{"recorded older version", func(*hk) []byte { return []byte("#!/bin/sh\n# v0\n") }, 0o755,
			func(*hk) string { return sha256Hex([]byte("#!/bin/sh\n# v0\n")) }, StateOutdated},
		{"edited since install", func(*hk) []byte { return []byte("#!/bin/sh\n# mine\n") }, 0o755,
			func(*hk) string { return sha256Hex([]byte("#!/bin/sh\n# v0\n")) }, StateModified},
		{"unrecorded other content", func(*hk) []byte { return []byte("#!/bin/sh\n") }, 0o755, nil, StateModified},
		{"rendered but not executable", func(h *hk) []byte { return renderedScript(t, name, h.p.InstalledBinary()) }, 0o644, nil, StateOutdated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHK(t)
			p := filepath.Join(h.p.HookScriptsDir(), name)
			h.write(p, string(tc.content(h)), tc.mode)
			var m *Manifest
			if tc.recorded != nil {
				m = &Manifest{Artifacts: []Artifact{{Step: "hooks.scripts", Kind: KindFile, Path: p, SHA256: tc.recorded(h)}}}
			}
			det := HooksScriptsStep{}.Detect(context.Background(), h.rp(), h.state(m))
			var got State
			for _, a := range det.Artifacts {
				if a.ID == "hooks.scripts/"+name {
					got = a.State
				}
			}
			if got != tc.want {
				t.Errorf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

// An outdated script is replaced without a Confirm and without a backup; a
// modified one is kept by default, and replaced after a backup when the user
// confirmed. A kept script is not recorded.
func TestHookScriptsOutdatedModifiedApply(t *testing.T) {
	h := newHK(t)
	step := HooksScriptsStep{Version: "v1.0.0"}
	rawUPS, _ := fs.ReadFile(integration.FS, "hooks/"+HookScriptUserPromptSubmit)
	upsPath := filepath.Join(h.p.HookScriptsDir(), HookScriptUserPromptSubmit)
	endPath := filepath.Join(h.p.HookScriptsDir(), HookScriptSessionEnd)
	h.write(upsPath, string(rawUPS), 0o755) // manual install of this version
	h.write(endPath, "#!/bin/sh\n# mine\n", 0o700)

	r := h.run(step, h.state(nil), nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if b, _ := os.ReadFile(upsPath); !bytes.Equal(b, renderedScript(t, HookScriptUserPromptSubmit, h.p.InstalledBinary())) {
		t.Error("the raw script was not replaced by the rendered one")
	}
	if b, _ := os.ReadFile(endPath); string(b) != "#!/bin/sh\n# mine\n" {
		t.Error("a modified script was overwritten without a confirm")
	}
	if len(r.res.Artifacts) != 1 || r.res.Artifacts[0].Path != upsPath {
		t.Errorf("only the replaced script is recorded: %+v", r.res.Artifacts)
	}
	if baks, _ := filepath.Glob(upsPath + ".bak.*"); len(baks) != 0 {
		t.Errorf("an outdated script needs no backup: %v", baks)
	}

	// Confirmed overwrite of the modified one: backup, mode 0755.
	r = h.run(step, h.state(manifestOf(r.res)), applyAll(h, "hooks.scripts/"+HookScriptSessionEnd))
	if r.err != nil {
		t.Fatal(r.err)
	}
	baks, _ := filepath.Glob(endPath + ".bak.claude-memory.*")
	if len(baks) != 1 {
		t.Fatalf("backups of the modified script: %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != "#!/bin/sh\n# mine\n" {
		t.Error("the backup does not hold the user's script")
	}
	if info, _ := os.Stat(endPath); info.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want 0755", info.Mode().Perm())
	}
	if r.det.Artifacts[1].State != StateModified {
		t.Errorf("detect state %s", r.det.Artifacts[1].State)
	}
	// Not executable but rendered: chmod via rewrite, no backup, no confirm.
	h.write(upsPath, string(renderedScript(t, HookScriptUserPromptSubmit, h.p.InstalledBinary())), 0o644)
	r = h.run(step, h.state(nil), nil)
	if info, _ := os.Stat(upsPath); r.err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("mode not fixed: %v %v", info.Mode().Perm(), r.err)
	}
}

// The dir artifact is recorded only when this step created the directory.
func TestHookScriptsDirOnlyWhenCreated(t *testing.T) {
	h := newHK(t)
	if err := os.MkdirAll(h.p.HookScriptsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	r := h.run(HooksScriptsStep{Version: "v1"}, h.state(nil), nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, a := range r.res.Artifacts {
		if a.Kind == KindDir {
			t.Errorf("a pre-existing directory was recorded as ours: %+v", a)
		}
	}
	if len(r.res.Artifacts) != 2 {
		t.Errorf("artifacts %+v", r.res.Artifacts)
	}
}

// A binary path that cannot be rendered safely blocks the step.
func TestHookScriptsUnsafeBinPathBlocked(t *testing.T) {
	h := newHK(t)
	st := h.state(nil)
	st.BinPath.Set("/opt/we$ird/claude-memory", SourceFlag)
	det := HooksScriptsStep{}.Detect(context.Background(), h.rp(), st)
	if det.State != StateBlocked || !strings.Contains(det.Detail, "unsafe") {
		t.Errorf("detect = %s (%s)", det.State, det.Detail)
	}
}

// Detect and Plan write nothing (read-only ports).
func TestHookStepsDetectPlanDoNotWrite(t *testing.T) {
	h := newHK(t)
	n := len(h.fs.Writes())
	for _, step := range []Step{HooksScriptsStep{}, HooksSettingsStep{}} {
		st := h.state(nil)
		det := step.Detect(context.Background(), h.rp(), st)
		ch := Choices{}
		for _, a := range det.Artifacts {
			ch[a.ID] = ChoiceApply
		}
		if _, err := step.Plan(context.Background(), h.rp(), st, ch); err != nil {
			t.Fatal(err)
		}
	}
	h.assertNoWrites(n)
}

// ---- hooks.settings -------------------------------------------------------------

const (
	artUPS = "hooks.settings/UserPromptSubmit"
	artEnd = "hooks.settings/SessionEnd"
)

// settingsFixture reads testdata/settings/<name>.<suffix> with the fixture's
// scripts dir swapped for this test's.
func (h *hk) fixture(name, suffix string) []byte {
	h.t.Helper()
	b := readTestdata(h.t, filepath.Join("testdata", "settings", name+suffix))
	if b == nil {
		return nil
	}
	b = bytes.ReplaceAll(b, []byte(testScriptsDir), []byte(h.p.HookScriptsDir()))
	esc := func(s string) []byte { return []byte(strings.ReplaceAll(s, "/", `\/`)) } // JSON-escaped slashes
	return bytes.ReplaceAll(b, esc(testScriptsDir), esc(h.p.HookScriptsDir()))
}

// The step reproduces the library goldens (same inputs, same results) and a
// second run writes nothing and leaves the same state: AC-37, AC-64.
func TestHooksSettingsGoldens(t *testing.T) {
	cases := []struct {
		name      string
		fixture   string // "" = no settings.json
		golden    string
		recorded  string // "", "desired" or "old": entries the manifest recorded
		overwrite bool   // the user confirmed overwriting modified entries
		wantWrite bool
		after     State // aggregate state after the run
	}{
		{"absent file", "", "missing", "", false, true, StateOK},
		{"empty object", "empty-object", "empty-object", "", false, true, StateOK},
		{"no hooks key", "no-hooks-key", "no-hooks-key", "", false, true, StateOK},
		{"other events", "other-events", "other-events", "", false, true, StateOK},
		{"other hooks on the same event", "other-hooks-same-event", "other-hooks-same-event", "", false, true, StateOK},
		{"identical", "ours-identical", "ours-identical", "", false, false, StateOK},
		{"legacy $HOME kept", "ours-legacy-home", "ours-legacy-home", "", false, false, StateModified},
		{"legacy $HOME overwritten", "ours-legacy-home", "ours-legacy-home-overwrite", "", true, true, StateOK},
		{"duplicates kept (AC-70 note only)", "ours-duplicated", "ours-duplicated", "", false, false, StateModified},
		{"duplicates collapsed", "ours-duplicated", "ours-duplicated-overwrite", "", true, true, StateOK},
		{"crlf", "crlf", "crlf", "", false, true, StateOK},
		{"tab indented", "tab-indented", "tab-indented", "", false, true, StateOK},
		{"four spaces", "four-space", "four-space", "", false, true, StateOK},
		{"no trailing newline", "no-trailing-newline", "no-trailing-newline", "", false, true, StateOK},
		{"user-raised timeout kept", "ours-user-timeout", "ours-user-timeout", "desired", false, false, StateModified},
		{"user-raised timeout overwritten", "ours-user-timeout", "ours-user-timeout-overwrite", "desired", true, true, StateOK},
		{"recorded outdated under a matcher", "ours-under-matcher", "ours-under-matcher", "old", false, true, StateOK},
		{"unknown top-level keys order", "unknown-top-level-keys-order", "unknown-top-level-keys-order", "", false, true, StateOK},
		{"unicode escapes unchanged", "unicode-escapes-unchanged-noop", "unicode-escapes-unchanged-noop", "", false, false, StateOK},
		{"unicode escapes merged", "unicode-escapes-merge", "unicode-escapes-merge", "", false, true, StateOK},
		{"big number unchanged", "big-number-unchanged", "big-number-unchanged", "", false, true, StateOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHK(t)
			if tc.fixture != "" {
				h.write(h.p.SettingsJSON(), string(h.fixture(tc.fixture, ".in.json")), 0o600)
			}
			var choose map[string]Choice
			if tc.overwrite {
				choose = applyAll(h, artUPS, artEnd)
			}
			step := HooksSettingsStep{Version: "v1.0.0"}
			n := len(h.fs.Writes())
			var prior *Manifest
			if tc.recorded != "" {
				dir := h.p.HookScriptsDir()
				if tc.recorded == "old" {
					dir = "/opt/old/hooks/claude-memory"
				}
				prior = &Manifest{}
				for _, e := range DesiredHooks(dir) {
					prior.Upsert(Artifact{Step: "hooks.settings", Kind: KindSettingsHook, Path: h.p.SettingsJSON(), Identity: e.Event, Entry: e.Canonical()})
				}
			}
			r := h.run(step, h.state(prior), choose)
			if r.err != nil {
				t.Fatal(r.err)
			}
			wrote := len(h.fs.Writes()) > n
			if wrote != tc.wantWrite {
				t.Errorf("wrote = %v, want %v (%v)", wrote, tc.wantWrite, h.fs.Writes()[n:])
			}
			got, _ := os.ReadFile(h.p.SettingsJSON())
			if want := h.fixture(tc.golden, ".merge.golden.json"); !bytes.Equal(got, want) {
				t.Errorf("settings.json differs from the %s golden\n--- got ---\n%s\n--- want ---\n%s", tc.golden, got, want)
			}
			// one backup per write, none otherwise
			baks, _ := filepath.Glob(h.p.SettingsJSON() + ".bak.claude-memory.*")
			if wantBak := tc.wantWrite && tc.fixture != ""; (len(baks) == 1) != wantBak || len(baks) > 1 {
				t.Errorf("backups %v, want %v", baks, wantBak)
			}

			// Second run: nothing to apply, nothing written, same state.
			n = len(h.fs.Writes())
			if prior == nil {
				prior = &Manifest{}
			}
			for _, a := range r.res.Artifacts {
				prior.Upsert(a)
			}
			r2 := h.run(step, h.state(prior), nil)
			if r2.err != nil || r2.det.State != tc.after || r2.ran {
				t.Errorf("second run: det %s ran %v err %v, want %s and no apply", r2.det.State, r2.ran, r2.err, tc.after)
			}
			h.assertNoWrites(n)
			if again, _ := os.ReadFile(h.p.SettingsJSON()); !bytes.Equal(again, got) {
				t.Error("the second run changed the file")
			}
		})
	}
}

// Design 18 / AC-37: the overwrite is per event; the Created flags and the
// canonical entries are recorded (Design 23).
func TestHooksSettingsPerEventAndRecord(t *testing.T) {
	h := newHK(t)
	h.write(h.p.SettingsJSON(), string(h.fixture("ours-legacy-home", ".in.json")), 0o600)
	step := HooksSettingsStep{Version: "v1.0.0"}
	r := h.run(step, h.state(nil), applyAll(h, artUPS))
	if r.err != nil {
		t.Fatal(r.err)
	}
	a, err := AnalyzeSettings(mustRead(t, h.p.SettingsJSON()), DesiredHooks(h.p.HookScriptsDir()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Events[0].State != StateOK || a.Events[1].State != StateModified {
		t.Errorf("states after overwriting only UserPromptSubmit: %s, %s", a.Events[0].State, a.Events[1].State)
	}
	if len(r.res.Artifacts) != 1 || r.res.Artifacts[0].Identity != EventUserPromptSubmit {
		t.Fatalf("recorded: %+v", r.res.Artifacts)
	}
	rec := r.res.Artifacts[0]
	want := DesiredHooks(h.p.HookScriptsDir())[0].Canonical()
	if rec.Entry != want || rec.Kind != KindSettingsHook || rec.Path != h.p.SettingsJSON() || rec.CreatedFile || rec.CreatedContainer {
		t.Errorf("artifact %+v, want entry %s and no Created flags (the file and the hooks object existed)", rec, want)
	}
	if len(r.res.Notes) == 0 || !strings.Contains(r.res.Notes[0].Text, "backup of the previous settings file") {
		t.Errorf("notes %+v", r.res.Notes)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// CreatedFile / CreatedContainer are recorded when install created the file,
// and survive a later run that adds the other event.
func TestHooksSettingsCreatedFlags(t *testing.T) {
	h := newHK(t)
	step := HooksSettingsStep{Version: "v1.0.0"}
	r := h.run(step, h.state(nil), map[string]Choice{artEnd: ChoiceKeep})
	if r.err != nil || len(r.res.Artifacts) != 1 {
		t.Fatalf("first: %v %+v", r.err, r.res.Artifacts)
	}
	if a := r.res.Artifacts[0]; !a.CreatedFile || !a.CreatedContainer || a.Identity != EventUserPromptSubmit {
		t.Errorf("first artifact %+v, want CreatedFile and CreatedContainer", a)
	}
	// The user keeps editing; a later run adds SessionEnd to the file we made.
	r2 := h.run(step, h.state(manifestOf(r.res)), nil)
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	if len(r2.res.Artifacts) != 2 {
		t.Fatalf("second run records both events: %+v", r2.res.Artifacts)
	}
	for _, a := range r2.res.Artifacts {
		if !a.CreatedFile || !a.CreatedContainer {
			t.Errorf("flags lost on %+v", a)
		}
	}
}

// AC-37: an entry equal to the recorded one (script path moved) is outdated
// and replaced without a confirm; the same entry unrecorded is modified.
func TestHooksSettingsRecordedOutdatedReplaced(t *testing.T) {
	h := newHK(t)
	old := DesiredHooks("/opt/old/hooks/claude-memory")
	var m Manifest
	for _, e := range old {
		m.Artifacts = append(m.Artifacts, Artifact{Step: "hooks.settings", Kind: KindSettingsHook,
			Path: h.p.SettingsJSON(), Identity: e.Event, Entry: e.Canonical()})
	}
	h.write(h.p.SettingsJSON(), string(readTestdata(t, "testdata/settings/ours-recorded-outdated.in.json")), 0o644)
	step := HooksSettingsStep{Version: "v1.0.0"}
	det := step.Detect(context.Background(), h.rp(), h.state(&m))
	if det.State != StateOutdated {
		t.Fatalf("detect = %s (%s)", det.State, det.Detail)
	}
	r := h.run(step, h.state(&m), nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.Contains(string(mustRead(t, h.p.SettingsJSON())), h.p.HookScriptsDir()) {
		t.Error("the entries were not replaced")
	}
	if det := step.Detect(context.Background(), h.rp(), h.state(manifestOf(r.res))); det.State != StateOK {
		t.Errorf("after: %s", det.State)
	}
}

// AC-38: a symlink inside Home is followed (the target is edited and backed
// up, the link stays); one outside Home blocks the step, untouched.
func TestHooksSettingsSymlink(t *testing.T) {
	t.Run("inside home", func(t *testing.T) {
		h := newHK(t)
		target := filepath.Join(h.p.Home, "dotfiles", "claude-settings.json")
		h.write(target, "{\n  \"model\": \"opus\"\n}\n", 0o600)
		if err := os.MkdirAll(h.p.ClaudeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, h.p.SettingsJSON()); err != nil {
			t.Fatal(err)
		}
		r := h.run(HooksSettingsStep{Version: "v1"}, h.state(nil), nil)
		if r.err != nil {
			t.Fatal(r.err)
		}
		if li, _ := os.Lstat(h.p.SettingsJSON()); li.Mode()&fs.ModeSymlink == 0 {
			t.Error("the symlink was replaced by a file")
		}
		if info, _ := os.Stat(target); info.Mode().Perm() != 0o600 {
			t.Errorf("target mode %v, want 0600 kept", info.Mode().Perm())
		}
		if !strings.Contains(string(mustRead(t, target)), `"model": "opus"`) || !strings.Contains(string(mustRead(t, target)), "UserPromptSubmit") {
			t.Errorf("target not merged:\n%s", mustRead(t, target))
		}
		if baks, _ := filepath.Glob(target + ".bak.claude-memory.*"); len(baks) != 1 {
			t.Errorf("backup beside the target: %v", baks)
		}
		if baks, _ := filepath.Glob(h.p.SettingsJSON() + ".bak.*"); len(baks) != 0 {
			t.Errorf("backup beside the link: %v", baks)
		}
	})
	t.Run("outside home", func(t *testing.T) {
		h := newHK(t)
		outside := filepath.Join(h.root, "elsewhere", "settings.json")
		h.write(outside, "{}\n", 0o644)
		if err := os.MkdirAll(h.p.ClaudeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, h.p.SettingsJSON()); err != nil {
			t.Fatal(err)
		}
		n := len(h.fs.Writes())
		r := h.run(HooksSettingsStep{}, h.state(nil), nil)
		if r.det.State != StateBlocked || !strings.Contains(r.det.Detail, "outside") {
			t.Errorf("detect = %s (%s)", r.det.State, r.det.Detail)
		}
		h.assertNoWrites(n)
		if string(mustRead(t, outside)) != "{}\n" {
			t.Error("the target outside Home was touched")
		}
	})
}

// AC-38: a file that is not strict JSON blocks the step with the refusal, and
// the bytes stay.
func TestHooksSettingsRefusal(t *testing.T) {
	h := newHK(t)
	bad := string(readTestdata(t, "testdata/settings/refuse-trailing-comma.in.json"))
	h.write(h.p.SettingsJSON(), bad, 0o644)
	n := len(h.fs.Writes())
	r := h.run(HooksSettingsStep{}, h.state(nil), nil)
	if r.det.State != StateBlocked || !strings.Contains(r.det.Detail, "refusing to edit "+h.p.SettingsJSON()) ||
		!strings.Contains(r.det.Detail, "integration/settings.snippet.json") {
		t.Errorf("detect = %s (%s)", r.det.State, r.det.Detail)
	}
	h.assertNoWrites(n)
	if string(mustRead(t, h.p.SettingsJSON())) != bad {
		t.Error("the file changed")
	}
}

// AC-39: the file changed between Plan and Apply (Claude Code wrote it): the
// step fails with "changed during install, re-run" and writes nothing.
func TestHooksSettingsChangedBetweenPlanAndApply(t *testing.T) {
	h := newHK(t)
	h.write(h.p.SettingsJSON(), "{\"a\": 1}\n", 0o644)
	step, st, ctx := HooksSettingsStep{Version: "v1"}, h.state(nil), context.Background()
	det := step.Detect(ctx, h.rp(), st)
	ch := Choices{}
	for _, a := range det.Artifacts {
		ch[a.ID] = ChoiceApply
	}
	plan, err := step.Plan(ctx, h.rp(), st, ch)
	if err != nil {
		t.Fatal(err)
	}
	h.write(h.p.SettingsJSON(), "{\"a\": 2}\n", 0o644) // Claude Code rewrote it
	n := len(h.fs.Writes())
	_, err = step.Apply(ctx, h.wp(), st, plan)
	if !errors.Is(err, ErrSettingsChanged) || !strings.Contains(err.Error(), "changed during install, re-run") {
		t.Fatalf("err = %v", err)
	}
	h.assertNoWrites(n)
	if string(mustRead(t, h.p.SettingsJSON())) != "{\"a\": 2}\n" {
		t.Error("the concurrent edit was overwritten")
	}
}

// AC-70: our entries in the other settings files are Notes (Detect and
// Apply), those files are never edited, and the user settings file still gets
// its entries.
func TestHooksSettingsDuplicateNotes(t *testing.T) {
	h := newHK(t)
	local := filepath.Join(h.p.ClaudeDir, "settings.local.json")
	proj := filepath.Join(h.p.Cwd, ".claude", "settings.json")
	dup := string(h.fixture("ours-identical", ".in.json"))
	h.write(local, dup, 0o644)
	h.write(proj, "{ not json", 0o644)
	step := HooksSettingsStep{Version: "v1"}
	r := h.run(step, h.state(nil), nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	has := func(ns []Note, sub string) bool {
		for _, n := range ns {
			if n.Level == NoteWarn && strings.Contains(n.Text, sub) {
				return true
			}
		}
		return false
	}
	for _, ns := range [][]Note{r.det.Notes, r.res.Notes} {
		if !has(ns, "duplicate claude-memory hook in "+local) || !has(ns, proj+" is not readable") {
			t.Errorf("notes %+v", ns)
		}
	}
	if string(mustRead(t, local)) != dup || string(mustRead(t, proj)) != "{ not json" {
		t.Error("another settings file was edited")
	}
	if !strings.Contains(string(mustRead(t, h.p.SettingsJSON())), "UserPromptSubmit") {
		t.Error("settings.json was not wired")
	}
}

// Requires migrate and hooks.scripts (AC-7, v0.7).
func TestHooksSettingsRequires(t *testing.T) {
	t.Parallel()
	if got := (HooksSettingsStep{}).Requires(); len(got) != 2 || got[0] != MigrateStepID || got[1] != HooksScriptsStepID {
		t.Errorf("Requires = %v", got)
	}
}

// A failed write leaves settings.json intact (single rename, AC-39).
func TestHooksSettingsRenameFailureKeepsOriginal(t *testing.T) {
	h := newHK(t)
	orig := "{\"a\": 1}\n"
	h.write(h.p.SettingsJSON(), orig, 0o644)
	h.fs.Fail = func(op FSOp, p string) error {
		if op == OpRename && p == h.p.SettingsJSON() {
			return errors.New("disk full")
		}
		return nil
	}
	r := h.run(HooksSettingsStep{Version: "v1"}, h.state(nil), nil)
	if r.err == nil {
		t.Fatal("want an error")
	}
	if string(mustRead(t, h.p.SettingsJSON())) != orig {
		t.Error("the original was damaged")
	}
	if len(r.res.Artifacts) != 0 {
		t.Errorf("a failed apply recorded %+v", r.res.Artifacts)
	}
}
