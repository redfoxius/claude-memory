package setup

import (
	"bytes"
	"context"
	"io/fs"
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

type fullRig struct {
	t      *testing.T
	p      Paths
	root   string
	fs     *FakeFS
	runner *FakeRunner
	db     *statefulDB
	oll    *scriptOllama
	red    *Redactor
	out    *bytes.Buffer
	arm    bool // insert armStep before the doctor
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
	r.Script(func(Cmd) bool { return true }, Result{})
	oll := newOllama()
	oll.hasModel = false
	return &fullRig{t: t, p: p, root: root, fs: NewFakeFS(t, root), runner: r, db: newStatefulDB(), oll: oll,
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
	wp := WritePorts{ReadPorts: rp, FS: r.fs, Runner: r.runner, DB: r.db, Ollama: r.oll, Progress: rend.Progress}
	e := &Engine{Steps: steps, Read: rp, Write: wp, UI: NewFakePrompter(r.t, false), Reporter: rend,
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
	holders := 0
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
				holders++
				continue
			}
			t.Errorf("password form %q found in %s", f, p)
		}
		return nil
	})
	if holders == 0 {
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
	}
	if !strings.Contains(r.out.String(), "claude-memory doctor") {
		t.Errorf("the final doctor report is missing:\n%s", r.out)
	}
}

// AC-50 and AC-64: a re-run over a converged install does nothing: 0 writes
// (the lock file aside), 0 mutating commands, the manifest untouched, every
// step unchanged (so also run-twice idempotent); the doctor still runs.
func TestNoOpRerun(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	manifest, _ := os.ReadFile(r.p.Manifest())
	info, _ := os.Stat(r.p.Manifest())
	writes, mut, migrated, pulled := r.nonLockWrites(), r.runner.MutatingCalls(), len(r.db.migrated), r.pulls()

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
	for _, id := range []string{"platform", "binary", "prereqs", "topology", "envfile", "database", "migrate", "ollama", "namespaces"} {
		if o := outcomeOf(res, id); o != OutcomeUnchanged {
			t.Errorf("%s: outcome %q on a no-op re-run, want unchanged", id, o)
		}
	}
	if !strings.Contains(r.out.String(), "claude-memory doctor") {
		t.Error("the doctor must run on a no-op re-run")
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

// AC-62: --skip mcp prints mcp.registered as not installed with exit 0, and
// --skip database exempts pg.connect.
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
			t.Errorf("doctor check %q has no owning step in DoctorCheckSteps", c.ID)
		} else if !slices.Contains(AllStepIDs, owner) {
			t.Errorf("check %q: owner %q is not a pipeline step", c.ID, owner)
		}
	}
	for id := range DoctorCheckSteps {
		if !seen[id] {
			t.Errorf("DoctorCheckSteps names unknown check %q", id)
		}
	}
}

// The restart line is requested when a Claude-integration step was applied.
func TestFinalRestartLine(t *testing.T) {
	t.Parallel()
	r := newFullRig(t)
	rp := ReadPorts{FS: r.fs, Runner: r.runner, DB: r.db, Ollama: r.oll, Clock: NewFakeClock(time.Now()), Paths: r.p,
		Env: Env{}, Platform: PlatformInfo{OS: OSLinux, Arch: "amd64"}, Assets: integration.FS}
	for applied, want := range map[string]bool{"": false, "namespaces": false, "hooks.settings": true, "mcp": true} {
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
