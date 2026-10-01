// Package postgres provides a pgx-based implementation of the memory.Store interface.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

//go:embed migrations/0001_init.sql
var migrationSQL string

// Store is the Postgres adapter implementing memory.Store.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a new Postgres store with the given DSN.
// It attempts to run migrations idempotently before returning.
func New(ctx context.Context, dsn string) (*Store, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// Test the connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	s := &Store{pool: pool}

	// Run migrations
	if err := s.runMigrations(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return s, nil
}

// Close closes the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// runMigrations executes the migration SQL idempotently.
func (s *Store) runMigrations(ctx context.Context) error {
	// Split migration by ; to handle multiple statements
	// Note: this is a simple approach; for complex migrations use a real migration library.
	statements := strings.Split(migrationSQL, ";")
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			// If the error is about the extension already existing, that's fine
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("execute migration statement: %w", err)
			}
		}
	}
	return nil
}

// Create persists a new record and returns it with all fields set.
func (s *Store) Create(ctx context.Context, r *record.Record) (*record.Record, error) {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}

	now := time.Now().UTC()
	r.CreatedAt = now
	r.UpdatedAt = now

	// Convert embedding to pgvector if present
	var embeddingVec pgvector.Vector
	if len(r.Embedding) > 0 {
		embeddingVec = pgvector.NewVector(r.Embedding)
	}

	// Compute tsvector in Go to avoid type resolution issues
	tagsStr := strings.Join(r.Tags, " ")
	tsvec := fmt.Sprintf("setweight(to_tsvector('simple', %s), 'A') || setweight(to_tsvector('simple', %s), 'B') || setweight(to_tsvector('simple', %s), 'C')",
		"'" + strings.ReplaceAll(r.Title, "'", "''") + "'",
		"'" + strings.ReplaceAll(tagsStr, "'", "''") + "'",
		"'" + strings.ReplaceAll(r.Content, "'", "''") + "'")

	query := `
		INSERT INTO records (
			id, kind, title, content, repo, files, commit_sha, ticket, tags,
			status, deprecation_reason, superseded_by, source, confidence,
			seen_count, used_count, created_at, updated_at, last_used_at, embedding, tsvector_content
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20,
			` + tsvec + `
		) RETURNING id
	`

	err := s.pool.QueryRow(ctx, query,
		r.ID, string(r.Kind), r.Title, r.Content, r.Repo, r.Files, r.CommitSHA, r.Ticket, r.Tags,
		string(r.Status), r.DeprecationReason, r.SupersededBy, string(r.Source), r.Confidence,
		r.SeenCount, r.UsedCount, r.CreatedAt, r.UpdatedAt, r.LastUsedAt, embeddingVec,
	).Scan(&r.ID)

	if err != nil {
		return nil, fmt.Errorf("insert record: %w", err)
	}

	return r, nil
}

// Get retrieves a record by ID, or returns ErrNotFound if it does not exist.
func (s *Store) Get(ctx context.Context, id string) (*record.Record, error) {
	r := &record.Record{}

	query := `
		SELECT
			id, kind, title, content, repo, files, commit_sha, ticket, tags,
			status, deprecation_reason, superseded_by, source, confidence,
			seen_count, used_count, created_at, updated_at, last_used_at, embedding
		FROM records
		WHERE id = $1
	`

	var embeddingVec pgvector.Vector
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&r.ID, (*string)(&r.Kind), &r.Title, &r.Content, &r.Repo, &r.Files, &r.CommitSHA, &r.Ticket, &r.Tags,
		(*string)(&r.Status), &r.DeprecationReason, &r.SupersededBy, (*string)(&r.Source), &r.Confidence,
		&r.SeenCount, &r.UsedCount, &r.CreatedAt, &r.UpdatedAt, &r.LastUsedAt, &embeddingVec,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, memory.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query record: %w", err)
	}

	if len(embeddingVec.Slice()) > 0 {
		r.Embedding = embeddingVec.Slice()
	}

	return r, nil
}

// Update modifies fields of an existing record and returns the updated record,
// or returns ErrNotFound if it does not exist.
// The updates map must only contain whitelisted column names.
func (s *Store) Update(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
	if len(updates) == 0 {
		return s.Get(ctx, id)
	}

	// Whitelist allowed columns to update
	allowedColumns := map[string]bool{
		"title":                true,
		"content":              true,
		"tags":                 true,
		"files":                true,
		"ticket":               true,
		"status":               true,
		"confidence":           true,
		"deprecation_reason":   true,
		"superseded_by":        true,
		"seen_count":           true,
		"used_count":           true,
		"last_used_at":         true,
		"embedding":            true,
	}

	// Build the SET clause dynamically
	var setClauses []string
	var args []interface{}
	argCount := 1

	// Always update updated_at
	setClauses = append(setClauses, "updated_at = $"+fmt.Sprint(argCount))
	args = append(args, time.Now().UTC())
	argCount++

	for col, val := range updates {
		if !allowedColumns[col] {
			return nil, fmt.Errorf("update: column %q not allowed", col)
		}

		// Special handling for embedding to convert to pgvector
		if col == "embedding" {
			if embeddingSlice, ok := val.([]float32); ok && len(embeddingSlice) > 0 {
				val = pgvector.NewVector(embeddingSlice)
			}
		}

		setClauses = append(setClauses, col+" = $"+fmt.Sprint(argCount))
		args = append(args, val)
		argCount++
	}

	args = append(args, id)

	// If title, content, or tags were updated, recompute tsvector
	if _, hasTitle := updates["title"]; hasTitle {
		setClauses = append(setClauses, "tsvector_content = (SELECT "+
			"setweight(to_tsvector('simple', COALESCE(title, '')), 'A') || "+
			"setweight(to_tsvector('simple', COALESCE(array_to_string(tags, ' '), '')), 'B') || "+
			"setweight(to_tsvector('simple', COALESCE(content, '')), 'C') "+
			"FROM records WHERE id = $"+fmt.Sprint(argCount-1)+")")
	} else if _, hasContent := updates["content"]; hasContent {
		setClauses = append(setClauses, "tsvector_content = (SELECT "+
			"setweight(to_tsvector('simple', COALESCE(title, '')), 'A') || "+
			"setweight(to_tsvector('simple', COALESCE(array_to_string(tags, ' '), '')), 'B') || "+
			"setweight(to_tsvector('simple', COALESCE(content, '')), 'C') "+
			"FROM records WHERE id = $"+fmt.Sprint(argCount-1)+")")
	} else if _, hasTags := updates["tags"]; hasTags {
		setClauses = append(setClauses, "tsvector_content = (SELECT "+
			"setweight(to_tsvector('simple', COALESCE(title, '')), 'A') || "+
			"setweight(to_tsvector('simple', COALESCE(array_to_string(tags, ' '), '')), 'B') || "+
			"setweight(to_tsvector('simple', COALESCE(content, '')), 'C') "+
			"FROM records WHERE id = $"+fmt.Sprint(argCount-1)+")")
	}

	query := fmt.Sprintf(`
		UPDATE records
		SET %s
		WHERE id = $%d
		RETURNING id, kind, title, content, repo, files, commit_sha, ticket, tags,
		          status, deprecation_reason, superseded_by, source, confidence,
		          seen_count, used_count, created_at, updated_at, last_used_at, embedding
	`, strings.Join(setClauses, ", "), argCount)

	r := &record.Record{}
	var embeddingVec pgvector.Vector
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&r.ID, (*string)(&r.Kind), &r.Title, &r.Content, &r.Repo, &r.Files, &r.CommitSHA, &r.Ticket, &r.Tags,
		(*string)(&r.Status), &r.DeprecationReason, &r.SupersededBy, (*string)(&r.Source), &r.Confidence,
		&r.SeenCount, &r.UsedCount, &r.CreatedAt, &r.UpdatedAt, &r.LastUsedAt, &embeddingVec,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, memory.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update record: %w", err)
	}

	if len(embeddingVec.Slice()) > 0 {
		r.Embedding = embeddingVec.Slice()
	}

	return r, nil
}

// Search performs a hybrid (semantic + full-text) ranked search.
// If embedding is nil, falls back to full-text-only with Similarity 0.
// Excludes deprecated records by default; flags candidate records as unverified.
func (s *Store) Search(ctx context.Context, query string, embedding []float32, repo string, opts memory.SearchOptions) (*memory.SearchResult, error) {
	result := &memory.SearchResult{Records: []*memory.SearchRecord{}}

	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	}

	// If embedding is nil, fall back to full-text-only search (degraded)
	if len(embedding) == 0 {
		return s.searchFullTextOnly(ctx, query, repo, opts, limit)
	}

	// Use RRF (Reciprocal Rank Fusion) hybrid search
	records, err := s.searchHybridRRF(ctx, query, embedding, repo, opts, limit)
	if err != nil {
		// If the hybrid search fails, degrade to full-text-only
		return s.searchFullTextOnly(ctx, query, repo, opts, limit)
	}

	result.Records = records
	result.Degraded = false
	return result, nil
}

// FindCandidates fetches the top-N nearest records using RRF hybrid ranking.
// Returns records scoped to the same repo or repo="*".
func (s *Store) FindCandidates(ctx context.Context, embedding []float32, repo string, limit int) ([]*memory.Candidate, error) {
	if limit <= 0 {
		limit = 5
	}

	if len(embedding) == 0 {
		// No embedding; return empty candidates
		return []*memory.Candidate{}, nil
	}

	embeddingVec := pgvector.NewVector(embedding)

	// RRF hybrid query (k=60)
	query := `
		WITH vector_ranks AS (
			SELECT
				id,
				ROW_NUMBER() OVER (ORDER BY embedding <-> $1) as vector_rank
			FROM records
			WHERE (repo = $2 OR repo = '*')
				AND status IN ('candidate', 'active')
		),
		fts_ranks AS (
			SELECT
				id,
				ROW_NUMBER() OVER (ORDER BY ts_rank(tsvector_content, plainto_tsquery('simple', $3)) DESC) as fts_rank
			FROM records
			WHERE (repo = $2 OR repo = '*')
				AND status IN ('candidate', 'active')
				AND tsvector_content @@ plainto_tsquery('simple', $3)
		)
		SELECT DISTINCT
			r.id,
			r.title,
			1.0 / (60 + COALESCE(v.vector_rank, 1e9)) +
			1.0 / (60 + COALESCE(f.fts_rank, 1e9)) as rrf_score,
			1.0 - (r.embedding <-> $1) as similarity
		FROM records r
		LEFT JOIN vector_ranks v ON r.id = v.id
		LEFT JOIN fts_ranks f ON r.id = f.id
		WHERE (r.repo = $2 OR r.repo = '*')
			AND r.status IN ('candidate', 'active')
			AND (v.id IS NOT NULL OR f.id IS NOT NULL)
		ORDER BY rrf_score DESC
		LIMIT $4
	`

	rows, err := s.pool.Query(ctx, query, embeddingVec, repo, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query candidates: %w", err)
	}
	defer rows.Close()

	candidates := []*memory.Candidate{}
	for rows.Next() {
		var id, title string
		var score, similarity float64
		if err := rows.Scan(&id, &title, &score, &similarity); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		candidates = append(candidates, &memory.Candidate{
			ID:         id,
			Title:      title,
			Score:      score,
			Similarity: similarity,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("candidate rows error: %w", err)
	}

	return candidates, nil
}

// List returns all records matching the given filters (all optional).
func (s *Store) List(ctx context.Context, filters memory.ListFilters) ([]*record.Record, error) {
	query := "SELECT id, kind, title, content, repo, files, commit_sha, ticket, tags, status, deprecation_reason, superseded_by, source, confidence, seen_count, used_count, created_at, updated_at, last_used_at, embedding FROM records WHERE 1=1"
	var args []interface{}
	argCount := 1

	if filters.Repo != nil {
		query += " AND repo = $" + fmt.Sprint(argCount)
		args = append(args, *filters.Repo)
		argCount++
	}

	if filters.Kind != nil {
		query += " AND kind = $" + fmt.Sprint(argCount)
		args = append(args, string(*filters.Kind))
		argCount++
	}

	if filters.Status != nil {
		query += " AND status = $" + fmt.Sprint(argCount)
		args = append(args, string(*filters.Status))
	}

	query += " ORDER BY created_at DESC"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list records: %w", err)
	}
	defer rows.Close()

	records := []*record.Record{}
	for rows.Next() {
		r := &record.Record{}
		var embeddingVec pgvector.Vector
		if err := rows.Scan(
			&r.ID, (*string)(&r.Kind), &r.Title, &r.Content, &r.Repo, &r.Files, &r.CommitSHA, &r.Ticket, &r.Tags,
			(*string)(&r.Status), &r.DeprecationReason, &r.SupersededBy, (*string)(&r.Source), &r.Confidence,
			&r.SeenCount, &r.UsedCount, &r.CreatedAt, &r.UpdatedAt, &r.LastUsedAt, &embeddingVec,
		); err != nil {
			return nil, fmt.Errorf("scan record: %w", err)
		}
		if len(embeddingVec.Slice()) > 0 {
			r.Embedding = embeddingVec.Slice()
		}
		records = append(records, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list rows error: %w", err)
	}

	return records, nil
}

// WithTx runs fn inside one database transaction: commit if fn returns nil,
// rollback otherwise. The write path runs entirely inside it, so advisory locks
// and all writes are atomic (AC-16, AC-17).
func (s *Store) WithTx(ctx context.Context, fn func(tx memory.TxStore) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	txStore := &txStoreImpl{tx: tx}
	if err := fn(txStore); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// DeleteCandidatesByTTL hard-deletes candidate records untouched for longer than ttlDays,
// preserving active and deprecated records regardless of age.
func (s *Store) DeleteCandidatesByTTL(ctx context.Context, ttlDays int) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -ttlDays)

	query := `
		DELETE FROM records
		WHERE status = 'candidate'
			AND GREATEST(COALESCE(created_at, '1970-01-01'),
			             COALESCE(updated_at, '1970-01-01'),
			             COALESCE(last_used_at, '1970-01-01')) < $1
	`

	result, err := s.pool.Exec(ctx, query, cutoff)
	if err != nil {
		return fmt.Errorf("delete by ttl: %w", err)
	}

	_ = result.RowsAffected()
	return nil
}
