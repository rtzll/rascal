-- +goose Up
ALTER TABLE runs ADD COLUMN execution_profile TEXT NOT NULL DEFAULT 'default';
ALTER TABLE runs ADD COLUMN admission_decision TEXT NOT NULL DEFAULT 'allow';
ALTER TABLE runs ADD COLUMN admission_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN admission_next_eligible_at INTEGER;

CREATE INDEX IF NOT EXISTS idx_runs_admission_next_eligible ON runs (status, admission_next_eligible_at ASC, seq ASC);

-- +goose Down
DROP INDEX IF EXISTS idx_runs_admission_next_eligible;
ALTER TABLE runs DROP COLUMN admission_next_eligible_at;
ALTER TABLE runs DROP COLUMN admission_reason;
ALTER TABLE runs DROP COLUMN admission_decision;
ALTER TABLE runs DROP COLUMN execution_profile;
