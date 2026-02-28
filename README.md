# Rascal

Rascal is a self-hosted coding-agent runner for GitHub repositories.

It gives you one CLI to provision/deploy the orchestrator, trigger agent runs,
and ship PRs.

## Why Rascal

- Own your runtime: runs execute on your server.
- Keep workflow simple: trigger from CLI or GitHub labels/comments.
- Stay in GitHub-native flow: branch, commit, PR, review.

## Mental Model

`rascal` (CLI) -> `rascald` (orchestrator API) -> runner container -> branch +
PR on GitHub.

## Quickstart (10 Minutes)

2. `GITHUB_ADMIN_TOKEN`
- Local-only token for label/webhook setup.
- Create a [fine-grained PAT](https://github.com/settings/personal-access-tokens/new) scoped to the target repo.
- Fine-grained PAT (single repo) recommended with:
  - `Webhooks`: **Read and write** (list/create/update repository webhooks)
  - `Issues`: **Read and write** (label management)
  - `Metadata`: **Read-only** (usually implicit)

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

You can store secrets in an env file and pass it to bootstrap:

```bash
HCLOUD_TOKEN=...
GITHUB_ADMIN_TOKEN=...
GITHUB_RUNTIME_TOKEN=...
```

Note: `rascal bootstrap` will automatically source `./.rascal.env` if present.  
Rascal auto-loads `./.rascal.env` for all commands (as fallback).  
You can also point to a custom file globally with `--env-file` or `RASCAL_ENV_FILE`.

Note: GitHub does not provide an API to mint user PATs from another PAT, so runtime token creation is manual.

## Quickstart

```bash
./bin/rascal bootstrap \
  --repo OWNER/REPO \
  --domain rascal.example.com
```

4. Verify:

```bash
./bin/rascal doctor --host <server_ip>
```

5. Run first task:

```bash
./bin/rascal run -t "Add a short CONTRIBUTING.md section for local dev setup"
```

## Core Commands

```bash
./bin/rascal run -t "..."
./bin/rascal run --issue OWNER/REPO#123
./bin/rascal ps
./bin/rascal logs <run_id> --follow
./bin/rascal open <run_id>
./bin/rascal retry <run_id>
./bin/rascal cancel <run_id>
./bin/rascal doctor --host <server_ip>
./bin/rascal config view
```

## Learn More

- Setup and token details: [docs/setup.md](docs/setup.md)
- Config and precedence: [docs/config.md](docs/config.md)
- Command guide: [docs/commands.md](docs/commands.md)
- Webhooks and Cloudflare notes: [docs/webhooks.md](docs/webhooks.md)
- Operations and troubleshooting: [docs/operations.md](docs/operations.md)
- Operator runbook (failure modes + exact commands): [docs/runbook.md](docs/runbook.md)
- Architecture overview: [docs/architecture.md](docs/architecture.md)
- Deployment flow (blue/green + drain): [docs/deployment.md](docs/deployment.md)
