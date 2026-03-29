# Setup

## Prerequisites

- Go 1.26+
- Docker available on the server
- Agent auth completed locally (e.g., `~/.codex/auth.json` for Codex, or an
  OAuth token file for Claude Code)
- A GitHub repository you can administer

## Tokens

Rascal uses three values during initial setup.

### Local setup credentials

These are used from your machine while provisioning infrastructure and wiring up
GitHub.

1. `HCLOUD_TOKEN`

- Used locally to provision Hetzner resources.
- Needs read/write API access.
- Create in [Hetzner Cloud Console](https://console.hetzner.cloud/) at
  `Project -> Security -> API Tokens`.
- Docs:
  [Generate an API token](https://docs.hetzner.com/cloud/api/getting-started/generating-api-token)

2. `GITHUB_ADMIN_TOKEN`

- Local-only token for setup tasks (label + webhook management).
- Create a fine-grained PAT at
  [GitHub token settings](https://github.com/settings/personal-access-tokens/new).
- Scope to the target repo.
- Required repository permissions:
  - `Webhooks`: Read and write
  - `Issues`: Read and write

### Server runtime credential

This token is stored on the server and used by Rascal runs after setup
completes.

3. `RASCAL_GITHUB_TOKEN`

- Stored on server for runner operations.
- Scope to the target repo.
- Required repository permissions:
  - `Contents`: Read and write
  - `Pull requests`: Read and write
  - `Issues`: Read and write

Docs:

- [Managing personal access tokens](https://docs.github.com/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)
- [Fine-grained PAT permissions](https://docs.github.com/authentication/keeping-your-account-and-data-secure/creating-a-personal-access-token#permissions)

## Env File

Rascal auto-loads `.rascal.env` from the current working directory.

Start from the committed template:

```bash
cp .rascal.env.example .rascal.env
```

```bash
# Hetzner Cloud API token used locally to provision the server
HCLOUD_TOKEN=...

# GitHub PAT used locally to create/update repo webhooks and labels
GITHUB_ADMIN_TOKEN=...

# GitHub PAT stored on the server; controls what Rascal runs can do in the repo
RASCAL_GITHUB_TOKEN=...
```

You can also set a custom env file via `--env-file` or `RASCAL_ENV_FILE`.

## Paths

### A) Provision + Deploy + Webhook (recommended)

```bash
rascal init --provision \
  --repo OWNER/REPO \
  --domain rascal.example.com
```

### B) Existing Host

```bash
rascal init \
  --repo OWNER/REPO \
  --host YOUR_SERVER_IP \
  --domain rascal.example.com
```

### C) No Domain

You can run Rascal over host IP without a domain.

- CLI-triggered runs work over SSH transport or direct server URL.
- GitHub webhooks can also target an IP URL if publicly reachable, but a stable
  domain + TLS is easier to operate.

## Verify Setup

```bash
rascal doctor --host YOUR_SERVER_IP
rascal config view
```

## Local Verification

Before relying on a new setup, run the local smoke checks:

```bash
make smoke
```

This runs both smoke checks: `smoke-noop` and `smoke-docker`.
The Docker-backed smoke check requires a working local Docker daemon.
