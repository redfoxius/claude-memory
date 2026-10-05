package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/importer"
	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/namespace"
	"github.com/redfoxius/claude-memory/internal/scrub"
)

// fakeImportSvc scripts Store outcomes by title.
type fakeImportSvc struct {
	reqs []*memory.StoreRequest
	out  func(req *memory.StoreRequest) (*memory.StoreResponse, error)
}

func (f *fakeImportSvc) Store(_ context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error) {
	f.reqs = append(f.reqs, req)
	return f.out(req)
}

// importFixture builds deps over a temp projects dir with two memory files.
type importFixture struct {
	deps    importDeps
	out     bytes.Buffer
	opened  int
	svc     *fakeImportSvc
	cwdNS   string
	cwdWhy  string
	projDir string
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "work", "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(root, "projects")
	memDir := filepath.Join(proj, "-enc", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\ndescription: %s\nmetadata:\n  type: feedback\n---\nbody of %s\n"
	for _, name := range []string{"a", "b"} {
		title := "Title " + name
		if name == "b" {
			title = "Key AKIAABCDEFGHIJKLMNOP leaked"
		}
		if err := os.WriteFile(filepath.Join(memDir, name+".md"), []byte(fmt.Sprintf(doc, title, name)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &importFixture{cwdNS: "work", cwdWhy: namespace.WhyRule + " x", projDir: proj}
	f.svc = &fakeImportSvc{out: func(req *memory.StoreRequest) (*memory.StoreResponse, error) {
		return &memory.StoreResponse{ID: "3f2a91c0-0000-0000-0000-000000000000", Decision: memory.ActionAdd}, nil
	}}
	f.deps = importDeps{
		Env: importer.Env{
			FS:          readOnlyFS{},
			NamespaceOf: func(string) string { return "work" },
			Toplevel:    func(string) (string, bool) { return "", false },
			Decode:      func(string) (string, bool) { return home, true },
		},
		Cwd:         home,
		Home:        root,
		NamespaceOf: func(string) (string, string) { return f.cwdNS, f.cwdWhy },
		Scrub:       scrub.New().Scrub,
		Open: func(ns string) (importService, time.Duration, func(), error) {
			f.opened++
			return f.svc, 90 * 24 * time.Hour, func() {}, nil
		},
		Out: &f.out,
		Err: &f.out,
	}
	return f
}

func TestImportDryRunOpensNothingAndScrubsTitles(t *testing.T) {
	f := newImportFixture(t)
	err := runImport(context.Background(), f.deps, []string{"automem", "--dry-run", "--projects-dir", f.projDir})
	if err != nil {
		t.Fatal(err)
	}
	if f.opened != 0 || len(f.svc.reqs) != 0 {
		t.Errorf("dry run opened %d services, stored %d", f.opened, len(f.svc.reqs))
	}
	out := f.out.String()
	for _, want := range []string{"dedup against existing records is not evaluated", "Title a", "would redact: no", "would redact: yes", "***AWS_KEY_REDACTED***", "2 to import, 0 skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "AKIAABCDEFGHIJKLMNOP") {
		t.Errorf("dry run printed an unscrubbed secret:\n%s", out)
	}
}

func TestImportRealRunSummaryAndReminder(t *testing.T) {
	f := newImportFixture(t)
	f.svc.out = func(req *memory.StoreRequest) (*memory.StoreResponse, error) {
		if strings.HasPrefix(req.Title, "Key") {
			return &memory.StoreResponse{Decision: memory.ActionSkip, SkipReason: "already imported"}, nil
		}
		return &memory.StoreResponse{ID: "3f2a91c0-0000-0000-0000-000000000000", Decision: memory.ActionAdd}, nil
	}
	if err := runImport(context.Background(), f.deps, []string{"automem", "--projects-dir", f.projDir}); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{"added 3f2a91c0", "skipped (already imported)", "added 1, skipped 1", "1 candidates await `claude-memory review`; unreviewed candidates are deleted after 90 days (MEMORY_CANDIDATE_TTL)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, r := range f.svc.reqs {
		if r.Source != "import" || r.ImportKey == "" || r.Namespace != "" {
			t.Errorf("request = %+v", r)
		}
	}
}

func TestImportNoReminderWhenNothingAdded(t *testing.T) {
	f := newImportFixture(t)
	f.svc.out = func(*memory.StoreRequest) (*memory.StoreResponse, error) {
		return &memory.StoreResponse{Decision: memory.ActionSkip, SkipReason: "already imported"}, nil
	}
	if err := runImport(context.Background(), f.deps, []string{"automem", "--projects-dir", f.projDir}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.out.String(), "await") {
		t.Errorf("reminder printed:\n%s", f.out.String())
	}
}

func TestImportValidationErrorSkipsInfraErrorStops(t *testing.T) {
	f := newImportFixture(t)
	f.svc.out = func(req *memory.StoreRequest) (*memory.StoreResponse, error) {
		if req.Title == "Title a" {
			return nil, fmt.Errorf("store validation: %w: content exceeds maximum size", memory.ErrInvalidRequest)
		}
		return nil, errors.New("embedding provider unavailable")
	}
	err := runImport(context.Background(), f.deps, []string{"automem", "--projects-dir", f.projDir})
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("err = %v, want exit 1", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "skipped (store validation: invalid request: content exceeds maximum size)") ||
		!strings.Contains(out, "error: embedding provider unavailable") || len(f.svc.reqs) != 2 {
		t.Errorf("output (%d stores):\n%s", len(f.svc.reqs), out)
	}
}

func TestImportInfraErrorStopsRemainingItems(t *testing.T) {
	f := newImportFixture(t)
	f.svc.out = func(*memory.StoreRequest) (*memory.StoreResponse, error) { return nil, errors.New("db down") }
	if err := runImport(context.Background(), f.deps, []string{"automem", "--projects-dir", f.projDir}); err == nil {
		t.Fatal("want error")
	}
	if len(f.svc.reqs) != 1 {
		t.Errorf("stores = %d, want 1 (stop at the first infra error)", len(f.svc.reqs))
	}
}

func TestImportNamespaceRules(t *testing.T) {
	t.Run("fallback without --namespace refuses, exit 2", func(t *testing.T) {
		f := newImportFixture(t)
		f.cwdNS, f.cwdWhy = "global", namespace.WhyFallback
		err := runImport(context.Background(), f.deps, []string{"automem", "--dry-run", "--projects-dir", f.projDir})
		if exitCode(err) != 2 || !strings.Contains(err.Error(), "no namespace mapping for") || !strings.Contains(err.Error(), "--namespace") {
			t.Errorf("err = %v (code %d)", err, exitCode(err))
		}
	})
	t.Run("explicit --namespace wins over the fallback", func(t *testing.T) {
		f := newImportFixture(t)
		f.cwdNS, f.cwdWhy = "global", namespace.WhyFallback
		if err := runImport(context.Background(), f.deps, []string{"automem", "--dry-run", "--namespace", "work", "--projects-dir", f.projDir}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.out.String(), "2 to import") {
			t.Errorf("output:\n%s", f.out.String())
		}
	})
	t.Run("invalid --namespace, exit 2", func(t *testing.T) {
		f := newImportFixture(t)
		err := runImport(context.Background(), f.deps, []string{"automem", "--namespace", "Bad Name"})
		if exitCode(err) != 2 {
			t.Errorf("err = %v (code %d)", err, exitCode(err))
		}
	})
	t.Run("items of another namespace are skipped", func(t *testing.T) {
		f := newImportFixture(t)
		if err := runImport(context.Background(), f.deps, []string{"automem", "--dry-run", "--namespace", "pet", "--projects-dir", f.projDir}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(f.out.String(), "other namespace (work)") || !strings.Contains(f.out.String(), "0 to import, 2 skipped") {
			t.Errorf("output:\n%s", f.out.String())
		}
	})
}

func TestImportUsageErrors(t *testing.T) {
	f := newImportFixture(t)
	for _, args := range [][]string{nil, {"bogus"}, {"automem", "extra"}, {"insights", "--projects-dir", "x"}, {"automem", "--bogus"}} {
		if err := runImport(context.Background(), f.deps, args); exitCode(err) != 2 {
			t.Errorf("args %v: err = %v (code %d), want exit 2", args, err, exitCode(err))
		}
	}
}

// import is reachable from run() and --dry-run needs no env file, DSN,
// Postgres or Ollama (lesson of PR 1: a command missing from run()'s switch
// is unreachable). The decoder is the real setup.DecodeProjectName over a
// temp tree.
func TestImportDispatchDryRunNeedsNoConfig(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(root, "work", "my-repo")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	enc := strings.ReplaceAll(proj, string(filepath.Separator), "-")
	memDir := filepath.Join(home, ".claude", "projects", enc, "memory")
	cfgDir := filepath.Join(home, ".config", "claude-memory")
	for _, d := range []string{memDir, cfgDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(memDir, "rule.md"), "---\ndescription: Prefer small PRs\nmetadata:\n  type: feedback\n---\nKeep them small.\n")
	write(filepath.Join(memDir, "MEMORY.md"), "- index\n")
	write(filepath.Join(memDir, "me.md"), "---\ndescription: About me\nmetadata:\n  type: user\n---\nx\n")
	write(filepath.Join(cfgDir, "namespaces.yaml"), "namespaces:\n  - namespace: testns\n    paths: [\""+filepath.Join(root, "work")+"/**\"]\n")

	out, code := runChild(t, []string{"HOME=" + home, "MEMORY_NAMESPACE=other"}, "import", "automem", "--dry-run", "--namespace", "testns")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"dedup against existing records is not evaluated", "Prefer small PRs", "repo=*", "MEMORY.md: index file", "me.md: type user", "1 to import, 2 skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, dsnRequired) || strings.Contains(out, "Keep them small") {
		t.Errorf("unexpected output:\n%s", out)
	}

	// MEMORY_NAMESPACE must not leak into the home dir's namespace: with
	// --namespace other, the item (namespaces.yaml says testns) is skipped.
	out, code = runChild(t, []string{"HOME=" + home, "MEMORY_NAMESPACE=other"}, "import", "automem", "--dry-run", "--namespace", "other")
	if code != 0 || !strings.Contains(out, "other namespace (testns)") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// A real run (no --dry-run) is dispatched too: it loads the config and fails
// on the missing DSN, instead of "unknown subcommand".
func TestImportDispatchRealRunLoadsConfig(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := runChild(t, []string{"HOME=" + home}, "import", "automem", "--namespace", "testns")
	if code != 1 || !strings.Contains(out, dsnRequired) || strings.Contains(out, "unknown subcommand") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestPrintableEscapesControlChars(t *testing.T) {
	if got := printable("a\x1b[31mb\nc"); strings.ContainsAny(got, "\x1b\n") {
		t.Errorf("printable = %q", got)
	}
}
