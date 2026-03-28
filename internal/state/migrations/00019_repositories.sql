-- +goose Up
CREATE TABLE repositories (
  full_name TEXT PRIMARY KEY,
  webhook_key TEXT NOT NULL UNIQUE,
  enabled BOOLEAN NOT NULL DEFAULT 1,
  encrypted_webhook_secret BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_repositories_webhook_key ON repositories (webhook_key);

-- +goose Down
DROP INDEX IF EXISTS idx_repositories_webhook_key;
DROP TABLE IF EXISTS repositories;
