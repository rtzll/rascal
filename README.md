# rascal

Rascal is a self-hosted coding-agent orchestrator.

## Current v1 implementation

- `cmd/rascald`: Orchestrator API server
  - `GET /healthz`
  - `POST /v1/tasks`
  - `GET /v1/tasks/{id}`
  - `POST /v1/tasks/issue`
  - `GET /v1/runs`
  - `GET /v1/runs/{id}`
  - `GET /v1/runs/{id}/logs`
  - `POST /v1/runs/{id}/cancel`
  - `POST /v1/webhooks/github`
- `cmd/rascal`: CLI
  - `bootstrap`
  - `init`
  - `run`
  - `issue`
  - `ps`
  - `logs`
  - `open`
  - `retry` (alias: `rerun`)
  - `cancel`
  - `task`
  - `config`
  - `auth`
  - `doctor`
  - `completion`
- Per-run artifact layout under `RASCAL_DATA_DIR/runs/<run_id>/`
- File-backed state store with atomic writes and webhook delivery idempotency
- Task-level run serialization (one active run per task)
- Runner abstraction with `noop` and `docker` launchers
- Docker runner image scaffold in `runner/`

## Quick start (local)

```bash
go run ./cmd/rascald
```

In another shell:

```bash
export RASCAL_SERVER_URL=http://127.0.0.1:8080
go run ./cmd/rascal doctor
go run ./cmd/rascal run -R OWNER/REPO -t "implement feature"
go run ./cmd/rascal ps
go run ./cmd/rascal logs <run_id>
```

Global UX flags:

- `--output table|json|yaml`
- `--quiet`
- `--config <path>`

## Bootstrap

Configure local CLI and GitHub webhook:

```bash
go run ./cmd/rascal bootstrap \
  --repo OWNER/REPO \
  --domain rascal.example.com \
  --github-token "$GITHUB_TOKEN"
```

Deploy to an existing server over SSH:

```bash
go run ./cmd/rascal bootstrap \
  --repo OWNER/REPO \
  --domain rascal.example.com \
  --github-token "$GITHUB_TOKEN" \
  --host YOUR_SERVER_IP \
  --deploy-existing
```

Generate shell completion scripts:

```bash
go run ./cmd/rascal completion zsh
go run ./cmd/rascal completion bash
```

## Runner image

Build the runner image:

```bash
docker build -t rascal-runner:latest ./runner
```

Then set server env:

```bash
RASCAL_RUNNER_MODE=docker
RASCAL_RUNNER_IMAGE=rascal-runner:latest
```

## Notes

- `RASCAL_RUNNER_MODE` defaults to `noop` for safe local scaffolding.
- Set `RASCAL_API_TOKEN` on server and client for authenticated API access.
- Set `RASCAL_GITHUB_WEBHOOK_SECRET` to enforce webhook signature validation.
- Optionally set `RASCAL_RUNNER_MAX_ATTEMPTS` to retry transient runner failures.
