package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/redfoxius/claude-memory/internal/config"
	"github.com/redfoxius/claude-memory/internal/postgres"
)

// cmdMigrate implements the "migrate" subcommand (AC-3): it runs after the
// config is loaded (it needs the DSN and the env file's 0600 check), applies the idempotent migrations, and
// closes it. The hook skips migrations, so this is how a new schema gets
// applied explicitly after an upgrade.
func cmdMigrate(cfg *config.Config, args []string) error {
	if len(args) > 0 {
		return usageError(fmt.Errorf("usage: claude-memory migrate (no arguments)"))
	}
	ctx := context.Background()
	// Prober.Migrate is the one sanitizing path: it withholds pgx's parse
	// detail when the DSN has a password (net/url can quote a fragment of it).
	if err := (postgres.Prober{}).Migrate(ctx, cfg.PGDSN); err != nil {
		return err
	}
	fmt.Printf("schema up to date (migrations: %s)\n", strings.Join(postgres.MigrationIDs(), ","))
	return nil
}
