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
	password               string
}

// OllamaTarget is the embedder endpoint (Design 16).
type OllamaTarget struct {
	URL, Model     string
	EmbedMaxTokens string // only from the env file or Env; "" = binary default
}

// NSRule is one namespaces mapping chosen in Configure (AC-47).
type NSRule struct {
	Namespace string
	Globs     []string
}

// Inputs is the parsed command line plus the environment snapshot, filled
// by cmd before Run and read by every step (Design 16).
type Inputs struct {
	Yes            bool
	DryRun         bool
	Reconfigure    bool
	Upgrade        bool
	Skip           []string // step ids (AC-53)
	Topology       string
	PGDSN          string
	PGPassword     string // read from stdin before Detect (AC-31)
	OllamaURL      string
	OllamaModel    string
	PRRepos        string
	JobsBackend    string
	BinDir         string
	BinDirExplicit bool
	Env            Env
}

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
