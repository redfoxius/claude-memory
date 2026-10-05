package mcpserver

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"

	"claude-memory/internal/memory"
)

// Server wires the 7 claude-memory MCP tools onto the go-sdk, backed by a
// Service. It never touches the network itself — the caller (cmd/claude-
// memory/serve.go) chooses the transport (stdio only in the MVP).
type Server struct {
	mcp    *mcp.Server
	svc    Service
	logger zerolog.Logger

	// Reliability events (nil events = disabled).
	events    memory.EventSink
	sessionID string
	classify  func(error) memory.ErrorClass
	warnOnce  sync.Once
}

// Option configures a Server.
type Option func(*Server)

// WithEvents makes memory_search and memory_store record one content-free
// reliability event per call into sink (the file spool, so it works while
// Postgres is down). classify maps a failed call to its error class; a nil
// classify treats every unrecognised error as internal. Appending is
// best-effort: a failure never changes a tool result.
func WithEvents(sink memory.EventSink, sessionID string, classify func(error) memory.ErrorClass) Option {
	return func(s *Server) { s.events, s.sessionID, s.classify = sink, sessionID, classify }
}

// New builds a Server with all 7 tools registered. logger must write to
// stderr only — stdout is reserved for MCP protocol frames (AC-40).
func New(svc Service, logger zerolog.Logger, opts ...Option) *Server {
	s := &Server{svc: svc, logger: logger}
	for _, o := range opts {
		o(s)
	}

	impl := &mcp.Implementation{Name: "claude-memory", Version: "0.1.0"}
	srv := mcp.NewServer(impl, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_search",
		Description: "Search shared memory with hybrid (semantic + full-text) ranking, excluding deprecated records by default.",
	}, s.handleSearch)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_store",
		Description: "Store a new fact into memory, applying dedup/merge logic. Requires no confirmation step.",
	}, s.handleStore)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_update",
		Description: "Update fields of an existing memory record; re-embeds when title or content changes.",
	}, s.handleUpdate)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_deprecate",
		Description: "Mark a memory record as deprecated with a reason, optionally pointing at its replacement.",
	}, s.handleDeprecate)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_get",
		Description: "Fetch a single memory record by id, including its full content.",
	}, s.handleGet)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_list",
		Description: "List memory records, optionally filtered by repo, kind, and status.",
	}, s.handleList)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "memory_feedback",
		Description: "Record feedback (useful, outdated, or wrong) on a memory record and apply any resulting lifecycle transition.",
	}, s.handleFeedback)

	s.mcp = srv
	return s
}

// Run serves the registered tools over t until the transport closes or ctx
// is canceled.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	return s.mcp.Run(ctx, t)
}
