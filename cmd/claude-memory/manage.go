package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/namespace"
	"github.com/redfoxius/claude-memory/internal/record"
)

// manageUsage is the usage text of the record-management subcommands.
const manageUsage = `usage:
  claude-memory ls [--status S] [--kind K] [--repo R] [--limit N] [--namespace NS]
  claude-memory show ID [--namespace NS]
  claude-memory rm ID [--reason TEXT] [--hard [--yes]] [--namespace NS]
  claude-memory edit ID [--namespace NS]
  claude-memory promote ID [--namespace NS]
  claude-memory review [--namespace NS]`

// defaultRmReason is the deprecation reason of `rm` without --reason.
const defaultRmReason = "removed via CLI"

// mgmtService is what the management subcommands need from the memory
// service, declared by the consumer. *memory.Service satisfies it.
type mgmtService interface {
	ListRecords(ctx context.Context, filters memory.ListFilters) ([]*record.Record, error)
	GetRecord(ctx context.Context, id string) (*record.Record, error)
	UpdateRecord(ctx context.Context, req *memory.UpdateRequest) (*record.Record, error)
	DeprecateRecord(ctx context.Context, req *memory.DeprecateRequest) (*record.Record, error)
	DeleteRecord(ctx context.Context, id string) error
	Similar(ctx context.Context, id string, limit int) ([]*memory.Candidate, error)
	StaleHint(ctx context.Context, rec *record.Record) *memory.StaleHint
	Checkout() (memory.Checkout, bool)
	Namespace() string
}

var _ mgmtService = (*memory.Service)(nil)

// mgmtDeps are the ports of the management subcommands; main.go builds the
// real ones, tests pass fakes and a scripted Stdin.
type mgmtDeps struct {
	// Open builds the service scoped to the target namespace: ns is the
	// --namespace value ("" = the cwd's namespace, warning about the built-in
	// fallback only when warnFallback is set). It is called after the flags
	// are validated, so a usage error never touches the database.
	Open   func(ns string, warnFallback bool) (mgmtService, func(), error)
	Stdin  io.Reader
	Out    io.Writer
	Err    io.Writer
	Editor func(path string) error
	Now    func() time.Time
}

// flags returns a flag set with the shared --namespace flag.
func (d mgmtDeps) flags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(d.Err)
	ns := fs.String("namespace", "", "namespace to operate on (default: the cwd's)")
	return fs, ns
}

// open validates the --namespace value (usage error, exit 2) and builds the
// service.
func (d mgmtDeps) open(ns string, warnFallback bool) (mgmtService, func(), error) {
	if ns != "" && !namespace.ValidName(ns) {
		return nil, nil, usageError(fmt.Errorf("invalid --namespace %q", ns))
	}
	return d.Open(ns, warnFallback)
}

// parseFlags parses args allowing flags after positional arguments
// (`rm ID --hard`) and returns the positionals. Problems are usage errors.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageError(err)
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// oneID requires exactly one positional (the record id).
func oneID(pos []string, usage string) (string, error) {
	if len(pos) != 1 {
		return "", usageError(errors.New("usage: claude-memory " + usage))
	}
	return pos[0], nil
}

// lineReader reads plain lines; ok is false at EOF.
type lineReader struct{ r *bufio.Reader }

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: bufio.NewReader(r)} }

func (l *lineReader) Line() (string, bool) {
	s, err := l.r.ReadString('\n')
	if err != nil && s == "" {
		return "", false
	}
	return strings.TrimRight(s, "\r\n"), true
}

var shortIDRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

// shortID is the first 8 hex chars of a record id.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// resolveID finds the record for a full UUID or an 8-hex short id. A short id
// is matched only against the service's own namespace (all statuses), so a
// global record needs its full UUID.
func resolveID(ctx context.Context, svc mgmtService, arg string) (*record.Record, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	if _, err := uuid.Parse(arg); err == nil && len(arg) == 36 {
		rec, err := svc.GetRecord(ctx, arg)
		if errors.Is(err, memory.ErrNotFound) {
			return nil, fmt.Errorf("record %s not found", arg)
		}
		return rec, err
	}
	if !shortIDRe.MatchString(arg) {
		return nil, usageError(fmt.Errorf("invalid id %q: use a full UUID or an 8-hex short id", arg))
	}
	recs, err := svc.ListRecords(ctx, memory.ListFilters{})
	if err != nil {
		return nil, err
	}
	var matches []*record.Record
	for _, r := range recs {
		if strings.HasPrefix(r.ID, arg) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("record %s not found in namespace %s (a global record needs its full UUID)", arg, svc.Namespace())
	case 1:
		return matches[0], nil
	}
	var b strings.Builder
	for _, r := range matches {
		fmt.Fprintf(&b, "\n  %s  %s  %s", r.ID, r.Status, truncRunes(oneLine(r.Title), 60))
	}
	return nil, fmt.Errorf("short id %s is ambiguous, use the full UUID:%s", arg, b.String())
}

// ownNamespace refuses to mutate a record outside the target namespace
// (including a global record visible through the read fallback).
func ownNamespace(rec *record.Record, svc mgmtService) error {
	if rec.Namespace != svc.Namespace() {
		return fmt.Errorf("record is in namespace %s; use --namespace %s", rec.Namespace, rec.Namespace)
	}
	return nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncRunes cuts s to at most n runes, ending with "…" when it was longer.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// staleInfo is the staleness verdict of one record.
type staleInfo struct {
	State   string // "stale", "fresh" or "unchecked"
	Commits int    // commits touching the files; 0 = unknown
}

// String is the text of the stale line: `stale: N commits`, `stale`, `fresh`
// or `unchecked`.
func (s staleInfo) String() string {
	if s.State == "stale" && s.Commits > 0 {
		return fmt.Sprintf("stale: %d commits", s.Commits)
	}
	return s.State
}

// staleCheck computes the verdict against the cwd checkout. Service.StaleHint
// returns nil for both "fresh" and "cannot check", so the cases it cannot
// check are screened out first: no checkout, another repo, no baseline commit
// or no usable files.
func staleCheck(ctx context.Context, svc mgmtService, rec *record.Record) staleInfo {
	co, ok := svc.Checkout()
	if !ok || co.Repo != rec.Repo || rec.CommitSHA == nil || len(memory.NormalizeFiles(co.Dir, rec.Files)) == 0 {
		return staleInfo{State: "unchecked"}
	}
	if h := svc.StaleHint(ctx, rec); h != nil {
		return staleInfo{State: "stale", Commits: h.Commits}
	}
	return staleInfo{State: "fresh"}
}

// shellEditor returns the Editor port: $VISUAL, else $EDITOR, else vi, run as
// `sh -c '<editor> "$1"' sh <path>` (editors with arguments work; the path is
// never shell-parsed) on the terminal's stdio.
func shellEditor(getenv func(string) string) func(path string) error {
	return func(path string) error {
		cmd := editorCommand(editorProgram(getenv), path)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
}

// editorProgram is $VISUAL, else $EDITOR, else vi.
func editorProgram(getenv func(string) string) string {
	if ed := getenv("VISUAL"); ed != "" {
		return ed
	}
	if ed := getenv("EDITOR"); ed != "" {
		return ed
	}
	return "vi"
}

func editorCommand(editor, path string) *exec.Cmd {
	return exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
}

// recordAction helpers shared by the subcommands and review.

// promoteRecord approves a candidate. It reports whether it changed anything.
func promoteRecord(ctx context.Context, d mgmtDeps, svc mgmtService, rec *record.Record) (bool, error) {
	switch rec.Status {
	case record.StatusActive:
		fmt.Fprintln(d.Out, "already active")
		return false, nil
	case record.StatusDeprecated:
		return false, errors.New("cannot promote a deprecated record")
	}
	active := record.StatusActive
	if _, err := svc.UpdateRecord(ctx, &memory.UpdateRequest{ID: rec.ID, Status: &active}); err != nil {
		return false, err
	}
	fmt.Fprintf(d.Out, "promoted %s\n", shortID(rec.ID))
	return true, nil
}

// deprecateRecord deprecates a record. It reports whether it changed anything.
func deprecateRecord(ctx context.Context, d mgmtDeps, svc mgmtService, rec *record.Record, reason string) (bool, error) {
	if rec.Status == record.StatusDeprecated {
		fmt.Fprintln(d.Out, "already deprecated")
		return false, nil
	}
	if _, err := svc.DeprecateRecord(ctx, &memory.DeprecateRequest{ID: rec.ID, Reason: reason}); err != nil {
		return false, err
	}
	fmt.Fprintf(d.Out, "deprecated %s\n", shortID(rec.ID))
	return true, nil
}

// hardDelete asks for confirmation (unless yes) and deletes the record. EOF or
// anything but y/yes means no. eof reports that stdin ended at the prompt.
// hint names the alternative shown when other records reference the record.
func hardDelete(ctx context.Context, d mgmtDeps, in *lineReader, svc mgmtService, rec *record.Record, yes bool, hint string) (deleted, eof bool, err error) {
	if !yes {
		fmt.Fprintf(d.Out, "delete %s %q permanently? [y/N] ", shortID(rec.ID), oneLine(rec.Title))
		line, ok := in.Line()
		if !ok {
			fmt.Fprintln(d.Out, "not deleted")
			return false, true, nil
		}
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(d.Out, "not deleted")
			return false, false, nil
		}
	}
	if err := svc.DeleteRecord(ctx, rec.ID); err != nil {
		var ref *memory.ErrReferenced
		if errors.As(err, &ref) {
			ids := make([]string, len(ref.IDs))
			for i, id := range ref.IDs {
				ids[i] = shortID(id)
			}
			return false, false, fmt.Errorf("not deleted: referenced by superseded_by of %s; %s", strings.Join(ids, ", "), hint)
		}
		return false, false, err
	}
	fmt.Fprintf(d.Out, "deleted %s\n", shortID(rec.ID))
	return true, false, nil
}

// runLs lists the target namespace's records, newest first.
func runLs(ctx context.Context, d mgmtDeps, args []string) error {
	fs, nsFlag := d.flags("ls")
	status := fs.String("status", "", "only this status (default: candidate and active)")
	kind := fs.String("kind", "", "only this kind")
	repo := fs.String("repo", "", "only this repo")
	limit := fs.Int("limit", 50, "maximum records (0 = all)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 || *limit < 0 {
		return usageError(errors.New(manageUsage))
	}
	filters := memory.ListFilters{}
	if *status != "" {
		st := record.Status(*status)
		if !st.IsValid() {
			return usageError(fmt.Errorf("invalid --status %q", *status))
		}
		filters.Status = &st
	}
	if *kind != "" {
		k := record.Kind(*kind)
		if !k.IsValid() {
			return usageError(fmt.Errorf("invalid --kind %q", *kind))
		}
		filters.Kind = &k
	}
	if *repo != "" {
		filters.Repo = repo
	}

	svc, done, err := d.open(*nsFlag, false)
	if err != nil {
		return err
	}
	defer done()
	recs, err := svc.ListRecords(ctx, filters)
	if err != nil {
		return err
	}
	if *status == "" {
		kept := recs[:0:0]
		for _, r := range recs {
			if r.Status != record.StatusDeprecated {
				kept = append(kept, r)
			}
		}
		recs = kept
	}
	if *limit > 0 && len(recs) > *limit {
		recs = recs[:*limit]
	}

	if len(recs) == 0 {
		fmt.Fprintln(d.Out, "no records")
		return nil
	}
	for _, r := range recs {
		fmt.Fprintf(d.Out, "%-8s  %-10s  %-10s  %-8s  %-20s  %s  %s\n",
			shortID(r.ID), r.Status, r.Kind, r.Source, truncRunes(r.Repo, 20),
			r.CreatedAt.UTC().Format("2006-01-02"), truncRunes(oneLine(r.Title), 80))
	}
	return nil
}

// runShow prints one record in full.
func runShow(ctx context.Context, d mgmtDeps, args []string) error {
	fs, nsFlag := d.flags("show")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	arg, err := oneID(pos, "show ID [--namespace NS]")
	if err != nil {
		return err
	}
	svc, done, err := d.open(*nsFlag, false)
	if err != nil {
		return err
	}
	defer done()
	rec, err := resolveID(ctx, svc, arg)
	if err != nil {
		return err
	}
	st := staleCheck(ctx, svc, rec)

	opt := func(s *string) string {
		if s == nil || *s == "" {
			return "-"
		}
		return *s
	}
	list := func(s []string) string {
		if len(s) == 0 {
			return "-"
		}
		return strings.Join(s, ", ")
	}
	ts := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	lastUsed := "-"
	if rec.LastUsedAt != nil {
		lastUsed = ts(*rec.LastUsedAt)
	}
	w := d.Out
	fmt.Fprintf(w, "id:          %s\n", rec.ID)
	fmt.Fprintf(w, "title:       %s\n", oneLine(rec.Title))
	fmt.Fprintf(w, "kind:        %s\n", rec.Kind)
	fmt.Fprintf(w, "status:      %s\n", rec.Status)
	fmt.Fprintf(w, "source:      %s\n", rec.Source)
	fmt.Fprintf(w, "repo:        %s\n", rec.Repo)
	fmt.Fprintf(w, "namespace:   %s\n", rec.Namespace)
	fmt.Fprintf(w, "tags:        %s\n", list(rec.Tags))
	fmt.Fprintf(w, "files:       %s\n", list(rec.Files))
	fmt.Fprintf(w, "commit_sha:  %s\n", opt(rec.CommitSHA))
	fmt.Fprintf(w, "ticket:      %s\n", opt(rec.Ticket))
	fmt.Fprintf(w, "confidence:  %.2f\n", rec.Confidence)
	fmt.Fprintf(w, "seen/used:   %d/%d\n", rec.SeenCount, rec.UsedCount)
	fmt.Fprintf(w, "created:     %s\n", ts(rec.CreatedAt))
	fmt.Fprintf(w, "updated:     %s\n", ts(rec.UpdatedAt))
	fmt.Fprintf(w, "last used:   %s\n", lastUsed)
	fmt.Fprintf(w, "deprecation: %s\n", opt(rec.DeprecationReason))
	fmt.Fprintf(w, "superseded:  %s\n", opt(rec.SupersededBy))
	fmt.Fprintf(w, "%s\n\n%s\n", st, rec.Content)
	return nil
}

// runRm deprecates a record, or with --hard deletes it.
func runRm(ctx context.Context, d mgmtDeps, args []string) error {
	fs, nsFlag := d.flags("rm")
	reason := fs.String("reason", defaultRmReason, "deprecation reason")
	hard := fs.Bool("hard", false, "delete the row permanently (asks first)")
	yes := fs.Bool("yes", false, "with --hard: do not ask")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	arg, err := oneID(pos, "rm ID [--reason TEXT] [--hard [--yes]] [--namespace NS]")
	if err != nil {
		return err
	}
	if *yes && !*hard {
		return usageError(errors.New("--yes only applies to --hard"))
	}
	svc, done, err := d.open(*nsFlag, true)
	if err != nil {
		return err
	}
	defer done()
	rec, err := resolveID(ctx, svc, arg)
	if err != nil {
		return err
	}
	if err := ownNamespace(rec, svc); err != nil {
		return err
	}
	if *hard {
		_, _, err := hardDelete(ctx, d, newLineReader(d.Stdin), svc, rec, *yes, "deprecate it instead (rm without --hard)")
		return err
	}
	_, err = deprecateRecord(ctx, d, svc, rec, *reason)
	return err
}

// runPromote sets a candidate to active.
func runPromote(ctx context.Context, d mgmtDeps, args []string) error {
	fs, nsFlag := d.flags("promote")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	arg, err := oneID(pos, "promote ID [--namespace NS]")
	if err != nil {
		return err
	}
	svc, done, err := d.open(*nsFlag, true)
	if err != nil {
		return err
	}
	defer done()
	rec, err := resolveID(ctx, svc, arg)
	if err != nil {
		return err
	}
	if err := ownNamespace(rec, svc); err != nil {
		return err
	}
	_, err = promoteRecord(ctx, d, svc, rec)
	return err
}
