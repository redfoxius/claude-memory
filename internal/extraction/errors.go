package extraction

import "errors"

// ErrHaikuFailed is returned when the haiku subprocess fails, times out,
// or returns output that cannot be parsed as JSON (AC-25).
var ErrHaikuFailed = errors.New("haiku subprocess failed")

// ErrInvalidSchema is returned when a draft record does not conform to the
// expected JSON schema (AC-45).
var ErrInvalidSchema = errors.New("invalid schema")

// ErrExtractionFailed marks an infrastructure failure during extraction (the
// haiku call failed, its envelope reported an error, its output was
// unparsable, or every store failed). Unlike a legitimate skip (nothing worth
// extracting) the input was not really processed, so callers that track
// progress (ingest-pr's cursor) must not treat it as done.
var ErrExtractionFailed = errors.New("extraction failed")
