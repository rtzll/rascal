# Architecture

Rascal has three runtime parts and a small set of internal abstractions that
keep orchestration, execution, and persistence separate.

## Core Model

The easiest way to read Rascal is to separate control-plane responsibilities
from execution-plane responsibilities.

- Control plane: `rascal` and `rascald`
- Execution plane: detached runner containers launched via the Docker launcher
- Agent backends: `goose` and `codex`
- Packaging: separate runner images for Goose and Codex

In simple terms:

- A Task is the long-lived unit of work.
- A Run is one attempt to advance that task.
- A Task tracks the backend selected for its latest run.
- A Task may have one current backend-specific `AgentSession`.
- A Run uses the backend recorded on that run and may resume the task's session when the backend still matches.
- A detached container is execution state for a run, not the run itself.

This split is important during deploys and restarts:

- Blue/green is control-plane topology for `rascald`.
- Active work keeps running in detached containers in the execution plane.
- After cutover or restart, the active slot recovers and adopts detached run
  supervision.

## High-Level Flow

```text
rascal (CLI) or GitHub webhook
            |
            v
      rascald control plane
  - create/update task
  - create run
  - persist state
  - schedule/supervise
            |
            v
   Docker launcher / execution plane
  - start detached runner container
  - inspect / stop / remove container
            |
            v
   rascal-runner in container
  - clone repo
  - run goose or codex
  - write artifacts and meta.json
  - push branch / update PR
            |
            v
      rascald finalization
  - read artifacts
  - update run/task state
  - post GitHub status/comments
```

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
- It stores runs, tasks, run leases, detached run executions, cancel requests, webhook deliveries, task agent session records, and encrypted stored credentials plus credential leases.
- SQL schema lives in embedded migrations and typed queries are generated under `internal/state/sqlitegen`.

5. Supporting integrations

- `internal/github`: GitHub API and webhook payload handling.
- `internal/runsummary`: PR body and completion comment formatting.
- `internal/logs`: tailing run log files.

## Packaging Model

- Rascal builds and deploys one orchestrator binary: `rascald`.
- Rascal also builds one runner binary: `rascal-runner`.
- That runner binary is packaged into separate Docker images for Goose and Codex.
- `rascald` selects the runner image based on the task/run backend.
- Blue/green deploy replaces the control plane, while runner containers remain detached in the execution plane.

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

## Lifecycle Summary

Task and run lifecycle:

```text
Task created or reused
    |
    +--> Run queued --> Run running --> review | succeeded | failed | canceled
                    |
                    +--> detached RunExecution created and supervised
```

Deploy and recovery lifecycle:

```text
active slot A running
    |
    +--> deploy prepares slot B
    +--> slot B passes readiness
    +--> traffic flips to B
    +--> slot A drains and releases supervision
    +--> slot B adopts detached executions
```

## System Invariants

- A run belongs to exactly one task.
- A run uses the backend recorded on that run.
- A task may have at most one current backend-specific session record.
- Changing a task backend must discard incompatible task-scoped session resume state before the next run starts.
- At most one orchestrator instance should own a run lease at a time.
- `run_executions` store detached execution metadata, not user-visible business progress.
- Only the active slot should process webhook traffic during blue/green overlap.

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
- `codex_credentials`: encrypted stored credential payloads and allocation metadata.
- `credential_leases`: per-run credential assignments and lease expiry state.
- `deliveries`: webhook dedupe/claim bookkeeping.

## Where State Lives

| Location | What lives there | Notes |
| --- | --- | --- |
| SQLite state DB | tasks, runs, leases, execution handles, sessions, credentials | Primary control-plane source of truth |
| Run directory | per-run artifacts, logs, `meta.json`, transient auth material | Short-lived execution artifacts |
| Task session directory | resumable backend session state | Optional and task-scoped |
| Docker runtime | detached runner container process state | Execution-plane state, not the system of record |
| Caddy and systemd config on host | active slot routing and service activation | Deployment/control-plane topology |

## Source of Truth by Object

| Object | Source of truth | Why |
| --- | --- | --- |
| Task | `tasks` table | Durable unit of work across iterations |
| Run | `runs` table | User-visible attempt and final outcome |
| Active supervision owner | `run_leases` table | Coordinates which `rascald` instance supervises |
| Detached container identity | `run_executions` table | Enables adoption and cleanup across restarts |
| Session resume state | `task_agent_sessions` plus mounted session directory | Tracks backend session identity and storage root |
| Run artifacts | run directory on disk | Execution outputs consumed during finalization |
| Live container process | Docker runtime | Actual execution process while the run is active |

## Run Artifacts

Each run directory stores metadata and artifacts such as:

- `context.json`
- `instructions.md`
- `runner.log`
- `agent.ndjson` (canonical agent stream log path for both backends)
- `agent_output.txt` (structured/fallback agent output, especially for Codex)
- `deterministic-checks.json` (deterministic check runs and outputs)
- `commit_message.txt`
- `pr_body.md`
- `review-loop.json` (review loop counts/outcome)
- `review-findings.json` (structured reviewer findings)
- `review-summary.md` (human-readable review summary)
- `meta.json`
- `response_target.json` and completion-comment markers when comment-triggered flows are used

## Session Behavior

- Session policy is configured at the orchestrator via `off`, `pr-only`, or `all`.
- `pr-only` currently resumes for `pr_comment`, `pr_review`, `pr_review_comment`, `retry`, and `issue_edited`.
- Goose resumes by named Goose session plus mounted session storage.
- Codex resumes by reusing a task-scoped `CODEX_HOME` and the discovered backend session id.
- If a task switches backend between runs, Rascal starts a fresh session for the new backend and replaces the stored task session record.
- If a Goose resume attempt fails because the stored session is missing or invalid, the runner falls back to a fresh session.

## Failure and Recovery

Common failure and recovery cases:

- `rascald` restart:
  persisted run execution handles let the restarted process recover and re-adopt detached runs.
- Blue/green deploy during active work:
  detached containers keep running while the new active slot adopts supervision.
- Missing detached container during adoption:
  Rascal marks the run failed because execution disappeared before finalization.
- Lease ownership loss:
  the local instance stops supervision so another instance can take over safely.
- Credential lease renewal failure:
  Rascal requests cancellation and attempts to stop the detached run.
- Cancel during slot rotation:
  cancel intent is persisted, and the active slot after cutover should still stop/finalize the run.

## Credential Handling

Rascal uses stored Codex credentials managed by `rascald`.

- Stored credentials are encrypted before being persisted in SQLite.
- Each credential is either `personal` (owned by a user) or `shared`.
- When a run starts, `rascald` asks the credential broker to lease a credential
  for that run and records the selected credential id in state.
- The broker chooses from eligible credentials using the configured allocation
  strategy and tracks lease assignment per run.
- The leased auth blob is written into the run-scoped `codex/auth.json` file
  and removed during run cleanup.
- While a run is active, `rascald` renews the credential lease. If renewal is
  lost, the run is canceled.
- Bootstrap and deploy can seed an initial shared stored credential from a
  local Codex auth file.

## Runner Environment Contract

Required:

- `RASCAL_RUN_ID`
- `RASCAL_TASK_ID`
- `RASCAL_REPO`
- `GH_TOKEN`

Common optional:

- `RASCAL_TASK`
- `RASCAL_AGENT_BACKEND` (`goose` or `codex`; default normalization falls back to `codex`)
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
- `RASCAL_REVIEW_LOOP_ENABLED` (default: `false`)
- `RASCAL_REVIEW_MAX_INITIAL_PASSES` (default: `1`)
- `RASCAL_REVIEW_MAX_FIX_PASSES` (default: `1`)
- `RASCAL_REVIEW_MAX_VERIFICATION_PASSES` (default: `1`)
- `RASCAL_DETERMINISTIC_CHECK_COMMANDS` (optional `;;` or newline-delimited shell commands)
- `CODEX_HOME` (run-scoped `/rascal-meta/codex` in stateless mode, or task-scoped mount in resume mode for Codex)
- `GOOSE_PATH_ROOT` (run-scoped `/rascal-meta/goose` in stateless mode, or task-scoped mount in resume mode for Goose)

Goose compatibility aliases still accepted by the runner:

- `RASCAL_GOOSE_SESSION_MODE`
- `RASCAL_GOOSE_SESSION_RESUME`
- `RASCAL_GOOSE_SESSION_KEY`
- `RASCAL_GOOSE_SESSION_NAME`

## Further Reading

- [Glossary](glossary.md)
- [Config](config.md)
- [Operations](operations.md)
- [Operator Runbook](runbook.md)
- [Deployment](deployment.md)
