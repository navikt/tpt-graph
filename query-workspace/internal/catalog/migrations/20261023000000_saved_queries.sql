-- +goose Up
CREATE TABLE saved_queries (
    id text PRIMARY KEY,
    name text NOT NULL,
    category text NOT NULL DEFAULT 'Draft',
    cypher text NOT NULL,
    parameters jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX saved_queries_created_at_idx ON saved_queries (created_at DESC, id);

-- +goose Down
DROP TABLE saved_queries;
