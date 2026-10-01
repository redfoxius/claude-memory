package mcpserver

import (
	"time"

	"github.com/rs/zerolog"
)

// logCall records one tool invocation at info level: tool name, the
// supplied identifying fields (record ids, filters — never content or
// titles), and call duration (AC-40). Errors are logged at warn level with
// the same fields plus the error text (already sanitized by toToolError).
func (s *Server) logCall(tool string, start time.Time, err error, fields map[string]interface{}) {
	var ev *zerolog.Event
	if err != nil {
		ev = s.logger.Warn().Err(err)
	} else {
		ev = s.logger.Info()
	}
	ev = ev.Str("tool", tool).Dur("duration", time.Since(start))
	if len(fields) > 0 {
		ev = ev.Fields(fields)
	}
	ev.Msg("mcp tool call")
}
