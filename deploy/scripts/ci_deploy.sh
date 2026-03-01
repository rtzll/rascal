#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

DEPLOY_HOST="${DEPLOY_HOST:-}"
DEPLOY_USER="${DEPLOY_USER:-root}"
DEPLOY_PORT="${DEPLOY_PORT:-22}"
DEPLOY_DOMAIN="${DEPLOY_DOMAIN:-}"
DEPLOY_SSH_KEY_PATH="${DEPLOY_SSH_KEY_PATH:-$HOME/.ssh/id_ed25519}"
DEPLOY_GOARCH="${DEPLOY_GOARCH:-}"
DEPLOY_CODEX_AUTH_PATH="${DEPLOY_CODEX_AUTH_PATH:-}"

RASCAL_API_TOKEN="${RASCAL_API_TOKEN:-}"
RASCAL_GITHUB_WEBHOOK_SECRET="${RASCAL_GITHUB_WEBHOOK_SECRET:-${GITHUB_WEBHOOK_SECRET:-}}"
GITHUB_RUNTIME_TOKEN="${GITHUB_RUNTIME_TOKEN:-${RASCAL_GITHUB_RUNTIME_TOKEN:-}}"

if [[ -z "$DEPLOY_HOST" ]]; then
  echo "DEPLOY_HOST is required" >&2
  exit 1
fi
if [[ -z "$RASCAL_API_TOKEN" ]]; then
  echo "RASCAL_API_TOKEN is required" >&2
  exit 1
fi
if [[ -z "$RASCAL_GITHUB_WEBHOOK_SECRET" ]]; then
  echo "RASCAL_GITHUB_WEBHOOK_SECRET (or GITHUB_WEBHOOK_SECRET) is required" >&2
  exit 1
fi
if [[ -z "$GITHUB_RUNTIME_TOKEN" ]]; then
  echo "GITHUB_RUNTIME_TOKEN (or RASCAL_GITHUB_RUNTIME_TOKEN) is required" >&2
  exit 1
fi
if [[ ! -f "$DEPLOY_SSH_KEY_PATH" ]]; then
  echo "DEPLOY_SSH_KEY_PATH does not exist: $DEPLOY_SSH_KEY_PATH" >&2
  exit 1
fi
if ! [[ "$DEPLOY_PORT" =~ ^[0-9]+$ ]] || [[ "$DEPLOY_PORT" -le 0 ]]; then
  echo "DEPLOY_PORT must be a positive integer" >&2
  exit 1
fi
if [[ -n "$DEPLOY_CODEX_AUTH_PATH" && ! -f "$DEPLOY_CODEX_AUTH_PATH" ]]; then
  echo "DEPLOY_CODEX_AUTH_PATH does not exist: $DEPLOY_CODEX_AUTH_PATH" >&2
  exit 1
fi

echo "Building rascal CLI"
go build -o "$ROOT_DIR/bin/rascal" ./cmd/rascal

args=(
  infra deploy-existing
  --host "$DEPLOY_HOST"
  --ssh-user "$DEPLOY_USER"
  --ssh-port "$DEPLOY_PORT"
  --ssh-key "$DEPLOY_SSH_KEY_PATH"
  --api-token "$RASCAL_API_TOKEN"
  --github-runtime-token "$GITHUB_RUNTIME_TOKEN"
  --webhook-secret "$RASCAL_GITHUB_WEBHOOK_SECRET"
)
if [[ -n "$DEPLOY_DOMAIN" ]]; then
  args+=(--domain "$DEPLOY_DOMAIN")
fi
if [[ -n "$DEPLOY_GOARCH" ]]; then
  args+=(--goarch "$DEPLOY_GOARCH")
fi
if [[ -n "$DEPLOY_CODEX_AUTH_PATH" ]]; then
  args+=(--codex-auth "$DEPLOY_CODEX_AUTH_PATH")
fi

echo "Running deploy via rascal CLI"
"$ROOT_DIR/bin/rascal" "${args[@]}"
