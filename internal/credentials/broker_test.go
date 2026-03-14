package credentials_test

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/credentials"
	credentialstrategies "github.com/rtzll/rascal/internal/credentials/strategies"
	"github.com/rtzll/rascal/internal/credentialstrategy"
	"github.com/rtzll/rascal/internal/state"
)

func newBrokerTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func createCredential(t *testing.T, store *state.Store, c credentials.Cipher, id, owner string, scope state.CredentialScope) {
	t.Helper()
	blob, err := c.Encrypt([]byte(`{"token":"` + id + `"}`))
	if err != nil {
		t.Fatalf("encrypt auth blob: %v", err)
	}
	if _, err := store.CreateCodexCredential(state.CreateCodexCredentialInput{
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

func TestBrokerAcquireOwnThenShared(t *testing.T) {
	store := newBrokerTestStore(t)
	c, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	if _, err := store.UpsertUser(state.UpsertUserInput{ID: "u1", ExternalLogin: "u1", Role: state.UserRoleUser}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	createCredential(t, store, c, "cred-personal", "u1", state.CredentialScopePersonal)
	createCredential(t, store, c, "cred-shared", "", state.CredentialScopeShared)

	strategy, err := credentialstrategies.ByName(credentialstrategy.NameRequesterOwnThenShared)
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	broker := credentials.NewBroker(store, strategy, c, 90*time.Second)

	lease, err := broker.Acquire(t.Context(), credentials.AcquireRequest{RunID: "run-a", UserID: "u1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.CredentialID != "cred-personal" {
		t.Fatalf("credential_id = %s, want cred-personal", lease.CredentialID)
	}
	if string(lease.AuthBlob) == "" {
		t.Fatal("expected decrypted auth blob")
	}
	if err := broker.Release(t.Context(), lease.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestBrokerRenewReleaseAndExpiryReclaim(t *testing.T) {
	store := newBrokerTestStore(t)
	c, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	createCredential(t, store, c, "cred-shared", "", state.CredentialScopeShared)
	strategy, err := credentialstrategies.ByName(credentialstrategy.NameSharedLeastActiveLeases)
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	broker := credentials.NewBroker(store, strategy, c, 50*time.Millisecond)

	lease, err := broker.Acquire(t.Context(), credentials.AcquireRequest{RunID: "run-1", UserID: "u1"})
	if err != nil {
		t.Fatalf("acquire run-1: %v", err)
	}
	if err := broker.Renew(t.Context(), lease.ID); err != nil {
		t.Fatalf("renew run-1: %v", err)
	}
	if err := broker.Release(t.Context(), lease.ID); err != nil {
		t.Fatalf("release run-1: %v", err)
	}
	if err := broker.Renew(t.Context(), lease.ID); err == nil {
		t.Fatal("expected renew to fail after release")
	}

	lease2, err := broker.Acquire(t.Context(), credentials.AcquireRequest{RunID: "run-2", UserID: "u2"})
	if err != nil {
		t.Fatalf("acquire run-2: %v", err)
	}
	time.Sleep(75 * time.Millisecond)
	lease3, err := broker.Acquire(t.Context(), credentials.AcquireRequest{RunID: "run-3", UserID: "u3"})
	if err != nil {
		t.Fatalf("acquire run-3 after expiry reclaim: %v", err)
	}
	if lease3.CredentialID != lease2.CredentialID {
		t.Fatalf("credential mismatch after reclaim: %s vs %s", lease3.CredentialID, lease2.CredentialID)
	}
	if err := broker.Release(t.Context(), lease3.ID); err != nil {
		t.Fatalf("release run-3: %v", err)
	}
}

func TestBrokerConcurrentAcquireAllowsCredentialReuse(t *testing.T) {
	store := newBrokerTestStore(t)
	c, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	createCredential(t, store, c, "cred-shared", "", state.CredentialScopeShared)
	strategy, err := credentialstrategies.ByName(credentialstrategy.NamePriorityBurst)
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	broker := credentials.NewBroker(store, strategy, c, 90*time.Second)

	var successes atomic.Int64
	var leaseIDs []string
	var leaseMu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			lease, err := broker.Acquire(t.Context(), credentials.AcquireRequest{
				RunID:  fmt.Sprintf("run-c-%d", idx),
				UserID: "u1",
			})
			if err == nil {
				successes.Add(1)
				leaseMu.Lock()
				leaseIDs = append(leaseIDs, lease.ID)
				leaseMu.Unlock()
				return
			}
			leaseMu.Lock()
			errs = append(errs, err)
			leaseMu.Unlock()
		}(i)
	}
	wg.Wait()
	if successes.Load() != 10 {
		t.Fatalf("expected all acquires to succeed, got %d (errs=%v)", successes.Load(), errs)
	}
	for _, leaseID := range leaseIDs {
		if err := broker.Release(t.Context(), leaseID); err != nil {
			t.Fatalf("release %s: %v", leaseID, err)
		}
	}
}

func TestBrokerSkipsCooldownCredentials(t *testing.T) {
	store := newBrokerTestStore(t)
	c, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	createCredential(t, store, c, "cred-cooldown", "", state.CredentialScopeShared)
	createCredential(t, store, c, "cred-active", "", state.CredentialScopeShared)

	until := time.Now().UTC().Add(10 * time.Minute)
	if err := store.SetCodexCredentialStatus("cred-cooldown", state.CredentialStatusCooldown, &until, "auth failure"); err != nil {
		t.Fatalf("set cooldown: %v", err)
	}

	strategy, err := credentialstrategies.ByName(credentialstrategy.NameSharedLeastActiveLeases)
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	broker := credentials.NewBroker(store, strategy, c, 90*time.Second)
	lease, err := broker.Acquire(t.Context(), credentials.AcquireRequest{RunID: "run-cooldown", UserID: "u1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.CredentialID != "cred-active" {
		t.Fatalf("credential_id = %s, want cred-active", lease.CredentialID)
	}
	if err := broker.Release(t.Context(), lease.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
}
