// Package main provides the claude-memory service entry point.
// The service exposes MCP tools for semantic memory management,
// with capture from inline tool calls, session extraction, and PR ingest.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"claude-memory/internal/config"
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

// Subcommand stubs return "not implemented" for now.
// Each will be implemented in a separate file by later work items.

func cmdServe(cfg *config.Config) error {
	return fmt.Errorf("serve: not implemented")
}

func cmdHook(cfg *config.Config) error {
	return fmt.Errorf("hook: not implemented")
}

func cmdExtract(cfg *config.Config) error {
	return fmt.Errorf("extract: not implemented")
}

func cmdIngestPR(cfg *config.Config) error {
	return fmt.Errorf("ingest-pr: not implemented")
}

func cmdCleanup(cfg *config.Config) error {
	return fmt.Errorf("cleanup: not implemented")
}

func cmdSeed(cfg *config.Config) error {
	return fmt.Errorf("seed: not implemented")
}

func cmdEvalRetrieval(cfg *config.Config) error {
	return fmt.Errorf("eval-retrieval: not implemented")
}
