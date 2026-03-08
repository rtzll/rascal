# Glossary

## Core Terms

- `Task`: durable unit of work tracked across retries, follow-up input, and PR iteration.
- `Run`: one execution attempt to advance a task.
- `AgentBackend`: the backend recorded for a task or run attempt. Rascal currently supports `goose` and `codex`, and a task may switch backends across runs.
- `AgentSession`: optional task-scoped backend state used to resume later runs. Rascal resets it when the task switches backends.
- `RunExecution`: detached execution metadata for a run, such as container identity and observed state.
- `RunLease`: supervision ownership record for a running run. It tells Rascal which orchestrator instance currently owns supervision.

## System Terms

- `Control plane`: `rascal` plus `rascald`. This layer accepts work, persists state, schedules runs, and supervises execution.
- `Execution plane`: detached Docker containers started for `rascal-runner`.
- `Runner image`: Docker image used to execute a run. Rascal maintains separate images for Goose and Codex backends.
- `Active slot`: the currently live `rascald` slot in blue/green deploys. Only this slot should process webhook traffic.
- `Inactive slot`: the standby slot prepared during blue/green deploys before cutover.
- `Draining`: shutdown mode where an old slot stops accepting work and relinquishes run supervision without canceling detached execution.

## Deployment Terms

- `Blue/green deploy`: deployment pattern where one slot is prepared and health-checked before traffic switches away from the currently active slot.
- `Cutover`: the moment traffic flips from one slot to the other.
- `Rollback`: restoring traffic and service ownership to the previously healthy slot if deploy activation fails.
- `Detached execution`: Docker container execution that continues independently of the `rascald` process that launched it.
- `Adoption`: when a newly active or restarted `rascald` instance recovers a persisted run execution handle and resumes supervision.

## Run Outcome Terms

- `queued`: run accepted but not yet executing.
- `running`: run currently executing under supervision.
- `review`: run produced or updated a PR and is waiting for reviewer input.
- `succeeded`: run completed without needing further feedback.
- `failed`: run ended unsuccessfully.
- `canceled`: run was canceled by user action or a control-plane decision.

## Credential Terms

- `Stored credential`: encrypted Codex auth payload stored in Rascal state.
- `Credential lease`: temporary assignment of a stored credential to one run.
