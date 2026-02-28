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

type Run struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	Repo       string    `json:"repo"`
	Task       string    `json:"task"`
	BaseBranch string    `json:"base_branch"`
	HeadBranch string    `json:"head_branch"`
	Trigger    string    `json:"trigger"`
	Status     RunStatus `json:"status"`
	RunDir     string    `json:"run_dir"`

	PRNumber int    `json:"pr_number,omitempty"`
	PRURL    string `json:"pr_url,omitempty"`
	Error    string `json:"error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateRunInput struct {
	ID         string
	TaskID     string
	Repo       string
	Task       string
	BaseBranch string
	HeadBranch string
	Trigger    string
	RunDir     string
}
