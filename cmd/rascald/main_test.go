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
		activeRuns:    make(map[string]string),
		queuedRuns:    make(map[string][]string),
		runCancels:    make(map[string]context.CancelFunc),
		maxConcurrent: defaultMaxConcurrent(),
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

func TestBeginDrainCancelsQueuedRuns(t *testing.T) {
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
		return ok && r.Status == state.StatusCanceled
	}, "queued run canceled by drain")
}
