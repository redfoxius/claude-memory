package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"claude-memory/internal/setup"
)

func TestRootGuard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		euid      int
		allowRoot bool
		wantErr   bool
	}{
		{"root without the flag", 0, false, true},
		{"root with --allow-root", 0, true, false},
		{"normal user", 501, false, false},
		{"normal user with the flag", 501, true, false},
	}
	for _, tc := range cases {
		err := rootGuard(tc.euid, tc.allowRoot)
		if (err != nil) != tc.wantErr || (err != nil && !errors.Is(err, errRoot)) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	if got := errRoot.Error(); got != "refusing to install for root; run as your user (or pass --allow-root)" {
		t.Errorf("message = %q", got)
	}
}

func TestParseInstallFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		wantErr string // substring; "" = must parse
		check   func(t *testing.T, o installOptions)
	}{
		{"none", nil, "", nil},
		{"full set", []string{
			"--yes", "--dry-run", "--reconfigure", "--topology", "remote", "--skip", "ollama,namespaces", "--skip=binary",
			"--bin-dir", "/opt/bin", "--ollama-url", "http://h:11434", "--pg-dsn", "postgresql://u@h:5432/db",
			"--pg-sslmode", "require", "--pg-password-stdin", "--namespace", "work=~/work/**", "--namespace=oss=~/oss/**",
			"--jobs-backend", "none", "--no-doctor", "--allow-root",
		}, "", func(t *testing.T, o installOptions) {
			in := o.Inputs
			if !in.Yes || !in.DryRun || !in.Reconfigure || in.Upgrade || in.Topology != "remote" ||
				!slices.Equal(in.Skip, []string{"ollama", "namespaces", "binary"}) || in.BinDir != "/opt/bin" || !in.BinDirExplicit ||
				in.OllamaURL != "http://h:11434" || in.PGDSN != "postgresql://u@h:5432/db" || in.PGSSLMode != "require" ||
				!slices.Equal(in.Namespaces, []string{"work=~/work/**", "oss=~/oss/**"}) ||
				in.JobsBackend != "none" || !o.PasswordStdin || !in.NoDoctor || !o.AllowRoot {
				t.Errorf("parsed %+v", o)
			}
		}},
		{"upgrade is yes", []string{"--upgrade"}, "", func(t *testing.T, o installOptions) {
			if !o.Inputs.Yes || !o.Inputs.Upgrade {
				t.Errorf("--upgrade parsed as %+v", o.Inputs)
			}
		}},
		{"no bin-dir is not explicit", []string{"--yes"}, "", func(t *testing.T, o installOptions) {
			if o.Inputs.BinDirExplicit {
				t.Error("BinDirExplicit without --bin-dir")
			}
		}},
		{"unknown flag", []string{"--bogus"}, "flag provided but not defined", nil},
		{"positional argument", []string{"extra"}, "no arguments", nil},
		{"topology docker-local", []string{"--topology", "docker-local"}, "deferred", nil},
		{"topology docker-server", []string{"--topology=docker-server"}, "deferred", nil},
		{"topology junk", []string{"--topology", "mars"}, "want local or remote", nil},
		{"--only", []string{"--only", "mcp"}, "§12.1", nil},
		{"--only=", []string{"--only=mcp"}, "§12.1", nil},
		{"--purge-database", []string{"--purge-database"}, "§12.1", nil},
		{"--seed-file", []string{"--seed-file", "x"}, "§12.1", nil},
		{"--latency", []string{"--latency"}, "§12.1", nil},
		{"jobs-backend cron", []string{"--jobs-backend", "cron"}, "§12.1", nil},
		{"jobs-backend junk", []string{"--jobs-backend", "launchctl"}, "want launchd, systemd or none", nil},
		{"--claude-md empty", []string{"--claude-md="}, "--claude-md needs a path", nil},
		{"--claude-md blank", []string{"--claude-md", "  "}, "--claude-md needs a path", nil},
		{"--claude-md path", []string{"--claude-md", "acme/CLAUDE.md"}, "", func(t *testing.T, o installOptions) {
			if o.Inputs.ClaudeMD != "acme/CLAUDE.md" {
				t.Errorf("ClaudeMD = %q", o.Inputs.ClaudeMD)
			}
		}},
		{"--pr-repos is 2b", []string{"--pr-repos", "/a"}, "slice 2b", nil},
		{"--no-jobs is 2b", []string{"--no-jobs"}, "slice 2b", nil},
		{"pg-sslmode junk", []string{"--pg-sslmode", "verify-full"}, "want prefer, require or disable", nil},
		{"pg-dsn with a password", []string{"--pg-dsn", "postgresql://u:pw@h/db"}, "--pg-dsn", nil},
		{"ollama-url junk", []string{"--ollama-url", "ftp://h"}, "--ollama-url", nil},
		{"namespace without glob", []string{"--namespace", "work"}, "--namespace", nil},
		{"bad bool", []string{"--yes=maybe"}, "invalid value for flag --yes", nil},
		{"deferred flag after -- is positional", []string{"--", "--only"}, "no arguments", nil},
		{"missing value", []string{"--topology"}, "needs an argument", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			o, err := parseInstallFlags(tc.args, &stderr)
			if tc.wantErr != "" {
				if exitCode(err) != 2 {
					t.Fatalf("exit code %d (err %v), want 2", exitCode(err), err)
				}
				if msg := err.Error() + stderr.String(); !strings.Contains(msg, tc.wantErr) {
					t.Errorf("error %q does not contain %q", msg, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, o)
			}
		})
	}
}

func TestParseInstallFlagsHelp(t *testing.T) {
	t.Parallel()
	o, err := parseInstallFlags([]string{"-h"}, io.Discard)
	if err != nil || !o.Help {
		t.Errorf("-h: %+v, %v", o, err)
	}
}

// TestResolveInstallPlatform is the first open item of the previous slice:
// jobsBackendOverride is applied and an unsupported OS (or an override the OS
// cannot have) exits 2 before any write.
func TestResolveInstallPlatform(t *testing.T) {
	t.Parallel()
	linux := setup.PlatformInfo{OS: setup.OSLinux, Arch: "amd64", JobsBackend: setup.JobsSystemd}
	darwin := setup.PlatformInfo{OS: setup.OSDarwin, Arch: "arm64", JobsBackend: setup.JobsLaunchd}
	freebsd := setup.PlatformInfo{OS: "freebsd", Arch: "amd64", JobsBackend: setup.JobsNone}

	if p, err := resolveInstallPlatform(linux, "none"); err != nil || p.JobsBackend != setup.JobsNone {
		t.Errorf("linux none: %+v, %v", p, err)
	}
	if p, err := resolveInstallPlatform(darwin, ""); err != nil || p.JobsBackend != setup.JobsLaunchd {
		t.Errorf("darwin default: %+v, %v", p, err)
	}
	for _, tc := range []struct {
		name string
		p    setup.PlatformInfo
		ov   string
	}{
		{"launchd on linux", linux, "launchd"},
		{"systemd on darwin", darwin, "systemd"},
		{"unsupported OS", freebsd, ""},
	} {
		if _, err := resolveInstallPlatform(tc.p, tc.ov); exitCode(err) != 2 {
			t.Errorf("%s: exit %d (err %v), want 2", tc.name, exitCode(err), err)
		}
	}
	// The unsupported-OS message wins over a --jobs-backend complaint (C7).
	_, err := resolveInstallPlatform(freebsd, "launchd")
	if err == nil || !strings.Contains(err.Error(), "unsupported OS freebsd") {
		t.Errorf("unsupported OS message: %v", err)
	}
}

func TestInstallExit(t *testing.T) {
	t.Parallel()
	red := setup.NewRedactor()
	red.Register("S3ntinel-secret")
	if err := installExit(setup.RunResult{ExitCode: 0}, red); err != nil {
		t.Errorf("exit 0: %v", err)
	}
	if err := installExit(setup.RunResult{ExitCode: 1}, red); exitCode(err) != 1 || !errors.Is(err, errQuiet) {
		t.Errorf("exit 1 must be quiet: %v", err)
	}
	if err := installExit(setup.RunResult{ExitCode: 130, Err: setup.ErrInterrupted}, red); exitCode(err) != 130 || !errors.Is(err, errQuiet) {
		t.Errorf("exit 130 must be quiet: %v", err)
	}
	if err := installExit(setup.RunResult{ExitCode: 1, Err: errors.New("boom")}, red); exitCode(err) != 1 || errors.Is(err, errQuiet) {
		t.Errorf("exit 1 with an error must print it: %v", err)
	}
	err := installExit(setup.RunResult{ExitCode: 2, Err: fmt.Errorf("bad value S3ntinel-secret: %w", setup.ErrTooManyAttempts)}, red)
	if exitCode(err) != 2 || strings.Contains(err.Error(), "S3ntinel-secret") {
		t.Errorf("exit 2 must print a redacted message: %v", err)
	}
}

// TestParseInstallFlagsNeverEchoInput (S4): the error text holds neither a
// typed value nor a positional argument, and a bad --pg-password-stdin value
// gets a fixed message.
func TestParseInstallFlagsNeverEchoInput(t *testing.T) {
	t.Parallel()
	const secret = "Hunter2-secret"
	for _, args := range [][]string{
		{"--pg-password-stdin=" + secret},
		{"--yes=" + secret},
		{secret},
		{"--pg-dsn", "postgresql://u:" + secret + "@h/db"},
		{"--ollama-url", "http://u:" + secret + "@h"},
		{"--", secret},
	} {
		var stderr bytes.Buffer
		_, err := parseInstallFlags(args, &stderr)
		if exitCode(err) != 2 {
			t.Errorf("%q: exit %d (%v), want 2", args, exitCode(err), err)
			continue
		}
		if strings.Contains(err.Error()+stderr.String(), secret) {
			t.Errorf("%q: the typed value was echoed: %v / %s", args, err, stderr.String())
		}
	}
}
