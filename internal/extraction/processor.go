package extraction

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
	"claude-memory/internal/transcript"
)

// Config controls extraction behavior.
type Config struct {
	// MinMessages is the minimum number of messages required to trigger extraction (AC-22).
	MinMessages int

	// CharBudget is the maximum characters for the transcript rendering.
	CharBudget int

	// HaikuTimeout is the timeout for haiku subprocess calls.
	HaikuTimeout time.Duration

	// Repo, when set, is the repo name every extracted draft is stored
	// under (the resolved git checkout's name). Empty falls back to the
	// transcript's own inference (basename of the session cwd).
	Repo string
}

// Result summarizes the outcome of an extraction run.
type Result struct {
	// RecordsProcessed is the number of records extracted and processed.
	RecordsProcessed int

	// RecordsStored is the number of records successfully stored (after dedup).
	RecordsStored int

	// SkippedReason is non-empty if extraction was skipped (e.g., gating failed).
	SkippedReason string

	// Errors is a list of non-fatal errors encountered during processing.
	Errors []string
}

// PRInput contains the PR data for extraction.
type PRInput struct {
	Title       string
	Description string
	Repo        string
	URL         string
	// CommitSHA is the PR's provider merge commit, recorded as the
	// staleness baseline (empty = none).
	CommitSHA string
}

// StoreWriter is the interface extraction needs against the memory service:
// persisting a decided record, and looking up real dedup candidates for a
// draft *before* a decision is made about it (AC-13, AC-14 Interface Note).
// It is declared here, by the consumer, sized to exactly what extraction
// calls; it's satisfied by *memory.Service and can be faked for testing.
type StoreWriter interface {
	// Store persists a record using an already-decided ExtractionDecision.
	Store(ctx context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error)

	// FindCandidatesForText returns the top-N nearest existing records for a
	// draft's title/tags/content, scoped to repo (or repo="*"), using the
	// same embedding composition as the write path. Extraction calls this
	// between the two haiku calls, so the decision call is grounded in real
	// data instead of guessing blind.
	FindCandidatesForText(ctx context.Context, title string, tags []string, content string, repo string) ([]*memory.Candidate, error)
}

// ProcessSession extracts facts from a Claude Code session transcript.
// This is the entry point for the SessionEnd hook (AC-21/AC-22).
//
// Returns a Result with the number of records stored, or zero if gating fails (AC-22).
// Errors in parsing or subprocess execution are logged but do not fail the call;
// instead, zero records are returned (AC-24, AC-25).
// If runner is nil, a default CLIHaikuRunner is created.
func ProcessSession(
	ctx context.Context,
	writer StoreWriter,
	transcriptPath string,
	cfg Config,
	runner HaikuRunner,
) (*Result, error) {
	result := &Result{}

	// Parse the transcript (AC-24).
	transcriptCfg := transcript.Config{CharBudget: cfg.CharBudget}
	tr, err := transcript.Parse(transcriptPath, transcriptCfg)
	if err != nil {
		slog.Error("failed to parse transcript", "path", transcriptPath, "error", err)
		result.SkippedReason = fmt.Sprintf("parse error: %v", err)
		return result, nil
	}

	// Gate: check if we should extract (AC-22).
	if !shouldExtract(tr, cfg.MinMessages) {
		slog.Debug("skipping extraction", "reason", "gating failed", "messages", tr.MessageCount, "hasFileEdits", tr.HasFileEdits)
		result.SkippedReason = fmt.Sprintf("gating failed: %d messages, file_edits=%v", tr.MessageCount, tr.HasFileEdits)
		return result, nil
	}

	if runner == nil {
		runner = NewCLIHaikuRunner(cfg.HaikuTimeout)
	}

	// Step 1: haiku proposes 0-3 draft records, no dedup decision yet (AC-23, AC-45).
	drafts, skipReason := extractDrafts(ctx, runner, "session", tr.CompactText, transcriptPath)
	if skipReason != "" {
		result.SkippedReason = skipReason
		return result, nil
	}

	repo := tr.Repo
	if cfg.Repo != "" {
		repo = cfg.Repo // the resolved checkout's name beats basename(cwd)
	}
	processDrafts(ctx, writer, runner, drafts, repo, record.SourceSession, "", result)

	return result, nil
}

// ProcessPR extracts facts from a PR (Azure DevOps).
// This is the entry point for the ingest-pr CLI (AC-26/AC-27/AC-28).
// If runner is nil, a default CLIHaikuRunner is created.
func ProcessPR(
	ctx context.Context,
	writer StoreWriter,
	pr PRInput,
	cfg Config,
	runner HaikuRunner,
) (*Result, error) {
	result := &Result{}

	// Combine PR text into a single document for extraction.
	prText := fmt.Sprintf("PR Title: %s\n\nPR Description:\n%s\n\nPR URL: %s", pr.Title, pr.Description, pr.URL)

	if runner == nil {
		runner = NewCLIHaikuRunner(cfg.HaikuTimeout)
	}

	// Step 1: haiku proposes 0-3 draft records, no dedup decision yet (AC-23, AC-45).
	drafts, skipReason := extractDrafts(ctx, runner, "PR", prText, pr.URL)
	if skipReason != "" {
		result.SkippedReason = skipReason
		return result, nil
	}

	processDrafts(ctx, writer, runner, drafts, pr.Repo, record.SourcePR, pr.CommitSHA, result)

	return result, nil
}

// extractDrafts runs the first haiku call (extraction only, no decision) and
// parses its output into validated draft records (AC-23, AC-24, AC-25,
// AC-45). logRef identifies the input for log messages (a path or URL).
func extractDrafts(ctx context.Context, runner HaikuRunner, source, text, logRef string) ([]*DraftRecord, string) {
	prompt := BuildExtractionPrompt(source, text)

	output, err := runner.Run(ctx, prompt)
	if err != nil {
		slog.Error("haiku extraction call failed", "ref", logRef, "error", err)
		return nil, fmt.Sprintf("haiku error: %v", err)
	}

	rawDrafts, err := ParseHaikuOutput(output)
	if err != nil {
		slog.Error("failed to parse haiku extraction output", "ref", logRef, "error", err)
		return nil, fmt.Sprintf("parse haiku output error: %v", err)
	}

	drafts := make([]*DraftRecord, 0, len(rawDrafts))
	for i, rawDraft := range rawDrafts {
		draft, err := ParseDraftRecord(rawDraft)
		if err != nil {
			slog.Warn("discarding invalid draft record", "ref", logRef, "index", i, "error", err)
			continue
		}
		drafts = append(drafts, draft)
	}

	return drafts, ""
}

// processDrafts runs, for each draft, step 2 (candidate lookup) and step 3
// (the decision haiku call), then stores the result (AC-13, AC-14).
func processDrafts(
	ctx context.Context,
	writer StoreWriter,
	runner HaikuRunner,
	drafts []*DraftRecord,
	repo string,
	source record.Source,
	commitSHA string,
	result *Result,
) {
	for _, draft := range drafts {
		result.RecordsProcessed++

		action, targetID := decideAction(ctx, writer, runner, draft, repo)

		storeReq := ConvertDraftToStoreRequest(draft, repo, action, targetID)
		storeReq.Source = source
		if commitSHA != "" {
			storeReq.CommitSHA = &commitSHA
		}

		resp, err := writer.Store(ctx, storeReq)
		if err != nil {
			slog.Error("failed to store extracted record", "title", draft.Title, "error", err)
			result.Errors = append(result.Errors, fmt.Sprintf("store: %v", err))
			continue
		}

		result.RecordsStored++
		slog.Debug("extracted and stored record", "id", resp.ID, "title", draft.Title, "decision", resp.Decision)
	}
}

// decideAction performs steps 2 and 3 for one draft: fetch its real top-5
// candidates (AC-13), then ask haiku to decide ADD/UPDATE/SUPERSEDE/NOOP
// given those candidates (AC-14). Any failure along the way (candidate
// lookup error, haiku failure, invalid/unparseable decision JSON, or an
// invented target_id not among the offered candidates) falls back to ADD —
// the same fallback writepath.go already applies when re-validating an
// ExtractionDecision under its own lock, so this never silently drops a
// draft, it just never trusts an ungrounded or invented target.
func decideAction(ctx context.Context, writer StoreWriter, runner HaikuRunner, draft *DraftRecord, repo string) (memory.WriteAction, *string) {
	candidates, err := writer.FindCandidatesForText(ctx, draft.Title, draft.Tags, draft.Content, repo)
	if err != nil {
		slog.Warn("candidate lookup failed; defaulting to ADD", "title", draft.Title, "error", err)
		return memory.ActionAdd, nil
	}

	decisionPrompt := BuildDecisionPrompt("session/PR", draft, candidates)
	decisionOutput, err := runner.Run(ctx, decisionPrompt)
	if err != nil {
		slog.Warn("decision haiku call failed; defaulting to ADD", "title", draft.Title, "error", err)
		return memory.ActionAdd, nil
	}

	decision, err := ParseDecision(decisionOutput)
	if err != nil {
		slog.Warn("invalid decision output; defaulting to ADD", "title", draft.Title, "error", err)
		return memory.ActionAdd, nil
	}

	return resolveDecision(decision, candidates)
}

// resolveDecision converts a haiku Decision into a WriteAction + target id,
// validating any target_id against the real candidate id set offered to
// haiku (AC-13, AC-14). An invented id, or a non-ADD decision with no
// candidates available, falls back to ADD rather than being trusted —
// haiku's decision is advisory input, never a bypass (see also
// writepath.go's own re-validation under the AC-16 lock).
func resolveDecision(d *Decision, candidates []*memory.Candidate) (memory.WriteAction, *string) {
	action := parseAction(d.Action)

	if action == memory.ActionAdd {
		return memory.ActionAdd, nil
	}

	if len(candidates) == 0 || d.TargetID == nil {
		slog.Warn("decision requires a target but none is usable; falling back to ADD", "action", action)
		return memory.ActionAdd, nil
	}

	for _, c := range candidates {
		if c.ID == *d.TargetID {
			return action, d.TargetID
		}
	}

	slog.Warn("decision target_id not among offered candidates; falling back to ADD",
		"action", action, "target_id", *d.TargetID)
	return memory.ActionAdd, nil
}

// shouldExtract checks if the session meets the gating criteria (AC-22).
// Extract if file edits happened OR message count exceeds the threshold.
func shouldExtract(tr *transcript.Transcript, minMessages int) bool {
	if tr.HasFileEdits {
		return true
	}
	if tr.MessageCount > minMessages {
		return true
	}
	return false
}

// parseAction converts a validated decision action string to a memory.WriteAction.
// Decision has already passed ValidateDecision/isValidAction by this point.
func parseAction(s string) memory.WriteAction {
	switch s {
	case "ADD":
		return memory.ActionAdd
	case "UPDATE":
		return memory.ActionUpdate
	case "SUPERSEDE":
		return memory.ActionSupersede
	case "NOOP":
		return memory.ActionNoop
	default:
		// Should not happen after schema validation, but default to ADD for safety.
		return memory.ActionAdd
	}
}
