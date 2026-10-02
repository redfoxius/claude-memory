// Package setup holds the install/doctor feature: the doctor checks, the
// install step engine (slice 2), and the pure libraries they share
// (settings.json merge, CLAUDE.md managed block, .claude.json reader,
// manifest, redaction).
//
// The package is pure with respect to its environment (spec AC-63, AC-68):
// it declares every external concern as a port (ports.go) and receives
// every path and environment input as a value (Paths, Env, PlatformInfo)
// built once in cmd/claude-memory/main.go. It never imports os/exec,
// net/http, database/sql, pgx or golang.org/x/term, and never calls
// os.Getenv, os.LookupEnv, os.Environ, os.UserHomeDir, os.Getuid,
// os.Executable or os.Getwd. guard_test.go enforces both rules.
package setup

import "path/filepath"

// Paths is every filesystem location the feature uses, computed once in
// main.go from HOME, CLAUDE_CONFIG_DIR, --bin-dir, os.Getuid(),
// os.Executable() and os.Getwd() (AC-68). Every path is absolute. Tests
// construct it directly from t.TempDir().
type Paths struct {
	Home            string // $HOME, absolute
	ClaudeDir       string // $CLAUDE_CONFIG_DIR, else Home/.claude
	ClaudeJSON      string // $CLAUDE_CONFIG_DIR/.claude.json, else Home/.claude.json
	ConfigDir       string // Home/.config/claude-memory (env, namespaces.yaml, install.json, install.lock)
	StateDir        string // Home/.local/state/claude-memory (job logs, bootstrap.sql)
	ShareDir        string // Home/.local/share/claude-memory ([S3] docker bundle)
	BinDir          string // --bin-dir, else Home/.local/bin
	LaunchAgentsDir string // Home/Library/LaunchAgents (darwin)
	SystemdUserDir  string // Home/.config/systemd/user (linux)
	Cwd             string // for project settings (AC-70) and `namespaces which`
	Self            string // os.Executable, symlinks resolved ("" when unknown)
	UID             int
	// EphemeralDirs are the directories a `go run` or `go test` binary lives
	// in: os.TempDir(), $GOTMPDIR when set, $GOCACHE else UserCacheDir/go-build.
	// The binary step refuses a Self under any of them (AC-35). Computed in
	// main; the step also resolves their symlinks through the FS port.
	EphemeralDirs []string
}

// File names inside the directories of Paths.
const (
	EnvFileName        = "env"
	NamespacesFileName = "namespaces.yaml"
	ManifestFileName   = "install.json"
	LockFileName       = "install.lock"
	BootstrapFileName  = "bootstrap.sql"
	BinaryName         = "claude-memory"
)

// EnvFile is the path of the env file (<ConfigDir>/env).
func (p Paths) EnvFile() string { return filepath.Join(p.ConfigDir, EnvFileName) }

// NamespacesFile is the path of namespaces.yaml (<ConfigDir>/namespaces.yaml).
func (p Paths) NamespacesFile() string { return filepath.Join(p.ConfigDir, NamespacesFileName) }

// Manifest is the path of install.json (<ConfigDir>/install.json).
func (p Paths) Manifest() string { return filepath.Join(p.ConfigDir, ManifestFileName) }

// Lock is the path of the install lock file (<ConfigDir>/install.lock).
func (p Paths) Lock() string { return filepath.Join(p.ConfigDir, LockFileName) }

// Bootstrap is the path of the temporary bootstrap.sql (<StateDir>/bootstrap.sql).
func (p Paths) Bootstrap() string { return filepath.Join(p.StateDir, BootstrapFileName) }

// InstalledBinary is the path the binary step installs to (<BinDir>/claude-memory).
func (p Paths) InstalledBinary() string { return filepath.Join(p.BinDir, BinaryName) }

// SettingsJSON is Claude Code's user settings file (<ClaudeDir>/settings.json).
func (p Paths) SettingsJSON() string { return filepath.Join(p.ClaudeDir, "settings.json") }

// HookScriptsDir is where the hook wrapper scripts live
// (<ClaudeDir>/hooks/claude-memory).
func (p Paths) HookScriptsDir() string { return filepath.Join(p.ClaudeDir, "hooks", "claude-memory") }

// SkillsDir is Claude Code's user skills directory (<ClaudeDir>/skills).
func (p Paths) SkillsDir() string { return filepath.Join(p.ClaudeDir, "skills") }

// Env is an allow-listed snapshot of the process environment (AC-68):
// MEMORY_*, NO_COLOR, CI, PATH and XDG_RUNTIME_DIR. It is built once in
// main.go; internal/setup reads the environment only through it.
type Env map[string]string

// Get returns the value of key, or "" when it is unset.
func (e Env) Get(key string) string { return e[key] }

// Lookup returns the value of key and whether it is set.
func (e Env) Lookup(key string) (string, bool) {
	v, ok := e[key]
	return v, ok
}
