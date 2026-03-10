-- +goose Up
CREATE TABLE repositories (
  full_name TEXT NOT NULL PRIMARY KEY,
  webhook_key TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  encrypted_github_token BLOB NOT NULL,
  encrypted_webhook_secret BLOB NOT NULL,
  allow_manual INTEGER NOT NULL DEFAULT 1,
  allow_issue_label INTEGER NOT NULL DEFAULT 1,
  allow_issue_edit INTEGER NOT NULL DEFAULT 1,
  allow_pr_comment INTEGER NOT NULL DEFAULT 1,
  allow_pr_review INTEGER NOT NULL DEFAULT 1,
  allow_pr_review_comment INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_repositories_webhook_key ON repositories (webhook_key);

-- +goose Down
DROP INDEX IF EXISTS idx_repositories_webhook_key;
DROP TABLE IF EXISTS repositories;
