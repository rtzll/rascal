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

Tip: use `doctor` to confirm both local and remote resolution.

```bash
./bin/rascal doctor --host YOUR_SERVER_IP
```

## Server Runner Egress Policy

`rascald` supports runner egress modes via environment variables:

- `RASCAL_RUNNER_EGRESS_MODE=open|safe-default|allowlist`
- `RASCAL_RUNNER_EGRESS_ALLOWLIST` (comma/space/newline-separated domains, IPs, or CIDRs; required for `allowlist`)

Mode behavior:

- `open`: no egress filtering (legacy behavior)
- `safe-default`: blocks metadata and private/local targets (`127.0.0.0/8`, `169.254.169.254/32`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, and IPv6 local/link-local ranges)
- `allowlist`: blocks all outbound destinations except the configured allowlist
