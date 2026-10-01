package scrub

// Adapter wraps the Scrubber to satisfy the memory.Scrubber interface.
// The memory service expects Scrub(text string) (redacted string, wasRedacted bool),
// but our Scrubber returns *RedactionResult.
type Adapter struct {
	*Scrubber
}

// NewAdapter creates a new Adapter wrapping a Scrubber.
func NewAdapter(s *Scrubber) *Adapter {
	return &Adapter{s}
}

// Scrub redacts common secret patterns from text,
// returning the redacted text and whether any redactions occurred.
// This method adapts the Scrubber's *RedactionResult API to the interface
// expected by internal/memory.
func (a *Adapter) Scrub(text string) (redacted string, wasRedacted bool) {
	result := a.Scrubber.Scrub(text)
	return result.Text, result.Redacted
}
