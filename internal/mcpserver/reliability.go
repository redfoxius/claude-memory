package mcpserver

import (
	"context"
	"time"

	"github.com/redfoxius/claude-memory/internal/memory"
)

// emit records one reliability event for a finished memory_search or
// memory_store call. namespace "" means the service's own. It runs after the
// result is built and never fails the call: an append error is logged once per
// server and ignored.
func (s *Server) emit(ctx context.Context, t memory.EventType, namespace string, outcome memory.FeedbackOutcome, class memory.ErrorClass) {
	if s.events == nil {
		return
	}
	if namespace == "" {
		namespace = s.svc.Namespace()
	}
	e := memory.NewEvent(time.Now(), namespace, t)
	e.Via, e.Outcome, e.ErrorClass, e.SessionID = memory.ViaMCP, outcome, class, s.sessionID
	if err := s.events.Append(ctx, e); err != nil {
		s.warnOnce.Do(func() {
			s.logger.Warn().Err(err).Msg("recording reliability events failed; further failures are not logged")
		})
	}
}

// classifyErr maps a service error to its error class.
func (s *Server) classifyErr(err error) memory.ErrorClass {
	if s.classify != nil {
		return s.classify(err)
	}
	return memory.ClassifyError(err, nil)
}

// storeOutcome maps the decision of a stored response; any other decision is
// an error, never "added".
func storeOutcome(d memory.WriteAction) (memory.FeedbackOutcome, memory.ErrorClass) {
	switch d {
	case memory.ActionAdd:
		return memory.OutcomeAdded, ""
	case memory.ActionUpdate:
		return memory.OutcomeUpdated, ""
	case memory.ActionSupersede:
		return memory.OutcomeSuperseded, ""
	case memory.ActionNoop:
		return memory.OutcomeNoop, ""
	}
	return memory.OutcomeError, memory.ErrClassInternal
}
