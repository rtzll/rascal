-- +goose Up
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'open',
  pending_input BOOLEAN NOT NULL DEFAULT 0,
  last_run_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_repo_pr ON tasks (repo, pr_number);

CREATE TABLE IF NOT EXISTS runs (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  task_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  task TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  head_branch TEXT NOT NULL,
  trigger TEXT NOT NULL,
  debug BOOLEAN NOT NULL DEFAULT 1,
  status TEXT NOT NULL,
  run_dir TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  pr_url TEXT NOT NULL DEFAULT '',
  head_sha TEXT NOT NULL DEFAULT '',
  context TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  started_at INTEGER,
  completed_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_runs_status_seq ON runs (status, seq DESC);
CREATE INDEX IF NOT EXISTS idx_runs_task_seq ON runs (task_id, seq DESC);

CREATE TABLE IF NOT EXISTS deliveries (
  id TEXT PRIMARY KEY,
  seen_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_deliveries_seen_at ON deliveries (seen_at ASC);

-- +goose Down
DROP INDEX IF EXISTS idx_deliveries_seen_at;
DROP TABLE IF EXISTS deliveries;
DROP INDEX IF EXISTS idx_runs_task_seq;
DROP INDEX IF EXISTS idx_runs_status_seq;
DROP TABLE IF EXISTS runs;
DROP INDEX IF EXISTS idx_tasks_repo_pr;
DROP TABLE IF EXISTS tasks;
