package setup

import (
	"context"
	"errors"
)

// InstallSteps is the step registry of `claude-memory install` (AC-7), in
// execution order: platform, binary, prereqs, topology, envfile, database,
// migrate, ollama, namespaces, hooks.scripts, hooks.settings, mcp, doctor.
// The remaining slice-2b steps (skills, claude-md, jobs) arrive with their
// own work items; until then WritePorts.Jobs (and ReadPorts.Jobs) are nil, so
// the registry must not contain a jobs step (registry test). The mcp step
// needs WritePorts.ClaudeCLI, which the composition root sets.
//
// version is the running binary's version; red is the run's Redactor, which
// the steps that handle passwords or error text from the network need.
//
// The final `doctor` Step (AC-62) is registered last. It is
// a Finalizer: the engine runs it after Apply on read-only ports, never on a
// dry run; Inputs.NoDoctor makes it do nothing.
func InstallSteps(version string, red *Redactor) []Step {
	return []Step{
		PlatformStep{},
		BinaryStep{Version: version},
		PrereqsStep{},
		TopologyStep{},
		EnvFileStep{Version: version},
		DatabaseStep{Version: version, Redactor: red},
		MigrateStep{Redactor: red},
		OllamaStep{Redactor: red},
		NamespacesStep{Version: version},
		HooksScriptsStep{Version: version},
		HooksSettingsStep{Version: version},
		MCPStep{Version: version},
		DoctorStep{Version: version, Redactor: red},
	}
}

// AllStepIDs are the ids of the whole AC-7 pipeline, in order. `--skip`
// accepts every one of them, also those this build does not register (a no-op), so a
// script written for the full install keeps working.
var AllStepIDs = []string{
	"platform", "binary", "prereqs", "topology", "envfile", "database", "migrate", "ollama", "namespaces",
	"hooks.scripts", "hooks.settings", "mcp", "skills", "claude-md", "jobs", "doctor",
}

// unownedChecks are doctor checks no install step owns (the manifest and the
// state directory are engine bookkeeping): their fails are never exempt.
var unownedChecks = []string{"manifest", "dirs.state"}

// DoctorStepID is the id of the final doctor step.
const DoctorStepID = "doctor"

// DoctorCheckSteps maps every doctor check id to the install step that owns
// it (AC-62): a fail of the check is exempt ("not installed: <step>
// skipped") only when that step was skipped by the user, is soft blocked by
// such a skip, or is not registered in this build. A test fails when a check
// has neither an entry nor a place in unownedChecks.
var DoctorCheckSteps = map[string]string{
	"binary.version":   "binary",
	"env.file":         "envfile",
	"env.perms":        "envfile",
	"env.format":       "envfile",
	"pg.connect":       "database",
	"pg.latency":       "database",
	"pg.vector":        "database",
	"pg.schema":        "migrate",
	"ollama.reachable": "ollama",
	"ollama.model":     "ollama",
	"ollama.embed":     "ollama",
	"tools.git":        "prereqs",
	"tools.claude":     "prereqs",
	"tools.az":         "prereqs",
	"mcp.registered":   "mcp",
	"hooks.scripts":    "hooks.scripts",
	"hooks.settings":   "hooks.settings",
	"skills":           "skills",
	"claude-md":        "claude-md",
	"namespaces":       "namespaces",
	"jobs":             "jobs",
}

// DoctorStep is the final doctor (AC-62). Its Detect/Plan/Apply are inert (it
// always reports ok, so the engine never plans or applies it); the work is in
// Final.
type DoctorStep struct {
	Version  string
	Redactor *Redactor
}

var (
	_ Step      = DoctorStep{}
	_ Finalizer = DoctorStep{}
)

// ID implements Step.
func (DoctorStep) ID() string { return DoctorStepID }

// Title implements Step.
func (DoctorStep) Title() string { return "Final doctor" }

// Requires implements Step.
func (DoctorStep) Requires() []string { return nil }

// Detect implements Step.
func (DoctorStep) Detect(_ context.Context, _ ReadPorts, st *RunState) Detection {
	detail := "runs last, read-only"
	if st != nil {
		switch {
		case st.Inputs.NoDoctor:
			detail = "disabled by --no-doctor"
		case st.Inputs.DryRun:
			detail = "not run in --dry-run"
		}
	}
	return Detection{State: StateOK, Detail: detail}
}

// Plan implements Step.
func (DoctorStep) Plan(context.Context, ReadPorts, *RunState, Choices) (Plan, error) {
	return Plan{}, nil
}

// Apply implements Step; the engine never calls it (Detect is always ok).
func (DoctorStep) Apply(context.Context, WritePorts, *RunState, Plan) (StepResult, error) {
	return StepResult{}, errors.New("doctor: nothing to apply")
}

// Final implements Finalizer: the doctor registry in-process on the read-only
// ports, per-check timeout and overall deadline as the standalone doctor.
func (s DoctorStep) Final(ctx context.Context, rc ReadPorts, st *RunState, fv FinalView) FinalResult {
	if st.Inputs.NoDoctor {
		return FinalResult{}
	}
	deps := DoctorDeps{
		Paths: rc.Paths, Env: rc.Env, Platform: rc.Platform, FS: rc.FS, Runner: rc.Runner,
		Clock: rc.Clock, DB: rc.DB, Ollama: rc.Ollama, Assets: rc.Assets,
		Version: s.Version, Redactor: s.Redactor,
	}
	rep := RunDoctor(ctx, deps, DoctorOptions{})
	for i, c := range rep.Checks {
		if c.Status != StatusFail {
			continue
		}
		if name, ok := fv.NotInstalled[DoctorCheckSteps[c.ID]]; ok {
			rep.Checks[i].NotInstalled = "not installed: " + name + " skipped"
		}
	}
	// Running MCP servers and hooks read the binary, env file and namespaces
	// at start, so any applied change calls for a restart (AC-62 v0.6).
	restart := len(st.Applied) > 0
	return FinalResult{
		Ran: true, Report: rep, Restart: restart,
		Meta: ReportMeta{Version: s.Version, Platform: rc.Platform, ConfigDir: rc.Paths.ClaudeDir, Bin: rc.Paths.Self},
	}
}
