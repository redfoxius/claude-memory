-- Keep the statement separator out of comments: migrations are split on it.
-- Management CLI import (mgmt-cli PR 2). migrationSQL runs at every service
-- start, so this file must be a no-op on re-run. Statement order matters:
-- the second ADD CONSTRAINT fails with "already exists" (swallowed by the
-- runner) and the DROP is then a no-op. Do not DROP and re-ADD one name.
ALTER TABLE events ADD CONSTRAINT events_source_check_v2
    CHECK (source IN ('inline', 'session', 'pr', 'cleanup', 'import'));

ALTER TABLE events DROP CONSTRAINT IF EXISTS events_source_check;

ALTER TABLE records ADD COLUMN IF NOT EXISTS import_key TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_records_import_key
    ON records(namespace, import_key) WHERE import_key IS NOT NULL
