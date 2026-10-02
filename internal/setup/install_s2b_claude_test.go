package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-memory/integration"
)

// Engine-level tests of the skills and claude-md steps (WI-S2-11, WI-S2-12)
// over the full rig, with the real embedded assets.

func embeddedSkill(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(integration.FS, "skills/"+name+"/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func embeddedSection(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(integration.FS, "claude-md-section.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func loadManifestOf(t *testing.T, r *fullRig) *Manifest {
	t.Helper()
	m, err := LoadManifest(r.fs, r.p)
	if err != nil || !m.Present() {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	return m.Manifest
}

// A converging first run installs both skills and the block, records what
// uninstall needs (owned dirs, per-file hashes, the block hash and CreatedFile),
// and the final doctor is green for both checks.
func TestFullRunSkillsAndClaudeMD(t *testing.T) {
	r := newFullRig(t)
	res := r.run(r.inputs())
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	for _, id := range []string{"skills", "claude-md"} {
		if o := outcomeOf(res, id); o != OutcomeApplied {
			t.Errorf("%s: %q, want applied", id, o)
		}
	}
	m := loadManifestOf(t, r)
	for _, name := range SkillNames {
		dir := filepath.Join(r.p.SkillsDir(), name)
		if a, ok := m.Lookup(KindDir, dir, ""); !ok || a.Step != "skills" {
			t.Errorf("owned dir %s missing: %+v", dir, m.Artifacts)
		}
		a, ok := m.Lookup(KindFile, filepath.Join(dir, "SKILL.md"), "")
		if !ok || a.SHA256 != sha256Hex(embeddedSkill(t, name)) {
			t.Errorf("file artifact %s: %+v", name, a)
		}
	}
	blk, ok := m.Lookup(KindMDBlock, filepath.Join(r.p.ClaudeDir, "CLAUDE.md"), ClaudeMDBlockIdentity)
	if !ok || !blk.CreatedFile || blk.SHA256 != MDSectionHash(embeddedSection(t)) || blk.Step != "claude-md" {
		t.Errorf("md-block artifact: %+v", blk)
	}
	for _, id := range []string{"skills", "claude-md"} {
		if strings.Contains(r.out.String(), "FAIL  "+id) || strings.Contains(r.out.String(), "WARN  "+id) {
			t.Errorf("final doctor flags %s:\n%s", id, r.out)
		}
	}
}

// AC-12 / AC-6: --yes keeps a modified skill file and a modified block (drift
// notes, exit 0, no write, no backup); the repeated run is a no-op.
func TestYesKeepsModifiedSkillAndBlock(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	skill := filepath.Join(r.p.SkillsDir(), "remember", "SKILL.md")
	md := filepath.Join(r.p.ClaudeDir, "CLAUDE.md")
	if err := os.WriteFile(skill, []byte("my own skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edited := "# mine\n" + MDBeginMarker + "\nmy own words\n" + MDEndMarker + "\n"
	if err := os.WriteFile(md, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	writes := r.nonLockWrites()
	res := r.run(r.inputs())
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	if n := r.nonLockWrites() - writes; n != 0 {
		t.Errorf("%d writes under --yes over modified files: %v", n, r.fs.Writes()[writes:])
	}
	if string(mustRead(t, skill)) != "my own skill\n" || string(mustRead(t, md)) != edited {
		t.Error("a modified file was overwritten under --yes")
	}
	drift := map[string]bool{}
	for _, o := range res.Outcomes {
		for _, n := range o.Notes {
			if strings.Contains(n.Text, "drift: skills/remember/SKILL.md") || strings.Contains(n.Text, "drift: claude-md/block") {
				drift[o.StepID] = true
			}
		}
	}
	if !drift["skills"] || !drift["claude-md"] {
		t.Errorf("drift notes missing: %v\n%s", drift, r.out)
	}
	for _, o := range res.Outcomes {
		if (o.StepID == "skills" || o.StepID == "claude-md") && o.Outcome != OutcomeUnchanged {
			t.Errorf("%s: %q", o.StepID, o.Outcome)
		}
	}
}

// AC-42 / AC-12: --yes with an explicit --claude-md inside a git repository is
// skipped with the reason (exit 0, nothing written there); outside git it is
// written; the user-level default is written even when Home is a git repo.
func TestYesClaudeMDGitRule(t *testing.T) {
	t.Run("explicit in git: skipped", func(t *testing.T) {
		r := newFullRig(t)
		repo := filepath.Join(r.p.Cwd, "acme")
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(repo, "CLAUDE.md")
		res := r.run(r.inputs(func(in *Inputs) { in.ClaudeMD = target }))
		if res.ExitCode != ExitOK {
			t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
		}
		if o := outcomeOf(res, "claude-md"); o != OutcomeSkipped {
			t.Errorf("claude-md: %q, want skipped", o)
		}
		if _, err := os.Stat(target); err == nil {
			t.Error("the repository file was written under --yes")
		}
		if _, err := os.Stat(filepath.Join(r.p.ClaudeDir, "CLAUDE.md")); err == nil {
			t.Error("the user-level file was written instead")
		}
		if !strings.Contains(r.out.String(), "inside the git repository") {
			t.Errorf("the reason is not printed:\n%s", r.out)
		}
		// The skipped step did not stop the rest: skills are installed.
		if o := outcomeOf(res, "skills"); o != OutcomeApplied {
			t.Errorf("skills: %q", o)
		}
	})
	t.Run("explicit outside git: written", func(t *testing.T) {
		r := newFullRig(t)
		dir := filepath.Join(r.p.Cwd, "plain")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "CLAUDE.md")
		if res := r.run(r.inputs(func(in *Inputs) { in.ClaudeMD = target })); res.ExitCode != ExitOK {
			t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
		}
		if !strings.Contains(string(mustRead(t, target)), MDBeginMarker) {
			t.Error("block not written")
		}
		m := loadManifestOf(t, r)
		if _, ok := m.Lookup(KindMDBlock, target, ClaudeMDBlockIdentity); !ok {
			t.Errorf("artifact missing: %+v", m.Artifacts)
		}
		// The next run refreshes the recorded path without any flag.
		r.out.Reset()
		writes := r.nonLockWrites()
		if res := r.run(r.inputs()); res.ExitCode != ExitOK || r.nonLockWrites() != writes {
			t.Errorf("re-run: exit %d, %d new writes\n%s", res.ExitCode, r.nonLockWrites()-writes, r.out)
		}
	})
	t.Run("default in a git Home: written", func(t *testing.T) {
		r := newFullRig(t)
		if err := os.MkdirAll(filepath.Join(r.p.Home, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if res := r.run(r.inputs()); res.ExitCode != ExitOK || outcomeOf(res, "claude-md") != OutcomeApplied {
			t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
		}
	})
}

// A dry run changes nothing but shows the diffs of both steps (AC-13).
func TestDryRunSkillsAndClaudeMD(t *testing.T) {
	r := newFullRig(t)
	res := r.run(r.inputs(func(in *Inputs) { in.DryRun = true }))
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	if r.nonLockWrites() != 0 {
		t.Errorf("writes in dry-run: %v", r.fs.Writes())
	}
	for _, want := range []string{"skills/remember/SKILL.md", "create the file with the claude-memory block", MDBeginMarker} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("dry-run output lacks %q", want)
		}
	}
}

// --skip skills,claude-md installs neither and exits 0.
func TestSkipSkillsAndClaudeMD(t *testing.T) {
	r := newFullRig(t)
	res := r.run(r.inputs(func(in *Inputs) { in.Skip = []string{"skills", "claude-md"} }))
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	for _, id := range []string{"skills", "claude-md"} {
		if o := outcomeOf(res, id); o != OutcomeSkipped {
			t.Errorf("%s: %q", id, o)
		}
	}
	if _, err := os.Stat(r.p.SkillsDir()); err == nil {
		t.Error("skills dir created")
	}
	if _, err := os.Stat(filepath.Join(r.p.ClaudeDir, "CLAUDE.md")); err == nil {
		t.Error("CLAUDE.md created")
	}
}

// AC-51: a hand install (INSTALL.md steps 6-7) of this version is recognized:
// nothing is rewritten, no skill directory is claimed as ours, and the files
// and block are adopted into the manifest; the re-run is a no-op.
func TestHandInstalledSkillsAndBlockAdopted(t *testing.T) {
	r := newFullRig(t)
	for _, name := range SkillNames {
		p := filepath.Join(r.p.SkillsDir(), name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, embeddedSkill(t, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	md := filepath.Join(r.p.ClaudeDir, "CLAUDE.md")
	hand := "# my notes\n\n" + MDBeginMarker + "\n" + embeddedSection(t) + MDEndMarker + "\n"
	if err := os.MkdirAll(r.p.ClaudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(md, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	res := r.run(r.inputs())
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	for _, id := range []string{"skills", "claude-md"} {
		if o := outcomeOf(res, id); o != OutcomeUnchanged {
			t.Errorf("%s: %q, want unchanged", id, o)
		}
	}
	for _, w := range r.fs.Writes() {
		if strings.Contains(w, "/skills/") || strings.Contains(w, "CLAUDE.md") {
			t.Errorf("a hand-installed file was rewritten: %s", w)
		}
	}
	if string(mustRead(t, md)) != hand {
		t.Error("CLAUDE.md changed")
	}
	m := loadManifestOf(t, r)
	for _, name := range SkillNames {
		if _, ok := m.Lookup(KindFile, filepath.Join(r.p.SkillsDir(), name, "SKILL.md"), ""); !ok {
			t.Errorf("%s not adopted", name)
		}
		if _, ok := m.Lookup(KindDir, filepath.Join(r.p.SkillsDir(), name), ""); ok {
			t.Errorf("the pre-existing %s directory was claimed as ours", name)
		}
	}
	if b, ok := m.Lookup(KindMDBlock, md, ClaudeMDBlockIdentity); !ok || b.CreatedFile {
		t.Errorf("block adoption: %+v", b)
	}
	r.out.Reset()
	writes := r.nonLockWrites()
	if res := r.run(r.inputs()); res.ExitCode != ExitOK || r.nonLockWrites() != writes {
		t.Errorf("re-run wrote %v", r.fs.Writes()[writes:])
	}
}

// AC-41 interactive: show diff, then overwrite; the backup is kept and the
// confirmation of the combined plan is asked once.
func TestInteractiveSkillOverwriteWithDiff(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	skill := filepath.Join(r.p.SkillsDir(), "remember", "SKILL.md")
	if err := os.WriteFile(skill, []byte("my own skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.ui = NewFakePrompter(t, true).
		ExpectSelect("Skills [modified]", 0).
		ExpectSelect("skills/remember/SKILL.md differs from this version", 2). // show diff
		ExpectSelect("skills/remember/SKILL.md differs from this version", 1). // overwrite
		ExpectConfirm("Apply this plan?", true)
	res := r.run(r.inputs(func(in *Inputs) { in.Yes = false }))
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d, err %v\n%s", res.ExitCode, res.Err, r.out)
	}
	if !strings.Contains(r.out.String(), "-my own skill") {
		t.Errorf("the diff was not shown:\n%s", r.out)
	}
	if string(mustRead(t, skill)) != string(embeddedSkill(t, "remember")) {
		t.Error("not overwritten")
	}
	if baks, _ := filepath.Glob(skill + ".bak.claude-memory.*"); len(baks) != 1 || string(mustRead(t, baks[0])) != "my own skill\n" {
		t.Errorf("backups %v", baks)
	}
	// Choosing keep leaves the file alone.
	if err := os.WriteFile(skill, []byte("again mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.ui = NewFakePrompter(t, true).
		ExpectSelect("Skills [modified]", 0).
		ExpectSelect("differs from this version", 0)
	if res := r.run(r.inputs(func(in *Inputs) { in.Yes = false })); res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	if string(mustRead(t, skill)) != "again mine\n" {
		t.Error("kept file changed")
	}
}

// S9: when no diff can be built (a hand-pasted section), the interactive run
// does not offer "overwrite": it says why and keeps.
func TestInteractiveHandPastedSectionIsKept(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	md := filepath.Join(r.p.ClaudeDir, "CLAUDE.md")
	pasted := "# mine\n\n" + embeddedSection(t)
	if err := os.WriteFile(md, []byte(pasted), 0o644); err != nil {
		t.Fatal(err)
	}
	r.ui = NewFakePrompter(t, true).ExpectSelect("CLAUDE.md section [modified]", 0) // apply; no further question
	res := r.run(r.inputs(func(in *Inputs) { in.Yes = false }))
	if res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	if string(mustRead(t, md)) != pasted {
		t.Error("the hand-pasted file changed")
	}
	if !strings.Contains(r.out.String(), "hand-pasted") {
		t.Errorf("the reason is not printed:\n%s", r.out)
	}
}

// M2: a block recorded at v1, updated by hand to v2, and then v3 released: the
// adoption refreshes the stale hash (so v3 is "outdated", not "modified"); the
// no-op re-run still writes nothing (AC-50).
func TestAdoptRefreshesStaleHash(t *testing.T) {
	r := newFullRig(t)
	r.mustConverge()
	md := filepath.Join(r.p.ClaudeDir, "CLAUDE.md")
	m := loadManifestOf(t, r)
	blk, _ := m.LastMDBlock()
	m.Upsert(Artifact{Step: blk.Step, Kind: blk.Kind, Path: blk.Path, Identity: blk.Identity, SHA256: MDSectionHash("v1 wording\n"), Version: blk.Version, CreatedFile: blk.CreatedFile})
	if err := SaveManifest(r.fs, r.p, m); err != nil {
		t.Fatal(err)
	}
	_ = md
	if res := r.run(r.inputs()); res.ExitCode != ExitOK {
		t.Fatalf("exit %d\n%s", res.ExitCode, r.out)
	}
	got, _ := loadManifestOf(t, r).LastMDBlock()
	if got.SHA256 != MDSectionHash(embeddedSection(t)) || !got.CreatedFile {
		t.Errorf("stale hash not refreshed: %+v", got)
	}
	writes := r.nonLockWrites()
	r.out.Reset()
	if res := r.run(r.inputs()); res.ExitCode != ExitOK || r.nonLockWrites() != writes {
		t.Errorf("no-op re-run wrote %v", r.fs.Writes()[writes:])
	}
}
