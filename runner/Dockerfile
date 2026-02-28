FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG NODE_MAJOR=22

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git gh bash jq ripgrep make gcc g++ openssl \
    python3 python3-pip nodejs npm \
    && rm -rf /var/lib/apt/lists/*

# Expected to provide `codex` in PATH.
# If your environment uses a different package name/version, adjust this line.
RUN npm install -g @openai/codex

# Goose is expected to be available in PATH for `goose run`.
# Install it here according to your preferred pinned release.
# Example:
#   RUN curl -fsSL <GOOSE_RELEASE_URL> | tar -xz -C /usr/local/bin goose

ENV GOOSE_PROVIDER=codex \
    GOOSE_MODEL=gpt-5.2-codex \
    GOOSE_MODE=auto \
    GOOSE_DISABLE_KEYRING=1 \
    GOOSE_DISABLE_SESSION_NAMING=true \
    GOOSE_CONTEXT_STRATEGY=summarize \
    GH_PROMPT_DISABLED=1 \
    GIT_TERMINAL_PROMPT=0

WORKDIR /work
COPY entrypoint.sh /usr/local/bin/rascal-entrypoint
RUN chmod +x /usr/local/bin/rascal-entrypoint

ENTRYPOINT ["/usr/local/bin/rascal-entrypoint"]
