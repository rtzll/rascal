-- +goose Up
CREATE TABLE IF NOT EXISTS run_leases (
  run_id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  heartbeat_at INTEGER NOT NULL,
  lease_expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_run_leases_expires ON run_leases (lease_expires_at ASC);

-- +goose Down
DROP INDEX IF EXISTS idx_run_leases_expires;
DROP TABLE IF EXISTS run_leases;
