package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"claude-memory/internal/evalset"
	"claude-memory/internal/memory"
	"claude-memory/internal/record"
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

// fixtureStore is the slice of memory.Service that resetEvalFixtures needs.
// Both calls are confined to the service's own namespace.
type fixtureStore interface {
	Namespace() string
	ListRecords(ctx context.Context, filters memory.ListFilters) ([]*record.Record, error)
	DeleteRecord(ctx context.Context, id string) error
}

// resetEvalFixtures deletes every record of the default `eval` namespace so
// each run starts from the same state (leftovers from earlier runs would
// duplicate seeds and fill the top-3). It refuses to touch any other
// namespace and returns how many records were removed. Records referenced by
// another record's superseded_by are retried after their referrers go.
func resetEvalFixtures(ctx context.Context, svc fixtureStore) (int, error) {
	if svc.Namespace() != evalNamespace {
		return 0, fmt.Errorf("refusing to reset namespace %q: only %q may be reset", svc.Namespace(), evalNamespace)
	}
	recs, err := svc.ListRecords(ctx, memory.ListFilters{})
	if err != nil {
		return 0, err
	}
	removed := 0
	for pass := 0; pass < 3 && len(recs) > 0; pass++ {
		var left []*record.Record
		for _, r := range recs {
			if r.Namespace != evalNamespace {
				return removed, fmt.Errorf("record %s belongs to namespace %q; aborting reset", r.ID, r.Namespace)
			}
			if err := svc.DeleteRecord(ctx, r.ID); err != nil {
				var ref *memory.ErrReferenced
				if errors.As(err, &ref) {
					left = append(left, r)
					continue
				}
				return removed, fmt.Errorf("delete %s: %w", r.ID, err)
			}
			removed++
		}
		recs = left
	}
	if len(recs) > 0 {
		return removed, fmt.Errorf("%d eval records could not be deleted (still referenced)", len(recs))
	}
	return removed, nil
}
