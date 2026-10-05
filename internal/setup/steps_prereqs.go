package setup

import (
	"context"
	"strings"
)

// PrereqsStepID is the id of the prerequisites step (AC-7).
const PrereqsStepID = "prereqs"

// prereq is one tool the install looks for on PATH.
type prereq struct {
	Tool      string // the executable name for LookPath
	Component string // the hint-table key
	Required  bool   // a missing required tool blocks the step
	Why       string // what needs it, for the note of an optional tool
	Level     NoteLevel
}

// prereqTools is the AC-18 list. git and claude block: the claude-md and
// mcp steps cannot do their job without them. psql, az, gh, glab and ollama only warn
// or inform: psql is run by the user for bootstrap.sql, az, gh and glab serve the
// ingest-pr job, and the ollama step reports its own blocked state.
var prereqTools = []prereq{
	{Tool: "git", Component: CompGit, Required: true},
	{Tool: "claude", Component: CompClaude, Required: true},
	{Tool: "psql", Component: CompPsql, Why: "needed to run bootstrap.sql when install creates the database", Level: NoteWarn},
	{Tool: "az", Component: CompAz, Why: "needed by the ingest-pr job for Azure DevOps repos", Level: NoteInfo},
	{Tool: "gh", Component: CompGh, Why: "needed by the ingest-pr job for GitHub repos", Level: NoteInfo},
	{Tool: "glab", Component: CompGlab, Why: "needed by the ingest-pr job for GitLab repos", Level: NoteInfo},
	{Tool: "ollama", Component: CompOllama, Why: "the ollama step needs it for a local embedder", Level: NoteInfo},
}

// PrereqsStep looks for the external tools on PATH (LookPath only; it runs
// nothing and installs nothing). A missing required tool makes the step
// blocked with the package hint as its Remedy and no BlockedBy, which gives
// the Configure-phase re-check / skip / quit prompt (Design 15.3).
type PrereqsStep struct{}

var _ Step = PrereqsStep{}

// ID implements Step.
func (PrereqsStep) ID() string { return PrereqsStepID }

// Title implements Step.
func (PrereqsStep) Title() string { return "Prerequisites" }

// Requires implements Step.
func (PrereqsStep) Requires() []string { return nil }

// Detect implements Step.
func (PrereqsStep) Detect(_ context.Context, rc ReadPorts, _ *RunState) Detection {
	var found, missingReq []string
	var missingReqComps []string
	var notes []Note
	for _, t := range prereqTools {
		if _, err := rc.Runner.LookPath(t.Tool); err == nil {
			found = append(found, t.Tool)
			continue
		}
		if t.Required {
			missingReq = append(missingReq, t.Tool)
			missingReqComps = append(missingReqComps, t.Component)
			continue
		}
		notes = append(notes, Note{t.Level, t.Tool + " not found (" + t.Why + ") · " + Hint(rc.Platform, t.Component)})
	}
	if len(missingReq) > 0 {
		return Detection{
			State:  StateBlocked,
			Detail: "missing: " + strings.Join(missingReq, ", "),
			Remedy: hintList(rc.Platform, missingReqComps...),
			Notes:  notes,
		}
	}
	return Detection{State: StateOK, Detail: "found: " + strings.Join(found, ", "), Notes: notes}
}

// Plan implements Step: the step has nothing to apply.
func (PrereqsStep) Plan(context.Context, ReadPorts, *RunState, Choices) (Plan, error) {
	return Plan{}, nil
}

// Apply implements Step.
func (PrereqsStep) Apply(context.Context, WritePorts, *RunState, Plan) (StepResult, error) {
	return StepResult{}, nil
}
