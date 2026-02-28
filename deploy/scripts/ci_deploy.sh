#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

DEPLOY_HOST="${DEPLOY_HOST:-}"
DEPLOY_USER="${DEPLOY_USER:-root}"
DEPLOY_PORT="${DEPLOY_PORT:-22}"
DEPLOY_RUNNER_IMAGE="${DEPLOY_RUNNER_IMAGE:-rascal-runner:latest}"
DEPLOY_SSH_KEY_PATH="${DEPLOY_SSH_KEY_PATH:-$HOME/.ssh/id_ed25519}"
DEPLOY_KNOWN_HOSTS_PATH="${DEPLOY_KNOWN_HOSTS_PATH:-$HOME/.ssh/known_hosts}"

if [[ -z "$DEPLOY_HOST" ]]; then
  echo "DEPLOY_HOST is required" >&2
  exit 1
fi

if [[ ! -f "$DEPLOY_SSH_KEY_PATH" ]]; then
  echo "DEPLOY_SSH_KEY_PATH does not exist: $DEPLOY_SSH_KEY_PATH" >&2
  exit 1
fi

if [[ ! -s "$DEPLOY_SSH_KEY_PATH" ]]; then
  echo "DEPLOY_SSH_KEY_PATH is empty: $DEPLOY_SSH_KEY_PATH" >&2
  exit 1
fi

if [[ ! -f "$DEPLOY_KNOWN_HOSTS_PATH" ]]; then
  echo "DEPLOY_KNOWN_HOSTS_PATH does not exist: $DEPLOY_KNOWN_HOSTS_PATH" >&2
  exit 1
fi

if [[ ! -s "$DEPLOY_KNOWN_HOSTS_PATH" ]]; then
  echo "DEPLOY_KNOWN_HOSTS_PATH is empty: $DEPLOY_KNOWN_HOSTS_PATH" >&2
  exit 1
fi

if ! [[ "$DEPLOY_PORT" =~ ^[0-9]+$ ]] || [[ "$DEPLOY_PORT" -le 0 ]]; then
  echo "DEPLOY_PORT must be a positive integer" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

ssh_base=(
  ssh
  -p "$DEPLOY_PORT"
  -o BatchMode=yes
  -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$DEPLOY_KNOWN_HOSTS_PATH"
  -i "$DEPLOY_SSH_KEY_PATH"
  "${DEPLOY_USER}@${DEPLOY_HOST}"
)
scp_base=(
  scp
  -P "$DEPLOY_PORT"
  -o BatchMode=yes
  -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$DEPLOY_KNOWN_HOSTS_PATH"
  -i "$DEPLOY_SSH_KEY_PATH"
)

remote_machine="$("${ssh_base[@]}" "uname -m")"
remote_machine="$(echo "$remote_machine" | tr '[:upper:]' '[:lower:]' | xargs)"
case "$remote_machine" in
  x86_64|amd64)
    GOARCH="amd64"
    ;;
  aarch64|arm64)
    GOARCH="arm64"
    ;;
  *)
    echo "Unsupported remote architecture: $remote_machine" >&2
    exit 1
    ;;
esac

echo "Remote architecture: $remote_machine -> GOARCH=$GOARCH"

echo "Building rascald"
GOOS=linux GOARCH="$GOARCH" CGO_ENABLED=0 go build -o "$TMP_DIR/rascald" ./cmd/rascald

echo "Packaging runner assets"
tar -C "$ROOT_DIR" -czf "$TMP_DIR/runner.tgz" runner

echo "Uploading deployment artifacts"
"${ssh_base[@]}" "mkdir -p /opt/rascal /tmp/rascal-bootstrap /etc/systemd/system"
"${scp_base[@]}" "$TMP_DIR/rascald" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/rascald"
"${scp_base[@]}" "$TMP_DIR/runner.tgz" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/runner.tgz"
"${scp_base[@]}" "$ROOT_DIR/deploy/systemd/rascal.service" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/rascal.service"
"${scp_base[@]}" "$ROOT_DIR/deploy/scripts/install_docker.sh" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/install_docker.sh"

echo "Installing and restarting rascal service"
"${ssh_base[@]}" "RASCAL_RUNNER_IMAGE=$(printf '%q' "$DEPLOY_RUNNER_IMAGE") bash -s" <<'REMOTE'
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Remote deploy must run as root (or a user with equivalent privileges)." >&2
  exit 1
fi

if [ ! -f /etc/rascal/rascal.env ]; then
  echo "Missing /etc/rascal/rascal.env. Bootstrap the server first before using CI deploy." >&2
  exit 1
fi

chmod +x /tmp/rascal-bootstrap/install_docker.sh
/tmp/rascal-bootstrap/install_docker.sh

mkdir -p /opt/rascal
tar -xzf /tmp/rascal-bootstrap/runner.tgz -C /opt/rascal
docker build -t "$RASCAL_RUNNER_IMAGE" /opt/rascal/runner

install -m 0755 /tmp/rascal-bootstrap/rascald /opt/rascal/rascald
install -m 0644 /tmp/rascal-bootstrap/rascal.service /etc/systemd/system/rascal.service

systemctl daemon-reload
systemctl enable rascal --now
systemctl restart rascal
systemctl is-active --quiet rascal

if command -v curl >/dev/null 2>&1; then
  curl -fsS http://127.0.0.1:8080/healthz >/dev/null
fi

rm -rf /tmp/rascal-bootstrap
REMOTE

echo "Deployment succeeded"
