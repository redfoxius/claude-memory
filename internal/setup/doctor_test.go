package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"claude-memory/integration"
)

// Doctor tests (plan WI-S1-11): one fixture HOME that passes every check,
// then one mutation per check id × branch. Every test runs with the
// read-only FakeFS and FakeRunner and asserts zero writes and zero mutating
// commands at cleanup (AC-57).

// ---- fake probers ----------------------------------------------------------

type fakeDB struct {
	mu     sync.Mutex
	status DBStatus
	err    error
	hang   bool          // block until ctx is done
	stuck  chan struct{} // when set, block until closed, ignoring ctx
	dsns   []string
}

func (f *fakeDB) Probe(ctx context.Context, dsn string) (DBStatus, error) {
	f.mu.Lock()
	f.dsns = append(f.dsns, dsn)
	f.mu.Unlock()
	if f.stuck != nil {
		<-f.stuck
	}
	if f.hang {
		<-ctx.Done()
		return DBStatus{ErrorClass: DBErrUnreachable}, ctx.Err()
	}
	return f.status, f.err
}

func (f *fakeDB) Migrate(context.Context, string) error {
	panic("doctor must never migrate (AC-57)")
}

func (f *fakeDB) LocalServerEvidence(context.Context) (bool, string) { return false, "" }

type fakeOllama struct {
	versionErr error
	hasModel   bool
	tagsErr    error
	dims       int
	latency    time.Duration
	embedErr   error
	hang       bool
	calls      []string
	mu         sync.Mutex
}

func (f *fakeOllama) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeOllama) Version(ctx context.Context, url string) (string, error) {
	f.record("version " + url)
	if f.hang {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if f.versionErr != nil {
		return "", f.versionErr
	}
	return "0.12.3", nil
}

func (f *fakeOllama) HasModel(_ context.Context, url, model string) (bool, error) {
	f.record("tags " + model)
	return f.hasModel, f.tagsErr
}

func (f *fakeOllama) Pull(context.Context, string, string, func(int64, int64)) error {
	panic("doctor must never pull (AC-57)")
}

func (f *fakeOllama) EmbedDims(_ context.Context, url, model string) (int, time.Duration, error) {
	f.record("embed " + model)
	return f.dims, f.latency, f.embedErr
}

// ---- fixture ---------------------------------------------------------------

const testDSN = "postgresql://claude_memory:Secret-pass-123@db.example:5432/claude_memory"

type doctorFixture struct {
	t      *testing.T
	p      Paths
	fs     *FakeFS
	runner *FakeRunner
	db     *fakeDB
	ollama *fakeOllama
	env    Env
	plat   PlatformInfo
	red    *Redactor
}

func healthyDB() DBStatus {
	return DBStatus{Connected: true, RTT: 4 * time.Millisecond, ServerVersion: "16.4", VectorVersion: "0.7.0",
		Migrations: []MigrationStatus{{ID: "0001"}, {ID: "0002"}}}
}

func writeFile(t *testing.T, p string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func asset(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(integration.FS, name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func settingsWith(entries ...HookEntry) []byte {
	type group struct {
		Hooks []hookEntryJSON `json:"hooks"`
	}
	hooks := map[string][]group{}
	for _, e := range entries {
		hooks[e.Event] = append(hooks[e.Event], group{Hooks: []hookEntryJSON{e.json()}})
	}
	b, _ := json.MarshalIndent(map[string]any{"model": "opus", "hooks": hooks}, "", "  ")
	return append(b, '\n')
}

// newDoctorFixture builds a HOME on which every check passes (on linux: the
// jobs check is info there in slice 1, tools.az is info without PR repos and
// manifest is info without install.json).
func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()
	p := testPaths(t)
	root := filepath.Dir(p.Home)
	f := &doctorFixture{t: t, p: p, red: NewRedactor(),
		db:     &fakeDB{status: healthyDB()},
		ollama: &fakeOllama{hasModel: true, dims: 1024, latency: 40 * time.Millisecond},
		env:    Env{"PATH": p.BinDir + ":/usr/bin:/bin"},
		plat:   PlatformInfo{OS: OSLinux, Arch: "amd64", OSVersion: "Ubuntu 24.04", JobsBackend: JobsNone},
	}
	f.fs = NewFakeFS(t, root)
	f.fs.ReadOnly = true
	f.runner = NewFakeRunner(t)
	f.runner.ReadOnly = true
	f.runner.SetPath("git", "/usr/bin/git").SetPath("claude", filepath.Join(p.BinDir, "claude")).
		SetPath(BinaryName, p.InstalledBinary())
	t.Cleanup(func() {
		if w := f.fs.Writes(); len(w) != 0 {
			t.Errorf("doctor attempted FS writes (AC-57): %v", w)
		}
		if n := f.runner.MutatingCalls(); n != 0 {
			t.Errorf("doctor ran %d mutating commands (AC-57)", n)
		}
	})

	writeFile(t, p.EnvFile(), []byte("# claude-memory\nMEMORY_PG_DSN="+testDSN+"\nMEMORY_OLLAMA_URL=http://127.0.0.1:11434\nMEMORY_EMBED_MAX_TOKENS=2048\n"), 0o600)
	writeFile(t, p.InstalledBinary(), []byte("#!/bin/false\n"), 0o755)
	writeFile(t, filepath.Join(p.BinDir, "claude"), []byte("#!/bin/false\n"), 0o755)
	f.writeHookScripts(p.InstalledBinary())
	writeFile(t, p.SettingsJSON(), settingsWith(DesiredHooks(p.HookScriptsDir())...), 0o644)
	reg := fmt.Sprintf(`{"numStartups": 3, "mcpServers": {"claude-memory": {"type": "stdio", "command": %q, "args": ["serve"], "env": {}}}}`, p.InstalledBinary())
	writeFile(t, p.ClaudeJSON, []byte(reg), 0o600)
	for _, name := range integration.Skills {
		writeFile(t, filepath.Join(p.SkillsDir(), name, "SKILL.md"), asset(t, integration.SkillFile(name)), 0o644)
	}
	md, _, err := UpsertMDBlock([]byte("# My notes\n"), string(asset(t, integration.ClaudeMDSection)))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(p.ClaudeDir, "CLAUDE.md"), md, 0o644)
	writeFile(t, p.NamespacesFile(), []byte("default: global\nnamespaces:\n  - namespace: work\n    paths: [\"~/work/**\"]\n"), 0o600)
	if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.Cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// writeHookScripts installs the embedded hook scripts rendered for bin, the
// way `install` writes them (AC-36).
func (f *doctorFixture) writeHookScripts(bin string) {
	f.t.Helper()
	for name, a := range map[string]string{
		HookScriptUserPromptSubmit: integration.HookUserPromptSubmit,
		HookScriptSessionEnd:       integration.HookSessionEnd,
	} {
		out, err := RenderHookScript(asset(f.t, a), bin)
		if err != nil {
			f.t.Fatal(err)
		}
		writeFile(f.t, filepath.Join(f.p.HookScriptsDir(), name), out, 0o755)
	}
}

func (f *doctorFixture) deps() DoctorDeps {
	return DoctorDeps{Paths: f.p, Env: f.env, Platform: f.plat, FS: f.fs, Runner: f.runner,
		Clock: NewFakeClock(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)), DB: f.db, Ollama: f.ollama,
		Assets: integration.FS, Version: "test", Redactor: f.red}
}

func (f *doctorFixture) run() DoctorReport {
	f.t.Helper()
	return RunDoctor(context.Background(), f.deps(), DoctorOptions{Timeout: 2 * time.Second, Deadline: 5 * time.Second})
}

// darwin switches the fixture to macOS with both launchd jobs loaded.
func (f *doctorFixture) darwin(prRepos bool) {
	f.plat = PlatformInfo{OS: OSDarwin, Arch: "arm64", OSVersion: "macOS 15.1", JobsBackend: JobsLaunchd}
	f.runner.SetPath("git", "/usr/bin/git")
	if prRepos {
		f.appendEnv("MEMORY_PR_INGEST_REPOS=" + f.p.Home + "/work\n")
		f.runner.SetPath("az", "/usr/bin/az")
	}
	// The plists install renders: the doctor compares against the rendering.
	lj := LaunchdJobs{FS: f.fs, Runner: f.runner, Paths: f.p, Assets: integration.FS}
	for _, j := range DefaultJobSpecs(f.p, f.p.InstalledBinary(), ComputeJobPATH(f.runner)) {
		if j.Name == JobIngestPR && !prRepos {
			continue
		}
		files, err := lj.Render(j)
		if err != nil {
			f.t.Fatal(err)
		}
		for path, b := range files {
			writeFile(f.t, path, b, 0o644)
		}
	}
	loaded := readTestdata(f.t, "testdata/fixtures/launchd/launchctl-print-loaded.txt")
	f.runner.Script(ArgvPrefix("launchctl", "print"), Result{Stdout: loaded})
}

func (f *doctorFixture) appendEnv(line string) {
	b, err := os.ReadFile(f.p.EnvFile())
	if err != nil {
		f.t.Fatal(err)
	}
	writeFile(f.t, f.p.EnvFile(), append(b, line...), 0o600)
}

func (f *doctorFixture) setEnvFile(content string, mode os.FileMode) {
	writeFile(f.t, f.p.EnvFile(), []byte(content), mode)
}

func mustResult(t *testing.T, r DoctorReport, id string) CheckResult {
	t.Helper()
	c, ok := r.Result(id)
	if !ok {
		t.Fatalf("no result for %s", id)
	}
	return c
}

// ---- tests ------------------------------------------------------------------

var wantCheckIDs = []string{
	"binary.version", "env.file", "env.perms", "env.format",
	"pg.connect", "pg.latency", "pg.vector", "pg.schema",
	"ollama.reachable", "ollama.model", "ollama.embed",
	"tools.git", "tools.claude", "tools.az", "tools.gh", "tools.glab",
	"mcp.registered", "hooks.scripts", "hooks.settings", "skills", "claude-md",
	"namespaces", "jobs", "dirs.state", "manifest",
}

func TestDoctorCheckTable(t *testing.T) {
	t.Parallel()
	if got := CheckIDs(); !slices.Equal(got, wantCheckIDs) {
		t.Fatalf("CheckIDs() =\n%v\nwant (AC-58, 25 checks)\n%v", got, wantCheckIDs)
	}
	for _, s := range SkillNames {
		if !slices.Contains(integration.Skills, s) {
			t.Errorf("SkillNames has %q, integration.Skills does not", s)
		}
	}
	for _, a := range []string{assetHookUserPromptSubmit, assetHookSessionEnd, assetClaudeMDSection} {
		if _, err := fs.Stat(integration.FS, a); err != nil {
			t.Errorf("asset %s: %v", a, err)
		}
	}
}

func TestDoctorHealthy(t *testing.T) {
	t.Parallel()
	f := newDoctorFixture(t)
	start := time.Now()
	r := f.run()
	if el := time.Since(start); el > time.Second {
		t.Errorf("healthy run took %s, want well under 1s (AC-59)", el)
	}
	if len(r.Checks) != 25 {
		t.Fatalf("%d checks, want 25", len(r.Checks))
	}
	wantInfo := map[string]bool{"tools.az": true, "tools.gh": true, "tools.glab": true, "jobs": true, "manifest": true}
	for _, c := range r.Checks {
		want := StatusPass
		if wantInfo[c.ID] {
			want = StatusInfo
		}
		if c.Status != want {
			t.Errorf("%s: %s (%s), want %s", c.ID, c.Status, c.Detail, want)
		}
	}
	if !r.OK(true) {
		t.Error("healthy run not OK under --strict")
	}
	if len(f.db.dsns) != 1 || f.db.dsns[0] != testDSN {
		t.Errorf("probed DSNs %q, want exactly the env file's", f.db.dsns)
	}
}

func TestDoctorHealthyDarwin(t *testing.T) {
	t.Parallel()
	f := newDoctorFixture(t)
	f.darwin(true)
	r := f.run()
	for _, id := range []string{"jobs", "tools.az"} {
		if c := mustResult(t, r, id); c.Status != StatusPass {
			t.Errorf("%s: %s (%s), want pass", id, c.Status, c.Detail)
		}
	}
	for _, c := range f.runner.Calls() {
		if c.Argv[0] != "launchctl" || c.Argv[1] != "print" {
			t.Errorf("unexpected command %q", c.Argv)
		}
	}
	if n := len(f.runner.Calls()); n != 2 {
		t.Errorf("%d launchctl calls, want one per job", n)
	}
}

// TestDoctorBranches covers each check id's failure branches (AC-58).
func TestDoctorBranches(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		mutate func(f *doctorFixture)
		want   map[string]Status
		detail map[string]string // substring of the detail
		remedy map[string]string // substring of the remedy
	}
	rm := func(f *doctorFixture, p string) {
		if err := os.RemoveAll(p); err != nil {
			f.t.Fatal(err)
		}
	}
	cases := []tc{
		// binary.version
		{name: "binary: another claude-memory first on PATH", mutate: func(f *doctorFixture) {
			other := filepath.Join(filepath.Dir(f.p.Home), "opt", "claude-memory")
			writeFile(f.t, other, []byte("x"), 0o755)
			f.runner.SetPath(BinaryName, other)
		}, want: map[string]Status{"binary.version": StatusWarn}, detail: map[string]string{"binary.version": "another file"}},
		{name: "binary: bin dir not on PATH", mutate: func(f *doctorFixture) { f.env["PATH"] = "/usr/bin:/bin" },
			want: map[string]Status{"binary.version": StatusInfo}},

		// env.file / env.perms / env.format
		{name: "env: no file, no DSN", mutate: func(f *doctorFixture) { rm(f, f.p.EnvFile()) },
			want: map[string]Status{"env.file": StatusFail, "env.perms": StatusSkip, "env.format": StatusSkip, "pg.connect": StatusSkip, "pg.schema": StatusSkip,
				"ollama.reachable": StatusPass}},
		{name: "env: no file, DSN in the environment", mutate: func(f *doctorFixture) {
			rm(f, f.p.EnvFile())
			f.env["MEMORY_PG_DSN"] = testDSN
		}, want: map[string]Status{"env.file": StatusWarn, "env.perms": StatusSkip, "env.format": StatusFail, "pg.connect": StatusSkip},
			detail: map[string]string{"env.format": "do not see your shell"}},
		{name: "env: group-readable", mutate: func(f *doctorFixture) { f.setEnvFile("MEMORY_PG_DSN="+testDSN+"\n", 0o644) },
			want: map[string]Status{"env.perms": StatusFail, "env.format": StatusPass, "pg.connect": StatusPass}, remedy: map[string]string{"env.perms": "chmod 600"}},
		{name: "env: DEPLOY.md export form", mutate: func(f *doctorFixture) {
			f.setEnvFile(`export MEMORY_PG_DSN="`+testDSN+"\"\n", 0o600)
		}, want: map[string]Status{"env.format": StatusFail, "pg.connect": StatusSkip}, remedy: map[string]string{"env.format": "remove `export`"}},
		{name: "env: quoted DSN", mutate: func(f *doctorFixture) { f.setEnvFile(`MEMORY_PG_DSN="`+testDSN+"\"\n", 0o600) },
			want: map[string]Status{"env.format": StatusFail}, remedy: map[string]string{"env.format": "remove the quotes"}},
		{name: "env: unencoded base64 password", mutate: func(f *doctorFixture) {
			f.setEnvFile("MEMORY_PG_DSN=postgresql://claude_memory:ab/cd+ef==@db:5432/claude_memory\n", 0o600)
		}, want: map[string]Status{"env.format": StatusFail, "pg.connect": StatusSkip}, remedy: map[string]string{"env.format": "URL-encode"}},
		{name: "env: invalid number and a stray line", mutate: func(f *doctorFixture) {
			f.appendEnv("MEMORY_HOOK_TIMEOUT=fast\njunk line\n")
		}, want: map[string]Status{"env.format": StatusWarn, "pg.connect": StatusPass},
			detail: map[string]string{"env.format": "MEMORY_HOOK_TIMEOUT is not a valid duration"}},
		{name: "env: no DSN key", mutate: func(f *doctorFixture) { f.setEnvFile("MEMORY_OLLAMA_URL=http://127.0.0.1:11434\n", 0o600) },
			want: map[string]Status{"env.format": StatusFail, "pg.connect": StatusSkip, "pg.latency": StatusSkip}},

		// pg.*
		{name: "pg: auth", mutate: func(f *doctorFixture) {
			f.db.status, f.db.err = DBStatus{ErrorClass: DBErrAuth}, errors.New("connect: password authentication failed")
		}, want: map[string]Status{"pg.connect": StatusFail, "pg.latency": StatusSkip, "pg.vector": StatusSkip, "pg.schema": StatusSkip},
			detail: map[string]string{"pg.connect": "authentication failed"}},
		{name: "pg: no database", mutate: func(f *doctorFixture) { f.db.status, f.db.err = DBStatus{ErrorClass: DBErrNoDB}, errors.New("3D000") },
			want: map[string]Status{"pg.connect": StatusFail}, detail: map[string]string{"pg.connect": "does not exist"}},
		{name: "pg: pg_hba", mutate: func(f *doctorFixture) {
			f.db.status, f.db.err = DBStatus{ErrorClass: DBErrHBA}, errors.New("no pg_hba.conf entry")
		},
			want: map[string]Status{"pg.connect": StatusFail}, remedy: map[string]string{"pg.connect": "pg_hba.conf"}},
		{name: "pg: unreachable", mutate: func(f *doctorFixture) {
			f.db.status, f.db.err = DBStatus{ErrorClass: DBErrUnreachable}, errors.New("dial tcp: i/o timeout")
		},
			want: map[string]Status{"pg.connect": StatusFail}, remedy: map[string]string{"pg.connect": "tailscale status"}},
		{name: "pg: dsn", mutate: func(f *doctorFixture) {
			f.db.status, f.db.err = DBStatus{ErrorClass: DBErrDSN}, errors.New("parse dsn")
		},
			want: map[string]Status{"pg.connect": StatusFail}, remedy: map[string]string{"pg.connect": "URL-encode"}},
		{name: "pg: slow", mutate: func(f *doctorFixture) { f.db.status.RTT = 150 * time.Millisecond },
			want: map[string]Status{"pg.latency": StatusWarn}, detail: map[string]string{"pg.latency": "150 ms"}},
		{name: "pg: no vector, no schema", mutate: func(f *doctorFixture) {
			f.db.status.VectorVersion = ""
			f.db.status.Migrations = []MigrationStatus{{ID: "0001", Missing: []string{"extension vector", "records", "records.id"}}, {ID: "0002", Missing: []string{"records.namespace"}}}
		}, want: map[string]Status{"pg.vector": StatusFail, "pg.schema": StatusFail}, remedy: map[string]string{"pg.schema": "claude-memory migrate"}},
		{name: "pg: schema behind", mutate: func(f *doctorFixture) {
			f.db.status.Migrations[1].Missing = []string{"records.namespace", "index idx_records_namespace_repo"}
		}, want: map[string]Status{"pg.schema": StatusWarn}, detail: map[string]string{"pg.schema": "0002"}, remedy: map[string]string{"pg.schema": "claude-memory migrate"}},
		{name: "pg: newer schema", mutate: func(f *doctorFixture) { f.db.status.Unknown = []string{"records.future_col"} },
			want: map[string]Status{"pg.schema": StatusInfo}},
		{name: "pg: catalog unreadable", mutate: func(f *doctorFixture) {
			f.db.status.Migrations, f.db.status.VectorVersion = nil, ""
			f.db.err = errors.New("read schema: permission denied")
		}, want: map[string]Status{"pg.connect": StatusPass, "pg.vector": StatusFail, "pg.schema": StatusFail}},

		// ollama.*
		{name: "ollama: down", mutate: func(f *doctorFixture) { f.ollama.versionErr = errors.New("connection refused") },
			want:   map[string]Status{"ollama.reachable": StatusFail, "ollama.model": StatusSkip, "ollama.embed": StatusSkip},
			remedy: map[string]string{"ollama.reachable": "brew services start ollama"}},
		{name: "ollama: model missing", mutate: func(f *doctorFixture) { f.ollama.hasModel = false },
			want: map[string]Status{"ollama.model": StatusFail, "ollama.embed": StatusSkip}, remedy: map[string]string{"ollama.model": "ollama pull bge-m3"}},
		{name: "ollama: wrong dims", mutate: func(f *doctorFixture) { f.ollama.dims = 768 },
			want: map[string]Status{"ollama.embed": StatusFail}, detail: map[string]string{"ollama.embed": "returns 768 dims, schema needs 1024"}},
		{name: "ollama: slow embed", mutate: func(f *doctorFixture) { f.ollama.latency = 1800 * time.Millisecond },
			want: map[string]Status{"ollama.embed": StatusWarn}, detail: map[string]string{"ollama.embed": "800 ms"}},
		{name: "ollama: remote URL and model from env file", mutate: func(f *doctorFixture) {
			f.appendEnv("MEMORY_OLLAMA_URL=http://gpu-box:11434\nMEMORY_OLLAMA_MODEL=other-model\n")
			f.setEnvFile("MEMORY_PG_DSN="+testDSN+"\nMEMORY_OLLAMA_URL=http://gpu-box:11434\nMEMORY_OLLAMA_MODEL=other-model\n", 0o600)
		}, want: map[string]Status{"ollama.reachable": StatusPass}, detail: map[string]string{"ollama.reachable": "remote", "ollama.model": "other-model"}},

		// tools.*
		{name: "tools: none on PATH", mutate: func(f *doctorFixture) {
			f.runner.paths = map[string]string{BinaryName: f.p.InstalledBinary()}
			f.appendEnv("MEMORY_PR_INGEST_REPOS=/work\n")
		}, want: map[string]Status{"tools.git": StatusWarn, "tools.claude": StatusWarn, "tools.az": StatusWarn}},
		{name: "tools: az present with PR repos", mutate: func(f *doctorFixture) {
			f.appendEnv("MEMORY_PR_INGEST_REPOS=/work\n")
			f.runner.SetPath("az", "/usr/bin/az")
		}, want: map[string]Status{"tools.az": StatusPass}},

		{name: "tools: gh absent", mutate: func(f *doctorFixture) {},
			want: map[string]Status{"tools.gh": StatusInfo, "tools.glab": StatusInfo}, detail: map[string]string{"tools.gh": "not on PATH (needed only for GitHub/GitLab repos)"}},
		{name: "tools: gh logged in", mutate: func(f *doctorFixture) {
			f.runner.SetPath("gh", "/usr/bin/gh")
			f.runner.Script(ArgvPrefix("gh", "auth", "status", "--hostname", "github.com"), Result{})
		}, want: map[string]Status{"tools.gh": StatusInfo}, detail: map[string]string{"tools.gh": "logged in"}},
		{name: "tools: gh not logged in", mutate: func(f *doctorFixture) {
			f.runner.SetPath("gh", "/usr/bin/gh")
			f.runner.Script(ArgvPrefix("gh", "auth", "status"), Result{ExitCode: 1, Stderr: []byte("You are not logged into any GitHub hosts.")})
		}, want: map[string]Status{"tools.gh": StatusInfo}, detail: map[string]string{"tools.gh": "not logged in"}, remedy: map[string]string{"tools.gh": "gh auth login"}},
		{name: "tools: gh offline", mutate: func(f *doctorFixture) {
			f.runner.SetPath("gh", "/usr/bin/gh")
			f.runner.Script(ArgvPrefix("gh", "auth", "status"), Result{ExitCode: 1, Stderr: []byte("error connecting to api.github.com")})
		}, want: map[string]Status{"tools.gh": StatusInfo}, detail: map[string]string{"tools.gh": "could not check"}},
		{name: "tools: gh timeout never fails the row", mutate: func(f *doctorFixture) {
			f.runner.SetPath("gh", "/usr/bin/gh")
			f.runner.ScriptError(ArgvPrefix("gh", "auth", "status"), context.DeadlineExceeded)
		}, want: map[string]Status{"tools.gh": StatusInfo}, detail: map[string]string{"tools.gh": "could not check"}},
		{name: "tools: glab logged in", mutate: func(f *doctorFixture) {
			f.runner.SetPath("glab", "/usr/bin/glab")
			f.runner.Script(ArgvPrefix("glab", "auth", "status", "--hostname=gitlab.com"), Result{})
		}, want: map[string]Status{"tools.glab": StatusInfo}, detail: map[string]string{"tools.glab": "logged in"}},

		// mcp.registered
		{name: "mcp: no .claude.json", mutate: func(f *doctorFixture) { rm(f, f.p.ClaudeJSON) },
			want: map[string]Status{"mcp.registered": StatusFail}, remedy: map[string]string{"mcp.registered": "claude mcp add --scope user claude-memory"}},
		{name: "mcp: no server key", mutate: func(f *doctorFixture) { writeFile(f.t, f.p.ClaudeJSON, []byte(`{"mcpServers":{}}`), 0o600) },
			want: map[string]Status{"mcp.registered": StatusFail}},
		{name: "mcp: other command", mutate: func(f *doctorFixture) {
			other := filepath.Join(f.p.Home, "src", "claude-memory", "bin", "claude-memory")
			writeFile(f.t, other, []byte("x"), 0o755)
			writeFile(f.t, f.p.ClaudeJSON, []byte(fmt.Sprintf(`{"mcpServers":{"claude-memory":{"type":"stdio","command":%q,"args":["serve"],"env":{}}}}`, other)), 0o600)
		}, want: map[string]Status{"mcp.registered": StatusWarn}, remedy: map[string]string{"mcp.registered": "claude mcp remove --scope user"}},
		{name: "mcp: command missing on disk", mutate: func(f *doctorFixture) { rm(f, f.p.InstalledBinary()) },
			want: map[string]Status{"mcp.registered": StatusFail, "hooks.scripts": StatusFail}, detail: map[string]string{"mcp.registered": "does not exist"}},
		{name: "mcp: local-scope shadow", mutate: func(f *doctorFixture) {
			reg := fmt.Sprintf(`{"mcpServers":{"claude-memory":{"type":"stdio","command":%q,"args":["serve"],"env":{}}},"projects":{"/w/p":{"mcpServers":{"claude-memory":{"command":"x"}}}}}`, f.p.InstalledBinary())
			writeFile(f.t, f.p.ClaudeJSON, []byte(reg), 0o600)
		}, want: map[string]Status{"mcp.registered": StatusWarn}, detail: map[string]string{"mcp.registered": "/w/p"}},
		{name: "mcp: unparseable .claude.json", mutate: func(f *doctorFixture) { writeFile(f.t, f.p.ClaudeJSON, []byte(`{"mcpServers":`), 0o600) },
			want: map[string]Status{"mcp.registered": StatusWarn}},

		// hooks.scripts
		{name: "hooks.scripts: missing", mutate: func(f *doctorFixture) { rm(f, f.p.HookScriptsDir()) },
			want: map[string]Status{"hooks.scripts": StatusFail, "hooks.settings": StatusFail}},
		{name: "hooks.scripts: not executable", mutate: func(f *doctorFixture) {
			_ = os.Chmod(filepath.Join(f.p.HookScriptsDir(), HookScriptSessionEnd), 0o644)
		}, want: map[string]Status{"hooks.scripts": StatusFail}, remedy: map[string]string{"hooks.scripts": "chmod 755"}},
		{name: "hooks.scripts: edited", mutate: func(f *doctorFixture) {
			p := filepath.Join(f.p.HookScriptsDir(), HookScriptSessionEnd)
			b, _ := os.ReadFile(p)
			writeFile(f.t, p, append(b, "# local tweak\n"...), 0o755)
		}, want: map[string]Status{"hooks.scripts": StatusWarn}, detail: map[string]string{"hooks.scripts": "modified"}},
		{name: "hooks.scripts: recorded older version", mutate: func(f *doctorFixture) {
			p := filepath.Join(f.p.HookScriptsDir(), HookScriptSessionEnd)
			old := []byte("#!/usr/bin/env bash\n# old version\n")
			writeFile(f.t, p, old, 0o755)
			writeManifest(f, Artifact{Step: "hooks.scripts", Kind: KindFile, Path: p, SHA256: sha256Hex(old), Version: "v0"})
		}, want: map[string]Status{"hooks.scripts": StatusWarn, "manifest": StatusPass}, detail: map[string]string{"hooks.scripts": "outdated"}},

		// hooks.settings
		{name: "hooks.settings: no file", mutate: func(f *doctorFixture) { rm(f, f.p.SettingsJSON()) },
			want: map[string]Status{"hooks.settings": StatusFail}, remedy: map[string]string{"hooks.settings": "settings.snippet.json"}},
		{name: "hooks.settings: comments", mutate: func(f *doctorFixture) { writeFile(f.t, f.p.SettingsJSON(), []byte("{ // hi\n}\n"), 0o644) },
			want: map[string]Status{"hooks.settings": StatusFail}, detail: map[string]string{"hooks.settings": "refusing"}},
		{name: "hooks.settings: one event missing", mutate: func(f *doctorFixture) {
			writeFile(f.t, f.p.SettingsJSON(), settingsWith(DesiredHooks(f.p.HookScriptsDir())[0]), 0o644)
		}, want: map[string]Status{"hooks.settings": StatusFail}, detail: map[string]string{"hooks.settings": "no SessionEnd entry"}},
		{name: "hooks.settings: legacy HOME form", mutate: func(f *doctorFixture) {
			b := strings.ReplaceAll(string(asset(f.t, integration.SettingsSnippet)), "$HOME", "$HOME")
			writeFile(f.t, f.p.SettingsJSON(), []byte(b), 0o644)
		}, want: map[string]Status{"hooks.settings": StatusWarn}, detail: map[string]string{"hooks.settings": "legacy $HOME"}},
		{name: "hooks.settings: duplicates", mutate: func(f *doctorFixture) {
			d := DesiredHooks(f.p.HookScriptsDir())
			writeFile(f.t, f.p.SettingsJSON(), settingsWith(d[0], d[0], d[1]), 0o644)
		}, want: map[string]Status{"hooks.settings": StatusWarn}, detail: map[string]string{"hooks.settings": "2 claude-memory entries"}},
		{name: "hooks.settings: duplicate in settings.local.json (AC-70)", mutate: func(f *doctorFixture) {
			writeFile(f.t, filepath.Join(f.p.ClaudeDir, "settings.local.json"), settingsWith(DesiredHooks(f.p.HookScriptsDir())[0]), 0o644)
		}, want: map[string]Status{"hooks.settings": StatusWarn}, detail: map[string]string{"hooks.settings": "settings.local.json"}},
		{name: "hooks.settings: duplicate in project settings (AC-70)", mutate: func(f *doctorFixture) {
			writeFile(f.t, filepath.Join(f.p.Cwd, ".claude", "settings.json"), settingsWith(DesiredHooks(f.p.HookScriptsDir())[1]), 0o644)
		}, want: map[string]Status{"hooks.settings": StatusWarn}, detail: map[string]string{"hooks.settings": "fire twice"}},
		{name: "hooks.settings: command path missing", mutate: func(f *doctorFixture) {
			writeFile(f.t, f.p.SettingsJSON(), settingsWith(DesiredHooks(filepath.Join(f.p.Home, "elsewhere", "claude-memory"))...), 0o644)
		}, want: map[string]Status{"hooks.settings": StatusFail}, detail: map[string]string{"hooks.settings": "which does not exist"}},

		// skills
		{name: "skills: not installed", mutate: func(f *doctorFixture) { rm(f, f.p.SkillsDir()) },
			want: map[string]Status{"skills": StatusWarn}, detail: map[string]string{"skills": "not installed"}},
		{name: "skills: edited", mutate: func(f *doctorFixture) {
			writeFile(f.t, filepath.Join(f.p.SkillsDir(), "remember", "SKILL.md"), []byte("mine\n"), 0o644)
		}, want: map[string]Status{"skills": StatusWarn}, detail: map[string]string{"skills": "remember: SKILL.md modified"}},

		// claude-md
		{name: "claude-md: no file", mutate: func(f *doctorFixture) { rm(f, filepath.Join(f.p.ClaudeDir, "CLAUDE.md")) },
			want: map[string]Status{"claude-md": StatusInfo}},
		{name: "claude-md: hand-pasted", mutate: func(f *doctorFixture) {
			writeFile(f.t, filepath.Join(f.p.ClaudeDir, "CLAUDE.md"), []byte("## Shared semantic memory (`claude-memory`)\ntext\n"), 0o644)
		}, want: map[string]Status{"claude-md": StatusInfo}, detail: map[string]string{"claude-md": "hand-pasted"}},
		{name: "claude-md: unbalanced markers", mutate: func(f *doctorFixture) {
			writeFile(f.t, filepath.Join(f.p.ClaudeDir, "CLAUDE.md"), []byte(MDBeginMarker+"\n"), 0o644)
		}, want: map[string]Status{"claude-md": StatusInfo}, detail: map[string]string{"claude-md": "unbalanced"}},

		// namespaces
		{name: "namespaces: absent", mutate: func(f *doctorFixture) { rm(f, f.p.NamespacesFile()) },
			want: map[string]Status{"namespaces": StatusInfo}, detail: map[string]string{"namespaces": "→ global (fallback)"}},
		{name: "namespaces: broken", mutate: func(f *doctorFixture) { writeFile(f.t, f.p.NamespacesFile(), []byte("namespaces: [\n"), 0o600) },
			want: map[string]Status{"namespaces": StatusWarn}},
		{name: "namespaces: invalid name", mutate: func(f *doctorFixture) {
			writeFile(f.t, f.p.NamespacesFile(), []byte("namespaces:\n  - namespace: Bad Name\n    paths: [x]\n"), 0o600)
		}, want: map[string]Status{"namespaces": StatusWarn}},
		{name: "namespaces: pr_ingest problem", mutate: func(f *doctorFixture) {
			writeFile(f.t, f.p.NamespacesFile(), []byte("namespaces:\n  - namespace: w\n    paths: [x]\n    pr_ingest: {provider: bitbucket}\n"), 0o600)
		}, want: map[string]Status{"namespaces": StatusWarn}, detail: map[string]string{"namespaces": "w: pr_ingest.provider"}},
		{name: "namespaces: cwd matches a rule", mutate: func(f *doctorFixture) {
			writeFile(f.t, f.p.NamespacesFile(), []byte(fmt.Sprintf("namespaces:\n  - namespace: proj\n    paths: [%q]\n", f.p.Cwd)), 0o600)
		}, want: map[string]Status{"namespaces": StatusPass}, detail: map[string]string{"namespaces": "→ proj (rule"}},
		{name: "namespaces: invalid MEMORY_NAMESPACE", mutate: func(f *doctorFixture) { f.env["MEMORY_NAMESPACE"] = "Not Valid" },
			want: map[string]Status{"namespaces": StatusWarn}},

		// jobs
		{name: "jobs: darwin legacy run-with-env plist", mutate: func(f *doctorFixture) {
			f.darwin(false)
			legacy := strings.ReplaceAll(string(readTestdata(f.t, "testdata/fixtures/launchd/legacy-cleanup.plist")), "__HOME__", f.p.Home)
			writeFile(f.t, filepath.Join(f.p.LaunchAgentsDir, LaunchdLabelPrefix+JobCleanup+".plist"), []byte(legacy), 0o644)
		}, want: map[string]Status{"jobs": StatusWarn}, detail: map[string]string{"jobs": "run-with-env.sh"}, remedy: map[string]string{"jobs": "claude-memory install --upgrade"}},
		{name: "jobs: darwin cleanup missing", mutate: func(f *doctorFixture) {
			f.darwin(false)
			rm(f, f.p.LaunchAgentsDir)
		}, want: map[string]Status{"jobs": StatusWarn}, detail: map[string]string{"jobs": "cleanup: not installed"}},
		{name: "jobs: darwin not loaded", mutate: func(f *doctorFixture) {
			f.darwin(false)
			notFound := readTestdata(f.t, "testdata/fixtures/launchd/launchctl-print-not-found.txt")
			f.runner.Handler = func(c Cmd) (Result, bool) {
				return Result{ExitCode: 113, Stderr: notFound}, len(c.Argv) > 1 && c.Argv[1] == "print"
			}
		}, want: map[string]Status{"jobs": StatusWarn}, detail: map[string]string{"jobs": "not loaded"}, remedy: map[string]string{"jobs": "claude-memory install --upgrade"}},
		{name: "jobs: darwin rendered plists are ok", mutate: func(f *doctorFixture) { f.darwin(true) },
			want: map[string]Status{"jobs": StatusPass}},
		{name: "jobs: darwin hand-written plist differs from the rendering", mutate: func(f *doctorFixture) {
			f.darwin(false)
			plist := strings.ReplaceAll(string(readTestdata(f.t, "testdata/fixtures/launchd/direct-cleanup.plist")), "__HOME__", f.p.Home)
			writeFile(f.t, filepath.Join(f.p.LaunchAgentsDir, LaunchdLabelPrefix+JobCleanup+".plist"), []byte(plist), 0o644)
		}, want: map[string]Status{"jobs": StatusWarn}, detail: map[string]string{"jobs": "differs from the plist this version renders"},
			remedy: map[string]string{"jobs": "(interactive) and confirm the overwrite"}},
		{name: "jobs: darwin recorded unedited plist differs from the rendering", mutate: func(f *doctorFixture) {
			f.darwin(false)
			path := filepath.Join(f.p.LaunchAgentsDir, LaunchdLabelPrefix+JobCleanup+".plist")
			old, _ := os.ReadFile(path)
			old = bytes.Replace(old, []byte("<integer>15</integer>"), []byte("<integer>45</integer>"), 1)
			writeFile(f.t, path, old, 0o644)
			writeManifest(f, Artifact{Step: "jobs", Kind: KindLaunchd, Path: path, Identity: LaunchdLabelPrefix + JobCleanup, SHA256: sha256Hex(old), Version: "v0"})
		}, want: map[string]Status{"jobs": StatusWarn}, detail: map[string]string{"jobs": "written by install, unedited"},
			remedy: map[string]string{"jobs": "claude-memory install --upgrade"}},
		{name: "jobs: unsupported OS", mutate: func(f *doctorFixture) { f.plat = PlatformInfo{OS: "windows", Arch: "amd64"} },
			want: map[string]Status{"jobs": StatusInfo}},

		// dirs.state
		{name: "dirs.state: absent", mutate: func(f *doctorFixture) { rm(f, f.p.StateDir) },
			want: map[string]Status{"dirs.state": StatusInfo}},
		{name: "dirs.state: leftover bootstrap.sql", mutate: func(f *doctorFixture) { writeFile(f.t, f.p.Bootstrap(), []byte("\\set app_pw x\n"), 0o600) },
			want: map[string]Status{"dirs.state": StatusInfo}, detail: map[string]string{"dirs.state": "holds a password"}},
		{name: "dirs.state: read-only", mutate: func(f *doctorFixture) {
			_ = os.Chmod(f.p.StateDir, 0o500)
			f.t.Cleanup(func() { _ = os.Chmod(f.p.StateDir, 0o700) })
		}, want: map[string]Status{"dirs.state": StatusWarn}},
		{name: "dirs.state: a file", mutate: func(f *doctorFixture) { rm(f, f.p.StateDir); writeFile(f.t, f.p.StateDir, nil, 0o600) },
			want: map[string]Status{"dirs.state": StatusWarn}},

		// manifest
		{name: "manifest: corrupt", mutate: func(f *doctorFixture) { writeFile(f.t, f.p.Manifest(), []byte("{nope"), 0o600) },
			want: map[string]Status{"manifest": StatusWarn}},
		{name: "manifest: artifact missing and other config dir", mutate: func(f *doctorFixture) {
			writeManifest(f, Artifact{Step: "skills", Kind: KindFile, Path: filepath.Join(f.p.SkillsDir(), "gone", "SKILL.md"), SHA256: "00", Version: "v1"})
			m, _ := LoadManifest(f.fs, f.p)
			m.Manifest.ClaudeConfigDir = "/elsewhere/.claude"
			b, _ := MarshalManifest(m.Manifest)
			writeFile(f.t, f.p.Manifest(), b, 0o600)
		}, want: map[string]Status{"manifest": StatusWarn}, detail: map[string]string{"manifest": "gone"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newDoctorFixture(t)
			c.mutate(f)
			r := f.run()
			for id, want := range c.want {
				got := mustResult(t, r, id)
				if got.Status != want {
					t.Errorf("%s: %s (%s), want %s", id, got.Status, got.Detail, want)
				}
				if got.Status != StatusPass && got.Status != StatusSkip && got.Status != StatusInfo && got.Remedy == "" {
					t.Errorf("%s: %s without a remedy (AC-58)", id, got.Status)
				}
			}
			for id, sub := range c.detail {
				if got := mustResult(t, r, id); !strings.Contains(got.Detail, sub) {
					t.Errorf("%s detail %q does not contain %q", id, got.Detail, sub)
				}
			}
			for id, sub := range c.remedy {
				if got := mustResult(t, r, id); !strings.Contains(got.Remedy, sub) {
					t.Errorf("%s remedy %q does not contain %q", id, got.Remedy, sub)
				}
			}
		})
	}
}

func writeManifest(f *doctorFixture, arts ...Artifact) {
	f.t.Helper()
	m := &Manifest{Schema: ManifestSchema, BinaryVersion: "v1", Platform: ManifestPlatform{OS: "linux", Arch: "amd64"},
		Topology: "remote", JobsBackend: JobsNone, ClaudeConfigDir: f.p.ClaudeDir,
		InstalledAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Artifacts: arts}
	b, err := MarshalManifest(m)
	if err != nil {
		f.t.Fatal(err)
	}
	writeFile(f.t, f.p.Manifest(), b, 0o600)
}

// TestDoctorTimeouts is AC-59: hanging endpoints are bounded by --timeout
// per check and --deadline overall; dependents of a timed-out check skip.
func TestDoctorTimeouts(t *testing.T) {
	t.Parallel()
	t.Run("per-check timeout", func(t *testing.T) {
		t.Parallel()
		f := newDoctorFixture(t)
		f.db.hang, f.ollama.hang = true, true
		start := time.Now()
		r := RunDoctor(context.Background(), f.deps(), DoctorOptions{Timeout: 300 * time.Millisecond, Deadline: 5 * time.Second})
		if el := time.Since(start); el > 3*time.Second {
			t.Errorf("took %s; the probes run concurrently, so the bound is one timeout", el)
		}
		for id, want := range map[string]Status{"pg.connect": StatusFail, "pg.schema": StatusSkip, "ollama.reachable": StatusFail, "ollama.embed": StatusSkip, "skills": StatusPass} {
			if got := mustResult(t, r, id); got.Status != want {
				t.Errorf("%s: %s (%s), want %s", id, got.Status, got.Detail, want)
			}
		}
		if got := mustResult(t, r, "pg.connect"); !strings.Contains(got.Detail, "timed out after 300ms") {
			t.Errorf("pg.connect detail %q", got.Detail)
		}
	})
	t.Run("deadline with a prober that ignores its context", func(t *testing.T) {
		t.Parallel()
		f := newDoctorFixture(t)
		f.db.stuck = make(chan struct{})
		t.Cleanup(func() { close(f.db.stuck) })
		start := time.Now()
		r := RunDoctor(context.Background(), f.deps(), DoctorOptions{Timeout: 5 * time.Second, Deadline: 100 * time.Millisecond})
		if el := time.Since(start); el > time.Second {
			t.Errorf("took %s, want the 100ms deadline", el)
		}
		got := mustResult(t, r, "pg.connect")
		if got.Status != StatusFail || !strings.Contains(got.Detail, "--deadline") {
			t.Errorf("pg.connect: %s %q", got.Status, got.Detail)
		}
		if got := mustResult(t, r, "pg.latency"); got.Status != StatusFail && got.Status != StatusSkip {
			t.Errorf("pg.latency: %s", got.Status)
		}
		if r.OK(false) {
			t.Error("a timed-out run is OK")
		}
	})
}

func TestRunChecksScheduling(t *testing.T) {
	t.Parallel()
	clk := NewFakeClock(time.Unix(0, 0))
	var mu sync.Mutex
	var order []string
	mark := func(id string, st Status) func(context.Context) (Status, string, string) {
		return func(context.Context) (Status, string, string) {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			return st, id + " ran", "fix " + id
		}
	}
	checks := []Check{
		{ID: "a", Run: mark("a", StatusFail)},
		{ID: "b", Requires: []string{"a"}, Run: mark("b", StatusPass)},
		{ID: "c", Requires: []string{"b"}, Run: mark("c", StatusPass)},
		{ID: "d", Run: mark("d", StatusWarn)},
		{ID: "e", Requires: []string{"d"}, Run: mark("e", StatusPass)},
		{ID: "f", Run: func(context.Context) (Status, string, string) { panic("boom") }},
		{ID: "g", Requires: []string{"zzz"}, Run: mark("g", StatusPass)},
		{ID: "h", Run: func(context.Context) (Status, string, string) { return "", "", "" }},
	}
	r := RunChecks(context.Background(), checks, clk, DoctorOptions{})
	want := map[string]Status{"a": StatusFail, "b": StatusSkip, "c": StatusSkip, "d": StatusWarn, "e": StatusPass, "f": StatusFail, "g": StatusFail, "h": StatusFail}
	var ids []string
	for _, c := range r.Checks {
		ids = append(ids, c.ID)
		if c.Status != want[c.ID] {
			t.Errorf("%s: %s (%s), want %s", c.ID, c.Status, c.Detail, want[c.ID])
		}
	}
	if !slices.Equal(ids, []string{"a", "b", "c", "d", "e", "f", "g", "h"}) {
		t.Errorf("report order %v", ids)
	}
	if b, _ := r.Result("b"); b.Detail != "because a failed" {
		t.Errorf("b detail %q", b.Detail)
	}
	if c, _ := r.Result("c"); c.Detail != "because b was skipped" {
		t.Errorf("c detail %q", c.Detail)
	}
	if f, _ := r.Result("f"); !strings.Contains(f.Detail, "boom") {
		t.Errorf("panic not reported: %q", f.Detail)
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.Contains(order, "b") || slices.Contains(order, "c") || slices.Contains(order, "g") {
		t.Errorf("a dependent of a failed check ran: %v", order)
	}
	if r.Summary != (DoctorSummary{Pass: 1, Fail: 4, Warn: 1, Skip: 2}) {
		t.Errorf("summary %+v", r.Summary)
	}
	if !r.OK(false) == true && r.Summary.Fail == 0 {
		t.Error("unreachable")
	}
}

func TestDoctorReportOK(t *testing.T) {
	t.Parallel()
	warn := DoctorReport{Summary: DoctorSummary{Pass: 3, Warn: 1}}
	if !warn.OK(false) || warn.OK(true) {
		t.Error("warn: OK(false) must be true, OK(true) false (--strict)")
	}
	fail := DoctorReport{Summary: DoctorSummary{Pass: 3, Fail: 1}}
	if fail.OK(false) {
		t.Error("fail must not be OK")
	}
	info := DoctorReport{Summary: DoctorSummary{Info: 2, Skip: 1}}
	if !info.OK(true) {
		t.Error("info/skip must be OK under --strict")
	}
}

// TestDoctorSentinelPassword is AC-30 (slice 1 half): the DSN password never
// appears in doctor output, in any encoding, even when a prober echoes it.
func TestDoctorSentinelPassword(t *testing.T) {
	t.Parallel()
	const sentinel = "S3ntinel-pw-$@:/x"
	enc := url.UserPassword("claude_memory", sentinel).String() // claude_memory:S3ntinel-pw-$%40%3A%2Fx
	dsn := "postgresql://" + enc + "@db.example:5432/claude_memory"
	forms := []string{sentinel, url.QueryEscape(sentinel), url.PathEscape(sentinel), strings.TrimPrefix(enc, "claude_memory:")}

	for _, tc := range []struct {
		name   string
		mutate func(f *doctorFixture)
	}{
		{"prober echoes the DSN", func(f *doctorFixture) {
			f.db.status = DBStatus{ErrorClass: DBErrOther}
			f.db.err = errors.New("connect: failed for " + dsn + " (password " + sentinel + ")")
			f.ollama.versionErr = errors.New("dial " + dsn)
		}},
		{"connected", func(f *doctorFixture) {}},
		{"DSN in the environment only", func(f *doctorFixture) {
			f.setEnvFile("MEMORY_OLLAMA_URL=http://127.0.0.1:11434\n", 0o600)
			f.env["MEMORY_PG_DSN"] = dsn
			f.db.status = DBStatus{ErrorClass: DBErrAuth}
			f.db.err = errors.New("auth failed: " + sentinel)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDoctorFixture(t)
			f.setEnvFile("MEMORY_PG_DSN="+dsn+"\n", 0o600)
			tc.mutate(f)
			r := f.run()
			var text, js bytes.Buffer
			meta := ReportMeta{Version: "test", Platform: f.plat, ConfigDir: f.p.ClaudeDir, Bin: f.p.Self}
			if err := WriteDoctorText(f.red.Writer(&text), r, meta, f.red); err != nil {
				t.Fatal(err)
			}
			if err := WriteDoctorJSON(f.red.Writer(&js), r, meta, f.red); err != nil {
				t.Fatal(err)
			}
			// Also the raw report, before the renderers' own redaction.
			raw := fmt.Sprintf("%+v", r)
			for _, out := range []string{text.String(), js.String(), raw} {
				for _, form := range forms {
					if strings.Contains(out, form) {
						t.Errorf("output contains the password form %q:\n%s", form, out)
					}
				}
			}
			if !json.Valid(js.Bytes()) {
				t.Errorf("JSON output invalid:\n%s", js.String())
			}
		})
	}
}

// TestDoctorNeverRunsRegisteredMCPCommand is AC-67 (unit half): with a
// .claude.json that registers a sentinel command, hook entries and job
// plists that point at sentinel scripts, doctor and every slice-1 Detect
// run without executing any of them or `claude mcp get|list`.
func TestDoctorNeverRunsRegisteredMCPCommand(t *testing.T) {
	t.Parallel()
	f := newDoctorFixture(t)
	f.darwin(true)
	// The fixture's command, moved under the test root (FakeFS refuses
	// anything outside it) and made a real executable that would leave a
	// marker if it ever ran.
	sentinelCmd := filepath.Join(f.p.Home, "ac67-sentinel-must-never-run")
	writeFile(t, sentinelCmd, []byte("#!/bin/sh\ntouch "+sentinelCmd+".ran\n"), 0o755)
	writeFile(t, f.p.ClaudeJSON, bytes.ReplaceAll(readTestdata(t, "testdata/fixtures/claude-json/sentinel.json"),
		[]byte("/tmp/ac67-sentinel-must-never-run"), []byte(sentinelCmd)), 0o600)
	sentinelHook := filepath.Join(f.p.Home, "sentinel-hook.sh")
	writeFile(t, sentinelHook, []byte("#!/bin/sh\ntouch /tmp/never\n"), 0o755)
	writeFile(t, f.p.SettingsJSON(), settingsWith(
		HookEntry{Event: EventUserPromptSubmit, Command: sentinelHook + " claude-memory hook", Timeout: 5},
		HookEntry{Event: EventSessionEnd, Command: "claude-memory extract", Timeout: 5}), 0o644)
	f.runner.SetPath("claude", "/usr/local/bin/claude")

	deny := func(c Cmd) bool {
		base := filepath.Base(c.Argv[0])
		return c.Argv[0] == sentinelCmd || c.Argv[0] == sentinelHook || base == "claude" || base == BinaryName ||
			slices.Contains(c.Argv, "serve")
	}
	f.runner.Deny(deny, "AC-67: the registered MCP command, hook commands, the claude CLI and claude-memory itself must never run")

	r := f.run()
	if got := mustResult(t, r, "mcp.registered"); got.Status != StatusWarn && got.Status != StatusFail {
		t.Errorf("mcp.registered: %s (%s); the sentinel registration differs from the installed binary", got.Status, got.Detail)
	}

	// Every slice-1 Detect, directly.
	reg, err := ReadMCPRegistration(f.fs, f.p)
	if err != nil || !reg.Present || reg.Server.Command != sentinelCmd {
		t.Fatalf("ReadMCPRegistration = %+v, %v", reg, err)
	}
	lj := LaunchdJobs{FS: f.fs, Runner: f.runner, Paths: f.p, Assets: integration.FS}
	for _, j := range DefaultJobSpecs(f.p, f.p.InstalledBinary(), ComputeJobPATH(f.runner)) {
		if _, _, err := lj.Detect(context.Background(), j, nil); err != nil {
			t.Errorf("Detect %s: %v", j.Name, err)
		}
	}
	if _, err := os.Stat(sentinelCmd + ".ran"); err == nil {
		t.Error("the sentinel MCP command ran")
	}
	for _, c := range f.runner.Calls() {
		if deny(c) || deniedCall(c) != "" {
			t.Errorf("forbidden command ran: %q", c.Argv)
		}
		if !slices.Equal(c.Argv[:2], []string{"launchctl", "print"}) {
			t.Errorf("unexpected command %q (doctor runs only `launchctl print`)", c.Argv)
		}
	}
}

// ---- golden output (AC-60) -------------------------------------------------

// normalizeRoot replaces the per-test temp root with a fixed token.
func normalizeRoot(b []byte, root string) []byte {
	return bytes.ReplaceAll(b, []byte(root), []byte("$ROOT"))
}

// goldenDetailChecks keep their `detail` in the goldens: their text is the
// part of the output users act on (the env-file findings, the MCP registration
// and the manifest summary).
var goldenDetailChecks = []string{"env.file", "mcp.registered", "manifest"}

// stripGoldenDetails returns r with Detail cleared on every check outside
// goldenDetailChecks.
func stripGoldenDetails(r DoctorReport) DoctorReport {
	out := r
	out.Checks = slices.Clone(r.Checks)
	for i := range out.Checks {
		if !slices.Contains(goldenDetailChecks, out.Checks[i].ID) {
			out.Checks[i].Detail = ""
		}
	}
	return out
}

func TestDoctorGoldenJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(f *doctorFixture)
	}{
		{"healthy", func(*doctorFixture) {}},
		{"empty-home", func(f *doctorFixture) {
			if err := os.RemoveAll(f.p.Home); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(f.p.Home, 0o755); err != nil {
				t.Fatal(err)
			}
			f.ollama.versionErr = errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
			f.runner.paths = map[string]string{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDoctorFixture(t)
			tc.mutate(f)
			r := f.run()
			meta := ReportMeta{Version: "test", Platform: f.plat, ConfigDir: f.p.ClaudeDir, Bin: f.p.Self}
			var js, text bytes.Buffer
			if err := WriteDoctorJSON(&js, r, meta, f.red); err != nil {
				t.Fatal(err)
			}
			if err := WriteDoctorText(&text, r, meta, f.red); err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(f.p.Home)

			// The goldens pin status, remedy and the schema, not the free
			// text of `detail` (AC-60, plan WI-S2-0): only the checks in
			// goldenDetailChecks keep it.
			gr := stripGoldenDetails(r)
			var gjs, gtext bytes.Buffer
			if err := WriteDoctorJSON(&gjs, gr, meta, f.red); err != nil {
				t.Fatal(err)
			}
			if err := WriteDoctorText(&gtext, gr, meta, f.red); err != nil {
				t.Fatal(err)
			}
			checkGolden(t, "testdata/doctor/"+tc.name+".golden.json", normalizeRoot(gjs.Bytes(), root))
			checkGolden(t, "testdata/doctor/"+tc.name+".golden.txt", normalizeRoot(gtext.Bytes(), root))

			// Every AC-60 key is always present. This key-set assertion, run
			// on the unstripped report, is the schema contract: the goldens
			// no longer compare `detail`.
			var doc map[string]any
			if err := json.Unmarshal(js.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"schema", "version", "platform", "ok", "summary", "checks"} {
				if _, ok := doc[k]; !ok {
					t.Errorf("JSON lacks %q", k)
				}
			}
			for _, k := range []string{"os", "arch", "jobs", "config_dir", "bin"} {
				if _, ok := doc["platform"].(map[string]any)[k]; !ok {
					t.Errorf("JSON platform lacks %q", k)
				}
			}
			for _, c := range doc["checks"].([]any) {
				for _, k := range []string{"id", "title", "status", "detail", "remedy", "duration_ms"} {
					if _, ok := c.(map[string]any)[k]; !ok {
						t.Errorf("JSON check lacks %q: %v", k, c)
					}
				}
			}
			if strings.Contains(js.String(), "\x1b[") || strings.Contains(text.String(), "\x1b[") {
				t.Error("ANSI escape in output")
			}
		})
	}
}

// A file byte-equal to the raw embedded script (a manual install of this
// version) is ok in doctor, with the detail "manual install" (WI-S2-0).
func TestDoctorHookScriptsManualInstall(t *testing.T) {
	t.Parallel()
	f := newDoctorFixture(t)
	writeFile(t, filepath.Join(f.p.HookScriptsDir(), HookScriptUserPromptSubmit), asset(t, integration.HookUserPromptSubmit), 0o755)
	writeFile(t, filepath.Join(f.p.HookScriptsDir(), HookScriptSessionEnd), asset(t, integration.HookSessionEnd), 0o755)
	c := mustResult(t, f.run(), "hooks.scripts")
	if c.Status != StatusPass || !strings.Contains(c.Detail, "manual install") {
		t.Errorf("hooks.scripts = %s (%s), want pass with \"manual install\"", c.Status, c.Detail)
	}
}

// A script rendered for another binary path is not this version's script.
func TestDoctorHookScriptsOtherBinary(t *testing.T) {
	t.Parallel()
	f := newDoctorFixture(t)
	f.writeHookScripts(filepath.Join(f.p.Home, "elsewhere", "claude-memory"))
	writeFile(t, filepath.Join(f.p.Home, "elsewhere", "claude-memory"), []byte("#!/bin/false\n"), 0o755)
	c := mustResult(t, f.run(), "hooks.scripts")
	if c.Status != StatusWarn || !strings.Contains(c.Detail, "modified") {
		t.Errorf("hooks.scripts = %s (%s), want warn (differs from the rendered script)", c.Status, c.Detail)
	}
}

// TestDoctorStickyBinPath is H3/AC-35: after `install --bin-dir X` the
// manifest records the binary, and doctor (which has no --bin-dir) compares
// hooks, MCP and jobs against that path and checks X, not Paths.BinDir, for
// PATH membership.
func TestDoctorStickyBinPath(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, onPath bool) (*doctorFixture, DoctorReport) {
		f := newDoctorFixture(t)
		f.darwin(true)
		binDir := filepath.Join(f.p.Home, "opt", "x")
		bin := filepath.Join(binDir, BinaryName)
		writeFile(t, bin, []byte("#!/bin/false\n"), 0o755)
		if err := os.Remove(f.p.InstalledBinary()); err != nil {
			t.Fatal(err)
		}
		f.p.Self = bin
		f.runner.SetPath(BinaryName, bin)
		f.writeHookScripts(bin)
		writeFile(t, f.p.ClaudeJSON, []byte(fmt.Sprintf(`{"mcpServers":{"claude-memory":{"type":"stdio","command":%q,"args":["serve"],"env":{}}}}`, bin)), 0o600)
		for _, job := range []string{JobCleanup, JobIngestPR} {
			pl := filepath.Join(f.p.LaunchAgentsDir, LaunchdLabelPrefix+job+".plist")
			b, err := os.ReadFile(pl)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, pl, []byte(strings.ReplaceAll(string(b), f.p.InstalledBinary(), bin)), 0o644)
		}
		writeManifest(f, Artifact{Step: BinaryStepName, Kind: KindFile, Path: bin, Version: "v1"})
		f.env = Env{"PATH": "/usr/bin:/bin"}
		if onPath {
			f.env["PATH"] = binDir + ":/usr/bin:/bin"
		}
		return f, f.run()
	}
	t.Run("on PATH", func(t *testing.T) {
		t.Parallel()
		_, r := setup(t, true)
		for _, id := range []string{"binary.version", "mcp.registered", "hooks.scripts", "jobs"} {
			if c := mustResult(t, r, id); c.Status != StatusPass {
				t.Errorf("%s = %s (%s), want pass", id, c.Status, c.Detail)
			}
		}
	})
	t.Run("not on PATH", func(t *testing.T) {
		t.Parallel()
		f, r := setup(t, false)
		c := mustResult(t, r, "binary.version")
		if c.Status != StatusInfo || !strings.Contains(c.Detail, filepath.Join(f.p.Home, "opt", "x")+" is not on PATH") {
			t.Errorf("binary.version = %s (%s), want the not-on-PATH info for the recorded dir", c.Status, c.Detail)
		}
		for _, id := range []string{"mcp.registered", "hooks.scripts", "jobs"} {
			if c := mustResult(t, r, id); c.Status != StatusPass {
				t.Errorf("%s = %s (%s), want pass", id, c.Status, c.Detail)
			}
		}
	})
}
