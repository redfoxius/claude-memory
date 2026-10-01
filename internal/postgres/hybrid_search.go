package postgres

import (
	"context"
	"fmt"

	"github.com/pgvector/pgvector-go"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

const rffK = 60 // Reciprocal Rank Fusion constant

// searchHybridRRF performs a hybrid search using Reciprocal Rank Fusion
// to combine vector (semantic) and full-text (keyword) ranking.
// The embedding must be non-nil and of length > 0.
func (s *Store) searchHybridRRF(
	ctx context.Context,
	query string,
	embedding []float32,
	repo string,
	opts memory.SearchOptions,
	limit int,
) ([]*memory.SearchRecord, error) {
	embeddingVec := pgvector.NewVector(embedding)

	// Build WHERE clause for filters
	var whereClause string
	var args []interface{}
	argCount := 1

	// Base repo scope (same repo or "*")
	args = append(args, embeddingVec, repo, query)
	baseArgCount := argCount + 3

	if !opts.IncludeDeprecated {
		whereClause = " AND r.status IN ('candidate', 'active')"
	}

	if opts.Kind != nil {
		whereClause += fmt.Sprintf(" AND r.kind = $%d", baseArgCount)
		args = append(args, string(*opts.Kind))
		baseArgCount++
	}

	if len(opts.Tags) > 0 {
		// Match any of the requested tags
		whereClause += fmt.Sprintf(" AND r.tags && $%d", baseArgCount)
		args = append(args, opts.Tags)
		baseArgCount++
	}

	sqlQuery := fmt.Sprintf(`
		WITH vector_ranks AS (
			SELECT
				id,
				ROW_NUMBER() OVER (ORDER BY embedding <-> $1) as vector_rank
			FROM records
			WHERE (repo = $2 OR repo = '*')
				%s
		),
		fts_ranks AS (
			SELECT
				id,
				ROW_NUMBER() OVER (ORDER BY ts_rank(tsvector_content, plainto_tsquery('simple', $3)) DESC) as fts_rank
			FROM records
			WHERE (repo = $2 OR repo = '*')
				AND tsvector_content @@ plainto_tsquery('simple', $3)
				%s
		)
		SELECT DISTINCT
			r.id,
			r.kind,
			r.title,
			r.repo,
			r.tags,
			r.status,
			r.confidence,
			(1.0 / (%d + COALESCE(v.vector_rank, 1e9)) +
			 1.0 / (%d + COALESCE(f.fts_rank, 1e9))) as rrf_score,
			(1.0 - (r.embedding <-> $1)) as similarity
		FROM records r
		LEFT JOIN vector_ranks v ON r.id = v.id
		LEFT JOIN fts_ranks f ON r.id = f.id
		WHERE (r.repo = $2 OR r.repo = '*')
			%s
			AND (v.id IS NOT NULL OR f.id IS NOT NULL)
		ORDER BY rrf_score DESC
		LIMIT $%d
	`, whereClause, whereClause, rffK, rffK, whereClause, baseArgCount)

	args = append(args, limit)

	rows, err := s.pool.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("hybrid search query: %w", err)
	}
	defer rows.Close()

	results := []*memory.SearchRecord{}
	for rows.Next() {
		var id string
		var kind string
		var title string
		var repo string
		var tags []string
		var status string
		var confidence float64
		var score float64
		var similarity float64

		if err := rows.Scan(&id, &kind, &title, &repo, &tags, &status, &confidence, &score, &similarity); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}

		results = append(results, &memory.SearchRecord{
			ID:         id,
			Kind:       record.Kind(kind),
			Title:      title,
			Repo:       repo,
			Tags:       tags,
			Status:     record.Status(status),
			Confidence: confidence,
			Score:      score,
			Similarity: similarity,
			Unverified: record.Status(status) == record.StatusCandidate,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search rows error: %w", err)
	}

	return results, nil
}

// searchFullTextOnly performs a full-text-only search when the embedding
// provider is unavailable or the query has no embedding.
// Results have Similarity = 0 (AC-7).
func (s *Store) searchFullTextOnly(
	ctx context.Context,
	query string,
	repo string,
	opts memory.SearchOptions,
	limit int,
) (*memory.SearchResult, error) {
	var args []interface{}
	var whereClause string
	argCount := 1

	args = append(args, query, repo)
	baseArgCount := argCount + 2

	// Base repo scope
	if !opts.IncludeDeprecated {
		whereClause = " AND status IN ('candidate', 'active')"
	}

	if opts.Kind != nil {
		whereClause += fmt.Sprintf(" AND kind = $%d", baseArgCount)
		args = append(args, string(*opts.Kind))
		baseArgCount++
	}

	if len(opts.Tags) > 0 {
		whereClause += fmt.Sprintf(" AND tags && $%d", baseArgCount)
		args = append(args, opts.Tags)
		baseArgCount++
	}

	sqlQuery := fmt.Sprintf(`
		SELECT
			id,
			kind,
			title,
			repo,
			tags,
			status,
			confidence,
			ts_rank(tsvector_content, plainto_tsquery('simple', $1)) as score
		FROM records
		WHERE (repo = $2 OR repo = '*')
			AND tsvector_content @@ plainto_tsquery('simple', $1)
			%s
		ORDER BY score DESC
		LIMIT $%d
	`, whereClause, baseArgCount)

	args = append(args, limit)

	rows, err := s.pool.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("full-text search query: %w", err)
	}
	defer rows.Close()

	results := []*memory.SearchRecord{}
	for rows.Next() {
		var id string
		var kind string
		var title string
		var repo string
		var tags []string
		var status string
		var confidence float64
		var score float64

		if err := rows.Scan(&id, &kind, &title, &repo, &tags, &status, &confidence, &score); err != nil {
			return nil, fmt.Errorf("scan full-text result: %w", err)
		}

		results = append(results, &memory.SearchRecord{
			ID:         id,
			Kind:       record.Kind(kind),
			Title:      title,
			Repo:       repo,
			Tags:       tags,
			Status:     record.Status(status),
			Confidence: confidence,
			Score:      score,
			Similarity: 0, // AC-7: nil embedding → Similarity 0
			Unverified: record.Status(status) == record.StatusCandidate,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("full-text rows error: %w", err)
	}

	return &memory.SearchResult{
		Records:  results,
		Degraded: true,
	}, nil
}
