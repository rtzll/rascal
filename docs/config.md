# Config

## Config File

Default path:

`$XDG_CONFIG_HOME/rascal/config.toml`

If `XDG_CONFIG_HOME` is unset, Rascal falls back to:

`~/.config/rascal/config.toml`

Inspect current effective values:

```bash
rascal config path
rascal config view
```

## Resolution Order

Rascal resolves values in this order:

1. CLI flags
2. Environment variables
3. Config file (`config.toml`)
4. Built-in defaults or runtime inference

## Env Loading

For convenience, Rascal auto-loads:

- `.rascal.env` (current working directory)

You can override with:

- `--env-file <path>`
- `RASCAL_ENV_FILE=<path>`

## Canonical Auth Env Keys

Rascal-owned auth configuration uses these canonical environment variables:

- `RASCAL_API_TOKEN`
- `RASCAL_GITHUB_TOKEN`
- `RASCAL_GITHUB_WEBHOOK_SECRET`

## Common Keys

- `server_url`
- `api_token`
- `default_repo`
- `transport` (`auto`, `http`, `ssh`)
- `host`
- `domain`
- `ssh_host`
- `ssh_user`
- `ssh_port`
- `ssh_key`

Set values:

```bash
rascal config set server_url https://rascal.example.com
rascal config set default_repo OWNER/REPO
rascal config set transport ssh
```

Tip: use `doctor` to confirm both local and remote resolution.

```bash
rascal doctor --host YOUR_SERVER_IP
```

## Server Credential Settings

Rascal uses encrypted stored credentials tagged by provider for agent runs.

Each credential is tagged with a `provider` (`codex` or `anthropic`). The broker
automatically filters credentials matching the run's runtime:

- `codex` credentials: used by `codex` and `goose-codex` runtimes (auth.json
  format).
- `anthropic` credentials: used by `claude` and `goose-claude` runtimes (OAuth
  token format).
- Legacy credentials with no provider tag are treated as `codex` credentials.

- `RASCAL_CREDENTIAL_STRATEGY` Allocation strategy for choosing among eligible
  credentials. Default: `requester_own_then_shared`

- `RASCAL_CREDENTIAL_LEASE_TTL` How long a credential lease stays valid before
  it must be renewed. Default: `90s`

- `RASCAL_CREDENTIAL_RENEW_INTERVAL` How often the orchestrator renews an active
  credential lease. Default: `30s`

- `RASCAL_CREDENTIAL_ENCRYPTION_KEY` Key used to encrypt stored credential auth
  blobs in SQLite. If unset, Rascal falls back to `RASCAL_API_TOKEN`.
  Recommended: set a dedicated encryption key instead of reusing the API token.

Operators can manage stored credentials with `rascal auth credentials ...`. Use
`--provider codex` or `--provider anthropic` when creating credentials to tag
them for specific runtimes. Bootstrap and deploy flows can seed an initial
shared codex credential with `--codex-auth ~/.codex/auth.json`.
