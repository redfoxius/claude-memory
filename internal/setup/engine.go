package setup

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// The install engine (plan Design 15-19, WI-S2-1c, WI-S2-14b): phases 0-7. It is
// synchronous, starts no goroutines, prints nothing (a Reporter receives the
// data) and reaches the outside only through ReadPorts / WritePorts. The
// final doctor (phase 7) runs through the Finalizer step; the summary is
// printed by the renderer after Run.

// Exit codes of Engine.Run (spec §10).
const (
	ExitOK          = 0
	ExitFailed      = 1   // a step failed, or stayed blocked/awaiting under --yes
	ExitUsage       = 2   // invalid flag, lock held, non-interactive without --yes
	ExitInterrupted = 130 // Ctrl-C, "quit", or a prompter that was interrupted
)

// ErrInterrupted is what a Prompter returns on Ctrl-C or end of input; the
// engine maps it (and a cancelled context) to exit 130.
var ErrInterrupted = errors.New("interrupted")

// ErrNeedsInput is what the non-interactive prompter handed to Configure
// returns under --yes: a step must read flags/Env/defaults instead of asking,
// and fail with the flag name when there is none (AC-12).
var ErrNeedsInput = errors.New("input needed but running non-interactively")

// FieldReaders maps a RunState field to the steps that read it (Design 16);
// rule A re-Detects these when Configure changed the field. Steps absent from
// the engine's list are ignored.
var FieldReaders = map[string][]string{
	"BinPath":        {"binary", "hooks.scripts", "mcp", "jobs"},
	"Topology":       {"topology", "database"},
	"DB":             {"envfile", "database", "migrate"},
	"Ollama":         {"envfile", "ollama"},
	"NSRules":        nil,
	"PRRepos":        {"envfile", "jobs"},
	"ClaudeMDTarget": {"claude-md"},
	"JobPATH":        {"jobs"},
}

// EnvKeyFields maps an env key to the RunState field it feeds. An env-key
// artifact has the ID "<step>/<KEY>"; rule A applies a changed one whose
// field the user answered in this run's Configure (spec AC-6 exception).
var EnvKeyFields = map[string]string{
	"MEMORY_PG_DSN":           "DB",
	"MEMORY_OLLAMA_URL":       "Ollama",
	"MEMORY_OLLAMA_MODEL":     "Ollama",
	"MEMORY_EMBED_MAX_TOKENS": "Ollama",
	"MEMORY_PR_INGEST_REPOS":  "PRRepos",
}

// MissingInputter is optional on a Step: it reports a required input that no
// source supplied, so Configure runs even when no artifact is chosen apply.
type MissingInputter interface {
	MissingInput(st *RunState) bool
}

// StatusRow is one row of the status table (AC-5).
type StatusRow struct {
	StepID    string
	Title     string
	State     State
	Choice    string // apply | keep | skip | "apply (after <dep>)" | "blocked"
	Detail    string
	Artifacts []ArtifactState
}

// StepPlan is one step's share of the combined plan.
type StepPlan struct {
	StepID, Title string
	Plan          Plan
	AfterDep      string // set for a step that is planned only after <dep> is applied
}

// CombinedPlan is what is shown before the single confirmation (AC-5, AC-13).
type CombinedPlan struct {
	Steps []StepPlan
	Notes []Note
}

// Outcome is how a step ended.
type Outcome string

// Step outcomes.
const (
	OutcomeUnchanged Outcome = "unchanged" // nothing to do, or everything kept
	OutcomeApplied   Outcome = "applied"
	OutcomeFailed    Outcome = "failed"
	OutcomeBlocked   Outcome = "blocked"
	OutcomeSkipped   Outcome = "skipped"
	OutcomeNotRun    Outcome = "not-run" // the run was interrupted before the step
)

// StepOutcome is the per-step result in RunResult.
type StepOutcome struct {
	StepID, Title string
	Outcome       Outcome
	Detail        string
	Remedy        string
	Err           error
	// Hard is set for a blocked step that makes the run exit 1 (an external
	// block under --yes, an awaiting step, a failed dependency); a step the
	// user skipped, and its dependents, are not hard.
	Hard bool
	// Notes holds the step's drift notes (a kept modified artifact, AC-52)
	// and the Notes of its Apply.
	Notes  []Note
	Result StepResult
}

// Reporter receives the engine's data for rendering (WI-S2-1b). A nil
// Reporter discards everything.
type Reporter interface {
	// Notes: preflight and Seed notes, printed before the status table, and
	// blocked-step messages.
	Notes(ns []Note)
	// Table: the status table after Detect, and the re-Detected rows again.
	Table(rows []StatusRow)
	// Plan: the combined plan with its diffs and notes.
	Plan(cp CombinedPlan)
	// Await: the instructions of a step that pauses for a user action.
	Await(stepID string, instructions []string)
	// StepDone: after each step that ran Apply or ended failed/blocked.
	StepDone(o StepOutcome)
}

// Adopter is optionally implemented by a Step to record artifacts that are
// already correct on disk but not in the manifest (a hand install, AC-51: "ok
// and recorded"). Adopt runs on read-only ports after Apply, never on a dry
// run, for steps that ran cleanly; it returns every artifact that is ok now
// (never one the step would still have to change, and never a directory it did
// not create). The engine records only those whose key the manifest lacks, so
// a re-run writes nothing (AC-50).
type Adopter interface {
	Adopt(ctx context.Context, rc ReadPorts, st *RunState) []Artifact
}

// FinalView is what the engine tells a Finalizer about the run it follows.
type FinalView struct {
	// NotInstalled maps a step id to the step name to print in "not
	// installed: <step> skipped" (AC-62): steps the user skipped, steps soft
	// blocked by such a skip, and ids this build does not register. A step
	// that was kept, or left blocked under --yes, is NOT in it.
	NotInstalled map[string]string
}

// FinalResult is the output of a Finalizer: the annotated doctor report to
// print and whether to print the restart line.
type FinalResult struct {
	Ran     bool // false: the step did nothing (--no-doctor)
	Report  DoctorReport
	Meta    ReportMeta
	Restart bool
}

// Finalizer is implemented by the one Step that runs after Apply (phase 7,
// AC-62). It gets read-only ports only. It runs only when the plan was
// confirmed and applied (never on --dry-run, a declined plan or Ctrl-C).
type Finalizer interface {
	Final(ctx context.Context, rc ReadPorts, st *RunState, fv FinalView) FinalResult
}

// FinalReporter is optionally implemented by a Reporter to print the final
// doctor report; a Reporter without it discards the report.
type FinalReporter interface {
	Final(fr FinalResult)
}

// RunResult is what Engine.Run returns.
type RunResult struct {
	ExitCode int
	Err      error // the usage error behind exit 2
	Outcomes []StepOutcome
	State    *RunState
	DryRun   bool // stopped after the plan
	Declined bool // the user declined the confirmation
}

// Engine runs the install phases over Steps (in AC-7 order).
type Engine struct {
	Steps    []Step
	Read     ReadPorts  // Detect, Seed, Configure, Plan
	Write    WritePorts // Apply; its FS/Runner are read-only adapters under --dry-run
	UI       Prompter
	Reporter Reporter
	Version  string // running binary version ("dev"/"" never warns on downgrade)
	// KnownIDs are step ids that --skip accepts although this build does not
	// register them (the 2b ids in 2a): skipping one is a no-op. Any other
	// unknown id is a usage error.
	KnownIDs []string
}

// Run executes phases 0-7 for in.
func (e *Engine) Run(ctx context.Context, in Inputs) RunResult {
	s := &session{e: e, ctx: ctx, in: in, st: NewRunState(in), idx: map[string]*stepRun{}}
	s.auto = in.Yes || in.Upgrade || (in.DryRun && !s.interactive())
	res := RunResult{State: s.st, DryRun: in.DryRun}
	code, err := s.run(&res)
	res.ExitCode, res.Err = code, err
	res.Outcomes = s.outcomes()
	return res
}

// ---- internals ------------------------------------------------------------

type abort struct {
	code int
	err  error
}

func (a *abort) Error() string { return a.err.Error() }
func (a *abort) Unwrap() error { return a.err }

type stepRun struct {
	step    Step
	det     Detection
	choices Choices

	userSkip bool   // --skip, an interactive skip, or a skipped Await
	skipWhy  string // "skipped by you" / "skipped by --skip"
	afterDep string // planned only once this prerequisite is applied

	resolved bool // a blocked step whose re-check passed
	failed   bool
	failErr  error
	blocked  string // reason; set when the step ends blocked
	hard     bool
	remedy   string

	plan    Plan
	planned bool // plan is current for choices (phase 5, or after rule B)

	applied bool
	ran     bool

	finalDetail string // the final doctor's summary line (outcome detail)
	result      StepResult
}

type session struct {
	e    *Engine
	ctx  context.Context
	in   Inputs
	st   *RunState
	auto bool
	runs []*stepRun
	idx  map[string]*stepRun

	man   *Manifest
	notes []Note
}

func (s *session) interactive() bool { return s.e.UI != nil && s.e.UI.Interactive() }

func (s *session) report() Reporter {
	if s.e.Reporter == nil {
		return nopReporter{}
	}
	return s.e.Reporter
}

type nopReporter struct{}

func (nopReporter) Notes([]Note)           {}
func (nopReporter) Table([]StatusRow)      {}
func (nopReporter) Plan(CombinedPlan)      {}
func (nopReporter) Await(string, []string) {}
func (nopReporter) StepDone(StepOutcome)   {}

// autoPrompter is what Configure gets under --yes: nothing is ever asked.
type autoPrompter struct{}

func (autoPrompter) Select(q string, _ []string, _ int) (int, error) {
	return 0, fmt.Errorf("%w: %s", ErrNeedsInput, q)
}
func (autoPrompter) Confirm(q string, _ bool) (bool, error) {
	return false, fmt.Errorf("%w: %s", ErrNeedsInput, q)
}
func (autoPrompter) Text(q, _ string, _ func(string) error) (string, error) {
	return "", fmt.Errorf("%w: %s", ErrNeedsInput, q)
}
func (autoPrompter) Secret(q string) (string, error) {
	return "", fmt.Errorf("%w: %s", ErrNeedsInput, q)
}
func (autoPrompter) Interactive() bool { return false }

func (s *session) ui() Prompter {
	if s.auto {
		return autoPrompter{}
	}
	return s.e.UI
}

// askErr maps a prompter error to an abort: interrupted → 130, else 2.
func (s *session) askErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInterrupted) || s.ctx.Err() != nil {
		return &abort{ExitInterrupted, ErrInterrupted}
	}
	return &abort{ExitUsage, err}
}

func (s *session) checkCtx() error {
	if s.ctx.Err() != nil {
		return &abort{ExitInterrupted, ErrInterrupted}
	}
	return nil
}

func (s *session) run(res *RunResult) (code int, err error) {
	if err := s.setup(); err != nil {
		return ExitUsage, err
	}
	if !s.auto && !s.interactive() {
		return ExitUsage, errors.New("non-interactive session: pass --yes and the needed flags")
	}

	// Phase 0: preflight.
	pre, err := Preflight(s.e.Write, s.in, s.e.Version, !s.in.DryRun)
	if err != nil {
		return ExitUsage, err
	}
	if pre.Unlock != nil {
		defer func() { _ = pre.Unlock() }()
	}
	s.st.Prior, s.st.Downgrade, s.st.ConfigDirChanged = pre.Prior, pre.Downgrade, pre.ConfigDirChanged
	s.man = cloneManifest(pre.Prior.Manifest)
	s.notes = append(s.notes, pre.Notes...)

	// Phase 1a: Seed.
	for _, r := range s.runs {
		sd, ok := r.step.(Seeder)
		if !ok {
			continue
		}
		ns, err := sd.Seed(s.ctx, s.e.Read, s.st)
		s.notes = append(s.notes, ns...)
		if err != nil {
			s.report().Notes(s.notes)
			return ExitUsage, fmt.Errorf("%s: %w", r.step.ID(), err)
		}
	}
	if len(s.notes) > 0 {
		s.report().Notes(s.notes)
	}

	body := func() error {
		// Phase 1b: Detect all, then the table.
		for _, r := range s.runs {
			s.detect(r)
		}
		s.initChoices()
		s.report().Table(s.rows(s.runs))

		// Phase 2: choices.
		if !s.auto {
			if err := s.askChoices(); err != nil {
				return err
			}
		}
		s.resolveAfterDep()

		// Phase 3 + 4: Configure (with the blocked-step prompt), rule A.
		before := s.snapshot()
		if err := s.configurePhase(); err != nil {
			return err
		}
		s.ruleA(before)

		// Phase 5: plan, one confirmation.
		cp := s.plan()
		s.report().Plan(cp)
		if s.in.DryRun {
			return nil
		}
		if s.hasWork() && !s.auto {
			ok, err := s.ui().Confirm("Apply this plan?", true)
			if err := s.askErr(err); err != nil {
				return err
			}
			if !ok {
				res.Declined = true
				return nil
			}
		}

		// Phase 6: apply, then phase 7: the final doctor.
		if err := s.applyPhase(); err != nil {
			return err
		}
		if err := s.adoptPhase(); err != nil {
			return err
		}
		return s.finalPhase()
	}
	if err := body(); err != nil {
		var a *abort
		if errors.As(err, &a) {
			return a.code, a.err
		}
		return ExitUsage, err
	}
	if s.ctx.Err() != nil {
		return ExitInterrupted, ErrInterrupted
	}
	if res.Declined {
		return ExitOK, nil
	}
	if s.exitFailed() {
		return ExitFailed, nil
	}
	return ExitOK, nil
}

func (s *session) setup() error {
	for _, st := range s.e.Steps {
		if _, dup := s.idx[st.ID()]; dup {
			return fmt.Errorf("engine: duplicate step id %q", st.ID())
		}
		r := &stepRun{step: st, choices: Choices{}}
		s.runs = append(s.runs, r)
		s.idx[st.ID()] = r
	}
	for _, r := range s.runs {
		for _, req := range r.step.Requires() {
			if _, ok := s.idx[req]; !ok {
				return fmt.Errorf("engine: step %q requires unknown step %q", r.step.ID(), req)
			}
		}
	}
	for _, id := range s.in.Skip {
		r, ok := s.idx[id]
		if !ok {
			if slices.Contains(s.e.KnownIDs, id) {
				continue
			}
			return fmt.Errorf("--skip: unknown step %q", id)
		}
		r.userSkip, r.skipWhy = true, "skipped by --skip"
	}
	return nil
}

// detect runs Detect for r (read-only ports) and normalises the result: a
// step that reports no artifacts and is not blocked gets one artifact named
// after the step.
func (s *session) detect(r *stepRun) {
	r.det = r.step.Detect(s.ctx, s.e.Read, s.st)
	if len(r.det.Artifacts) == 0 && r.det.State != StateBlocked {
		r.det.Artifacts = []ArtifactState{{ID: r.step.ID(), State: r.det.State, Detail: r.det.Detail}}
	}
}

func (s *session) defaultChoice(a ArtifactState) Choice {
	if a.State == StateOutdated && s.st.Downgrade {
		return ChoiceKeep
	}
	return DefaultChoice(a.State)
}

func (s *session) defaultChoices(r *stepRun) Choices {
	ch := Choices{}
	for _, a := range r.det.Artifacts {
		if r.userSkip {
			ch[a.ID] = ChoiceSkip
		} else {
			ch[a.ID] = s.defaultChoice(a)
		}
	}
	return ch
}

func (s *session) initChoices() {
	for _, r := range s.runs {
		r.choices = s.defaultChoices(r)
	}
}

func hasApply(ch Choices) bool {
	for _, c := range ch {
		if c == ChoiceApply {
			return true
		}
	}
	return false
}

func (r *stepRun) wantsApply() bool { return r.afterDep != "" || hasApply(r.choices) }

// resolveAfterDep marks a step blocked only by a pending prerequisite as
// "apply (after <dep>)" when that prerequisite is going to be applied.
func (s *session) resolveAfterDep() {
	for _, r := range s.runs {
		r.afterDep = ""
		if r.userSkip || r.det.State != StateBlocked || r.det.BlockedBy == "" {
			continue
		}
		if dep, ok := s.idx[r.det.BlockedBy]; ok && !dep.userSkip && !dep.failed && dep.wantsApply() {
			r.afterDep = dep.step.ID()
		}
	}
}

func (s *session) choiceLabel(r *stepRun) string {
	switch {
	case r.userSkip:
		return string(ChoiceSkip)
	case r.afterDep != "":
		return "apply (after " + r.afterDep + ")"
	case hasApply(r.choices):
		return string(ChoiceApply)
	case r.det.State == StateBlocked:
		return "blocked"
	case len(r.choices) == 0:
		return string(ChoiceSkip)
	}
	for _, c := range r.choices {
		if c == ChoiceSkip {
			return string(ChoiceSkip)
		}
	}
	return string(ChoiceKeep)
}

func (s *session) rows(runs []*stepRun) []StatusRow {
	out := make([]StatusRow, 0, len(runs))
	for _, r := range runs {
		out = append(out, StatusRow{
			StepID: r.step.ID(), Title: r.step.Title(), State: r.det.State,
			Choice: s.choiceLabel(r), Detail: r.det.Detail,
			Artifacts: slices.Clone(r.det.Artifacts),
		})
	}
	return out
}

// askChoices is phase 2, interactive only: one Select per step that has
// something to do, and an overwrite Confirm per modified artifact (AC-6).
func (s *session) askChoices() error {
	for _, r := range s.runs {
		if r.userSkip || r.det.State == StateBlocked {
			continue
		}
		pending := false
		defIdx := 1 // keep
		for _, a := range r.det.Artifacts {
			if a.State != StateOK {
				pending = true
			}
			if s.defaultChoice(a) == ChoiceApply {
				defIdx = 0
			}
		}
		if !pending {
			continue
		}
		// Contract: Detail may reach the Prompter here, unredacted (the engine
		// has no Redactor). Steps must never put a DSN or secret in Detail, and
		// the TTY Prompter must redact questions (see Prompter).
		q := fmt.Sprintf("%s [%s]: %s", r.step.Title(), r.det.State, r.det.Detail)
		i, err := s.ui().Select(q, []string{string(ChoiceApply), string(ChoiceKeep), string(ChoiceSkip)}, defIdx)
		if errors.Is(err, ErrTooManyAttempts) {
			s.skipStep(r) // AC-10: three invalid answers skip this step
			continue
		}
		if err := s.askErr(err); err != nil {
			return err
		}
		ch := Choices{}
		tooMany := false
		switch i {
		case 0:
		arts:
			for _, a := range r.det.Artifacts {
				switch a.State {
				case StateAbsent, StateOutdated:
					ch[a.ID] = ChoiceApply
				case StateModified:
					ok, err := s.ui().Confirm(fmt.Sprintf("overwrite your modified %s? a backup is kept", a.ID), false)
					if errors.Is(err, ErrTooManyAttempts) {
						tooMany = true
						break arts
					}
					if err := s.askErr(err); err != nil {
						return err
					}
					if ok {
						ch[a.ID] = ChoiceApply
					} else {
						ch[a.ID] = ChoiceKeep
					}
				default:
					ch[a.ID] = ChoiceKeep
				}
			}
		case 1:
			for _, a := range r.det.Artifacts {
				ch[a.ID] = ChoiceKeep
			}
		default:
			for _, a := range r.det.Artifacts {
				ch[a.ID] = ChoiceSkip
			}
			r.userSkip, r.skipWhy = true, "skipped by you"
		}
		if tooMany {
			s.skipStep(r)
			continue
		}
		r.choices = ch
	}
	return nil
}

// skipStep aborts one step as skipped by the user (AC-10: a prompt that got
// three invalid answers). Its choices become skip; the run goes on.
func (s *session) skipStep(r *stepRun) {
	r.userSkip, r.skipWhy = true, "skipped by you"
	r.choices = s.defaultChoices(r)
}

// ---- Configure and rule A -------------------------------------------------

type fieldSnap struct {
	set bool
	val any
	src Source
}

func mkSnap[T any](f Field[T]) fieldSnap { return fieldSnap{f.IsSet(), f.Get(), f.Source()} }

func (s *session) snapshot() map[string]fieldSnap {
	st := s.st
	return map[string]fieldSnap{
		"BinPath": mkSnap(st.BinPath), "Topology": mkSnap(st.Topology), "DB": mkSnap(st.DB),
		"Ollama": mkSnap(st.Ollama), "NSRules": mkSnap(st.NSRules), "PRRepos": mkSnap(st.PRRepos),
		"ClaudeMDTarget": mkSnap(st.ClaudeMDTarget), "JobPATH": mkSnap(st.JobPATH),
	}
}

func fieldChanged(a, b fieldSnap) bool {
	return a.set != b.set || !reflect.DeepEqual(a.val, b.val)
}

func (s *session) configurePhase() error {
	for _, r := range s.runs {
		if err := s.checkCtx(); err != nil {
			return err
		}
		if r.userSkip {
			continue
		}
		// A prerequisite whose block was just resolved (blockedPrompt, an earlier
		// step in this loop) makes r's Detection stale: r was Detected as
		// blocked by it, with no artifacts and no afterDep. Re-Detect it so it
		// gets Configure / MissingInput like any other step.
		if r.det.State == StateBlocked && r.det.BlockedBy != "" && s.idx[r.det.BlockedBy].resolved {
			s.detect(r)
			r.choices = s.defaultChoices(r)
			s.resolveAfterDep()
		}
		if r.det.State == StateBlocked && r.det.BlockedBy == "" {
			if err := s.blockedPrompt(r); err != nil {
				return err
			}
			if r.blocked != "" || r.userSkip {
				continue
			}
		}
		if r.det.State == StateBlocked && r.det.BlockedBy != "" && r.afterDep == "" {
			continue // blocked by a prerequisite that will not be applied
		}
		cf, ok := r.step.(Configurer)
		if !ok {
			continue
		}
		need := hasApply(r.choices) || r.afterDep != "" || s.in.Reconfigure
		if mi, ok := r.step.(MissingInputter); ok && mi.MissingInput(s.st) {
			need = true
		}
		if !need {
			continue
		}
		if err := cf.Configure(s.ctx, s.e.Read, s.ui(), s.st); err != nil {
			if errors.Is(err, ErrTooManyAttempts) {
				s.skipStep(r)
				continue
			}
			if errors.Is(err, ErrInterrupted) || s.ctx.Err() != nil {
				return &abort{ExitInterrupted, ErrInterrupted}
			}
			r.failed, r.failErr = true, err
		}
	}
	return nil
}

// blockedPrompt is the Configure-phase prompt for a step blocked by an
// external condition (Design 15.3, M1): re-check / skip / quit.
func (s *session) blockedPrompt(r *stepRun) error {
	why := r.det.Detail
	if s.auto || r.det.Remedy == "" {
		r.blocked, r.remedy, r.hard = "blocked: "+why, r.det.Remedy, true
		return nil
	}
	for {
		s.report().Notes([]Note{{NoteWarn, fmt.Sprintf("%s: blocked: %s · fix: %s", r.step.Title(), r.det.Detail, r.det.Remedy)}})
		i, err := s.ui().Select(fmt.Sprintf("%s is blocked", r.step.Title()), []string{"re-check", "skip", "quit"}, 1)
		if errors.Is(err, ErrTooManyAttempts) {
			s.skipStep(r)
			return nil
		}
		if err := s.askErr(err); err != nil {
			return err
		}
		switch i {
		case 1:
			r.userSkip, r.skipWhy = true, "skipped by you"
			r.choices = s.defaultChoices(r)
			return nil
		case 2:
			return &abort{ExitInterrupted, ErrInterrupted}
		}
		s.detect(r)
		if r.det.State != StateBlocked {
			r.resolved = true
			r.choices = s.defaultChoices(r)
			return nil
		}
	}
}

// ruleA is the re-Detect after Configure (Design 15.4): the readers of every
// changed field, and the dependents of every resolved step.
func (s *session) ruleA(before map[string]fieldSnap) {
	after := s.snapshot()
	var changed []string
	for k, a := range after {
		if fieldChanged(before[k], a) {
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)
	answered := map[string]bool{}
	for _, k := range changed {
		if after[k].src == SourcePrompt {
			answered[k] = true
		}
	}
	set := map[string]bool{}
	for _, k := range changed {
		for _, id := range FieldReaders[k] {
			set[id] = true
		}
	}
	for _, r := range s.runs {
		if r.resolved {
			for _, d := range s.runs {
				if d.det.BlockedBy == r.step.ID() {
					set[d.step.ID()] = true
				}
			}
		}
	}
	var redone []*stepRun
	for _, r := range s.runs {
		if !set[r.step.ID()] || r.userSkip || r.failed || r.blocked != "" {
			continue
		}
		old, oldCh := r.det, r.choices
		s.detect(r)
		r.choices = s.reconcile(r, old, oldCh, r.det, answered, !s.auto)
		redone = append(redone, r)
	}
	if len(redone) > 0 {
		s.resolveAfterDep()
		s.report().Table(s.rows(redone))
	}
}

// reconcile maps the previous choices onto a fresh Detect: an artifact whose
// state did not change keeps its choice; one whose state changed takes the
// AC-6 default; a re-Detect never escalates to an overwrite, except for an
// env key whose field the user answered interactively (AC-6 v0.5).
func (s *session) reconcile(r *stepRun, old Detection, oldCh Choices, now Detection, answered map[string]bool, interactive bool) Choices {
	oldState := map[string]State{}
	for _, a := range old.Artifacts {
		oldState[a.ID] = a.State
	}
	out := Choices{}
	for _, a := range now.Artifacts {
		prev, hadState := oldState[a.ID]
		c, hadChoice := oldCh[a.ID]
		switch {
		case r.userSkip:
			out[a.ID] = ChoiceSkip
		case interactive && a.State != StateOK && envKeyAnswered(a.ID, answered):
			// Checked before "state unchanged": a kept hand-edited key
			// (modified -> modified) is still overwritten by a new answer.
			out[a.ID] = ChoiceApply
		case hadState && hadChoice && prev == a.State:
			out[a.ID] = c
		default:
			out[a.ID] = s.defaultChoice(a)
		}
	}
	return out
}

// envKeyAnswered reports whether id is an env-key artifact ("envfile/<KEY>")
// whose RunState field the user answered in this run's Configure.
func envKeyAnswered(id string, answered map[string]bool) bool {
	key, ok := strings.CutPrefix(id, "envfile/")
	if !ok {
		return false
	}
	f, ok := EnvKeyFields[key]
	return ok && answered[f]
}

// ---- Plan -----------------------------------------------------------------

func (s *session) hasWork() bool {
	for _, r := range s.runs {
		if r.failed || r.blocked != "" || r.userSkip {
			continue
		}
		if r.wantsApply() {
			return true
		}
	}
	return false
}

func (s *session) plan() CombinedPlan {
	var cp CombinedPlan
	for _, r := range s.runs {
		cp.Notes = append(cp.Notes, r.det.Notes...)
		if r.userSkip || r.failed || r.blocked != "" || !r.wantsApply() {
			continue
		}
		sp := StepPlan{StepID: r.step.ID(), Title: r.step.Title(), AfterDep: r.afterDep}
		if r.afterDep == "" {
			p, err := r.step.Plan(s.ctx, s.e.Read, s.st, r.choices)
			if err != nil {
				r.failed, r.failErr = true, fmt.Errorf("plan: %w", err)
				continue
			}
			sp.Plan, r.plan, r.planned = p, p, true
		}
		cp.Steps = append(cp.Steps, sp)
	}
	return cp
}

// ---- Apply ----------------------------------------------------------------

func (s *session) applyPhase() error {
	for _, r := range s.runs {
		if err := s.checkCtx(); err != nil {
			return err
		}
		if !r.userSkip && !r.failed && r.blocked == "" && r.det.State == StateBlocked &&
			r.det.BlockedBy != "" && r.afterDep == "" {
			// Blocked by a prerequisite that is not going to be applied.
			dep := s.idx[r.det.BlockedBy]
			r.blocked, r.remedy = "blocked by "+r.det.BlockedBy+": "+r.det.Detail, r.det.Remedy
			// Like gate: a skipped or kept-absent prerequisite is a soft block
			// (the user chose not to install it); a failed or hard-blocked
			// one is hard.
			switch {
			case dep.userSkip:
				r.hard = false
			case dep.failed:
				r.hard = true
			case dep.blocked != "":
				r.hard = dep.hard
			case !dep.applied && dep.det.State == StateAbsent:
				r.hard = false
			default:
				r.hard = true
			}
			s.report().StepDone(s.outcomeOf(r))
			continue
		}
		if r.userSkip || r.failed || r.blocked != "" || !r.wantsApply() {
			continue
		}
		if s.gate(r) {
			s.report().StepDone(s.outcomeOf(r))
			continue
		}
		if err := s.applyStep(r); err != nil {
			return err
		}
		s.report().StepDone(s.outcomeOf(r))
	}
	return nil
}

// gate blocks r when a direct prerequisite failed, is blocked, or was
// skipped without being ok (AC-7).
func (s *session) gate(r *stepRun) bool {
	for _, req := range r.step.Requires() {
		d := s.idx[req]
		switch {
		case d.failed:
			r.blocked, r.hard = "blocked by "+req+": it failed", true
		case d.blocked != "":
			r.blocked, r.hard = "blocked by "+req+": "+strings.TrimPrefix(d.blocked, "blocked: "), d.hard
		case d.userSkip && d.det.State != StateOK:
			r.blocked, r.hard = "blocked by "+req+": "+d.skipWhy, false
		case !d.applied && d.det.State == StateAbsent:
			// Kept while absent: nothing was installed, so dependents cannot run
			// (soft block, like a user skip).
			r.blocked, r.hard = "blocked by "+req+": it is not installed (kept)", false
		default:
			continue
		}
		return true
	}
	return false
}

func (s *session) ruleBNeeded(r *stepRun) bool {
	seen := map[string]bool{}
	var walk func(id string) bool
	walk = func(id string) bool {
		for _, req := range s.idx[id].step.Requires() {
			if seen[req] {
				continue
			}
			seen[req] = true
			if s.st.Applied[req] || walk(req) {
				return true
			}
		}
		return false
	}
	return walk(r.step.ID())
}

func (s *session) applyStep(r *stepRun) error {
	// Rule B: a prerequisite was applied in this run, so r's picture is stale.
	if r.afterDep != "" || s.ruleBNeeded(r) {
		old, oldCh := r.det, r.choices
		s.detect(r)
		if r.det.State == StateBlocked {
			r.blocked, r.hard, r.remedy = "blocked: "+r.det.Detail, true, r.det.Remedy
			return nil
		}
		r.choices = s.reconcile(r, old, oldCh, r.det, nil, false)
		r.afterDep, r.planned = "", false
		if !hasApply(r.choices) {
			return nil
		}
	}
	chosen := applyIDs(r.choices)
	plan := r.plan
	if !r.planned {
		firstToken := r.plan.Token // from the plan the user confirmed (phase 5)
		var err error
		if plan, err = r.step.Plan(s.ctx, s.e.Read, s.st, r.choices); err != nil {
			r.failed, r.failErr = true, fmt.Errorf("plan: %w", err)
			return nil
		}
		// A Rule-B re-plan must not reset the Token: it carries what the
		// confirmed plan was based on, so a change since then is caught.
		if firstToken != "" && plan.Token != "" {
			plan.Token = firstToken
		}
	}
	r.ran = true
	res, err := r.step.Apply(s.ctx, s.e.Write, s.st, plan)
	if err != nil {
		r.failed, r.failErr = true, err
		return nil
	}
	if res.Await != nil {
		return s.awaitFlow(r, res, chosen)
	}
	return s.finish(r, res, chosen)
}

func applyIDs(ch Choices) []string {
	var ids []string
	for id, c := range ch {
		if c == ChoiceApply {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// verify re-Detects r and checks the success rule (Design 18): every artifact
// chosen apply must be ok. A kept modified artifact is not a failure.
func (s *session) verify(r *stepRun, chosen []string) error {
	s.detect(r)
	state := map[string]State{}
	for _, a := range r.det.Artifacts {
		state[a.ID] = a.State
	}
	var bad []string
	for _, id := range chosen {
		if state[id] != StateOK {
			got := string(state[id])
			if got == "" {
				got = "missing"
			}
			bad = append(bad, id+" is "+got)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("after apply: %s", strings.Join(bad, "; "))
	}
	return nil
}

// finish is the common success path of a normal Apply and the Await follow-up
// (Design 19 risk): the success rule, the manifest merge and write.
func (s *session) finish(r *stepRun, res StepResult, chosen []string) error {
	if err := s.verify(r, chosen); err != nil {
		r.failed, r.failErr = true, err
		return nil
	}
	r.result = res
	return s.commit(r, res)
}

func mergeResults(a, b StepResult) StepResult {
	a.Artifacts = append(a.Artifacts, b.Artifacts...)
	a.Removed = append(a.Removed, b.Removed...)
	a.Notes = append(a.Notes, b.Notes...)
	a.Diffs = append(a.Diffs, b.Diffs...)
	a.Await = b.Await
	return a
}

// commit records a successful step: the manifest (only on change, AC-9) and
// the run state.
func (s *session) commit(r *stepRun, res StepResult) error {
	if err := s.persist(res); err != nil {
		r.failed, r.failErr = true, fmt.Errorf("write manifest: %w", err)
		return nil
	}
	r.applied = true
	s.st.Applied[r.step.ID()] = true
	s.st.Results = append(s.st.Results, StepRecord{StepID: r.step.ID(), Result: r.result})
	return s.checkCtx()
}

// persist merges res into the manifest and writes it when the artifact set,
// a recorded field, or (when something was applied) the header changed.
func (s *session) persist(res StepResult) error {
	changed := false
	for _, a := range res.Artifacts {
		if s.man.Upsert(a) {
			changed = true
		}
	}
	if s.man.Trim(res.Removed) {
		changed = true
	}
	if s.headerDiffers() {
		changed = true
	}
	if !changed {
		return nil
	}
	now := s.e.Read.Clock.Now().UTC()
	if s.man.InstalledAt.IsZero() {
		s.man.InstalledAt = now
	}
	s.man.UpdatedAt = now
	s.fillHeader()
	return SaveManifest(s.e.Write.FS, s.e.Read.Paths, s.man)
}

func (s *session) desiredHeader() (version, topology, jobs, claudeDir string, plat ManifestPlatform) {
	topology = s.man.Topology
	if s.st.Topology.IsSet() {
		topology = string(s.st.Topology.Get())
	}
	pl := s.e.Read.Platform
	return s.e.Version, topology, pl.JobsBackend, s.e.Read.Paths.ClaudeDir, ManifestPlatform{OS: pl.OS, Arch: pl.Arch}
}

func (s *session) headerDiffers() bool {
	v, t, j, c, p := s.desiredHeader()
	m := s.man
	return m.Schema != ManifestSchema || m.BinaryVersion != v || m.Topology != t || m.JobsBackend != j ||
		m.ClaudeConfigDir != c || m.Platform != p
}

func (s *session) fillHeader() {
	v, t, j, c, p := s.desiredHeader()
	s.man.Schema, s.man.BinaryVersion, s.man.Topology, s.man.JobsBackend = ManifestSchema, v, t, j
	s.man.ClaudeConfigDir, s.man.Platform = c, p
}

func cloneManifest(m *Manifest) *Manifest {
	if m == nil {
		return &Manifest{}
	}
	c := *m
	c.Artifacts = slices.Clone(m.Artifacts)
	return &c
}

// ---- Adoption -------------------------------------------------------------

// adoptPhase records the already-correct artifacts of every Adopter step that
// ended cleanly and that the manifest does not know yet (AC-51). It runs after
// Apply, so what a step just wrote is already recorded and is not repeated.
func (s *session) adoptPhase() error {
	for _, r := range s.runs {
		ad, ok := r.step.(Adopter)
		if !ok || r.userSkip || r.failed || r.blocked != "" || r.det.State == StateBlocked {
			continue
		}
		var fresh []Artifact
		for _, a := range ad.Adopt(s.ctx, s.e.Read, s.st) {
			if _, known := s.man.Lookup(a.Kind, a.Path, a.Identity); !known {
				fresh = append(fresh, a)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		if err := s.persist(StepResult{Artifacts: fresh}); err != nil {
			r.failed, r.failErr = true, fmt.Errorf("write manifest: %w", err)
			s.report().StepDone(s.outcomeOf(r))
		}
	}
	return s.checkCtx()
}

// ---- Final doctor (phase 7) -----------------------------------------------

// finalPhase runs the Finalizer step (the doctor) once Apply is done. A fail
// that counts (not annotated "not installed") fails the step, so the run
// exits 1 (AC-62). An interrupted run skips it.
func (s *session) finalPhase() error {
	if err := s.checkCtx(); err != nil {
		return err
	}
	for _, r := range s.runs {
		fin, ok := r.step.(Finalizer)
		if !ok || r.userSkip || r.failed || r.blocked != "" {
			continue
		}
		fr := fin.Final(s.ctx, s.e.Read, s.st, s.finalView())
		if err := s.checkCtx(); err != nil {
			return err // Ctrl-C during the doctor: exit 130, no bogus FAIL lines
		}
		if !fr.Ran {
			continue
		}
		if rep, ok := s.report().(FinalReporter); ok {
			rep.Final(fr)
		}
		r.ran = true
		if n := fr.Report.HardFails(); n > 0 {
			r.failed, r.failErr = true, fmt.Errorf("doctor reported %d failing check(s)", n)
		} else {
			// Not "applied": the doctor changes nothing, and a no-op re-run
			// must still summarise as unchanged.
			r.finalDetail = summaryLine(fr.Report)
		}
		s.report().StepDone(s.outcomeOf(r))
	}
	return s.checkCtx()
}

// finalView computes which doctor fails are exempt as "not installed".
func (s *session) finalView() FinalView {
	ni := map[string]string{}
	for _, r := range s.runs {
		id := r.step.ID()
		if r.userSkip {
			ni[id] = id
		} else if r.blocked != "" && !r.hard {
			if root := s.skippedRoot(r, map[string]bool{}); root != "" {
				ni[id] = root
			}
		}
	}
	for _, id := range s.e.KnownIDs {
		if _, registered := s.idx[id]; !registered {
			ni[id] = id
		}
	}
	return FinalView{NotInstalled: ni}
}

// skippedRoot returns the id of the user-skipped step that r is (softly)
// blocked by, walking Requires; "" when the cause is something else.
func (s *session) skippedRoot(r *stepRun, seen map[string]bool) string {
	for _, req := range r.step.Requires() {
		if seen[req] {
			continue
		}
		seen[req] = true
		d := s.idx[req]
		switch {
		case d.userSkip && d.det.State != StateOK:
			return req
		case d.blocked != "" && !d.hard:
			if root := s.skippedRoot(d, seen); root != "" {
				return root
			}
		}
	}
	return ""
}

// ---- Await ----------------------------------------------------------------

// awaitFlow handles an Apply that paused for a user action (Design 19).
func (s *session) awaitFlow(r *stepRun, first StepResult, chosen []string) error {
	aw := first.Await
	first.Await = nil
	// What the first Apply already did is real: it is recorded when the step
	// ends blocked, skipped or quit, so the manifest matches the disk.
	// It is deliberately NOT recorded when the follow-up Plan/Apply or the
	// success rule fails: a failed step records nothing (AC-9), and the next
	// run's Detect sees what is on disk and converges from there.
	record := func() error {
		r.result = first
		if err := s.persist(first); err != nil {
			r.failed, r.failErr = true, fmt.Errorf("write manifest: %w", err)
		}
		return nil
	}
	if s.auto {
		r.blocked, r.hard = "blocked: awaiting user action", true
		r.remedy = strings.Join(aw.Instructions, "\n")
		s.report().Await(r.step.ID(), aw.Instructions)
		return record()
	}
	for {
		s.report().Await(r.step.ID(), aw.Instructions)
		i, err := s.ui().Select(r.step.Title()+": waiting for you", []string{"re-check", "skip", "quit"}, 0)
		if errors.Is(err, ErrTooManyAttempts) {
			// AC-10: three invalid answers skip the step, like the other prompts.
			r.userSkip, r.skipWhy = true, "skipped by you"
			return record()
		}
		if err := s.askErr(err); err != nil {
			_ = record()
			return err
		}
		switch i {
		case 1:
			r.userSkip, r.skipWhy = true, "skipped by you"
			return record()
		case 2:
			_ = record()
			return &abort{ExitInterrupted, ErrInterrupted}
		}
		s.detect(r)
		state := map[string]State{}
		for _, a := range r.det.Artifacts {
			state[a.ID] = a.State
		}
		pass := true
		for _, id := range aw.Artifacts {
			if state[id] != StateOK {
				pass = false
				s.report().Notes([]Note{{NoteInfo, fmt.Sprintf("%s: %s is still %s", r.step.Title(), id, orMissing(state[id]))}})
			}
		}
		if pass {
			break
		}
	}
	// The awaited artifacts are ok: the remaining non-ok ones take their AC-6
	// defaults and are applied once (a second Await is a failure, no loop).
	follow := Choices{}
	for _, a := range r.det.Artifacts {
		follow[a.ID] = s.defaultChoice(a)
	}
	all := slices.Clone(chosen)
	for _, id := range applyIDs(follow) {
		if !slices.Contains(all, id) {
			all = append(all, id)
		}
	}
	final := first
	if hasApply(follow) {
		plan, err := r.step.Plan(s.ctx, s.e.Read, s.st, follow)
		if err != nil {
			r.failed, r.failErr = true, fmt.Errorf("plan: %w", err)
			return nil
		}
		if err := s.checkCtx(); err != nil {
			_ = record()
			return err
		}
		res2, err := r.step.Apply(s.ctx, s.e.Write, s.st, plan)
		if err != nil {
			r.failed, r.failErr = true, err
			return nil
		}
		if res2.Await != nil {
			r.failed, r.failErr = true, errors.New("step asked to wait again after the follow-up apply")
			return nil
		}
		final = mergeResults(first, res2)
	}
	if err := s.verify(r, all); err != nil {
		r.failed, r.failErr = true, err
		return nil
	}
	r.result = final
	return s.commit(r, final)
}

func orMissing(s State) string {
	if s == "" {
		return "missing"
	}
	return string(s)
}

// ---- outcomes -------------------------------------------------------------

func (s *session) driftNotes(r *stepRun) []Note {
	var out []Note
	for _, a := range r.det.Artifacts {
		if a.State == StateModified && r.choices[a.ID] != ChoiceApply {
			out = append(out, Note{NoteWarn, fmt.Sprintf("drift: %s was modified by you and is kept (%s)", a.ID, a.Detail)})
		}
	}
	return out
}

func (s *session) outcomeOf(r *stepRun) StepOutcome {
	o := StepOutcome{StepID: r.step.ID(), Title: r.step.Title(), Outcome: OutcomeUnchanged, Result: r.result}
	switch {
	case r.failed:
		o.Outcome, o.Err, o.Hard = OutcomeFailed, r.failErr, true
		if r.failErr != nil {
			o.Detail = r.failErr.Error()
		}
		o.Remedy = r.det.Remedy
	case r.blocked != "":
		o.Outcome, o.Detail, o.Hard, o.Remedy = OutcomeBlocked, r.blocked, r.hard, r.remedy
	case r.userSkip:
		o.Outcome, o.Detail = OutcomeSkipped, r.skipWhy
	case r.applied:
		o.Outcome, o.Detail = OutcomeApplied, r.det.Detail
	}
	if r.finalDetail != "" && o.Outcome == OutcomeUnchanged {
		o.Detail = r.finalDetail
	}
	if r.applied {
		o.Notes = append(o.Notes, r.result.Notes...)
	}
	o.Notes = append(o.Notes, s.driftNotes(r)...)
	return o
}

func (s *session) outcomes() []StepOutcome {
	out := make([]StepOutcome, 0, len(s.runs))
	for _, r := range s.runs {
		o := s.outcomeOf(r)
		if r.det.State == "" && r.det.Detail == "" && len(r.det.Artifacts) == 0 && !r.failed {
			o.Outcome = OutcomeNotRun
		}
		out = append(out, o)
	}
	return out
}

func (s *session) exitFailed() bool {
	if s.in.DryRun {
		// Design 15 step 5: --dry-run stops at the plan with exit 0; a step that
		// failed Configure/Plan is shown in the outcomes, not in the exit code.
		return false
	}
	for _, r := range s.runs {
		if r.failed {
			return true
		}
		if r.blocked != "" && r.hard && !s.in.DryRun {
			return true
		}
	}
	return false
}
