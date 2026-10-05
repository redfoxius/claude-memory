package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for WI-S2-8: the namespaces step and its goldens (AC-47, AC-64).
// Goldens live in testdata/namespaces/<case>.golden (the file after install).

type nsRunOut struct {
	det    Detection
	plan   Plan
	res    StepResult
	before string
	after  string
}

func (h *s23) nsStep() NamespacesStep { return NamespacesStep{Version: "v1.0.0", fsRoot: h.root} }

// nsRun detects, plans with the default choices and applies when something is
// chosen, like the engine does.
func (h *s23) nsRun(st *RunState) nsRunOut {
	h.t.Helper()
	step, ctx := h.nsStep(), context.Background()
	var r nsRunOut
	if b, err := os.ReadFile(h.p.NamespacesFile()); err == nil {
		r.before = string(b)
	}
	r.det = step.Detect(ctx, h.rp(), st)
	if r.det.State == StateBlocked {
		h.t.Fatalf("blocked: %s", r.det.Detail)
	}
	ch := Choices{}
	for _, a := range r.det.Artifacts {
		ch[a.ID] = DefaultChoice(a.State)
	}
	var err error
	if r.plan, err = step.Plan(ctx, h.rp(), st, ch); err != nil {
		h.t.Fatal(err)
	}
	if hasApply(ch) {
		if r.res, err = step.Apply(ctx, h.wp(), st, r.plan); err != nil {
			h.t.Fatal(err)
		}
	}
	if b, err := os.ReadFile(h.p.NamespacesFile()); err == nil {
		r.after = string(b)
	}
	return r
}

func goldenNS(t *testing.T, name, got string) {
	t.Helper()
	checkGolden(t, filepath.Join("testdata", "namespaces", name+".golden"), []byte(got))
}

func nsState(rules ...NSRule) *RunState {
	st := NewRunState(Inputs{})
	if len(rules) > 0 {
		st.NSRules.Set(rules, SourceFlag)
	}
	return st
}

// A second run over the result is ok and writes nothing (AC-64 run twice).
func (h *s23) nsAssertIdempotent(st *RunState) {
	h.t.Helper()
	before := len(h.fs.Writes())
	r := h.nsRun(st)
	if r.det.State != StateOK {
		h.t.Errorf("second run: %s (%s)", r.det.State, r.det.Detail)
	}
	if got := len(h.fs.Writes()); got != before {
		h.t.Errorf("second run wrote: %v", h.fs.Writes()[before:])
	}
}

func TestNamespacesCreateFresh(t *testing.T) {
	h := newS23(t)
	st := nsState(NSRule{"work", []string{"~/work/acme/**"}}, NSRule{"pet-game", []string{"~/src/pet-game", "~/src/pet-game/**"}})
	r := h.nsRun(st)
	if r.det.State != StateAbsent {
		t.Fatalf("detect = %s", r.det.State)
	}
	goldenNS(t, "fresh", r.after)
	if fi, err := os.Stat(h.p.NamespacesFile()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v %v", fi, err)
	}
	var dirArt bool
	for _, a := range r.res.Artifacts {
		if a.Kind == KindDir && a.Path == h.p.ConfigDir {
			dirArt = true
		}
		if a.Kind == KindFile {
			t.Errorf("namespaces.yaml is user-owned and kept; no file artifact: %+v", a)
		}
	}
	if !dirArt {
		t.Errorf("created ConfigDir not recorded: %+v", r.res.Artifacts)
	}
	h.nsAssertIdempotent(st)
}

func TestNamespacesCreateNoMappings(t *testing.T) {
	h := newS23(t)
	r := h.nsRun(nsState())
	goldenNS(t, "empty", r.after)
	h.nsAssertIdempotent(nsState())
}

func TestNamespacesValidFileIsOK(t *testing.T) {
	h := newS23(t)
	body := "default: scratch\nnamespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n"
	h.write(h.p.NamespacesFile(), body, 0o600)
	before := len(h.fs.Writes())
	r := h.nsRun(nsState(NSRule{"a", []string{"/x/**"}}))
	if r.det.State != StateOK || r.after != body || len(h.fs.Writes()) != before {
		t.Errorf("valid file touched: %s %q %v", r.det.State, r.after, h.fs.Writes()[before:])
	}
	if len(r.plan.Actions) != 0 {
		t.Errorf("actions: %+v", r.plan.Actions)
	}
}

func TestNamespacesAddMappingsToExisting(t *testing.T) {
	h := newS23(t)
	h.write(h.p.NamespacesFile(), "default: scratch\nnamespaces:\n  - namespace: a\n    paths:\n      - /x/**\n", 0o600)
	st := nsState(NSRule{"a", []string{"/x/**", "/y/**"}}, NSRule{"b", []string{"/z"}})
	r := h.nsRun(st)
	if r.det.State != StateOutdated {
		t.Fatalf("detect = %s", r.det.State)
	}
	goldenNS(t, "add", r.after)
	if strings.Contains(r.after, "scratch") == false {
		t.Error("default namespace lost")
	}
	for _, a := range r.res.Artifacts {
		if a.Kind == KindDir {
			t.Errorf("existing ConfigDir recorded as created: %+v", a)
		}
	}
	h.nsAssertIdempotent(st)
}

// An unparseable file is modified, reported and never written, even when
// mappings are requested and the choice is forced to apply.
func TestNamespacesUnparseableNeverWritten(t *testing.T) {
	h := newS23(t)
	bad := "default: [unclosed\nnamespaces: {{{\n"
	h.write(h.p.NamespacesFile(), bad, 0o600)
	st := nsState(NSRule{"a", []string{"/x/**"}})
	before := len(h.fs.Writes())
	r := h.nsRun(st)
	if r.det.State != StateModified || !strings.Contains(r.det.Detail, "parse") {
		t.Fatalf("detect = %s %q", r.det.State, r.det.Detail)
	}
	step := h.nsStep()
	p, err := step.Plan(context.Background(), h.rp(), st, Choices{NamespacesArtifact: ChoiceApply})
	if err != nil || len(p.Actions) != 0 || len(p.Notes) == 0 {
		t.Fatalf("plan: %+v %v", p, err)
	}
	// Even a hand-built plan with actions cannot make Apply write.
	res, err := step.Apply(context.Background(), h.wp(), st, Plan{Actions: []Action{{Artifact: NamespacesArtifact, Verb: "write"}}})
	if err != nil || len(res.Artifacts) != 0 {
		t.Errorf("apply: %+v %v", res, err)
	}
	if got, _ := os.ReadFile(h.p.NamespacesFile()); string(got) != bad || len(h.fs.Writes()) != before {
		t.Errorf("file written: %q %v", got, h.fs.Writes()[before:])
	}
}

func TestNamespacesInvalidNameInFileIsModified(t *testing.T) {
	h := newS23(t)
	h.write(h.p.NamespacesFile(), "namespaces:\n  - namespace: Bad Name\n    paths: [\"/x\"]\n", 0o600)
	if r := h.nsRun(nsState()); r.det.State != StateModified {
		t.Errorf("detect = %s", r.det.State)
	}
}

func TestNamespacesSeedFlags(t *testing.T) {
	h := newS23(t)
	st := NewRunState(Inputs{Namespaces: []string{"a=/x/**", "b=/y", "a=/z", "a=/x/**"}})
	if _, err := h.nsStep().Seed(context.Background(), h.rp(), st); err != nil {
		t.Fatal(err)
	}
	got := st.NSRules.Get()
	if len(got) != 2 || strings.Join(got[0].Globs, ",") != "/x/**,/z" || st.NSRules.Source() != SourceFlag {
		t.Errorf("NSRules = %+v (%s)", got, st.NSRules.Source())
	}
	for _, bad := range []string{"nogl", "=/x", "a=", "Bad=/x"} {
		st := NewRunState(Inputs{Namespaces: []string{bad}})
		if _, err := h.nsStep().Seed(context.Background(), h.rp(), st); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNamespacesConfigureSuggestionsAndPrompts(t *testing.T) {
	h := newS23(t)
	home := h.p.Home
	for _, d := range []string{"work/acme/x", "work/acme/y", "src/game"} {
		h.write(filepath.Join(home, d, ".keep"), "", 0o600)
	}
	proj := filepath.Join(h.p.ClaudeDir, "projects")
	for _, d := range []string{"work/acme/x", "work/acme/y", "src/game"} {
		h.write(filepath.Join(proj, "-"+replaceSlashes(filepath.Join(home, d)[1:]), "s.jsonl"), "{}", 0o600)
	}
	// Suggestions are most-frequent first: ~/work/acme, then ~/src.
	ui := newPW(t, true).
		conf("Map ~/work/acme/** ", true).text("Namespace name for ~/work/acme/**", "").
		conf("Map ~/src/** ", false).
		text("Add another mapping", "extra=/e/**").
		text("Add another mapping", "")
	st := nsState()
	st.NSRules.Clear()
	if err := h.nsStep().Configure(context.Background(), h.rp(), ui, st); err != nil {
		t.Fatal(err)
	}
	got := st.NSRules.Get()
	if len(got) != 2 || got[0].Namespace != "acme" || got[0].Globs[0] != "~/work/acme/**" ||
		got[1].Namespace != "extra" || st.NSRules.Source() != SourcePrompt {
		t.Fatalf("NSRules = %+v", got)
	}
	// Apply emits one `namespaces which` hint per suggested dir.
	r := h.nsRun(st)
	var hints int
	for _, n := range r.res.Notes {
		if strings.Contains(n.Text, "claude-memory namespaces which ") {
			hints++
		}
	}
	if hints != 2 {
		t.Errorf("which hints = %d: %+v", hints, r.res.Notes)
	}
}

func TestNamespacesConfigureSilent(t *testing.T) {
	h := newS23(t)
	// Non-interactive: nothing asked.
	if err := h.nsStep().Configure(context.Background(), h.rp(), newPW(t, false), nsState()); err != nil {
		t.Fatal(err)
	}
	// Flags given: nothing asked even interactively.
	if err := h.nsStep().Configure(context.Background(), h.rp(), newPW(t, true), nsState(NSRule{"a", []string{"/x"}})); err != nil {
		t.Fatal(err)
	}
}

// Detect and Plan take only ReadPorts; dry-run (read-only FS) never writes.
func TestNamespacesDetectPlanWriteNothing(t *testing.T) {
	h := newS23(t)
	st := nsState(NSRule{"a", []string{"/x"}})
	_ = h.nsStep().Detect(context.Background(), h.rp(), st)
	if _, err := h.nsStep().Plan(context.Background(), h.rp(), st, Choices{NamespacesArtifact: ChoiceApply}); err != nil {
		t.Fatal(err)
	}
	if w := h.fs.Writes(); len(w) != 0 {
		t.Errorf("writes: %v", w)
	}
}

// Re-rendering an existing file that holds comments or unknown keys warns in
// Plan and Apply and keeps a backup; a file without them does neither.
func TestNamespacesRerenderLossWarnsAndBacksUp(t *testing.T) {
	for name, tc := range map[string]struct {
		body  string
		lossy bool
	}{
		"comment":         {"# my note\ndefault: scratch\nnamespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n", true},
		"line comment":    {"default: scratch # why\nnamespaces: []\n", true},
		"unknown top":     {"default: scratch\nextra: 1\nnamespaces: []\n", true},
		"unknown rule":    {"namespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n    note: hi\n", true},
		"pr_ingest known": {"namespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n    pr_ingest:\n      enabled: false\n      provider: github\n", false},
		"pr_ingest typo":  {"namespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n    pr_ingest:\n      enabeld: false\n", true},
		"clean":           {"default: scratch\nnamespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newS23(t)
			h.write(h.p.NamespacesFile(), tc.body, 0o600)
			r := h.nsRun(nsState(NSRule{"b", []string{"/y/**"}}))
			warns := func(ns []Note) (n int) {
				for _, x := range ns {
					if x.Level == NoteWarn && strings.Contains(x.Text, "drops") {
						n++
					}
				}
				return
			}
			baks, _ := filepath.Glob(h.p.NamespacesFile() + envBackupSuffix + "*")
			if tc.lossy {
				if warns(r.plan.Notes) != 1 || warns(r.res.Notes) != 1 || len(baks) != 1 {
					t.Fatalf("plan %+v apply %+v baks %v", r.plan.Notes, r.res.Notes, baks)
				}
				if got, _ := os.ReadFile(baks[0]); string(got) != tc.body {
					t.Errorf("backup = %q", got)
				}
			} else if warns(r.plan.Notes)+warns(r.res.Notes) != 0 || len(baks) != 0 {
				t.Errorf("unexpected warn/backup: %+v %+v %v", r.plan.Notes, r.res.Notes, baks)
			}
		})
	}
}

// Configure offers nothing for an unparseable file and skips directories an
// existing rule already covers; the prompt shows the current namespace.
func TestNamespacesConfigureSkipsUnparseableAndCovered(t *testing.T) {
	h := newS23(t)
	home := h.p.Home
	proj := filepath.Join(h.p.ClaudeDir, "projects")
	for _, d := range []string{"work/acme/x", "src/game"} {
		h.write(filepath.Join(home, d, ".keep"), "", 0o600)
		h.write(filepath.Join(proj, "-"+replaceSlashes(filepath.Join(home, d)[1:]), "s.jsonl"), "{}", 0o600)
	}
	st := nsState()
	st.NSRules.Clear()

	h.write(h.p.NamespacesFile(), "default: [unclosed\n", 0o600)
	if err := h.nsStep().Configure(context.Background(), h.rp(), newPW(t, true), st); err != nil || len(st.NSRules.Get()) != 0 {
		t.Fatalf("unparseable: %v %+v", err, st.NSRules.Get())
	}

	h.write(h.p.NamespacesFile(), "namespaces:\n  - namespace: work\n    paths: [\"~/work/acme/**\"]\n", 0o600)
	ui := newPW(t, true).
		conf("Map ~/src/** to its own namespace? (currently \"global\", fallback)", false).
		text("Add another mapping", "")
	if err := h.nsStep().Configure(context.Background(), h.rp(), ui, st); err != nil {
		t.Fatal(err)
	}
}

// The install step re-renders through namespace.Marshal, which must keep a
// rule's pr_ingest section.
func TestNamespacesStepKeepsPRIngest(t *testing.T) {
	h := newS23(t)
	h.write(h.p.NamespacesFile(), "namespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n    pr_ingest:\n      enabled: false\n", 0o600)
	r := h.nsRun(nsState(NSRule{"b", []string{"/y/**"}}))
	if !strings.Contains(r.after, "pr_ingest:") || !strings.Contains(r.after, "enabled: false") || !strings.Contains(r.after, "/y/**") {
		t.Errorf("after =\n%s", r.after)
	}
}

// A malformed pr_ingest must never be re-rendered (it would become "enabled"):
// the step treats the file like an unparseable one.
func TestNamespacesMalformedPRIngestNeverWritten(t *testing.T) {
	for name, body := range map[string]string{
		"false":         "namespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n    pr_ingest: false\n",
		"enabled maybe": "namespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n    pr_ingest: {enabled: maybe}\n",
		"provider list": "namespaces:\n  - namespace: a\n    paths: [\"/x/**\"]\n    pr_ingest: {provider: [gitlab]}\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := newS23(t)
			h.write(h.p.NamespacesFile(), body, 0o600)
			st := nsState(NSRule{"b", []string{"/y/**"}})
			before := len(h.fs.Writes())
			r := h.nsRun(st)
			if r.det.State != StateModified || !strings.Contains(r.det.Detail, "fix pr_ingest") {
				t.Fatalf("detect = %s %q", r.det.State, r.det.Detail)
			}
			if got, _ := os.ReadFile(h.p.NamespacesFile()); string(got) != body || len(h.fs.Writes()) != before {
				t.Errorf("file was written: %q", got)
			}
		})
	}
}
