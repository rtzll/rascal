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
- Runtime logic lives in Go (`cmd/rascal-runner`).
- `runner/entrypoint.sh` is a thin shim that only executes `/usr/local/bin/rascal-runner`.

## Flow

1. User triggers run via CLI or GitHub event.
2. `rascald` creates run + task context and queues execution.
3. Runner executes task and writes artifacts/logs.
4. Result is persisted (status, PR URL/number, head SHA).
5. User monitors via `ps`, `logs`, and `open`.

## State

Persistent state is stored on the server in a SQLite database file under Rascal
data dir.

When Goose session persistence is enabled, Rascal also stores task-scoped Goose
session state on disk under `${RASCAL_DATA_DIR}/goose-sessions/<task-key>/`.
Runs stay short-lived; each run mounts this directory, uses it, then exits.
There is no always-on background worker.

Each run stores:

- metadata (`run_id`, task, repo, branches, trigger)
- artifacts (`context.json`, instructions, logs, `meta.json`)

## Runner Env Contract

Required:

- `RASCAL_RUN_ID`
- `RASCAL_TASK_ID`
- `RASCAL_REPO`
- `GH_TOKEN`

Common optional:

- `RASCAL_TASK`
- `RASCAL_BASE_BRANCH` (default: `main`)
- `RASCAL_HEAD_BRANCH` (default: `rascal/<run_id>`)
- `RASCAL_ISSUE_NUMBER` (default: `0`)
- `RASCAL_TRIGGER` (default: `cli`)
- `RASCAL_GOOSE_DEBUG` (default: `true`)
- `RASCAL_META_DIR` (default: `/rascal-meta`)
- `RASCAL_WORK_ROOT` (default: `/work`)
- `RASCAL_REPO_DIR` (default: `${RASCAL_WORK_ROOT}/repo`)
- `RASCAL_GOOSE_SESSION_MODE` (`off`, `pr-only`, `all`; default: `off`)
- `RASCAL_GOOSE_SESSION_RESUME` (set by orchestrator per run)
- `RASCAL_GOOSE_SESSION_KEY` (stable task-scoped key when resume is enabled)
- `RASCAL_GOOSE_SESSION_NAME` (stable Goose session name when resume is enabled)
- `GOOSE_PATH_ROOT` (run-scoped `/rascal-meta/goose` in stateless mode, or task-scoped mount in resume mode)
