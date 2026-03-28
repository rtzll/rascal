package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

const defaultAuthFailureCooldown = 5 * time.Minute

type CredentialStore interface {
	GetActiveCredentialLeaseByRunID(runID string) (state.CredentialLease, bool, error)
	SetCredentialStatus(id string, status state.CredentialStatus, cooldownUntil *time.Time, lastError string) error
}

type CredentialManager struct {
	store  CredentialStore
	broker CredentialBroker
}

type CredentialRequest struct {
	RunID   string
	RunDir  string
	UserID  string
	Runtime runtime.Runtime
}

type CredentialHandle struct {
	LeaseID      string
	CredentialID string
	AuthFilePath string

	manager *CredentialManager

	mu       sync.Mutex
	released bool
}

func NewCredentialManager(store CredentialStore, broker CredentialBroker) *CredentialManager {
	return &CredentialManager{
		store:  store,
		broker: broker,
	}
}

func AuthPath(runDir string, rt runtime.Runtime) (dir, file string) {
	switch runtime.NormalizeRuntime(string(rt)) {
	case runtime.RuntimeClaude, runtime.RuntimeGooseClaude:
		dir = filepath.Join(runDir, "claude")
		file = filepath.Join(dir, "oauth_token")
	default:
		dir = filepath.Join(runDir, "codex")
		file = filepath.Join(dir, "auth.json")
	}
	return dir, file
}

func IsAuthFailure(errText string) bool {
	text := strings.ToLower(strings.TrimSpace(errText))
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"unauthorized",
		"invalid api key",
		"invalid token",
		"authentication failed",
		"forbidden",
		"permission denied",
		"bad credentials",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (m *CredentialManager) PrepareCredential(ctx context.Context, req CredentialRequest) (*CredentialHandle, error) {
	if m == nil || m.broker == nil {
		return nil, nil
	}

	req.RunID = strings.TrimSpace(req.RunID)
	req.RunDir = strings.TrimSpace(req.RunDir)
	req.UserID = strings.TrimSpace(req.UserID)
	if req.RunID == "" {
		return nil, fmt.Errorf("run id is required")
	}
	if req.UserID == "" {
		req.UserID = "system"
	}

	authDir, authPath := AuthPath(req.RunDir, req.Runtime)
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		return nil, fmt.Errorf("create auth dir: %w", err)
	}

	lease, err := m.broker.Acquire(ctx, AcquireRequest{
		RunID:    req.RunID,
		UserID:   req.UserID,
		Provider: string(req.Runtime.Provider()),
	})
	if err != nil {
		return nil, fmt.Errorf("acquire credential lease: %w", err)
	}

	if err := writeAuthFileAtomic(authDir, authPath, lease.AuthBlob); err != nil {
		if releaseErr := m.broker.Release(ctx, lease.ID); releaseErr != nil {
			return nil, fmt.Errorf("release credential lease %s after auth file failure: %v: %w", lease.ID, releaseErr, err)
		}
		return nil, err
	}

	return &CredentialHandle{
		LeaseID:      lease.ID,
		CredentialID: lease.CredentialID,
		AuthFilePath: authPath,
		manager:      m,
	}, nil
}

func (m *CredentialManager) ActiveHandleForRun(runID, runDir string, rt runtime.Runtime) (*CredentialHandle, bool, error) {
	if m == nil || m.store == nil {
		return nil, false, nil
	}
	lease, ok, err := m.store.GetActiveCredentialLeaseByRunID(runID)
	if err != nil || !ok {
		if err != nil {
			return nil, false, fmt.Errorf("lookup active credential lease for run %s: %w", runID, err)
		}
		return nil, false, nil
	}
	_, authPath := AuthPath(runDir, rt)
	return &CredentialHandle{
		LeaseID:      lease.ID,
		CredentialID: lease.CredentialID,
		AuthFilePath: authPath,
		manager:      m,
	}, true, nil
}

func (h *CredentialHandle) Renew(ctx context.Context) error {
	if h == nil || h.manager == nil || h.manager.broker == nil || strings.TrimSpace(h.LeaseID) == "" {
		return nil
	}
	if err := h.manager.broker.Renew(ctx, h.LeaseID); err != nil {
		return fmt.Errorf("renew credential lease %s: %w", h.LeaseID, err)
	}
	return nil
}

func (h *CredentialHandle) Release(ctx context.Context) error {
	if h == nil {
		return nil
	}

	h.mu.Lock()
	if h.released {
		h.mu.Unlock()
		return nil
	}
	h.released = true
	h.mu.Unlock()

	var errs []string
	if h.manager != nil && h.manager.broker != nil && strings.TrimSpace(h.LeaseID) != "" {
		if err := h.manager.broker.Release(ctx, h.LeaseID); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if strings.TrimSpace(h.AuthFilePath) != "" {
		if err := os.Remove(h.AuthFilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Sprintf("remove auth file %s: %v", h.AuthFilePath, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (h *CredentialHandle) MarkAuthFailure(_ context.Context, runError string, cooldown time.Duration) error {
	if h == nil || h.manager == nil || h.manager.store == nil || strings.TrimSpace(h.CredentialID) == "" {
		return nil
	}
	if !IsAuthFailure(runError) {
		return nil
	}
	if cooldown <= 0 {
		cooldown = defaultAuthFailureCooldown
	}
	until := time.Now().UTC().Add(cooldown)
	if err := h.manager.store.SetCredentialStatus(h.CredentialID, state.CredentialStatusCooldown, &until, strings.TrimSpace(runError)); err != nil {
		return fmt.Errorf("set credential %s cooldown: %w", h.CredentialID, err)
	}
	return nil
}

func writeAuthFileAtomic(authDir, authPath string, blob []byte) error {
	tmpFile, err := os.CreateTemp(authDir, "auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp auth file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanupTemp := func() error {
		return removeIfExists(tmpPath)
	}
	if _, err := tmpFile.Write(blob); err != nil {
		closeErr := tmpFile.Close()
		cleanupErr := cleanupTemp()
		if closeErr != nil {
			return fmt.Errorf("write auth file: %w", errors.Join(err, closeErr, cleanupErr))
		}
		return fmt.Errorf("write auth file: %w", errors.Join(err, cleanupErr))
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		closeErr := tmpFile.Close()
		cleanupErr := cleanupTemp()
		if closeErr != nil {
			return fmt.Errorf("chmod auth file: %w", errors.Join(err, closeErr, cleanupErr))
		}
		return fmt.Errorf("chmod auth file: %w", errors.Join(err, cleanupErr))
	}
	if err := tmpFile.Close(); err != nil {
		cleanupErr := cleanupTemp()
		return fmt.Errorf("close auth file: %w", errors.Join(err, cleanupErr))
	}
	if err := os.Rename(tmpPath, authPath); err != nil {
		cleanupErr := cleanupTemp()
		return fmt.Errorf("publish auth file: %w", errors.Join(err, cleanupErr))
	}
	if err := os.Chmod(authPath, 0o600); err != nil {
		return fmt.Errorf("chmod published auth file: %w", err)
	}
	return nil
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
