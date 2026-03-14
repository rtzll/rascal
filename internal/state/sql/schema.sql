CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  agent_backend TEXT NOT NULL DEFAULT 'codex',
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  created_by_user_id TEXT NOT NULL DEFAULT '',
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
  agent_backend TEXT NOT NULL DEFAULT 'codex',
  base_branch TEXT NOT NULL,
  head_branch TEXT NOT NULL,
  trigger TEXT NOT NULL,
  debug BOOLEAN NOT NULL DEFAULT 1,
  status TEXT NOT NULL,
  run_dir TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  created_by_user_id TEXT NOT NULL DEFAULT '',
  credential_id TEXT NOT NULL DEFAULT '',
  pr_url TEXT NOT NULL DEFAULT '',
  pr_status TEXT NOT NULL DEFAULT 'none',
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
CREATE INDEX idx_runs_created_by ON runs (created_by_user_id, seq DESC);
CREATE INDEX idx_runs_credential_id ON runs (credential_id, seq DESC);

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

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  external_login TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL DEFAULT 'user',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE api_keys (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  label TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  disabled_at INTEGER,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX idx_api_keys_user_id ON api_keys (user_id);

CREATE TABLE codex_credentials (
  id TEXT PRIMARY KEY,
  owner_user_id TEXT,
  scope TEXT NOT NULL,
  encrypted_auth_blob BLOB NOT NULL,
  weight INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'active',
  cooldown_until INTEGER,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY(owner_user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX idx_codex_credentials_owner ON codex_credentials (owner_user_id);
CREATE INDEX idx_codex_credentials_scope_status ON codex_credentials (scope, status, cooldown_until);

CREATE TABLE credential_leases (
  id TEXT PRIMARY KEY,
  credential_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  strategy TEXT NOT NULL,
  acquired_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  released_at INTEGER,
  FOREIGN KEY(credential_id) REFERENCES codex_credentials(id) ON DELETE CASCADE
);

CREATE INDEX idx_credential_leases_credential_active ON credential_leases (credential_id, released_at, expires_at);
CREATE INDEX idx_credential_leases_run_active ON credential_leases (run_id, released_at, expires_at);
CREATE INDEX idx_credential_leases_expires_active ON credential_leases (expires_at, released_at);

CREATE TABLE credential_usage (
  credential_id TEXT NOT NULL,
  window_start INTEGER NOT NULL,
  tokens INTEGER NOT NULL DEFAULT 0,
  runs INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (credential_id, window_start),
  FOREIGN KEY(credential_id) REFERENCES codex_credentials(id) ON DELETE CASCADE
);

CREATE TABLE run_leases (
  run_id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  heartbeat_at INTEGER NOT NULL,
  lease_expires_at INTEGER NOT NULL
);

CREATE INDEX idx_run_leases_expires ON run_leases (lease_expires_at ASC);

CREATE TABLE run_executions (
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

CREATE UNIQUE INDEX idx_run_executions_container_id ON run_executions (container_id);
CREATE INDEX idx_run_executions_status ON run_executions (status);

CREATE TABLE run_token_usage (
  run_id TEXT PRIMARY KEY,
  backend TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  total_tokens INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER,
  output_tokens INTEGER,
  cached_input_tokens INTEGER,
  reasoning_output_tokens INTEGER,
  raw_usage_json TEXT NOT NULL DEFAULT '',
  captured_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE TABLE run_cancels (
  run_id TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  source TEXT NOT NULL,
  requested_at INTEGER NOT NULL
);

CREATE TABLE scheduler_pauses (
  scope TEXT PRIMARY KEY,
  reason TEXT NOT NULL DEFAULT '',
  paused_until INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX idx_scheduler_pauses_until ON scheduler_pauses (paused_until ASC);

CREATE TABLE campaigns (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  max_concurrent INTEGER NOT NULL DEFAULT 1,
  stop_after_failures INTEGER NOT NULL DEFAULT 1,
  continue_on_failure INTEGER NOT NULL DEFAULT 0,
  skip_if_open_pr INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  started_at INTEGER,
  completed_at INTEGER
);

CREATE INDEX idx_campaigns_state_updated ON campaigns (state, updated_at DESC);

CREATE TABLE campaign_items (
  id TEXT PRIMARY KEY,
  campaign_id TEXT NOT NULL,
  item_order INTEGER NOT NULL,
  repo TEXT NOT NULL,
  task TEXT NOT NULL,
  task_id TEXT NOT NULL DEFAULT '',
  base_branch TEXT NOT NULL DEFAULT '',
  backend_override TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  skip_reason TEXT NOT NULL DEFAULT '',
  failure_reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY(campaign_id) REFERENCES campaigns(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_campaign_items_campaign_order ON campaign_items (campaign_id, item_order);
CREATE INDEX idx_campaign_items_campaign_state ON campaign_items (campaign_id, state, item_order ASC);
CREATE INDEX idx_campaign_items_run_id ON campaign_items (run_id);

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
