# Commands

## Setup

```bash
./bin/rascal bootstrap --repo OWNER/REPO --domain rascal.example.com
./bin/rascal deploy --host YOUR_SERVER_IP --skip-env-upload --skip-auth-upload
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
./bin/rascal github setup OWNER/REPO --github-token "$GITHUB_TOKEN" --webhook-secret "$WEBHOOK_SECRET"
./bin/rascal github disable OWNER/REPO --github-token "$GITHUB_TOKEN"
```

## Infra Helpers

```bash
./bin/rascal infra provision-hetzner
./bin/rascal infra deploy-existing --host YOUR_SERVER_IP
```

## Shell Completions

```bash
./bin/rascal completion zsh
./bin/rascal completion bash
./bin/rascal completion fish
```
