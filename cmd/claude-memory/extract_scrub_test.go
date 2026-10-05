package main

import (
	"strings"
	"testing"
)

// TestSessionScrubberIsWired pins that runExtract hands ProcessSession a real
// scrubber: non-nil, and it redacts a fake token.
func TestSessionScrubberIsWired(t *testing.T) {
	s := sessionScrubber()
	if s == nil {
		t.Fatal("sessionScrubber() is nil: transcripts would reach haiku unscrubbed")
	}
	tok := "ghp_" + strings.Repeat("x", 36)
	out, redacted := s.Scrub("token " + tok)
	if !redacted || strings.Contains(out, tok) {
		t.Errorf("token not redacted: %q", out)
	}
}
