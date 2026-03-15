# Deployment

This doc describes how Rascal deploys `rascald` to a single host using
blue/green slots, what is running where, and what happens during cutover and
drain.

Detached runner containers now preserve in-flight runs across deploys and
process restarts. Blue/green remains in place for readiness-checked cutover,
webhook/API continuity, and rollback safety rather than for keeping active runs
alive.

## What Gets Deployed

Rascal deploy uploads/builds these artifacts on the server:

- `/opt/rascal/rascald` (Linux binary)
- `/opt/rascal/runner/rascal-runner` (Linux runner binary copied into image
  build context)
- `/etc/systemd/system/rascal@.service` (slot unit)
- `/etc/rascal/rascal.env` (shared runtime env)
- `/etc/rascal/rascal-blue.env` and `/etc/rascal/rascal-green.env` (slot env)
- `/etc/caddy/Caddyfile` + `/etc/caddy/rascal-upstream.caddy` (proxy target)
- Docker images for the configured runner tags (defaults:
  `rascal-runner-goose:latest` and `rascal-runner-codex:latest`)

It also writes:

- `/etc/rascal/active_slot` with `blue` or `green`

## Runtime Topology

- `rascal@blue` listens on `127.0.0.1:18080`
- `rascal@green` listens on `127.0.0.1:18081`
- Caddy proxies external traffic to the currently selected slot
- Legacy single-unit `rascal` service mode is not supported
- Slot identity is set by env:
  - `rascal@blue` gets `RASCAL_SLOT=blue`
  - `rascal@green` gets `RASCAL_SLOT=green`

Default deployed env also includes agent session persistence knobs (enabled by
default):

- `RASCAL_TASK_SESSION_MODE=all`
- `RASCAL_TASK_SESSION_ROOT=/var/lib/rascal/agent-sessions`
- `RASCAL_TASK_SESSION_TTL_DAYS=14`
- `RASCAL_RUNNER_IMAGE_GOOSE` and `RASCAL_RUNNER_IMAGE_CODEX` set the
  runtime-specific runner images
- `RASCAL_AGENT_RUNTIME` is optional and overrides the default runtime when set

## Blue/Green Sequence

Given active slot `A` and inactive slot `B`, deploy does:

1. Build `rascald` for Linux and upload artifacts.
2. Build `rascal-runner` for Linux and upload artifacts.
3. Ensure base packages (`docker`, `caddy`, `curl`, `sqlite3`, `ripgrep`) exist.
4. Install uploaded `rascal-runner` into `/opt/rascal/runner/rascal-runner`.
5. Build/update runner images on host.
6. Install/update systemd unit and env files.
7. Start/restart slot `B`.
8. Wait for slot `B` readiness (`/readyz` on `B` port).
9. Update Caddy upstream to slot `B` and reload Caddy.
10. Verify proxy readiness via Caddy.
11. Write `/etc/rascal/active_slot = B`.
12. Stop old slot `A` with `systemctl stop --no-block` (non-blocking).
13. Disable old slot unit; keep new slot unit enabled/active.

Important: deploy success is no longer coupled to waiting for old-slot run
completion.

Detached execution means blue/green is no longer required to preserve active
task execution during deploy. Its remaining value is:

- readiness-checked cutover before traffic moves
- rollback if proxy activation fails
- overlap safety while both slots are briefly alive
- avoiding API/webhook downtime during `rascald` replacement

## Drain Behavior

When old slot gets `SIGTERM`:

1. Enters draining mode.
2. Stops accepting new work.
3. Stops local run supervision goroutines.
4. Releases run leases quickly.
5. Exits without canceling active detached run containers.

This allows fast cutover while the next slot adopts supervision.

## Runner Entrypoint

- Container entrypoint script is intentionally minimal (`runner/entrypoint.sh`).
- It only executes `/usr/local/bin/rascal-runner`.
- Task workflow behavior (git/agent/PR/meta handling) is implemented in Go in
  `cmd/rascal-runner`.

## Overlap Safety (Both Slots Alive Briefly)

During overlap, only active slot may process webhooks:

- `rascald` reads `/etc/rascal/active_slot` per webhook request.
- If instance slot (`RASCAL_SLOT`) does not match active slot, webhook is
  accepted-but-skipped.

Additional safeguards:

- Webhook delivery dedupe is atomic claim/finalize (no check-then-insert race).
- Run start is DB-atomic (`queued -> running`) with task-level exclusivity, so
  two instances cannot both start work for the same queued run/task.
- Detached execution handles are persisted, so startup recovery can adopt active
  runs immediately after slot rotation.

## Cancellation Semantics

- Cancel intent is persisted in `run_cancels`.
- Active cancellation stops the detached container by persisted execution
  handle, even after supervision handoff.
- Final run state is written after terminal observation/finalization.
- Containers are explicitly removed during terminal cleanup.

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
5. `blue` relinquishes supervision and exits; detached run container keeps
   executing.
6. `green` adopts supervision using persisted execution handle state.
7. On terminal completion, `green` finalizes run state and removes container.
