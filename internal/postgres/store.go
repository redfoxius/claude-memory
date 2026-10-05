// Package postgres provides a pgx-based implementation of the memory.Store interface.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

//go:embed migrations/0001_init.sql
var migrationInitSQL string

//go:embed migrations/0002_namespaces.sql
var migrationNamespacesSQL string

//go:embed migrations/0003_events.sql
var migrationEventsSQL string

//go:embed migrations/0004_mgmt_import.sql
var migrationMgmtImportSQL string

//go:embed migrations/0005_reliability.sql
var migrationReliabilitySQL string

// migrationSQL is every migration, applied in order. Each statement is
// idempotent, so re-running on an already-migrated database is a no-op.
var migrationSQL = migrationInitSQL + ";\n" + migrationNamespacesSQL + ";\n" + migrationEventsSQL + ";\n" + migrationMgmtImportSQL + ";\n" + migrationReliabilitySQL

// Store is the Postgres adapter implementing memory.Store.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a new Postgres store with the given DSN.
// It attempts to run migrations idempotently before returning.
func New(ctx context.Context, dsn string) (*Store, error) {
	s, err := Open(ctx, dsn)
	if err != nil {
		return nil, err
	}

	// Run migrations
	if err := s.runMigrations(ctx); err != nil {
		s.pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return s, nil
}

// Open connects to Postgres without running migrations. It is for the hook's
// latency-sensitive hot path, which assumes a long-running subcommand
// (serve, seed, cleanup, ingest-pr) already applied the schema.
func Open(ctx context.Context, dsn string) (*Store, error) {
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

	return &Store{pool: pool}, nil
}

// Close closes the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// migrationLockKey is the session-level advisory lock that serializes
// migrations across concurrently starting processes.
const migrationLockKey int64 = 0x636c6d656d6d6967 // "clmemmig"

// alreadyExists reports a "the object is already there" error, by SQLSTATE
// (42710 duplicate_object, 42P07 duplicate_table, 42P06 duplicate_schema) so
// it does not depend on the server's message language. The English text match
// is only a fallback for errors that carry no PgError.
func alreadyExists(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42710", "42P07", "42P06":
			return true
		}
		return false
	}
	return strings.Contains(err.Error(), "already exists")
}

// runMigrations executes the migration SQL idempotently. Every statement runs
// on one dedicated connection holding an advisory lock, so several processes
// starting at once apply the schema one after another instead of racing.
func (s *Store) runMigrations(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	// Waiting for another migrator is bounded by ctx only: it may be building
	// an index. The DDL itself must not wait on a table lock for long.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// Best effort, on a fresh context so a cancelled ctx still unlocks.
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()
	if _, err := conn.Exec(ctx, "SET lock_timeout = '5s'"); err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}
	// The connection returns to the pool: do not leak the setting.
	defer func() { _, _ = conn.Exec(context.Background(), "RESET lock_timeout") }()

	// Split migration by ; to handle multiple statements
	// Note: this is a simple approach; for complex migrations use a real migration library.
	for _, stmt := range strings.Split(migrationSQL, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := conn.Exec(ctx, stmt); err != nil && !alreadyExists(err) {
			return fmt.Errorf("execute migration statement: %w", err)
		}
	}
	return nil
}

// Create persists a new record and returns it with all fields set.
func (s *Store) Create(ctx context.Context, r *record.Record) (*record.Record, error) {
	if r.Namespace == "" {
		// An unscoped row would be unreachable by every search.
		return nil, errors.New("create record: namespace is required")
	}
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

	// Compute tsvector via bound parameters (not string-concatenated SQL) —
	// see tsvectorExpr's doc comment and the security skill's A05 guidance.
	tagsStr := strings.Join(r.Tags, " ")
	const tsvecArgStart = 21
	tsvec := tsvectorExpr(tsvecArgStart)

	query, args := buildInsert(r, embeddingVec, tagsStr, tsvec)
	err := s.pool.QueryRow(ctx, query, args...).Scan(&r.ID)

	if err != nil {
		return nil, insertError(err)
	}

	return r, nil
}

// Get retrieves a record by ID, or returns ErrNotFound if it does not exist.
func (s *Store) Get(ctx context.Context, id string) (*record.Record, error) {
	r := &record.Record{}

	query := `
		SELECT
			id, kind, title, content, repo, namespace, files, commit_sha, ticket, tags,
			status, deprecation_reason, superseded_by, source, confidence,
			seen_count, used_count, created_at, updated_at, last_used_at, embedding
		FROM records
		WHERE id = $1
	`

	var embeddingVec pgvector.Vector
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&r.ID, (*string)(&r.Kind), &r.Title, &r.Content, &r.Repo, &r.Namespace, &r.Files, &r.CommitSHA, &r.Ticket, &r.Tags,
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
		"title":              true,
		"content":            true,
		"tags":               true,
		"files":              true,
		"ticket":             true,
		"commit_sha":         true,
		"status":             true,
		"confidence":         true,
		"deprecation_reason": true,
		"superseded_by":      true,
		"seen_count":         true,
		"used_count":         true,
		"last_used_at":       true,
		"embedding":          true,
	}

	// Build the SET clause dynamically
	var setClauses []string
	var args []interface{}
	argCount := 1

	// Always update updated_at
	setClauses = append(setClauses, "updated_at = $"+fmt.Sprint(argCount))
	args = append(args, time.Now().UTC())
	argCount++

	// Track the placeholder number assigned to each of title/content, so
	// the tsvector recompute below can bind the NEW value for whichever of
	// them is part of this update (0 means "not part of this update, use
	// the existing column" — see tsvectorUpdateExpr).
	var titleArgIdx, contentArgIdx int

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
		switch col {
		case "title":
			titleArgIdx = argCount
		case "content":
			contentArgIdx = argCount
		}
		argCount++
	}

	// tags is bound to its own column as a []string (above), but the
	// tsvector recompute needs a space-joined string, so bind that
	// separately rather than splicing it into SQL text.
	var tagsArgIdx int
	if tagsVal, hasTags := updates["tags"]; hasTags {
		var tagsStr string
		if tagsSlice, ok := tagsVal.([]string); ok {
			tagsStr = strings.Join(tagsSlice, " ")
		}
		args = append(args, tagsStr)
		tagsArgIdx = argCount
		argCount++
	}

	args = append(args, id)

	// If title, content, or tags were updated, recompute tsvector from the
	// NEW values (bound above) for whichever of them changed, and the
	// existing column for the others — never from a stale pre-update
	// snapshot (AC-5/AC-8).
	if titleArgIdx > 0 || contentArgIdx > 0 || tagsArgIdx > 0 {
		setClauses = append(setClauses, "tsvector_content = "+tsvectorUpdateExpr(titleArgIdx, tagsArgIdx, contentArgIdx))
	}

	query := fmt.Sprintf(`
		UPDATE records
		SET %s
		WHERE id = $%d
		RETURNING id, kind, title, content, repo, namespace, files, commit_sha, ticket, tags,
		          status, deprecation_reason, superseded_by, source, confidence,
		          seen_count, used_count, created_at, updated_at, last_used_at, embedding
	`, strings.Join(setClauses, ", "), argCount)

	r := &record.Record{}
	var embeddingVec pgvector.Vector
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&r.ID, (*string)(&r.Kind), &r.Title, &r.Content, &r.Repo, &r.Namespace, &r.Files, &r.CommitSHA, &r.Ticket, &r.Tags,
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
		// If the hybrid search fails, degrade to full-text-only. Log at warn
		// level: a silent degrade here previously hid a real SQL bug (an
		// undefined-alias error in every hybrid query) behind what looked
		// like a healthy full-text-only fallback.
		slog.WarnContext(ctx, "hybrid search failed; degrading to full-text-only", "error", err)
		return s.searchFullTextOnly(ctx, query, repo, opts, limit)
	}

	result.Records = records
	result.Degraded = false
	return result, nil
}

// FindCandidates fetches the top-N nearest records by vector (cosine)
// similarity. Returns records scoped to the same repo or repo="*".
//
// This port has no query-text parameter (dedup only ever runs off the
// write's own embedding, per the Interface Note in the plan), so ranking
// is vector-only here — unlike Search's RRF fusion. Candidate.Similarity
// is the true cosine similarity (1 - cosine distance via `<=>`, matching
// the records.embedding HNSW index's vector_cosine_ops), which is what
// AC-15's dedup thresholds (0.80/0.92) compare against.
func (s *Store) FindCandidates(ctx context.Context, embedding []float32, namespace, repo string, limit int) ([]*memory.Candidate, error) {
	if limit <= 0 {
		limit = 5
	}

	if len(embedding) == 0 {
		// No embedding; return empty candidates
		return []*memory.Candidate{}, nil
	}

	embeddingVec := pgvector.NewVector(embedding)

	query := `
		SELECT
			id,
			title,
			1.0 / (60 + ROW_NUMBER() OVER (ORDER BY embedding <=> $1)) as rrf_score,
			COALESCE(1.0 - (embedding <=> $1), 0) as similarity
		FROM records
		WHERE (repo = $2 OR repo = '*')
			AND namespace = $4
			AND status IN ('candidate', 'active')
		ORDER BY embedding <=> $1
		LIMIT $3
	`

	rows, err := s.pool.Query(ctx, query, embeddingVec, repo, limit, namespace)
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
	query := "SELECT id, kind, title, content, repo, namespace, files, commit_sha, ticket, tags, status, deprecation_reason, superseded_by, source, confidence, seen_count, used_count, created_at, updated_at, last_used_at, embedding FROM records WHERE 1=1"
	var args []interface{}
	argCount := 1

	if filters.Namespace != nil {
		query += " AND namespace = $" + fmt.Sprint(argCount)
		args = append(args, *filters.Namespace)
		argCount++
	}

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
			&r.ID, (*string)(&r.Kind), &r.Title, &r.Content, &r.Repo, &r.Namespace, &r.Files, &r.CommitSHA, &r.Ticket, &r.Tags,
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

// Delete hard-deletes a record in one transaction. It returns
// *memory.ErrReferenced when another record's superseded_by points at it and
// memory.ErrNotFound when no row matched.
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT id FROM records WHERE superseded_by = $1 AND id <> $1`, id)
	if err != nil {
		return fmt.Errorf("query references: %w", err)
	}
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return fmt.Errorf("scan reference: %w", err)
		}
		refs = append(refs, ref)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("references rows: %w", err)
	}
	if len(refs) > 0 {
		return &memory.ErrReferenced{IDs: refs}
	}

	tag, err := tx.Exec(ctx, `DELETE FROM records WHERE id = $1`, id)
	if err != nil {
		// A reference added after the check above trips the superseded_by FK.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return &memory.ErrReferenced{}
		}
		return fmt.Errorf("delete record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return memory.ErrNotFound
	}
	return tx.Commit(ctx)
}

// DeleteCandidatesByTTL hard-deletes candidate records untouched for longer than ttlDays,
// preserving active and deprecated records regardless of age.
// Returns the number of records deleted. Each deleted row also gets a
// record_deleted event, written by the same statement (the CTE), so the
// returned count equals the events written.
func (s *Store) DeleteCandidatesByTTL(ctx context.Context, ttlDays int) (int, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -ttlDays)

	query := `
		WITH d AS (
			DELETE FROM records
			WHERE status = 'candidate'
				AND GREATEST(COALESCE(created_at, '1970-01-01'),
				             COALESCE(updated_at, '1970-01-01'),
				             COALESCE(last_used_at, '1970-01-01')) < $1
			RETURNING id, namespace
		)
		INSERT INTO events (id, at, namespace, type, record_id, source, via)
		SELECT gen_random_uuid(), now(), d.namespace, 'record_deleted', d.id, 'cleanup', 'ttl'
		FROM d
	`

	result, err := s.pool.Exec(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete by ttl: %w", err)
	}

	return int(result.RowsAffected()), nil
}

// buildInsert builds the records INSERT shared by Store.Create and the
// transaction store's Create. import_key joins the column list only when the
// record carries one, so every non-import write still works on a database
// that has not applied migration 0004 (old schema, or the hook path).
func buildInsert(r *record.Record, embeddingVec pgvector.Vector, tagsStr, tsvec string) (string, []any) {
	args := []any{
		r.ID, string(r.Kind), r.Title, r.Content, r.Repo, r.Files, r.CommitSHA, r.Ticket, r.Tags,
		string(r.Status), r.DeprecationReason, r.SupersededBy, string(r.Source), r.Confidence,
		r.SeenCount, r.UsedCount, r.CreatedAt, r.UpdatedAt, r.LastUsedAt, embeddingVec,
		r.Title, tagsStr, r.Content, r.Namespace,
	}
	extraCol, extraArg := "", ""
	if r.ImportKey != nil {
		args = append(args, *r.ImportKey)
		extraCol, extraArg = ", import_key", fmt.Sprintf(", $%d", len(args))
	}
	query := `
		INSERT INTO records (
			id, kind, title, content, repo, files, commit_sha, ticket, tags,
			status, deprecation_reason, superseded_by, source, confidence,
			seen_count, used_count, created_at, updated_at, last_used_at, embedding, tsvector_content, namespace` + extraCol + `
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20,
			` + tsvec + `, $24` + extraArg + `
		) RETURNING id
	`
	return query, args
}

// insertError wraps a records INSERT error; a unique violation on the import
// key index becomes memory.ErrImportKeyExists.
func insertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_records_import_key" {
		return fmt.Errorf("insert record: %w", memory.ErrImportKeyExists)
	}
	return fmt.Errorf("insert record: %w", err)
}
