-- +goose Up
ALTER TABLE run_executions ADD COLUMN error_text TEXT NOT NULL DEFAULT '';
ALTER TABLE run_executions ADD COLUMN pr_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE run_executions ADD COLUMN pr_url TEXT NOT NULL DEFAULT '';
ALTER TABLE run_executions ADD COLUMN head_sha TEXT NOT NULL DEFAULT '';
ALTER TABLE run_executions ADD COLUMN task_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE run_executions ADD COLUMN reported_at INTEGER;

-- +goose Down
SELECT 1;
