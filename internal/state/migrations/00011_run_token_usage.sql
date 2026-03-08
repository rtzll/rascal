-- +goose Up
CREATE TABLE IF NOT EXISTS run_token_usage (
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

-- +goose Down
DROP TABLE IF EXISTS run_token_usage;
