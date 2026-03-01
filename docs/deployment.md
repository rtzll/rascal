# Deployment

This doc describes how Rascal deploys `rascald` to a single host using
blue/green slots, what is running where, and what happens during cutover and
drain.

## What Gets Deployed

Rascal deploy uploads/builds these artifacts on the server:

- `/opt/rascal/rascald` (Linux binary)
- `/etc/systemd/system/rascal@.service` (slot unit)
- `/etc/rascal/rascal.env` (shared runtime env)
- `/etc/rascal/rascal-blue.env` and `/etc/rascal/rascal-green.env` (slot env)
- `/etc/caddy/Caddyfile` + `/etc/caddy/rascal-upstream.caddy` (proxy target)
- Docker image `rascal-runner:latest`

It also writes:

- `/etc/rascal/active_slot` with `blue` or `green`

## Runtime Topology

- `rascal@blue` listens on `127.0.0.1:18080`
- `rascal@green` listens on `127.0.0.1:18081`
- Caddy proxies external traffic to the currently selected slot
- Slot identity is set by env:
  - `rascal@blue` gets `RASCAL_SLOT=blue`
  - `rascal@green` gets `RASCAL_SLOT=green`

## Blue/Green Sequence

Given active slot `A` and inactive slot `B`, deploy does:

1. Build `rascald` for Linux and upload artifacts.
2. Ensure base packages (`docker`, `caddy`, `curl`, `sqlite3`) exist.
3. Build/update runner image on host.
4. Install/update systemd unit and env files.
5. Start/restart slot `B`.
6. Wait for slot `B` readiness (`/readyz` on `B` port).
7. Update Caddy upstream to slot `B` and reload Caddy.
8. Verify proxy readiness via Caddy.
9. Write `/etc/rascal/active_slot = B`.
10. Stop old slot `A` with `systemctl stop --no-block` (non-blocking).
11. Disable old slot unit; keep new slot unit enabled/active.

Important: deploy success is no longer coupled to waiting for old-slot drain.

## Drain Behavior

When old slot gets `SIGTERM`:

1. Enters draining mode.
2. Stops accepting new work.
3. Cancels queued runs immediately.
4. Waits up to 5 minutes for active runs to finish.
5. If timeout hits, cancels remaining active runs with drain-timeout reason.
6. Waits a short final window, then exits.

This allows fast cutover while old work winds down in the background.

## Overlap Safety (Both Slots Alive Briefly)

During overlap, only active slot may process webhooks:

- `rascald` reads `/etc/rascal/active_slot` per webhook request.
- If instance slot (`RASCAL_SLOT`) does not match active slot, webhook is
  accepted-but-skipped.

Additional safeguards:

- Webhook delivery dedupe is atomic claim/finalize (no check-then-insert race).
- Run start is DB-atomic (`queued -> running`) with task-level exclusivity, so
  two instances cannot both start work for the same queued run/task.

## Cancellation Semantics

- User cancel marks run as `canceled` immediately.
- Active cancellation propagates to runner context.
- Docker launcher explicitly stops and removes the run container on cancel.
- Final success write is guarded so canceled runs cannot later become
  `succeeded` or `awaiting_feedback`.
- Cancel reason distinguishes user cancel vs shutdown/drain timeout.

## Rollback Behavior

If Caddy reload/readiness fails after switching upstream during deploy:

- Deploy attempts rollback:
  - restore upstream to previous slot
  - reload/restart Caddy
  - stop new slot and restart previous slot

## Quick Inspection Commands

```bash
ssh root@HOST 'cat /etc/rascal/active_slot'
ssh root@HOST 'systemctl status rascal@blue --no-pager'
ssh root@HOST 'systemctl status rascal@green --no-pager'
ssh root@HOST 'cat /etc/caddy/rascal-upstream.caddy'
ssh root@HOST 'curl -fsS http://127.0.0.1:18080/readyz || true'
ssh root@HOST 'curl -fsS http://127.0.0.1:18081/readyz || true'
```

## End-to-End Example Flow

Example: `blue` is active and running a job, deploy is triggered.

1. Deploy prepares `green` and passes `green` readiness.
2. Caddy upstream switches to `green`.
3. `active_slot` flips to `green`.
4. `blue` gets stop request with `--no-block`; deploy returns success quickly.
5. `blue` drains in background; existing job can finish or be canceled on
   timeout.
6. If canceled, runner container is explicitly stopped/removed and run stays
   terminally `canceled`.
