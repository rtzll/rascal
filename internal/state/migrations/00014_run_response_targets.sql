-- +goose Up
ALTER TABLE runs ADD COLUMN response_target_repo TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN response_target_issue_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN response_target_requested_by TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN response_target_trigger TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN response_target_review_thread_id INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE runs DROP COLUMN response_target_review_thread_id;
ALTER TABLE runs DROP COLUMN response_target_trigger;
ALTER TABLE runs DROP COLUMN response_target_requested_by;
ALTER TABLE runs DROP COLUMN response_target_issue_number;
ALTER TABLE runs DROP COLUMN response_target_repo;
