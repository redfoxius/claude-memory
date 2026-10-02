// Package record defines the domain types for memory records.
package record

import (
	"time"
)

// Kind represents the category of a memory record.
type Kind string

// GlobalNamespace is the shared namespace searched alongside the current one.
// Nothing is written to it automatically.
const GlobalNamespace = "global"

const (
	KindPattern   Kind = "pattern"
	KindDecision  Kind = "decision"
	KindGotcha    Kind = "gotcha"
	KindConvention Kind = "convention"
)

// IsValid returns true if the kind is a recognized value.
func (k Kind) IsValid() bool {
	switch k {
	case KindPattern, KindDecision, KindGotcha, KindConvention:
		return true
	}
	return false
}

// Status represents the lifecycle state of a record.
type Status string

const (
	StatusCandidate  Status = "candidate"
	StatusActive     Status = "active"
	StatusDeprecated Status = "deprecated"
)

// IsValid returns true if the status is a recognized value.
func (s Status) IsValid() bool {
	switch s {
	case StatusCandidate, StatusActive, StatusDeprecated:
		return true
	}
	return false
}

// Source represents the capture source of a record.
type Source string

const (
	SourceInline  Source = "inline"
	SourceSession Source = "session"
	SourcePR      Source = "pr"
	// SourceImport marks records created by the one-off `import` command.
	SourceImport Source = "import"
)

// IsValid returns true if the source is a recognized value.
func (s Source) IsValid() bool {
	switch s {
	case SourceInline, SourceSession, SourcePR, SourceImport:
		return true
	}
	return false
}

// Record represents a single memory unit.
// All fields are persisted to the database except Embedding, which is
// stored as a vector column.
type Record struct {
	// ID is a UUID uniquely identifying this record.
	ID string

	// Kind categorizes the record type.
	Kind Kind

	// Title is a short, semantic summary of the record.
	Title string

	// Content is the full record text in markdown.
	Content string

	// Namespace isolates records between projects/companies (e.g. "acme",
	// "pet-game"). All reads and writes are scoped to it; "global" holds
	// explicitly-shared, stack-generic facts visible from every namespace.
	Namespace string

	// Repo is the repository scope: a specific repo name (e.g., "billing-service")
	// or "*" for cross-repo availability.
	Repo string

	// Files is a list of file paths relevant to this record.
	Files []string

	// CommitSHA is an optional commit hash associated with the record.
	CommitSHA *string

	// Ticket is an optional ticket/issue identifier.
	Ticket *string

	// Tags are semantic labels for this record.
	Tags []string

	// Status indicates the lifecycle state.
	Status Status

	// DeprecationReason is set when Status is StatusDeprecated.
	DeprecationReason *string

	// SupersededBy is the ID of the record that replaced this one.
	SupersededBy *string

	// Source indicates where the record came from.
	Source Source

	// ImportKey is the dedup key of an imported record (nil otherwise). It is
	// write-only: Create stores it, Get/List never select it.
	ImportKey *string

	// Confidence is a score from 0.0 to 1.0 indicating trust in the record.
	Confidence float64

	// SeenCount is the number of times this record has been encountered again.
	SeenCount int

	// UsedCount is the number of times this record has been actively used/referenced.
	UsedCount int

	// CreatedAt is when the record was first stored.
	CreatedAt time.Time

	// UpdatedAt is when the record was last modified.
	UpdatedAt time.Time

	// LastUsedAt is when the record was last accessed or seen again.
	LastUsedAt *time.Time

	// Embedding is the 1024-dimensional vector embedding of title+tags+content.
	// It may be nil or zero if embedding generation failed and the record is
	// not searchable by semantic similarity.
	Embedding []float32
}

// New creates a new Record with provided fields and initializes computed fields.
// It does not perform validation; use Validate() to check the record before persistence.
func New(
	id string,
	kind Kind,
	title, content, repo string,
	source Source,
	confidence float64,
) *Record {
	now := time.Now().UTC()
	return &Record{
		ID:        id,
		Kind:      kind,
		Title:     title,
		Content:   content,
		Repo:      repo,
		Files:     []string{},
		Tags:      []string{},
		Status:    StatusCandidate,
		Source:    source,
		Confidence: confidence,
		CreatedAt: now,
		UpdatedAt: now,
	}
}
