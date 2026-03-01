#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

DEPLOY_HOST="${DEPLOY_HOST:-}"
DEPLOY_USER="${DEPLOY_USER:-root}"
DEPLOY_PORT="${DEPLOY_PORT:-22}"
DEPLOY_RUNNER_IMAGE="${DEPLOY_RUNNER_IMAGE:-rascal-runner:latest}"
DEPLOY_DOMAIN="${DEPLOY_DOMAIN:-}"
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

cat >"$TMP_DIR/Caddyfile" <<'CADDY'
(rascal_common) {
  encode gzip zstd
  import /etc/caddy/rascal-upstream.caddy

  log {
    output file /var/log/caddy/rascal-access.log
    format json
  }
}
CADDY
if [[ -z "$DEPLOY_DOMAIN" ]]; then
cat >>"$TMP_DIR/Caddyfile" <<'CADDY_LOCAL'

:8080 {
  import rascal_common
}
CADDY_LOCAL
fi
if [[ -n "$DEPLOY_DOMAIN" ]]; then
cat >>"$TMP_DIR/Caddyfile" <<CADDY_DOMAIN

$DEPLOY_DOMAIN {
  import rascal_common
}
CADDY_DOMAIN
fi

echo "Uploading deployment artifacts"
"${ssh_base[@]}" "mkdir -p /opt/rascal /tmp/rascal-bootstrap /etc/systemd/system /etc/caddy"
"${scp_base[@]}" "$TMP_DIR/rascald" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/rascald"
"${scp_base[@]}" "$TMP_DIR/runner.tgz" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/runner.tgz"
"${scp_base[@]}" "$ROOT_DIR/deploy/systemd/rascal@.service" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/rascal@.service"
"${scp_base[@]}" "$ROOT_DIR/deploy/scripts/install_docker.sh" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/install_docker.sh"
"${scp_base[@]}" "$TMP_DIR/Caddyfile" "${DEPLOY_USER}@${DEPLOY_HOST}:/tmp/rascal-bootstrap/Caddyfile"

echo "Installing with blue/green slot switch"
"${ssh_base[@]}" "RASCAL_RUNNER_IMAGE=$(printf '%q' "$DEPLOY_RUNNER_IMAGE") DEPLOY_DOMAIN=$(printf '%q' "$DEPLOY_DOMAIN") bash -s" <<'REMOTE'
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Remote deploy must run as root (or a user with equivalent privileges)." >&2
  exit 1
fi

if [ ! -f /etc/rascal/rascal.env ]; then
  echo "Missing /etc/rascal/rascal.env. Bootstrap the server first before using CI deploy." >&2
  exit 1
fi

check_http() {
  local url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --max-time 5 "$url" >/dev/null
    return $?
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -q -T 5 -O - "$url" >/dev/null
    return $?
  fi
  return 1
}

slot_port() {
  case "$1" in
    blue) echo 18080 ;;
    green) echo 18081 ;;
    *) return 1 ;;
  esac
}

detect_active_slot() {
  local slot=""
  if [ -f /etc/rascal/active_slot ]; then
    slot="$(tr -d '[:space:]' </etc/rascal/active_slot || true)"
  fi
  case "$slot" in
    blue|green) echo "$slot"; return ;;
  esac
  if systemctl is-active --quiet "rascal@blue"; then
    echo blue
    return
  fi
  if systemctl is-active --quiet "rascal@green"; then
    echo green
    return
  fi
  echo blue
}

chmod +x /tmp/rascal-bootstrap/install_docker.sh
/tmp/rascal-bootstrap/install_docker.sh

if ! command -v sqlite3 >/dev/null 2>&1; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y sqlite3
fi
if ! command -v caddy >/dev/null 2>&1; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y caddy
fi

mkdir -p /opt/rascal
tar -xzf /tmp/rascal-bootstrap/runner.tgz -C /opt/rascal
docker build -t "$RASCAL_RUNNER_IMAGE" /opt/rascal/runner

install -m 0755 /tmp/rascal-bootstrap/rascald /opt/rascal/rascald
install -m 0644 /tmp/rascal-bootstrap/rascal@.service /etc/systemd/system/rascal@.service

cat >/etc/rascal/rascal-blue.env <<'EOF_BLUE'
RASCAL_LISTEN_ADDR=127.0.0.1:18080
RASCAL_SLOT=blue
EOF_BLUE
cat >/etc/rascal/rascal-green.env <<'EOF_GREEN'
RASCAL_LISTEN_ADDR=127.0.0.1:18081
RASCAL_SLOT=green
EOF_GREEN

systemctl daemon-reload

active_slot="$(detect_active_slot)"
if [ "$active_slot" = "blue" ]; then
  inactive_slot="green"
else
  inactive_slot="blue"
fi
active_port="$(slot_port "$active_slot")"
inactive_port="$(slot_port "$inactive_slot")"

if ! systemctl is-active --quiet "rascal@$active_slot"; then
  systemctl enable "rascal@$active_slot" >/dev/null 2>&1 || true
  systemctl restart "rascal@$active_slot"
fi

systemctl enable "rascal@$inactive_slot" >/dev/null 2>&1 || true
systemctl restart "rascal@$inactive_slot"

ready=0
for _ in $(seq 1 45); do
  if check_http "http://127.0.0.1:${inactive_port}/readyz"; then
    ready=1
    break
  fi
  sleep 2
done
if [ "$ready" -ne 1 ]; then
  echo "inactive slot ${inactive_slot} failed readiness checks" >&2
  systemctl status "rascal@${inactive_slot}" --no-pager || true
  journalctl -u "rascal@${inactive_slot}" -n 80 --no-pager || true
  systemctl stop "rascal@${inactive_slot}" || true
  exit 1
fi

if [ -n "${DEPLOY_DOMAIN:-}" ] || [ ! -f /etc/caddy/Caddyfile ]; then
  install -m 0644 /tmp/rascal-bootstrap/Caddyfile /etc/caddy/Caddyfile
fi
cat >/etc/caddy/rascal-upstream.caddy <<EOF_UPSTREAM
reverse_proxy 127.0.0.1:${inactive_port}
EOF_UPSTREAM

if [ -z "${DEPLOY_DOMAIN:-}" ] && systemctl is-active --quiet rascal; then
  systemctl stop rascal || true
  systemctl disable rascal >/dev/null 2>&1 || true
fi

systemctl enable caddy --now
if ! (systemctl reload caddy || systemctl restart caddy); then
  echo "failed to reload caddy with new upstream; rolling back" >&2
  cat >/etc/caddy/rascal-upstream.caddy <<EOF_ROLLBACK
reverse_proxy 127.0.0.1:${active_port}
EOF_ROLLBACK
  (systemctl reload caddy || systemctl restart caddy) || true
  systemctl stop "rascal@${inactive_slot}" || true
  exit 1
fi

if [ -n "${DEPLOY_DOMAIN:-}" ]; then
  if command -v curl >/dev/null 2>&1; then
    if ! curl -fsS --resolve "${DEPLOY_DOMAIN}:443:127.0.0.1" "https://${DEPLOY_DOMAIN}/readyz" >/dev/null; then
      echo "proxy readiness check failed on caddy; rolling back" >&2
      cat >/etc/caddy/rascal-upstream.caddy <<EOF_ROLLBACK
reverse_proxy 127.0.0.1:${active_port}
EOF_ROLLBACK
      (systemctl reload caddy || systemctl restart caddy) || true
      systemctl stop "rascal@${inactive_slot}" || true
      systemctl restart "rascal@${active_slot}" || true
      exit 1
    fi
  fi
else
  if ! check_http "http://127.0.0.1:8080/readyz"; then
    echo "proxy readiness check failed on caddy; rolling back" >&2
    cat >/etc/caddy/rascal-upstream.caddy <<EOF_ROLLBACK
reverse_proxy 127.0.0.1:${active_port}
EOF_ROLLBACK
    (systemctl reload caddy || systemctl restart caddy) || true
    systemctl stop "rascal@${inactive_slot}" || true
    systemctl restart "rascal@${active_slot}" || true
    exit 1
  fi
fi

echo "$inactive_slot" >/etc/rascal/active_slot
sync
sleep 3
if systemctl is-active --quiet rascal; then
  systemctl stop rascal || true
  systemctl disable rascal >/dev/null 2>&1 || true
fi
if [ "$active_slot" != "$inactive_slot" ]; then
  systemctl stop "rascal@${active_slot}" || true
  systemctl disable "rascal@${active_slot}" >/dev/null 2>&1 || true
fi
systemctl enable "rascal@${inactive_slot}" >/dev/null 2>&1 || true
systemctl is-active --quiet "rascal@${inactive_slot}"

if [ -z "${DEPLOY_DOMAIN:-}" ] && (command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1); then
  healthy=0
  for _ in $(seq 1 20); do
    if check_http "http://127.0.0.1:8080/readyz"; then
      healthy=1
      break
    fi
    sleep 1
  done
  if [ "$healthy" -ne 1 ]; then
    echo "rascal health check failed after blue/green switch" >&2
    systemctl status "rascal@${inactive_slot}" --no-pager || true
    journalctl -u "rascal@${inactive_slot}" -n 80 --no-pager || true
    exit 1
  fi
fi

rm -rf /tmp/rascal-bootstrap
REMOTE

echo "Deployment succeeded"
