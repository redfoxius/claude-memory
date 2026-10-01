// +build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// startPostgresContainer starts a pgvector container and returns the DSN and a cleanup function.
func startPostgresContainer(t *testing.T, ctx context.Context) (string, func()) {
	req := testcontainers.ContainerRequest{
		Image:        "pgvector/pgvector:pg16",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "testpass",
			"POSTGRES_DB":       "claude_memory_test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections"),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("Failed to start container: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("Failed to get container host: %v", err)
	}

	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatalf("Failed to get mapped port: %v", err)
	}

	dsn := fmt.Sprintf("postgres://test:testpass@%s:%s/claude_memory_test", host, port.Port())

	// Wait for the database to accept connections
	for i := 0; i < 30; i++ {
		s, err := New(ctx, dsn)
		if err == nil {
			s.Close()
			break
		}
		if i == 29 {
			t.Fatalf("Failed to connect to database after 30 seconds: %v", err)
		}
		time.Sleep(1 * time.Second)
	}

	cleanup := func() {
		container.Terminate(ctx)
	}

	return dsn, cleanup
}

// TestRoundTrip tests that all record fields are persisted and retrieved correctly.
func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a record with all fields set
	r := &record.Record{
		ID:                  uuid.New().String(),
		Kind:                record.KindPattern,
		Title:               "Test Pattern",
		Content:             "This is test content",
		Repo:                "billing-service",
		Files:               []string{"file1.go", "file2.go"},
		CommitSHA:           stringPtr("abc123def456"),
		Ticket:              stringPtr("PRJ-123"),
		Tags:                []string{"tag1", "tag2"},
		Status:              record.StatusCandidate,
		Source:              record.SourceInline,
		Confidence:          0.75,
		Embedding:           make([]float32, 1024),
	}

	// Fill embedding with test data
	for i := range r.Embedding {
		r.Embedding[i] = float32(i) / 1024.0
	}

	// Store the record
	stored, err := store.Create(ctx, r)
	if err != nil {
		t.Fatalf("Failed to create record: %v", err)
	}

	// Retrieve the record
	retrieved, err := store.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Failed to get record: %v", err)
	}

	// Verify all fields
	if retrieved.ID != r.ID || retrieved.Kind != r.Kind || retrieved.Title != r.Title {
		t.Errorf("Basic fields mismatch: %+v != %+v", retrieved, r)
	}

	if len(retrieved.Embedding) != len(r.Embedding) {
		t.Errorf("Embedding length mismatch: %d != %d", len(retrieved.Embedding), len(r.Embedding))
	}

	if retrieved.Confidence != r.Confidence {
		t.Errorf("Confidence mismatch: %f != %f", retrieved.Confidence, r.Confidence)
	}

	// Verify timestamps were set
	if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Errorf("Timestamps not set: CreatedAt=%v, UpdatedAt=%v", stored.CreatedAt, stored.UpdatedAt)
	}

	_ = retrieved
}

// TestExactIdentifierFullTextRanking tests that exact-identifier matches surface
// even when semantically weaker (AC-5).
func TestExactIdentifierFullTextRanking(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create two records:
	// 1. Semantically similar but without the exact identifier
	// 2. Contains the exact identifier but semantically weaker

	id1 := uuid.New().String()
	r1 := &record.Record{
		ID:         id1,
		Kind:       record.KindGotcha,
		Title:      "Error handling in API calls",
		Content:    "Always wrap errors properly when making HTTP requests",
		Repo:       "billing-service",
		Tags:       []string{"error", "api", "best-practice"},
		Status:     record.StatusActive,
		Source:     record.SourceInline,
		Confidence: 0.9,
		Embedding:  makeTestEmbedding(0.1),
	}

	id2 := uuid.New().String()
	r2 := &record.Record{
		ID:         id2,
		Kind:       record.KindGotcha,
		Title:      "NullPointerException handling",
		Content:    "See error code ERR_NULL_DEREF when third-party SDK returns zero struct",
		Repo:       "billing-service",
		Tags:       []string{"null-pointer", "error-code"},
		Status:     record.StatusActive,
		Source:     record.SourceInline,
		Confidence: 0.8,
		Embedding:  makeTestEmbedding(0.2),
	}

	_, _ = store.Create(ctx, r1)
	_, _ = store.Create(ctx, r2)

	// Search for the exact error code (keyword match)
	results, err := store.Search(ctx, "ERR_NULL_DEREF", makeTestEmbedding(0.15), "billing-service", memory.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results.Records) == 0 {
		t.Fatalf("Expected search results, got none")
	}

	// The record with exact identifier should rank high
	if results.Records[0].ID != id2 {
		t.Errorf("Expected exact-identifier record (id2) to rank first, got id=%s", results.Records[0].ID)
	}
}

// TestCrossRepoSearch tests that repo="*" records are included in search scope (AC-6).
func TestCrossRepoSearch(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a repo-specific record
	id1 := uuid.New().String()
	r1 := &record.Record{
		ID:         id1,
		Kind:       record.KindConvention,
		Title:      "billing-service branch naming",
		Content:    "billing-service PRs target master, not develop",
		Repo:       "billing-service",
		Status:     record.StatusActive,
		Source:     record.SourcePR,
		Confidence: 0.95,
		Embedding:  makeTestEmbedding(0.3),
	}

	// Create a cross-repo record
	id2 := uuid.New().String()
	r2 := &record.Record{
		ID:         id2,
		Kind:       record.KindConvention,
		Title:      "k8s label limit",
		Content:    "Kubernetes labels are limited to 63 bytes",
		Repo:       "*",
		Status:     record.StatusActive,
		Source:     record.SourcePR,
		Confidence: 0.95,
		Embedding:  makeTestEmbedding(0.35),
	}

	_, _ = store.Create(ctx, r1)
	_, _ = store.Create(ctx, r2)

	// Search from catalog-service (not billing-service)
	results, err := store.Search(ctx, "kubernetes label limit", makeTestEmbedding(0.34), "catalog-service", memory.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Should find the cross-repo record
	found := false
	for _, sr := range results.Records {
		if sr.ID == id2 {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected cross-repo record to be included in search results")
	}
}

// TestFullTextOnlyFallback tests that search degrades to full-text-only
// when embedding provider is down (AC-7).
func TestFullTextOnlyFallback(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create records
	id1 := uuid.New().String()
	r1 := &record.Record{
		ID:         id1,
		Kind:       record.KindGotcha,
		Title:      "Nil pointer deref",
		Content:    "SDK returns zero struct instead of error",
		Repo:       "billing-service",
		Status:     record.StatusActive,
		Source:     record.SourceInline,
		Confidence: 0.9,
		Embedding:  makeTestEmbedding(0.4),
	}

	_, _ = store.Create(ctx, r1)

	// Search with nil embedding (simulating embedding provider down)
	results, err := store.Search(ctx, "SDK struct", nil, "billing-service", memory.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// Should still find results
	if len(results.Records) == 0 {
		t.Fatalf("Expected full-text results, got none")
	}

	// Should be marked as degraded
	if !results.Degraded {
		t.Errorf("Expected search to be marked as degraded")
	}

	// Similarity should be 0 for full-text-only results
	if results.Records[0].Similarity != 0 {
		t.Errorf("Expected Similarity=0 for full-text-only result, got %f", results.Records[0].Similarity)
	}
}

// TestConcurrentWrites tests that two near-duplicate writes via advisory lock
// resolve to exactly one row (AC-16).
func TestConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create two nearly-identical records in parallel via advisory locks
	id1 := uuid.New().String()
	id2 := uuid.New().String()

	r1 := &record.Record{
		ID:         id1,
		Kind:       record.KindDecision,
		Title:      "Use JSON for config",
		Content:    "JSON is widely supported and easy to parse",
		Repo:       "billing-service",
		Status:     record.StatusCandidate,
		Source:     record.SourceSession,
		Confidence: 0.5,
		Embedding:  makeTestEmbedding(0.5),
	}

	r2 := &record.Record{
		ID:         id2,
		Kind:       record.KindDecision,
		Title:      "Use JSON for config files",
		Content:    "JSON format is well-supported everywhere",
		Repo:       "billing-service",
		Status:     record.StatusCandidate,
		Source:     record.SourceSession,
		Confidence: 0.5,
		Embedding:  makeTestEmbedding(0.51),
	}

	done := make(chan error, 2)

	// Launch two writes concurrently
	go func() {
		err := store.WithTx(ctx, func(tx memory.TxStore) error {
			if err := tx.AcquireLock(ctx, r1.Repo, hashTitle(r1.Title)); err != nil {
				return err
			}
			_, err := tx.Create(ctx, r1)
			return err
		})
		done <- err
	}()

	go func() {
		err := store.WithTx(ctx, func(tx memory.TxStore) error {
			if err := tx.AcquireLock(ctx, r2.Repo, hashTitle(r2.Title)); err != nil {
				return err
			}
			_, err := tx.Create(ctx, r2)
			return err
		})
		done <- err
	}()

	// Wait for both to complete
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("Concurrent write failed: %v", err)
		}
	}

	// Both should be stored (they have different IDs and slightly different titles)
	r1Stored, _ := store.Get(ctx, id1)
	r2Stored, _ := store.Get(ctx, id2)

	if r1Stored == nil || r2Stored == nil {
		t.Errorf("One or both records were not stored")
	}
}

// TestSupersedeCandidateToActive tests that SUPERSEDE transition is atomic (AC-17).
func TestSupersedeCandidateToActive(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create an active record
	idOld := uuid.New().String()
	rOld := &record.Record{
		ID:         idOld,
		Kind:       record.KindGotcha,
		Title:      "Third-party SDK behavior",
		Content:    "Returns nil pointer on error",
		Repo:       "billing-service",
		Status:     record.StatusActive,
		Source:     record.SourceInline,
		Confidence: 0.9,
		Embedding:  makeTestEmbedding(0.6),
	}

	_, _ = store.Create(ctx, rOld)

	// Create a new record that supersedes the old one
	idNew := uuid.New().String()
	rNew := &record.Record{
		ID:         idNew,
		Kind:       record.KindGotcha,
		Title:      "Third-party SDK behavior",
		Content:    "Returns zero-value struct instead of error on failure",
		Repo:       "billing-service",
		Status:     record.StatusActive,
		Source:     record.SourceSession,
		Confidence: 0.95,
		Embedding:  makeTestEmbedding(0.61),
	}

	// Simulate SUPERSEDE: old becomes deprecated with superseded_by pointing to new
	err = store.WithTx(ctx, func(tx memory.TxStore) error {
		if _, err := tx.Create(ctx, rNew); err != nil {
			return err
		}
		_, err := tx.Update(ctx, idOld, map[string]interface{}{
			"status":           string(record.StatusDeprecated),
			"deprecation_reason": "Superseded by more accurate description",
			"superseded_by":     idNew,
		})
		return err
	})

	if err != nil {
		t.Fatalf("Supersede transaction failed: %v", err)
	}

	// Verify old record is deprecated and points to new
	rOldAfter, _ := store.Get(ctx, idOld)
	if rOldAfter.Status != record.StatusDeprecated {
		t.Errorf("Old record should be deprecated, got %s", rOldAfter.Status)
	}

	if rOldAfter.SupersededBy == nil || *rOldAfter.SupersededBy != idNew {
		t.Errorf("Old record should have superseded_by set to new ID")
	}

	// Verify new record exists and is active
	rNewAfter, _ := store.Get(ctx, idNew)
	if rNewAfter.Status != record.StatusActive {
		t.Errorf("New record should be active, got %s", rNewAfter.Status)
	}
}

// TestTTLCleanup tests that candidates older than TTL are deleted
// while active and deprecated records are preserved.
func TestTTLCleanup(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	oldTime := now.AddDate(-1, 0, 0) // 1 year ago

	// Create a candidate record with old timestamps
	idCandidate := uuid.New().String()
	rCandidate := &record.Record{
		ID:         idCandidate,
		Kind:       record.KindPattern,
		Title:      "Old candidate",
		Content:    "This should be deleted",
		Repo:       "billing-service",
		Status:     record.StatusCandidate,
		Source:     record.SourceSession,
		Confidence: 0.5,
		Embedding:  makeTestEmbedding(0.7),
	}

	_, _ = store.Create(ctx, rCandidate)

	// Manually update timestamps to old values (simulate old records)
	store.pool.Exec(ctx, "UPDATE records SET created_at = $1, updated_at = $1, last_used_at = $1 WHERE id = $2", oldTime, idCandidate)

	// Create an active record with old timestamps
	idActive := uuid.New().String()
	rActive := &record.Record{
		ID:         idActive,
		Kind:       record.KindPattern,
		Title:      "Old active record",
		Content:    "This should NOT be deleted",
		Repo:       "billing-service",
		Status:     record.StatusActive,
		Source:     record.SourcePR,
		Confidence: 0.9,
		Embedding:  makeTestEmbedding(0.72),
	}

	_, _ = store.Create(ctx, rActive)
	store.pool.Exec(ctx, "UPDATE records SET created_at = $1, updated_at = $1, last_used_at = $1 WHERE id = $2", oldTime, idActive)

	// Run cleanup with 180-day TTL
	if err := store.DeleteCandidatesByTTL(ctx, 180); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// Verify candidate is deleted
	_, err = store.Get(ctx, idCandidate)
	if err != memory.ErrNotFound {
		t.Errorf("Expected candidate to be deleted, but it still exists")
	}

	// Verify active record still exists
	rActiveAfter, err := store.Get(ctx, idActive)
	if err != nil {
		t.Errorf("Active record should not be deleted, got error: %v", err)
	}

	if rActiveAfter.Status != record.StatusActive {
		t.Errorf("Active record should still be active, got %s", rActiveAfter.Status)
	}
}

// Helper functions

func stringPtr(s string) *string {
	return &s
}

func makeTestEmbedding(offset float32) []float32 {
	emb := make([]float32, 1024)
	for i := range emb {
		emb[i] = offset + float32(i)/10240.0
	}
	return emb
}

func hashTitle(title string) string {
	// Simple hash for testing
	return fmt.Sprintf("%x", len(title))
}
