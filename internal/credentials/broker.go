package credentials

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

var (
	ErrNoCredentialAvailable = errors.New("no credentials available")
	ErrLeaseLost             = errors.New("credential lease lost")
)

type CredentialBroker interface {
	Acquire(ctx context.Context, req AcquireRequest) (Lease, error)
	Renew(ctx context.Context, leaseID string) error
	Release(ctx context.Context, leaseID string) error
}

type Broker struct {
	store                 *state.Store
	strategy              AllocationStrategy
	cipher                Cipher
	leaseTTL              time.Duration
	usageWindow           time.Duration
	cooldownOnCryptoError time.Duration
}

func NewBroker(store *state.Store, strategy AllocationStrategy, cipher Cipher, leaseTTL time.Duration) *Broker {
	if leaseTTL <= 0 {
		leaseTTL = 90 * time.Second
	}
	return &Broker{
		store:                 store,
		strategy:              strategy,
		cipher:                cipher,
		leaseTTL:              leaseTTL,
		usageWindow:           time.Hour,
		cooldownOnCryptoError: 5 * time.Minute,
	}
}

func (b *Broker) Acquire(_ context.Context, req AcquireRequest) (Lease, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	req.UserID = strings.TrimSpace(req.UserID)
	req.Provider = strings.TrimSpace(req.Provider)
	if req.RunID == "" || req.UserID == "" {
		return Lease{}, fmt.Errorf("run_id and user_id are required")
	}
	if req.Provider == "" {
		req.Provider = "codex"
	}
	if b.store == nil || b.strategy == nil || b.cipher == nil {
		return Lease{}, ErrNoCredentialAvailable
	}
	now := time.Now().UTC()
	if _, err := b.store.ReclaimExpiredCredentialLeases(now); err != nil {
		return Lease{}, fmt.Errorf("reclaim expired credential leases: %w", err)
	}
	for attempt := 0; attempt < 12; attempt++ {
		candidates, err := b.store.ListCredentialCandidates(req.UserID, req.Provider, now, now.Add(-b.usageWindow))
		if err != nil {
			return Lease{}, fmt.Errorf("list credential candidates for %s: %w", req.UserID, err)
		}
		if len(candidates) == 0 {
			return Lease{}, ErrNoCredentialAvailable
		}

		states := make([]CredentialState, 0, len(candidates))
		for _, c := range candidates {
			states = append(states, CredentialState{
				ID:            c.ID,
				OwnerUserID:   c.OwnerUserID,
				Scope:         c.Scope,
				Weight:        c.Weight,
				Status:        c.Status,
				CooldownUntil: c.CooldownUntil,
				ActiveLeases:  c.ActiveLeases,
				UsageTokens:   c.UsageTokens,
				UsageRuns:     c.UsageRuns,
				LastError:     c.LastError,
				CreatedAt:     c.CreatedAt,
				UpdatedAt:     c.UpdatedAt,
			})
		}

		credentialID, err := b.strategy.Select(req, states)
		if err != nil {
			if errors.Is(err, ErrNoCredentialMatch) {
				return Lease{}, ErrNoCredentialAvailable
			}
			return Lease{}, fmt.Errorf("select credential for run %s: %w", req.RunID, err)
		}

		leaseID, err := newLeaseID()
		if err != nil {
			return Lease{}, err
		}
		acquiredAt := time.Now().UTC()
		expiresAt := acquiredAt.Add(b.leaseTTL)
		ok, err := b.store.TryCreateCredentialLease(state.CreateCredentialLeaseInput{
			ID:           leaseID,
			CredentialID: credentialID,
			RunID:        req.RunID,
			UserID:       req.UserID,
			Strategy:     b.strategy.Name(),
			AcquiredAt:   acquiredAt,
			ExpiresAt:    expiresAt,
			Now:          acquiredAt,
		})
		if err != nil {
			return Lease{}, fmt.Errorf("create credential lease for credential %s: %w", credentialID, err)
		}
		if !ok {
			continue
		}

		credential, exists, err := b.store.GetCredential(credentialID)
		if err != nil {
			if _, _, releaseErr := b.store.ReleaseCredentialLease(leaseID); releaseErr != nil {
				return Lease{}, fmt.Errorf("release credential lease %s after lookup failure: %v: %w", leaseID, releaseErr, err)
			}
			return Lease{}, fmt.Errorf("get credential %s: %w", credentialID, err)
		}
		if !exists {
			if _, _, releaseErr := b.store.ReleaseCredentialLease(leaseID); releaseErr != nil {
				return Lease{}, fmt.Errorf("release credential lease %s after credential %s disappeared: %w", leaseID, credentialID, releaseErr)
			}
			return Lease{}, ErrNoCredentialAvailable
		}
		authBlob, err := b.cipher.Decrypt(credential.EncryptedAuthBlob)
		if err != nil {
			if _, _, releaseErr := b.store.ReleaseCredentialLease(leaseID); releaseErr != nil {
				return Lease{}, fmt.Errorf("release credential lease %s after decrypt failure: %v: %w", leaseID, releaseErr, err)
			}
			until := time.Now().UTC().Add(b.cooldownOnCryptoError)
			if statusErr := b.store.SetCredentialStatus(credentialID, state.CredentialStatusCooldown, &until, "credential decrypt failure"); statusErr != nil {
				return Lease{}, fmt.Errorf("set credential %s cooldown after decrypt failure: %v: %w", credentialID, statusErr, err)
			}
			return Lease{}, fmt.Errorf("decrypt credential %s auth blob: %w", credentialID, err)
		}
		if err := b.store.SetRunCredentialID(req.RunID, credentialID); err != nil {
			if _, _, releaseErr := b.store.ReleaseCredentialLease(leaseID); releaseErr != nil {
				return Lease{}, fmt.Errorf("release credential lease %s after run credential update failure: %v: %w", leaseID, releaseErr, err)
			}
			return Lease{}, fmt.Errorf("set run %s credential id %s: %w", req.RunID, credentialID, err)
		}
		return Lease{
			ID:           leaseID,
			CredentialID: credentialID,
			RunID:        req.RunID,
			UserID:       req.UserID,
			Strategy:     b.strategy.Name(),
			AcquiredAt:   acquiredAt,
			ExpiresAt:    expiresAt,
			AuthBlob:     authBlob,
		}, nil
	}
	return Lease{}, ErrNoCredentialAvailable
}

func (b *Broker) Renew(_ context.Context, leaseID string) error {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return fmt.Errorf("lease id is required")
	}
	now := time.Now().UTC()
	ok, err := b.store.RenewCredentialLease(leaseID, now.Add(b.leaseTTL), now)
	if err != nil {
		return fmt.Errorf("renew credential lease %s: %w", leaseID, err)
	}
	if !ok {
		return ErrLeaseLost
	}
	return nil
}

func (b *Broker) Release(_ context.Context, leaseID string) error {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return nil
	}
	lease, released, err := b.store.ReleaseCredentialLease(leaseID)
	if err != nil {
		return fmt.Errorf("release credential lease %s: %w", leaseID, err)
	}
	if !released {
		return nil
	}
	if err := b.store.UpsertCredentialUsage(lease.CredentialID, time.Now().UTC().Truncate(time.Hour), 0, 1); err != nil {
		return fmt.Errorf("record credential usage for %s: %w", lease.CredentialID, err)
	}
	return nil
}

func (b *Broker) MarkCredentialCooldown(credentialID, reason string, duration time.Duration) error {
	if duration <= 0 {
		duration = 5 * time.Minute
	}
	until := time.Now().UTC().Add(duration)
	if err := b.store.SetCredentialStatus(credentialID, state.CredentialStatusCooldown, &until, strings.TrimSpace(reason)); err != nil {
		return fmt.Errorf("set credential %s cooldown: %w", credentialID, err)
	}
	return nil
}

func newLeaseID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate lease id: %w", err)
	}
	return "lease_" + hex.EncodeToString(buf), nil
}
