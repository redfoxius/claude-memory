package setup

import (
	"context"
	"fmt"
)

// Slice-2 engine data model (plan Design 16-19). The engine phases that
// drive these types are WI-S2-1c; this file is data and contracts only.

// Source says where a RunState field's value came from (Design 16).
type Source string

// Value sources, in Seed precedence order (flag -> env file -> Env ->
// default); manifest, generated and prompt are owner-specific.
const (
	SourceFlag      Source = "flag"
	SourceEnv       Source = "env"
	SourceEnvFile   Source = "envfile"
	SourceManifest  Source = "manifest"
	SourceDefault   Source = "default"
	SourceGenerated Source = "generated"
	SourcePrompt    Source = "prompt"
)

// Field is one RunState value with its provenance. The zero Field is unset
// ("not managed in this run", Design 16): Value is the zero value, IsSet is
// false and Source is "". Only the owning step calls Set (Seed, then an
// optional Configure override); everyone else reads. The convention is
// enforced by review and by the engine's change tracking, not by the
// compiler (Go has no field-level write access).
type Field[T any] struct {
	value T
	set   bool
	src   Source
}

// Set stores v with its source and marks the field set (default and
// generated values count as set, N3).
func (f *Field[T]) Set(v T, src Source) {
	f.value, f.set, f.src = v, true, src
}

// Clear returns the field to the unset state.
func (f *Field[T]) Clear() { *f = Field[T]{} }

// Get returns the value (the zero value when unset).
func (f Field[T]) Get() T { return f.value }

// IsSet reports whether an owner set the field.
func (f Field[T]) IsSet() bool { return f.set }

// Source returns where the value came from ("" when unset).
func (f Field[T]) Source() Source { return f.src }

// Topology is the database topology (Design 16).
type Topology string

// Topologies handled by slice 2a.
const (
	TopologyLocal  Topology = "local"
	TopologyRemote Topology = "remote"
)

// DBMode says whether the database already exists or install creates it.
type DBMode string

// Database modes.
const (
	DBModeExisting DBMode = "existing"
	DBModeCreate   DBMode = "create"
)

// DBTarget is the database the install talks to. The password is
// unexported: it lives only here, in the Redactor, the env file and
// bootstrap.sql (Design 16). Construction, parsing and DSN() are WI-S2-4b
// (dsn.go).
type DBTarget struct {
	Host, Port, Name, User string
	SSLMode                string
	Mode                   DBMode
	Source                 Source
	password               secret
}

// OllamaTarget is the embedder endpoint (Design 16).
type OllamaTarget struct {
	URL, Model     string
	EmbedMaxTokens string // only from the env file or Env; "" = binary default
	// SkipPull is set by Configure when the user declined to pull a missing
	// model: Detect then reports the model ok with a warning, so the engine
	// plans no pull. It is never written to the env file or the manifest.
	SkipPull bool
}

// NSRule is one namespaces mapping chosen in Configure (AC-47).
type NSRule struct {
	Namespace string
	Globs     []string
}

// Inputs is the parsed command line plus the environment snapshot, filled
// by cmd before Run and read by every step (Design 16).
type Inputs struct {
	Yes         bool
	DryRun      bool
	Reconfigure bool
	Upgrade     bool
	Skip        []string // step ids (AC-53)
	Topology    string
	PGDSN       string
	// PGSSLMode is --pg-sslmode (prefer|require|disable). It overrides the
	// sslmode of a --pg-dsn, an env-file DSN or a shell DSN (the value then
	// counts as flag-sourced, so the env file is rewritten, not kept as
	// hand-edited); it is the default of the TLS prompt and the sslmode of
	// the local create path.
	PGSSLMode   string
	NoDoctor    bool   // --no-doctor: the final doctor step is skipped
	PGPassword  string // read from stdin before Detect (AC-31)
	OllamaURL   string
	OllamaModel string
	PRRepos     string
	// ClaudeMD is --claude-md PATH (AC-4, AC-42): the CLAUDE.md file that
	// receives the managed block; "" = the default <ClaudeDir>/CLAUDE.md. It
	// is resolved against Paths.Cwd by the claude-md step.
	ClaudeMD       string
	Namespaces     []string // --namespace NAME=GLOB, repeatable (AC-47)
	JobsBackend    string
	BinDir         string
	BinDirExplicit bool
	Env            Env
}

// String prints the inputs without the stdin password and without Env values
// (a shell DSN may hold a password): %v of a RunState must be safe to log.
func (in Inputs) String() string {
	type plain Inputs // no methods, so no recursion
	if in.PGPassword != "" {
		in.PGPassword = Mask
	}
	n := len(in.Env)
	in.Env = nil
	return fmt.Sprintf("%+v (env: %d vars)", plain(in), n)
}

// GoString is String, so %#v is as safe as %v.
func (in Inputs) GoString() string { return in.String() }

// Prior is what a previous install left behind, loaded by preflight.
type Prior struct {
	Manifest *Manifest // nil when there is none
	EnvDoc   []byte    // raw env-file bytes; nil when absent (parsed by the envfile step)
}

// StepRecord is the engine's record of one step's outcome (RunState.Results).
type StepRecord struct {
	StepID string
	Result StepResult
	Err    error
}

// RunState is the typed state shared by the steps of one run: no map, one
// writer per field (Design 16). Inputs and Prior are filled by cmd and
// preflight; each step-owned Field is written only by its owner (Seed, then
// an optional Configure override); Applied and Results by the engine.
type RunState struct {
	Inputs Inputs
	Prior  Prior

	// Written by engine preflight.
	Downgrade        bool
	ConfigDirChanged bool

	// Step-owned (owner in the comment).
	BinPath        Field[string]       // binary
	Topology       Field[Topology]     // topology
	DB             Field[DBTarget]     // database
	Ollama         Field[OllamaTarget] // ollama
	NSRules        Field[[]NSRule]     // namespaces
	PRRepos        Field[string]       // jobs [2b]
	ClaudeMDTarget Field[string]       // claude-md
	JobPATH        Field[string]       // jobs [2b]

	// Written by the engine.
	// Auto is set when nothing is asked: --yes/--upgrade, or a --dry-run
	// without a TTY. A step whose default must differ for an unattended run
	// (claude-md, AC-42) reads it in Detect.
	Auto    bool
	Applied map[string]bool // step id -> applied in this run
	Results []StepRecord
}

// NewRunState returns a RunState for in with its engine maps initialised.
func NewRunState(in Inputs) *RunState {
	return &RunState{Inputs: in, Applied: map[string]bool{}}
}

// Note is a message for the renderer: warn or info (Design 17).
type Note struct {
	Level NoteLevel
	Text  string
}

// NoteLevel is the severity of a Note.
type NoteLevel string

// Note levels.
const (
	NoteWarn NoteLevel = "warn"
	NoteInfo NoteLevel = "info"
)

// ArtifactState is the Detect result for one artifact of a step (Design 18),
// e.g. ID "hooks.settings/UserPromptSubmit".
type ArtifactState struct {
	ID     string
	State  State
	Detail string
}

// ArtifactKey identifies a manifest artifact (for StepResult.Removed).
type ArtifactKey struct {
	Kind     ArtifactKind
	Path     string
	Identity string
}

// Diff is a unified diff of one artifact; the renderer redacts it.
type Diff struct {
	Artifact string // artifact id
	Path     string
	Unified  string
}

// Detection is a step's Detect result (Design 17). State is the worst of
// Artifacts and is for the table only.
type Detection struct {
	State  State
	Detail string
	// BlockedBy names the step whose pending Apply blocks this one.
	BlockedBy string
	// Remedy is set when blocked by an external condition (BlockedBy empty);
	// it keys the Configure-phase blocked-step prompt (Design 15.3).
	Remedy    string
	Artifacts []ArtifactState
	Notes     []Note
	// SkipReason, when set, makes the engine treat the step as skipped (all
	// choices skip, outcome "skipped") with this text as the reason. It is a
	// documented, non-failing refusal, e.g. claude-md under --yes for an
	// explicit path inside a git repository (AC-42). Never set it for a
	// problem the user must fix.
	SkipReason string
}

// Choices maps an artifact ID to the user's choice; apply on a modified
// artifact is a confirmed overwrite (Design 17).
type Choices map[string]Choice

// Action is display data for the combined plan; Apply executes its own Plan
// (Design 17). Verb: write|chmod|remove|run|migrate|pull|register|load.
type Action struct {
	Artifact, Verb, Path, Desc string
}

// Plan is what a step would do under the given Choices.
type Plan struct {
	Actions []Action
	Diffs   []Diff
	Notes   []Note
	// Token is opaque, step-private data carried from Plan to Apply: the
	// hooks.settings step puts the sha256 of the settings file it planned
	// against here, so Apply can abort when the file changed while the user
	// was confirming (AC-39); hooks.scripts does the same per script. The
	// engine keeps the Token of the plan the user confirmed when it re-plans
	// a step after a prerequisite was applied (Rule B). Limit: a step planned
	// only after its prerequisite (afterDep) has no confirmed plan, so its
	// Token covers just the span from that re-plan to Apply.
	Token string
}

// Await asks the user to do something outside install, then re-check
// (Design 19). Artifacts are the IDs that must re-detect ok.
type Await struct {
	Instructions []string
	Artifacts    []string
}

// StepResult is what Apply reports (Design 17).
type StepResult struct {
	Artifacts []Artifact    // recorded or updated in the manifest
	Removed   []ArtifactKey // dropped from the manifest
	Notes     []Note
	Diffs     []Diff // unified diffs actually applied
	Await     *Await
}

// ProgressSink receives long-running progress (the Ollama pull); the
// renderer implements it (Design 17, L1).
type ProgressSink func(step, label string, done, total int64)

// Step is one unit of install work (Design 17, replacing spec §10 Step).
// Detect, Plan (and Seed/Configure) get only ReadPorts; Apply gets WritePorts.
type Step interface {
	ID() string
	Title() string
	Requires() []string
	Detect(ctx context.Context, rc ReadPorts, st *RunState) Detection
	Plan(ctx context.Context, rc ReadPorts, st *RunState, ch Choices) (Plan, error)
	Apply(ctx context.Context, wc WritePorts, st *RunState, p Plan) (StepResult, error)
}

// Seeder is implemented by steps that own RunState fields: Seed sets them
// from Inputs, Env and Prior with read-only ports, once per run. An error
// means an invalid flag value (exit 2, before any Detect).
type Seeder interface {
	Seed(ctx context.Context, rc ReadPorts, st *RunState) ([]Note, error)
}

// Configurer is implemented by steps that ask questions; it may only
// override its own RunState fields (Design 15.3, 16).
type Configurer interface {
	Configure(ctx context.Context, rc ReadPorts, ui Prompter, st *RunState) error
}

// String renders a Field for debugging without leaking a DBTarget password
// (DBTarget has no exported password, and %v of Field prints the struct).
func (f Field[T]) String() string {
	if !f.set {
		return "unset"
	}
	return fmt.Sprintf("set(%s)", f.src)
}

// GoString keeps %#v of a Field from reflecting into the unexported value
// (a DBTarget's password): it prints the same as String.
func (f Field[T]) GoString() string { return f.String() }

// secret is a password held in memory. Every fmt verb prints Mask, so a
// stray %v, %+v or %#v of the value (or of a pointer to it) cannot leak it.
// Use string(s) where the password is genuinely needed.
type secret string

// String implements fmt.Stringer.
func (secret) String() string { return Mask }

// GoString implements fmt.GoStringer.
func (secret) GoString() string { return `"` + Mask + `"` }

// Format implements fmt.Formatter for every verb.
func (secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(Mask)) }
