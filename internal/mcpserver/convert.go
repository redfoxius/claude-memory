package mcpserver

import (
	"time"

	"claude-memory/internal/record"
)

// recordToOutput converts a domain record to its wire shape, omitting the
// embedding vector (never useful to a calling session) and flattening
// optional pointer fields to empty strings when unset.
func recordToOutput(r *record.Record) RecordOutput {
	out := RecordOutput{
		ID:         r.ID,
		Kind:       string(r.Kind),
		Title:      r.Title,
		Content:    r.Content,
		Repo:       r.Repo,
		Files:      r.Files,
		Tags:       r.Tags,
		Status:     string(r.Status),
		Source:     string(r.Source),
		Confidence: r.Confidence,
		SeenCount:  r.SeenCount,
		UsedCount:  r.UsedCount,
		CreatedAt:  r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  r.UpdatedAt.Format(time.RFC3339),
	}
	if r.CommitSHA != nil {
		out.CommitSHA = *r.CommitSHA
	}
	if r.Ticket != nil {
		out.Ticket = *r.Ticket
	}
	if r.DeprecationReason != nil {
		out.DeprecationReason = *r.DeprecationReason
	}
	if r.SupersededBy != nil {
		out.SupersededBy = *r.SupersededBy
	}
	if r.LastUsedAt != nil {
		out.LastUsedAt = r.LastUsedAt.Format(time.RFC3339)
	}
	return out
}
