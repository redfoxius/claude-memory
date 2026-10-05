// Package mcpserver wires claude-memory's 7 MCP tools (memory_search,
// memory_store, memory_update, memory_deprecate, memory_get, memory_list,
// memory_feedback) onto the official modelcontextprotocol/go-sdk, over
// stdio only. It depends on a small, consumer-declared interface of the
// memory service's methods rather than on *memory.Service directly, so it
// stays unit-testable against a fake.
package mcpserver

import (
	"context"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// Service is the subset of memory.Service's exported behaviour the MCP
// tool layer needs. It is declared here (the consumer), per
// golang-architecture's "ports declared by the consumer" rule, rather than
// imported as a pre-declared interface from internal/memory. *memory.Service
// satisfies it structurally — no adapter type is required.
type Service interface {
	// Namespace is the service's own namespace (reliability events).
	Namespace() string

	// Search backs memory_search (AC-4, AC-5, AC-6, AC-7).
	Search(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResult, error)

	// Store backs memory_store, including the inline 0.80-0.92
	// needs-judgment round trip (AC-13 through AC-19, AC-57).
	Store(ctx context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error)

	// GetRecord backs memory_get (AC-10).
	GetRecord(ctx context.Context, id string) (*record.Record, error)

	// StaleHint reports whether rec's files changed since it was recorded
	// (nil = fresh or unchecked); memory_get shows it.
	StaleHint(ctx context.Context, rec *record.Record) *memory.StaleHint

	// UpdateRecord backs memory_update (AC-8, AC-10).
	UpdateRecord(ctx context.Context, req *memory.UpdateRequest) (*record.Record, error)

	// DeprecateRecord backs memory_deprecate (AC-9, AC-10).
	DeprecateRecord(ctx context.Context, req *memory.DeprecateRequest) (*record.Record, error)

	// ListRecords backs memory_list (AC-11).
	ListRecords(ctx context.Context, filters memory.ListFilters) ([]*record.Record, error)

	// Feedback backs memory_feedback (AC-10, AC-12, AC-34, AC-36).
	Feedback(ctx context.Context, req *memory.FeedbackRequest) (*memory.FeedbackResponse, error)
}
