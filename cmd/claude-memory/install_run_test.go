package main

import (
	"bytes"
	"context"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"claude-memory/integration"
	"claude-memory/internal/setup"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files under testdata/")

// checkGolden compares got with testdata/<name>, or rewrites it under -update.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create it)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch (run with -update to accept)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// ---- fakes ----------------------------------------------------------------

// countingFS is the read-only FS with a counter on every write-side call, so
// a dry run can prove that nothing even tried to write or take the lock.
type countingFS struct {
	readOnlyFS
	writes atomic.Int32
}

func (c *countingFS) WriteFileAtomic(p string, b []byte, m fs.FileMode) error {
	c.writes.Add(1)
	return c.readOnlyFS.WriteFileAtomic(p, b, m)
}
func (c *countingFS) MkdirAll(p string, m fs.FileMode) error {
	c.writes.Add(1)
	return c.readOnlyFS.MkdirAll(p, m)
}
func (c *countingFS) Remove(p string) error {
	c.writes.Add(1)
	return c.readOnlyFS.Remove(p)
}
func (c *countingFS) Chmod(p string, m fs.FileMode) error {
	c.writes.Add(1)
	return c.readOnlyFS.Chmod(p, m)
}
func (c *countingFS) Lock(p string) (func() error, error) {
	c.writes.Add(1)
	return c.readOnlyFS.Lock(p)
}

// countingRunner runs nothing: every tool is "found", every command succeeds
// with no output, and Mutating commands are counted.
type countingRunner struct {
	mutating atomic.Int32
	total    atomic.Int32
}

func (r *countingRunner) Run(_ context.Context, c setup.Cmd) (setup.Result, error) {
	r.total.Add(1)
	if c.Mutating {
		r.mutating.Add(1)
		return setup.Result{}, setup.ErrReadOnly
	}
	return setup.Result{}, nil
}
func (r *countingRunner) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

// fakeDB implements setup.DBProber: a reachable server whose schema is behind.
type fakeDB struct{ migrates atomic.Int32 }

func (*fakeDB) Probe(context.Context, string) (setup.DBStatus, error) {
	return setup.DBStatus{
		Connected: true, ServerVersion: "16.4", VectorVersion: "0.7.0", TLS: true,
		Migrations: []setup.MigrationStatus{{ID: "0001"}, {ID: "0002", Missing: []string{"records.namespace"}}},
	}, nil
}
func (*fakeDB) LocalServerEvidence(context.Context) (bool, string) { return false, "" }
func (d *fakeDB) Migrate(context.Context, string) error            { d.migrates.Add(1); return nil }

// fakeOllama implements setup.OllamaProber: up, with the model missing.
type fakeOllama struct{ pulls atomic.Int32 }

func (*fakeOllama) Version(context.Context, string) (string, error)        { return "0.5.1", nil }
func (*fakeOllama) HasModel(context.Context, string, string) (bool, error) { return false, nil }
func (*fakeOllama) EmbedDims(context.Context, string, string) (int, time.Duration, error) {
	return 1024, 12 * time.Millisecond, nil
}
func (o *fakeOllama) Pull(context.Context, string, string, func(done, total int64)) error {
	o.pulls.Add(1)
	return nil
}

type dryRunFixture struct {
	home   string
	fs     *countingFS
	runner *countingRunner
	db     *fakeDB
	ollama *fakeOllama
	run    installRun
	out    *bytes.Buffer
}

// newDryRunFixture builds an installRun over fakes with HOME in a temp dir.
// Self is a fixed path outside every ephemeral directory.
func newDryRunFixture(t *testing.T, args ...string) *dryRunFixture {
	t.Helper()
	home := t.TempDir()
	opts, err := parseInstallFlags(args, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}
	paths, err := buildPaths(getenv, opts.Inputs.BinDir, 501, "/opt/build/claude-memory", "/work/elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	red := setup.NewRedactor()
	env := setup.Env{"PATH": "/usr/bin:/bin"}
	opts.Inputs.Env = env
	f := &dryRunFixture{home: home, fs: &countingFS{}, runner: &countingRunner{}, db: &fakeDB{}, ollama: &fakeOllama{}, out: &bytes.Buffer{}}
	plat := setup.PlatformInfo{OS: setup.OSDarwin, OSVersion: "macOS 15.0", Arch: "arm64", PackageManagers: []string{"brew"},
		JobsBackend: setup.JobsLaunchd, JobsBackendReason: "launchd user agents (macOS)"}
	clock := fixedClock{time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	read := setup.ReadPorts{FS: f.fs, Runner: f.runner, DB: f.db, Ollama: f.ollama, Clock: clock,
		Paths: paths, Env: env, Platform: plat, Assets: integration.FS}
	write := setup.WritePorts{ReadPorts: read, FS: f.fs, Runner: f.runner, DB: f.db, Ollama: f.ollama}
	f.run = installRun{
		Opts: opts,
		Deps: setupDeps{Paths: paths, Env: env, Platform: plat, Redactor: red},
		Read: read, Write: write, Out: f.out,
	}
	return f
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// tree lists every entry below dir.
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err == nil && p != dir {
			out = append(out, p)
		}
		return nil
	})
	return out
}

const dryRunSentinel = "S3ntinel-pw-9f2"

var dryRunArgs = []string{"--dry-run", "--yes", "--topology", "remote",
	"--pg-dsn", "postgresql://claude_memory@db.example.com:5432/claude_memory?sslmode=require",
	"--ollama-url", "http://ollama.example.com:11434", "--namespace", "work=~/work/**"}

func (f *dryRunFixture) execute(t *testing.T) string {
	t.Helper()
	f.run.Opts.Inputs.PGPassword = dryRunSentinel
	if err := runInstall(context.Background(), f.run); err != nil {
		t.Fatalf("runInstall: %v\n%s", err, f.out)
	}
	return f.out.String()
}

// TestInstallDryRunGolden is AC-13: --dry-run prints the plan with diffs and
// the "nothing was changed" line, tries 0 writes (not even the lock) and runs
// 0 mutating commands, and touches no probe that changes state. HOME stays
// empty. The golden has HOME replaced by $HOME.
func TestInstallDryRunGolden(t *testing.T) {
	f := newDryRunFixture(t, dryRunArgs...)
	out := f.execute(t)

	if n := f.fs.writes.Load(); n != 0 {
		t.Errorf("%d write-side FS calls (including Lock) under --dry-run, want 0", n)
	}
	if n := f.runner.mutating.Load(); n != 0 {
		t.Errorf("%d mutating commands under --dry-run, want 0", n)
	}
	if f.db.migrates.Load() != 0 || f.ollama.pulls.Load() != 0 {
		t.Errorf("dry run migrated (%d) or pulled (%d)", f.db.migrates.Load(), f.ollama.pulls.Load())
	}
	if got := tree(t, f.home); len(got) != 0 {
		t.Errorf("HOME was written to: %v", got)
	}
	if strings.Contains(out, dryRunSentinel) {
		t.Errorf("the password reached the output:\n%s", out)
	}
	if !strings.Contains(out, "Dry run: nothing was changed.") {
		t.Errorf("missing the dry-run line:\n%s", out)
	}
	checkGolden(t, "install_dry_run.golden", []byte(strings.ReplaceAll(out, f.home, "$HOME")))
}

// TestInstallUpgradeIsYes is AC-52: --upgrade and --yes print the same plan.
func TestInstallUpgradeIsYes(t *testing.T) {
	render := func(flagName string) string {
		args := append([]string{flagName}, dryRunArgs[2:]...) // dryRunArgs[:2] = --dry-run --yes
		args = append([]string{"--dry-run"}, args...)
		f := newDryRunFixture(t, args...)
		return strings.ReplaceAll(f.execute(t), f.home, "$HOME")
	}
	yes, upgrade := render("--yes"), render("--upgrade")
	if yes != upgrade {
		t.Errorf("--upgrade differs from --yes\n--- yes ---\n%s\n--- upgrade ---\n%s", yes, upgrade)
	}
}

// TestInstallBinaryOutsideCheckout is AC-34 (S2) in-process: the cwd is not a
// checkout (Paths.Cwd is a made-up directory) and the plan still carries the
// embedded assets and installs the binary.
func TestInstallBinaryOutsideCheckout(t *testing.T) {
	f := newDryRunFixture(t, dryRunArgs...)
	out := f.execute(t)
	if !strings.Contains(out, "copy /opt/build/claude-memory") {
		t.Errorf("the plan does not install the binary:\n%s", out)
	}
}

// TestWritablePortsDryRun checks Design 20's second layer: under dry-run the
// production builder hands Apply the read-only adapters.
func TestWritablePortsDryRun(t *testing.T) {
	t.Parallel()
	d := setupDeps{FS: readOnlyFS{}, Runner: execRunner{readOnly: true}}
	for _, dry := range []bool{true, false} {
		wp := writablePorts(d, dry)
		_, werr := wp.FS.Lock(filepath.Join(t.TempDir(), "lock"))
		_, rerr := wp.Runner.Run(context.Background(), setup.Cmd{Argv: []string{"true"}, Mutating: true})
		if dry {
			if werr == nil || rerr == nil {
				t.Errorf("dry-run ports must refuse writes and mutating commands: %v, %v", werr, rerr)
			}
		} else if werr != nil || rerr != nil {
			t.Errorf("writable ports refused: %v, %v", werr, rerr)
		}
		if wp.ClaudeCLI != nil || wp.Jobs != nil || wp.ReadPorts.Jobs != nil {
			t.Errorf("2a leaves ClaudeCLI, Jobs and ReadPorts.Jobs nil: %+v", wp)
		}
	}
	rp := readOnlyPorts(setupDeps{FS: readOnlyFS{}})
	if rp.Jobs != nil {
		t.Error("ReadPorts.Jobs must be nil in 2a")
	}
}
