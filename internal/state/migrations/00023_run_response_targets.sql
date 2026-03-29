-- +goose Up
CREATE TABLE run_response_targets (
  run_id TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  requested_by TEXT NOT NULL DEFAULT '',
  trigger TEXT NOT NULL DEFAULT '',
  review_thread_id INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY(run_id) REFERENCES runs(id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE IF EXISTS run_response_targets;
