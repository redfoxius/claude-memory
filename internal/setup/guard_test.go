package setup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Architecture guard (AC-18, AC-63, AC-68): internal/setup reaches the
// outside world only through its ports.

// bannedImports may not appear among the package's dependencies (direct or
// transitive, non-test). A prefix ending in "/" bans the whole tree.
var bannedImports = []string{
	"os/exec",
	"net/http",
	"database/sql",
	"github.com/jackc/", // pgx, pgconn, pgxpool, ...
	"golang.org/x/term",
}

// bannedOSCalls are ambient-state reads the package must take from Paths and
// Env instead (AC-68), plus process-wide environment mutation.
var bannedOSCalls = []string{
	"Getenv", "LookupEnv", "Environ", "Setenv", "Unsetenv", "Clearenv",
	"UserHomeDir", "UserConfigDir", "UserCacheDir",
	"Getuid", "Geteuid", "Executable", "Getwd", "Chdir",
}

func TestGuardImports(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain; run without -short (CI does once)")
	}
	t.Parallel()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if !slices.Contains(deps, "claude-memory/internal/setup") {
		t.Fatalf("go list output does not include the package itself: %v", deps)
	}
	for _, d := range deps {
		for _, b := range bannedImports {
			if d == strings.TrimSuffix(b, "/") || (strings.HasSuffix(b, "/") && strings.HasPrefix(d, b)) {
				t.Errorf("internal/setup depends on banned package %s (spec §4, AC-63)", d)
			}
		}
	}
}

func TestGuardNoAmbientOSCalls(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		osName := ""
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path != "os" {
				continue
			}
			osName = "os"
			if imp.Name != nil {
				osName = imp.Name.Name
			}
		}
		if osName == "" {
			continue
		}
		if osName == "." || osName == "_" {
			t.Errorf("%s: dot/blank import of os is not allowed", name)
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if ok && id.Name == osName && slices.Contains(bannedOSCalls, sel.Sel.Name) {
				t.Errorf("%s: os.%s is banned in internal/setup; take it from Paths/Env (AC-68)",
					fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no non-test sources found")
	}
}

// deniedArgv0 are programs no setup code may run (AC-18): shells and env
// (no shell, argv only), privilege escalation, package managers, network
// transfer, remote shells, psql (bootstrap.sql is printed, never run) and
// docker (topology C is deferred).
var deniedArgv0 = []string{
	"sh", "bash", "zsh", "dash", "env", "sudo", "doas",
	"brew", "apt", "apt-get", "dnf", "yum", "pacman",
	"curl", "wget", "ssh", "scp", "rsync", "psql", "docker",
}

// deniedCall returns why c must never run from internal/setup, or "" when it
// may. claude is allowed only as `claude mcp add --scope user …` and
// `claude mcp remove --scope user …`; `mcp get`/`mcp list` are denied for
// any program because they spawn the registered server (AC-67).
func deniedCall(c Cmd) string {
	if len(c.Argv) == 0 {
		return "empty argv"
	}
	base := filepath.Base(c.Argv[0])
	if slices.Contains(deniedArgv0, base) {
		return "program " + base + " is on the AC-18 deny list"
	}
	for i := 0; i+1 < len(c.Argv); i++ {
		if c.Argv[i] == "mcp" && (c.Argv[i+1] == "get" || c.Argv[i+1] == "list") {
			return "`mcp " + c.Argv[i+1] + "` spawns the registered MCP server (AC-67)"
		}
	}
	if base == "claude" {
		a := c.Argv
		if len(a) >= 5 && a[1] == "mcp" && (a[2] == "add" || a[2] == "remove") && a[3] == "--scope" && a[4] == "user" {
			return ""
		}
		return "only `claude mcp add|remove --scope user …` may run (AC-18, AC-40)"
	}
	return ""
}

// assertNoDeniedCalls fails t for every recorded call deniedCall rejects.
// NewFakeRunner runs it at cleanup, so every setup test is covered.
func assertNoDeniedCalls(t testing.TB, calls []Cmd) {
	t.Helper()
	for _, c := range calls {
		if reason := deniedCall(c); reason != "" {
			t.Errorf("denied command %q: %s", c.Argv, reason)
		}
	}
}

func TestDeniedCall(t *testing.T) {
	t.Parallel()
	cases := []struct {
		argv   []string
		denied bool
	}{
		{[]string{"launchctl", "print", "gui/501/io.github.claude-memory.cleanup"}, false},
		{[]string{"/usr/bin/git", "--version"}, false},
		{[]string{"sw_vers", "-productVersion"}, false},
		{[]string{"claude", "mcp", "add", "--scope", "user", "claude-memory", "--", "/bin/cm", "serve"}, false},
		{[]string{"/Users/x/.local/bin/claude", "mcp", "remove", "--scope", "user", "claude-memory"}, false},
		{[]string{"claude", "mcp", "add", "claude-memory", "--", "/bin/cm", "serve"}, true}, // default scope local
		{[]string{"claude", "mcp", "get", "claude-memory"}, true},
		{[]string{"claude", "mcp", "list"}, true},
		{[]string{"npx", "@anthropic-ai/claude-code", "mcp", "list"}, true},
		{[]string{"claude", "-p", "hello"}, true},
		{[]string{"claude"}, true},
		{[]string{"sh", "-c", "echo hi"}, true},
		{[]string{"/bin/bash", "-lc", "x"}, true},
		{[]string{"/usr/bin/env", "FOO=1", "x"}, true},
		{[]string{"sudo", "-u", "postgres", "psql"}, true},
		{[]string{"/opt/homebrew/bin/brew", "install", "ollama"}, true},
		{[]string{"apt-get", "install", "-y", "postgresql-16"}, true},
		{[]string{"curl", "-fsSL", "https://ollama.com/install.sh"}, true},
		{[]string{"ssh", "host"}, true},
		{[]string{"psql", "-d", "postgres"}, true},
		{[]string{"docker", "compose", "up", "-d"}, true},
		{nil, true},
	}
	for _, tc := range cases {
		got := deniedCall(Cmd{Argv: tc.argv}) != ""
		if got != tc.denied {
			t.Errorf("deniedCall(%q) denied=%v, want %v", tc.argv, got, tc.denied)
		}
	}
}
