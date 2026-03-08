package credentials

import "time"

type AcquireRequest struct {
	RunID  string
	UserID string
}

type Lease struct {
	ID           string
	CredentialID string
	RunID        string
	UserID       string
	Strategy     string
	AcquiredAt   time.Time
	ExpiresAt    time.Time
	AuthBlob     []byte
}

type CredentialState struct {
	ID              string
	OwnerUserID     string
	Scope           string
	Weight          int
	MaxActiveLeases int
	Status          string
	CooldownUntil   *time.Time
	ActiveLeases    int
	UsageTokens     int64
	UsageRuns       int64
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
