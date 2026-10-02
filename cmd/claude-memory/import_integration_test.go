//go:build integration

package main

import (
	"context"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"claude-memory/internal/config"
	"claude-memory/internal/memory"
	"claude-memory/internal/postgres"
	"claude-memory/internal/scrub"
)

// hashEmbedder gives every distinct text its own one-hot vector, so distinct
// items are never similar and equal items are identical (no Ollama).
type hashEmbedder struct{}

func (hashEmbedder) Embed(_ context.Context, text string, _ int) ([]float32, error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	v := make([]float32, 1024)
	v[h.Sum32()%1024] = 1
	return v, nil
}

func queryInt(t *testing.T, conn *pgx.Conn, q string, args ...any) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// Import end to end on a real Postgres with a fake embedder: candidates
// with the import source and key, events, and a re-run that is a no-op even
// after one record was edited and another deprecated.
func TestImportEndToEnd(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	store, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := &config.Config{MaxContentChars: 20000, StoreSimAsk: 0.65, StoreSimUpdate: 0.85, EmbedMaxTokens: 2048}
	svc := memory.New(store, hashEmbedder{}, scrub.NewAdapter(scrub.New()), &systemClock{}, cfg).
		WithNamespace("acme").WithEvents(store)

	f := newImportFixture(t)
	f.deps.Open = func(string) (importService, time.Duration, func(), error) { return svc, 24 * time.Hour, func() {}, nil }
	args := []string{"automem", "--projects-dir", f.projDir}

	if err := runImport(ctx, f.deps, args); err != nil {
		t.Fatalf("first run: %v\n%s", err, f.out.String())
	}
	if !strings.Contains(f.out.String(), "added 2, skipped 0") || !strings.Contains(f.out.String(), "2 candidates await") {
		t.Fatalf("first run output:\n%s", f.out.String())
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if n := queryInt(t, conn, `SELECT count(*) FROM records WHERE source = 'import' AND status = 'candidate' AND namespace = 'acme' AND import_key LIKE 'automem:%' AND commit_sha IS NULL AND confidence = 0.5`); n != 2 {
		t.Fatalf("imported candidate rows = %d, want 2", n)
	}
	if n := queryInt(t, conn, `SELECT count(*) FROM events WHERE type = 'record_created' AND source = 'import' AND status = 'candidate'`); n != 2 {
		t.Errorf("record_created import events = %d, want 2", n)
	}
	// The scrubber ran: the leaked key never reached the database.
	if n := queryInt(t, conn, `SELECT count(*) FROM records WHERE title LIKE '%AKIA%'`); n != 0 {
		t.Errorf("unscrubbed title stored")
	}

	// Edit one record and deprecate the other: the re-run must still add nothing.
	var ids []string
	rows, err := conn.Query(ctx, `SELECT id::text FROM records ORDER BY title`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	newTitle := "Edited title that drifted"
	if _, err := svc.UpdateRecord(ctx, &memory.UpdateRequest{ID: ids[0], Title: &newTitle}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeprecateRecord(ctx, &memory.DeprecateRequest{ID: ids[1], Reason: "rejected in review"}); err != nil {
		t.Fatal(err)
	}

	f.out.Reset()
	if err := runImport(ctx, f.deps, args); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "added 0, skipped 2") || strings.Count(out, "skipped (already imported)") != 2 || strings.Contains(out, "await") {
		t.Errorf("re-run output:\n%s", out)
	}
	if n := queryInt(t, conn, `SELECT count(*) FROM records`); n != 2 {
		t.Errorf("rows after re-run = %d, want 2", n)
	}
	if n := queryInt(t, conn, `SELECT count(*) FROM records WHERE status = 'candidate' AND source = 'import'`); n != 1 {
		t.Errorf("candidate import rows = %d, want 1 (one deprecated)", n)
	}
}
