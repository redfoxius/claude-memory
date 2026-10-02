package setup

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"claude-memory/integration"
)

// testJobPATH is the PATH of the jobs in these tests.
const testJobPATH = "/opt/homebrew/bin:/usr/bin:/bin"

// launchctlNotFoundExit is the exit status of `launchctl print` for a
// service that is not loaded (113 on macOS 11+; to be re-confirmed on the
// owner's Mac per WI-S1-0). Detect treats every non-zero exit as "not
// loaded", so the exact value is not load-bearing.
const launchctlNotFoundExit = 113

func TestLaunchdInspect(t *testing.T) {
	t.Parallel()
	loaded := Result{Stdout: nil}
	cases := []struct {
		name     string
		plist    string // fixture ("" = none, "@rendered" = what Render returns)
		recorded func(path string, fixture []byte) map[string]string
		print    *Result
		printErr error
		paths    map[string]string // LookPath results
		state    State
		check    func(t *testing.T, s LaunchdJobStatus)
	}{
		{name: "absent", state: StateAbsent},
		{name: "legacy run-with-env", plist: "legacy-cleanup", print: &loaded, state: StateOutdated,
			check: func(t *testing.T, s LaunchdJobStatus) {
				if !s.Legacy || !strings.Contains(s.Detail, "run-with-env.sh") {
					t.Errorf("status %+v, want legacy flagged", s)
				}
			}},
		{name: "rendered loaded", plist: "@rendered", print: &loaded, state: StateOK,
			paths: map[string]string{"claude": "/opt/homebrew/bin/claude", "git": "/usr/bin/git"},
			check: func(t *testing.T, s LaunchdJobStatus) {
				if !s.Loaded || s.LastExit != "0" || !strings.Contains(s.Detail, "last exit code 0") {
					t.Errorf("status %+v", s)
				}
			}},
		{name: "rendered not loaded", plist: "@rendered",
			print: &Result{ExitCode: launchctlNotFoundExit}, state: StateOutdated,
			check: func(t *testing.T, s LaunchdJobStatus) {
				if !s.LoadedKnown || s.Loaded || !strings.Contains(s.Detail, "not loaded") {
					t.Errorf("status %+v", s)
				}
			}},
		{name: "PATH lacks tool dirs", plist: "@rendered", print: &loaded, state: StateOutdated,
			paths: map[string]string{"claude": "/Users/owner/.volta/bin/claude", "az": "/usr/local/bin/az", "git": "/usr/bin/git"},
			check: func(t *testing.T, s LaunchdJobStatus) {
				want := []string{"/Users/owner/.volta/bin", "/usr/local/bin"}
				if !slices.Equal(s.MissingPathDirs, want) {
					t.Errorf("missing = %v, want %v", s.MissingPathDirs, want)
				}
			}},
		{name: "hand-written direct plist, unrecorded", plist: "direct-cleanup", print: &loaded, state: StateModified,
			paths: map[string]string{"claude": "/opt/homebrew/bin/claude"},
			check: func(t *testing.T, s LaunchdJobStatus) {
				if !strings.Contains(s.Detail, "differs from the plist this version renders") {
					t.Errorf("detail %q", s.Detail)
				}
			}},
		{name: "recorded and unedited but differs from the rendering", plist: "direct-cleanup", print: &loaded, state: StateOutdated,
			paths: map[string]string{"claude": "/opt/homebrew/bin/claude"},
			recorded: func(path string, fixture []byte) map[string]string {
				return map[string]string{path: sha256Hex(fixture)}
			}},
		{name: "recorded and edited", plist: "direct-cleanup", print: &loaded, state: StateModified,
			paths:    map[string]string{"claude": "/opt/homebrew/bin/claude"},
			recorded: func(path string, _ []byte) map[string]string { return map[string]string{path: "0123abcd"} }},
		{name: "foreign program", plist: "foreign-program", print: &loaded, state: StateModified},
		{name: "unparseable", plist: "unparseable", state: StateModified,
			check: func(t *testing.T, s LaunchdJobStatus) {
				if s.ParseErr == nil {
					t.Error("ParseErr not set")
				}
			}},
		{name: "launchctl unavailable", plist: "@rendered", printErr: errors.New("launchctl: not found"), state: StateOK,
			check: func(t *testing.T, s LaunchdJobStatus) {
				if s.LoadedKnown || !strings.Contains(s.Detail, "load state unknown") {
					t.Errorf("status %+v", s)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := testPaths(t)
			fsys := NewFakeFS(t, filepath.Dir(p.Home))
			fsys.ReadOnly = true
			job := DefaultJobSpecs(p, p.InstalledBinary(), testJobPATH)[0]
			if job.Name != JobCleanup {
				t.Fatalf("first default job = %s", job.Name)
			}
			l := LaunchdJobs{FS: fsys, Paths: p, Assets: integration.FS}
			var fixture []byte
			switch tc.plist {
			case "":
			case "@rendered":
				files, err := l.Render(job)
				if err != nil {
					t.Fatal(err)
				}
				fixture = files[l.PlistPath(job)]
			default:
				fixture = readTestdata(t, filepath.Join("testdata", "fixtures", "launchd", tc.plist+".plist"))
				fixture = []byte(strings.ReplaceAll(string(fixture), "__HOME__", p.Home))
			}
			if fixture != nil {
				writeTestFile(t, l.PlistPath(job), fixture, 0o644)
			}
			var recorded map[string]string
			if tc.recorded != nil {
				recorded = tc.recorded(l.PlistPath(job), fixture)
			}
			argv := []string{"launchctl", "print", "gui/501/" + job.Label}
			runner := NewFakeRunner(t)
			runner.ReadOnly = true
			switch {
			case tc.printErr != nil:
				runner.ScriptError(ArgvEq(argv...), tc.printErr)
			case tc.print != nil:
				res := *tc.print
				if res.ExitCode == 0 {
					res.Stdout = readTestdata(t, "testdata/fixtures/launchd/launchctl-print-loaded.txt")
				} else {
					res.Stderr = readTestdata(t, "testdata/fixtures/launchd/launchctl-print-not-found.txt")
				}
				runner.Script(ArgvEq(argv...), res)
			}
			for name, full := range tc.paths {
				runner.SetPath(name, full)
			}
			l.Runner = runner

			s, err := l.Inspect(context.Background(), job, recorded)
			if err != nil {
				t.Fatal(err)
			}
			if s.State != tc.state {
				t.Errorf("state = %s (%s), want %s", s.State, s.Detail, tc.state)
			}
			if tc.check != nil {
				tc.check(t, s)
			}
			st, detail, err := l.Detect(context.Background(), job, recorded)
			if err != nil || st != s.State || detail != s.Detail {
				t.Errorf("Detect = %s, %q, %v; Inspect = %s, %q", st, detail, err, s.State, s.Detail)
			}
			if runner.MutatingCalls() != 0 || len(fsys.Writes()) != 0 {
				t.Errorf("mutating calls %d, writes %v: Detect must be read-only", runner.MutatingCalls(), fsys.Writes())
			}
		})
	}
}

func TestDefaultJobSpecs(t *testing.T) {
	t.Parallel()
	p := testPaths(t)
	jobs := DefaultJobSpecs(p, p.InstalledBinary(), testJobPATH)
	if len(jobs) != 2 {
		t.Fatalf("jobs = %+v", jobs)
	}
	want := map[string][3]int{JobCleanup: {7, 15}, JobIngestPR: {7, 0}}
	for _, j := range jobs {
		w := want[j.Name]
		if j.Label != LaunchdLabelPrefix+j.Name || j.Program != p.InstalledBinary() ||
			!slices.Equal(j.Args, []string{j.Name}) || j.Hour != w[0] || j.Minute != w[1] ||
			j.PATH != testJobPATH || j.LogPath != filepath.Join(p.StateDir, j.Name+".log") {
			t.Errorf("job %+v", j)
		}
	}
}

func TestParsePlist(t *testing.T) {
	t.Parallel()
	pl, err := parsePlist(readTestdata(t, "testdata/fixtures/launchd/direct-cleanup.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if pl.get("Label").str != "io.github.claude-memory.cleanup" ||
		pl.get("StartCalendarInterval").get("Minute").str != "15" ||
		pl.get("RunAtLoad").kind != "false" ||
		len(pl.get("ProgramArguments").arr) != 2 {
		t.Errorf("parsed %+v", pl)
	}
	for _, bad := range []string{
		"<plist><dict><string>x</string></dict></plist>",       // value without key
		"<plist><dict><key>a</key><key>b</key></dict></plist>", // key without value
		"<plist><dict><key>a</key><widget/></dict></plist>",    // unknown element
		"<notplist/>",
		"",
	} {
		if _, err := parsePlist([]byte(bad)); err == nil {
			t.Errorf("parsePlist(%q) succeeded", bad)
		}
	}
}
