package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redfoxius/claude-memory/internal/memory"
	"github.com/redfoxius/claude-memory/internal/record"
)

const (
	reviewPrompt        = "[a]pprove [e]dit [d]eprecate [x]delete [s]kip [v]iew [q]uit > "
	reviewReasonDefault = "rejected in review"
	reviewPreviewLines  = 15
	reviewSimilarCount  = 3
)

// reviewCounts are the actions taken in one review pass.
type reviewCounts struct{ approved, edited, deprecated, deleted, skipped int }

func (c reviewCounts) String() string {
	return fmt.Sprintf("approved %d, edited %d, deprecated %d, deleted %d, skipped %d",
		c.approved, c.edited, c.deprecated, c.deleted, c.skipped)
}

// runReview walks the target namespace's candidates newest first and asks
// what to do with each, reading plain lines from d.Stdin.
func runReview(ctx context.Context, d mgmtDeps, args []string) error {
	fs, nsFlag := d.flags("review")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError(errors.New("usage: claude-memory review [--namespace NS]"))
	}
	svc, done, err := d.open(*nsFlag, true)
	if err != nil {
		return err
	}
	defer done()

	cand := record.StatusCandidate
	recs, err := svc.ListRecords(ctx, memory.ListFilters{Status: &cand}) // newest first
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fmt.Fprintln(d.Out, "no candidates")
		return nil
	}

	in := newLineReader(d.Stdin)
	var counts reviewCounts
	for i := 0; i < len(recs); i++ {
		if quit := reviewOne(ctx, d, in, svc, recs[i], i+1, len(recs), &counts); quit {
			break
		}
	}
	fmt.Fprintln(d.Out, counts)
	return nil
}

// reviewOne shows one record and loops on its prompt until an action moves on
// (approve, deprecate, delete, skip) or the user quits. It reports quit.
// A failed action prints the error and prompts again on the same record.
func reviewOne(ctx context.Context, d mgmtDeps, in *lineReader, svc mgmtService, rec *record.Record, i, n int, c *reviewCounts) (quit bool) {
	showCandidate(ctx, d, svc, rec, i, n)
	for {
		fmt.Fprint(d.Out, reviewPrompt)
		line, ok := in.Line()
		if !ok {
			fmt.Fprintln(d.Out)
			return true
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "q":
			return true
		case "s", "":
			c.skipped++
			return false
		case "v":
			fmt.Fprintln(d.Out, rec.Content)
		case "a":
			did, err := promoteRecord(ctx, d, svc, rec)
			if err != nil {
				fmt.Fprintf(d.Err, "error: %v\n", err)
				continue
			}
			if did {
				c.approved++
			}
			return false
		case "e":
			updated, changed, err := editRecord(ctx, d, svc, rec)
			if err != nil {
				fmt.Fprintf(d.Err, "error: %v\n", err)
				continue
			}
			if changed {
				c.edited++
			}
			rec = updated
			showCandidate(ctx, d, svc, rec, i, n)
		case "d":
			fmt.Fprintf(d.Out, "reason (Enter = %s): ", reviewReasonDefault)
			reason, ok := in.Line()
			if !ok {
				fmt.Fprintln(d.Out)
				return true // EOF aborts the action
			}
			if reason = strings.TrimSpace(reason); reason == "" {
				reason = reviewReasonDefault
			}
			did, err := deprecateRecord(ctx, d, svc, rec, reason)
			if err != nil {
				fmt.Fprintf(d.Err, "error: %v\n", err)
				continue
			}
			if did {
				c.deprecated++
			}
			return false
		case "x":
			deleted, eof, err := hardDelete(ctx, d, in, svc, rec, false, "use [d]eprecate instead")
			if eof {
				return true // EOF aborts the action
			}
			if err != nil {
				fmt.Fprintf(d.Err, "error: %v\n", err)
				continue
			}
			if deleted {
				c.deleted++
				return false
			}
		default:
			fmt.Fprintln(d.Out, "unknown key")
		}
	}
}

// showCandidate prints the review block of one record (AC-15).
func showCandidate(ctx context.Context, d mgmtDeps, svc mgmtService, rec *record.Record, i, n int) {
	w := d.Out
	list := func(s []string) string {
		if len(s) == 0 {
			return "-"
		}
		return strings.Join(s, ", ")
	}
	fmt.Fprintf(w, "\n[%d/%d] %s  %s  repo=%s  source=%s  age=%s  seen=%d used=%d\n",
		i, n, shortID(rec.ID), rec.Kind, rec.Repo, rec.Source, age(d.Now().Sub(rec.CreatedAt)), rec.SeenCount, rec.UsedCount)
	fmt.Fprintf(w, "%s\n", oneLine(rec.Title))
	fmt.Fprintf(w, "%s\n", staleCheck(ctx, svc, rec))
	fmt.Fprintf(w, "tags: %s\nfiles: %s\n\n", list(rec.Tags), list(rec.Files))

	lines := strings.Split(strings.TrimRight(rec.Content, "\n"), "\n")
	shown := lines
	if len(shown) > reviewPreviewLines {
		shown = shown[:reviewPreviewLines]
	}
	for _, l := range shown {
		fmt.Fprintln(w, l)
	}
	if more := len(lines) - len(shown); more > 0 {
		fmt.Fprintf(w, "... %d more lines (v to view)\n", more)
	}

	sims, err := svc.Similar(ctx, rec.ID, reviewSimilarCount)
	switch {
	case errors.Is(err, memory.ErrNoEmbedding):
		fmt.Fprintln(w, "\nsimilar: unavailable")
	case err != nil:
		fmt.Fprintf(w, "\nsimilar: unavailable (%v)\n", err)
	case len(sims) == 0:
		fmt.Fprintln(w, "\nsimilar: none")
	default:
		fmt.Fprintln(w, "\nsimilar:")
		for _, s := range sims {
			status := "?"
			if r, err := svc.GetRecord(ctx, s.ID); err == nil {
				status = string(r.Status)
			}
			fmt.Fprintf(w, "  %s  %-9s  %.2f  %s\n", shortID(s.ID), status, s.Similarity, truncRunes(oneLine(s.Title), 80))
		}
	}
}

// age renders a duration as minutes, hours or days.
func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d/time.Minute), 0))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
