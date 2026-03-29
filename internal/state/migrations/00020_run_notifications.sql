-- +goose Up
CREATE TABLE run_notifications (
  run_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  repo TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  github_comment_id INTEGER,
  posted_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, kind)
);

CREATE INDEX idx_run_notifications_posted_at ON run_notifications (posted_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_run_notifications_posted_at;
DROP TABLE IF EXISTS run_notifications;
