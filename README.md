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

## Quickstart

1. Install the CLI so `rascal` is available on your `PATH`:

```bash
go install ./cmd/rascal
```

2. Copy the local env template and fill in the required tokens:

```bash
cp .rascal.env.example .rascal.env
```

Then edit `.rascal.env`:

```bash
# Hetzner Cloud API token used locally to provision the server
HCLOUD_TOKEN=...

# GitHub PAT used locally to create/update repo webhooks and labels
GITHUB_ADMIN_TOKEN=...

# GitHub PAT stored on the server; controls what Rascal runs can do in the repo
RASCAL_GITHUB_TOKEN=...
```

3. Initialize Rascal:

```bash
rascal init --provision \
  --repo OWNER/REPO \
  --domain rascal.example.com
```

4. Verify:

```bash
rascal doctor --host <server_ip>
```

5. Run first task:

```bash
rascal run -t "Add a short CONTRIBUTING.md section for local dev setup"
```

## Core Commands

```bash
rascal run -t "..."
rascal run --issue OWNER/REPO#123
rascal ps
rascal task <task_id>
rascal logs <run_id> --follow
rascal open <run_id>
rascal retry <run_id>
rascal cancel <run_id>
rascal doctor --host <server_ip>
rascal config view
```

## Learn More

- Contributing and local verification: [CONTRIBUTING.md](CONTRIBUTING.md)
- Docs index: [docs/README.md](docs/README.md)
- Setup and token details: [docs/setup.md](docs/setup.md)
- Config and precedence: [docs/config.md](docs/config.md)
- Command guide: [docs/commands.md](docs/commands.md)
