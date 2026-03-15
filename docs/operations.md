# Operations

## Health Checks

```bash
./bin/rascal doctor --host YOUR_SERVER_IP
```

`doctor` validates local config, server health, transport choice, and remote
runtime prerequisites.

## Run Statuses

- `queued`: accepted, waiting for execution
- `running`: currently executing in runner
- `review`: run created/updated PR and is waiting for reviewer input
- `succeeded`: finished without requiring feedback
- `failed`: execution failed
- `canceled`: canceled by user or superseded flow

## Live Logs

```bash
./bin/rascal logs <run_id> --follow
```

Log output includes:

- `runner.log`: orchestration, git, push, PR operations
- `agent.ndjson`: canonical agent-side stream log path for both Goose and Codex
  runs
- `agent_output.txt`: structured/fallback agent output when the backend writes
  it

## Recovery Patterns

Retry failed/canceled run:

```bash
./bin/rascal retry <run_id>
```

Cancel active run:

```bash
./bin/rascal cancel <run_id>
```

## Deployment Model

Rascal deploys `rascald` with blue/green slots, but active task execution is
detached into Docker containers.

Operationally this means:

- Blue/green still provides readiness-checked cutover, rollback safety, and
  API/webhook continuity.
- Blue/green is no longer required to keep active runs alive during deploy.
- After deploy, restart, or slot rotation, the active slot should recover and
  adopt detached run supervision.

## Troubleshooting by Layer

| Symptom                                          | Likely layer                           | First checks                                                                                                                                                         |
| ------------------------------------------------ | -------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `rascal` command cannot reach server             | Control plane / transport              | `./bin/rascal config view`, `./bin/rascal doctor --host YOUR_SERVER_IP`                                                                                              |
| Webhook arrives but no run is created            | Control plane / webhook path           | `curl -fsS https://YOUR_DOMAIN/healthz`, `./bin/rascal logs caddy-access --host YOUR_SERVER_IP --follow`, `./bin/rascal logs rascald --host YOUR_SERVER_IP --follow` |
| Run is stuck in `queued`                         | Control plane / scheduler              | `./bin/rascal ps`, `./bin/rascal logs rascald --host YOUR_SERVER_IP --follow`                                                                                        |
| Run is `running` but appears idle                | Execution plane / backend              | `./bin/rascal logs <run_id> --follow`, inspect detached containers on host                                                                                           |
| Cancel does not take effect                      | Execution plane / supervision adoption | `./bin/rascal cancel <run_id>`, `./bin/rascal logs rascald --host YOUR_SERVER_IP --follow`                                                                           |
| Auth failures in Codex runs                      | Credential layer                       | inspect run logs, verify stored credential status and lease availability                                                                                             |
| Deploy succeeds locally but service is unhealthy | Deployment / blue-green cutover        | check active slot, slot readiness, Caddy logs, and rollback readiness                                                                                                |

## First-Response Commands

```bash
./bin/rascal doctor --host YOUR_SERVER_IP
./bin/rascal ps
./bin/rascal config view
./bin/rascal logs rascald --host YOUR_SERVER_IP --lines 200
```

## Agent Session Resume

Rascal can persist agent session state on disk and resume it across later runs
for the same task/PR, without any background process.

Server env controls:

- `RASCAL_TASK_SESSION_MODE=off|pr-only|all` (default: `all`)
- `RASCAL_TASK_SESSION_ROOT` (default: `${RASCAL_DATA_DIR}/agent-sessions`)
- `RASCAL_TASK_SESSION_TTL_DAYS` (default: `14`, set `0` to disable cleanup)

`pr-only` resumes for iterative PR triggers:

- `pr_comment`
- `pr_review`
- `pr_review_comment`
- `pr_review_thread`
- `retry`
- `issue_edited` (same task)

To reset a task session manually, delete its directory under
`${RASCAL_TASK_SESSION_ROOT}`.

Tradeoff: resume can reduce repeated context rebuilding and token usage, but can
carry stale context. Reset the task session directory when context drift is
suspected.

Agent runtime notes:

- Goose resumes a named Goose session with a task-scoped mounted session
  directory.
- Codex resumes by reusing a task-scoped `CODEX_HOME` and the last discovered
  runtime session id.
- Switching a task between Goose and Codex is supported; Rascal clears stale
  task-scoped resume state and starts a fresh session for the new agent runtime.

## Credential Leasing

For Codex runs, Rascal uses leased stored credentials.

Operational notes:

- Credential leases are granted per run and renewed while the run is active.
- If lease renewal fails, Rascal requests cancellation of the run.
- Shared credentials may be reused across concurrent runs; personal credentials
  are only available to their owner.
- Stored credential payloads are encrypted at rest in SQLite using
  `RASCAL_CREDENTIAL_ENCRYPTION_KEY`.
- Manage credentials with `rascal auth credentials ...`.
- `rascal init --codex-auth ...` and `rascal deploy --codex-auth ...` seed or
  update a shared stored credential for the server.

## Safe Manual Interventions

- Restart or inspect the inactive slot during blue/green troubleshooting.
- Inspect detached containers with `docker ps` or `docker inspect`.
- Roll traffic back to a known-good slot by restoring Caddy upstream and
  `active_slot`.
- Retry or cancel runs through Rascal commands.
- Remove stale task session directories when intentionally resetting task
  session resume.
- Change the configured server agent runtime; future runs can migrate existing
  tasks to the new runtime.

## Unsafe Manual Interventions

- Removing active `rascal-*` containers unless you intend to fail the run.
- Editing SQLite state directly on disk.
- Forcing both blue and green slots to process live webhook traffic at once.
- Deleting run directories before finalization completes.

## Troubleshooting Checklist

1. Run `doctor` first.
2. Confirm server URL and API token in `config view`.
3. Follow logs for run-level errors.
4. Verify webhook health/signature if GitHub triggers fail.
5. Identify the failing layer: control plane, execution plane, backend,
   credentials, or deployment.
6. Prefer reversible interventions before deleting containers or artifacts.
