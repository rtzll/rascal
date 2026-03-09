-- +goose Up
CREATE TABLE IF NOT EXISTS repositories (
  full_name TEXT PRIMARY KEY,
  webhook_key TEXT NOT NULL UNIQUE,
  enabled BOOLEAN NOT NULL DEFAULT 1,
  encrypted_github_token BLOB NOT NULL,
  encrypted_webhook_secret BLOB NOT NULL,
  agent_backend TEXT NOT NULL DEFAULT '',
  agent_session_mode TEXT NOT NULL DEFAULT '',
  base_branch_override TEXT NOT NULL DEFAULT '',
  max_concurrent_runs INTEGER NOT NULL DEFAULT 0,
  allow_manual BOOLEAN NOT NULL DEFAULT 1,
  allow_issue_label BOOLEAN NOT NULL DEFAULT 1,
  allow_issue_edit BOOLEAN NOT NULL DEFAULT 1,
  allow_pr_comment BOOLEAN NOT NULL DEFAULT 1,
  allow_pr_review BOOLEAN NOT NULL DEFAULT 1,
  allow_pr_review_comment BOOLEAN NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_repositories_enabled ON repositories (enabled, full_name);
CREATE INDEX IF NOT EXISTS idx_repositories_webhook_key ON repositories (webhook_key);

CREATE TABLE IF NOT EXISTS repository_user_roles (
  repo_full_name TEXT NOT NULL,
  user_id TEXT NOT NULL,
  role TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (repo_full_name, user_id),
  FOREIGN KEY(repo_full_name) REFERENCES repositories(full_name) ON DELETE CASCADE,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_repository_user_roles_user_id ON repository_user_roles (user_id, role);

-- +goose Down
DROP INDEX IF EXISTS idx_repository_user_roles_user_id;
DROP TABLE IF EXISTS repository_user_roles;
DROP INDEX IF EXISTS idx_repositories_webhook_key;
DROP INDEX IF EXISTS idx_repositories_enabled;
DROP TABLE IF EXISTS repositories;
