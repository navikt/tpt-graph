-- +goose Up
ALTER TABLE saved_queries ADD COLUMN question TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE saved_queries DROP COLUMN question;
