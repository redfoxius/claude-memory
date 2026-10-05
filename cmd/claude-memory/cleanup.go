package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/eventspool"
	"github.com/redfoxius/claude-memory/internal/memory"
)

const (
	// eventsRetention is how long events are kept (constant, no env var).
	eventsRetention = 365 * 24 * time.Hour
	// staleCacheMaxAge is the age past which a per-session verdict cache file
	// is removed: the hook creates one per Claude Code session and nothing
	// else deletes them.
	staleCacheMaxAge = 7 * 24 * time.Hour
)

// cleanupStore is the subset of internal/postgres.Store's methods cleanupCmd
// depends on, declared here (the consumer) rather than imported from the
// producer (golang-architecture: ports declared by the consumer). The
// composition root (main.go) passes in an already-constructed
// *postgres.Store, which satisfies this interface.
type cleanupStore interface {
	memory.EventSink // target of the spool drain
	DeleteCandidatesByTTL(ctx context.Context, ttlDays int) (int, error)
	PruneEvents(ctx context.Context, before time.Time) (int, error)
}

// cleanupCmd drains the hook's event spool, deletes candidate records that
// have not been touched for longer than the configured TTL (AC-35; the store
// writes a record_deleted event per row), prunes events older than
// eventsRetention, and removes old stale-cache files under stateDir. Active
// and deprecated records are never deleted (AC-37). It prints the count of
// deleted candidates, the count of pruned events and, when any exist, the
// spool files left behind.
func cleanupCmd(ctx context.Context, cfg *config.Config, store cleanupStore, stateDir string, now time.Time, out io.Writer) error {
	// Convert TTL duration to days (CandidateTTL is in time.Duration).
	ttlDays := int(cfg.CandidateTTL / (24 * time.Hour))

	slog.InfoContext(ctx, "cleanup started", "ttl_days", ttlDays)

	spool := filepath.Join(stateDir, "events")
	drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := eventspool.Drain(drainCtx, spool, store); err != nil {
		slog.WarnContext(ctx, "cleanup: event spool drain failed", "error", err)
	}

	count, err := store.DeleteCandidatesByTTL(ctx, ttlDays)
	if err != nil {
		return fmt.Errorf("delete candidates by ttl: %w", err)
	}

	pruned, err := store.PruneEvents(ctx, now.Add(-eventsRetention))
	if err != nil {
		return fmt.Errorf("prune events: %w", err)
	}

	sweepStaleCache(filepath.Join(stateDir, "stale-cache"), now.Add(-staleCacheMaxAge))

	fmt.Fprintf(out, "%d\n%d\n", count, pruned)
	if _, draining, failed := eventspool.Pending(spool); draining+failed > 0 {
		fmt.Fprintf(out, "spool: %d draining, %d failed files left\n", draining, failed)
	}
	slog.InfoContext(ctx, "cleanup completed", "deleted_count", count, "events_pruned", pruned)

	return nil
}

// sweepStaleCache removes *.json files in dir last modified before cutoff.
func sweepStaleCache(dir string, cutoff time.Time) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(f)
		}
	}
}
