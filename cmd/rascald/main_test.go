package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/state"
)

type fakeLauncher struct {
	mu     sync.Mutex
	calls  int
	specs  []runner.Spec
	waitCh <-chan struct{}
	res    runner.Result
	err    error
	resSeq []runner.Result
	errSeq []error
}

type stubbornLauncher struct {
	mu    sync.Mutex
	calls int
	wait  <-chan struct{}
	res   runner.Result
}

type postedIssueComment struct {
	repo        string
	issueNumber int
	body        string
}

type postedIssueReaction struct {
	repo        string
	issueNumber int
	content     string
}

type fakeGitHubClient struct {
	mu sync.Mutex

	issueData ghapi.IssueData
	issueErr  error

	issueReactions []postedIssueReaction

	issueComments            []postedIssueComment
	createIssueCommentErr    error
	createIssueCommentErrSeq []error
	createIssueCommentCalls  int
}

func (f *fakeLauncher) Start(ctx context.Context, spec runner.Spec) (runner.Result, error) {
	f.mu.Lock()
	f.calls++
	f.specs = append(f.specs, spec)
	callIdx := f.calls - 1
	res := f.res
	err := f.err
	if callIdx < len(f.resSeq) {
		res = f.resSeq[callIdx]
	}
	if callIdx < len(f.errSeq) {
		err = f.errSeq[callIdx]
	}
	f.mu.Unlock()

	if f.waitCh != nil {
		select {
		case <-f.waitCh:
		case <-ctx.Done():
			return runner.Result{}, ctx.Err()
		}
	}
	return res, err
}

func (l *stubbornLauncher) Start(_ context.Context, _ runner.Spec) (runner.Result, error) {
	l.mu.Lock()
	l.calls++
	l.mu.Unlock()
	if l.wait != nil {
		<-l.wait
	}
	return l.res, nil
}

func (f *fakeGitHubClient) GetIssue(_ context.Context, _ string, _ int) (ghapi.IssueData, error) {
	if f.issueErr != nil {
		return ghapi.IssueData{}, f.issueErr
	}
	return f.issueData, nil
}

func (f *fakeGitHubClient) addIssueReaction(repo string, issueNumber int, content string) {
	f.issueReactions = append(f.issueReactions, postedIssueReaction{
		repo:        repo,
		issueNumber: issueNumber,
		content:     content,
	})
}

func (f *fakeGitHubClient) AddIssueReaction(_ context.Context, repo string, issueNumber int, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addIssueReaction(repo, issueNumber, content)
	return nil
}

func (f *fakeGitHubClient) AddIssueCommentReaction(_ context.Context, _ string, _ int64, _ string) error {
	return nil
}

func (f *fakeGitHubClient) AddPullRequestReviewReaction(_ context.Context, _ string, _ int, _ int64, _ string) error {
	return nil
}

func (f *fakeGitHubClient) AddPullRequestReviewCommentReaction(_ context.Context, _ string, _ int64, _ string) error {
	return nil
}

func (f *fakeGitHubClient) CreateIssueComment(_ context.Context, repo string, issueNumber int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	callIdx := f.createIssueCommentCalls
	f.createIssueCommentCalls++
	err := f.createIssueCommentErr
	if callIdx < len(f.createIssueCommentErrSeq) {
		err = f.createIssueCommentErrSeq[callIdx]
	}
	if err != nil {
		return err
	}
	f.issueComments = append(f.issueComments, postedIssueComment{
		repo:        repo,
		issueNumber: issueNumber,
		body:        body,
	})
	return nil
}

func (f *fakeGitHubClient) postedComments() []postedIssueComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]postedIssueComment, len(f.issueComments))
	copy(out, f.issueComments)
	return out
}

func (f *fakeGitHubClient) createCommentCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createIssueCommentCalls
}

func (f *fakeGitHubClient) postedReactions() []postedIssueReaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]postedIssueReaction, len(f.issueReactions))
	copy(out, f.issueReactions)
	return out
}

func (f *fakeLauncher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestServer(t *testing.T, launcher runner.Launcher) *server {
	t.Helper()

	dataDir := t.TempDir()
	cfg := config.ServerConfig{
		DataDir:    dataDir,
		StatePath:  filepath.Join(dataDir, "state.db"),
		MaxRuns:    200,
		RunnerMode: "noop",
	}
	store, err := state.New(cfg.StatePath, cfg.MaxRuns)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	return &server{
		cfg:           cfg,
		store:         store,
		launcher:      launcher,
		gh:            ghapi.NewAPIClient(""),
		runCancels:    make(map[string]context.CancelFunc),
		runCancelNote: make(map[string]string),
		maxConcurrent: defaultMaxConcurrent(),
		instanceID:    "test-instance",
	}
}

func webhookRequest(t *testing.T, payload []byte, eventType, deliveryID, secret string) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(payload)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return req
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for condition: %s", msg)
}

func waitForServerIdle(t *testing.T, s *server) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool {
		return s.activeRunCount() == 0
	}, "server idle")
}

func TestHandleWebhookRecordsDeliveryOnlyAfterSuccess(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	deliveryID := "delivery-1"

	badReq := webhookRequest(t, []byte("{"), "issues", deliveryID, "")
	badRec := httptest.NewRecorder()
	s.handleWebhook(badRec, badReq)
	if badRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for bad payload, got %d", badRec.Code)
	}
	if s.store.DeliverySeen(deliveryID) {
		t.Fatal("delivery should not be recorded when processing fails")
	}

	goodPayload := []byte(`{"action":"labeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body"},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	goodReq := webhookRequest(t, goodPayload, "issues", deliveryID, "")
	goodRec := httptest.NewRecorder()
	s.handleWebhook(goodRec, goodReq)
	if goodRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for good payload, got %d", goodRec.Code)
	}
	if !s.store.DeliverySeen(deliveryID) {
		t.Fatal("delivery should be recorded after successful processing")
	}
	if got := len(s.store.ListRuns(10)); got != 1 {
		t.Fatalf("expected one run, got %d", got)
	}

	dupReq := webhookRequest(t, goodPayload, "issues", deliveryID, "")
	dupRec := httptest.NewRecorder()
	s.handleWebhook(dupRec, dupReq)
	if dupRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for duplicate delivery, got %d", dupRec.Code)
	}
	if got := len(s.store.ListRuns(10)); got != 1 {
		t.Fatalf("expected one run after duplicate, got %d", got)
	}
}

func TestHandleWebhookIgnoresIssueLabeledOnPR(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	payload := []byte(`{"action":"labeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body","pull_request":{}},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)

	req := webhookRequest(t, payload, "issues", "delivery-pr", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if got := len(s.store.ListRuns(10)); got != 0 {
		t.Fatalf("expected zero runs, got %d", got)
	}
}

func TestHandleWebhookIssueClosedCancelsRunsAndCompletesTask(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
	taskID := "owner/repo#7"

	runningRun, err := s.createAndQueueRun(runRequest{TaskID: taskID, Repo: "owner/repo", Task: "work", IssueNumber: 7})
	if err != nil {
		t.Fatalf("create running run: %v", err)
	}
	queuedRun, err := s.createAndQueueRun(runRequest{TaskID: taskID, Repo: "owner/repo", Task: "queued", IssueNumber: 7})
	if err != nil {
		t.Fatalf("create queued run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")

	payload := []byte(`{"action":"closed","issue":{"number":7,"title":"Title","body":"Body","labels":[{"name":"rascal"}]},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "issues", "delivery-closed", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for closed issue event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return s.store.IsTaskCompleted(taskID) }, "task marked completed")
	waitFor(t, time.Second, func() bool {
		r, ok := s.store.GetRun(queuedRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "queued run canceled")
	waitFor(t, time.Second, func() bool {
		r, ok := s.store.GetRun(runningRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "running run canceled")

	close(waitCh)
}

func TestHandleWebhookIssueReopenedReenablesTask(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	taskID := "owner/repo#7"

	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", IssueNumber: 7}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	if err := s.store.MarkTaskCompleted(taskID); err != nil {
		t.Fatalf("mark task completed: %v", err)
	}

	payload := []byte(`{"action":"reopened","issue":{"number":7,"title":"Title","body":"Body","labels":[{"name":"rascal"}]},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "issues", "delivery-reopened", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for reopened issue event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return !s.store.IsTaskCompleted(taskID) }, "task reopened")
	waitFor(t, time.Second, func() bool { return len(s.store.ListRuns(10)) == 1 }, "run queued")
}

func TestHandleWebhookIssueEditedRequeuesRuns(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
	defer close(waitCh)
	taskID := "owner/repo#7"

	runningRun, err := s.createAndQueueRun(runRequest{TaskID: taskID, Repo: "owner/repo", Task: "work", IssueNumber: 7})
	if err != nil {
		t.Fatalf("create running run: %v", err)
	}
	queuedRun, err := s.createAndQueueRun(runRequest{TaskID: taskID, Repo: "owner/repo", Task: "stale", IssueNumber: 7})
	if err != nil {
		t.Fatalf("create queued run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")

	payload := []byte(`{"action":"edited","issue":{"number":7,"title":"New Title","body":"New Body","labels":[{"name":"rascal"}]},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "issues", "delivery-edited", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for edited issue event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool {
		r, ok := s.store.GetRun(queuedRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "queued run canceled")

	var editedRun state.Run
	waitFor(t, time.Second, func() bool {
		for _, run := range s.store.ListRuns(20) {
			if run.Trigger == "issue_edited" {
				editedRun = run
				return true
			}
		}
		return false
	}, "issue edited run queued")

	if editedRun.Task != "New Title\n\nNew Body" {
		t.Fatalf("expected updated task text, got %q", editedRun.Task)
	}
	if editedRun.TaskID != taskID {
		t.Fatalf("expected edited run task id %q, got %q", taskID, editedRun.TaskID)
	}
	if editedRun.ID == runningRun.ID || editedRun.ID == queuedRun.ID {
		t.Fatalf("expected new run for edit, got existing run id %q", editedRun.ID)
	}

}

func TestHandleListRunsSupportsAllQuery(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	for i := 1; i <= 3; i++ {
		_, err := s.store.AddRun(state.CreateRunInput{
			ID:     fmt.Sprintf("run_%d", i),
			TaskID: fmt.Sprintf("task_%d", i),
			Repo:   "owner/repo",
			Task:   fmt.Sprintf("Task %d", i),
		})
		if err != nil {
			t.Fatalf("add run %d: %v", i, err)
		}
	}

	limitReq := httptest.NewRequest(http.MethodGet, "/v1/runs?limit=2", nil)
	limitRec := httptest.NewRecorder()
	s.handleListRuns(limitRec, limitReq)
	if limitRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for limit query, got %d", limitRec.Code)
	}
	var limitOut struct {
		Runs []state.Run `json:"runs"`
	}
	if err := json.NewDecoder(limitRec.Body).Decode(&limitOut); err != nil {
		t.Fatalf("decode limit response: %v", err)
	}
	if len(limitOut.Runs) != 2 {
		t.Fatalf("expected 2 runs with limit=2, got %d", len(limitOut.Runs))
	}

	allReq := httptest.NewRequest(http.MethodGet, "/v1/runs?all=1", nil)
	allRec := httptest.NewRecorder()
	s.handleListRuns(allRec, allReq)
	if allRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for all query, got %d", allRec.Code)
	}
	var allOut struct {
		Runs []state.Run `json:"runs"`
	}
	if err := json.NewDecoder(allRec.Body).Decode(&allOut); err != nil {
		t.Fatalf("decode all response: %v", err)
	}
	if len(allOut.Runs) != 3 {
		t.Fatalf("expected 3 runs with all=1, got %d", len(allOut.Runs))
	}
}

func TestHandleListRunsAllIgnoresLimitValue(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	for i := 1; i <= 2; i++ {
		_, err := s.store.AddRun(state.CreateRunInput{
			ID:     fmt.Sprintf("run_all_%d", i),
			TaskID: fmt.Sprintf("task_all_%d", i),
			Repo:   "owner/repo",
			Task:   fmt.Sprintf("Task all %d", i),
		})
		if err != nil {
			t.Fatalf("add run %d: %v", i, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs?all=true&limit=bad", nil)
	rec := httptest.NewRecorder()
	s.handleListRuns(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for all=true with bad limit, got %d", rec.Code)
	}
	var out struct {
		Runs []state.Run `json:"runs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode all+limit response: %v", err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("expected all runs when all=true, got %d", len(out.Runs))
	}
}

func TestHandleListRunsInvalidAllReturnsBadRequest(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})

	req := httptest.NewRequest(http.MethodGet, "/v1/runs?all=notabool", nil)
	rec := httptest.NewRecorder()
	s.handleListRuns(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid all query, got %d", rec.Code)
	}
}

func TestHandleWebhookInactiveSlotIsSkipped(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	slotFile := filepath.Join(t.TempDir(), "active_slot")
	if err := os.WriteFile(slotFile, []byte("green\n"), 0o644); err != nil {
		t.Fatalf("write active slot file: %v", err)
	}
	s.cfg.Slot = "blue"
	s.cfg.ActiveSlotPath = slotFile

	payload := []byte(`{"action":"labeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body"},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "issues", "delivery-slot", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for inactive slot skip, got %d", rec.Code)
	}
	if got := len(s.store.ListRuns(10)); got != 0 {
		t.Fatalf("expected no runs when inactive slot handles webhook, got %d", got)
	}
}

func TestHandleWebhookSignatureValidation(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	s.cfg.GitHubWebhookSecret = "secret"
	payload := []byte(`{"action":"labeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body"},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)

	badReq := webhookRequest(t, payload, "issues", "sig-1", "wrong-secret")
	badRec := httptest.NewRecorder()
	s.handleWebhook(badRec, badReq)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid signature, got %d", badRec.Code)
	}

	goodReq := webhookRequest(t, payload, "issues", "sig-2", "secret")
	goodRec := httptest.NewRecorder()
	s.handleWebhook(goodRec, goodReq)
	if goodRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for valid signature, got %d", goodRec.Code)
	}
}

func TestHandleWebhookIssueCommentUsesExistingPRTaskAndLastBranches(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo    = "owner/repo"
		taskID  = "owner/repo#7"
		prNum   = 7
		baseRef = "develop"
		headRef = "rascal/task-7"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.store.AddRun(state.CreateRunInput{
		ID:         "seed_run",
		TaskID:     taskID,
		Repo:       repo,
		Task:       "seed",
		BaseBranch: baseRef,
		HeadBranch: headRef,
		Trigger:    "seed",
		RunDir:     filepath.Join(t.TempDir(), "seed_run"),
		PRNumber:   prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := s.store.SetRunStatus(seedRun.ID, state.StatusSucceeded, ""); err != nil {
		t.Fatalf("mark seed run succeeded: %v", err)
	}

	payload := []byte(`{"action":"created","issue":{"number":7,"pull_request":{}},"comment":{"id":101,"body":"  please address review notes  ","user":{"login":"alice"}},"repository":{"full_name":"owner/repo"},"sender":{"login":"alice"}}`)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.store.ListRuns(20)
		for _, run := range runs {
			if run.Trigger == "pr_comment" {
				got = run
				return true
			}
		}
		return false
	}, "pr_comment run created")

	if got.TaskID != taskID {
		t.Fatalf("task id = %q, want %q", got.TaskID, taskID)
	}
	if got.PRNumber != prNum {
		t.Fatalf("pr number = %d, want %d", got.PRNumber, prNum)
	}
	if got.BaseBranch != baseRef {
		t.Fatalf("base branch = %q, want %q", got.BaseBranch, baseRef)
	}
	if got.HeadBranch != headRef {
		t.Fatalf("head branch = %q, want %q", got.HeadBranch, headRef)
	}
	if got.Context != "please address review notes" {
		t.Fatalf("context = %q, want trimmed comment body", got.Context)
	}
}

func TestHandleWebhookIssueCommentEditedUsesUpdatedContext(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo    = "owner/repo"
		taskID  = "owner/repo#17"
		prNum   = 17
		baseRef = "main"
		headRef = "rascal/pr-17"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.store.AddRun(state.CreateRunInput{
		ID:         "seed_run_edited",
		TaskID:     taskID,
		Repo:       repo,
		Task:       "seed",
		BaseBranch: baseRef,
		HeadBranch: headRef,
		Trigger:    "seed",
		RunDir:     filepath.Join(t.TempDir(), "seed_run_edited"),
		PRNumber:   prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := s.store.SetRunStatus(seedRun.ID, state.StatusSucceeded, ""); err != nil {
		t.Fatalf("mark seed run succeeded: %v", err)
	}

	payload := []byte(`{"action":"edited","issue":{"number":17,"pull_request":{}},"comment":{"id":202,"body":"  updated feedback  ","user":{"login":"alice"}},"changes":{"body":{"from":"prior feedback"}},"repository":{"full_name":"owner/repo"},"sender":{"login":"alice"}}`)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-edited", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.store.ListRuns(20)
		for _, run := range runs {
			if run.Trigger == "pr_comment" {
				got = run
				return true
			}
		}
		return false
	}, "pr_comment run created")

	if got.TaskID != taskID {
		t.Fatalf("task id = %q, want %q", got.TaskID, taskID)
	}
	if got.PRNumber != prNum {
		t.Fatalf("pr number = %d, want %d", got.PRNumber, prNum)
	}
	if got.BaseBranch != baseRef {
		t.Fatalf("base branch = %q, want %q", got.BaseBranch, baseRef)
	}
	if got.HeadBranch != headRef {
		t.Fatalf("head branch = %q, want %q", got.HeadBranch, headRef)
	}
	if got.Context != "updated feedback" {
		t.Fatalf("context = %q, want trimmed updated comment body", got.Context)
	}
}

func TestHandleWebhookIssueCommentEditedSkipsUnchangedBody(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	payload := []byte(`{"action":"edited","issue":{"number":9,"pull_request":{}},"comment":{"id":303,"body":"  same feedback  ","user":{"login":"alice"}},"changes":{"body":{"from":"same feedback"}},"repository":{"full_name":"owner/repo"},"sender":{"login":"alice"}}`)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-nochange", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.store.ListRuns(10) {
		if run.Trigger == "pr_comment" {
			t.Fatalf("expected no pr_comment run for unchanged edit")
		}
	}
}

func TestHandleWebhookPullRequestReviewUsesStateFallbackContext(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo    = "owner/repo"
		taskID  = "owner/repo#11"
		prNum   = 11
		baseRef = "main"
		headRef = "rascal/pr-11"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.store.AddRun(state.CreateRunInput{
		ID:         "seed_review",
		TaskID:     taskID,
		Repo:       repo,
		Task:       "seed",
		BaseBranch: baseRef,
		HeadBranch: headRef,
		Trigger:    "seed",
		RunDir:     filepath.Join(t.TempDir(), "seed_review"),
		PRNumber:   prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := s.store.SetRunStatus(seedRun.ID, state.StatusSucceeded, ""); err != nil {
		t.Fatalf("mark seed run succeeded: %v", err)
	}

	payload := []byte(`{"action":"submitted","review":{"id":303,"body":"   ","state":"changes_requested","user":{"login":"bob"}},"pull_request":{"number":11},"repository":{"full_name":"owner/repo"},"sender":{"login":"bob"}}`)
	req := webhookRequest(t, payload, "pull_request_review", "delivery-review", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.store.ListRuns(20)
		for _, run := range runs {
			if run.Trigger == "pr_review" {
				got = run
				return true
			}
		}
		return false
	}, "pr_review run created")

	if got.TaskID != taskID {
		t.Fatalf("task id = %q, want %q", got.TaskID, taskID)
	}
	if got.PRNumber != prNum {
		t.Fatalf("pr number = %d, want %d", got.PRNumber, prNum)
	}
	if got.BaseBranch != baseRef {
		t.Fatalf("base branch = %q, want %q", got.BaseBranch, baseRef)
	}
	if got.HeadBranch != headRef {
		t.Fatalf("head branch = %q, want %q", got.HeadBranch, headRef)
	}
	if got.Context != "review state: changes_requested" {
		t.Fatalf("context = %q, want review state fallback", got.Context)
	}
}

func TestHandleWebhookPullRequestReviewCommentIncludesInlineLocation(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo    = "owner/repo"
		taskID  = "owner/repo#12"
		prNum   = 12
		baseRef = "main"
		headRef = "rascal/pr-12"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.store.AddRun(state.CreateRunInput{
		ID:         "seed_review_comment",
		TaskID:     taskID,
		Repo:       repo,
		Task:       "seed",
		BaseBranch: baseRef,
		HeadBranch: headRef,
		Trigger:    "seed",
		RunDir:     filepath.Join(t.TempDir(), "seed_review_comment"),
		PRNumber:   prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if _, err := s.store.SetRunStatus(seedRun.ID, state.StatusSucceeded, ""); err != nil {
		t.Fatalf("mark seed run succeeded: %v", err)
	}

	payload := []byte(`{"action":"created","comment":{"id":404,"body":"Please rename this helper","path":"cmd/rascald/main.go","line":515,"start_line":512,"user":{"login":"eve"}},"pull_request":{"number":12},"repository":{"full_name":"owner/repo"},"sender":{"login":"eve"}}`)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.store.ListRuns(20)
		for _, run := range runs {
			if run.Trigger == "pr_review_comment" {
				got = run
				return true
			}
		}
		return false
	}, "pr_review_comment run created")

	if got.TaskID != taskID {
		t.Fatalf("task id = %q, want %q", got.TaskID, taskID)
	}
	if got.PRNumber != prNum {
		t.Fatalf("pr number = %d, want %d", got.PRNumber, prNum)
	}
	if got.BaseBranch != baseRef {
		t.Fatalf("base branch = %q, want %q", got.BaseBranch, baseRef)
	}
	if got.HeadBranch != headRef {
		t.Fatalf("head branch = %q, want %q", got.HeadBranch, headRef)
	}
	wantContext := "Please rename this helper\n\nInline comment location: cmd/rascald/main.go:512-515"
	if got.Context != wantContext {
		t.Fatalf("context = %q, want %q", got.Context, wantContext)
	}
}

func TestCreateAndQueueRunWritesResponseTarget(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{
		TaskID:   "owner/repo#99",
		Repo:     "owner/repo",
		Task:     "Address PR #99 feedback",
		Trigger:  "pr_comment",
		PRNumber: 99,
		ResponseTarget: &runResponseTarget{
			RequestedBy: " alice ",
		},
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	target, ok, err := loadRunResponseTarget(run.RunDir)
	if err != nil {
		t.Fatalf("load run response target: %v", err)
	}
	if !ok {
		t.Fatal("expected run response target file")
	}
	if target.Repo != "owner/repo" {
		t.Fatalf("target repo = %q, want owner/repo", target.Repo)
	}
	if target.IssueNumber != 99 {
		t.Fatalf("target issue number = %d, want 99", target.IssueNumber)
	}
	if target.RequestedBy != "alice" {
		t.Fatalf("target requested_by = %q, want alice", target.RequestedBy)
	}
	if target.Trigger != "pr_comment" {
		t.Fatalf("target trigger = %q, want pr_comment", target.Trigger)
	}
}

func TestHandleWebhookIssueCommentIgnoresBotActor(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	payload := []byte(`{"action":"created","issue":{"number":9,"pull_request":{}},"comment":{"id":501,"body":"please fix","user":{"login":"rascal[bot]"}},"repository":{"full_name":"owner/repo"},"sender":{"login":"human"}}`)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-bot", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if got := len(s.store.ListRuns(10)); got != 0 {
		t.Fatalf("expected zero runs for bot-authored comment, got %d", got)
	}
}

func TestCreateAndQueueRunSerializesPerTask(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	_, err := s.createAndQueueRun(runRequest{TaskID: "task-1", Repo: "owner/repo", Task: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	second, err := s.createAndQueueRun(runRequest{TaskID: "task-1", Repo: "owner/repo", Task: "second"})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}

	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to start only")
	launcher.mu.Lock()
	firstSpecCount := len(launcher.specs)
	firstSpecDebug := false
	if firstSpecCount > 0 {
		firstSpecDebug = launcher.specs[0].Debug
	}
	launcher.mu.Unlock()
	if firstSpecCount == 0 || !firstSpecDebug {
		t.Fatalf("expected first run spec debug=true, got count=%d debug=%t", firstSpecCount, firstSpecDebug)
	}
	r2, ok := s.store.GetRun(second.ID)
	if !ok {
		t.Fatalf("missing second run %s", second.ID)
	}
	if r2.Status != state.StatusQueued {
		t.Fatalf("expected second run queued, got %s", r2.Status)
	}

	close(waitCh)
	waitFor(t, 2*time.Second, func() bool {
		return launcher.Calls() == 2
	}, "second run to start after first completes")
	waitFor(t, 2*time.Second, func() bool {
		r, ok := s.store.GetRun(second.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "second run to complete")
}

func TestCreateAndQueueRunRespectsGlobalConcurrencyLimit(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	s.maxConcurrent = 1
	defer waitForServerIdle(t, s)

	_, err := s.createAndQueueRun(runRequest{TaskID: "task-1", Repo: "owner/repo", Task: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	second, err := s.createAndQueueRun(runRequest{TaskID: "task-2", Repo: "owner/repo", Task: "second"})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}

	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "only one run starts while at concurrency limit")
	r2, ok := s.store.GetRun(second.ID)
	if !ok {
		t.Fatalf("missing second run %s", second.ID)
	}
	if r2.Status != state.StatusQueued {
		t.Fatalf("expected second run queued, got %s", r2.Status)
	}

	close(waitCh)
	waitFor(t, 2*time.Second, func() bool {
		return launcher.Calls() == 2
	}, "second run to start after slot is available")
	waitFor(t, 2*time.Second, func() bool {
		r, ok := s.store.GetRun(second.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "second run to complete")
}

func TestMergedPRMarksTaskCompleteAndCancelsQueuedRuns(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"
	taskID := "owner/repo#123"

	_, err := s.createAndQueueRun(runRequest{TaskID: taskID, Repo: "owner/repo", Task: "first", PRNumber: 55})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	queuedRun, err := s.createAndQueueRun(runRequest{TaskID: taskID, Repo: "owner/repo", Task: "queued", PRNumber: 55})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")

	if err := s.store.SetTaskPR(taskID, "owner/repo", 55); err != nil {
		t.Fatalf("set task pr: %v", err)
	}
	awaitingRun, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_awaiting_merge",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Task:        "await merge",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 55,
		PRNumber:    55,
	})
	if err != nil {
		t.Fatalf("add awaiting run: %v", err)
	}
	if _, err := s.store.SetRunStatus(awaitingRun.ID, state.StatusReview, ""); err != nil {
		t.Fatalf("set awaiting status: %v", err)
	}

	payload := []byte(`{"action":"closed","pull_request":{"number":55,"merged":true},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "pull_request", "delivery-merged", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for merged pr event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return s.store.IsTaskCompleted(taskID) }, "task marked completed")
	waitFor(t, time.Second, func() bool {
		r, ok := s.store.GetRun(queuedRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "queued run canceled")
	waitFor(t, time.Second, func() bool {
		r, ok := s.store.GetRun(awaitingRun.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "awaiting-feedback run marked succeeded on merge")
	reactions := fakeGH.postedReactions()
	foundRocket := false
	for _, r := range reactions {
		if r.repo == "owner/repo" && r.issueNumber == 55 && r.content == ghapi.ReactionRocket {
			foundRocket = true
			break
		}
	}
	if !foundRocket {
		t.Fatalf("expected merged PR rocket reaction, got %+v", reactions)
	}

	close(waitCh)
}

func TestClosedUnmergedPRCancelsAwaitingFeedbackRuns(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"
	taskID := "owner/repo#987"
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 99}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_unmerged",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Task:        "wait for merge",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 99,
		PRNumber:    99,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusReview, ""); err != nil {
		t.Fatalf("set awaiting status: %v", err)
	}

	payload := []byte(`{"action":"closed","pull_request":{"number":99,"merged":false},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "pull_request", "delivery-closed-unmerged", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for closed unmerged pr event, got %d", rec.Code)
	}

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if updated.Status != state.StatusCanceled {
		t.Fatalf("expected canceled status, got %s", updated.Status)
	}
	if !strings.Contains(updated.Error, "without merge") {
		t.Fatalf("expected unmerged close reason, got %q", updated.Error)
	}
	reactions := fakeGH.postedReactions()
	foundMinus := false
	for _, r := range reactions {
		if r.repo == "owner/repo" && r.issueNumber == 99 && r.content == ghapi.ReactionMinusOne {
			foundMinus = true
			break
		}
	}
	if !foundMinus {
		t.Fatalf("expected -1 reaction on closed unmerged PR, got %+v", reactions)
	}
}

func TestExecuteRunRetriesLauncherFailure(t *testing.T) {
	launcher := &fakeLauncher{
		errSeq: []error{
			errors.New("transient launcher error"),
			nil,
		},
	}
	s := newTestServer(t, launcher)
	s.cfg.RunnerMaxAttempts = 2
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{
		TaskID: "owner/repo#retry",
		Repo:   "owner/repo",
		Task:   "retry task",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	waitFor(t, 4*time.Second, func() bool {
		r, ok := s.store.GetRun(run.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "run to succeed after retry")

	if calls := launcher.Calls(); calls != 2 {
		t.Fatalf("expected 2 launcher calls, got %d", calls)
	}
}

func TestExecuteRunPostsCompletionCommentForCommentTriggeredRun(t *testing.T) {
	launcher := &fakeLauncher{
		res: runner.Result{
			PRNumber: 77,
			PRURL:    "https://example.com/pr/77",
			HeadSHA:  "0123456789abcdef0123456789abcdef01234567",
		},
	}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_comment_completion",
		TaskID:     "owner/repo#77",
		Repo:       "owner/repo",
		Task:       "Address PR #77 feedback",
		BaseBranch: "main",
		HeadBranch: "rascal/pr-77",
		Trigger:    "pr_comment",
		RunDir:     t.TempDir(),
		PRNumber:   77,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 77,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "commit_message.txt"), []byte("fix(rascal): address feedback\n\n- updated handlers\n- added tests\n"), 0o644); err != nil {
		t.Fatalf("write commit message: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte(`{"event":"x","usage":{"total_tokens":123000}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	s.executeRun(run.ID)

	comments := fakeGH.postedComments()
	if len(comments) != 1 {
		t.Fatalf("expected one posted comment, got %d", len(comments))
	}
	comment := comments[0]
	if comment.repo != "owner/repo" || comment.issueNumber != 77 {
		t.Fatalf("unexpected comment target: %+v", comment)
	}
	if !strings.Contains(comment.body, "@alice implemented in commit [`0123456789ab`]") {
		t.Fatalf("expected requester mention with short sha, got body:\n%s", comment.body)
	}
	if !strings.Contains(comment.body, "- updated handlers") {
		t.Fatalf("expected commit body bullets in comment, got:\n%s", comment.body)
	}
	if !strings.Contains(comment.body, "<details><summary>Goose Details</summary>") {
		t.Fatalf("expected goose details section, got:\n%s", comment.body)
	}
	if !strings.Contains(comment.body, "Rascal run `run_comment_completion` completed in ") || !strings.Contains(comment.body, "123K tokens") {
		t.Fatalf("expected runtime and token summary, got:\n%s", comment.body)
	}
}

func TestPostRunCompletionCommentSkipsDuplicateWhenMarkerExists(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_comment_dedupe",
		TaskID:     "owner/repo#88",
		Repo:       "owner/repo",
		Task:       "Address PR #88 feedback",
		BaseBranch: "main",
		HeadBranch: "rascal/pr-88",
		Trigger:    "pr_comment",
		RunDir:     t.TempDir(),
		PRNumber:   88,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 88,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}

	s.postRunCompletionCommentBestEffort(run)
	s.postRunCompletionCommentBestEffort(run)

	if calls := fakeGH.createCommentCalls(); calls != 1 {
		t.Fatalf("expected a single github comment call, got %d", calls)
	}
	comments := fakeGH.postedComments()
	if len(comments) != 1 {
		t.Fatalf("expected one posted comment, got %d", len(comments))
	}
	markerPath := runCompletionCommentMarkerPath(run.RunDir)
	markerData, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read completion marker: %v", err)
	}
	var marker runCompletionCommentMarker
	if err := json.Unmarshal(markerData, &marker); err != nil {
		t.Fatalf("decode completion marker: %v", err)
	}
	if marker.RunID != run.ID {
		t.Fatalf("marker run_id = %q, want %q", marker.RunID, run.ID)
	}
}

func TestPostRunCompletionCommentRetriesAfterPostFailure(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{
		createIssueCommentErrSeq: []error{
			errors.New("transient github failure"),
			nil,
		},
	}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_comment_retry",
		TaskID:     "owner/repo#89",
		Repo:       "owner/repo",
		Task:       "Address PR #89 feedback",
		BaseBranch: "main",
		HeadBranch: "rascal/pr-89",
		Trigger:    "pr_comment",
		RunDir:     t.TempDir(),
		PRNumber:   89,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 89,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}

	s.postRunCompletionCommentBestEffort(run)

	if calls := fakeGH.createCommentCalls(); calls != 1 {
		t.Fatalf("expected one github comment call after first attempt, got %d", calls)
	}
	if comments := fakeGH.postedComments(); len(comments) != 0 {
		t.Fatalf("expected no posted comments after failed attempt, got %d", len(comments))
	}
	if _, err := os.Stat(runCompletionCommentMarkerPath(run.RunDir)); !os.IsNotExist(err) {
		t.Fatalf("expected marker to be absent after failed post, stat err=%v", err)
	}

	s.postRunCompletionCommentBestEffort(run)

	if calls := fakeGH.createCommentCalls(); calls != 2 {
		t.Fatalf("expected second github comment call on retry, got %d", calls)
	}
	if comments := fakeGH.postedComments(); len(comments) != 1 {
		t.Fatalf("expected one posted comment after retry, got %d", len(comments))
	}
	if _, err := os.Stat(runCompletionCommentMarkerPath(run.RunDir)); err != nil {
		t.Fatalf("expected marker after successful retry: %v", err)
	}
}

func TestHandleTaskSubresourcesGet(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	taskID := "owner/repo#22"
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{
		ID:          taskID,
		Repo:        "owner/repo",
		IssueNumber: 22,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/owner%2Frepo%2322", nil)
	rec := httptest.NewRecorder()
	s.handleTaskSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHandleCreateTaskRespectsProvidedTaskID(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		strings.NewReader(`{"task_id":"owner/repo#99","repo":"owner/repo","task":"follow-up","base_branch":"main"}`),
	)
	rec := httptest.NewRecorder()
	s.handleCreateTask(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var out struct {
		Run state.Run `json:"run"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Run.TaskID != "owner/repo#99" {
		t.Fatalf("expected task id owner/repo#99, got %q", out.Run.TaskID)
	}
	if !out.Run.Debug {
		t.Fatal("expected debug=true by default")
	}
}

func TestHandleCreateTaskAcceptsDebugFalse(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		strings.NewReader(`{"task_id":"owner/repo#100","repo":"owner/repo","task":"quiet debug","base_branch":"main","debug":false}`),
	)
	rec := httptest.NewRecorder()
	s.handleCreateTask(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var out struct {
		Run state.Run `json:"run"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Run.Debug {
		t.Fatal("expected debug=false when explicitly requested")
	}
}

func TestHandleCancelRunQueued(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	first, err := s.createAndQueueRun(runRequest{TaskID: "t1", Repo: "owner/repo", Task: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	second, err := s.createAndQueueRun(runRequest{TaskID: "t1", Repo: "owner/repo", Task: "second"})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run start")

	rec := httptest.NewRecorder()
	s.handleCancelRun(rec, second.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for queued cancel, got %d", rec.Code)
	}

	updated, ok := s.store.GetRun(second.ID)
	if !ok {
		t.Fatalf("missing run %s", second.ID)
	}
	if updated.Status != state.StatusCanceled {
		t.Fatalf("expected canceled status, got %s", updated.Status)
	}

	_ = first
}

func TestHandleCancelRunActiveUsesUserReason(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{TaskID: "active-cancel", Repo: "owner/repo", Task: "cancel me"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "run start")

	rec := httptest.NewRecorder()
	s.handleCancelRun(rec, run.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for active cancel, got %d", rec.Code)
	}

	close(waitCh)
	waitFor(t, 2*time.Second, func() bool {
		updated, ok := s.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusCanceled && strings.Contains(updated.Error, "canceled by user")
	}, "active run canceled with user reason")
}

func TestCanceledRunDoesNotTransitionToSuccess(t *testing.T) {
	done := make(chan struct{})
	launcher := &stubbornLauncher{
		wait: done,
		res: runner.Result{
			PRNumber: 42,
			PRURL:    "https://example.com/pr/42",
		},
	}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{TaskID: "cancel-guard", Repo: "owner/repo", Task: "guard cancel"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		current, ok := s.store.GetRun(run.ID)
		return ok && current.Status == state.StatusRunning
	}, "run enters running status")

	rec := httptest.NewRecorder()
	s.handleCancelRun(rec, run.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for active cancel, got %d", rec.Code)
	}

	close(done)
	waitFor(t, 2*time.Second, func() bool {
		current, ok := s.store.GetRun(run.ID)
		return ok && current.Status == state.StatusCanceled
	}, "run remains canceled after launcher returns success")

	current, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if current.Status != state.StatusCanceled {
		t.Fatalf("expected final canceled status, got %s", current.Status)
	}
}

func TestCancelActiveRunsUsesDrainReason(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{TaskID: "drain-reason", Repo: "owner/repo", Task: "drain"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "run start")

	s.cancelActiveRuns("orchestrator shutdown drain timeout")
	close(waitCh)

	waitFor(t, 2*time.Second, func() bool {
		current, ok := s.store.GetRun(run.ID)
		return ok && current.Status == state.StatusCanceled && strings.Contains(current.Error, "drain timeout")
	}, "run canceled with drain reason")
}

func TestExecuteRunHonorsPersistedCancelBeforeStart(t *testing.T) {
	launcher := &fakeLauncher{}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_pre_cancel",
		TaskID:     "task_pre_cancel",
		Repo:       "owner/repo",
		Task:       "should not start",
		BaseBranch: "main",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.store.RequestRunCancel(run.ID, "persisted cancel", "user"); err != nil {
		t.Fatalf("request run cancel: %v", err)
	}

	s.executeRun(run.ID)

	if calls := launcher.Calls(); calls != 0 {
		t.Fatalf("expected launcher not to start, got calls=%d", calls)
	}
	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusCanceled {
		t.Fatalf("expected canceled status, got %s", updated.Status)
	}
	if !strings.Contains(updated.Error, "persisted cancel") {
		t.Fatalf("expected persisted cancel reason, got %q", updated.Error)
	}
}

func TestPersistedRunCancelStopsActiveRun(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	run, err := s.createAndQueueRun(runRequest{TaskID: "persisted-cancel", Repo: "owner/repo", Task: "cancel while running"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "run start")

	if err := s.store.RequestRunCancel(run.ID, "cancel from store", "user"); err != nil {
		t.Fatalf("request run cancel: %v", err)
	}

	waitFor(t, 4*time.Second, func() bool {
		current, ok := s.store.GetRun(run.ID)
		return ok && current.Status == state.StatusCanceled
	}, "run canceled from persisted request")
	current, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if !strings.Contains(current.Error, "cancel from store") {
		t.Fatalf("expected persisted cancel reason in run error, got %q", current.Error)
	}
}

func TestRecoverQueueStateAppliesPersistedCancel(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_recover_cancel",
		TaskID:     "task_recover_cancel",
		Repo:       "owner/repo",
		Task:       "recover queued cancel",
		BaseBranch: "main",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.store.RequestRunCancel(run.ID, "queued canceled before restart", "user"); err != nil {
		t.Fatalf("request run cancel: %v", err)
	}

	s.recoverQueuedCancels()
	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusCanceled {
		t.Fatalf("expected recovered run canceled, got %s", updated.Status)
	}
	if !strings.Contains(updated.Error, "queued canceled before restart") {
		t.Fatalf("unexpected recovered cancel reason: %q", updated.Error)
	}
}

func TestRecoverRunningRunExpiredLeaseRequeues(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_recover_expired_lease",
		TaskID:     "task_recover_expired_lease",
		Repo:       "owner/repo",
		Task:       "recover running expired lease",
		BaseBranch: "main",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if err := s.store.UpsertRunLease(run.ID, "other-instance", time.Nanosecond); err != nil {
		t.Fatalf("upsert run lease: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	s.recoverRunningRuns()

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusQueued {
		t.Fatalf("expected queued status after recovery, got %s", updated.Status)
	}
	if updated.StartedAt != nil {
		t.Fatalf("expected started_at cleared on requeue")
	}
	if _, ok := s.store.GetRunLease(run.ID); ok {
		t.Fatalf("expected stale run lease deleted")
	}
}

func TestRecoverRunningRunValidLeaseKeepsRunning(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_recover_valid_lease",
		TaskID:     "task_recover_valid_lease",
		Repo:       "owner/repo",
		Task:       "recover running valid lease",
		BaseBranch: "main",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if err := s.store.UpsertRunLease(run.ID, "other-instance", 2*time.Minute); err != nil {
		t.Fatalf("upsert run lease: %v", err)
	}

	s.recoverRunningRuns()

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusRunning {
		t.Fatalf("expected running status with valid lease, got %s", updated.Status)
	}
}

func TestRecoverRunningRunWithoutLeaseOldStartRequeues(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_recover_no_lease_old",
		TaskID:     "task_recover_no_lease_old",
		Repo:       "owner/repo",
		Task:       "recover running no lease old start",
		BaseBranch: "main",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	oldStart := time.Now().UTC().Add(-2 * runLeaseTTL)
	if _, err := s.store.UpdateRun(run.ID, func(r *state.Run) error {
		r.StartedAt = &oldStart
		return nil
	}); err != nil {
		t.Fatalf("set old started_at: %v", err)
	}

	s.recoverRunningRuns()

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusQueued {
		t.Fatalf("expected queued status without lease and old start, got %s", updated.Status)
	}
}

func TestHandleRunLogsRespectsLines(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_logs",
		TaskID:     "task_logs",
		Repo:       "owner/repo",
		Task:       "show logs",
		BaseBranch: "main",
		RunDir:     runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	var runnerLog strings.Builder
	var gooseLog strings.Builder
	for i := 1; i <= 5; i++ {
		_, _ = fmt.Fprintf(&runnerLog, "runner-%d\n", i)
		_, _ = fmt.Fprintf(&gooseLog, "goose-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte(runnerLog.String()), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte(gooseLog.String()), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?lines=2", nil)
	rec := httptest.NewRecorder()
	s.handleRunSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "runner-1") || strings.Contains(body, "goose-1") {
		t.Fatalf("expected oldest lines to be omitted, got:\n%s", body)
	}
	if !strings.Contains(body, "runner-5") || !strings.Contains(body, "goose-5") {
		t.Fatalf("expected newest lines to be present, got:\n%s", body)
	}
}

func TestHandleRunLogsJSONIncludesStatusAndDone(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_logs_json",
		TaskID:     "task_logs_json",
		Repo:       "owner/repo",
		Task:       "show logs as json",
		BaseBranch: "main",
		RunDir:     runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusSucceeded, ""); err != nil {
		t.Fatalf("set run status: %v", err)
	}

	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\nrunner-2\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte("goose-1\ngoose-2\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?lines=1&format=json", nil)
	rec := httptest.NewRecorder()
	s.handleRunSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected json content type, got %q", rec.Header().Get("Content-Type"))
	}
	var out struct {
		Logs      string          `json:"logs"`
		RunStatus state.RunStatus `json:"run_status"`
		Done      bool            `json:"done"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.RunStatus != state.StatusSucceeded {
		t.Fatalf("expected succeeded status, got %s", out.RunStatus)
	}
	if !out.Done {
		t.Fatal("expected done=true for succeeded run")
	}
	if strings.Contains(out.Logs, "runner-1") || strings.Contains(out.Logs, "goose-1") {
		t.Fatalf("expected oldest lines to be omitted, got:\n%s", out.Logs)
	}
	if !strings.Contains(out.Logs, "runner-2") || !strings.Contains(out.Logs, "goose-2") {
		t.Fatalf("expected newest lines to be present, got:\n%s", out.Logs)
	}
}

func TestHandleRunLogsMissingGooseFileStillReturnsRunnerLogs(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_logs_missing_goose",
		TaskID:     "task_logs_missing_goose",
		Repo:       "owner/repo",
		Task:       "show logs without goose output",
		BaseBranch: "main",
		RunDir:     runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\nrunner-2\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?lines=5", nil)
	rec := httptest.NewRecorder()
	s.handleRunSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "runner-2") {
		t.Fatalf("expected runner logs in response, got:\n%s", body)
	}
	if !strings.Contains(body, "== goose.ndjson ==") {
		t.Fatalf("expected goose section header in response, got:\n%s", body)
	}
	if !strings.Contains(body, "(goose.ndjson not found)") {
		t.Fatalf("expected missing goose note, got:\n%s", body)
	}
}

func TestHandleRunLogsRejectsInvalidFormat(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_logs_bad_format",
		TaskID:     "task_logs_bad_format",
		Repo:       "owner/repo",
		Task:       "bad format",
		BaseBranch: "main",
		RunDir:     runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte("goose-1\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?format=xml", nil)
	rec := httptest.NewRecorder()
	s.handleRunSubresources(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestBuildHeadBranchUsesTaskSummaryForAdHocRunTaskID(t *testing.T) {
	got := buildHeadBranch(
		"run_97073bc1e7787f7c",
		"When running bootstrap with --skip-deploy, preserve host/domain values.\n\nKeep it small.",
		"run_97073bc1e7787f7c",
	)
	if !strings.HasPrefix(got, "rascal/when-running-bootstrap") {
		t.Fatalf("expected summary-based branch prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "-97073bc1e7") {
		t.Fatalf("expected short run-id suffix, got %q", got)
	}
}

func TestBuildHeadBranchUsesTaskIDForNamedTasks(t *testing.T) {
	got := buildHeadBranch("owner/repo#123", "ignored task text", "run_deadbeefcafefeed")
	if !strings.HasPrefix(got, "rascal/owner/repo-123-") {
		t.Fatalf("expected task-id-based branch prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "-deadbeefca") {
		t.Fatalf("expected short run-id suffix, got %q", got)
	}
}

func TestHandleReadyReflectsDrainingState(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	readyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRec := httptest.NewRecorder()
	s.handleReady(readyRec, readyReq)
	if readyRec.Code != http.StatusOK {
		t.Fatalf("expected ready 200 before drain, got %d", readyRec.Code)
	}

	s.beginDrain()

	notReadyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	notReadyRec := httptest.NewRecorder()
	s.handleReady(notReadyRec, notReadyReq)
	if notReadyRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected ready 503 during drain, got %d", notReadyRec.Code)
	}
}

func TestCreateAndQueueRunRejectedWhenDraining(t *testing.T) {
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	s.beginDrain()
	_, err := s.createAndQueueRun(runRequest{
		TaskID: "owner/repo#1",
		Repo:   "owner/repo",
		Task:   "should be rejected",
	})
	if !errors.Is(err, errServerDraining) {
		t.Fatalf("expected errServerDraining, got %v", err)
	}
}

func TestBeginDrainLeavesQueuedRunsForNextSlot(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	_, err := s.createAndQueueRun(runRequest{TaskID: "owner/repo#drain", Repo: "owner/repo", Task: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	queued, err := s.createAndQueueRun(runRequest{TaskID: "owner/repo#drain", Repo: "owner/repo", Task: "queued"})
	if err != nil {
		t.Fatalf("create queued run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")

	s.beginDrain()

	waitFor(t, time.Second, func() bool {
		r, ok := s.store.GetRun(queued.ID)
		return ok && r.Status == state.StatusQueued
	}, "queued run remains queued during drain")
}
