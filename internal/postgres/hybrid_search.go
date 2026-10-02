package postgres

import (
	"context"
	"fmt"

	"github.com/pgvector/pgvector-go"

	"claude-memory/internal/memory"
	"claude-memory/internal/record"
)

const rffK = 60 // Reciprocal Rank Fusion constant

// statusTieBreak is the SQL sort key that puts an active record ahead of a
// candidate one when they are otherwise equally relevant (AC-4: "rank
// candidate records lower than active"). It is applied as a secondary sort
// key — inside each RRF component ranking (so identical distance/ts_rank
// gives the active record the better component rank) and in the final
// ORDER BY — never as a multiplier on the fused score.
//
// Why not a multiplier: RRF scores are rank-compressed (1/(60+1) = 0.01639
// vs 1/(60+10) = 0.01429, only ~13% apart), so a 0.85 candidate factor
// (the previous approach) was equivalent to demoting a candidate by ~11
// rank positions — it pushed a candidate with cosine 0.67-0.72 (the
// clearly correct answer) below active records at cosine 0.35-0.50
// (measured on bge-m3, eval paraphrase_005/_006, 2026-10-01).
// Score and Similarity are therefore left unweighted: Score is the plain
// RRF/ts_rank value and Similarity the raw cosine that AC-15/AC-32
// thresholds compare against.
const statusTieBreak = "(CASE WHEN r.status = 'active' THEN 0 ELSE 1 END)"

// searchHybridRRF performs a hybrid search using Reciprocal Rank Fusion
// to combine vector (semantic) and full-text (keyword) ranking.
// The embedding must be non-nil and of length > 0.
// Uses the `<=>` cosine-distance operator (matching the records.embedding
// HNSW index, built WITH vector_cosine_ops) so Similarity = 1 - cosine
// distance is true cosine similarity, and so ANN index lookups are used.
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

	// Base repo scope (same repo or "*"). $3/$4 are the OR-semantics
	// tsquery strings (all terms / identifier-like terms), NULL when empty.
	prompt, idToks := ftsInputs(query)
	args = append(args, embeddingVec, repo, prompt, idToks, sortedStopwords())
	baseArgCount := argCount + 5

	if !opts.IncludeDeprecated {
		whereClause = " AND r.status IN ('candidate', 'active')"
	}

	// Namespace isolation: only the caller's namespace(s) are ever visible.
	whereClause += fmt.Sprintf(" AND r.namespace = ANY($%d)", baseArgCount)
	args = append(args, opts.Namespaces)
	baseArgCount++

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
				ROW_NUMBER() OVER (ORDER BY embedding <=> $1, %s, id) as vector_rank
			FROM records r
			WHERE (repo = $2 OR repo = '*')
				%s
		),
		%s,
		fts_scored AS (
			SELECT
				r.id,
				ts_rank(r.tsvector_content, fts_q.q_all) AS rk,
				ts_rank('{1,1,1,1}', r.tsvector_content, fts_q.q_all) AS rk_flat,
				COALESCE(r.tsvector_content @@ fts_q.q_id, false) AS id_hit,
				%s AS st
			FROM records r, fts_q
			WHERE (r.repo = $2 OR r.repo = '*')
				AND r.tsvector_content @@ fts_q.q_all
				%s
		),
		fts_ranks AS (
			-- OR-noise guard: identifier matches always count; others only when
			-- close to the best match (flat weights; see ftsRelativeFloor).
			SELECT id, ROW_NUMBER() OVER (ORDER BY rk DESC, st, id) AS fts_rank
			FROM fts_scored
			WHERE id_hit OR rk_flat >= %g * (SELECT MAX(rk_flat) FROM fts_scored)
		)
		SELECT
			r.id,
			r.kind,
			r.title,
			r.repo,
			r.namespace,
			r.files,
			r.commit_sha,
			r.tags,
			r.status,
			r.confidence,
			(1.0 / (%d + COALESCE(v.vector_rank, 1e9)) +
			 1.0 / (%d + COALESCE(f.fts_rank, 1e9))) as rrf_score,
			COALESCE(1.0 - (r.embedding <=> $1), 0) as similarity
		FROM records r
		LEFT JOIN vector_ranks v ON r.id = v.id
		LEFT JOIN fts_ranks f ON r.id = f.id
		WHERE (r.repo = $2 OR r.repo = '*')
			%s
			AND (v.id IS NOT NULL OR f.id IS NOT NULL)
		ORDER BY rrf_score DESC, %s, similarity DESC, r.id
		LIMIT $%d
	`, statusTieBreak, whereClause, ftsQueryCTE(3, 4, 5), statusTieBreak, whereClause, ftsRelativeFloor, rffK, rffK, whereClause, statusTieBreak, baseArgCount)

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
		var namespace string
		var files []string
		var commitSHA *string
		var tags []string
		var status string
		var confidence float64
		var score float64
		var similarity float64

		if err := rows.Scan(&id, &kind, &title, &repo, &namespace, &files, &commitSHA, &tags, &status, &confidence, &score, &similarity); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}

		results = append(results, &memory.SearchRecord{
			ID:         id,
			Kind:       record.Kind(kind),
			Title:      title,
			Repo:       repo,
			Namespace:  namespace,
			Files:      files,
			CommitSHA:  derefString(commitSHA),
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

	prompt, idToks := ftsInputs(query)
	args = append(args, prompt, repo, idToks, sortedStopwords())
	baseArgCount := argCount + 4

	// Base repo scope
	if !opts.IncludeDeprecated {
		whereClause = " AND status IN ('candidate', 'active')"
	}

	// Namespace isolation: only the caller's namespace(s) are ever visible.
	whereClause += fmt.Sprintf(" AND namespace = ANY($%d)", baseArgCount)
	args = append(args, opts.Namespaces)
	baseArgCount++

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
		WITH %s,
		scored AS (
			SELECT
				r.id, r.kind, r.title, r.repo, r.namespace, r.files, r.commit_sha, r.tags, r.status, r.confidence,
				ts_rank(r.tsvector_content, fts_q.q_all) AS score,
				ts_rank('{1,1,1,1}', r.tsvector_content, fts_q.q_all) AS rk_flat,
				COALESCE(r.tsvector_content @@ fts_q.q_id, false) AS id_hit,
				%s AS st
			FROM records r, fts_q
			WHERE (r.repo = $2 OR r.repo = '*')
				AND r.tsvector_content @@ fts_q.q_all
				%s
		)
		SELECT id, kind, title, repo, namespace, files, commit_sha, tags, status, confidence, score
		FROM scored
		WHERE id_hit OR rk_flat >= %g * (SELECT MAX(rk_flat) FROM scored)
		ORDER BY score DESC, st, id
		LIMIT $%d
	`, ftsQueryCTE(1, 3, 4), statusTieBreak, whereClause, ftsRelativeFloor, baseArgCount)

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
		var namespace string
		var files []string
		var commitSHA *string
		var tags []string
		var status string
		var confidence float64
		var score float64

		if err := rows.Scan(&id, &kind, &title, &repo, &namespace, &files, &commitSHA, &tags, &status, &confidence, &score); err != nil {
			return nil, fmt.Errorf("scan full-text result: %w", err)
		}

		results = append(results, &memory.SearchRecord{
			ID:         id,
			Kind:       record.Kind(kind),
			Title:      title,
			Repo:       repo,
			Namespace:  namespace,
			Files:      files,
			CommitSHA:  derefString(commitSHA),
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

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
