package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/eventspool"
	"claude-memory/internal/memory"
)

// relNow is the top of the current hour: a past instant, because the spool
// scan validates event times against the real clock. Events lie before it.
var relNow = time.Now().UTC().Truncate(time.Hour)

func relEv(ns string, typ memory.EventType, via memory.EventVia, outcome memory.FeedbackOutcome, class memory.ErrorClass, session string, at time.Time) memory.Event {
	e := memory.NewEvent(at, ns, typ)
	e.Via, e.Outcome, e.ErrorClass, e.SessionID = via, outcome, class, session
	return e
}

func writeSpool(t *testing.T, dir string, evs ...memory.Event) {
	t.Helper()
	if err := (eventspool.Sink{Dir: dir}).Append(context.Background(), evs...); err != nil {
		t.Fatal(err)
	}
}

func runStatsJSON(t *testing.T, fake *fakeStats, spool string) statsReport {
	t.Helper()
	var out bytes.Buffer
	if err := runStats(context.Background(), fake, statsOptions{Since: 24 * time.Hour, JSON: true}, relNow, spool, &out); err != nil {
		t.Fatal(err)
	}
	var rep statsReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out.String())
	}
	return rep
}

func nsBlock(rep statsReport, ns string) (statsBlock, bool) {
	for _, b := range rep.Namespaces {
		if b.Namespace == ns {
			return b, true
		}
	}
	return statsBlock{}, false
}

// Spool events are added unless the table has them; out-of-window events and
// duplicates are ignored; a spool-only namespace gets its own block.
func TestRunStatsReliabilityFromSpool(t *testing.T) {
	spool := t.TempDir()
	h := func(m int) time.Time { return relNow.Add(-time.Duration(m) * time.Minute) }
	inDB := relEv("acme", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", "s1", h(5))
	fresh1 := relEv("acme", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeDegraded, "", "s1", h(4))
	fresh2 := relEv("acme", memory.EventStoreAttempted, memory.ViaMCP, memory.OutcomeError, memory.ErrClassDBUnavailable, "s1", h(3))
	hook := relEv("only-spool", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeError, memory.ErrClassDBUnavailable, "", h(2))
	old := relEv("acme", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", "s0", relNow.Add(-48*time.Hour))
	writeSpool(t, spool, inDB, fresh1, fresh2, hook, old, fresh1) // fresh1 twice

	dbCounts := func() memory.EventCounts {
		c := emptyCounts()
		c.Reliability.Search["mcp"] = map[string]int{"ok": 1}
		return c
	}
	fake := &fakeStats{
		nss:  []string{"acme"},
		byNS: map[string]memory.EventCounts{"": dbCounts(), "acme": dbCounts()},
		inDB: map[string]bool{inDB.ID: true},
	}
	rep := runStatsJSON(t, fake, spool)

	tot := rep.Total.Reliability
	if rep.ReliabilityFromSpool != 3 || tot.Search["mcp"]["ok"] != 1 || tot.Search["mcp"]["degraded"] != 1 ||
		tot.Search["hook"]["error"] != 1 || tot.Store["error"] != 1 || tot.Failures["db_unavailable"] != 2 {
		t.Errorf("reliability_from_spool=%d total=%+v", rep.ReliabilityFromSpool, tot)
	}
	if tot.FirstDown == nil || tot.LastDown == nil || !tot.FirstDown.Equal(h(3)) || !tot.LastDown.Equal(h(2)) {
		t.Errorf("down window = %v..%v", tot.FirstDown, tot.LastDown)
	}
	if _, ok := nsBlock(rep, "only-spool"); !ok || len(rep.Namespaces) != 2 {
		t.Fatalf("namespaces = %+v", rep.Namespaces)
	}
	only, _ := nsBlock(rep, "only-spool")
	if only.Reliability.Search["hook"]["error"] != 1 || only.FailureRateHookSearch.Num != 1 || only.FailureRateHookSearch.Den != 1 {
		t.Errorf("spool-only block = %+v", only.Reliability)
	}
	if only.Feedback == nil || only.Inventory == nil {
		t.Error("spool-only block has nil maps (JSON null)")
	}
	// The spool is untouched.
	if n, _, _ := eventspool.Pending(spool); n != 6 {
		t.Errorf("spool lines = %d, want 6", n)
	}
}

// DB-down hours merge as a set: a spool event in an hour the table already
// counts adds nothing; another hour adds one.
func TestRunStatsDownHoursMergeAsSet(t *testing.T) {
	spool := t.TempDir()
	sameHour := relNow.Add(-20 * time.Minute)
	earlier := relNow.Add(-150 * time.Minute) // two hours before the other
	writeSpool(t, spool,
		relEv("acme", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeError, memory.ErrClassDBUnavailable, "", sameHour),
		relEv("acme", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeError, memory.ErrClassDBUnavailable, "", earlier))
	c := emptyCounts()
	first, last := sameHour.Add(-5*time.Minute), sameHour.Add(5*time.Minute)
	c.Reliability.DownHours = []time.Time{sameHour.Truncate(time.Hour)}
	c.Reliability.DBDownHours, c.Reliability.FirstDown, c.Reliability.LastDown = 1, &first, &last
	rep := runStatsJSON(t, &fakeStats{byNS: map[string]memory.EventCounts{"": c}}, spool)
	if got := rep.Total.Reliability; got.DBDownHours != 2 || !got.FirstDown.Equal(earlier) || !got.LastDown.Equal(last) {
		t.Errorf("down hours = %d %v..%v", got.DBDownHours, got.FirstDown, got.LastDown)
	}
}

// The latest serve session sums its table rows with its spool-only rows, and
// a newer session that only exists in the spool wins.
func TestRunStatsLatestServeSession(t *testing.T) {
	spool := t.TempDir()
	h := func(m int) time.Time { return relNow.Add(-time.Duration(m) * time.Minute) }
	first, last := h(60), h(30)
	fake := &fakeStats{
		sessID: "aaaaaaaa-1111-4111-8111-111111111111", sessAt: last,
		sessions: map[string]memory.SessionCounts{"aaaaaaaa-1111-4111-8111-111111111111": {First: &first, Last: &last, Searches: 2, Stores: 1, Failures: 1}},
	}
	writeSpool(t, spool,
		relEv("acme", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", "aaaaaaaa-1111-4111-8111-111111111111", h(10)),
		relEv("acme", memory.EventStoreAttempted, memory.ViaMCP, memory.OutcomeError, memory.ErrClassInternal, "aaaaaaaa-1111-4111-8111-111111111111", h(9)),
		relEv("acme", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeOK, "", "hook-sess", h(1)))
	rep := runStatsJSON(t, fake, spool)
	s := rep.LatestServeSession
	if s == nil || s.Searches != 3 || s.Stores != 2 || s.Failures != 2 || !s.First.Equal(first) || !s.Last.Equal(h(9)) {
		t.Fatalf("session = %+v", s)
	}

	// A newer session present only in the spool replaces it.
	writeSpool(t, spool, relEv("acme", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", "bbbbbbbb-2222-4222-8222-222222222222", h(2)))
	s = runStatsJSON(t, fake, spool).LatestServeSession
	if s == nil || s.ID != "bbbbbbbb-2222-4222-8222-222222222222" || s.Searches != 1 || s.Stores != 0 {
		t.Errorf("session = %+v", s)
	}

	// None at all: null in JSON, n/a in text.
	if got := runStatsJSON(t, &fakeStats{}, t.TempDir()).LatestServeSession; got != nil {
		t.Errorf("session = %+v, want null", got)
	}
}

func TestRunStatsReliabilityText(t *testing.T) {
	spool := t.TempDir()
	h := func(m int) time.Time { return relNow.Add(-time.Duration(m) * time.Minute) }
	writeSpool(t, spool,
		relEv("acme", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", "s1111111-1111-4111-8111-111111111111", h(5)),
		relEv("acme", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeError, memory.ErrClassTimeout, "s1111111-1111-4111-8111-111111111111", h(4)),
		relEv("acme", memory.EventStoreAttempted, memory.ViaMCP, memory.OutcomeAdded, "", "s1111111-1111-4111-8111-111111111111", h(3)),
		relEv("acme", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeError, memory.ErrClassDBUnavailable, "", h(2)))
	const layout = "2006-01-02 15:04"
	var out bytes.Buffer
	if err := runStats(context.Background(), &fakeStats{}, statsOptions{Since: 24 * time.Hour}, relNow, spool, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"searches (mcp):      ok=1 degraded=0 error=1",
		"searches (hook):     ok=0 degraded=0 error=1",
		"store attempts:      added=1 updated=0 superseded=0 noop=0 needs_judgment=0 error=0",
		"failures:            db_unavailable=1 embedding_unavailable=0 invalid_request=0 timeout=1 internal=0",
		"failure rate:        mcp search 50.0% (1/2), hook search 100.0% (1/1), store 0.0% (0/1)",
		"db-down hours:       1 (" + h(2).Format(layout) + " - " + h(2).Format(layout) + " UTC)",
		"latest serve session: s1111111  " + h(5).Format(layout) + " - " + h(3).Format(layout) + " UTC  searches=2 store-attempts=1 failures=1",
		"4 reliability events counted from the spool",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "spool is full") {
		t.Error("spool reported full")
	}
}

func TestRunStatsEmptyReliabilityIsNA(t *testing.T) {
	var out bytes.Buffer
	if err := runStats(context.Background(), &fakeStats{}, statsOptions{Since: time.Hour}, relNow, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"failure rate:        mcp search n/a, hook search n/a, store n/a",
		"db-down hours:       0 (n/a)", "latest serve session: n/a", "searches (mcp):      ok=0 degraded=0 error=0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunStatsSpoolFull(t *testing.T) {
	spool := t.TempDir()
	f, err := os.Create(filepath.Join(spool, "spool.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(10<<20 + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var out bytes.Buffer
	if err := runStats(context.Background(), &fakeStats{}, statsOptions{Since: time.Hour}, relNow, spool, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "spool is full: new events are being dropped and not counted") {
		t.Errorf("no warning:\n%s", out.String())
	}
	if rep := runStatsJSON(t, &fakeStats{}, spool); !rep.SpoolFull {
		t.Error("spool_full false in JSON")
	}
}

// The SQL namespace order is kept; spool-only namespaces follow it, sorted.
func TestRunStats_SpoolOnlyNamespacesAppendedAfterSQLOrder(t *testing.T) {
	spool := t.TempDir()
	writeSpool(t, spool,
		relEv("spool-b", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", "s1", relNow.Add(-time.Minute)),
		relEv("spool-a", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", "s1", relNow.Add(-2*time.Minute)),
	)
	fake := &fakeStats{nss: []string{"zeta", "alpha"}, byNS: map[string]memory.EventCounts{}}
	rep := runStatsJSON(t, fake, spool)
	var got []string
	for _, b := range rep.Namespaces {
		got = append(got, b.Namespace)
	}
	if want := "zeta,alpha,spool-a,spool-b"; strings.Join(got, ",") != want {
		t.Errorf("namespaces = %v, want %s", got, want)
	}
}
