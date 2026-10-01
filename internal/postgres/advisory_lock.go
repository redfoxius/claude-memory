package postgres

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

// txStoreImpl implements memory.TxStore for use within WithTx transactions.
type txStoreImpl struct {
	tx pgx.Tx
}

// AcquireLock takes a transaction-scoped advisory lock keyed on repo + titleHash.
// Uses pg_advisory_xact_lock which is automatically released on commit/rollback (AC-16).
func (t *txStoreImpl) AcquireLock(ctx context.Context, repo string, titleHash string) error {
	// Generate a lock ID by hashing repo||':'||titleHash
	hashKey := repo + ":" + titleHash
	lockID := hashFNV(hashKey)

	query := "SELECT pg_advisory_xact_lock($1)"
	if _, err := t.tx.Exec(ctx, query, lockID); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	return nil
}

// FindCandidates is the transaction-scoped version of Store.FindCandidates.
// It fetches top-N nearest records using RRF hybrid ranking within the transaction.
func (t *txStoreImpl) FindCandidates(ctx context.Context, embedding []float32, repo string, limit int) ([]*memory.Candidate, error) {
	if limit <= 0 {
		limit = 5
	}

	if len(embedding) == 0 {
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

	rows, err := t.tx.Query(ctx, query, embeddingVec, repo, query, limit)
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

// Get is the transaction-scoped version of Store.Get.
func (t *txStoreImpl) Get(ctx context.Context, id string) (*record.Record, error) {
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
	err := t.tx.QueryRow(ctx, query, id).Scan(
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

// Create is the transaction-scoped version of Store.Create.
func (t *txStoreImpl) Create(ctx context.Context, r *record.Record) (*record.Record, error) {
	if r.ID == "" {
		return nil, errors.New("create: record ID must be set")
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

	err := t.tx.QueryRow(ctx, query,
		r.ID, string(r.Kind), r.Title, r.Content, r.Repo, r.Files, r.CommitSHA, r.Ticket, r.Tags,
		string(r.Status), r.DeprecationReason, r.SupersededBy, string(r.Source), r.Confidence,
		r.SeenCount, r.UsedCount, r.CreatedAt, r.UpdatedAt, r.LastUsedAt, embeddingVec,
	).Scan(&r.ID)

	if err != nil {
		return nil, fmt.Errorf("insert record: %w", err)
	}

	return r, nil
}

// Update is the transaction-scoped version of Store.Update.
func (t *txStoreImpl) Update(ctx context.Context, id string, updates map[string]interface{}) (*record.Record, error) {
	if len(updates) == 0 {
		return t.Get(ctx, id)
	}

	// Whitelist allowed columns to update (same as Store.Update)
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
	err := t.tx.QueryRow(ctx, query, args...).Scan(
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

// hashFNV computes an FNV-1a hash for use as a Postgres advisory lock ID.
// This ensures that the same repo+titleHash combination always gets the same lock ID.
func hashFNV(key string) int64 {
	hash := fnv.New64a()
	hash.Write([]byte(key))
	return int64(hash.Sum64())
}
