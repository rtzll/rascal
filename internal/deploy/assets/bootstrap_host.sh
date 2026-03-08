#!/usr/bin/env bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

ensure_bootstrap_dirs() {
  mkdir -p /opt/rascal /etc/rascal /var/lib/rascal /tmp/rascal-bootstrap /etc/caddy
}

ensure_base_packages() {
  apt-get -qq update >/dev/null
  apt-get install -y -qq sqlite3 ripgrep curl gpg debian-keyring debian-archive-keyring apt-transport-https ca-certificates gnupg lsb-release >/dev/null
}

ensure_docker() {
  if command -v docker >/dev/null 2>&1; then
    echo "docker already installed"
    return
  fi

  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg

  codename="$(. /etc/os-release && echo "${VERSION_CODENAME:-}")"
  if [[ -z "$codename" ]]; then
    echo "missing VERSION_CODENAME in /etc/os-release" >&2
    exit 1
  fi

  cat >/etc/apt/sources.list.d/docker.list <<EOF_DOCKER
deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu ${codename} stable
EOF_DOCKER

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

  curl -1sLf "https://dl.cloudsmith.io/public/caddy/stable/gpg.key" | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf "https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt" -o /etc/apt/sources.list.d/caddy-stable.list
  chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  chmod o+r /etc/apt/sources.list.d/caddy-stable.list

  apt-get -qq update >/dev/null
  apt-get install -y -qq caddy >/dev/null
}

ensure_bootstrap_dirs
ensure_base_packages
ensure_docker
ensure_caddy
