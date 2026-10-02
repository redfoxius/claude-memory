package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeEnvFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // umask-proof
		t.Fatal(err)
	}
	return path
}

type wantFinding struct {
	Line int
	Kind FindingKind
	Key  string
}

func findingsOf(ef *EnvFile) []wantFinding {
	var out []wantFinding
	for _, f := range ef.Findings {
		out = append(out, wantFinding{f.Line, f.Kind, f.Key})
	}
	return out
}

// TestParseEnvFile covers each AC-27 finding kind, the parsed values and
// their order.
func TestParseEnvFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		content  string
		mode     os.FileMode
		values   map[string]string
		order    []string
		findings []wantFinding
	}{
		{
			name:    "plain",
			content: "# comment\n\nMEMORY_PG_DSN=postgresql://u:p%2Fw@h:5432/db?sslmode=prefer\n  MEMORY_OLLAMA_MODEL = bge-m3  \nEMPTY=\n",
			mode:    0o600,
			values: map[string]string{
				"MEMORY_PG_DSN":       "postgresql://u:p%2Fw@h:5432/db?sslmode=prefer",
				"MEMORY_OLLAMA_MODEL": "bge-m3",
				"EMPTY":               "",
			},
			order: []string{"MEMORY_PG_DSN", "MEMORY_OLLAMA_MODEL", "EMPTY"},
		},
		{
			name:     "export prefix (DEPLOY.md form) is rejected",
			content:  "export MEMORY_PG_DSN=postgresql://u:p@h/db\nMEMORY_OLLAMA_URL=http://127.0.0.1:11434\n",
			mode:     0o600,
			values:   map[string]string{"MEMORY_OLLAMA_URL": "http://127.0.0.1:11434"},
			order:    []string{"MEMORY_OLLAMA_URL"},
			findings: []wantFinding{{1, FindingExportPrefix, "MEMORY_PG_DSN"}},
		},
		{
			name:    "export with quoted value reports both",
			content: "export\tMEMORY_PG_DSN=\"postgresql://u:p@h/db\"\n",
			mode:    0o600,
			values:  map[string]string{},
			findings: []wantFinding{
				{1, FindingExportPrefix, "MEMORY_PG_DSN"},
				{1, FindingQuotedWholeValue, "MEMORY_PG_DSN"},
			},
		},
		{
			name:     "exported is a key, not the export keyword",
			content:  "exported=1\n",
			mode:     0o600,
			values:   map[string]string{"exported": "1"},
			order:    []string{"exported"},
			findings: nil,
		},
		{
			name:    "quoted whole value is kept verbatim",
			content: "A=\"value with spaces\"\nB='single'\n",
			mode:    0o600,
			values:  map[string]string{"A": `"value with spaces"`, "B": "'single'"},
			order:   []string{"A", "B"},
			findings: []wantFinding{
				{1, FindingQuotedWholeValue, "A"},
				{2, FindingQuotedWholeValue, "B"},
			},
		},
		{
			name: "unparseable values",
			content: "A=$(cat secret)\nB=${HOME}/x\nC=$HOME/x\nD=back\\slash\nE=\"unbalanced\nF=it's\n" +
				"G=\"has $dollar\"\nH=`cmd`\nI=\"a\"b\"\n",
			mode: 0o600,
			values: map[string]string{
				"A": "$(cat secret)", "B": "${HOME}/x", "C": "$HOME/x", "D": `back\slash`,
				"E": `"unbalanced`, "F": "it's", "G": `"has $dollar"`, "H": "`cmd`", "I": `"a"b"`,
			},
			order: []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"},
			findings: []wantFinding{
				{1, FindingUnparseableValue, "A"}, {2, FindingUnparseableValue, "B"},
				{3, FindingUnparseableValue, "C"}, {4, FindingUnparseableValue, "D"},
				{5, FindingUnparseableValue, "E"}, {6, FindingUnparseableValue, "F"},
				{7, FindingUnparseableValue, "G"}, {8, FindingUnparseableValue, "H"},
				{9, FindingUnparseableValue, "I"},
			},
		},
		{
			name:    "a lone dollar or digit after it is plain text",
			content: "PW=p$4ss&w0rd\n",
			mode:    0o600,
			values:  map[string]string{"PW": "p$4ss&w0rd"},
			order:   []string{"PW"},
		},
		{
			name:     "invalid key",
			content:  "BAD KEY=1\n=2\n",
			mode:     0o600,
			values:   map[string]string{},
			findings: []wantFinding{{1, FindingUnparseableValue, "BAD KEY"}, {2, FindingUnparseableValue, ""}},
		},
		{
			name:     "duplicate: first non-empty wins",
			content:  "A=\nA=one\nA=two\n",
			mode:     0o600,
			values:   map[string]string{"A": "one"},
			order:    []string{"A"},
			findings: []wantFinding{{2, FindingDuplicate, "A"}, {3, FindingDuplicate, "A"}},
		},
		{
			name:     "no equals",
			content:  "JUSTAWORD\nA=1\n",
			mode:     0o600,
			values:   map[string]string{"A": "1"},
			order:    []string{"A"},
			findings: []wantFinding{{1, FindingNoEquals, ""}},
		},
		{
			name:     "group/world readable is a finding, not an error",
			content:  "A=1\nexport B=2\n",
			mode:     0o644,
			values:   map[string]string{"A": "1"},
			order:    []string{"A"},
			findings: []wantFinding{{0, FindingGroupWorldReadable, ""}, {2, FindingExportPrefix, "B"}},
		},
		{
			name:    "value keeps everything after the first equals",
			content: "URL=http://h/x?a=b=c\n",
			mode:    0o600,
			values:  map[string]string{"URL": "http://h/x?a=b=c"},
			order:   []string{"URL"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeEnvFile(t, tc.content, tc.mode)
			before := os.Environ()
			ef, err := ParseEnvFile(path)
			if err != nil {
				t.Fatalf("ParseEnvFile: %v", err)
			}
			if !reflect.DeepEqual(os.Environ(), before) {
				t.Error("ParseEnvFile changed the process environment")
			}
			if ef.Mode != tc.mode {
				t.Errorf("Mode = %#o, want %#o", ef.Mode, tc.mode)
			}
			if !reflect.DeepEqual(ef.Values, tc.values) {
				t.Errorf("Values = %#v, want %#v", ef.Values, tc.values)
			}
			if !reflect.DeepEqual(ef.Order, tc.order) {
				t.Errorf("Order = %#v, want %#v", ef.Order, tc.order)
			}
			if got := findingsOf(ef); !reflect.DeepEqual(got, tc.findings) {
				t.Errorf("Findings = %+v, want %+v", got, tc.findings)
			}
			for _, f := range ef.Findings {
				if f.Detail == "" {
					t.Errorf("finding %+v has no detail", f)
				}
			}
		})
	}
}

func TestParseEnvFileMissing(t *testing.T) {
	t.Parallel()
	_, err := ParseEnvFile(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

// TestLoadFromFileSkipsExportAndKeepsFirstValue pins LoadFromFile's
// behavior on top of ParseEnvFile: an `export` line is not loaded (it used
// to set a variable literally named "export KEY"), quoted values keep their
// quotes, and the process environment wins over the file.
func TestLoadFromFileSkipsExportAndKeepsFirstValue(t *testing.T) {
	clearMemoryEnv(t)
	t.Setenv("MEMORY_OLLAMA_URL", "http://from-env:11434")
	path := writeEnvFile(t, "export MEMORY_PG_DSN=postgres://x\n"+
		"MEMORY_OLLAMA_MODEL=\"quoted\"\n"+
		"MEMORY_OLLAMA_URL=http://from-file:11434\n"+
		"MEMORY_EMBED_MAX_TOKENS=\nMEMORY_EMBED_MAX_TOKENS=512\n", 0o600)
	if err := LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	if v, ok := os.LookupEnv("MEMORY_PG_DSN"); ok {
		t.Errorf("MEMORY_PG_DSN set from an export line: %q", v)
	}
	if _, ok := os.LookupEnv("export MEMORY_PG_DSN"); ok {
		t.Error(`variable "export MEMORY_PG_DSN" was set`)
	}
	if got := os.Getenv("MEMORY_OLLAMA_MODEL"); got != `"quoted"` {
		t.Errorf("MEMORY_OLLAMA_MODEL = %q, want the quotes kept", got)
	}
	if got := os.Getenv("MEMORY_OLLAMA_URL"); got != "http://from-env:11434" {
		t.Errorf("MEMORY_OLLAMA_URL = %q, the environment must win", got)
	}
	if got := os.Getenv("MEMORY_EMBED_MAX_TOKENS"); got != "512" {
		t.Errorf("MEMORY_EMBED_MAX_TOKENS = %q, want 512", got)
	}
}
