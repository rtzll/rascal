# Webhooks

## Endpoint

Rascal expects GitHub webhooks at:

`https://YOUR_DOMAIN/v1/webhooks/github`

## Setup

The `init` flow configures webhook + `rascal` label when admin token is
available.

```bash
rascal init --provision --repo OWNER/REPO --domain rascal.example.com
```

If needed, you can re-run `init` with `--skip-deploy` to resync webhook
configuration.

## Validation

Health check:

```bash
curl -fsS https://YOUR_DOMAIN/healthz
```

Webhook endpoint should not redirect for `POST` requests:

```bash
curl -i -X POST https://YOUR_DOMAIN/v1/webhooks/github
```

Without signature, `401/403/405` can be expected. `3xx` redirects are a problem.

The public proxy also rejects malformed webhook traffic before it reaches
`rascald`: only the expected method/path/header shape is forwarded, request
bodies are size-capped, and slow/oversized requests are cut off at Caddy.

Synthetic webhook test from CLI:

```bash
rascal github webhook test \
  --repo OWNER/REPO \
  --webhook-secret "$RASCAL_GITHUB_WEBHOOK_SECRET" \
  --dry-run
```

## Cloudflare Notes

If using Cloudflare proxy (orange cloud):

- Set SSL/TLS mode to `Full (strict)` in Cloudflare dashboard
  (`SSL/TLS -> Overview`).
- Avoid redirect rules that loop back to the same hostname/path.
- During first setup/debug, `DNS only` mode is often easiest.

## Common Failures

- `401 invalid webhook signature`: webhook secret mismatch between GitHub and
  server.
- `403 resource not accessible by token`: missing admin token permissions.
- Delivery timeouts: DNS/TLS/redirect misconfiguration.
