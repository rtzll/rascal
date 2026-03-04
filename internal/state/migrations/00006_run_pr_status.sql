-- +goose Up
ALTER TABLE runs ADD COLUMN pr_status TEXT NOT NULL DEFAULT 'none';

UPDATE runs
SET pr_status = 'open'
WHERE pr_number > 0
  AND status = 'awaiting_feedback';

-- +goose Down
ALTER TABLE runs DROP COLUMN pr_status;
