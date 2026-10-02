package setup

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"claude-memory/internal/namespace"
)

// Ports (spec §10). Every interface the package depends on is declared
// here, by the consumer. Concrete adapters live in cmd/claude-memory (os,
// exec, terminal), internal/postgres and internal/ollama, and are
// constructed only in cmd/claude-memory/main.go.

// ErrDryRun is returned by every write method of a read-only or dry-run FS
// adapter (doctor, install --dry-run): those modes are write-free by
// construction (AC-13, AC-57).
var ErrDryRun = errors.New("write refused: read-only or dry-run mode")

// ErrReadOnly is returned by a read-only Runner for a Cmd whose Mutating flag
// is set (doctor, install --dry-run, every Detect).
var ErrReadOnly = errors.New("mutating command refused: read-only runner")

// Prompter is the line-oriented user interaction port (AC-10, AC-11) [S2].
// The TTY adapter reads passwords without echo (golang.org/x/term) and
// registers every secret with the Redactor before returning it (AC-30).
type Prompter interface {
	// Select shows numbered opts with def preselected and returns the index
	// chosen (Enter accepts def).
	Select(q string, opts []string, def int) (int, error)
	// Confirm asks a yes/no question; def is the answer Enter gives.
	Confirm(q string, def bool) (bool, error)
	// Text asks for a line of text with a default; validate (may be nil)
	// rejects input, which is re-asked up to 3 times.
	Text(q, def string, validate func(string) error) (string, error)
	// Secret reads a line without echo and registers it with the Redactor
	// before returning it.
	Secret(q string) (string, error)
	// Interactive reports whether stdin and stdout are both TTYs.
	Interactive() bool
}

// ReadFS is the read half of the filesystem port. Every path is absolute.
// Detect, Seed, Configure and Plan receive only this half (Design 20), so a
// write from them does not compile.
type ReadFS interface {
	ReadFile(p string) ([]byte, error)
	Stat(p string) (fs.FileInfo, error)
	Lstat(p string) (fs.FileInfo, error)
	ReadDir(p string) ([]fs.DirEntry, error)
	EvalSymlinks(p string) (string, error)
	// Writable reports whether p could be written, via access(2); it never
	// writes anything (doctor dirs.state).
	Writable(p string) bool
}

// FS is the full filesystem port: ReadFS plus the write half (slice 2). The
// read-only (doctor) and dry-run adapters return ErrDryRun from every write
// method.
type FS interface {
	ReadFS

	// WriteFileAtomic writes b to p via a temp file in the same directory,
	// fsync and rename, with the given mode (AC-9).
	WriteFileAtomic(p string, b []byte, mode fs.FileMode) error
	MkdirAll(p string, mode fs.FileMode) error
	Remove(p string) error
	Chmod(p string, mode fs.FileMode) error
	// Lock takes a process-scoped flock on p (never a PID file); unlock
	// releases it, and so does process exit (AC-9).
	Lock(p string) (unlock func() error, err error)
}

// Cmd is one external command: argv only, never a shell (spec "No shell").
type Cmd struct {
	Argv []string // Argv[0] is the program (a name looked up on PATH, or a path)
	Dir  string   // working directory; "" = inherit
	// Env holds KEY=VALUE entries added to the inherited environment (e.g.
	// the AC-16 XDG_RUNTIME_DIR / DBUS_SESSION_BUS_ADDRESS for systemctl --user).
	Env   []string
	Stdin []byte // nil = no stdin
	// Mutating marks a command that changes state (launchctl bootstrap,
	// systemctl enable, claude mcp add, ...). It is set by the step code; a
	// read-only Runner refuses it with ErrReadOnly.
	Mutating bool
}

// Result is the outcome of a command that ran.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Runner executes external commands (argv only, never through a shell).
// Run returns a nil error with a non-zero Result.ExitCode when the command
// ran and failed; a non-nil error means it could not run at all (not found,
// context cancelled or timed out, ErrReadOnly). The read-only Runner (doctor,
// Detect, dry-run) rejects every Cmd with Mutating set.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
	// LookPath resolves name on PATH like exec.LookPath.
	LookPath(name string) (string, error)
}

// Clock supplies the current time (backup suffixes, manifest timestamps,
// durations).
type Clock interface{ Now() time.Time }

// DBErrorClass classifies a failed database probe (AC-20).
type DBErrorClass string

// Database probe error classes, derived from *pgconn.PgError SQLSTATE and
// net/deadline errors by the adapter.
const (
	DBErrUnreachable DBErrorClass = "unreachable" // net.Error, context.DeadlineExceeded
	DBErrAuth        DBErrorClass = "auth"        // 28P01, 28000 (also a missing role)
	DBErrNoDB        DBErrorClass = "nodb"        // 3D000
	DBErrHBA         DBErrorClass = "hba"         // 28000 with "no pg_hba.conf entry"
	DBErrDSN         DBErrorClass = "dsn"         // the DSN does not parse
	DBErrOther       DBErrorClass = "other"
)

// MigrationStatus is the schema state of one embedded migration, detected
// by introspection (plan Design 6).
type MigrationStatus struct {
	ID      string   // "0001", "0002", ...
	Missing []string // objects of this migration not found (e.g. "records.namespace"); empty = applied
}

// Applied reports whether every object of the migration is present.
func (m MigrationStatus) Applied() bool { return len(m.Missing) == 0 }

// DBStatus is what a read-only database probe found (doctor pg.*, AC-58).
type DBStatus struct {
	Connected     bool
	ErrorClass    DBErrorClass // "" when Connected
	RTT           time.Duration
	TLS           bool   // the connection uses TLS (pg_stat_ssl)
	ServerVersion string // e.g. "16.4"
	VectorVersion string // installed pgvector version; "" = not installed
	// Migrations lists every embedded migration in order with its missing
	// objects; nil when the probe did not get that far.
	Migrations []MigrationStatus
	// Unknown lists objects in our tables that no embedded migration
	// creates (a newer binary's schema; reported as info).
	Unknown []string
}

// DBProbe is the read half of the database port (Design 20): Detect, Seed,
// Configure and Plan receive only this.
type DBProbe interface {
	// Probe connects read-only (postgres.Open: no migrations) and reports
	// ping RTT, TLS, the vector extension and schema objects. On failure the
	// returned status carries the ErrorClass, and the error its cause.
	Probe(ctx context.Context, dsn string) (DBStatus, error)
	// LocalServerEvidence reports whether a local Postgres server exists:
	// TCP 127.0.0.1:5432 plus a local socket or postgres process (AC-19),
	// with a one-line description of the evidence [S2].
	LocalServerEvidence(ctx context.Context) (bool, string)
}

// DBProber probes and migrates the database. Adapter: internal/postgres.
type DBProber interface {
	DBProbe
	// Migrate applies the embedded migrations (postgres.New + Close); used
	// by the migrate step [S2].
	Migrate(ctx context.Context, dsn string) error
}

// OllamaProbe is the read half of the Ollama port (Design 20).
type OllamaProbe interface {
	// Version calls GET /api/version.
	Version(ctx context.Context, url string) (string, error)
	// HasModel reports whether model is listed by GET /api/tags.
	HasModel(ctx context.Context, url, model string) (bool, error)
	// EmbedDims embeds one short text and returns the vector length and
	// the request latency (the schema needs 1024).
	EmbedDims(ctx context.Context, url, model string) (dims int, latency time.Duration, err error)
}

// OllamaProber talks to the Ollama HTTP API. Adapter: internal/ollama.
// Pull is used from slice 2.
type OllamaProber interface {
	OllamaProbe
	// Pull runs POST /api/pull, reporting streamed progress [S2].
	Pull(ctx context.Context, url, model string, progress func(done, total int64)) error
}

// JobSpec describes one scheduled job (AC-43).
type JobSpec struct {
	Name    string   // "cleanup" | "ingest-pr"
	Label   string   // launchd label (io.github.claude-memory.<name>) or systemd unit base name
	Program string   // absolute path of the installed binary
	Args    []string // subcommand arguments, e.g. ["cleanup"]
	Hour    int      // daily start time, local
	Minute  int
	PATH    string // explicit PATH for the job
	LogPath string // <StateDir>/<name>.log
}

// JobDetector is the read half of the job port (Design 20): render and
// detect, never change anything.
type JobDetector interface {
	// Render returns the unit/plist files for j, keyed by absolute path.
	Render(j JobSpec) (map[string][]byte, error)
	// Detect reports the job's state read-only (file read plus one
	// launchctl print / systemctl --user show) with a one-line detail.
	Detect(ctx context.Context, j JobSpec) (State, string, error)
}

// JobManager manages scheduled jobs for one backend: launchd (Detect in
// slice 1, the rest in slice 2) or systemd (slice 2).
type JobManager interface {
	JobDetector
	Install(ctx context.Context, j JobSpec) error
	Remove(ctx context.Context, j JobSpec) error
}

// ClaudeCLI is the claude CLI, writers only [S2]: `claude mcp add|remove
// --scope user`. There is deliberately no MCPGet/MCPList: those spawn the
// registered server, so registration is detected by reading
// Paths.ClaudeJSON instead (AC-40, AC-67).
type ClaudeCLI interface {
	MCPAdd(ctx context.Context, name string, argv []string) error
	MCPRemove(ctx context.Context, name string) error
}

// Namespaces is the namespaces.yaml store [S2]. Adapter: internal/namespace.
type Namespaces interface {
	Load(path string) (*namespace.Config, error)
	Init(path, def string, rules []namespace.Rule) error
	Add(path, name string, globs ...string) error
}

// Check is one doctor probe (AC-58). Run returns the status, a one-line
// detail and a one-line remedy ("" when none). Checks never print; the
// renderer prints through the Redactor. A check whose Requires include a
// check that failed is reported skip ("because <id> failed") without
// running (AC-58).
type Check struct {
	ID, Title string
	Requires  []string
	Run       func(ctx context.Context) (Status, string /*detail*/, string /*remedy*/)
}

// ReadPorts is everything Detect, Seed, Configure and Plan may touch
// (Design 20). Its port fields are the narrowed read interfaces, so a file
// write, a migration, a model pull or a job install from those phases does
// not compile. The one runtime guard left is Runner (Cmd.Mutating ->
// ErrReadOnly). Jobs is nil when no step reads it (2a).
type ReadPorts struct {
	FS       ReadFS
	Runner   Runner // read-only adapter
	DB       DBProbe
	Ollama   OllamaProbe
	Jobs     JobDetector
	Clock    Clock
	Paths    Paths
	Env      Env
	Platform PlatformInfo
	Assets   fs.FS
}

// WritePorts is what Apply (and uninstall) receives: the read ports plus the
// writable counterparts (Design 20). Under --dry-run its FS and Runner are
// the read-only adapters. ClaudeCLI and Jobs are nil in 2a.
type WritePorts struct {
	ReadPorts // embedded read view: its FS/Runner/DB/Ollama/Jobs are the same adapters

	// Writable counterparts; they shadow the embedded read fields.
	FS        FS
	Runner    Runner // mutating allowed (read-only adapter under --dry-run)
	DB        DBProber
	Ollama    OllamaProber
	Jobs      JobManager
	ClaudeCLI ClaudeCLI
	Progress  ProgressSink
}
