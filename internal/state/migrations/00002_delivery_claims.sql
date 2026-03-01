-- +goose Up
ALTER TABLE deliveries ADD COLUMN status TEXT NOT NULL DEFAULT 'processing';
ALTER TABLE deliveries ADD COLUMN claim_token TEXT NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN claimed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN claimed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deliveries ADD COLUMN processed_at INTEGER;
ALTER TABLE deliveries ADD COLUMN last_error TEXT NOT NULL DEFAULT '';

UPDATE deliveries
SET
  status = 'processed',
  processed_at = seen_at,
  claim_token = '',
  claimed_by = '',
  claimed_at = seen_at,
  last_error = ''
WHERE status = 'processing';

-- +goose Down
-- SQLite cannot drop columns in-place. Keep the additional columns.
