package main

import (
	"fmt"
	"runtime/debug"
)

// version is set at build time by the Makefile's LDFLAGS
// (-X main.version=$(git describe --tags --always --dirty)). When empty, the
// version comes from the module build info (spec AC-2).
var version = ""

// versionInfo is what `claude-memory version` reports.
type versionInfo struct {
	Version  string // ldflags version, else the module version; "" when neither is known
	Revision string // vcs.revision from the build info
	Modified bool   // vcs.modified: the build had uncommitted changes
}

// resolveVersion combines the ldflags version with the build info (nil when
// unavailable).
func resolveVersion(ldflagsVersion string, bi *debug.BuildInfo) versionInfo {
	v := versionInfo{Version: ldflagsVersion}
	if bi == nil {
		return v
	}
	if v.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = s.Value
		case "vcs.modified":
			v.Modified = s.Value == "true"
		}
	}
	return v
}

// Short is the version alone: the version string, else "dev".
func (v versionInfo) Short() string {
	if v.Version == "" {
		return "dev"
	}
	return v.Version
}

// String formats the version line, e.g.
// "claude-memory v0.3.0-4-gabc1234-dirty (revision abc1234def56, modified)",
// or "claude-memory dev" when nothing is known.
func (v versionInfo) String() string {
	s := "claude-memory " + v.Short()
	if v.Revision != "" {
		rev := v.Revision
		if len(rev) > 12 {
			rev = rev[:12]
		}
		s += " (revision " + rev
		if v.Modified {
			s += ", modified"
		}
		s += ")"
	}
	return s
}

// buildVersion returns this binary's version information.
func buildVersion() versionInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	return resolveVersion(version, bi)
}

// cmdVersion implements the "version" subcommand. It needs no config, so
// main dispatches it before loading the env file (AC-1).
func cmdVersion(args []string) error {
	if len(args) > 0 {
		return usageError(fmt.Errorf("usage: claude-memory version (no arguments)"))
	}
	fmt.Println(buildVersion())
	return nil
}
