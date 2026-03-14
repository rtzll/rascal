package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/credentials"
	credentialstrategies "github.com/rtzll/rascal/internal/credentials/strategies"
	"github.com/rtzll/rascal/internal/credentialstrategy"
	"github.com/rtzll/rascal/internal/state"
)

func withPrincipal(req *http.Request, userID string, role state.UserRole) *http.Request {
	ctx := req.Context()
	ctx = context.WithValue(ctx, authPrincipalKey{}, authPrincipal{
		UserID:        userID,
		ExternalLogin: userID,
		Role:          role,
	})
	return req.WithContext(ctx)
}

func TestCredentialAPIOwnerAdminAuthorization(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	cipher, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	strategy, err := credentialstrategies.ByName(credentialstrategy.NameRequesterOwnThenShared)
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	s.cipher = cipher
	s.broker = credentials.NewBroker(s.store, strategy, cipher, 90*time.Second)

	if _, err := s.store.UpsertUser(state.UpsertUserInput{ID: "admin", ExternalLogin: "admin", Role: state.UserRoleAdmin}); err != nil {
		t.Fatalf("upsert admin: %v", err)
	}
	if _, err := s.store.UpsertUser(state.UpsertUserInput{ID: "owner", ExternalLogin: "owner", Role: state.UserRoleUser}); err != nil {
		t.Fatalf("upsert owner: %v", err)
	}
	if _, err := s.store.UpsertUser(state.UpsertUserInput{ID: "other", ExternalLogin: "other", Role: state.UserRoleUser}); err != nil {
		t.Fatalf("upsert other: %v", err)
	}

	body := []byte(`{"id":"cred-owner","scope":"shared","owner_user_id":"other","auth_blob":"{\"token\":\"x\"}"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/v1/credentials", bytes.NewReader(body))
	createReq = withPrincipal(createReq, "owner", state.UserRoleUser)
	createRec := httptest.NewRecorder()
	s.handleCredentials(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", createRec.Code, createRec.Body.String())
	}

	created, ok, err := s.store.GetCodexCredential("cred-owner")
	if err != nil || !ok {
		t.Fatalf("credential not found after create: ok=%t err=%v", ok, err)
	}
	if created.Scope != "personal" {
		t.Fatalf("scope = %s, want personal", created.Scope)
	}
	if created.OwnerUserID != "owner" {
		t.Fatalf("owner_user_id = %s, want owner", created.OwnerUserID)
	}

	otherGetReq := httptest.NewRequest(http.MethodGet, "/v1/credentials/cred-owner", nil)
	otherGetReq = withPrincipal(otherGetReq, "other", state.UserRoleUser)
	otherGetRec := httptest.NewRecorder()
	s.handleCredentialSubresources(otherGetRec, otherGetReq)
	if otherGetRec.Code != http.StatusNotFound {
		t.Fatalf("other user get status = %d, want 404", otherGetRec.Code)
	}

	adminGetReq := httptest.NewRequest(http.MethodGet, "/v1/credentials/cred-owner", nil)
	adminGetReq = withPrincipal(adminGetReq, "admin", state.UserRoleAdmin)
	adminGetRec := httptest.NewRecorder()
	s.handleCredentialSubresources(adminGetRec, adminGetReq)
	if adminGetRec.Code != http.StatusOK {
		t.Fatalf("admin get status = %d, want 200", adminGetRec.Code)
	}

	adminCreateShared := httptest.NewRequest(http.MethodPost, "/v1/credentials", bytes.NewReader([]byte(`{"id":"cred-shared","scope":"shared","auth_blob":"{\"token\":\"y\"}"}`)))
	adminCreateShared = withPrincipal(adminCreateShared, "admin", state.UserRoleAdmin)
	adminCreateRec := httptest.NewRecorder()
	s.handleCredentials(adminCreateRec, adminCreateShared)
	if adminCreateRec.Code != http.StatusCreated {
		t.Fatalf("admin create shared status = %d, want 201", adminCreateRec.Code)
	}

	ownerListReq := httptest.NewRequest(http.MethodGet, "/v1/credentials", nil)
	ownerListReq = withPrincipal(ownerListReq, "owner", state.UserRoleUser)
	ownerListRec := httptest.NewRecorder()
	s.handleCredentials(ownerListRec, ownerListReq)
	if ownerListRec.Code != http.StatusOK {
		t.Fatalf("owner list status = %d, want 200", ownerListRec.Code)
	}
	var payload map[string][]map[string]any
	if err := json.Unmarshal(ownerListRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode owner list: %v", err)
	}
	for _, c := range payload["credentials"] {
		id, ok := c["id"].(string)
		if ok && id == "cred-shared" {
			t.Fatalf("owner should not list shared credential")
		}
	}
}

func TestCredentialAPIRejectsInvalidStatusUpdate(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	cipher, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	s.cipher = cipher
	if _, err := s.store.UpsertUser(state.UpsertUserInput{ID: "owner", ExternalLogin: "owner", Role: state.UserRoleUser}); err != nil {
		t.Fatalf("upsert owner: %v", err)
	}

	blob, err := cipher.Encrypt([]byte(`{"token":"x"}`))
	if err != nil {
		t.Fatalf("encrypt auth blob: %v", err)
	}
	if _, err := s.store.CreateCodexCredential(state.CreateCodexCredentialInput{
		ID:                "cred-invalid-status",
		OwnerUserID:       "owner",
		Scope:             state.CredentialScopePersonal,
		EncryptedAuthBlob: blob,
		Weight:            1,
		Status:            state.CredentialStatusActive,
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/v1/credentials/cred-invalid-status", bytes.NewReader([]byte(`{"status":"paused"}`)))
	req = withPrincipal(req, "owner", state.UserRoleUser)
	rec := httptest.NewRecorder()
	s.handleCredentialSubresources(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCreateTaskPersistsRequesterIdentity(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader([]byte(`{"repo":"owner/repo","task":"do work"}`)))
	req = withPrincipal(req, "owner", state.UserRoleUser)
	rec := httptest.NewRecorder()
	s.handleCreateTask(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Run state.Run `json:"run"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	info, ok := s.store.GetRunCredentialInfo(payload.Run.ID)
	if !ok {
		t.Fatalf("run credential info missing for %s", payload.Run.ID)
	}
	if info.CreatedByUserID != "owner" {
		t.Fatalf("created_by_user_id = %s, want owner", info.CreatedByUserID)
	}
}

func TestSchedulerAcquiresCredentialAndCleansEphemeralAuthFile(t *testing.T) {
	waitCh := make(chan struct{})
	s := newTestServer(t, &fakeLauncher{waitCh: waitCh, res: fakeRunResult{ExitCode: 0}})
	defer waitForServerIdle(t, s)
	cipher, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	strategy, err := credentialstrategies.ByName(credentialstrategy.NameRequesterOwnThenShared)
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	s.cipher = cipher
	s.broker = credentials.NewBroker(s.store, strategy, cipher, 90*time.Second)

	if _, err := s.store.UpsertUser(state.UpsertUserInput{ID: "owner", ExternalLogin: "owner", Role: state.UserRoleUser}); err != nil {
		t.Fatalf("upsert owner: %v", err)
	}
	blob, err := cipher.Encrypt([]byte(`{"token":"run-token"}`))
	if err != nil {
		t.Fatalf("encrypt auth blob: %v", err)
	}
	if _, err := s.store.CreateCodexCredential(state.CreateCodexCredentialInput{
		ID:                "cred-owner",
		OwnerUserID:       "owner",
		Scope:             "personal",
		EncryptedAuthBlob: blob,
		Weight:            1,
		Status:            "active",
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	run, err := s.createAndQueueRun(runRequest{
		Repo:            "owner/repo",
		Task:            "do work",
		CreatedByUserID: "owner",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	authPath := filepath.Join(run.RunDir, "codex", "auth.json")
	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(authPath)
		return err == nil
	}, "ephemeral auth file created")

	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if !bytes.Contains(data, []byte("run-token")) {
		t.Fatalf("auth file does not contain leased credential payload")
	}
	if _, ok, err := s.store.GetActiveCredentialLeaseByRunID(run.ID); err != nil || !ok {
		t.Fatalf("expected active credential lease, ok=%t err=%v", ok, err)
	}

	close(waitCh)
	waitFor(t, 3*time.Second, func() bool {
		r, ok := s.store.GetRun(run.ID)
		return ok && state.IsFinalRunStatus(r.Status)
	}, "run completion")
	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(authPath)
		return errors.Is(err, os.ErrNotExist)
	}, "ephemeral auth cleanup")
	waitFor(t, 2*time.Second, func() bool {
		_, ok, err := s.store.GetActiveCredentialLeaseByRunID(run.ID)
		return err == nil && !ok
	}, "credential lease release")
}

func TestSchedulerAllowsConcurrentRunsToReuseSharedCredential(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh, res: fakeRunResult{ExitCode: 0}}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
	cipher, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	strategy, err := credentialstrategies.ByName(credentialstrategy.NameRequesterOwnThenShared)
	if err != nil {
		t.Fatalf("strategy: %v", err)
	}
	s.cipher = cipher
	s.broker = credentials.NewBroker(s.store, strategy, cipher, 90*time.Second)
	s.maxConcurrent = 2

	if _, err := s.store.UpsertUser(state.UpsertUserInput{ID: "owner", ExternalLogin: "owner", Role: state.UserRoleUser}); err != nil {
		t.Fatalf("upsert owner: %v", err)
	}
	blob, err := cipher.Encrypt([]byte(`{"token":"shared-token"}`))
	if err != nil {
		t.Fatalf("encrypt auth blob: %v", err)
	}
	if _, err := s.store.CreateCodexCredential(state.CreateCodexCredentialInput{
		ID:                "cred-shared",
		Scope:             "shared",
		EncryptedAuthBlob: blob,
		Weight:            1,
		Status:            "active",
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	runA, err := s.createAndQueueRun(runRequest{
		TaskID:          "owner/repo#reuse-a",
		Repo:            "owner/repo",
		Task:            "reuse shared credential a",
		CreatedByUserID: "owner",
	})
	if err != nil {
		t.Fatalf("create run A: %v", err)
	}
	runB, err := s.createAndQueueRun(runRequest{
		TaskID:          "owner/repo#reuse-b",
		Repo:            "owner/repo",
		Task:            "reuse shared credential b",
		CreatedByUserID: "owner",
	})
	if err != nil {
		t.Fatalf("create run B: %v", err)
	}

	_ = waitForRunExecution(t, s, runA.ID)
	_ = waitForRunExecution(t, s, runB.ID)

	waitFor(t, 2*time.Second, func() bool {
		if _, err := os.Stat(filepath.Join(runA.RunDir, "codex", "auth.json")); err != nil {
			return false
		}
		if _, err := os.Stat(filepath.Join(runB.RunDir, "codex", "auth.json")); err != nil {
			return false
		}
		if _, ok, err := s.store.GetActiveCredentialLeaseByRunID(runA.ID); err != nil || !ok {
			return false
		}
		if _, ok, err := s.store.GetActiveCredentialLeaseByRunID(runB.ID); err != nil || !ok {
			return false
		}
		return true
	}, "shared auth files and leases created for both runs")

	if calls := launcher.Calls(); calls != 2 {
		t.Fatalf("expected two concurrent launcher calls, got %d", calls)
	}

	close(waitCh)
	waitFor(t, 3*time.Second, func() bool {
		a, okA := s.store.GetRun(runA.ID)
		b, okB := s.store.GetRun(runB.ID)
		return okA && okB && state.IsFinalRunStatus(a.Status) && state.IsFinalRunStatus(b.Status)
	}, "both runs complete")
}
