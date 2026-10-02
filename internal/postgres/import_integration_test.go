//go:build integration
// +build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

func eventSourceConstraints(t *testing.T, ctx context.Context, s *Store) []string {
	t.Helper()
	rows, err := s.pool.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid = 'events'::regclass AND conname LIKE 'events_source_check%' ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

// AC-37: migrationSQL runs at every service start, so 0004 applied again must
// change nothing and return no error: the _v2 constraint stays the only
// source CHECK, and the import column and index stay single.
func TestMigration0004Idempotent(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	s, err := New(ctx, dsn) // first apply (0001..0004)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := []string{"events_source_check_v2"}
	if got := eventSourceConstraints(t, ctx, s); !slices.Equal(got, want) {
		t.Fatalf("after first run constraints = %v, want %v", got, want)
	}
	for i := 0; i < 2; i++ {
		if err := s.runMigrations(ctx); err != nil {
			t.Fatalf("re-run %d: %v", i+1, err)
		}
		if got := eventSourceConstraints(t, ctx, s); !slices.Equal(got, want) {
			t.Fatalf("after re-run %d constraints = %v, want %v", i+1, got, want)
		}
	}
	if n := countRows(t, ctx, s, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'records' AND column_name = 'import_key'`); n != 1 {
		t.Errorf("import_key columns = %d", n)
	}
	if n := countRows(t, ctx, s, `SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_records_import_key'`); n != 1 {
		t.Errorf("idx_records_import_key = %d", n)
	}

	// An event with source=import is accepted; an unknown source is not.
	e := ev("acme", memory.EventRecordCreated, uuid.New().String(), time.Minute)
	e.Source, e.Status = memory.EventSourceImport, record.StatusCandidate
	if err := s.Append(ctx, e); err != nil {
		t.Errorf("append import event: %v", err)
	}
}

// A database that already ran 0003 (the old constraint) upgrades in place.
func TestMigration0004UpgradesOldConstraint(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Simulate the pre-0004 state.
	for _, q := range []string{
		`ALTER TABLE events DROP CONSTRAINT events_source_check_v2`,
		`ALTER TABLE events ADD CONSTRAINT events_source_check CHECK (source IN ('inline', 'session', 'pr', 'cleanup'))`,
		`DROP INDEX idx_records_import_key`,
		`ALTER TABLE records DROP COLUMN import_key`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.runMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if got := eventSourceConstraints(t, ctx, s); !slices.Equal(got, []string{"events_source_check_v2"}) {
		t.Errorf("constraints = %v", got)
	}
}

func importRecord(ns, title string, key *string) *record.Record {
	r := record.New(uuid.New().String(), record.KindPattern, title, "content of "+title, "repo-a", record.SourceImport, 0.5)
	r.Namespace = ns
	r.ImportKey = key
	r.Embedding = make([]float32, 1024)
	r.Embedding[0] = 1
	return r
}

// AC-38: Store.Create and the tx Create write import_key only when set; the
// partial unique index rejects a duplicate key within a namespace only.
func TestCreateImportKeyBothPaths(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	key := "automem:k1"
	if _, err := s.Create(ctx, importRecord("ns-a", "direct with key", &key)); err != nil {
		t.Fatalf("Store.Create with key: %v", err)
	}
	if _, err := s.Create(ctx, importRecord("ns-a", "direct no key", nil)); err != nil {
		t.Fatalf("Store.Create without key: %v", err)
	}
	if _, err := s.Create(ctx, importRecord("ns-a", "dup key", &key)); err == nil {
		t.Error("duplicate (namespace, import_key) was accepted")
	}
	if _, err := s.Create(ctx, importRecord("ns-b", "same key other ns", &key)); err != nil {
		t.Errorf("same key in another namespace: %v", err)
	}

	key2 := "automem:k2"
	var exists, missing bool
	err = s.WithTx(ctx, func(tx memory.TxStore) error {
		if _, err := tx.Create(ctx, importRecord("ns-a", "tx with key", &key2)); err != nil {
			return err
		}
		if _, err := tx.Create(ctx, importRecord("ns-a", "tx no key", nil)); err != nil {
			return err
		}
		if exists, err = tx.ImportKeyExists(ctx, "ns-a", key2); err != nil {
			return err
		}
		missing, err = tx.ImportKeyExists(ctx, "ns-a", "automem:none")
		return err
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if !exists || missing {
		t.Errorf("ImportKeyExists = %v / %v, want true / false", exists, missing)
	}
	if n := countRows(t, ctx, s, `SELECT count(*) FROM records WHERE import_key IS NOT NULL`); n != 3 {
		t.Errorf("rows with key = %d, want 3", n)
	}
}

// Old schema + new binary: a write without an import key must not touch the
// import_key column, so it works on a database that has not run 0004.
func TestCreateWithoutKeyWorksOnPre0004Schema(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{`DROP INDEX idx_records_import_key`, `ALTER TABLE records DROP COLUMN import_key`} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(ctx, importRecord("ns-a", "plain", nil)); err != nil {
		t.Errorf("Create without key on old schema: %v", err)
	}
	err = s.WithTx(ctx, func(tx memory.TxStore) error {
		_, err := tx.Create(ctx, importRecord("ns-a", "plain tx", nil))
		return err
	})
	if err != nil {
		t.Errorf("tx Create without key on old schema: %v", err)
	}
}

// D1: several processes starting at once must all migrate without error (the
// migration lock serializes them), in several rounds on fresh databases.
func TestConcurrentNewOnFreshDatabase(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 4; round++ {
		name := fmt.Sprintf("race_%d", round)
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		// The extension needs a superuser, and tests connect as the owner of
		// a throwaway container, which is one.
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := New(ctx, u.String())
				if err == nil {
					s.Close()
				}
				errs[i] = err
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("round %d starter %d: %v", round, i, err)
			}
		}
	}
}

// D2: a unique violation on the import key index maps to ErrImportKeyExists
// from both Create paths (a concurrent import won the race).
func TestCreateDuplicateImportKeyMapsToSentinel(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := "automem:dup"
	if _, err := s.Create(ctx, importRecord("ns-a", "first", &key)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, importRecord("ns-a", "second", &key)); !errors.Is(err, memory.ErrImportKeyExists) {
		t.Errorf("Store.Create err = %v", err)
	}
	err = s.WithTx(ctx, func(tx memory.TxStore) error {
		_, err := tx.Create(ctx, importRecord("ns-a", "third", &key))
		return err
	})
	if !errors.Is(err, memory.ErrImportKeyExists) {
		t.Errorf("tx Create err = %v", err)
	}
}
