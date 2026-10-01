-- Initialize claude-memory database schema

-- Create extensions if not already present
CREATE EXTENSION IF NOT EXISTS vector;

-- Create schema_migrations table for tracking applied migrations
CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Create the main records table
CREATE TABLE IF NOT EXISTS records (
    id UUID PRIMARY KEY,
    kind VARCHAR(20) NOT NULL,
    title TEXT NOT NULL,
    content TEXT NOT NULL,
    repo TEXT NOT NULL,
    files TEXT[] DEFAULT '{}',
    commit_sha TEXT,
    ticket TEXT,
    tags TEXT[] DEFAULT '{}',
    status VARCHAR(20) NOT NULL,
    deprecation_reason TEXT,
    superseded_by UUID REFERENCES records(id),
    source VARCHAR(20) NOT NULL,
    confidence FLOAT8 NOT NULL,
    seen_count INT DEFAULT 0,
    used_count INT DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_used_at TIMESTAMP,
    embedding VECTOR(1024)
);

-- Create indexes on commonly queried fields
CREATE INDEX IF NOT EXISTS idx_records_repo ON records(repo);
CREATE INDEX IF NOT EXISTS idx_records_status ON records(status);
CREATE INDEX IF NOT EXISTS idx_records_source ON records(source);
CREATE INDEX IF NOT EXISTS idx_records_created_at ON records(created_at);
CREATE INDEX IF NOT EXISTS idx_records_updated_at ON records(updated_at);
CREATE INDEX IF NOT EXISTS idx_records_last_used_at ON records(last_used_at);

-- Create HNSW index for vector similarity search (cosine distance)
CREATE INDEX IF NOT EXISTS idx_records_embedding_hnsw
    ON records USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

-- Create tsvector column for full-text search (computed at insert/update time)
ALTER TABLE records ADD COLUMN IF NOT EXISTS tsvector_content TSVECTOR;

-- Create GIN index on tsvector for full-text search
CREATE INDEX IF NOT EXISTS idx_records_tsvector_gin
    ON records USING gin(tsvector_content);
