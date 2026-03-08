-- +goose Up
CREATE TABLE IF NOT EXISTS scheduler_pauses (
  scope TEXT PRIMARY KEY,
  reason TEXT NOT NULL DEFAULT '',
  paused_until INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scheduler_pauses_until ON scheduler_pauses (paused_until ASC);

-- +goose Down
DROP TABLE IF EXISTS scheduler_pauses;
