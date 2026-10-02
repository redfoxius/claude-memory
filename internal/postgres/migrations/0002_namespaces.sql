-- Namespaces: isolate records between projects/companies. Existing rows are
-- backfilled to 'acme' via the column default, which is then dropped so
-- every future insert must state its namespace explicitly.
ALTER TABLE records ADD COLUMN IF NOT EXISTS namespace TEXT NOT NULL DEFAULT 'acme';

ALTER TABLE records ALTER COLUMN namespace DROP DEFAULT;

CREATE INDEX IF NOT EXISTS idx_records_namespace_repo ON records(namespace, repo);

CREATE INDEX IF NOT EXISTS idx_records_namespace_status ON records(namespace, status)
