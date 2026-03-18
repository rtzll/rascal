package credentials

import (
	"time"

	"github.com/rtzll/rascal/internal/credentialstrategy"
	"github.com/rtzll/rascal/internal/state"
)

type AcquireRequest struct {
	RunID             string
	UserID            string
	CredentialRuntime string
}

type Lease struct {
	ID           string
	CredentialID string
	RunID        string
	UserID       string
	Strategy     credentialstrategy.Name
	AcquiredAt   time.Time
	ExpiresAt    time.Time
	AuthBlob     []byte
}

type CredentialState struct {
	ID            string
	OwnerUserID   string
	Scope         state.CredentialScope
	Weight        int
	Status        state.CredentialStatus
	CooldownUntil *time.Time
	ActiveLeases  int
	UsageTokens   int64
	UsageRuns     int64
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
