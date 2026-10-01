package extraction

import (
	"encoding/json"
	"fmt"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// DraftRecord is the JSON schema haiku emits for a single extracted candidate
// record in the *first* extraction call (AC-23). It intentionally carries no
// dedup decision (no action/target_id): haiku cannot know what already
// exists in memory at this point, so asking it to decide here would mean
// deciding blind and risking an invented target id (AC-13/AC-14). The
// ADD/UPDATE/SUPERSEDE/NOOP decision is made by a *second* haiku call, given
// this draft plus its real top-5 candidates — see Decision below.
type DraftRecord struct {
	Kind    record.Kind `json:"kind"`
	Title   string      `json:"title"`
	Content string      `json:"content"`
	Tags    []string    `json:"tags,omitempty"`
	Files   []string    `json:"files,omitempty"`
	Ticket  *string     `json:"ticket,omitempty"`
}

// ValidateDraft validates a single draft record against the schema.
// Returns an error if any required field is missing or invalid.
func ValidateDraft(draft *DraftRecord) error {
	if draft == nil {
		return fmt.Errorf("%w: draft is nil", ErrInvalidSchema)
	}

	if draft.Kind == "" {
		return fmt.Errorf("%w: kind is required", ErrInvalidSchema)
	}
	if !isValidKind(draft.Kind) {
		return fmt.Errorf("%w: invalid kind: %s", ErrInvalidSchema, draft.Kind)
	}

	if draft.Title == "" {
		return fmt.Errorf("%w: title is required", ErrInvalidSchema)
	}

	if draft.Content == "" {
		return fmt.Errorf("%w: content is required", ErrInvalidSchema)
	}

	return nil
}

// ParseDraftRecord parses a single haiku draft record from JSON.
// Returns ErrInvalidSchema if the JSON does not parse or fails validation.
func ParseDraftRecord(rawJSON json.RawMessage) (*DraftRecord, error) {
	var draft DraftRecord
	if err := json.Unmarshal(rawJSON, &draft); err != nil {
		return nil, fmt.Errorf("%w: failed to parse draft record: %v", ErrInvalidSchema, err)
	}

	if err := ValidateDraft(&draft); err != nil {
		return nil, err
	}

	return &draft, nil
}

// Decision is the JSON schema haiku emits for the *second* extraction call
// (AC-14): given a draft record and its real top-5 nearest existing records
// (memory.Service.FindCandidatesForText), haiku decides whether to ADD,
// UPDATE, SUPERSEDE, or NOOP, and if not ADD, which candidate it targets.
// TargetID is validated against the actual candidate id set by the caller
// (processor.go's resolveDecision) before it is ever trusted — haiku cannot
// be allowed to invent a target id that was never offered to it.
type Decision struct {
	Action   string  `json:"action"` // ADD, UPDATE, SUPERSEDE, or NOOP
	TargetID *string `json:"target_id,omitempty"`
}

// ValidateDecision validates a single decision against the schema.
// UPDATE, SUPERSEDE, and NOOP all require a target_id (they refer to an
// existing record); ADD never does.
func ValidateDecision(d *Decision) error {
	if d == nil {
		return fmt.Errorf("%w: decision is nil", ErrInvalidSchema)
	}

	if !isValidAction(d.Action) {
		return fmt.Errorf("%w: invalid action: %s", ErrInvalidSchema, d.Action)
	}

	if d.Action != "ADD" && (d.TargetID == nil || *d.TargetID == "") {
		return fmt.Errorf("%w: action %s requires target_id", ErrInvalidSchema, d.Action)
	}

	return nil
}

// ParseDecision parses the second-call haiku decision output as a single
// JSON object (not an array). Returns ErrInvalidSchema if the JSON does not
// parse or fails validation.
func ParseDecision(data []byte) (*Decision, error) {
	var d Decision
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("%w: failed to parse decision: %v", ErrInvalidSchema, err)
	}

	if err := ValidateDecision(&d); err != nil {
		return nil, err
	}

	return &d, nil
}

// ConvertDraftToStoreRequest converts a validated draft to a memory.StoreRequest.
// This is used to persist the extracted record via Service.Store. action and
// targetID come from the (already-validated-against-real-candidates) decision
// step, never from the draft itself.
func ConvertDraftToStoreRequest(draft *DraftRecord, repo string, action memory.WriteAction, targetID *string) *memory.StoreRequest {
	return &memory.StoreRequest{
		Kind:    draft.Kind,
		Title:   draft.Title,
		Content: draft.Content,
		Repo:    repo,
		Tags:    draft.Tags,
		Files:   draft.Files,
		Ticket:  draft.Ticket,
		Source:  record.SourceSession, // Will be overridden by ProcessSession/ProcessPR.
		ExtractionDecision: &memory.ExtractionDecision{
			Action:   action,
			TargetID: targetID,
		},
	}
}

// isValidKind checks if the kind is one of the known record kinds.
func isValidKind(kind record.Kind) bool {
	switch kind {
	case record.KindGotcha, record.KindDecision, record.KindPattern, record.KindConvention:
		return true
	default:
		return false
	}
}

// isValidAction checks if the action is one of the expected dedup decisions.
func isValidAction(action string) bool {
	switch action {
	case "ADD", "UPDATE", "SUPERSEDE", "NOOP":
		return true
	default:
		return false
	}
}
