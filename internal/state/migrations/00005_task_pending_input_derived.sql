-- +goose Up
ALTER TABLE tasks DROP COLUMN pending_input;

-- +goose Down
ALTER TABLE tasks ADD COLUMN pending_input BOOLEAN NOT NULL DEFAULT 0;
UPDATE tasks
SET pending_input = EXISTS(
  SELECT 1
  FROM runs
  WHERE runs.task_id = tasks.id
    AND runs.status = 'queued'
);
