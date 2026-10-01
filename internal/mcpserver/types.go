package mcpserver

// Input/output shapes for the 7 MCP tools, per spec §10. Field names use
// snake_case JSON tags to match the tool contracts; enum-like string
// fields (kind, status, source, outcome, action) are validated by hand in
// the handlers since the go-sdk's struct-tag schema inference does not
// emit JSON Schema "enum" constraints on its own.

// SearchInput is the input to memory_search.
type SearchInput struct {
	Query string   `json:"query" jsonschema:"text to search memory for"`
	Repo  string   `json:"repo,omitempty" jsonschema:"repo scope; omitted means current repo plus '*'"`
	Kind  string   `json:"kind,omitempty" jsonschema:"filter: pattern, decision, gotcha, or convention"`
	Tags  []string `json:"tags,omitempty" jsonschema:"filter: match any of these tags"`
	Limit int      `json:"limit,omitempty" jsonschema:"max results to return (default 5)"`
}

// SearchResultItem is one ranked result from memory_search. Content is
// deliberately absent — it is only ever returned by memory_get.
type SearchResultItem struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Repo       string   `json:"repo"`
	Namespace  string   `json:"namespace"`
	Tags       []string `json:"tags,omitempty"`
	Status     string   `json:"status"`
	Confidence float64  `json:"confidence"`
	Score      float64  `json:"score"`
	Similarity float64  `json:"similarity"`
	Unverified bool     `json:"unverified"`
	// StaleHint is true when the record's files changed since it was
	// recorded; StaleCommits is the commit count when known.
	StaleHint    bool `json:"stale_hint,omitempty"`
	StaleCommits int  `json:"stale_commits,omitempty"`
}

// SearchOutput is the output of memory_search.
type SearchOutput struct {
	Results  []SearchResultItem `json:"results"`
	Degraded bool               `json:"degraded,omitempty"`
}

// StoreDecisionInput resolves a prior memory_store "needs_judgment"
// response on a follow-up call (AC-15).
type StoreDecisionInput struct {
	Action   string `json:"action" jsonschema:"ADD, UPDATE, SUPERSEDE, or NOOP"`
	TargetID string `json:"target_id,omitempty" jsonschema:"target record id for UPDATE, SUPERSEDE, or NOOP"`
}

// StoreInput is the input to memory_store.
type StoreInput struct {
	Kind       string              `json:"kind" jsonschema:"pattern, decision, gotcha, or convention"`
	Title      string              `json:"title" jsonschema:"short title"`
	Content    string              `json:"content" jsonschema:"markdown content"`
	Repo       string              `json:"repo,omitempty" jsonschema:"repo name, or '*' for cross-repo; defaults to '*'"`
	Namespace  string              `json:"namespace,omitempty" jsonschema:"omit to store in the current namespace; 'global' shares a stack-generic fact with every namespace (only when explicitly intended; never put company-specific content there)"`
	Files      []string            `json:"files,omitempty"`
	CommitSHA  string              `json:"commit_sha,omitempty"`
	Ticket     string              `json:"ticket,omitempty"`
	Tags       []string            `json:"tags,omitempty"`
	Source     string              `json:"source,omitempty" jsonschema:"inline, session, or pr; defaults to inline"`
	Confidence *float64            `json:"confidence,omitempty"`
	Decision   *StoreDecisionInput `json:"decision,omitempty" jsonschema:"optional decision resolving a prior needs_judgment response"`
}

// StoreCandidate is a nearby existing record a store decision was weighed
// against (id, title, similarity only — never content).
type StoreCandidate struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Similarity float64 `json:"similarity"`
}

// StoreOutput is the output of memory_store. Status is "stored" once a
// decision has been applied, or "needs_judgment" when the inline write
// path's top candidate falls in the 0.80-0.92 band and is awaiting a
// Decision on a follow-up call (AC-15).
type StoreOutput struct {
	Namespace            string           `json:"namespace,omitempty"`
	ID                   string           `json:"id,omitempty"`
	Decision             string           `json:"decision,omitempty"`
	Status               string           `json:"status"`
	CandidatesConsidered []StoreCandidate `json:"candidates_considered,omitempty"`
}

// UpdateInput is the input to memory_update. Any subset of the optional
// fields may be set; absent fields are left unchanged.
type UpdateInput struct {
	ID         string   `json:"id" jsonschema:"record id to update"`
	Title      *string  `json:"title,omitempty"`
	Content    *string  `json:"content,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Files      []string `json:"files,omitempty"`
	Ticket     *string  `json:"ticket,omitempty"`
	Status     *string  `json:"status,omitempty" jsonschema:"candidate, active, or deprecated"`
	Confidence *float64 `json:"confidence,omitempty"`
	CommitSHA  *string  `json:"commit_sha,omitempty" jsonschema:"baseline commit for staleness checks; set it after verifying the record against current code"`
}

// DeprecateInput is the input to memory_deprecate.
type DeprecateInput struct {
	ID           string  `json:"id" jsonschema:"record id to deprecate"`
	Reason       string  `json:"reason" jsonschema:"why this record is being deprecated"`
	SupersededBy *string `json:"superseded_by,omitempty" jsonschema:"id of the record that replaces this one"`
}

// GetInput is the input to memory_get.
type GetInput struct {
	ID string `json:"id" jsonschema:"record id to fetch"`
}

// ListInput is the input to memory_list. All filters are optional and
// combine with AND logic.
type ListInput struct {
	Repo   string `json:"repo,omitempty"`
	Kind   string `json:"kind,omitempty" jsonschema:"pattern, decision, gotcha, or convention"`
	Status string `json:"status,omitempty" jsonschema:"candidate, active, or deprecated"`
}

// ListOutput is the output of memory_list.
type ListOutput struct {
	Records []RecordOutput `json:"records"`
}

// FeedbackInput is the input to memory_feedback.
type FeedbackInput struct {
	ID      string  `json:"id" jsonschema:"record id feedback applies to"`
	Outcome string  `json:"outcome" jsonschema:"useful, outdated, or wrong"`
	Note    *string `json:"note,omitempty"`
}

// FeedbackOutput is the output of memory_feedback.
type FeedbackOutput struct {
	ID        string `json:"id"`
	NewStatus string `json:"new_status"`
}

// RecordOutput is the full record shape returned by memory_get,
// memory_update, memory_deprecate, and memory_list. The embedding vector
// is intentionally omitted — it is never useful to a calling session.
type RecordOutput struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Repo      string `json:"repo"`
	Namespace string `json:"namespace"`
	// StaleHint / StaleCommits: see SearchResultItem (memory_get only).
	StaleHint         bool     `json:"stale_hint,omitempty"`
	StaleCommits      int      `json:"stale_commits,omitempty"`
	Files             []string `json:"files,omitempty"`
	CommitSHA         string   `json:"commit_sha,omitempty"`
	Ticket            string   `json:"ticket,omitempty"`
	Tags              []string `json:"tags,omitempty"`
	Status            string   `json:"status"`
	DeprecationReason string   `json:"deprecation_reason,omitempty"`
	SupersededBy      string   `json:"superseded_by,omitempty"`
	Source            string   `json:"source"`
	Confidence        float64  `json:"confidence"`
	SeenCount         int      `json:"seen_count"`
	UsedCount         int      `json:"used_count"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
	LastUsedAt        string   `json:"last_used_at,omitempty"`
}
