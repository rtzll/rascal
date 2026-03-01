-- +goose Up
CREATE TABLE IF NOT EXISTS run_cancels (
  run_id TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  source TEXT NOT NULL,
  requested_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS run_cancels;
