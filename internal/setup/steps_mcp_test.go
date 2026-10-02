package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Tests for WI-S2-10: the mcp step (AC-40 register, AC-51, AC-67 Detect half).
// The runner is a strict FakeRunner: any command it is asked to run that is
// not scripted fails the test, so a Detect or Plan that ran `claude` (or the
// registered server) would be caught.

type mcpRig struct {
	*hk
	claude *fakeClaude
}

func newMCPRig(t *testing.T) *mcpRig {
	h := newHK(t)
	h.runner.SetPath("claude", "/usr/bin/claude")
	return &mcpRig{hk: h, claude: newFakeClaude(t, h.p)}
}

func (r *mcpRig) wp() WritePorts {
	w := r.hk.wp()
	w.ClaudeCLI = r.claude
	return w
}

// runMCP is Detect -> Plan -> Apply like the engine, with the fake claude.
func (r *mcpRig) runMCP(st *RunState, choice Choice) hkRun {
	r.t.Helper()
	ctx, step := context.Background(), MCPStep{Version: "v1.0.0"}
	var out hkRun
	out.det = step.Detect(ctx, r.rp(), st)
	if out.det.State == StateBlocked {
		return out
	}
	ch := Choices{MCPStepID: DefaultChoice(out.det.State)}
	if choice != "" {
		ch[MCPStepID] = choice
	}
	out.plan, out.err = step.Plan(ctx, r.rp(), st, ch)
	if out.err == nil && hasApply(ch) {
		out.ran = true
		out.res, out.err = step.Apply(ctx, r.wp(), st, out.plan)
	}
	return out
}

func (r *mcpRig) register(command string, args string, env string) {
	r.t.Helper()
	r.write(r.p.ClaudeJSON, `{"mcpServers":{"claude-memory":{"type":"stdio","command":"`+command+`","args":`+args+`,"env":`+env+`}}}`, 0o600)
}

func TestMCPAbsentAddsThenNoOp(t *testing.T) {
	r := newMCPRig(t)
	bin := r.p.InstalledBinary()
	out := r.runMCP(r.state(nil), "")
	if out.err != nil || out.det.State != StateAbsent {
		t.Fatalf("detect %s err %v", out.det.State, out.err)
	}
	if got := r.claude.Calls(); len(got) != 1 || got[0] != "add claude-memory -- "+bin+" serve" {
		t.Errorf("claude calls %v, want one add", got)
	}
	if len(out.res.Artifacts) != 1 || out.res.Artifacts[0].Kind != KindMCP || out.res.Artifacts[0].Identity != MCPServerName ||
		out.res.Artifacts[0].Step != "mcp" {
		t.Errorf("artifacts %+v", out.res.Artifacts)
	}
	if len(out.plan.Actions) != 1 || out.plan.Actions[0].Verb != "register" ||
		out.plan.Actions[0].Desc != "claude mcp add --scope user claude-memory -- "+bin+" serve" {
		t.Errorf("plan %+v", out.plan.Actions)
	}

	// Run twice: ok, no claude call, no runner call at all, no PATH needed.
	r.runner = NewFakeRunner(t) // `claude` is no longer on PATH
	out = r.runMCP(r.state(manifestOf(out.res)), "")
	if out.det.State != StateOK || out.ran || len(r.claude.Calls()) != 1 {
		t.Errorf("second run: %s ran=%v calls=%v", out.det.State, out.ran, r.claude.Calls())
	}
	if n := len(r.runner.Calls()); n != 0 {
		t.Errorf("Detect ran %d commands, want 0", n)
	}
}

func TestMCPOutdatedRemovesThenAdds(t *testing.T) {
	cases := []struct{ name, command, args, env string }{
		{"other command", "/old/claude-memory", `["serve"]`, `{}`},
		{"other args", "BIN", `["serve","--x"]`, `{}`},
		{"env and command", "/old/claude-memory", `["serve"]`, `{"MEMORY_PG_DSN":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMCPRig(t)
			bin := r.p.InstalledBinary()
			r.register(strings.ReplaceAll(tc.command, "BIN", bin), tc.args, tc.env)
			out := r.runMCP(r.state(nil), "")
			if out.err != nil || out.det.State != StateOutdated {
				t.Fatalf("detect %s err %v", out.det.State, out.err)
			}
			want := []string{"remove claude-memory", "add claude-memory -- " + bin + " serve"}
			if got := r.claude.Calls(); strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("calls %v, want %v", got, want)
			}
			if len(out.plan.Actions) != 2 || !strings.HasPrefix(out.plan.Actions[0].Desc, "claude mcp remove --scope user claude-memory") {
				t.Errorf("plan %+v", out.plan.Actions)
			}
			if det := (MCPStep{}).Detect(context.Background(), r.rp(), r.state(nil)); det.State != StateOK {
				t.Errorf("after: %s (%s)", det.State, det.Detail)
			}
		})
	}
}

// AC-67 (Detect half): even when the registered command is a sentinel, Detect
// and Plan run nothing; an ok registration needs no claude call.
// F4: our command and args with an added env is a user customization:
// modified, kept by default, replaced (env dropped, named in the plan) only
// when the overwrite is confirmed.
func TestMCPEnvOnlyDifferenceIsModified(t *testing.T) {
	r := newMCPRig(t)
	bin := r.p.InstalledBinary()
	r.register(bin, `["serve"]`, `{"MEMORY_PG_DSN":"x","B_KEY":"y"}`)
	out := r.runMCP(r.state(nil), "")
	if out.err != nil || out.det.State != StateModified || out.ran || len(r.claude.Calls()) != 0 {
		t.Fatalf("default: state %s ran %v calls %v err %v", out.det.State, out.ran, r.claude.Calls(), out.err)
	}
	out = r.runMCP(r.state(nil), ChoiceApply)
	if out.err != nil {
		t.Fatal(out.err)
	}
	if want := "claude mcp remove --scope user claude-memory (drops its env: B_KEY, MEMORY_PG_DSN)"; out.plan.Actions[0].Desc != want {
		t.Errorf("plan desc %q, want %q", out.plan.Actions[0].Desc, want)
	}
	if got := r.claude.Calls(); len(got) != 2 || got[0] != "remove claude-memory" {
		t.Errorf("calls %v", got)
	}
	if strings.Contains(out.plan.Actions[0].Desc, `"x"`) {
		t.Error("env values must never be shown")
	}
	if a := out.res.Artifacts[0]; a.Entry != bin+" serve" {
		t.Errorf("recorded entry %q (F6)", a.Entry)
	}
}

// A5: Apply refuses to register over an unreadable .claude.json.
func TestMCPApplyRefusesUnparseable(t *testing.T) {
	r := newMCPRig(t)
	st, ctx := r.state(nil), context.Background()
	plan, _ := MCPStep{}.Plan(ctx, r.rp(), st, Choices{MCPStepID: ChoiceApply})
	r.write(r.p.ClaudeJSON, "{ half", 0o600) // Claude Code is mid-write
	if _, err := (MCPStep{}).Apply(ctx, r.wp(), st, plan); err == nil || len(r.claude.Calls()) != 0 {
		t.Errorf("err %v calls %v", err, r.claude.Calls())
	}
}

// F2: an ok hand-installed registration is adopted.
func TestMCPAdopt(t *testing.T) {
	r := newMCPRig(t)
	st, ctx := r.state(nil), context.Background()
	if got := (MCPStep{Version: "v1"}).Adopt(ctx, r.rp(), st); len(got) != 0 {
		t.Errorf("absent registration adopted: %+v", got)
	}
	r.register("/old/claude-memory", `["serve"]`, `{}`)
	if got := (MCPStep{Version: "v1"}).Adopt(ctx, r.rp(), st); len(got) != 0 {
		t.Errorf("outdated registration adopted: %+v", got)
	}
	r.register(r.p.InstalledBinary(), `["serve"]`, `{}`)
	got := (MCPStep{Version: "v1"}).Adopt(ctx, r.rp(), st)
	if len(got) != 1 || got[0].Kind != KindMCP || got[0].Entry != r.p.InstalledBinary()+" serve" {
		t.Errorf("adopt = %+v", got)
	}
}

func TestMCPDetectNeverRunsAnything(t *testing.T) {
	r := newMCPRig(t)
	sentinelCmd := r.root + "/sentinel-server"
	r.register(sentinelCmd, `["serve"]`, `{}`)
	r.runner.Deny(Argv0("sentinel-server"), "the registered MCP command must never run")
	r.runner.Deny(func(c Cmd) bool { return len(c.Argv) > 0 && c.Argv[0] == "claude" }, "claude must not run during Detect/Plan")
	st, ctx := r.state(nil), context.Background()
	det := MCPStep{}.Detect(ctx, r.rp(), st)
	if det.State != StateOutdated {
		t.Fatalf("detect = %s", det.State)
	}
	if _, err := (MCPStep{}).Plan(ctx, r.rp(), st, Choices{MCPStepID: ChoiceApply}); err != nil {
		t.Fatal(err)
	}
	if n := len(r.runner.Calls()); n != 0 {
		t.Errorf("%d commands ran: %v", n, r.runner.Calls())
	}
}

func TestMCPNoClaudeOnPathBlocksWithCommand(t *testing.T) {
	h := newHK(t) // no claude on PATH
	bin := h.p.InstalledBinary()
	det := MCPStep{}.Detect(context.Background(), h.rp(), h.state(nil))
	if det.State != StateBlocked || det.BlockedBy != "" {
		t.Fatalf("detect %s (%s)", det.State, det.Detail)
	}
	if want := "claude mcp add --scope user claude-memory -- " + bin + " serve"; det.Remedy != want {
		t.Errorf("remedy %q, want %q", det.Remedy, want)
	}
	// Outdated: the remedy removes first.
	hh := &mcpRig{hk: h, claude: newFakeClaude(t, h.p)}
	hh.register("/old/claude-memory", `["serve"]`, `{}`)
	det = MCPStep{}.Detect(context.Background(), h.rp(), h.state(nil))
	if det.State != StateBlocked || !strings.HasPrefix(det.Remedy, "claude mcp remove --scope user claude-memory && claude mcp add") {
		t.Errorf("outdated + no claude: %s %q", det.State, det.Remedy)
	}
	// ok needs no claude at all.
	hh.register(bin, `["serve"]`, `{}`)
	if det := (MCPStep{}).Detect(context.Background(), h.rp(), h.state(nil)); det.State != StateOK {
		t.Errorf("ok registration without claude: %s", det.State)
	}
}

func TestMCPUnreadableFileBlocked(t *testing.T) {
	r := newMCPRig(t)
	r.write(r.p.ClaudeJSON, "{ half written", 0o600)
	det := MCPStep{}.Detect(context.Background(), r.rp(), r.state(nil))
	if det.State != StateBlocked || det.Remedy == "" || !strings.Contains(det.Detail, "registration unknown") {
		t.Errorf("detect %s (%s) remedy %q", det.State, det.Detail, det.Remedy)
	}
	if string(mustRead(t, r.p.ClaudeJSON)) != "{ half written" {
		t.Error(".claude.json was touched")
	}
}

func TestMCPLocalShadowNote(t *testing.T) {
	r := newMCPRig(t)
	bin := r.p.InstalledBinary()
	r.write(r.p.ClaudeJSON, `{"mcpServers":{"claude-memory":{"type":"stdio","command":"`+bin+`","args":["serve"],"env":{}}},
		"projects":{"/work/x":{"mcpServers":{"claude-memory":{"command":"/other"}}}}}`, 0o600)
	det := MCPStep{}.Detect(context.Background(), r.rp(), r.state(nil))
	if det.State != StateOK || len(det.Notes) != 1 || !strings.Contains(det.Notes[0].Text, "/work/x") {
		t.Errorf("detect %s notes %+v", det.State, det.Notes)
	}
}

func TestMCPApplyFailures(t *testing.T) {
	t.Run("add fails", func(t *testing.T) {
		r := newMCPRig(t)
		r.claude.failAdd = errors.New("exit 1: boom")
		out := r.runMCP(r.state(nil), "")
		if out.err == nil || !strings.Contains(out.err.Error(), "claude mcp add") || len(out.res.Artifacts) != 0 {
			t.Errorf("err %v artifacts %+v", out.err, out.res.Artifacts)
		}
	})
	t.Run("remove fails: no add", func(t *testing.T) {
		r := newMCPRig(t)
		r.register("/old/claude-memory", `["serve"]`, `{}`)
		r.claude.failRemove = errors.New("exit 1")
		out := r.runMCP(r.state(nil), "")
		if out.err == nil || strings.Contains(strings.Join(r.claude.Calls(), "|"), "add") {
			t.Errorf("err %v calls %v", out.err, r.claude.Calls())
		}
	})
	t.Run("nil ClaudeCLI", func(t *testing.T) {
		r := newMCPRig(t)
		st := r.state(nil)
		plan, _ := MCPStep{}.Plan(context.Background(), r.rp(), st, Choices{MCPStepID: ChoiceApply})
		if _, err := (MCPStep{}).Apply(context.Background(), r.hk.wp(), st, plan); err == nil {
			t.Error("Apply without an adapter must fail, not panic")
		}
	})
	t.Run("keep: nothing planned", func(t *testing.T) {
		r := newMCPRig(t)
		out := r.runMCP(r.state(nil), ChoiceKeep)
		if len(out.plan.Actions) != 0 || len(r.claude.Calls()) != 0 {
			t.Errorf("plan %+v calls %v", out.plan, r.claude.Calls())
		}
	})
}
