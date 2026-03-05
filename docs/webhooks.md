# Webhooks

## Endpoint

Rascal expects GitHub webhooks at:

`https://YOUR_DOMAIN/v1/webhooks/github`

## Setup

The init flow configures webhook + `rascal` label when repo + admin token are
available.

```bash
./bin/rascal init --repo OWNER/REPO --provision
```

If needed, you can re-run setup without deploy to resync webhook
configuration:

```bash
./bin/rascal github setup OWNER/REPO \
  --github-token "$GITHUB_ADMIN_TOKEN" \
  --webhook-secret "$WEBHOOK_SECRET"
```

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

Synthetic webhook test from CLI:

```bash
./bin/rascal github webhook test \
  --repo OWNER/REPO \
  --webhook-secret "$WEBHOOK_SECRET" \
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
