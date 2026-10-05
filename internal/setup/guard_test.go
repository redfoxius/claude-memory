package setup

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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
// Env instead (AC-68), process-wide environment mutation, and every
// filesystem verb: the package touches the disk only through the FS port, so
// the same code is testable with a fake and dry-run cannot write (AC-63,
// plan WI-S2-0, Design 20). The only `os` identifiers allowed are the
// error sentinels ErrNotExist, ErrExist and ErrPermission, FileMode and the
// Mode* constants.
var bannedOSCalls = []string{
	"Getenv", "LookupEnv", "Environ", "Setenv", "Unsetenv", "Clearenv",
	"UserHomeDir", "UserConfigDir", "UserCacheDir",
	"Getuid", "Geteuid", "Executable", "Getwd", "Chdir",
	"Open", "OpenFile", "Create", "CreateTemp", "ReadFile", "WriteFile",
	"ReadDir", "Stat", "Lstat", "Rename", "Chmod", "Chown", "Symlink",
	"Link", "Truncate", "DirFS",
}

// bannedOSPrefixes ban a whole family of os functions (Mkdir, MkdirAll,
// MkdirTemp, Remove, RemoveAll).
var bannedOSPrefixes = []string{"Mkdir", "Remove"}

// bannedNetPrefixes ban the net package's connection entry points: network
// access goes through the DB/Ollama ports.
var bannedNetPrefixes = []string{"Dial", "Listen"}

func TestGuardImports(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the go toolchain; run without -short (CI does once)")
	}
	t.Parallel()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", "./...")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if !slices.Contains(deps, "github.com/redfoxius/claude-memory/internal/setup") {
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

// sourceFiles returns every non-test .go file under the package directory,
// recursively (a sub-package of internal/setup is held to the same rules).
func sourceFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// importName returns the local name f gives the package imported as path
// ("" when it does not import it).
func importName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path
	}
	return ""
}

func TestGuardNoAmbientOSCalls(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := sourceFiles(t)
	if len(files) == 0 {
		t.Fatal("no non-test sources found")
	}
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, v := range guardViolations(fset, f) {
			t.Error(v)
		}
	}
}

// guardViolations reports every banned os/net use in f.
func guardViolations(fset *token.FileSet, f *ast.File) []string {
	var out []string
	name := fset.Position(f.Pos()).Filename
	osName, netName := importName(f, "os"), importName(f, "net")
	for _, pkg := range []struct{ path, local string }{{"os", osName}, {"net", netName}} {
		if pkg.local == "." || pkg.local == "_" {
			out = append(out, name+": dot/blank import of "+pkg.path+" is not allowed")
		}
	}
	if osName == "" && netName == "" {
		return out
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case osName != "" && id.Name == osName && bannedOS(sel.Sel.Name):
			out = append(out, fmt.Sprintf("%s: os.%s is banned in internal/setup; take it from Paths/Env or the FS port (AC-68, AC-63)",
				fset.Position(sel.Pos()), sel.Sel.Name))
		case netName != "" && id.Name == netName && hasAnyPrefix(sel.Sel.Name, bannedNetPrefixes):
			out = append(out, fmt.Sprintf("%s: net.%s is banned in internal/setup; network access goes through a port (AC-63)",
				fset.Position(sel.Pos()), sel.Sel.Name))
		}
		return true
	})
	return out
}

func bannedOS(name string) bool {
	return slices.Contains(bannedOSCalls, name) || hasAnyPrefix(name, bannedOSPrefixes)
}

func hasAnyPrefix(s string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(s, p) })
}

// TestGuardDetectsViolations proves the guard has teeth: each snippet must
// be reported, and the allowed identifiers must not be.
func TestGuardDetectsViolations(t *testing.T) {
	t.Parallel()
	check := func(src string) []string {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "snippet.go", "package x\n"+src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		return guardViolations(fset, f)
	}
	for _, call := range []string{
		"os.Getenv", "os.ReadFile", "os.WriteFile", "os.Open", "os.OpenFile", "os.Create", "os.CreateTemp",
		"os.ReadDir", "os.Stat", "os.Lstat", "os.Mkdir", "os.MkdirAll", "os.MkdirTemp", "os.Remove", "os.RemoveAll",
		"os.Rename", "os.Chmod", "os.Chown", "os.Symlink", "os.Link", "os.Truncate",
	} {
		if v := check("import \"os\"\nvar _ = " + call + "\n"); len(v) != 1 {
			t.Errorf("%s: violations %v, want 1", call, v)
		}
	}
	for _, ok := range []string{"os.ErrNotExist", "os.ErrExist", "os.ErrPermission", "os.FileMode(0)", "os.ModeDir", "os.ModePerm", "os.ModeSymlink"} {
		if v := check("import \"os\"\nvar _ = " + ok + "\n"); len(v) != 0 {
			t.Errorf("%s must be allowed: %v", ok, v)
		}
	}
	for _, call := range []string{"net.Dial", "net.DialTimeout", "net.DialTCP", "net.Listen", "net.ListenTCP", "net.ListenPacket"} {
		if v := check("import \"net\"\nvar _ = " + call + "\n"); len(v) != 1 {
			t.Errorf("%s: violations %v, want 1", call, v)
		}
	}
	for _, ok := range []string{"net.ParseIP", "net.SplitHostPort"} {
		if v := check("import \"net\"\nvar _ = " + ok + "\n"); len(v) != 0 {
			t.Errorf("%s must be allowed: %v", ok, v)
		}
	}
	// An aliased import is still caught.
	if v := check("import o \"os\"\nvar _ = o.ReadFile\n"); len(v) != 1 {
		t.Errorf("aliased os.ReadFile: %v", v)
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
