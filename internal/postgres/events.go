package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"claude-memory/internal/memory"
)

// eventColumns is the column list of the events table, in INSERT order.
const eventColumns = 14

// Append implements memory.EventSink: one multi-row INSERT, idempotent by
// event id (a re-sent event is ignored). A data or constraint error (SQLSTATE
// class 22 or 23) is returned wrapping memory.ErrEventRejected, so the spool
// drain can retry row by row instead of retrying the same batch forever; any
// other error is returned as is.
func (s *Store) Append(ctx context.Context, evs ...memory.Event) error {
	if len(evs) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(`INSERT INTO events (id, at, namespace, type, record_id, related_id, source, status,
		outcome, via, similarity, stale, stale_commits, session_id) VALUES `)
	args := make([]interface{}, 0, len(evs)*eventColumns)
	for i, e := range evs {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(")
		for c := 0; c < eventColumns; c++ {
			if c > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "$%d", i*eventColumns+c+1)
		}
		sb.WriteString(")")
		args = append(args,
			e.ID, e.At, e.Namespace, string(e.Type),
			nullable(e.RecordID), nullable(e.RelatedID), nullable(string(e.Source)), nullable(string(e.Status)),
			nullable(string(e.Outcome)), nullable(string(e.Via)),
			e.Similarity, e.Stale, e.StaleCommits, nullable(e.SessionID),
		)
	}
	sb.WriteString(" ON CONFLICT (id) DO NOTHING")

	if _, err := s.pool.Exec(ctx, sb.String(), args...); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && len(pgErr.Code) >= 2 && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23") {
			return fmt.Errorf("%w: %v", memory.ErrEventRejected, err)
		}
		return fmt.Errorf("append events: %w", err)
	}
	return nil
}

// nullable maps "" to SQL NULL.
func nullable(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// PruneEvents deletes events older than before and returns how many. It is
// the only delete on the events table (retention, run by cleanup).
func (s *Store) PruneEvents(ctx context.Context, before time.Time) (int, error) {
	res, err := s.pool.Exec(ctx, `DELETE FROM events WHERE at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("prune events: %w", err)
	}
	return int(res.RowsAffected()), nil
}
