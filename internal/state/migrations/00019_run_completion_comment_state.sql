-- +goose Up
ALTER TABLE runs ADD COLUMN completion_comment_state TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE runs ADD COLUMN completion_comment_claimed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN completion_comment_claimed_at INTEGER;
ALTER TABLE runs ADD COLUMN completion_comment_posted_at INTEGER;
ALTER TABLE runs ADD COLUMN completion_comment_error TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 1;
