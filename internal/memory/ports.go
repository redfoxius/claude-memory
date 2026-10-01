// Package memory provides the application service for semantic memory management.
// It declares interfaces for all external concerns (Store, EmbeddingProvider, Scrubber, Clock)
// and orchestrates the search/dedup/write logic without importing concrete implementations.
package memory

import (
	"context"
	"errors"
	"time"

	"claude-memory/internal/record"
)

// Store is the port through which the service persists and retrieves records.
// The concrete implementation (e.g., postgres.Store) must be wired in the composition root.
type Store interface {
	// Create persists a new record and returns the stored record with all fields set.
	Create(ctx context.Context, r *record.Record) (*record.Record, error)

	// Get retrieves a record by ID, or returns ErrNotFound if it does not exist.
	Get(ctx context.Context, id string) (*record.Record, error)

	// Update modifies fields of an existing record and returns the updated record,
	// or returns ErrNotFound if it does not exist.
	Update(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error)

	// Search performs a hybrid (semantic + full-text) ranked search, including:
	// - fetching top-N results ranked by fused score (pgvector cosine + tsvector BM25)
	// - excluding deprecated records by default
	// - flagging candidate records as unverified.
	// If embeddings are provided (not nil), use them for semantic ranking;
	// if the embedding provider fails during search (degraded=true), fall back to full-text-only.
	Search(ctx context.Context, query string, embedding []float32, repo string, options SearchOptions) (*SearchResult, error)

	// FindCandidates fetches the top-N nearest records in the same repo (or repo="*")
	// using the same fused ranking as Search, used before a write to check for dedup.
	// The returned candidates include their ID, title, and fused score, but not full content.
	FindCandidates(ctx context.Context, embedding []float32, namespace, repo string, limit int) ([]*Candidate, error)

	// List returns all records matching the given filters (all optional).
	List(ctx context.Context, filters ListFilters) ([]*record.Record, error)

	// WithTx runs fn inside one database transaction: commit if fn returns nil,
	// rollback otherwise. The write path (dedup/merge/SUPERSEDE) runs entirely
	// inside it, so the advisory lock and all writes are atomic (AC-16, AC-17).
	WithTx(ctx context.Context, fn func(tx TxStore) error) error

	// DeleteCandidatesByTTL hard-deletes candidate records untouched for longer than ttlDays,
	// preserving active and deprecated records regardless of age.
	// Returns the number of records deleted.
	DeleteCandidatesByTTL(ctx context.Context, ttlDays int) (int, error)
}

// TxStore is the subset of Store available inside WithTx. Every call runs in
// the enclosing transaction.
type TxStore interface {
	// AcquireLock takes a transaction-scoped advisory lock
	// (pg_advisory_xact_lock) keyed on repo + normalized-title hash; it is
	// released automatically on commit/rollback (AC-16).
	AcquireLock(ctx context.Context, namespace, repo string, titleHash string) error
	FindCandidates(ctx context.Context, embedding []float32, namespace, repo string, limit int) ([]*Candidate, error)
	Get(ctx context.Context, id string) (*record.Record, error)
	Create(ctx context.Context, r *record.Record) (*record.Record, error)
	Update(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error)
}

// EmbeddingProvider is the port for semantic embedding generation.
// The concrete implementation (e.g., ollama.Embedder) must be wired in the composition root.
type EmbeddingProvider interface {
	// Embed returns a 1024-dimensional embedding for the given text,
	// or an error if the provider is unreachable.
	// The implementation must truncate input to maxTokens before sending to the model.
	Embed(ctx context.Context, text string, maxTokens int) ([]float32, error)
}

// Scrubber is the port for secret/PII redaction.
// The concrete implementation (e.g., scrub.Scrubber) must be wired in the composition root.
type Scrubber interface {
	// Scrub redacts common secret patterns (API keys, tokens, passwords, JWTs, etc.)
	// from the given text, returning the redacted text and a boolean indicating
	// whether any redaction occurred.
	Scrub(text string) (redacted string, wasRedacted bool)
}

// Clock is the port for time, allowing tests to mock time.Now().
type Clock interface {
	// Now returns the current time in UTC.
	Now() time.Time
}

// SearchOptions configures a search query.
type SearchOptions struct {
	// Kind filters by record kind (optional).
	Kind *record.Kind

	// Namespaces restricts the search to these namespaces (the caller's own,
	// plus "global"). The service always sets it; the store never searches
	// across namespaces it was not given.
	Namespaces []string

	// Tags filters by one or more tags; a record matches if it contains any of the requested tags.
	Tags []string

	// Limit is the maximum number of results to return (default 5).
	Limit int

	// IncludeDeprecated, if false (the default), excludes deprecated records from results.
	IncludeDeprecated bool
}

// SearchResult is the result of a hybrid search.
type SearchResult struct {
	// Records are the ranked results, with unverified status and score.
	Records []*SearchRecord

	// Degraded is true if the embedding provider was unavailable and
	// the search fell back to full-text-only ranking.
	Degraded bool
}

// SearchRecord is a single search result, with enough data to show in a card
// (title, repo, id) and to measure relevance (score, status).
type SearchRecord struct {
	ID         string
	Kind       record.Kind
	Title      string
	Repo       string
	Tags       []string
	Status     record.Status
	Confidence float64
	Score      float64 // fused (RRF) rank score — ordering only
	// Similarity is the cosine similarity of the query embedding to this
	// record (0 when the search ran degraded / full-text-only). The hook
	// threshold (AC-32, config.Config.HookSimThreshold) compares against
	// this, not Score.
	Similarity float64
	Unverified bool // True if Status is StatusCandidate.
}

// Candidate is a record fetched during the dedup phase before a write.
// It includes only the fields needed for comparison (ID, title, score).
type Candidate struct {
	ID    string
	Title string
	// Score is the fused (RRF) rank score — for ordering only.
	Score float64
	// Similarity is the cosine similarity (1 - cosine distance) between the
	// write's embedding and this record's embedding, in [-1, 1]. The
	// dedup thresholds (AC-15: config.Config.StoreSimAsk /
	// config.Config.StoreSimUpdate) compare against this, never against
	// Score.
	Similarity float64
}

// StoreRequest is the input to Store, corresponding to the memory_store MCP tool.
type StoreRequest struct {
	Kind              record.Kind
	Title             string
	Content           string
	// Namespace overrides the service's namespace for this write. Only
	// record.GlobalNamespace is accepted (explicit shared facts); empty means
	// the service's own namespace.
	Namespace         string
	Repo              string
	Files             []string
	CommitSHA         *string
	Ticket            *string
	Tags              []string
	Source            record.Source
	Confidence        *float64 // If nil, defaults are applied per source (AC-29).
	ExtractionDecision *ExtractionDecision // Optional; honored only for session/pr sources.
}

// ExtractionDecision is the action chosen by the haiku extraction for session/PR paths,
// provided to Service.Store to guide the write-path dedup logic.
// For inline paths, the service computes the decision itself based on thresholds.
type ExtractionDecision struct {
	Action   WriteAction
	TargetID *string // Used for UPDATE/SUPERSEDE to identify the target record.
}

// WriteAction is the dedup/merge decision: what to do when storing a new fact.
type WriteAction string

const (
	ActionAdd       WriteAction = "ADD"       // New record.
	ActionUpdate    WriteAction = "UPDATE"    // Enriches an existing record.
	ActionSupersede WriteAction = "SUPERSEDE" // New fact replaces old one; old becomes deprecated.
	ActionNoop      WriteAction = "NOOP"      // Existing record seen again; increment seen_count.
)

// StoreResponse is the output of Store, corresponding to the memory_store MCP tool.
type StoreResponse struct {
	ID                   string
	Decision             WriteAction
	CandidatesConsidered []*Candidate
}

// UpdateRequest is the input to Update, corresponding to the memory_update MCP tool.
type UpdateRequest struct {
	ID         string
	Title      *string
	Content    *string
	Tags       []string
	Files      []string
	Ticket     *string
	Status     *record.Status
	Confidence *float64
}

// DeprecateRequest is the input to Deprecate, corresponding to the memory_deprecate MCP tool.
type DeprecateRequest struct {
	ID          string
	Reason      string
	SupersededBy *string
}

// ListFilters configures the List query.
type ListFilters struct {
	Namespace *string
	Repo   *string
	Kind   *record.Kind
	Status *record.Status
}

// FeedbackRequest is the input to Feedback, corresponding to the memory_feedback MCP tool.
type FeedbackRequest struct {
	ID      string
	Outcome FeedbackOutcome
	Note    *string
}

// FeedbackOutcome is the user's assessment of a record.
type FeedbackOutcome string

const (
	FeedbackUseful   FeedbackOutcome = "useful"
	FeedbackOutdated FeedbackOutcome = "outdated"
	FeedbackWrong    FeedbackOutcome = "wrong"
)

// FeedbackResponse is the output of Feedback.
type FeedbackResponse struct {
	ID        string
	NewStatus record.Status
}

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("record not found")
