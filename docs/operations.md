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
- `agent.ndjson`: canonical agent-side stream log path for both Goose and Codex runs
- `agent_output.txt`: structured/fallback agent output when the backend writes it

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

- Blue/green still provides readiness-checked cutover, rollback safety, and API/webhook continuity.
- Blue/green is no longer required to keep active runs alive during deploy.
- After deploy, restart, or slot rotation, the active slot should recover and adopt detached run supervision.

## Agent Session Resume

Rascal can persist agent session state on disk and resume it across later runs
for the same task/PR, without any background process.

Server env controls:

- `RASCAL_AGENT_SESSION_MODE=off|pr-only|all` (default: `all`)
- `RASCAL_AGENT_SESSION_ROOT` (default: `${RASCAL_DATA_DIR}/agent-sessions`)
- `RASCAL_AGENT_SESSION_TTL_DAYS` (default: `14`, set `0` to disable cleanup)

Older Goose-specific env names are still accepted as compatibility aliases.

`pr-only` resumes for iterative PR triggers:

- `pr_comment`
- `pr_review`
- `pr_review_comment`
- `retry`
- `issue_edited` (same task)

To reset a task session manually, delete its directory under
`${RASCAL_AGENT_SESSION_ROOT}`.

Tradeoff: resume can reduce repeated context rebuilding and token usage, but can
carry stale context. Reset the task session directory when context drift is
suspected.

Backend notes:

- Goose resumes a named Goose session with a task-scoped mounted session directory.
- Codex resumes by reusing a task-scoped `CODEX_HOME` and the last discovered backend session id.

## Credential Leasing

For Codex runs, Rascal can use a leased stored credential instead of relying
only on the static server auth file.

Operational notes:

- Credential leases are granted per run and renewed while the run is active.
- If lease renewal fails, Rascal requests cancellation of the run.
- Shared credentials can be constrained with `max_active_leases`; personal
  credentials are only available to their owner.
- Stored credential payloads are encrypted at rest in SQLite using
  `RASCAL_CREDENTIAL_ENCRYPTION_KEY`.
- If no stored credential is available, Rascal can fall back to
  `RASCAL_CODEX_AUTH_PATH` when that file exists.

## Troubleshooting Checklist

1. Run `doctor` first.
2. Confirm server URL and API token in `config view`.
3. Follow logs for run-level errors.
4. Verify webhook health/signature if GitHub triggers fail.
