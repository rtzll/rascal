FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG NODE_MAJOR=24
ARG GO_VERSION=1.26.0
ARG GOOSE_VERSION=1.26.1

RUN apt-get -qq update && apt-get -y -qq install --no-install-recommends \
    ca-certificates curl gnupg bzip2 xz-utils zip unzip \
    git gh \
    bash jq ripgrep make patch diffutils file less \
    python3 libxcb1 libgomp1 \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "${arch}" in \
      amd64) go_arch="amd64" ;; \
      arm64) go_arch="arm64" ;; \
      *) echo "unsupported architecture: ${arch}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${go_arch}.tar.gz" -o /tmp/go.tar.gz; \
    rm -rf /usr/local/go; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; \
    /usr/local/go/bin/go version; \
    rm -f /tmp/go.tar.gz

RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - >/dev/null \
    && apt-get -qq update \
    && apt-get -y -qq install --no-install-recommends nodejs \
    && npm install -g --silent --no-progress --no-fund --no-audit @openai/codex \
    && node --version \
    && codex --version \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "${arch}" in \
      amd64) goose_asset="goose-x86_64-unknown-linux-gnu.tar.bz2" ;; \
      arm64) goose_asset="goose-aarch64-unknown-linux-gnu.tar.bz2" ;; \
      *) echo "unsupported architecture: ${arch}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/block/goose/releases/download/v${GOOSE_VERSION}/${goose_asset}" -o /tmp/goose.tar.bz2; \
    tar -xjf /tmp/goose.tar.bz2 -C /tmp; \
    install -m 0755 /tmp/goose /usr/local/bin/goose; \
    goose --version; \
    rm -f /tmp/goose.tar.bz2 /tmp/goose

ENV GOOSE_PROVIDER=codex \
    GOOSE_MODEL=gpt-5.2-codex \
    GOOSE_MODE=auto \
    GOOSE_DISABLE_KEYRING=1 \
    GOOSE_DISABLE_SESSION_NAMING=true \
    GOOSE_CONTEXT_STRATEGY=summarize \
    PATH=/usr/local/go/bin:$PATH \
    GH_PROMPT_DISABLED=1 \
    GIT_TERMINAL_PROMPT=0

WORKDIR /work
COPY rascal-runner /usr/local/bin/rascal-runner
COPY entrypoint.sh /usr/local/bin/rascal-entrypoint
RUN chmod +x /usr/local/bin/rascal-runner /usr/local/bin/rascal-entrypoint

ENTRYPOINT ["/usr/local/bin/rascal-entrypoint"]
