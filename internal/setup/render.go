package setup

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// The install renderer (WI-S2-1b, Design 17). It implements Reporter and
// the ProgressSink. It is the only thing in an install run that prints:
// steps and checks return data. Every message is built whole, redacted
// through the Redactor, and only then coloured and written in one call, so a
// secret is never split across writes and never reaches the writer (AC-30).
//
// Contract: a step's Detection.Detail also reaches Prompter questions
// (engine askChoices), which this renderer does not print. The TTY Prompter
// (WI-S2-2) must redact questions itself, and steps must never put a DSN or
// other secret in Detail.

// RenderOptions says how the renderer behaves on its writer (AC-14).
type RenderOptions struct {
	// TTY: the writer is a terminal (stdout is a TTY). cmd decides this.
	TTY bool
	// Env supplies NO_COLOR, TERM and CI.
	Env Env
	// DryRun adds the "nothing was changed" footer to the plan.
	DryRun bool
}

// Renderer prints install output. Safe for concurrent use (the progress sink
// may be called from the Pull goroutine).
type Renderer struct {
	w     io.Writer
	red   *Redactor
	color bool
	live  bool // single-line progress with cursor movement
	dry   bool

	mu       sync.Mutex
	liveOpen bool            // a progress line without its newline is on screen
	started  map[string]bool // progress start line printed, by step+label
}

var (
	_ Reporter = (*Renderer)(nil)
)

// NewRenderer returns a Renderer writing to w. Colour needs a TTY, no
// NO_COLOR (non-empty) and TERM other than "dumb"; live progress needs a TTY
// and no CI (non-empty) (AC-14).
func NewRenderer(w io.Writer, red *Redactor, o RenderOptions) *Renderer {
	if red == nil {
		red = NewRedactor()
	}
	return &Renderer{
		w:       w,
		red:     red,
		color:   o.TTY && o.Env.Get("NO_COLOR") == "" && o.Env.Get("TERM") != "dumb",
		live:    o.TTY && o.Env.Get("CI") == "",
		dry:     o.DryRun,
		started: map[string]bool{},
	}
}

// ANSI colours.
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiClear  = "\r\x1b[2K"
)

func (r *Renderer) paint(code, s string) string {
	if !r.color || s == "" {
		return s
	}
	return code + s + ansiReset
}

// emit redacts s and writes it in one call, first closing an open progress
// line. Colour codes are added by the callers after redaction of their
// inputs, so the final redact here only needs to catch strings assembled
// from already-redacted parts (it is idempotent).
func (r *Renderer) emit(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitLocked(s)
}

func (r *Renderer) emitLocked(s string) {
	if r.liveOpen {
		s = "\n" + s
		r.liveOpen = false
	}
	_, _ = io.WriteString(r.w, s)
}

// rd redacts a text fragment before it is styled.
func (r *Renderer) rd(s string) string { return r.red.Redact(s) }

// ---- Reporter -------------------------------------------------------------

// Notes prints notes grouped warn first, then info.
func (r *Renderer) Notes(ns []Note) {
	if len(ns) == 0 {
		return
	}
	r.emit(r.notesText(ns, ""))
}

func (r *Renderer) notesText(ns []Note, indent string) string {
	var b strings.Builder
	for _, lvl := range []NoteLevel{NoteWarn, NoteInfo} {
		for _, n := range ns {
			if (n.Level == NoteWarn) != (lvl == NoteWarn) {
				continue
			}
			label, code := "info", ansiCyan
			if lvl == NoteWarn {
				label, code = "warn", ansiYellow
			}
			fmt.Fprintf(&b, "%s%s %s\n", indent, r.paint(code, label+":"), r.indentCont(r.rd(n.Text), indent+"      "))
		}
	}
	return b.String()
}

// indentCont indents the continuation lines of a multi-line text.
func (r *Renderer) indentCont(s, indent string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+indent)
}

// Table prints the status table: one row per step (STEP, STATE, CHOICE,
// DETAIL), with the non-ok artifacts of multi-artifact steps beneath.
func (r *Renderer) Table(rows []StatusRow) {
	if len(rows) == 0 {
		return
	}
	titleW, stateW, choiceW := len("STEP"), len("STATE"), len("CHOICE")
	for _, row := range rows {
		titleW = max(titleW, utf8.RuneCountInString(r.rd(row.Title)))
		stateW = max(stateW, len(row.State))
		choiceW = max(choiceW, utf8.RuneCountInString(r.rd(row.Choice)))
	}
	var b strings.Builder
	b.WriteString(r.paint(ansiBold, pad("STEP", titleW)+"  "+pad("STATE", stateW)+"  "+pad("CHOICE", choiceW)+"  DETAIL"))
	b.WriteString("\n")
	for _, row := range rows {
		line := pad(r.rd(row.Title), titleW) + "  " + r.paint(stateColor(row.State), pad(string(row.State), stateW)) +
			"  " + pad(r.rd(row.Choice), choiceW)
		if d := r.rd(row.Detail); d != "" {
			line += "  " + d
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
		if len(row.Artifacts) > 1 {
			for _, a := range row.Artifacts {
				if a.State == StateOK {
					continue
				}
				al := "  - " + r.rd(a.ID) + " " + r.paint(stateColor(a.State), string(a.State))
				if d := r.rd(a.Detail); d != "" {
					al += " " + d
				}
				b.WriteString(al + "\n")
			}
		}
	}
	r.emit(b.String() + "\n")
}

func stateColor(s State) string {
	switch s {
	case StateOK:
		return ansiGreen
	case StateAbsent, StateOutdated:
		return ansiYellow
	case StateModified, StateBlocked:
		return ansiRed
	}
	return ""
}

func pad(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// Plan prints the combined plan: per step the actions, its redacted unified
// diffs and notes, then the plan-level notes. Under --dry-run it ends with
// the "nothing was changed" line (AC-13).
func (r *Renderer) Plan(cp CombinedPlan) {
	var b strings.Builder
	b.WriteString(r.paint(ansiBold, "Plan") + "\n")
	any := false
	for _, sp := range cp.Steps {
		if len(sp.Plan.Actions) == 0 && len(sp.Plan.Diffs) == 0 && len(sp.Plan.Notes) == 0 {
			continue
		}
		any = true
		head := r.rd(sp.Title)
		if sp.AfterDep != "" {
			head += " (after " + r.rd(sp.AfterDep) + ")"
		}
		b.WriteString("\n" + r.paint(ansiBold, head) + "\n")
		for _, a := range sp.Plan.Actions {
			line := "  " + pad(r.rd(a.Verb), 8) + r.rd(a.Path)
			if a.Desc != "" {
				line += " — " + r.rd(a.Desc)
			}
			b.WriteString(strings.TrimRight(line, " ") + "\n")
		}
		for _, d := range sp.Plan.Diffs {
			b.WriteString(r.diffText(d))
		}
		b.WriteString(r.notesText(sp.Plan.Notes, "  "))
	}
	if !any {
		b.WriteString("\nNothing to do.\n")
	}
	if len(cp.Notes) > 0 {
		b.WriteString("\n" + r.notesText(cp.Notes, ""))
	}
	if r.dry {
		b.WriteString("\n" + r.paint(ansiBold, "Dry run: nothing was changed.") + "\n")
	}
	r.emit(b.String() + "\n")
}

// diffText renders one Diff, redacted, indented by two spaces, with
// +/-/@@ lines coloured.
func (r *Renderer) diffText(d Diff) string {
	u := r.rd(d.Unified)
	if u == "" {
		return ""
	}
	var b strings.Builder
	if d.Path != "" {
		b.WriteString("  " + r.paint(ansiDim, "diff "+r.rd(d.Path)) + "\n")
	}
	for _, l := range strings.SplitAfter(strings.TrimSuffix(u, "\n"), "\n") {
		l = strings.TrimSuffix(l, "\n")
		code := ""
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			code = ansiBold
		case strings.HasPrefix(l, "@@"):
			code = ansiCyan
		case strings.HasPrefix(l, "+"):
			code = ansiGreen
		case strings.HasPrefix(l, "-"):
			code = ansiRed
		}
		b.WriteString("    " + r.paint(code, l) + "\n")
	}
	return b.String()
}

// Await prints the instructions of a step that waits for the user.
func (r *Renderer) Await(stepID string, instructions []string) {
	var b strings.Builder
	b.WriteString(r.paint(ansiYellow, "…") + " " + r.rd(stepID) + " — waiting for you\n")
	for _, in := range instructions {
		b.WriteString("    " + r.indentCont(r.rd(in), "    ") + "\n")
	}
	r.emit(b.String())
}

// StepDone prints the Apply line: `✓ step — detail`, or
// `✗ step — error · fix: …` (AC-14), then the step's notes.
func (r *Renderer) StepDone(o StepOutcome) {
	title := r.rd(o.Title)
	detail := r.rd(o.Detail)
	var line string
	switch o.Outcome {
	case OutcomeApplied:
		line = r.paint(ansiGreen, "✓") + " " + title + withPrefix(" — ", detail)
	case OutcomeUnchanged:
		line = r.paint(ansiGreen, "✓") + " " + title + " — " + valueOr(detail, "unchanged")
	case OutcomeFailed:
		msg := detail
		if msg == "" && o.Err != nil {
			msg = r.red.RedactError(o.Err)
		}
		line = r.paint(ansiRed, "✗") + " " + title + withPrefix(" — ", msg) + withPrefix(" · fix: ", r.rd(o.Remedy))
	case OutcomeBlocked:
		mark := "–"
		if o.Hard {
			mark = r.paint(ansiRed, "✗")
		}
		line = mark + " " + title + " — blocked" + withPrefix(": ", detail) + withPrefix(" · fix: ", r.rd(o.Remedy))
	case OutcomeSkipped:
		line = "– " + title + " — " + valueOr(detail, "skipped")
	default: // OutcomeNotRun
		line = "– " + title + " — not run"
	}
	r.emit(line + "\n" + r.notesText(o.Notes, "    "))
}

func withPrefix(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}

func valueOr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Summary prints the final line of an install run. It is not part of
// Reporter; cmd calls it after Engine.Run returns.
func (r *Renderer) Summary(res RunResult) {
	if res.DryRun {
		return // the plan already ended with the dry-run line
	}
	if res.Declined {
		r.emit("Declined: nothing was changed.\n")
		return
	}
	var applied, unchanged, skipped, failed, blocked, notRun int
	for _, o := range res.Outcomes {
		switch o.Outcome {
		case OutcomeApplied:
			applied++
		case OutcomeUnchanged:
			unchanged++
		case OutcomeSkipped:
			skipped++
		case OutcomeFailed:
			failed++
		case OutcomeBlocked:
			blocked++
		case OutcomeNotRun:
			notRun++
		}
	}
	var parts []string
	for _, p := range []struct {
		n    int
		name string
	}{{applied, "applied"}, {unchanged, "unchanged"}, {skipped, "skipped"}, {blocked, "blocked"}, {failed, "failed"}, {notRun, "not run"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.name))
		}
	}
	if len(parts) == 0 {
		parts = []string{"nothing to do"}
	}
	head := "Done"
	code := ansiGreen
	switch {
	case res.ExitCode == ExitInterrupted:
		head, code = "Interrupted", ansiYellow
	case res.ExitCode != ExitOK:
		head, code = "Finished with problems", ansiRed
	}
	r.emit("\n" + r.paint(code, head) + ": " + strings.Join(parts, " · ") + "\n")
}

// ---- progress sink --------------------------------------------------------

// Progress is the ProgressSink for WritePorts.Progress. On a TTY with CI
// unset it rewrites a single line; otherwise it prints one start line and
// one end line (AC-14).
func (r *Renderer) Progress(step, label string, done, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	finished := total > 0 && done >= total
	label = r.rd(label)
	text := label
	if total > 0 {
		text += fmt.Sprintf(" %s / %s (%d%%)", humanBytes(done), humanBytes(total), done*100/total)
	} else if done > 0 {
		text += " " + humanBytes(done)
	}
	if r.live {
		line := ansiClear + "  " + text
		if !r.color {
			line = "\r  " + text + "\x1b[K" // erase to end of line, no colour code
		}
		_, _ = io.WriteString(r.w, line)
		r.liveOpen = true
		if finished {
			_, _ = io.WriteString(r.w, "\n")
			r.liveOpen = false
		}
		return
	}
	key := step + "\x00" + label
	if !r.started[key] {
		r.started[key] = true
		_, _ = io.WriteString(r.w, "  "+label+" …\n")
	}
	if finished {
		delete(r.started, key)
		_, _ = io.WriteString(r.w, "  "+label+" — done ("+humanBytes(total)+")\n")
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
