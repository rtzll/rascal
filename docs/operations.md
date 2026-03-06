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
- `goose.ndjson`: agent-side stream events

## Recovery Patterns

Retry failed/canceled run:

```bash
./bin/rascal retry <run_id>
```

Cancel active run:

```bash
./bin/rascal cancel <run_id>
```

## Goose Session Resume

Rascal can persist Goose session state on disk and resume it across later runs
for the same task/PR, without any background process.

Server env controls:

- `RASCAL_GOOSE_SESSION_MODE=off|pr-only|all` (default: `all`)
- `RASCAL_GOOSE_SESSION_ROOT` (default: `${RASCAL_DATA_DIR}/goose-sessions`)
- `RASCAL_GOOSE_SESSION_TTL_DAYS` (default: `14`, set `0` to disable cleanup)

`pr-only` resumes for iterative PR triggers:

- `pr_comment`
- `pr_review`
- `pr_review_comment`
- `retry`
- `issue_edited` (same task)

To reset a task session manually, delete its directory under
`${RASCAL_GOOSE_SESSION_ROOT}`.

Tradeoff: resume can reduce repeated context rebuilding and token usage, but can
carry stale context. Reset the task session directory when context drift is
suspected.

## Troubleshooting Checklist

1. Run `doctor` first.
2. Confirm server URL and API token in `config view`.
3. Follow logs for run-level errors.
4. Verify webhook health/signature if GitHub triggers fail.
