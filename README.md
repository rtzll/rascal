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

1. Build the CLI:

```bash
go build -o ./bin/rascal ./cmd/rascal
```

2. Copy the local env template and fill in the required tokens:

```bash
cp ./.rascal.env.example ./.rascal.env
```

Then edit `./.rascal.env`:

```bash
HCLOUD_TOKEN=...
GITHUB_ADMIN_TOKEN=...
RASCAL_GITHUB_TOKEN=...
```

3. Bootstrap:

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

- Contributing and local verification: [CONTRIBUTING.md](CONTRIBUTING.md)
- Setup and token details: [docs/setup.md](docs/setup.md)
- Config and precedence: [docs/config.md](docs/config.md)
- Command guide: [docs/commands.md](docs/commands.md)
- Glossary of core terms: [docs/glossary.md](docs/glossary.md)
- Webhooks and Cloudflare notes: [docs/webhooks.md](docs/webhooks.md)
- Operations and troubleshooting: [docs/operations.md](docs/operations.md)
- Operator runbook (failure modes + exact commands): [docs/runbook.md](docs/runbook.md)
- Architecture overview: [docs/architecture.md](docs/architecture.md)
- Deployment flow (blue/green + drain): [docs/deployment.md](docs/deployment.md)
