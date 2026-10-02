package main

import (
	"bufio"
	"context"
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

// detectPlatform reports the platform as data (AC-15): OS and arch, OS
// version, WSL and the jobs backend. It reads files through fsys and runs
// only the read-only `sw_vers -productVersion` through run. Slice 1 detects
// the launchd backend only; on linux the backend is none until slice 2 adds
// the systemd probe (AC-16), and package managers are slice 2 as well.
func detectPlatform(ctx context.Context, fsys setup.FS, run setup.Runner, goos, goarch string) setup.PlatformInfo {
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
		p.JobsBackendReason = "systemd user timers are detected from slice 2 on"
	default:
		p.JobsBackendReason = "unsupported OS " + goos
	}
	return p
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
