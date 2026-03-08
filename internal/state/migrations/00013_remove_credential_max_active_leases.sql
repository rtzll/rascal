-- +goose Up
ALTER TABLE codex_credentials DROP COLUMN max_active_leases;

-- +goose Down
ALTER TABLE codex_credentials ADD COLUMN max_active_leases INTEGER NOT NULL DEFAULT 1;
