-- +goose Up
CREATE TABLE IF NOT EXISTS run_executions (
  run_id TEXT PRIMARY KEY,
  backend TEXT NOT NULL,
  container_name TEXT NOT NULL,
  container_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'created',
  exit_code INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_observed_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_run_executions_container_id ON run_executions (container_id);
CREATE INDEX IF NOT EXISTS idx_run_executions_status ON run_executions (status);

-- +goose Down
DROP INDEX IF EXISTS idx_run_executions_status;
DROP INDEX IF EXISTS idx_run_executions_container_id;
DROP TABLE IF EXISTS run_executions;
