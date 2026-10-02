package setup

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// Step ids of the Claude Code hook integration (AC-7: hooks.scripts, then
// hooks.settings).
const (
	HooksScriptsStepID  = "hooks.scripts"
	HooksSettingsStepID = "hooks.settings"
)

// HookScriptsDirArtifact is the plan-action id of the scripts directory.
const HookScriptsDirArtifact = HooksScriptsStepID

func hookScriptArtifact(name string) string   { return HooksScriptsStepID + "/" + name }
func hookSettingsArtifact(ev string) string   { return HooksSettingsStepID + "/" + ev }
func hookScriptBackupSuffix(ts string) string { return ".bak.claude-memory." + ts }

// hookScriptSpecs lists the scripts install owns, with their embedded assets.
var hookScriptSpecs = []struct{ name, asset string }{
	{HookScriptUserPromptSubmit, assetHookUserPromptSubmit},
	{HookScriptSessionEnd, assetHookSessionEnd},
}

// ---- hooks.scripts -----------------------------------------------------------

// HooksScriptsStep writes the two hook wrapper scripts to
// <ClaudeDir>/hooks/claude-memory/ at mode 0755, rendered with the installed
// binary path (RunState.BinPath) as the default of CLAUDE_MEMORY_BIN, and
// records their sha256 (AC-36). One artifact per script (Design 18).
//
// States (AC-36, AC-51): the file equals the rendered script -> ok (a wrong
// mode makes it outdated); the raw embedded script (a manual install of this
// version) or the content the manifest recorded -> outdated, replaced without
// an overwrite Confirm because the content is ours; anything else -> modified,
// kept unless confirmed, then replaced after a timestamped backup.
//
// The directory <ClaudeDir>/hooks/claude-memory is recorded as an owned dir
// artifact only when this step created it (Design 23).
type HooksScriptsStep struct {
	// Version is the running binary's version, recorded on its artifacts.
	Version string
}

var _ Step = HooksScriptsStep{}

// ID implements Step.
func (HooksScriptsStep) ID() string { return HooksScriptsStepID }

// Title implements Step.
func (HooksScriptsStep) Title() string { return "Hook scripts" }

// Requires implements Step.
func (HooksScriptsStep) Requires() []string { return nil }

type hookScript struct {
	name, path string
	rendered   []byte
	state      State
	detail     string
	exists     bool
	content    []byte
	mode       fs.FileMode
}

type scriptsAnalysis struct {
	dir        string
	dirMissing bool
	scripts    []hookScript
}

func (a scriptsAnalysis) find(id string) (hookScript, bool) {
	for _, s := range a.scripts {
		if hookScriptArtifact(s.name) == id {
			return s, true
		}
	}
	return hookScript{}, false
}

func recordedScriptHash(m *Manifest, path string) string {
	for _, a := range m.Find(KindFile) {
		if a.Step == HooksScriptsStepID && a.Path == path {
			return a.SHA256
		}
	}
	return ""
}

func analyzeScripts(rfs ReadFS, assets fs.FS, p Paths, st *RunState, bin string) (scriptsAnalysis, error) {
	a := scriptsAnalysis{dir: p.HookScriptsDir()}
	if _, err := rfs.Stat(a.dir); errors.Is(err, fs.ErrNotExist) {
		a.dirMissing = true
	} else if err != nil {
		return a, fmt.Errorf("stat %s: %w", a.dir, err)
	}
	for _, sp := range hookScriptSpecs {
		raw, err := fs.ReadFile(assets, sp.asset)
		if err != nil {
			return a, fmt.Errorf("embedded %s: %w", sp.asset, err)
		}
		rendered, err := RenderHookScript(raw, bin)
		if err != nil {
			return a, fmt.Errorf("%s: %w", sp.name, err)
		}
		s := hookScript{name: sp.name, path: filepath.Join(p.HookScriptsDir(), sp.name), rendered: rendered}
		info, err := rfs.Stat(s.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			s.state, s.detail = StateAbsent, "will be created"
		case err != nil:
			return a, fmt.Errorf("stat %s: %w", s.path, err)
		case !info.Mode().IsRegular():
			s.exists, s.state, s.detail = true, StateModified, s.path+" is not a regular file"
		default:
			b, err := rfs.ReadFile(s.path)
			if err != nil {
				return a, fmt.Errorf("read %s: %w", s.path, err)
			}
			s.exists, s.content, s.mode = true, b, info.Mode().Perm()
			h, rec := sha256Hex(b), recordedScriptHash(st.Prior.Manifest, s.path)
			switch {
			case h == sha256Hex(rendered) && s.mode&0o111 == 0:
				s.state, s.detail = StateOutdated, "matches this version but is not executable"
			case h == sha256Hex(rendered):
				s.state, s.detail = StateOK, "matches this version"
			case h == sha256Hex(raw):
				s.state, s.detail = StateOutdated, "a manual install of this version; rendered for "+bin
			case rec != "" && h == rec:
				s.state, s.detail = StateOutdated, "an older version written by install"
			case rec != "":
				s.state, s.detail = StateModified, "edited since install"
			default:
				s.state, s.detail = StateModified, "differs from this version (edited, or a manual install of another version)"
			}
		}
		a.scripts = append(a.scripts, s)
	}
	return a, nil
}

// Detect implements Step.
func (HooksScriptsStep) Detect(_ context.Context, rc ReadPorts, st *RunState) Detection {
	an, err := analyzeScripts(rc.FS, rc.Assets, rc.Paths, st, binTarget(rc, st))
	if err != nil {
		return Detection{State: StateBlocked, Detail: err.Error()}
	}
	var arts []ArtifactState
	var states []State
	var pending []string
	for _, s := range an.scripts {
		arts = append(arts, ArtifactState{ID: hookScriptArtifact(s.name), State: s.state, Detail: s.detail})
		states = append(states, s.state)
		if s.state != StateOK {
			pending = append(pending, s.name+": "+s.detail)
		}
	}
	d := Detection{State: worstState(states...), Artifacts: arts}
	if len(pending) > 0 {
		d.Detail = strings.Join(pending, "; ")
	} else {
		d.Detail = "both scripts in " + an.dir + " match this version"
	}
	return d
}

// Plan implements Step.
func (HooksScriptsStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	an, err := analyzeScripts(rc.FS, rc.Assets, rc.Paths, st, binTarget(rc, st))
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	for _, s := range an.scripts {
		id := hookScriptArtifact(s.name)
		if ch[id] != ChoiceApply || s.state == StateOK {
			continue
		}
		if an.dirMissing && len(p.Actions) == 0 {
			p.Actions = append(p.Actions, Action{Artifact: HookScriptsDirArtifact, Verb: "write", Path: an.dir, Desc: "create directory (mode 0755)"})
		}
		desc := "create (mode 0755)"
		switch s.state {
		case StateOutdated:
			desc = "replace (" + s.detail + ")"
		case StateModified:
			desc = "replace your modified script (a timestamped backup is kept)"
		}
		p.Actions = append(p.Actions, Action{Artifact: id, Verb: "write", Path: s.path, Desc: desc})
		if s.exists { // a new script is the embedded asset: no diff worth showing
			p.Diffs = append(p.Diffs, Diff{Artifact: id, Path: s.path, Unified: UnifiedDiff(s.path, s.path, s.content, s.rendered)})
		}
	}
	return p, nil
}

// Apply implements Step: it re-reads every script, so a concurrent edit is
// not overwritten unseen, writes each chosen one atomically at 0755 and
// records the hash of every script that now equals the rendered one.
func (h HooksScriptsStep) Apply(_ context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	chosen := map[string]bool{}
	for _, a := range p.Actions {
		chosen[a.Artifact] = true
	}
	an, err := analyzeScripts(wc.FS, wc.Assets, wc.Paths, st, binTarget(wc.ReadPorts, st))
	if err != nil {
		return res, err
	}
	if an.dirMissing {
		if _, err := wc.FS.Stat(wc.Paths.ClaudeDir); errors.Is(err, fs.ErrNotExist) {
			if err := wc.FS.MkdirAll(wc.Paths.ClaudeDir, 0o700); err != nil {
				return res, fmt.Errorf("create %s: %w", wc.Paths.ClaudeDir, err)
			}
		}
		if err := wc.FS.MkdirAll(an.dir, 0o755); err != nil {
			return res, fmt.Errorf("create %s: %w", an.dir, err)
		}
		res.Artifacts = append(res.Artifacts, Artifact{Step: HooksScriptsStepID, Kind: KindDir, Path: an.dir, Version: h.Version})
	}
	for _, s := range an.scripts {
		if chosen[hookScriptArtifact(s.name)] && s.state != StateOK {
			if s.state == StateModified && s.exists && s.content != nil {
				bak := s.path + hookScriptBackupSuffix(wc.Clock.Now().UTC().Format("20060102T150405Z"))
				if err := wc.FS.WriteFileAtomic(bak, s.content, s.mode); err != nil {
					return res, fmt.Errorf("back up %s: %w", s.path, err)
				}
				res.Notes = append(res.Notes, Note{NoteInfo, "backup of your modified " + s.name + ": " + bak})
			}
			if err := wc.FS.WriteFileAtomic(s.path, s.rendered, 0o755); err != nil {
				return res, fmt.Errorf("write %s: %w", s.path, err)
			}
			if s.exists {
				res.Diffs = append(res.Diffs, Diff{Artifact: hookScriptArtifact(s.name), Path: s.path, Unified: UnifiedDiff(s.path, s.path, s.content, s.rendered)})
			}
		} else if s.state != StateOK {
			continue // kept (modified or not chosen): not ours to record
		}
		res.Artifacts = append(res.Artifacts, Artifact{Step: HooksScriptsStepID, Kind: KindFile, Path: s.path,
			SHA256: sha256Hex(s.rendered), Version: h.Version})
	}
	return res, nil
}

// ---- hooks.settings ----------------------------------------------------------

// HooksSettingsStep ensures exactly one of our entries per event in
// <ClaudeDir>/settings.json (AC-37..AC-39, AC-70), one artifact per event
// (Design 18). The merge keeps every other byte of the file; a modified
// entry (legacy $HOME form, user-raised timeout, duplicates) is kept unless
// the user confirmed its overwrite. It requires migrate (AC-7), so a failed
// migration never leaves a wired hook against an empty schema.
//
// The canonical entry written per event is recorded, plus CreatedFile when
// this run created settings.json and CreatedContainer when it created the
// "hooks" object (Design 23). settings.local.json and the project's settings
// files are only read (duplicates become Notes, AC-70).
type HooksSettingsStep struct {
	// Version is the running binary's version, recorded on its artifacts.
	Version string
}

var _ Step = HooksSettingsStep{}

// ID implements Step.
func (HooksSettingsStep) ID() string { return HooksSettingsStepID }

// Title implements Step.
func (HooksSettingsStep) Title() string { return "Hook entries in settings.json" }

// Requires implements Step.
func (HooksSettingsStep) Requires() []string { return []string{MigrateStepID} }

type settingsView struct {
	path     string
	file     *SettingsFile
	analysis SettingsAnalysis
	recorded RecordedHooks
	desired  []HookEntry
}

func loadSettingsView(rfs ReadFS, p Paths, st *RunState) (settingsView, error) {
	v := settingsView{path: p.SettingsJSON(), desired: DesiredHooks(p.HookScriptsDir())}
	f, err := ReadSettingsFile(rfs, p.Home, v.path)
	if err != nil {
		return v, err
	}
	v.file = f
	v.recorded = st.Prior.Manifest.RecordedHooks(v.path)
	if v.analysis, err = AnalyzeSettings(f.Content, v.desired, v.recorded); err != nil {
		var sr *SettingsRefusal
		if errors.As(err, &sr) {
			sr.Path = v.path
		}
		return v, err
	}
	return v, nil
}

func settingsToken(f *SettingsFile) string {
	if !f.Exists {
		return "absent"
	}
	return hex.EncodeToString(f.Hash[:])
}

// settingsNotes are the read-only findings around the edit (AC-70): our
// entries in the other settings files, files that cannot be read, and our
// entries under events install does not manage.
func settingsNotes(rfs ReadFS, p Paths, v settingsView) []Note {
	var ns []Note
	rep := ScanOtherSettings(rfs, p)
	for _, d := range rep.Duplicates {
		ns = append(ns, Note{NoteWarn, fmt.Sprintf("duplicate claude-memory hook in %s (%s): it would fire twice; install never edits this file, remove the entry by hand", d.Path, d.Event)})
	}
	for _, path := range slices.Sorted(maps.Keys(rep.Unreadable)) {
		ns = append(ns, Note{NoteWarn, path + " is not readable strict JSON, so duplicates there were not checked"})
	}
	for _, s := range v.analysis.Stray {
		ns = append(ns, Note{NoteWarn, fmt.Sprintf("claude-memory entry under %s in %s is not an event install manages; left untouched", s.Event, v.path)})
	}
	return ns
}

// Detect implements Step.
func (HooksSettingsStep) Detect(_ context.Context, rc ReadPorts, st *RunState) Detection {
	v, err := loadSettingsView(rc.FS, rc.Paths, st)
	if err != nil {
		// AC-38: a refusal is a failure of this step; the file is untouched.
		return Detection{State: StateBlocked, Detail: err.Error()}
	}
	d := Detection{State: v.analysis.State, Notes: settingsNotes(rc.FS, rc.Paths, v)}
	for _, e := range v.analysis.Events {
		d.Artifacts = append(d.Artifacts, ArtifactState{ID: hookSettingsArtifact(e.Event), State: e.State, Detail: e.Detail})
	}
	if v.file.Exists {
		d.Detail = v.path + ": " + v.analysis.Detail
	} else {
		d.Detail = v.path + " will be created"
	}
	return d
}

// mergeFor merges the events chosen apply into the file; a modified event is
// overwritten only because the choice says so (the engine asked the Confirm).
func (v settingsView) mergeFor(ch map[string]bool) ([]byte, MergeSummary, bool, error) {
	var want []HookEntry
	overwrite := map[string]bool{}
	for i, e := range v.analysis.Events {
		if !ch[hookSettingsArtifact(e.Event)] || e.State == StateOK {
			continue
		}
		want = append(want, v.desired[i])
		if e.State == StateModified {
			overwrite[e.Event] = true
		}
	}
	return MergeSettings(v.file.Content, want, v.recorded, overwrite)
}

// Plan implements Step.
func (HooksSettingsStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	v, err := loadSettingsView(rc.FS, rc.Paths, st)
	if err != nil {
		return Plan{}, err
	}
	chosen := map[string]bool{}
	for id, c := range ch {
		chosen[id] = c == ChoiceApply
	}
	out, _, changed, err := v.mergeFor(chosen)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	if !changed {
		return p, nil
	}
	for _, e := range v.analysis.Events {
		id := hookSettingsArtifact(e.Event)
		if !chosen[id] || e.State == StateOK {
			continue
		}
		desc := "add the " + e.Event + " entry"
		switch e.State {
		case StateOutdated:
			desc = "replace the " + e.Event + " entry written by an earlier install"
		case StateModified:
			desc = "replace your modified " + e.Event + " entry"
		}
		p.Actions = append(p.Actions, Action{Artifact: id, Verb: "write", Path: v.path, Desc: desc})
	}
	if v.file.Exists {
		p.Notes = append(p.Notes, Note{NoteInfo, "a timestamped backup of " + v.file.Target + " is kept beside it"})
	}
	old := v.path
	if !v.file.Exists {
		old = ""
	}
	p.Diffs = append(p.Diffs, Diff{Artifact: HooksSettingsStepID, Path: v.path, Unified: UnifiedDiff(old, v.path, v.file.Content, out)})
	p.Token = settingsToken(v.file)
	return p, nil
}

// Apply implements Step: re-read, abort when the file changed since Plan
// (AC-39), merge, then one backup + one atomic write that keeps the mode and
// follows an in-Home symlink. Nothing is written when the merge changes
// nothing.
func (h HooksSettingsStep) Apply(_ context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	v, err := loadSettingsView(wc.FS, wc.Paths, st)
	if err != nil {
		return res, err
	}
	if p.Token != "" && settingsToken(v.file) != p.Token {
		return res, fmt.Errorf("%s: %w", v.file.Target, ErrSettingsChanged)
	}
	chosen := map[string]bool{}
	for _, a := range p.Actions {
		chosen[a.Artifact] = true
	}
	out, sum, changed, err := v.mergeFor(chosen)
	if err != nil {
		return res, err
	}
	if changed {
		backup, err := WriteSettingsFile(wc.FS, wc.Clock, v.file, out)
		if err != nil {
			return res, err
		}
		if backup != "" {
			res.Notes = append(res.Notes, Note{NoteInfo, "backup of the previous settings file: " + backup})
		}
		old := v.path
		if !v.file.Exists {
			old = ""
		}
		res.Diffs = append(res.Diffs, Diff{Artifact: HooksSettingsStepID, Path: v.path, Unified: UnifiedDiff(old, v.path, v.file.Content, out)})
	}
	for _, d := range sum.Drift {
		res.Notes = append(res.Notes, Note{NoteWarn, "drift: " + d})
	}
	res.Notes = append(res.Notes, settingsNotes(wc.FS, wc.Paths, v)...)

	// Record every event whose entry now equals the desired one, keeping the
	// Created* flags an earlier run recorded.
	after, err := AnalyzeSettings(out, v.desired, v.recorded)
	if err != nil {
		return res, err
	}
	m := st.Prior.Manifest
	container := sum.CreatedHooksKey || m.CreatedHooksKey(v.path)
	createdFile := !v.file.Exists
	for _, a := range m.Find(KindSettingsHook) {
		if a.Path == v.path && a.CreatedFile {
			createdFile = true
		}
	}
	for i, e := range after.Events {
		if e.State != StateOK {
			continue
		}
		res.Artifacts = append(res.Artifacts, Artifact{
			Step: HooksSettingsStepID, Kind: KindSettingsHook, Path: v.path, Identity: e.Event,
			Entry: v.desired[i].Canonical(), Version: h.Version,
			CreatedContainer: container, CreatedFile: createdFile,
		})
	}
	return res, nil
}
