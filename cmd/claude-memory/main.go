// Package main provides the claude-memory service entry point.
// The service exposes MCP tools for semantic memory management,
// with capture from inline tool calls, session extraction, and PR ingest.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"claude-memory/integration"
	"claude-memory/internal/azuredevops"
	"claude-memory/internal/cliexec"
	"claude-memory/internal/config"
	"claude-memory/internal/eventspool"
	"claude-memory/internal/extraction"
	"claude-memory/internal/github"
	"claude-memory/internal/gitlab"
	"claude-memory/internal/gitlog"
	"claude-memory/internal/importer"
	"claude-memory/internal/memory"
	"claude-memory/internal/namespace"
	"claude-memory/internal/ollama"
	"claude-memory/internal/postgres"
	"claude-memory/internal/prcursor"
	"claude-memory/internal/prsource"
	"claude-memory/internal/scrub"
	"claude-memory/internal/setup"
)

func main() {
	if err := run(); err != nil {
		if !errors.Is(err, errQuiet) {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(exitCode(err))
	}
}

// errQuiet marks an exit whose reason the command already printed (doctor's
// report): main exits with the code and prints nothing more.
var errQuiet = errors.New("exit status already reported")

// quietExit exits with code without an "error:" line.
func quietExit(code int) error { return &exitError{code: code, err: errQuiet} }

// exitError carries a process exit code other than 1: 2 for usage errors,
// 3 when doctor cannot start (AC-59).
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// usageError marks err as a usage error (exit 2).
func usageError(err error) error { return &exitError{code: 2, err: err} }

// exitCode is the process exit code for err: 0 for nil, the exitError code
// when present, else 1.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}

// loadConfig loads the env file if it exists, then the environment.
func loadConfig() (*config.Config, error) {
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "claude-memory", "env")
	if err := config.LoadFromFile(configPath); err != nil {
		return nil, fmt.Errorf("load config file: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func run() error {
	// Early dispatch: these subcommands need no database, Ollama or DSN, and
	// must work with no env file, a 0644 one, or no MEMORY_PG_DSN (AC-1).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "namespaces":
			// Local file management.
			return cmdNamespaces(os.Args[2:])
		case "version":
			return cmdVersion(os.Args[2:])
		case "import":
			// Before config load: --dry-run needs no env file, DSN, Postgres or
			// Ollama; a real run loads the config itself, lazily.
			return cmdImport(os.Args[2:])
		case "install":
			// Before config load: no env file or DSN is needed (AC-1).
			return cmdInstall(os.Args[2:])
		case "doctor":
			opts, err := parseDoctorFlags(os.Args[2:], os.Stderr)
			if err != nil {
				return err
			}
			ctx := context.Background()
			deps, err := buildSetupDeps(ctx, "")
			if err != nil {
				return &exitError{code: 3, err: fmt.Errorf("doctor cannot start: %w", err)}
			}
			return cmdDoctor(ctx, opts, deps)
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Parse subcommand.
	flag.Parse()
	args := flag.Args()

	if len(args) == 0 {
		return fmt.Errorf("no subcommand specified; available: serve, hook, extract, ingest-pr, cleanup, stats, ls, show, rm, edit, promote, review, import, seed, eval-retrieval, migrate, namespaces, install, doctor, version")
	}

	subcommand := args[0]

	switch subcommand {
	case "serve":
		return cmdServe(cfg)
	case "hook":
		return cmdHook(cfg)
	case "extract":
		return cmdExtract(cfg)
	case "ingest-pr":
		return cmdIngestPR(cfg)
	case "cleanup":
		return cmdCleanup(cfg)
	case "stats":
		return cmdStats(cfg, args[1:])
	case "seed":
		return cmdSeed(cfg)
	case "eval-retrieval":
		return cmdEvalRetrieval(cfg)
	case "migrate":
		return cmdMigrate(cfg, args[1:])
	case "ls", "show", "rm", "edit", "promote", "review":
		return cmdManage(cfg, subcommand, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", subcommand)
	}
}

// buildPostgresStore constructs the Postgres store adapter alone, for
// subcommands (cleanup) that need direct Postgres access but must not
// construct Ollama or any other adapter they don't use (plan constraint
// `02-plan.md:88-89`: every concrete adapter is built only here, and only
// when actually needed). buildService below composes this with the other
// adapters for subcommands that need the full memory.Service.
func buildPostgresStore(ctx context.Context, cfg *config.Config, migrate bool) (*postgres.Store, func(), error) {
	open := postgres.New
	if !migrate {
		open = postgres.Open
	}
	store, err := open(ctx, cfg.PGDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("create postgres store: %w", err)
	}

	cleanup := func() {
		store.Close()
	}

	return store, cleanup, nil
}

// buildService constructs the memory.Service with all its dependencies.
// This is the composition root: the only place concrete adapters are
// constructed.
func buildService(ctx context.Context, cfg *config.Config, migrate bool) (*memory.Service, func(), error) {
	svc, _, cleanup, err := newService(ctx, cfg, migrate, false)
	return svc, cleanup, err
}

// buildServiceWithEvents is buildService for the long-running and scheduled
// subcommands (serve, extract --run, ingest-pr): the service records its
// lifecycle events into Postgres, and the same store is returned as the sink
// the spool drain writes to. The hook never uses it.
func buildServiceWithEvents(ctx context.Context, cfg *config.Config) (*memory.Service, memory.EventSink, func(), error) {
	return newService(ctx, cfg, true, true)
}

func newService(ctx context.Context, cfg *config.Config, migrate, events bool) (*memory.Service, memory.EventSink, func(), error) {
	store, cleanup, err := buildPostgresStore(ctx, cfg, migrate)
	if err != nil {
		return nil, nil, nil, err
	}

	// Default namespace for this process: the working directory's. The MCP
	// server is started per project by Claude Code, so this scopes serve;
	// hook, extract and ingest-pr re-scope per call from their own paths.
	if cfg.Namespace == "" {
		wd, _ := os.Getwd()
		cfg.Namespace = resolveNamespace(wd)
	}

	// Create HTTP client for Ollama with a reasonable timeout.
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Create Ollama embedder.
	embedder := ollama.New(httpClient, cfg.OllamaURL, cfg.OllamaModel, cfg.EmbedMaxTokens)

	// Create scrub adapter.
	scrubber := scrub.NewAdapter(scrub.New())

	// Create clock (uses time.Now).
	clock := &systemClock{}

	// Build the memory service.
	svc := memory.New(store, embedder, scrubber, clock, cfg)
	if events {
		svc = svc.WithEvents(store)
	}

	return svc, store, cleanup, nil
}

// setupValues are the plain values every setup command (doctor, install)
// computes first: Paths, Env, Platform and the Redactor (Design 20). They
// need only read-only probing, so they are built before any adapter that can
// write.
type setupValues struct {
	Paths    setup.Paths
	Env      setup.Env
	Platform setup.PlatformInfo
	Redactor *setup.Redactor
}

// buildSetupValues computes the setupValues from the process (AC-68). binDir
// is the --bin-dir flag ("" = default). Paths.EphemeralDirs is computed here
// (AC-35) and detectPlatform gets the uid (systemd bus retry). The error is
// errBadHome (or another Paths error) when the paths cannot be computed.
func buildSetupValues(ctx context.Context, binDir string) (setupValues, error) {
	self, err := os.Executable()
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
			self = resolved
		}
	} else {
		self = ""
	}
	cwd, _ := os.Getwd()
	paths, err := buildPaths(os.Getenv, binDir, os.Getuid(), self, cwd)
	if err != nil {
		return setupValues{}, err
	}
	paths.EphemeralDirs = ephemeralDirs(os.TempDir(), os.Getenv, os.UserCacheDir)
	return setupValues{
		Paths:    paths,
		Env:      buildEnv(os.Environ()),
		Platform: detectPlatform(ctx, readOnlyFS{}, execRunner{readOnly: true}, runtime.GOOS, runtime.GOARCH, paths.UID),
		Redactor: setup.NewRedactor(),
	}, nil
}

// buildSetupDeps builds the values and the read-only adapters doctor runs
// with (AC-57, AC-68); install builds on it too (writablePorts swaps in the
// writable FS and Runner). The setup adapters are chosen in this file
// (buildSetupDeps, readOnlyPorts, writablePorts, cmdInstall for the
// prompter); their implementations live in setup_adapters.go. binDir is the
// --bin-dir flag ("" = default).
func buildSetupDeps(ctx context.Context, binDir string) (setupDeps, error) {
	v, err := buildSetupValues(ctx, binDir)
	if err != nil {
		return setupDeps{}, err
	}
	redactor := v.Redactor
	return setupDeps{
		Assets:   integration.FS,
		Paths:    v.Paths,
		Env:      v.Env,
		Platform: v.Platform,
		FS:       readOnlyFS{},
		Runner:   execRunner{readOnly: true},
		Clock:    &systemClock{},
		DB:       postgres.Prober{},
		// No client Timeout: a pull streams for minutes; the checks bound
		// each call with their context.
		Ollama:   ollama.Prober{Client: &http.Client{}},
		Redactor: redactor,
		Stdout:   redactor.Writer(os.Stdout),
		Stderr:   redactor.Writer(os.Stderr),
	}, nil
}

// readOnlyPorts is what Detect, Seed, Configure, Plan and the final doctor
// get (Design 20): the narrowed read interfaces over deps' read-only
// adapters. Jobs is the launchd detector over the read-only FS and Runner.
func readOnlyPorts(d setupDeps) setup.ReadPorts {
	return setup.ReadPorts{
		Jobs:     setup.LaunchdJobs{FS: d.FS, Runner: d.Runner, Paths: d.Paths, Assets: d.Assets},
		FS:       d.FS, // setup.ReadFS view of the read-only adapter
		Runner:   d.Runner,
		DB:       d.DB,
		Ollama:   d.Ollama,
		Clock:    d.Clock,
		Paths:    d.Paths,
		Env:      d.Env,
		Platform: d.Platform,
		Assets:   d.Assets,
	}
}

// writablePorts is what Apply gets (install, uninstall): the writable FS, a
// Runner that allows mutating commands, and the full DB/Ollama probers. With
// dryRun the FS and Runner are the read-only adapters and DB.Migrate and
// Ollama.Pull are refused: a second layer beneath the engine never reaching
// Apply (Design 20). ClaudeCLI and Jobs run over that same FS and Runner, so
// under dryRun they are read-only guarded (ErrDryRun, ErrReadOnly). The
// renderer sets Progress.
func writablePorts(d setupDeps, dryRun bool) setup.WritePorts {
	var fsys setup.FS = newWritableFS()
	var runner setup.Runner = execRunner{}
	db, oll := d.DB, d.Ollama
	if dryRun {
		fsys = readOnlyFS{}
		runner = execRunner{readOnly: true}
		db, oll = readOnlyDB{d.DB}, readOnlyOllama{d.Ollama}
	}
	rp := readOnlyPorts(d)
	rp.FS, rp.Runner = fsys, runner
	jobs := setup.LaunchdJobs{FS: fsys, Runner: runner, Paths: d.Paths, Assets: d.Assets}
	rp.Jobs = jobs
	return setup.WritePorts{
		Jobs:      setup.LaunchdManager{LaunchdJobs: jobs, Write: fsys},
		ReadPorts: rp,
		FS:        fsys,
		Runner:    runner,
		DB:        db,
		Ollama:    oll,
		ClaudeCLI: claudeCLI{runner: runner},
	}
}

// systemClock implements the memory.Clock interface using time.Now().
type systemClock struct{}

func (*systemClock) Now() time.Time {
	return time.Now().UTC()
}

// Subcommand implementations.

func cmdServe(cfg *config.Config) error {
	ctx := context.Background()
	svc, sink, cleanup, err := buildServiceWithEvents(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	// Move the hook's spooled events into Postgres without delaying startup.
	go drainSpool(ctx, sink, 30*time.Second)

	// Staleness checks: the server's working directory is its checkout.
	// Resolved once; HEAD is read per call (no pinned head). Outside a
	// checkout, nothing is ever flagged.
	history := gitlog.Cached(gitlog.Exec{}, gitlog.NewMapCache(), cfg.StaleTimeout)
	wd, _ := os.Getwd()
	rctx, cancel := context.WithTimeout(ctx, time.Second)
	if co, _, ok, err := history.Resolve(rctx, wd); err == nil && ok {
		svc = svc.WithCheckout(co).WithCodeHistory(history, cfg.StaleTimeout)
	}
	cancel()

	return serveCmd(ctx, cfg, svc)
}

func cmdHook(cfg *config.Config) error {
	hookStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.HookTimeout)
	defer cancel()

	// Hot path: no migration check (serve/seed/cleanup/ingest-pr apply the schema).
	svc, cleanup, err := buildService(ctx, cfg, false)
	if err != nil {
		// Silent failure per AC-31: error on Ollama/Postgres down -> exit 0, no output
		slog.DebugContext(ctx, "failed to build service", "error", err)
		return nil
	}
	defer cleanup()

	deps := hookDeps{History: buildCodeHistory(cfg), Events: eventspool.Sink{Dir: spoolDir()}}
	return hookCmd(ctx, cfg, svc, deps, hookStart)
}

// cmdExtract is implemented in extract.go (WI-12).

// scopeManage scopes the service for a management command: the cwd's
// namespace and checkout first (stale lines), then --namespace, which always
// wins. The fallback warning is only for commands that write.
func scopeManage(ctx context.Context, svc *memory.Service, history memory.CodeHistory, cfg *config.Config, cwd, ns string, warnFallback bool) *memory.Service {
	cwdNS := resolveNamespace(cwd)
	if warnFallback && ns == "" {
		warnIfFallback(cwd, cwdNS)
	}
	svc, _ = scopeCheckout(ctx, svc.WithNamespace(cwdNS), history, cfg, cwd)
	if ns != "" {
		svc = svc.WithNamespace(ns)
	}
	return svc
}

// cmdManage is the composition root of the record-management subcommands
// (ls, show, rm, edit, promote, review). The service is built lazily by
// Open, once the flags are valid: events enabled (the writes emit the same
// events as the MCP tools), scoped to the cwd's checkout for stale lines and
// to the cwd's namespace, and finally to --namespace, which always wins.
func cmdManage(cfg *config.Config, subcommand string, args []string) error {
	ctx := context.Background()
	d := mgmtDeps{
		Open: func(ns string, warnFallback bool) (mgmtService, func(), error) {
			svc, _, cleanup, err := buildServiceWithEvents(ctx, cfg)
			if err != nil {
				return nil, nil, fmt.Errorf("build service: %w", err)
			}
			cwd, _ := os.Getwd()
			history := gitlog.Cached(gitlog.Exec{}, gitlog.NewMapCache(), cfg.StaleTimeout)
			return scopeManage(ctx, svc, history, cfg, cwd, ns, warnFallback), cleanup, nil
		},
		Stdin:  os.Stdin,
		Out:    os.Stdout,
		Err:    os.Stderr,
		Editor: shellEditor(os.Getenv),
		Now:    time.Now,
	}
	switch subcommand {
	case "ls":
		return runLs(ctx, d, args)
	case "show":
		return runShow(ctx, d, args)
	case "rm":
		return runRm(ctx, d, args)
	case "edit":
		return runEdit(ctx, d, args)
	case "promote":
		return runPromote(ctx, d, args)
	default:
		return runReview(ctx, d, args)
	}
}

// cmdIngestPR implements the "ingest-pr" subcommand's composition root: it
// parses the subcommand's own flags, builds a *memory.Service via
// buildService only when not doing a dry run (dry-run never calls Ollama
// or Postgres), constructs the PR cursor store and the three PR sources
// (az, gh, glab through one exec adapter), and hands them to runIngestPR
// (ingestpr.go), which never constructs an adapter itself.
func cmdIngestPR(cfg *config.Config) error {
	fs := flag.NewFlagSet("ingest-pr", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be ingested without calling haiku or writing")
	if err := fs.Parse(flag.Args()[1:]); err != nil {
		return fmt.Errorf("parse ingest-pr flags: %w", err)
	}

	ctx := context.Background()

	var svc extraction.StoreWriter
	if !*dryRun {
		s, sink, cleanup, err := buildServiceWithEvents(ctx, cfg)
		if err != nil {
			return fmt.Errorf("build service: %w", err)
		}
		defer cleanup()
		drainSpool(ctx, sink, 30*time.Second)
		svc = s
	}

	scrubber := scrub.NewAdapter(scrub.New())
	scrubText := func(s string) string { out, _ := scrubber.Scrub(s); return out }
	gh := cliexec.Runner{
		Bin:   "gh",
		Env:   []string{"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1"},
		Drop:  []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_DEBUG"},
		Scrub: scrubText,
	}
	glab := cliexec.Runner{
		Bin:   "glab",
		Env:   []string{"NO_COLOR=1"},
		Drop:  []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN", "GITLAB_HOST", "GLAB_DEBUG"},
		Scrub: scrubText,
	}

	nsCfg := loadNamespaces()
	ports := ingestPorts{
		Sources: map[prsource.Provider]prsource.Source{
			prsource.ProviderAzureDevOps: azuredevops.New(nil),
			prsource.ProviderGitHub:      github.New(gh),
			prsource.ProviderGitLab:      gitlab.New(glab),
		},
		Cursors: prcursor.NewStore(filepath.Join(stateDir(), "pr-cursors")),
		Settings: func(repoPath string) (string, namespace.PRIngest, string) {
			ns := resolveNamespace(repoPath)
			if !*dryRun {
				warnIfFallback(repoPath, ns)
			}
			p, problem := nsCfg.PRIngestFor(ns)
			return ns, p, problem
		},
		Scrubber: scrubber,
	}
	if concrete, ok := svc.(*memory.Service); ok {
		ports.Scope = func(ns string) extraction.StoreWriter { return concrete.WithNamespace(ns) }
	}
	return runIngestPR(ctx, cfg, svc, ports, *dryRun)
}

func cmdCleanup(cfg *config.Config) error {
	ctx := context.Background()
	store, cleanup, err := buildPostgresStore(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer cleanup()

	return cleanupCmd(ctx, cfg, store, stateDir(), time.Now(), os.Stdout)
}

// cmdSeed is implemented in seed.go (WI-17).

func cmdEvalRetrieval(cfg *config.Config) error {
	ctx := context.Background()
	args := flag.Args()[1:] // Skip the subcommand itself

	svc, cleanup, err := buildService(ctx, cfg, true)
	if err != nil {
		return fmt.Errorf("build service: %w", err)
	}
	defer cleanup()

	// Fixtures are synthetic: keep them out of every real namespace.
	return evalCmd(ctx, args, svc.WithNamespace("eval"))
}

// newCodeHistory builds the git adapter for one-shot processes that need no
// verdict cache (session extraction's commit baselines). Composition root.
func newCodeHistory() memory.CodeHistory { return gitlog.Exec{} }

// sessionIDRe bounds session ids used in cache file names.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// buildCodeHistory returns the hook's per-session git adapter factory: the
// exec adapter wrapped in a verdict cache — a file per session when the id
// is safe to use as a file name, otherwise an in-process map. This is the
// composition root: the only place gitlog is constructed.
func buildCodeHistory(cfg *config.Config) func(sessionID string) memory.CodeHistory {
	return func(sessionID string) memory.CodeHistory {
		var store gitlog.Store = gitlog.NewMapCache()
		if sessionIDRe.MatchString(sessionID) {
			store = gitlog.NewFileCache(filepath.Join(stateDir(), "stale-cache", sessionID+".json"))
		}
		return gitlog.Cached(gitlog.Exec{}, store, cfg.StaleTimeoutHook)
	}
}

// cmdImport is the composition root of `import`. It runs before the config is
// loaded; the service (and with it the DSN and Ollama) is built by Open only
// for a real run.
func cmdImport(args []string) error {
	ctx := context.Background()
	cwd, _ := os.Getwd()
	history := gitlog.Exec{}
	nsCfg := loadNamespaces() // once: every directory lookup reuses it
	d := importDeps{
		Env: importer.Env{
			FS: readOnlyFS{},
			// namespaces.yaml only: MEMORY_NAMESPACE must not decide where a
			// foreign home dir's knowledge goes.
			NamespaceOf: func(dir string) string { return nsCfg.Resolve(dir) },
			Toplevel: func(dir string) (string, bool) {
				co, _, ok, err := history.Resolve(ctx, dir)
				return co.Dir, err == nil && ok
			},
			Decode: func(name string) (string, bool) { return setup.DecodeProjectName(readOnlyFS{}, "/", name) },
		},
		Cwd:         cwd,
		Home:        os.Getenv("HOME"),
		NamespaceOf: func(dir string) (string, string) { return nsCfg.Explain(dir) },
		Scrub:       scrub.New().Scrub,
		Open: func(ns string) (importService, time.Duration, func(), error) {
			cfg, err := loadConfig()
			if err != nil {
				return nil, 0, nil, err
			}
			svc, _, cleanup, err := buildServiceWithEvents(ctx, cfg)
			if err != nil {
				return nil, 0, nil, fmt.Errorf("build service: %w", err)
			}
			return svc.WithNamespace(ns), cfg.CandidateTTL, cleanup, nil
		},
		Out: os.Stdout,
		Err: os.Stderr,
	}
	return runImport(ctx, d, args)
}
