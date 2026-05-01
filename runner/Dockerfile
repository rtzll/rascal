ARG GO_VERSION=1.26.2
ARG GOOSE_VERSION=1.33.1
ARG GOLANGCI_LINT_VERSION=v2.11.2
ARG SQLC_VERSION=v1.31.1

FROM golang:${GO_VERSION}-bookworm AS go-runtime

FROM go-runtime AS repo-tools
ARG GOLANGCI_LINT_VERSION
ARG SQLC_VERSION
RUN curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b /usr/local/bin "${GOLANGCI_LINT_VERSION}" \
    && /usr/local/bin/golangci-lint version \
    && GOBIN=/usr/local/bin CGO_ENABLED=0 go install github.com/sqlc-dev/sqlc/cmd/sqlc@"${SQLC_VERSION}" \
    && /usr/local/bin/sqlc version

FROM node:24-bookworm-slim AS codex-runtime
RUN npm install -g --silent --no-progress --no-fund --no-audit @openai/codex \
    && node --version \
    && npm --version \
    && codex --version

FROM ubuntu:26.04 AS goose-runtime
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

FROM ubuntu:26.04 AS claude-runtime
ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get -qq update \
    && apt-get -y -qq install --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://claude.ai/install.sh | bash \
    && /root/.local/bin/claude --version

FROM ubuntu:26.04 AS runner-base
ARG DEBIAN_FRONTEND=noninteractive
ARG RUNTIME_UID=10001
ARG RUNTIME_GID=10001
RUN apt-get -qq update && apt-get -y -qq install --no-install-recommends \
    ca-certificates curl gnupg bzip2 xz-utils zip unzip \
    git gh \
    bash jq ripgrep make patch diffutils file less sqlite3 \
    python3 libxcb1 libgomp1 \
    build-essential \
    procps \
    dnsutils iputils-ping netcat-openbsd traceroute net-tools \
    && rm -rf /var/lib/apt/lists/*

COPY --from=codex-runtime /usr/local/ /usr/local/
COPY --from=go-runtime /usr/local/go/ /usr/local/go/
COPY --from=repo-tools /usr/local/bin/golangci-lint /usr/local/bin/golangci-lint
COPY --from=repo-tools /usr/local/bin/sqlc /usr/local/bin/sqlc

RUN node --version \
    && npm --version \
    && codex --version \
    && /usr/local/go/bin/go version \
    && golangci-lint version \
    && sqlc version \
    && sqlite3 --version

ENV PATH=/usr/local/go/bin:$PATH \
    GH_PROMPT_DISABLED=1 \
    GIT_TERMINAL_PROMPT=0

WORKDIR /work
ARG CACHE_BUST
COPY rascal-runner /usr/local/bin/rascal-runner
COPY entrypoint.sh /usr/local/bin/rascal-entrypoint
RUN chmod +x /usr/local/bin/rascal-runner /usr/local/bin/rascal-entrypoint
RUN groupadd --system --gid "${RUNTIME_GID}" rascal \
    && useradd --system --uid "${RUNTIME_UID}" --gid "${RUNTIME_GID}" --create-home --home-dir /home/rascal --shell /usr/sbin/nologin rascal \
    && install -d -m 0755 -o rascal -g rascal /work /rascal-meta

FROM runner-base AS goose-codex-runner
COPY --from=goose-runtime /usr/local/bin/goose /usr/local/bin/goose
RUN goose --version
ENV GOOSE_PROVIDER=codex \
    GOOSE_MODEL=gpt-5.4 \
    GOOSE_MODE=auto \
    GOOSE_MAX_TURNS=100 \
    GOOSE_AUTO_COMPACT_THRESHOLD=0.0 \
    GOOSE_DISABLE_KEYRING=1 \
    GOOSE_DISABLE_SESSION_NAMING=true \
    GOOSE_CONTEXT_STRATEGY=summarize
USER rascal:rascal
ENTRYPOINT ["/usr/local/bin/rascal-entrypoint"]

FROM runner-base AS codex-runner
USER rascal:rascal
ENTRYPOINT ["/usr/local/bin/rascal-entrypoint"]

FROM codex-runner AS smoke-codex-runner
USER root
COPY smoke/codex /usr/local/bin/codex
COPY smoke/gh /usr/local/bin/gh
COPY smoke/git /usr/local/bin/git
COPY smoke/seed-repo /tmp/rascal-smoke-seed
RUN chmod +x /usr/local/bin/codex /usr/local/bin/gh /usr/local/bin/git \
    && install -d -m 0755 /opt/rascal-smoke \
    && git init --bare /opt/rascal-smoke/remote.git \
    && git init /tmp/rascal-smoke-worktree \
    && git -C /tmp/rascal-smoke-worktree config user.name "smoke-bot" \
    && git -C /tmp/rascal-smoke-worktree config user.email "smoke-bot@example.com" \
    && cp -R /tmp/rascal-smoke-seed/. /tmp/rascal-smoke-worktree/ \
    && git -C /tmp/rascal-smoke-worktree add -A \
    && git -C /tmp/rascal-smoke-worktree commit -m "chore(smoke): seed repository" \
    && git -C /tmp/rascal-smoke-worktree branch -M main \
    && git -C /tmp/rascal-smoke-worktree remote add origin file:///opt/rascal-smoke/remote.git \
    && git -C /tmp/rascal-smoke-worktree push origin main \
    && chown -R rascal:rascal /opt/rascal-smoke \
    && rm -rf /tmp/rascal-smoke-seed /tmp/rascal-smoke-worktree
USER rascal:rascal
ENTRYPOINT ["/usr/local/bin/rascal-entrypoint"]

FROM runner-base AS claude-runner
COPY --from=claude-runtime /root/.local/bin/claude /usr/local/bin/claude
COPY --from=claude-runtime /root/.local/share/claude /usr/local/share/claude
RUN claude --version
USER rascal:rascal
ENTRYPOINT ["/usr/local/bin/rascal-entrypoint"]

FROM runner-base AS goose-claude-runner
COPY --from=goose-runtime /usr/local/bin/goose /usr/local/bin/goose
COPY --from=claude-runtime /root/.local/bin/claude /usr/local/bin/claude
COPY --from=claude-runtime /root/.local/share/claude /usr/local/share/claude
RUN goose --version && claude --version
ENV GOOSE_PROVIDER=claude-code \
    GOOSE_MODEL=claude-sonnet-4-5 \
    GOOSE_MODE=auto \
    GOOSE_MAX_TURNS=100 \
    GOOSE_AUTO_COMPACT_THRESHOLD=0.0 \
    GOOSE_DISABLE_KEYRING=1 \
    GOOSE_DISABLE_SESSION_NAMING=true \
    GOOSE_CONTEXT_STRATEGY=summarize
USER rascal:rascal
ENTRYPOINT ["/usr/local/bin/rascal-entrypoint"]
