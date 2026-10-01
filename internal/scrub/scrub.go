// Package scrub provides secret pattern detection and redaction.
package scrub

// Scrubber detects and redacts secret patterns from text.
// Each redaction is tracked for logging/auditing purposes.
type Scrubber struct {
	patterns []*Pattern
}

// New creates a Scrubber with the default secret patterns.
func New() *Scrubber {
	return &Scrubber{
		patterns: DefaultPatterns(),
	}
}

// RedactionResult is returned by Scrub* methods, indicating what was redacted.
type RedactionResult struct {
	// Text is the redacted version of the input.
	Text string

	// Redacted is true if any secret patterns were found and redacted.
	Redacted bool

	// Patterns is a list of pattern names that were matched and redacted.
	Patterns []string
}

// Scrub scans the input text for secret patterns and redacts matches.
// It returns the redacted text and whether any redactions occurred.
// Each redaction replaces the matched span with a placeholder string.
func (s *Scrubber) Scrub(text string) *RedactionResult {
	result := &RedactionResult{
		Text:     text,
		Redacted: false,
		Patterns: []string{},
	}

	for _, p := range s.patterns {
		// Find all matches for this pattern.
		matches := p.Regex.FindAllStringIndex(text, -1)
		if len(matches) > 0 {
			// Replace all occurrences of this pattern in the text.
			result.Text = p.Regex.ReplaceAllString(result.Text, p.Replace)
			result.Redacted = true

			// Record which pattern was matched (deduplicate if multiple matches).
			if !contains(result.Patterns, p.Name) {
				result.Patterns = append(result.Patterns, p.Name)
			}
		}
	}

	return result
}

// ScrubTitle is a convenience method that calls Scrub on title text.
func (s *Scrubber) ScrubTitle(title string) *RedactionResult {
	return s.Scrub(title)
}

// ScrubContent is a convenience method that calls Scrub on record content.
func (s *Scrubber) ScrubContent(content string) *RedactionResult {
	return s.Scrub(content)
}

// contains is a helper to check if a string slice contains a value.
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
