-- +goose Up
UPDATE runs
SET status = 'review'
WHERE status = 'awaiting_feedback';

-- +goose Down
UPDATE runs
SET status = 'awaiting_feedback'
WHERE status = 'review';
