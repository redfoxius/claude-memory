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

	"claude-memory/internal/azuredevops"
	"claude-memory/internal/config"
	"claude-memory/internal/extraction"
	"claude-memory/internal/gitlog"
	"claude-memory/internal/memory"
	"claude-memory/internal/ollama"
	"claude-memory/internal/postgres"
	"claude-memory/internal/prcursor"
	"claude-memory/internal/scrub"
	"claude-memory/internal/setup"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitCode(err))
	}
}

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

	// Load config from file first if it exists, then from environment.
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "claude-memory", "env")
	if err := config.LoadFromFile(configPath); err != nil {
		return fmt.Errorf("load config file: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Parse subcommand.
	flag.Parse()
	args := flag.Args()

	if len(args) == 0 {
		return fmt.Errorf("no subcommand specified; available: serve, hook, extract, ingest-pr, cleanup, seed, eval-retrieval, migrate, namespaces, doctor, version")
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
	case "seed":
		return cmdSeed(cfg)
	case "eval-retrieval":
		return cmdEvalRetrieval(cfg)
	case "migrate":
		return cmdMigrate(cfg, args[1:])
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
	store, cleanup, err := buildPostgresStore(ctx, cfg, migrate)
	if err != nil {
		return nil, nil, err
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

	return svc, cleanup, nil
}

// buildSetupDeps builds the values (Paths, Env, PlatformInfo) and the
// read-only adapters doctor runs with (AC-57, AC-68). This is the only place
// internal/setup's adapters are constructed. binDir is the --bin-dir flag
// ("" = default). The error is errBadHome (or another Paths error) when the
// paths cannot be computed.
func buildSetupDeps(ctx context.Context, binDir string) (setupDeps, error) {
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
		return setupDeps{}, err
	}

	fsys := readOnlyFS{}
	runner := execRunner{readOnly: true}
	redactor := setup.NewRedactor()
	return setupDeps{
		Paths:    paths,
		Env:      buildEnv(os.Environ()),
		Platform: detectPlatform(ctx, fsys, runner, runtime.GOOS, runtime.GOARCH),
		FS:       fsys,
		Runner:   runner,
		Clock:    &systemClock{},
		Redactor: redactor,
		Stdout:   redactor.Writer(os.Stdout),
		Stderr:   redactor.Writer(os.Stderr),
	}, nil
}

// systemClock implements the memory.Clock interface using time.Now().
type systemClock struct{}

func (*systemClock) Now() time.Time {
	return time.Now().UTC()
}

// Subcommand implementations.

func cmdServe(cfg *config.Config) error {
	ctx := context.Background()
	svc, cleanup, err := buildService(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer cleanup()

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

	deps := hookDeps{History: buildCodeHistory(cfg)}
	return hookCmd(ctx, cfg, svc, deps, hookStart)
}

// cmdExtract is implemented in extract.go (WI-12).

// cmdIngestPR implements the "ingest-pr" subcommand's composition root: it
// parses the subcommand's own flags, builds a *memory.Service via
// buildService only when not doing a dry run (dry-run never calls Ollama
// or Postgres), constructs the PR cursor store and Azure DevOps client,
// and hands all three ports to runIngestPR (ingestpr.go), which never
// constructs an adapter itself.
func cmdIngestPR(cfg *config.Config) error {
	fs := flag.NewFlagSet("ingest-pr", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be ingested without calling haiku or writing")
	if err := fs.Parse(flag.Args()[1:]); err != nil {
		return fmt.Errorf("parse ingest-pr flags: %w", err)
	}

	ctx := context.Background()

	var svc extraction.StoreWriter
	if !*dryRun {
		s, cleanup, err := buildService(ctx, cfg, true)
		if err != nil {
			return fmt.Errorf("build service: %w", err)
		}
		defer cleanup()
		svc = s
	}

	cursorStore := prcursor.NewStore(filepath.Join(stateDir(), "pr-cursors"))
	client := azuredevops.New(nil)

	var scope scopeFunc
	if concrete, ok := svc.(*memory.Service); ok {
		scope = func(repoPath string) extraction.StoreWriter {
			ns := resolveNamespace(repoPath)
			warnIfFallback(repoPath, ns)
			return concrete.WithNamespace(ns)
		}
	}
	return runIngestPR(ctx, cfg, svc, cursorStore, client, scope, *dryRun)
}

func cmdCleanup(cfg *config.Config) error {
	ctx := context.Background()
	store, cleanup, err := buildPostgresStore(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer cleanup()

	return cleanupCmd(ctx, cfg, store)
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
