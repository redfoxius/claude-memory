package setup

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// Engine tests with fake steps (WI-S2-1c). Every test builds explicit Paths
// from realTempDir(t); nothing touches the real HOME.

type eh struct {
	t     *testing.T
	p     Paths
	fs    *FakeFS
	clk   *FakeClock
	log   *FakeLog
	rep   *FakeReporter
	ui    *FakePrompter
	world *FakeWorld
	ver   string
}

func newEH(t *testing.T, interactive bool) *eh {
	t.Helper()
	p := testPaths(t)
	return &eh{
		t: t, p: p, fs: NewFakeFS(t, p.Home[:len(p.Home)-len("/home")]),
		clk: NewFakeClock(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)),
		log: &FakeLog{}, rep: &FakeReporter{}, ui: NewFakePrompter(t, interactive),
		world: NewFakeWorld(nil), ver: "v1.2.0",
	}
}

func (h *eh) step(id string, req []string, arts ...string) *FakeStep {
	return &FakeStep{StepID: id, Req: req, Log: h.log, DetectF: h.world.DetectFor(arts...), ApplyF: h.world.ApplyOK()}
}

func (h *eh) engine(steps ...*FakeStep) *Engine {
	rp := ReadPorts{FS: h.fs, Runner: NewFakeRunner(h.t), Clock: h.clk, Paths: h.p}
	var ss []Step
	for _, s := range steps {
		ss = append(ss, s)
	}
	return &Engine{
		Steps: ss, Read: rp, Write: WritePorts{ReadPorts: rp, FS: h.fs},
		UI: h.ui, Reporter: h.rep, Version: h.ver,
	}
}

func (h *eh) run(in Inputs, steps ...*FakeStep) RunResult {
	h.t.Helper()
	return h.engine(steps...).Run(context.Background(), in)
}

// nonLockWrites are the FS write attempts other than the flock (AC-50 counts
// writes; the lock file is not content).
func (h *eh) nonLockWrites() []string {
	var out []string
	for _, w := range h.fs.Writes() {
		if !strings.HasPrefix(w, "lock ") {
			out = append(out, w)
		}
	}
	return out
}

func (h *eh) manifest() *Manifest {
	h.t.Helper()
	l, err := LoadManifest(h.fs, h.p)
	if err != nil || l.Manifest == nil {
		h.t.Fatalf("manifest: %+v, %v", l, err)
	}
	return l.Manifest
}

func outcome(t *testing.T, r RunResult, id string) StepOutcome {
	t.Helper()
	for _, o := range r.Outcomes {
		if o.StepID == id {
			return o
		}
	}
	t.Fatalf("no outcome for %s in %+v", id, r.Outcomes)
	return StepOutcome{}
}

func wantExit(t *testing.T, r RunResult, code int) {
	t.Helper()
	if r.ExitCode != code {
		t.Fatalf("exit = %d (err %v), want %d; outcomes %+v", r.ExitCode, r.Err, code, r.Outcomes)
	}
}

func hasNote(ns []Note, sub string) bool {
	for _, n := range ns {
		if strings.Contains(n.Text, sub) {
			return true
		}
	}
	return false
}

func TestEngineOrderAndNoApplyBeforeConfirm(t *testing.T) {
	t.Parallel()
	setup := func(h *eh) (*FakeStep, *FakeStep) {
		h.world = NewFakeWorld(map[string]State{"a/x": StateAbsent, "b/x": StateAbsent})
		return h.step("a", nil, "a/x"), h.step("b", []string{"a"}, "b/x")
	}
	t.Run("declined", func(t *testing.T) {
		h := newEH(t, true)
		a, b := setup(h)
		h.ui.ExpectSelect("a", -1).ExpectSelect("b", -1).ExpectConfirm("Apply", false)
		r := h.run(Inputs{}, a, b)
		wantExit(t, r, ExitOK)
		if !r.Declined || h.log.Count("Apply a")+h.log.Count("Apply b") != 0 {
			t.Fatalf("declined=%v log=%v", r.Declined, h.log.Calls())
		}
		if len(h.nonLockWrites()) != 0 {
			t.Fatalf("writes before confirm: %v", h.nonLockWrites())
		}
	})
	t.Run("accepted", func(t *testing.T) {
		h := newEH(t, true)
		a, b := setup(h)
		h.ui.ExpectSelect("a", -1).ExpectSelect("b", -1).ExpectConfirm("Apply", true)
		r := h.run(Inputs{}, a, b)
		wantExit(t, r, ExitOK)
		want := []string{"Seed a", "Seed b", "Detect a", "Detect b", "Configure a", "Configure b", "Plan a", "Plan b", "Apply a", "Detect a", "Detect b", "Plan b", "Apply b", "Detect b"}
		// Rule B re-Detects b (its prerequisite a was applied) before applying it.
		if got := h.log.Calls(); !slices.Equal(got, want) {
			t.Fatalf("calls:\n got %v\nwant %v", got, want)
		}
		if h.log.Index("Apply a") < h.log.Index("Plan b") {
			t.Fatal("an Apply ran before every step was planned")
		}
	})
}

func TestEngineRuleAReadersAndNoSpuriousRedetect(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"ns/x": StateAbsent})
	var envPlan Choices
	applied := false
	env := &FakeStep{StepID: "envfile", Log: h.log,
		DetectF: func(n int, st *RunState) Detection {
			s := StateOK
			if st.DB.Get().Host == "new" && !applied {
				s = StateOutdated
			}
			return Detection{State: s, Artifacts: []ArtifactState{{ID: "envfile/MEMORY_PG_DSN", State: s}}}
		},
		PlanF: func(_ *RunState, ch Choices) (Plan, error) { envPlan = ch; return Plan{}, nil },
		ApplyF: func(int, WritePorts, *RunState, Plan) (StepResult, error) {
			applied = true
			return StepResult{}, nil
		}}
	db := h.step("database", nil, "database/database")
	h.world.Set("database/database", StateOK)
	db.SeedF = func(st *RunState) ([]Note, error) {
		st.DB.Set(DBTarget{Host: "old"}, SourceEnvFile)
		return nil, nil
	}
	db.ConfigureF = func(_ Prompter, st *RunState) error {
		st.DB.Set(DBTarget{Host: "new"}, SourcePrompt)
		return nil
	}
	ns := h.step("namespaces", nil, "ns/x")
	ns.ConfigureF = func(_ Prompter, st *RunState) error {
		st.NSRules.Set([]NSRule{{Namespace: "x"}}, SourcePrompt)
		return nil
	}
	env.Log, db.Log = h.log, h.log
	r := h.engine(env, db, ns).Run(context.Background(), Inputs{Yes: true, Reconfigure: true})
	wantExit(t, r, ExitOK)
	if envPlan["envfile/MEMORY_PG_DSN"] != ChoiceApply {
		t.Fatalf("envfile choices = %v, want the DSN key apply", envPlan)
	}
	// envfile precedes database, yet is re-Detected as a reader of DB:
	// initial + rule A + post-apply. database: initial + rule A (reader).
	// namespaces: NSRules has no reader, so initial + post-apply only.
	for id, want := range map[string]int{"envfile": 3, "database": 2, "namespaces": 2} {
		if got := h.log.Count("Detect " + id); got != want {
			t.Errorf("Detect %s ran %d times, want %d (%v)", id, got, want, h.log.Calls())
		}
	}
}

func TestEngineRuleAEnvKeyExceptionInteractive(t *testing.T) {
	t.Parallel()
	for _, interactive := range []bool{true, false} {
		name := "yes"
		if interactive {
			name = "interactive"
		}
		t.Run(name, func(t *testing.T) {
			h := newEH(t, interactive)
			var planned Choices
			done := false
			env := &FakeStep{StepID: "envfile", Log: h.log,
				DetectF: func(n int, st *RunState) Detection {
					s := StateOK
					if st.DB.Get().Host == "new" && !done {
						s = StateModified // the DSN line differs from what we recorded
					}
					return Detection{State: s, Artifacts: []ArtifactState{{ID: "envfile/MEMORY_PG_DSN", State: s, Detail: "edited"}}}
				},
				PlanF: func(_ *RunState, ch Choices) (Plan, error) { planned = ch; return Plan{}, nil },
				ApplyF: func(int, WritePorts, *RunState, Plan) (StepResult, error) {
					done = true
					return StepResult{}, nil
				}}
			db := &FakeStep{StepID: "database", Log: h.log}
			db.ConfigureF = func(_ Prompter, st *RunState) error {
				st.DB.Set(DBTarget{Host: "new"}, SourcePrompt)
				return nil
			}
			in := Inputs{Reconfigure: true}
			if interactive {
				h.ui.ExpectConfirm("Apply", true) // no overwrite Confirm: the answer is the consent
			} else {
				in.Yes = true
			}
			r := h.run(in, env, db)
			wantExit(t, r, ExitOK)
			if interactive {
				if planned["envfile/MEMORY_PG_DSN"] != ChoiceApply || h.log.Count("Apply envfile") != 1 {
					t.Fatalf("interactive: planned %v, log %v", planned, h.log.Calls())
				}
			} else {
				if h.log.Count("Apply envfile") != 0 {
					t.Fatalf("--yes overwrote a modified key: %v", h.log.Calls())
				}
				if !hasNote(outcome(t, r, "envfile").Notes, "drift") {
					t.Fatalf("no drift note: %+v", outcome(t, r, "envfile"))
				}
			}
		})
	}
}

func TestEngineRuleBDropsOkAndKeepsNewlyModified(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"a/x": StateAbsent, "b/1": StateAbsent, "b/2": StateAbsent, "b/3": StateAbsent})
	a := h.step("a", nil, "a/x")
	b := h.step("b", []string{"a"}, "b/1", "b/2", "b/3")
	var lastPlan Choices
	b.PlanF = func(_ *RunState, ch Choices) (Plan, error) {
		lastPlan = ch
		var p Plan
		for _, id := range applyIDs(ch) {
			p.Actions = append(p.Actions, Action{Artifact: id, Verb: "write"})
		}
		return p, nil
	}
	base := b.DetectF
	b.DetectF = func(n int, st *RunState) Detection {
		if n >= 2 { // after a was applied: b/1 got fixed by it, b/2 turned modified
			return Detection{State: StateModified, Artifacts: []ArtifactState{
				{ID: "b/1", State: StateOK}, {ID: "b/2", State: StateModified, Detail: "hand edit"}, {ID: "b/3", State: h.world.Get("b/3")}}}
		}
		return base(n, st)
	}
	r := h.run(Inputs{Yes: true}, a, b)
	wantExit(t, r, ExitOK)
	if lastPlan["b/1"] == ChoiceApply || lastPlan["b/2"] == ChoiceApply || lastPlan["b/3"] != ChoiceApply {
		t.Fatalf("rule B plan = %v", lastPlan)
	}
	if !hasNote(outcome(t, r, "b").Notes, "b/2") {
		t.Fatalf("newly modified b/2 not reported: %+v", outcome(t, r, "b").Notes)
	}
}

func TestEngineRuleBAllDroppedSkipsApply(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"a/x": StateAbsent, "b/1": StateAbsent})
	a := h.step("a", nil, "a/x")
	b := h.step("b", []string{"a"}, "b/1")
	b.DetectF = func(n int, _ *RunState) Detection {
		s := StateAbsent
		if n >= 2 {
			s = StateOK
		}
		return Detection{State: s, Artifacts: []ArtifactState{{ID: "b/1", State: s}}}
	}
	r := h.run(Inputs{Yes: true}, a, b)
	wantExit(t, r, ExitOK)
	if h.log.Count("Apply b") != 0 {
		t.Fatalf("b applied although it re-Detected ok: %v", h.log.Calls())
	}
}

func TestEngineModifiedPlusAbsentUnderYes(t *testing.T) { // B-1 regression
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"s/m": StateModified, "s/a": StateAbsent})
	s := h.step("s", nil, "s/m", "s/a")
	var got Plan
	s.ApplyF = func(n int, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
		got = p
		return h.world.ApplyOK()(n, wc, st, p)
	}
	r := h.run(Inputs{Yes: true}, s)
	wantExit(t, r, ExitOK)
	if len(got.Actions) != 1 || got.Actions[0].Artifact != "s/a" {
		t.Fatalf("applied %v, want only s/a", got.Actions)
	}
	o := outcome(t, r, "s")
	if o.Outcome != OutcomeApplied || !hasNote(o.Notes, "drift") {
		t.Fatalf("outcome %+v", o)
	}
}

func TestEngineModifiedOverwriteConfirmInteractive(t *testing.T) {
	t.Parallel()
	h := newEH(t, true)
	h.world = NewFakeWorld(map[string]State{"s/m": StateModified, "s/a": StateAbsent})
	s := h.step("s", nil, "s/m", "s/a")
	h.ui.ExpectSelect("s", -1).ExpectConfirm("overwrite your modified s/m", true).ExpectConfirm("Apply", true)
	var got Plan
	s.ApplyF = func(n int, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
		got = p
		return h.world.ApplyOK()(n, wc, st, p)
	}
	wantExit(t, h.run(Inputs{}, s), ExitOK)
	if len(got.Actions) != 2 {
		t.Fatalf("confirmed overwrite not applied: %v", got.Actions)
	}
}

func awaitStep(h *eh, secondAwait bool) *FakeStep {
	h.world = NewFakeWorld(nil)
	db := &FakeStep{StepID: "database", Log: h.log}
	db.DetectF = func(n int, _ *RunState) Detection {
		d, bf := StateAbsent, StateOK
		switch {
		case n == 3:
			d, bf = StateOK, StateOutdated
		case n >= 4:
			d, bf = StateOK, StateOK
		}
		return Detection{State: d, Artifacts: []ArtifactState{{ID: "database/database", State: d}, {ID: "database/bootstrap-file", State: bf}}}
	}
	db.ApplyF = func(n int, _ WritePorts, _ *RunState, _ Plan) (StepResult, error) {
		if n == 1 {
			return StepResult{
				Artifacts: []Artifact{{Step: "database", Kind: KindFile, Path: "/x/db", Version: "v1"}},
				Await:     &Await{Instructions: []string{"run: psql -f bootstrap.sql"}, Artifacts: []string{"database/database"}},
			}, nil
		}
		if secondAwait {
			return StepResult{Await: &Await{Instructions: []string{"again"}}}, nil
		}
		return StepResult{}, nil // deletes bootstrap.sql: never recorded
	}
	return db
}

func TestEngineAwaitRecheckFollowUpLoopFree(t *testing.T) { // N1
	t.Parallel()
	h := newEH(t, true)
	db := awaitStep(h, false)
	h.ui.ExpectSelect("database", -1).ExpectConfirm("Apply", true).
		ExpectSelect("waiting", 0).ExpectSelect("waiting", 0) // first re-check fails, second passes
	r := h.run(Inputs{}, db)
	wantExit(t, r, ExitOK)
	if h.log.Count("Apply database") != 2 {
		t.Fatalf("applies: %v", h.log.Calls())
	}
	if o := outcome(t, r, "database"); o.Outcome != OutcomeApplied {
		t.Fatalf("outcome %+v", o)
	}
	m := h.manifest()
	if len(m.Artifacts) != 1 || m.Artifacts[0].Path != "/x/db" {
		t.Fatalf("manifest = %+v (bootstrap-file must not be recorded)", m.Artifacts)
	}
	if got := h.rep.Awaits["database"]; len(got) != 1 {
		t.Fatalf("await instructions not reported: %v", h.rep.Awaits)
	}
}

func TestEngineAwaitSecondAwaitIsFailure(t *testing.T) {
	t.Parallel()
	h := newEH(t, true)
	db := awaitStep(h, true)
	h.ui.ExpectSelect("database", -1).ExpectConfirm("Apply", true).
		ExpectSelect("waiting", 0).ExpectSelect("waiting", 0)
	r := h.run(Inputs{}, db)
	wantExit(t, r, ExitFailed)
	if o := outcome(t, r, "database"); o.Outcome != OutcomeFailed {
		t.Fatalf("outcome %+v", o)
	}
	if h.log.Count("Apply database") != 2 {
		t.Fatalf("a third Apply ran: %v", h.log.Calls())
	}
}

func TestEngineAwaitSkipBlocksDependents(t *testing.T) {
	t.Parallel()
	h := newEH(t, true)
	db := awaitStep(h, false)
	mig := h.step("migrate", []string{"database"}, "migrate/x")
	h.world.Set("migrate/x", StateAbsent)
	h.ui.ExpectSelect("database", -1).ExpectSelect("migrate", -1).ExpectConfirm("Apply", true).ExpectSelect("waiting", 1)
	r := h.run(Inputs{}, db, mig)
	wantExit(t, r, ExitOK) // skipped by the user: not a failure
	if o := outcome(t, r, "database"); o.Outcome != OutcomeSkipped {
		t.Fatalf("database %+v", o)
	}
	if o := outcome(t, r, "migrate"); o.Outcome != OutcomeBlocked || o.Hard {
		t.Fatalf("migrate %+v", o)
	}
	if h.log.Count("Apply migrate") != 0 {
		t.Fatal("dependent applied")
	}
}

func TestEngineAwaitQuitExits130AndKeepsAppliedArtifacts(t *testing.T) {
	t.Parallel()
	h := newEH(t, true)
	db := awaitStep(h, false)
	h.ui.ExpectSelect("database", -1).ExpectConfirm("Apply", true).ExpectSelect("waiting", 2)
	r := h.run(Inputs{}, db)
	wantExit(t, r, ExitInterrupted)
	if m := h.manifest(); len(m.Artifacts) != 1 {
		t.Fatalf("manifest = %+v", m.Artifacts)
	}
}

func TestEngineAwaitUnderYesBlocksDependentsExit1(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	db := awaitStep(h, false)
	mig := h.step("migrate", []string{"database"}, "migrate/x")
	h.world.Set("migrate/x", StateAbsent)
	other := h.step("other", nil, "other/x")
	h.world.Set("other/x", StateAbsent)
	r := h.run(Inputs{Yes: true}, db, mig, other)
	wantExit(t, r, ExitFailed)
	o := outcome(t, r, "database")
	if o.Outcome != OutcomeBlocked || !o.Hard || !strings.Contains(o.Remedy, "psql") {
		t.Fatalf("database %+v", o)
	}
	if outcome(t, r, "migrate").Outcome != OutcomeBlocked || outcome(t, r, "other").Outcome != OutcomeApplied {
		t.Fatalf("outcomes %+v", r.Outcomes)
	}
	if got := h.rep.Awaits["database"]; len(got) == 0 {
		t.Fatal("instructions not reported")
	}
}

func TestEngineFailingStepRecordsNothing(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"f/x": StateAbsent, "g/x": StateAbsent, "h/x": StateAbsent})
	f := h.step("f", nil, "f/x")
	f.ApplyF = func(int, WritePorts, *RunState, Plan) (StepResult, error) {
		return StepResult{Artifacts: []Artifact{{Step: "f", Kind: KindFile, Path: "/x/f"}}}, errors.New("boom")
	}
	g := h.step("g", []string{"f"}, "g/x")
	hh := h.step("h", nil, "h/x")
	r := h.run(Inputs{Yes: true}, f, g, hh)
	wantExit(t, r, ExitFailed)
	if outcome(t, r, "f").Outcome != OutcomeFailed || outcome(t, r, "g").Outcome != OutcomeBlocked || outcome(t, r, "h").Outcome != OutcomeApplied {
		t.Fatalf("outcomes %+v", r.Outcomes)
	}
	for _, a := range h.manifest().Artifacts {
		if a.Step == "f" || a.Step == "g" {
			t.Fatalf("manifest records %+v of a failed/blocked step", a)
		}
	}
	if h.log.Count("Apply g") != 0 {
		t.Fatal("dependent of a failed step applied")
	}
}

func TestEngineVerifyFailureIsStepFailure(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"s/x": StateAbsent})
	s := h.step("s", nil, "s/x")
	s.ApplyF = func(int, WritePorts, *RunState, Plan) (StepResult, error) { return StepResult{}, nil } // changes nothing
	r := h.run(Inputs{Yes: true}, s)
	wantExit(t, r, ExitFailed)
	if o := outcome(t, r, "s"); o.Outcome != OutcomeFailed || !strings.Contains(o.Detail, "s/x") {
		t.Fatalf("%+v", o)
	}
}

func TestEngineNoOpRunWritesNothingAndSeedsWithoutConfigure(t *testing.T) { // H1, AC-50
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"database/x": StateAbsent})
	// Run 1 applies something so a manifest exists.
	db := h.step("database", nil, "database/x")
	db.SeedF = func(st *RunState) ([]Note, error) { st.Topology.Set(TopologyLocal, SourceDefault); return nil, nil }
	wantExit(t, h.run(Inputs{Yes: true}, db), ExitOK)
	before, err := os.ReadFile(h.p.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	writes := len(h.nonLockWrites())

	// Run 2: everything is ok. Seed fills Topology from the manifest, nothing
	// is configured, and an unset PRRepos leaves the envfile step ok.
	h.log = &FakeLog{}
	db2 := h.step("database", nil, "database/x")
	db2.SeedF = func(st *RunState) ([]Note, error) {
		if m := st.Prior.Manifest; m != nil {
			st.Topology.Set(Topology(m.Topology), SourceManifest)
		}
		return nil, nil
	}
	env := h.step("envfile", nil)
	env.DetectF = func(_ int, st *RunState) Detection {
		s := StateOK
		if st.PRRepos.IsSet() { // an unset field is "not managed": no artifact, no write
			s = StateAbsent
		}
		return Detection{State: s, Artifacts: []ArtifactState{{ID: "envfile/MEMORY_PR_INGEST_REPOS", State: s}}}
	}
	r := h.run(Inputs{Yes: true}, db2, env)
	wantExit(t, r, ExitOK)
	if r.State.Topology.Get() != TopologyLocal || r.State.Topology.Source() != SourceManifest {
		t.Fatalf("Seed did not fill Topology from Prior: %+v", r.State.Topology)
	}
	for _, c := range h.log.Calls() {
		if strings.HasPrefix(c, "Configure") || strings.HasPrefix(c, "Apply") || strings.HasPrefix(c, "Plan") {
			t.Fatalf("a no-op run called %q: %v", c, h.log.Calls())
		}
	}
	if len(h.nonLockWrites()) != writes {
		t.Fatalf("no-op run wrote: %v", h.nonLockWrites()[writes:])
	}
	after, _ := os.ReadFile(h.p.Manifest())
	if string(after) != string(before) {
		t.Fatal("manifest bytes changed on a no-op run")
	}
	for _, o := range r.Outcomes {
		if o.Outcome != OutcomeUnchanged {
			t.Fatalf("outcome %+v", o)
		}
	}
}

func TestEngineManifestHeaderRecorded(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"s/x": StateAbsent})
	s := h.step("s", nil, "s/x")
	s.SeedF = func(st *RunState) ([]Note, error) { st.Topology.Set(TopologyRemote, SourceFlag); return nil, nil }
	e := h.engine(s)
	e.Read.Platform = PlatformInfo{OS: "linux", Arch: "arm64", JobsBackend: JobsSystemd}
	wantExit(t, e.Run(context.Background(), Inputs{Yes: true}), ExitOK)
	m := h.manifest()
	if m.BinaryVersion != "v1.2.0" || m.Topology != "remote" || m.JobsBackend != JobsSystemd ||
		m.ClaudeConfigDir != h.p.ClaudeDir || m.Platform.OS != "linux" || m.InstalledAt.IsZero() || m.Schema != ManifestSchema {
		t.Fatalf("header = %+v", m)
	}
}

func TestEngineLockHeldExit2(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	if _, err := h.fs.Lock(h.p.Lock()); err != nil {
		t.Fatal(err)
	}
	s := h.step("s", nil, "s/x")
	h.world.Set("s/x", StateAbsent)
	r := h.run(Inputs{Yes: true}, s)
	wantExit(t, r, ExitUsage)
	if !errors.Is(r.Err, ErrLocked) || h.log.Count("Detect s") != 0 {
		t.Fatalf("err %v log %v", r.Err, h.log.Calls())
	}
}

func TestEngineLockReleasedAfterRun(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	wantExit(t, h.run(Inputs{Yes: true}, h.step("s", nil, "s/x")), ExitOK)
	if _, err := h.fs.Lock(h.p.Lock()); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}

func TestEngineDryRunNeverLocksOrWrites(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.fs.Fail = func(op FSOp, path string) error {
		if op == OpLock {
			t.Errorf("dry-run called Lock(%s)", path)
		}
		return nil
	}
	h.world = NewFakeWorld(map[string]State{"s/x": StateAbsent})
	s := h.step("s", nil, "s/x")
	r := h.run(Inputs{DryRun: true}, s)
	wantExit(t, r, ExitOK)
	if !r.DryRun || len(h.rep.Plans) != 1 || len(h.rep.Plans[0].Steps) != 1 {
		t.Fatalf("plans %+v", h.rep.Plans)
	}
	for _, w := range h.fs.Writes() {
		t.Errorf("dry-run write attempt: %s", w)
	}
	if h.log.Count("Apply s") != 0 {
		t.Fatal("dry-run applied")
	}
}

func TestEngineSeedErrorExit2BeforeDetectAndNotesBeforeTable(t *testing.T) { // N6
	t.Parallel()
	h := newEH(t, false)
	bad := h.step("database", nil, "d/x")
	bad.SeedF = func(*RunState) ([]Note, error) {
		return []Note{{NoteWarn, "shell DSN differs"}}, errors.New("--pg-dsn: invalid port")
	}
	r := h.run(Inputs{Yes: true}, bad, h.step("other", nil, "o/x"))
	wantExit(t, r, ExitUsage)
	if h.log.Count("Detect database") != 0 || h.log.Count("Detect other") != 0 {
		t.Fatalf("Detect ran after a Seed error: %v", h.log.Calls())
	}
	if !hasNote(h.rep.Notes_, "shell DSN differs") {
		t.Fatalf("seed notes lost: %v", h.rep.Notes_)
	}

	h2 := newEH(t, false)
	ok := h2.step("s", nil, "s/x")
	ok.SeedF = func(*RunState) ([]Note, error) { return []Note{{NoteInfo, "n"}}, nil }
	h2.run(Inputs{Yes: true}, ok)
	if len(h2.rep.Events) < 2 || h2.rep.Events[0] != "notes" || h2.rep.Events[1] != "table" {
		t.Fatalf("events %v: Seed notes must precede the table", h2.rep.Events)
	}
}

func TestEngineUnsetThenDefaultUnderYesRedetectsReader(t *testing.T) { // N3
	t.Parallel()
	h := newEH(t, false)
	var envPlan Choices
	env := &FakeStep{StepID: "envfile", Log: h.log, DetectF: func(_ int, st *RunState) Detection {
		s := StateOK
		if st.Ollama.IsSet() { // unset = not managed; set = key to write
			s = StateAbsent
		}
		return Detection{State: s, Artifacts: []ArtifactState{{ID: "envfile/MEMORY_OLLAMA_URL", State: s}}}
	}, PlanF: func(_ *RunState, ch Choices) (Plan, error) { envPlan = ch; return Plan{}, nil },
		ApplyF: func(int, WritePorts, *RunState, Plan) (StepResult, error) { return StepResult{}, nil }}
	// After Apply the key exists: model it so the success rule holds.
	applied := false
	env.ApplyF = func(int, WritePorts, *RunState, Plan) (StepResult, error) { applied = true; return StepResult{}, nil }
	inner := env.DetectF
	env.DetectF = func(n int, st *RunState) Detection {
		if applied {
			return Detection{State: StateOK, Artifacts: []ArtifactState{{ID: "envfile/MEMORY_OLLAMA_URL", State: StateOK}}}
		}
		return inner(n, st)
	}
	ol := &FakeStep{StepID: "ollama", Log: h.log}
	ol.MissingF = func(st *RunState) bool { return !st.Ollama.IsSet() }
	ol.ConfigureF = func(_ Prompter, st *RunState) error {
		st.Ollama.Set(OllamaTarget{URL: "http://127.0.0.1:11434", Model: "bge-m3"}, SourceDefault)
		return nil
	}
	r := h.run(Inputs{Yes: true}, env, ol)
	wantExit(t, r, ExitOK)
	if envPlan["envfile/MEMORY_OLLAMA_URL"] != ChoiceApply || h.log.Count("Apply envfile") != 1 {
		t.Fatalf("plan %v log %v", envPlan, h.log.Calls())
	}
}

func TestEngineConfigureFailureUnderYesFailsStep(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"d/x": StateAbsent, "o/x": StateAbsent})
	d := h.step("database", nil, "d/x")
	d.ConfigureF = func(ui Prompter, _ *RunState) error {
		if _, err := ui.Text("DSN", "", nil); err != nil { // a step that wrongly prompts
			return errors.New("pass --pg-dsn (and --pg-password-stdin)")
		}
		return nil
	}
	o := h.step("other", nil, "o/x")
	r := h.run(Inputs{Yes: true}, d, o)
	wantExit(t, r, ExitFailed)
	if got := outcome(t, r, "database"); got.Outcome != OutcomeFailed || !strings.Contains(got.Detail, "--pg-dsn") {
		t.Fatalf("%+v", got)
	}
	if outcome(t, r, "other").Outcome != OutcomeApplied || h.log.Count("Apply database") != 0 {
		t.Fatalf("isolation broken: %v", h.log.Calls())
	}
}

func blockedPrereqs(h *eh) (*FakeStep, *FakeStep) {
	h.world = NewFakeWorld(map[string]State{"t/x": StateAbsent})
	fixed := func() bool { return h.log.Count("Detect prereqs") >= 2 }
	pr := &FakeStep{StepID: "prereqs", StepTitle: "Prerequisites", Log: h.log, DetectF: func(int, *RunState) Detection {
		if fixed() {
			return Detection{State: StateOK, Artifacts: []ArtifactState{{ID: "prereqs", State: StateOK}}}
		}
		return Detection{State: StateBlocked, Detail: "psql missing", Remedy: "brew install libpq"}
	}}
	tp := &FakeStep{StepID: "topology", Req: []string{"prereqs"}, Log: h.log, ApplyF: h.world.ApplyOK(), DetectF: func(int, *RunState) Detection {
		if !fixed() {
			return Detection{State: StateBlocked, BlockedBy: "prereqs", Detail: "needs prereqs"}
		}
		s := h.world.Get("t/x")
		return Detection{State: s, Artifacts: []ArtifactState{{ID: "t/x", State: s}}}
	}}
	return pr, tp
}

func TestEngineBlockedPromptRecheckResolves(t *testing.T) { // M1
	t.Parallel()
	h := newEH(t, true)
	pr, tp := blockedPrereqs(h)
	h.ui.ExpectSelect("Prerequisites", 0).ExpectConfirm("Apply", true)
	r := h.run(Inputs{}, pr, tp)
	wantExit(t, r, ExitOK)
	if h.log.Count("Apply topology") != 1 || outcome(t, r, "topology").Outcome != OutcomeApplied {
		t.Fatalf("dependent not applied: %v", h.log.Calls())
	}
	// Re-check re-Detects only that step; the dependent is re-Detected by rule A.
	if h.log.Count("Detect prereqs") != 2 {
		t.Fatalf("calls %v", h.log.Calls())
	}
	// HIGH-1: the dependent's Detection was stale (blocked, no artifacts) when
	// its prerequisite resolved; it must still get its Configure.
	if h.log.Count("Configure topology") != 1 {
		t.Fatalf("resolved dependent skipped Configure: %v", h.log.Calls())
	}
}

func TestEngineBlockedPromptSkipBlocksDependents(t *testing.T) {
	t.Parallel()
	h := newEH(t, true)
	pr, tp := blockedPrereqs(h)
	h.ui.ExpectSelect("Prerequisites", 1) // skip; no work left, so no confirmation
	r := h.run(Inputs{}, pr, tp)
	wantExit(t, r, ExitOK)
	if o := outcome(t, r, "prereqs"); o.Outcome != OutcomeSkipped {
		t.Fatalf("%+v", o)
	}
	if o := outcome(t, r, "topology"); o.Outcome != OutcomeBlocked || o.Hard {
		t.Fatalf("%+v", o)
	}
}

func TestEngineBlockedPromptQuitExits130WithoutWrites(t *testing.T) {
	t.Parallel()
	h := newEH(t, true)
	pr, tp := blockedPrereqs(h)
	h.ui.ExpectSelect("Prerequisites", 2)
	r := h.run(Inputs{}, pr, tp)
	wantExit(t, r, ExitInterrupted)
	if w := h.nonLockWrites(); len(w) != 0 {
		t.Fatalf("writes before quit: %v", w)
	}
}

func TestEngineBlockedUnderYesNoPromptExit1(t *testing.T) {
	t.Parallel()
	h := newEH(t, false) // a prompt would fail the test
	pr, tp := blockedPrereqs(h)
	r := h.run(Inputs{Yes: true}, pr, tp)
	wantExit(t, r, ExitFailed)
	if o := outcome(t, r, "prereqs"); o.Outcome != OutcomeBlocked || !o.Hard || o.Remedy != "brew install libpq" {
		t.Fatalf("%+v", o)
	}
	if o := outcome(t, r, "topology"); o.Outcome != OutcomeBlocked || !o.Hard {
		t.Fatalf("%+v", o)
	}
}

func TestEngineCtrlCStopsBeforeNextAction(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"a/x": StateAbsent, "b/x": StateAbsent})
	ctx, cancel := context.WithCancel(context.Background())
	a := h.step("a", nil, "a/x")
	okApply := a.ApplyF
	a.ApplyF = func(n int, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
		res, err := okApply(n, wc, st, p)
		cancel() // Ctrl-C arrives while a is applying
		return res, err
	}
	b := h.step("b", nil, "b/x")
	r := h.engine(a, b).Run(ctx, Inputs{Yes: true})
	wantExit(t, r, ExitInterrupted)
	if h.log.Count("Apply b") != 0 {
		t.Fatalf("applied after Ctrl-C: %v", h.log.Calls())
	}
	if m := h.manifest(); len(m.Artifacts) != 1 || m.Artifacts[0].Step != "a" {
		t.Fatalf("manifest must hold the completed step only: %+v", m.Artifacts)
	}
}

func TestEngineSkipFlag(t *testing.T) { // AC-53
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"a/x": StateAbsent, "b/x": StateAbsent, "c/x": StateAbsent})
	a, b, c := h.step("a", nil, "a/x"), h.step("b", []string{"a"}, "b/x"), h.step("c", nil, "c/x")
	r := h.run(Inputs{Yes: true, Skip: []string{"a"}}, a, b, c)
	wantExit(t, r, ExitOK)
	if outcome(t, r, "a").Outcome != OutcomeSkipped || outcome(t, r, "b").Outcome != OutcomeBlocked || outcome(t, r, "c").Outcome != OutcomeApplied {
		t.Fatalf("%+v", r.Outcomes)
	}
	r2 := newEH(t, false).run(Inputs{Yes: true, Skip: []string{"nope"}}, h.step("a", nil, "a/x"))
	wantExit(t, r2, ExitUsage)
}

func TestEngineNonInteractiveWithoutYesExit2(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	r := h.run(Inputs{}, h.step("a", nil, "a/x"))
	wantExit(t, r, ExitUsage)
	if h.log.Count("Detect a") != 0 || h.log.Count("Seed a") != 0 {
		t.Fatalf("ran before the TTY check: %v", h.log.Calls())
	}
}

func TestEngineDowngradeDefaultsOutdatedToKeep(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	h.ver = "v1.1.0"
	if err := SaveManifest(h.fs, h.p, &Manifest{Schema: 1, BinaryVersion: "v1.2.0", ClaudeConfigDir: h.p.ClaudeDir}); err != nil {
		t.Fatal(err)
	}
	h.world = NewFakeWorld(map[string]State{"s/x": StateOutdated})
	r := h.run(Inputs{Yes: true}, h.step("s", nil, "s/x"))
	wantExit(t, r, ExitOK)
	if h.log.Count("Apply s") != 0 || !r.State.Downgrade || !hasNote(h.rep.Notes_, "older") {
		t.Fatalf("downgrade not honoured: %v notes %v", h.log.Calls(), h.rep.Notes_)
	}
}

func TestEngineDuplicateAndUnknownRequires(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	wantExit(t, h.run(Inputs{Yes: true}, h.step("a", nil, "a"), h.step("a", nil, "a")), ExitUsage)
	wantExit(t, newEH(t, false).run(Inputs{Yes: true}, h.step("b", []string{"zz"}, "b")), ExitUsage)
}

// afterDepSteps: a (absent, applied) and b (BlockedBy a until a is applied).
// bAfter says what b's Detect reports once a is applied.
func afterDepSteps(h *eh, bStillBlocked bool) (*FakeStep, *FakeStep) {
	h.world = NewFakeWorld(map[string]State{"a/x": StateAbsent, "b/x": StateAbsent})
	a := h.step("a", nil, "a/x")
	b := &FakeStep{StepID: "b", Req: []string{"a"}, Log: h.log, ApplyF: h.world.ApplyOK(),
		DetectF: func(int, *RunState) Detection {
			if bStillBlocked || h.world.Get("a/x") != StateOK {
				return Detection{State: StateBlocked, BlockedBy: "a", Detail: "needs a"}
			}
			st := h.world.Get("b/x")
			return Detection{State: st, Artifacts: []ArtifactState{{ID: "b/x", State: st}}}
		}}
	return a, b
}

func TestEngineApplyAfterDepRedetectsAbsentAndApplies(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	a, b := afterDepSteps(h, false)
	r := h.run(Inputs{Yes: true}, a, b)
	wantExit(t, r, ExitOK)
	if outcome(t, r, "a").Outcome != OutcomeApplied || outcome(t, r, "b").Outcome != OutcomeApplied {
		t.Fatalf("%+v", r.Outcomes)
	}
	if h.log.Index("Apply a") > h.log.Index("Apply b") || h.log.Count("Apply b") != 1 {
		t.Fatalf("b must apply after a: %v", h.log.Calls())
	}
	// b is planned only after a was applied (Plan b follows Apply a).
	if h.log.Count("Plan b") != 1 || h.log.Index("Plan b") < h.log.Index("Apply a") {
		t.Fatalf("b planned before a applied: %v", h.log.Calls())
	}
	// StepPlan.AfterDep and the empty-plan case: b has no concrete plan yet.
	if len(h.rep.Plans) != 1 || len(h.rep.Plans[0].Steps) != 2 {
		t.Fatalf("plans %+v", h.rep.Plans)
	}
	sp := h.rep.Plans[0].Steps[1]
	if sp.StepID != "b" || sp.AfterDep != "a" || len(sp.Plan.Actions) != 0 {
		t.Fatalf("b's StepPlan = %+v", sp)
	}
	if h.rep.Plans[0].Steps[0].AfterDep != "" {
		t.Fatalf("a's StepPlan = %+v", h.rep.Plans[0].Steps[0])
	}
}

func TestEngineApplyAfterDepStillBlockedIsHardBlock(t *testing.T) {
	t.Parallel()
	h := newEH(t, false)
	a, b := afterDepSteps(h, true)
	r := h.run(Inputs{Yes: true}, a, b)
	wantExit(t, r, ExitFailed)
	if outcome(t, r, "a").Outcome != OutcomeApplied {
		t.Fatalf("%+v", r.Outcomes)
	}
	if o := outcome(t, r, "b"); o.Outcome != OutcomeBlocked || !o.Hard {
		t.Fatalf("b = %+v", o)
	}
	if h.log.Count("Apply b") != 0 {
		t.Fatalf("blocked b was applied: %v", h.log.Calls())
	}
}

func TestResolveAfterDepAndLabel(t *testing.T) {
	t.Parallel()
	mk := func(id string, st State, by string, ch Choices) *stepRun {
		return &stepRun{step: &FakeStep{StepID: id}, det: Detection{State: st, BlockedBy: by}, choices: ch}
	}
	a := mk("a", StateAbsent, "", Choices{"a/x": ChoiceApply})
	b := mk("b", StateBlocked, "a", Choices{})
	c := mk("c", StateBlocked, "kept", Choices{})
	kept := mk("kept", StateAbsent, "", Choices{"kept/x": ChoiceKeep})
	s := &session{runs: []*stepRun{a, b, c, kept}, idx: map[string]*stepRun{"a": a, "b": b, "c": c, "kept": kept}}
	s.resolveAfterDep()
	if b.afterDep != "a" || c.afterDep != "" {
		t.Fatalf("afterDep b=%q c=%q", b.afterDep, c.afterDep)
	}
	if got := s.choiceLabel(b); got != "apply (after a)" {
		t.Fatalf("label %q", got)
	}
	if got := s.choiceLabel(c); got != "blocked" {
		t.Fatalf("label %q", got)
	}
	// A failed or user-skipped prerequisite does not unblock.
	a.failed = true
	s.resolveAfterDep()
	if b.afterDep != "" {
		t.Fatalf("afterDep on a failed prerequisite: %q", b.afterDep)
	}
	a.failed, a.userSkip = false, true
	s.resolveAfterDep()
	if b.afterDep != "" {
		t.Fatalf("afterDep on a skipped prerequisite: %q", b.afterDep)
	}
	a.userSkip = false
	s.resolveAfterDep()
	rows := s.rows([]*stepRun{b})
	if len(rows) != 1 || rows[0].Choice != "apply (after a)" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestEngineKeptAbsentPrerequisiteSoftBlocksDependents(t *testing.T) { // MED-2
	t.Parallel()
	h := newEH(t, true)
	h.world = NewFakeWorld(map[string]State{"a/x": StateAbsent, "b/x": StateAbsent})
	a, b := h.step("a", nil, "a/x"), h.step("b", []string{"a"}, "b/x")
	h.ui.ExpectSelect("a [absent]", 1).ExpectSelect("b [absent]", 0).ExpectConfirm("Apply", true)
	r := h.run(Inputs{}, a, b)
	wantExit(t, r, ExitOK) // soft block, like a user skip
	if o := outcome(t, r, "b"); o.Outcome != OutcomeBlocked || o.Hard {
		t.Fatalf("b = %+v", o)
	}
	if h.log.Count("Apply a") != 0 || h.log.Count("Apply b") != 0 {
		t.Fatalf("applied: %v", h.log.Calls())
	}
}

func TestEngineRuleAEnvKeyAnswerOverridesKeptModifiedKey(t *testing.T) { // MED-3
	t.Parallel()
	h := newEH(t, true)
	var planned Choices
	done := false
	env := &FakeStep{StepID: "envfile", Log: h.log,
		DetectF: func(int, *RunState) Detection {
			s := StateModified // the user hand-edited the DSN line, before and after the answer
			if done {
				s = StateOK
			}
			return Detection{State: s, Artifacts: []ArtifactState{{ID: "envfile/MEMORY_PG_DSN", State: s, Detail: "edited"}}}
		},
		PlanF: func(_ *RunState, ch Choices) (Plan, error) { planned = ch; return Plan{}, nil },
		ApplyF: func(int, WritePorts, *RunState, Plan) (StepResult, error) {
			done = true
			return StepResult{}, nil
		}}
	db := &FakeStep{StepID: "database", Log: h.log, ConfigureF: func(_ Prompter, st *RunState) error {
		st.DB.Set(DBTarget{Host: "new"}, SourcePrompt)
		return nil
	}}
	h.ui.ExpectSelect("envfile", 0).ExpectConfirm("overwrite your modified", false). // keep the hand edit ...
												ExpectConfirm("Apply", true) // ... then the new answer wins, with no second overwrite Confirm
	r := h.run(Inputs{Reconfigure: true}, env, db)
	wantExit(t, r, ExitOK)
	if planned["envfile/MEMORY_PG_DSN"] != ChoiceApply || h.log.Count("Apply envfile") != 1 {
		t.Fatalf("planned %v, log %v", planned, h.log.Calls())
	}
}

func TestEnvKeyAnsweredRequiresEnvfilePrefix(t *testing.T) { // LOW-2
	t.Parallel()
	ans := map[string]bool{"DB": true}
	for id, want := range map[string]bool{
		"envfile/MEMORY_PG_DSN":   true,
		"envfile/MEMORY_OTHER":    false,
		"other/MEMORY_PG_DSN":     false,
		"a/envfile/MEMORY_PG_DSN": false,
		"MEMORY_PG_DSN":           false,
	} {
		if got := envKeyAnswered(id, ans); got != want {
			t.Errorf("envKeyAnswered(%q) = %v, want %v", id, got, want)
		}
	}
	if envKeyAnswered("envfile/MEMORY_PG_DSN", map[string]bool{}) {
		t.Error("unanswered field must not match")
	}
}

func TestEngineDryRunStepFailureStillExits0(t *testing.T) { // LOW-1: dry-run stops at the plan with exit 0
	t.Parallel()
	h := newEH(t, false)
	h.world = NewFakeWorld(map[string]State{"p/x": StateAbsent, "c/x": StateAbsent})
	pf := h.step("planfail", nil, "p/x")
	pf.PlanF = func(*RunState, Choices) (Plan, error) { return Plan{}, errors.New("cannot plan") }
	cf := h.step("cfgfail", nil, "c/x")
	cf.ConfigureF = func(Prompter, *RunState) error { return errors.New("pass --x") }
	r := h.run(Inputs{DryRun: true, Yes: true}, pf, cf)
	wantExit(t, r, ExitOK)
	if outcome(t, r, "planfail").Outcome != OutcomeFailed || outcome(t, r, "cfgfail").Outcome != OutcomeFailed {
		t.Fatalf("failures must still be visible in the outcomes: %+v", r.Outcomes)
	}
	// The same failures outside dry-run exit 1.
	h2 := newEH(t, false)
	h2.world = NewFakeWorld(map[string]State{"p/x": StateAbsent})
	pf2 := h2.step("planfail", nil, "p/x")
	pf2.PlanF = func(*RunState, Choices) (Plan, error) { return Plan{}, errors.New("cannot plan") }
	wantExit(t, h2.run(Inputs{Yes: true}, pf2), ExitFailed)
}

func TestEngineCtrlCAtChoicePromptExits130(t *testing.T) { // LOW-7
	t.Parallel()
	h := newEH(t, true)
	h.world = NewFakeWorld(map[string]State{"s/x": StateAbsent})
	h.ui.ExpectSelectErr("s [absent]", ErrInterrupted)
	r := h.run(Inputs{}, h.step("s", nil, "s/x"))
	wantExit(t, r, ExitInterrupted)
	if h.log.Count("Apply s") != 0 || len(h.nonLockWrites()) != 0 {
		t.Fatalf("applied/wrote after Ctrl-C: %v %v", h.log.Calls(), h.nonLockWrites())
	}
}

func TestEngineCtrlCAtConfirmExits130(t *testing.T) { // LOW-7
	t.Parallel()
	h := newEH(t, true)
	h.world = NewFakeWorld(map[string]State{"s/x": StateAbsent})
	h.ui.ExpectSelect("s [absent]", 0).ExpectConfirmErr("Apply", ErrInterrupted)
	r := h.run(Inputs{}, h.step("s", nil, "s/x"))
	wantExit(t, r, ExitInterrupted)
	if r.Declined || h.log.Count("Apply s") != 0 || len(h.nonLockWrites()) != 0 {
		t.Fatalf("applied/wrote after Ctrl-C: %v %v", h.log.Calls(), h.nonLockWrites())
	}
}
