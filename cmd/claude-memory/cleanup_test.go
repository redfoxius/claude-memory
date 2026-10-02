package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-memory/internal/config"
	"claude-memory/internal/eventspool"
	"claude-memory/internal/memory"
)

type fakeCleanupStore struct {
	ttl       int
	deleted   int
	pruned    int
	pruneArg  time.Time
	appended  []memory.Event
	appendErr error
	order     []string
}

func (f *fakeCleanupStore) Append(_ context.Context, evs ...memory.Event) error {
	f.order = append(f.order, "drain")
	f.appended = append(f.appended, evs...)
	return f.appendErr
}
func (f *fakeCleanupStore) DeleteCandidatesByTTL(_ context.Context, d int) (int, error) {
	f.order = append(f.order, "ttl")
	f.ttl = d
	return f.deleted, nil
}
func (f *fakeCleanupStore) PruneEvents(_ context.Context, before time.Time) (int, error) {
	f.order = append(f.order, "prune")
	f.pruneArg = before
	return f.pruned, nil
}

func touch(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// AC-29: drain first, TTL delete, 365 d prune, cache sweep, three output lines.
func TestCleanupCmd(t *testing.T) {
	state := t.TempDir()
	old := filepath.Join(state, "stale-cache", "old.json")
	fresh := filepath.Join(state, "stale-cache", "fresh.json")
	other := filepath.Join(state, "stale-cache", "keep.txt")
	touch(t, old, 8*24*time.Hour)
	touch(t, fresh, 24*time.Hour)
	touch(t, other, 30*24*time.Hour)

	// A settled spool of one valid event and a stuck draining file's leftovers.
	spool := filepath.Join(state, "events")
	if err := (eventspool.Sink{Dir: spool}).Append(context.Background(), validEvent()); err != nil {
		t.Fatal(err)
	}
	if _, err := eventspool.Drain(context.Background(), spool, &fakeCleanupStore{}); err != nil { // renames the live file
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(spool, "spool.*.draining"))
	mt := time.Now().Add(-time.Minute)
	_ = os.Chtimes(files[0], mt, mt)
	touch(t, filepath.Join(spool, "spool.1.2.failed"), time.Hour)

	store := &fakeCleanupStore{deleted: 3, pruned: 7}
	now := time.Now()
	var out bytes.Buffer
	cfg := &config.Config{CandidateTTL: 180 * 24 * time.Hour}
	if err := cleanupCmd(context.Background(), cfg, store, state, now, &out); err != nil {
		t.Fatal(err)
	}

	if store.ttl != 180 || !store.pruneArg.Equal(now.Add(-eventsRetention)) {
		t.Errorf("ttl = %d prune before = %v", store.ttl, store.pruneArg)
	}
	if strings.Join(store.order, ",") != "drain,ttl,prune" || len(store.appended) != 1 {
		t.Errorf("order = %v appended = %d", store.order, len(store.appended))
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old cache file not swept")
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should be kept", p)
		}
	}
	// The drained file is gone, the .failed one is reported on the third line.
	if got, want := out.String(), "3\n7\nspool: 0 draining, 1 failed files left\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestCleanupCmdNoLeftoversTwoLines(t *testing.T) {
	var out bytes.Buffer
	cfg := &config.Config{CandidateTTL: 24 * time.Hour}
	if err := cleanupCmd(context.Background(), cfg, &fakeCleanupStore{}, t.TempDir(), time.Now(), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "0\n0\n" {
		t.Errorf("output = %q", out.String())
	}
}

// A drain failure (transient) is logged, never fails cleanup; the leftover
// draining file shows on the third line.
func TestCleanupCmdDrainFailureIsNotFatal(t *testing.T) {
	state := t.TempDir()
	spool := filepath.Join(state, "events")
	if err := (eventspool.Sink{Dir: spool}).Append(context.Background(), validEvent()); err != nil {
		t.Fatal(err)
	}
	if _, err := eventspool.Drain(context.Background(), spool, &fakeCleanupStore{}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(spool, "spool.*.draining"))
	mt := time.Now().Add(-time.Minute)
	_ = os.Chtimes(files[0], mt, mt)

	var out bytes.Buffer
	store := &fakeCleanupStore{appendErr: os.ErrDeadlineExceeded}
	if err := cleanupCmd(context.Background(), &config.Config{CandidateTTL: 24 * time.Hour}, store, state, time.Now(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "spool: 1 draining, 0 failed files left\n") {
		t.Errorf("output = %q", out.String())
	}
}

func validEvent() memory.Event {
	e := memory.NewEvent(time.Now(), "acme", memory.EventCardInjected)
	e.RecordID = "11111111-1111-4111-8111-111111111111"
	return e
}
