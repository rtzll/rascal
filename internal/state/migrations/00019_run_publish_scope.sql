-- +goose Up
ALTER TABLE runs ADD COLUMN publish_scope TEXT NOT NULL DEFAULT 'branch_scoped';
ALTER TABLE runs ADD COLUMN publish_branches TEXT NOT NULL DEFAULT '[]';

-- +goose Down
SELECT 1;
