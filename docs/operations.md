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

## Troubleshooting Checklist

1. Run `doctor` first.
2. Confirm server URL and API token in `config view`.
3. Follow logs for run-level errors.
4. Verify webhook health/signature if GitHub triggers fail.
