-- +goose Up
ALTER TABLE task_agent_sessions RENAME TO task_sessions;

-- +goose Down
ALTER TABLE task_sessions RENAME TO task_agent_sessions;
