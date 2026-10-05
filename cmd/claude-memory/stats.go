package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
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
	// Reliability: the newest mcp session, its DB counts, and which spooled
	// event ids the table already holds.
	StatsLatestServeSession(ctx context.Context, since time.Time) (string, time.Time, error)
	StatsSessionCounts(ctx context.Context, since time.Time, sessionID string) (memory.SessionCounts, error)
	EventIDsExist(ctx context.Context, ids []string) (map[string]bool, error)
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
	// Failure rates: error outcomes / attempts (n/a with no attempts).
	FailureRateMCPSearch  ratio `json:"failure_rate_mcp_search"`
	FailureRateHookSearch ratio `json:"failure_rate_hook_search"`
	FailureRateStore      ratio `json:"failure_rate_store"`
}

func newStatsBlock(ns string, c memory.EventCounts) statsBlock {
	c.Reliability = withMaps(c.Reliability)
	return statsBlock{
		Namespace:       ns,
		EventCounts:     c,
		PrecisionProxy:  ratio{c.UsefulWithin, c.CardRecords},
		UsefulAny:       ratio{c.UsefulAny, c.CardRecords},
		PromotionRate:   ratio{c.CandidatesPromoted, c.CandidatesCreated},
		ImportPromotion: ratio{c.ImportPromoted, c.ImportCreated},
		StaleFlagRate:   ratio{c.CardsStale, c.CardsChecked},
		CheckCoverage:   ratio{c.CardsChecked, c.Cards},

		FailureRateMCPSearch:  failureRatio(c.Reliability.Search["mcp"]),
		FailureRateHookSearch: failureRatio(c.Reliability.Search["hook"]),
		FailureRateStore:      failureRatio(c.Reliability.Store),
	}
}

// emptyCounts is a zero EventCounts with every map allocated.
func emptyCounts() memory.EventCounts {
	return memory.EventCounts{
		Feedback:        map[string]int{},
		CreatedBySource: map[string]int{},
		DeprecatedByVia: map[string]int{},
		Inventory:       map[string]map[string]int{},
		Reliability:     memory.NewReliabilityCounts(),
	}
}

// withMaps allocates any nil map of r.
func withMaps(r memory.ReliabilityCounts) memory.ReliabilityCounts {
	if r.Search == nil {
		r.Search = map[string]map[string]int{}
	}
	if r.Store == nil {
		r.Store = map[string]int{}
	}
	if r.Failures == nil {
		r.Failures = map[string]int{}
	}
	return r
}

// freshSpoolEvents returns the reliability events in the spool with at >=
// since whose ids the events table does not hold yet (the drain has not run),
// each id once. It reads the spool without modifying it.
func freshSpoolEvents(ctx context.Context, r statsReader, spool string, since time.Time) ([]memory.Event, bool, error) {
	all, full := eventspool.Scan(spool)
	var cand []memory.Event
	var ids []string
	seen := map[string]bool{}
	for _, e := range all {
		if (e.Type != memory.EventSearchCalled && e.Type != memory.EventStoreAttempted) || e.At.Before(since) || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		cand = append(cand, e)
		ids = append(ids, e.ID)
	}
	if len(ids) == 0 {
		return nil, full, nil
	}
	exist, err := r.EventIDsExist(ctx, ids)
	if err != nil {
		return nil, full, err
	}
	var fresh []memory.Event
	for _, e := range cand {
		if !exist[e.ID] {
			fresh = append(fresh, e)
		}
	}
	return fresh, full, nil
}

// addSpoolEvent folds one reliability event into c.
func addSpoolEvent(c *memory.ReliabilityCounts, e memory.Event) {
	*c = withMaps(*c)
	outcome := string(e.Outcome)
	if e.Type == memory.EventSearchCalled {
		via := string(e.Via)
		if c.Search[via] == nil {
			c.Search[via] = map[string]int{}
		}
		c.Search[via][outcome]++
	} else {
		c.Store[outcome]++
	}
	if e.ErrorClass == "" {
		return
	}
	c.Failures[string(e.ErrorClass)]++
	if e.ErrorClass != memory.ErrClassDBUnavailable {
		return
	}
	hour := e.At.UTC().Truncate(time.Hour)
	if !slices.ContainsFunc(c.DownHours, hour.Equal) {
		c.DownHours = append(c.DownHours, hour)
		c.DBDownHours = len(c.DownHours)
	}
	at := e.At.UTC()
	if c.FirstDown == nil || at.Before(*c.FirstDown) {
		c.FirstDown = &at
	}
	if c.LastDown == nil || at.After(*c.LastDown) {
		c.LastDown = &at
	}
}

// latestServeSession finds the serve session with the newest mcp event, in the
// table or among the fresh spool events, and sums its table rows with its
// fresh spool rows (the two never overlap). nil when there is none.
func latestServeSession(ctx context.Context, r statsReader, since time.Time, fresh []memory.Event) (*memory.SessionCounts, error) {
	id, at, err := r.StatsLatestServeSession(ctx, since)
	if err != nil {
		return nil, err
	}
	for _, e := range fresh {
		if e.Via == memory.ViaMCP && e.SessionID != "" && e.At.After(at) {
			id, at = e.SessionID, e.At
		}
	}
	if id == "" {
		return nil, nil
	}
	c, err := r.StatsSessionCounts(ctx, since, id)
	if err != nil {
		return nil, err
	}
	for _, e := range fresh {
		if e.Via != memory.ViaMCP || e.SessionID != id {
			continue
		}
		t := e.At.UTC()
		if c.First == nil || t.Before(*c.First) {
			c.First = &t
		}
		if c.Last == nil || t.After(*c.Last) {
			c.Last = &t
		}
		if e.Type == memory.EventSearchCalled {
			c.Searches++
		} else {
			c.Stores++
		}
		if e.ErrorClass != "" {
			c.Failures++
		}
	}
	return &c, nil
}

// failureRatio is error / all attempts over an outcome -> count map.
func failureRatio(outcomes map[string]int) ratio {
	total := 0
	for _, n := range outcomes {
		total += n
	}
	return ratio{outcomes[string(memory.OutcomeError)], total}
}

// statsReport is the whole report.
type statsReport struct {
	Since        time.Time    `json:"since"`
	Total        statsBlock   `json:"total"`
	Namespaces   []statsBlock `json:"namespaces"`
	SpoolPending int          `json:"spool_pending"`
	// ReliabilityFromSpool counts spool-resident reliability events added to
	// the numbers because the table does not hold them yet.
	ReliabilityFromSpool int                   `json:"reliability_from_spool"`
	SpoolFull            bool                  `json:"spool_full"`
	LatestServeSession   *memory.SessionCounts `json:"latest_serve_session"`
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
	byNS := map[string]memory.EventCounts{}
	for _, ns := range namespaces {
		c, err := r.StatsCounts(ctx, since, ns)
		if err != nil {
			return err
		}
		byNS[ns] = c
	}

	// Spool-resident reliability events the table does not hold yet.
	fresh, spoolFull, err := freshSpoolEvents(ctx, r, spool, since)
	if err != nil {
		return err
	}
	for _, e := range fresh {
		addSpoolEvent(&total.Reliability, e)
		c, ok := byNS[e.Namespace]
		if !ok { // a namespace seen only in the spool: zero DB counts
			c = emptyCounts()
		}
		addSpoolEvent(&c.Reliability, e)
		byNS[e.Namespace] = c
	}
	session, err := latestServeSession(ctx, r, since, fresh)
	if err != nil {
		return err
	}

	rep := statsReport{
		Since: since.UTC(), Total: newStatsBlock("", total), Namespaces: []statsBlock{},
		ReliabilityFromSpool: len(fresh), SpoolFull: spoolFull, LatestServeSession: session,
	}
	// SQL order first (stable JSON array order), then spool-only namespaces
	// in sorted order.
	names := append([]string(nil), namespaces...)
	inSQL := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		inSQL[ns] = true
	}
	var spoolOnly []string
	for ns := range byNS {
		if !inSQL[ns] {
			spoolOnly = append(spoolOnly, ns)
		}
	}
	sort.Strings(spoolOnly)
	names = append(names, spoolOnly...)
	for _, ns := range names {
		rep.Namespaces = append(rep.Namespaces, newStatsBlock(ns, byNS[ns]))
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
	writeServeSession(w, rep.LatestServeSession)
	for _, b := range rep.Namespaces {
		writeStatsBlock(w, "namespace "+b.Namespace, b)
	}
	fmt.Fprintf(w, "\n%d events still in the spool\n", rep.SpoolPending)
	if rep.ReliabilityFromSpool > 0 {
		fmt.Fprintf(w, "%d reliability events counted from the spool (not in the database yet)\n", rep.ReliabilityFromSpool)
	}
	if rep.SpoolFull {
		fmt.Fprintln(w, "spool is full: new events are being dropped and not counted")
	}
}

var (
	searchOutcomes = []string{"ok", "degraded", "error"}
	storeOutcomes  = []string{"added", "updated", "superseded", "noop", "needs_judgment", "error"}
	errorClasses   = []string{"db_unavailable", "embedding_unavailable", "invalid_request", "timeout", "internal"}
)

// writeServeSession prints the total block's latest serve session line.
func writeServeSession(w io.Writer, s *memory.SessionCounts) {
	if s == nil {
		fmt.Fprintf(w, "latest serve session: n/a\n")
		return
	}
	id := s.ID
	if len(id) > 8 {
		id = id[:8]
	}
	fmt.Fprintf(w, "latest serve session: %s  %s  searches=%d store-attempts=%d failures=%d\n",
		id, timeRange(s.First, s.Last), s.Searches, s.Stores, s.Failures)
}

// timeRange prints "first - last" in UTC, or n/a when either is missing.
func timeRange(first, last *time.Time) string {
	if first == nil || last == nil {
		return "n/a"
	}
	const layout = "2006-01-02 15:04"
	return first.UTC().Format(layout) + " - " + last.UTC().Format(layout) + " UTC"
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
	rel := b.Reliability
	fmt.Fprintf(w, "searches (mcp):      %s\n", joinCounts(rel.Search["mcp"], searchOutcomes))
	fmt.Fprintf(w, "searches (hook):     %s\n", joinCounts(rel.Search["hook"], searchOutcomes))
	fmt.Fprintf(w, "store attempts:      %s\n", joinCounts(rel.Store, storeOutcomes))
	fmt.Fprintf(w, "failures:            %s\n", joinCounts(rel.Failures, errorClasses))
	fmt.Fprintf(w, "failure rate:        mcp search %s, hook search %s, store %s\n",
		b.FailureRateMCPSearch, b.FailureRateHookSearch, b.FailureRateStore)
	fmt.Fprintf(w, "db-down hours:       %d (%s)\n", rel.DBDownHours, timeRange(rel.FirstDown, rel.LastDown))
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
