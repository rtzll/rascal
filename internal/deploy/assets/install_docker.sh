#!/usr/bin/env bash
set -euo pipefail

have_docker=0
have_sqlite=0
if command -v docker >/dev/null 2>&1; then
  have_docker=1
fi
if command -v sqlite3 >/dev/null 2>&1; then
  have_sqlite=1
fi

if [[ "$have_docker" -eq 1 && "$have_sqlite" -eq 1 ]]; then
  echo "docker and sqlite3 already installed"
  exit 0
fi

export DEBIAN_FRONTEND=noninteractive
apt-get -qq update >/dev/null

if [[ "$have_sqlite" -eq 0 ]]; then
  apt-get install -y -qq sqlite3 >/dev/null
fi

if [[ "$have_docker" -eq 1 ]]; then
  echo "docker already installed"
  exit 0
fi

apt-get install -y -qq ca-certificates curl gnupg lsb-release >/dev/null
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
