package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// handleSearch implements memory_search (AC-4, AC-5, AC-6, AC-7).
func (s *Server) handleSearch(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	start := time.Now()

	// One reliability event per call, after the result is built. Anything that
	// returns before the outcome is set counts as an internal error.
	outcome, class, canceled := memory.OutcomeError, memory.ErrClassInternal, false
	defer func() {
		if !canceled { // a cancelled call is not a service failure
			s.emit(ctx, memory.EventSearchCalled, "", outcome, class)
		}
	}()

	var kind *record.Kind
	if in.Kind != "" {
		k := record.Kind(in.Kind)
		if !k.IsValid() {
			class = memory.ErrClassInvalidRequest
			return nil, SearchOutput{}, fmt.Errorf("invalid kind %q", in.Kind)
		}
		kind = &k
	}

	req := &memory.SearchRequest{
		Query: in.Query,
		Repo:  in.Repo,
		Kind:  kind,
		Tags:  in.Tags,
		Limit: in.Limit,
	}

	result, err := s.svc.Search(ctx, req)
	if err != nil {
		class, canceled = s.classifyErr(err), memory.IsCanceled(err)
		s.logCall("memory_search", start, err, nil)
		return nil, SearchOutput{}, toToolError("memory_search", err)
	}

	out := SearchOutput{Degraded: result.Degraded}
	outcome, class = memory.OutcomeOK, ""
	if result.Degraded {
		outcome = memory.OutcomeDegraded
	}
	for _, r := range result.Records {
		item := SearchResultItem{
			ID:         r.ID,
			Kind:       string(r.Kind),
			Title:      r.Title,
			Repo:       r.Repo,
			Namespace:  r.Namespace,
			Tags:       r.Tags,
			Status:     string(r.Status),
			Confidence: r.Confidence,
			Score:      r.Score,
			Similarity: r.Similarity,
			Unverified: r.Unverified,
		}
		if r.Stale != nil {
			item.StaleHint = true
			item.StaleCommits = r.Stale.Commits
		}
		out.Results = append(out.Results, item)
	}

	s.logCall("memory_search", start, nil, map[string]interface{}{
		"result_count": len(out.Results),
		"degraded":     out.Degraded,
	})
	return nil, out, nil
}

// handleStore implements memory_store (AC-13 through AC-19, AC-29, AC-57).
func (s *Server) handleStore(ctx context.Context, _ *mcp.CallToolRequest, in StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	start := time.Now()

	// One reliability event per call, after the result is built, from the
	// branch taken below (never from the caller's namespace). Anything that
	// returns before the outcome is set counts as an internal error.
	outcome, class, ns, canceled := memory.OutcomeError, memory.ErrClassInternal, "", false
	defer func() {
		if !canceled { // a cancelled call is not a service failure
			s.emit(ctx, memory.EventStoreAttempted, ns, outcome, class)
		}
	}()

	kind := record.Kind(in.Kind)
	if !kind.IsValid() {
		class = memory.ErrClassInvalidRequest
		return nil, StoreOutput{}, fmt.Errorf("invalid kind %q", in.Kind)
	}

	source := record.SourceInline
	if in.Source != "" {
		src := record.Source(in.Source)
		if !src.IsValid() {
			class = memory.ErrClassInvalidRequest
			return nil, StoreOutput{}, fmt.Errorf("invalid source %q", in.Source)
		}
		if src == record.SourceImport {
			class = memory.ErrClassInvalidRequest
			return nil, StoreOutput{}, fmt.Errorf("source %q is reserved for the import command", in.Source)
		}
		source = src
	}

	repo := in.Repo
	if repo == "" {
		repo = "*"
	}

	req := &memory.StoreRequest{
		Kind:       kind,
		Title:      in.Title,
		Content:    in.Content,
		Namespace:  in.Namespace,
		Repo:       repo,
		Files:      in.Files,
		Tags:       in.Tags,
		Source:     source,
		Confidence: in.Confidence,
	}
	if in.CommitSHA != "" {
		req.CommitSHA = &in.CommitSHA
	}
	if in.Ticket != "" {
		req.Ticket = &in.Ticket
	}

	if in.Decision != nil {
		action := memory.WriteAction(in.Decision.Action)
		switch action {
		case memory.ActionAdd, memory.ActionUpdate, memory.ActionSupersede, memory.ActionNoop:
		default:
			class = memory.ErrClassInvalidRequest
			return nil, StoreOutput{}, fmt.Errorf("invalid decision action %q", in.Decision.Action)
		}
		dec := &memory.ExtractionDecision{Action: action}
		if in.Decision.TargetID != "" {
			dec.TargetID = &in.Decision.TargetID
		}
		req.ExtractionDecision = dec
	}

	resp, err := s.svc.Store(ctx, req)
	if err != nil {
		class, canceled = s.classifyErr(err), memory.IsCanceled(err)
		s.logCall("memory_store", start, err, map[string]interface{}{"repo": repo})
		return nil, StoreOutput{}, toToolError("memory_store", err)
	}

	out := StoreOutput{Namespace: resp.Namespace}
	ns = resp.Namespace
	for _, c := range resp.CandidatesConsidered {
		out.CandidatesConsidered = append(out.CandidatesConsidered, StoreCandidate{
			ID:         c.ID,
			Title:      c.Title,
			Similarity: c.Similarity,
		})
	}

	if resp.ID == "" && len(resp.CandidatesConsidered) > 0 {
		// Inline write landed in the 0.80-0.92 band: no record was written;
		// the caller must re-call with a Decision (AC-15).
		out.Status = "needs_judgment"
		outcome, class = memory.OutcomeNeedsJudgment, ""
		s.logCall("memory_store", start, nil, map[string]interface{}{
			"status":     out.Status,
			"candidates": len(out.CandidatesConsidered),
		})
		return nil, out, nil
	}

	out.Status = "stored"
	outcome, class = storeOutcome(resp.Decision)
	out.ID = resp.ID
	out.Decision = string(resp.Decision)

	s.logCall("memory_store", start, nil, map[string]interface{}{
		"status":   out.Status,
		"id":       out.ID,
		"decision": out.Decision,
	})
	return nil, out, nil
}

// handleUpdate implements memory_update (AC-8, AC-10).
func (s *Server) handleUpdate(ctx context.Context, _ *mcp.CallToolRequest, in UpdateInput) (*mcp.CallToolResult, RecordOutput, error) {
	start := time.Now()

	req := &memory.UpdateRequest{
		ID:         in.ID,
		Title:      in.Title,
		Content:    in.Content,
		Tags:       in.Tags,
		Files:      in.Files,
		Ticket:     in.Ticket,
		Confidence: in.Confidence,
		CommitSHA:  in.CommitSHA,
	}
	if in.Status != nil {
		st := record.Status(*in.Status)
		if !st.IsValid() {
			return nil, RecordOutput{}, fmt.Errorf("invalid status %q", *in.Status)
		}
		req.Status = &st
	}

	rec, err := s.svc.UpdateRecord(ctx, req)
	s.logCall("memory_update", start, err, map[string]interface{}{"id": in.ID})
	if err != nil {
		return nil, RecordOutput{}, toToolError("memory_update", err)
	}
	return nil, recordToOutput(rec), nil
}

// handleDeprecate implements memory_deprecate (AC-9, AC-10).
func (s *Server) handleDeprecate(ctx context.Context, _ *mcp.CallToolRequest, in DeprecateInput) (*mcp.CallToolResult, RecordOutput, error) {
	start := time.Now()

	req := &memory.DeprecateRequest{
		ID:           in.ID,
		Reason:       in.Reason,
		SupersededBy: in.SupersededBy,
	}

	rec, err := s.svc.DeprecateRecord(ctx, req)
	s.logCall("memory_deprecate", start, err, map[string]interface{}{"id": in.ID})
	if err != nil {
		return nil, RecordOutput{}, toToolError("memory_deprecate", err)
	}
	return nil, recordToOutput(rec), nil
}

// handleGet implements memory_get (AC-10).
func (s *Server) handleGet(ctx context.Context, _ *mcp.CallToolRequest, in GetInput) (*mcp.CallToolResult, RecordOutput, error) {
	start := time.Now()

	rec, err := s.svc.GetRecord(ctx, in.ID)
	s.logCall("memory_get", start, err, map[string]interface{}{"id": in.ID})
	if err != nil {
		return nil, RecordOutput{}, toToolError("memory_get", err)
	}
	out := recordToOutput(rec)
	if h := s.svc.StaleHint(ctx, rec); h != nil {
		out.StaleHint = true
		out.StaleCommits = h.Commits
	}
	return nil, out, nil
}

// handleList implements memory_list (AC-11).
func (s *Server) handleList(ctx context.Context, _ *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, ListOutput, error) {
	start := time.Now()

	filters := memory.ListFilters{}
	if in.Repo != "" {
		filters.Repo = &in.Repo
	}
	if in.Kind != "" {
		k := record.Kind(in.Kind)
		if !k.IsValid() {
			return nil, ListOutput{}, fmt.Errorf("invalid kind %q", in.Kind)
		}
		filters.Kind = &k
	}
	if in.Status != "" {
		st := record.Status(in.Status)
		if !st.IsValid() {
			return nil, ListOutput{}, fmt.Errorf("invalid status %q", in.Status)
		}
		filters.Status = &st
	}

	recs, err := s.svc.ListRecords(ctx, filters)
	if err != nil {
		s.logCall("memory_list", start, err, nil)
		return nil, ListOutput{}, toToolError("memory_list", err)
	}

	out := ListOutput{Records: make([]RecordOutput, 0, len(recs))}
	for _, r := range recs {
		out.Records = append(out.Records, recordToOutput(r))
	}

	s.logCall("memory_list", start, nil, map[string]interface{}{"result_count": len(out.Records)})
	return nil, out, nil
}

// handleFeedback implements memory_feedback (AC-10, AC-12, AC-34, AC-36).
func (s *Server) handleFeedback(ctx context.Context, _ *mcp.CallToolRequest, in FeedbackInput) (*mcp.CallToolResult, FeedbackOutput, error) {
	start := time.Now()

	outcome := memory.FeedbackOutcome(in.Outcome)
	switch outcome {
	case memory.FeedbackUseful, memory.FeedbackOutdated, memory.FeedbackWrong:
	default:
		return nil, FeedbackOutput{}, fmt.Errorf("invalid outcome %q", in.Outcome)
	}

	req := &memory.FeedbackRequest{
		ID:      in.ID,
		Outcome: outcome,
		Note:    in.Note,
	}

	resp, err := s.svc.Feedback(ctx, req)
	s.logCall("memory_feedback", start, err, map[string]interface{}{"id": in.ID, "outcome": in.Outcome})
	if err != nil {
		return nil, FeedbackOutput{}, toToolError("memory_feedback", err)
	}

	return nil, FeedbackOutput{ID: resp.ID, NewStatus: string(resp.NewStatus)}, nil
}
