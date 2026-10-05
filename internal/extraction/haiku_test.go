package extraction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/memory"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return b
}

func TestUnwrapHaikuEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"real empty array", string(readTestdata(t, "envelope_empty.json")), "[]", false},
		{"real fenced draft array", string(readTestdata(t, "envelope_draft.json")), "", false},
		{"real fenced decision", string(readTestdata(t, "envelope_decision.json")), `{"action":"ADD","target_id":null,"reason":"no match"}`, false},
		{"fenced empty array", `{"type":"result","subtype":"success","is_error":false,"result":"` + "```json\\n[]\\n```" + `"}`, "[]", false},
		{"fence without info string", `{"type":"result","subtype":"success","result":"` + "```\\n[1]\\n```" + `"}`, "[1]", false},
		{"unfenced array", `{"type":"result","subtype":"success","result":"[{\"a\":1}]"}`, `[{"a":1}]`, false},
		{"prose around json", `{"type":"result","subtype":"success","result":"Here you go: [1,2] done"}`, "[1,2]", false},
		{"is_error true", string(readTestdata(t, "envelope_error.json")), "", true},
		{"is_error flag only", `{"type":"result","is_error":true,"result":"boom"}`, "", true},
		{"non-success subtype", `{"type":"result","subtype":"error_max_turns","result":"x"}`, "", true},
		{"bare array passthrough", `[{"a":1}]`, `[{"a":1}]`, false},
		{"object without type passthrough", `{"action":"ADD"}`, `{"action":"ADD"}`, false},
		{"garbage", `{not json`, "", true},
		{"envelope with non-json text", `{"type":"result","subtype":"success","result":"I need clarification"}`, "I need clarification", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unwrapHaikuEnvelope([]byte(tc.in))
			if tc.wantErr {
				if !errors.Is(err, ErrHaikuFailed) {
					t.Fatalf("want ErrHaikuFailed, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != "" && strings.TrimSpace(string(got)) != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestUnwrapHaikuEnvelopeErrorTruncatesResult(t *testing.T) {
	long := strings.Repeat("x", 5000)
	_, err := unwrapHaikuEnvelope([]byte(`{"type":"result","is_error":true,"result":"` + long + `"}`))
	if err == nil || len(err.Error()) > 1000 {
		t.Fatalf("expected truncated error, got len %d", len(err.Error()))
	}
}

// stubClaude writes an executable that prints the given testdata file, so the
// real CLIHaikuRunner subprocess path runs without the real claude binary.
func stubClaude(t *testing.T, files ...string) *CLIHaikuRunner {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\n"
	counter := filepath.Join(dir, "n")
	script += "n=$(cat " + counter + " 2>/dev/null || echo 0)\necho $((n+1)) > " + counter + "\ncase $n in\n"
	for i, f := range files {
		abs, _ := filepath.Abs(filepath.Join("testdata", f))
		script += "  " + string(rune('0'+i)) + ") cat " + abs + " ;;\n"
	}
	script += "esac\n"
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &CLIHaikuRunner{timeout: 10 * time.Second, bin: bin}
}

// Real-shaped envelopes through the real runner, extractDrafts and the
// decision parser: drafts must parse and the decision Action must be set
// (the pre-fix behaviour was a silent empty Action and ADD fallback).
func TestEnvelopeEndToEnd(t *testing.T) {
	runner := stubClaude(t, "envelope_draft.json", "envelope_decision.json")

	drafts, err := extractDrafts(context.Background(), runner, "session", "text", "ref")
	if err != nil {
		t.Fatalf("extractDrafts error: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Title == "" {
		t.Fatalf("want 1 draft with title, got %+v", drafts)
	}

	out, err := runner.Run(context.Background(), "decide")
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseDecision(out)
	if err != nil {
		t.Fatalf("ParseDecision: %v", err)
	}
	if d.Action == "" || parseAction(d.Action) != memory.ActionAdd {
		t.Fatalf("Action not parsed: %q", d.Action)
	}
}

func TestCLIHaikuRunnerErrorEnvelope(t *testing.T) {
	runner := stubClaude(t, "envelope_error.json")
	_, err := runner.Run(context.Background(), "x")
	if !errors.Is(err, ErrHaikuFailed) {
		t.Fatalf("want ErrHaikuFailed, got %v", err)
	}
}

// The subprocess must carry the guard variable and the lean flag set, while
// the rest of the environment (auth, PATH) passes through.
func TestCLIHaikuRunnerArgsAndEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok-passthrough")
	t.Setenv(SubprocessEnv, "")
	dir := t.TempDir()
	dump := filepath.Join(dir, "dump")
	script := "#!/bin/sh\ncat >/dev/null\n{ printf 'ARGS:'; for a in \"$@\"; do printf '[%s]' \"$a\"; done; echo; env; } > " + dump + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"[]"}'` + "\n"
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := (&CLIHaikuRunner{timeout: 10 * time.Second, bin: bin}).Run(context.Background(), "p")
	if err != nil || strings.TrimSpace(string(out)) != "[]" {
		t.Fatalf("Run: %q, %v", out, err)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"ARGS:[-p][--model][haiku][--output-format][json][--no-session-persistence][--strict-mcp-config][--tools][]",
		"\n" + SubprocessEnv + "=1\n",
		"\nCLAUDE_CODE_OAUTH_TOKEN=tok-passthrough\n",
		"\nPATH=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("subprocess dump lacks %q:\n%s", want, got)
		}
	}
}
