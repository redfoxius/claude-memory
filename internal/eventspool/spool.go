// Package eventspool is the hook's event path: an append-only JSON-lines
// file the hook writes in one syscall (no database round trip), and the drain
// that moves its lines into the events table from long-running subcommands.
package eventspool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"claude-memory/internal/memory"
)

const (
	spoolFile = "spool.jsonl"
	// maxSpoolBytes caps the spool: past it the hook appends nothing, so a
	// drain that never runs cannot fill the disk.
	maxSpoolBytes = 10 << 20
	// drainSettle is how old a renamed spool's mtime must be before it is
	// read: an appender that opened the file just before the rename may still
	// be writing into it. There are no locks; this is the whole protocol.
	drainSettle = 2 * time.Second
	batchSize   = 500
)

// Sink appends events to <Dir>/spool.jsonl. It implements memory.EventSink.
type Sink struct {
	Dir string
}

// Append writes all events in a single write on a file opened O_APPEND. The
// buffer starts with "\n" so a torn previous line (crash, full disk) can never
// merge with this one; the drain skips empty lines. Over the size cap it
// appends nothing.
func (s Sink) Append(_ context.Context, evs ...memory.Event) error {
	if len(evs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	buf.WriteByte('\n')
	for _, e := range evs {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}

	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("create spool dir: %w", err)
	}
	path := filepath.Join(s.Dir, spoolFile)
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxSpoolBytes {
		return errors.New("spool is over its size cap; event dropped")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open spool: %w", err)
	}
	_, werr := f.Write(buf.Bytes())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("write spool: %w", werr)
	}
	return nil
}

// DrainResult counts what one Drain pass did.
type DrainResult struct {
	Inserted int // events handed to the sink and accepted
	Skipped  int // malformed or invalid lines, never sent
	Rejected int // events the store rejected (their file is kept as .failed)
}

// Drain moves the spool into sink. The live file is renamed to
// spool.<pid>.<nanos>.draining (nothing to do when it does not exist), then
// every *.draining file whose mtime is at least 2 s old is read: invalid lines
// are skipped, valid ones go to sink in batches of 500 (sink dedups by event
// id, so a repeated drain is harmless). A batch the sink rejects with
// memory.ErrEventRejected is retried row by row. A file with no rejects is
// deleted, one with rejects is renamed to .failed and never drained again, and
// any other sink error keeps the file for the next drain and stops.
func Drain(ctx context.Context, dir string, sink memory.EventSink) (DrainResult, error) {
	var res DrainResult
	live := filepath.Join(dir, spoolFile)
	draining := filepath.Join(dir, fmt.Sprintf("spool.%d.%d.draining", os.Getpid(), time.Now().UnixNano()))
	if err := os.Rename(live, draining); err == nil {
		// Restart the settle clock: the live file's mtime is the last append,
		// possibly long ago, but an appender that opened it just before the
		// rename may still write into this inode.
		now := time.Now()
		_ = os.Chtimes(draining, now, now)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("rename spool: %w", err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "spool.*.draining"))
	if err != nil {
		return res, err
	}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil || time.Since(fi.ModTime()) < drainSettle {
			continue // gone (a concurrent drain) or possibly still being written
		}
		if err := drainFile(ctx, f, sink, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

func drainFile(ctx context.Context, path string, sink memory.EventSink, res *DrainResult) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}

	now := time.Now()
	var valid []memory.Event
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e memory.Event
		if json.Unmarshal(line, &e) != nil || e.Validate(now) != nil {
			res.Skipped++
			continue
		}
		valid = append(valid, e)
	}

	rejected := 0
	for start := 0; start < len(valid); start += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := valid[start:min(start+batchSize, len(valid))]
		err := sink.Append(ctx, batch...)
		if err == nil {
			res.Inserted += len(batch)
			continue
		}
		if !errors.Is(err, memory.ErrEventRejected) {
			return err // transient: keep the file for the next drain
		}
		for _, e := range batch {
			err := sink.Append(ctx, e)
			switch {
			case err == nil:
				res.Inserted++
			case errors.Is(err, memory.ErrEventRejected):
				rejected++
			default:
				return err
			}
		}
	}

	res.Rejected += rejected
	if rejected > 0 {
		failed := strings.TrimSuffix(path, ".draining") + ".failed"
		if err := os.Rename(path, failed); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("quarantine %s: %w", filepath.Base(path), err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
	}
	return nil
}

// Scan reads the valid events still in the spool (the live file and every
// *.draining file) without modifying anything; full reports a live file over
// the size cap. Invalid and torn lines are skipped, as the drain does.
func Scan(dir string) (evs []memory.Event, full bool) {
	live := filepath.Join(dir, spoolFile)
	if fi, err := os.Stat(live); err == nil && fi.Size() > maxSpoolBytes {
		full = true
	}
	files, _ := filepath.Glob(filepath.Join(dir, "spool.*.draining"))
	now := time.Now()
	for _, path := range append([]string{live}, files...) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var e memory.Event
			if json.Unmarshal(line, &e) == nil && e.Validate(now) == nil {
				evs = append(evs, e)
			}
		}
	}
	return evs, full
}

// Pending reports what is still in the spool: events (non-empty lines) in the
// live file and in *.draining files, the number of *.draining files, and the
// number of *.failed files.
func Pending(dir string) (lines, draining, failed int) {
	countLines := func(path string) int {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0
		}
		n := 0
		for _, l := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSpace(l)) > 0 {
				n++
			}
		}
		return n
	}
	lines = countLines(filepath.Join(dir, spoolFile))
	files, _ := filepath.Glob(filepath.Join(dir, "spool.*.draining"))
	draining = len(files)
	for _, f := range files {
		lines += countLines(f)
	}
	fl, _ := filepath.Glob(filepath.Join(dir, "spool.*.failed"))
	failed = len(fl)
	return lines, draining, failed
}
