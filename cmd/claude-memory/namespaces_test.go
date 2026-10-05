package main

import (
	"bytes"
	"github.com/redfoxius/claude-memory/internal/namespace"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNamespacesFile writes body to a temp namespaces.yaml (never the real
// HOME) and returns its path.
func writeNamespacesFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "namespaces.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestListNamespaces_NoFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing.yaml")
	var buf bytes.Buffer
	if err := listNamespaces(&buf, p, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"NAMESPACE", "(shared; searched together with every namespace)", "default: global (built-in)", "namespaces init"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestListNamespaces_OnlyDefault(t *testing.T) {
	p := writeNamespacesFile(t, "default: personal\n")
	var buf bytes.Buffer
	if err := listNamespaces(&buf, p, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "personal") || !strings.Contains(out, "(default for unmatched directories)") {
		t.Errorf("default namespace not listed:\n%s", out)
	}
	if !strings.Contains(out, "default: personal\n") || strings.Contains(out, "built-in") || strings.Contains(out, "namespaces init") {
		t.Errorf("unexpected trailer:\n%s", out)
	}
	if strings.Index(out, "personal") > strings.Index(out, "global") {
		t.Errorf("global must be last:\n%s", out)
	}
}

func TestListNamespaces_MultipleGlobs(t *testing.T) {
	p := writeNamespacesFile(t, `default: global
namespaces:
  - namespace: pet-game
    paths: ["~/src/pet-game", "~/src/pet-game/**"]
  - namespace: work
    paths: ["~/work/acme/**"]
`)
	var buf bytes.Buffer
	if err := listNamespaces(&buf, p, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	want := []string{
		"NAMESPACE   PATHS",
		"pet-game    ~/src/pet-game",
		"            ~/src/pet-game/**",
		"work        ~/work/acme/**",
		"global      (shared; searched together with every namespace)",
		"default: global",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestListNamespaces_JSON(t *testing.T) {
	p := writeNamespacesFile(t, `default: personal
namespaces:
  - namespace: work
    paths: ["~/work/acme/**"]
`)
	var buf bytes.Buffer
	if err := listNamespaces(&buf, p, true); err != nil {
		t.Fatal(err)
	}
	var got namespacesListJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if got.Default != "personal" || len(got.Namespaces) != 3 {
		t.Fatalf("got %+v", got)
	}
	if e := got.Namespaces[0]; e.Namespace != "personal" || !e.IsDefault {
		t.Errorf("entry 0: %+v", e)
	}
	if e := got.Namespaces[1]; e.Namespace != "work" || e.IsDefault || len(e.Paths) != 1 {
		t.Errorf("entry 1: %+v", e)
	}
	if !strings.Contains(buf.String(), `"paths": []`) {
		t.Errorf("empty paths must be [] not null:\n%s", buf.String())
	}
}

func TestCmdNamespaces_UsageMentionsList(t *testing.T) {
	if !strings.Contains(namespacesUsage, "namespaces list") {
		t.Error("namespacesUsage must mention list")
	}
	err := cmdNamespaces([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), `unknown namespaces command "bogus"`) || !strings.Contains(err.Error(), "namespaces list") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNamespacesAddRefusesMalformedPRIngest(t *testing.T) {
	body := "namespaces:\n  - namespace: s\n    paths: [\"/s\"]\n    pr_ingest: false\n"
	p := writeNamespacesFile(t, body)
	err := namespace.Add(p, "s", "/more")
	if err == nil || !strings.Contains(err.Error(), "fix pr_ingest in "+p+" first") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != body {
		t.Errorf("file changed: %q", got)
	}
}
