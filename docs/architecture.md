# Architecture

Rascal has three runtime parts and a small set of internal abstractions that
keep orchestration, execution, and persistence separate.

## Runtime Components

1. `rascal` (CLI)

- Local operator interface.
- Handles bootstrap, deploy, config, run creation, logs, and control commands.
- Lives in `cmd/rascal`.

2. `rascald` (orchestrator server)

- Receives API requests and GitHub webhooks.
- Persists task, run, lease, cancellation, and detached execution state.
- Schedules runs serially per task and concurrently across different tasks.
- Starts detached runner containers and supervises them via persisted execution handles.
- Supports blue/green slot handoff by letting a new slot adopt detached execution supervision.
- Lives in `cmd/rascald`.

3. Runner container (`rascal-runner`)

- Clones the repository and checks out the target branches.
- Executes the selected agent backend (`goose` or `codex`).
- Commits changes, pushes the head branch, and creates or reuses a PR.
- Writes canonical artifacts into mounted `/rascal-meta`.
- Runtime logic lives in Go in `cmd/rascal-runner`.
- `runner/entrypoint.sh` is a thin shim that only executes `/usr/local/bin/rascal-runner`.

## Code-Level Abstractions

These are the main layers in the Go codebase.

1. Entry points

- `cmd/rascal`: operator-facing CLI.
- `cmd/rascald`: HTTP API, webhook handling, scheduling, supervision, recovery.
- `cmd/rascal-runner`: in-container task executor.

2. Agent abstraction

- `internal/agent` defines backend normalization (`goose`, `codex`) and session policy (`off`, `pr-only`, `all`).
- This keeps backend/session decisions out of the higher-level orchestrator flow.

3. Execution abstraction

- `internal/runner` defines the `Launcher` interface and `Spec`/`ExecutionHandle` contract.
- Current production implementation is Docker; `noop` exists for non-runtime/test scenarios.
- Session mounting is backend-aware: Goose uses `GOOSE_PATH_ROOT`, Codex uses `CODEX_HOME`.

4. Persistence abstraction

- `internal/state` owns SQLite-backed persistence and state transitions.
- It stores runs, tasks, run leases, detached run executions, cancel requests, webhook deliveries, and task agent session records.
- SQL schema lives in embedded migrations and typed queries are generated under `internal/state/sqlitegen`.

5. Supporting integrations

- `internal/github`: GitHub API and webhook payload handling.
- `internal/runsummary`: PR body and completion comment formatting.
- `internal/logs`: tailing run log files.

## Execution Flow

1. User triggers a run from the CLI or via GitHub webhook.
2. `rascald` creates or updates task context, writes run artifacts, and queues the run.
3. Scheduler claims a queued run, enforces per-task serialization, and records a run lease.
4. `rascald` resolves backend/session settings and persists a deterministic detached execution handle.
5. `internal/runner` starts a detached Docker container for `rascal-runner`.
6. Active slot supervises the detached execution by inspect/stop/remove operations and lease heartbeats.
7. On slot rotation or process restart, a new slot can recover the persisted handle and adopt supervision.
8. `rascal-runner` finalizes `meta.json`; `rascald` reads that artifact, updates run/task state, posts GitHub reactions/comments, and removes the container.
9. User monitors via `ps`, `logs`, and `open`.

## Persistence Model

Persistent state is stored on the server in a SQLite database under the Rascal
data directory.

By default, task-scoped agent session state is also stored on disk under
`${RASCAL_DATA_DIR}/agent-sessions/<task-key>/`. Older Goose-specific env names
are still accepted as compatibility aliases, but the current abstraction is
agent-backend-agnostic.

Runs stay short-lived. Each run mounts its run directory plus, when session
resume is enabled, a task-scoped session directory. There is no always-on
background worker.

Key persisted entities:

- `runs`: user-visible execution records and final outcome.
- `tasks`: long-lived task identity across retries and follow-up feedback.
- `run_leases`: supervision ownership and heartbeat expiry.
- `run_executions`: detached execution handle metadata for adoption and cleanup.
- `run_cancels`: persisted cancel intent.
- `task_agent_sessions`: stable backend session identifiers and mounted session roots.
- `deliveries`: webhook dedupe/claim bookkeeping.

## Run Artifacts

Each run directory stores metadata and artifacts such as:

- `context.json`
- `instructions.md`
- `runner.log`
- `goose.ndjson` (canonical agent stream log path for both backends)
- `agent_output.txt` (structured/fallback agent output, especially for Codex)
- `commit_message.txt`
- `pr_body.md`
- `meta.json`
- `response_target.json` and completion-comment markers when comment-triggered flows are used

## Session Behavior

- Session policy is configured at the orchestrator via `off`, `pr-only`, or `all`.
- `pr-only` currently resumes for `pr_comment`, `pr_review`, `pr_review_comment`, `retry`, and `issue_edited`.
- Goose resumes by named Goose session plus mounted session storage.
- Codex resumes by reusing a task-scoped `CODEX_HOME` and the discovered backend session id.
- If a Goose resume attempt fails because the stored session is missing or invalid, the runner falls back to a fresh session.

## Runner Environment Contract

Required:

- `RASCAL_RUN_ID`
- `RASCAL_TASK_ID`
- `RASCAL_REPO`
- `GH_TOKEN`

Common optional:

- `RASCAL_TASK`
- `RASCAL_AGENT_BACKEND` (`goose` or `codex`; default normalization falls back to `goose`)
- `RASCAL_BASE_BRANCH` (default: `main`)
- `RASCAL_HEAD_BRANCH` (default: `rascal/<run_id>`)
- `RASCAL_ISSUE_NUMBER` (default: `0`)
- `RASCAL_PR_NUMBER` (default: `0`)
- `RASCAL_TRIGGER` (default: `cli`)
- `RASCAL_GOOSE_DEBUG` (default: `true`)
- `RASCAL_CONTEXT`
- `RASCAL_META_DIR` (default: `/rascal-meta`)
- `RASCAL_WORK_ROOT` (default: `/work`)
- `RASCAL_REPO_DIR` (default: `${RASCAL_WORK_ROOT}/repo`)
- `RASCAL_AGENT_SESSION_MODE` (`off`, `pr-only`, `all`; orchestrator default: `all`)
- `RASCAL_AGENT_SESSION_RESUME` (set by orchestrator per run)
- `RASCAL_AGENT_SESSION_KEY` (stable task-scoped key when resume is enabled)
- `RASCAL_AGENT_SESSION_ID` (backend session id when known)
- `CODEX_HOME` (run-scoped `/rascal-meta/codex` in stateless mode, or task-scoped mount in resume mode for Codex)
- `GOOSE_PATH_ROOT` (run-scoped `/rascal-meta/goose` in stateless mode, or task-scoped mount in resume mode for Goose)

Goose compatibility aliases still accepted by the runner:

- `RASCAL_GOOSE_SESSION_MODE`
- `RASCAL_GOOSE_SESSION_RESUME`
- `RASCAL_GOOSE_SESSION_KEY`
- `RASCAL_GOOSE_SESSION_NAME`
