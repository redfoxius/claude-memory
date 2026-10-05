//go:build integration
// +build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redfoxius/claude-memory/internal/memory"
)

// A pre-namespace database (only 0001 applied) with a legacy row: the
// migrations backfill it to 'work', re-running them changes nothing, and
// afterwards an INSERT without a namespace is refused (the default is dropped).
func TestMigration0002_BackfillAndIdempotent(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := Open(ctx, dsn) // no migrations
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, stmt := range strings.Split(migrationInitSQL, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := store.pool.Exec(ctx, stmt); err != nil && !alreadyExists(err) {
			t.Fatalf("apply 0001: %v", err)
		}
	}

	const legacyID = "11111111-1111-1111-1111-111111111111"
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO records (id, kind, title, content, repo, status, source, confidence, created_at, updated_at)
		VALUES ($1, 'gotcha', 'legacy', 'legacy content', '*', 'active', 'inline', 0.8, NOW(), NOW())`, legacyID); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	namespaceOf := func() string {
		var ns string
		if err := store.pool.QueryRow(ctx, `SELECT namespace FROM records WHERE id = $1`, legacyID).Scan(&ns); err != nil {
			t.Fatalf("read namespace: %v", err)
		}
		return ns
	}

	for run := 1; run <= 2; run++ {
		if err := store.runMigrations(ctx); err != nil {
			t.Fatalf("migrations run %d: %v", run, err)
		}
		if got := namespaceOf(); got != "work" {
			t.Errorf("after run %d legacy namespace = %q, want work", run, got)
		}
	}

	// A row moved elsewhere must survive a re-run (the backfill is not repeated).
	if _, err := store.pool.Exec(ctx, `UPDATE records SET namespace = 'global' WHERE id = $1`, legacyID); err != nil {
		t.Fatal(err)
	}
	if err := store.runMigrations(ctx); err != nil {
		t.Fatalf("migrations re-run: %v", err)
	}
	if got := namespaceOf(); got != "global" {
		t.Errorf("re-run changed namespace to %q, want global", got)
	}

	_, err = store.pool.Exec(ctx, `
		INSERT INTO records (id, kind, title, content, repo, status, source, confidence, created_at, updated_at)
		VALUES ('22222222-2222-2222-2222-222222222222', 'gotcha', 'bare', 'c', '*', 'active', 'inline', 0.8, NOW(), NOW())`)
	if err == nil {
		t.Error("INSERT without namespace succeeded; the column default must be dropped")
	}
}

// The write-path advisory lock is keyed per namespace: the same repo and
// title hash in two namespaces do not block each other, but do inside one.
func TestAdvisoryLock_PerNamespace(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const repo, hash = "r", "same-title-hash"
	holding := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- store.WithTx(ctx, func(tx memory.TxStore) error {
			if err := tx.AcquireLock(ctx, "ns-a", repo, hash); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding

	lockIn := func(ns string) chan error {
		ch := make(chan error, 1)
		go func() {
			ch <- store.WithTx(ctx, func(tx memory.TxStore) error {
				return tx.AcquireLock(ctx, ns, repo, hash)
			})
		}()
		return ch
	}

	other := lockIn("ns-b")
	select {
	case err := <-other:
		if err != nil {
			t.Fatalf("ns-b lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ns-b lock blocked behind ns-a")
	}

	same := lockIn("ns-a")
	select {
	case err := <-same:
		t.Fatalf("second ns-a lock did not block (err=%v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-same:
		if err != nil {
			t.Fatalf("ns-a lock after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ns-a lock never acquired after release")
	}
}
