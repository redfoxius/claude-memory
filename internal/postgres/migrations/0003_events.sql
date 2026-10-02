-- Keep the statement separator out of comments: migrations are split on it.
-- Usage and lifecycle events (staleness-metrics, PR B). Ids, enums and
-- numbers only: no title, content, repo, file path, note or query text.
-- The table is append-only and the only delete is the retention prune in
-- cleanup. Named constraints let a later migration add an enum value.
CREATE TABLE IF NOT EXISTS events (
    id            UUID PRIMARY KEY,
    at            TIMESTAMPTZ NOT NULL,
    namespace     TEXT NOT NULL,
    type          VARCHAR(32) NOT NULL
                  CONSTRAINT events_type_check CHECK (type IN ('card_injected', 'feedback',
                      'record_created', 'record_updated', 'record_superseded',
                      'record_deprecated', 'record_promoted', 'record_deleted')),
    record_id     UUID,
    related_id    UUID,
    source        VARCHAR(20)
                  CONSTRAINT events_source_check CHECK (source IN ('inline', 'session', 'pr', 'cleanup')),
    status        VARCHAR(20)
                  CONSTRAINT events_status_check CHECK (status IN ('candidate', 'active', 'deprecated')),
    outcome       VARCHAR(20)
                  CONSTRAINT events_outcome_check CHECK (outcome IN ('useful', 'outdated', 'wrong')),
    via           VARCHAR(20)
                  CONSTRAINT events_via_check CHECK (via IN ('tool', 'feedback', 'seen', 'ttl')),
    similarity    REAL,
    stale         BOOLEAN,
    stale_commits SMALLINT,
    session_id    VARCHAR(64)
);

CREATE INDEX IF NOT EXISTS idx_events_at ON events(at);

CREATE INDEX IF NOT EXISTS idx_events_type_at ON events(type, at);

CREATE INDEX IF NOT EXISTS idx_events_record ON events(record_id)
