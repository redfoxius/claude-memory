package setup

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// settings.json hook merge library (spec AC-37, AC-38, AC-39, AC-70; plan
// WI-S1-7, Design 2 and 3). The pure functions (AnalyzeSettings,
// MergeSettings, UnmergeSettings) work on bytes; ReadSettingsFile and
// WriteSettingsFile add the file rules (symlinks, backup, hash re-check).
//
// Our entries are recognized by identity (IsOurHookCommand) and whether we
// may change one is decided by the manifest: only an entry equal to the
// canonical entry install recorded is ours to replace (outdated); any other
// entry of ours is modified (drift) and is kept unless the caller passes
// overwriteModified (which slice 2 does only after an explicit Confirm). No
// marker key is ever written inside a hook entry.

// Hook events install manages.
const (
	EventUserPromptSubmit = "UserPromptSubmit"
	EventSessionEnd       = "SessionEnd"
)

// Hook script names, inside Paths.HookScriptsDir().
const (
	HookScriptUserPromptSubmit = "user-prompt-submit.sh"
	HookScriptSessionEnd       = "session-end.sh"
)

// DefaultHookTimeout is the timeout (seconds) of the entries install writes.
const DefaultHookTimeout = 5

// HookEntry is one hook entry install wants under hooks.<Event>:
// {"type":"command","command":Command,"timeout":Timeout}, wrapped in a group
// {"hooks":[entry]} when it is added.
type HookEntry struct {
	Event   string
	Command string
	Timeout int
}

// DesiredHooks returns the entries install writes for hook scripts in
// scriptsDir (normally Paths.HookScriptsDir()), in a fixed event order.
func DesiredHooks(scriptsDir string) []HookEntry {
	return []HookEntry{
		{Event: EventUserPromptSubmit, Command: filepath.Join(scriptsDir, HookScriptUserPromptSubmit), Timeout: DefaultHookTimeout},
		{Event: EventSessionEnd, Command: filepath.Join(scriptsDir, HookScriptSessionEnd), Timeout: DefaultHookTimeout},
	}
}

// hookEntryJSON fixes the key order of a written entry.
type hookEntryJSON struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type hookGroupJSON struct {
	Hooks []hookEntryJSON `json:"hooks"`
}

func (h HookEntry) json() hookEntryJSON {
	return hookEntryJSON{Type: "command", Command: h.Command, Timeout: h.Timeout}
}

// Canonical returns the canonical JSON of the entry (sorted keys, compact):
// the form the manifest records for a settings-hook artifact.
func (h HookEntry) Canonical() string {
	s, _ := marshalNoEscape(h.json()) // keys type, command, timeout; then canonicalized
	c, _ := canonicalJSON([]byte(s))
	return c
}

// RecordedHooks maps a hook event to the canonical JSON of the entry the
// manifest records install wrote there (Manifest.RecordedHooks).
type RecordedHooks map[string]string

var ourHookSuffixes = []string{
	"/claude-memory/" + HookScriptUserPromptSubmit,
	"/claude-memory/" + HookScriptSessionEnd,
}

// IsOurHookCommand reports whether a hook command is ours by identity (spec
// §2 "Our hook entry"): it runs …/claude-memory/user-prompt-submit.sh or
// …/claude-memory/session-end.sh, or contains `claude-memory hook` /
// `claude-memory extract`. This recognizes hand installs ($HOME/... form).
func IsOurHookCommand(cmd string) bool {
	c := strings.TrimSpace(cmd)
	if strings.Contains(c, "claude-memory hook") || strings.Contains(c, "claude-memory extract") {
		return true
	}
	c = strings.Trim(c, `"'`)
	for _, s := range ourHookSuffixes {
		if strings.HasSuffix(c, s) {
			return true
		}
	}
	return false
}

// IsLegacyHookCommand reports whether cmd uses the `$HOME/...` form of
// integration/settings.snippet.json (or `~/...`), which install never writes.
func IsLegacyHookCommand(cmd string) bool {
	c := strings.Trim(strings.TrimSpace(cmd), `"'`)
	return strings.HasPrefix(c, "$HOME/") || strings.HasPrefix(c, "${HOME}/") || strings.HasPrefix(c, "~/")
}

// ExpandHome expands a leading $HOME, ${HOME} or ~ in a hook command path,
// the way the shell Claude Code runs the command through would (doctor
// checks that the path exists).
func ExpandHome(cmd, home string) string {
	c := strings.Trim(strings.TrimSpace(cmd), `"'`)
	for _, p := range []string{"$HOME/", "${HOME}/", "~/"} {
		if strings.HasPrefix(c, p) {
			return filepath.Join(home, c[len(p):])
		}
	}
	return c
}

// HookFinding is one of our entries found in a settings file.
type HookFinding struct {
	Event     string
	Group     int    // index of the group in hooks.<Event>
	Index     int    // index of the entry in the group's "hooks" array
	Command   string // the entry's command
	Canonical string // canonical JSON of the entry
	// GroupKeys is true when the group holds keys besides "hooks" (e.g. a
	// matcher); a replacement keeps them.
	GroupKeys bool
	Legacy    bool // IsLegacyHookCommand(Command)
}

// HookEventState is the state of one event install manages.
type HookEventState struct {
	Event  string
	State  State
	Detail string
	Ours   []HookFinding
	Others int // entries in this event that are not ours
}

// SettingsAnalysis is the read-only view of a settings file that doctor and
// the hooks.settings Detect share.
type SettingsAnalysis struct {
	HooksKey bool             // the file has a "hooks" object
	Events   []HookEventState // one per desired entry, in DesiredHooks order
	// Stray lists our entries under events install does not manage.
	Stray []HookFinding
	// State aggregates Events: modified if any is modified, ok if all are
	// ok, absent if all are absent, else outdated (something to apply).
	State  State
	Detail string
}

// SettingsRefusal is the AC-38 refusal: the file is not something we edit.
type SettingsRefusal struct {
	Path      string // "" when the caller did not name the file
	Reason    string
	Line, Col int // 1-based; 0 when not position-specific
}

func (e *SettingsRefusal) Error() string {
	p := e.Path
	if p == "" {
		p = "settings.json"
	}
	at := ""
	if e.Line > 0 {
		at = fmt.Sprintf(" at line %d:%d", e.Line, e.Col)
	}
	return fmt.Sprintf("refusing to edit %s: %s%s; fix it or merge integration/settings.snippet.json by hand", p, e.Reason, at)
}

func refusalFrom(err error) error {
	var je *jsonError
	if errors.As(err, &je) {
		return &SettingsRefusal{Reason: je.Reason, Line: je.Line, Col: je.Col}
	}
	return &SettingsRefusal{Reason: err.Error()}
}

func refuseAt(src []byte, n *jsonNode, format string, args ...any) error {
	je := newJSONError(src, n.pos(), format, args...)
	if n.isNew {
		je.Line, je.Col = 0, 0
	}
	return &SettingsRefusal{Reason: je.Reason, Line: je.Line, Col: je.Col}
}

// settingsModel is a parsed settings file with our hook slots located.
type settingsModel struct {
	doc   *jsonDoc
	hooks *jsonNode // the "hooks" object, nil when absent
	slots map[string][]hookSlot
	other map[string]int
}

type hookSlot struct {
	HookFinding
	groups *jsonNode // the event array
	arr    *jsonNode // the group's "hooks" array
}

func loadSettingsModel(b []byte) (*settingsModel, error) {
	doc, err := parseJSONDoc(b)
	if err != nil {
		return nil, refusalFrom(err)
	}
	m := &settingsModel{doc: doc, slots: map[string][]hookSlot{}, other: map[string]int{}}
	if doc.root.kind != jsonObject {
		return nil, refuseAt(doc.src, doc.root, "the top-level value is %s, not an object", doc.root.kind)
	}
	hk := doc.root.member("hooks")
	if hk == nil {
		return m, nil
	}
	if hk.val.kind != jsonObject {
		return nil, refuseAt(doc.src, hk.val, `"hooks" is %s, not an object`, hk.val.kind)
	}
	m.hooks = hk.val
	for _, ev := range m.hooks.kids {
		if err := m.scanEvent(ev); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *settingsModel) scanEvent(ev *jsonKid) error {
	src := m.doc.src
	if ev.val.kind != jsonArray {
		return refuseAt(src, ev.val, `"hooks.%s" is %s, not an array`, ev.key, ev.val.kind)
	}
	for gi, g := range ev.val.kids {
		if g.val.kind != jsonObject {
			return refuseAt(src, g.val, `"hooks.%s[%d]" is %s, not an object`, ev.key, gi, g.val.kind)
		}
		hs := g.val.member("hooks")
		if hs == nil {
			continue
		}
		if hs.val.kind != jsonArray {
			return refuseAt(src, hs.val, `"hooks.%s[%d].hooks" is %s, not an array`, ev.key, gi, hs.val.kind)
		}
		for ii, h := range hs.val.kids {
			cmd, _ := h.val.member("command").valOrNil().stringValue()
			if h.val.kind != jsonObject || !IsOurHookCommand(cmd) {
				m.other[ev.key]++
				continue
			}
			canon, err := canonicalJSON(h.val.raw)
			if err != nil {
				return refuseAt(src, h.val, "hook entry does not re-parse: %v", err)
			}
			m.slots[ev.key] = append(m.slots[ev.key], hookSlot{
				HookFinding: HookFinding{Event: ev.key, Group: gi, Index: ii, Command: cmd, Canonical: canon,
					GroupKeys: len(g.val.kids) > 1, Legacy: IsLegacyHookCommand(cmd)},
				groups: ev.val, arr: hs.val,
			})
		}
	}
	return nil
}

func (k *jsonKid) valOrNil() *jsonNode {
	if k == nil {
		return nil
	}
	return k.val
}

func findings(slots []hookSlot) []HookFinding {
	out := make([]HookFinding, len(slots))
	for i, s := range slots {
		out[i] = s.HookFinding
	}
	return out
}

// eventState applies the AC-37 rules to the entries of ours in one event.
func eventState(ours []hookSlot, want HookEntry, recorded string) (State, string) {
	switch {
	case len(ours) == 0:
		return StateAbsent, "no claude-memory entry"
	case len(ours) > 1:
		return StateModified, fmt.Sprintf("%d claude-memory entries (duplicates)", len(ours))
	}
	c := ours[0].Canonical
	switch {
	case c == want.Canonical():
		return StateOK, "installed"
	case recorded != "" && c == recorded:
		return StateOutdated, "entry written by an earlier install differs from this version"
	case ours[0].Legacy:
		return StateModified, "legacy $HOME/... entry from a manual install"
	case recorded == "":
		return StateModified, "entry not written by install (manual install or edited)"
	default:
		return StateModified, "entry edited since install"
	}
}

// AnalyzeSettings reports, read-only, the state of our entries in a
// settings file's content (nil or empty = missing). It returns a
// *SettingsRefusal when the content is not something MergeSettings would
// edit.
func AnalyzeSettings(b []byte, desired []HookEntry, recorded RecordedHooks) (SettingsAnalysis, error) {
	m, err := loadSettingsModel(b)
	if err != nil {
		return SettingsAnalysis{}, err
	}
	a := SettingsAnalysis{HooksKey: m.hooks != nil}
	managed := map[string]bool{}
	var states []State
	for _, d := range desired {
		managed[d.Event] = true
		st, detail := eventState(m.slots[d.Event], d, recorded[d.Event])
		a.Events = append(a.Events, HookEventState{Event: d.Event, State: st, Detail: detail,
			Ours: findings(m.slots[d.Event]), Others: m.other[d.Event]})
		states = append(states, st)
	}
	if m.hooks != nil {
		for _, ev := range m.hooks.kids {
			if !managed[ev.key] {
				a.Stray = append(a.Stray, findings(m.slots[ev.key])...)
			}
		}
	}
	a.State, a.Detail = aggregateHookStates(a.Events)
	return a, nil
}

func aggregateHookStates(evs []HookEventState) (State, string) {
	count := map[State]int{}
	var parts []string
	for _, e := range evs {
		count[e.State]++
		if e.State != StateOK {
			parts = append(parts, e.Event+": "+e.Detail)
		}
	}
	detail := strings.Join(parts, "; ")
	switch {
	case count[StateModified] > 0:
		return StateModified, detail
	case count[StateOK] == len(evs):
		return StateOK, "all claude-memory hook entries installed"
	case count[StateAbsent] == len(evs):
		return StateAbsent, detail
	default:
		return StateOutdated, detail
	}
}

// MergeSummary describes what a merge or unmerge did (or, with changed ==
// false, that it had nothing to do).
type MergeSummary struct {
	Added    int // groups added for an absent event
	Replaced int // entries replaced in place (outdated, or modified with overwrite)
	Deduped  int // duplicate entries of ours removed (overwrite only)
	Removed  int // entries removed by UnmergeSettings
	// Drift lists "<event>: <reason>" for entries of ours kept unchanged
	// because they are modified (merge) or not the recorded entry (unmerge).
	Drift []string
	// CreatedHooksKey is true when the merge created the "hooks" object
	// (the manifest records it so uninstall may remove it again, AC-54).
	CreatedHooksKey bool
}

// MergeSettings ensures exactly one desired entry per event (AC-37). The
// returned bytes equal b, and changed is false, when nothing semantic
// changes — regardless of formatting. Modified entries are kept and listed
// in Drift unless overwriteModified is set, in which case the preferred one
// (an entry already equal to desired, else the first) is replaced in place
// and the other entries of ours removed.
func MergeSettings(b []byte, desired []HookEntry, recorded RecordedHooks, overwriteModified bool) ([]byte, MergeSummary, bool, error) {
	var sum MergeSummary
	m, err := loadSettingsModel(b)
	if err != nil {
		return nil, sum, false, err
	}
	for _, d := range desired {
		ours := m.slots[d.Event]
		st, detail := eventState(ours, d, recorded[d.Event])
		switch st {
		case StateOK:
		case StateAbsent:
			if err := m.addEntry(d, &sum); err != nil {
				return nil, sum, false, err
			}
			sum.Added++
		case StateOutdated:
			if err := replaceSlot(ours[0], d); err != nil {
				return nil, sum, false, err
			}
			sum.Replaced++
		case StateModified:
			if !overwriteModified {
				sum.Drift = append(sum.Drift, d.Event+": "+detail)
				continue
			}
			keep := 0
			for i, s := range ours {
				if s.Canonical == d.Canonical() {
					keep = i
					break
				}
			}
			if ours[keep].Canonical != d.Canonical() {
				if err := replaceSlot(ours[keep], d); err != nil {
					return nil, sum, false, err
				}
				sum.Replaced++
			}
			drop := slices.Delete(slices.Clone(ours), keep, keep+1)
			removeSlots(drop)
			sum.Deduped += len(drop)
		}
	}
	return m.doc.bytes(), sum, m.doc.changed(), nil
}

// UnmergeSettings removes the entries of ours that equal the recorded
// entry for their event (AC-54). Entries of ours that differ are kept and
// listed in Drift. A group left with no entries is removed, then an event
// left with no groups; the "hooks" object is removed when it is left empty
// and removeHooksKey is set (install recorded that it created it).
func UnmergeSettings(b []byte, recorded RecordedHooks, removeHooksKey bool) ([]byte, MergeSummary, bool, error) {
	var sum MergeSummary
	m, err := loadSettingsModel(b)
	if err != nil {
		return nil, sum, false, err
	}
	if m.hooks == nil {
		return m.doc.bytes(), sum, false, nil
	}
	for _, ev := range slices.Clone(m.hooks.kids) {
		var drop []hookSlot
		for _, s := range m.slots[ev.key] {
			if rec, ok := recorded[ev.key]; ok && s.Canonical == rec {
				drop = append(drop, s)
			} else {
				sum.Drift = append(sum.Drift, ev.key+": entry differs from the one install recorded; kept")
			}
		}
		removeSlots(drop)
		sum.Removed += len(drop)
		if len(drop) > 0 && len(ev.val.kids) == 0 {
			m.hooks.removeMember(ev.key)
		}
	}
	if removeHooksKey && len(m.hooks.kids) == 0 && m.hooks.changed {
		m.doc.root.removeMember("hooks")
	}
	return m.doc.bytes(), sum, m.doc.changed(), nil
}

// addEntry appends a new group {"hooks":[entry]} to hooks.<event>,
// creating the event array and the "hooks" object when missing.
func (m *settingsModel) addEntry(d HookEntry, sum *MergeSummary) error {
	group, err := newJSONNode(hookGroupJSON{Hooks: []hookEntryJSON{d.json()}})
	if err != nil {
		return err
	}
	if m.hooks == nil {
		m.hooks = newEmptyObject()
		m.doc.root.appendKid("hooks", m.hooks)
		sum.CreatedHooksKey = true
	}
	ev := m.hooks.member(d.Event)
	if ev == nil {
		arr := newEmptyArray()
		m.hooks.appendKid(d.Event, arr)
		ev = m.hooks.member(d.Event)
	}
	ev.val.appendKid("", group)
	return nil
}

// replaceSlot replaces one entry in place: the inner element is swapped,
// so its group's other keys (a matcher) and siblings are untouched.
func replaceSlot(s hookSlot, d HookEntry) error {
	n, err := newJSONNode(d.json())
	if err != nil {
		return err
	}
	s.arr.kids[s.Index].val = n
	return nil
}

// removeSlots removes entries, highest positions first so earlier indexes
// stay valid, and drops a group whose "hooks" array becomes empty.
func removeSlots(slots []hookSlot) {
	slots = slices.Clone(slots)
	slices.SortFunc(slots, func(a, b hookSlot) int {
		if a.Group != b.Group {
			return b.Group - a.Group
		}
		return b.Index - a.Index
	})
	for _, s := range slots {
		s.arr.removeKid(s.Index)
		if len(s.arr.kids) == 0 {
			s.groups.removeKid(s.Group)
		}
	}
}

// ---- files -------------------------------------------------------------------

// ErrSettingsChanged is returned by WriteSettingsFile when the file changed
// since it was read (e.g. Claude Code wrote it meanwhile) (AC-39).
var ErrSettingsChanged = errors.New("settings file changed during install, re-run")

// SettingsBackupSuffix prefixes the timestamp of a settings backup:
// settings.json.bak.claude-memory.<UTC yyyymmddThhmmssZ> (AC-39).
const SettingsBackupSuffix = ".bak.claude-memory."

// SettingsFile is a settings file as read before an edit.
type SettingsFile struct {
	Path    string // the path asked for
	Target  string // the file edited: Path, or its symlink target inside Home
	Exists  bool
	Content []byte
	Hash    [32]byte // sha256 of Content (zero when !Exists)
	Mode    fs.FileMode
}

// ReadSettingsFile reads a settings file through the FS port, following a
// symlink only when its target lies inside home (AC-38; §8: dotfiles). A
// missing file is not an error (Exists false).
func ReadSettingsFile(fsys FS, home, path string) (*SettingsFile, error) {
	f := &SettingsFile{Path: path, Target: path}
	li, err := fsys.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if li.Mode()&fs.ModeSymlink != 0 {
		target, err := fsys.EvalSymlinks(path)
		if err != nil {
			return nil, &SettingsRefusal{Path: path, Reason: fmt.Sprintf("cannot resolve symlink: %v", err)}
		}
		if !underDir(fsys, target, home) {
			return nil, &SettingsRefusal{Path: path, Reason: fmt.Sprintf("symlink target %s is outside %s", target, home)}
		}
		f.Target = target
	}
	info, err := fsys.Stat(f.Target)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &SettingsRefusal{Path: path, Reason: "not a regular file"}
	}
	b, err := fsys.ReadFile(f.Target)
	if err != nil {
		return nil, err
	}
	f.Exists, f.Content, f.Hash, f.Mode = true, b, sha256.Sum256(b), info.Mode().Perm()
	return f, nil
}

// underDir reports whether p lies inside dir, comparing symlink-resolved
// forms when dir resolves (e.g. macOS /var → /private/var).
func underDir(fsys FS, p, dir string) bool {
	inside := func(p, dir string) bool {
		rel, err := filepath.Rel(dir, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	if inside(p, dir) {
		return true
	}
	if rd, err := fsys.EvalSymlinks(dir); err == nil {
		return inside(p, rd)
	}
	return false
}

// SettingsBackupPath is the AC-39 backup path for target at time ts
// (formatted by the caller's clock as UTC).
func SettingsBackupPath(target string, c Clock) string {
	return target + SettingsBackupSuffix + c.Now().UTC().Format("20060102T150405Z")
}

// WriteSettingsFile saves out over f (AC-39): it re-reads the file and
// aborts with ErrSettingsChanged if its hash changed since f was read,
// copies the original to a timestamped backup with the same mode (one
// backup per write; rotation is deferred, spec §12.1), re-checks the hash
// once more right before the write, and writes atomically keeping the mode
// (0644 for a new file, its directory created 0700). It returns the backup
// path ("" when the file did not exist). Callers write only when
// MergeSettings reported changed.
func WriteSettingsFile(fsys FS, clk Clock, f *SettingsFile, out []byte) (string, error) {
	if err := recheckSettings(fsys, f); err != nil {
		return "", err
	}
	mode := fs.FileMode(0o644)
	backup := ""
	if f.Exists {
		mode = f.Mode
		backup = SettingsBackupPath(f.Target, clk)
		if err := fsys.WriteFileAtomic(backup, f.Content, mode); err != nil {
			return "", fmt.Errorf("backup %s: %w", backup, err)
		}
		if err := recheckSettings(fsys, f); err != nil {
			return backup, err
		}
	} else if err := fsys.MkdirAll(filepath.Dir(f.Target), 0o700); err != nil {
		return "", err
	}
	if err := fsys.WriteFileAtomic(f.Target, out, mode); err != nil {
		return backup, err
	}
	return backup, nil
}

func recheckSettings(fsys FS, f *SettingsFile) error {
	b, err := fsys.ReadFile(f.Target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if f.Exists {
			return fmt.Errorf("%s: %w", f.Target, ErrSettingsChanged)
		}
		return nil
	case err != nil:
		return err
	case !f.Exists || sha256.Sum256(b) != f.Hash:
		return fmt.Errorf("%s: %w", f.Target, ErrSettingsChanged)
	}
	return nil
}

// ---- other settings files (AC-70) --------------------------------------------

// OtherSettingsFiles lists the settings files doctor and Detect inspect
// read-only for duplicate entries of ours (AC-70), excluding any that is
// the user settings file itself (e.g. Cwd == Home).
func OtherSettingsFiles(p Paths) []string {
	cands := []string{filepath.Join(p.ClaudeDir, "settings.local.json")}
	if p.Cwd != "" {
		cands = append(cands,
			filepath.Join(p.Cwd, ".claude", "settings.json"),
			filepath.Join(p.Cwd, ".claude", "settings.local.json"))
	}
	var out []string
	for _, c := range cands {
		if filepath.Clean(c) != filepath.Clean(p.SettingsJSON()) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// OtherSettingsHook is one of our entries found in another settings file.
type OtherSettingsHook struct {
	Path    string
	Event   string
	Command string
}

// OtherSettingsReport is the result of ScanOtherSettings.
type OtherSettingsReport struct {
	Duplicates []OtherSettingsHook
	// Unreadable maps a file that exists but could not be read or parsed to
	// the reason (doctor reports it; the file is never edited).
	Unreadable map[string]string
}

// ScanOtherSettings reads the AC-70 files read-only and reports every entry
// of ours in them: each would fire the hook a second time.
func ScanOtherSettings(fsys FS, p Paths) OtherSettingsReport {
	var r OtherSettingsReport
	for _, path := range OtherSettingsFiles(p) {
		b, err := fsys.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil && len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		var m *settingsModel
		if err == nil {
			m, err = loadSettingsModel(b)
		}
		if err != nil {
			if r.Unreadable == nil {
				r.Unreadable = map[string]string{}
			}
			var sr *SettingsRefusal
			if errors.As(err, &sr) {
				sr.Path = path
			}
			r.Unreadable[path] = err.Error()
			continue
		}
		if m.hooks == nil {
			continue
		}
		for _, ev := range m.hooks.kids {
			for _, s := range m.slots[ev.key] {
				r.Duplicates = append(r.Duplicates, OtherSettingsHook{Path: path, Event: ev.key, Command: s.Command})
			}
		}
	}
	return r
}
