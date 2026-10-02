package setup

// Status is the outcome of one doctor check (spec §2 "Check", AC-58).
type Status string

// Check statuses, in the order the summary reports them.
const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusWarn Status = "warn"
	StatusInfo Status = "info"
	StatusSkip Status = "skip"
)

// State is the result of a step's (or detector's) Detect (spec §2 "Step
// state").
type State string

// Step states.
const (
	// StateAbsent: the artifact does not exist.
	StateAbsent State = "absent"
	// StateOK: the artifact is present and is what we would install.
	StateOK State = "ok"
	// StateOutdated: we installed it (the manifest records it), the user did
	// not change it, and the embedded/desired version differs.
	StateOutdated State = "outdated"
	// StateModified: it differs from what we recorded, or it is ours by
	// identity but we have no record of writing it in this form.
	StateModified State = "modified"
	// StateBlocked: a prerequisite is missing or the platform cannot run it.
	StateBlocked State = "blocked"
)

// Choice is what the user picks for a step (AC-6) [S2].
type Choice string

// Step choices. Overwriting a modified artifact additionally needs an
// explicit Prompter.Confirm; re-asking inputs is the --reconfigure flag.
const (
	ChoiceApply Choice = "apply"
	ChoiceKeep  Choice = "keep"
	ChoiceSkip  Choice = "skip"
)

// DefaultChoice is the AC-6 default choice for a state.
func DefaultChoice(s State) Choice {
	switch s {
	case StateAbsent, StateOutdated:
		return ChoiceApply
	case StateOK, StateModified:
		return ChoiceKeep
	default:
		return ChoiceSkip
	}
}
