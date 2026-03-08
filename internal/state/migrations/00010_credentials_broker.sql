-- +goose Up
ALTER TABLE tasks ADD COLUMN created_by_user_id TEXT NOT NULL DEFAULT '';

ALTER TABLE runs ADD COLUMN created_by_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN credential_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_runs_created_by ON runs (created_by_user_id, seq DESC);
CREATE INDEX IF NOT EXISTS idx_runs_credential_id ON runs (credential_id, seq DESC);

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  external_login TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL DEFAULT 'user',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  label TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  disabled_at INTEGER,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys (user_id);

CREATE TABLE IF NOT EXISTS codex_credentials (
  id TEXT PRIMARY KEY,
  owner_user_id TEXT,
  scope TEXT NOT NULL,
  encrypted_auth_blob BLOB NOT NULL,
  weight INTEGER NOT NULL DEFAULT 1,
  max_active_leases INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'active',
  cooldown_until INTEGER,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY(owner_user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_codex_credentials_owner ON codex_credentials (owner_user_id);
CREATE INDEX IF NOT EXISTS idx_codex_credentials_scope_status ON codex_credentials (scope, status, cooldown_until);

CREATE TABLE IF NOT EXISTS credential_leases (
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

CREATE INDEX IF NOT EXISTS idx_credential_leases_credential_active ON credential_leases (credential_id, released_at, expires_at);
CREATE INDEX IF NOT EXISTS idx_credential_leases_run_active ON credential_leases (run_id, released_at, expires_at);
CREATE INDEX IF NOT EXISTS idx_credential_leases_expires_active ON credential_leases (expires_at, released_at);

CREATE TABLE IF NOT EXISTS credential_usage (
  credential_id TEXT NOT NULL,
  window_start INTEGER NOT NULL,
  tokens INTEGER NOT NULL DEFAULT 0,
  runs INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (credential_id, window_start),
  FOREIGN KEY(credential_id) REFERENCES codex_credentials(id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE IF EXISTS credential_usage;
DROP INDEX IF EXISTS idx_credential_leases_expires_active;
DROP INDEX IF EXISTS idx_credential_leases_run_active;
DROP INDEX IF EXISTS idx_credential_leases_credential_active;
DROP TABLE IF EXISTS credential_leases;
DROP INDEX IF EXISTS idx_codex_credentials_scope_status;
DROP INDEX IF EXISTS idx_codex_credentials_owner;
DROP TABLE IF EXISTS codex_credentials;
DROP INDEX IF EXISTS idx_api_keys_user_id;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS users;
DROP INDEX IF EXISTS idx_runs_credential_id;
DROP INDEX IF EXISTS idx_runs_created_by;
-- SQLite cannot drop columns in-place for tasks.created_by_user_id,
-- runs.created_by_user_id, and runs.credential_id.
