package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/record"
)

// Service orchestrates the memory management logic: search, dedup/merge, write-path decisions.
// It depends only on interfaces (ports) it declares in this package, never on concrete
// implementations (postgres, ollama) — those are wired in the composition root.
type Service struct {
	store             Store
	embeddingProvider EmbeddingProvider
	scrubber          Scrubber
	clock             Clock
	cfg               *config.Config
	namespace         string
	events            EventSink // nil = no events

	// Staleness checking (all optional; see staleness.go).
	history       CodeHistory
	checkout      *Checkout
	pinnedHead    string
	headPinned    bool
	staleCeiling  time.Duration
	staleDeadline time.Time
}

// DefaultNamespace is used when no namespace is configured or resolvable:
// the shared global namespace.
const DefaultNamespace = record.GlobalNamespace

// New constructs a Service with all required dependencies.
// All adapters must implement the port interfaces declared in ports.go.
func New(
	store Store,
	embeddingProvider EmbeddingProvider,
	scrubber Scrubber,
	clock Clock,
	cfg *config.Config,
) *Service {
	ns := cfg.Namespace
	if ns == "" {
		ns = DefaultNamespace
	}
	return &Service{
		store:             store,
		embeddingProvider: embeddingProvider,
		scrubber:          scrubber,
		clock:             clock,
		cfg:               cfg,
		namespace:         ns,
	}
}

// WithNamespace returns a copy of the service scoped to another namespace.
// Used by callers (PR ingest) that handle several namespaces in one run.
func (s *Service) WithNamespace(ns string) *Service {
	c := *s
	c.namespace = ns
	return &c
}

// Namespace reports the namespace the service reads and writes.
func (s *Service) Namespace() string { return s.namespace }

// searchNamespaces is the set a search covers: the service's own namespace
// plus the explicit "global" one.
func (s *Service) searchNamespaces() []string {
	if s.namespace == record.GlobalNamespace {
		return []string{record.GlobalNamespace}
	}
	return []string{s.namespace, record.GlobalNamespace}
}

// accessible reports whether a record may be read or modified by this
// service: it must belong to the service's namespace or to "global".
func (s *Service) accessible(r *record.Record) bool {
	return r.Namespace == s.namespace || r.Namespace == record.GlobalNamespace
}

// getAccessible loads a record by id and hides records of other namespaces
// behind ErrNotFound, so ids never leak across namespaces.
func (s *Service) getAccessible(ctx context.Context, id string) (*record.Record, error) {
	rec, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.accessible(rec) {
		return nil, ErrNotFound
	}
	return rec, nil
}

// Cfg exposes the service's configuration (thresholds, limits) for callers
// that need to report on or compare against the configured defaults, such
// as the evalset harness's threshold recommendations.
func (s *Service) Cfg() *config.Config {
	return s.cfg
}

// Search performs a hybrid semantic + full-text search using the query text,
// optionally including a pre-computed embedding for the query (for efficiency
// when the embedding was already computed upstream).
// If no embedding is provided, Search computes one from the query text.
// Search respects the configured thresholds and returns results ranked by fused score.
// If the embedding provider is unavailable, Search degrades to full-text-only (AC-7).
// Deprecated records are excluded by default; candidates are flagged as unverified.
func (s *Service) Search(ctx context.Context, req *SearchRequest) (*SearchResult, error) {
	if req == nil {
		return nil, errors.New("search request is required")
	}

	// Compute or use provided embedding.
	// If embedding fails, embedding will be nil and Store will use full-text-only ranking.
	embedding := req.Embedding
	if embedding == nil {
		embedding = s.embedQueryText(ctx, req.Query)
		// Note: if embedQueryText returns nil, the Store will degrade to full-text-only (AC-7).
	}

	opts := SearchOptions{
		Kind:              req.Kind,
		Tags:              req.Tags,
		Limit:             req.Limit,
		IncludeDeprecated: false,
		Namespaces:        s.searchNamespaces(),
	}
	if opts.Limit == 0 {
		opts.Limit = 5
	}

	// Delegate to the store for hybrid search.
	result, err := s.store.Search(ctx, req.Query, embedding, req.Repo, opts)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	s.annotateStale(ctx, result.Records, req.MinSimilarity)

	return result, nil
}

// FindCandidates fetches the top-N nearest records in the same repo or repo="*",
// used during the write path to check for dedup before deciding to ADD/UPDATE/SUPERSEDE/NOOP.
// This is a read-only, internal service method (not an MCP tool).
// It uses the same fused-score ranking as Search and returns only {id, title, score}.
func (s *Service) FindCandidates(ctx context.Context, embedding []float32, repo string, limit int) ([]*Candidate, error) {
	if limit <= 0 {
		limit = 5
	}
	candidates, err := s.store.FindCandidates(ctx, embedding, s.namespace, repo, limit)
	if err != nil {
		return nil, fmt.Errorf("find candidates: %w", err)
	}
	return candidates, nil
}

// FindCandidatesForText computes a candidate-lookup embedding for a draft's
// title/tags/content, built the same way the write path composes its own
// embedding input (secret-scrubbed, then title+tags+content via
// composeEmbedInput — see embedinput.go), and returns the top-N nearest
// existing records in the same repo (or repo="*"). It is read-only: it never
// writes anything and never acquires the AC-16 advisory lock.
//
// This exists so extraction (session/PR paths) can see real candidates
// *before* a haiku call decides ADD/UPDATE/SUPERSEDE/NOOP (AC-13, AC-14,
// Interface Note) — the decision must never be made blind. Store/writepath.go
// still re-fetches a fresh top-5 and re-validates any target id under the
// AC-16 lock before committing; this method only feeds the earlier
// extraction-time decision, it is not a substitute for that re-validation.
func (s *Service) FindCandidatesForText(ctx context.Context, title string, tags []string, content string, repo string) ([]*Candidate, error) {
	scrubbedTitle, _ := s.scrubber.Scrub(title)
	scrubbedContent, _ := s.scrubber.Scrub(content)
	embedInput := composeEmbedInput(scrubbedTitle, tags, scrubbedContent)

	embedding, err := s.embeddingProvider.Embed(ctx, embedInput, s.cfg.EmbedMaxTokens)
	if err != nil {
		return nil, fmt.Errorf("embedding provider unavailable: %w", err)
	}

	return s.FindCandidates(ctx, embedding, repo, 5)
}

// Store persists a new record, applying the dedup/merge logic.
// Implemented in writepath.go (WI-3b).
// See writepath.go for full documentation.

// Get/Update/Deprecate/List are implemented as GetRecord/UpdateRecord/
// DeprecateRecord/ListRecords in crud.go.

// Feedback records user feedback (useful/outdated/wrong) and applies lifecycle transitions.
// Implemented in lifecycle.go (WI-3c).
// See lifecycle.go for full documentation.

// SearchRequest is the input to Search.
type SearchRequest struct {
	Query     string
	Embedding []float32 // Optional; if provided, used instead of computing from Query.
	Repo      string
	Kind      *record.Kind
	Tags      []string
	Limit     int // Default 5.

	// MinSimilarity is the lowest cosine similarity the caller will show
	// (the hook's card threshold). Results below it are not staleness-
	// checked. 0 checks every result.
	MinSimilarity float64
}
