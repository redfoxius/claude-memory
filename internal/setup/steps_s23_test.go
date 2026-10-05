package setup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for WI-S2-3: platform, prereqs and binary steps, and the hint table.
// They run on FakeFS/FakeRunner over realTempDir(t); no test touches HOME.

type s23 struct {
	t      *testing.T
	p      Paths
	root   string
	fs     *FakeFS
	runner *FakeRunner
	env    Env
	plat   PlatformInfo
}

func newS23(t *testing.T) *s23 {
	t.Helper()
	p := testPaths(t)
	root := filepath.Dir(p.Home)
	// The running executable lives in a build directory, not in BinDir.
	p.Self = filepath.Join(root, "build", "claude-memory")
	return &s23{
		t: t, p: p, root: root, fs: NewFakeFS(t, root), runner: NewFakeRunner(t),
		env:  Env{"PATH": "/usr/bin:" + filepath.Dir(p.InstalledBinary())},
		plat: PlatformInfo{OS: OSLinux, Arch: "amd64", JobsBackend: JobsNone},
	}
}

func (h *s23) write(path, content string, mode fs.FileMode) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *s23) rp() ReadPorts {
	return ReadPorts{FS: h.fs, Runner: h.runner, Paths: h.p, Env: h.env, Platform: h.plat, Clock: NewFakeClock(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))}
}

func (h *s23) wp() WritePorts {
	return WritePorts{ReadPorts: h.rp(), FS: h.fs, Runner: h.runner}
}

// ---- hints ----------------------------------------------------------------

func TestHintTable(t *testing.T) {
	t.Parallel()
	comps := []string{CompGit, CompClaude, CompPsql, CompPostgres, CompOllama, CompAz}
	for _, pm := range []string{"brew", "apt-get", "dnf", "pacman", ""} {
		for _, c := range comps {
			p := PlatformInfo{}
			if pm != "" {
				p.PackageManagers = []string{pm}
			}
			if Hint(p, c) == "" {
				t.Errorf("no hint for (%q, %s)", pm, c)
			}
		}
	}
	if got := Hint(PlatformInfo{PackageManagers: []string{"brew", "apt-get"}}, CompGit); got != "brew install git" {
		t.Errorf("first package manager must win: %q", got)
	}
	if got := Hint(PlatformInfo{PackageManagers: []string{"zypper"}}, CompGit); !strings.Contains(got, "git-scm.com") {
		t.Errorf("unknown package manager must fall back: %q", got)
	}
	if Hint(PlatformInfo{}, "nonsense") != "" {
		t.Error("unknown component must have no hint")
	}
}

// ---- platform -------------------------------------------------------------

func TestPlatformStep(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	s := PlatformStep{}
	ctx := context.Background()

	for _, v := range []string{"", "launchd", "systemd", "none"} {
		if _, err := s.Seed(ctx, h.rp(), NewRunState(Inputs{JobsBackend: v})); err != nil {
			t.Errorf("--jobs-backend %q rejected: %v", v, err)
		}
	}
	if _, err := s.Seed(ctx, h.rp(), NewRunState(Inputs{JobsBackend: "cron"})); err == nil {
		t.Error("--jobs-backend cron must be rejected")
	}

	h.plat = PlatformInfo{OS: OSDarwin, Arch: "arm64", OSVersion: "macOS 15.1", JobsBackend: JobsLaunchd}
	d := s.Detect(ctx, h.rp(), nil)
	if d.State != StateOK || !strings.Contains(d.Detail, "macOS 15.1") || !strings.Contains(d.Detail, "jobs: launchd") {
		t.Errorf("darwin: %+v", d)
	}
	if !hasNote(d.Notes, "Homebrew not found") {
		t.Errorf("darwin without brew: notes %+v", d.Notes)
	}
	h.plat.PackageManagers = []string{"brew"}
	if d := s.Detect(ctx, h.rp(), nil); len(d.Notes) != 0 {
		t.Errorf("darwin with brew: notes %+v", d.Notes)
	}

	h.plat = PlatformInfo{OS: "windows", Arch: "amd64"}
	d = s.Detect(ctx, h.rp(), nil)
	if d.State != StateBlocked || d.Remedy != "" || !strings.Contains(d.Detail, "unsupported OS windows") {
		t.Errorf("windows: %+v", d)
	}
	if err := UnsupportedError(h.plat); err == nil || !strings.Contains(err.Error(), "see integration/INSTALL.md") {
		t.Errorf("UnsupportedError = %v", err)
	}
	if UnsupportedError(PlatformInfo{OS: OSLinux}) != nil {
		t.Error("linux is supported")
	}
}

// ---- prereqs --------------------------------------------------------------

func TestPrereqsDetect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newS23(t)
	h.plat.PackageManagers = []string{"apt-get"}
	s := PrereqsStep{}

	h.runner.SetPath("git", "/usr/bin/git").SetPath("claude", "/usr/bin/claude")
	d := s.Detect(ctx, h.rp(), nil)
	if d.State != StateOK || d.Remedy != "" || d.BlockedBy != "" {
		t.Fatalf("required tools present: %+v", d)
	}
	var warn, info int
	for _, n := range d.Notes {
		switch n.Level {
		case NoteWarn:
			warn++
		case NoteInfo:
			info++
		}
	}
	if warn != 1 || info != 4 || !hasNote(d.Notes, "psql not found") || !hasNote(d.Notes, "postgresql-client") {
		t.Errorf("optional tools missing: notes %+v", d.Notes)
	}

	h2 := newS23(t)
	h2.plat.PackageManagers = []string{"brew"}
	h2.runner.SetPath("psql", "/usr/bin/psql")
	d = s.Detect(ctx, h2.rp(), nil)
	if d.State != StateBlocked || d.BlockedBy != "" {
		t.Fatalf("missing required: %+v", d)
	}
	if !strings.Contains(d.Detail, "git, claude") || !strings.Contains(d.Remedy, "brew install git") ||
		!strings.Contains(d.Remedy, "claude: ") {
		t.Errorf("blocked detail/remedy: %+v", d)
	}
	if d.Artifacts != nil {
		t.Errorf("a blocked prereq has no artifacts: %+v", d.Artifacts)
	}
}

// hookPrompter lets a test change the world when a Select is answered.
type hookPrompter struct {
	Prompter
	onSelect func(q string)
}

func (p hookPrompter) Select(q string, opts []string, def int) (int, error) {
	i, err := p.Prompter.Select(q, opts, def)
	if p.onSelect != nil {
		p.onSelect(q)
	}
	return i, err
}

func prereqsEngine(h *s23, ui Prompter) *Engine {
	rp := h.rp()
	return &Engine{Steps: []Step{PrereqsStep{}}, Read: rp, Write: WritePorts{ReadPorts: rp, FS: h.fs, Runner: h.runner}, UI: ui, Version: "v1"}
}

func TestPrereqsBlockedPromptThroughEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("yes: stays blocked, exit 1, remedy kept", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		r := prereqsEngine(h, NewFakePrompter(t, false)).Run(ctx, Inputs{Yes: true})
		wantExit(t, r, ExitFailed)
		o := outcome(t, r, PrereqsStepID)
		if o.Outcome != OutcomeBlocked || !strings.Contains(o.Remedy, "git:") || !o.Hard {
			t.Errorf("outcome %+v", o)
		}
	})
	t.Run("interactive skip", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		ui := NewFakePrompter(t, true).ExpectSelect("Prerequisites is blocked", 1)
		r := prereqsEngine(h, ui).Run(ctx, Inputs{})
		wantExit(t, r, ExitOK)
		if o := outcome(t, r, PrereqsStepID); o.Outcome != OutcomeSkipped {
			t.Errorf("outcome %+v", o)
		}
	})
	t.Run("interactive quit", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		ui := NewFakePrompter(t, true).ExpectSelect("Prerequisites is blocked", 2)
		wantExit(t, prereqsEngine(h, ui).Run(ctx, Inputs{}), ExitInterrupted)
	})
	t.Run("interactive re-check after installing the tools", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		fp := NewFakePrompter(t, true).ExpectSelect("Prerequisites is blocked", 0)
		ui := hookPrompter{Prompter: fp, onSelect: func(string) {
			h.runner.SetPath("git", "/usr/bin/git").SetPath("claude", "/usr/bin/claude")
		}}
		r := prereqsEngine(h, ui).Run(ctx, Inputs{})
		wantExit(t, r, ExitOK)
		if o := outcome(t, r, PrereqsStepID); o.Outcome != OutcomeUnchanged {
			t.Errorf("outcome %+v", o)
		}
	})
}

// ---- binary ---------------------------------------------------------------

func seedBinary(t *testing.T, h *s23, in Inputs, m *Manifest) (*RunState, []Note, error) {
	t.Helper()
	st := NewRunState(in)
	st.Prior.Manifest = m
	notes, err := BinaryStep{Version: "v1"}.Seed(context.Background(), h.rp(), st)
	return st, notes, err
}

func TestBinaryApplyFreshInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newS23(t)
	h.write(h.p.Self, "BINARY-V1", 0o755)
	s := BinaryStep{Version: "v1.2.0"}
	st, notes, err := seedBinary(t, h, Inputs{}, nil)
	if err != nil || len(notes) != 0 {
		t.Fatalf("Seed: %v %v", notes, err)
	}
	if st.BinPath.Get() != h.p.InstalledBinary() || st.BinPath.Source() != SourceDefault {
		t.Fatalf("BinPath %v %s", st.BinPath.Get(), st.BinPath.Source())
	}
	h.env = Env{"PATH": "/usr/bin"} // BinDir not on PATH

	d := s.Detect(ctx, h.rp(), st)
	if d.State != StateAbsent || len(d.Artifacts) != 2 || d.Artifacts[0].ID != BinaryFileArtifact || d.Artifacts[1].ID != BinaryDirArtifact {
		t.Fatalf("Detect: %+v", d)
	}
	if !hasNote(d.Notes, "not on PATH") || !hasNote(d.Notes, filepath.Dir(h.p.InstalledBinary())) {
		t.Errorf("PATH note: %+v", d.Notes)
	}

	ch := Choices{BinaryFileArtifact: ChoiceApply, BinaryDirArtifact: ChoiceApply}
	plan, err := s.Plan(ctx, h.rp(), st, ch)
	if err != nil || len(plan.Actions) != 2 {
		t.Fatalf("Plan: %+v %v", plan, err)
	}
	if len(h.fs.Writes()) != 0 {
		t.Fatalf("Plan wrote: %v", h.fs.Writes())
	}
	res, err := s.Apply(ctx, h.wp(), st, plan)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(h.p.InstalledBinary())
	if err != nil || string(got) != "BINARY-V1" {
		t.Fatalf("installed: %q %v", got, err)
	}
	if fi, _ := os.Stat(h.p.InstalledBinary()); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if len(res.Artifacts) != 2 {
		t.Fatalf("artifacts: %+v", res.Artifacts)
	}
	dirA, fileA := res.Artifacts[0], res.Artifacts[1]
	if dirA.Kind != KindDir || dirA.Path != filepath.Dir(h.p.InstalledBinary()) || dirA.Step != BinaryStepName ||
		fileA.Kind != KindFile || fileA.Path != h.p.InstalledBinary() || fileA.SHA256 != sha256Hex([]byte("BINARY-V1")) ||
		fileA.Version != "v1.2.0" || fileA.Step != BinaryStepName {
		t.Errorf("artifacts: %+v", res.Artifacts)
	}
	if !ArtifactRetained(dirA, h.p) {
		t.Error("the <BinDir> dir artifact must be retained (Design 23)")
	}
	if d := s.Detect(ctx, h.rp(), st); d.State != StateOK {
		t.Errorf("re-Detect: %+v", d)
	}
}

func TestBinaryDetectStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("same file is ok", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		h.p.Self = h.p.InstalledBinary()
		h.write(h.p.Self, "X", 0o755)
		st, _, err := seedBinary(t, h, Inputs{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		d := BinaryStep{}.Detect(ctx, h.rp(), st)
		if d.State != StateOK || len(d.Notes) != 0 {
			t.Errorf("%+v", d)
		}
	})
	t.Run("symlink to the running binary is ok", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		h.write(h.p.Self, "X", 0o755)
		if err := os.MkdirAll(filepath.Dir(h.p.InstalledBinary()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(h.p.Self, h.p.InstalledBinary()); err != nil {
			t.Fatal(err)
		}
		st, _, _ := seedBinary(t, h, Inputs{}, nil)
		if d := (BinaryStep{}).Detect(ctx, h.rp(), st); d.State != StateOK {
			t.Errorf("%+v", d)
		}
	})
	t.Run("identical copy is ok, different is outdated, non-executable is outdated", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		h.write(h.p.Self, "NEW", 0o755)
		st, _, _ := seedBinary(t, h, Inputs{}, nil)
		h.write(h.p.InstalledBinary(), "NEW", 0o755)
		if d := (BinaryStep{}).Detect(ctx, h.rp(), st); d.State != StateOK {
			t.Errorf("identical: %+v", d)
		}
		h.write(h.p.InstalledBinary(), "OLD", 0o755)
		d := BinaryStep{}.Detect(ctx, h.rp(), st)
		if d.State != StateOutdated || DefaultChoice(d.State) != ChoiceApply {
			t.Errorf("different: %+v", d)
		}
		h.write(h.p.InstalledBinary(), "NEW", 0o644)
		if d := (BinaryStep{}).Detect(ctx, h.rp(), st); d.State != StateOutdated || !strings.Contains(d.Detail, "not executable") {
			t.Errorf("0644: %+v", d)
		}
	})
	t.Run("existing dir: dir artifact is ok, nothing to create", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		h.write(h.p.Self, "X", 0o755)
		if err := os.MkdirAll(filepath.Dir(h.p.InstalledBinary()), 0o755); err != nil {
			t.Fatal(err)
		}
		st, _, _ := seedBinary(t, h, Inputs{}, nil)
		d := BinaryStep{}.Detect(ctx, h.rp(), st)
		if d.State != StateAbsent || len(d.Artifacts) != 2 || d.Artifacts[1].State != StateOK {
			t.Errorf("%+v", d)
		}
	})
	t.Run("unknown self and a directory target are blocked", func(t *testing.T) {
		t.Parallel()
		h := newS23(t)
		st, _, _ := seedBinary(t, h, Inputs{}, nil)
		h.p.Self = ""
		if d := (BinaryStep{}).Detect(ctx, h.rp(), st); d.State != StateBlocked {
			t.Errorf("no self: %+v", d)
		}
		h.p.Self = filepath.Join(h.root, "build", "claude-memory")
		if err := os.MkdirAll(h.p.InstalledBinary(), 0o755); err != nil {
			t.Fatal(err)
		}
		if d := (BinaryStep{}).Detect(ctx, h.rp(), st); d.State != StateBlocked || !strings.Contains(d.Detail, "directory") {
			t.Errorf("dir target: %+v", d)
		}
	})
}

func TestBinaryPathNoteUsesRecordedBinDir(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.write(h.p.Self, "X", 0o755)
	other := filepath.Join(h.root, "opt", "bin", "claude-memory")
	m := &Manifest{Artifacts: []Artifact{{Step: BinaryStepName, Kind: KindFile, Path: other}}}
	st, _, _ := seedBinary(t, h, Inputs{}, m)
	// BinDir (~/.local/bin) is on PATH, the recorded dir is not: the note
	// names the recorded dir (N5: filepath.Dir(BinPath), not Paths.BinDir).
	d := BinaryStep{}.Detect(context.Background(), h.rp(), st)
	if !hasNote(d.Notes, filepath.Dir(other)+" is not on PATH") {
		t.Errorf("notes %+v", d.Notes)
	}
	h.env = Env{"PATH": filepath.Dir(other)}
	if d := (BinaryStep{}).Detect(context.Background(), h.rp(), st); len(d.Notes) != 0 {
		t.Errorf("on PATH: notes %+v", d.Notes)
	}
}

func TestBinaryQuarantineProbe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newS23(t)
	h.plat = PlatformInfo{OS: OSDarwin}
	h.write(h.p.Self, "X", 0o755)
	h.write(h.p.InstalledBinary(), "X", 0o755)
	st, _, _ := seedBinary(t, h, Inputs{}, nil)
	argv := []string{"xattr", "-p", "com.apple.quarantine", h.p.InstalledBinary()}
	h.runner.Script(ArgvEq(argv...), Result{Stdout: []byte("0081;...")})
	d := BinaryStep{}.Detect(ctx, h.rp(), st)
	if !hasNote(d.Notes, "xattr -d com.apple.quarantine "+h.p.InstalledBinary()) {
		t.Errorf("notes %+v", d.Notes)
	}
	for _, c := range h.runner.Calls() {
		if c.Mutating {
			t.Errorf("mutating call %v", c.Argv)
		}
	}

	h2 := newS23(t)
	h2.plat = PlatformInfo{OS: OSDarwin}
	h2.write(h2.p.Self, "X", 0o755)
	h2.write(h2.p.InstalledBinary(), "X", 0o755)
	st2, _, _ := seedBinary(t, h2, Inputs{}, nil)
	h2.runner.Script(ArgvEq("xattr", "-p", "com.apple.quarantine", h2.p.InstalledBinary()), Result{ExitCode: 1})
	if d := (BinaryStep{}).Detect(ctx, h2.rp(), st2); hasNote(d.Notes, "quarantine") {
		t.Errorf("no attribute: notes %+v", d.Notes)
	}

	// Not on darwin, and an absent target, never run xattr.
	h3 := newS23(t)
	h3.write(h3.p.Self, "X", 0o755)
	st3, _, _ := seedBinary(t, h3, Inputs{}, nil)
	BinaryStep{}.Detect(ctx, h3.rp(), st3)
	h3.plat = PlatformInfo{OS: OSDarwin}
	BinaryStep{}.Detect(ctx, h3.rp(), st3)
	if n := len(h3.runner.Calls()); n != 0 {
		t.Errorf("unexpected commands: %v", h3.runner.Calls())
	}
}

func TestBinarySeedRefusesEphemeralSelf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		dirs    func(h *s23) []string
		skip    []string
		wantErr bool
	}{
		{"under os.TempDir", func(h *s23) []string { return []string{filepath.Join(h.root, "build")} }, nil, true},
		{"under GOTMPDIR (second entry)", func(h *s23) []string {
			return []string{filepath.Join(h.root, "elsewhere"), filepath.Join(h.root, "build")}
		}, nil, true},
		{"under GOCACHE", func(h *s23) []string { return []string{h.root + "/build/"} }, nil, true},
		{"stable path", func(h *s23) []string { return []string{filepath.Join(h.root, "tmp")} }, nil, false},
		{"sibling with a shared prefix", func(h *s23) []string { return []string{filepath.Join(h.root, "bui")} }, nil, false},
		{"--skip binary is not refused", func(h *s23) []string { return []string{filepath.Join(h.root, "build")} }, []string{"binary"}, false},
	}
	for _, tc := range cases {
		h := newS23(t)
		h.write(h.p.Self, "X", 0o755)
		h.p.EphemeralDirs = tc.dirs(h)
		_, _, err := seedBinary(t, h, Inputs{Skip: tc.skip}, nil)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "make install") || !strings.Contains(err.Error(), h.p.Self)) {
			t.Errorf("%s: message %q", tc.name, err)
		}
	}

	// A symlinked ephemeral dir (macOS /var -> /private/var): Self is the
	// resolved path, the dir entry is not.
	h := newS23(t)
	h.write(h.p.Self, "X", 0o755)
	link := filepath.Join(h.root, "tmplink")
	if err := os.Symlink(filepath.Join(h.root, "build"), link); err != nil {
		t.Fatal(err)
	}
	h.p.EphemeralDirs = []string{link}
	if _, _, err := seedBinary(t, h, Inputs{}, nil); err == nil {
		t.Error("a symlinked ephemeral dir must still refuse")
	}
}

func TestBinarySeedSticky(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	x := filepath.Join(h.root, "x", "claude-memory")
	y := filepath.Join(h.root, "y", "claude-memory")
	m := &Manifest{Artifacts: []Artifact{{Step: BinaryStepName, Kind: KindFile, Path: x}}}

	st, notes, err := seedBinary(t, h, Inputs{}, m)
	if err != nil || st.BinPath.Get() != x || st.BinPath.Source() != SourceManifest || len(notes) != 0 {
		t.Errorf("manifest: %v %s %v %v", st.BinPath.Get(), st.BinPath.Source(), notes, err)
	}

	h.p.BinDir = filepath.Dir(y)
	st, notes, err = seedBinary(t, h, Inputs{BinDirExplicit: true}, m)
	if err != nil || st.BinPath.Get() != y || st.BinPath.Source() != SourceFlag {
		t.Errorf("explicit: %v %s %v", st.BinPath.Get(), st.BinPath.Source(), err)
	}
	if len(notes) != 1 || notes[0].Level != NoteInfo || !strings.Contains(notes[0].Text, x) {
		t.Errorf("explicit note: %+v", notes)
	}
	// Explicit to the same recorded path: no note.
	h.p.BinDir = filepath.Dir(x)
	if _, notes, _ := seedBinary(t, h, Inputs{BinDirExplicit: true}, m); len(notes) != 0 {
		t.Errorf("same path: %+v", notes)
	}
}

// AC-50 / v0.4: `install --bin-dir X` then a plain `install --yes` is a no-op
// and writes nothing under ~/.local/bin.
func TestBinaryBinDirStickyNoOpThroughEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newS23(t)
	h.write(h.p.Self, "BINARY", 0o755)
	xDir := filepath.Join(h.root, "x", "bin")
	runEngine := func(binDir string) RunResult {
		p := h.p
		p.BinDir = binDir
		rp := ReadPorts{FS: h.fs, Runner: h.runner, Paths: p, Env: Env{"PATH": xDir}, Platform: h.plat, Clock: NewFakeClock(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))}
		e := &Engine{Steps: []Step{BinaryStep{Version: "v1"}}, Read: rp,
			Write: WritePorts{ReadPorts: rp, FS: h.fs, Runner: h.runner}, UI: NewFakePrompter(t, false), Version: "v1"}
		in := Inputs{Yes: true}
		if binDir != h.p.BinDir {
			in.BinDirExplicit = true
		}
		return e.Run(ctx, in)
	}
	r1 := runEngine(xDir)
	wantExit(t, r1, ExitOK)
	if b, err := os.ReadFile(filepath.Join(xDir, "claude-memory")); err != nil || string(b) != "BINARY" {
		t.Fatalf("run 1: %q %v", b, err)
	}
	before := len(h.fs.Writes())
	r2 := runEngine(h.p.BinDir)
	wantExit(t, r2, ExitOK)
	if o := outcome(t, r2, BinaryStepName); o.Outcome != OutcomeUnchanged {
		t.Errorf("run 2 outcome: %+v", o)
	}
	for _, w := range h.fs.Writes()[before:] {
		if !strings.HasPrefix(w, "lock ") {
			t.Errorf("run 2 wrote %s", w)
		}
	}
	if _, err := os.Stat(h.p.InstalledBinary()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("run 2 installed into ~/.local/bin: %v", err)
	}
}

// A binary under an ephemeral dir exits 2 from the engine, before any Detect.
func TestBinaryEphemeralExits2ThroughEngine(t *testing.T) {
	t.Parallel()
	h := newS23(t)
	h.write(h.p.Self, "X", 0o755)
	h.p.EphemeralDirs = []string{filepath.Join(h.root, "build")}
	rp := h.rp()
	e := &Engine{Steps: []Step{BinaryStep{}}, Read: rp, Write: WritePorts{ReadPorts: rp, FS: h.fs, Runner: h.runner}, UI: NewFakePrompter(t, false)}
	r := e.Run(context.Background(), Inputs{Yes: true})
	wantExit(t, r, ExitUsage)
	if r.Err == nil || !strings.Contains(r.Err.Error(), "make install") {
		t.Errorf("err = %v", r.Err)
	}
	if _, err := os.Stat(h.p.InstalledBinary()); err == nil {
		t.Error("binary installed despite the refusal")
	}
}
