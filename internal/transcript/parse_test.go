package transcript

import (
	"os"
	"strings"
	"testing"
)

// writeTemp writes content to a temp JSONL file and returns its path,
// registering cleanup with t.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	tmp, err := os.CreateTemp("", "transcript_test_*.jsonl")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(tmp.Name()) })

	if _, err := tmp.WriteString(content); err != nil {
		t.Fatalf("failed to write to temp file: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("failed to close temp file: %v", err)
	}
	return tmp.Name()
}

func TestParseMissingFile(t *testing.T) {
	_, err := Parse("/nonexistent/path/transcript.jsonl", Config{CharBudget: 5000})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if !IsNotFound(err) {
		t.Fatal("IsNotFound should return true")
	}
}

func TestParseMalformed(t *testing.T) {
	path := writeTemp(t, "{invalid json")

	_, err := Parse(path, Config{CharBudget: 5000})
	if err != ErrMalformed {
		t.Fatalf("expected ErrMalformed, got %v", err)
	}
	if !IsMalformed(err) {
		t.Fatal("IsMalformed should return true")
	}
}

// Fixtures below mirror the real Claude Code JSONL shape (as inspected
// under ~/.claude/projects/*/*.jsonl): a leading "mode" line with no cwd,
// "user"/"assistant" lines with a top-level "cwd" and a "message" field
// whose "content" is either a bare string or an array of typed content
// blocks (text/thinking/tool_use/tool_result/image), plus assorted
// session-management noise line types. All content here is synthetic.
func TestParseValid(t *testing.T) {
	transcript := `{"type":"mode","mode":"normal"}
{"type":"user","cwd":"/Users/test/work/myrepo","message":{"role":"user","content":"Hello"}}
{"type":"assistant","cwd":"/Users/test/work/myrepo","message":{"role":"assistant","content":[{"type":"text","text":"Hi there"}]}}
{"type":"user","cwd":"/Users/test/work/myrepo","message":{"role":"user","content":"Can you write a file?"}}
{"type":"assistant","cwd":"/Users/test/work/myrepo","message":{"role":"assistant","content":[{"type":"thinking","thinking":"planning..."},{"type":"tool_use","id":"toolu_1","name":"Write","input":{"file_path":"/tmp/out.txt","content":"data"}}]}}
`
	path := writeTemp(t, transcript)

	tr, err := Parse(path, Config{CharBudget: 5000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tr.MessageCount != 4 {
		t.Errorf("expected 4 messages, got %d", tr.MessageCount)
	}
	if !tr.HasFileEdits {
		t.Error("expected HasFileEdits to be true (Write tool present)")
	}
	if tr.Cwd != "/Users/test/work/myrepo" {
		t.Errorf("expected cwd to be set, got %q", tr.Cwd)
	}
	if tr.Repo != "myrepo" {
		t.Errorf("expected repo to be 'myrepo', got %q", tr.Repo)
	}

	wantLines := []string{
		"USER: Hello",
		"ASSISTANT: Hi there",
		"USER: Can you write a file?",
		"[tool: Write /tmp/out.txt]",
	}
	want := strings.Join(wantLines, "\n")
	if tr.CompactText != want {
		t.Errorf("CompactText mismatch:\ngot:  %q\nwant: %q", tr.CompactText, want)
	}
	// The thinking block's content must not leak into the rendering.
	if strings.Contains(tr.CompactText, "planning") {
		t.Error("expected thinking block to be excluded from CompactText")
	}
}

func TestParseNoFileEdits(t *testing.T) {
	transcript := `{"type":"user","cwd":"/repo","message":{"role":"user","content":"Hello"}}
{"type":"assistant","cwd":"/repo","message":{"role":"assistant","content":[{"type":"text","text":"Hi"}]}}
`
	path := writeTemp(t, transcript)

	tr, err := Parse(path, Config{CharBudget: 5000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.HasFileEdits {
		t.Error("expected HasFileEdits to be false")
	}
}

func TestInferRepo(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"/Users/test/work/billing-service", "billing-service"},
		{"/home/user/project/my-service", "my-service"},
		{"my-repo", "my-repo"},
		{"/", ""},
	}

	for _, tc := range testCases {
		got := inferRepo(tc.input)
		if got != tc.expected {
			t.Errorf("inferRepo(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// TestParseEditToolUse verifies HasFileEdits detection against the real
// content-block shape (message.content[].type=="tool_use", "name"=="Edit"),
// not a flattened/simplified stand-in for it.
func TestParseEditToolUse(t *testing.T) {
	transcript := `{"type":"user","cwd":"/repo","message":{"role":"user","content":"Edit the file"}}
{"type":"assistant","cwd":"/repo","message":{"role":"assistant","content":[{"type":"text","text":"OK"},{"type":"tool_use","id":"toolu_2","name":"Edit","input":{"file_path":"/repo/main.go","old_string":"a","new_string":"b"}}]}}
`
	path := writeTemp(t, transcript)

	tr, err := Parse(path, Config{CharBudget: 5000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tr.HasFileEdits {
		t.Error("expected HasFileEdits to be true (Edit tool present)")
	}
	if !strings.Contains(tr.CompactText, "[tool: Edit /repo/main.go]") {
		t.Errorf("expected tool_use marker in CompactText, got %q", tr.CompactText)
	}
}

// TestParseSkipsMetaAndNoise checks that isMeta turns and non-chat line
// types (system, attachment, file-history-snapshot, ...) are excluded from
// the rendering, even though isMeta lines still count toward MessageCount
// (they did occupy a conversational turn).
func TestParseSkipsMetaAndNoise(t *testing.T) {
	transcript := `{"type":"mode","mode":"normal"}
{"type":"system","subtype":"turn_duration","cwd":"/repo"}
{"type":"attachment","attachment":{"type":"prompt_snapshot"}}
{"type":"user","cwd":"/repo","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>noise</local-command-caveat>"}}
{"type":"user","cwd":"/repo","message":{"role":"user","content":"real question"}}
{"type":"assistant","cwd":"/repo","message":{"role":"assistant","content":[{"type":"text","text":"real answer"}]}}
`
	path := writeTemp(t, transcript)

	tr, err := Parse(path, Config{CharBudget: 5000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// isMeta line + 2 real turns = 3 user/assistant-typed lines.
	if tr.MessageCount != 3 {
		t.Errorf("expected 3 messages, got %d", tr.MessageCount)
	}
	if strings.Contains(tr.CompactText, "noise") {
		t.Errorf("expected isMeta content excluded, got %q", tr.CompactText)
	}
	want := "USER: real question\nASSISTANT: real answer"
	if tr.CompactText != want {
		t.Errorf("CompactText = %q, want %q", tr.CompactText, want)
	}
}

// TestParseToolResult checks that tool_result blocks render truncated and
// labeled, including the is_error case, without leaking into HasFileEdits.
func TestParseToolResult(t *testing.T) {
	longOutput := strings.Repeat("x", 500)
	transcript := `{"type":"assistant","cwd":"/repo","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_3","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","cwd":"/repo","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":"` + longOutput + `","is_error":false}]}}
{"type":"assistant","cwd":"/repo","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_4","name":"Bash","input":{"command":"false"}}]}}
{"type":"user","cwd":"/repo","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_4","content":"boom","is_error":true}]}}
`
	path := writeTemp(t, transcript)

	tr, err := Parse(path, Config{CharBudget: 100000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr.HasFileEdits {
		t.Error("Bash is not a file-edit tool; expected HasFileEdits false")
	}
	if !strings.Contains(tr.CompactText, "TOOL_RESULT: "+strings.Repeat("x", toolResultCharCap)+"...[truncated]") {
		t.Errorf("expected truncated tool_result body, got %q", tr.CompactText)
	}
	if !strings.Contains(tr.CompactText, "TOOL_RESULT (error): boom") {
		t.Errorf("expected labeled error tool_result, got %q", tr.CompactText)
	}
}

// TestBuildCompactTextTruncatesKeepingTail verifies the char-budget
// behavior directly: when over budget, the head is dropped, the tail is
// kept, and the cut is marked.
func TestBuildCompactTextTruncatesKeepingTail(t *testing.T) {
	lines := []string{"USER: first", "ASSISTANT: second", "USER: third and final line"}
	got := buildCompactText(lines, 10)

	if !strings.HasPrefix(got, truncatedHeadMarker) {
		t.Errorf("expected truncated marker prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "final line") {
		t.Errorf("expected tail to be kept, got %q", got)
	}
	if strings.Contains(got, "first") {
		t.Errorf("expected head to be dropped, got %q", got)
	}
}

func TestBuildCompactTextUnderBudget(t *testing.T) {
	lines := []string{"USER: hi", "ASSISTANT: hello"}
	got := buildCompactText(lines, 5000)
	want := "USER: hi\nASSISTANT: hello"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildCompactTextEmpty(t *testing.T) {
	if got := buildCompactText(nil, 5000); got != "" {
		t.Errorf("expected empty string for no renderable lines, got %q", got)
	}
}
