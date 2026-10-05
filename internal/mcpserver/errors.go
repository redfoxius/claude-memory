package mcpserver

import (
	"errors"
	"fmt"

	"github.com/redfoxius/claude-memory/internal/memory"
)

// toToolError maps a service-layer error to the error returned from a tool
// handler. The go-sdk packs a non-nil handler error into the tool's
// CallToolResult as a tool-level error (IsError=true), not a protocol
// error, so the message is safe to return directly to the caller.
//
// memory.ErrNotFound becomes a plain "not found" error (AC-10), regardless
// of how many layers wrapped it. Every other error (including a
// Postgres/Ollama-unreachable failure during a write, AC-57) is returned
// with the failing operation named but otherwise passed through — lower
// layers are responsible for never embedding secrets/DSNs in error text.
func toToolError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, memory.ErrNotFound) {
		return errors.New("not found")
	}
	return fmt.Errorf("%s: %w", op, err)
}
