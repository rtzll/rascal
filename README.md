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

2. Create `./.rascal.env` with required tokens:

```bash
HCLOUD_TOKEN=...
GITHUB_ADMIN_TOKEN=...
GITHUB_RUNTIME_TOKEN=...
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

## Builds

Local builds include version metadata for `rascal` and `rascald`:

```bash
make build
./bin/rascal --version
```

Tagged CLI releases are packaged and published with GoReleaser.

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
