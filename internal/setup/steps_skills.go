package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// SkillsStepID is the id of the skills step (AC-7: after mcp).
const SkillsStepID = "skills"

func skillFileArtifact(name, rel string) string { return SkillsStepID + "/" + name + "/" + rel }
func skillDirArtifact(name string) string       { return SkillsStepID + "/" + name }

// SkillsStep copies each embedded skill directory (remember, memory-digest) to
// <ClaudeDir>/skills/<name>/ and records the sha256 of every file (AC-41). One
// artifact per file (Design 18).
//
// States (AC-41, AC-51): a file equal to the embedded one is ok (and is
// recorded, also when it was put there by hand); equal to what the manifest
// recorded but different from the embedded one -> outdated, refreshed without
// an overwrite Confirm because the content is ours; any other difference
// (edited after install, or a hand install of another version) -> modified,
// kept by default and under --yes, and overwritten only after the interactive
// "show diff / overwrite / keep" question, with a timestamped backup beside
// the file.
//
// Each <ClaudeDir>/skills/<name> directory this step creates is recorded as an
// owned dir artifact, so uninstall can remove it when empty (Design 23); a
// directory that already existed is not. Writes go through the FS port, atomic
// and keeping an existing mode; a file that is an in-Home symlink is edited at
// its target, one outside Home is refused (like settings.json, AC-38).
type SkillsStep struct {
	// Version is the running binary's version, recorded on its artifacts.
	Version string
}

var (
	_ Step           = SkillsStep{}
	_ Adopter        = SkillsStep{}
	_ ModifiedDiffer = SkillsStep{}
)

// ID implements Step.
func (SkillsStep) ID() string { return SkillsStepID }

// Title implements Step.
func (SkillsStep) Title() string { return "Skills" }

// Requires implements Step.
func (SkillsStep) Requires() []string { return nil }

// ErrSkillChanged is returned by Apply when a skill file differs from what Plan
// saw, e.g. it was edited while the plan awaited confirmation: the overwrite
// the user confirmed was for another content (AC-6), so it is not done.
var ErrSkillChanged = errors.New("skill file changed during install, re-run")

type skillFile struct {
	name, rel, id string
	path          string // <ClaudeDir>/skills/<name>/<rel>, as asked
	embedded      []byte
	state         State
	detail        string
	exists        bool
	content       []byte
	target        string // the file written: path, or its in-Home symlink target
	mode          fs.FileMode
	refused       error // the file cannot be edited (outside symlink, not regular)
	recorded      string
}

type skillsAnalysis struct {
	files        []skillFile
	dirMissing   map[string]bool // skill name -> its directory does not exist
	claudeAbsent bool
}

func recordedSkillHash(m *Manifest, p string) string {
	for _, a := range m.Find(KindFile) {
		if a.Step == SkillsStepID && a.Path == p {
			return a.SHA256
		}
	}
	return ""
}

// embeddedSkillFiles lists the files of one embedded skill directory, as
// paths relative to it, in lexical order.
func embeddedSkillFiles(assets fs.FS, name string) ([]string, error) {
	root := path.Join(assetSkillsDir, name)
	var rels []string
	err := fs.WalkDir(assets, root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			rels = append(rels, strings.TrimPrefix(p, root+"/"))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("embedded skill %s: %w", name, err)
	}
	if len(rels) == 0 {
		return nil, fmt.Errorf("embedded skill %s has no files", name)
	}
	return rels, nil
}

func analyzeSkills(rfs ReadFS, assets fs.FS, p Paths, st *RunState) (skillsAnalysis, error) {
	a := skillsAnalysis{dirMissing: map[string]bool{}}
	if _, err := rfs.Stat(p.ClaudeDir); errors.Is(err, fs.ErrNotExist) {
		a.claudeAbsent = true
	}
	for _, name := range SkillNames {
		rels, err := embeddedSkillFiles(assets, name)
		if err != nil {
			return a, err
		}
		dir := filepath.Join(p.SkillsDir(), name)
		if _, err := rfs.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			a.dirMissing[name] = true
		} else if err != nil {
			return a, fmt.Errorf("stat %s: %w", dir, err)
		}
		for _, rel := range rels {
			emb, err := fs.ReadFile(assets, path.Join(assetSkillsDir, name, rel))
			if err != nil {
				return a, fmt.Errorf("embedded skill %s/%s: %w", name, rel, err)
			}
			f := skillFile{name: name, rel: rel, id: skillFileArtifact(name, rel),
				path: filepath.Join(dir, filepath.FromSlash(rel)), embedded: emb}
			f.target = f.path
			f.recorded = recordedSkillHash(st.Prior.Manifest, f.path)
			var sf *SettingsFile
			err = CheckParentInHome(rfs, p.Home, f.path)
			if err == nil {
				sf, err = ReadSettingsFile(rfs, p.Home, f.path)
			}
			var sr *SettingsRefusal
			switch {
			case errors.Is(err, ErrParentOutsideHome):
				f.refused, f.state, f.detail = fmt.Errorf("refusing to edit %w", err), StateModified, err.Error()
			case errors.As(err, &sr):
				f.refused, f.state, f.detail = fmt.Errorf("refusing to edit %s: %s", f.path, sr.Reason), StateModified, sr.Reason
			case err != nil:
				return a, fmt.Errorf("read %s: %w", f.path, err)
			case !sf.Exists:
				f.state, f.detail = StateAbsent, "will be created"
			default:
				f.exists, f.content, f.target, f.mode = true, sf.Content, sf.Target, sf.Mode
				f.state, f.detail = fileState(f.content, emb, f.recorded)
			}
			a.files = append(a.files, f)
		}
	}
	return a, nil
}

// Detect implements Step.
func (SkillsStep) Detect(_ context.Context, rc ReadPorts, st *RunState) Detection {
	an, err := analyzeSkills(rc.FS, rc.Assets, rc.Paths, st)
	if err != nil {
		return Detection{State: StateBlocked, Detail: err.Error()}
	}
	var arts []ArtifactState
	var states []State
	var pending []string
	hand := false
	for _, f := range an.files {
		arts = append(arts, ArtifactState{ID: f.id, State: f.state, Detail: f.detail})
		states = append(states, f.state)
		if f.state != StateOK {
			pending = append(pending, f.name+"/"+f.rel+": "+f.detail)
		}
		if f.state == StateModified && f.recorded == "" && f.exists {
			hand = true
		}
	}
	d := Detection{State: worstState(states...), Artifacts: arts}
	if len(pending) > 0 {
		d.Detail = strings.Join(pending, "; ")
	} else {
		d.Detail = strings.Join(SkillNames, ", ") + " in " + rc.Paths.SkillsDir() + " match this version"
	}
	if hand {
		// AC-41: the first run after a hand install says so explicitly.
		d.Notes = append(d.Notes, Note{NoteInfo, "files from a manual install that differ from this version are kept; choose overwrite to adopt ours"})
	}
	return d
}

func skillToken(f skillFile) string {
	if !f.exists {
		return f.id + "=absent"
	}
	return f.id + "=" + sha256Hex(f.content)
}

// Plan implements Step.
func (SkillsStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	an, err := analyzeSkills(rc.FS, rc.Assets, rc.Paths, st)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	var tokens []string
	dirDone := map[string]bool{}
	for _, f := range an.files {
		if ch[f.id] != ChoiceApply || f.state == StateOK {
			continue
		}
		if f.refused != nil {
			return Plan{}, f.refused
		}
		if an.dirMissing[f.name] && !dirDone[f.name] {
			dirDone[f.name] = true
			p.Actions = append(p.Actions, Action{Artifact: skillDirArtifact(f.name), Verb: "write",
				Path: filepath.Join(rc.Paths.SkillsDir(), f.name), Desc: "create directory (mode 0755)"})
		}
		tokens = append(tokens, skillToken(f))
		desc := "create"
		switch f.state {
		case StateOutdated:
			desc = "replace (" + f.detail + ")"
		case StateModified:
			desc = "replace your modified file (a timestamped backup is kept beside it)"
		}
		p.Actions = append(p.Actions, Action{Artifact: f.id, Verb: "write", Path: f.path, Desc: desc})
		old := f.path
		if !f.exists {
			old = ""
		}
		p.Diffs = append(p.Diffs, Diff{Artifact: f.id, Path: f.path, Unified: UnifiedDiff(old, f.path, f.content, f.embedded)})
	}
	p.Token = strings.Join(tokens, ";")
	return p, nil
}

func (s SkillsStep) fileArtifact(f skillFile) Artifact {
	return Artifact{Step: SkillsStepID, Kind: KindFile, Path: f.path, SHA256: sha256Hex(f.embedded), Version: s.Version}
}

// Apply implements Step: it re-reads every skill file and aborts when a chosen
// one is not what Plan saw (Token), so an overwrite the user confirmed for one
// content never hits another (AC-6). It writes each chosen file atomically
// (keeping an existing mode, 0644 for a new file), backs up a modified one,
// and records every file that now equals the embedded one plus each skill
// directory it created.
func (s SkillsStep) Apply(_ context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	chosen := map[string]bool{}
	for _, a := range p.Actions {
		chosen[a.Artifact] = true
	}
	an, err := analyzeSkills(wc.FS, wc.Assets, wc.Paths, st)
	if err != nil {
		return res, err
	}
	tokens := strings.Split(p.Token, ";")
	planned := map[string]bool{}
	for _, t := range tokens {
		if id, _, ok := strings.Cut(t, "="); ok {
			planned[id] = true
		}
	}
	for _, f := range an.files {
		if !chosen[f.id] || f.state == StateOK {
			continue
		}
		if f.refused != nil {
			return res, f.refused
		}
		if planned[f.id] && !slices.Contains(tokens, skillToken(f)) {
			return res, fmt.Errorf("%s: %w", f.path, ErrSkillChanged)
		}
	}
	created := map[string]bool{}
	for _, f := range an.files {
		if chosen[f.id] && f.state != StateOK {
			if an.dirMissing[f.name] && !created[f.name] {
				if an.claudeAbsent {
					if err := wc.FS.MkdirAll(wc.Paths.ClaudeDir, 0o700); err != nil {
						return res, fmt.Errorf("create %s: %w", wc.Paths.ClaudeDir, err)
					}
				}
				dir := filepath.Join(wc.Paths.SkillsDir(), f.name)
				if err := wc.FS.MkdirAll(dir, 0o755); err != nil {
					return res, fmt.Errorf("create %s: %w", dir, err)
				}
				created[f.name] = true
				res.Artifacts = append(res.Artifacts, Artifact{Step: SkillsStepID, Kind: KindDir, Path: dir, Version: s.Version})
			}
			// A file in a subdirectory of the skill: create (and own) the
			// directories between the skill directory and the file.
			if !f.exists && f.target == f.path {
				skillDir := filepath.Join(wc.Paths.SkillsDir(), f.name)
				var missing []string
				for d := filepath.Dir(f.path); d != skillDir && pathUnder(d, skillDir); d = filepath.Dir(d) {
					if _, err := wc.FS.Stat(d); errors.Is(err, fs.ErrNotExist) {
						missing = append(missing, d)
					}
				}
				for i := len(missing) - 1; i >= 0; i-- {
					if err := wc.FS.MkdirAll(missing[i], 0o755); err != nil {
						return res, fmt.Errorf("create %s: %w", missing[i], err)
					}
					res.Artifacts = append(res.Artifacts, Artifact{Step: SkillsStepID, Kind: KindDir, Path: missing[i], Version: s.Version})
				}
			}
			mode := fs.FileMode(0o644)
			if f.exists {
				mode = f.mode
				if f.state == StateModified {
					bak := UniqueBackupPath(wc.FS, SettingsBackupPath(f.target, wc.Clock))
					if err := wc.FS.WriteFileAtomic(bak, f.content, BackupFileMode); err != nil {
						return res, fmt.Errorf("back up %s: %w", f.path, err)
					}
					res.Notes = append(res.Notes, Note{NoteInfo, "backup of your modified " + f.name + "/" + f.rel + ": " + bak})
				}
			}
			if err := wc.FS.WriteFileAtomic(f.target, f.embedded, mode); err != nil {
				return res, fmt.Errorf("write %s: %w", f.path, err)
			}
			if f.exists {
				res.Diffs = append(res.Diffs, Diff{Artifact: f.id, Path: f.path, Unified: UnifiedDiff(f.path, f.path, f.content, f.embedded)})
			}
		} else if f.state != StateOK {
			continue // kept (modified, or not chosen): not ours to record
		}
		res.Artifacts = append(res.Artifacts, s.fileArtifact(f))
	}
	return res, nil
}

// Adopt implements Adopter: a skill file already equal to the embedded one (a
// hand install of this version) is recorded (AC-51). A directory is not: this
// step did not create it.
func (s SkillsStep) Adopt(_ context.Context, rc ReadPorts, st *RunState) []Artifact {
	an, err := analyzeSkills(rc.FS, rc.Assets, rc.Paths, st)
	if err != nil {
		return nil
	}
	var out []Artifact
	for _, f := range an.files {
		if f.state == StateOK {
			out = append(out, s.fileArtifact(f))
		}
	}
	return out
}

// ModifiedDiff implements ModifiedDiffer: installed file -> this version.
func (SkillsStep) ModifiedDiff(_ context.Context, rc ReadPorts, st *RunState, id string) (string, error) {
	an, err := analyzeSkills(rc.FS, rc.Assets, rc.Paths, st)
	if err != nil {
		return "", err
	}
	for _, f := range an.files {
		if f.id == id {
			if f.refused != nil {
				return "", f.refused
			}
			return UnifiedDiff(f.path+" (installed)", f.path+" (this version)", f.content, f.embedded), nil
		}
	}
	return "", fmt.Errorf("unknown artifact %s", id)
}
