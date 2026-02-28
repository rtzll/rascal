# Rascal — v1 Coding Agent Plan (Goose + Codex CLI on a single VPS)

## Goal

Build **Rascal**, a self-hosted system for spawning autonomous coding agents on a Hetzner VPS.

A single agent run is called a **rascal**. A rascal:

- runs inside an isolated Docker container on the VPS
- clones a target GitHub repository
- applies a task using **goose** configured to use the **Codex CLI provider**
- pushes a branch and creates or updates a PR
- posts progress back to the issue / PR
- can iterate when you comment on the PR until it is merged

This document is scoped to the “few potent commands” UX so someone can be productive immediately, while keeping the internals composable for later expansion.

## Scope for v1

### In scope

- One VPS (Hetzner), Docker-based runner
- Goose CLI using the **Codex CLI provider** (no MCP/extensions)
- GitHub only:
  - trigger via label on issues (and optionally via CLI)
  - iterate via PR comments / review comments
  - results via PR + comments
- Minimal orchestrator service (REST API + webhook receiver + Docker runner)
- Minimal CLI for:
  - bootstrap a server + deploy orchestrator
  - push credentials from laptop to server
  - enable a repo webhook
  - start runs and inspect status/logs
  - implemented with **Cobra + Viper** for maintainable command UX

### Explicitly not in scope (v1)

- Multi-host scaling / queues / autoscaling
- Running goose with API providers
- MCP servers, goose extensions, or “desktop goose” features
- GitHub App auth, multi-tenant auth, SSO enterprise complexity
- Long-term artifact storage beyond local server filesystem

## Path-of-success defaults (critical)

These defaults are chosen so headless automation works reliably and does not hang.

### Goose defaults

- Use CLI provider: `GOOSE_PROVIDER=codex`
- Run fully autonomous inside the sandbox:
  - `GOOSE_MODE=auto` (maps to Codex `--yolo`)
  - Rascal’s safety model is **isolation + scoped credentials**, not interactive approvals.
  - If you ever run agents **outside** the sandbox (e.g., local dev), switch to `GOOSE_MODE=smart_approve`.
- Avoid keyring issues inside containers:
  - `GOOSE_DISABLE_KEYRING=1`
- Avoid extra model call for session naming:
  - `GOOSE_DISABLE_SESSION_NAMING=true`
- Avoid session persistence complexity:
  - run with `goose run --no-session`
- Prefer streaming JSON output for machine logs:
  - `--output-format stream-json`

### Codex auth defaults

- Force Codex to use file-based credential storage so it’s copyable to the VPS:
  - in `~/.codex/config.toml`: `cli_auth_credentials_store = "file"`
- Treat `~/.codex/auth.json` like a password.

### GitHub auth defaults

- Use a token (fine-grained PAT preferred) stored server-side.
- Inject into containers as `GH_TOKEN` environment variable.
- Never require browser auth inside containers.

### CLI defaults

- Use `cobra` for command structure, help text, and shell completions.
- Use `viper` for config loading + env overrides.
- Config source precedence:
  1) explicit CLI flags
  2) environment variables (`RASCAL_*`)
  3) config file (`~/.rascal/config.toml`)
  4) built-in defaults
- Provide built-in completion generation:
  - `rascal completion bash|zsh|fish|powershell`

## UX: a few potent commands

The CLI will have many internal subcommands, but v1 should strongly steer users to a minimal happy path.

### Happy path: one command to get to “working”

1) Bootstrap server + deploy orchestrator + push creds + enable webhook:

    rascal bootstrap \
      --repo OWNER/REPO \
      --hcloud-token $HCLOUD_TOKEN \
      --github-token $GITHUB_TOKEN \
      --domain rascal.example.com

2) Add label `rascal` to any issue in that repo, or:

    rascal issue OWNER/REPO#123

3) Watch progress:

    rascal ps
    rascal logs <run_id>

### CLI command surface (v1)

Top-level commands users should actually need:

- `rascal bootstrap`  
  Provisions (optional), deploys orchestrator, pushes creds, enables webhook.

- `rascal run -R OWNER/REPO -t "task text"`  
  Starts an ad-hoc run.

- `rascal issue OWNER/REPO#123`  
  Starts a run from an issue.

- `rascal ps`  
  Lists runs (and basic status).

- `rascal logs <run_id>`  
  Streams logs (server-side) or prints last N lines.

- `rascal doctor`  
  Validates local + remote setup and prints actionable errors.

- `rascal completion <shell>`  
  Generates shell completion scripts (`bash`, `zsh`, `fish`, `powershell`).

Everything else exists but is “advanced / internal”:

- `rascal infra ...` (provisioning, deploy, TLS)
- `rascal auth ...` (push/rotate creds)
- `rascal repo ...` (enable/disable webhook)

## Architecture

Rascal is split into:

1) **CLI** (runs on laptop)
2) **Orchestrator** service (runs on VPS, behind Caddy)
3) **Runner** (Docker containers spawned per run)

### Data flows

- CLI → Orchestrator: HTTPS REST, authenticated via `RASCAL_API_TOKEN`.
- GitHub → Orchestrator: webhook POST, verified with HMAC secret.
- Orchestrator → Docker: spawn a container per run, mount per-run directories.
- Runner → GitHub: create/update PR, comment, update labels.

### Why mount per-run Goose paths?

You can avoid mounting if you truly want “no persistence”, but v1 should mount a per-run root for:

- deterministic file locations (`GOOSE_PATH_ROOT`)
- post-mortem debugging (stream-json logs, transcripts, tool events)
- concurrency safety (no shared mutable goose state)

Rascal uses per-run mounts by default.

## Component 1: Orchestrator (server)

A single Go binary that:

- exposes REST API for CLI
- receives GitHub webhooks
- persists run/task state to disk
- spawns Docker containers for runs
- streams logs from per-run directories

### Suggested file layout on VPS

- `/opt/rascal/`  
  Deployed app + config

- `/var/lib/rascal/`  
  Persistent state and artifacts
  - `state.json` (or SQLite later)
  - `runs/<run_id>/`
    - `meta.json`
    - `instructions.md`
    - `goose.ndjson`
    - `runner.log`
    - `workspace/` (optional, can be deleted after run)
    - `goose/` (GOOSE_PATH_ROOT mount)
    - `creds/` (per-run credential copy)

- `/etc/rascal/`  
  Secrets/config (root-only)
  - `rascal.env` (systemd env file)
  - `github_webhook_secret`
  - `github_token`
  - `codex_auth.json` (canonical)
  - `rascal_api_token`

### Endpoints (v1)

All endpoints are under `/v1`.

- `GET /healthz`  
  Basic health check

- `GET /v1/runs`  
  List recent runs (id, status, repo, created_at)

- `GET /v1/runs/{id}`  
  Full run details (including links to PR and logs)

- `GET /v1/runs/{id}/logs`  
  Returns server-side logs (runner + goose NDJSON)

- `POST /v1/tasks`  
  Body: `{ repo, task, base_branch? }` → starts a run

- `POST /v1/tasks/issue`  
  Body: `{ repo, issue_number }` → starts a run (fetch issue title/body)

- `POST /v1/webhooks/github`  
  Receives GitHub events:
  - `issues` (labeled/unlabeled)
  - `issue_comment` (PR comments)
  - `pull_request_review` (review feedback)
  - `pull_request` (closed/merged)

  Verifies signature; enqueues internal actions.

### Task/run lifecycle

Use a single “Run” type as the source of truth; “Task” is a grouping.

Run states:

- `queued`
- `running`
- `awaiting_feedback` (PR opened, waiting for human input)
- `succeeded`
- `failed`
- `canceled`

Rules:

- A run is immutable after completion.
- A new PR comment triggers a *new run* linked to the same task.
- Concurrency control:
  - never run two active runs for the same task_id at once
  - if a webhook arrives while running, mark “pending_input=true” and trigger a follow-up run after completion

### State persistence

MVP: `state.json` on disk, rewritten atomically.

- Keep a small index for quick lists:
  - last 200 runs
- Keep full details in per-run directory:
  - meta.json is canonical for run artifact info

### Webhook idempotency

Store `X-GitHub-Delivery` IDs you’ve processed (bounded LRU) in `state.json` so retries don’t create duplicate runs.

### GitHub webhook triggers (v1)

#### Issue labeled: start a task

Trigger: `issues` event, action `labeled`, label name `rascal`.

Action:

- create `task_id = OWNER/REPO#<issue_number>`
- start run with task derived from issue title/body + repo checkout

#### PR comment: iterate

Trigger: `issue_comment` event for a PR (not an issue), action `created`, author != rascal bot user.

Action:

- locate the task linked to that PR (store mapping in meta.json)
- start a new run with:
  - same repo
  - same branch
  - add “feedback” context containing the new comment
- agent updates PR with new commits + posts summary comment

#### PR review feedback: iterate

Trigger: `pull_request_review` event, action `submitted`.

Action:

- same as PR comment iteration, but include review body and state

#### PR merged: complete

Trigger: `pull_request` event, action `closed`, merged = true

Action:

- mark task succeeded, stop accepting new feedback runs

## Component 2: Docker agent image

A Docker image capable of:

- running goose
- running codex CLI (installed inside the image)
- running git + gh
- running tests/build tools needed for typical repos (start small)

### What the image contains (v1)

- `goose` CLI pinned to a known stable version
- `codex` CLI pinned
- `git`, `gh`, `bash`, `jq`, `ripgrep`
- common build deps (keep lean; add as needed):
  - `node`, `python3`, `pip`, `make`, `gcc`, `g++`, `openssl`, `ca-certificates`

### Container mounts

Per run:

- `/rascal-meta` → host `/var/lib/rascal/runs/<run_id>/` (rw)
- `/work` → host `/var/lib/rascal/runs/<run_id>/workspace` (rw) or tmpfs

Recommended: delete `/work` after success, keep `/rascal-meta` for debugging.

### Container environment (minimal)

Required:

- `RASCAL_RUN_ID`
- `RASCAL_TASK_ID`
- `RASCAL_REPO` (OWNER/REPO)
- `RASCAL_BASE_BRANCH` (default `main`)
- `RASCAL_HEAD_BRANCH` (e.g. `rascal/<task_id>/<run_id>`)
- `RASCAL_TRIGGER` (`cli`, `issue_label`, `pr_comment`, `pr_review`)
- `RASCAL_CONTEXT_JSON` (path to a JSON file in /rascal-meta)
- `GH_TOKEN` (GitHub token)

Goose/Codex:

- `GOOSE_PROVIDER=codex`
- `GOOSE_MODEL=gpt-5.2-codex`
- `GOOSE_MODE=auto`
- `GOOSE_DISABLE_KEYRING=1`
- `GOOSE_DISABLE_SESSION_NAMING=true`
- `GOOSE_CONTEXT_STRATEGY=summarize`
- `GOOSE_PATH_ROOT=/rascal-meta/goose`

Codex storage:

- `CODEX_HOME=/rascal-meta/codex`

GitHub headless flags:

- `GH_PROMPT_DISABLED=1`
- `GIT_TERMINAL_PROMPT=0`

### entrypoint.sh (contract)

The entrypoint script must:

1) Write a structured run header to `/rascal-meta/runner.log`
2) Clone repo into `/work/repo` and checkout base branch
3) Create or checkout `RASCAL_HEAD_BRANCH`
4) Generate `/rascal-meta/instructions.md`
5) Run goose in headless mode and capture output:

    goose run --no-session -i /rascal-meta/instructions.md --output-format stream-json > /rascal-meta/goose.ndjson

6) After goose completes:
   - run lightweight verification (configurable):
     - `git status`
     - optional `make test` / `npm test` if repo provides a known command
   - ensure there is at least one commit:
     - if working tree dirty, commit with message `rascal: <task_id> (<run_id>)`
   - push branch
   - ensure PR exists (create if missing)
7) Write `/rascal-meta/meta.json` with:
   - `run_id`, `task_id`, timestamps, exit_code
   - `repo`, `base_branch`, `head_branch`
   - `pr_number`, `pr_url`
   - `head_sha`
8) Exit non-zero if goose failed, or if push/PR failed.

Important: do not rely on goose session resume for continuation. Every iteration is a new run with a fresh instruction file.

### Instruction file contents (v1)

Rascal should generate an instruction file that:

- restates the task in plain language
- includes explicit constraints:
  - do not ask for interactive input
  - do not require MCP tools
  - keep changes minimal and scoped
- includes repo-local guidance files if present:
  - `AGENTS.md`, `CONTRIBUTING.md`, `.goosehints`, `CLAUDE.md`, etc
- defines success criteria:
  - tests pass or explain why not
  - PR has clear summary + checklist

## Component 3: Authentication (simple, token-first)

v1 should avoid trying to forward “browser auth” from laptop to server.

### GitHub token

- Use a **fine-grained** PAT scoped to the single target repository (avoid org-wide tokens).
- Inject as `GH_TOKEN` for headless `gh` usage.

- The GitHub token is stored on the server as the canonical credential.
- It is copied into each run’s per-run `creds/` and injected as `GH_TOKEN`.

Minimum permissions (conceptual):

- ability to:
  - read repo
  - push branch
  - create PR
  - comment on PR / issue
  - create repo webhooks (for bootstrap)

Prefer separate tokens later; v1 can use one.

### Codex credentials

Preferred pattern:

- Login locally on laptop with `codex login`
- Ensure `cli_auth_credentials_store = "file"` so `~/.codex/auth.json` exists
- `rascal bootstrap` securely copies `~/.codex/auth.json` to the server
- Orchestrator copies canonical codex auth → per-run creds → container mount at `CODEX_HOME`

## Component 4: Provisioning / deploy

`rascal bootstrap` composes these internal steps.

### Mode 1: Provision + deploy (Hetzner)

Inputs:

- `HCLOUD_TOKEN`
- SSH public key path (default `~/.ssh/id_ed25519.pub`)
- optional: server type/region/image

Steps:

1) Create VPS, attach firewall (80/443/22 only)
2) Wait for SSH
3) Install Docker, Caddy, systemd unit
4) Deploy orchestrator binary and `rascal.env`
5) Configure Caddy with TLS for `--domain`
6) Start orchestrator

### Mode 2: Deploy to existing server

Inputs:

- `--host`
- `--ssh-user` (default `root`)
- `--ssh-key`

Steps:

- same as deploy steps above (skip provision)

### Webhook setup

During bootstrap (or `rascal repo enable`), CLI should:

- Ensure the `rascal` label exists in the repo (create if needed)
- Create or update webhook:
  - URL: `https://<domain>/v1/webhooks/github`
  - events: issues, issue_comment, pull_request_review, pull_request
  - secret: generated on bootstrap and stored server-side

## Observability (v1)

- Every run has:
  - `/rascal-meta/runner.log`
  - `/rascal-meta/goose.ndjson`
  - `/rascal-meta/meta.json`
- Orchestrator logs to journald and rotates.

CLI UX should make it easy to fetch:

- last N lines of runner log
- last N model events from goose.ndjson
- links to PR

## Security posture (v1)

- Orchestrator API requires `Authorization: Bearer $RASCAL_API_TOKEN`
- Webhooks verify GitHub signature
- Secrets on VPS are root-only (chmod 0700 directory)
- Per-run credential copying prevents runs from mutating canonical creds
- No secrets printed into logs (redact token-like strings)

## Implementation plan (for a coding agent)

This is ordered so a coding agent can start building immediately.

### Milestone 0: Repo skeleton

- `cmd/rascal/` (CLI)
- `cmd/rascald/` (orchestrator)
- `internal/`:
  - `config/`
  - `state/`
  - `runner/` (docker spawn)
  - `github/` (webhook verify + API client)
  - `logs/` (tail/stream)
- `deploy/`:
  - `systemd/rascal.service`
  - `caddy/Caddyfile.tmpl`
  - `scripts/install_docker.sh`

### Milestone 1: Orchestrator MVP (no webhooks yet)

- Implement `/healthz`
- Implement `POST /v1/tasks` → create run dir → spawn container
- Implement `GET /v1/runs`, `GET /v1/runs/{id}`
- Implement `GET /v1/runs/{id}/logs` tailing `/rascal-meta/runner.log`

### Milestone 2: Runner image + entrypoint

- Build Dockerfile
- Implement `entrypoint.sh` contract
- Ensure it produces meta.json even on failure

### Milestone 3: Webhook receiver + idempotency

- Verify signature
- Parse event type + action
- Implement:
  - labeled issue → start run
  - PR comment → start follow-up run
  - PR merged → mark complete
- Store delivery ID LRU in state.json

### Milestone 4: CLI UX

- Build CLI with Cobra command tree and Viper-backed config loading.
- `rascal bootstrap` (initially “deploy to existing server”)
- `rascal run`, `rascal issue`, `rascal ps`, `rascal logs`
- Config file in `~/.rascal/config.toml`:
  - server URL
  - api token
  - default repo
- Add `rascal completion <shell>` and verify completion generation in tests.

### Milestone 5: Hetzner provisioning (optional but desired in v1)

- Add `rascal bootstrap --hcloud-token` provisioning path
- Keep “existing host” mode working

## Roadmap ideas (not commitments)

These are **current explorations / possible avenues** after v1. They are **not** required next steps, and we may change direction based on what v1 teaches us.

- Stronger sandboxing:
  - Firecracker / microVM runners behind the same `Sandbox` interface
  - network egress policies (allowlist GitHub + model endpoints)
- Credentials hardening:
  - GitHub App installation tokens (replace PATs)
  - “credential broker” that injects short‑lived tokens per request instead of storing long‑lived secrets in the sandbox
- More providers:
  - additional goose CLI providers (Claude Code, Gemini CLI)
  - API-provider mode when we want MCP / extensions
- Orchestration:
  - queue + scheduling + per-repo concurrency controls
  - multiple machines / runners
- UX:
  - web UI dashboard
  - artifact retention policies + external storage (e.g., S3)
