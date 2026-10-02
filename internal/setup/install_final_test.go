package setup

import (
	"bytes"
	"context"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"claude-memory/integration"
)

// Cross-cutting install tests (WI-S2-14b): a full 2a run over fakes. FakeFS
// on a temp root, a strict FakeRunner, a stateful database and Ollama.
// Nothing touches the real HOME, ~/.claude, ~/.local/bin or launchd.

const sentinel = "S3ntinel-pw-$@:/x"

// statefulDB is healthy once Migrate has run; armed makes every Probe fail
// (a database that goes away after the install steps, seen only by the
// final doctor).
type statefulDB struct {
	*scriptDB
	armed atomic.Bool
}

func newStatefulDB() *statefulDB {
	d := &statefulDB{scriptDB: &scriptDB{}}
	behind := DBStatus{Connected: true, VectorVersion: "0.8.0",
		Migrations: []MigrationStatus{{ID: "0001"}, {ID: "0002", Missing: []string{"records.namespace"}}}}
	d.fn = func(DBTarget) (DBStatus, error) {
		if len(d.migrated) > 0 {
			return healthy, nil
		}
		return behind, nil
	}
	return d
}

func (d *statefulDB) Probe(ctx context.Context, dsn string) (DBStatus, error) {
	if d.armed.Load() {
		return DBStatus{ErrorClass: DBErrUnreachable}, context.DeadlineExceeded
	}
	return d.scriptDB.Probe(ctx, dsn)
}

// armStep is a test-only step placed before the doctor: its Apply makes the
// database unreachable, so only the final doctor sees the failure.
type armStep struct{ db *statefulDB }

func (armStep) ID() string         { return "arm" }
func (armStep) Title() string      { return "Arm" }
func (armStep) Requires() []string { return nil }
func (armStep) Plan(context.Context, ReadPorts, *RunState, Choices) (Plan, error) {
	return Plan{}, nil
}
func (a armStep) Detect(context.Context, ReadPorts, *RunState) Detection {
	if a.db.armed.Load() {
		return Detection{State: StateOK}
	}
	return Detection{State: StateAbsent}
}
func (a armStep) Apply(context.Context, WritePorts, *RunState, Plan) (StepResult, error) {
	a.db.armed.Store(true)
	return StepResult{}, nil
}

// tamperStep is a test-only step whose Apply runs fn once.
type tamperStep struct{ fn func() }

func (tamperStep) ID() string         { return "tamper" }
func (tamperStep) Title() string      { return "Tamper" }
func (tamperStep) Requires() []string { return nil }
func (tamperStep) Plan(context.Context, ReadPorts, *RunState, Choices) (Plan, error) {
	return Plan{}, nil
}
func (tamperStep) Detect(context.Context, ReadPorts, *RunState) Detection {
	return Detection{State: StateAbsent}
}
func (t tamperStep) Apply(context.Context, WritePorts, *RunState, Plan) (StepResult, error) {
	t.fn()
	return StepResult{}, nil
}

type fullRig struct {
	t      *testing.T
	p      Paths
	root   string
	fs     *FakeFS
	runner *FakeRunner
	claude *fakeClaude
	db     *statefulDB
	oll    *scriptOllama
	red    *Redactor
	out    *bytes.Buffer
	arm    bool // insert armStep before the doctor
	// tamper, when set, runs as a step placed right before hooks.settings
	// (after migrate): a change made after the user confirmed the plan.
	tamper func()
	// ui scripts an interactive session; nil = non-interactive (--yes).
	ui *FakePrompter
}

func newFullRig(t *testing.T) *fullRig {
	t.Helper()
	p := testPaths(t)
	root := filepath.Dir(p.Home)
	p.Self = filepath.Join(root, "build", "claude-memory")
	if err := os.MkdirAll(filepath.Dir(p.Self), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Self, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewFakeRunner(t)
	for _, tool := range []string{"git", "psql", "claude", "ollama"} {
		r.SetPath(tool, "/usr/bin/"+tool)
	}
	// The only command a 2a remote run may issue is the quarantine probe the
	// binary step makes on the installed copy; any other command fails the
	// test (strict FakeRunner: unscripted = error) and is checked against the
	// AC-18 deny list.
	r.Script(ArgvPrefix("xattr"), Result{ExitCode: 1})
	oll := newOllama()
	oll.hasModel = false
	return &fullRig{t: t, p: p, root: root, fs: NewFakeFS(t, root), runner: r, claude: newFakeClaude(t, p), db: newStatefulDB(), oll: oll,
		red: NewRedactor(), out: &bytes.Buffer{}}
}

func (r *fullRig) inputs(extra ...func(*Inputs)) Inputs {
	in := Inputs{Yes: true, Topology: "remote", PGPassword: sentinel,
		PGDSN:     "postgresql://claude_memory@db.example.com:5432/claude_memory?sslmode=require",
		OllamaURL: "http://ollama.example.com:11434", Namespaces: []string{"work=~/work/**"},
		Env: Env{"PATH": "/usr/bin:" + filepath.Dir(r.p.InstalledBinary())}}
	for _, f := range extra {
		f(&in)
	}
	return in
}

func (r *fullRig) run(in Inputs) RunResult {
	r.t.Helper()
	r.red.Register(sentinel)
	rend := NewRenderer(r.out, r.red, RenderOptions{})
	plat := PlatformInfo{OS: OSLinux, Arch: "amd64", JobsBackend: JobsNone}
	clk := NewFakeClock(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	rp := ReadPorts{FS: r.fs, Runner: r.runner, DB: r.db, Ollama: r.oll, Clock: clk, Paths: r.p, Env: in.Env,
		Platform: plat, Assets: integration.FS}
	steps := InstallSteps("v1.2.0", r.red)
	if r.arm {
		steps = slices.Insert(steps, len(steps)-1, Step(armStep{r.db}))
	}
	if r.tamper != nil {
		i := slices.IndexFunc(steps, func(s Step) bool { return s.ID() == "hooks.settings" })
		steps = slices.Insert(steps, i, Step(tamperStep{r.tamper}))
	}
	wp := WritePorts{ReadPorts: rp, FS: r.fs, Runner: r.runner, DB: r.db, Ollama: r.oll, ClaudeCLI: r.claude, Progress: rend.Progress}
	ui := r.ui
	if ui == nil {
		ui = NewFakePrompter(r.t, false)
	}
	e := &Engine{Steps: steps, Read: rp, Write: wp, UI: ui, Reporter: rend,
		Version: "v1.2.0", KnownIDs: AllStepIDs}
	res := e.Run(context.Background(), in)
	rend.Summary(res)
	return res
}

func (r *fullRig) nonLockWrites() int {
	n := 0
	for _, w := range r.fs.Writes() {
		if !strings.HasPrefix(w, "lock ") {
			n++
		}
	}
	return n
}

func (r *fullRig) pulls() int {
	n := 0
	for _, c := range r.oll.calls {
		if strings.HasPrefix(c, "pull") {
			n++
		}
	}
	return n
}

func outcomeOf(res RunResult, id string) Outcome {
	for _, o := range res.Outcomes {
		if o.StepID == id {
			return o.Outcome
		}
	}
	return ""
}

func (r *fullRig) mustConverge() {
	r.t.Helper()
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		r.t.Fatalf("first run: exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	r.out.Reset()
}

// AC-30: the sentinel password, in every encoding, reaches only the env file,
// bootstrap.sql and the env backup; no other written file, the output or a
// command.
func TestFullRunSentinel(t *testing.T) {
	r := newFullRig(t)
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	forms := []string{sentinel, url.QueryEscape(sentinel), url.PathEscape(sentinel), url.UserPassword("", sentinel).String()[1:]}
	holders := map[string]bool{}
	_ = filepath.WalkDir(r.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, f := range forms {
			if !bytes.Contains(b, []byte(f)) {
				continue
			}
			if p == r.p.EnvFile() || p == r.p.Bootstrap() || strings.HasPrefix(filepath.Base(p), "env.bak.claude-memory.") {
				holders[p] = true
				continue
			}
			t.Errorf("password form %q found in %s", f, p)
		}
		return nil
	})
	if !holders[r.p.EnvFile()] {
		t.Error("the env file should hold the password (sanity)")
	}
	for _, f := range forms {
		if strings.Contains(r.out.String(), f) {
			t.Errorf("password form %q in the output", f)
		}
		for _, c := range r.runner.Calls() {
			if strings.Contains(strings.Join(c.Argv, " ")+strings.Join(c.Env, " ")+string(c.Stdin), f) {
				t.Errorf("password form %q in a command: %v", f, c.Argv)
			}
		}
		for _, c := range r.claude.Calls() {
			if strings.Contains(c, f) {
				t.Errorf("password form %q in a claude mcp call: %s", f, c)
			}
		}
	}
	if !strings.Contains(r.out.String(), "claude-memory doctor") {
		t.Errorf("the final doctor report is missing:\n%s", r.out)
	}
}

// AC-50 and AC-64: a re-run over a converged install does nothing: 0 writes
// (the lock file aside), 0 mutating commands, the manifest untouched, and every
// 2a step reports unchanged, which is the run-twice check for all of them; the
// doctor still runs and the summary counts nothing as applied.
func TestNoOpRerun(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	manifest, _ := os.ReadFile(r.p.Manifest())
	info, _ := os.Stat(r.p.Manifest())
	writes, mut, migrated, pulled := r.nonLockWrites(), r.runner.MutatingCalls(), len(r.db.migrated), r.pulls()
	claudeCalls := len(r.claude.Calls())

	res := r.run(r.inputs())
	if res.ExitCode != ExitOK {
		t.Fatalf("re-run: exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	if n := r.nonLockWrites() - writes; n != 0 {
		t.Errorf("re-run attempted %d writes, want 0: %v", n, r.fs.Writes()[len(r.fs.Writes())-n:])
	}
	if n := r.runner.MutatingCalls() - mut; n != 0 {
		t.Errorf("re-run ran %d mutating commands, want 0", n)
	}
	if got, _ := os.ReadFile(r.p.Manifest()); !bytes.Equal(got, manifest) {
		t.Error("manifest changed on a no-op re-run")
	}
	if info2, _ := os.Stat(r.p.Manifest()); !info2.ModTime().Equal(info.ModTime()) {
		t.Error("manifest was rewritten")
	}
	if len(r.db.migrated) != migrated || r.pulls() != pulled {
		t.Error("re-run migrated or pulled again")
	}
	if n := len(r.claude.Calls()); n != claudeCalls {
		t.Errorf("re-run made %d claude calls, want 0 (an ok registration needs none, AC-40)", n-claudeCalls)
	}
	for _, id := range []string{"platform", "binary", "prereqs", "topology", "envfile", "database", "migrate", "ollama", "namespaces",
		"hooks.scripts", "hooks.settings", "mcp", "skills", "claude-md"} {
		if o := outcomeOf(res, id); o != OutcomeUnchanged {
			t.Errorf("%s: outcome %q on a no-op re-run, want unchanged", id, o)
		}
	}
	if !strings.Contains(r.out.String(), "claude-memory doctor") {
		t.Error("the doctor must run on a no-op re-run")
	}
	if !strings.Contains(r.out.String(), "Done: 15 unchanged\n") {
		t.Errorf("want the summary \"Done: 15 unchanged\" (nothing applied):\n%s", r.out)
	}
}

// AC-12 / AC-52: --yes never overwrites a hand-edited env key; --upgrade and
// --yes produce identical output.
func TestYesKeepsModifiedAndUpgradeEqualsYes(t *testing.T) {
	flow := func(mod func(*Inputs)) string {
		r := newFullRig(t)
		r.mustConverge()
		b, _ := os.ReadFile(r.p.EnvFile())
		// An `export` line is a format finding the binary does not read: the
		// envfile/format artifact becomes `modified` and --yes keeps it.
		edited := append(slices.Clone(b), []byte("export FOO=bar\n")...)
		if err := os.WriteFile(r.p.EnvFile(), edited, 0o600); err != nil {
			t.Fatal(err)
		}
		res := r.run(r.inputs(mod))
		if res.ExitCode != ExitOK {
			t.Fatalf("second run: %d %v\n%s", res.ExitCode, res.Err, r.out)
		}
		if after, _ := os.ReadFile(r.p.EnvFile()); !bytes.Equal(after, edited) {
			t.Errorf("--yes overwrote the modified env file:\n%s", after)
		}
		return strings.ReplaceAll(r.out.String(), r.root, "$ROOT")
	}
	yes := flow(func(in *Inputs) { in.Yes = true })
	upgrade := flow(func(in *Inputs) { in.Yes, in.Upgrade = true, true })
	if yes != upgrade {
		t.Errorf("--upgrade output differs from --yes\n--- yes ---\n%s\n--- upgrade ---\n%s", yes, upgrade)
	}
	if !strings.Contains(yes, "modified  keep") {
		t.Errorf("the modified env file is not shown as kept:\n%s", yes)
	}
}

// AC-62: a kept modified env file whose DSN then fails pg.connect counts and
// exits 1; kept is not exempt.
func TestKeptModifiedEnvFileDoctorFailExits1(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	b, _ := os.ReadFile(r.p.EnvFile())
	edited := append(slices.Clone(b), []byte("export FOO=bar\n")...)
	if err := os.WriteFile(r.p.EnvFile(), edited, 0o600); err != nil {
		t.Fatal(err)
	}
	r.arm = true
	res := r.run(r.inputs())
	out := r.out.String()
	if res.ExitCode != ExitFailed {
		t.Fatalf("exit %d, want 1\n%s", res.ExitCode, out)
	}
	if !strings.Contains(out, "FAIL  pg.connect") {
		t.Errorf("pg.connect must fail:\n%s", out)
	}
	if strings.Contains(out, "not installed: envfile") || strings.Contains(out, "not installed: database") {
		t.Errorf("a kept artifact must not be exempt:\n%s", out)
	}
	if got, _ := os.ReadFile(r.p.EnvFile()); !bytes.Equal(got, edited) {
		t.Error("the modified env file was not kept")
	}
}

// AC-62: --skip mcp (an id this build does not register, accepted through
// KnownIDs) prints mcp.registered as not installed with exit 0, and --skip of
// the registered database step exempts pg.connect.
func TestSkippedStepFailIsNotInstalled(t *testing.T) {
	r := newFullRig(t)
	res := r.run(r.inputs(func(in *Inputs) { in.Skip = []string{"mcp"} }))
	out := r.out.String()
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d, want 0\n%s", res.ExitCode, out)
	}
	if !strings.Contains(out, "mcp.registered") || !strings.Contains(out, "not installed: mcp skipped") {
		t.Errorf("mcp.registered must print as not installed:\n%s", out)
	}

	r = newFullRig(t)
	r.mustConverge()
	r.arm = true
	res = r.run(r.inputs(func(in *Inputs) { in.Skip = []string{"database"} }))
	out = r.out.String()
	if res.ExitCode != ExitOK {
		t.Fatalf("--skip database: exit %d, want 0\n%s", res.ExitCode, out)
	}
	if !strings.Contains(out, "not installed: database skipped") {
		t.Errorf("pg.connect must print as not installed (database skipped):\n%s", out)
	}
}

// --no-doctor prints no doctor report; --dry-run never reaches the final
// doctor and writes nothing.
func TestNoDoctorAndDryRun(t *testing.T) {
	r := newFullRig(t)
	res := r.run(r.inputs(func(in *Inputs) { in.NoDoctor = true }))
	if res.ExitCode != ExitOK || strings.Contains(r.out.String(), "claude-memory doctor") {
		t.Errorf("--no-doctor: exit %d, output:\n%s", res.ExitCode, r.out)
	}

	r = newFullRig(t)
	res = r.run(r.inputs(func(in *Inputs) { in.DryRun = true }))
	if res.ExitCode != ExitOK || strings.Contains(r.out.String(), "claude-memory doctor") {
		t.Errorf("--dry-run: exit %d, output:\n%s", res.ExitCode, r.out)
	}
	if n := r.nonLockWrites(); n != 0 {
		t.Errorf("--dry-run wrote %d times", n)
	}
}

// Every doctor check has an owning step in DoctorCheckSteps, and every owner
// is a step id of the AC-7 pipeline.
func TestDoctorCheckStepMapComplete(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	d := newDoctor(DoctorDeps{Paths: p, FS: NewFakeFS(t, filepath.Dir(p.Home)), Runner: NewFakeRunner(t)})
	seen := map[string]bool{}
	for _, c := range d.checks() {
		seen[c.ID] = true
		owner, ok := DoctorCheckSteps[c.ID]
		if !ok {
			if !slices.Contains(unownedChecks, c.ID) {
				t.Errorf("doctor check %q has no owning step in DoctorCheckSteps (nor in unownedChecks)", c.ID)
			}
		} else if !slices.Contains(AllStepIDs, owner) {
			t.Errorf("check %q: owner %q is not a pipeline step", c.ID, owner)
		}
	}
	for _, id := range unownedChecks {
		if _, mapped := DoctorCheckSteps[id]; mapped || !seen[id] {
			t.Errorf("unowned check %q must be a real check and not mapped", id)
		}
	}
	for id := range DoctorCheckSteps {
		if !seen[id] {
			t.Errorf("DoctorCheckSteps names unknown check %q", id)
		}
	}
}

// The restart line is requested when this run applied any change.
func TestFinalRestartLine(t *testing.T) {
	t.Parallel()
	r := newFullRig(t)
	rp := ReadPorts{FS: r.fs, Runner: r.runner, DB: r.db, Ollama: r.oll, Clock: NewFakeClock(time.Now()), Paths: r.p,
		Env: Env{}, Platform: PlatformInfo{OS: OSLinux, Arch: "amd64"}, Assets: integration.FS}
	for applied, want := range map[string]bool{"": false, "namespaces": true, "hooks.settings": true, "mcp": true} {
		st := NewRunState(Inputs{})
		if applied != "" {
			st.Applied[applied] = true
		}
		fr := (DoctorStep{Version: "v1"}).Final(context.Background(), rp, st, FinalView{})
		if !fr.Ran || fr.Restart != want {
			t.Errorf("applied %q: Ran=%v Restart=%v, want restart %v", applied, fr.Ran, fr.Restart, want)
		}
	}
}

// finalStep is a fake Finalizer that records the FinalView the engine hands it.
type finalStep struct {
	*FakeStep
	view   FinalView
	called bool
}

func (f *finalStep) Final(_ context.Context, _ ReadPorts, _ *RunState, fv FinalView) FinalResult {
	f.view, f.called = fv, true
	return FinalResult{Ran: true}
}

// TestFinalViewExemptions drives finalView/skippedRoot through the engine:
// only a step the user skipped (and what that soft-blocks) and unregistered
// ids are "not installed"; kept, failed and --yes-blocked steps are not.
func TestFinalViewExemptions(t *testing.T) {
	absent := func(h *eh) { h.world = NewFakeWorld(map[string]State{"alpha/x": StateAbsent, "beta/x": StateAbsent}) }
	blockedAlpha := func(*RunState) Detection {
		return Detection{State: StateBlocked, Detail: "tool missing", Remedy: "install it"}
	}
	cases := []struct {
		name     string
		in       Inputs
		prompts  func(*FakePrompter)
		alphaDet func(*RunState) Detection // nil: the world's
		betaDet  func(*RunState) Detection
		want     map[string]string
		exit     int
	}{
		{name: "--skip of a registered step", in: Inputs{Yes: true, Skip: []string{"alpha"}},
			want: map[string]string{"alpha": "alpha", "beta": "alpha", "mcp": "mcp"}, exit: ExitOK},
		{name: "interactive skip", in: Inputs{},
			prompts: func(p *FakePrompter) {
				p.ExpectSelect("alpha", 2).ExpectSelect("beta", 0).ExpectConfirm("Apply", true)
			},
			want: map[string]string{"alpha": "alpha", "beta": "alpha", "mcp": "mcp"}, exit: ExitOK},
		{name: "three bad answers skip", in: Inputs{},
			prompts: func(p *FakePrompter) {
				p.ExpectSelectErr("alpha", ErrTooManyAttempts).ExpectSelect("beta", 0).ExpectConfirm("Apply", true)
			},
			want: map[string]string{"alpha": "alpha", "beta": "alpha", "mcp": "mcp"}, exit: ExitOK},
		{name: "blocked-step prompt skip", in: Inputs{}, alphaDet: blockedAlpha,
			prompts: func(p *FakePrompter) {
				p.ExpectSelect("beta", 0).ExpectSelect("alpha is blocked", 1).ExpectConfirm("Apply", true)
			},
			want: map[string]string{"alpha": "alpha", "beta": "alpha", "mcp": "mcp"}, exit: ExitOK},
		{name: "blocked under --yes is not exempt", in: Inputs{Yes: true}, alphaDet: blockedAlpha,
			want: map[string]string{"mcp": "mcp"}, exit: ExitFailed},
		{name: "kept while absent is not exempt", in: Inputs{},
			prompts: func(p *FakePrompter) {
				p.ExpectSelect("alpha", 1).ExpectSelect("beta", 0).ExpectConfirm("Apply", true)
			},
			want: map[string]string{"mcp": "mcp"}, exit: ExitOK},
		{name: "blocked by a kept-absent prerequisite is soft but not exempt", in: Inputs{},
			betaDet: func(*RunState) Detection {
				return Detection{State: StateBlocked, BlockedBy: "alpha", Detail: "needs alpha"}
			},
			prompts: func(p *FakePrompter) { p.ExpectSelect("alpha", 1) },
			want:    map[string]string{"mcp": "mcp"}, exit: ExitOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newEH(t, c.in.Yes == false)
			absent(h)
			alpha, beta := h.step("alpha", nil, "alpha/x"), h.step("beta", []string{"alpha"}, "beta/x")
			if c.alphaDet != nil {
				alpha.DetectF = func(_ int, st *RunState) Detection { return c.alphaDet(st) }
			}
			if c.betaDet != nil {
				beta.DetectF = func(_ int, st *RunState) Detection { return c.betaDet(st) }
			}
			fin := &finalStep{FakeStep: &FakeStep{StepID: "doctor", Log: h.log}}
			if c.prompts != nil {
				c.prompts(h.ui)
			}
			e := h.engine()
			e.Steps, e.KnownIDs = []Step{alpha, beta, fin}, []string{"mcp"}
			res := e.Run(context.Background(), c.in)
			wantExit(t, res, c.exit)
			if !fin.called {
				t.Fatal("the final step did not run")
			}
			if !maps.Equal(fin.view.NotInstalled, c.want) {
				t.Errorf("NotInstalled = %v, want %v", fin.view.NotInstalled, c.want)
			}
		})
	}
}

// Ctrl-C during the final doctor exits 130 and reports nothing.
func TestFinalPhaseInterrupted(t *testing.T) {
	h := newEH(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	fin := &cancelFinal{finalStep: &finalStep{FakeStep: &FakeStep{StepID: "doctor", Log: h.log}}, cancel: cancel}
	e := h.engine()
	e.Steps = []Step{fin}
	res := e.Run(ctx, Inputs{Yes: true})
	wantExit(t, res, ExitInterrupted)
	for _, o := range h.rep.Outcomes {
		if o.StepID == "doctor" {
			t.Errorf("doctor reported after Ctrl-C: %+v", o)
		}
	}
}

type cancelFinal struct {
	*finalStep
	cancel func()
}

func (c *cancelFinal) Final(ctx context.Context, rc ReadPorts, st *RunState, fv FinalView) FinalResult {
	c.cancel()
	return c.finalStep.Final(ctx, rc, st, fv)
}
