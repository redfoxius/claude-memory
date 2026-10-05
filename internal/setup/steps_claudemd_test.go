package setup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// Tests for WI-S2-12: the claude-md step (AC-42, AC-6, AC-12, AC-51, AC-64).
// The embedded section is a fixed stand-in (testSection), so the goldens do not
// churn with the real wording. FakeFS over a temp root; nothing touches the
// real HOME.

type cmk struct{ *hk }

func newCMK(t *testing.T) *cmk { return &cmk{newHK(t)} }

func (c *cmk) assets() fs.FS {
	return fstest.MapFS{assetClaudeMDSection: {Data: []byte(testSection)}}
}

func (c *cmk) rp() ReadPorts {
	r := c.hk.s23.rp()
	r.Assets = c.assets()
	return r
}

func (c *cmk) wp() WritePorts { return WritePorts{ReadPorts: c.rp(), FS: c.fs, Runner: c.runner} }

func (c *cmk) state(m *Manifest) *RunState {
	st := NewRunState(Inputs{})
	st.Prior.Manifest = m
	if _, err := (ClaudeMDStep{}).Seed(context.Background(), c.rp(), st); err != nil {
		c.t.Fatal(err)
	}
	return st
}

type cmkRun struct {
	det  Detection
	plan Plan
	res  StepResult
	ran  bool
	err  error
}

// run is the engine's Detect -> Plan -> Apply; overwrite answers the AC-6
// overwrite question for a modified block.
func (c *cmk) run(st *RunState, overwrite bool) cmkRun {
	c.t.Helper()
	var r cmkRun
	step := ClaudeMDStep{Version: "v1"}
	ctx := context.Background()
	r.det = step.Detect(ctx, c.rp(), st)
	if r.det.State == StateBlocked || r.det.SkipReason != "" {
		return r
	}
	ch := Choices{ClaudeMDArtifact: DefaultChoice(r.det.State)}
	if overwrite && r.det.State == StateModified {
		ch[ClaudeMDArtifact] = ChoiceApply
	}
	if r.plan, r.err = step.Plan(ctx, c.rp(), st, ch); r.err != nil {
		return r
	}
	if ch[ClaudeMDArtifact] == ChoiceApply {
		r.ran = true
		r.res, r.err = step.Apply(ctx, c.wp(), st, r.plan)
	}
	return r
}

func (c *cmk) target() string { return filepath.Join(c.p.ClaudeDir, "CLAUDE.md") }

// Golden cases: the input file (testdata/mdblock/<in>.in.md, "" = none), the
// manifest hash, the state Detect reports, and whether an overwrite is chosen.
func TestClaudeMDGolden(t *testing.T) {
	cases := []struct {
		name, in  string
		recorded  string
		state     State
		overwrite bool
		writes    bool
	}{
		{"absent-file", "", "", StateAbsent, false, true},
		{"existing-text", "insert-at-end", "", StateAbsent, false, true},
		{"outdated", "refresh", MDSectionHash(oldSection), StateOutdated, false, true},
		{"modified-overwrite", "edited", "", StateModified, true, true},
		{"modified-kept", "edited", "", StateModified, false, false},
		{"fenced-markers", "fenced-markers-only", "", StateAbsent, false, true},
		{"fenced-begin-plus-block", "fenced-begin-plus-block", "", StateOK, false, false},
		{"crlf", "crlf", "", StateAbsent, false, true},
		{"ok", "idempotent", "", StateOK, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCMK(t)
			var before []byte
			if tc.in != "" {
				before = readTestdata(t, filepath.Join("testdata", "mdblock", tc.in+".in.md"))
				c.write(c.target(), string(before), 0o640)
			}
			var m *Manifest
			if tc.recorded != "" {
				m = &Manifest{}
				m.Upsert(Artifact{Step: "claude-md", Kind: KindMDBlock, Path: c.target(), Identity: ClaudeMDBlockIdentity, SHA256: tc.recorded})
			}
			n := len(c.fs.Writes())
			r := c.run(c.state(m), tc.overwrite)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.det.State != tc.state {
				t.Fatalf("state %s (%s), want %s", r.det.State, r.det.Detail, tc.state)
			}
			got, _ := os.ReadFile(c.target())
			if !tc.writes {
				c.assertNoWrites(n)
				if tc.in != "" && string(got) != string(before) {
					t.Error("the file changed without a write")
				}
				return
			}
			checkGolden(t, filepath.Join("testdata", "claudemd", tc.name+".golden.md"), got)
			// The text outside the markers is untouched.
			if blk, err := FindMDBlock(before); err == nil && blk.Found {
				if !strings.HasPrefix(string(got), string(before[:blk.Start])) || !strings.HasSuffix(string(got), string(before[blk.End:])) {
					t.Error("text outside the markers changed")
				}
			}
			if len(r.res.Artifacts) != 1 {
				t.Fatalf("artifacts %+v", r.res.Artifacts)
			}
			a := r.res.Artifacts[0]
			if a.Kind != KindMDBlock || a.Path != c.target() || a.SHA256 != MDSectionHash(testSection) || a.CreatedFile != (tc.in == "") {
				t.Errorf("artifact %+v", a)
			}
			// A backup (mode 0600) only when a modified block is overwritten.
			baks, _ := filepath.Glob(c.target() + ".bak.claude-memory.*")
			if (tc.state != StateModified) == (len(baks) != 0) {
				t.Errorf("backups %v for input %q", baks, tc.in)
			}
			for _, b := range baks {
				if info, _ := os.Stat(b); info.Mode().Perm() != BackupFileMode {
					t.Errorf("backup mode %v", info.Mode().Perm())
				}
				if string(mustRead(t, b)) != string(before) {
					t.Error("backup differs from the original")
				}
			}
			// Run twice: the second run is ok and writes nothing (AC-64, AC-50).
			n = len(c.fs.Writes())
			r2 := c.run(c.state(manifestOf(r.res)), false)
			if r2.det.State != StateOK || r2.err != nil {
				t.Fatalf("second run %s %v", r2.det.State, r2.err)
			}
			c.assertNoWrites(n)
		})
	}
}

// New files get 0644 and an existing file keeps its mode.
func TestClaudeMDModes(t *testing.T) {
	c := newCMK(t)
	c.write(c.target(), "# mine\n", 0o640)
	if r := c.run(c.state(nil), false); r.err != nil {
		t.Fatal(r.err)
	}
	if info, _ := os.Stat(c.target()); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640 kept", info.Mode().Perm())
	}
	c2 := newCMK(t)
	if r := c2.run(c2.state(nil), false); r.err != nil {
		t.Fatal(r.err)
	}
	if info, _ := os.Stat(c2.target()); info.Mode().Perm() != 0o644 {
		t.Errorf("new file mode %v, want 0644", info.Mode().Perm())
	}
}

// AC-42: malformed markers are refused (blocked, bytes untouched); a
// hand-pasted section is modified and an overwrite is refused with the hint.
func TestClaudeMDRefusals(t *testing.T) {
	for _, in := range []string{"duplicated", "unbalanced", "out-of-order"} {
		t.Run(in, func(t *testing.T) {
			c := newCMK(t)
			b := readTestdata(t, filepath.Join("testdata", "mdblock", in+".in.md"))
			c.write(c.target(), string(b), 0o644)
			n := len(c.fs.Writes())
			r := c.run(c.state(nil), true)
			if r.det.State != StateBlocked || !strings.Contains(r.det.Detail, "markers") || r.det.Remedy == "" {
				t.Errorf("detect %s (%s) remedy %q", r.det.State, r.det.Detail, r.det.Remedy)
			}
			c.assertNoWrites(n)
		})
	}
	t.Run("hand-pasted", func(t *testing.T) {
		c := newCMK(t)
		b := readTestdata(t, filepath.Join("testdata", "mdblock", "hand-pasted.in.md"))
		c.write(c.target(), string(b), 0o644)
		n := len(c.fs.Writes())
		r := c.run(c.state(nil), false)
		if r.det.State != StateModified || !strings.Contains(r.det.Detail, "hand-pasted") {
			t.Fatalf("detect %s (%s)", r.det.State, r.det.Detail)
		}
		c.assertNoWrites(n)
		r = c.run(c.state(nil), true)
		if !errors.Is(r.err, ErrMDHandPasted) {
			t.Errorf("overwrite of a hand-pasted section: %v", r.err)
		}
		c.assertNoWrites(n)
		if string(mustRead(t, c.target())) != string(b) {
			t.Error("file changed")
		}
	})
}

// AC-38 analogue: a symlink inside Home is followed (the target is edited, the
// link stays); one outside Home is refused.
func TestClaudeMDSymlink(t *testing.T) {
	t.Run("inside home", func(t *testing.T) {
		c := newCMK(t)
		target := filepath.Join(c.p.Home, "dotfiles", "CLAUDE.md")
		c.write(target, "# dotfiles\n", 0o600)
		if err := os.MkdirAll(c.p.ClaudeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, c.target()); err != nil {
			t.Fatal(err)
		}
		r := c.run(c.state(nil), false)
		if r.err != nil {
			t.Fatal(r.err)
		}
		if li, _ := os.Lstat(c.target()); li.Mode()&fs.ModeSymlink == 0 {
			t.Error("the symlink was replaced by a file")
		}
		if info, _ := os.Stat(target); info.Mode().Perm() != 0o600 {
			t.Errorf("target mode %v", info.Mode().Perm())
		}
		checkGolden(t, filepath.Join("testdata", "claudemd", "symlink.golden.md"), mustRead(t, target))
		if baks, _ := filepath.Glob(target + ".bak.claude-memory.*"); len(baks) != 0 {
			t.Errorf("a plain insertion must not leave a backup: %v", baks)
		}
		// The artifact is recorded at the path asked, not the resolved target.
		if len(r.res.Artifacts) != 1 || r.res.Artifacts[0].Path != c.target() {
			t.Errorf("artifacts %+v", r.res.Artifacts)
		}
	})
	t.Run("outside home", func(t *testing.T) {
		c := newCMK(t)
		outside := filepath.Join(c.root, "elsewhere", "CLAUDE.md")
		c.write(outside, "# x\n", 0o644)
		if err := os.MkdirAll(c.p.ClaudeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, c.target()); err != nil {
			t.Fatal(err)
		}
		n := len(c.fs.Writes())
		r := c.run(c.state(nil), false)
		if r.det.State != StateBlocked || !strings.Contains(r.det.Detail, "outside") {
			t.Errorf("detect %s (%s)", r.det.State, r.det.Detail)
		}
		c.assertNoWrites(n)
	})
}

// AC-39 analogue: a change between Plan and Apply aborts the write.
func TestClaudeMDTokenDetectsConcurrentChange(t *testing.T) {
	c := newCMK(t)
	c.write(c.target(), "# mine\n", 0o644)
	st := c.state(nil)
	step := ClaudeMDStep{}
	ctx := context.Background()
	plan, err := step.Plan(ctx, c.rp(), st, Choices{ClaudeMDArtifact: ChoiceApply})
	if err != nil || plan.Token == "" {
		t.Fatalf("plan %+v %v", plan, err)
	}
	c.write(c.target(), "# mine\n\nadded meanwhile\n", 0o644)
	n := len(c.fs.Writes())
	if _, err := step.Apply(ctx, c.wp(), st, plan); !errors.Is(err, ErrClaudeMDChanged) {
		t.Fatalf("apply: %v", err)
	}
	c.assertNoWrites(n)
	// An absent file that appears meanwhile is a change as well.
	c2 := newCMK(t)
	st2 := c2.state(nil)
	plan2, err := step.Plan(ctx, c2.rp(), st2, Choices{ClaudeMDArtifact: ChoiceApply})
	if err != nil {
		t.Fatal(err)
	}
	c2.write(c2.target(), "# appeared\n", 0o644)
	if _, err := step.Apply(ctx, c2.wp(), st2, plan2); !errors.Is(err, ErrClaudeMDChanged) {
		t.Fatalf("apply over a file that appeared: %v", err)
	}
}

// An ok block that the manifest does not know (a hand install) is adopted
// without any write; CreatedFile stays false.
func TestClaudeMDAdopt(t *testing.T) {
	c := newCMK(t)
	c.write(c.target(), "# mine\n\n<!-- BEGIN claude-memory -->\n"+testSection+"<!-- END claude-memory -->\n", 0o644)
	st := c.state(nil)
	got := ClaudeMDStep{Version: "v1"}.Adopt(context.Background(), c.rp(), st)
	if len(got) != 1 || got[0].CreatedFile || got[0].SHA256 != MDSectionHash(testSection) || got[0].Path != c.target() {
		t.Fatalf("adopt %+v", got)
	}
	// Not ok -> nothing to adopt.
	c2 := newCMK(t)
	if got := (ClaudeMDStep{}).Adopt(context.Background(), c2.rp(), c2.state(nil)); len(got) != 0 {
		t.Errorf("adopted an absent block: %+v", got)
	}
	// A recorded CreatedFile survives a refresh and an adoption.
	m := &Manifest{}
	m.Upsert(Artifact{Step: "claude-md", Kind: KindMDBlock, Path: c.target(), Identity: ClaudeMDBlockIdentity, SHA256: MDSectionHash(oldSection), CreatedFile: true})
	r := c.run(c.state(m), false)
	if r.err != nil || r.det.State != StateOK {
		t.Fatalf("%v %s", r.err, r.det.State)
	}
	if got := (ClaudeMDStep{}).Adopt(context.Background(), c.rp(), c.state(m)); len(got) != 1 || !got[0].CreatedFile {
		t.Errorf("CreatedFile lost on adoption: %+v", got)
	}
}

// Seed: target sources (AC-42).
func TestClaudeMDSeed(t *testing.T) {
	c := newCMK(t)
	seed := func(in Inputs, m *Manifest) (*RunState, error) {
		st := NewRunState(in)
		st.Prior.Manifest = m
		_, err := ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
		return st, err
	}
	st, _ := seed(Inputs{}, nil)
	if st.ClaudeMDTarget.Get() != c.target() || st.ClaudeMDTarget.Source() != SourceDefault {
		t.Errorf("default: %v %v", st.ClaudeMDTarget.Get(), st.ClaudeMDTarget.Source())
	}
	st, _ = seed(Inputs{ClaudeMD: "proj/CLAUDE.md"}, nil)
	if want := filepath.Join(c.p.Cwd, "proj", "CLAUDE.md"); st.ClaudeMDTarget.Get() != want || st.ClaudeMDTarget.Source() != SourceFlag {
		t.Errorf("relative flag: %v %v", st.ClaudeMDTarget.Get(), st.ClaudeMDTarget.Source())
	}
	st, _ = seed(Inputs{ClaudeMD: "~/notes/CLAUDE.md"}, nil)
	if want := filepath.Join(c.p.Home, "notes", "CLAUDE.md"); st.ClaudeMDTarget.Get() != want {
		t.Errorf("~ flag: %v", st.ClaudeMDTarget.Get())
	}
	// An explicit path equal to the user-level file is the default, not a shared file.
	st, _ = seed(Inputs{ClaudeMD: c.target()}, nil)
	if st.ClaudeMDTarget.Source() != SourceDefault {
		t.Errorf("explicit default path source %v", st.ClaudeMDTarget.Source())
	}
	m := &Manifest{}
	m.Upsert(Artifact{Step: "claude-md", Kind: KindMDBlock, Path: "/x/CLAUDE.md", Identity: ClaudeMDBlockIdentity})
	if st, _ = seed(Inputs{}, m); st.ClaudeMDTarget.Get() != "/x/CLAUDE.md" || st.ClaudeMDTarget.Source() != SourceManifest {
		t.Errorf("manifest: %v %v", st.ClaudeMDTarget.Get(), st.ClaudeMDTarget.Source())
	}
	if st, _ = seed(Inputs{ClaudeMD: "/y/CLAUDE.md"}, m); st.ClaudeMDTarget.Get() != "/y/CLAUDE.md" {
		t.Errorf("flag over manifest: %v", st.ClaudeMDTarget.Get())
	}
	// A flag equal to the recorded path is the recorded one (M3).
	if st, _ = seed(Inputs{ClaudeMD: "/x/CLAUDE.md"}, m); st.ClaudeMDTarget.Source() != SourceManifest {
		t.Errorf("flag equal to the recorded path: %v", st.ClaudeMDTarget.Source())
	}
	// The last recorded block wins (M1).
	m.Upsert(Artifact{Step: "claude-md", Kind: KindMDBlock, Path: "/z/CLAUDE.md", Identity: ClaudeMDBlockIdentity})
	if st, _ = seed(Inputs{}, m); st.ClaudeMDTarget.Get() != "/z/CLAUDE.md" {
		t.Errorf("last recorded block: %v", st.ClaudeMDTarget.Get())
	}
}

// AC-42: --yes + explicit path in a git repository is skipped with the reason;
// the same path outside git, and the default target in a git Home, are
// written. Interactively (Auto false) the git path is offered normally.
func TestClaudeMDYesAndGit(t *testing.T) {
	gitDir := func(c *cmk, dir string) {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("yes + explicit in git: skipped", func(t *testing.T) {
		c := newCMK(t)
		repo := filepath.Join(c.root, "work", "work")
		gitDir(c, repo)
		st := NewRunState(Inputs{Yes: true, ClaudeMD: filepath.Join(repo, "CLAUDE.md")})
		st.Auto = true
		if _, err := (ClaudeMDStep{}).Seed(context.Background(), c.rp(), st); err != nil {
			t.Fatal(err)
		}
		n := len(c.fs.Writes())
		r := c.run(st, false)
		if !strings.Contains(r.det.SkipReason, "inside the git repository "+repo) {
			t.Fatalf("skip reason %q", r.det.SkipReason)
		}
		c.assertNoWrites(n)
	})
	t.Run("yes + explicit in git, block ok: nothing to skip", func(t *testing.T) {
		c := newCMK(t)
		repo := filepath.Join(c.root, "work", "work")
		gitDir(c, repo)
		c.write(filepath.Join(repo, "CLAUDE.md"), "<!-- BEGIN claude-memory -->\n"+testSection+"<!-- END claude-memory -->\n", 0o644)
		st := NewRunState(Inputs{Yes: true, ClaudeMD: filepath.Join(repo, "CLAUDE.md")})
		st.Auto = true
		_, _ = ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
		if r := c.run(st, false); r.det.SkipReason != "" || r.det.State != StateOK {
			t.Errorf("%+v", r.det)
		}
	})
	t.Run("interactive + explicit in git: planned with a note", func(t *testing.T) {
		c := newCMK(t)
		repo := filepath.Join(c.root, "work", "work")
		gitDir(c, repo)
		st := NewRunState(Inputs{ClaudeMD: filepath.Join(repo, "CLAUDE.md")})
		_, _ = ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
		r := c.run(st, false)
		if r.err != nil || r.det.SkipReason != "" || len(r.plan.Diffs) != 1 {
			t.Fatalf("%+v %v", r.det, r.err)
		}
		found := false
		for _, n := range r.plan.Notes {
			found = found || strings.Contains(n.Text, "inside the git repository")
		}
		if !found {
			t.Errorf("no git note in the plan: %+v", r.plan.Notes)
		}
		if _, err := os.Stat(filepath.Join(repo, "CLAUDE.md")); err != nil {
			t.Errorf("not written interactively: %v", err)
		}
	})
	t.Run("yes + explicit outside git: written", func(t *testing.T) {
		c := newCMK(t)
		dir := filepath.Join(c.root, "work", "plain")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		st := NewRunState(Inputs{Yes: true, ClaudeMD: filepath.Join(dir, "CLAUDE.md")})
		st.Auto = true
		_, _ = ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
		if r := c.run(st, false); r.err != nil || r.det.SkipReason != "" || !r.ran {
			t.Fatalf("%+v %v", r.det, r.err)
		}
		if !strings.Contains(string(mustRead(t, filepath.Join(dir, "CLAUDE.md"))), MDBeginMarker) {
			t.Error("block not written")
		}
	})
	t.Run("yes + default target, Home is a git repo: written", func(t *testing.T) {
		c := newCMK(t)
		gitDir(c, c.p.Home)
		st := c.state(nil)
		st.Auto, st.Inputs.Yes = true, true
		if r := c.run(st, false); r.err != nil || r.det.SkipReason != "" || !r.ran {
			t.Fatalf("%+v %v", r.det, r.err)
		}
	})
	t.Run("a missing parent directory of a project file is an error, not created", func(t *testing.T) {
		c := newCMK(t)
		st := NewRunState(Inputs{ClaudeMD: filepath.Join(c.root, "work", "nope", "CLAUDE.md")})
		_, _ = ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
		n := len(c.fs.Writes())
		if r := c.run(st, false); r.err == nil || !strings.Contains(r.err.Error(), "does not exist") {
			t.Errorf("err %v", r.err)
		}
		c.assertNoWrites(n)
	})
}

// ModifiedDiff shows what an overwrite changes; a hand-pasted section errors.
func TestClaudeMDModifiedDiff(t *testing.T) {
	c := newCMK(t)
	c.write(c.target(), "<!-- BEGIN claude-memory -->\nmine\n<!-- END claude-memory -->\n", 0o644)
	d, err := ClaudeMDStep{}.ModifiedDiff(context.Background(), c.rp(), c.state(nil), ClaudeMDArtifact)
	if err != nil || !strings.Contains(d, "-mine") || !strings.Contains(d, "+Use memory_search") {
		t.Errorf("diff %q %v", d, err)
	}
}

func autoState(c *cmk, in Inputs, m *Manifest) *RunState {
	st := NewRunState(in)
	st.Auto, st.Inputs.Yes = true, true
	st.Prior.Manifest = m
	_, _ = ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
	return st
}

// S1: a recorded path inside git is refreshed under --yes only when outdated;
// a block the user deleted is not re-added.
func TestClaudeMDYesRecordedPathInGit(t *testing.T) {
	c := newCMK(t)
	repo := filepath.Join(c.root, "work", "work")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "CLAUDE.md")
	rec := func(h string) *Manifest {
		m := &Manifest{}
		m.Upsert(Artifact{Step: "claude-md", Kind: KindMDBlock, Path: target, Identity: ClaudeMDBlockIdentity, SHA256: h})
		return m
	}
	// Outdated: refreshed.
	c.write(target, "<!-- BEGIN claude-memory -->\n"+oldSection+"<!-- END claude-memory -->\n", 0o644)
	m := rec(MDSectionHash(oldSection))
	if r := c.run(autoState(c, Inputs{}, m), false); r.err != nil || r.det.SkipReason != "" || !r.ran || r.det.State != StateOutdated {
		t.Fatalf("outdated: %+v %v", r.det, r.err)
	}
	// Block deleted by the user: skipped, nothing written.
	c.write(target, "# the user removed our block\n", 0o644)
	n := len(c.fs.Writes())
	r := c.run(autoState(c, Inputs{}, m), false)
	if r.det.SkipReason == "" {
		t.Fatalf("a deleted block was re-added: %+v", r.det)
	}
	c.assertNoWrites(n)
}

// gitLstatFails makes the walk for ".git" fail with an I/O error.
type gitLstatFails struct{ ReadFS }

func (g gitLstatFails) Lstat(p string) (fs.FileInfo, error) {
	if filepath.Base(p) == ".git" {
		return nil, errors.New("input/output error")
	}
	return g.ReadFS.Lstat(p)
}

// S2: a walk error fails closed under --yes.
func TestClaudeMDYesGitWalkErrorFailsClosed(t *testing.T) {
	c := newCMK(t)
	dir := filepath.Join(c.root, "work", "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := autoState(c, Inputs{ClaudeMD: filepath.Join(dir, "CLAUDE.md")}, nil)
	rp := c.rp()
	rp.FS = gitLstatFails{c.fs}
	d := (ClaudeMDStep{}).Detect(context.Background(), rp, st)
	if !strings.Contains(d.SkipReason, "cannot tell") {
		t.Fatalf("skip reason %q (detail %q)", d.SkipReason, d.Detail)
	}
}

// S3: a directory symlinked into a repository counts as inside it.
func TestClaudeMDYesSymlinkedDirIntoRepo(t *testing.T) {
	c := newCMK(t)
	repo := filepath.Join(c.root, "work", "work")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.root, "work", "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	r := c.run(autoState(c, Inputs{ClaudeMD: filepath.Join(link, "CLAUDE.md")}, nil), false)
	if !strings.Contains(r.det.SkipReason, "inside the git repository") {
		t.Fatalf("skip reason %q", r.det.SkipReason)
	}
}

// S4: one path rule: a literal path outside Home is allowed; a directory
// symlink inside Home resolving outside is refused, like a file symlink.
func TestClaudeMDPathPolicy(t *testing.T) {
	c := newCMK(t)
	outside := filepath.Join(c.root, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// Literal outside path: fine.
	st := NewRunState(Inputs{ClaudeMD: filepath.Join(outside, "CLAUDE.md")})
	_, _ = ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
	if r := c.run(st, false); r.err != nil || !r.ran {
		t.Fatalf("literal outside path: %+v %v", r.det, r.err)
	}
	// Directory symlink inside Home pointing outside: refused.
	if err := os.MkdirAll(c.p.Home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(c.p.Home, "proj")); err != nil {
		t.Fatal(err)
	}
	st = NewRunState(Inputs{ClaudeMD: filepath.Join(c.p.Home, "proj", "CLAUDE.md")})
	_, _ = ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
	n := len(c.fs.Writes())
	r := c.run(st, false)
	if r.det.State != StateBlocked || !strings.Contains(r.det.Detail, "outside") {
		t.Fatalf("detect %s (%s)", r.det.State, r.det.Detail)
	}
	c.assertNoWrites(n)
}

// S6: a target that does not end in .md gets a warning.
func TestClaudeMDNonMarkdownTargetWarns(t *testing.T) {
	c := newCMK(t)
	st := NewRunState(Inputs{ClaudeMD: filepath.Join(c.p.Cwd, "notes.txt")})
	_, _ = ClaudeMDStep{}.Seed(context.Background(), c.rp(), st)
	d := (ClaudeMDStep{}).Detect(context.Background(), c.rp(), st)
	for _, n := range d.Notes {
		if n.Level == NoteWarn && strings.Contains(n.Text, "does not end in .md") {
			return
		}
	}
	t.Errorf("notes %+v", d.Notes)
}
