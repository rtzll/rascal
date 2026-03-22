# Ubiquitous Language

## Task Lifecycle

| Term | Definition | Aliases to avoid |
|------|-----------|-----------------|
| **Task** | A durable unit of work tracked across retries, follow-up input, and PR iteration | Job, ticket, issue |
| **Run** | One execution attempt to advance a task | Attempt, execution (ambiguous with RunExecution) |
| **Run Outcome** | The terminal status of a run: `queued`, `running`, `review`, `succeeded`, `failed`, or `canceled` | State, phase |
| **Instruction** | The user-supplied prompt or directive that a run executes against | Prompt, message, request |

## Execution Infrastructure

| Term | Definition | Aliases to avoid |
|------|-----------|-----------------|
| **RunExecution** | Detached execution metadata for a run: container identity and observed lifecycle state | Execution (alone, too vague), container |
| **Execution Handle** | Deterministic identifier (backend, name, container ID) enabling adoption and cleanup across control-plane restarts | Container ref, execution ref |
| **Detached Execution** | A Docker container execution that continues independently of the `rascald` process that launched it | Background job, async process |
| **Adoption** | When a newly active or restarted `rascald` instance recovers a persisted execution handle and resumes supervision | Recovery, reconnect |

## Actors and Roles

| Term | Definition | Aliases to avoid |
|------|-----------|-----------------|
| **Runner** | The control-plane abstraction that starts, inspects, stops, and removes detached executions | Launcher (acceptable internal alias), executor |
| **Worker** | The in-execution component that performs a run inside the launched container environment | Agent (overloaded), runner (confusing with Runner) |
| **Scheduler** | The orchestrator subsystem that dispatches queued runs to workers | Dispatcher, queue processor |

## Runtime Configuration

| Term | Definition | Aliases to avoid |
|------|-----------|-----------------|
| **Runtime** | The user-facing selection that determines both the harness and the model provider (`goose-codex`, `codex`, `claude`, `goose-claude`) | Agent type, mode |
| **Harness** | The tool wrapper invoked by the worker, derived from the runtime (`goose` or `direct`) | Framework, CLI wrapper |
| **Model Provider** | The underlying model service used by a runtime (`codex`, `anthropic`, `gemini`) | Backend (overloaded with container backend), API |

## Session Management

| Term | Definition | Aliases to avoid |
|------|-----------|-----------------|
| **Task Session** | Optional task-scoped harness state used to resume context across later runs | Context, memory, conversation |
| **Task Session Policy** | Policy governing whether a task-scoped session may resume: `off`, `pr-only`, or `all` | Session mode (acceptable internal alias) |

## Credentials

| Term | Definition | Aliases to avoid |
|------|-----------|-----------------|
| **Stored Credential** | An encrypted auth payload persisted in Rascal state, tagged with a provider that determines which runtimes can use it | Key, secret, token |
| **Credential Lease** | A temporary, time-bounded assignment of a stored credential to exactly one run | Lock, reservation, checkout |
| **Credential Provider** | The credential family a run needs, derived from its runtime (`codex` for codex/goose-codex, `anthropic` for claude/goose-claude) | Auth type, credential kind |

## Control and Scheduling

| Term | Definition | Aliases to avoid |
|------|-----------|-----------------|
| **Control Plane** | `rascal` (CLI) plus `rascald` (server) — accepts work, persists state, schedules runs, and supervises execution | Server, backend |
| **Execution Plane** | The set of detached Docker containers running `rascal-runner` instances | Worker pool, container fleet |
| **Scheduler Pause** | A temporary, time-bounded suspension of all task scheduling, triggered by control-plane conditions such as provider usage limits | Freeze, cooldown, backoff |

## Deployment

| Term | Definition | Aliases to avoid |
|------|-----------|-----------------|
| **Active Slot** | The currently live `rascald` instance in a blue/green deploy that processes webhook traffic | Primary, leader |
| **Inactive Slot** | The standby `rascald` instance prepared during a blue/green deploy before cutover | Secondary, standby |
| **Cutover** | The moment traffic flips from the active slot to the newly deployed slot | Switch, failover (implies failure) |
| **Draining** | Shutdown mode where the old slot stops accepting work and relinquishes run supervision without canceling detached execution | Graceful shutdown (too generic) |
| **Rollback** | Restoring traffic and service ownership to the previously healthy slot if deploy activation fails | Revert |
| **Runner Image** | A Docker image used to execute runs, maintained separately per runtime | Container image (too generic) |

## Relationships

- A **Task** contains one or more **Runs** (serial, not parallel)
- A **Run** produces exactly one **RunExecution** when it enters the execution plane
- A **RunExecution** is identified by an **Execution Handle** that survives control-plane restarts
- A **Runtime** derives exactly one **Harness** and one **Model Provider**
- A **Runtime** also determines which **Credential Provider** the run needs
- A **Credential Lease** binds exactly one **Stored Credential** to exactly one **Run**
- A **Task Session** is scoped to a **Task** and reset when the task switches **Runtimes**
- A **Scheduler Pause** halts the **Scheduler** globally — existing **Detached Executions** continue unaffected
- During **Cutover**, the new **Active Slot** performs **Adoption** of all persisted **Execution Handles**

## Example dialogue

> **Dev:** "When a user triggers a new **Run** on a **Task**, how does Rascal pick which credential to use?"
>
> **Domain expert:** "The **Scheduler** looks at the **Run**'s **Runtime**, derives the **Credential Provider** from it, then asks the credential broker for a **Credential Lease** on an available **Stored Credential** tagged with that provider."
>
> **Dev:** "What if we're in a **Scheduler Pause** because the provider hit usage limits?"
>
> **Domain expert:** "The **Run** stays `queued`. The **Scheduler Pause** only blocks dispatch — any **Detached Executions** already running continue. Once the pause expires, the **Scheduler** resumes and the **Run** gets a **Credential Lease** normally."
>
> **Dev:** "And during a **Cutover**, does the new **Active Slot** need to re-lease credentials?"
>
> **Domain expert:** "No. **Adoption** recovers the **Execution Handle** and its associated **Credential Lease**. The lease is tied to the **Run**, not the slot."

## Flagged ambiguities

- **"Execution"** alone is ambiguous — it could mean a **Run** (business concept), a **RunExecution** (infrastructure metadata), or a **Detached Execution** (the actual container). Always qualify with the full term.
- **"Agent"** is used as a code namespace (`internal/agent/`) for runtime configuration types, not as a domain entity. Avoid using "agent" to refer to the worker, the harness, or the runtime. Prefer the specific term.
- **"SessionPolicy" vs "TaskSessionPolicy" vs "SessionMode"** — code uses these as aliases. Canonical term should be **Task Session Policy** to make the task-scoping explicit.
- **"Worker pause"** in the orchestrator code (`workerPauseScope = "workers"`) actually pauses the **Scheduler**, not individual **Workers**. The canonical term **Scheduler Pause** better reflects what is actually suspended.
- **"Backend"** is overloaded — used for container backend type (in **Execution Handle**) and could be confused with **Model Provider**. Context usually disambiguates, but prefer the specific term when writing documentation.
