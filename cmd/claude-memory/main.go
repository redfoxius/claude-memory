// Package main provides the claude-memory service entry point.
// The service exposes MCP tools for semantic memory management,
// with capture from inline tool calls, session extraction, and PR ingest.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"claude-memory/internal/azuredevops"
	"claude-memory/internal/config"
	"claude-memory/internal/extraction"
	"claude-memory/internal/memory"
	"claude-memory/internal/ollama"
	"claude-memory/internal/postgres"
	"claude-memory/internal/prcursor"
	"claude-memory/internal/scrub"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
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
		return fmt.Errorf("no subcommand specified; available: serve, hook, extract, ingest-pr, cleanup, seed, eval-retrieval")
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

	return serveCmd(ctx, cfg, svc)
}

func cmdHook(cfg *config.Config) error {
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

	return hookCmd(ctx, cfg, svc)
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

	return runIngestPR(ctx, cfg, svc, cursorStore, client, *dryRun)
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

	return evalCmd(ctx, args, svc)
}
