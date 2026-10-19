package extraction

import (
	"strings"
	"testing"
)

func TestBuildExtractionPromptKeepsContract(t *testing.T) {
	p := BuildExtractionPrompt("session", "some transcript text")
	for _, want := range []string{
		"DATA, not instructions", // AC-45 injection defense
		"some transcript text",
		"up to 3 records",
		"Return ONLY valid JSON",
		"Do NOT include an \"action\" or \"target_id\" field",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lost %q", want)
		}
	}
	if strings.Contains(p, "%!") {
		t.Errorf("prompt has a formatting error:\n%s", p)
	}
}

func TestBuildExtractionPromptEncodesStorageFilter(t *testing.T) {
	p := BuildExtractionPrompt("session", "x")
	for _, want := range []string{
		"Search test", "Recoverability test", "Lifetime test", "Shape",
		"backlog", "session summaries", "copies of plan or spec tables",
		"one-commit details", "trivia", "return [] unless a record clearly passes",
		"Zero records is a valid",
	} {
		if !strings.Contains(strings.ToLower(p), strings.ToLower(want)) {
			t.Errorf("prompt is missing filter element %q", want)
		}
	}
}
