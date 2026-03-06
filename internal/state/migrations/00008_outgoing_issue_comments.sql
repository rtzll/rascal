-- +goose Up
CREATE TABLE outgoing_issue_comments (
  comment_id INTEGER PRIMARY KEY,
  repo TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  run_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

CREATE INDEX idx_outgoing_issue_comments_created_at ON outgoing_issue_comments (created_at ASC);

-- +goose Down
DROP INDEX IF EXISTS idx_outgoing_issue_comments_created_at;
DROP TABLE IF EXISTS outgoing_issue_comments;
