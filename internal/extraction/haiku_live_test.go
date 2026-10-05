//go:build live

package extraction

import (
	"context"
	"testing"
	"time"
)

// TestLiveHaikuExtraction runs the real `claude` CLI on a tiny synthetic input.
// Opt-in: go test -tags live -run TestLiveHaikuExtraction ./internal/extraction
// It makes one paid API call and writes nothing to the database.
func TestLiveHaikuExtraction(t *testing.T) {
	runner := NewCLIHaikuRunner(90 * time.Second)
	prompt := BuildExtractionPrompt("session", "User: why does GET /items/42 return 500?\n"+
		"Assistant: pgx returns ErrNoRows when nothing matches; internal/api/items.go must map it to 404.\n"+
		"Fixed by mapping pgx.ErrNoRows to http.StatusNotFound.")

	out, err := runner.Run(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := ParseHaikuOutput(out)
	if err != nil {
		t.Fatalf("ParseHaikuOutput: %v (output: %.300s)", err, out)
	}
	valid := 0
	for _, r := range raw {
		if _, err := ParseDraftRecord(r); err == nil {
			valid++
		}
	}
	t.Logf("haiku returned %d candidate(s), %d valid draft(s)", len(raw), valid)
}
