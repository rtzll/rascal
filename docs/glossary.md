# Glossary

## Core Terms

- `Task`: durable unit of work tracked across retries, follow-up input, and PR
  iteration.
- `Run`: one execution attempt to advance a task.
- `RunExecution`: detached execution metadata for a run, such as container
  identity and observed state.
- `Runner`: the execution abstraction (`runner.Runner` interface) that starts,
  inspects, stops, and removes detached executions.
- `Worker`: the in-execution component that performs a run inside the launched
  environment.
- `Runtime`: the user-facing selection that determines both the harness and the
  model provider. Valid runtimes are `goose-codex`, `codex`, `claude`, and
  `goose-claude`.
- `Harness`: the tool wrapper invoked by the worker, derived from the runtime.
  `goose-codex` and `goose-claude` use the `goose` harness; `codex` and `claude`
  use the `direct` harness.
- `ModelProvider`: the underlying model/service used by a runtime. `codex` and
  `goose-codex` runtimes use the `codex` provider; `claude` and `goose-claude`
  runtimes use the `anthropic` provider.
- `SessionMode`: policy governing whether a task-scoped session may resume
  (`off`, `pr-only`, `all`). Defined in `internal/runtime`.
- `TaskSession`: optional task-scoped harness state used to resume later runs.
  Rascal resets it when the task switches runtimes.
- `RunLease`: supervision ownership record for a running run. It tells Rascal
  which orchestrator instance currently owns supervision.

## System Terms

- `Control plane`: `rascal` plus `rascald`. This layer accepts work, persists
  state, schedules runs, and supervises execution.
- `Execution plane`: detached Docker containers started for `rascal-runner`.
- `Runner image`: Docker image used to execute a run. Rascal maintains separate
  images per runtime (Goose-Codex, Codex, Claude, Goose-Claude).
- `Active slot`: the currently live `rascald` slot in blue/green deploys. Only
  this slot should process webhook traffic.
- `Inactive slot`: the standby slot prepared during blue/green deploys before
  cutover.
- `Draining`: shutdown mode where an old slot stops accepting work and
  relinquishes run supervision without canceling detached execution.
- `Scheduler pause`: temporary suspension of all task scheduling, triggered by
  control-plane conditions such as provider usage limits.
- `Deep module`: a package boundary that owns a cohesive area of behavior, such
  as `internal/github`, `internal/apiclient`, `internal/clientconfig`, or
  `internal/remote`.

## Deployment Terms

- `Blue/green deploy`: deployment pattern where one slot is prepared and
  health-checked before traffic switches away from the currently active slot.
- `Cutover`: the moment traffic flips from one slot to the other.
- `Rollback`: restoring traffic and service ownership to the previously healthy
  slot if deploy activation fails.
- `Detached execution`: Docker container execution that continues independently
  of the `rascald` process that launched it.
- `Adoption`: when a newly active or restarted `rascald` instance recovers a
  persisted run execution handle and resumes supervision.

## Run Outcome Terms

- `queued`: run accepted but not yet executing.
- `running`: run currently executing under supervision.
- `review`: run produced or updated a PR and is waiting for reviewer input.
- `succeeded`: run completed without needing further feedback.
- `failed`: run ended unsuccessfully.
- `canceled`: run was canceled by user action or a control-plane decision.

## Credential Terms

- `Stored credential`: encrypted auth payload stored in Rascal state, tagged
  with a `provider` (`codex` or `anthropic`) that determines which runtimes can
  use it.
- `Credential lease`: temporary assignment of a stored credential to one run.
- `Credential provider`: the credential family a run needs, derived from its
  runtime. `codex` and `goose-codex` runtimes use `codex` credentials; `claude`
  and `goose-claude` runtimes use `anthropic` credentials.
