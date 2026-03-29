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

ensure_docker() {
  if command -v docker >/dev/null 2>&1; then
    echo "docker already installed"
    return
  fi

  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg

  codename="$(. /etc/os-release && echo "$VERSION_CODENAME")"
  echo \
    "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
    ${codename} stable" >/etc/apt/sources.list.d/docker.list

  apt-get -qq update >/dev/null
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
  systemctl enable docker
  systemctl start docker
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
}

ensure_base_packages
ensure_docker
ensure_caddy
ensure_host_layout
