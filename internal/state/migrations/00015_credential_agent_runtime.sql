-- +goose Up
ALTER TABLE codex_credentials ADD COLUMN agent_runtime TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_codex_credentials_runtime ON codex_credentials (agent_runtime);

-- +goose Down
DROP INDEX IF EXISTS idx_codex_credentials_runtime;
-- SQLite cannot drop columns in-place for codex_credentials.agent_runtime.
