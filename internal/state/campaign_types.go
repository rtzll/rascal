package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type CampaignState string

const (
	CampaignStateDraft     CampaignState = "draft"
	CampaignStateRunning   CampaignState = "running"
	CampaignStatePaused    CampaignState = "paused"
	CampaignStateCompleted CampaignState = "completed"
	CampaignStateCanceled  CampaignState = "canceled"
	CampaignStateFailed    CampaignState = "failed"
)

var campaignStateTransitions = map[CampaignState]map[CampaignState]struct{}{
	CampaignStateDraft: {
		CampaignStateDraft:    {},
		CampaignStateRunning:  {},
		CampaignStateCanceled: {},
	},
	CampaignStateRunning: {
		CampaignStateRunning:   {},
		CampaignStatePaused:    {},
		CampaignStateCompleted: {},
		CampaignStateCanceled:  {},
		CampaignStateFailed:    {},
	},
	CampaignStatePaused: {
		CampaignStatePaused:   {},
		CampaignStateRunning:  {},
		CampaignStateCanceled: {},
		CampaignStateFailed:   {},
	},
	CampaignStateCompleted: {
		CampaignStateCompleted: {},
	},
	CampaignStateCanceled: {
		CampaignStateCanceled: {},
	},
	CampaignStateFailed: {
		CampaignStateFailed:   {},
		CampaignStatePaused:   {},
		CampaignStateRunning:  {},
		CampaignStateCanceled: {},
	},
}

func ValidateCampaignStateTransition(from, to CampaignState) error {
	next, ok := campaignStateTransitions[from]
	if !ok {
		return fmt.Errorf("invalid current campaign state %q", from)
	}
	if _, ok := next[to]; !ok {
		return fmt.Errorf("invalid campaign state transition %q -> %q", from, to)
	}
	return nil
}

type CampaignItemState string

const (
	CampaignItemStatePending   CampaignItemState = "pending"
	CampaignItemStateQueued    CampaignItemState = "queued"
	CampaignItemStateRunning   CampaignItemState = "running"
	CampaignItemStateReview    CampaignItemState = "review"
	CampaignItemStateSucceeded CampaignItemState = "succeeded"
	CampaignItemStateFailed    CampaignItemState = "failed"
	CampaignItemStateSkipped   CampaignItemState = "skipped"
	CampaignItemStateCanceled  CampaignItemState = "canceled"
)

var campaignItemStateTransitions = map[CampaignItemState]map[CampaignItemState]struct{}{
	CampaignItemStatePending: {
		CampaignItemStatePending:  {},
		CampaignItemStateQueued:   {},
		CampaignItemStateFailed:   {},
		CampaignItemStateSkipped:  {},
		CampaignItemStateCanceled: {},
	},
	CampaignItemStateQueued: {
		CampaignItemStateQueued:    {},
		CampaignItemStateRunning:   {},
		CampaignItemStateReview:    {},
		CampaignItemStateSucceeded: {},
		CampaignItemStateFailed:    {},
		CampaignItemStateSkipped:   {},
		CampaignItemStateCanceled:  {},
	},
	CampaignItemStateRunning: {
		CampaignItemStateRunning:   {},
		CampaignItemStateReview:    {},
		CampaignItemStateSucceeded: {},
		CampaignItemStateFailed:    {},
		CampaignItemStateCanceled:  {},
	},
	CampaignItemStateReview: {
		CampaignItemStateReview:    {},
		CampaignItemStateSucceeded: {},
		CampaignItemStateCanceled:  {},
	},
	CampaignItemStateSucceeded: {
		CampaignItemStateSucceeded: {},
	},
	CampaignItemStateFailed: {
		CampaignItemStateFailed:   {},
		CampaignItemStatePending:  {},
		CampaignItemStateCanceled: {},
	},
	CampaignItemStateSkipped: {
		CampaignItemStateSkipped:  {},
		CampaignItemStatePending:  {},
		CampaignItemStateCanceled: {},
	},
	CampaignItemStateCanceled: {
		CampaignItemStateCanceled: {},
		CampaignItemStatePending:  {},
	},
}

func ValidateCampaignItemStateTransition(from, to CampaignItemState) error {
	next, ok := campaignItemStateTransitions[from]
	if !ok {
		return fmt.Errorf("invalid current campaign item state %q", from)
	}
	if _, ok := next[to]; !ok {
		return fmt.Errorf("invalid campaign item state transition %q -> %q", from, to)
	}
	return nil
}

func CampaignItemStateFromRunStatus(status RunStatus) CampaignItemState {
	switch status {
	case StatusQueued:
		return CampaignItemStateQueued
	case StatusRunning:
		return CampaignItemStateRunning
	case StatusReview:
		return CampaignItemStateReview
	case StatusSucceeded:
		return CampaignItemStateSucceeded
	case StatusFailed:
		return CampaignItemStateFailed
	case StatusCanceled:
		return CampaignItemStateCanceled
	default:
		return CampaignItemStatePending
	}
}

type CampaignExecutionPolicy struct {
	MaxConcurrent     int  `json:"max_concurrent" toml:"max_concurrent"`
	StopAfterFailures int  `json:"stop_after_failures" toml:"stop_after_failures"`
	ContinueOnFailure bool `json:"continue_on_failure" toml:"continue_on_failure"`
	SkipIfOpenPR      bool `json:"skip_if_open_pr" toml:"skip_if_open_pr"`
}

func NormalizeCampaignExecutionPolicy(in CampaignExecutionPolicy) CampaignExecutionPolicy {
	out := in
	if !out.SkipIfOpenPR && out.MaxConcurrent == 0 && out.StopAfterFailures == 0 && !out.ContinueOnFailure {
		out.SkipIfOpenPR = true
	}
	if out.MaxConcurrent <= 0 {
		out.MaxConcurrent = 1
	}
	if out.StopAfterFailures < 0 {
		out.StopAfterFailures = 0
	}
	if out.StopAfterFailures == 0 && !out.ContinueOnFailure {
		out.StopAfterFailures = 1
	}
	return out
}

type Campaign struct {
	ID          string                  `json:"id" toml:"id"`
	Name        string                  `json:"name" toml:"name"`
	Description string                  `json:"description,omitempty" toml:"description,omitempty"`
	State       CampaignState           `json:"state" toml:"state"`
	Policy      CampaignExecutionPolicy `json:"policy" toml:"policy"`
	CreatedAt   time.Time               `json:"created_at" toml:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at" toml:"updated_at"`
	StartedAt   *time.Time              `json:"started_at,omitempty" toml:"started_at,omitempty"`
	CompletedAt *time.Time              `json:"completed_at,omitempty" toml:"completed_at,omitempty"`
}

type CampaignItem struct {
	ID              string            `json:"id" toml:"id"`
	CampaignID      string            `json:"campaign_id" toml:"campaign_id"`
	Order           int               `json:"order" toml:"order"`
	Repo            string            `json:"repo" toml:"repo"`
	Task            string            `json:"task" toml:"task"`
	TaskID          string            `json:"task_id" toml:"task_id"`
	BaseBranch      string            `json:"base_branch,omitempty" toml:"base_branch,omitempty"`
	BackendOverride string            `json:"backend,omitempty" toml:"backend,omitempty"`
	State           CampaignItemState `json:"state" toml:"state"`
	RunID           string            `json:"run_id,omitempty" toml:"run_id,omitempty"`
	SkipReason      string            `json:"skip_reason,omitempty" toml:"skip_reason,omitempty"`
	FailureReason   string            `json:"failure_reason,omitempty" toml:"failure_reason,omitempty"`
	CreatedAt       time.Time         `json:"created_at" toml:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at" toml:"updated_at"`
}

type CampaignItemInput struct {
	Repo            string `json:"repo" toml:"repo"`
	Task            string `json:"task" toml:"task"`
	TaskID          string `json:"task_id,omitempty" toml:"task_id,omitempty"`
	BaseBranch      string `json:"base_branch,omitempty" toml:"base_branch,omitempty"`
	BackendOverride string `json:"backend,omitempty" toml:"backend,omitempty"`
}

type CreateCampaignInput struct {
	ID          string
	Name        string
	Description string
	Policy      CampaignExecutionPolicy
	Items       []CampaignItemInput
}

type CampaignSummary struct {
	TotalItems  int `json:"total_items" toml:"total_items"`
	ActiveItems int `json:"active_items" toml:"active_items"`
	Pending     int `json:"pending" toml:"pending"`
	Queued      int `json:"queued" toml:"queued"`
	Running     int `json:"running" toml:"running"`
	Review      int `json:"review" toml:"review"`
	Succeeded   int `json:"succeeded" toml:"succeeded"`
	Failed      int `json:"failed" toml:"failed"`
	Skipped     int `json:"skipped" toml:"skipped"`
	Canceled    int `json:"canceled" toml:"canceled"`
}

func SummarizeCampaignItems(items []CampaignItem) CampaignSummary {
	summary := CampaignSummary{TotalItems: len(items)}
	for _, item := range items {
		switch item.State {
		case CampaignItemStatePending:
			summary.Pending++
		case CampaignItemStateQueued:
			summary.Queued++
			summary.ActiveItems++
		case CampaignItemStateRunning:
			summary.Running++
			summary.ActiveItems++
		case CampaignItemStateReview:
			summary.Review++
		case CampaignItemStateSucceeded:
			summary.Succeeded++
		case CampaignItemStateFailed:
			summary.Failed++
		case CampaignItemStateSkipped:
			summary.Skipped++
		case CampaignItemStateCanceled:
			summary.Canceled++
		}
	}
	return summary
}

func NewCampaignID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("create campaign id: %w", err)
	}
	return "camp_" + hex.EncodeToString(buf), nil
}

func NormalizeCampaignItemInput(in CampaignItemInput) CampaignItemInput {
	return CampaignItemInput{
		Repo:            NormalizeRepo(in.Repo),
		Task:            strings.TrimSpace(in.Task),
		TaskID:          strings.TrimSpace(in.TaskID),
		BaseBranch:      strings.TrimSpace(in.BaseBranch),
		BackendOverride: strings.TrimSpace(strings.ToLower(in.BackendOverride)),
	}
}
