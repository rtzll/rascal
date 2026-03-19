-- +goose Up
-- Rename agent_backend → agent_runtime in tasks.
ALTER TABLE tasks RENAME COLUMN agent_backend TO agent_runtime;

-- Rename agent_backend → agent_runtime in runs.
ALTER TABLE runs RENAME COLUMN agent_backend TO agent_runtime;

-- Rename columns in task_agent_sessions.
ALTER TABLE task_agent_sessions RENAME COLUMN agent_backend TO agent_runtime;
ALTER TABLE task_agent_sessions RENAME COLUMN backend_session_id TO runtime_session_id;

-- Drop old index that references agent_backend.
DROP INDEX IF EXISTS idx_task_agent_sessions_backend_updated;
CREATE INDEX idx_task_agent_sessions_runtime_updated ON task_agent_sessions (agent_runtime, updated_at DESC);

-- Rename codex_credentials → credentials and agent_runtime → provider.
-- SQLite doesn't support renaming tables with FK references cleanly,
-- so we recreate the dependent tables.

-- First drop indexes on codex_credentials.
DROP INDEX IF EXISTS idx_codex_credentials_owner;
DROP INDEX IF EXISTS idx_codex_credentials_scope_status;
DROP INDEX IF EXISTS idx_codex_credentials_runtime;

-- Rename table and column.
ALTER TABLE codex_credentials RENAME TO credentials;
ALTER TABLE credentials RENAME COLUMN agent_runtime TO provider;

-- Recreate indexes with new names.
CREATE INDEX idx_credentials_owner ON credentials (owner_user_id);
CREATE INDEX idx_credentials_scope_status ON credentials (scope, status, cooldown_until);
CREATE INDEX idx_credentials_provider ON credentials (provider);

-- Migrate provider values: "claude" → "anthropic" to match ModelProvider constants.
UPDATE credentials SET provider = 'anthropic' WHERE provider = 'claude';

-- +goose Down
-- Reverse provider value migration.
UPDATE credentials SET provider = 'claude' WHERE provider = 'anthropic';

-- Reverse: rename credentials back to codex_credentials.
DROP INDEX IF EXISTS idx_credentials_owner;
DROP INDEX IF EXISTS idx_credentials_scope_status;
DROP INDEX IF EXISTS idx_credentials_provider;

ALTER TABLE credentials RENAME COLUMN provider TO agent_runtime;
ALTER TABLE credentials RENAME TO codex_credentials;

CREATE INDEX idx_codex_credentials_owner ON codex_credentials (owner_user_id);
CREATE INDEX idx_codex_credentials_scope_status ON codex_credentials (scope, status, cooldown_until);
CREATE INDEX idx_codex_credentials_runtime ON codex_credentials (agent_runtime);

DROP INDEX IF EXISTS idx_task_agent_sessions_runtime_updated;
CREATE INDEX idx_task_agent_sessions_backend_updated ON task_agent_sessions (agent_backend, updated_at DESC);

ALTER TABLE task_agent_sessions RENAME COLUMN runtime_session_id TO backend_session_id;
ALTER TABLE task_agent_sessions RENAME COLUMN agent_runtime TO agent_backend;
ALTER TABLE runs RENAME COLUMN agent_runtime TO agent_backend;
ALTER TABLE tasks RENAME COLUMN agent_runtime TO agent_backend;
