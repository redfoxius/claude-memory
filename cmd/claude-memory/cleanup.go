package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"claude-memory/internal/config"
)

// ttlDeleter is the subset of internal/postgres.Store's methods cleanupCmd
// depends on, declared here (the consumer) rather than imported from the
// producer (golang-architecture: ports declared by the consumer). The
// composition root (main.go) passes in an already-constructed
// *postgres.Store, which satisfies this interface.
type ttlDeleter interface {
	DeleteCandidatesByTTL(ctx context.Context, ttlDays int) (int, error)
}

// cleanupCmd deletes candidate records that have not been touched for longer
// than the configured TTL (AC-35). Active and deprecated records are never deleted (AC-37).
// It prints the count of deleted candidates.
func cleanupCmd(ctx context.Context, cfg *config.Config, store ttlDeleter) error {
	// Convert TTL duration to days (CandidateTTL is in time.Duration).
	ttlDays := int(cfg.CandidateTTL / (24 * time.Hour))

	slog.InfoContext(ctx, "cleanup started", "ttl_days", ttlDays)

	count, err := store.DeleteCandidatesByTTL(ctx, ttlDays)
	if err != nil {
		return fmt.Errorf("delete candidates by ttl: %w", err)
	}

	fmt.Printf("%d\n", count)
	slog.InfoContext(ctx, "cleanup completed", "deleted_count", count)

	return nil
}
