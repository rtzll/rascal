# rascal

Rascal is a self-hosted coding-agent runner for GitHub repositories.

It gives you one command to:
- provision a Hetzner VM (optional)
- deploy `rascald` + runner
- configure GitHub label/webhook
- start and manage autonomous coding runs from CLI

## Prerequisites

- Go 1.26+
- Docker available locally
- `codex login` completed locally (`~/.codex/auth.json` exists)
- A GitHub repo you can administer

## Token setup

Rascal uses three tokens for production bootstrap:

1. `HCLOUD_TOKEN`
- Used locally by `rascal bootstrap` to provision Hetzner hosts.
- Use a **read/write** token (needs to create SSH keys, firewalls, and servers).
- Create it in [Hetzner Cloud Console](https://console.hetzner.cloud/): `Project -> Security -> API Tokens`.
- Docs: [Generate an API token](https://docs.hetzner.com/cloud/api/getting-started/generating-api-token).

2. `GITHUB_ADMIN_TOKEN`
- Local-only token for label/webhook setup.
- Create a [fine-grained PAT](https://github.com/settings/personal-access-tokens/new) scoped to the target repo.
- Fine-grained PAT (single repo) recommended with:
  - `Administration`: **Read and write** (webhooks)
  - `Issues`: **Read and write** (label management)

3. `GITHUB_RUNTIME_TOKEN`
- Stored on server; used by runner for git push + PR/comment workflows.
- Keep this least-privilege compared to admin token.
- Create a separate [fine-grained PAT](https://github.com/settings/personal-access-tokens/new) scoped to the target repo.
- Fine-grained PAT (single repo) recommended with:
  - `Contents`: **Read and write** (clone/push branch)
  - `Pull requests`: **Read and write** (open/update PR)
  - `Issues`: **Read and write** (comments/status messaging)

For GitHub token details:
- [Managing personal access tokens](https://docs.github.com/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)
- [Fine-grained PAT permissions](https://docs.github.com/authentication/keeping-your-account-and-data-secure/creating-a-personal-access-token#permissions)

Note: GitHub does not provide an API to mint user PATs from another PAT, so runtime token creation is manual.

You can store secrets in an env file and pass it to bootstrap:

```bash
# .rascal.env
HCLOUD_TOKEN=...
GITHUB_ADMIN_TOKEN=...
GITHUB_RUNTIME_TOKEN=...
```

Rascal auto-loads `./.rascal.env` for all commands (as fallback).  
You can also point to a custom file globally with `--env-file` or `RASCAL_ENV_FILE`.

## Quickstart

```bash
go run ./cmd/rascal bootstrap \
  --repo OWNER/REPO \
  --domain rascal.example.com
```

This writes local config to `~/.rascal/config.toml` (server URL, API token, repo, host/domain).

## Daily use

```bash
go run ./cmd/rascal issue OWNER/REPO#123
go run ./cmd/rascal ps
go run ./cmd/rascal logs <run_id>
go run ./cmd/rascal open <run_id>
```

Useful commands:

```bash
go run ./cmd/rascal doctor
go run ./cmd/rascal config view
go run ./cmd/rascal completion zsh
```

## Existing host (no provisioning)

```bash
go run ./cmd/rascal bootstrap \
  --repo OWNER/REPO \
  --host YOUR_SERVER_IP \
  --domain rascal.example.com
```

Domain notes:
- Domain is optional for CLI-triggered runs.
- For GitHub webhook triggers, a stable public URL is recommended.
- Without a domain, Rascal can use `http://<server_ip>:8080` if reachable from GitHub.
- Flags still override values from `--env-file`.
