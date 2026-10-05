package setup

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// launchd jobs (spec AC-44, AC-58 `jobs`; plan WI-S1-10, WI-S2-13a).
// LaunchdJobs is the read-only half: Render, plus Inspect/Detect (one plist
// read and one `launchctl print gui/<uid>/<label>` per job, never a mutating
// launchctl call). LaunchdManager adds Install. There is no Remove: uninstall
// is not part of this build.

// LaunchdLabelPrefix is the label prefix of our jobs, so hand-installed
// jobs that follow the same naming are recognized (§13 #6).
const LaunchdLabelPrefix = "io.github.claude-memory."

// Job names.
const (
	JobCleanup  = "cleanup"
	JobIngestPR = "ingest-pr"
)

// legacyWrapperSuffix identifies the plists of the manual install, whose
// ProgramArguments[0] is …/claude-memory-run-with-env.sh: the wrapper
// `source`s the env file, which corrupts passwords containing $ or &.
const legacyWrapperSuffix = "run-with-env.sh"

// launchdDefaultPATH is the PATH launchd gives a job without
// EnvironmentVariables.PATH.
const launchdDefaultPATH = "/usr/bin:/bin:/usr/sbin:/sbin"

// JobTools are the external programs the jobs execute; a job's PATH must
// contain their directories (AC-43): session extraction runs claude,
// ingest-pr runs git and az, gh or glab.
var JobTools = []string{"claude", "git", "az", "gh", "glab"}

// DefaultJobSpecs returns the two jobs (AC-43) running binPath (from
// ResolveBinPath) with jobPATH as their PATH and the log paths filled in.
func DefaultJobSpecs(p Paths, binPath, jobPATH string) []JobSpec {
	mk := func(name string, hour, minute int) JobSpec {
		return JobSpec{Name: name, Label: LaunchdLabelPrefix + name, Program: binPath,
			Args: []string{name}, Hour: hour, Minute: minute, PATH: jobPATH,
			LogPath: filepath.Join(p.StateDir, name+".log")}
	}
	return []JobSpec{mk(JobCleanup, 7, 15), mk(JobIngestPR, 7, 0)}
}

// ComputeJobPATH is the PATH given to the jobs (AC-43): the directories of
// JobTools as found on the user's PATH (Runner.LookPath), in JobTools order,
// then /usr/bin and /bin. Preferring a stable shim directory over a versioned
// one (nvm) is deferred.
func ComputeJobPATH(r Runner) string {
	var dirs []string
	for _, tool := range JobTools {
		if p, err := r.LookPath(tool); err == nil && p != "" {
			if d := filepath.Dir(p); !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	for _, d := range []string{"/usr/bin", "/bin"} {
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return strings.Join(dirs, string(filepath.ListSeparator))
}

// recordedJobHashes maps each launchd plist path the manifest recorded to its
// sha256, the input of JobDetector.Detect. The jobs step and doctor both use
// it, so they compare against the same record. nil without a manifest.
func recordedJobHashes(m *Manifest) map[string]string {
	var out map[string]string
	for _, a := range m.Find(KindLaunchd) {
		if a.Path == "" || a.SHA256 == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[a.Path] = a.SHA256
	}
	return out
}

// assetLaunchdTemplate is the embedded plist template, relative to Assets.
const assetLaunchdTemplate = "launchd/job.plist.tmpl"

// LaunchdJobs inspects launchd jobs through the ports. Runner may be the
// read-only adapter: every command it runs has Mutating unset. Assets is the
// embedded integration tree (the plist template).
type LaunchdJobs struct {
	FS     ReadFS
	Runner Runner
	Paths  Paths
	Assets fs.FS
}

// LaunchdManager is LaunchdJobs plus the write half (Install). Write and the
// embedded FS are the same adapter; under --dry-run both refuse every write
// and the Runner refuses launchctl bootout/bootstrap.
type LaunchdManager struct {
	LaunchdJobs
	Write FS
	// Sleep pauses between the bootstrap attempts; nil sleeps for real
	// (cut short by ctx). Tests inject a no-op.
	Sleep func(time.Duration)
}

// bootstrapRetryDelay is the pause before the one bootstrap retry.
const bootstrapRetryDelay = 500 * time.Millisecond

// bootstrapTransient reports the exit codes bootstrap gives right after a
// bootout of a loaded job (5 "Input/output error", 37 "Operation already in
// progress"): the service is still being torn down.
func bootstrapTransient(code int) bool { return code == 5 || code == 37 }

func (m LaunchdManager) pause(ctx context.Context, d time.Duration) {
	if m.Sleep != nil {
		m.Sleep(d)
		return
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

var (
	_ JobDetector = LaunchdJobs{}
	_ JobManager  = LaunchdManager{}
)

var plistTemplateFuncs = template.FuncMap{"xml": func(s string) (string, error) {
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return "", err
	}
	return b.String(), nil
}}

// Render implements JobDetector: the plist for j, keyed by its absolute path.
func (l LaunchdJobs) Render(j JobSpec) (map[string][]byte, error) {
	raw, err := fs.ReadFile(l.Assets, assetLaunchdTemplate)
	if err != nil {
		return nil, fmt.Errorf("embedded %s: %w", assetLaunchdTemplate, err)
	}
	t, err := template.New("job").Funcs(plistTemplateFuncs).Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", assetLaunchdTemplate, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, j); err != nil {
		return nil, fmt.Errorf("render %s for %s: %w", assetLaunchdTemplate, j.Name, err)
	}
	return map[string][]byte{l.PlistPath(j): buf.Bytes()}, nil
}

// launchctlNotLoaded reports whether a failed `launchctl bootout` only means
// the job was not loaded: exit 3 (measured on macOS 26.2, WI-S1-0) or 113, or
// stderr saying so.
func launchctlNotLoaded(res Result) bool {
	if res.ExitCode == 3 || res.ExitCode == 113 {
		return true
	}
	e := strings.ToLower(string(res.Stderr))
	return strings.Contains(e, "no such process") || strings.Contains(e, "not loaded") || strings.Contains(e, "could not find")
}

// Install implements JobManager: it writes the plist, boots out a loaded copy
// (a not-loaded job is fine) and bootstraps the new one, so a changed plist is
// always the one launchd runs.
func (m LaunchdManager) Install(ctx context.Context, j JobSpec) error {
	files, err := m.Render(j)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(j.LogPath); dir != "." {
		if err := m.Write.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	for p, b := range files {
		if err := m.Write.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
		}
		if err := m.Write.WriteFileAtomic(p, b, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
	}
	domain := "gui/" + strconv.Itoa(m.Paths.UID)
	res, err := m.Runner.Run(ctx, Cmd{Argv: []string{"launchctl", "bootout", domain + "/" + j.Label}, Mutating: true})
	if err != nil {
		return fmt.Errorf("launchctl bootout %s: %w", j.Label, err)
	}
	if res.ExitCode != 0 && !launchctlNotLoaded(res) {
		return fmt.Errorf("launchctl bootout %s: exit %d: %s", j.Label, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	plist := m.PlistPath(j)
	bootstrap := Cmd{Argv: []string{"launchctl", "bootstrap", domain, plist}, Mutating: true}
	res, err = m.Runner.Run(ctx, bootstrap)
	if err == nil && bootstrapTransient(res.ExitCode) {
		// One bounded retry: the booted-out job may still be going away.
		m.pause(ctx, bootstrapRetryDelay)
		res, err = m.Runner.Run(ctx, bootstrap)
	}
	if err != nil {
		return fmt.Errorf("launchctl bootstrap %s: %w", j.Label, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("launchctl bootstrap %s: exit %d: %s", j.Label, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// PlistPath is <LaunchAgentsDir>/<label>.plist.
func (l LaunchdJobs) PlistPath(j JobSpec) string {
	return filepath.Join(l.Paths.LaunchAgentsDir, j.Label+".plist")
}

// LaunchdJobStatus is everything Inspect found about one job.
type LaunchdJobStatus struct {
	PlistPath string
	Exists    bool
	ParseErr  error
	Label     string
	Program   string   // ProgramArguments[0]
	Args      []string // ProgramArguments[1:]
	Legacy    bool     // Program is the run-with-env.sh wrapper
	PATH      string   // EnvironmentVariables.PATH ("" when unset)
	HasPATH   bool
	// MissingPathDirs are directories of JobTools found on the user's PATH
	// that the job's PATH lacks.
	MissingPathDirs []string
	LoadedKnown     bool   // launchctl print ran
	Loaded          bool   // launchctl print found the service
	LastExit        string // "last exit code" from launchctl print ("" unknown)
	// Hash is the sha256 of the file on disk.
	Hash   string
	State  State
	Detail string
}

// Detect implements JobDetector. recorded maps plist path to the sha256 the
// manifest recorded (recordedJobHashes; nil without a manifest).
func (l LaunchdJobs) Detect(ctx context.Context, j JobSpec, recorded map[string]string) (State, string, error) {
	s, err := l.Inspect(ctx, j, recorded)
	return s.State, s.Detail, err
}

// Inspect reads the job's plist and asks launchctl whether it is loaded.
// State: absent (no plist); modified (unparseable, foreign label, another
// program that is not the legacy wrapper, other arguments, or a plist that
// differs from the rendering and is not one install recorded unedited);
// outdated (legacy run-with-env.sh wrapper, a recorded unedited plist that
// differs from the rendering, PATH lacking tool directories, or not loaded:
// install replaces/loads it); ok otherwise.
func (l LaunchdJobs) Inspect(ctx context.Context, j JobSpec, recorded map[string]string) (LaunchdJobStatus, error) {
	s := LaunchdJobStatus{PlistPath: l.PlistPath(j)}
	b, err := l.FS.ReadFile(s.PlistPath)
	if errors.Is(err, fs.ErrNotExist) {
		s.State, s.Detail = StateAbsent, "no plist "+s.PlistPath
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.Exists = true
	s.Hash = sha256Hex(b)
	pl, err := parsePlist(b)
	if err == nil && pl.kind != "dict" {
		err = errors.New("top-level value is not a dict")
	}
	if err != nil {
		s.ParseErr = err
		s.State, s.Detail = StateModified, fmt.Sprintf("unparseable plist %s: %v", s.PlistPath, err)
		return s, nil
	}
	s.Label = pl.get("Label").str
	if pa := pl.get("ProgramArguments"); len(pa.arr) > 0 {
		s.Program = pa.arr[0].str
		for _, a := range pa.arr[1:] {
			s.Args = append(s.Args, a.str)
		}
	}
	if env := pl.get("EnvironmentVariables"); env.kind == "dict" {
		if p, ok := env.dict["PATH"]; ok {
			s.PATH, s.HasPATH = p.str, true
		}
	}
	s.Legacy = strings.HasSuffix(filepath.Base(s.Program), legacyWrapperSuffix)
	s.MissingPathDirs = l.missingToolDirs(s)
	l.probeLoaded(ctx, j, &s)

	worst := StateOK
	var notes []string
	note := func(st State, msg string) {
		notes = append(notes, msg)
		if st == StateModified || worst == StateOK {
			worst = st
		}
	}
	switch {
	case s.Label != j.Label:
		note(StateModified, fmt.Sprintf("plist label is %q, want %q", s.Label, j.Label))
	case s.Legacy:
		note(StateOutdated, "runs the legacy run-with-env.sh wrapper (it sources the env file: passwords with $ or & break); the job should run the binary directly")
	case s.Program != j.Program:
		note(StateModified, fmt.Sprintf("runs %s, not the installed binary %s", s.Program, j.Program))
	}
	if !s.Legacy && !slices.Equal(s.Args, j.Args) {
		note(StateModified, fmt.Sprintf("arguments %q, want %q", s.Args, j.Args))
	}
	if len(s.MissingPathDirs) > 0 {
		note(StateOutdated, "job PATH lacks "+strings.Join(s.MissingPathDirs, ", "))
	}
	rendered, err := l.Render(j)
	if err != nil {
		return s, err
	}
	if s.Hash != sha256Hex(rendered[s.PlistPath]) && !s.Legacy {
		switch {
		case recorded[s.PlistPath] == s.Hash:
			note(StateOutdated, "differs from this version's plist (binary path, schedule or PATH changed); written by install, unedited")
		case worst != StateModified:
			note(StateModified, "differs from the plist this version renders (edited, or a manual install)")
		}
	}
	if s.LoadedKnown && !s.Loaded {
		note(StateOutdated, "plist present but not loaded")
	}
	s.State = worst
	if len(notes) == 0 {
		notes = append(notes, "loaded")
		if s.LastExit != "" {
			notes[0] += "; last exit code " + s.LastExit
		}
	}
	if !s.LoadedKnown && worst == StateOK {
		notes = append(notes, "load state unknown (launchctl print did not run)")
	}
	s.Detail = strings.Join(notes, "; ")
	return s, nil
}

// missingToolDirs returns the directories of the JobTools on the user's
// PATH (Runner.LookPath) that the job's PATH lacks.
func (l LaunchdJobs) missingToolDirs(s LaunchdJobStatus) []string {
	jobPATH := s.PATH
	if !s.HasPATH {
		jobPATH = launchdDefaultPATH
	}
	have := filepath.SplitList(jobPATH)
	var missing []string
	for _, tool := range JobTools {
		p, err := l.Runner.LookPath(tool)
		if err != nil || p == "" {
			continue
		}
		dir := filepath.Dir(p)
		if !slices.Contains(have, dir) && !slices.Contains(missing, dir) {
			missing = append(missing, dir)
		}
	}
	return missing
}

var launchctlLastExitRe = regexp.MustCompile(`(?m)^\s*last exit code\s*=\s*(.+?)\s*$`)

// probeLoaded runs the read-only `launchctl print gui/<uid>/<label>`.
// Exit 0 means loaded; any other exit (113 "Could not find service") means
// not loaded; a run error leaves the state unknown.
func (l LaunchdJobs) probeLoaded(ctx context.Context, j JobSpec, s *LaunchdJobStatus) {
	res, err := l.Runner.Run(ctx, Cmd{Argv: []string{"launchctl", "print", "gui/" + strconv.Itoa(l.Paths.UID) + "/" + j.Label}})
	if err != nil {
		return
	}
	s.LoadedKnown = true
	s.Loaded = res.ExitCode == 0
	if m := launchctlLastExitRe.FindSubmatch(res.Stdout); s.Loaded && m != nil {
		s.LastExit = string(m[1])
	}
}

// ---- minimal XML plist reader ------------------------------------------------

// plistValue is one plist value: kind is the element name (string,
// integer, real, date, data, true, false, array, dict); str holds scalar
// text.
type plistValue struct {
	kind string
	str  string
	arr  []plistValue
	dict map[string]plistValue
}

func (v plistValue) get(key string) plistValue { return v.dict[key] }

// parsePlist parses an XML property list and returns its top-level value.
func parsePlist(b []byte) (plistValue, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	inPlist := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return plistValue{}, errors.New("no plist value")
		}
		if err != nil {
			return plistValue{}, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "plist" && !inPlist {
			inPlist = true
			continue
		}
		if !inPlist {
			return plistValue{}, fmt.Errorf("unexpected <%s> before <plist>", se.Name.Local)
		}
		return parsePlistValue(dec, se)
	}
}

func parsePlistValue(dec *xml.Decoder, se xml.StartElement) (plistValue, error) {
	v := plistValue{kind: se.Name.Local}
	switch v.kind {
	case "string", "integer", "real", "date", "data", "key":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return v, err
		}
		v.str = s
		return v, nil
	case "true", "false":
		v.str = v.kind
		return v, dec.Skip()
	case "array", "dict":
	default:
		return v, fmt.Errorf("unknown plist element <%s>", v.kind)
	}
	pendingKey, haveKey := "", false
	if v.kind == "dict" {
		v.dict = map[string]plistValue{}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return v, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if haveKey {
				return v, fmt.Errorf("dict key %q has no value", pendingKey)
			}
			return v, nil
		case xml.StartElement:
			child, err := parsePlistValue(dec, t)
			if err != nil {
				return v, err
			}
			switch {
			case v.kind == "array":
				v.arr = append(v.arr, child)
			case child.kind == "key":
				if haveKey {
					return v, fmt.Errorf("dict key %q has no value", pendingKey)
				}
				pendingKey, haveKey = child.str, true
			case !haveKey:
				return v, fmt.Errorf("dict value <%s> without a key", child.kind)
			default:
				v.dict[pendingKey] = child
				haveKey = false
			}
		}
	}
}
