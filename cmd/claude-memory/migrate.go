package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"claude-memory/internal/config"
	"claude-memory/internal/postgres"
	"claude-memory/internal/setup"
)

// migrationIDs are the embedded migrations postgres.New applies, in order
// (internal/postgres/migrations). migrate_test.go keeps the list in sync.
var migrationIDs = []string{"0001", "0002"}

// cmdMigrate implements the "migrate" subcommand (AC-3): it runs after the
// config is loaded (it needs the DSN and the env file's 0600 check), opens
// the store with postgres.New, which applies the idempotent migrations, and
// closes it. The hook skips migrations, so this is how a new schema gets
// applied explicitly after an upgrade.
func cmdMigrate(cfg *config.Config, args []string) error {
	if len(args) > 0 {
		return usageError(fmt.Errorf("usage: claude-memory migrate (no arguments)"))
	}
	ctx := context.Background()
	if err := migrateDSN(ctx, cfg.PGDSN); err != nil {
		return err
	}
	fmt.Printf("schema up to date (migrations: %s)\n", strings.Join(migrationIDs, ","))
	return nil
}

// migrateDSN applies the migrations to dsn. The error is redacted: a pgx
// error can echo the DSN, password included (AC-30).
func migrateDSN(ctx context.Context, dsn string) error {
	store, err := postgres.New(ctx, dsn)
	if err != nil {
		return errors.New("migrate: " + redactWithDSN(dsn).RedactError(err))
	}
	store.Close()
	return nil
}

// redactWithDSN returns a Redactor that also masks dsn's password literal.
func redactWithDSN(dsn string) *setup.Redactor {
	r := setup.NewRedactor()
	if pw := dsnPassword(dsn); pw != "" {
		r.Register(pw)
	}
	return r
}

// dsnPassword extracts the password of a URL-form DSN ("" when none).
func dsnPassword(dsn string) string {
	_, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return ""
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return ""
	}
	_, pw, ok := strings.Cut(rest[:at], ":")
	if !ok {
		return ""
	}
	return pw
}
