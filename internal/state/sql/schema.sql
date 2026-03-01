CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'open',
  last_run_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX idx_tasks_repo_pr ON tasks (repo, pr_number);

CREATE TABLE runs (
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

CREATE INDEX idx_runs_status_seq ON runs (status, seq DESC);
CREATE INDEX idx_runs_task_seq ON runs (task_id, seq DESC);

CREATE TABLE run_leases (
  run_id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  heartbeat_at INTEGER NOT NULL,
  lease_expires_at INTEGER NOT NULL
);

CREATE INDEX idx_run_leases_expires ON run_leases (lease_expires_at ASC);

CREATE TABLE run_cancels (
  run_id TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  source TEXT NOT NULL,
  requested_at INTEGER NOT NULL
);

CREATE TABLE deliveries (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL DEFAULT 'processing',
  claim_token TEXT NOT NULL DEFAULT '',
  claimed_by TEXT NOT NULL DEFAULT '',
  claimed_at INTEGER NOT NULL DEFAULT 0,
  processed_at INTEGER,
  seen_at INTEGER NOT NULL,
  last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_deliveries_seen_at ON deliveries (seen_at ASC);
