# Config

## Config File

Default path:

`~/.rascal/config.toml`

Inspect current effective values:

```bash
./bin/rascal config view
```

## Resolution Order

Rascal resolves values in this order:

1. CLI flags
2. Environment variables
3. Config file (`config.toml`)
4. Built-in defaults or runtime inference

## Env Loading

For convenience, Rascal auto-loads:

- `./.rascal.env` (current working directory)

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
./bin/rascal config set server_url https://rascal.example.com
./bin/rascal config set default_repo OWNER/REPO
./bin/rascal config set transport ssh
```

## `default_repo` vs Repository Registry

- `default_repo` lives in local CLI config and only fills command arguments.
- Registered repositories live on the daemon in SQLite (`repositories` table)
  and define admission policy, webhook key/secret, runtime token, and defaults.
- A repo can be your local `default_repo` but still be rejected by the daemon if
  it is not registered or is disabled.

Tip: use `doctor` to confirm both local and remote resolution.

```bash
./bin/rascal doctor --host YOUR_SERVER_IP
```

## Server Credential Settings

Rascal uses encrypted stored credentials for Codex runs.

- `RASCAL_CREDENTIAL_STRATEGY`
  Allocation strategy for choosing among eligible credentials.
  Default: `requester_own_then_shared`

- `RASCAL_CREDENTIAL_LEASE_TTL`
  How long a credential lease stays valid before it must be renewed.
  Default: `90s`

- `RASCAL_CREDENTIAL_RENEW_INTERVAL`
  How often the orchestrator renews an active credential lease.
  Default: `30s`

- `RASCAL_CREDENTIAL_ENCRYPTION_KEY`
  Key used to encrypt stored credential auth blobs in SQLite.
  If unset, Rascal falls back to `RASCAL_API_TOKEN`.
  Recommended: set a dedicated encryption key instead of reusing the API token.

Operators can manage stored credentials with `rascal auth credentials ...`.
Bootstrap and deploy flows can seed an initial shared credential with
`--codex-auth ~/.codex/auth.json`.
