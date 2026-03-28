package state

import (
	"fmt"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
)

type RunStatus string

const (
	StatusQueued    RunStatus = "queued"
	StatusRunning   RunStatus = "running"
	StatusReview    RunStatus = "review"
	StatusSucceeded RunStatus = "succeeded"
	StatusFailed    RunStatus = "failed"
	StatusCanceled  RunStatus = "canceled"
)

type RunStatusReason string

const (
	RunStatusReasonNone                 RunStatusReason = ""
	RunStatusReasonUserCanceled         RunStatusReason = "user_canceled"
	RunStatusReasonIssueClosed          RunStatusReason = "issue_closed"
	RunStatusReasonIssueEdited          RunStatusReason = "issue_edited"
	RunStatusReasonPRClosed             RunStatusReason = "pr_closed"
	RunStatusReasonPRMerged             RunStatusReason = "pr_merged"
	RunStatusReasonReviewThreadResolved RunStatusReason = "review_thread_resolved"
	RunStatusReasonTaskCompleted        RunStatusReason = "task_completed"
	RunStatusReasonShutdown             RunStatusReason = "shutdown"
	RunStatusReasonCredentialLeaseLost  RunStatusReason = "credential_lease_lost"
)

func NormalizeRunStatusReason(reason RunStatusReason) RunStatusReason {
	switch strings.ToLower(strings.TrimSpace(string(reason))) {
	case string(RunStatusReasonUserCanceled):
		return RunStatusReasonUserCanceled
	case string(RunStatusReasonIssueClosed):
		return RunStatusReasonIssueClosed
	case string(RunStatusReasonIssueEdited):
		return RunStatusReasonIssueEdited
	case string(RunStatusReasonPRClosed):
		return RunStatusReasonPRClosed
	case string(RunStatusReasonPRMerged):
		return RunStatusReasonPRMerged
	case string(RunStatusReasonReviewThreadResolved):
		return RunStatusReasonReviewThreadResolved
	case string(RunStatusReasonTaskCompleted):
		return RunStatusReasonTaskCompleted
	case string(RunStatusReasonShutdown):
		return RunStatusReasonShutdown
	case string(RunStatusReasonCredentialLeaseLost):
		return RunStatusReasonCredentialLeaseLost
	default:
		return RunStatusReasonNone
	}
}

var runStatusTransitions = map[RunStatus]map[RunStatus]struct{}{
	StatusQueued: {
		StatusQueued:   {},
		StatusRunning:  {},
		StatusFailed:   {},
		StatusCanceled: {},
	},
	StatusRunning: {
		StatusQueued:    {}, // allow lease recovery requeue
		StatusRunning:   {},
		StatusReview:    {},
		StatusSucceeded: {},
		StatusFailed:    {},
		StatusCanceled:  {},
	},
	StatusReview: {
		StatusReview:    {},
		StatusSucceeded: {},
		StatusCanceled:  {},
	},
	StatusSucceeded: {
		StatusSucceeded: {},
	},
	StatusFailed: {
		StatusFailed: {},
	},
	StatusCanceled: {
		StatusCanceled: {},
	},
}

func IsFinalRunStatus(status RunStatus) bool {
	switch status {
	case StatusReview, StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

func NormalizeRunStatus(status RunStatus) RunStatus {
	switch strings.ToLower(strings.TrimSpace(string(status))) {
	case string(StatusRunning):
		return StatusRunning
	case string(StatusReview):
		return StatusReview
	case string(StatusSucceeded):
		return StatusSucceeded
	case string(StatusFailed):
		return StatusFailed
	case string(StatusCanceled):
		return StatusCanceled
	default:
		return StatusQueued
	}
}

func ParseRunStatus(raw string) (RunStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(StatusQueued):
		return StatusQueued, true
	case string(StatusRunning):
		return StatusRunning, true
	case string(StatusReview):
		return StatusReview, true
	case string(StatusSucceeded):
		return StatusSucceeded, true
	case string(StatusFailed):
		return StatusFailed, true
	case string(StatusCanceled):
		return StatusCanceled, true
	default:
		return "", false
	}
}

func ValidateRunStatusTransition(from, to RunStatus) error {
	fromRaw := string(from)
	toRaw := string(to)
	from, ok := ParseRunStatus(fromRaw)
	if !ok {
		return fmt.Errorf("invalid current run status %q", fromRaw)
	}
	to, ok = ParseRunStatus(toRaw)
	if !ok {
		return fmt.Errorf("invalid target run status %q", toRaw)
	}
	next := runStatusTransitions[from]
	if _, ok := next[to]; !ok {
		return fmt.Errorf("invalid run status transition %q -> %q", from, to)
	}
	return nil
}

func NormalizeRepo(repo string) string {
	return strings.ToLower(strings.TrimSpace(repo))
}

type PRStatus string

const (
	PRStatusNone           PRStatus = "none"
	PRStatusOpen           PRStatus = "open"
	PRStatusMerged         PRStatus = "merged"
	PRStatusClosedUnmerged PRStatus = "closed_unmerged"
)

type TaskStatus string

const (
	TaskOpen      TaskStatus = "open"
	TaskCompleted TaskStatus = "completed"
)

func NormalizeTaskStatus(status TaskStatus) TaskStatus {
	switch strings.ToLower(strings.TrimSpace(string(status))) {
	case string(TaskCompleted):
		return TaskCompleted
	default:
		return TaskOpen
	}
}

func ParseTaskStatus(raw string) (TaskStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(TaskOpen):
		return TaskOpen, true
	case string(TaskCompleted):
		return TaskCompleted, true
	default:
		return "", false
	}
}

type Run struct {
	ID           string          `json:"id"`
	TaskID       string          `json:"task_id"`
	Repo         string          `json:"repo"`
	Instruction  string          `json:"instruction"`
	AgentRuntime runtime.Runtime `json:"agent_runtime"`
	BaseBranch   string          `json:"base_branch"`
	HeadBranch   string          `json:"head_branch"`
	Trigger      runtrigger.Name `json:"trigger"`
	Debug        bool            `json:"debug"`
	Status       RunStatus       `json:"status"`
	RunDir       string          `json:"run_dir"`

	IssueNumber  int             `json:"issue_number,omitempty"`
	PRNumber     int             `json:"pr_number,omitempty"`
	PRURL        string          `json:"pr_url,omitempty"`
	PRStatus     PRStatus        `json:"pr_status"`
	HeadSHA      string          `json:"head_sha,omitempty"`
	Context      string          `json:"context,omitempty"`
	Error        string          `json:"error,omitempty"`
	StatusReason RunStatusReason `json:"status_reason,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Task struct {
	ID           string          `json:"id"`
	Repo         string          `json:"repo"`
	AgentRuntime runtime.Runtime `json:"agent_runtime"`
	IssueNumber  int             `json:"issue_number,omitempty"`
	PRNumber     int             `json:"pr_number,omitempty"`
	Status       TaskStatus      `json:"status"`
	// PendingInput is derived at read time from whether the task has queued runs.
	PendingInput bool   `json:"pending_input"`
	LastRunID    string `json:"last_run_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RunLease struct {
	RunID          string    `json:"run_id"`
	OwnerID        string    `json:"owner_id"`
	HeartbeatAt    time.Time `json:"heartbeat_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type RunExecutionBackend string

const (
	RunExecutionBackendDocker RunExecutionBackend = "docker"
	RunExecutionBackendNoop   RunExecutionBackend = "noop"
)

func NormalizeRunExecutionBackend(backend RunExecutionBackend) RunExecutionBackend {
	switch strings.ToLower(strings.TrimSpace(string(backend))) {
	case string(RunExecutionBackendNoop):
		return RunExecutionBackendNoop
	default:
		return RunExecutionBackendDocker
	}
}

func ParseRunExecutionBackend(raw string) (RunExecutionBackend, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(RunExecutionBackendDocker):
		return RunExecutionBackendDocker, true
	case string(RunExecutionBackendNoop):
		return RunExecutionBackendNoop, true
	default:
		return "", false
	}
}

type RunExecutionStatus string

const (
	RunExecutionStatusCreated  RunExecutionStatus = "created"
	RunExecutionStatusRunning  RunExecutionStatus = "running"
	RunExecutionStatusStopping RunExecutionStatus = "stopping"
	RunExecutionStatusExited   RunExecutionStatus = "exited"
)

func NormalizeRunExecutionStatus(status RunExecutionStatus) RunExecutionStatus {
	switch strings.ToLower(strings.TrimSpace(string(status))) {
	case string(RunExecutionStatusRunning):
		return RunExecutionStatusRunning
	case string(RunExecutionStatusStopping):
		return RunExecutionStatusStopping
	case string(RunExecutionStatusExited):
		return RunExecutionStatusExited
	default:
		return RunExecutionStatusCreated
	}
}

func ParseRunExecutionStatus(raw string) (RunExecutionStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(RunExecutionStatusCreated):
		return RunExecutionStatusCreated, true
	case string(RunExecutionStatusRunning):
		return RunExecutionStatusRunning, true
	case string(RunExecutionStatusStopping):
		return RunExecutionStatusStopping, true
	case string(RunExecutionStatusExited):
		return RunExecutionStatusExited, true
	default:
		return "", false
	}
}

type RunExecution struct {
	RunID          string              `json:"run_id"`
	Backend        RunExecutionBackend `json:"backend"`
	ContainerName  string              `json:"container_name"`
	ContainerID    string              `json:"container_id"`
	Status         RunExecutionStatus  `json:"status"`
	ExitCode       int                 `json:"exit_code"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	LastObservedAt time.Time           `json:"last_observed_at"`
}

type RunTokenUsage struct {
	RunID                 string          `json:"run_id"`
	AgentRuntime          runtime.Runtime `json:"agent_runtime"`
	Provider              string          `json:"provider,omitempty"`
	Model                 string          `json:"model,omitempty"`
	TotalTokens           int64           `json:"total_tokens"`
	InputTokens           *int64          `json:"input_tokens,omitempty"`
	OutputTokens          *int64          `json:"output_tokens,omitempty"`
	CachedInputTokens     *int64          `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens *int64          `json:"reasoning_output_tokens,omitempty"`
	RawUsageJSON          string          `json:"raw_usage_json,omitempty"`
	CapturedAt            time.Time       `json:"captured_at"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

type RunCancelRequest struct {
	RunID       string    `json:"run_id"`
	Reason      string    `json:"reason"`
	Source      string    `json:"source"`
	RequestedAt time.Time `json:"requested_at"`
}

type DeliveryStatus string

const (
	DeliveryStatusProcessing DeliveryStatus = "processing"
	DeliveryStatusProcessed  DeliveryStatus = "processed"
)

func NormalizeDeliveryStatus(status DeliveryStatus) DeliveryStatus {
	switch strings.ToLower(strings.TrimSpace(string(status))) {
	case string(DeliveryStatusProcessed):
		return DeliveryStatusProcessed
	default:
		return DeliveryStatusProcessing
	}
}

func ParseDeliveryStatus(raw string) (DeliveryStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(DeliveryStatusProcessing):
		return DeliveryStatusProcessing, true
	case string(DeliveryStatusProcessed):
		return DeliveryStatusProcessed, true
	default:
		return "", false
	}
}

type CreateRunInput struct {
	ID           string
	TaskID       string
	Repo         string
	Instruction  string
	AgentRuntime runtime.Runtime
	BaseBranch   string
	HeadBranch   string
	Trigger      runtrigger.Name
	Debug        *bool
	RunDir       string
	IssueNumber  int
	PRNumber     int
	PRStatus     PRStatus
	Context      string
}

type UpsertTaskInput struct {
	ID           string
	Repo         string
	AgentRuntime runtime.Runtime
	IssueNumber  int
	PRNumber     int
}

type TaskSession struct {
	TaskID           string          `json:"task_id"`
	AgentRuntime     runtime.Runtime `json:"agent_runtime"`
	RuntimeSessionID string          `json:"runtime_session_id,omitempty"`
	SessionKey       string          `json:"session_key,omitempty"`
	SessionRoot      string          `json:"session_root,omitempty"`
	LastRunID        string          `json:"last_run_id,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type UpsertTaskSessionInput struct {
	TaskID           string
	AgentRuntime     runtime.Runtime
	RuntimeSessionID string
	SessionKey       string
	SessionRoot      string
	LastRunID        string
}
