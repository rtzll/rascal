-- +goose Up
ALTER TABLE tasks ADD COLUMN agent_backend TEXT NOT NULL DEFAULT 'codex';

ALTER TABLE runs ADD COLUMN agent_backend TEXT NOT NULL DEFAULT 'codex';

CREATE TABLE task_agent_sessions (
  task_id TEXT PRIMARY KEY,
  agent_backend TEXT NOT NULL,
  backend_session_id TEXT NOT NULL DEFAULT '',
  session_key TEXT NOT NULL DEFAULT '',
  session_root TEXT NOT NULL DEFAULT '',
  last_run_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX idx_task_agent_sessions_backend_updated ON task_agent_sessions (agent_backend, updated_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_task_agent_sessions_backend_updated;
DROP TABLE IF EXISTS task_agent_sessions;
