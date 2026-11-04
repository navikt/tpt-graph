-- +goose Up
-- The workspace is a proof of concept: saved queries are recreated for nested folders and
-- query kinds instead of being migrated from categories.
DROP TABLE saved_queries;

CREATE TABLE saved_queries (
    id text PRIMARY KEY,
    name text NOT NULL,
    folder_path text NOT NULL DEFAULT 'Drafts',
    kind text NOT NULL CHECK (kind IN ('question', 'cypher')),
    question text NOT NULL DEFAULT '',
    cypher text NOT NULL,
    parameters jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX saved_queries_created_at_idx ON saved_queries (created_at DESC, id);

-- +goose Down
DROP TABLE saved_queries;

CREATE TABLE saved_queries (
    id text PRIMARY KEY,
    name text NOT NULL,
    category text NOT NULL DEFAULT 'Draft',
    cypher text NOT NULL,
    parameters jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    question text NOT NULL DEFAULT ''
);

CREATE INDEX saved_queries_created_at_idx ON saved_queries (created_at DESC, id);
