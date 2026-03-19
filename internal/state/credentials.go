package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/credentialstrategy"
	"github.com/rtzll/rascal/internal/state/sqlitegen"
)

type UserRole string

const (
	UserRoleUser  UserRole = "user"
	UserRoleAdmin UserRole = "admin"
)

type User struct {
	ID            string    `json:"id"`
	ExternalLogin string    `json:"external_login"`
	Role          UserRole  `json:"role"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type APIPrincipal struct {
	APIKeyID      string
	UserID        string
	ExternalLogin string
	Role          UserRole
}

type RunCredentialInfo struct {
	RunID           string
	CreatedByUserID string
	CredentialID    string
}

type CredentialScope string

const (
	CredentialScopePersonal CredentialScope = "personal"
	CredentialScopeShared   CredentialScope = "shared"
)

func NormalizeCredentialScope(scope CredentialScope) CredentialScope {
	switch strings.ToLower(strings.TrimSpace(string(scope))) {
	case string(CredentialScopeShared):
		return CredentialScopeShared
	default:
		return CredentialScopePersonal
	}
}

func ParseCredentialScope(scope string) (CredentialScope, bool) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "":
		return CredentialScopePersonal, true
	case string(CredentialScopePersonal):
		return CredentialScopePersonal, true
	case string(CredentialScopeShared):
		return CredentialScopeShared, true
	default:
		return "", false
	}
}

type CredentialStatus string

const (
	CredentialStatusActive   CredentialStatus = "active"
	CredentialStatusDisabled CredentialStatus = "disabled"
	CredentialStatusCooldown CredentialStatus = "cooldown"
)

func NormalizeCredentialStatus(status CredentialStatus) CredentialStatus {
	switch strings.ToLower(strings.TrimSpace(string(status))) {
	case string(CredentialStatusDisabled):
		return CredentialStatusDisabled
	case string(CredentialStatusCooldown):
		return CredentialStatusCooldown
	default:
		return CredentialStatusActive
	}
}

func ParseCredentialStatus(status string) (CredentialStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "":
		return CredentialStatusActive, true
	case string(CredentialStatusActive):
		return CredentialStatusActive, true
	case string(CredentialStatusDisabled):
		return CredentialStatusDisabled, true
	case string(CredentialStatusCooldown):
		return CredentialStatusCooldown, true
	default:
		return "", false
	}
}

type Credential struct {
	ID                string
	OwnerUserID       string
	Scope             CredentialScope
	Provider          string
	EncryptedAuthBlob []byte
	Weight            int
	Status            CredentialStatus
	CooldownUntil     *time.Time
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type CredentialCandidate struct {
	ID            string
	OwnerUserID   string
	Scope         CredentialScope
	Provider      string
	Weight        int
	Status        CredentialStatus
	CooldownUntil *time.Time
	ActiveLeases  int
	UsageTokens   int64
	UsageRuns     int64
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CredentialLease struct {
	ID           string
	CredentialID string
	RunID        string
	UserID       string
	Strategy     credentialstrategy.Name
	AcquiredAt   time.Time
	ExpiresAt    time.Time
	ReleasedAt   *time.Time
}

type UpsertUserInput struct {
	ID            string
	ExternalLogin string
	Role          UserRole
}

type UpsertAPIKeyInput struct {
	ID      string
	UserID  string
	KeyHash string
	Label   string
}

type CreateCredentialInput struct {
	ID                string
	OwnerUserID       string
	Scope             CredentialScope
	Provider          string
	EncryptedAuthBlob []byte
	Weight            int
	Status            CredentialStatus
	CooldownUntil     *time.Time
	LastError         string
}

type UpdateCredentialInput struct {
	ID                string
	OwnerUserID       string
	Scope             CredentialScope
	Provider          string
	EncryptedAuthBlob []byte
	Weight            int
	Status            CredentialStatus
	CooldownUntil     *time.Time
	LastError         string
}

type CreateCredentialLeaseInput struct {
	ID           string
	CredentialID string
	RunID        string
	UserID       string
	Strategy     credentialstrategy.Name
	AcquiredAt   time.Time
	ExpiresAt    time.Time
	Now          time.Time
}

func normalizeUserRole(role UserRole) UserRole {
	switch strings.ToLower(strings.TrimSpace(string(role))) {
	case string(UserRoleAdmin):
		return UserRoleAdmin
	default:
		return UserRoleUser
	}
}

func (s *Store) UpsertUser(in UpsertUserInput) (User, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.ExternalLogin = strings.TrimSpace(in.ExternalLogin)
	in.Role = normalizeUserRole(in.Role)
	if in.ID == "" || in.ExternalLogin == "" {
		return User{}, fmt.Errorf("id and external_login are required")
	}
	now := time.Now().UTC().UnixNano()
	if err := s.q.UpsertUser(context.Background(), sqlitegen.UpsertUserParams{
		ID:            in.ID,
		ExternalLogin: in.ExternalLogin,
		Role:          string(in.Role),
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		return User{}, fmt.Errorf("upsert user %s: %w", in.ID, err)
	}
	row, err := s.q.GetUserByID(context.Background(), in.ID)
	if err != nil {
		return User{}, fmt.Errorf("get user %s after upsert: %w", in.ID, err)
	}
	return fromDBUser(row), nil
}

func (s *Store) GetUserByID(userID string) (User, bool) {
	row, err := s.q.GetUserByID(context.Background(), strings.TrimSpace(userID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, false
		}
		return User{}, false
	}
	return fromDBUser(row), true
}

func (s *Store) GetUserByExternalLogin(externalLogin string) (User, bool) {
	row, err := s.q.GetUserByExternalLogin(context.Background(), strings.TrimSpace(externalLogin))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, false
		}
		return User{}, false
	}
	return fromDBUser(row), true
}

func (s *Store) UpsertAPIKey(in UpsertAPIKeyInput) error {
	in.ID = strings.TrimSpace(in.ID)
	in.UserID = strings.TrimSpace(in.UserID)
	in.KeyHash = strings.TrimSpace(in.KeyHash)
	in.Label = strings.TrimSpace(in.Label)
	if in.ID == "" || in.UserID == "" || in.KeyHash == "" {
		return fmt.Errorf("id, user_id and key_hash are required")
	}
	now := time.Now().UTC().UnixNano()
	if err := s.q.UpsertAPIKey(context.Background(), sqlitegen.UpsertAPIKeyParams{
		ID:         in.ID,
		UserID:     in.UserID,
		KeyHash:    in.KeyHash,
		Label:      in.Label,
		CreatedAt:  now,
		LastUsedAt: now,
		DisabledAt: sql.NullInt64{},
	}); err != nil {
		return fmt.Errorf("upsert api key %s for user %s: %w", in.ID, in.UserID, err)
	}
	return nil
}

func (s *Store) ResolveAPIPrincipalByKeyHash(keyHash string) (APIPrincipal, bool, error) {
	keyHash = strings.TrimSpace(keyHash)
	if keyHash == "" {
		return APIPrincipal{}, false, nil
	}
	row, err := s.q.GetAPIKeyPrincipal(context.Background(), keyHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIPrincipal{}, false, nil
		}
		return APIPrincipal{}, false, fmt.Errorf("resolve api principal by key hash: %w", err)
	}
	if _, err := s.q.TouchAPIKeyLastUsed(context.Background(), sqlitegen.TouchAPIKeyLastUsedParams{
		LastUsedAt: time.Now().UTC().UnixNano(),
		ID:         row.ApiKeyID,
	}); err != nil {
		log.Printf("touch api key last used for %s failed: %v", row.ApiKeyID, err)
	}
	return APIPrincipal{
		APIKeyID:      row.ApiKeyID,
		UserID:        row.UserID,
		ExternalLogin: row.ExternalLogin,
		Role:          normalizeUserRole(UserRole(row.Role)),
	}, true, nil
}

func (s *Store) SetTaskCreatedByUser(taskID, userID string) error {
	taskID = strings.TrimSpace(taskID)
	userID = strings.TrimSpace(userID)
	if taskID == "" || userID == "" {
		return nil
	}
	_, err := s.q.SetTaskCreatedByUser(context.Background(), sqlitegen.SetTaskCreatedByUserParams{
		CreatedByUserID: userID,
		UpdatedAt:       time.Now().UTC().UnixNano(),
		ID:              taskID,
	})
	if err != nil {
		return fmt.Errorf("set task %s created_by_user_id to %s: %w", taskID, userID, err)
	}
	return nil
}

func (s *Store) SetRunCreatedByUser(runID, userID string) error {
	runID = strings.TrimSpace(runID)
	userID = strings.TrimSpace(userID)
	if runID == "" || userID == "" {
		return nil
	}
	_, err := s.q.SetRunCreatedByUser(context.Background(), sqlitegen.SetRunCreatedByUserParams{
		CreatedByUserID: userID,
		UpdatedAt:       time.Now().UTC().UnixNano(),
		ID:              runID,
	})
	if err != nil {
		return fmt.Errorf("set run %s created_by_user_id to %s: %w", runID, userID, err)
	}
	return nil
}

func (s *Store) SetRunCredentialID(runID, credentialID string) error {
	runID = strings.TrimSpace(runID)
	credentialID = strings.TrimSpace(credentialID)
	if runID == "" {
		return nil
	}
	_, err := s.q.SetRunCredentialID(context.Background(), sqlitegen.SetRunCredentialIDParams{
		CredentialID: credentialID,
		UpdatedAt:    time.Now().UTC().UnixNano(),
		ID:           runID,
	})
	if err != nil {
		return fmt.Errorf("set run %s credential_id to %s: %w", runID, credentialID, err)
	}
	return nil
}

func (s *Store) GetRunCredentialInfo(runID string) (RunCredentialInfo, bool) {
	row, err := s.q.GetRunCredentialInfo(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunCredentialInfo{}, false
		}
		return RunCredentialInfo{}, false
	}
	return RunCredentialInfo{
		RunID:           row.ID,
		CreatedByUserID: row.CreatedByUserID,
		CredentialID:    row.CredentialID,
	}, true
}

func (s *Store) CreateCredential(in CreateCredentialInput) (Credential, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.OwnerUserID = strings.TrimSpace(in.OwnerUserID)
	if in.ID == "" {
		return Credential{}, fmt.Errorf("id is required")
	}
	scope, ok := ParseCredentialScope(string(in.Scope))
	if !ok {
		return Credential{}, fmt.Errorf("invalid credential scope %q", in.Scope)
	}
	status, ok := ParseCredentialStatus(string(in.Status))
	if !ok {
		return Credential{}, fmt.Errorf("invalid credential status %q", in.Status)
	}
	in.Scope = scope
	in.Status = status
	if in.Weight <= 0 {
		in.Weight = 1
	}
	now := time.Now().UTC()
	if err := s.q.CreateCredential(context.Background(), sqlitegen.CreateCredentialParams{
		ID:                in.ID,
		OwnerUserID:       toNullString(in.OwnerUserID),
		Scope:             string(in.Scope),
		Provider:          strings.TrimSpace(in.Provider),
		EncryptedAuthBlob: in.EncryptedAuthBlob,
		Weight:            int64(in.Weight),
		Status:            string(in.Status),
		CooldownUntil:     toNullInt64(in.CooldownUntil),
		LastError:         in.LastError,
		CreatedAt:         now.UnixNano(),
		UpdatedAt:         now.UnixNano(),
	}); err != nil {
		return Credential{}, fmt.Errorf("create credential %s: %w", in.ID, err)
	}
	out, ok, err := s.GetCredential(in.ID)
	if err != nil {
		return Credential{}, fmt.Errorf("update credential %s: %w", in.ID, err)
	}
	if !ok {
		return Credential{}, fmt.Errorf("credential %q not found after create", in.ID)
	}
	return out, nil
}

func (s *Store) UpdateCredential(in UpdateCredentialInput) (Credential, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.OwnerUserID = strings.TrimSpace(in.OwnerUserID)
	if in.ID == "" {
		return Credential{}, fmt.Errorf("id is required")
	}
	scope, ok := ParseCredentialScope(string(in.Scope))
	if !ok {
		return Credential{}, fmt.Errorf("invalid credential scope %q", in.Scope)
	}
	status, ok := ParseCredentialStatus(string(in.Status))
	if !ok {
		return Credential{}, fmt.Errorf("invalid credential status %q", in.Status)
	}
	in.Scope = scope
	in.Status = status
	if in.Weight <= 0 {
		in.Weight = 1
	}
	rows, err := s.q.UpdateCredential(context.Background(), sqlitegen.UpdateCredentialParams{
		OwnerUserID:       toNullString(in.OwnerUserID),
		Scope:             string(in.Scope),
		Provider:          strings.TrimSpace(in.Provider),
		EncryptedAuthBlob: in.EncryptedAuthBlob,
		Weight:            int64(in.Weight),
		Status:            string(in.Status),
		CooldownUntil:     toNullInt64(in.CooldownUntil),
		LastError:         in.LastError,
		UpdatedAt:         time.Now().UTC().UnixNano(),
		ID:                in.ID,
	})
	if err != nil {
		return Credential{}, fmt.Errorf("update credential %s: %w", in.ID, err)
	}
	if rows == 0 {
		return Credential{}, fmt.Errorf("credential %q not found", in.ID)
	}
	out, ok, err := s.GetCredential(in.ID)
	if err != nil {
		return Credential{}, err
	}
	if !ok {
		return Credential{}, fmt.Errorf("credential %q not found after update", in.ID)
	}
	return out, nil
}

func (s *Store) SetCredentialStatus(credentialID string, status CredentialStatus, cooldownUntil *time.Time, lastError string) error {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return nil
	}
	parsed, ok := ParseCredentialStatus(string(status))
	if !ok {
		return fmt.Errorf("invalid credential status %q", status)
	}
	status = parsed
	_, err := s.q.SetCredentialStatus(context.Background(), sqlitegen.SetCredentialStatusParams{
		Status:        string(status),
		CooldownUntil: toNullInt64(cooldownUntil),
		LastError:     strings.TrimSpace(lastError),
		UpdatedAt:     time.Now().UTC().UnixNano(),
		ID:            credentialID,
	})
	if err != nil {
		return fmt.Errorf("set credential %s status to %s: %w", credentialID, status, err)
	}
	return nil
}

func (s *Store) GetCredential(credentialID string) (Credential, bool, error) {
	row, err := s.q.GetCredential(context.Background(), strings.TrimSpace(credentialID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Credential{}, false, nil
		}
		return Credential{}, false, fmt.Errorf("get credential %s: %w", credentialID, err)
	}
	return fromDBCredential(row), true, nil
}

func (s *Store) ListCredentialsByOwner(ownerUserID string) ([]Credential, error) {
	rows, err := s.q.ListCredentialsByOwner(context.Background(), toNullString(ownerUserID))
	if err != nil {
		return nil, fmt.Errorf("list credentials by owner %s: %w", ownerUserID, err)
	}
	out := make([]Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBCredential(row))
	}
	return out, nil
}

func (s *Store) ListSharedCredentials() ([]Credential, error) {
	rows, err := s.q.ListSharedCredentials(context.Background())
	if err != nil {
		return nil, fmt.Errorf("list shared credentials: %w", err)
	}
	out := make([]Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBCredential(row))
	}
	return out, nil
}

func (s *Store) ListAllCredentials() ([]Credential, error) {
	rows, err := s.q.ListAllCredentials(context.Background())
	if err != nil {
		return nil, fmt.Errorf("list all credentials: %w", err)
	}
	out := make([]Credential, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBCredential(row))
	}
	return out, nil
}

func (s *Store) ListCredentialCandidates(requesterUserID, provider string, now, usageWindowStart time.Time) ([]CredentialCandidate, error) {
	if strings.TrimSpace(provider) == "" {
		provider = "codex"
	}
	rows, err := s.q.ListCredentialCandidates(context.Background(), sqlitegen.ListCredentialCandidatesParams{
		Now:              now.UTC().UnixNano(),
		UsageWindowStart: usageWindowStart.UTC().UnixNano(),
		RequesterUserID:  toNullString(requesterUserID),
		Provider:         provider,
	})
	if err != nil {
		return nil, fmt.Errorf("list credential candidates for %s: %w", requesterUserID, err)
	}
	out := make([]CredentialCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, CredentialCandidate{
			ID:            row.ID,
			OwnerUserID:   fromNullString(row.OwnerUserID),
			Scope:         NormalizeCredentialScope(CredentialScope(row.Scope)),
			Provider:      row.Provider,
			Weight:        int(row.Weight),
			Status:        NormalizeCredentialStatus(CredentialStatus(row.Status)),
			CooldownUntil: fromNullTime(row.CooldownUntil),
			ActiveLeases:  int(row.ActiveLeases),
			UsageTokens:   row.UsageTokens,
			UsageRuns:     row.UsageRuns,
			LastError:     row.LastError,
			CreatedAt:     time.Unix(0, row.CreatedAt).UTC(),
			UpdatedAt:     time.Unix(0, row.UpdatedAt).UTC(),
		})
	}
	return out, nil
}

func (s *Store) TryCreateCredentialLease(in CreateCredentialLeaseInput) (bool, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.CredentialID = strings.TrimSpace(in.CredentialID)
	in.RunID = strings.TrimSpace(in.RunID)
	in.UserID = strings.TrimSpace(in.UserID)
	in.Strategy = credentialstrategy.NormalizeName(in.Strategy.String())
	if in.ID == "" || in.CredentialID == "" || in.RunID == "" || in.UserID == "" {
		return false, fmt.Errorf("id, credential_id, run_id and user_id are required")
	}
	if in.AcquiredAt.IsZero() {
		in.AcquiredAt = time.Now().UTC()
	}
	if in.ExpiresAt.IsZero() || !in.ExpiresAt.After(in.AcquiredAt) {
		in.ExpiresAt = in.AcquiredAt.Add(90 * time.Second)
	}
	if in.Now.IsZero() {
		in.Now = in.AcquiredAt
	}
	rows, err := s.q.TryCreateCredentialLease(context.Background(), sqlitegen.TryCreateCredentialLeaseParams{
		ID:           in.ID,
		CredentialID: in.CredentialID,
		RunID:        in.RunID,
		UserID:       in.UserID,
		Strategy:     in.Strategy.String(),
		AcquiredAt:   in.AcquiredAt.UTC().UnixNano(),
		ExpiresAt:    in.ExpiresAt.UTC().UnixNano(),
		Now:          sql.NullInt64{Int64: in.Now.UTC().UnixNano(), Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("create credential lease %s for credential %s: %w", in.ID, in.CredentialID, err)
	}
	return rows > 0, nil
}

func (s *Store) GetCredentialLease(leaseID string) (CredentialLease, bool, error) {
	row, err := s.q.GetCredentialLease(context.Background(), strings.TrimSpace(leaseID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CredentialLease{}, false, nil
		}
		return CredentialLease{}, false, fmt.Errorf("get credential lease %s: %w", leaseID, err)
	}
	return fromDBCredentialLease(row), true, nil
}

func (s *Store) GetActiveCredentialLeaseByRunID(runID string) (CredentialLease, bool, error) {
	row, err := s.q.GetActiveCredentialLeaseByRunID(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CredentialLease{}, false, nil
		}
		return CredentialLease{}, false, fmt.Errorf("get active credential lease for run %s: %w", runID, err)
	}
	return fromDBCredentialLease(row), true, nil
}

func (s *Store) RenewCredentialLease(leaseID string, expiresAt, now time.Time) (bool, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return false, fmt.Errorf("lease id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if expiresAt.IsZero() || !expiresAt.After(now) {
		expiresAt = now.Add(90 * time.Second)
	}
	rows, err := s.q.RenewCredentialLease(context.Background(), sqlitegen.RenewCredentialLeaseParams{
		ExpiresAt:   expiresAt.UTC().UnixNano(),
		ID:          leaseID,
		ExpiresAt_2: now.UTC().UnixNano(),
	})
	if err != nil {
		return false, fmt.Errorf("renew credential lease %s: %w", leaseID, err)
	}
	return rows > 0, nil
}

func (s *Store) ReleaseCredentialLease(leaseID string) (CredentialLease, bool, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return CredentialLease{}, false, nil
	}
	row, err := s.q.ReleaseCredentialLease(context.Background(), sqlitegen.ReleaseCredentialLeaseParams{
		ReleasedAt: sql.NullInt64{Int64: time.Now().UTC().UnixNano(), Valid: true},
		ID:         leaseID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CredentialLease{}, false, nil
		}
		return CredentialLease{}, false, fmt.Errorf("release credential lease %s: %w", leaseID, err)
	}
	return fromDBCredentialLease(row), true, nil
}

func (s *Store) ReleaseCredentialLeaseByRunID(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	_, err := s.q.ReleaseCredentialLeaseByRunID(context.Background(), sqlitegen.ReleaseCredentialLeaseByRunIDParams{
		ReleasedAt: sql.NullInt64{Int64: time.Now().UTC().UnixNano(), Valid: true},
		RunID:      runID,
	})
	if err != nil {
		return fmt.Errorf("release credential lease by run %s: %w", runID, err)
	}
	return nil
}

func (s *Store) ReclaimExpiredCredentialLeases(now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := s.q.ReclaimExpiredCredentialLeases(context.Background(), sqlitegen.ReclaimExpiredCredentialLeasesParams{
		ReleasedAt: sql.NullInt64{Int64: now.UTC().UnixNano(), Valid: true},
		ExpiresAt:  now.UTC().UnixNano(),
	})
	if err != nil {
		return 0, fmt.Errorf("reclaim expired credential leases before %s: %w", now.UTC().Format(time.RFC3339), err)
	}
	return int(rows), nil
}

func (s *Store) UpsertCredentialUsage(credentialID string, windowStart time.Time, tokens, runs int64) error {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return nil
	}
	if windowStart.IsZero() {
		windowStart = time.Now().UTC().Truncate(time.Hour)
	}
	if err := s.q.UpsertCredentialUsage(context.Background(), sqlitegen.UpsertCredentialUsageParams{
		CredentialID: credentialID,
		WindowStart:  windowStart.UTC().UnixNano(),
		Tokens:       tokens,
		Runs:         runs,
	}); err != nil {
		return fmt.Errorf("upsert credential usage for %s: %w", credentialID, err)
	}
	return nil
}

func fromDBUser(row sqlitegen.User) User {
	return User{
		ID:            row.ID,
		ExternalLogin: row.ExternalLogin,
		Role:          normalizeUserRole(UserRole(row.Role)),
		CreatedAt:     time.Unix(0, row.CreatedAt).UTC(),
		UpdatedAt:     time.Unix(0, row.UpdatedAt).UTC(),
	}
}

func fromDBCredential(row sqlitegen.Credential) Credential {
	return Credential{
		ID:                row.ID,
		OwnerUserID:       fromNullString(row.OwnerUserID),
		Scope:             NormalizeCredentialScope(CredentialScope(row.Scope)),
		Provider:          row.Provider,
		EncryptedAuthBlob: append([]byte(nil), row.EncryptedAuthBlob...),
		Weight:            int(row.Weight),
		Status:            NormalizeCredentialStatus(CredentialStatus(row.Status)),
		CooldownUntil:     fromNullTime(row.CooldownUntil),
		LastError:         row.LastError,
		CreatedAt:         time.Unix(0, row.CreatedAt).UTC(),
		UpdatedAt:         time.Unix(0, row.UpdatedAt).UTC(),
	}
}

func fromDBCredentialLease(row sqlitegen.CredentialLease) CredentialLease {
	return CredentialLease{
		ID:           row.ID,
		CredentialID: row.CredentialID,
		RunID:        row.RunID,
		UserID:       row.UserID,
		Strategy:     credentialstrategy.NormalizeName(row.Strategy),
		AcquiredAt:   time.Unix(0, row.AcquiredAt).UTC(),
		ExpiresAt:    time.Unix(0, row.ExpiresAt).UTC(),
		ReleasedAt:   fromNullTime(row.ReleasedAt),
	}
}

func toNullString(v string) sql.NullString {
	v = strings.TrimSpace(v)
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func fromNullString(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return strings.TrimSpace(v.String)
}

func fromNullTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(0, v.Int64).UTC()
	return &t
}
