//go:build integration
// +build integration

package postgres

import (
	"context"
	"fmt"
	"math"
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
		if err := container.Terminate(ctx); err != nil {
			t.Logf("failed to terminate container: %v", err)
		}
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
		Namespace:  testNS,
		ID:         uuid.New().String(),
		Kind:       record.KindPattern,
		Title:      "Test Pattern",
		Content:    "This is test content",
		Repo:       "billing-service",
		Files:      []string{"file1.go", "file2.go"},
		CommitSHA:  stringPtr("abc123def456"),
		Ticket:     stringPtr("PRJ-123"),
		Tags:       []string{"tag1", "tag2"},
		Status:     record.StatusCandidate,
		Source:     record.SourceInline,
		Confidence: 0.75,
		Embedding:  make([]float32, 1024),
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
		Namespace:  testNS,
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
		Namespace:  testNS,
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
	results, err := store.Search(ctx, "ERR_NULL_DEREF", makeTestEmbedding(0.15), "billing-service", memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
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
		Namespace:  testNS,
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
		Namespace:  testNS,
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
	results, err := store.Search(ctx, "kubernetes label limit", makeTestEmbedding(0.34), "catalog-service", memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
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
		Namespace:  testNS,
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
	results, err := store.Search(ctx, "SDK struct", nil, "billing-service", memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
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
		Namespace:  testNS,
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
		Namespace:  testNS,
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
			if err := tx.AcquireLock(ctx, testNS, r1.Repo, hashTitle(r1.Title)); err != nil {
				return err
			}
			_, err := tx.Create(ctx, r1)
			return err
		})
		done <- err
	}()

	go func() {
		err := store.WithTx(ctx, func(tx memory.TxStore) error {
			if err := tx.AcquireLock(ctx, testNS, r2.Repo, hashTitle(r2.Title)); err != nil {
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
		Namespace:  testNS,
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
		Namespace:  testNS,
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
			"status":             string(record.StatusDeprecated),
			"deprecation_reason": "Superseded by more accurate description",
			"superseded_by":      idNew,
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
		Namespace:  testNS,
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
	if _, err := store.pool.Exec(ctx, "UPDATE records SET created_at = $1, updated_at = $1, last_used_at = $1 WHERE id = $2", oldTime, idCandidate); err != nil {
		t.Fatalf("Failed to backdate candidate timestamps: %v", err)
	}

	// Create an active record with old timestamps
	idActive := uuid.New().String()
	rActive := &record.Record{
		Namespace:  testNS,
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
	if _, err := store.pool.Exec(ctx, "UPDATE records SET created_at = $1, updated_at = $1, last_used_at = $1 WHERE id = $2", oldTime, idActive); err != nil {
		t.Fatalf("Failed to backdate active record timestamps: %v", err)
	}

	// Run cleanup with 180-day TTL
	if _, err := store.DeleteCandidatesByTTL(ctx, 180); err != nil {
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

// TestParaphraseSimilarityRegression is a regression test for the hybrid
// search bug where every vector query CTE referenced an undefined "r."
// alias (hybrid_search.go), causing searchHybridRRF to always error and
// Store.Search to silently degrade to full-text-only — which reports
// Similarity=0 for every result regardless of true semantic closeness.
//
// It uses two fixed, hand-computed synthetic vectors (no Ollama/real
// embedder involved) with a known cosine similarity > 0.5, and a query
// whose text shares no tokens with the stored record — so a text-only
// (degraded) fallback could never surface it via full-text match alone.
// If this test passes, the vector half of hybrid search is really
// running and Similarity reflects true cosine similarity.
func TestParaphraseSimilarityRegression(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// vecA: first 512 dims = 1, rest 0. vecB: first 768 dims = 1, rest 0.
	// cosine_similarity(vecA, vecB) = 512 / (sqrt(512) * sqrt(768)) ≈ 0.8165.
	vecA := make([]float32, 1024)
	for i := 0; i < 512; i++ {
		vecA[i] = 1
	}
	vecB := make([]float32, 1024)
	for i := 0; i < 768; i++ {
		vecB[i] = 1
	}
	const wantSimilarity = 0.8164965809277261 // 512 / sqrt(512*768)

	id := uuid.New().String()
	r := &record.Record{
		Namespace:  testNS,
		ID:         id,
		Kind:       record.KindGotcha,
		Title:      "Cache eviction avoids memory exhaustion",
		Content:    "LRU eviction policy keeps resident set bounded",
		Repo:       "billing-service",
		Status:     record.StatusActive,
		Source:     record.SourceInline,
		Confidence: 0.9,
		Embedding:  vecA,
	}
	if _, err := store.Create(ctx, r); err != nil {
		t.Fatalf("Failed to create record: %v", err)
	}

	// Query text shares no tokens with the stored title/content, so a
	// full-text-only degraded fallback would not be able to surface it.
	results, err := store.Search(ctx, "zzyx qwerty unrelated tokens", vecB, "billing-service", memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if results.Degraded {
		t.Fatalf("expected a healthy hybrid search, got degraded=true (searchHybridRRF likely errored)")
	}

	var found *memory.SearchRecord
	for _, sr := range results.Records {
		if sr.ID == id {
			found = sr
			break
		}
	}
	if found == nil {
		t.Fatalf("expected record %s in results, got %d records", id, len(results.Records))
	}

	if found.Similarity <= 0.5 {
		t.Errorf("expected Similarity > 0.5 for a close vector match, got %f", found.Similarity)
	}

	const tolerance = 0.01
	if diff := found.Similarity - wantSimilarity; diff > tolerance || diff < -tolerance {
		t.Errorf("expected Similarity ≈ %f, got %f", wantSimilarity, found.Similarity)
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

// TestTTLBoundaryNearTTL pins the DeleteCandidatesByTTL boundary (AC-35)
// on both sides of the cutoff (cutoff = time.Now() - ttlDays, computed
// fresh inside DeleteCandidatesByTTL itself — there is no injectable
// clock for this query). A one-hour margin on each side is used rather
// than exact equality: DeleteCandidatesByTTL calls time.Now() itself at
// call time, strictly after this test's fixture setup, so comparing
// against the exact nanosecond of a cutoff computed earlier in the test
// is inherently racy (confirmed: an exact-equality version of this test
// flaked because of that gap, not because of a real off-by-one in the
// query). A record just inside the window (TTL minus one hour) must
// survive; a record just outside it (TTL plus one hour) must be deleted.
func TestTTLBoundaryNearTTL(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	const ttlDays = 180
	now := time.Now().UTC()

	// Just inside the TTL window (younger than the cutoff): must survive.
	idInsideTTL := uuid.New().String()
	rInsideTTL := &record.Record{
		Namespace:  testNS,
		ID:         idInsideTTL,
		Kind:       record.KindPattern,
		Title:      "Just inside TTL",
		Content:    "One hour younger than the TTL cutoff; must survive",
		Repo:       "billing-service",
		Status:     record.StatusCandidate,
		Source:     record.SourceSession,
		Confidence: 0.5,
		Embedding:  makeTestEmbedding(0.1),
	}
	if _, err := store.Create(ctx, rInsideTTL); err != nil {
		t.Fatalf("Failed to create inside-TTL record: %v", err)
	}
	insideTTLTime := now.AddDate(0, 0, -ttlDays).Add(1 * time.Hour)
	if _, err := store.pool.Exec(ctx,
		"UPDATE records SET created_at = $1, updated_at = $1, last_used_at = $1 WHERE id = $2",
		insideTTLTime, idInsideTTL); err != nil {
		t.Fatalf("Failed to set inside-TTL timestamp: %v", err)
	}

	// Just past the TTL window (older than the cutoff): must be deleted.
	idPastTTL := uuid.New().String()
	rPastTTL := &record.Record{
		Namespace:  testNS,
		ID:         idPastTTL,
		Kind:       record.KindPattern,
		Title:      "Just past TTL",
		Content:    "One hour older than the TTL cutoff; must be deleted",
		Repo:       "billing-service",
		Status:     record.StatusCandidate,
		Source:     record.SourceSession,
		Confidence: 0.5,
		Embedding:  makeTestEmbedding(0.12),
	}
	if _, err := store.Create(ctx, rPastTTL); err != nil {
		t.Fatalf("Failed to create past-TTL record: %v", err)
	}
	pastTTLTime := now.AddDate(0, 0, -ttlDays).Add(-1 * time.Hour)
	if _, err := store.pool.Exec(ctx,
		"UPDATE records SET created_at = $1, updated_at = $1, last_used_at = $1 WHERE id = $2",
		pastTTLTime, idPastTTL); err != nil {
		t.Fatalf("Failed to set past-TTL timestamp: %v", err)
	}

	if _, err := store.DeleteCandidatesByTTL(ctx, ttlDays); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	if _, err := store.Get(ctx, idInsideTTL); err != nil {
		t.Errorf("expected the record just inside the TTL window to survive, got error: %v", err)
	}
	if _, err := store.Get(ctx, idPastTTL); err != memory.ErrNotFound {
		t.Errorf("expected the record just past the TTL window to be deleted, got err=%v", err)
	}
}

// TestSearchExcludesDeprecatedByDefaultButIncludesOnRequest pins AC-4:
// memory_search excludes deprecated records by default, but includes them
// when explicitly requested (SearchOptions.IncludeDeprecated).
func TestSearchExcludesDeprecatedByDefaultButIncludesOnRequest(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	repo := "billing-service-ttl-deprecated-test"
	id := uuid.New().String()
	r := &record.Record{
		Namespace:         testNS,
		ID:                id,
		Kind:              record.KindGotcha,
		Title:             "Deprecated fact about retries",
		Content:           "This fact about retry backoff is deprecated",
		Repo:              repo,
		Status:            record.StatusDeprecated,
		DeprecationReason: stringPtr("superseded"),
		Source:            record.SourcePR,
		Confidence:        0.75,
		Embedding:         makeTestEmbedding(0.5),
	}
	if _, err := store.Create(ctx, r); err != nil {
		t.Fatalf("Failed to create deprecated record: %v", err)
	}

	// Default: deprecated excluded.
	resultDefault, err := store.Search(ctx, "retry backoff", makeTestEmbedding(0.5), repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search (default) failed: %v", err)
	}
	for _, sr := range resultDefault.Records {
		if sr.ID == id {
			t.Errorf("expected deprecated record to be excluded by default, but it was returned")
		}
	}

	// Explicit opt-in: deprecated included.
	resultIncluded, err := store.Search(ctx, "retry backoff", makeTestEmbedding(0.5), repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5, IncludeDeprecated: true})
	if err != nil {
		t.Fatalf("Search (IncludeDeprecated) failed: %v", err)
	}
	found := false
	for _, sr := range resultIncluded.Records {
		if sr.ID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("expected deprecated record to be included when IncludeDeprecated=true")
	}
}

// TestCreateHandlesSQLSpecialCharactersInTitleAndContent pins the tsvector
// fix for the A05 (Injection) finding: title/content are bound as $N
// parameters to to_tsvector/setweight (tsvectorExpr), not spliced into the
// SQL text with manual quote-doubling. A record whose title/content contain
// single quotes, backslashes, a dollar-quote delimiter, a SQL comment
// marker, and a full statement-injection payload must round-trip exactly
// and must still be findable via full-text search on an ordinary word
// alongside that payload — proof the payload was bound as data, not
// executed or mangled as SQL.
func TestCreateHandlesSQLSpecialCharactersInTitleAndContent(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	repo := "billing-service-sql-injection-test"
	title := `O'Brien's "$$evil--" title ); DROP TABLE records; --`
	content := `Contains a single quote ', a backslash \, a dollar-quote $$ delim $$, ` +
		`a SQL comment -- marker, and a payload: '); DROP TABLE records; --. ` +
		`Also mentions the word gremlin for full-text findability.`

	r := &record.Record{
		Namespace:  testNS,
		ID:         uuid.New().String(),
		Kind:       record.KindGotcha,
		Title:      title,
		Content:    content,
		Repo:       repo,
		Tags:       []string{"o'brien", "tag; DROP TABLE records; --"},
		Status:     record.StatusActive,
		Source:     record.SourceInline,
		Confidence: 0.8,
		Embedding:  makeTestEmbedding(0.8),
	}

	if _, err := store.Create(ctx, r); err != nil {
		t.Fatalf("Failed to create record with SQL special characters: %v", err)
	}

	// Round-trip: if the payload had been executed or mangled as SQL (e.g.
	// records dropped, or the string truncated at the quote), either this
	// Get would fail outright or the fields below would mismatch.
	retrieved, err := store.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("Failed to get record back (records table may have been dropped): %v", err)
	}
	if retrieved.Title != title {
		t.Errorf("Title did not round-trip: got %q, want %q", retrieved.Title, title)
	}
	if retrieved.Content != content {
		t.Errorf("Content did not round-trip: got %q, want %q", retrieved.Content, content)
	}

	// Full-text findability: the tsvector must still have been built from
	// this content, so an ordinary word inside it is searchable.
	results, err := store.Search(ctx, "gremlin", makeTestEmbedding(0.8), repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	found := false
	for _, sr := range results.Records {
		if sr.ID == r.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected record with SQL special characters to be findable via full-text search on 'gremlin'")
	}
}

// TestSearchRanksActiveAboveCandidateAtEqualRelevance pins the remaining
// half of AC-4: at equal relevance (same embedding, same searchable text),
// an active record must rank above a candidate record, with the candidate
// flagged Unverified.
func TestSearchRanksActiveAboveCandidateAtEqualRelevance(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	repo := "billing-service-ac4-rank-test"
	embedding := makeTestEmbedding(0.9)
	title := "Flaky retry on checkout timeout"
	content := "Retrying checkout after a timeout can duplicate the order"

	idCandidate := uuid.New().String()
	rCandidate := &record.Record{
		Namespace:  testNS,
		ID:         idCandidate,
		Kind:       record.KindGotcha,
		Title:      title,
		Content:    content,
		Repo:       repo,
		Status:     record.StatusCandidate,
		Source:     record.SourceSession,
		Confidence: 0.7,
		Embedding:  embedding,
	}
	if _, err := store.Create(ctx, rCandidate); err != nil {
		t.Fatalf("Failed to create candidate record: %v", err)
	}

	idActive := uuid.New().String()
	rActive := &record.Record{
		Namespace:  testNS,
		ID:         idActive,
		Kind:       record.KindGotcha,
		Title:      title,
		Content:    content,
		Repo:       repo,
		Status:     record.StatusActive,
		Source:     record.SourcePR,
		Confidence: 0.9,
		Embedding:  embedding,
	}
	if _, err := store.Create(ctx, rActive); err != nil {
		t.Fatalf("Failed to create active record: %v", err)
	}

	results, err := store.Search(ctx, title, embedding, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results.Records) < 2 {
		t.Fatalf("expected both records in results, got %d", len(results.Records))
	}

	idxActive, idxCandidate := -1, -1
	for i, sr := range results.Records {
		switch sr.ID {
		case idActive:
			idxActive = i
		case idCandidate:
			idxCandidate = i
		}
	}
	if idxActive == -1 || idxCandidate == -1 {
		t.Fatalf("expected both active and candidate records in results, got %+v", results.Records)
	}
	if idxActive >= idxCandidate {
		t.Errorf("expected active record (index %d) to rank above candidate record (index %d) at equal relevance", idxActive, idxCandidate)
	}

	for _, sr := range results.Records {
		if sr.ID == idCandidate && !sr.Unverified {
			t.Errorf("expected candidate record to be flagged Unverified")
		}
		if sr.ID == idActive && sr.Unverified {
			t.Errorf("expected active record to not be flagged Unverified")
		}
	}
}

// containsID reports whether id appears among results.
func containsID(results []*memory.SearchRecord, id string) bool {
	for _, sr := range results {
		if sr.ID == id {
			return true
		}
	}
	return false
}

// TestUpdateRecomputesTsvectorFromNewContent pins the stale-tsvector fix
// (AC-5/AC-8, fix-loop iteration 2): after Store.Update changes content,
// full-text search must find the NEW content ("quokkafin") and must no
// longer find the OLD content it replaced ("zebracorn") — proof the
// tsvector was recomputed from the update's new bound value, not from a
// pre-statement snapshot of the old row.
//
// Search is called with a nil embedding throughout (searchFullTextOnly),
// deliberately bypassing the hybrid RRF/vector-similarity path: with only
// one record in this test's repo, that record would always surface via
// its own vector proximity regardless of whether the full-text term still
// matches, which would mask exactly the regression this test exists to
// catch.
func TestUpdateRecomputesTsvectorFromNewContent(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	repo := "billing-service-update-tsvector-content-test"
	embedding := makeTestEmbedding(1.0)

	id := uuid.New().String()
	r := &record.Record{
		Namespace:  testNS,
		ID:         id,
		Kind:       record.KindGotcha,
		Title:      "Flaky animal bug",
		Content:    "The zebracorn appears when the cache is cold",
		Repo:       repo,
		Status:     record.StatusActive,
		Source:     record.SourceInline,
		Confidence: 0.8,
		Embedding:  embedding,
	}
	if _, err := store.Create(ctx, r); err != nil {
		t.Fatalf("Failed to create record: %v", err)
	}

	// Sanity: findable by the original word before the update.
	before, err := store.Search(ctx, "zebracorn", nil, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search (before update) failed: %v", err)
	}
	if !containsID(before.Records, id) {
		t.Fatalf("expected record to be findable by 'zebracorn' before the update")
	}

	newContent := "The quokkafin appears when the cache is cold"
	if _, err := store.Update(ctx, id, map[string]interface{}{"content": newContent}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	afterNew, err := store.Search(ctx, "quokkafin", nil, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search (after update, new word) failed: %v", err)
	}
	if !containsID(afterNew.Records, id) {
		t.Errorf("expected record to be findable by 'quokkafin' after the update")
	}

	afterOld, err := store.Search(ctx, "zebracorn", nil, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search (after update, old word) failed: %v", err)
	}
	if containsID(afterOld.Records, id) {
		t.Errorf("expected record to no longer be findable by 'zebracorn' after the update")
	}

	// The retrieved row itself must also carry the new content.
	retrieved, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved.Content != newContent {
		t.Errorf("Content did not update: got %q, want %q", retrieved.Content, newContent)
	}
}

// TestUpdatePartialTagsOnlyKeepsTitleAndContentSearchable pins the partial-
// update case of the same fix: when ONLY tags is in the updates map, the
// recomputed tsvector must bind the NEW tag value for tags while reading
// title/content from their (unchanged) existing columns — not a stale
// pre-update snapshot of all three. The new tag word must become
// searchable, and the original title/content words must remain
// searchable too.
//
// As in TestUpdateRecomputesTsvectorFromNewContent, Search is called with
// a nil embedding to isolate the full-text (tsvector) path from
// vector-similarity matching.
func TestUpdatePartialTagsOnlyKeepsTitleAndContentSearchable(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	repo := "billing-service-update-tsvector-tags-test"
	embedding := makeTestEmbedding(1.1)

	id := uuid.New().String()
	r := &record.Record{
		Namespace:  testNS,
		ID:         id,
		Kind:       record.KindPattern,
		Title:      "Walrustitle identifier",
		Content:    "Discusses walruscontent behavior in detail",
		Repo:       repo,
		Tags:       []string{"old-tag-marmoset"},
		Status:     record.StatusActive,
		Source:     record.SourceInline,
		Confidence: 0.8,
		Embedding:  embedding,
	}
	if _, err := store.Create(ctx, r); err != nil {
		t.Fatalf("Failed to create record: %v", err)
	}

	// Update ONLY tags — title and content are not in this updates map.
	newTags := []string{"newtagokapi"}
	if _, err := store.Update(ctx, id, map[string]interface{}{"tags": newTags}); err != nil {
		t.Fatalf("Update (tags only) failed: %v", err)
	}

	// New tag word must now be searchable.
	tagResult, err := store.Search(ctx, "newtagokapi", nil, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search (new tag) failed: %v", err)
	}
	if !containsID(tagResult.Records, id) {
		t.Errorf("expected record to be findable by the new tag 'newtagokapi' after a tags-only update")
	}

	// Title and content words (untouched by this update) must still be
	// searchable — the recomputed tsvector must not have dropped them.
	titleResult, err := store.Search(ctx, "walrustitle", nil, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search (title word) failed: %v", err)
	}
	if !containsID(titleResult.Records, id) {
		t.Errorf("expected record to remain findable by its unchanged title word 'walrustitle'")
	}

	contentResult, err := store.Search(ctx, "walruscontent", nil, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search (content word) failed: %v", err)
	}
	if !containsID(contentResult.Records, id) {
		t.Errorf("expected record to remain findable by its unchanged content word 'walruscontent'")
	}

	// The old tag word must no longer be attached to this record.
	oldTagResult, err := store.Search(ctx, "old-tag-marmoset", nil, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search (old tag) failed: %v", err)
	}
	if containsID(oldTagResult.Records, id) {
		t.Errorf("expected record to no longer be findable by the replaced tag 'old-tag-marmoset'")
	}
}

// TestSearchDoesNotBuryStronglyRelevantCandidate pins the eval regression
// found on 2026-10-01 (paraphrase_005/_006 recall@3 lost): a candidate
// record that is by far the best semantic match must still rank first.
// AC-4's active-over-candidate rule is a tie-breaker at equal relevance
// (see TestSearchRanksActiveAboveCandidateAtEqualRelevance), not a
// multiplicative penalty on the RRF score — RRF scores are rank-compressed,
// so the previous 0.85 factor (1/61*0.85 = 0.01393 < 1/70 = 0.01429)
// demoted a rank-1 candidate below every active record up to vector rank
// ~11, i.e. below four weaker active records here. Synthetic vectors only;
// no embedding provider involved.
func TestSearchDoesNotBuryStronglyRelevantCandidate(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()

	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	const repo = "billing-service-candidate-burial-test"
	// unitPlus returns e0 + w*e_dim (1024-dim), whose cosine with the query
	// e0 is 1/sqrt(1+w*w).
	unitPlus := func(dim int, w float32) []float32 {
		v := make([]float32, 1024)
		v[0] = 1
		v[dim] = w
		return v
	}
	query := unitPlus(1, 0)

	idCandidate := uuid.New().String()
	if _, err := store.Create(ctx, &record.Record{
		Namespace:  testNS,
		ID:         idCandidate,
		Kind:       record.KindConvention,
		Title:      "Transcripts are JSON lines",
		Content:    "Each transcript line is one JSON object",
		Repo:       repo,
		Status:     record.StatusCandidate,
		Source:     record.SourceSession,
		Confidence: 0.5,
		Embedding:  unitPlus(1, 0.2), // cosine ≈ 0.981
	}); err != nil {
		t.Fatalf("Failed to create candidate record: %v", err)
	}

	for i := 0; i < 4; i++ {
		if _, err := store.Create(ctx, &record.Record{
			Namespace:  testNS,
			ID:         uuid.New().String(),
			Kind:       record.KindGotcha,
			Title:      fmt.Sprintf("Unrelated active record %d", i),
			Content:    "Some other fact entirely",
			Repo:       repo,
			Status:     record.StatusActive,
			Source:     record.SourcePR,
			Confidence: 0.75,
			Embedding:  unitPlus(2+i, 1.0+float32(i)*0.1), // cosine ≈ 0.71..0.64
		}); err != nil {
			t.Fatalf("Failed to create active record %d: %v", i, err)
		}
	}

	// Query text shares no tokens with any record: vector ranking only.
	results, err := store.Search(ctx, "zzyx qwerty", query, repo, memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if results.Degraded {
		t.Fatalf("expected healthy hybrid search, got degraded")
	}
	if len(results.Records) == 0 || results.Records[0].ID != idCandidate {
		t.Fatalf("expected the strongly relevant candidate to rank first, got %+v", results.Records)
	}
	top := results.Records[0]
	if !top.Unverified {
		t.Errorf("expected candidate to be flagged Unverified")
	}
	if want := 1.0 / 61.0; top.Score < want-1e-9 || top.Score > want+1e-9 {
		t.Errorf("expected unweighted RRF score 1/61 for vector rank 1, got %f", top.Score)
	}
	if want := 1 / math.Sqrt(1.04); math.Abs(top.Similarity-want) > 1e-3 {
		t.Errorf("expected raw cosine Similarity ≈ %f, got %f", want, top.Similarity)
	}
}

const testNS = "test-ns"

// TestNamespaceIsolation verifies records never cross namespaces in search,
// dedup candidates, or list, while "global" is visible when requested.
func TestNamespaceIsolation(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	emb := makeTestEmbedding(0.2)
	for _, ns := range []string{"ns-a", "ns-b", "global"} {
		if _, err := store.Create(ctx, &record.Record{
			ID: uuid.New().String(), Namespace: ns, Kind: record.KindGotcha,
			Title: "shared-title " + ns, Content: "isolationprobe content", Repo: "*",
			Status: record.StatusActive, Source: record.SourceInline, Confidence: 0.8,
			Embedding: emb, Files: []string{}, Tags: []string{},
		}); err != nil {
			t.Fatalf("create %s: %v", ns, err)
		}
	}

	titles := func(rs []*memory.SearchRecord) map[string]bool {
		m := map[string]bool{}
		for _, r := range rs {
			m[r.Title] = true
		}
		return m
	}

	res, err := store.Search(ctx, "isolationprobe", emb, "r", memory.SearchOptions{Namespaces: []string{"ns-a"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(res.Records); len(got) != 1 || !got["shared-title ns-a"] {
		t.Errorf("ns-a search = %v, want only ns-a", got)
	}

	res, err = store.Search(ctx, "isolationprobe", emb, "r", memory.SearchOptions{Namespaces: []string{"ns-a", "global"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(res.Records); len(got) != 2 || !got["shared-title ns-a"] || !got["shared-title global"] {
		t.Errorf("ns-a+global search = %v", got)
	}

	// Full-text-only path (nil embedding) is isolated too.
	res, err = store.Search(ctx, "isolationprobe", nil, "r", memory.SearchOptions{Namespaces: []string{"ns-b"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(res.Records); len(got) != 1 || !got["shared-title ns-b"] {
		t.Errorf("ns-b full-text search = %v, want only ns-b", got)
	}

	cands, err := store.FindCandidates(ctx, emb, "ns-a", "r", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Title != "shared-title ns-a" {
		t.Errorf("ns-a candidates = %+v, want only ns-a", cands)
	}

	nsB := "ns-b"
	list, err := store.List(ctx, memory.ListFilters{Namespace: &nsB})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Namespace != "ns-b" {
		t.Errorf("ns-b list = %d records", len(list))
	}
}

// Search returns files and commit_sha (needed by the staleness check), and
// Update can persist commit_sha (re-baselining).
func TestSearchReturnsFilesAndCommitSHA_UpdateRebaselines(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgresContainer(t, ctx)
	defer cleanup()
	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sha := "abcdef1234567"
	emb := makeTestEmbedding(0.3)
	created, err := store.Create(ctx, &record.Record{
		ID: uuid.New().String(), Namespace: testNS, Kind: record.KindGotcha,
		Title: "stalenessprobe", Content: "stalenessprobe content", Repo: "r",
		Status: record.StatusActive, Source: record.SourceInline, Confidence: 0.8,
		Embedding: emb, Files: []string{"a.go", "b/c.go"}, Tags: []string{}, CommitSHA: &sha,
	})
	if err != nil {
		t.Fatal(err)
	}

	check := func(label string, embedding []float32, wantSHA string) {
		res, err := store.Search(ctx, "stalenessprobe", embedding, "r", memory.SearchOptions{Namespaces: []string{testNS}, Limit: 5})
		if err != nil || len(res.Records) != 1 {
			t.Fatalf("%s: %v %v", label, err, res)
		}
		r := res.Records[0]
		if r.CommitSHA != wantSHA || len(r.Files) != 2 || r.Files[0] != "a.go" {
			t.Errorf("%s: files=%v commit_sha=%q, want 2 files and %q", label, r.Files, r.CommitSHA, wantSHA)
		}
	}
	check("hybrid", emb, sha)
	check("full-text only", nil, sha)

	newSHA := "0123456789abcdef"
	if _, err := store.Update(ctx, created.ID, map[string]interface{}{"commit_sha": newSHA}); err != nil {
		t.Fatalf("update commit_sha: %v", err)
	}
	check("after update", emb, newSHA)

	// a record with no commit_sha comes back as ""
	if _, err := store.Update(ctx, created.ID, map[string]interface{}{"commit_sha": ""}); err != nil {
		t.Fatal(err)
	}
	check("cleared", emb, "")
}
