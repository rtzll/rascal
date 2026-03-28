package credentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/credentialstrategy"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

type fakeCredentialBroker struct {
	acquireLease Lease
	acquireErr   error
	releaseErr   error
	renewErr     error

	mu           sync.Mutex
	releaseCalls []string
	renewCalls   []string
}

type fixedStrategy struct{}

func (fixedStrategy) Name() credentialstrategy.Name {
	return credentialstrategy.Name("fixed")
}

func (fixedStrategy) Select(_ AcquireRequest, candidates []CredentialState) (string, error) {
	if len(candidates) == 0 {
		return "", ErrNoCredentialMatch
	}
	return candidates[0].ID, nil
}

func newManagerTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func createManagerCredential(t *testing.T, store *state.Store, c Cipher, id, owner string, scope state.CredentialScope) {
	t.Helper()
	blob, err := c.Encrypt([]byte(`{"token":"` + id + `"}`))
	if err != nil {
		t.Fatalf("encrypt auth blob: %v", err)
	}
	if _, err := store.CreateCredential(state.CreateCredentialInput{
		ID:                id,
		OwnerUserID:       owner,
		Scope:             scope,
		EncryptedAuthBlob: blob,
		Weight:            1,
		Status:            state.CredentialStatusActive,
	}); err != nil {
		t.Fatalf("create credential %s: %v", id, err)
	}
}

func (f *fakeCredentialBroker) Acquire(context.Context, AcquireRequest) (Lease, error) {
	if f.acquireErr != nil {
		return Lease{}, f.acquireErr
	}
	return f.acquireLease, nil
}

func (f *fakeCredentialBroker) Renew(_ context.Context, leaseID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls = append(f.renewCalls, leaseID)
	return f.renewErr
}

func (f *fakeCredentialBroker) Release(_ context.Context, leaseID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls = append(f.releaseCalls, leaseID)
	return f.releaseErr
}

type recordingCredentialStore struct {
	activeLease state.CredentialLease
	activeOK    bool
	activeErr   error

	mu          sync.Mutex
	statusID    string
	status      state.CredentialStatus
	cooldown    *time.Time
	lastError   string
	statusCalls int
}

func (r *recordingCredentialStore) GetActiveCredentialLeaseByRunID(string) (state.CredentialLease, bool, error) {
	return r.activeLease, r.activeOK, r.activeErr
}

func (r *recordingCredentialStore) SetCredentialStatus(id string, status state.CredentialStatus, cooldownUntil *time.Time, lastError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statusID = id
	r.status = status
	r.lastError = lastError
	r.statusCalls++
	if cooldownUntil != nil {
		copyUntil := *cooldownUntil
		r.cooldown = &copyUntil
	}
	return nil
}

func TestCredentialManagerPrepareCredentialHappyPath(t *testing.T) {
	store := newManagerTestStore(t)
	cipher, err := NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	createManagerCredential(t, store, cipher, "cred-shared", "", state.CredentialScopeShared)
	manager := NewCredentialManager(store, NewBroker(store, fixedStrategy{}, cipher, 90*time.Second))

	runDir := filepath.Join(t.TempDir(), "run")
	handle, err := manager.PrepareCredential(t.Context(), CredentialRequest{
		RunID:   "run-happy",
		RunDir:  runDir,
		UserID:  "u1",
		Runtime: runtime.RuntimeCodex,
	})
	if err != nil {
		t.Fatalf("prepare credential: %v", err)
	}
	if handle == nil {
		t.Fatal("expected credential handle")
	}
	if handle.CredentialID != "cred-shared" {
		t.Fatalf("credential id = %q, want cred-shared", handle.CredentialID)
	}

	data, err := os.ReadFile(handle.AuthFilePath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if !strings.Contains(string(data), "cred-shared") {
		t.Fatalf("auth file missing decrypted payload: %q", string(data))
	}
	info, err := os.Stat(handle.AuthFilePath)
	if err != nil {
		t.Fatalf("stat auth file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth file mode = %o, want 600", info.Mode().Perm())
	}
	if _, ok, err := store.GetActiveCredentialLeaseByRunID("run-happy"); err != nil || !ok {
		t.Fatalf("expected active lease, ok=%t err=%v", ok, err)
	}

	if err := handle.Release(t.Context()); err != nil {
		t.Fatalf("release credential: %v", err)
	}
	if _, err := os.Stat(handle.AuthFilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected auth file removed, err=%v", err)
	}
	if _, ok, err := store.GetActiveCredentialLeaseByRunID("run-happy"); err != nil || ok {
		t.Fatalf("expected released lease, ok=%t err=%v", ok, err)
	}
}

func TestCredentialManagerPrepareCredentialNoCredentialsAvailable(t *testing.T) {
	store := newManagerTestStore(t)
	manager := NewCredentialManager(store, NewBroker(store, nil, nil, 90*time.Second))

	handle, err := manager.PrepareCredential(t.Context(), CredentialRequest{
		RunID:   "run-none",
		RunDir:  t.TempDir(),
		UserID:  "u1",
		Runtime: runtime.RuntimeCodex,
	})
	if !errors.Is(err, ErrNoCredentialAvailable) {
		t.Fatalf("expected ErrNoCredentialAvailable, got handle=%v err=%v", handle, err)
	}
}

func TestCredentialManagerPrepareCredentialReleasesLeaseOnAuthPathFailure(t *testing.T) {
	runDir := t.TempDir()
	blockingPath := filepath.Join(runDir, "codex")
	if err := os.WriteFile(blockingPath, []byte("block"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	broker := &fakeCredentialBroker{
		acquireLease: Lease{
			ID:           "lease-123",
			CredentialID: "cred-123",
			AuthBlob:     []byte(`{"token":"x"}`),
		},
	}
	manager := NewCredentialManager(nil, broker)

	handle, err := manager.PrepareCredential(t.Context(), CredentialRequest{
		RunID:   "run-blocked",
		RunDir:  runDir,
		UserID:  "u1",
		Runtime: runtime.RuntimeCodex,
	})
	if err == nil {
		t.Fatalf("expected prepare failure, got handle=%v", handle)
	}
	if !strings.Contains(err.Error(), "create auth dir") {
		t.Fatalf("expected create auth dir failure, got %v", err)
	}
	if len(broker.releaseCalls) != 0 {
		t.Fatalf("expected no release before acquire, got %v", broker.releaseCalls)
	}
}

func TestCredentialManagerPrepareCredentialReleasesLeaseOnWriteFailure(t *testing.T) {
	runDir := t.TempDir()
	authDir := filepath.Join(runDir, "codex")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(authDir, "auth.json"), 0o755); err != nil {
		t.Fatalf("mkdir blocking auth path: %v", err)
	}
	broker := &fakeCredentialBroker{
		acquireLease: Lease{
			ID:           "lease-123",
			CredentialID: "cred-123",
			AuthBlob:     []byte(`{"token":"x"}`),
		},
	}
	manager := NewCredentialManager(nil, broker)

	handle, err := manager.PrepareCredential(t.Context(), CredentialRequest{
		RunID:   "run-write-fail",
		RunDir:  runDir,
		UserID:  "u1",
		Runtime: runtime.RuntimeCodex,
	})
	if err == nil {
		t.Fatalf("expected prepare failure, got handle=%v", handle)
	}
	if !strings.Contains(err.Error(), "publish auth file") {
		t.Fatalf("expected publish auth file failure, got %v", err)
	}
	if len(broker.releaseCalls) != 1 || broker.releaseCalls[0] != "lease-123" {
		t.Fatalf("expected lease release after failure, got %v", broker.releaseCalls)
	}
}

func TestCredentialManagerPrepareCredentialClaudeRuntime(t *testing.T) {
	broker := &fakeCredentialBroker{
		acquireLease: Lease{
			ID:           "lease-claude",
			CredentialID: "cred-claude",
			AuthBlob:     []byte("oauth-token"),
		},
	}
	manager := NewCredentialManager(nil, broker)

	handle, err := manager.PrepareCredential(t.Context(), CredentialRequest{
		RunID:   "run-claude",
		RunDir:  t.TempDir(),
		UserID:  "u1",
		Runtime: runtime.RuntimeClaude,
	})
	if err != nil {
		t.Fatalf("prepare credential: %v", err)
	}
	if !strings.HasSuffix(handle.AuthFilePath, filepath.Join("claude", "oauth_token")) {
		t.Fatalf("unexpected auth path: %s", handle.AuthFilePath)
	}
}

func TestCredentialManagerActiveHandleForRun(t *testing.T) {
	store := &recordingCredentialStore{
		activeLease: state.CredentialLease{
			ID:           "lease-active",
			CredentialID: "cred-active",
		},
		activeOK: true,
	}
	manager := NewCredentialManager(store, nil)

	handle, ok, err := manager.ActiveHandleForRun("run-active", "/tmp/run-active", runtime.RuntimeCodex)
	if err != nil {
		t.Fatalf("active handle: %v", err)
	}
	if !ok || handle == nil {
		t.Fatalf("expected active handle, ok=%t handle=%v", ok, handle)
	}
	if handle.LeaseID != "lease-active" || handle.CredentialID != "cred-active" {
		t.Fatalf("unexpected handle: %#v", handle)
	}
}

func TestCredentialHandleRenewAndLeaseLost(t *testing.T) {
	broker := &fakeCredentialBroker{}
	manager := NewCredentialManager(nil, broker)
	handle := &CredentialHandle{
		LeaseID:      "lease-renew",
		CredentialID: "cred-renew",
		AuthFilePath: filepath.Join(t.TempDir(), "auth.json"),
		manager:      manager,
	}

	if err := handle.Renew(t.Context()); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if len(broker.renewCalls) != 1 || broker.renewCalls[0] != "lease-renew" {
		t.Fatalf("unexpected renew calls: %v", broker.renewCalls)
	}

	broker.renewErr = ErrLeaseLost
	if err := handle.Renew(t.Context()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expected ErrLeaseLost, got %v", err)
	}
}

func TestCredentialHandleReleaseIsIdempotentAndMissingFileSafe(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	broker := &fakeCredentialBroker{}
	manager := NewCredentialManager(nil, broker)
	handle := &CredentialHandle{
		LeaseID:      "lease-release",
		CredentialID: "cred-release",
		AuthFilePath: authPath,
		manager:      manager,
	}

	if err := handle.Release(t.Context()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := handle.Release(t.Context()); err != nil {
		t.Fatalf("second release should be idempotent: %v", err)
	}
	if len(broker.releaseCalls) != 1 {
		t.Fatalf("expected exactly one broker release, got %v", broker.releaseCalls)
	}

	authPath2 := filepath.Join(t.TempDir(), "missing.json")
	handle2 := &CredentialHandle{
		LeaseID:      "lease-missing",
		CredentialID: "cred-missing",
		AuthFilePath: authPath2,
		manager:      manager,
	}
	if err := handle2.Release(t.Context()); err != nil {
		t.Fatalf("release missing auth file: %v", err)
	}
}

func TestCredentialHandleMarkAuthFailure(t *testing.T) {
	store := &recordingCredentialStore{}
	manager := NewCredentialManager(store, nil)
	handle := &CredentialHandle{
		LeaseID:      "lease-auth",
		CredentialID: "cred-auth",
		manager:      manager,
	}

	if err := handle.MarkAuthFailure(t.Context(), "Bad credentials from provider", 2*time.Minute); err != nil {
		t.Fatalf("mark auth failure: %v", err)
	}
	if store.statusCalls != 1 {
		t.Fatalf("expected cooldown status update, got %d", store.statusCalls)
	}
	if store.statusID != "cred-auth" || store.status != state.CredentialStatusCooldown {
		t.Fatalf("unexpected status update: id=%s status=%s", store.statusID, store.status)
	}
	if store.cooldown == nil || !store.cooldown.After(time.Now().UTC()) {
		t.Fatalf("expected future cooldown, got %v", store.cooldown)
	}

	store2 := &recordingCredentialStore{}
	manager2 := NewCredentialManager(store2, nil)
	handle2 := &CredentialHandle{
		LeaseID:      "lease-noauth",
		CredentialID: "cred-noauth",
		manager:      manager2,
	}
	if err := handle2.MarkAuthFailure(t.Context(), "connection timeout", 2*time.Minute); err != nil {
		t.Fatalf("mark non-auth failure: %v", err)
	}
	if store2.statusCalls != 0 {
		t.Fatalf("expected no cooldown for non-auth failure, got %d", store2.statusCalls)
	}
}
