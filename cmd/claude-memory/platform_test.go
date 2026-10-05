package main

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/setup"
)

// mapFS is a read-only setup.FS over an in-memory file map (cmd tests cannot
// use internal/setup's test fakes).
type mapFS map[string]string

func (m mapFS) ReadFile(p string) ([]byte, error) {
	if s, ok := m[p]; ok {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("open %s: %w", p, fs.ErrNotExist)
}
func (m mapFS) Stat(p string) (fs.FileInfo, error) {
	if _, ok := m[p]; ok {
		return fakeFileInfo{name: filepath.Base(p)}, nil
	}
	return nil, fs.ErrNotExist
}
func (m mapFS) Lstat(p string) (fs.FileInfo, error)               { return nil, fs.ErrNotExist }
func (m mapFS) ReadDir(p string) ([]fs.DirEntry, error)           { return nil, fs.ErrNotExist }
func (m mapFS) EvalSymlinks(p string) (string, error)             { return p, nil }
func (m mapFS) Writable(string) bool                              { return false }
func (m mapFS) WriteFileAtomic(string, []byte, fs.FileMode) error { return setup.ErrDryRun }
func (m mapFS) MkdirAll(string, fs.FileMode) error                { return setup.ErrDryRun }
func (m mapFS) Remove(string) error                               { return setup.ErrDryRun }
func (m mapFS) Chmod(string, fs.FileMode) error                   { return setup.ErrDryRun }
func (m mapFS) Lock(string) (func() error, error)                 { return nil, setup.ErrDryRun }

type fakeFileInfo struct{ name string }

func (f fakeFileInfo) Name() string     { return f.name }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

// scriptRunner answers commands by exact argv and records every call; an
// unscripted command fails the test.
type scriptRunner struct {
	t       *testing.T
	answers map[string]setup.Result
	onPath  []string // names LookPath finds
	calls   [][]string
	envs    [][]string // Cmd.Env of each call
}

func (r *scriptRunner) Run(_ context.Context, c setup.Cmd) (setup.Result, error) {
	r.calls = append(r.calls, c.Argv)
	r.envs = append(r.envs, c.Env)
	if c.Mutating {
		r.t.Errorf("detectPlatform ran a mutating command %q", c.Argv)
	}
	if res, ok := r.answers[fmt.Sprint(c.Argv)+fmt.Sprint(c.Env)]; ok {
		return res, nil
	}
	if res, ok := r.answers[fmt.Sprint(c.Argv)]; ok {
		return res, nil
	}
	r.t.Errorf("unscripted command %q", c.Argv)
	return setup.Result{}, fmt.Errorf("unscripted %q", c.Argv)
}
func (r *scriptRunner) LookPath(name string) (string, error) {
	if slices.Contains(r.onPath, name) {
		return "/usr/bin/" + name, nil
	}
	return "", fs.ErrNotExist
}

func TestDetectPlatform(t *testing.T) {
	t.Parallel()
	swVers := fmt.Sprint([]string{"sw_vers", "-productVersion"})
	showEnv := fmt.Sprint([]string{"systemctl", "--user", "show-environment"})
	showEnvRetry := showEnv + fmt.Sprint([]string{"XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"})
	cases := []struct {
		name      string
		goos      string
		files     mapFS
		answers   map[string]setup.Result
		onPath    []string
		wantCalls int
		want      setup.PlatformInfo
	}{
		{
			name:    "macOS",
			goos:    "darwin",
			answers: map[string]setup.Result{swVers: {Stdout: []byte("15.1\n")}},
			want: setup.PlatformInfo{OS: "darwin", Arch: "arm64", OSVersion: "macOS 15.1",
				JobsBackend: setup.JobsLaunchd, JobsBackendReason: "launchd user agents (macOS)"},
		},
		{
			name:    "macOS, sw_vers fails",
			goos:    "darwin",
			answers: map[string]setup.Result{swVers: {ExitCode: 1}},
			want: setup.PlatformInfo{OS: "darwin", Arch: "arm64", OSVersion: "macOS",
				JobsBackend: setup.JobsLaunchd, JobsBackendReason: "launchd user agents (macOS)"},
		},
		{
			name: "Ubuntu with a user systemd and apt-get",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n",
				kernelReleasePath: "6.8.0-45-generic\n",
			},
			answers: map[string]setup.Result{showEnv: {}},
			onPath:  []string{"apt-get"},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Ubuntu 24.04.1 LTS",
				JobsBackend: setup.JobsSystemd, JobsBackendReason: "systemd user units (systemctl --user)",
				PackageManagers: []string{"apt-get"}},
			wantCalls: 1,
		},
		{
			name: "Fedora without PRETTY_NAME, dnf",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "NAME=Fedora Linux\nVERSION_ID=40\n",
				kernelReleasePath: "6.10.6-200.fc40.x86_64\n",
			},
			answers: map[string]setup.Result{showEnv: {}},
			onPath:  []string{"dnf"},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Fedora Linux 40",
				JobsBackend: setup.JobsSystemd, JobsBackendReason: "systemd user units (systemctl --user)",
				PackageManagers: []string{"dnf"}},
			wantCalls: 1,
		},
		{
			name: "ssh into a lingering user instance: no env, bus exists, retry works (AC-16)",
			goos: "linux",
			files: mapFS{
				osReleasePath:        "PRETTY_NAME=\"Debian GNU/Linux 12\"\n",
				kernelReleasePath:    "6.1.0\n",
				"/run/user/1000/bus": "",
			},
			answers: map[string]setup.Result{
				showEnv:      {ExitCode: 1},
				showEnvRetry: {},
			},
			onPath: []string{"apt-get", "brew"},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Debian GNU/Linux 12",
				JobsBackend:       setup.JobsSystemd,
				JobsBackendReason: "systemd user units via /run/user/1000/bus (session without its own bus)",
				SystemdEnv:        []string{"XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"},
				PackageManagers:   []string{"brew", "apt-get"}},
			wantCalls: 2,
		},
		{
			name: "no bus: backend none, no retry (AC-16)",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "PRETTY_NAME=\"Ubuntu 22.04\"\n",
				kernelReleasePath: "5.15.0\n",
			},
			answers: map[string]setup.Result{showEnv: {ExitCode: 1}},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Ubuntu 22.04",
				JobsBackend: setup.JobsNone, JobsBackendReason: "no systemd user instance"},
			wantCalls: 1,
		},
		{
			name: "bus exists but the retry fails too",
			goos: "linux",
			files: mapFS{
				osReleasePath:        "PRETTY_NAME=\"Ubuntu 22.04\"\n",
				kernelReleasePath:    "5.15.0\n",
				"/run/user/1000/bus": "",
			},
			answers: map[string]setup.Result{showEnv: {ExitCode: 1}, showEnvRetry: {ExitCode: 1}},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Ubuntu 22.04",
				JobsBackend: setup.JobsNone, JobsBackendReason: "no systemd user instance"},
			wantCalls: 2,
		},
		{
			name: "WSL1 (capital M), systemctl missing",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "PRETTY_NAME=\"Ubuntu 20.04.6 LTS\"\n",
				kernelReleasePath: "4.4.0-19041-Microsoft\n",
			},
			answers: map[string]setup.Result{showEnv: {ExitCode: 127}},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Ubuntu 20.04.6 LTS", WSL: true,
				JobsBackend: setup.JobsNone, JobsBackendReason: "no systemd user instance"},
			wantCalls: 1,
		},
		{
			name: "WSL2 with systemd",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "PRETTY_NAME='Debian GNU/Linux 12 (bookworm)'\n",
				kernelReleasePath: "5.15.153.1-microsoft-standard-WSL2\n",
			},
			answers: map[string]setup.Result{showEnv: {}},
			onPath:  []string{"pacman"},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Debian GNU/Linux 12 (bookworm)", WSL: true,
				JobsBackend: setup.JobsSystemd, JobsBackendReason: "systemd user units (systemctl --user)",
				PackageManagers: []string{"pacman"}},
			wantCalls: 1,
		},
		{
			name:    "linux without os-release",
			goos:    "linux",
			files:   mapFS{},
			answers: map[string]setup.Result{showEnv: {ExitCode: 1}},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Linux",
				JobsBackend: setup.JobsNone, JobsBackendReason: "no systemd user instance"},
			wantCalls: 1,
		},
		{
			name:   "Windows (AC-17: unsupported, nothing probed)",
			goos:   "windows",
			onPath: []string{"brew"},
			want: setup.PlatformInfo{OS: "windows", Arch: "arm64",
				JobsBackend: setup.JobsNone, JobsBackendReason: "unsupported OS windows"},
		},
	}
	for _, tc := range cases {
		r := &scriptRunner{t: t, answers: tc.answers, onPath: tc.onPath}
		got := detectPlatform(context.Background(), tc.files, r, tc.goos, "arm64", 1000)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
		wantCalls := tc.wantCalls
		if tc.goos == "darwin" {
			wantCalls = 1
		}
		if len(r.calls) != wantCalls {
			t.Errorf("%s: ran %q, want %d command(s)", tc.name, r.calls, wantCalls)
		}
		if got.Supported() != slices.Contains([]string{"darwin", "linux"}, tc.goos) {
			t.Errorf("%s: Supported() = %v", tc.name, got.Supported())
		}
	}
}

func TestJobsBackendOverride(t *testing.T) {
	t.Parallel()
	linux := setup.PlatformInfo{OS: "linux", JobsBackend: setup.JobsSystemd, SystemdEnv: []string{"XDG_RUNTIME_DIR=/run/user/1"}}
	darwin := setup.PlatformInfo{OS: "darwin", JobsBackend: setup.JobsLaunchd}
	cases := []struct {
		name     string
		p        setup.PlatformInfo
		override string
		want     string
		wantErr  bool
		wantEnv  bool
	}{
		{"empty keeps", linux, "", setup.JobsSystemd, false, true},
		{"same keeps env", linux, "systemd", setup.JobsSystemd, false, true},
		{"none on linux drops env", linux, "none", setup.JobsNone, false, false},
		{"none on darwin", darwin, "none", setup.JobsNone, false, false},
		{"systemd on linux with none detected", setup.PlatformInfo{OS: "linux", JobsBackend: setup.JobsNone}, "systemd", setup.JobsSystemd, false, false},
		{"launchd on linux", linux, "launchd", "", true, false},
		{"systemd on darwin", darwin, "systemd", "", true, false},
		{"cron", linux, "cron", "", true, false},
	}
	for _, tc := range cases {
		got, err := jobsBackendOverride(tc.p, tc.override)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		if err != nil {
			continue
		}
		if got.JobsBackend != tc.want || (len(got.SystemdEnv) > 0) != tc.wantEnv {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
}
