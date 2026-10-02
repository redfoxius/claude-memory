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
)

// launchd jobs, read-only half (spec AC-44, AC-58 `jobs`; plan WI-S1-10).
// Slice 1 only detects: one plist read plus one `launchctl print
// gui/<uid>/<label>` per job, never a mutating launchctl call. Render,
// Install and Remove (and therefore the full JobManager interface) arrive in
// slice 2 (WI-S2-13).

// LaunchdLabelPrefix is the label prefix of our jobs, kept from the manual
// install so hand-installed jobs are recognized (§13 #6).
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
// ingest-pr runs git and az.
var JobTools = []string{"claude", "git", "az"}

// DefaultJobSpecs returns the two jobs (AC-43) running binPath (from
// ResolveBinPath) with the log paths filled in; PATH is computed by the jobs
// step [S2].
func DefaultJobSpecs(p Paths, binPath string) []JobSpec {
	mk := func(name string, hour, minute int) JobSpec {
		return JobSpec{Name: name, Label: LaunchdLabelPrefix + name, Program: binPath,
			Args: []string{name}, Hour: hour, Minute: minute, LogPath: filepath.Join(p.StateDir, name+".log")}
	}
	return []JobSpec{mk(JobCleanup, 7, 15), mk(JobIngestPR, 7, 0)}
}

// LaunchdJobs inspects launchd jobs through the ports. Runner may be the
// read-only adapter: every command it runs has Mutating unset.
type LaunchdJobs struct {
	FS     ReadFS
	Runner Runner
	Paths  Paths
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
	State           State
	Detail          string
}

// Detect implements the read-only half of JobManager.
func (l LaunchdJobs) Detect(ctx context.Context, j JobSpec) (State, string, error) {
	s, err := l.Inspect(ctx, j)
	return s.State, s.Detail, err
}

// Inspect reads the job's plist and asks launchctl whether it is loaded.
// State: absent (no plist); modified (unparseable, foreign label, another
// program that is not the legacy wrapper, other arguments); outdated
// (legacy run-with-env.sh wrapper, PATH lacking tool directories, or not
// loaded: apply in slice 2 replaces/loads it); ok otherwise.
func (l LaunchdJobs) Inspect(ctx context.Context, j JobSpec) (LaunchdJobStatus, error) {
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
