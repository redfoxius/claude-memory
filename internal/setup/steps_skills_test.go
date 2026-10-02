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

// Tests for WI-S2-11: the skills step (AC-41, AC-51, AC-6, AC-12, AC-64).
// The embedded skills are a fixed stand-in; FakeFS over a temp root, nothing
// touches the real HOME.

var skillAssets = fstest.MapFS{
	"skills/remember/SKILL.md":          {Data: []byte("remember v2\n")},
	"skills/remember/references/why.md": {Data: []byte("why v2\n")},
	"skills/memory-digest/SKILL.md":     {Data: []byte("digest v2\n")},
}

type sk struct{ *hk }

func newSK(t *testing.T) *sk { return &sk{newHK(t)} }

func (s *sk) rp() ReadPorts {
	r := s.hk.s23.rp()
	r.Assets = skillAssets
	return r
}

func (s *sk) wp() WritePorts { return WritePorts{ReadPorts: s.rp(), FS: s.fs, Runner: s.runner} }

func (s *sk) path(name, rel string) string {
	return filepath.Join(s.p.SkillsDir(), name, filepath.FromSlash(rel))
}

type skRun struct {
	det  Detection
	plan Plan
	res  StepResult
	ran  bool
	err  error
}

func (s *sk) run(m *Manifest, choose map[string]Choice) skRun {
	s.t.Helper()
	st := NewRunState(Inputs{})
	st.Prior.Manifest = m
	step := SkillsStep{Version: "v1"}
	ctx := context.Background()
	var r skRun
	r.det = step.Detect(ctx, s.rp(), st)
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
	if r.plan, r.err = step.Plan(ctx, s.rp(), st, ch); r.err != nil {
		return r
	}
	if hasApply(ch) {
		r.ran = true
		r.res, r.err = step.Apply(ctx, s.wp(), st, r.plan)
	}
	return r
}

func (r skRun) stateOf(id string) State {
	for _, a := range r.det.Artifacts {
		if a.ID == id {
			return a.State
		}
	}
	return ""
}

const (
	idRemember = "skills/remember/SKILL.md"
	idWhy      = "skills/remember/references/why.md"
	idDigest   = "skills/memory-digest/SKILL.md"
)

// A fresh install creates both directories and all three files, records the
// owned dirs and per-file hashes, and a re-run is a no-op (AC-41, AC-50).
func TestSkillsFreshAndRerun(t *testing.T) {
	s := newSK(t)
	r := s.run(nil, nil)
	if r.err != nil || r.det.State != StateAbsent || !r.ran {
		t.Fatalf("fresh: %s %v", r.det.State, r.err)
	}
	var dirs, files int
	for _, a := range r.res.Artifacts {
		switch a.Kind {
		case KindDir:
			dirs++
			if a.Step != "skills" || !pathUnder(a.Path, s.p.SkillsDir()) {
				t.Errorf("dir artifact %+v", a)
			}
		case KindFile:
			files++
			b, _ := os.ReadFile(a.Path)
			if a.SHA256 != sha256Hex(b) || a.Step != "skills" {
				t.Errorf("file artifact %+v", a)
			}
		}
	}
	if dirs != 3 || files != 3 { // remember, remember/references, memory-digest
		t.Fatalf("artifacts %+v", r.res.Artifacts)
	}
	if info, _ := os.Stat(s.path("remember", "SKILL.md")); info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	n := len(s.fs.Writes())
	r2 := s.run(manifestOf(r.res), nil)
	if r2.det.State != StateOK || r2.err != nil || r2.ran {
		t.Fatalf("re-run: %s %v ran=%v", r2.det.State, r2.err, r2.ran)
	}
	s.assertNoWrites(n)
}

// The AC-41 / AC-51 states table, per file.
func TestSkillsStates(t *testing.T) {
	rec := func(content string) func(*sk) *Manifest {
		return func(s *sk) *Manifest {
			m := &Manifest{}
			m.Upsert(Artifact{Step: "skills", Kind: KindFile, Path: s.path("remember", "SKILL.md"), SHA256: sha256Hex([]byte(content))})
			return m
		}
	}
	cases := []struct {
		name  string
		file  string // installed content; "" = absent
		m     func(*sk) *Manifest
		want  State
		write bool // Apply (default choice) writes the file
	}{
		{"absent", "", nil, StateAbsent, true},
		{"equal to embedded, unrecorded (hand install of this version)", "remember v2\n", nil, StateOK, false},
		{"equal to embedded, recorded", "remember v2\n", rec("remember v2\n"), StateOK, false},
		{"recorded unedited, older embedded", "remember v1\n", rec("remember v1\n"), StateOutdated, true},
		{"recorded then edited", "mine\n", rec("remember v1\n"), StateModified, false},
		{"hand install of another version", "remember v0\n", nil, StateModified, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSK(t)
			if tc.file != "" {
				s.write(s.path("remember", "SKILL.md"), tc.file, 0o644)
			}
			var m *Manifest
			if tc.m != nil {
				m = tc.m(s)
			}
			r := s.run(m, nil)
			if r.err != nil {
				t.Fatal(r.err)
			}
			if got := r.stateOf(idRemember); got != tc.want {
				t.Fatalf("state %s, want %s", got, tc.want)
			}
			b, _ := os.ReadFile(s.path("remember", "SKILL.md"))
			switch {
			case tc.write && string(b) != "remember v2\n":
				t.Errorf("not refreshed: %q", b)
			case !tc.write && tc.file != "" && string(b) != tc.file:
				t.Errorf("file changed without consent: %q", b)
			}
			// Modified files are kept by default: the engine's --yes behavior.
			if tc.want == StateModified {
				if baks, _ := filepath.Glob(s.path("remember", "SKILL.md") + ".bak*"); len(baks) != 0 {
					t.Errorf("backup for a kept file: %v", baks)
				}
			}
		})
	}
	// Outdated is replaced without a backup (the content is ours).
	s := newSK(t)
	s.write(s.path("remember", "SKILL.md"), "remember v1\n", 0o644)
	_ = s.run(rec("remember v1\n")(s), nil)
	if baks, _ := filepath.Glob(s.path("remember", "SKILL.md") + ".bak*"); len(baks) != 0 {
		t.Errorf("backup for an outdated file: %v", baks)
	}
}

// Overwrite of a modified file: a unique 0600 backup beside it; the existing
// directory is not recorded as ours; an existing mode is kept.
func TestSkillsOverwriteModified(t *testing.T) {
	s := newSK(t)
	s.write(s.path("remember", "SKILL.md"), "mine\n", 0o640)
	r := s.run(nil, map[string]Choice{idRemember: ChoiceApply})
	if r.err != nil || !r.ran {
		t.Fatal(r.err)
	}
	if string(mustRead(t, s.path("remember", "SKILL.md"))) != "remember v2\n" {
		t.Error("not overwritten")
	}
	if info, _ := os.Stat(s.path("remember", "SKILL.md")); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640 kept", info.Mode().Perm())
	}
	baks, _ := filepath.Glob(s.path("remember", "SKILL.md") + ".bak.claude-memory.*")
	if len(baks) != 1 || string(mustRead(t, baks[0])) != "mine\n" {
		t.Fatalf("backups %v", baks)
	}
	if info, _ := os.Stat(baks[0]); info.Mode().Perm() != BackupFileMode {
		t.Errorf("backup mode %v", info.Mode().Perm())
	}
	for _, a := range r.res.Artifacts {
		if a.Kind == KindDir && strings.HasSuffix(a.Path, "remember") {
			t.Errorf("a directory that already existed is recorded as owned: %+v", a)
		}
	}
	// memory-digest's directory was created by this run.
	if _, ok := manifestOf(r.res).Lookup(KindDir, filepath.Join(s.p.SkillsDir(), "memory-digest"), ""); !ok {
		t.Errorf("owned dir missing: %+v", r.res.Artifacts)
	}
	// A second overwrite never reuses the backup name.
	s.write(s.path("remember", "SKILL.md"), "mine again\n", 0o640)
	if r := s.run(nil, map[string]Choice{idRemember: ChoiceApply}); r.err != nil {
		t.Fatal(r.err)
	}
	if baks, _ := filepath.Glob(s.path("remember", "SKILL.md") + ".bak.claude-memory.*"); len(baks) != 2 {
		t.Errorf("backups %v", baks)
	}
}

// Only the modified file is kept; its siblings are still installed (per-file
// artifacts), and the kept one is not recorded.
func TestSkillsPerFileChoice(t *testing.T) {
	s := newSK(t)
	s.write(s.path("remember", "SKILL.md"), "mine\n", 0o644)
	r := s.run(nil, nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if string(mustRead(t, s.path("remember", "SKILL.md"))) != "mine\n" {
		t.Error("the modified file changed")
	}
	if string(mustRead(t, s.path("remember", "references/why.md"))) != "why v2\n" || string(mustRead(t, s.path("memory-digest", "SKILL.md"))) != "digest v2\n" {
		t.Error("siblings not installed")
	}
	for _, a := range r.res.Artifacts {
		if a.Kind == KindFile && a.Path == s.path("remember", "SKILL.md") {
			t.Error("a kept modified file was recorded")
		}
	}
}

// AC-41: the first run after a hand install says so (a Note).
func TestSkillsHandInstallNote(t *testing.T) {
	s := newSK(t)
	s.write(s.path("remember", "SKILL.md"), "older hand copy\n", 0o644)
	r := s.run(nil, nil)
	found := false
	for _, n := range r.det.Notes {
		found = found || (n.Level == NoteInfo && strings.Contains(n.Text, "manual install") && strings.Contains(n.Text, "choose overwrite to adopt ours"))
	}
	if !found {
		t.Errorf("notes %+v", r.det.Notes)
	}
	// A recorded edit is not a hand install.
	s2 := newSK(t)
	s2.write(s2.path("remember", "SKILL.md"), "edited\n", 0o644)
	m := &Manifest{}
	m.Upsert(Artifact{Step: "skills", Kind: KindFile, Path: s2.path("remember", "SKILL.md"), SHA256: sha256Hex([]byte("v1"))})
	if r := s2.run(m, nil); len(r.det.Notes) != 0 {
		t.Errorf("notes %+v", r.det.Notes)
	}
}

// AC-39 analogue: a file edited between Plan and Apply aborts the write.
func TestSkillsTokenDetectsConcurrentChange(t *testing.T) {
	s := newSK(t)
	s.write(s.path("remember", "SKILL.md"), "mine\n", 0o644)
	st := NewRunState(Inputs{})
	step := SkillsStep{}
	ctx := context.Background()
	ch := Choices{idRemember: ChoiceApply, idWhy: ChoiceApply, idDigest: ChoiceApply}
	plan, err := step.Plan(ctx, s.rp(), st, ch)
	if err != nil || plan.Token == "" {
		t.Fatal(err)
	}
	s.write(s.path("remember", "SKILL.md"), "mine, edited after the confirmation\n", 0o644)
	n := len(s.fs.Writes())
	if _, err := step.Apply(ctx, s.wp(), st, plan); !errors.Is(err, ErrSkillChanged) {
		t.Fatalf("apply: %v", err)
	}
	s.assertNoWrites(n)
}

// Adopt returns only ok files, never a directory.
func TestSkillsAdopt(t *testing.T) {
	s := newSK(t)
	s.write(s.path("remember", "SKILL.md"), "remember v2\n", 0o644)
	s.write(s.path("remember", "references/why.md"), "edited\n", 0o644)
	got := SkillsStep{Version: "v1"}.Adopt(context.Background(), s.rp(), NewRunState(Inputs{}))
	if len(got) != 1 || got[0].Kind != KindFile || got[0].Path != s.path("remember", "SKILL.md") || got[0].SHA256 != sha256Hex([]byte("remember v2\n")) {
		t.Fatalf("adopt %+v", got)
	}
}

// Symlinks: inside Home the target is written and the link stays; outside Home
// the file is refused (modified; an overwrite is an error), untouched.
func TestSkillsSymlink(t *testing.T) {
	t.Run("inside home", func(t *testing.T) {
		s := newSK(t)
		target := filepath.Join(s.p.Home, "dotfiles", "remember.md")
		s.write(target, "remember v1\n", 0o600)
		if err := os.MkdirAll(filepath.Dir(s.path("remember", "SKILL.md")), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, s.path("remember", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		m := &Manifest{}
		m.Upsert(Artifact{Step: "skills", Kind: KindFile, Path: s.path("remember", "SKILL.md"), SHA256: sha256Hex([]byte("remember v1\n"))})
		if r := s.run(m, nil); r.err != nil {
			t.Fatal(r.err)
		}
		if li, _ := os.Lstat(s.path("remember", "SKILL.md")); li.Mode()&fs.ModeSymlink == 0 {
			t.Error("the symlink was replaced")
		}
		if string(mustRead(t, target)) != "remember v2\n" {
			t.Error("target not refreshed")
		}
	})
	t.Run("outside home", func(t *testing.T) {
		s := newSK(t)
		outside := filepath.Join(s.root, "elsewhere", "remember.md")
		s.write(outside, "x\n", 0o644)
		if err := os.MkdirAll(filepath.Dir(s.path("remember", "SKILL.md")), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, s.path("remember", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		r := s.run(nil, nil)
		if r.stateOf(idRemember) != StateModified {
			t.Fatalf("state %s", r.stateOf(idRemember))
		}
		if string(mustRead(t, outside)) != "x\n" {
			t.Error("outside file touched")
		}
		r = s.run(nil, map[string]Choice{idRemember: ChoiceApply})
		if r.err == nil || !strings.Contains(r.err.Error(), "outside") {
			t.Errorf("overwrite of an outside symlink: %v", r.err)
		}
		if string(mustRead(t, outside)) != "x\n" {
			t.Error("outside file touched")
		}
	})
}

func TestSkillsModifiedDiff(t *testing.T) {
	s := newSK(t)
	s.write(s.path("remember", "SKILL.md"), "mine\n", 0o644)
	d, err := SkillsStep{}.ModifiedDiff(context.Background(), s.rp(), NewRunState(Inputs{}), idRemember)
	if err != nil || !strings.Contains(d, "-mine") || !strings.Contains(d, "+remember v2") {
		t.Errorf("diff %q %v", d, err)
	}
	if _, err := (SkillsStep{}).ModifiedDiff(context.Background(), s.rp(), NewRunState(Inputs{}), "skills/nope"); err == nil {
		t.Error("unknown id accepted")
	}
}
