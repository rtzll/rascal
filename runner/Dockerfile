ARG GO_VERSION=1.26.1
ARG GOOSE_VERSION=1.27.2

FROM golang:${GO_VERSION}-bookworm AS go-runtime

FROM node:24-bookworm-slim AS codex-runtime
RUN npm install -g --silent --no-progress --no-fund --no-audit @openai/codex \
    && node --version \
    && npm --version \
    && codex --version

FROM ubuntu:24.04 AS goose-runtime
ARG DEBIAN_FRONTEND=noninteractive
ARG GOOSE_VERSION
RUN apt-get -qq update \
    && apt-get -y -qq install --no-install-recommends ca-certificates curl bzip2 \
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
    rm -f /tmp/goose.tar.bz2 /tmp/goose

FROM ubuntu:24.04
ARG DEBIAN_FRONTEND=noninteractive
ARG RUNTIME_UID=10001
ARG RUNTIME_GID=10001
RUN apt-get -qq update && apt-get -y -qq install --no-install-recommends \
    ca-certificates curl gnupg bzip2 xz-utils zip unzip \
    git gh \
    bash jq ripgrep make patch diffutils file less \
    python3 libxcb1 libgomp1 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=codex-runtime /usr/local/ /usr/local/
COPY --from=go-runtime /usr/local/go/ /usr/local/go/
COPY --from=goose-runtime /usr/local/bin/goose /usr/local/bin/goose

RUN node --version \
    && npm --version \
    && codex --version \
    && /usr/local/go/bin/go version \
    && goose --version

ENV GOOSE_PROVIDER=codex \
    GOOSE_MODEL=gpt-5.4 \
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
RUN groupadd --system --gid "${RUNTIME_GID}" rascal \
    && useradd --system --uid "${RUNTIME_UID}" --gid "${RUNTIME_GID}" --create-home --home-dir /home/rascal --shell /usr/sbin/nologin rascal \
    && install -d -m 0755 -o rascal -g rascal /work /rascal-meta
USER rascal:rascal

ENTRYPOINT ["/usr/local/bin/rascal-entrypoint"]
