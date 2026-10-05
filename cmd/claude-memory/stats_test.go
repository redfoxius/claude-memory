package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/memory"
)

func TestParseStatsFlags(t *testing.T) {
	ok := map[string]time.Duration{
		"":                  30 * 24 * time.Hour,
		"--since 7d":        7 * 24 * time.Hour,
		"--since 36h":       36 * time.Hour,
		"--since=1d --json": 24 * time.Hour,
		"--since 90m":       90 * time.Minute,
	}
	for in, want := range ok {
		var args []string
		if in != "" {
			args = strings.Fields(in)
		}
		o, err := parseStatsFlags(args, io.Discard)
		if err != nil || o.Since != want {
			t.Errorf("%q: %+v %v", in, o, err)
		}
	}
	if o, _ := parseStatsFlags([]string{"--json"}, io.Discard); !o.JSON {
		t.Error("--json not parsed")
	}
	for _, in := range []string{"--since x", "--since 0d", "--since -3d", "--since", "--bogus", "extra", "--since 1.5d", "--since 3651d", "--since 99999999999d"} {
		_, err := parseStatsFlags(strings.Fields(in), io.Discard)
		if err == nil || exitCode(err) != 2 {
			t.Errorf("%q: err = %v (code %d), want a usage error", in, err, exitCode(err))
		}
	}
}

type fakeStats struct {
	nss       []string
	byNS      map[string]memory.EventCounts
	sinceSeen time.Time
	err       error

	// reliability
	sessID   string
	sessAt   time.Time
	sessions map[string]memory.SessionCounts
	inDB     map[string]bool
}

func (f *fakeStats) StatsLatestServeSession(context.Context, time.Time) (string, time.Time, error) {
	return f.sessID, f.sessAt, f.err
}
func (f *fakeStats) StatsSessionCounts(_ context.Context, _ time.Time, id string) (memory.SessionCounts, error) {
	c := f.sessions[id]
	c.ID = id
	return c, f.err
}
func (f *fakeStats) EventIDsExist(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		if f.inDB[id] {
			out[id] = true
		}
	}
	return out, f.err
}

func (f *fakeStats) StatsNamespaces(_ context.Context, since time.Time) ([]string, error) {
	f.sinceSeen = since
	return f.nss, f.err
}
func (f *fakeStats) StatsCounts(_ context.Context, _ time.Time, ns string) (memory.EventCounts, error) {
	return f.byNS[ns], f.err
}

func TestStatsBlockRatios(t *testing.T) {
	b := newStatsBlock("x", memory.EventCounts{
		Cards: 40, CardRecords: 20, CardsChecked: 10, CardsStale: 4,
		UsefulWithin: 5, UsefulAny: 8, CandidatesCreated: 10, CandidatesPromoted: 3,
		ImportCreated: 20, ImportPromoted: 5,
	})
	for name, tc := range map[string]struct {
		got  ratio
		want string
	}{
		"precision":  {b.PrecisionProxy, "25.0% (5/20)"},
		"useful any": {b.UsefulAny, "40.0% (8/20)"},
		"promotion":  {b.PromotionRate, "30.0% (3/10)"},
		"import":     {b.ImportPromotion, "25.0% (5/20)"},
		"stale":      {b.StaleFlagRate, "40.0% (4/10)"},
		"coverage":   {b.CheckCoverage, "25.0% (10/40)"},
	} {
		if tc.got.String() != tc.want {
			t.Errorf("%s = %q, want %q", name, tc.got, tc.want)
		}
	}
}

// AC-28: zero denominators print n/a; an empty table prints zeros.
func TestRunStatsEmptyPrintsZerosAndNA(t *testing.T) {
	var out bytes.Buffer
	fake := &fakeStats{}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := runStats(context.Background(), fake, statsOptions{Since: 30 * 24 * time.Hour}, now, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"== all namespaces ==", "cards injected:      0 (0 distinct records)",
		"useful (2 h):        n/a", "promotion rate:      n/a", "import promotion:    n/a", "stale-flag rate:     n/a", "check coverage:      n/a",
		"feedback:            useful=0 outdated=0 wrong=0", "records created:     inline=0 session=0 pr=0",
		"0 events still in the spool",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if want := now.Add(-30 * 24 * time.Hour); !fake.sinceSeen.Equal(want) {
		t.Errorf("since = %v, want %v", fake.sinceSeen, want)
	}
}

func TestRunStatsTextBlocksAndSpoolLine(t *testing.T) {
	spool := t.TempDir()
	if err := os.WriteFile(filepath.Join(spool, "spool.jsonl"), []byte("\n{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeStats{
		nss: []string{"work", "global"},
		byNS: map[string]memory.EventCounts{
			"": {Cards: 3, CardRecords: 2, Feedback: map[string]int{"useful": 1},
				CreatedBySource:    map[string]int{"pr": 2},
				Inventory:          map[string]map[string]int{"pr": {"active": 5}},
				DeprecatedByVia:    map[string]int{"feedback": 1},
				CandidatesCreated:  4,
				CandidatesPromoted: 1,
				ImportCreated:      6,
				ImportPromoted:     3},
			"work": {Cards: 3, CardRecords: 2},
		},
	}
	var out bytes.Buffer
	if err := runStats(context.Background(), fake, statsOptions{Since: 24 * time.Hour}, time.Now(), spool, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"== all namespaces ==", "== namespace work ==", "== namespace global ==",
		"cards injected:      3 (2 distinct records)", "feedback:            useful=1 outdated=0 wrong=0",
		"records created:     inline=0 session=0 pr=2", "pr[candidate=0 active=5 deprecated=0]",
		"promotion rate:      25.0% (1/4)", "import promotion:    50.0% (3/6)", "pr=2 import=0", "deprecated(tool=0 feedback=1)",
		"2 events still in the spool",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestRunStatsJSONAndErrors(t *testing.T) {
	fake := &fakeStats{nss: []string{"work"}, byNS: map[string]memory.EventCounts{"": {Cards: 2, CardRecords: 1, UsefulWithin: 1}}}
	var out bytes.Buffer
	if err := runStats(context.Background(), fake, statsOptions{Since: time.Hour, JSON: true}, time.Now(), t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Total struct {
			Cards          int `json:"cards_injected"`
			PrecisionProxy struct {
				Num, Den int
				Ratio    *float64
			} `json:"precision_proxy"`
			PromotionRate struct{ Ratio *float64 } `json:"promotion_rate"`
		}
		Namespaces []struct{ Namespace string }
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out.String())
	}
	if rep.Total.Cards != 2 || rep.Total.PrecisionProxy.Ratio == nil || *rep.Total.PrecisionProxy.Ratio != 1 ||
		rep.Total.PromotionRate.Ratio != nil || len(rep.Namespaces) != 1 || rep.Namespaces[0].Namespace != "work" {
		t.Errorf("report = %+v", rep)
	}

	boom := &fakeStats{err: errors.New("db down")}
	if err := runStats(context.Background(), boom, statsOptions{Since: time.Hour}, time.Now(), t.TempDir(), io.Discard); err == nil {
		t.Error("expected the reader's error")
	}
}

// A bad flag exits 2 with a usage message before anything is built or
// connected (the DSN points nowhere).
func TestStatsBadFlagExitsUsage(t *testing.T) {
	t.Parallel()
	out, code := runChild(t, []string{"HOME=" + t.TempDir(), "MEMORY_PG_DSN=postgresql://u:p@127.0.0.1:1/db"}, "stats", "--since", "x")
	if code != 2 || !strings.Contains(out, "--since") {
		t.Errorf("code = %d, output = %q", code, out)
	}
}
