package main

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"claude-memory/internal/setup"
)

// Files and commands detectPlatform reads.
const (
	osReleasePath     = "/etc/os-release"
	kernelReleasePath = "/proc/sys/kernel/osrelease"
)

// packageManagers are the package managers whose presence on PATH feeds the
// hint table, in preference order (AC-15). brew is never required.
var packageManagers = []string{"brew", "apt-get", "dnf", "pacman"}

// detectPlatform reports the platform as data (AC-15): OS and arch, OS
// version, WSL, package managers and the jobs backend (AC-16). It reads
// files through fsys and runs only read-only commands through run
// (`sw_vers -productVersion`, `systemctl --user show-environment`). uid names
// /run/user/<uid> for the systemd bus retry.
func detectPlatform(ctx context.Context, fsys setup.FS, run setup.Runner, goos, goarch string, uid int) setup.PlatformInfo {
	p := setup.PlatformInfo{OS: goos, Arch: goarch, JobsBackend: setup.JobsNone}
	switch goos {
	case setup.OSDarwin:
		p.OSVersion = "macOS"
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		res, err := run.Run(cctx, setup.Cmd{Argv: []string{"sw_vers", "-productVersion"}})
		if v := strings.TrimSpace(string(res.Stdout)); err == nil && res.ExitCode == 0 && v != "" {
			p.OSVersion = "macOS " + v
		}
		p.JobsBackend = setup.JobsLaunchd
		p.JobsBackendReason = "launchd user agents (macOS)"
	case setup.OSLinux:
		p.OSVersion = linuxVersion(fsys)
		if b, err := fsys.ReadFile(kernelReleasePath); err == nil {
			// WSL1 kernels say "Microsoft", WSL2 "microsoft".
			p.WSL = strings.Contains(strings.ToLower(string(b)), "microsoft")
		}
		p.JobsBackend, p.JobsBackendReason, p.SystemdEnv = probeSystemd(ctx, fsys, run, uid)
	default:
		p.JobsBackendReason = "unsupported OS " + goos
		return p
	}
	for _, pm := range packageManagers {
		if _, err := run.LookPath(pm); err == nil {
			p.PackageManagers = append(p.PackageManagers, pm)
		}
	}
	return p
}

// probeSystemd decides the linux jobs backend (AC-16). `systemctl --user
// show-environment` exiting 0 means a user instance is reachable. When it is
// not and /run/user/<uid>/bus exists (an ssh session into a lingering user
// instance), the probe is retried with that bus in Cmd.Env, and the same
// variables are returned for every later `systemctl --user` call. Otherwise
// the backend is none: there is no cron fallback.
func probeSystemd(ctx context.Context, fsys setup.FS, run setup.Runner, uid int) (backend, reason string, env []string) {
	try := func(env []string) bool {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		res, err := run.Run(cctx, setup.Cmd{Argv: []string{"systemctl", "--user", "show-environment"}, Env: env})
		return err == nil && res.ExitCode == 0
	}
	if try(nil) {
		return setup.JobsSystemd, "systemd user units (systemctl --user)", nil
	}
	runDir := fmt.Sprintf("/run/user/%d", uid)
	bus := runDir + "/bus"
	if _, err := fsys.Stat(bus); err == nil {
		retry := []string{"XDG_RUNTIME_DIR=" + runDir, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + bus}
		if try(retry) {
			return setup.JobsSystemd, "systemd user units via " + bus + " (session without its own bus)", retry
		}
	}
	return setup.JobsNone, "no systemd user instance", nil
}

// jobsBackendOverride applies --jobs-backend (AC-16) to a detected platform.
// An empty override keeps the detected backend. launchd is valid only on
// darwin and systemd only on linux; "none" is valid anywhere. The systemd
// bus variables are kept only while the backend stays systemd.
func jobsBackendOverride(p setup.PlatformInfo, override string) (setup.PlatformInfo, error) {
	if override == "" || override == p.JobsBackend {
		return p, nil
	}
	switch override {
	case setup.JobsLaunchd:
		if p.OS != setup.OSDarwin {
			return p, fmt.Errorf("--jobs-backend launchd needs macOS, this is %s", p.OS)
		}
	case setup.JobsSystemd:
		if p.OS != setup.OSLinux {
			return p, fmt.Errorf("--jobs-backend systemd needs linux, this is %s", p.OS)
		}
	case setup.JobsNone:
	default:
		return p, fmt.Errorf("--jobs-backend %q: want launchd, systemd or none", override)
	}
	p.JobsBackend = override
	p.JobsBackendReason = "--jobs-backend " + override
	if override != setup.JobsSystemd {
		p.SystemdEnv = nil
	}
	return p, nil
}

// linuxVersion returns PRETTY_NAME from /etc/os-release, else NAME plus
// VERSION_ID, else "Linux".
func linuxVersion(fsys setup.FS) string {
	b, err := fsys.ReadFile(osReleasePath)
	if err != nil {
		return "Linux"
	}
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `'"`)
		}
		kv[k] = v
	}
	switch {
	case kv["PRETTY_NAME"] != "":
		return kv["PRETTY_NAME"]
	case kv["NAME"] != "":
		return strings.TrimSpace(kv["NAME"] + " " + kv["VERSION_ID"])
	}
	return "Linux"
}
