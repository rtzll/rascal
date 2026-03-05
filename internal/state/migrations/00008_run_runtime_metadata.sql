-- +goose Up
ALTER TABLE runs ADD COLUMN runtime_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN runtime_ref TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 1;
