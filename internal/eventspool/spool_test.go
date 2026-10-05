package eventspool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"claude-memory/internal/memory"
)

func ev(t memory.EventType) memory.Event {
	e := memory.NewEvent(time.Now(), "work", t)
	e.RecordID = uuid.New().String()
	return e
}

// fakeSink dedups by id like the real table. rejectIDs fail with
// ErrEventRejected (a batch containing one is rejected whole); transient makes
// every Append fail with a non-reject error.
type fakeSink struct {
	mu        sync.Mutex
	rows      map[string]memory.Event
	calls     int
	rejectIDs map[string]bool
	transient error
}

func newSink() *fakeSink { return &fakeSink{rows: map[string]memory.Event{}} }

func (f *fakeSink) Append(_ context.Context, evs ...memory.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.transient != nil {
		return f.transient
	}
	for _, e := range evs {
		if f.rejectIDs[e.ID] {
			return memory.ErrEventRejected
		}
	}
	for _, e := range evs {
		f.rows[e.ID] = e
	}
	return nil
}

// settle backdates every spool.* file so the drain's 2 s rule lets it through.
func settle(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-time.Minute)
	files, _ := filepath.Glob(filepath.Join(dir, "spool.*"))
	for _, f := range files {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// drainSettled renames the live file, backdates it, and drains.
func drainSettled(t *testing.T, dir string, sink memory.EventSink) (DrainResult, error) {
	t.Helper()
	if _, err := Drain(context.Background(), dir, sink); err != nil { // renames; a fresh file is left
		t.Fatal(err)
	}
	settle(t, dir)
	return Drain(context.Background(), dir, sink)
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	a, b := ev(memory.EventCardInjected), ev(memory.EventFeedback)
	sim := 0.8
	a.Similarity = &sim
	if err := (Sink{Dir: dir}).Append(context.Background(), a, b); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "spool.jsonl")); fi.Mode().Perm() != 0o600 {
		t.Errorf("spool mode = %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", fi.Mode().Perm())
	}
	sink := newSink()
	res, err := drainSettled(t, dir, sink)
	if err != nil || res.Inserted != 2 || res.Skipped != 0 || res.Rejected != 0 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if got := sink.rows[a.ID]; got.Similarity == nil || *got.Similarity != 0.8 || got.RecordID != a.RecordID {
		t.Errorf("round-tripped event = %+v", got)
	}
	if left := listDir(t, dir); len(left) != 0 {
		t.Errorf("files left: %v", left)
	}
}

func TestDrainNothingToDo(t *testing.T) {
	res, err := Drain(context.Background(), filepath.Join(t.TempDir(), "none"), newSink())
	if err != nil || res != (DrainResult{}) {
		t.Errorf("res = %+v err = %v", res, err)
	}
}

// A torn last line followed by an append: one skipped, every new line intact.
func TestTornLineThenAppend(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "spool.jsonl")
	if err := os.WriteFile(spool, []byte(`{"id":"torn`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, b := ev(memory.EventCardInjected), ev(memory.EventCardInjected)
	if err := (Sink{Dir: dir}).Append(context.Background(), a, b); err != nil {
		t.Fatal(err)
	}
	sink := newSink()
	res, err := drainSettled(t, dir, sink)
	if err != nil || res.Inserted != 2 || res.Skipped != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestMalformedAndInvalidLinesSkippedNeverSent(t *testing.T) {
	dir := t.TempDir()
	good := ev(memory.EventCardInjected)
	badEnum := ev(memory.EventCardInjected)
	badEnum.Via = "bogus"
	noNS := ev(memory.EventFeedback)
	noNS.Namespace = ""
	s := Sink{Dir: dir}
	for _, e := range []memory.Event{badEnum, noNS, good} {
		if err := s.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := os.OpenFile(filepath.Join(dir, "spool.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("not json\n[]\n")
	f.Close()

	sink := newSink()
	res, err := drainSettled(t, dir, sink)
	if err != nil || res.Inserted != 1 || res.Skipped != 4 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(sink.rows) != 1 || sink.rows[good.ID].ID == "" {
		t.Errorf("sink rows = %v", sink.rows)
	}
}

// A drain interrupted by a transient error keeps the file; re-running inserts
// nothing new (the sink dedups by id) and then removes it.
func TestTransientErrorKeepsFileAndRerunIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	a, b := ev(memory.EventCardInjected), ev(memory.EventCardInjected)
	if err := (Sink{Dir: dir}).Append(context.Background(), a, b); err != nil {
		t.Fatal(err)
	}
	sink := newSink()
	sink.transient = errors.New("connection reset")
	if _, err := drainSettled(t, dir, sink); err == nil {
		t.Fatal("expected the transient error")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.draining")); len(files) != 1 {
		t.Fatalf("draining files = %v", listDir(t, dir))
	}
	sink.transient = nil
	res, err := Drain(context.Background(), dir, sink)
	if err != nil || res.Inserted != 2 || len(sink.rows) != 2 {
		t.Fatalf("res = %+v err = %v rows = %d", res, err, len(sink.rows))
	}
	if left := listDir(t, dir); len(left) != 0 {
		t.Errorf("files left: %v", left)
	}
	// Re-sending the same events (an interrupted pass) adds no rows.
	if err := sink.Append(context.Background(), a, b); err != nil || len(sink.rows) != 2 {
		t.Errorf("not idempotent: %d rows", len(sink.rows))
	}
}

func TestFreshDrainingFileIsLeft(t *testing.T) {
	dir := t.TempDir()
	if err := (Sink{Dir: dir}).Append(context.Background(), ev(memory.EventCardInjected)); err != nil {
		t.Fatal(err)
	}
	sink := newSink()
	res, err := Drain(context.Background(), dir, sink) // renames, mtime is "now": too fresh
	if err != nil || res.Inserted != 0 || sink.calls != 0 {
		t.Fatalf("res = %+v err = %v calls = %d", res, err, sink.calls)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "spool.*.draining")); len(files) != 1 {
		t.Errorf("files = %v", listDir(t, dir))
	}
	lines, draining, failed := Pending(dir)
	if lines != 1 || draining != 1 || failed != 0 {
		t.Errorf("Pending = %d %d %d", lines, draining, failed)
	}
}

// One rejected row of three: 2 inserted, file renamed .failed, 1 rejected.
func TestRejectedRowQuarantinesFile(t *testing.T) {
	dir := t.TempDir()
	a, bad, c := ev(memory.EventCardInjected), ev(memory.EventCardInjected), ev(memory.EventCardInjected)
	if err := (Sink{Dir: dir}).Append(context.Background(), a, bad, c); err != nil {
		t.Fatal(err)
	}
	sink := newSink()
	sink.rejectIDs = map[string]bool{bad.ID: true}
	res, err := drainSettled(t, dir, sink)
	if err != nil || res.Inserted != 2 || res.Rejected != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(sink.rows) != 2 {
		t.Errorf("rows = %d", len(sink.rows))
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.failed")); len(files) != 1 {
		t.Errorf("files = %v", listDir(t, dir))
	}
	// A failed file is never drained again.
	sink.calls = 0
	if res, err := Drain(context.Background(), dir, sink); err != nil || res != (DrainResult{}) || sink.calls != 0 {
		t.Errorf("redrain: %+v %v calls=%d", res, err, sink.calls)
	}
	if lines, draining, failed := Pending(dir); lines != 0 || draining != 0 || failed != 1 {
		t.Errorf("Pending = %d %d %d", lines, draining, failed)
	}
}

// A file that vanishes between listing and reading (a concurrent drain) is ok.
func TestConcurrentDrainsAreHarmless(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := (Sink{Dir: dir}).Append(context.Background(), ev(memory.EventCardInjected)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Drain(context.Background(), dir, newSink()); err != nil {
		t.Fatal(err)
	}
	settle(t, dir)
	sink := newSink()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Drain(context.Background(), dir, sink)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("drain error: %v", err)
		}
	}
	if len(sink.rows) != 5 {
		t.Errorf("rows = %d", len(sink.rows))
	}
}

func TestSizeCap(t *testing.T) {
	dir := t.TempDir()
	spool := filepath.Join(dir, "spool.jsonl")
	big := strings.Repeat("x", maxSpoolBytes+1)
	if err := os.WriteFile(spool, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Sink{Dir: dir}).Append(context.Background(), ev(memory.EventCardInjected)); err == nil {
		t.Error("expected an error over the cap")
	}
	if fi, _ := os.Stat(spool); fi.Size() != int64(len(big)) {
		t.Errorf("spool grew to %d", fi.Size())
	}
}

func TestBatchesOf500(t *testing.T) {
	dir := t.TempDir()
	evs := make([]memory.Event, 1200)
	for i := range evs {
		evs[i] = ev(memory.EventCardInjected)
	}
	if err := (Sink{Dir: dir}).Append(context.Background(), evs...); err != nil {
		t.Fatal(err)
	}
	sink := newSink()
	res, err := drainSettled(t, dir, sink)
	if err != nil || res.Inserted != 1200 || sink.calls != 3 {
		t.Errorf("res = %+v err = %v calls = %d", res, err, sink.calls)
	}
}

// A spool whose last append was long ago but which is renamed just now must
// not be read in the same pass: an appender may still hold the old inode.
func TestJustRenamedOldFileIsNotDrainedInSamePass(t *testing.T) {
	dir := t.TempDir()
	if err := (Sink{Dir: dir}).Append(context.Background(), ev(memory.EventCardInjected)); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "spool.jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	sink := newSink()
	res, err := Drain(context.Background(), dir, sink)
	if err != nil || res.Inserted != 0 || sink.calls != 0 {
		t.Fatalf("res = %+v err = %v calls = %d", res, err, sink.calls)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "spool.*.draining")); len(files) != 1 {
		t.Errorf("files = %v", listDir(t, dir))
	}
}
