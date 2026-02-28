package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
		StatePath:  filepath.Join(dataDir, "state.json"),
		MaxRuns:    200,
		RunnerMode: "noop",
	}
	store, err := state.New(cfg.StatePath, cfg.MaxRuns)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	return &server{
		cfg:        cfg,
		store:      store,
		launcher:   launcher,
		gh:         ghapi.NewAPIClient(""),
		activeRuns: make(map[string]string),
		queuedRuns: make(map[string][]string),
		runCancels: make(map[string]context.CancelFunc),
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
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.activeRuns) == 0
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

func TestMergedPRMarksTaskCompleteAndCancelsQueuedRuns(t *testing.T) {
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
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

	close(waitCh)
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
