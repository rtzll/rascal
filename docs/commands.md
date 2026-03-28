# Commands

## Setup

```bash
rascal init --provision --repo OWNER/REPO --domain rascal.example.com
rascal deploy --host YOUR_SERVER_IP
rascal doctor --host YOUR_SERVER_IP
rascal config view
```

## Start Runs

Ad-hoc task:

```bash
rascal run -t "Fix flaky test in cmd package"
```

Issue-driven task:

```bash
rascal run --issue OWNER/REPO#123
```

## Monitor Runs

```bash
rascal ps
rascal ps --limit 10
rascal ps --all
rascal task <task_id>
rascal logs <run_id> --follow
rascal logs run <run_id> --follow
rascal logs rascald --follow
rascal logs caddy --follow
rascal logs caddy-access --follow
rascal open <run_id>
```

## Control Runs

```bash
rascal retry <run_id>
rascal cancel <run_id>
```

## GitHub Integrations

```bash
rascal github status OWNER/REPO
rascal github setup OWNER/REPO --github-admin-token "$GITHUB_ADMIN_TOKEN" --webhook-secret "$RASCAL_GITHUB_WEBHOOK_SECRET"
rascal github disable OWNER/REPO --github-admin-token "$GITHUB_ADMIN_TOKEN"
rascal github webhook test --repo OWNER/REPO --webhook-secret "$RASCAL_GITHUB_WEBHOOK_SECRET" --dry-run
rascal auth sync --host "$SERVER_IP"
```

`rascal auth sync` preserves the existing server webhook secret when
`--webhook-secret` is omitted. Pass `--webhook-secret` only when you intend to
rotate it.

## Provisioning

```bash
rascal provision
rascal init --host YOUR_SERVER_IP --repo OWNER/REPO --domain rascal.example.com
```

## Shell Completions

```bash
rascal completion zsh
rascal completion bash
rascal completion fish
```
