package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"claude-memory/integration"
)

// ---- rendering (AC-44, AC-64) ---------------------------------------------

func TestRenderPlistGoldens(t *testing.T) {
	t.Parallel()
	p := Paths{Home: "/Users/test", LaunchAgentsDir: "/Users/test/Library/LaunchAgents", StateDir: "/Users/test/.local/state/claude-memory", UID: 501}
	l := LaunchdJobs{Paths: p, Assets: integration.FS}
	for _, j := range DefaultJobSpecs(p, "/Users/test/.local/bin/claude-memory", "/opt/homebrew/bin:/Users/test/.volta/bin:/usr/bin:/bin") {
		first, err := l.Render(j)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := l.Render(j)
		path := l.PlistPath(j)
		if !bytes.Equal(first[path], again[path]) {
			t.Errorf("%s: render is not deterministic", j.Name)
		}
		if len(first) != 1 || filepath.Dir(path) != p.LaunchAgentsDir {
			t.Errorf("%s: files %v", j.Name, first)
		}
		checkGolden(t, filepath.Join("testdata", "launchd", j.Name+".plist"), first[path])
		if _, err := parsePlist(first[path]); err != nil {
			t.Errorf("%s: rendered plist does not parse: %v", j.Name, err)
		}
	}
}

func TestRenderPlistEscapesXML(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	l := LaunchdJobs{Paths: p, Assets: integration.FS}
	j := DefaultJobSpecs(p, "/opt/a&b/<claude-memory>", "/opt/a&b:/usr/bin")[0]
	files, err := l.Render(j)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := parsePlist(files[l.PlistPath(j)])
	if err != nil {
		t.Fatal(err)
	}
	if got := pl.get("ProgramArguments").arr[0].str; got != "/opt/a&b/<claude-memory>" {
		t.Errorf("program round-trips as %q", got)
	}
	if got := pl.get("EnvironmentVariables").get("PATH").str; got != "/opt/a&b:/usr/bin" {
		t.Errorf("PATH round-trips as %q", got)
	}
}

func TestComputeJobPATH(t *testing.T) {
	t.Parallel()
	r := NewFakeRunner(t)
	r.SetPath("claude", "/Users/o/.volta/bin/claude").SetPath("git", "/usr/bin/git").SetPath("az", "/opt/homebrew/bin/az")
	if got, want := ComputeJobPATH(r), "/Users/o/.volta/bin:/usr/bin:/opt/homebrew/bin:/bin"; got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
	if got, want := ComputeJobPATH(NewFakeRunner(t)), "/usr/bin:/bin"; got != want {
		t.Errorf("no tools: PATH = %q, want %q", got, want)
	}
}

func TestRecordedJobHashes(t *testing.T) {
	t.Parallel()
	if recordedJobHashes(nil) != nil {
		t.Error("nil manifest must give nil")
	}
	m := &Manifest{Artifacts: []Artifact{
		{Step: "jobs", Kind: KindLaunchd, Path: "/a.plist", Identity: "a", SHA256: "h1"},
		{Step: "skills", Kind: KindFile, Path: "/f", SHA256: "h2"},
	}}
	if got := recordedJobHashes(m); len(got) != 1 || got["/a.plist"] != "h1" {
		t.Errorf("recordedJobHashes = %v", got)
	}
}

// ---- Install (launchctl argv, bootout tolerance) --------------------------

func TestLaunchdInstallArgvAndBootoutTolerance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		bootout Result
		wantErr string
	}{
		{"loaded job", Result{}, ""},
		{"exit 3 (measured on macOS 26.2)", Result{ExitCode: 3, Stderr: []byte("Boot-out failed: 3: No such process")}, ""},
		{"exit 113", Result{ExitCode: 113}, ""},
		{"stderr not loaded", Result{ExitCode: 1, Stderr: []byte("service is not loaded")}, ""},
		{"stderr No such process", Result{ExitCode: 5, Stderr: []byte("No such process")}, ""},
		{"real failure", Result{ExitCode: 5, Stderr: []byte("Input/output error")}, "bootout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := testPaths(t)
			fsys := NewFakeFS(t, filepath.Dir(p.Home))
			r := NewFakeRunner(t)
			j := DefaultJobSpecs(p, p.InstalledBinary(), "/usr/bin:/bin")[0]
			r.Script(ArgvEq("launchctl", "bootout", "gui/501/"+j.Label), tc.bootout)
			plist := filepath.Join(p.LaunchAgentsDir, j.Label+".plist")
			r.Script(ArgvEq("launchctl", "bootstrap", "gui/501", plist), Result{})
			m := LaunchdManager{LaunchdJobs: LaunchdJobs{FS: fsys, Runner: r, Paths: p, Assets: integration.FS}, Write: fsys}
			err := m.Install(context.Background(), j)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var argv [][]string
			for _, c := range r.Calls() {
				if !c.Mutating {
					t.Errorf("%v is not marked Mutating", c.Argv)
				}
				argv = append(argv, c.Argv)
			}
			want := [][]string{{"launchctl", "bootout", "gui/501/" + j.Label}, {"launchctl", "bootstrap", "gui/501", plist}}
			if !slices.EqualFunc(argv, want, slices.Equal) {
				t.Errorf("argv = %v, want %v", argv, want)
			}
			b, err := os.ReadFile(plist)
			if err != nil || !bytes.Contains(b, []byte("<string>"+j.Label+"</string>")) {
				t.Errorf("plist not written: %v", err)
			}
		})
	}
}

func TestLaunchdInstallBootstrapFailure(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	r := NewFakeRunner(t)
	r.Script(ArgvPrefix("launchctl", "bootout"), Result{ExitCode: 3})
	r.Script(ArgvPrefix("launchctl", "bootstrap"), Result{ExitCode: 5, Stderr: []byte("Bootstrap failed: 5: Input/output error")})
	m := LaunchdManager{LaunchdJobs: LaunchdJobs{FS: fsys, Runner: r, Paths: p, Assets: integration.FS}, Write: fsys}
	err := m.Install(context.Background(), DefaultJobSpecs(p, p.InstalledBinary(), "/usr/bin")[0])
	if err == nil || !strings.Contains(err.Error(), "bootstrap") || !strings.Contains(err.Error(), "Input/output error") {
		t.Errorf("err = %v", err)
	}
}

func TestLaunchdInstallReadOnlyRefused(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	fsys := NewFakeFS(t, filepath.Dir(p.Home))
	fsys.ReadOnly = true
	r := NewFakeRunner(t)
	r.ReadOnly = true
	m := LaunchdManager{LaunchdJobs: LaunchdJobs{FS: fsys, Runner: r, Paths: p, Assets: integration.FS}, Write: fsys}
	if err := m.Install(context.Background(), DefaultJobSpecs(p, p.InstalledBinary(), "/usr/bin")[0]); err == nil {
		t.Error("a read-only FS must refuse Install")
	}
	if len(r.Calls()) != 0 {
		t.Errorf("launchctl ran after the write was refused: %v", r.Calls())
	}
}

// ---- the jobs step over the full rig --------------------------------------

func (r *fullRig) plistPath(name string) string {
	return filepath.Join(r.p.LaunchAgentsDir, LaunchdLabelPrefix+name+".plist")
}

func (r *fullRig) launchctlMutating(sub string) int {
	n := 0
	for _, c := range r.runner.Calls() {
		if c.Mutating && len(c.Argv) > 1 && c.Argv[0] == "launchctl" && c.Argv[1] == sub {
			n++
		}
	}
	return n
}

func (r *fullRig) recordedJob(name string) (Artifact, bool) {
	r.t.Helper()
	ml, err := LoadManifest(r.fs, r.p)
	if err != nil {
		r.t.Fatal(err)
	}
	return ml.Manifest.Lookup(KindLaunchd, r.plistPath(name), LaunchdLabelPrefix+name)
}

func withPRRepos(in *Inputs) { in.PRRepos = "/work/api,/work/web" }

func TestJobsStepInstallsCleanupOnlyWithoutPRRepos(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	b, err := os.ReadFile(r.plistPath(JobCleanup))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(r.p.InstalledBinary())) || !bytes.Contains(b, []byte("<string>/usr/bin:/bin</string>")) {
		t.Errorf("plist lacks the binary or PATH:\n%s", b)
	}
	if _, err := os.Stat(r.plistPath(JobIngestPR)); err == nil {
		t.Error("ingest-pr installed without PR repos")
	}
	if !r.launchd.isLoaded(LaunchdLabelPrefix + JobCleanup) {
		t.Error("cleanup is not loaded")
	}
	a, ok := r.recordedJob(JobCleanup)
	if !ok || a.SHA256 != sha256Hex(b) || a.Step != "jobs" || a.Version != "v1.2.0" {
		t.Errorf("recorded artifact = %+v, %v", a, ok)
	}
	if _, ok := r.recordedJob(JobIngestPR); ok {
		t.Error("ingest-pr recorded")
	}
	if st, err := os.Stat(r.p.StateDir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("state dir: %v, %v", st, err)
	}
}

// AC-43 + AC-50: with --pr-repos both jobs are installed, the value reaches the
// env file only through the envfile step, and a re-run does nothing.
func TestJobsStepWithPRReposRunTwice(t *testing.T) {
	r := newFullRig(t)
	if res := r.run(r.inputs(withPRRepos)); res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	for _, n := range []string{JobCleanup, JobIngestPR} {
		if _, err := os.Stat(r.plistPath(n)); err != nil {
			t.Errorf("%s plist: %v", n, err)
		}
		if _, ok := r.recordedJob(n); !ok {
			t.Errorf("%s not recorded", n)
		}
	}
	env, _ := os.ReadFile(r.p.EnvFile())
	if !strings.Contains(string(env), "MEMORY_PR_INGEST_REPOS=/work/api,/work/web\n") {
		t.Errorf("env file lacks the PR repos:\n%s", env)
	}
	if !strings.Contains(r.out.String(), "Azure DevOps") {
		t.Errorf("no Azure-only note:\n%s", r.out)
	}
	writes, mut := r.nonLockWrites(), r.runner.MutatingCalls()
	r.out.Reset()
	// The second run reads PR repos from the env file, not the flag.
	res := r.run(r.inputs())
	if res.ExitCode != ExitOK {
		t.Fatalf("second run: %d %v\n%s", res.ExitCode, res.Err, r.out)
	}
	if n := r.nonLockWrites() - writes; n != 0 {
		t.Errorf("second run wrote %d times: %v", n, r.fs.Writes())
	}
	if n := r.runner.MutatingCalls() - mut; n != 0 {
		t.Errorf("second run ran %d mutating commands", n)
	}
	if o := outcomeOf(res, "jobs"); o != OutcomeUnchanged {
		t.Errorf("jobs outcome %q, want unchanged", o)
	}
}

func TestJobsStepSkips(t *testing.T) {
	for _, tc := range []struct {
		name  string
		linux bool
		mod   func(*Inputs)
		want  string
	}{
		{"--no-jobs", false, func(in *Inputs) { in.NoJobs = true }, "--no-jobs"},
		{"--jobs-backend none", false, func(in *Inputs) { in.JobsBackend = JobsNone }, "--jobs-backend none"},
		{"linux", true, nil, "macOS only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFullRig(t)
			r.linux = tc.linux
			var mods []func(*Inputs)
			if tc.mod != nil {
				mods = append(mods, tc.mod)
			}
			res := r.run(r.inputs(mods...))
			if res.ExitCode != ExitOK {
				t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
			}
			if o := outcomeOf(res, "jobs"); o != OutcomeSkipped {
				t.Errorf("jobs outcome %q, want skipped", o)
			}
			if !strings.Contains(r.out.String(), tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, r.out)
			}
			if _, err := os.Stat(r.p.LaunchAgentsDir); err == nil {
				t.Error("LaunchAgents was touched")
			}
			if n := r.launchctlMutating("bootstrap") + r.launchctlMutating("bootout"); n != 0 {
				t.Errorf("%d launchctl mutations", n)
			}
		})
	}
}

// AC-44: a hand-installed legacy wrapper plist is replaced under --yes.
func TestJobsStepReplacesLegacyPlist(t *testing.T) {
	r := newFullRig(t)
	legacy := strings.ReplaceAll(string(readTestdata(t, "testdata/fixtures/launchd/legacy-cleanup.plist")), "__HOME__", r.p.Home)
	writeFile(t, r.plistPath(JobCleanup), []byte(legacy), 0o644)
	r.launchd.loaded[LaunchdLabelPrefix+JobCleanup] = true
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	b, _ := os.ReadFile(r.plistPath(JobCleanup))
	if strings.Contains(string(b), "run-with-env.sh") || !bytes.Contains(b, []byte(r.p.InstalledBinary())) {
		t.Errorf("legacy plist not replaced:\n%s", b)
	}
	if r.launchctlMutating("bootout") != 1 || r.launchctlMutating("bootstrap") != 1 {
		t.Errorf("reload: bootout %d, bootstrap %d", r.launchctlMutating("bootout"), r.launchctlMutating("bootstrap"))
	}
	if matches, _ := filepath.Glob(r.plistPath(JobCleanup) + ".bak*"); len(matches) != 0 {
		t.Errorf("a legacy plist needs no backup: %v", matches)
	}
}

// AC-51: a hand install that equals the rendering is adopted: nothing is
// rewritten or reloaded, and the plist is recorded.
func TestJobsStepAdoptsMatchingPlist(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge() // creates the binary, env etc.; jobs installed
	// Forget the jobs: drop the manifest records of them, keep files + loaded.
	ml, _ := LoadManifest(r.fs, r.p)
	for _, a := range ml.Manifest.Find(KindLaunchd) {
		ml.Manifest.Drop(a.Key())
	}
	if err := SaveManifest(r.fs, r.p, ml.Manifest); err != nil {
		t.Fatal(err)
	}
	boot := r.launchctlMutating("bootstrap")
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	if r.launchctlMutating("bootstrap") != boot {
		t.Error("an ok plist was reloaded")
	}
	if _, ok := r.recordedJob(JobCleanup); !ok {
		t.Error("the matching plist was not adopted into the manifest")
	}
}

// A job that is installed but not loaded is outdated: it is loaded again.
func TestJobsStepReloadsNotLoadedJob(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	delete(r.launchd.loaded, LaunchdLabelPrefix+JobCleanup)
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	if !r.launchd.isLoaded(LaunchdLabelPrefix + JobCleanup) {
		t.Error("cleanup was not loaded again")
	}
}

// A recorded, unedited plist that no longer equals the rendering (here: a new
// tool directory in PATH) is refreshed under --yes, with no overwrite Confirm
// and no backup, and the recorded hash follows.
func TestJobsStepRefreshesOutdatedRecordedPlist(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	old, _ := r.recordedJob(JobCleanup)
	r.runner.SetPath("az", "/opt/homebrew/bin/az")
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	b, _ := os.ReadFile(r.plistPath(JobCleanup))
	if !bytes.Contains(b, []byte("/opt/homebrew/bin")) {
		t.Errorf("plist not refreshed:\n%s", b)
	}
	if a, _ := r.recordedJob(JobCleanup); a.SHA256 == old.SHA256 || a.SHA256 != sha256Hex(b) {
		t.Errorf("recorded hash %q (old %q), file %q", a.SHA256, old.SHA256, sha256Hex(b))
	}
	if matches, _ := filepath.Glob(r.plistPath(JobCleanup) + ".bak*"); len(matches) != 0 {
		t.Errorf("unexpected backup %v", matches)
	}
}

// jobPorts are the rig's ports as the jobs step sees them.
func (r *fullRig) jobPorts() WritePorts {
	jobs := LaunchdJobs{FS: r.fs, Runner: r.runner, Paths: r.p, Assets: integration.FS}
	rp := ReadPorts{FS: r.fs, Runner: r.runner, Clock: NewFakeClock(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)), Paths: r.p,
		Env: Env{}, Platform: PlatformInfo{OS: OSDarwin, Arch: "arm64", JobsBackend: JobsLaunchd}, Assets: integration.FS, Jobs: jobs}
	return WritePorts{ReadPorts: rp, FS: r.fs, Runner: r.runner, Jobs: LaunchdManager{LaunchdJobs: jobs, Write: r.fs}}
}

func (r *fullRig) jobState(wc WritePorts) *RunState {
	r.t.Helper()
	st := NewRunState(Inputs{Yes: true})
	ml, err := LoadManifest(r.fs, r.p)
	if err != nil {
		r.t.Fatal(err)
	}
	st.Prior.Manifest = ml.Manifest
	st.BinPath.Set(r.p.InstalledBinary(), SourceDefault)
	if _, err := (JobsStep{}).Seed(context.Background(), wc.ReadPorts, st); err != nil {
		r.t.Fatal(err)
	}
	return st
}

// An edited plist is modified: --yes keeps it untouched (no write at all); a
// confirmed overwrite (the generic Confirm sets the artifact choice to apply)
// keeps a 0600 backup beside it, and a plist that changes between Plan and
// Apply aborts the apply (AC-39 style Token).
func TestJobsStepModifiedPlistKeptThenOverwrittenWithBackup(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	edited, _ := os.ReadFile(r.plistPath(JobCleanup))
	edited = bytes.Replace(edited, []byte("<integer>15</integer>"), []byte("<integer>45</integer>"), 1)
	writeFile(t, r.plistPath(JobCleanup), edited, 0o644)
	writes := r.nonLockWrites()
	res := r.run(r.inputs())
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	if got, _ := os.ReadFile(r.plistPath(JobCleanup)); !bytes.Equal(got, edited) {
		t.Error("--yes overwrote an edited plist")
	}
	if n := r.nonLockWrites() - writes; n != 0 {
		t.Errorf("--yes kept the plist but wrote %d times: %v", n, r.fs.Writes())
	}

	wc := r.jobPorts()
	st := r.jobState(wc)
	step := JobsStep{Version: "v1.2.0"}
	det := step.Detect(context.Background(), wc.ReadPorts, st)
	if det.State != StateModified {
		t.Fatalf("detect = %s (%s), want modified", det.State, det.Detail)
	}
	plan, err := step.Plan(context.Background(), wc.ReadPorts, st, Choices{"jobs/cleanup": ChoiceApply})
	if err != nil {
		t.Fatal(err)
	}
	// Edited again after the user confirmed: abort, nothing replaced.
	writeFile(t, r.plistPath(JobCleanup), append(slices.Clone(edited), '\n'), 0o644)
	if _, err := step.Apply(context.Background(), wc, st, plan); !errors.Is(err, ErrJobChanged) {
		t.Fatalf("Apply after a concurrent edit = %v, want ErrJobChanged", err)
	}
	writeFile(t, r.plistPath(JobCleanup), edited, 0o644)

	res2, err := step.Apply(context.Background(), wc, st, plan)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(r.plistPath(JobCleanup))
	if !bytes.Contains(got, []byte("<integer>15</integer>")) {
		t.Errorf("plist not replaced:\n%s", got)
	}
	baks, _ := filepath.Glob(r.plistPath(JobCleanup) + SettingsBackupSuffix + "*")
	if len(baks) != 1 {
		t.Fatalf("backups = %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); !bytes.Equal(b, edited) {
		t.Error("the backup is not the edited plist")
	}
	if info, _ := os.Stat(baks[0]); info.Mode().Perm() != 0o600 {
		t.Errorf("backup mode %v", info.Mode().Perm())
	}
	if len(res2.Artifacts) != 1 || res2.Artifacts[0].SHA256 != sha256Hex(got) {
		t.Errorf("artifacts = %+v", res2.Artifacts)
	}
}

func TestJobsStepSeed(t *testing.T) {
	t.Parallel()
	rc := func(env Env) ReadPorts {
		r := NewFakeRunner(t)
		r.SetPath("claude", "/opt/c/claude")
		return ReadPorts{Runner: r, Env: env}
	}
	envFile := []byte("MEMORY_PR_INGEST_REPOS=/from/file\n")
	for _, tc := range []struct {
		name    string
		in      Inputs
		env     Env
		doc     []byte
		want    string
		src     Source
		wantErr bool
	}{
		{name: "none"},
		{name: "flag wins", in: Inputs{PRRepos: "/flag"}, env: Env{"MEMORY_PR_INGEST_REPOS": "/shell"}, doc: envFile, want: "/flag", src: SourceFlag},
		{name: "env file over shell", env: Env{"MEMORY_PR_INGEST_REPOS": "/shell"}, doc: envFile, want: "/from/file", src: SourceEnvFile},
		{name: "shell", env: Env{"MEMORY_PR_INGEST_REPOS": "/shell"}, want: "/shell", src: SourceEnv},
		{name: "relative flag", in: Inputs{PRRepos: "repo"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := NewRunState(tc.in)
			st.Prior.EnvDoc = tc.doc
			_, err := JobsStep{}.Seed(context.Background(), rc(tc.env), st)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				return
			}
			if st.PRRepos.IsSet() != (tc.want != "") || st.PRRepos.Get() != tc.want || st.PRRepos.Source() != tc.src {
				t.Errorf("PRRepos = %q (%v, %s), want %q (%s)", st.PRRepos.Get(), st.PRRepos.IsSet(), st.PRRepos.Source(), tc.want, tc.src)
			}
			if got := st.JobPATH.Get(); got != "/opt/c:/usr/bin:/bin" {
				t.Errorf("JobPATH = %q", got)
			}
		})
	}
}

func TestValidatePRRepos(t *testing.T) {
	t.Parallel()
	for v, ok := range map[string]bool{"/a": true, "/a, /b": true, "/a,,": true, "": false, " , ": false, "a/b": false, "/a,b": false} {
		if err := ValidatePRRepos(v); (err == nil) != ok {
			t.Errorf("ValidatePRRepos(%q) = %v, want ok=%v", v, err, ok)
		}
	}
}
