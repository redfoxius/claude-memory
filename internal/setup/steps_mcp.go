package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MCPStepID is the id of the MCP registration step (AC-7: after
// hooks.settings).
const MCPStepID = "mcp"

// MCPStep registers the user-scope MCP server (AC-40):
// `claude mcp add --scope user claude-memory -- <bin> serve`, preceded by
// `claude mcp remove --scope user claude-memory` when an entry with another
// command, args or env is registered (outdated). It never passes -e and never
// writes .claude.json itself.
//
// Detect is a file read only (ReadMCPRegistration against RunState.BinPath):
// it never runs `claude mcp get|list` or the registered command, both of which
// spawn `claude-memory serve` (AC-67). An ok registration needs no `claude`
// call and no `claude` on PATH. Without `claude` on PATH an absent or outdated
// registration is blocked, with the command to run by hand as the remedy.
type MCPStep struct {
	// Version is the running binary's version, recorded on its artifact.
	Version string
}

var _ Step = MCPStep{}

// ID implements Step.
func (MCPStep) ID() string { return MCPStepID }

// Title implements Step.
func (MCPStep) Title() string { return "MCP registration" }

// Requires implements Step.
func (MCPStep) Requires() []string { return nil }

func mcpAddCommand(bin string) string {
	return "claude mcp add --scope user " + MCPServerName + " -- " + bin + " serve"
}

func mcpRemoveCommand() string { return "claude mcp remove --scope user " + MCPServerName }

// Detect implements Step.
func (MCPStep) Detect(_ context.Context, rc ReadPorts, st *RunState) Detection {
	bin := binTarget(rc, st)
	reg, err := ReadMCPRegistration(rc.FS, rc.Paths)
	if err != nil {
		return Detection{State: StateBlocked, Detail: "cannot read " + rc.Paths.ClaudeJSON + ": " + err.Error()}
	}
	if reg.ParseError != nil {
		// Claude Code owns and rewrites this file; registering against a
		// file we cannot read would hide what is there.
		return Detection{State: StateBlocked, Detail: reg.ParseError.Error() + " (registration unknown)",
			Remedy: "restart Claude Code, then re-run; or register by hand: " + mcpAddCommand(bin)}
	}
	state, detail := reg.State(bin)
	d := Detection{State: state, Detail: detail, Artifacts: []ArtifactState{{ID: MCPStepID, State: state, Detail: detail}}}
	if len(reg.LocalShadows) > 0 {
		d.Notes = append(d.Notes, Note{NoteWarn, "a local-scope " + MCPServerName + " entry overrides the user scope in: " +
			strings.Join(capList(reg.LocalShadows, 3), ", ") + "; remove it with `claude mcp remove --scope local " + MCPServerName + "` there"})
	}
	if state == StateOK {
		return d
	}
	if _, err := rc.Runner.LookPath("claude"); err != nil {
		remedy := mcpAddCommand(bin)
		if state == StateOutdated {
			remedy = mcpRemoveCommand() + " && " + remedy
		}
		return Detection{State: StateBlocked, Detail: "the claude CLI is not on PATH; " + detail, Remedy: remedy, Notes: d.Notes}
	}
	return d
}

// Plan implements Step.
func (MCPStep) Plan(_ context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error) {
	var p Plan
	if ch[MCPStepID] != ChoiceApply {
		return p, nil
	}
	bin := binTarget(rc, st)
	reg, err := ReadMCPRegistration(rc.FS, rc.Paths)
	if err != nil {
		return p, err
	}
	state, _ := reg.State(bin)
	switch state {
	case StateOK:
		return p, nil
	case StateOutdated:
		p.Actions = append(p.Actions, Action{Artifact: MCPStepID, Verb: "run", Desc: mcpRemoveCommand()})
	}
	p.Actions = append(p.Actions, Action{Artifact: MCPStepID, Verb: "register", Desc: mcpAddCommand(bin)})
	return p, nil
}

// Apply implements Step: it re-reads the registration and runs exactly the
// `claude` calls the state calls for.
func (m MCPStep) Apply(ctx context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error) {
	var res StepResult
	if len(p.Actions) == 0 {
		return res, nil
	}
	if wc.ClaudeCLI == nil {
		return res, errors.New("no claude CLI adapter is configured")
	}
	bin := binTarget(wc.ReadPorts, st)
	reg, err := ReadMCPRegistration(wc.FS, wc.Paths)
	if err != nil {
		return res, err
	}
	state, _ := reg.State(bin)
	if state == StateOutdated {
		if err := wc.ClaudeCLI.MCPRemove(ctx, MCPServerName); err != nil {
			return res, fmt.Errorf("claude mcp remove: %w", err)
		}
	}
	if state != StateOK {
		if err := wc.ClaudeCLI.MCPAdd(ctx, MCPServerName, []string{bin, "serve"}); err != nil {
			return res, fmt.Errorf("claude mcp add: %w", err)
		}
	}
	res.Artifacts = append(res.Artifacts, Artifact{Step: MCPStepID, Kind: KindMCP, Path: wc.Paths.ClaudeJSON, Identity: MCPServerName, Version: m.Version})
	return res, nil
}
