package main

import (
	"context"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"testing"

	"claude-memory/internal/setup"
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
func (m mapFS) Stat(p string) (fs.FileInfo, error)                { return nil, fs.ErrNotExist }
func (m mapFS) Lstat(p string) (fs.FileInfo, error)               { return nil, fs.ErrNotExist }
func (m mapFS) ReadDir(p string) ([]fs.DirEntry, error)           { return nil, fs.ErrNotExist }
func (m mapFS) EvalSymlinks(p string) (string, error)             { return p, nil }
func (m mapFS) Writable(string) bool                              { return false }
func (m mapFS) WriteFileAtomic(string, []byte, fs.FileMode) error { return setup.ErrDryRun }
func (m mapFS) MkdirAll(string, fs.FileMode) error                { return setup.ErrDryRun }
func (m mapFS) Remove(string) error                               { return setup.ErrDryRun }
func (m mapFS) Chmod(string, fs.FileMode) error                   { return setup.ErrDryRun }
func (m mapFS) Lock(string) (func() error, error)                 { return nil, setup.ErrDryRun }

// scriptRunner answers commands by exact argv and records every call; an
// unscripted command fails the test.
type scriptRunner struct {
	t       *testing.T
	answers map[string]setup.Result
	calls   [][]string
}

func (r *scriptRunner) Run(_ context.Context, c setup.Cmd) (setup.Result, error) {
	r.calls = append(r.calls, c.Argv)
	if c.Mutating {
		r.t.Errorf("detectPlatform ran a mutating command %q", c.Argv)
	}
	if res, ok := r.answers[fmt.Sprint(c.Argv)]; ok {
		return res, nil
	}
	r.t.Errorf("unscripted command %q", c.Argv)
	return setup.Result{}, fmt.Errorf("unscripted %q", c.Argv)
}
func (r *scriptRunner) LookPath(name string) (string, error) { return "", fs.ErrNotExist }

func TestDetectPlatform(t *testing.T) {
	t.Parallel()
	swVers := fmt.Sprint([]string{"sw_vers", "-productVersion"})
	cases := []struct {
		name    string
		goos    string
		files   mapFS
		answers map[string]setup.Result
		want    setup.PlatformInfo
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
			name: "Ubuntu",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n",
				kernelReleasePath: "6.8.0-45-generic\n",
			},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Ubuntu 24.04.1 LTS",
				JobsBackend: setup.JobsNone, JobsBackendReason: "systemd user timers are detected from slice 2 on"},
		},
		{
			name: "Fedora without PRETTY_NAME",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "NAME=Fedora Linux\nVERSION_ID=40\n",
				kernelReleasePath: "6.10.6-200.fc40.x86_64\n",
			},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Fedora Linux 40",
				JobsBackend: setup.JobsNone, JobsBackendReason: "systemd user timers are detected from slice 2 on"},
		},
		{
			name: "WSL1 (capital M)",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "PRETTY_NAME=\"Ubuntu 20.04.6 LTS\"\n",
				kernelReleasePath: "4.4.0-19041-Microsoft\n",
			},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Ubuntu 20.04.6 LTS", WSL: true,
				JobsBackend: setup.JobsNone, JobsBackendReason: "systemd user timers are detected from slice 2 on"},
		},
		{
			name: "WSL2",
			goos: "linux",
			files: mapFS{
				osReleasePath:     "PRETTY_NAME='Debian GNU/Linux 12 (bookworm)'\n",
				kernelReleasePath: "5.15.153.1-microsoft-standard-WSL2\n",
			},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Debian GNU/Linux 12 (bookworm)", WSL: true,
				JobsBackend: setup.JobsNone, JobsBackendReason: "systemd user timers are detected from slice 2 on"},
		},
		{
			name:  "linux without os-release",
			goos:  "linux",
			files: mapFS{},
			want: setup.PlatformInfo{OS: "linux", Arch: "arm64", OSVersion: "Linux",
				JobsBackend: setup.JobsNone, JobsBackendReason: "systemd user timers are detected from slice 2 on"},
		},
		{
			name: "Windows",
			goos: "windows",
			want: setup.PlatformInfo{OS: "windows", Arch: "arm64",
				JobsBackend: setup.JobsNone, JobsBackendReason: "unsupported OS windows"},
		},
	}
	for _, tc := range cases {
		r := &scriptRunner{t: t, answers: tc.answers}
		got := detectPlatform(context.Background(), tc.files, r, tc.goos, "arm64")
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
		if tc.goos != "darwin" && len(r.calls) != 0 {
			t.Errorf("%s: ran %q, want no commands", tc.name, r.calls)
		}
		if got.Supported() != slices.Contains([]string{"darwin", "linux"}, tc.goos) {
			t.Errorf("%s: Supported() = %v", tc.name, got.Supported())
		}
	}
}
