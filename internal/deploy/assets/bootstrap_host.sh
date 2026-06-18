#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

ensure_base_packages() {
  local missing=0
  for cmd in sqlite3 rg curl gpg; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      missing=1
      break
    fi
  done
  if [[ "$missing" -eq 0 ]]; then
    echo "base packages already installed"
    return
  fi
  apt-get -qq update >/dev/null
  apt-get install -y -qq sqlite3 ripgrep curl gpg debian-keyring debian-archive-keyring apt-transport-https ca-certificates gnupg lsb-release >/dev/null
}

ensure_podman() {
  if command -v podman >/dev/null 2>&1; then
    echo "podman already installed"
    return
  fi

  apt-get -qq update >/dev/null
  apt-get install -y -qq podman uidmap slirp4netns fuse-overlayfs >/dev/null
}

ensure_rascal_user() {
  if ! getent group rascal >/dev/null 2>&1; then
    groupadd --system --gid 10001 rascal
  elif [[ "$(getent group rascal | cut -d: -f3)" != "10001" ]]; then
    echo "existing rascal group does not use gid 10001" >&2
    exit 1
  fi
  if ! id -u rascal >/dev/null 2>&1; then
    useradd --system --uid 10001 --gid rascal --create-home --home-dir /var/lib/rascal --shell /usr/sbin/nologin rascal
  elif [[ "$(id -u rascal)" != "10001" ]]; then
    echo "existing rascal user does not use uid 10001" >&2
    exit 1
  fi
  if ! grep -q '^rascal:' /etc/subuid 2>/dev/null; then
    echo 'rascal:100000:65536' >>/etc/subuid
  fi
  if ! grep -q '^rascal:' /etc/subgid 2>/dev/null; then
    echo 'rascal:100000:65536' >>/etc/subgid
  fi
  install -d -m 0700 -o rascal -g rascal /run/user/10001
  loginctl enable-linger rascal >/dev/null 2>&1 || true
}

ensure_caddy() {
  if command -v caddy >/dev/null 2>&1; then
    echo "caddy already installed"
    return
  fi

  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' -o /etc/apt/sources.list.d/caddy-stable.list
  chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  chmod o+r /etc/apt/sources.list.d/caddy-stable.list
  apt-get -qq update >/dev/null
  apt-get install -y -qq caddy >/dev/null
}

ensure_host_layout() {
  mkdir -p /opt/rascal /etc/rascal /var/lib/rascal /tmp/rascal-bootstrap /etc/caddy
  chown -R rascal:rascal /opt/rascal /var/lib/rascal /etc/rascal
}

ensure_base_packages
ensure_podman
ensure_rascal_user
ensure_caddy
ensure_host_layout
