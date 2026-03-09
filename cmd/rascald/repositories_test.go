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
	"time"

	"github.com/rtzll/rascal/internal/state"
)

type repoFixtureInput struct {
	fullName             string
	webhookKey           string
	githubToken          string
	webhookSecret        string
	enabled              bool
	maxConcurrentRuns    int
	allowManual          bool
	allowIssueLabel      bool
	allowIssueEdit       bool
	allowPRComment       bool
	allowPRReview        bool
	allowPRReviewComment bool
}

func mustCreateRepositoryFixture(t *testing.T, s *server, in repoFixtureInput) state.Repository {
	t.Helper()
	token := in.githubToken
	if token == "" {
		token = "gh-token"
	}
	secret := in.webhookSecret
	if secret == "" {
		secret = "wh-secret"
	}
	encToken, err := s.cipher.Encrypt([]byte(token))
	if err != nil {
		t.Fatalf("encrypt github token: %v", err)
	}
	encSecret, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("encrypt webhook secret: %v", err)
	}
	repo, err := s.store.CreateRepository(state.CreateRepositoryInput{
		FullName:               in.fullName,
		WebhookKey:             in.webhookKey,
		Enabled:                in.enabled,
		EncryptedGitHubToken:   encToken,
		EncryptedWebhookSecret: encSecret,
		MaxConcurrentRuns:      in.maxConcurrentRuns,
		AllowManual:            in.allowManual,
		AllowIssueLabel:        in.allowIssueLabel,
		AllowIssueEdit:         in.allowIssueEdit,
		AllowPRComment:         in.allowPRComment,
		AllowPRReview:          in.allowPRReview,
		AllowPRReviewComment:   in.allowPRReviewComment,
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

func TestManualRunAdmissionRejectsUnknownRepoWhenRegistryEnabled(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:             "owner/known",
		webhookKey:           "11111111111111111111111111111111",
		enabled:              true,
		allowManual:          true,
		allowIssueLabel:      true,
		allowIssueEdit:       true,
		allowPRComment:       true,
		allowPRReview:        true,
		allowPRReviewComment: true,
	})

	body := `{"repo":"owner/missing","task":"test manual"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	s.handleCreateTask(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestManualRunAdmissionRequiresRepositoryRoleForNonAdmin(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:             "owner/repo",
		webhookKey:           "22222222222222222222222222222222",
		enabled:              true,
		allowManual:          true,
		allowIssueLabel:      true,
		allowIssueEdit:       true,
		allowPRComment:       true,
		allowPRReview:        true,
		allowPRReviewComment: true,
	})
	if _, err := s.store.UpsertUser(state.UpsertUserInput{
		ID:            "u1",
		ExternalLogin: "alice",
		Role:          state.UserRoleUser,
	}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewBufferString(`{"repo":"owner/repo","task":"manual trigger"}`))
	req = withPrincipal(req, "u1", state.UserRoleUser)
	rec := httptest.NewRecorder()
	s.handleCreateTask(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without role, got %d (%s)", rec.Code, rec.Body.String())
	}

	if _, err := s.store.UpsertRepositoryUserRole(state.UpsertRepositoryUserRoleInput{
		RepoFullName: "owner/repo",
		UserID:       "u1",
		Role:         state.RepositoryRoleTrigger,
	}); err != nil {
		t.Fatalf("grant repository trigger role: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewBufferString(`{"repo":"owner/repo","task":"manual trigger"}`))
	req = withPrincipal(req, "u1", state.UserRoleUser)
	rec = httptest.NewRecorder()
	s.handleCreateTask(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with role, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestScopedWebhookRejectsRepoMismatchAndTriggerDeny(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	repo := mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:             "owner/repo",
		webhookKey:           "33333333333333333333333333333333",
		webhookSecret:        "secret-1",
		enabled:              true,
		allowManual:          true,
		allowIssueLabel:      false,
		allowIssueEdit:       true,
		allowPRComment:       true,
		allowPRReview:        true,
		allowPRReviewComment: true,
	})

	mismatchPayload := []byte(`{"action":"labeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body"},"repository":{"full_name":"owner/other"},"sender":{"login":"dev"}}`)
	mismatchReq := scopedWebhookRequest(t, repo.WebhookKey, "issues", "d1", "secret-1", mismatchPayload)
	mismatchRec := httptest.NewRecorder()
	s.handleWebhookScoped(mismatchRec, mismatchReq)
	if mismatchRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for repo mismatch, got %d", mismatchRec.Code)
	}

	denyPayload := []byte(`{"action":"labeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body"},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	denyReq := scopedWebhookRequest(t, repo.WebhookKey, "issues", "d2", "secret-1", denyPayload)
	denyRec := httptest.NewRecorder()
	s.handleWebhookScoped(denyRec, denyReq)
	if denyRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for denied trigger, got %d (%s)", denyRec.Code, denyRec.Body.String())
	}
	var out struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.NewDecoder(denyRec.Body).Decode(&out); err != nil {
		t.Fatalf("decode denied webhook response: %v", err)
	}
	if out.Accepted {
		t.Fatal("expected accepted=false for denied trigger")
	}
	if runs := s.store.ListRuns(10); len(runs) != 0 {
		t.Fatalf("expected no runs for denied trigger, got %d", len(runs))
	}
}

func TestSchedulerSkipsCappedRepositoryRun(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	s.maxConcurrent = 2
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:             "owner/repo-a",
		webhookKey:           "44444444444444444444444444444444",
		enabled:              true,
		maxConcurrentRuns:    1,
		allowManual:          true,
		allowIssueLabel:      true,
		allowIssueEdit:       true,
		allowPRComment:       true,
		allowPRReview:        true,
		allowPRReviewComment: true,
	})
	mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:             "owner/repo-b",
		webhookKey:           "55555555555555555555555555555555",
		enabled:              true,
		maxConcurrentRuns:    0,
		allowManual:          true,
		allowIssueLabel:      true,
		allowIssueEdit:       true,
		allowPRComment:       true,
		allowPRReview:        true,
		allowPRReviewComment: true,
	})

	runA1, err := s.createAndQueueRun(runRequest{
		TaskID: "task-a-1",
		Repo:   "owner/repo-a",
		Task:   "run a1",
	})
	if err != nil {
		t.Fatalf("create run a1: %v", err)
	}
	runA2, err := s.createAndQueueRun(runRequest{
		TaskID: "task-a-2",
		Repo:   "owner/repo-a",
		Task:   "run a2",
	})
	if err != nil {
		t.Fatalf("create run a2: %v", err)
	}
	runB1, err := s.createAndQueueRun(runRequest{
		TaskID: "task-b-1",
		Repo:   "owner/repo-b",
		Task:   "run b1",
	})
	if err != nil {
		t.Fatalf("create run b1: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return launcher.Calls() >= 2 }, "two admissible runs started")
	waitFor(t, 2*time.Second, func() bool {
		a1, ok := s.store.GetRun(runA1.ID)
		return ok && a1.Status == state.StatusRunning
	}, "repo-a run 1 running")
	waitFor(t, 2*time.Second, func() bool {
		b1, ok := s.store.GetRun(runB1.ID)
		return ok && b1.Status == state.StatusRunning
	}, "repo-b run running")

	a2, ok := s.store.GetRun(runA2.ID)
	if !ok {
		t.Fatalf("missing run %s", runA2.ID)
	}
	if a2.Status != state.StatusQueued {
		t.Fatalf("expected capped run to stay queued, got %s", a2.Status)
	}
}
