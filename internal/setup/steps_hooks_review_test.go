package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the review of WI-S2-9/10 (security A1-A8, conformance F2-F3).

// A2: a modified script the user confirmed overwriting for one content is not
// overwritten when it changed again before Apply: no backup, no write.
func TestHookScriptsChangedBetweenPlanAndApply(t *testing.T) {
	h := newHK(t)
	step, st, ctx := HooksScriptsStep{Version: "v1"}, h.state(nil), context.Background()
	name := HookScriptSessionEnd
	p := filepath.Join(h.p.HookScriptsDir(), name)
	raw, _ := os.ReadFile("../../integration/hooks/" + name)
	h.write(p, string(raw), 0o755) // raw embedded: outdated, no Confirm needed
	h.write(filepath.Join(h.p.HookScriptsDir(), HookScriptUserPromptSubmit), "mine\n", 0o755)
	det := step.Detect(ctx, h.rp(), st)
	ch := Choices{}
	for _, a := range det.Artifacts {
		ch[a.ID] = ChoiceApply
	}
	plan, err := step.Plan(ctx, h.rp(), st, ch)
	if err != nil {
		t.Fatal(err)
	}
	h.write(p, "#!/bin/sh\n# the user's own, written after the plan\n", 0o755) // outdated -> modified
	n := len(h.fs.Writes())
	if _, err := step.Apply(ctx, h.wp(), st, plan); !errors.Is(err, ErrHookScriptChanged) {
		t.Fatalf("err = %v, want ErrHookScriptChanged", err)
	}
	h.assertNoWrites(n)
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "the user's own") {
		t.Error("the changed script was overwritten")
	}
}

// F3: a new script shows a /dev/null diff of the rendered script; a mode-only
// fix is a chmod without a diff.
func TestHookScriptsPlanDiffAndChmod(t *testing.T) {
	h := newHK(t)
	step, st, ctx := HooksScriptsStep{Version: "v1"}, h.state(nil), context.Background()
	ch := Choices{"hooks.scripts/" + HookScriptSessionEnd: ChoiceApply, "hooks.scripts/" + HookScriptUserPromptSubmit: ChoiceApply}
	plan, err := step.Plan(ctx, h.rp(), st, ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Diffs) != 2 || !strings.Contains(plan.Diffs[0].Unified, "--- /dev/null") ||
		!strings.Contains(plan.Diffs[0].Unified, "CLAUDE_MEMORY_BIN:-"+h.p.InstalledBinary()) {
		t.Errorf("diffs %+v", plan.Diffs)
	}
	up := filepath.Join(h.p.HookScriptsDir(), HookScriptUserPromptSubmit)
	h.write(up, string(renderedScript(t, HookScriptUserPromptSubmit, h.p.InstalledBinary())), 0o644)
	plan, _ = step.Plan(ctx, h.rp(), st, Choices{"hooks.scripts/" + HookScriptUserPromptSubmit: ChoiceApply})
	if len(plan.Actions) != 1 || plan.Actions[0].Verb != "chmod" || plan.Actions[0].Desc != "chmod 0755" || len(plan.Diffs) != 0 {
		t.Errorf("mode-only plan %+v", plan)
	}
	n := len(h.fs.Writes())
	if _, err := step.Apply(ctx, h.wp(), st, plan); err != nil {
		t.Fatal(err)
	}
	if w := h.fs.Writes()[n:]; len(w) != 1 || !strings.HasPrefix(w[0], "chmod ") {
		t.Errorf("writes %v, want one chmod", w)
	}
	if info, _ := os.Stat(up); info.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
}

// A3: overwriting a modified event that has duplicates says so in the plan.
func TestHooksSettingsPlanNamesDuplicateRemoval(t *testing.T) {
	h := newHK(t)
	h.write(h.p.SettingsJSON(), string(h.fixture("ours-duplicated", ".in.json")), 0o644)
	st, ctx := h.state(nil), context.Background()
	plan, err := HooksSettingsStep{}.Plan(ctx, h.rp(), st, Choices{artUPS: ChoiceApply})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || !strings.Contains(plan.Actions[0].Desc, "and remove 1 duplicate entries") {
		t.Errorf("plan %+v", plan.Actions)
	}
}

// AC-38: a duplicate key anywhere blocks the step; the file is untouched.
func TestHooksSettingsDuplicateKeyRefused(t *testing.T) {
	h := newHK(t)
	in := string(readTestdata(t, "testdata/settings/refuse-duplicate-key.in.json"))
	h.write(h.p.SettingsJSON(), in, 0o644)
	n := len(h.fs.Writes())
	r := h.run(HooksSettingsStep{}, h.state(nil), nil)
	if r.det.State != StateBlocked || !strings.Contains(r.det.Detail, "duplicate key") {
		t.Errorf("detect %s (%s)", r.det.State, r.det.Detail)
	}
	h.assertNoWrites(n)
}

// A8: a backup never overwrites an earlier one (same second) and is 0600.
func TestSettingsBackupUniqueAndPrivate(t *testing.T) {
	h := newHK(t)
	h.write(h.p.SettingsJSON(), "{}\n", 0o644)
	clk := NewFakeClock(testNow)
	first := SettingsBackupPath(h.p.SettingsJSON(), clk)
	h.write(first, "an earlier backup\n", 0o600)
	f, err := ReadSettingsFile(h.fs, h.p.Home, h.p.SettingsJSON())
	if err != nil {
		t.Fatal(err)
	}
	backup, err := WriteSettingsFile(h.fs, clk, f, []byte("{\"a\":1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if backup != first+".1" {
		t.Errorf("backup %s, want %s.1", backup, first)
	}
	if b, _ := os.ReadFile(first); string(b) != "an earlier backup\n" {
		t.Error("an existing backup was overwritten")
	}
	if info, _ := os.Stat(backup); info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v, want 0600 (the original was 0644)", info.Mode().Perm())
	}
}

// A4: the binary path cannot break out of the script's ${VAR:-default}.
func TestRenderHookScriptRejectsBraceAndQuote(t *testing.T) {
	t.Parallel()
	raw := []byte(`CLAUDE_MEMORY_BIN="${CLAUDE_MEMORY_BIN:-$HOME/.local/bin/claude-memory}"` + "\n")
	for _, bin := range []string{"/opt/a}b/claude-memory", "/opt/it's/claude-memory"} {
		if _, err := RenderHookScript(raw, bin); err == nil {
			t.Errorf("%q was accepted", bin)
		}
	}
}

// F2: Adopt on ok-but-unrecorded scripts and entries; nothing for anything
// that is not ok, and never a dir.
func TestHooksAdopt(t *testing.T) {
	h := newHK(t)
	ctx := context.Background()
	st := h.state(nil)
	if got := (HooksScriptsStep{}).Adopt(ctx, h.rp(), st); len(got) != 0 {
		t.Errorf("scripts adopted from nothing: %+v", got)
	}
	r := h.run(HooksScriptsStep{Version: "v1"}, st, nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	got := (HooksScriptsStep{Version: "v1"}).Adopt(ctx, h.rp(), h.state(nil)) // manifest empty: a hand install
	if len(got) != 2 || got[0].Kind != KindFile || got[0].SHA256 == "" {
		t.Errorf("scripts adopt = %+v", got)
	}
	h.write(h.p.SettingsJSON(), string(h.fixture("ours-identical", ".in.json")), 0o644)
	got = (HooksSettingsStep{Version: "v1"}).Adopt(ctx, h.rp(), h.state(nil))
	if len(got) != 2 || got[0].Kind != KindSettingsHook || got[0].Entry == "" || got[0].CreatedFile || got[0].CreatedContainer {
		t.Errorf("settings adopt = %+v", got)
	}
	h.write(h.p.SettingsJSON(), string(h.fixture("ours-legacy-home", ".in.json")), 0o644)
	if got := (HooksSettingsStep{}).Adopt(ctx, h.rp(), h.state(nil)); len(got) != 0 {
		t.Errorf("a modified entry was adopted: %+v", got)
	}
}
