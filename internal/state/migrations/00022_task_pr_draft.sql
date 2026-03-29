-- +goose Up
ALTER TABLE tasks ADD COLUMN pr_draft BOOLEAN NOT NULL DEFAULT 0;

-- +goose Down
SELECT 1;
