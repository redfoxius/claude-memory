//go:build integration
// +build integration

package postgres

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// ev builds an event at a fixed offset before now.
func ev(ns string, typ memory.EventType, recID string, ago time.Duration) memory.Event {
	e := memory.NewEvent(time.Now().Add(-ago), ns, typ)
	e.RecordID = recID
	return e
}

func countRows(t *testing.T, ctx context.Context, s *Store, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// AC-13, AC-14: migration 0003 twice changes nothing; exact column set and
// named constraints.
func TestEventsMigrationIdempotentAndShape(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.runMigrations(ctx); err != nil { // second application
		t.Fatalf("second migration run: %v", err)
	}

	rows, err := s.pool.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'events' ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	want := []string{"at", "id", "namespace", "outcome", "record_id", "related_id", "session_id", "similarity",
		"source", "stale", "stale_commits", "status", "type", "via"}
	if !slices.Equal(cols, want) {
		t.Errorf("columns = %v, want exactly %v", cols, want)
	}

	for _, name := range []string{"events_type_check", "events_source_check", "events_status_check", "events_outcome_check", "events_via_check"} {
		if n := countRows(t, ctx, s, `SELECT count(*) FROM pg_constraint WHERE conname = $1`, name); n != 1 {
			t.Errorf("constraint %s present %d times", name, n)
		}
	}
	for _, name := range []string{"idx_events_at", "idx_events_type_at", "idx_events_record"} {
		if n := countRows(t, ctx, s, `SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`, name); n != 1 {
			t.Errorf("index %s present %d times", name, n)
		}
	}
	if !slices.Equal(MigrationIDs(), []string{"0001", "0002", "0003"}) {
		t.Errorf("MigrationIDs = %v", MigrationIDs())
	}
}

// Append is idempotent by id and maps data/constraint errors to ErrEventRejected.
func TestEventsAppend(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a := ev("acme", memory.EventCardInjected, uuid.New().String(), time.Hour)
	sim, stale, commits := 0.8, true, 2
	a.Similarity, a.Stale, a.StaleCommits, a.SessionID = &sim, &stale, &commits, "sess-1"
	b := ev("acme", memory.EventRecordSuperseded, uuid.New().String(), time.Hour)
	b.RelatedID, b.Source = uuid.New().String(), memory.EventSourceInline

	if err := s.Append(ctx); err != nil {
		t.Errorf("empty Append: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Append(ctx, a, b); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	if n := countRows(t, ctx, s, `SELECT count(*) FROM events`); n != 2 {
		t.Errorf("rows = %d, want 2 (idempotent by id)", n)
	}
	var gotSim float32
	var gotStale *bool
	var gotCommits *int16
	var related *string
	if err := s.pool.QueryRow(ctx, `SELECT similarity, stale, stale_commits FROM events WHERE id = $1`, a.ID).Scan(&gotSim, &gotStale, &gotCommits); err != nil {
		t.Fatal(err)
	}
	if gotSim != 0.8 || gotStale == nil || !*gotStale || gotCommits == nil || *gotCommits != 2 {
		t.Errorf("stored card = %v %v %v", gotSim, gotStale, gotCommits)
	}
	if err := s.pool.QueryRow(ctx, `SELECT related_id::text FROM events WHERE id = $1`, b.ID).Scan(&related); err != nil || related == nil || *related != b.RelatedID {
		t.Errorf("related_id = %v %v", related, err)
	}

	bad := ev("acme", memory.EventCardInjected, uuid.New().String(), time.Hour)
	bad.Via = "bogus" // violates events_via_check (23514)
	if err := s.Append(ctx, bad); !errors.Is(err, memory.ErrEventRejected) {
		t.Errorf("bad enum: err = %v, want ErrEventRejected", err)
	}
	badUUID := ev("acme", memory.EventCardInjected, "not-a-uuid", time.Hour) // 22P02
	if err := s.Append(ctx, badUUID); !errors.Is(err, memory.ErrEventRejected) {
		t.Errorf("bad uuid: err = %v, want ErrEventRejected", err)
	}
	// A batch with one bad row inserts none of it; the good row alone works.
	good := ev("acme", memory.EventFeedback, uuid.New().String(), time.Hour)
	if err := s.Append(ctx, good, bad); !errors.Is(err, memory.ErrEventRejected) {
		t.Errorf("mixed batch: err = %v", err)
	}
	if err := s.Append(ctx, good); err != nil {
		t.Errorf("good row alone: %v", err)
	}
}

func createRecord(t *testing.T, ctx context.Context, s *Store, ns string, status record.Status, src record.Source) string {
	t.Helper()
	id := uuid.New().String()
	r := &record.Record{
		Namespace: ns, ID: id, Kind: record.KindPattern, Title: "t-" + id, Content: "c",
		Repo: "repo", Status: status, Source: src, Confidence: 0.5, Embedding: makeTestEmbedding(0.3),
	}
	if _, err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	return id
}

// AC-21: the cleanup CTE writes one record_deleted event per deleted row, and
// the count equals the events written. PruneEvents keeps -10 d, drops -400 d.
func TestCleanupCTEAndPrune(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	old := time.Now().UTC().AddDate(0, 0, -400)
	expired := map[string]string{ // id -> namespace
		createRecord(t, ctx, s, "acme", record.StatusCandidate, record.SourceSession): "acme",
		createRecord(t, ctx, s, "acme", record.StatusCandidate, record.SourceInline):  "acme",
		createRecord(t, ctx, s, "global", record.StatusCandidate, record.SourceInline):   "global",
	}
	activeOld := createRecord(t, ctx, s, "acme", record.StatusActive, record.SourcePR)
	freshCandidate := createRecord(t, ctx, s, "acme", record.StatusCandidate, record.SourceSession)
	for id := range expired {
		if _, err := s.pool.Exec(ctx, "UPDATE records SET created_at = $1, updated_at = $1, last_used_at = $1 WHERE id = $2", old, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, "UPDATE records SET created_at = $1, updated_at = $1, last_used_at = $1 WHERE id = $2", old, activeOld); err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteCandidatesByTTL(ctx, 180)
	if err != nil || n != 3 {
		t.Fatalf("deleted = %d, err = %v", n, err)
	}
	if left := countRows(t, ctx, s, `SELECT count(*) FROM records WHERE id IN ($1, $2)`, activeOld, freshCandidate); left != 2 {
		t.Errorf("active/fresh records deleted (%d of 2 left)", left)
	}
	rows, err := s.pool.Query(ctx, `SELECT record_id::text, namespace, source, via, type FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for rows.Next() {
		var id, ns, src, via, typ string
		if err := rows.Scan(&id, &ns, &src, &via, &typ); err != nil {
			t.Fatal(err)
		}
		if typ != "record_deleted" || src != "cleanup" || via != "ttl" {
			t.Errorf("event = %s %s %s", typ, src, via)
		}
		got[id] = ns
	}
	rows.Close()
	if !reflect.DeepEqual(got, expired) {
		t.Errorf("events = %v, want %v", got, expired)
	}
	if n, err := s.DeleteCandidatesByTTL(ctx, 180); err != nil || n != 0 {
		t.Errorf("second run = %d %v", n, err)
	}

	// Retention prune.
	if err := s.Append(ctx,
		ev("acme", memory.EventCardInjected, uuid.New().String(), 400*24*time.Hour),
		ev("acme", memory.EventCardInjected, uuid.New().String(), 10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	pruned, err := s.PruneEvents(ctx, time.Now().Add(-365*24*time.Hour))
	if err != nil || pruned != 1 {
		t.Fatalf("pruned = %d err = %v", pruned, err)
	}
	if left := countRows(t, ctx, s, `SELECT count(*) FROM events WHERE type = 'card_injected'`); left != 1 {
		t.Errorf("card events left = %d, want the -10 d one", left)
	}
}

// AC-27 on real SQL: every raw count over a seeded event set, including a
// useful 3-4 h after a card (counts in "any", not "2 h"), a feedback before the
// card (counts nowhere), and a candidate promoted later (promotion before the
// creation does not count).
func TestStatsCounts(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	A, B, C, D, E := uuid.New().String(), uuid.New().String(), uuid.New().String(), uuid.New().String(), uuid.New().String()
	h := time.Hour
	tr, fa, two := true, false, 2
	card := func(ns, rec string, ago time.Duration, stale *bool, commits *int) memory.Event {
		e := ev(ns, memory.EventCardInjected, rec, ago)
		sim := 0.9
		e.Similarity, e.Stale, e.StaleCommits = &sim, stale, commits
		return e
	}
	fb := func(rec string, ago time.Duration, o memory.FeedbackOutcome) memory.Event {
		e := ev("ns1", memory.EventFeedback, rec, ago)
		e.Outcome = o
		return e
	}
	created := func(ns, rec string, src memory.EventSource, st record.Status) memory.Event {
		e := ev(ns, memory.EventRecordCreated, rec, 20*h)
		e.Source, e.Status = src, st
		return e
	}
	via := func(ns string, typ memory.EventType, rec string, ago time.Duration, v memory.EventVia) memory.Event {
		e := ev(ns, typ, rec, ago)
		e.Via = v
		return e
	}
	deleted := via("ns2", memory.EventRecordDeleted, uuid.New().String(), 2*h, memory.ViaTTL)
	deleted.Source = memory.EventSourceCleanup
	superseded := ev("ns1", memory.EventRecordSuperseded, D, 3*h)
	superseded.RelatedID = uuid.New().String()

	evs := []memory.Event{
		card("ns1", A, 10*h, &tr, &two), card("ns1", B, 10*h, &fa, nil), card("ns1", C, 10*h, nil, nil),
		card("ns1", A, 9*h, nil, nil), card("ns2", A, 1*h, &fa, nil),
		card("ns1", A, 40*24*h, &tr, nil),                // outside the 30 d window
		fb(A, 9*h+30*time.Minute, memory.FeedbackUseful), // 30 min after a card: within 2 h
		fb(B, 6*h, memory.FeedbackUseful),                // 4 h after: any only
		fb(C, 11*h, memory.FeedbackUseful),               // before its card: nowhere
		fb(C, 5*h, memory.FeedbackOutdated),
		created("ns1", A, memory.EventSourceSession, record.StatusCandidate),
		created("ns1", B, memory.EventSourceInline, record.StatusCandidate),
		created("ns1", D, memory.EventSourcePR, record.StatusActive),
		created("ns2", E, memory.EventSourceSession, record.StatusCandidate),
		via("ns1", memory.EventRecordPromoted, A, 9*h+30*time.Minute, memory.ViaFeedback), // after A's creation
		via("ns1", memory.EventRecordPromoted, B, 30*h, memory.ViaTool),                   // before B's creation
		via("ns1", memory.EventRecordDeprecated, C, 5*h, memory.ViaFeedback),
		via("ns2", memory.EventRecordDeprecated, E, 4*h, memory.ViaTool),
		superseded, deleted,
	}
	if err := s.Append(ctx, evs...); err != nil {
		t.Fatal(err)
	}
	createRecord(t, ctx, s, "ns1", record.StatusActive, record.SourcePR)
	createRecord(t, ctx, s, "ns1", record.StatusCandidate, record.SourceSession)
	createRecord(t, ctx, s, "ns2", record.StatusCandidate, record.SourceInline)

	since := time.Now().Add(-30 * 24 * h)

	nss, err := s.StatsNamespaces(ctx, since)
	if err != nil || !slices.Equal(nss, []string{"ns1", "ns2"}) {
		t.Fatalf("namespaces = %v %v", nss, err)
	}

	total, err := s.StatsCounts(ctx, since, "")
	if err != nil {
		t.Fatal(err)
	}
	wantTotal := memory.EventCounts{
		Cards: 5, CardRecords: 3, CardsChecked: 3, CardsStale: 1,
		UsefulWithin: 1, UsefulAny: 2,
		Feedback:           map[string]int{"useful": 3, "outdated": 1},
		CreatedBySource:    map[string]int{"session": 2, "inline": 1, "pr": 1},
		CandidatesCreated:  3,
		CandidatesPromoted: 1,
		Superseded:         1,
		DeprecatedByVia:    map[string]int{"feedback": 1, "tool": 1},
		TTLDeleted:         1,
		Inventory: map[string]map[string]int{
			"pr": {"active": 1}, "session": {"candidate": 1}, "inline": {"candidate": 1}},
	}
	if !reflect.DeepEqual(total, wantTotal) {
		t.Errorf("total:\n got %+v\nwant %+v", total, wantTotal)
	}

	ns1, err := s.StatsCounts(ctx, since, "ns1")
	if err != nil {
		t.Fatal(err)
	}
	wantNS1 := memory.EventCounts{
		Cards: 4, CardRecords: 3, CardsChecked: 2, CardsStale: 1,
		UsefulWithin: 1, UsefulAny: 2,
		Feedback:           map[string]int{"useful": 3, "outdated": 1},
		CreatedBySource:    map[string]int{"session": 1, "inline": 1, "pr": 1},
		CandidatesCreated:  2,
		CandidatesPromoted: 1,
		Superseded:         1,
		DeprecatedByVia:    map[string]int{"feedback": 1},
		Inventory:          map[string]map[string]int{"pr": {"active": 1}, "session": {"candidate": 1}},
	}
	if !reflect.DeepEqual(ns1, wantNS1) {
		t.Errorf("ns1:\n got %+v\nwant %+v", ns1, wantNS1)
	}

	// A narrow window drops everything older.
	narrow, err := s.StatsCounts(ctx, time.Now().Add(-2*h), "")
	if err != nil {
		t.Fatal(err)
	}
	if narrow.Cards != 1 || narrow.CardRecords != 1 || narrow.UsefulAny != 0 || narrow.CandidatesCreated != 0 {
		t.Errorf("narrow = %+v", narrow)
	}
}

// AC-40: imported candidates are counted apart from the promotion rate. The
// 'import' event source only exists after the import migration, so this test
// lifts the source CHECK to insert such events.
func TestStatsCountsSeparateImports(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.pool.Exec(ctx, `ALTER TABLE events DROP CONSTRAINT IF EXISTS events_source_check`); err != nil {
		t.Fatal(err)
	}

	inl, imp1, imp2 := uuid.New().String(), uuid.New().String(), uuid.New().String()
	created := func(rec string, src memory.EventSource) memory.Event {
		e := ev("ns1", memory.EventRecordCreated, rec, 20*time.Hour)
		e.Source, e.Status = src, record.StatusCandidate
		return e
	}
	promoted := ev("ns1", memory.EventRecordPromoted, imp1, time.Hour)
	promoted.Via = memory.ViaTool
	promoted2 := ev("ns1", memory.EventRecordPromoted, inl, time.Hour)
	promoted2.Via = memory.ViaTool
	if err := s.Append(ctx, created(inl, memory.EventSourceInline), created(imp1, "import"), created(imp2, "import"), promoted, promoted2); err != nil {
		t.Fatal(err)
	}

	c, err := s.StatsCounts(ctx, time.Now().Add(-30*24*time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.CandidatesCreated != 1 || c.CandidatesPromoted != 1 || c.ImportCreated != 2 || c.ImportPromoted != 1 {
		t.Errorf("counts = created %d promoted %d, import created %d promoted %d; want 1 1 2 1",
			c.CandidatesCreated, c.CandidatesPromoted, c.ImportCreated, c.ImportPromoted)
	}
}
