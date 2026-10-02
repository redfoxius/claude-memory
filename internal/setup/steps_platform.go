package setup

import (
	"context"
	"fmt"
	"slices"
)

// PlatformStepID is the id of the platform step (AC-7: the first step).
const PlatformStepID = "platform"

// PlatformStep reports the detected platform and validates --jobs-backend.
// It owns no RunState field and writes nothing: PlatformInfo is plain data
// built by cmd (detectPlatform, with the --jobs-backend override applied).
type PlatformStep struct{}

var (
	_ Step   = PlatformStep{}
	_ Seeder = PlatformStep{}
)

// ID implements Step.
func (PlatformStep) ID() string { return PlatformStepID }

// Title implements Step.
func (PlatformStep) Title() string { return "Platform" }

// Requires implements Step.
func (PlatformStep) Requires() []string { return nil }

// UnsupportedError is the AC-17 refusal: install exits 2 with it before any
// prompt when the OS is neither darwin nor linux. It returns nil otherwise.
func UnsupportedError(p PlatformInfo) error {
	if p.Supported() {
		return nil
	}
	return fmt.Errorf("unsupported OS %s: see integration/INSTALL.md for manual steps", p.OS)
}

// ValidJobsBackend reports whether v is an accepted --jobs-backend value
// ("" means detect).
func ValidJobsBackend(v string) bool {
	return v == "" || slices.Contains([]string{JobsLaunchd, JobsSystemd, JobsNone}, v)
}

// Seed rejects an invalid --jobs-backend value (exit 2, before Detect).
func (PlatformStep) Seed(_ context.Context, _ ReadPorts, st *RunState) ([]Note, error) {
	if v := st.Inputs.JobsBackend; !ValidJobsBackend(v) {
		return nil, fmt.Errorf("--jobs-backend %q: want launchd, systemd or none", v)
	}
	return nil, nil
}

// Detect implements Step. An unsupported OS is blocked with no remedy, so it
// is a hard stop even interactively (the install command normally refuses it
// earlier with UnsupportedError).
func (PlatformStep) Detect(_ context.Context, rc ReadPorts, _ *RunState) Detection {
	p := rc.Platform
	if err := UnsupportedError(p); err != nil {
		return Detection{State: StateBlocked, Detail: err.Error()}
	}
	detail := fmt.Sprintf("%s (%s)", firstNonEmpty(p.OSVersion, p.OS), p.Arch)
	if p.JobsBackend != "" {
		detail += "; jobs: " + p.JobsBackend
	}
	d := Detection{State: StateOK, Detail: detail}
	if p.OS == OSDarwin && !slices.Contains(p.PackageManagers, "brew") {
		d.Notes = append(d.Notes, Note{NoteInfo, "Homebrew not found: install hints will be generic"})
	}
	if p.WSL {
		d.Notes = append(d.Notes, Note{NoteInfo, "WSL detected: install runs against the Linux side"})
	}
	return d
}

// Plan implements Step: the step has nothing to apply.
func (PlatformStep) Plan(context.Context, ReadPorts, *RunState, Choices) (Plan, error) {
	return Plan{}, nil
}

// Apply implements Step.
func (PlatformStep) Apply(context.Context, WritePorts, *RunState, Plan) (StepResult, error) {
	return StepResult{}, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
