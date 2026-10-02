package postgres

import (
	"context"
	"fmt"
	"time"

	"claude-memory/internal/memory"
)

// StatsNamespaces lists the namespaces `stats` reports on: those with events
// since `since` or with records. Sorted.
func (s *Store) StatsNamespaces(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT namespace FROM events WHERE at >= $1
		UNION
		SELECT namespace FROM records
		ORDER BY 1`, since)
	if err != nil {
		return nil, fmt.Errorf("stats namespaces: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ns string
		if err := rows.Scan(&ns); err != nil {
			return nil, fmt.Errorf("scan namespace: %w", err)
		}
		out = append(out, ns)
	}
	return out, rows.Err()
}

// StatsCounts returns the raw counts of events with at >= since for one
// namespace ("" = all namespaces), plus the current records inventory. Every
// number comes from the events table alone, except the inventory, which has
// no time filter: events.at (timestamptz) is never compared with the
// timestamp-without-zone columns of records. Ratios are computed by the
// caller.
func (s *Store) StatsCounts(ctx context.Context, since time.Time, namespace string) (memory.EventCounts, error) {
	c := memory.EventCounts{
		Feedback:        map[string]int{},
		CreatedBySource: map[string]int{},
		DeprecatedByVia: map[string]int{},
		Inventory:       map[string]map[string]int{},
	}

	// Cards.
	err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT record_id),
		       count(*) FILTER (WHERE stale IS NOT NULL),
		       count(*) FILTER (WHERE stale)
		FROM events
		WHERE type = 'card_injected' AND at >= $1 AND ($2 = '' OR namespace = $2)`,
		since, namespace).Scan(&c.Cards, &c.CardRecords, &c.CardsChecked, &c.CardsStale)
	if err != nil {
		return c, fmt.Errorf("stats cards: %w", err)
	}

	// Useful: a feedback(useful) at or after one of the record's injections.
	err = s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT c.record_id) FILTER (WHERE f.at <= c.at + make_interval(secs => $3)),
		       count(DISTINCT c.record_id)
		FROM events c
		JOIN events f ON f.record_id = c.record_id AND f.type = 'feedback'
		             AND f.outcome = 'useful' AND f.at >= c.at
		WHERE c.type = 'card_injected' AND c.at >= $1 AND ($2 = '' OR c.namespace = $2)`,
		since, namespace, memory.UsefulAttributionWindow.Seconds()).Scan(&c.UsefulWithin, &c.UsefulAny)
	if err != nil {
		return c, fmt.Errorf("stats useful: %w", err)
	}

	// Feedback by outcome, created by source.
	if err := s.countBy(ctx, c.Feedback, `
		SELECT outcome, count(*) FROM events
		WHERE type = 'feedback' AND at >= $1 AND ($2 = '' OR namespace = $2)
		GROUP BY outcome`, since, namespace); err != nil {
		return c, fmt.Errorf("stats feedback: %w", err)
	}
	if err := s.countBy(ctx, c.CreatedBySource, `
		SELECT source, count(*) FROM events
		WHERE type = 'record_created' AND at >= $1 AND ($2 = '' OR namespace = $2)
		GROUP BY source`, since, namespace); err != nil {
		return c, fmt.Errorf("stats created: %w", err)
	}

	// Promotion: candidates created in the window with a later promotion
	// event, imports counted apart so a bulk import reviewed in one pass does
	// not distort the rate.
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE c.source IS DISTINCT FROM 'import'),
		       count(*) FILTER (WHERE c.source IS DISTINCT FROM 'import' AND c.promoted),
		       count(*) FILTER (WHERE c.source = 'import'),
		       count(*) FILTER (WHERE c.source = 'import' AND c.promoted)
		FROM (
			SELECT e.source,
			       EXISTS (SELECT 1 FROM events p
			               WHERE p.type = 'record_promoted' AND p.record_id = e.record_id AND p.at >= e.at) AS promoted
			FROM events e
			WHERE e.type = 'record_created' AND e.status = 'candidate'
			  AND e.at >= $1 AND ($2 = '' OR e.namespace = $2)
		) c`,
		since, namespace).Scan(&c.CandidatesCreated, &c.CandidatesPromoted, &c.ImportCreated, &c.ImportPromoted)
	if err != nil {
		return c, fmt.Errorf("stats promotion: %w", err)
	}

	// Lifecycle: superseded, TTL-deleted, deprecated by via.
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE type = 'record_superseded'),
		       count(*) FILTER (WHERE type = 'record_deleted' AND via = 'ttl')
		FROM events
		WHERE type IN ('record_superseded', 'record_deleted') AND at >= $1 AND ($2 = '' OR namespace = $2)`,
		since, namespace).Scan(&c.Superseded, &c.TTLDeleted)
	if err != nil {
		return c, fmt.Errorf("stats lifecycle: %w", err)
	}
	if err := s.countBy(ctx, c.DeprecatedByVia, `
		SELECT via, count(*) FROM events
		WHERE type = 'record_deprecated' AND at >= $1 AND ($2 = '' OR namespace = $2)
		GROUP BY via`, since, namespace); err != nil {
		return c, fmt.Errorf("stats deprecated: %w", err)
	}

	// Inventory: the records table as it is now.
	rows, err := s.pool.Query(ctx, `
		SELECT source, status, count(*) FROM records
		WHERE $1 = '' OR namespace = $1
		GROUP BY source, status`, namespace)
	if err != nil {
		return c, fmt.Errorf("stats inventory: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var src, status string
		var n int
		if err := rows.Scan(&src, &status, &n); err != nil {
			return c, fmt.Errorf("scan inventory: %w", err)
		}
		if c.Inventory[src] == nil {
			c.Inventory[src] = map[string]int{}
		}
		c.Inventory[src][status] = n
	}
	return c, rows.Err()
}

// countBy runs a two-column (key, count) query into dst.
func (s *Store) countBy(ctx context.Context, dst map[string]int, query string, args ...interface{}) error {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key *string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return err
		}
		if key != nil {
			dst[*key] = n
		}
	}
	return rows.Err()
}
