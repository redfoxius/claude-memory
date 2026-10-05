package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

// seedStorer is the subset of memory.Service's write path cmdSeed depends
// on, declared here (the consumer) rather than imported from the producer,
// so tests can inject a fake instead of standing up a real Postgres/Ollama
// stack (golang-architecture: ports declared by the consumer).
type seedStorer interface {
	Store(ctx context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error)
}

// seedFact is one entry in a seed YAML file (e.g. seed/facts.yaml).
type seedFact struct {
	Kind    string   `yaml:"kind"`
	Title   string   `yaml:"title"`
	Content string   `yaml:"content"`
	Repo    string   `yaml:"repo"`
	Tags    []string `yaml:"tags"`
}

// seedFile is the top-level shape of a seed YAML file.
type seedFile struct {
	Facts []seedFact `yaml:"facts"`
}

// cmdSeed implements the "seed" subcommand (WI-17): it loads a YAML file of
// known facts and stores each one through memory.Service.Store's normal
// write path (source=inline) -- the same dedup/scrub/embedding logic any
// memory_store MCP call goes through; there is no bypass. With --dry-run,
// it only parses and validates the file, printing what would be stored,
// without building a service or touching Postgres/Ollama at all.
func cmdSeed(cfg *config.Config) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	file := fs.String("file", "seed/facts.yaml", "path to the seed facts YAML file")
	dryRun := fs.Bool("dry-run", false, "parse and print facts without storing them")
	if err := fs.Parse(flag.Args()[1:]); err != nil {
		return fmt.Errorf("parse seed flags: %w", err)
	}

	facts, err := loadSeedFile(*file)
	if err != nil {
		return fmt.Errorf("load seed file: %w", err)
	}

	if *dryRun {
		for _, f := range facts {
			fmt.Printf("dry-run: kind=%s repo=%s title=%q\n", f.Kind, f.Repo, f.Title)
		}
		return nil
	}

	ctx := context.Background()
	svc, cleanup, err := buildService(ctx, cfg, true)
	if err != nil {
		return fmt.Errorf("build service: %w", err)
	}
	defer cleanup()

	return seedAll(ctx, svc, facts, os.Stdout)
}

// loadSeedFile reads and validates a seed YAML file. Validation here is
// deliberately the same shape record.Record.Validate would reject later
// (invalid kind, missing title/content/repo), so --dry-run catches a bad
// file without needing a live service.
func loadSeedFile(path string) ([]seedFact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var sf seedFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	for i, f := range sf.Facts {
		if f.Title == "" {
			return nil, fmt.Errorf("fact %d: title is required", i)
		}
		if f.Content == "" {
			return nil, fmt.Errorf("fact %d (%q): content is required", i, f.Title)
		}
		if !record.Kind(f.Kind).IsValid() {
			return nil, fmt.Errorf("fact %d (%q): invalid kind %q", i, f.Title, f.Kind)
		}
		if f.Repo == "" {
			return nil, fmt.Errorf(`fact %d (%q): repo is required (use "*" for cross-repo)`, i, f.Title)
		}
	}

	return sf.Facts, nil
}

// seedAll stores every fact through svc's normal inline write path,
// printing the resulting decision and record id (or, for a fact that
// landed in the inline judgment range with no id yet assigned, an "ASK"
// line) for each one. Re-running the same file resolves near-verbatim
// repeats to NOOP/UPDATE rather than duplicate ADDs, because this goes
// through the exact same dedup logic (including the AC-16 advisory lock)
// as any other inline memory_store call.
func seedAll(ctx context.Context, svc seedStorer, facts []seedFact, out io.Writer) error {
	for _, f := range facts {
		req := &memory.StoreRequest{
			Kind:    record.Kind(f.Kind),
			Title:   f.Title,
			Content: f.Content,
			Repo:    f.Repo,
			Tags:    f.Tags,
			Source:  record.SourceInline,
		}

		resp, err := svc.Store(ctx, req)
		if err != nil {
			if _, werr := fmt.Fprintf(out, "ERROR %q: %v\n", f.Title, err); werr != nil {
				return fmt.Errorf("write output: %w", werr)
			}
			continue
		}

		if resp.ID == "" {
			if _, werr := fmt.Fprintf(out, "ASK (needs manual judgment, not stored) %q\n", f.Title); werr != nil {
				return fmt.Errorf("write output: %w", werr)
			}
			continue
		}

		if _, werr := fmt.Fprintf(out, "%s %s %q\n", resp.Decision, resp.ID, f.Title); werr != nil {
			return fmt.Errorf("write output: %w", werr)
		}
	}
	return nil
}
