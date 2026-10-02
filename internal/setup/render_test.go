package setup

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

const renderSecret = "s3cr3t-Passw0rd-xyz"

func newTestRenderer(o RenderOptions) (*Renderer, *bytes.Buffer) {
	red := NewRedactor()
	red.Register(renderSecret)
	var buf bytes.Buffer
	return NewRenderer(&buf, red, o), &buf
}

func renderRows() []StatusRow {
	return []StatusRow{
		{StepID: "binary", Title: "Binary", State: StateOK, Choice: "keep", Detail: "/home/u/.local/bin/claude-memory"},
		{StepID: "envfile", Title: "Env file", State: StateModified, Choice: "keep",
			Detail: "MEMORY_PG_DSN=postgresql://claude_memory:" + renderSecret + "@db:5432/claude_memory differs",
			Artifacts: []ArtifactState{
				{ID: "envfile/format", State: StateOK},
				{ID: "envfile/MEMORY_PG_DSN", State: StateModified, Detail: "differs from recorded"},
				{ID: "envfile/mode", State: StateOutdated, Detail: "mode 0644, want 0600"},
			}},
		{StepID: "hooks.settings", Title: "Hooks (settings)", State: StateAbsent, Choice: "apply", Detail: "2 events to add"},
		{StepID: "jobs", Title: "Scheduled jobs", State: StateAbsent, Choice: "apply (after binary)"},
		{StepID: "ollama", Title: "Ollama", State: StateBlocked, Choice: "skip", Detail: "ollama not found"},
	}
}

func renderPlan() CombinedPlan {
	oldEnv := "MEMORY_OLLAMA_URL=http://localhost:11434\n"
	newEnv := oldEnv + "MEMORY_PG_DSN=postgresql://claude_memory:" + renderSecret + "@db:5432/claude_memory\n"
	oldSet := "{\n  \"a\": 1\n}\n"
	newSet := "{\n  \"a\": 1,\n  \"hooks\": {}\n}\n"
	return CombinedPlan{
		Steps: []StepPlan{
			{StepID: "binary", Title: "Binary"}, // nothing to do: omitted
			{StepID: "envfile", Title: "Env file", Plan: Plan{
				Actions: []Action{{Artifact: "envfile/MEMORY_PG_DSN", Verb: "write", Path: "~/.config/claude-memory/env", Desc: "add MEMORY_PG_DSN"}, {Verb: "chmod", Path: "~/.config/claude-memory/env", Desc: "0600"}},
				Diffs:   []Diff{{Artifact: "envfile/MEMORY_PG_DSN", Path: "~/.config/claude-memory/env", Unified: UnifiedDiff("a/env", "b/env", []byte(oldEnv), []byte(newEnv))}},
				Notes:   []Note{{NoteInfo, "the password is kept in the env file only"}},
			}},
			{StepID: "hooks.settings", Title: "Hooks (settings)", Plan: Plan{
				Actions: []Action{{Verb: "write", Path: "~/.claude/settings.json"}},
				Diffs:   []Diff{{Path: "~/.claude/settings.json", Unified: UnifiedDiff("a/settings.json", "b/settings.json", []byte(oldSet), []byte(newSet))}},
			}},
			{StepID: "jobs", Title: "Scheduled jobs", AfterDep: "binary", Plan: Plan{
				Actions: []Action{{Verb: "load", Path: "io.github.claude-memory.digest", Desc: "launchd, daily"}},
				Notes:   []Note{{NoteWarn, "job PATH has no psql"}},
			}},
		},
		Notes: []Note{{NoteInfo, "1 step skipped: ollama"}},
	}
}

func renderOutcomes() []StepOutcome {
	return []StepOutcome{
		{StepID: "binary", Title: "Binary", Outcome: OutcomeUnchanged},
		{StepID: "envfile", Title: "Env file", Outcome: OutcomeApplied, Detail: "wrote 1 key",
			Notes: []Note{{NoteWarn, "drift: hooks.settings/Stop kept as is"}}},
		{StepID: "database", Title: "Database", Outcome: OutcomeFailed,
			Err: errors.New("connect postgresql://claude_memory:" + renderSecret + "@db:5432/x: refused"), Remedy: "check the host and port"},
		{StepID: "ollama", Title: "Ollama", Outcome: OutcomeBlocked, Detail: "ollama not found", Remedy: "brew install ollama", Hard: true},
		{StepID: "jobs", Title: "Scheduled jobs", Outcome: OutcomeBlocked, Detail: "needs database", Hard: false},
		{StepID: "skills", Title: "Skills", Outcome: OutcomeSkipped, Detail: "skipped by --skip"},
		{StepID: "claude-md", Title: "CLAUDE.md", Outcome: OutcomeNotRun},
	}
}

func assertNoSecret(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, renderSecret) {
		t.Errorf("secret leaked in output:\n%s", out)
	}
}

func TestRenderTableGolden(t *testing.T) {
	r, buf := newTestRenderer(RenderOptions{})
	r.Table(renderRows())
	assertNoSecret(t, buf.String())
	checkGolden(t, "testdata/render/table.golden", buf.Bytes())
}

func TestRenderPlanGolden(t *testing.T) {
	r, buf := newTestRenderer(RenderOptions{})
	r.Plan(renderPlan())
	out := buf.String()
	assertNoSecret(t, out)
	if !strings.Contains(out, "+MEMORY_PG_DSN=postgresql://claude_memory:***@db:5432/claude_memory") {
		t.Errorf("DSN diff line not masked as ***:\n%s", out)
	}
	checkGolden(t, "testdata/render/plan.golden", buf.Bytes())
}

func TestRenderDryRunGolden(t *testing.T) {
	r, buf := newTestRenderer(RenderOptions{DryRun: true})
	r.Notes([]Note{
		{NoteWarn, "Claude config dir changed since the last install (was ~/.claude, now ~/.claude-work)"},
		{NoteInfo, "manifest backup written to manifest.json.corrupt-1"},
		{NoteWarn, "this is a downgrade (manifest 0.9.0, binary 0.8.0)"},
	})
	r.Table(renderRows())
	r.Plan(renderPlan())
	r.Summary(RunResult{DryRun: true})
	assertNoSecret(t, buf.String())
	checkGolden(t, "testdata/render/dryrun.golden", buf.Bytes())
}

func TestRenderNotesGolden(t *testing.T) {
	r, buf := newTestRenderer(RenderOptions{})
	r.Notes([]Note{
		{NoteInfo, "first info"},
		{NoteWarn, "first warn\nsecond line of it"},
		{NoteInfo, "second info"},
		{NoteWarn, "second warn"},
	})
	r.Notes(nil)
	checkGolden(t, "testdata/render/notes.golden", buf.Bytes())
}

func TestRenderApplyLinesGolden(t *testing.T) {
	r, buf := newTestRenderer(RenderOptions{})
	r.Await("database", []string{"Run:", "  psql -f ~/.config/claude-memory/bootstrap.sql", "then choose re-check."})
	for _, o := range renderOutcomes() {
		r.StepDone(o)
	}
	r.Summary(RunResult{ExitCode: ExitFailed, Outcomes: renderOutcomes()})
	assertNoSecret(t, buf.String())
	checkGolden(t, "testdata/render/apply.golden", buf.Bytes())
}

func TestRenderSummaryVariants(t *testing.T) {
	tests := []struct {
		name string
		res  RunResult
		want string
	}{
		{"declined", RunResult{Declined: true}, "Declined: nothing was changed.\n"},
		{"empty", RunResult{}, "\nDone: nothing to do\n"},
		{"ok", RunResult{Outcomes: []StepOutcome{{Outcome: OutcomeApplied}, {Outcome: OutcomeApplied}, {Outcome: OutcomeUnchanged}}}, "\nDone: 2 applied · 1 unchanged\n"},
		{"interrupted", RunResult{ExitCode: ExitInterrupted, Outcomes: []StepOutcome{{Outcome: OutcomeApplied}, {Outcome: OutcomeNotRun}}}, "\nInterrupted: 1 applied · 1 not run\n"},
		{"dry run prints nothing", RunResult{DryRun: true}, ""},
	}
	for _, tc := range tests {
		r, buf := newTestRenderer(RenderOptions{})
		r.Summary(tc.res)
		if buf.String() != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, buf.String(), tc.want)
		}
	}
}

func TestRenderColorRules(t *testing.T) {
	tests := []struct {
		name  string
		o     RenderOptions
		color bool
	}{
		{"tty", RenderOptions{TTY: true, Env: Env{}}, true},
		{"not a tty", RenderOptions{TTY: false, Env: Env{}}, false},
		{"NO_COLOR", RenderOptions{TTY: true, Env: Env{"NO_COLOR": "1"}}, false},
		{"TERM dumb", RenderOptions{TTY: true, Env: Env{"TERM": "dumb"}}, false},
		{"CI does not turn colour off", RenderOptions{TTY: true, Env: Env{"CI": "true"}}, true},
	}
	for _, tc := range tests {
		r, buf := newTestRenderer(tc.o)
		r.Table(renderRows())
		r.Plan(renderPlan())
		for _, o := range renderOutcomes() {
			r.StepDone(o)
		}
		has := strings.Contains(buf.String(), "\x1b[")
		if has != tc.color {
			t.Errorf("%s: ANSI present = %v, want %v", tc.name, has, tc.color)
		}
		assertNoSecret(t, buf.String())
	}
}

// With colour on, stripping the escape codes yields exactly the plain output.
func TestRenderColorStripsToPlain(t *testing.T) {
	plain, pb := newTestRenderer(RenderOptions{})
	col, cb := newTestRenderer(RenderOptions{TTY: true, Env: Env{}})
	for _, r := range []*Renderer{plain, col} {
		r.Notes([]Note{{NoteWarn, "w"}, {NoteInfo, "i"}})
		r.Table(renderRows())
		r.Plan(renderPlan())
		for _, o := range renderOutcomes() {
			r.StepDone(o)
		}
	}
	stripped := ansiRe.ReplaceAllString(cb.String(), "")
	if stripped != pb.String() {
		t.Errorf("coloured output differs from plain after stripping:\n%s\n---\n%s", stripped, pb.String())
	}
}

func TestRenderProgressNonLive(t *testing.T) {
	for _, o := range []RenderOptions{
		{TTY: false, Env: Env{}},
		{TTY: true, Env: Env{"CI": "1"}},
	} {
		r, buf := newTestRenderer(o)
		for _, d := range []int64{0, 100, 500, 1024 * 1024} {
			r.Progress("ollama", "pulling nomic-embed-text", d, 1024*1024)
		}
		out := buf.String()
		if strings.Contains(out, "\r") || strings.Contains(out, "\x1b") {
			t.Errorf("%+v: cursor movement in non-live progress: %q", o, out)
		}
		if got := strings.Count(out, "\n"); got != 2 {
			t.Errorf("%+v: want one start and one end line, got %d lines: %q", o, got, out)
		}
		checkGolden(t, "testdata/render/progress_plain.golden", buf.Bytes())
	}
}

func TestRenderProgressLive(t *testing.T) {
	r, buf := newTestRenderer(RenderOptions{TTY: true, Env: Env{}})
	r.Progress("ollama", "pulling x", 0, 2048)
	r.Progress("ollama", "pulling x", 1024, 2048)
	out := buf.String()
	if strings.Count(out, "\r") != 2 || strings.Contains(out, "\n") {
		t.Errorf("live progress should rewrite one line: %q", out)
	}
	// Any other output closes the open line first.
	r.Notes([]Note{{NoteInfo, "n"}})
	if !strings.Contains(ansiRe.ReplaceAllString(buf.String(), ""), "(50%)\ninfo: n\n") {
		t.Errorf("open progress line not terminated before next output: %q", buf.String())
	}
	buf.Reset()
	r.Progress("ollama", "pulling x", 2048, 2048)
	if !strings.HasSuffix(buf.String(), "(100%)\n") {
		t.Errorf("finished live progress should end the line: %q", buf.String())
	}
}

func TestRenderProgressRedacts(t *testing.T) {
	for _, tty := range []bool{false, true} {
		r, buf := newTestRenderer(RenderOptions{TTY: tty, Env: Env{}})
		r.Progress("s", "pull "+renderSecret, 1, 2)
		r.Progress("s", "pull "+renderSecret, 2, 2)
		assertNoSecret(t, buf.String())
	}
}

func TestRenderRedactsEverySurface(t *testing.T) {
	dsn := "postgresql://u:" + renderSecret + "@h/db"
	r, buf := newTestRenderer(RenderOptions{})
	r.Notes([]Note{{NoteWarn, dsn}})
	r.Table([]StatusRow{{Title: dsn, State: StateOK, Choice: dsn, Detail: dsn, Artifacts: []ArtifactState{{ID: dsn, State: StateAbsent, Detail: dsn}, {ID: "b", State: StateOK}}}})
	r.Plan(CombinedPlan{Steps: []StepPlan{{Title: dsn, AfterDep: dsn, Plan: Plan{
		Actions: []Action{{Verb: dsn, Path: dsn, Desc: dsn}}, Diffs: []Diff{{Path: dsn, Unified: "+" + dsn + "\n"}}, Notes: []Note{{NoteInfo, dsn}}}}}, Notes: []Note{{NoteInfo, dsn}}})
	r.Await(dsn, []string{dsn})
	r.StepDone(StepOutcome{Title: dsn, Outcome: OutcomeFailed, Err: errors.New(dsn), Remedy: dsn, Notes: []Note{{NoteInfo, dsn}}})
	r.StepDone(StepOutcome{Title: dsn, Outcome: OutcomeApplied, Detail: dsn})
	assertNoSecret(t, buf.String())
	if !strings.Contains(buf.String(), "***") {
		t.Error("expected masked output")
	}
}

// The renderer writes each message with a single Write, so a secret can't be
// split across writes before a redacting writer sees it.
func TestRenderSingleWritePerMessage(t *testing.T) {
	cw := &countingWriter{}
	red := NewRedactor()
	r := NewRenderer(cw, red, RenderOptions{})
	r.Table(renderRows())
	r.Plan(renderPlan())
	r.StepDone(renderOutcomes()[1])
	if cw.writes != 3 {
		t.Errorf("writes = %d, want 3 (one per message)", cw.writes)
	}
}

type countingWriter struct{ writes int }

func (c *countingWriter) Write(p []byte) (int, error) { c.writes++; return len(p), nil }

func TestRenderConcurrentProgress(t *testing.T) {
	r, _ := newTestRenderer(RenderOptions{TTY: true, Env: Env{}})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := int64(0); d <= 10; d++ {
				r.Progress("s", "l", d, 10)
				r.Notes([]Note{{NoteInfo, "x"}})
			}
		}()
	}
	wg.Wait()
}

func TestRenderNilReporterPaths(t *testing.T) {
	var _ Reporter = (*Renderer)(nil)
	var _ ProgressSink = (*Renderer)(nil).Progress
	r, buf := newTestRenderer(RenderOptions{})
	r.Table(nil)
	r.Notes(nil)
	if buf.Len() != 0 {
		t.Errorf("empty input printed %q", buf.String())
	}
	r.Plan(CombinedPlan{})
	if !strings.Contains(buf.String(), "Nothing to do.") {
		t.Errorf("empty plan: %q", buf.String())
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
