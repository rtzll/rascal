# Architecture

Rascal has three runtime parts.

## Components

1. `rascal` (CLI)
- Local operator interface.
- Handles setup, config, run creation, logs, and control commands.

2. `rascald` (orchestrator server)
- Receives API requests and GitHub webhooks.
- Persists run/task state.
- Schedules and executes runs serially per task.

3. Runner container (`rascal-runner`)
- Clones repository and checks out target branch.
- Executes Goose/Codex task loop.
- Commits changes, pushes branch, opens/updates PR.

## Flow

1. User triggers run via CLI or GitHub event.
2. `rascald` creates run + task context and queues execution.
3. Runner executes task and writes artifacts/logs.
4. Result is persisted (status, PR URL/number, head SHA).
5. User monitors via `ps`, `logs`, and `open`.

## State

Persistent state is stored on the server in a JSON state file under Rascal data dir.

Each run stores:

- metadata (`run_id`, task, repo, branches, trigger)
- artifacts (`context.json`, instructions, logs, `meta.json`)
