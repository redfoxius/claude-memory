-- Keep the statement separator out of comments: migrations are split on it.
-- MCP and hook reliability events (mcp-reliability). migrationSQL runs at
-- every service start, so this file must be a no-op on re-run. Statement
-- order matters: the second ADD CONSTRAINT fails with "already exists"
-- (swallowed by the runner) and the DROP is then a no-op. Do not DROP and
-- re-ADD one name. The new constraints are supersets of the old ones, so an
-- older binary keeps working against this schema.
ALTER TABLE events ADD CONSTRAINT events_type_check_v2
    CHECK (type IN ('card_injected', 'feedback',
        'record_created', 'record_updated', 'record_superseded',
        'record_deprecated', 'record_promoted', 'record_deleted',
        'search_called', 'store_attempted'));

ALTER TABLE events DROP CONSTRAINT IF EXISTS events_type_check;

ALTER TABLE events ADD CONSTRAINT events_outcome_check_v2
    CHECK (outcome IN ('useful', 'outdated', 'wrong',
        'ok', 'degraded', 'error',
        'added', 'updated', 'superseded', 'noop', 'needs_judgment'));

ALTER TABLE events DROP CONSTRAINT IF EXISTS events_outcome_check;

ALTER TABLE events ADD CONSTRAINT events_via_check_v2
    CHECK (via IN ('tool', 'feedback', 'seen', 'ttl', 'mcp', 'hook'));

ALTER TABLE events DROP CONSTRAINT IF EXISTS events_via_check;

ALTER TABLE events ADD COLUMN IF NOT EXISTS error_class VARCHAR(24)
    CONSTRAINT events_error_class_check CHECK (error_class IN ('db_unavailable',
        'embedding_unavailable', 'invalid_request', 'timeout', 'internal'))
