package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"claude-memory/internal/namespace"
)

// NamespacesStepID is the id of the namespaces step (AC-7: after ollama).
const NamespacesStepID = "namespaces"

// NamespacesArtifact is the step's one artifact id.
const NamespacesArtifact = "namespaces/file"

const (
	maxSuggestedDirs = 5  // AC-47: at most 5 suggested parent directories
	maxExtraMappings = 20 // bound on free-form mappings typed in one Configure
)

// NamespacesStep owns <ConfigDir>/namespaces.yaml and RunState.NSRules (AC-47,
// Design 16, 20, 21). It never calls the os-backed namespace.Init/Add/Save:
// it reads through the FS port, edits the parsed *namespace.Config,
// renders with namespace.Marshal and writes with FS.WriteFileAtomic, so
// dry-run (read-only FS) cannot write.
//
//   - absent: create the file (default namespace "global") with the
//     --namespace / prompted mappings;
//   - valid: ok, or outdated when a requested mapping is missing (it is added
//     with Config.Add; re-rendering drops hand-written comments, as
//     `namespaces add` does);
//   - unparseable: modified, reported with the parse error and never written.
//
// Nothing is recorded in the manifest except a ConfigDir this step had to
// create (a retained dir artifact): the file is user-owned and kept by
// uninstall (AC-54, Design 23), so a "file" artifact would wrongly be
// reversed.
type NamespacesStep struct {
	Version string
	// fsRoot anchors project-name decoding; "" means "/" (tests inject a
	// sandbox root).
	fsRoot string
}

var (
	_ Step       = NamespacesStep{}
	_ Seeder     = NamespacesStep{}
	_ Configurer = NamespacesStep{}
)

// ID implements Step.
func (NamespacesStep) ID() string { return NamespacesStepID }

// Title implements Step.
func (NamespacesStep) Title() string { return "Namespaces" }

// Requires implements Step.
func (NamespacesStep) Requires() []string { return nil }

// ParseNSFlag parses one --namespace NAME=GLOB value.
func ParseNSFlag(v string) (NSRule, error) {
	name, glob, ok := strings.Cut(strings.TrimSpace(v), "=")
	name, glob = strings.TrimSpace(name), strings.TrimSpace(glob)
	switch {
	case !ok || name == "" || glob == "":
		return NSRule{}, errors.New("want NAME=GLOB")
	case !namespace.ValidName(name):
		return NSRule{}, fmt.Errorf("invalid namespace name %q (lowercase letters, digits, '-' and '_')", name)
	}
	return NSRule{Namespace: name, Globs: []string{glob}}, nil
}

// mergeNSRules groups rules by namespace, keeping first-seen order and
// dropping duplicate globs.
func mergeNSRules(in []NSRule) []NSRule {
	var out []NSRule
	idx := map[string]int{}
	for _, r := range in {
		i, ok := idx[r.Namespace]
		if !ok {
			idx[r.Namespace] = len(out)
			out = append(out, NSRule{Namespace: r.Namespace})
			i = len(out) - 1
		}
		for _, g := range r.Globs {
			if !slices.Contains(out[i].Globs, g) {
				out[i].Globs = append(out[i].Globs, g)
			}
		}
	}
	return out
}

// Seed implements Seeder: --namespace flags become NSRules. An invalid flag
// is an error (exit 2).
func (NamespacesStep) Seed(_ context.Context, _ ReadPorts, st *RunState) ([]Note, error) {
	var rules []NSRule
	for _, v := range st.Inputs.Namespaces {
		r, err := ParseNSFlag(v)
		if err != nil {
			return nil, fmt.Errorf("--namespace %q: %w", v, err)
		}
		rules = append(rules, r)
	}
	if len(rules) > 0 {
		st.NSRules.Set(mergeNSRules(rules), SourceFlag)
	}
	return nil, nil
}

// nsAnalysis is the shared view of the file used by Detect, Plan and Apply.
type nsAnalysis struct {
	path       string
	exists     bool
	dirMissing bool
	before     []byte
	parseErr   error
	cfg        *namespace.Config // nil when unparseable
	after      []byte            // rendered result; nil when nothing to write
	added      []string          // "NAME=GLOB" mappings that will be added
}

func analyzeNamespaces(rfs ReadFS, p Paths, st *RunState) (nsAnalysis, error) {
	a := nsAnalysis{path: p.NamespacesFile()}
	data, err := rfs.ReadFile(a.path)
	switch {
	case err == nil:
		a.exists, a.before = true, data
	case errors.Is(err, fs.ErrNotExist):
		if _, serr := rfs.Stat(p.ConfigDir); errors.Is(serr, fs.ErrNotExist) {
			a.dirMissing = true
		}
	default:
		return a, fmt.Errorf("read %s: %w", a.path, err)
	}
	if a.exists {
		cfg, perr := namespace.Parse(a.before, a.path, p.Home)
		if perr != nil {
			a.parseErr = perr
			return a, nil
		}
		a.cfg = cfg
	} else {
		a.cfg = &namespace.Config{Default: namespace.Fallback, Namespaces: []namespace.Rule{}}
	}
	changed := !a.exists
	for _, r := range st.NSRules.Get() {
		for _, g := range r.Globs {
			if hasNSGlob(a.cfg, r.Namespace, g) {
				continue
			}
			if err := a.cfg.Add(r.Namespace, g); err != nil {
				return a, fmt.Errorf("namespace %q: %w", r.Namespace, err)
			}
			a.added = append(a.added, r.Namespace+"="+g)
			changed = true
		}
	}
	if changed {
		if a.after, err = namespace.Marshal(a.cfg); err != nil {
			return a, err
		}
	}
	return a, nil
}

func hasNSGlob(c *namespace.Config, name, glob string) bool {
	for _, r := range c.Namespaces {
		if r.Namespace == name && slices.Contains(r.Paths, glob) {
			return true
		}
	}
	return false
}

// Detect implements Step.
func (NamespacesStep) Detect(_ context.Context, rc ReadPorts, st *RunState) Detection {
	a, err := analyzeNamespaces(rc.FS, rc.Paths, st)
	if err != nil {
		return Detection{State: StateBlocked, Detail: err.Error()}
	}
	art := ArtifactState{ID: NamespacesArtifact}
	switch {
	case a.parseErr != nil:
		art.State = StateModified
		art.Detail = a.parseErr.Error() + "; left untouched, fix or remove it"
	case !a.exists:
		art.State, art.Detail = StateAbsent, a.path+" will be created"
	case len(a.added) > 0:
		art.State, art.Detail = StateOutdated, a.path+": add "+strings.Join(a.added, ", ")
	default:
		art.State, art.Detail = StateOK, a.path+" is valid"
	}
	return Detection{State: art.State, Detail: art.Detail, Artifacts: []ArtifactState{art}}
}

// Plan implements Step. A modified (unparseable) file never yields an action,
// whatever the choice.
func (NamespacesStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	a, err := analyzeNamespaces(rc.FS, rc.Paths, st)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	if a.parseErr != nil {
		p.Notes = append(p.Notes, Note{NoteWarn, a.parseErr.Error() + "; " + a.path + " is left untouched"})
		return p, nil
	}
	if a.after == nil || ch[NamespacesArtifact] != ChoiceApply {
		return p, nil
	}
	if a.dirMissing {
		p.Actions = append(p.Actions, Action{Artifact: NamespacesArtifact, Verb: "write", Path: rc.Paths.ConfigDir, Desc: "create directory (mode 0700)"})
	}
	desc := "create with default namespace " + namespace.Fallback
	if a.exists {
		desc = "add " + strings.Join(a.added, ", ")
	} else if len(a.added) > 0 {
		desc += ", " + strings.Join(a.added, ", ")
	}
	p.Actions = append(p.Actions, Action{Artifact: NamespacesArtifact, Verb: "write", Path: a.path, Desc: desc})
	old := a.path
	if !a.exists {
		old = ""
	}
	p.Diffs = append(p.Diffs, Diff{Artifact: NamespacesArtifact, Path: a.path, Unified: UnifiedDiff(old, a.path, a.before, a.after)})
	return p, nil
}

// Apply implements Step: one atomic write (0600) through the FS port. It
// re-reads the file, so a concurrent edit is merged, not overwritten, and an
// up-to-date or unparseable file writes nothing.
func (s NamespacesStep) Apply(_ context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	a, err := analyzeNamespaces(wc.ReadPorts.FS, wc.Paths, st)
	if err != nil {
		return res, err
	}
	if a.parseErr != nil {
		res.Notes = append(res.Notes, Note{NoteWarn, a.parseErr.Error() + "; " + a.path + " is left untouched"})
		return res, nil
	}
	if a.after == nil {
		return res, nil
	}
	dir := wc.Paths.ConfigDir
	if err := wc.FS.MkdirAll(dir, 0o700); err != nil {
		return res, fmt.Errorf("create %s: %w", dir, err)
	}
	if a.dirMissing {
		res.Artifacts = append(res.Artifacts, Artifact{Step: NamespacesStepID, Kind: KindDir, Path: dir, Version: s.Version})
	}
	if err := wc.FS.WriteFileAtomic(a.path, a.after, 0o600); err != nil {
		return res, fmt.Errorf("write %s: %w", a.path, err)
	}
	old := a.path
	if !a.exists {
		old = ""
	}
	res.Diffs = append(res.Diffs, Diff{Artifact: NamespacesArtifact, Path: a.path, Unified: UnifiedDiff(old, a.path, a.before, a.after)})
	for _, d := range s.suggestions(wc.ReadPorts.FS, wc.Paths) {
		res.Notes = append(res.Notes, Note{NoteInfo, "check: claude-memory namespaces which " + d})
	}
	return res, nil
}

func (s NamespacesStep) root() string {
	if s.fsRoot != "" {
		return s.fsRoot
	}
	return string(filepath.Separator)
}

// suggestions returns up to 5 distinct existing parent directories of the
// projects Claude Code has seen (AC-47).
func (s NamespacesStep) suggestions(rfs ReadFS, p Paths) []string {
	dirs := ProjectDirs(rfs, s.root(), filepath.Join(p.ClaudeDir, "projects"))
	return SuggestParentDirs(dirs, p.Home, maxSuggestedDirs)
}

// tildeGlob renders dir/** with the home prefix collapsed to "~".
func tildeGlob(dir, home string) string {
	home = filepath.Clean(home)
	if dir == home {
		return "~/**"
	}
	if strings.HasPrefix(dir, home+string(filepath.Separator)) {
		dir = "~" + dir[len(home):]
	}
	return dir + "/**"
}

func suggestedName(dir string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(dir)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	n := strings.Trim(b.String(), "-_")
	if !namespace.ValidName(n) {
		return ""
	}
	return n
}

// Configure implements Configurer: it offers the suggested parent
// directories, then free-form NAME=GLOB mappings. It is silent when
// non-interactive or when --namespace flags already gave the mappings.
func (s NamespacesStep) Configure(_ context.Context, rc ReadPorts, ui Prompter, st *RunState) error {
	if !ui.Interactive() || st.NSRules.Source() == SourceFlag {
		return nil
	}
	var rules []NSRule
	for _, d := range s.suggestions(rc.FS, rc.Paths) {
		glob := tildeGlob(d, rc.Paths.Home)
		yes, err := ui.Confirm("Map "+glob+" to its own namespace?", false)
		if err != nil {
			return err
		}
		if !yes {
			continue
		}
		name, err := ui.Text("Namespace name for "+glob, suggestedName(d), func(v string) error {
			if !namespace.ValidName(v) {
				return errors.New("lowercase letters, digits, '-' and '_'")
			}
			return nil
		})
		if err != nil {
			return err
		}
		rules = append(rules, NSRule{Namespace: name, Globs: []string{glob}})
	}
	for i := 0; i < maxExtraMappings; i++ {
		v, err := ui.Text("Add another mapping NAME=GLOB (empty to finish)", "", func(v string) error {
			if strings.TrimSpace(v) == "" {
				return nil
			}
			_, err := ParseNSFlag(v)
			return err
		})
		if err != nil {
			return err
		}
		if strings.TrimSpace(v) == "" {
			break
		}
		r, _ := ParseNSFlag(v)
		rules = append(rules, r)
	}
	if len(rules) > 0 {
		st.NSRules.Set(mergeNSRules(rules), SourcePrompt)
	}
	return nil
}
