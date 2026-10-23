-- +goose Up
CREATE TABLE schema_snapshots (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    target text NOT NULL,
    schema_text text NOT NULL CHECK (octet_length(schema_text) BETWEEN 1 AND 512000),
    schema_hash text NOT NULL,
    discovery_method text NOT NULL,
    captured_at timestamptz NOT NULL
);

CREATE INDEX schema_snapshots_latest_idx ON schema_snapshots (target, captured_at DESC, id DESC);

-- +goose Down
DROP TABLE schema_snapshots;
