package api

import (
	"time"

	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

type ServiceStatusResponse struct {
	OK      bool   `json:"ok" toml:"ok"`
	Service string `json:"service" toml:"service"`
	Ready   bool   `json:"ready" toml:"ready"`
}

type CreateTaskRequest struct {
	TaskID     string              `json:"task_id,omitempty"`
	Repo       string              `json:"repo"`
	Task       string              `json:"task"`
	BaseBranch string              `json:"base_branch"`
	Trigger    runtrigger.Name     `json:"trigger,omitempty"`
	Debug      *bool               `json:"debug,omitempty"`
	Pipeline   *RunPipelineRequest `json:"pipeline,omitempty"`
}

type CreateIssueTaskRequest struct {
	Repo        string              `json:"repo"`
	IssueNumber int                 `json:"issue_number"`
	Debug       *bool               `json:"debug,omitempty"`
	Pipeline    *RunPipelineRequest `json:"pipeline,omitempty"`
}

type RunPipelineRequest struct {
	Enabled              bool     `json:"enabled,omitempty"`
	Phases               []string `json:"phases,omitempty"`
	MaxPhases            int      `json:"max_phases,omitempty"`
	MaxChildRunsPerPhase int      `json:"max_child_runs_per_phase,omitempty"`
	TokenBudgetTotal     int64    `json:"token_budget_total,omitempty"`
	WallClockBudgetSecs  int64    `json:"wall_clock_budget_secs,omitempty"`
}

type RunResponse struct {
	Run      state.Run          `json:"run" toml:"run"`
	Lineage  *state.RunLineage  `json:"lineage,omitempty" toml:"lineage,omitempty"`
	Pipeline *state.RunPipeline `json:"pipeline,omitempty" toml:"pipeline,omitempty"`
}

type RunsResponse struct {
	Runs []state.Run `json:"runs" toml:"runs"`
}

type TaskResponse struct {
	Task     state.Task         `json:"task" toml:"task"`
	Pipeline *state.RunPipeline `json:"pipeline,omitempty" toml:"pipeline,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error" toml:"error"`
}

type AcceptedResponse struct {
	Accepted     *bool `json:"accepted,omitempty" toml:"accepted,omitempty"`
	InactiveSlot bool  `json:"inactive_slot,omitempty" toml:"inactive_slot,omitempty"`
	Duplicate    bool  `json:"duplicate,omitempty" toml:"duplicate,omitempty"`
}

type RunCancelResponse struct {
	Run             *state.Run `json:"run,omitempty" toml:"run,omitempty"`
	Canceled        *bool      `json:"canceled,omitempty" toml:"canceled,omitempty"`
	Reason          string     `json:"reason,omitempty" toml:"reason,omitempty"`
	RunID           string     `json:"run_id,omitempty" toml:"run_id,omitempty"`
	CancelRequested *bool      `json:"cancel_requested,omitempty" toml:"cancel_requested,omitempty"`
}

type RunLogsResponse struct {
	Logs      string          `json:"logs" toml:"logs"`
	RunStatus state.RunStatus `json:"run_status" toml:"run_status"`
	Done      bool            `json:"done" toml:"done"`
}

type Credential struct {
	ID            string                 `json:"id" toml:"id"`
	OwnerUserID   string                 `json:"owner_user_id" toml:"owner_user_id"`
	Scope         state.CredentialScope  `json:"scope" toml:"scope"`
	Weight        int                    `json:"weight" toml:"weight"`
	Status        state.CredentialStatus `json:"status" toml:"status"`
	CooldownUntil *time.Time             `json:"cooldown_until,omitempty" toml:"cooldown_until,omitempty"`
	LastError     string                 `json:"last_error,omitempty" toml:"last_error,omitempty"`
	CreatedAt     time.Time              `json:"created_at" toml:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at" toml:"updated_at"`
}

func CredentialFromState(credential state.CodexCredential) Credential {
	return Credential{
		ID:            credential.ID,
		OwnerUserID:   credential.OwnerUserID,
		Scope:         credential.Scope,
		Weight:        credential.Weight,
		Status:        credential.Status,
		CooldownUntil: credential.CooldownUntil,
		LastError:     credential.LastError,
		CreatedAt:     credential.CreatedAt,
		UpdatedAt:     credential.UpdatedAt,
	}
}

type CredentialListResponse struct {
	Credentials []Credential `json:"credentials" toml:"credentials"`
}

type CredentialResponse struct {
	Credential Credential `json:"credential" toml:"credential"`
}

type CredentialDisabledResponse struct {
	Disabled   bool        `json:"disabled" toml:"disabled"`
	Credential *Credential `json:"credential,omitempty" toml:"credential,omitempty"`
}

type CreateCredentialRequest struct {
	ID          string                `json:"id,omitempty"`
	OwnerUserID string                `json:"owner_user_id,omitempty"`
	Scope       state.CredentialScope `json:"scope,omitempty"`
	AuthBlob    string                `json:"auth_blob"`
	Weight      int                   `json:"weight,omitempty"`
}

type UpdateCredentialRequest struct {
	OwnerUserID   *string                 `json:"owner_user_id,omitempty"`
	Scope         *state.CredentialScope  `json:"scope,omitempty"`
	AuthBlob      *string                 `json:"auth_blob,omitempty"`
	Weight        *int                    `json:"weight,omitempty"`
	Status        *state.CredentialStatus `json:"status,omitempty"`
	CooldownUntil *string                 `json:"cooldown_until,omitempty"`
	LastError     *string                 `json:"last_error,omitempty"`
}
