package extraction

import "errors"

// ErrHaikuFailed is returned when the haiku subprocess fails, times out,
// or returns output that cannot be parsed as JSON (AC-25).
var ErrHaikuFailed = errors.New("haiku subprocess failed")

// ErrInvalidSchema is returned when a draft record does not conform to the
// expected JSON schema (AC-45).
var ErrInvalidSchema = errors.New("invalid schema")
