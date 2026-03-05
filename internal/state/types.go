package state

import (
	"fmt"
	"time"
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

func CanonicalRunStatus(status RunStatus) RunStatus {
	return status
}

func IsFinalRunStatus(status RunStatus) bool {
	switch CanonicalRunStatus(status) {
	case StatusReview, StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

func ValidateRunStatusTransition(from, to RunStatus) error {
	from = CanonicalRunStatus(from)
	to = CanonicalRunStatus(to)
	next, ok := runStatusTransitions[from]
	if !ok {
		return fmt.Errorf("invalid current run status %q", from)
	}
	if _, ok := next[to]; !ok {
		return fmt.Errorf("invalid run status transition %q -> %q", from, to)
	}
	return nil
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

type Run struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	Repo       string    `json:"repo"`
	Task       string    `json:"task"`
	BaseBranch string    `json:"base_branch"`
	HeadBranch string    `json:"head_branch"`
	Trigger    string    `json:"trigger"`
	Debug      bool      `json:"debug"`
	Status     RunStatus `json:"status"`
	RunDir     string    `json:"run_dir"`

	IssueNumber int      `json:"issue_number,omitempty"`
	PRNumber    int      `json:"pr_number,omitempty"`
	PRURL       string   `json:"pr_url,omitempty"`
	PRStatus    PRStatus `json:"pr_status"`
	HeadSHA     string   `json:"head_sha,omitempty"`
	Context     string   `json:"context,omitempty"`
	Error       string   `json:"error,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Task struct {
	ID           string     `json:"id"`
	Repo         string     `json:"repo"`
	IssueNumber  int        `json:"issue_number,omitempty"`
	PRNumber     int        `json:"pr_number,omitempty"`
	Status       TaskStatus `json:"status"`
	PendingInput bool       `json:"pending_input"`
	LastRunID    string     `json:"last_run_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RunLease struct {
	RunID          string    `json:"run_id"`
	OwnerID        string    `json:"owner_id"`
	HeartbeatAt    time.Time `json:"heartbeat_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type RunCancelRequest struct {
	RunID       string    `json:"run_id"`
	Reason      string    `json:"reason"`
	Source      string    `json:"source"`
	RequestedAt time.Time `json:"requested_at"`
}

type CreateRunInput struct {
	ID          string
	TaskID      string
	Repo        string
	Task        string
	BaseBranch  string
	HeadBranch  string
	Trigger     string
	Debug       *bool
	RunDir      string
	IssueNumber int
	PRNumber    int
	PRStatus    PRStatus
	Context     string
}

type UpsertTaskInput struct {
	ID          string
	Repo        string
	IssueNumber int
	PRNumber    int
}
