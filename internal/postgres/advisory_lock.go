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

// AcquireLock takes a transaction-scoped advisory lock keyed on namespace + repo + titleHash.
// Uses pg_advisory_xact_lock which is automatically released on commit/rollback (AC-16).
func (t *txStoreImpl) AcquireLock(ctx context.Context, namespace, repo string, titleHash string) error {
	// Generate a lock ID by hashing namespace||':'||repo||':'||titleHash
	hashKey := namespace + ":" + repo + ":" + titleHash
	lockID := hashFNV(hashKey)

	query := "SELECT pg_advisory_xact_lock($1)"
	if _, err := t.tx.Exec(ctx, query, lockID); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	return nil
}

// FindCandidates is the transaction-scoped version of Store.FindCandidates.
// It fetches top-N nearest records by vector (cosine) similarity within the
// transaction. See Store.FindCandidates for why this is vector-only, not
// RRF-fused with full-text (no query-text parameter on this port).
func (t *txStoreImpl) FindCandidates(ctx context.Context, embedding []float32, namespace, repo string, limit int) ([]*memory.Candidate, error) {
	if limit <= 0 {
		limit = 5
	}

	if len(embedding) == 0 {
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

	rows, err := t.tx.Query(ctx, query, embeddingVec, repo, limit, namespace)
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
			id, kind, title, content, repo, namespace, files, commit_sha, ticket, tags,
			status, deprecation_reason, superseded_by, source, confidence,
			seen_count, used_count, created_at, updated_at, last_used_at, embedding
		FROM records
		WHERE id = $1
	`

	var embeddingVec pgvector.Vector
	err := t.tx.QueryRow(ctx, query, id).Scan(
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

// Create is the transaction-scoped version of Store.Create.
func (t *txStoreImpl) Create(ctx context.Context, r *record.Record) (*record.Record, error) {
	if r.Namespace == "" {
		// An unscoped row would be unreachable by every search.
		return nil, errors.New("create record: namespace is required")
	}
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

	// Compute tsvector via bound parameters (not string-concatenated SQL) —
	// see tsvectorExpr's doc comment and the security skill's A05 guidance.
	tagsStr := strings.Join(r.Tags, " ")
	const tsvecArgStart = 21
	tsvec := tsvectorExpr(tsvecArgStart)

	query := `
		INSERT INTO records (
			id, kind, title, content, repo, files, commit_sha, ticket, tags,
			status, deprecation_reason, superseded_by, source, confidence,
			seen_count, used_count, created_at, updated_at, last_used_at, embedding, tsvector_content, namespace
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20,
			` + tsvec + `, $24
		) RETURNING id
	`

	err := t.tx.QueryRow(ctx, query,
		r.ID, string(r.Kind), r.Title, r.Content, r.Repo, r.Files, r.CommitSHA, r.Ticket, r.Tags,
		string(r.Status), r.DeprecationReason, r.SupersededBy, string(r.Source), r.Confidence,
		r.SeenCount, r.UsedCount, r.CreatedAt, r.UpdatedAt, r.LastUsedAt, embeddingVec,
		r.Title, tagsStr, r.Content, r.Namespace,
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
	err := t.tx.QueryRow(ctx, query, args...).Scan(
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

// hashFNV computes an FNV-1a hash for use as a Postgres advisory lock ID.
// This ensures that the same repo+titleHash combination always gets the same lock ID.
func hashFNV(key string) int64 {
	hash := fnv.New64a()
	hash.Write([]byte(key))
	return int64(hash.Sum64())
}
