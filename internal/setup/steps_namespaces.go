package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

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
	lossy      string            // what re-rendering an existing file drops; "" when nothing
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
		if a.exists {
			a.lossy = rerenderLoss(a.before, a.after)
		}
	}
	return a, nil
}

// rerenderLoss describes what rewriting an existing, valid file with
// namespace.Marshal would lose: comments other than the generated header, and
// keys the Config struct does not know. "" means nothing is lost.
func rerenderLoss(before, after []byte) string {
	var doc yaml.Node
	if yaml.Unmarshal(before, &doc) != nil {
		return ""
	}
	var comments bool
	unknown := false
	var walk func(n *yaml.Node, depth int)
	walk = func(n *yaml.Node, depth int) {
		for _, c := range []string{n.HeadComment, n.LineComment, n.FootComment} {
			for _, line := range strings.Split(c, "\n") {
				if line = strings.TrimSpace(line); line != "" && !strings.Contains(string(after), line) {
					comments = true
				}
			}
		}
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, depth)
			}
		case yaml.MappingNode:
			var known []string
			switch depth {
			case 0:
				known = namespace.KnownKeys.Top
			case 2:
				known = namespace.KnownKeys.Rule
			}
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if (depth == 0 || depth == 2) && !slices.Contains(known, k.Value) {
					unknown = true
				}
				// A rule's pr_ingest section has its own known keys.
				if depth == 2 && k.Value == "pr_ingest" && v.Kind == yaml.MappingNode {
					for j := 0; j+1 < len(v.Content); j += 2 {
						if !slices.Contains(namespace.KnownKeys.PRIngest, v.Content[j].Value) {
							unknown = true
						}
					}
				}
				walk(k, depth+1)
				walk(v, depth+1)
			}
		case yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, depth+1)
			}
		}
	}
	walk(&doc, 0)
	switch {
	case comments && unknown:
		return "comments and unknown keys"
	case comments:
		return "comments"
	case unknown:
		return "unknown keys"
	}
	return ""
}

func lossyNote(a nsAnalysis) Note {
	return Note{NoteWarn, a.path + ": rewriting it drops " + a.lossy + " (a backup of the current file is kept beside it)"}
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
	if a.lossy != "" {
		p.Notes = append(p.Notes, lossyNote(a))
	}
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
	if a.lossy != "" {
		bak := a.path + envBackupSuffix + wc.Clock.Now().UTC().Format("20060102T150405Z")
		if err := wc.FS.WriteFileAtomic(bak, a.before, 0o600); err != nil {
			return res, fmt.Errorf("back up %s: %w", a.path, err)
		}
		res.Notes = append(res.Notes, lossyNote(a), Note{NoteInfo, "backup of the previous namespaces file: " + bak})
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
		ns, why := a.cfg.Explain(d)
		res.Notes = append(res.Notes, Note{NoteInfo, fmt.Sprintf("%s now resolves to namespace %q (%s); check: claude-memory namespaces which %s", d, ns, why, d)})
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
	// An unparseable file is never written, so nothing is offered for it; a
	// directory an existing rule already covers is not suggested again.
	a, err := analyzeNamespaces(rc.FS, rc.Paths, NewRunState(Inputs{}))
	if err != nil || a.parseErr != nil {
		return nil
	}
	var rules []NSRule
	for _, d := range s.suggestions(rc.FS, rc.Paths) {
		ns, why := a.cfg.Explain(d)
		if strings.HasPrefix(why, namespace.WhyRule) {
			continue
		}
		glob := tildeGlob(d, rc.Paths.Home)
		yes, err := ui.Confirm(fmt.Sprintf("Map %s to its own namespace? (currently %q, %s)", glob, ns, why), false)
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
