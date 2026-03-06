# Deployment

This doc describes how Rascal deploys `rascald` to a single host using
blue/green slots, what is running where, and what happens during cutover and
drain.

## What Gets Deployed

Rascal deploy uploads/builds these artifacts on the server:

- `/opt/rascal/rascald` (Linux binary)
- `/opt/rascal/runner/rascal-runner` (Linux runner binary copied into image build context)
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
2. Build `rascal-runner` for Linux and upload artifacts.
3. Ensure base packages (`docker`, `caddy`, `curl`, `sqlite3`) exist.
4. Install uploaded `rascal-runner` into `/opt/rascal/runner/rascal-runner`.
5. Build/update runner image on host.
6. Install/update systemd unit and env files.
7. If slot `B` is still running in draining mode from a previous deploy, reclaim it first:
   - call slot-local reclaim API on `B`
   - cancel active runs on `B` with reason `superseded by newer deploy while draining`
   - wait a short bounded cleanup window
   - stop `B`
8. Start/restart slot `B`.
9. Wait for slot `B` readiness (`/readyz` on `B` port).
10. Update Caddy upstream to slot `B` and reload Caddy.
11. Verify proxy readiness via Caddy.
12. Write `/etc/rascal/active_slot = B`.
13. Mark old slot `A` as draining via slot-local admin API (do not stop immediately).
14. Disable old slot unit; keep new slot unit enabled/active.

Important: deploy success is no longer coupled to waiting for old-slot drain.

## Slot States And Policy

Slots are treated as:

- `active`: receives traffic and schedules work.
- `draining`: no new work accepted, active work may continue.
- `inactive`: free to deploy.

Policy:

1. The immediately previous slot is allowed to drain active work with no fixed deploy timeout.
2. If the next deploy needs that slot and it is still draining, deploy explicitly reclaims it and cancels its active work.
3. The currently active slot is not canceled just because a new deploy starts.

## Drain And Shutdown Behavior

Deploy-driven drain is now explicit (`/v1/admin/drain`) and does not apply a fixed active-run timeout.

Generic process shutdown (`SIGTERM`, host shutdown, operator stop) keeps bounded shutdown behavior:

1. Enter draining mode.
2. Stop accepting new work.
3. Wait up to 5 minutes for active runs.
4. Cancel remaining runs with `orchestrator shutdown drain timeout`.

## Runner Entrypoint

- Container entrypoint script is intentionally minimal (`runner/entrypoint.sh`).
- It only executes `/usr/local/bin/rascal-runner`.
- Task workflow behavior (git/goose/PR/meta handling) is implemented in Go in `cmd/rascal-runner`.

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
  `succeeded` or `review`.
- Cancel reason distinguishes user cancel vs deploy reclaim vs shutdown timeout.

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

Example timeline:

1. `blue` is active and running a job.
2. Deploy N starts `green`, flips traffic to `green`, and marks `blue` draining.
3. `blue` keeps running its active job with no deploy-time fixed timeout.
4. Deploy N+1 starts while `blue` is still draining.
5. Deploy N+1 explicitly reclaims `blue` before reusing it:
   - cancel `blue` active runs with reason `superseded by newer deploy while draining`
   - bounded cleanup wait
   - stop `blue`
6. New version deploys onto `blue`, traffic flips, and `green` becomes the next draining slot.
