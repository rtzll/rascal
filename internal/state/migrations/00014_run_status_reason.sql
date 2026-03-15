-- +goose Up
ALTER TABLE runs ADD COLUMN status_reason TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE runs DROP COLUMN status_reason;
