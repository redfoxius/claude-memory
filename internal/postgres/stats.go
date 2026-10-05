package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

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
		Reliability:     memory.NewReliabilityCounts(),
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

	if err := s.reliabilityCounts(ctx, &c.Reliability, since, namespace); err != nil {
		return c, err
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

// reliabilityCounts fills the search_called / store_attempted counts. DB-down
// hours are distinct UTC clock hours (by event time) with a db_unavailable
// event.
func (s *Store) reliabilityCounts(ctx context.Context, r *memory.ReliabilityCounts, since time.Time, namespace string) error {
	rows, err := s.pool.Query(ctx, `
		SELECT via, outcome, count(*) FROM events
		WHERE type = 'search_called' AND at >= $1 AND ($2 = '' OR namespace = $2)
		GROUP BY via, outcome`, since, namespace)
	if err != nil {
		return fmt.Errorf("stats searches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var via, outcome string
		var n int
		if err := rows.Scan(&via, &outcome, &n); err != nil {
			return fmt.Errorf("scan searches: %w", err)
		}
		if r.Search[via] == nil {
			r.Search[via] = map[string]int{}
		}
		r.Search[via][outcome] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("stats searches: %w", err)
	}
	rows.Close()

	if err := s.countBy(ctx, r.Store, `
		SELECT outcome, count(*) FROM events
		WHERE type = 'store_attempted' AND at >= $1 AND ($2 = '' OR namespace = $2)
		GROUP BY outcome`, since, namespace); err != nil {
		return fmt.Errorf("stats store attempts: %w", err)
	}
	if err := s.countBy(ctx, r.Failures, `
		SELECT error_class, count(*) FROM events
		WHERE type IN ('search_called', 'store_attempted') AND error_class IS NOT NULL
		  AND at >= $1 AND ($2 = '' OR namespace = $2)
		GROUP BY error_class`, since, namespace); err != nil {
		return fmt.Errorf("stats failures: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		SELECT min(at), max(at)
		FROM events
		WHERE type IN ('search_called', 'store_attempted') AND error_class = 'db_unavailable'
		  AND at >= $1 AND ($2 = '' OR namespace = $2)`,
		since, namespace).Scan(&r.FirstDown, &r.LastDown)
	if err != nil {
		return fmt.Errorf("stats db-down window: %w", err)
	}
	hours, err := s.pool.Query(ctx, `
		SELECT DISTINCT date_trunc('hour', at AT TIME ZONE 'UTC') FROM events
		WHERE type IN ('search_called', 'store_attempted') AND error_class = 'db_unavailable'
		  AND at >= $1 AND ($2 = '' OR namespace = $2)
		ORDER BY 1`, since, namespace)
	if err != nil {
		return fmt.Errorf("stats db-down hours: %w", err)
	}
	defer hours.Close()
	for hours.Next() {
		var h time.Time
		if err := hours.Scan(&h); err != nil {
			return fmt.Errorf("scan db-down hour: %w", err)
		}
		r.DownHours = append(r.DownHours, h.UTC())
	}
	r.DBDownHours = len(r.DownHours)
	return hours.Err()
}

// StatsLatestServeSession returns the session id of the newest mcp
// reliability event with at >= since and that event's time ("" when none).
func (s *Store) StatsLatestServeSession(ctx context.Context, since time.Time) (string, time.Time, error) {
	var id string
	var at time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT session_id, at FROM events
		WHERE type IN ('search_called', 'store_attempted') AND via = 'mcp'
		  AND session_id IS NOT NULL AND at >= $1
		ORDER BY at DESC LIMIT 1`, since).Scan(&id, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("stats latest serve session: %w", err)
	}
	return id, at, nil
}

// StatsSessionCounts counts the mcp reliability events of one serve session
// with at >= since.
func (s *Store) StatsSessionCounts(ctx context.Context, since time.Time, sessionID string) (memory.SessionCounts, error) {
	c := memory.SessionCounts{ID: sessionID}
	err := s.pool.QueryRow(ctx, `
		SELECT min(at), max(at),
		       count(*) FILTER (WHERE type = 'search_called'),
		       count(*) FILTER (WHERE type = 'store_attempted'),
		       count(*) FILTER (WHERE error_class IS NOT NULL)
		FROM events
		WHERE type IN ('search_called', 'store_attempted') AND via = 'mcp'
		  AND session_id = $1 AND at >= $2`,
		sessionID, since).Scan(&c.First, &c.Last, &c.Searches, &c.Stores, &c.Failures)
	if err != nil {
		return c, fmt.Errorf("stats session counts: %w", err)
	}
	return c, nil
}

// EventIDsExist returns which of ids are already in the events table.
func (s *Store) EventIDsExist(ctx context.Context, ids []string) (map[string]bool, error) {
	found := map[string]bool{}
	if len(ids) == 0 {
		return found, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM events WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("event ids exist: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan event id: %w", err)
		}
		found[id] = true
	}
	return found, rows.Err()
}
