package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"claude-memory/internal/eventspool"
	"claude-memory/internal/memory"
)

// spoolDir is where the hook appends its events and the drains read them:
// ~/.local/state/claude-memory/events.
func spoolDir() string { return filepath.Join(stateDir(), "events") }

// drainSpool moves the hook's spooled events into sink, best-effort: any
// failure is logged at Debug and the next drain retries.
func drainSpool(ctx context.Context, sink memory.EventSink, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := eventspool.Drain(ctx, spoolDir(), sink)
	if err != nil {
		slog.Debug("event spool drain failed", "error", err)
		return
	}
	if res.Inserted+res.Skipped+res.Rejected > 0 {
		slog.Debug("event spool drained", "inserted", res.Inserted, "skipped", res.Skipped, "rejected", res.Rejected)
	}
}
