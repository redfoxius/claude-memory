package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"claude-memory/internal/evalset"
	"claude-memory/internal/memory"
)

// evalCmd runs the retrieval evaluation harness against an already-built
// memory.Service (constructed once by main.go's composition root).
// Usage: claude-memory eval-retrieval [--fixtures-dir=<path>] [--output=<file>]
// Defaults:
// - fixtures-dir: testdata/evalset/ (relative to CWD)
// - output: docs/specs/memory-mvp/eval-results.md (relative to CWD)
func evalCmd(ctx context.Context, args []string, svc *memory.Service) error {
	fs := flag.NewFlagSet("eval-retrieval", flag.ExitOnError)
	fixturesDir := fs.String("fixtures-dir", "testdata/evalset", "Directory containing seed_records.json and query_cases.json")
	outputFile := fs.String("output", "docs/specs/memory-mvp/eval-results.md", "Output file for evaluation results")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse eval flags: %w", err)
	}

	slog.InfoContext(ctx, "eval-retrieval started",
		"fixtures_dir", *fixturesDir,
		"output_file", *outputFile)

	// Open output file.
	out, err := os.Create(*outputFile)
	if err != nil {
		return fmt.Errorf("create output file %s: %w", *outputFile, err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil {
			slog.WarnContext(ctx, "failed to close eval output file", "path", *outputFile, "error", cerr)
		}
	}()

	// Run the evaluation.
	if err := evalset.Run(ctx, svc, *fixturesDir, out); err != nil {
		return fmt.Errorf("run evaluation: %w", err)
	}

	slog.InfoContext(ctx, "eval-retrieval completed", "output_file", *outputFile)
	return nil
}
