package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/eventspool"
	"claude-memory/internal/memory"
)

// defaultStatsSince is the reporting window when --since is not given.
const defaultStatsSince = 30 * 24 * time.Hour

// maxStatsDays bounds --since Nd (events are pruned after 365 days anyway).
const maxStatsDays = 3650

// statsReader is what `stats` needs from the store, declared by the consumer.
// *postgres.Store satisfies it. namespace "" means all namespaces.
type statsReader interface {
	StatsNamespaces(ctx context.Context, since time.Time) ([]string, error)
	StatsCounts(ctx context.Context, since time.Time, namespace string) (memory.EventCounts, error)
}

// statsOptions are the parsed `stats` flags.
type statsOptions struct {
	Since time.Duration
	JSON  bool
}

// parseDaysOrDuration parses "Nd" (days) or a Go duration ("36h").
func parseDaysOrDuration(s string) (time.Duration, error) {
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		if n > maxStatsDays {
			return 0, fmt.Errorf("%q exceeds %d days", s, maxStatsDays)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", s)
	}
	return d, nil
}

// parseStatsFlags parses `stats` arguments; any problem is a usage error.
func parseStatsFlags(args []string, stderr io.Writer) (statsOptions, error) {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.String("since", "30d", "reporting window: Nd or a Go duration (e.g. 36h)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return statsOptions{}, usageError(err)
	}
	if fs.NArg() > 0 {
		return statsOptions{}, usageError(errors.New("usage: claude-memory stats [--since 30d] [--json]"))
	}
	d, err := parseDaysOrDuration(*since)
	if err != nil {
		return statsOptions{}, usageError(fmt.Errorf("--since: %w", err))
	}
	return statsOptions{Since: d, JSON: *asJSON}, nil
}

// cmdStats builds only the Postgres store (no Ollama, no embedder), applies
// migrations and prints the report. It does not drain the spool.
func cmdStats(cfg *config.Config, args []string) error {
	opts, err := parseStatsFlags(args, os.Stderr)
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, cleanup, err := buildPostgresStore(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer cleanup()
	return runStats(ctx, store, opts, time.Now(), spoolDir(), os.Stdout)
}

// ratio is num/den; with a zero denominator it is "n/a".
type ratio struct{ Num, Den int }

func (r ratio) Value() *float64 {
	if r.Den == 0 {
		return nil
	}
	v := float64(r.Num) / float64(r.Den)
	return &v
}

func (r ratio) String() string {
	v := r.Value()
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%% (%d/%d)", *v*100, r.Num, r.Den)
}

func (r ratio) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Num   int      `json:"num"`
		Den   int      `json:"den"`
		Ratio *float64 `json:"ratio"` // null when the denominator is 0
	}{r.Num, r.Den, r.Value()})
}

// statsBlock is the report for one namespace ("" in Namespace = all).
type statsBlock struct {
	Namespace string `json:"namespace"`
	memory.EventCounts
	// Ratios computed from the counts (AC-27).
	PrecisionProxy  ratio `json:"precision_proxy"` // useful within 2 h / distinct injected
	UsefulAny       ratio `json:"useful_any_ratio"`
	PromotionRate   ratio `json:"promotion_rate"`
	ImportPromotion ratio `json:"import_promotion"`
	StaleFlagRate   ratio `json:"stale_flag_rate"`
	CheckCoverage   ratio `json:"check_coverage"`
}

func newStatsBlock(ns string, c memory.EventCounts) statsBlock {
	return statsBlock{
		Namespace:       ns,
		EventCounts:     c,
		PrecisionProxy:  ratio{c.UsefulWithin, c.CardRecords},
		UsefulAny:       ratio{c.UsefulAny, c.CardRecords},
		PromotionRate:   ratio{c.CandidatesPromoted, c.CandidatesCreated},
		ImportPromotion: ratio{c.ImportPromoted, c.ImportCreated},
		StaleFlagRate:   ratio{c.CardsStale, c.CardsChecked},
		CheckCoverage:   ratio{c.CardsChecked, c.Cards},
	}
}

// statsReport is the whole report.
type statsReport struct {
	Since        time.Time    `json:"since"`
	Total        statsBlock   `json:"total"`
	Namespaces   []statsBlock `json:"namespaces"`
	SpoolPending int          `json:"spool_pending"`
}

// runStats collects the counts for the window ending at now and prints the
// report: a total block and one block per namespace.
func runStats(ctx context.Context, r statsReader, opts statsOptions, now time.Time, spool string, out io.Writer) error {
	since := now.Add(-opts.Since)

	namespaces, err := r.StatsNamespaces(ctx, since)
	if err != nil {
		return err
	}
	total, err := r.StatsCounts(ctx, since, "")
	if err != nil {
		return err
	}
	rep := statsReport{Since: since.UTC(), Total: newStatsBlock("", total), Namespaces: []statsBlock{}}
	for _, ns := range namespaces {
		c, err := r.StatsCounts(ctx, since, ns)
		if err != nil {
			return err
		}
		rep.Namespaces = append(rep.Namespaces, newStatsBlock(ns, c))
	}
	rep.SpoolPending, _, _ = eventspool.Pending(spool)

	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	writeStatsText(out, rep, opts.Since)
	return nil
}

var (
	statsSources  = []string{"inline", "session", "pr", "import"}
	statsStatuses = []string{"candidate", "active", "deprecated"}
)

func writeStatsText(w io.Writer, rep statsReport, window time.Duration) {
	fmt.Fprintf(w, "claude-memory stats: events since %s (%s)\n", rep.Since.Format("2006-01-02 15:04 MST"), window)
	writeStatsBlock(w, "all namespaces", rep.Total)
	for _, b := range rep.Namespaces {
		writeStatsBlock(w, "namespace "+b.Namespace, b)
	}
	fmt.Fprintf(w, "\n%d events still in the spool\n", rep.SpoolPending)
}

func writeStatsBlock(w io.Writer, title string, b statsBlock) {
	fmt.Fprintf(w, "\n== %s ==\n", title)
	fmt.Fprintf(w, "cards injected:      %d (%d distinct records)\n", b.Cards, b.CardRecords)
	fmt.Fprintf(w, "useful (2 h):        %s  precision proxy\n", b.PrecisionProxy)
	fmt.Fprintf(w, "useful (any):        %s  attribution gap\n", b.UsefulAny)
	fmt.Fprintf(w, "feedback:            %s\n", joinCounts(b.Feedback, []string{"useful", "outdated", "wrong"}))
	fmt.Fprintf(w, "records created:     %s\n", joinCounts(b.CreatedBySource, statsSources))
	fmt.Fprintf(w, "inventory now:       %s\n", inventoryLine(b.Inventory))
	fmt.Fprintf(w, "promotion rate:      %s  candidates created in the window later promoted (imports excluded)\n", b.PromotionRate)
	fmt.Fprintf(w, "import promotion:    %s  imported candidates later promoted\n", b.ImportPromotion)
	fmt.Fprintf(w, "stale-flag rate:     %s  checked cards flagged stale\n", b.StaleFlagRate)
	fmt.Fprintf(w, "check coverage:      %s  cards whose staleness could be checked\n", b.CheckCoverage)
	fmt.Fprintf(w, "lifecycle:           superseded=%d deprecated(%s) ttl-deleted=%d\n",
		b.Superseded, joinCounts(b.DeprecatedByVia, []string{"tool", "feedback"}), b.TTLDeleted)
}

// joinCounts prints "k=n" for the listed keys in order, then any others.
func joinCounts(m map[string]int, order []string) string {
	var parts []string
	seen := map[string]bool{}
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
		seen[k] = true
	}
	var extra []string
	for k := range m {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func inventoryLine(inv map[string]map[string]int) string {
	var parts []string
	for _, src := range statsSources {
		parts = append(parts, fmt.Sprintf("%s[%s]", src, joinCounts(inv[src], statsStatuses)))
	}
	return strings.Join(parts, " ")
}
