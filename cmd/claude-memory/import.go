package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/redfoxius/claude-memory/internal/importer"
	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/namespace"
	"github.com/redfoxius/claude-memory/internal/record"
	"github.com/redfoxius/claude-memory/internal/scrub"
)

// importUsage is the usage text of the import subcommand.
const importUsage = `usage:
  claude-memory import automem [--projects-dir DIR] [--namespace NS] [--dry-run]
  claude-memory import insights [PATH...] [--namespace NS] [--dry-run]`

// importService is what a real import needs from the memory service,
// declared by the consumer. *memory.Service satisfies it.
type importService interface {
	Store(ctx context.Context, req *memory.StoreRequest) (*memory.StoreResponse, error)
}

var _ importService = (*memory.Service)(nil)

// importDeps are the ports of the import subcommand; main.go builds the real
// ones, tests pass fakes.
type importDeps struct {
	Env importer.Env
	Cwd string
	// Home is the user's home directory (default projects dir is
	// <Home>/.claude/projects).
	Home string
	// NamespaceOf explains the namespace of a directory from namespaces.yaml
	// only (never MEMORY_NAMESPACE): the namespace and how it was chosen.
	NamespaceOf func(dir string) (ns, why string)
	// Scrub is the scrubber used to preview titles in --dry-run.
	Scrub func(string) *scrub.RedactionResult
	// Open builds the service scoped to the target namespace. It is called
	// only for a real run, so --dry-run needs no config, DSN or Ollama.
	Open func(ns string) (svc importService, candidateTTL time.Duration, cleanup func(), err error)
	Out  io.Writer
	Err  io.Writer
}

// reviewReminder is printed after a run that added candidates; the TTL is the
// configured candidate lifetime (MEMORY_CANDIDATE_TTL).
const reviewReminder = "%d candidates await `claude-memory review`; unreviewed candidates are deleted after %d days (MEMORY_CANDIDATE_TTL)\n"

// runImport implements `import automem|insights`.
func runImport(ctx context.Context, d importDeps, args []string) error {
	if len(args) == 0 || (args[0] != "automem" && args[0] != "insights") {
		return usageError(errors.New(importUsage))
	}
	mode := args[0]
	fs := flag.NewFlagSet("import "+mode, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	nsFlag := fs.String("namespace", "", "namespace to import into (default: the cwd's, from namespaces.yaml)")
	dryRun := fs.Bool("dry-run", false, "print what would be imported; needs no database or Ollama")
	projectsDir := fs.String("projects-dir", "", "automem: Claude Code projects directory (default ~/.claude/projects)")
	pos, err := parseFlags(fs, args[1:])
	if err != nil {
		return err
	}
	if mode == "automem" && len(pos) > 0 || mode == "insights" && *projectsDir != "" {
		return usageError(errors.New(importUsage))
	}

	target, err := importTarget(d, *nsFlag)
	if err != nil {
		return err
	}

	var items []importer.Item
	var skips []importer.Skip
	if mode == "automem" {
		dir := *projectsDir
		if dir == "" {
			dir = filepath.Join(d.Home, ".claude", "projects")
		}
		items, skips, err = importer.DiscoverAutoMem(d.Env, dir, target)
	} else {
		items, skips, err = importer.DiscoverInsights(d.Env, d.insightsPaths(pos), target)
	}
	if err != nil {
		return err
	}

	if *dryRun {
		d.printPlan(target, items, skips)
		return nil
	}
	return d.store(ctx, target, items, skips)
}

// importTarget is the namespace imports go to: --namespace (validated), else
// the cwd's namespace from namespaces.yaml, refused when that is only the
// built-in fallback (AC-21).
func importTarget(d importDeps, flagNS string) (string, error) {
	if flagNS != "" {
		if !namespace.ValidName(flagNS) {
			return "", usageError(fmt.Errorf("invalid --namespace %q", flagNS))
		}
		return flagNS, nil
	}
	ns, why := d.NamespaceOf(d.Cwd)
	if why == namespace.WhyFallback {
		return "", usageError(fmt.Errorf("no namespace mapping for %s; pass --namespace", d.Cwd))
	}
	return ns, nil
}

// insightsPaths makes the PATH arguments absolute; the default is the cwd's
// git toplevel, else the cwd.
func (d importDeps) insightsPaths(pos []string) []string {
	if len(pos) == 0 {
		if top, ok := d.Env.Toplevel(d.Cwd); ok {
			return []string{top}
		}
		return []string{d.Cwd}
	}
	out := make([]string, len(pos))
	for i, p := range pos {
		if !filepath.IsAbs(p) {
			p = filepath.Join(d.Cwd, p)
		}
		out[i] = filepath.Clean(p)
	}
	return out
}

// keyPrefix is the import key shortened for display.
func keyPrefix(key string) string {
	if len(key) > 14 {
		return key[:14]
	}
	return key
}

// printPlan is --dry-run: what would be submitted, with scrubbed titles. It
// reports reasons and origins only, never file content.
func (d importDeps) printPlan(target string, items []importer.Item, skips []importer.Skip) {
	fmt.Fprintf(d.Out, "dry run into namespace %q: dedup against existing records is not evaluated\n", target)
	for _, it := range items {
		title := d.Scrub(it.Title)
		content := d.Scrub(it.Content)
		redact := "no"
		if title.Redacted || content.Redacted {
			redact = "yes"
		}
		fmt.Fprintf(d.Out, "would import  %-10s repo=%s files=%d key=%s would redact: %s  %s\n",
			it.Kind, it.Repo, len(it.Files), keyPrefix(it.Key), redact, printable(title.Text))
	}
	for _, s := range skips {
		fmt.Fprintf(d.Out, "skip          %s: %s\n", s.Origin, s.Reason)
	}
	fmt.Fprintf(d.Out, "%d to import, %d skipped\n", len(items), len(skips))
}

// store is the real run: every item goes through Service.Store (scrub,
// embed, dedup, advisory lock, events). A validation error skips the item; a
// DB or embedding error stops the run (a re-run continues where it stopped).
func (d importDeps) store(ctx context.Context, target string, items []importer.Item, skips []importer.Skip) error {
	svc, ttl, cleanup, err := d.Open(target)
	if err != nil {
		return err
	}
	defer cleanup()
	for _, s := range skips {
		fmt.Fprintf(d.Out, "%s: skipped (%s)\n", s.Origin, s.Reason)
	}
	skipped := len(skips)

	added := 0
	summary := func() {
		fmt.Fprintf(d.Out, "added %d, skipped %d\n", added, skipped)
		if added > 0 {
			fmt.Fprintf(d.Out, reviewReminder, added, int(ttl.Hours()/24))
		}
	}
	for _, it := range items {
		resp, err := svc.Store(ctx, &memory.StoreRequest{
			Kind:      it.Kind,
			Title:     it.Title,
			Content:   it.Content,
			Repo:      it.Repo,
			Files:     it.Files,
			Tags:      it.Tags,
			Source:    record.SourceImport,
			ImportKey: it.Key,
		})
		switch {
		case errors.Is(err, memory.ErrInvalidRequest):
			fmt.Fprintf(d.Out, "%s: skipped (%v)\n", it.Origin, err)
			skipped++
		case err != nil:
			fmt.Fprintf(d.Out, "%s: error: %v\n", it.Origin, err)
			summary()
			return fmt.Errorf("import stopped: %w", err)
		case resp.Decision == memory.ActionSkip:
			fmt.Fprintf(d.Out, "%s: skipped (%s)\n", it.Origin, resp.SkipReason)
			skipped++
		default:
			fmt.Fprintf(d.Out, "%s: added %s\n", it.Origin, shortID(resp.ID))
			added++
		}
	}
	summary()
	return nil
}

// printable escapes control characters so a title cannot drive the terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}
