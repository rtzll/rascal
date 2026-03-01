package state

import "time"

type RunStatus string

const (
	StatusQueued           RunStatus = "queued"
	StatusRunning          RunStatus = "running"
	StatusAwaitingFeedback RunStatus = "awaiting_feedback"
	StatusSucceeded        RunStatus = "succeeded"
	StatusFailed           RunStatus = "failed"
	StatusCanceled         RunStatus = "canceled"
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

	IssueNumber int    `json:"issue_number,omitempty"`
	PRNumber    int    `json:"pr_number,omitempty"`
	PRURL       string `json:"pr_url,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	Context     string `json:"context,omitempty"`
	Error       string `json:"error,omitempty"`

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
	Context     string
}

type UpsertTaskInput struct {
	ID          string
	Repo        string
	IssueNumber int
	PRNumber    int
}
