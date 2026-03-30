package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rtzll/rascal/internal/orchestrator"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

type repoFixtureInput struct {
	fullName      string
	webhookKey    string
	webhookSecret string
	enabled       bool
}

func mustCreateRepositoryFixture(t *testing.T, s *orchestrator.Server, in repoFixtureInput) state.Repository {
	t.Helper()

	secret := in.webhookSecret
	if secret == "" {
		secret = "wh-secret"
	}
	encryptedSecret, err := s.Cipher.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("encrypt webhook secret: %v", err)
	}
	repo, err := s.Store.CreateRepository(state.CreateRepositoryInput{
		FullName:               in.fullName,
		WebhookKey:             in.webhookKey,
		Enabled:                in.enabled,
		EncryptedWebhookSecret: encryptedSecret,
	})
	if err != nil {
		t.Fatalf("create repository fixture: %v", err)
	}
	return repo
}

func scopedWebhookRequest(t *testing.T, webhookKey, eventType, deliveryID, secret string, payload []byte) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github/"+webhookKey, bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

func TestScopedWebhookDisabledRepositoryBlocksAdmissionTriggers(t *testing.T) {
	t.Parallel()

	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	repo := mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:      "owner/repo",
		webhookKey:    "66666666666666666666666666666666",
		webhookSecret: "secret-2",
		enabled:       false,
	})

	for _, tc := range []struct {
		name       string
		eventType  string
		deliveryID string
		payload    []byte
	}{
		{
			name:       "label",
			eventType:  "issues",
			deliveryID: "disabled-label",
			payload:    []byte(`{"action":"labeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body"},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`),
		},
		{
			name:       "reopen",
			eventType:  "issues",
			deliveryID: "disabled-reopen",
			payload:    []byte(`{"action":"reopened","issue":{"number":7,"title":"Title","body":"Body","labels":[{"name":"rascal"}]},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`),
		},
	} {
		req := scopedWebhookRequest(t, repo.WebhookKey, tc.eventType, tc.deliveryID, "secret-2", tc.payload)
		rec := httptest.NewRecorder()
		s.HandleWebhookScoped(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: expected 202 for disabled repository trigger, got %d (%s)", tc.name, rec.Code, rec.Body.String())
		}

		var out struct {
			Accepted bool   `json:"accepted"`
			Reason   string `json:"reason"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("%s: decode response: %v", tc.name, err)
		}
		if out.Accepted {
			t.Fatalf("%s: expected accepted=false", tc.name)
		}
		if out.Reason != "repository disabled" {
			t.Fatalf("%s: reason = %q, want repository disabled", tc.name, out.Reason)
		}
	}

	if runs := s.Store.ListRuns(10); len(runs) != 0 {
		t.Fatalf("expected no runs for disabled repository admissions, got %d", len(runs))
	}
}

func TestScopedWebhookDisabledRepositoryAllowsPRClosedAndReopenedBookkeeping(t *testing.T) {
	t.Parallel()

	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	repo := mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:      "owner/repo",
		webhookKey:    "77777777777777777777777777777777",
		webhookSecret: "secret-3",
		enabled:       false,
	})
	taskID := "owner/repo#99"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:           taskID,
		Repo:         repo.FullName,
		AgentRuntime: runtime.RuntimeGooseCodex,
		PRNumber:     99,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:           "run_disabled_closed_reopened",
		TaskID:       taskID,
		Repo:         repo.FullName,
		Instruction:  "await merge",
		AgentRuntime: runtime.RuntimeGooseCodex,
		Trigger:      runtrigger.NamePRComment,
		RunDir:       t.TempDir(),
		IssueNumber:  99,
		PRNumber:     99,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	markRunReview(t, s, run.ID)

	closedPayload := []byte(`{"action":"closed","pull_request":{"number":99,"merged":false},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	closedReq := scopedWebhookRequest(t, repo.WebhookKey, "pull_request", "disabled-closed", "secret-3", closedPayload)
	closedRec := httptest.NewRecorder()
	s.HandleWebhookScoped(closedRec, closedReq)
	if closedRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for disabled repository closed PR event, got %d (%s)", closedRec.Code, closedRec.Body.String())
	}

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found after close", run.ID)
	}
	if updated.Status != state.StatusCanceled {
		t.Fatalf("closed PR run status = %s, want canceled", updated.Status)
	}
	if updated.PRStatus != state.PRStatusClosedUnmerged {
		t.Fatalf("closed PR status = %s, want %s", updated.PRStatus, state.PRStatusClosedUnmerged)
	}

	reopenedPayload := []byte(`{"action":"reopened","pull_request":{"number":99},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	reopenedReq := scopedWebhookRequest(t, repo.WebhookKey, "pull_request", "disabled-reopened", "secret-3", reopenedPayload)
	reopenedRec := httptest.NewRecorder()
	s.HandleWebhookScoped(reopenedRec, reopenedReq)
	if reopenedRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for disabled repository reopened PR event, got %d (%s)", reopenedRec.Code, reopenedRec.Body.String())
	}

	updated, ok = s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found after reopen", run.ID)
	}
	if updated.PRStatus != state.PRStatusOpen {
		t.Fatalf("reopened PR status = %s, want %s", updated.PRStatus, state.PRStatusOpen)
	}
}

func TestScopedWebhookDisabledRepositoryAllowsPRMergeBookkeeping(t *testing.T) {
	t.Parallel()

	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	repo := mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:      "owner/repo",
		webhookKey:    "88888888888888888888888888888888",
		webhookSecret: "secret-4",
		enabled:       false,
	})
	taskID := "owner/repo#55"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:           taskID,
		Repo:         repo.FullName,
		AgentRuntime: runtime.RuntimeGooseCodex,
		PRNumber:     55,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:           "run_disabled_merged",
		TaskID:       taskID,
		Repo:         repo.FullName,
		Instruction:  "await merge",
		AgentRuntime: runtime.RuntimeGooseCodex,
		Trigger:      runtrigger.NamePRComment,
		RunDir:       t.TempDir(),
		IssueNumber:  55,
		PRNumber:     55,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	markRunReview(t, s, run.ID)

	mergedPayload := []byte(`{"action":"closed","pull_request":{"number":55,"merged":true},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	mergedReq := scopedWebhookRequest(t, repo.WebhookKey, "pull_request", "disabled-merged", "secret-4", mergedPayload)
	mergedRec := httptest.NewRecorder()
	s.HandleWebhookScoped(mergedRec, mergedReq)
	if mergedRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for disabled repository merged PR event, got %d (%s)", mergedRec.Code, mergedRec.Body.String())
	}

	if !s.Store.IsTaskCompleted(taskID) {
		t.Fatal("expected merged PR to complete task for disabled repository")
	}
	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found after merge", run.ID)
	}
	if updated.Status != state.StatusSucceeded {
		t.Fatalf("merged PR run status = %s, want succeeded", updated.Status)
	}
	if updated.PRStatus != state.PRStatusMerged {
		t.Fatalf("merged PR status = %s, want %s", updated.PRStatus, state.PRStatusMerged)
	}
}
