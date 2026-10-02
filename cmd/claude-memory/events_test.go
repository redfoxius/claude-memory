package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"claude-memory/internal/eventspool"
	"claude-memory/internal/memory"
)

func spoolEvents(t *testing.T, dir string) []memory.Event {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "spool.jsonl"))
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	defer f.Close()
	var out []memory.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e memory.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad spool line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

const evID1, evID2 = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"

func hookEventsSvc(recs []*memory.SearchRecord) *memory.Service {
	store := &fakeHookStore{
		searchFn: func(context.Context, string, []float32, string, memory.SearchOptions) (*memory.SearchResult, error) {
			return &memory.SearchResult{Records: recs}, nil
		},
	}
	return memory.New(store, &fakeHookEmbedder{}, fakeHookScrubber{}, fakeHookClock{}, hookTestCfg())
}

// AC-17, AC-23: 2 cards -> 2 spool lines with ids, similarity, the stale
// tri-state and the session id, and no text.
func TestHookCmd_SpoolsOneEventPerCard(t *testing.T) {
	cfg := hookTestCfg()
	r1 := searchRecord(evID1, "Stale card", "repo", "c", 0.9, 0.5, false)
	r1.Stale, r1.StaleChecked = &memory.StaleHint{Commits: 3}, true
	r2 := searchRecord(evID2, "Fresh card", "repo", "c", 0.7, 0.4, false)
	r2.StaleChecked = true
	r3 := searchRecord("33333333-3333-4333-8333-333333333333", "Unchecked", "repo", "c", 0.6, 0.3, false)
	r4 := searchRecord("44444444-4444-4444-8444-444444444444", "Below", "repo", "c", 0.1, 0.3, false)
	svc := hookEventsSvc([]*memory.SearchRecord{r1, r2, r3, r4})

	dir := filepath.Join(t.TempDir(), "events")
	deps := hookDeps{Events: eventspool.Sink{Dir: dir}}
	stdout, err := runHookCmdDeps(t, context.Background(), cfg, svc, `{"prompt":"p","cwd":"/tmp","session_id":"sess-1"}`, deps)
	if err != nil || stdout == "" {
		t.Fatalf("err = %v stdout = %q", err, stdout)
	}

	evs := spoolEvents(t, dir)
	if len(evs) != 3 {
		t.Fatalf("spooled %d events, want 3 (one per card)", len(evs))
	}
	for _, e := range evs {
		if e.Type != memory.EventCardInjected || e.Namespace != "test-ns" || e.SessionID != "sess-1" || e.Similarity == nil {
			t.Errorf("event = %+v", e)
		}
		if err := e.Validate(time.Now()); err != nil {
			t.Errorf("invalid event: %v", err)
		}
	}
	if e := evs[0]; e.RecordID != evID1 || *e.Similarity != 0.9 || e.Stale == nil || !*e.Stale || e.StaleCommits == nil || *e.StaleCommits != 3 {
		t.Errorf("stale card event = %+v", e)
	}
	if e := evs[1]; e.RecordID != evID2 || e.Stale == nil || *e.Stale || e.StaleCommits != nil {
		t.Errorf("fresh card event = %+v", e)
	}
	if e := evs[2]; e.Stale != nil || e.StaleCommits != nil {
		t.Errorf("unchecked card event = %+v", e)
	}
}

func TestHookCmd_NoCardsNoSpoolAndBadSessionIDDropped(t *testing.T) {
	cfg := hookTestCfg()
	dir := filepath.Join(t.TempDir(), "events")
	deps := hookDeps{Events: eventspool.Sink{Dir: dir}}

	empty := hookEventsSvc(nil)
	if _, err := runHookCmdDeps(t, context.Background(), cfg, empty, `{"prompt":"p","cwd":"/tmp"}`, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("spool dir created without cards: %v", err)
	}

	one := hookEventsSvc([]*memory.SearchRecord{searchRecord(evID1, "t", "repo", "c", 0.9, 0.5, false)})
	if _, err := runHookCmdDeps(t, context.Background(), cfg, one, `{"prompt":"p","cwd":"/tmp","session_id":"../evil"}`, deps); err != nil {
		t.Fatal(err)
	}
	if evs := spoolEvents(t, dir); len(evs) != 1 || evs[0].SessionID != "" {
		t.Errorf("events = %+v", evs)
	}
}

// AC-22/23: an unwritable spool never changes the hook's output.
func TestHookCmd_UnwritableSpoolLeavesOutputUnchanged(t *testing.T) {
	cfg := hookTestCfg()
	recs := []*memory.SearchRecord{searchRecord(evID1, "t", "repo", "c", 0.9, 0.5, false)}

	base := t.TempDir()
	blocker := filepath.Join(base, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	withSpool, err := runHookCmdDeps(t, context.Background(), cfg, hookEventsSvc(recs),
		`{"prompt":"p","cwd":"/tmp"}`, hookDeps{Events: eventspool.Sink{Dir: filepath.Join(blocker, "events")}})
	if err != nil {
		t.Fatal(err)
	}
	without, err := runHookCmd(t, context.Background(), cfg, hookEventsSvc(recs), `{"prompt":"p","cwd":"/tmp"}`)
	if err != nil || withSpool != without || withSpool == "" {
		t.Errorf("outputs differ: %q vs %q (err %v)", withSpool, without, err)
	}
}
