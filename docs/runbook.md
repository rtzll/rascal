# Operator Runbook

This is the fastest path for common production issues.

Set variables once:

```bash
HOST=your-server-host
DOMAIN=rascal.example.com
REPO=OWNER/REPO
```

## 0) Quick Triage

```bash
./bin/rascal doctor --host "$HOST"
./bin/rascal ps
./bin/rascal config view
./bin/rascal logs rascald --host "$HOST" --follow
```

## 1) Webhook Not Triggering Runs

Symptoms:
- Label/comment in GitHub does nothing.
- No new run appears in `rascal ps`.

Checks:

```bash
curl -fsS "https://${DOMAIN}/healthz"
./bin/rascal logs caddy-access --host "$HOST" --follow
./bin/rascal logs rascald --host "$HOST" --follow
```

Resync webhook/label (no deploy):

```bash
set -a; source ./.rascal.env; set +a
./bin/rascal init \
  --repo "$REPO" \
  --server-url "https://${DOMAIN}" \
  --skip-deploy \
  --api-token "$RASCAL_API_TOKEN" \
  --webhook-secret "$RASCAL_GITHUB_WEBHOOK_SECRET" \
  --github-admin-token "$GITHUB_ADMIN_TOKEN"
```

If behind Cloudflare, use:
- SSL/TLS mode: `Full (strict)`
- Proxy mode: `DNS only` while validating webhook behavior

See also: [webhooks.md](webhooks.md)

## 2) Deploy Failing or Regressing

Blue/green deploy and rollback are primarily for restoring `rascald` API/webhook
service safely. In-flight task execution is detached in Docker containers and
should survive slot rotation while the active slot adopts supervision.

Run deploy directly:

```bash
set -a; source ./.rascal.env; set +a
./bin/rascal deploy \
  --host "$HOST" \
  --domain "$DOMAIN" \
  --codex-auth ~/.codex/auth.json \
  --github-runtime-token "$RASCAL_GITHUB_TOKEN"
```

That `--codex-auth` value seeds or updates the shared stored credential used
for Codex runs; it is not copied to a static server-side fallback file.

Inspect remote services:

```bash
./bin/rascal logs rascald --host "$HOST" --lines 300
./bin/rascal logs caddy --host "$HOST" --lines 300
./bin/rascal logs caddy-access --host "$HOST" --lines 300
```

Confirm active slot and health:

```bash
ssh root@"$HOST" 'cat /etc/rascal/active_slot'
ssh root@"$HOST" 'curl -fsS http://127.0.0.1:18080/readyz || true'
ssh root@"$HOST" 'curl -fsS http://127.0.0.1:18081/readyz || true'
curl -fsS "https://${DOMAIN}/readyz"
```

See also: [deployment.md](deployment.md)

## 3) Run Appears Stuck

Identify run and follow logs:

```bash
./bin/rascal ps
./bin/rascal logs run RUN_ID --follow
```

If a run should stop:

```bash
./bin/rascal cancel RUN_ID
./bin/rascal logs run RUN_ID --follow
```

Requeue after fix:

```bash
./bin/rascal retry RUN_ID
```

If no progress across runs, inspect server:

```bash
./bin/rascal logs rascald --host "$HOST" --follow
```

Check detached execution state on host:

```bash
ssh root@"$HOST" "docker ps -a --format '{{.Names}} {{.Status}}' | rg '^rascal-' || true"
```

## 4) Cancel Does Not Take Effect Quickly

Request cancel:

```bash
./bin/rascal cancel RUN_ID
./bin/rascal logs run RUN_ID --follow
```

Verify container stop on remote host:

```bash
ssh root@"$HOST" "docker ps --format '{{.Names}}' | rg '^rascal-' || true"
```

If a deploy recently rotated slots, remember execution is detached: the new
active slot should adopt supervision and continue cancellation/finalization.

If run remains active unexpectedly, capture:

```bash
./bin/rascal logs rascald --host "$HOST" --lines 300
./bin/rascal logs run RUN_ID --lines 300
```

## 5) Manual Rollback (Blue/Green)

Use only if automatic rollback did not recover service.

This restores the control plane to a known-good slot. It is not intended to
preserve already-running work because active runs continue in detached
containers and should be adopted by whichever slot becomes active.

1. Determine slot state:

```bash
ssh root@"$HOST" 'cat /etc/rascal/active_slot; systemctl is-active rascal@blue; systemctl is-active rascal@green'
```

2. Switch traffic back to known-good slot (example: `blue`):

```bash
ssh root@"$HOST" "cat >/etc/caddy/rascal-upstream.caddy <<'EOF'
reverse_proxy 127.0.0.1:18080
EOF
systemctl reload caddy || systemctl restart caddy
echo blue >/etc/rascal/active_slot
systemctl restart rascal@blue
systemctl stop --no-block rascal@green || true"
```

3. Verify recovery:

```bash
curl -fsS "https://${DOMAIN}/readyz"
./bin/rascal doctor --host "$HOST"
```

## 6) Post-Incident Checklist

```bash
./bin/rascal doctor --host "$HOST"
./bin/rascal ps
```

Then:
- Open an issue with failing run IDs and timestamps.
- Include snippets from `rascald`, `caddy`, and run logs.
