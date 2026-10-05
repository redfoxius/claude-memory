//go:build integration
// +build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"claude-memory/internal/memory"
)

func relEvent(ns string, typ memory.EventType, via memory.EventVia, outcome memory.FeedbackOutcome, class memory.ErrorClass, session string, at time.Time) memory.Event {
	e := memory.NewEvent(at, ns, typ)
	e.Via, e.Outcome, e.ErrorClass, e.SessionID = via, outcome, class, session
	return e
}

func constraintNames(t *testing.T, ctx context.Context, s *Store) []string {
	t.Helper()
	rows, err := s.pool.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid = 'events'::regclass
		AND conname ~ '^events_(type|outcome|via|error_class)_check' ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

var want0005 = []string{"events_error_class_check", "events_outcome_check_v2", "events_type_check_v2", "events_via_check_v2"}

// Migration 0005 runs at every start: applying it again changes nothing and a
// database that stopped at 0004 upgrades in place.
func TestMigration0005IdempotentAndUpgrade(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := constraintNames(t, ctx, s); !slices.Equal(got, want0005) {
		t.Fatalf("constraints = %v, want %v", got, want0005)
	}
	for i := 0; i < 2; i++ {
		if err := s.runMigrations(ctx); err != nil {
			t.Fatalf("re-run %d: %v", i+1, err)
		}
		if got := constraintNames(t, ctx, s); !slices.Equal(got, want0005) {
			t.Fatalf("after re-run %d constraints = %v", i+1, got)
		}
	}
	if n := countRows(t, ctx, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'events' AND column_name = 'error_class'`); n != 1 {
		t.Errorf("error_class columns = %d", n)
	}

	// Simulate the 0004 state: old constraints, no column.
	for _, q := range []string{
		`ALTER TABLE events DROP CONSTRAINT events_type_check_v2`,
		`ALTER TABLE events DROP CONSTRAINT events_outcome_check_v2`,
		`ALTER TABLE events DROP CONSTRAINT events_via_check_v2`,
		`ALTER TABLE events DROP COLUMN error_class`,
		`ALTER TABLE events ADD CONSTRAINT events_type_check CHECK (type IN ('card_injected', 'feedback', 'record_created', 'record_updated', 'record_superseded', 'record_deprecated', 'record_promoted', 'record_deleted'))`,
		`ALTER TABLE events ADD CONSTRAINT events_outcome_check CHECK (outcome IN ('useful', 'outdated', 'wrong'))`,
		`ALTER TABLE events ADD CONSTRAINT events_via_check CHECK (via IN ('tool', 'feedback', 'seen', 'ttl'))`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.runMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if got := constraintNames(t, ctx, s); !slices.Equal(got, want0005) {
		t.Errorf("after upgrade constraints = %v, want %v", got, want0005)
	}
	if err := s.Append(ctx, relEvent("work", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeError, memory.ErrClassTimeout, "", time.Now())); err != nil {
		t.Errorf("append after upgrade: %v", err)
	}
}

// Two processes starting at once on a fresh database both succeed.
func TestMigration0005ConcurrentNew(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	var wg sync.WaitGroup
	stores := make([]*Store, 2)
	errs := make([]error, 2)
	for i := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stores[i], errs[i] = New(ctx, dsn)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("New #%d: %v", i, err)
		}
		defer stores[i].Close()
	}
	if got := constraintNames(t, ctx, stores[0]); !slices.Equal(got, want0005) {
		t.Errorf("constraints = %v", got)
	}
}

// Every new enum value inserts; a bad class, outcome or via is rejected as a
// constraint error; Append always writes error_class (NULL when empty).
func TestReliabilityAppendValues(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()

	var evs []memory.Event
	for _, o := range []memory.FeedbackOutcome{memory.OutcomeOK, memory.OutcomeDegraded} {
		evs = append(evs, relEvent("work", memory.EventSearchCalled, memory.ViaMCP, o, "", "sess", now))
	}
	for _, o := range []memory.FeedbackOutcome{memory.OutcomeAdded, memory.OutcomeUpdated, memory.OutcomeSuperseded, memory.OutcomeNoop, memory.OutcomeNeedsJudgment} {
		evs = append(evs, relEvent("work", memory.EventStoreAttempted, memory.ViaMCP, o, "", "sess", now))
	}
	for _, c := range []memory.ErrorClass{memory.ErrClassDBUnavailable, memory.ErrClassEmbeddingUnavailable, memory.ErrClassInvalidRequest, memory.ErrClassTimeout, memory.ErrClassInternal} {
		evs = append(evs, relEvent("work", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeError, c, "", now))
	}
	if err := s.Append(ctx, evs...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if n := countRows(t, ctx, s, `SELECT count(*) FROM events WHERE error_class IS NULL`); n != 7 {
		t.Errorf("rows without class = %d, want 7", n)
	}
	// An old-type event still inserts with a NULL class.
	if err := s.Append(ctx, ev("work", memory.EventCardInjected, uuid.New().String(), time.Minute)); err != nil {
		t.Errorf("old event: %v", err)
	}

	bad := []memory.Event{
		relEvent("work", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeError, "bogus", "", now),
		relEvent("work", memory.EventSearchCalled, memory.ViaHook, "weird", "", "", now),
		relEvent("work", memory.EventSearchCalled, "weird", memory.OutcomeOK, "", "", now),
	}
	for i, e := range bad {
		if err := s.Append(ctx, e); !errors.Is(err, memory.ErrEventRejected) {
			t.Errorf("bad #%d: err = %v, want ErrEventRejected", i, err)
		}
	}
}

// StatsCounts reliability queries, window and namespace filters, DB-down
// hours, session counts, the latest session and the id lookup.
func TestReliabilityStats(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hour := time.Now().UTC().Truncate(time.Hour)
	at := func(m int) time.Time { return hour.Add(-time.Duration(m) * time.Minute) }
	sA, sB := uuid.New().String(), uuid.New().String()
	evs := []memory.Event{
		relEvent("work", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", sA, at(200)),
		relEvent("work", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeDegraded, "", sA, at(190)),
		relEvent("work", memory.EventStoreAttempted, memory.ViaMCP, memory.OutcomeAdded, "", sA, at(180)),
		relEvent("work", memory.EventStoreAttempted, memory.ViaMCP, memory.OutcomeError, memory.ErrClassEmbeddingUnavailable, sA, at(170)),
		relEvent("other", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeError, memory.ErrClassDBUnavailable, sB, at(30)),
		relEvent("work", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeError, memory.ErrClassDBUnavailable, "", at(20)),
		relEvent("work", memory.EventSearchCalled, memory.ViaHook, memory.OutcomeOK, "", "", at(10)),
		// Outside the window below.
		relEvent("work", memory.EventSearchCalled, memory.ViaMCP, memory.OutcomeOK, "", "old", at(60*30)),
	}
	if err := s.Append(ctx, evs...); err != nil {
		t.Fatal(err)
	}
	since := at(60 * 24)

	all, err := s.StatsCounts(ctx, since, "")
	if err != nil {
		t.Fatal(err)
	}
	r := all.Reliability
	if r.Search["mcp"]["ok"] != 1 || r.Search["mcp"]["degraded"] != 1 || r.Search["mcp"]["error"] != 1 ||
		r.Search["hook"]["ok"] != 1 || r.Search["hook"]["error"] != 1 || r.Store["added"] != 1 || r.Store["error"] != 1 {
		t.Errorf("counts = %+v", r)
	}
	if r.Failures["db_unavailable"] != 2 || r.Failures["embedding_unavailable"] != 1 || len(r.Failures) != 2 {
		t.Errorf("failures = %v", r.Failures)
	}
	// at(30) and at(20) are in the previous hour: one down hour.
	if r.DBDownHours != 1 || r.FirstDown == nil || !r.FirstDown.Equal(at(30)) || !r.LastDown.Equal(at(20)) {
		t.Errorf("down = %d %v..%v", r.DBDownHours, r.FirstDown, r.LastDown)
	}

	// Namespace filter and a window that drops the early events.
	other, _ := s.StatsCounts(ctx, since, "other")
	if other.Reliability.Search["mcp"]["error"] != 1 || len(other.Reliability.Search["hook"]) != 0 {
		t.Errorf("other = %+v", other.Reliability)
	}
	late, _ := s.StatsCounts(ctx, at(100), "")
	if late.Reliability.Search["mcp"]["ok"] != 0 || late.Reliability.Store["added"] != 0 {
		t.Errorf("late = %+v", late.Reliability)
	}

	// Two down hours.
	if err := s.Append(ctx, relEvent("work", memory.EventStoreAttempted, memory.ViaMCP, memory.OutcomeError, memory.ErrClassDBUnavailable, sA, at(150))); err != nil {
		t.Fatal(err)
	}
	all, _ = s.StatsCounts(ctx, since, "")
	if all.Reliability.DBDownHours != 2 {
		t.Errorf("down hours = %d, want 2", all.Reliability.DBDownHours)
	}

	// Sessions: newest mcp event is session B (hook events are ignored).
	id, when, err := s.StatsLatestServeSession(ctx, since)
	if err != nil || id != sB || !when.Equal(at(30)) {
		t.Errorf("latest session = %q %v %v", id, when, err)
	}
	sc, err := s.StatsSessionCounts(ctx, since, sA)
	if err != nil || sc.Searches != 2 || sc.Stores != 3 || sc.Failures != 2 || sc.First == nil || !sc.First.Equal(at(200)) || !sc.Last.Equal(at(150)) {
		t.Errorf("session A = %+v %v", sc, err)
	}
	none, err := s.StatsSessionCounts(ctx, since, uuid.New().String())
	if err != nil || none.Searches != 0 || none.First != nil {
		t.Errorf("empty session = %+v %v", none, err)
	}
	if id, _, _ := s.StatsLatestServeSession(ctx, hour.Add(time.Hour)); id != "" {
		t.Errorf("latest session in an empty window = %q", id)
	}

	// Id lookup: ANY($1::uuid[]) with strings.
	missing := uuid.New().String()
	found, err := s.EventIDsExist(ctx, []string{evs[0].ID, evs[1].ID, missing})
	if err != nil || len(found) != 2 || !found[evs[0].ID] || found[missing] {
		t.Errorf("found = %v %v", found, err)
	}
	if found, err := s.EventIDsExist(ctx, nil); err != nil || len(found) != 0 {
		t.Errorf("empty lookup = %v %v", found, err)
	}
}
