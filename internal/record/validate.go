package record

import (
	"fmt"
	"unicode/utf8"
)

// ValidationError is returned when a record fails validation.
// Multiple validation errors may be aggregated.
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "validation error"
	}
	if len(e.Issues) == 1 {
		return "validation error: " + e.Issues[0]
	}
	result := "validation errors:\n"
	for _, issue := range e.Issues {
		result += "  - " + issue + "\n"
	}
	return result
}

// Validate checks that the record conforms to all domain rules.
// It returns a ValidationError if any violation is found.
// Parameters are provided as arguments rather than as fields of Config
// to maintain zero dependencies in the domain package.
func Validate(r *Record, maxContentChars int) *ValidationError {
	var issues []string

	// Validate Kind enum.
	if !r.Kind.IsValid() {
		issues = append(issues, fmt.Sprintf("kind %q is not recognized", r.Kind))
	}

	// Validate Status enum.
	if !r.Status.IsValid() {
		issues = append(issues, fmt.Sprintf("status %q is not recognized", r.Status))
	}

	// Validate Source enum.
	if !r.Source.IsValid() {
		issues = append(issues, fmt.Sprintf("source %q is not recognized", r.Source))
	}

	// Validate Title is not empty.
	if r.Title == "" {
		issues = append(issues, "title is required and cannot be empty")
	}

	// Validate Content size.
	contentLen := utf8.RuneCountInString(r.Content)
	if contentLen > maxContentChars {
		issues = append(issues, fmt.Sprintf(
			"content size %d characters exceeds the limit of %d",
			contentLen, maxContentChars,
		))
	}

	// Validate Repo is not empty.
	if r.Repo == "" {
		issues = append(issues, "repo is required and cannot be empty")
	}

	// Validate Confidence is in range.
	if r.Confidence < 0.0 || r.Confidence > 1.0 {
		issues = append(issues, fmt.Sprintf("confidence %.2f is out of range [0.0, 1.0]", r.Confidence))
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}

	return nil
}
