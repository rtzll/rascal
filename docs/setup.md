# Setup

## Prerequisites

- Go 1.26+
- Docker available on the server
- `codex login` completed locally (`~/.codex/auth.json` exists)
- A GitHub repository you can administer

## Tokens

Rascal uses three tokens for production bootstrap.

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

3. `GITHUB_RUNTIME_TOKEN`

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

Rascal auto-loads `./.rascal.env` for all commands.

```bash
HCLOUD_TOKEN=...
GITHUB_ADMIN_TOKEN=...
GITHUB_RUNTIME_TOKEN=...
```

You can also set a custom env file via `--env-file` or `RASCAL_ENV_FILE`.

## Paths

### A) Provision + Deploy + Webhook (recommended)

```bash
./bin/rascal bootstrap \
  --repo OWNER/REPO \
  --domain rascal.example.com
```

### B) Existing Host

```bash
./bin/rascal bootstrap \
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
./bin/rascal doctor --host YOUR_SERVER_IP
./bin/rascal config view
```
