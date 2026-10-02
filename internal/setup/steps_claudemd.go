package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// ClaudeMDStepID is the id of the CLAUDE.md step (AC-7: after skills).
const ClaudeMDStepID = "claude-md"

// ClaudeMDArtifact is the step's one artifact id.
const ClaudeMDArtifact = "claude-md/block"

// ClaudeMDBlockIdentity names the managed block in a manifest md-block artifact.
const ClaudeMDBlockIdentity = "claude-memory"

// ErrClaudeMDChanged is returned by Apply when the target file differs from what
// Plan saw (the user, or another tool, edited it while the plan awaited
// confirmation): the consent covered another content, so nothing is written.
var ErrClaudeMDChanged = errors.New("CLAUDE.md changed during install, re-run")

// ClaudeMDStep inserts or refreshes the memory section between the
// "<!-- BEGIN claude-memory -->" and "<!-- END claude-memory -->" markers of a
// CLAUDE.md file (AC-42), leaving every byte outside the markers alone. The
// library (mdblock.go) is fence-aware and refuses unbalanced, duplicated or
// out-of-order markers; the step reports that as blocked and never guesses.
//
// Target (AC-42):
//   - default <ClaudeDir>/CLAUDE.md (user level);
//   - --claude-md PATH (relative to the working directory, "~/" allowed);
//   - a path recorded in the manifest by an earlier install;
//   - interactively, Configure offers the user-level file, the CLAUDE.md of the
//     git repository the working directory is in, or another path.
//
// --yes (and --upgrade) writes the user-level default unconditionally (also
// when Home itself is a git repository: it is the user's own file) and never
// writes an explicitly given --claude-md path inside a git repository: the step
// is then skipped with the reason (Detection.SkipReason), exit code 0. A path
// recorded by an earlier install is refreshed without that rule (its first
// write was consented to), and an explicit path equal to the user-level file is
// the default.
//
// States come from MDBlockState: ok / outdated (our recorded body) / modified
// (edited block, or a hand-pasted section without markers: delete it first, so
// an overwrite is refused) / absent. A modified block is kept by default and
// under --yes; overwriting it asks the AC-41-style question and keeps a
// timestamped backup of the whole file (like settings.json). The write is
// atomic, keeps the file's mode and follows a symlink that stays inside Home;
// Plan.Token (the file's hash) makes Apply abort when the file changed since.
//
// Recorded: one md-block artifact (path as asked, body hash = MDSectionHash of
// the embedded section) with CreatedFile when this install created the file,
// so uninstall can delete the file only if our block is its only content.
type ClaudeMDStep struct {
	// Version is the running binary's version, recorded on its artifact.
	Version string
}

var (
	_ Step           = ClaudeMDStep{}
	_ Seeder         = ClaudeMDStep{}
	_ Configurer     = ClaudeMDStep{}
	_ Adopter        = ClaudeMDStep{}
	_ ModifiedDiffer = ClaudeMDStep{}
)

// ID implements Step.
func (ClaudeMDStep) ID() string { return ClaudeMDStepID }

// Title implements Step.
func (ClaudeMDStep) Title() string { return "CLAUDE.md section" }

// Requires implements Step.
func (ClaudeMDStep) Requires() []string { return nil }

func defaultClaudeMD(p Paths) string { return filepath.Join(p.ClaudeDir, "CLAUDE.md") }

// resolveClaudeMDPath makes a --claude-md value absolute: "~/" is Home, a
// relative path is relative to the working directory.
func resolveClaudeMDPath(p Paths, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", errors.New("empty path")
	case raw == "~" || strings.HasPrefix(raw, "~/"):
		raw = filepath.Join(p.Home, strings.TrimPrefix(raw, "~"))
	case !filepath.IsAbs(raw):
		if p.Cwd == "" {
			return "", fmt.Errorf("%q is relative and the working directory is unknown", raw)
		}
		raw = filepath.Join(p.Cwd, raw)
	}
	return filepath.Clean(raw), nil
}

func recordedClaudeMDBlock(m *Manifest) (Artifact, bool) {
	for _, a := range m.Find(KindMDBlock) {
		if a.Step == ClaudeMDStepID {
			return a, true
		}
	}
	return Artifact{}, false
}

// Seed implements Seeder: flag, then the path an earlier install recorded, then
// the user-level default. An invalid --claude-md value is a usage error.
func (ClaudeMDStep) Seed(_ context.Context, rc ReadPorts, st *RunState) ([]Note, error) {
	def := defaultClaudeMD(rc.Paths)
	switch {
	case st.Inputs.ClaudeMD != "":
		p, err := resolveClaudeMDPath(rc.Paths, st.Inputs.ClaudeMD)
		if err != nil {
			return nil, fmt.Errorf("--claude-md: %w", err)
		}
		if p == def {
			st.ClaudeMDTarget.Set(def, SourceDefault) // the user's own file, not a shared one
		} else {
			st.ClaudeMDTarget.Set(p, SourceFlag)
		}
	default:
		if a, ok := recordedClaudeMDBlock(st.Prior.Manifest); ok && a.Path != "" {
			st.ClaudeMDTarget.Set(a.Path, SourceManifest)
		} else {
			st.ClaudeMDTarget.Set(def, SourceDefault)
		}
	}
	return nil, nil
}

type mdView struct {
	target   string
	section  string
	file     *SettingsFile
	state    State
	detail   string
	recorded string
}

func claudeMDTarget(rc ReadPorts, st *RunState) string {
	if st.ClaudeMDTarget.IsSet() {
		return st.ClaudeMDTarget.Get()
	}
	return defaultClaudeMD(rc.Paths)
}

// loadMDView reads the target and classifies the block. The error is a refusal
// (markers, symlink outside Home, not a regular file) or an I/O failure.
func loadMDView(rfs ReadFS, assets fs.FS, p Paths, st *RunState, target string) (mdView, error) {
	v := mdView{target: target}
	sec, err := fs.ReadFile(assets, assetClaudeMDSection)
	if err != nil {
		return v, fmt.Errorf("embedded %s: %w", assetClaudeMDSection, err)
	}
	v.section = string(sec)
	if a, ok := st.Prior.Manifest.Lookup(KindMDBlock, target, ClaudeMDBlockIdentity); ok {
		v.recorded = a.SHA256
	}
	if v.file, err = ReadSettingsFile(rfs, p.Home, target); err != nil {
		var sr *SettingsRefusal
		if errors.As(err, &sr) {
			return v, fmt.Errorf("refusing to edit %s: %s", target, sr.Reason)
		}
		return v, fmt.Errorf("read %s: %w", target, err)
	}
	if v.state, v.detail, err = MDBlockState(v.file.Content, v.file.Exists, v.section, v.recorded); err != nil {
		return v, fmt.Errorf("%s: %w", target, err)
	}
	return v, nil
}

// Detect implements Step.
func (ClaudeMDStep) Detect(_ context.Context, rc ReadPorts, st *RunState) Detection {
	target := claudeMDTarget(rc, st)
	v, err := loadMDView(rc.FS, rc.Assets, rc.Paths, st, target)
	if err != nil {
		d := Detection{State: StateBlocked, Detail: err.Error()}
		if errors.Is(err, ErrMDMarkers) {
			d.Remedy = "fix the claude-memory markers in " + target + " by hand (one BEGIN line, then one END line), then re-run"
		}
		return d
	}
	d := Detection{State: v.state, Detail: target + ": " + v.detail,
		Artifacts: []ArtifactState{{ID: ClaudeMDArtifact, State: v.state, Detail: v.detail}}}
	if a, ok := recordedClaudeMDBlock(st.Prior.Manifest); ok && a.Path != target {
		d.Notes = append(d.Notes, Note{NoteInfo, "a claude-memory block recorded earlier stays in " + a.Path + " (uninstall removes it)"})
	}
	if st.Auto && st.ClaudeMDTarget.Source() == SourceFlag && v.state != StateOK {
		for _, p := range []string{target, v.file.Target} {
			if in, root, err := InGitRepo(rc.FS, p, rc.Paths.GitCeiling); err == nil && in {
				d.SkipReason = fmt.Sprintf("--claude-md %s is inside the git repository %s; --yes never writes it (run without --yes to review the diff, or omit --claude-md for %s)",
					target, root, defaultClaudeMD(rc.Paths))
				d.Detail = "skipped: --claude-md is inside a git repository"
				d.Notes = append(d.Notes, Note{NoteWarn, "claude-md skipped: " + d.SkipReason})
				break
			}
		}
	}
	return d
}

// Configure implements Configurer: interactive only. Under --yes the target
// comes from the flag, the manifest or the default and nothing is asked.
func (ClaudeMDStep) Configure(_ context.Context, rc ReadPorts, ui Prompter, st *RunState) error {
	if !ui.Interactive() {
		return nil
	}
	switch src := st.ClaudeMDTarget.Source(); {
	case src == SourceFlag, src == SourceManifest && !st.Inputs.Reconfigure:
		return nil
	}
	cur := claudeMDTarget(rc, st)
	def := defaultClaudeMD(rc.Paths)
	paths := []string{def}
	opts := []string{"user level: " + def + " (recommended)"}
	if rc.Paths.Cwd != "" {
		if in, root, err := InGitRepo(rc.FS, filepath.Join(rc.Paths.Cwd, "CLAUDE.md"), rc.Paths.GitCeiling); err == nil && in {
			if repoFile := filepath.Join(root, "CLAUDE.md"); repoFile != def {
				paths = append(paths, repoFile)
				opts = append(opts, "this repository: "+repoFile)
			}
		}
	}
	opts = append(opts, "another path")
	defIdx := 0
	for i, p := range paths {
		if p == cur {
			defIdx = i
		}
	}
	i, err := ui.Select("Where should the claude-memory section go?", opts, defIdx)
	if err != nil {
		return err
	}
	chosen := cur
	if i < len(paths) {
		chosen = paths[i]
	} else {
		raw, err := ui.Text("Path of the CLAUDE.md file", cur, func(s string) error {
			_, err := resolveClaudeMDPath(rc.Paths, s)
			return err
		})
		if err != nil {
			return err
		}
		if chosen, err = resolveClaudeMDPath(rc.Paths, raw); err != nil {
			return err
		}
	}
	if chosen != cur {
		st.ClaudeMDTarget.Set(chosen, SourcePrompt)
	}
	return nil
}

func mdToken(f *SettingsFile) string { return settingsToken(f) }

// upsert computes the file content with the block written; a hand-pasted
// section is refused with its hint.
func (v mdView) upsert() ([]byte, bool, error) {
	out, changed, err := UpsertMDBlock(v.file.Content, v.section)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", v.target, err)
	}
	return out, changed, nil
}

// Plan implements Step.
func (ClaudeMDStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	var p Plan
	if ch[ClaudeMDArtifact] != ChoiceApply {
		return p, nil
	}
	v, err := loadMDView(rc.FS, rc.Assets, rc.Paths, st, claudeMDTarget(rc, st))
	if err != nil {
		return p, err
	}
	if v.state == StateOK {
		return p, nil
	}
	out, changed, err := v.upsert()
	if err != nil {
		return p, err
	}
	if !changed {
		return p, nil
	}
	if !v.file.Exists && !underDir(rc.FS, v.file.Target, rc.Paths.ClaudeDir) {
		if _, err := rc.FS.Stat(filepath.Dir(v.file.Target)); err != nil {
			return p, fmt.Errorf("directory of %s does not exist: %w", v.target, err)
		}
	}
	desc := "insert the claude-memory block"
	switch v.state {
	case StateOutdated:
		desc = "refresh the claude-memory block written by an earlier install"
	case StateModified:
		desc = "replace your modified claude-memory block"
	}
	if !v.file.Exists {
		desc = "create the file with the claude-memory block"
	}
	p.Actions = append(p.Actions, Action{Artifact: ClaudeMDArtifact, Verb: "write", Path: v.target, Desc: desc})
	if v.file.Exists {
		p.Notes = append(p.Notes, Note{NoteInfo, "a timestamped backup of " + v.file.Target + " is kept beside it"})
	}
	for _, c := range []string{v.target, v.file.Target} {
		if in, root, err := InGitRepo(rc.FS, c, rc.Paths.GitCeiling); err == nil && in {
			p.Notes = append(p.Notes, Note{NoteInfo, v.target + " is inside the git repository " + root + ": the block becomes part of a shared file; review the diff"})
			break
		}
	}
	old := v.target
	if !v.file.Exists {
		old = ""
	}
	p.Diffs = append(p.Diffs, Diff{Artifact: ClaudeMDArtifact, Path: v.target, Unified: UnifiedDiff(old, v.target, v.file.Content, out)})
	p.Token = mdToken(v.file)
	return p, nil
}

func (s ClaudeMDStep) artifact(v mdView, createdFile bool) Artifact {
	return Artifact{Step: ClaudeMDStepID, Kind: KindMDBlock, Path: v.target, Identity: ClaudeMDBlockIdentity,
		SHA256: MDSectionHash(v.section), Version: s.Version, CreatedFile: createdFile}
}

func priorCreatedFile(m *Manifest, target string) bool {
	a, ok := m.Lookup(KindMDBlock, target, ClaudeMDBlockIdentity)
	return ok && a.CreatedFile
}

// Apply implements Step: re-read, abort when the file changed since Plan, then
// one backup and one atomic write (mode kept, in-Home symlink followed).
func (s ClaudeMDStep) Apply(_ context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	v, err := loadMDView(wc.FS, wc.Assets, wc.Paths, st, claudeMDTarget(wc.ReadPorts, st))
	if err != nil {
		return res, err
	}
	if p.Token != "" && mdToken(v.file) != p.Token {
		return res, fmt.Errorf("%s: %w", v.file.Target, ErrClaudeMDChanged)
	}
	out, changed, err := v.upsert()
	if err != nil {
		return res, err
	}
	if changed {
		backup, err := WriteSettingsFile(wc.FS, wc.Clock, v.file, out)
		if err != nil {
			if errors.Is(err, ErrSettingsChanged) {
				err = fmt.Errorf("%s: %w", v.file.Target, ErrClaudeMDChanged)
			}
			return res, err
		}
		if backup != "" {
			res.Notes = append(res.Notes, Note{NoteInfo, "backup of the previous " + v.target + ": " + backup})
		}
		old := v.target
		if !v.file.Exists {
			old = ""
		}
		res.Diffs = append(res.Diffs, Diff{Artifact: ClaudeMDArtifact, Path: v.target, Unified: UnifiedDiff(old, v.target, v.file.Content, out)})
	}
	res.Artifacts = append(res.Artifacts, s.artifact(v, !v.file.Exists || priorCreatedFile(st.Prior.Manifest, v.target)))
	return res, nil
}

// Adopt implements Adopter: a block that already holds the embedded section (a
// hand install of this version between the markers) is recorded (AC-51). The
// file is never recorded as created by us (CreatedFile keeps an earlier value).
func (s ClaudeMDStep) Adopt(_ context.Context, rc ReadPorts, st *RunState) []Artifact {
	v, err := loadMDView(rc.FS, rc.Assets, rc.Paths, st, claudeMDTarget(rc, st))
	if err != nil || v.state != StateOK {
		return nil
	}
	return []Artifact{s.artifact(v, priorCreatedFile(st.Prior.Manifest, v.target))}
}

// ModifiedDiff implements ModifiedDiffer: the file as it is -> with this
// version's block. A hand-pasted section cannot be overwritten; the error says
// to delete it first.
func (ClaudeMDStep) ModifiedDiff(_ context.Context, rc ReadPorts, st *RunState, _ string) (string, error) {
	v, err := loadMDView(rc.FS, rc.Assets, rc.Paths, st, claudeMDTarget(rc, st))
	if err != nil {
		return "", err
	}
	out, _, err := v.upsert()
	if err != nil {
		return "", err
	}
	return UnifiedDiff(v.target+" (current)", v.target+" (with this version)", v.file.Content, out), nil
}
