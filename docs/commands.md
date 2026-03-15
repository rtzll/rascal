# Commands

## Setup

```bash
./bin/rascal init --provision --repo OWNER/REPO --domain rascal.example.com
./bin/rascal deploy --host YOUR_SERVER_IP
./bin/rascal doctor --host YOUR_SERVER_IP
./bin/rascal config view
```

## Start Runs

Ad-hoc task:

```bash
./bin/rascal run -t "Fix flaky test in cmd package"
```

Issue-driven task:

```bash
./bin/rascal run --issue OWNER/REPO#123
```

## Monitor Runs

```bash
./bin/rascal ps
./bin/rascal ps --limit 10
./bin/rascal ps --all
./bin/rascal logs <run_id> --follow
./bin/rascal logs run <run_id> --follow
./bin/rascal logs rascald --follow
./bin/rascal logs caddy --follow
./bin/rascal logs caddy-access --follow
./bin/rascal open <run_id>
```

## Control Runs

```bash
./bin/rascal retry <run_id>
./bin/rascal cancel <run_id>
```

## GitHub Integrations

```bash
./bin/rascal github status OWNER/REPO
./bin/rascal github setup OWNER/REPO --github-admin-token "$GITHUB_ADMIN_TOKEN" --webhook-secret "$RASCAL_GITHUB_WEBHOOK_SECRET"
./bin/rascal github disable OWNER/REPO --github-admin-token "$GITHUB_ADMIN_TOKEN"
./bin/rascal github webhook test --repo OWNER/REPO --webhook-secret "$RASCAL_GITHUB_WEBHOOK_SECRET" --dry-run
```

## Provisioning

```bash
./bin/rascal provision
./bin/rascal init --host YOUR_SERVER_IP --repo OWNER/REPO --domain rascal.example.com
```

## Shell Completions

```bash
./bin/rascal completion zsh
./bin/rascal completion bash
./bin/rascal completion fish
```
