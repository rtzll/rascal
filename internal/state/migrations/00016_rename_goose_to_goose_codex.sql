-- +goose Up
-- Rename "goose" agent runtime/backend to "goose-codex" in all tables.
UPDATE tasks SET agent_backend = 'goose-codex' WHERE agent_backend = 'goose';
UPDATE runs SET agent_backend = 'goose-codex' WHERE agent_backend = 'goose';
UPDATE task_agent_sessions SET agent_backend = 'goose-codex' WHERE agent_backend = 'goose';
UPDATE codex_credentials SET agent_runtime = 'goose-codex' WHERE agent_runtime = 'goose';

-- +goose Down
UPDATE tasks SET agent_backend = 'goose' WHERE agent_backend = 'goose-codex';
UPDATE runs SET agent_backend = 'goose' WHERE agent_backend = 'goose-codex';
UPDATE task_agent_sessions SET agent_backend = 'goose' WHERE agent_backend = 'goose-codex';
UPDATE codex_credentials SET agent_runtime = 'goose' WHERE agent_runtime = 'goose-codex';
