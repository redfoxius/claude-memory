package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"claude-memory/internal/setup"
)

// errBadHome means HOME is unset or relative: doctor exits 3 and install
// exits 2 before Detect (AC-68).
var errBadHome = errors.New("HOME is unset or not an absolute path")

// buildPaths computes every location internal/setup uses (AC-68). It is a
// pure function over its inputs; main.go supplies os.Getenv, the --bin-dir
// flag, os.Getuid(), the resolved os.Executable() and os.Getwd().
func buildPaths(getenv func(string) string, binDir string, uid int, self, cwd string) (setup.Paths, error) {
	home := getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return setup.Paths{}, fmt.Errorf("%w: %q", errBadHome, home)
	}
	home = filepath.Clean(home)

	claudeDir := filepath.Join(home, ".claude")
	claudeJSON := filepath.Join(home, ".claude.json")
	if d := getenv("CLAUDE_CONFIG_DIR"); d != "" {
		// CLAUDE_CONFIG_DIR moves both settings.json and .claude.json.
		if !filepath.IsAbs(d) {
			return setup.Paths{}, fmt.Errorf("CLAUDE_CONFIG_DIR must be an absolute path, got %q", d)
		}
		claudeDir = filepath.Clean(d)
		claudeJSON = filepath.Join(claudeDir, ".claude.json")
	}

	if binDir == "" {
		binDir = filepath.Join(home, ".local", "bin")
	} else if !filepath.IsAbs(binDir) {
		return setup.Paths{}, fmt.Errorf("--bin-dir must be an absolute path, got %q", binDir)
	}

	return setup.Paths{
		Home:            home,
		ClaudeDir:       claudeDir,
		ClaudeJSON:      claudeJSON,
		ConfigDir:       filepath.Join(home, ".config", "claude-memory"),
		StateDir:        filepath.Join(home, ".local", "state", "claude-memory"),
		ShareDir:        filepath.Join(home, ".local", "share", "claude-memory"),
		BinDir:          filepath.Clean(binDir),
		LaunchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		SystemdUserDir:  filepath.Join(home, ".config", "systemd", "user"),
		Cwd:             cwd,
		Self:            self,
		UID:             uid,
	}, nil
}

// envAllowList are the exact variable names internal/setup may see, besides
// the MEMORY_* prefix (AC-68).
var envAllowList = map[string]bool{
	"NO_COLOR":        true,
	"CI":              true,
	"PATH":            true,
	"XDG_RUNTIME_DIR": true,
}

// buildEnv snapshots the allow-listed variables from environ (os.Environ()
// form, KEY=VALUE).
func buildEnv(environ []string) setup.Env {
	env := setup.Env{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(k, "MEMORY_") || envAllowList[k] {
			env[k] = v
		}
	}
	return env
}

// ephemeralDirs lists where a `go run` binary lives (AC-35): the temp
// directory, $GOTMPDIR when set (go run builds there), and $GOCACHE, else
// <user cache dir>/go-build. Pure over its inputs; main passes os.TempDir,
// os.Getenv and os.UserCacheDir. Empty and relative entries are dropped.
func ephemeralDirs(tmpDir string, getenv func(string) string, userCacheDir func() (string, error)) []string {
	var out []string
	add := func(d string) {
		if d != "" && filepath.IsAbs(d) {
			out = append(out, filepath.Clean(d))
		}
	}
	add(tmpDir)
	add(getenv("GOTMPDIR"))
	if gc := getenv("GOCACHE"); gc != "" {
		add(gc)
	} else if cache, err := userCacheDir(); err == nil {
		add(filepath.Join(cache, "go-build"))
	}
	return out
}
