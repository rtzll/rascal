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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/credentials"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/state"
)

func TestInstructionTextPRGitContext(t *testing.T) {
	run := state.Run{
		ID:          "run_abc123",
		TaskID:      "task_xyz789",
		Repo:        "acme/widgets",
		Task:        "Address PR #137 feedback",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-xyz789",
		Trigger:     "pr_comment",
		IssueNumber: 42,
		PRNumber:    137,
		Context:     "Please rebase this on main and fix the conflicts.",
	}

	got := instructionText(run)

	for _, want := range []string{
		"## Git Context",
		"- Remote: `origin`",
		"- Base branch: `main`",
		"- Head branch: `rascal/task-xyz789`",
		"- You may use `git` and `gh` directly.",
		"- Push only to `origin` branch `rascal/task-xyz789`.",
		"`git push --force-with-lease origin HEAD:rascal/task-xyz789`",
		"`git push origin HEAD:rascal/task-xyz789`",
		"do not rely on the harness to publish those changes for you",
		"## Additional Context",
		"Please rebase this on main and fix the conflicts.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructionText() missing %q\nfull text:\n%s", want, got)
		}
	}
}

func TestInstructionTextNonPRRunOmitsGitContext(t *testing.T) {
	run := state.Run{
		ID:         "run_abc123",
		TaskID:     "task_xyz789",
		Repo:       "acme/widgets",
		Task:       "Fix flaky test",
		BaseBranch: "main",
		HeadBranch: "rascal/fix-flaky-test",
		Trigger:    "issue",
	}

	got := instructionText(run)
	if strings.Contains(got, "## Git Context") {
		t.Fatalf("instructionText() unexpectedly included Git Context\nfull text:\n%s", got)
	}
}

var (
	testStateTemplateOnce sync.Once
	testStateTemplatePath string
	testStateTemplateErr  error
)

type fakeLauncher struct {
	mu       sync.Mutex
	calls    int
	specs    []runner.Spec
	waitCh   <-chan struct{}
	res      fakeRunResult
	err      error
	resSeq   []fakeRunResult
	errSeq   []error
	execs    map[string]*fakeExecution
	nextExec int
}

type stubbornLauncher struct {
	mu       sync.Mutex
	calls    int
	wait     <-chan struct{}
	res      fakeRunResult
	lastSpec runner.Spec
	stopped  bool
}

type fakeRunResult struct {
	PRNumber int
	PRURL    string
	HeadSHA  string
	ExitCode int
	Error    string
}

type fakeExecution struct {
	handle    runner.ExecutionHandle
	spec      runner.Spec
	waitCh    <-chan struct{}
	result    fakeRunResult
	stopped   bool
	finalized bool
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

type postedIssueCommentReaction struct {
	repo      string
	commentID int64
	content   string
}

type postedPullRequestReviewReaction struct {
	repo       string
	pullNumber int
	reviewID   int64
	content    string
}

type postedPullRequestReviewCommentReaction struct {
	repo      string
	commentID int64
	content   string
}

type fakeGitHubClient struct {
	mu sync.Mutex

	issueData ghapi.IssueData
	issueErr  error

	issueReactions []postedIssueReaction
	removedIssues  []string

	issueCommentReactions             []postedIssueCommentReaction
	pullRequestReviewReactions        []postedPullRequestReviewReaction
	pullRequestReviewCommentReactions []postedPullRequestReviewCommentReaction

	issueComments            []postedIssueComment
	createIssueCommentErr    error
	createIssueCommentErrSeq []error
	createIssueCommentCalls  int
}

func (f *fakeLauncher) StartDetached(_ context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
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
	if err != nil {
		f.mu.Unlock()
		return runner.ExecutionHandle{}, err
	}
	if f.execs == nil {
		f.execs = make(map[string]*fakeExecution)
	}
	f.nextExec++
	handle := runner.ExecutionHandle{
		Backend: "fake",
		ID:      fmt.Sprintf("exec-%d", f.nextExec),
		Name:    fmt.Sprintf("rascal-%s", spec.RunID),
	}
	execRec := &fakeExecution{
		handle: handle,
		spec:   spec,
		waitCh: f.waitCh,
		result: res,
	}
	f.execs[handle.ID] = execRec
	f.execs[handle.Name] = execRec
	f.mu.Unlock()
	return handle, nil
}

func (f *fakeLauncher) lookupExecution(handle runner.ExecutionHandle) (*fakeExecution, bool) {
	if execRec, ok := f.execs[handle.ID]; ok {
		return execRec, true
	}
	if execRec, ok := f.execs[handle.Name]; ok {
		return execRec, true
	}
	return nil, false
}

func (f *fakeLauncher) Inspect(_ context.Context, handle runner.ExecutionHandle) (runner.ExecutionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	execRec, ok := f.lookupExecution(handle)
	if !ok {
		return runner.ExecutionState{}, runner.ErrExecutionNotFound
	}
	running := false
	if !execRec.stopped {
		if execRec.waitCh == nil {
			running = false
		} else {
			select {
			case <-execRec.waitCh:
				running = false
			default:
				running = true
			}
		}
	}
	if running {
		return runner.ExecutionState{Running: true}, nil
	}
	if !execRec.finalized {
		if err := writeFakeMeta(execRec.spec, execRec.result); err != nil {
			return runner.ExecutionState{}, err
		}
		execRec.finalized = true
	}
	exitCode := execRec.result.ExitCode
	return runner.ExecutionState{Running: false, ExitCode: &exitCode}, nil
}

func (f *fakeLauncher) Stop(_ context.Context, handle runner.ExecutionHandle, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	execRec, ok := f.lookupExecution(handle)
	if !ok {
		return runner.ErrExecutionNotFound
	}
	if execRec.waitCh != nil {
		select {
		case <-execRec.waitCh:
			return nil
		default:
		}
	}
	execRec.stopped = true
	if execRec.result.ExitCode == 0 {
		execRec.result.ExitCode = 137
	}
	if execRec.result.Error == "" {
		execRec.result.Error = "canceled"
	}
	return nil
}

func (f *fakeLauncher) Remove(_ context.Context, handle runner.ExecutionHandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	execRec, ok := f.lookupExecution(handle)
	if !ok {
		return nil
	}
	delete(f.execs, execRec.handle.ID)
	delete(f.execs, execRec.handle.Name)
	return nil
}

func (l *stubbornLauncher) StartDetached(_ context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
	l.mu.Lock()
	l.calls++
	l.lastSpec = spec
	l.mu.Unlock()
	return runner.ExecutionHandle{
		Backend: "fake",
		ID:      "stubborn-exec",
		Name:    "stubborn-" + spec.RunID,
	}, nil
}

func (l *stubbornLauncher) Inspect(_ context.Context, _ runner.ExecutionHandle) (runner.ExecutionState, error) {
	l.mu.Lock()
	spec := l.lastSpec
	waitCh := l.wait
	res := l.res
	stopped := l.stopped
	l.mu.Unlock()
	running := false
	if !stopped {
		if waitCh == nil {
			running = false
		} else {
			select {
			case <-waitCh:
				running = false
			default:
				running = true
			}
		}
	}
	if running {
		return runner.ExecutionState{Running: true}, nil
	}
	if stopped {
		if res.ExitCode == 0 {
			res.ExitCode = 137
		}
		if res.Error == "" {
			res.Error = "canceled"
		}
	}
	if err := writeFakeMeta(spec, res); err != nil {
		return runner.ExecutionState{}, err
	}
	exitCode := res.ExitCode
	return runner.ExecutionState{Running: false, ExitCode: &exitCode}, nil
}

func (l *stubbornLauncher) Stop(_ context.Context, _ runner.ExecutionHandle, _ time.Duration) error {
	l.mu.Lock()
	if l.wait != nil {
		select {
		case <-l.wait:
			l.mu.Unlock()
			return nil
		default:
		}
	}
	l.stopped = true
	l.mu.Unlock()
	return nil
}

func (l *stubbornLauncher) Remove(_ context.Context, _ runner.ExecutionHandle) error {
	return nil
}

func writeFakeMeta(spec runner.Spec, res fakeRunResult) error {
	meta := runner.Meta{
		RunID:      spec.RunID,
		TaskID:     spec.TaskID,
		Repo:       spec.Repo,
		BaseBranch: spec.BaseBranch,
		HeadBranch: spec.HeadBranch,
		PRNumber:   res.PRNumber,
		PRURL:      res.PRURL,
		HeadSHA:    res.HeadSHA,
		ExitCode:   res.ExitCode,
		Error:      strings.TrimSpace(res.Error),
	}
	if err := runner.WriteMeta(filepath.Join(spec.RunDir, "meta.json"), meta); err != nil {
		return fmt.Errorf("write fake run metadata: %w", err)
	}
	return nil
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

func (f *fakeGitHubClient) RemoveIssueReactions(_ context.Context, repo string, issueNumber int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedIssues = append(f.removedIssues, fmt.Sprintf("%s#%d", repo, issueNumber))
	filtered := f.issueReactions[:0]
	for _, reaction := range f.issueReactions {
		if reaction.repo == repo && reaction.issueNumber == issueNumber {
			continue
		}
		filtered = append(filtered, reaction)
	}
	f.issueReactions = filtered
	return nil
}

func (f *fakeGitHubClient) AddIssueCommentReaction(_ context.Context, repo string, commentID int64, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issueCommentReactions = append(f.issueCommentReactions, postedIssueCommentReaction{
		repo:      repo,
		commentID: commentID,
		content:   content,
	})
	return nil
}

func (f *fakeGitHubClient) AddPullRequestReviewReaction(_ context.Context, repo string, pullNumber int, reviewID int64, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullRequestReviewReactions = append(f.pullRequestReviewReactions, postedPullRequestReviewReaction{
		repo:       repo,
		pullNumber: pullNumber,
		reviewID:   reviewID,
		content:    content,
	})
	return nil
}

func (f *fakeGitHubClient) AddPullRequestReviewCommentReaction(_ context.Context, repo string, commentID int64, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pullRequestReviewCommentReactions = append(f.pullRequestReviewCommentReactions, postedPullRequestReviewCommentReaction{
		repo:      repo,
		commentID: commentID,
		content:   content,
	})
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

func findPostedComment(comments []postedIssueComment, marker string) (postedIssueComment, bool) {
	for _, comment := range comments {
		if strings.Contains(comment.body, marker) {
			return comment, true
		}
	}
	return postedIssueComment{}, false
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

func (f *fakeGitHubClient) postedIssueCommentReactions() []postedIssueCommentReaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]postedIssueCommentReaction, len(f.issueCommentReactions))
	copy(out, f.issueCommentReactions)
	return out
}

func (f *fakeGitHubClient) postedPullRequestReviewReactions() []postedPullRequestReviewReaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]postedPullRequestReviewReaction, len(f.pullRequestReviewReactions))
	copy(out, f.pullRequestReviewReactions)
	return out
}

func (f *fakeGitHubClient) postedPullRequestReviewCommentReactions() []postedPullRequestReviewCommentReaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]postedPullRequestReviewCommentReaction, len(f.pullRequestReviewCommentReactions))
	copy(out, f.pullRequestReviewCommentReactions)
	return out
}

func (f *fakeGitHubClient) removedIssueKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.removedIssues))
	copy(out, f.removedIssues)
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
	return newTestServerWithPaths(t, launcher, dataDir, filepath.Join(dataDir, "state.db"), "test-instance")
}

func newTestServerWithPaths(t *testing.T, launcher runner.Launcher, dataDir, statePath, instanceID string) *server {
	t.Helper()

	cfg := config.ServerConfig{
		DataDir:              dataDir,
		StatePath:            statePath,
		MaxRuns:              200,
		RunnerMode:           "noop",
		CredentialRenewEvery: 20 * time.Millisecond,
	}
	if err := prepareTestStatePath(cfg.StatePath); err != nil {
		t.Fatalf("prepare test state path: %v", err)
	}
	store, err := state.NewWithoutMigrate(cfg.StatePath, cfg.MaxRuns)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	return &server{
		cfg:                cfg,
		store:              store,
		launcher:           launcher,
		gh:                 ghapi.NewAPIClient(""),
		runCancels:         make(map[string]context.CancelFunc),
		maxConcurrent:      defaultMaxConcurrent(),
		instanceID:         strings.TrimSpace(instanceID),
		supervisorInterval: 10 * time.Millisecond,
		retryBackoff: func(_ int) time.Duration {
			return 10 * time.Millisecond
		},
	}
}

func prepareTestStatePath(statePath string) error {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		return fmt.Errorf("state path is required")
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if _, err := os.Stat(statePath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat state path %s: %w", statePath, err)
	}

	templatePath, err := testStateTemplate()
	if err != nil {
		return err
	}
	for _, suffix := range []string{"", "-shm", "-wal"} {
		src := templatePath + suffix
		dst := statePath + suffix
		if err := copyFileIfExists(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func testStateTemplate() (string, error) {
	testStateTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "rascal-state-template-*")
		if err != nil {
			testStateTemplateErr = fmt.Errorf("create template dir: %w", err)
			return
		}
		path := filepath.Join(dir, "state.db")
		store, err := state.New(path, 200)
		if err != nil {
			testStateTemplateErr = fmt.Errorf("create template state db: %w", err)
			return
		}
		if err := store.Close(); err != nil {
			testStateTemplateErr = fmt.Errorf("close template state db: %w", err)
			return
		}
		testStateTemplatePath = path
	})
	if testStateTemplateErr != nil {
		return "", testStateTemplateErr
	}
	return testStateTemplatePath, nil
}

func copyFileIfExists(src, dst string) (err error) {
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if info.IsDir() {
		return fmt.Errorf("copy %s: source is directory", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", src, closeErr)
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", dst, closeErr)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

func waitForRunExecution(t *testing.T, s *server, runID string) state.RunExecution {
	t.Helper()
	var execRec state.RunExecution
	waitFor(t, 2*time.Second, func() bool {
		rec, ok := s.store.GetRunExecution(runID)
		if !ok {
			return false
		}
		if strings.TrimSpace(rec.Status) != "running" {
			return false
		}
		execRec = rec
		return true
	}, "run execution persisted")
	return execRec
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
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for condition: %s", msg)
}

func waitForServerIdle(t *testing.T, s *server) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool {
		return s.activeRunCount() == 0
	}, "server idle")
}

func markRunSucceeded(t *testing.T, s *server, runID string) {
	t.Helper()
	if _, err := s.store.SetRunStatus(runID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set run running before success: %v", err)
	}
	if _, err := s.store.SetRunStatus(runID, state.StatusSucceeded, ""); err != nil {
		t.Fatalf("set run succeeded: %v", err)
	}
}

func markRunReview(t *testing.T, s *server, runID string) {
	t.Helper()
	if _, err := s.store.SetRunStatus(runID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set run running before review: %v", err)
	}
	if _, err := s.store.SetRunStatus(runID, state.StatusReview, ""); err != nil {
		t.Fatalf("set run review: %v", err)
	}
}

func TestHandleWebhookRecordsDeliveryOnlyAfterSuccess(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	_ = waitForRunExecution(t, s, runningRun.ID)

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
	waitFor(t, 3*time.Second, func() bool {
		r, ok := s.store.GetRun(runningRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "running run canceled")

	close(waitCh)
}

func TestHandleWebhookIssueReopenedReenablesTask(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
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
	_ = waitForRunExecution(t, s, runningRun.ID)

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

	close(waitCh)
	waitFor(t, 3*time.Second, func() bool {
		r, ok := s.store.GetRun(editedRun.ID)
		return ok && state.IsFinalRunStatus(r.Status)
	}, "edited run completed")

}

func TestHandleWebhookIssueLabeledMigratesTaskBackend(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	s.cfg.AgentSessionMode = agent.SessionModeAll
	defer waitForServerIdle(t, s)

	const taskID = "owner/repo#7"
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{
		ID:           taskID,
		Repo:         "owner/repo",
		AgentBackend: agent.BackendGoose,
		IssueNumber:  7,
	}); err != nil {
		t.Fatalf("upsert legacy task: %v", err)
	}
	if _, err := s.store.UpsertTaskAgentSession(state.UpsertTaskAgentSessionInput{
		TaskID:           taskID,
		AgentBackend:     agent.BackendGoose,
		BackendSessionID: "legacy-goose-session",
		SessionKey:       "legacy",
		SessionRoot:      filepath.Join(t.TempDir(), "legacy-session"),
		LastRunID:        "run_legacy",
	}); err != nil {
		t.Fatalf("upsert legacy task session: %v", err)
	}

	payload := []byte(`{"action":"labeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body"},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "issues", "delivery-backend-migrate", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for migrated labeled issue event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return len(s.store.ListRuns(10)) == 1 }, "run queued")

	run := s.store.ListRuns(10)[0]
	if run.AgentBackend != agent.BackendCodex {
		t.Fatalf("run backend = %s, want %s", run.AgentBackend, agent.BackendCodex)
	}

	task, ok := s.store.GetTask(taskID)
	if !ok {
		t.Fatalf("missing task %s", taskID)
	}
	if task.AgentBackend != agent.BackendCodex {
		t.Fatalf("task backend = %s, want %s", task.AgentBackend, agent.BackendCodex)
	}

	var session state.TaskAgentSession
	waitFor(t, time.Second, func() bool {
		var ok bool
		session, ok = s.store.GetTaskAgentSession(taskID)
		return ok
	}, "migrated task session")
	if session.AgentBackend != agent.BackendCodex {
		t.Fatalf("task session backend = %s, want %s", session.AgentBackend, agent.BackendCodex)
	}
	if session.BackendSessionID != "" {
		t.Fatalf("task session id = %q, want empty after backend migration", session.BackendSessionID)
	}
}

func TestHandleWebhookIssueUnlabeledRemovesPastReactions(t *testing.T) {
	t.Parallel()
	fakeGH := &fakeGitHubClient{}
	fakeGH.addIssueReaction("owner/repo", 7, ghapi.ReactionEyes)
	fakeGH.addIssueReaction("owner/repo", 7, ghapi.ReactionRocket)
	fakeGH.addIssueReaction("owner/repo", 8, ghapi.ReactionEyes)

	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	payload := []byte(`{"action":"unlabeled","label":{"name":"rascal"},"issue":{"number":7,"title":"Title","body":"Body","labels":[]},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "issues", "delivery-unlabeled", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for unlabeled issue event, got %d", rec.Code)
	}

	if got := fakeGH.removedIssueKeys(); len(got) != 1 || got[0] != "owner/repo#7" {
		t.Fatalf("unexpected removed reaction calls: %v", got)
	}
	if got := fakeGH.postedReactions(); len(got) != 1 || got[0].issueNumber != 8 {
		t.Fatalf("expected only unrelated issue reactions to remain, got %+v", got)
	}
}

func TestHandleListRunsSupportsAllQuery(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})

	req := httptest.NewRequest(http.MethodGet, "/v1/runs?all=notabool", nil)
	rec := httptest.NewRecorder()
	s.handleListRuns(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid all query, got %d", rec.Code)
	}
}

func TestHandleWebhookInactiveSlotIsSkipped(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#7"
		issueNum = 16
		prNum    = 7
		baseRef  = "develop"
		headRef  = "rascal/task-7"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
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
	markRunSucceeded(t, s, seedRun.ID)

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
	if got.IssueNumber != issueNum {
		t.Fatalf("issue number = %d, want %d", got.IssueNumber, issueNum)
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
	task, ok := s.store.GetTask(taskID)
	if !ok {
		t.Fatalf("expected task %q", taskID)
	}
	if task.IssueNumber != issueNum {
		t.Fatalf("task issue number = %d, want %d", task.IssueNumber, issueNum)
	}
}

func TestHandleWebhookIssueCommentEditedUsesUpdatedContext(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#17"
		issueNum = 23
		prNum    = 17
		baseRef  = "main"
		headRef  = "rascal/pr-17"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
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
	markRunSucceeded(t, s, seedRun.ID)

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
	if got.IssueNumber != issueNum {
		t.Fatalf("issue number = %d, want %d", got.IssueNumber, issueNum)
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
	t.Parallel()
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

func TestHandleWebhookIssueCommentIgnoresUnmanagedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	payload := []byte(`{"action":"created","issue":{"number":44,"pull_request":{}},"comment":{"id":707,"body":"please fix this","user":{"login":"alice"}},"repository":{"full_name":"owner/repo"},"sender":{"login":"alice"}}`)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-unmanaged", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.store.ListRuns(10) {
		if run.Trigger == "pr_comment" {
			t.Fatalf("expected no pr_comment run for unmanaged pr")
		}
	}
	if got := fakeGH.postedIssueCommentReactions(); len(got) != 0 {
		t.Fatalf("expected no issue comment reactions for unmanaged pr, got %+v", got)
	}
}

func TestHandleWebhookPullRequestReviewUsesStateFallbackContext(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#11"
		issueNum = 31
		prNum    = 11
		baseRef  = "main"
		headRef  = "rascal/pr-11"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
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
	markRunSucceeded(t, s, seedRun.ID)

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
	if got.IssueNumber != issueNum {
		t.Fatalf("issue number = %d, want %d", got.IssueNumber, issueNum)
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

func TestHandleWebhookPullRequestReviewIgnoresUnmanagedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	payload := []byte(`{"action":"submitted","review":{"id":808,"body":"needs changes","state":"changes_requested","user":{"login":"bob"}},"pull_request":{"number":45},"repository":{"full_name":"owner/repo"},"sender":{"login":"bob"}}`)
	req := webhookRequest(t, payload, "pull_request_review", "delivery-review-unmanaged", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.store.ListRuns(10) {
		if run.Trigger == "pr_review" {
			t.Fatalf("expected no pr_review run for unmanaged pr")
		}
	}
	if got := fakeGH.postedPullRequestReviewReactions(); len(got) != 0 {
		t.Fatalf("expected no review reactions for unmanaged pr, got %+v", got)
	}
}

func TestHandleWebhookPullRequestReviewCommentIncludesInlineLocation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#12"
		issueNum = 44
		prNum    = 12
		baseRef  = "main"
		headRef  = "rascal/pr-12"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
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
	markRunSucceeded(t, s, seedRun.ID)

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
	if got.IssueNumber != issueNum {
		t.Fatalf("issue number = %d, want %d", got.IssueNumber, issueNum)
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

func TestHandleWebhookPullRequestReviewCommentEditedBodyChangedQueuesRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#13"
		issueNum = 45
		prNum    = 13
		baseRef  = "main"
		headRef  = "rascal/pr-13"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.store.AddRun(state.CreateRunInput{
		ID:         "seed_review_comment_edited",
		TaskID:     taskID,
		Repo:       repo,
		Task:       "seed",
		BaseBranch: baseRef,
		HeadBranch: headRef,
		Trigger:    "seed",
		RunDir:     filepath.Join(t.TempDir(), "seed_review_comment_edited"),
		PRNumber:   prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	markRunSucceeded(t, s, seedRun.ID)

	payload := []byte(`{"action":"edited","comment":{"id":505,"body":"Refined inline feedback","path":"cmd/rascald/main.go","line":600,"user":{"login":"eve"}},"changes":{"body":{"from":"Old inline feedback"}},"pull_request":{"number":13},"repository":{"full_name":"owner/repo"},"sender":{"login":"eve"}}`)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment-edited", "")
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
	}, "pr_review_comment run created for edited review comment")

	if got.Context != "Refined inline feedback\n\nInline comment location: cmd/rascald/main.go:600" {
		t.Fatalf("context = %q, want edited inline comment body with location", got.Context)
	}
	if got.IssueNumber != issueNum {
		t.Fatalf("issue number = %d, want %d", got.IssueNumber, issueNum)
	}
}

func TestHandleWebhookPullRequestReviewCommentEditedSkipsUnchangedBody(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	payload := []byte(`{"action":"edited","comment":{"id":506,"body":"  same inline feedback  ","path":"cmd/rascald/main.go","line":601,"user":{"login":"eve"}},"changes":{"body":{"from":"same inline feedback"}},"pull_request":{"number":13},"repository":{"full_name":"owner/repo"},"sender":{"login":"eve"}}`)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment-nochange", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.store.ListRuns(10) {
		if run.Trigger == "pr_review_comment" {
			t.Fatalf("expected no pr_review_comment run for unchanged edit")
		}
	}
}

func TestHandleWebhookPullRequestReviewCommentIgnoresUnmanagedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	payload := []byte(`{"action":"created","comment":{"id":909,"body":"Please rename this helper","path":"cmd/rascald/main.go","line":515,"user":{"login":"eve"}},"pull_request":{"number":46},"repository":{"full_name":"owner/repo"},"sender":{"login":"eve"}}`)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment-unmanaged", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.store.ListRuns(10) {
		if run.Trigger == "pr_review_comment" {
			t.Fatalf("expected no pr_review_comment run for unmanaged pr")
		}
	}
	if got := fakeGH.postedPullRequestReviewCommentReactions(); len(got) != 0 {
		t.Fatalf("expected no review comment reactions for unmanaged pr, got %+v", got)
	}
}

func TestHandleWebhookPullRequestReviewThreadUnresolvedQueuesRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#14"
		issueNum = 46
		prNum    = 14
		baseRef  = "main"
		headRef  = "rascal/pr-14"
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.store.AddRun(state.CreateRunInput{
		ID:         "seed_review_thread",
		TaskID:     taskID,
		Repo:       repo,
		Task:       "seed",
		BaseBranch: baseRef,
		HeadBranch: headRef,
		Trigger:    "seed",
		RunDir:     filepath.Join(t.TempDir(), "seed_review_thread"),
		PRNumber:   prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	markRunSucceeded(t, s, seedRun.ID)

	payload := []byte(`{"action":"unresolved","thread":{"id":12,"path":"cmd/rascald/main.go","line":777,"start_line":775,"comments":[{"id":1,"body":"Please split this logic","user":{"login":"eve"}}]},"pull_request":{"number":14},"repository":{"full_name":"owner/repo"},"sender":{"login":"eve"}}`)
	req := webhookRequest(t, payload, "pull_request_review_thread", "delivery-review-thread-unresolved", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.store.ListRuns(20)
		for _, run := range runs {
			if run.Trigger == "pr_review_thread" {
				got = run
				return true
			}
		}
		return false
	}, "pr_review_thread run created")

	if got.TaskID != taskID {
		t.Fatalf("task id = %q, want %q", got.TaskID, taskID)
	}
	if got.PRNumber != prNum {
		t.Fatalf("pr number = %d, want %d", got.PRNumber, prNum)
	}
	if got.IssueNumber != issueNum {
		t.Fatalf("issue number = %d, want %d", got.IssueNumber, issueNum)
	}
	if got.BaseBranch != baseRef {
		t.Fatalf("base branch = %q, want %q", got.BaseBranch, baseRef)
	}
	if got.HeadBranch != headRef {
		t.Fatalf("head branch = %q, want %q", got.HeadBranch, headRef)
	}
	wantContext := "Please split this logic\n\nThread location: cmd/rascald/main.go:775-777"
	if got.Context != wantContext {
		t.Fatalf("context = %q, want %q", got.Context, wantContext)
	}
}

func TestHandleWebhookPullRequestReviewThreadResolvedCancelsQueuedThreadRuns(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		repo   = "owner/repo"
		taskID = "owner/repo#15"
		prNum  = 15
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: 47, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	threadRun, err := s.store.AddRun(state.CreateRunInput{
		ID:       "queued_review_thread",
		TaskID:   taskID,
		Repo:     repo,
		Task:     "Address unresolved review thread",
		Trigger:  "pr_review_thread",
		RunDir:   filepath.Join(t.TempDir(), "queued_review_thread"),
		PRNumber: prNum,
	})
	if err != nil {
		t.Fatalf("seed thread run: %v", err)
	}
	if err := os.MkdirAll(threadRun.RunDir, 0o755); err != nil {
		t.Fatalf("create thread run dir: %v", err)
	}
	if err := s.writeRunResponseTarget(threadRun, &runResponseTarget{
		Repo:           repo,
		IssueNumber:    prNum,
		Trigger:        "pr_review_thread",
		ReviewThreadID: 13,
	}); err != nil {
		t.Fatalf("write thread run target: %v", err)
	}
	otherThreadRun, err := s.store.AddRun(state.CreateRunInput{
		ID:       "queued_review_thread_other",
		TaskID:   taskID,
		Repo:     repo,
		Task:     "Address another unresolved review thread",
		Trigger:  "pr_review_thread",
		RunDir:   filepath.Join(t.TempDir(), "queued_review_thread_other"),
		PRNumber: prNum,
	})
	if err != nil {
		t.Fatalf("seed other thread run: %v", err)
	}
	if err := os.MkdirAll(otherThreadRun.RunDir, 0o755); err != nil {
		t.Fatalf("create other thread run dir: %v", err)
	}
	if err := s.writeRunResponseTarget(otherThreadRun, &runResponseTarget{
		Repo:           repo,
		IssueNumber:    prNum,
		Trigger:        "pr_review_thread",
		ReviewThreadID: 99,
	}); err != nil {
		t.Fatalf("write other thread run target: %v", err)
	}
	otherRun, err := s.store.AddRun(state.CreateRunInput{
		ID:       "queued_pr_comment",
		TaskID:   taskID,
		Repo:     repo,
		Task:     "Address PR feedback",
		Trigger:  "pr_comment",
		RunDir:   filepath.Join(t.TempDir(), "queued_pr_comment"),
		PRNumber: prNum,
	})
	if err != nil {
		t.Fatalf("seed comment run: %v", err)
	}

	payload := []byte(`{"action":"resolved","thread":{"id":13,"path":"cmd/rascald/main.go","line":800},"pull_request":{"number":15},"repository":{"full_name":"owner/repo"},"sender":{"login":"eve"}}`)
	req := webhookRequest(t, payload, "pull_request_review_thread", "delivery-review-thread-resolved", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	updatedThreadRun, ok := s.store.GetRun(threadRun.ID)
	if !ok {
		t.Fatalf("missing run %s", threadRun.ID)
	}
	if updatedThreadRun.Status != state.StatusCanceled {
		t.Fatalf("thread run status = %s, want %s", updatedThreadRun.Status, state.StatusCanceled)
	}

	updatedOtherThreadRun, ok := s.store.GetRun(otherThreadRun.ID)
	if !ok {
		t.Fatalf("missing run %s", otherThreadRun.ID)
	}
	if updatedOtherThreadRun.Status != state.StatusQueued {
		t.Fatalf("non-matching thread run status = %s, want %s", updatedOtherThreadRun.Status, state.StatusQueued)
	}

	updatedOtherRun, ok := s.store.GetRun(otherRun.ID)
	if !ok {
		t.Fatalf("missing run %s", otherRun.ID)
	}
	if updatedOtherRun.Status != state.StatusQueued {
		t.Fatalf("non-thread run status = %s, want %s", updatedOtherRun.Status, state.StatusQueued)
	}
}

func TestCreateAndQueueRunWritesResponseTarget(t *testing.T) {
	t.Parallel()
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

	target, ok := s.store.GetRunResponseTarget(run.ID)
	if !ok {
		t.Fatal("expected sqlite-backed run response target")
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
	if target.ReviewThreadID != 0 {
		t.Fatalf("target review_thread_id = %d, want 0", target.ReviewThreadID)
	}
	if _, err := os.Stat(filepath.Join(run.RunDir, runResponseTargetFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no response target sidecar for new runs, stat err=%v", err)
	}
}

func TestHandleWebhookPullRequestReviewThreadResolvedFallsBackToLegacyResponseTarget(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	const (
		taskID = "owner/repo#16"
		repo   = "owner/repo"
		prNum  = 16
	)
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: 47, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	threadRun, err := s.store.AddRun(state.CreateRunInput{
		ID:       "queued_review_thread_legacy",
		TaskID:   taskID,
		Repo:     repo,
		Task:     "Address unresolved review thread",
		Trigger:  "pr_review_thread",
		RunDir:   filepath.Join(t.TempDir(), "queued_review_thread_legacy"),
		PRNumber: prNum,
	})
	if err != nil {
		t.Fatalf("seed legacy thread run: %v", err)
	}
	if err := os.MkdirAll(threadRun.RunDir, 0o755); err != nil {
		t.Fatalf("create legacy thread run dir: %v", err)
	}
	if err := writeLegacyRunResponseTarget(threadRun.RunDir, runResponseTarget{
		Repo:           repo,
		IssueNumber:    prNum,
		Trigger:        "pr_review_thread",
		ReviewThreadID: 13,
	}); err != nil {
		t.Fatalf("write legacy thread run target: %v", err)
	}

	payload := []byte(`{"action":"resolved","thread":{"id":13,"path":"cmd/rascald/main.go","line":800},"pull_request":{"number":16},"repository":{"full_name":"owner/repo"},"sender":{"login":"eve"}}`)
	req := webhookRequest(t, payload, "pull_request_review_thread", "delivery-review-thread-resolved-legacy", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	updatedThreadRun, ok := s.store.GetRun(threadRun.ID)
	if !ok {
		t.Fatalf("missing run %s", threadRun.ID)
	}
	if updatedThreadRun.Status != state.StatusCanceled {
		t.Fatalf("legacy thread run status = %s, want %s", updatedThreadRun.Status, state.StatusCanceled)
	}
}

func TestCreateAndQueueRunWritesReviewThreadResponseTarget(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{
		TaskID:   "owner/repo#100",
		Repo:     "owner/repo",
		Task:     "Address PR #100 unresolved review thread",
		Trigger:  "pr_review_thread",
		PRNumber: 100,
		ResponseTarget: &runResponseTarget{
			RequestedBy:    " bob ",
			ReviewThreadID: 42,
		},
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	target, ok := s.store.GetRunResponseTarget(run.ID)
	if !ok {
		t.Fatal("expected sqlite-backed run response target")
	}
	if target.ReviewThreadID != 42 {
		t.Fatalf("target review_thread_id = %d, want 42", target.ReviewThreadID)
	}
	if target.RequestedBy != "bob" {
		t.Fatalf("target requested_by = %q, want bob", target.RequestedBy)
	}
	if _, err := os.Stat(filepath.Join(run.RunDir, runResponseTargetFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no response target sidecar for new runs, stat err=%v", err)
	}
}

func TestLoadRunResponseTargetFallsBackToLegacyFile(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_legacy_target",
		TaskID:     "owner/repo#101",
		Repo:       "owner/repo",
		Task:       "Address PR #101 feedback",
		BaseBranch: "main",
		HeadBranch: "rascal/pr-101",
		Trigger:    "pr_comment",
		RunDir:     t.TempDir(),
		PRNumber:   101,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := writeLegacyRunResponseTarget(run.RunDir, runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 101,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	}); err != nil {
		t.Fatalf("write legacy response target: %v", err)
	}

	target, ok, err := s.loadRunResponseTarget(run)
	if err != nil {
		t.Fatalf("load run response target: %v", err)
	}
	if !ok {
		t.Fatal("expected legacy response target fallback")
	}
	if target.RequestedBy != "alice" {
		t.Fatalf("target requested_by = %q, want alice", target.RequestedBy)
	}
	if target.IssueNumber != 101 {
		t.Fatalf("target issue_number = %d, want 101", target.IssueNumber)
	}
}

func TestCreateAndQueueRunDoesNotCreateRunDirWhenEnqueueFails(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})

	runsDir := filepath.Join(s.cfg.DataDir, "runs")
	if err := s.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	_, err := s.createAndQueueRun(runRequest{
		TaskID: "owner/repo#101",
		Repo:   "owner/repo",
		Task:   "fail before enqueue persists",
	})
	if err == nil {
		t.Fatal("expected enqueue failure")
	}
	if !strings.Contains(err.Error(), "upsert task") {
		t.Fatalf("unexpected enqueue error: %v", err)
	}

	_, statErr := os.Stat(runsDir)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no runs dir after failed enqueue, got err=%v", statErr)
	}
}

func TestHandleWebhookIssueCommentIgnoresBotActor(t *testing.T) {
	t.Parallel()
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

func TestHandleWebhookIssueCommentIgnoresRascalAutomationComment(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	payload := []byte(`{"action":"created","issue":{"number":9,"pull_request":{}},"comment":{"id":502,"body":"<!-- rascal:completion-comment -->\n\nRascal run ` + "`run_123`" + ` completed in 12s.","user":{"login":"rascal"}},"repository":{"full_name":"owner/repo"},"sender":{"login":"rascal"}}`)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-automation", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if got := len(s.store.ListRuns(10)); got != 0 {
		t.Fatalf("expected zero runs for rascal automation comment, got %d", got)
	}
}

func TestCreateAndQueueRunSerializesPerTask(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
	defer close(waitCh)
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
	markRunReview(t, s, awaitingRun.ID)

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

}

func TestMergedPRMatchesRepositoryCaseInsensitively(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	taskID := "owner/repo#123"
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 55}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_case_insensitive_merge",
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
	markRunReview(t, s, run.ID)

	payload := []byte(`{"action":"closed","pull_request":{"number":55,"merged":true},"repository":{"full_name":"Owner/Repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "pull_request", "delivery-merged-mixed-case", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for mixed-case merged pr event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return s.store.IsTaskCompleted(taskID) }, "task marked completed")
	waitFor(t, time.Second, func() bool {
		updated, ok := s.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "awaiting-feedback run marked succeeded on merge")
}

func TestPullRequestClosedIgnoresUnmanagedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	payload := []byte(`{"action":"closed","pull_request":{"number":456,"merged":true},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "pull_request", "delivery-merged-unmanaged", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for unmanaged pr event, got %d", rec.Code)
	}

	if _, ok := s.store.FindTaskByPR("owner/repo", 456); ok {
		t.Fatal("expected no task to be created for unmanaged pr")
	}
	if got := fakeGH.postedReactions(); len(got) != 0 {
		t.Fatalf("expected no issue reactions for unmanaged pr, got %+v", got)
	}
}

func TestClosedUnmergedPRCancelsAwaitingFeedbackRuns(t *testing.T) {
	t.Parallel()
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
	markRunReview(t, s, run.ID)

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

func TestClosedUnmergedEventDoesNotDowngradeMergedRunState(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	taskID := "owner/repo#321"
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 321}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_merged_guard",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Task:        "already merged",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 321,
		PRNumber:    321,
		PRStatus:    state.PRStatusMerged,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	markRunSucceeded(t, s, run.ID)
	if _, err := s.store.UpdateRun(run.ID, func(r *state.Run) error {
		r.PRStatus = state.PRStatusMerged
		return nil
	}); err != nil {
		t.Fatalf("set merged pr status: %v", err)
	}

	payload := []byte(`{"action":"closed","pull_request":{"number":321,"merged":false},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "pull_request", "delivery-stale-closed", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for stale closed event, got %d", rec.Code)
	}

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if updated.Status != state.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", updated.Status)
	}
	if updated.PRStatus != state.PRStatusMerged {
		t.Fatalf("pr status = %s, want merged", updated.PRStatus)
	}
}

func TestReopenedEventDoesNotDowngradeMergedRunState(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	taskID := "owner/repo#654"
	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 654}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_reopened_guard",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Task:        "already merged",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 654,
		PRNumber:    654,
		PRStatus:    state.PRStatusMerged,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	markRunSucceeded(t, s, run.ID)
	if _, err := s.store.UpdateRun(run.ID, func(r *state.Run) error {
		r.PRStatus = state.PRStatusMerged
		return nil
	}); err != nil {
		t.Fatalf("set merged pr status: %v", err)
	}

	payload := []byte(`{"action":"reopened","pull_request":{"number":654},"repository":{"full_name":"owner/repo"},"sender":{"login":"dev"}}`)
	req := webhookRequest(t, payload, "pull_request", "delivery-stale-reopened", "")
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for stale reopened event, got %d", rec.Code)
	}

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if updated.Status != state.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", updated.Status)
	}
	if updated.PRStatus != state.PRStatusMerged {
		t.Fatalf("pr status = %s, want merged", updated.PRStatus)
	}
}

func TestExecuteRunRetriesLauncherFailure(t *testing.T) {
	t.Parallel()
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

func TestExecuteRunSetsGooseSessionSpecForPROnlyCommentTrigger(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	s.cfg.AgentBackend = agent.BackendGoose
	sessionRoot := filepath.Join(t.TempDir(), "goose-sessions")
	s.cfg.GooseSessionMode = "pr-only"
	s.cfg.GooseSessionRoot = sessionRoot
	s.cfg.GooseSessionTTLDays = 0

	run, err := s.createAndQueueRun(runRequest{
		TaskID:  "owner/repo#123",
		Repo:    "owner/repo",
		Task:    "Address PR #123 feedback",
		Trigger: "pr_comment",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		r, ok := s.store.GetRun(run.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "run completion")

	if launcher.Calls() != 1 {
		t.Fatalf("expected 1 launcher call, got %d", launcher.Calls())
	}
	spec := launcher.specs[0]
	if !spec.GooseSessionResume {
		t.Fatal("expected GooseSessionResume=true for pr-only comment trigger")
	}
	if spec.GooseSessionTaskKey == "" {
		t.Fatal("expected GooseSessionTaskKey to be populated")
	}
	if spec.GooseSessionName == "" {
		t.Fatal("expected GooseSessionName to be populated")
	}
	if !strings.HasPrefix(spec.GooseSessionTaskDir, sessionRoot+string(os.PathSeparator)) {
		t.Fatalf("unexpected GooseSessionTaskDir %q (root %q)", spec.GooseSessionTaskDir, sessionRoot)
	}
}

func TestExecuteRunDisablesGooseSessionSpecForNonPROnlyTrigger(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	s.cfg.AgentBackend = agent.BackendGoose
	s.cfg.GooseSessionMode = "pr-only"
	s.cfg.GooseSessionRoot = filepath.Join(t.TempDir(), "goose-sessions")
	s.cfg.GooseSessionTTLDays = 0

	run, err := s.createAndQueueRun(runRequest{
		TaskID:  "owner/repo#124",
		Repo:    "owner/repo",
		Task:    "Initial issue run",
		Trigger: "issue_label",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		r, ok := s.store.GetRun(run.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "run completion")

	if launcher.Calls() != 1 {
		t.Fatalf("expected 1 launcher call, got %d", launcher.Calls())
	}
	spec := launcher.specs[0]
	if spec.GooseSessionResume {
		t.Fatal("expected GooseSessionResume=false for non PR-only trigger")
	}
	if spec.GooseSessionTaskDir != "" || spec.GooseSessionTaskKey != "" || spec.GooseSessionName != "" {
		t.Fatalf("expected empty goose session fields when resume disabled, got dir=%q key=%q name=%q", spec.GooseSessionTaskDir, spec.GooseSessionTaskKey, spec.GooseSessionName)
	}
}

func TestCleanupStaleGooseSessionDirs(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "goose-sessions")
	oldDir := filepath.Join(root, "old")
	freshDir := filepath.Join(root, "fresh")
	for _, dir := range []string{oldDir, freshDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	now := time.Now().UTC()
	if err := os.Chtimes(oldDir, now.AddDate(0, 0, -30), now.AddDate(0, 0, -30)); err != nil {
		t.Fatalf("chtimes old dir: %v", err)
	}
	if err := os.Chtimes(freshDir, now.AddDate(0, 0, -2), now.AddDate(0, 0, -2)); err != nil {
		t.Fatalf("chtimes fresh dir: %v", err)
	}

	removed, err := cleanupStaleGooseSessionDirs(root, 14, now)
	if err != nil {
		t.Fatalf("cleanupStaleGooseSessionDirs: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("expected old dir removed, stat err=%v", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("expected fresh dir to remain: %v", err)
	}
}

func TestExecuteRunPostsCompletionCommentForCommentTriggeredRun(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{
		res: fakeRunResult{
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
		ID:          "run_comment_completion",
		TaskID:      "owner/repo#77",
		Repo:        "owner/repo",
		Task:        "Address PR #77 feedback",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-77",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 16,
		PRNumber:    77,
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
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(`{"event":"x","usage":{"total_tokens":123000}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	s.executeRun(run.ID)

	comments := fakeGH.postedComments()
	if len(comments) != 2 {
		t.Fatalf("expected start and completion comments, got %d", len(comments))
	}
	startComment, ok := findPostedComment(comments, runStartCommentBodyMarker)
	if !ok {
		t.Fatalf("expected start comment, got %+v", comments)
	}
	if startComment.repo != "owner/repo" || startComment.issueNumber != 77 {
		t.Fatalf("unexpected start comment target: %+v", startComment)
	}
	if !strings.Contains(startComment.body, "Rascal started run `run_comment_completion` to address new PR feedback.") {
		t.Fatalf("expected concise start summary, got:\n%s", startComment.body)
	}
	if !strings.Contains(startComment.body, "<details><summary>Run Settings</summary>") {
		t.Fatalf("expected settings details in start comment, got:\n%s", startComment.body)
	}
	completionComment, ok := findPostedComment(comments, runCompletionCommentBodyMarker)
	if !ok {
		t.Fatalf("expected completion comment, got %+v", comments)
	}
	if !strings.Contains(completionComment.body, "@alice implemented in commit [`0123456789ab`]") {
		t.Fatalf("expected requester mention with short sha, got body:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "Closes #16") {
		t.Fatalf("expected original issue reference, got body:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "- updated handlers") {
		t.Fatalf("expected commit body bullets in comment, got:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "<details><summary>Agent Details</summary>") {
		t.Fatalf("expected agent details section, got:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "Rascal run `run_comment_completion` completed in ") || !strings.Contains(completionComment.body, "123K tokens") {
		t.Fatalf("expected runtime and token summary, got:\n%s", completionComment.body)
	}
	usage, ok := s.store.GetRunTokenUsage(run.ID)
	if !ok {
		t.Fatalf("expected persisted token usage for %s", run.ID)
	}
	if usage.TotalTokens != 123000 {
		t.Fatalf("total_tokens = %d, want 123000", usage.TotalTokens)
	}
}

func TestExecuteRunPersistsStructuredRunTokenUsage(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{
		res: fakeRunResult{
			HeadSHA: "0123456789abcdef0123456789abcdef01234567",
		},
	}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_token_usage",
		TaskID:     "owner/repo#88",
		Repo:       "owner/repo",
		Task:       "Capture token usage",
		BaseBranch: "main",
		HeadBranch: "rascal/pr-88",
		Trigger:    "issue_label",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	logBody := `{"type":"turn.completed","model":"gpt-5-codex","usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":40},"output_tokens":30,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":150}}`
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(logBody+"\n"), 0o644); err != nil {
		t.Fatalf("write agent log: %v", err)
	}

	s.executeRun(run.ID)

	usage, ok := s.store.GetRunTokenUsage(run.ID)
	if !ok {
		t.Fatalf("expected run token usage for %s", run.ID)
	}
	if usage.Backend != "codex" {
		t.Fatalf("backend = %q, want codex", usage.Backend)
	}
	if usage.Model != "gpt-5-codex" {
		t.Fatalf("model = %q, want gpt-5-codex", usage.Model)
	}
	if usage.TotalTokens != 150 {
		t.Fatalf("total_tokens = %d, want 150", usage.TotalTokens)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 120 {
		t.Fatalf("input_tokens = %v, want 120", usage.InputTokens)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 30 {
		t.Fatalf("output_tokens = %v, want 30", usage.OutputTokens)
	}
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 40 {
		t.Fatalf("cached_input_tokens = %v, want 40", usage.CachedInputTokens)
	}
	if usage.ReasoningOutputTokens == nil || *usage.ReasoningOutputTokens != 10 {
		t.Fatalf("reasoning_output_tokens = %v, want 10", usage.ReasoningOutputTokens)
	}
	if !strings.Contains(usage.RawUsageJSON, `"reasoning_tokens":10`) {
		t.Fatalf("expected raw usage json, got %q", usage.RawUsageJSON)
	}
}

func TestExecuteRunPostsDetailsWithoutCommitClaimWhenCommitMessageMissing(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{
		res: fakeRunResult{
			PRNumber: 52,
			PRURL:    "https://example.com/pr/52",
			HeadSHA:  "0109106ceba61adf1735bc980f83c15506b8da7a",
		},
	}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_comment_no_commit",
		TaskID:      "owner/repo#16",
		Repo:        "owner/repo",
		Task:        "Address PR #52 feedback",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-52",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 16,
		PRNumber:    52,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 52,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(`{"type":"message","message":{"content":[{"type":"text","text":"Request failed"}]}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	s.executeRun(run.ID)

	comments := fakeGH.postedComments()
	if len(comments) != 2 {
		t.Fatalf("expected start and completion comments, got %d", len(comments))
	}
	startComment, ok := findPostedComment(comments, runStartCommentBodyMarker)
	if !ok {
		t.Fatalf("expected start comment, got %+v", comments)
	}
	if startComment.issueNumber != 52 {
		t.Fatalf("start comment target issue number = %d, want 52", startComment.issueNumber)
	}
	completionComment, ok := findPostedComment(comments, runCompletionCommentBodyMarker)
	if !ok {
		t.Fatalf("expected completion comment, got %+v", comments)
	}
	if strings.Contains(completionComment.body, "implemented in commit") {
		t.Fatalf("did not expect commit claim without commit message, got body:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "@alice posted the run details below.") {
		t.Fatalf("expected neutral requester summary, got body:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "Closes #16") {
		t.Fatalf("expected original issue reference, got body:\n%s", completionComment.body)
	}
}

func TestExecuteRunRequeuesRunForGooseUsageLimit(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{
		res: fakeRunResult{
			ExitCode: 1,
			Error:    "goose run failed: exit status 1",
		},
	}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_comment_usage_limit",
		TaskID:      "owner/repo#53",
		Repo:        "owner/repo",
		Task:        "Address PR #53 feedback",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-53",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 16,
		PRNumber:    53,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 53,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}
	gooseLog := `{"type":"message","message":{"id":null,"role":"assistant","created":1772899608,"content":[{"type":"text","text":"Ran into this error: Request failed: Codex CLI error: You've hit your usage limit. To get more access now, send a request to your admin or try again at Mar 10th, 2099 6:31 AM..\n\nPlease retry if you think this is a transient or recoverable error."}],"metadata":{"userVisible":true,"agentVisible":true}}}`
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte(gooseLog+"\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	s.executeRun(run.ID)

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusQueued {
		t.Fatalf("expected queued status after usage limit, got %s", updated.Status)
	}
	if updated.StartedAt != nil {
		t.Fatalf("expected started_at cleared on requeue")
	}
	if updated.CompletedAt != nil {
		t.Fatalf("expected completed_at cleared on requeue")
	}
	comments := fakeGH.postedComments()
	if len(comments) != 1 {
		t.Fatalf("expected only the start comment while run is paused for retry, got %d", len(comments))
	}
	if _, ok := findPostedComment(comments, runStartCommentBodyMarker); !ok {
		t.Fatalf("expected start comment while paused for retry, got %+v", comments)
	}
	if calls := launcher.Calls(); calls != 1 {
		t.Fatalf("expected run not to restart before pause expiry, got launcher calls=%d", calls)
	}
	if _, err := os.Stat(runFailureCommentMarkerPath(run.RunDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no failure marker, got err=%v", err)
	}
	if pauseUntil, reason, ok, err := s.store.ActiveSchedulerPause(workerPauseScope, time.Now().UTC()); err != nil {
		t.Fatalf("load scheduler pause: %v", err)
	} else if !ok {
		t.Fatal("expected active scheduler pause after usage limit")
	} else {
		if !pauseUntil.After(time.Now().UTC()) {
			t.Fatalf("expected future pause deadline, got %s", pauseUntil)
		}
		if !strings.Contains(reason, "usage limit") {
			t.Fatalf("expected usage-limit pause reason, got %q", reason)
		}
	}
}

func TestExecuteRunRequeuesIssueTriggeredRunForUsageLimit(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{
		res: fakeRunResult{
			ExitCode: 1,
			Error:    "goose run failed: exit status 1",
		},
	}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_issue_usage_limit",
		TaskID:      "owner/repo#16",
		Repo:        "owner/repo",
		Task:        "Investigate issue #16",
		BaseBranch:  "main",
		HeadBranch:  "rascal/issue-16",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 16,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 16,
		RequestedBy: "alice",
		Trigger:     "issue_label",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}
	gooseLog := `{"type":"message","message":{"content":[{"type":"text","text":"Request failed: Codex CLI error: You've hit your usage limit. Try again at Mar 10th, 2099 6:31 AM."}]}}`
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte(gooseLog+"\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	s.executeRun(run.ID)

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusQueued {
		t.Fatalf("expected queued status after usage limit, got %s", updated.Status)
	}
	comments := fakeGH.postedComments()
	if len(comments) != 1 {
		t.Fatalf("expected only the start comment while run is paused for retry, got %d", len(comments))
	}
	if _, ok := findPostedComment(comments, runStartCommentBodyMarker); !ok {
		t.Fatalf("expected start comment while paused for retry, got %+v", comments)
	}
	if calls := launcher.Calls(); calls != 1 {
		t.Fatalf("expected run not to restart before pause expiry, got launcher calls=%d", calls)
	}
}

func TestExecuteRunDoesNotRequeueSuccessfulRunWhenTranscriptMentionsUsageLimit(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{
		res: fakeRunResult{
			PRNumber: 124,
			PRURL:    "https://github.com/owner/repo/pull/124",
			HeadSHA:  "abc123",
			ExitCode: 0,
		},
	}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_success_usage_limit_false_positive",
		TaskID:      "owner/repo#54",
		Repo:        "owner/repo",
		Task:        "Implement review thread webhook handling",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-54",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 54,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent_output.txt"), []byte("Implemented the requested change and opened a pull request."), 0o644); err != nil {
		t.Fatalf("write agent output: %v", err)
	}
	transcript := `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"cmd/rascald/main_test.go:2294: gooseLog := Request failed: Codex CLI error: You've hit your usage limit. Try again at Mar 10th, 2099 6:31 AM."}}`
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(transcript+"\n"), 0o644); err != nil {
		t.Fatalf("write agent transcript: %v", err)
	}

	s.executeRun(run.ID)

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusReview {
		t.Fatalf("expected review status after successful run, got %s", updated.Status)
	}
	if pauseUntil, reason, ok, err := s.store.ActiveSchedulerPause(workerPauseScope, time.Now().UTC()); err != nil {
		t.Fatalf("load scheduler pause: %v", err)
	} else if ok {
		t.Fatalf("did not expect scheduler pause, got until=%s reason=%q", pauseUntil, reason)
	}
}

func TestExecuteRunPostsTerminalFailureFeedbackForCredentialLeaseFailure(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"
	s.broker = credentials.NewBroker(nil, nil, nil, 0)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_credential_failure_feedback",
		TaskID:      "owner/repo#42",
		Repo:        "owner/repo",
		Task:        "Investigate issue #42",
		BaseBranch:  "main",
		HeadBranch:  "rascal/issue-42",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 42,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	s.executeRun(run.ID)

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	if !strings.Contains(updated.Error, "acquire credential lease: no credentials available") {
		t.Fatalf("unexpected error: %q", updated.Error)
	}
	if calls := launcher.Calls(); calls != 0 {
		t.Fatalf("expected launcher not to start, got calls=%d", calls)
	}

	reactions := fakeGH.postedReactions()
	if len(reactions) != 2 {
		t.Fatalf("expected eyes and confused reactions, got %+v", reactions)
	}
	if reactions[0].issueNumber != 42 || reactions[0].content != ghapi.ReactionEyes {
		t.Fatalf("expected first reaction to be eyes for issue 42, got %+v", reactions[0])
	}
	if reactions[1].issueNumber != 42 || reactions[1].content != ghapi.ReactionConfused {
		t.Fatalf("expected second reaction to be confused for issue 42, got %+v", reactions[1])
	}

	comments := fakeGH.postedComments()
	if len(comments) != 1 {
		t.Fatalf("expected one failure comment, got %d", len(comments))
	}
	if comments[0].repo != "owner/repo" || comments[0].issueNumber != 42 {
		t.Fatalf("unexpected failure comment target: %+v", comments[0])
	}
	if !strings.Contains(comments[0].body, "Reason: acquire credential lease: no credentials available") {
		t.Fatalf("expected failure reason in comment, got body:\n%s", comments[0].body)
	}
}

func TestScheduleRunsResumesAfterPauseDeadline(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	pauseUntil := time.Now().UTC().Add(150 * time.Millisecond)
	if _, err := s.store.PauseScheduler(workerPauseScope, "test pause", pauseUntil); err != nil {
		t.Fatalf("pause scheduler: %v", err)
	}

	if _, err := s.createAndQueueRun(runRequest{
		TaskID: "owner/repo#resume",
		Repo:   "owner/repo",
		Task:   "resume after pause",
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	if calls := launcher.Calls(); calls != 0 {
		t.Fatalf("expected scheduler to stay paused before deadline, got calls=%d", calls)
	}

	waitFor(t, 2*time.Second, func() bool { return launcher.Calls() == 1 }, "scheduler resume after pause deadline")
}

func TestParseUsageLimitRetryAtSupportsAbsoluteTimestampWithZone(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.March, 8, 12, 0, 0, 0, time.UTC)
	corpus := "Request failed: You've hit your usage limit. Try again at Mar 10th, 2026 6:31 AM UTC."

	retryAt, reason := parseUsageLimitRetryAt(corpus, now)

	expected := time.Date(2026, time.March, 10, 6, 31, 0, 0, time.UTC)
	if !retryAt.Equal(expected) {
		t.Fatalf("retryAt = %s, want %s", retryAt, expected)
	}
	if !strings.Contains(reason, "Mar 10, 2026 6:31 AM UTC") {
		t.Fatalf("unexpected reason %q", reason)
	}
}

func TestParseUsageLimitRetryAtSupportsRFC3339(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.March, 8, 12, 0, 0, 0, time.UTC)
	corpus := "You've hit your usage limit. Try again at 2026-03-10T06:31:00Z."

	retryAt, _ := parseUsageLimitRetryAt(corpus, now)

	expected := time.Date(2026, time.March, 10, 6, 31, 0, 0, time.UTC)
	if !retryAt.Equal(expected) {
		t.Fatalf("retryAt = %s, want %s", retryAt, expected)
	}
}

func TestParseUsageLimitRetryAtSupportsRelativeDelay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.March, 8, 12, 0, 0, 0, time.UTC)
	corpus := "You've hit your usage limit. Please try again in 2 hours 15 minutes."

	retryAt, reason := parseUsageLimitRetryAt(corpus, now)

	expected := now.Add(2*time.Hour + 15*time.Minute)
	if !retryAt.Equal(expected) {
		t.Fatalf("retryAt = %s, want %s", retryAt, expected)
	}
	if !strings.Contains(reason, "2 hours 15 minutes") {
		t.Fatalf("unexpected reason %q", reason)
	}
}

func TestPostRunStartCommentSkipsDuplicateWhenMarkerExists(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_start_comment_dedupe",
		TaskID:      "owner/repo#87",
		Repo:        "owner/repo",
		Task:        "Investigate issue #87",
		BaseBranch:  "main",
		HeadBranch:  "rascal/issue-87",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 87,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	now := time.Now().UTC()
	run.StartedAt = &now
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 87,
		RequestedBy: "alice",
		Trigger:     "issue_label",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}

	s.postRunStartCommentBestEffort(run, agent.SessionModeAll, true)
	s.postRunStartCommentBestEffort(run, agent.SessionModeAll, true)

	if calls := fakeGH.createCommentCalls(); calls != 1 {
		t.Fatalf("expected a single github comment call, got %d", calls)
	}
	comments := fakeGH.postedComments()
	if len(comments) != 1 {
		t.Fatalf("expected one posted start comment, got %d", len(comments))
	}
	if !strings.Contains(comments[0].body, runStartCommentBodyMarker) {
		t.Fatalf("expected start marker in comment body, got:\n%s", comments[0].body)
	}
	markerPath := runStartCommentMarkerPath(run.RunDir)
	markerData, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read start marker: %v", err)
	}
	var marker runCommentMarker
	if err := json.Unmarshal(markerData, &marker); err != nil {
		t.Fatalf("decode start marker: %v", err)
	}
	if marker.RunID != run.ID {
		t.Fatalf("marker run_id = %q, want %q", marker.RunID, run.ID)
	}
}

func TestPostRunStartCommentRetriesAfterPostFailure(t *testing.T) {
	t.Parallel()
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
		ID:          "run_start_comment_retry",
		TaskID:      "owner/repo#86",
		Repo:        "owner/repo",
		Task:        "Investigate issue #86",
		BaseBranch:  "main",
		HeadBranch:  "rascal/issue-86",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 86,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	now := time.Now().UTC()
	run.StartedAt = &now
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 86,
		RequestedBy: "alice",
		Trigger:     "issue_label",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}

	s.postRunStartCommentBestEffort(run, agent.SessionModeAll, false)

	if calls := fakeGH.createCommentCalls(); calls != 1 {
		t.Fatalf("expected one github comment call after first attempt, got %d", calls)
	}
	if comments := fakeGH.postedComments(); len(comments) != 0 {
		t.Fatalf("expected no posted comments after failed attempt, got %d", len(comments))
	}
	if _, err := os.Stat(runStartCommentMarkerPath(run.RunDir)); !os.IsNotExist(err) {
		t.Fatalf("expected start marker to be absent after failed post, stat err=%v", err)
	}

	s.postRunStartCommentBestEffort(run, agent.SessionModeAll, false)

	if calls := fakeGH.createCommentCalls(); calls != 2 {
		t.Fatalf("expected second github comment call on retry, got %d", calls)
	}
	if comments := fakeGH.postedComments(); len(comments) != 1 {
		t.Fatalf("expected one posted start comment after retry, got %d", len(comments))
	}
	if _, err := os.Stat(runStartCommentMarkerPath(run.RunDir)); err != nil {
		t.Fatalf("expected start marker after successful retry: %v", err)
	}
}

func TestPostRunStartCommentIncludesRunnerBuildCommit(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	fakeGH := &fakeGitHubClient{}
	s.gh = fakeGH
	s.cfg.GitHubToken = "token"

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          "run_start_comment_commit",
		TaskID:      "owner/repo#85",
		Repo:        "owner/repo",
		Task:        "Investigate issue #85",
		BaseBranch:  "main",
		HeadBranch:  "rascal/issue-85",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 85,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	now := time.Now().UTC()
	run.StartedAt = &now
	if err := runner.WriteMeta(filepath.Join(run.RunDir, "meta.json"), runner.Meta{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		BaseBranch:  run.BaseBranch,
		HeadBranch:  run.HeadBranch,
		BuildCommit: "deadbee",
		ExitCode:    1,
	}); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 85,
		RequestedBy: "alice",
		Trigger:     "issue_label",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}

	s.postRunStartCommentBestEffort(run, agent.SessionModeAll, false)

	comments := fakeGH.postedComments()
	if len(comments) != 1 {
		t.Fatalf("expected one posted start comment, got %d", len(comments))
	}
	if !strings.Contains(comments[0].body, "- Runner commit: `deadbee`") {
		t.Fatalf("expected runner commit in start comment, got:\n%s", comments[0].body)
	}
}

func TestPostRunCompletionCommentSkipsDuplicateWhenMarkerExists(t *testing.T) {
	t.Parallel()
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
	var marker runCommentMarker
	if err := json.Unmarshal(markerData, &marker); err != nil {
		t.Fatalf("decode completion marker: %v", err)
	}
	if marker.RunID != run.ID {
		t.Fatalf("marker run_id = %q, want %q", marker.RunID, run.ID)
	}
}

func TestPostRunCompletionCommentRetriesAfterPostFailure(t *testing.T) {
	t.Parallel()
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

func TestPostRunFailureCommentRetriesAfterPostFailure(t *testing.T) {
	t.Parallel()
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
		ID:         "run_failure_retry",
		TaskID:     "owner/repo#90",
		Repo:       "owner/repo",
		Task:       "Address PR #90 feedback",
		BaseBranch: "main",
		HeadBranch: "rascal/pr-90",
		Trigger:    "pr_comment",
		RunDir:     t.TempDir(),
		PRNumber:   90,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	run.Error = "goose run failed: exit status 1"
	if err := s.writeRunResponseTarget(run, &runResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 90,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	}); err != nil {
		t.Fatalf("write response target: %v", err)
	}

	s.postRunFailureCommentBestEffort(run)

	if calls := fakeGH.createCommentCalls(); calls != 1 {
		t.Fatalf("expected one github comment call after first attempt, got %d", calls)
	}
	if comments := fakeGH.postedComments(); len(comments) != 0 {
		t.Fatalf("expected no posted comments after failed attempt, got %d", len(comments))
	}
	if _, err := os.Stat(runFailureCommentMarkerPath(run.RunDir)); !os.IsNotExist(err) {
		t.Fatalf("expected failure marker to be absent after failed post, stat err=%v", err)
	}

	s.postRunFailureCommentBestEffort(run)

	if calls := fakeGH.createCommentCalls(); calls != 2 {
		t.Fatalf("expected second github comment call on retry, got %d", calls)
	}
	comments := fakeGH.postedComments()
	if len(comments) != 1 {
		t.Fatalf("expected one posted comment after retry, got %d", len(comments))
	}
	if !strings.Contains(comments[0].body, "Reason: goose run failed: exit status 1") {
		t.Fatalf("expected generic failure reason in comment, got body:\n%s", comments[0].body)
	}
	if _, err := os.Stat(runFailureCommentMarkerPath(run.RunDir)); err != nil {
		t.Fatalf("expected failure marker after successful retry: %v", err)
	}
}

func TestHandleTaskSubresourcesGet(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestHandleCreateIssueTaskNormalizesRepositoryCase(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks/issue",
		strings.NewReader(`{"repo":"Owner/Repo","issue_number":7}`),
	)
	rec := httptest.NewRecorder()
	s.handleCreateIssueTask(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var out struct {
		Run state.Run `json:"run"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Run.Repo != "owner/repo" {
		t.Fatalf("run repo = %q, want owner/repo", out.Run.Repo)
	}
	if out.Run.TaskID != "owner/repo#7" {
		t.Fatalf("run task id = %q, want owner/repo#7", out.Run.TaskID)
	}

	task, ok := s.store.GetTask("owner/repo#7")
	if !ok {
		t.Fatal("expected normalized task to be persisted")
	}
	if task.Repo != "owner/repo" {
		t.Fatalf("task repo = %q, want owner/repo", task.Repo)
	}
}

func TestHandleCancelRunQueued(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{TaskID: "active-cancel", Repo: "owner/repo", Task: "cancel me"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

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
	t.Parallel()
	done := make(chan struct{})
	cleanupDone := make(chan struct{})
	launcher := &stubbornLauncher{
		wait: done,
		res: fakeRunResult{
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
	s.afterRunCleanup = func(runID string) {
		if runID == run.ID {
			select {
			case <-cleanupDone:
			default:
				close(cleanupDone)
			}
		}
	}

	_ = waitForRunExecution(t, s, run.ID)

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
	waitFor(t, 5*time.Second, func() bool {
		select {
		case <-cleanupDone:
			return true
		default:
			return false
		}
	}, "run cleanup after canceled run")
}

func TestCancelActiveRunsUsesDrainReason(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{TaskID: "drain-reason", Repo: "owner/repo", Task: "drain"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	s.cancelActiveRuns("orchestrator shutdown drain timeout")
	close(waitCh)

	waitFor(t, 2*time.Second, func() bool {
		current, ok := s.store.GetRun(run.ID)
		return ok && current.Status == state.StatusCanceled && strings.Contains(current.Error, "drain timeout")
	}, "run canceled with drain reason")
}

func TestExecuteRunHonorsPersistedCancelBeforeStart(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	_ = waitForRunExecution(t, s, run.ID)

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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestExecuteRunPersistsRunExecutionHandle(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{TaskID: "task_exec_handle", Repo: "owner/repo", Task: "persist execution handle"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	execRec := waitForRunExecution(t, s, run.ID)
	if execRec.Backend == "" || execRec.ContainerID == "" || execRec.ContainerName == "" {
		t.Fatalf("unexpected execution record: %+v", execRec)
	}

	close(waitCh)
	waitFor(t, 2*time.Second, func() bool {
		updated, ok := s.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "run completion")
}

func TestRecoverRunningRunAdoptsDetachedExecution(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s1 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	run, err := s1.createAndQueueRun(runRequest{TaskID: "task_adopt", Repo: "owner/repo", Task: "adopt detached"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	execRec := waitForRunExecution(t, s1, run.ID)

	waitFor(t, 2*time.Second, func() bool {
		lease, ok := s1.store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-a"
	}, "instance-a lease ownership")

	s1.beginDrain()
	s1.stopRunSupervisors()
	if err := s1.store.DeleteRunLease(run.ID); err != nil {
		t.Fatalf("delete s1 lease: %v", err)
	}

	s2 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-b")
	defer waitForServerIdle(t, s2)
	s2.recoverRunningRuns()

	waitFor(t, 2*time.Second, func() bool {
		lease, ok := s2.store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-b"
	}, "instance-b lease ownership")

	adoptedExec, ok := s2.store.GetRunExecution(run.ID)
	if !ok {
		t.Fatalf("expected execution after adoption")
	}
	if adoptedExec.ContainerID != execRec.ContainerID {
		t.Fatalf("expected same container id after adoption: got %s want %s", adoptedExec.ContainerID, execRec.ContainerID)
	}

	close(waitCh)
	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s2.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "adopted run completion")
}

func TestDrainReleaseDoesNotDeleteAdoptedLease(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s1 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	run, err := s1.createAndQueueRun(runRequest{TaskID: "task_safe_lease_release", Repo: "owner/repo", Task: "safe lease release"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s1, run.ID)

	s2 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-b")
	defer waitForServerIdle(t, s2)
	s2.recoverRunningRuns()

	waitFor(t, 2*time.Second, func() bool {
		lease, ok := s2.store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-b"
	}, "instance-b lease ownership")

	s1.beginDrain()
	s1.stopRunSupervisors()
	if err := s1.waitForNoActiveRuns(3 * time.Second); err != nil {
		t.Fatalf("wait for s1 idle: %v", err)
	}

	lease, ok := s2.store.GetRunLease(run.ID)
	if !ok || lease.OwnerID != "instance-b" {
		t.Fatalf("expected adopted lease to remain with instance-b, got %+v ok=%t", lease, ok)
	}

	close(waitCh)
	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s2.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "completion after safe lease release")
}

func TestRecoverRunningRunFinalizesExitedDetachedExecution(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_recover_exited_exec",
		TaskID:     "task_recover_exited_exec",
		Repo:       "owner/repo",
		Task:       "recover exited detached run",
		BaseBranch: "main",
		HeadBranch: "rascal/recover-exited",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}

	handle, err := launcher.StartDetached(context.Background(), runner.Spec{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Task:        run.Task,
		BaseBranch:  run.BaseBranch,
		HeadBranch:  run.HeadBranch,
		Trigger:     run.Trigger,
		RunDir:      run.RunDir,
		IssueNumber: run.IssueNumber,
		PRNumber:    run.PRNumber,
		Context:     run.Context,
		Debug:       run.Debug,
	})
	if err != nil {
		t.Fatalf("start detached fake execution: %v", err)
	}
	if err := runner.WriteMeta(filepath.Join(run.RunDir, "meta.json"), runner.Meta{
		RunID:      run.ID,
		TaskID:     run.TaskID,
		Repo:       run.Repo,
		BaseBranch: run.BaseBranch,
		HeadBranch: run.HeadBranch,
		ExitCode:   0,
	}); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if _, err := s.store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       handle.Backend,
		ContainerName: handle.Name,
		ContainerID:   handle.ID,
		Status:        "running",
	}); err != nil {
		t.Fatalf("upsert run execution: %v", err)
	}

	s.recoverRunningRuns()
	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "recover exited execution finalization")
	if _, ok := s.store.GetRunExecution(run.ID); ok {
		t.Fatalf("expected execution record to be removed after finalization")
	}
}

func TestRecoverRunningRunMissingDetachedExecutionFails(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_recover_missing_exec",
		TaskID:     "task_recover_missing_exec",
		Repo:       "owner/repo",
		Task:       "recover missing detached run",
		BaseBranch: "main",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := s.store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       "docker",
		ContainerName: "rascal-run_recover_missing_exec",
		ContainerID:   "missing-execution-id",
		Status:        "running",
	}); err != nil {
		t.Fatalf("upsert run execution: %v", err)
	}

	s.recoverRunningRuns()
	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusFailed && strings.Contains(updated.Error, "detached container missing during adoption")
	}, "recover missing execution failure")
	if _, ok := s.store.GetRunExecution(run.ID); ok {
		t.Fatalf("expected missing execution record to be cleared")
	}
}

func TestRecoverRunningRunAdoptsByStableContainerName(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_recover_by_name",
		TaskID:     "task_recover_by_name",
		Repo:       "owner/repo",
		Task:       "recover by stable name",
		BaseBranch: "main",
		RunDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}

	handle, err := launcher.StartDetached(context.Background(), runner.Spec{
		RunID:      run.ID,
		TaskID:     run.TaskID,
		Repo:       run.Repo,
		Task:       run.Task,
		BaseBranch: run.BaseBranch,
		RunDir:     run.RunDir,
	})
	if err != nil {
		t.Fatalf("start detached fake execution: %v", err)
	}
	if _, err := s.store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       handle.Backend,
		ContainerName: handle.Name,
		ContainerID:   handle.Name,
		Status:        "created",
	}); err != nil {
		t.Fatalf("upsert placeholder execution: %v", err)
	}

	s.recoverRunningRuns()
	waitFor(t, 2*time.Second, func() bool {
		lease, ok := s.store.GetRunLease(run.ID)
		return ok && lease.OwnerID == s.instanceID
	}, "name-based adoption lease ownership")

	close(waitCh)
	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "name-based adoption completion")
}

func TestCancelRunWorksAfterAdoptionByDifferentInstance(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s1 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	run, err := s1.createAndQueueRun(runRequest{TaskID: "task_cancel_adopt", Repo: "owner/repo", Task: "cancel after adopt"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s1, run.ID)

	s1.beginDrain()
	s1.stopRunSupervisors()
	if err := s1.waitForNoActiveRuns(3 * time.Second); err != nil {
		t.Fatalf("wait for s1 idle: %v", err)
	}

	s2 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-b")
	defer waitForServerIdle(t, s2)
	s2.recoverRunningRuns()
	waitFor(t, 2*time.Second, func() bool {
		lease, ok := s2.store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-b"
	}, "instance-b lease ownership")

	rec := httptest.NewRecorder()
	s2.handleCancelRun(rec, run.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for adopted run cancel, got %d", rec.Code)
	}

	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s2.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusCanceled && strings.Contains(updated.Error, "canceled by user")
	}, "adopted run canceled")
}

func TestStopRunSupervisorsCatchesInFlightSupervisorRegistration(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	reachedBeforeSupervise := make(chan struct{})
	releaseBeforeSupervise := make(chan struct{})
	s.beforeSupervise = func(_ string) {
		close(reachedBeforeSupervise)
		<-releaseBeforeSupervise
	}

	run, err := s.createAndQueueRun(runRequest{TaskID: "task_stop_supervisor_race", Repo: "owner/repo", Task: "stop supervisor race"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)
	waitFor(t, time.Second, func() bool {
		select {
		case <-reachedBeforeSupervise:
			return true
		default:
			return false
		}
	}, "reach pre-supervisor hook")

	s.beginDrain()
	s.stopRunSupervisors()
	close(releaseBeforeSupervise)

	if err := s.waitForNoActiveRuns(500 * time.Millisecond); err != nil {
		t.Fatalf("wait for idle after stop supervisors: %v", err)
	}
}

func TestLateCancelDoesNotOverwriteSuccessfulCompletion(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.createAndQueueRun(runRequest{TaskID: "task_late_cancel_success", Repo: "owner/repo", Task: "late cancel success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	close(waitCh)

	rec := httptest.NewRecorder()
	s.handleCancelRun(rec, run.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for running cancel, got %d", rec.Code)
	}

	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "successful completion wins over late cancel")
}

func TestRepeatedHandoffPreservesDetachedExecutionHandle(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s1 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	run, err := s1.createAndQueueRun(runRequest{TaskID: "task_repeated_handoff", Repo: "owner/repo", Task: "repeated handoff"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	execRec := waitForRunExecution(t, s1, run.ID)

	s1.beginDrain()
	s1.stopRunSupervisors()
	if err := s1.waitForNoActiveRuns(3 * time.Second); err != nil {
		t.Fatalf("wait for s1 idle: %v", err)
	}

	s2 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-b")
	s2.recoverRunningRuns()
	waitFor(t, 2*time.Second, func() bool {
		lease, ok := s2.store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-b"
	}, "instance-b lease ownership")
	midExec, ok := s2.store.GetRunExecution(run.ID)
	if !ok {
		t.Fatalf("expected execution after first handoff")
	}
	if midExec.ContainerID != execRec.ContainerID {
		t.Fatalf("expected same container id after first handoff: got %s want %s", midExec.ContainerID, execRec.ContainerID)
	}

	s2.beginDrain()
	s2.stopRunSupervisors()
	if err := s2.store.DeleteRunLease(run.ID); err != nil {
		t.Fatalf("delete s2 lease: %v", err)
	}

	s3 := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-c")
	defer waitForServerIdle(t, s3)
	s3.recoverRunningRuns()
	waitFor(t, 2*time.Second, func() bool {
		lease, ok := s3.store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-c"
	}, "instance-c lease ownership")
	lastExec, ok := s3.store.GetRunExecution(run.ID)
	if !ok {
		t.Fatalf("expected execution after second handoff")
	}
	if lastExec.ContainerID != execRec.ContainerID {
		t.Fatalf("expected same container id after second handoff: got %s want %s", lastExec.ContainerID, execRec.ContainerID)
	}

	close(waitCh)
	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s3.store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "run completion after repeated handoff")
}

func TestDrainStopsSupervisionWithoutCancelingDetachedRun(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)

	run, err := s.createAndQueueRun(runRequest{TaskID: "task_drain_detached", Repo: "owner/repo", Task: "drain without cancel"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	execRec := waitForRunExecution(t, s, run.ID)

	s.beginDrain()
	s.stopRunSupervisors()
	if err := s.waitForNoActiveRuns(3 * time.Second); err != nil {
		t.Fatalf("wait for no active runs: %v", err)
	}

	updated, ok := s.store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusRunning {
		t.Fatalf("expected run to remain running during drain, got %s", updated.Status)
	}
	afterExec, ok := s.store.GetRunExecution(run.ID)
	if !ok {
		t.Fatalf("expected execution record to remain after drain")
	}
	if afterExec.ContainerID != execRec.ContainerID {
		t.Fatalf("expected same execution handle after drain")
	}

	close(waitCh)
}

func TestHandleRunLogsRespectsLines(t *testing.T) {
	t.Parallel()
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
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(gooseLog.String()), 0o644); err != nil {
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
	t.Parallel()
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
	markRunSucceeded(t, s, run.ID)

	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\nrunner-2\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte("goose-1\ngoose-2\n"), 0o644); err != nil {
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

func TestHandleRunLogsMissingAgentFileStillReturnsRunnerLogs(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_logs_missing_goose",
		TaskID:     "task_logs_missing_goose",
		Repo:       "owner/repo",
		Task:       "show logs without agent output",
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
	if !strings.Contains(body, "== agent.ndjson ==") {
		t.Fatalf("expected agent section header in response, got:\n%s", body)
	}
	if !strings.Contains(body, "(agent.ndjson not found)") {
		t.Fatalf("expected missing agent note, got:\n%s", body)
	}
}

func TestHandleRunLogsFallsBackToLegacyGooseLogFile(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeLauncher{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_logs_legacy_goose",
		TaskID:     "task_logs_legacy_goose",
		Repo:       "owner/repo",
		Task:       "show logs from legacy file",
		BaseBranch: "main",
		RunDir:     runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte("legacy-1\nlegacy-2\n"), 0o644); err != nil {
		t.Fatalf("write legacy goose log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?lines=5", nil)
	rec := httptest.NewRecorder()
	s.handleRunSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "== agent.ndjson ==") {
		t.Fatalf("expected agent section header in response, got:\n%s", body)
	}
	if !strings.Contains(body, "legacy-2") {
		t.Fatalf("expected legacy goose log contents, got:\n%s", body)
	}
	if strings.Contains(body, "(agent.ndjson not found)") {
		t.Fatalf("did not expect missing agent note when legacy file exists, got:\n%s", body)
	}
}

func TestHandleRunLogsRejectsInvalidFormat(t *testing.T) {
	t.Parallel()
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
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte("goose-1\n"), 0o644); err != nil {
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
	t.Parallel()
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
	t.Parallel()
	got := buildHeadBranch("owner/repo#123", "ignored task text", "run_deadbeefcafefeed")
	if !strings.HasPrefix(got, "rascal/owner/repo-123-") {
		t.Fatalf("expected task-id-based branch prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "-deadbeefca") {
		t.Fatalf("expected short run-id suffix, got %q", got)
	}
}

func TestHandleReadyReflectsDrainingState(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	first, err := s.createAndQueueRun(runRequest{TaskID: "owner/repo#drain", Repo: "owner/repo", Task: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	queued, err := s.createAndQueueRun(runRequest{TaskID: "owner/repo#drain", Repo: "owner/repo", Task: "queued"})
	if err != nil {
		t.Fatalf("create queued run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")
	_ = waitForRunExecution(t, s, first.ID)

	s.beginDrain()

	waitFor(t, time.Second, func() bool {
		r, ok := s.store.GetRun(queued.ID)
		return ok && r.Status == state.StatusQueued
	}, "queued run remains queued during drain")
}
