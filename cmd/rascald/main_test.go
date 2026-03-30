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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/credentials"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/orchestrator"
	"github.com/rtzll/rascal/internal/repositories"
	"github.com/rtzll/rascal/internal/runner"
	agentrt "github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

var (
	testStateTemplateOnce sync.Once
	testStateTemplatePath string
	testStateTemplateErr  error
)

type fakeRunner struct {
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

type stubbornRunner struct {
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

type repoFixtureInput struct {
	fullName             string
	webhookKey           string
	githubToken          string
	webhookSecret        string
	enabled              bool
	allowManual          bool
	allowIssueLabel      bool
	allowIssueEdit       bool
	allowPRComment       bool
	allowPRReview        bool
	allowPRReviewComment bool
}

type fakeGitHubClient struct {
	mu sync.Mutex

	issueData ghapi.IssueData
	issueErr  error
	pullData  ghapi.PullRequest
	pullErr   error

	issueReactions []postedIssueReaction
	removedIssues  []string

	issueCommentReactions             []postedIssueCommentReaction
	pullRequestReviewReactions        []postedPullRequestReviewReaction
	pullRequestReviewCommentReactions []postedPullRequestReviewCommentReaction

	issueComments                     []postedIssueComment
	createIssueCommentErr             error
	createIssueCommentErrSeq          []error
	createIssueCommentPostsOnErrorSeq []bool
	createIssueCommentCalls           int
}

func (f *fakeRunner) StartDetached(_ context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
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
		Backend: runner.ExecutionBackend("fake"),
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

func (f *fakeRunner) lookupExecution(handle runner.ExecutionHandle) (*fakeExecution, bool) {
	if execRec, ok := f.execs[handle.ID]; ok {
		return execRec, true
	}
	if execRec, ok := f.execs[handle.Name]; ok {
		return execRec, true
	}
	return nil, false
}

func (f *fakeRunner) Inspect(_ context.Context, handle runner.ExecutionHandle) (runner.ExecutionState, error) {
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

func (f *fakeRunner) Stop(_ context.Context, handle runner.ExecutionHandle, _ time.Duration) error {
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

func (f *fakeRunner) Remove(_ context.Context, handle runner.ExecutionHandle) error {
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

func (l *stubbornRunner) StartDetached(_ context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
	l.mu.Lock()
	l.calls++
	l.lastSpec = spec
	l.mu.Unlock()
	return runner.ExecutionHandle{
		Backend: runner.ExecutionBackend("fake"),
		ID:      "stubborn-exec",
		Name:    "stubborn-" + spec.RunID,
	}, nil
}

func (l *stubbornRunner) Inspect(_ context.Context, _ runner.ExecutionHandle) (runner.ExecutionState, error) {
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

func (l *stubbornRunner) Stop(_ context.Context, _ runner.ExecutionHandle, _ time.Duration) error {
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

func (l *stubbornRunner) Remove(_ context.Context, _ runner.ExecutionHandle) error {
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
	if err := runner.ReportRunResult(spec.ResultReportSocketPath, meta.RunResult()); err != nil {
		return fmt.Errorf("report fake run result: %w", err)
	}
	return nil
}

func (f *fakeGitHubClient) GetIssue(_ context.Context, _ string, _ int) (ghapi.IssueData, error) {
	if f.issueErr != nil {
		return ghapi.IssueData{}, f.issueErr
	}
	return f.issueData, nil
}

func (f *fakeGitHubClient) GetPullRequest(_ context.Context, _ string, _ int) (ghapi.PullRequest, error) {
	if f.pullErr != nil {
		return ghapi.PullRequest{}, f.pullErr
	}
	return f.pullData, nil
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
	postOnError := false
	if callIdx < len(f.createIssueCommentErrSeq) {
		err = f.createIssueCommentErrSeq[callIdx]
	}
	if callIdx < len(f.createIssueCommentPostsOnErrorSeq) {
		postOnError = f.createIssueCommentPostsOnErrorSeq[callIdx]
	}
	if err != nil && !postOnError {
		return err
	}
	f.issueComments = append(f.issueComments, postedIssueComment{
		repo:        repo,
		issueNumber: issueNumber,
		body:        body,
	})
	if err != nil {
		return err
	}
	return nil
}

func (f *fakeGitHubClient) ListIssueComments(_ context.Context, repo string, issueNumber int) ([]ghapi.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	comments := make([]ghapi.Comment, 0, len(f.issueComments))
	for _, comment := range f.issueComments {
		if comment.repo != repo || comment.issueNumber != issueNumber {
			continue
		}
		comments = append(comments, ghapi.Comment{Body: comment.body})
	}
	return comments, nil
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

func (f *fakeRunner) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func mustCreateRepositoryFixture(t *testing.T, s *orchestrator.Server, in repoFixtureInput) state.Repository {
	t.Helper()
	token := in.githubToken
	if token == "" {
		token = "gh-token"
	}
	secret := in.webhookSecret
	if secret == "" {
		secret = "wh-secret"
	}
	encToken, err := s.Cipher.Encrypt([]byte(token))
	if err != nil {
		t.Fatalf("encrypt github token: %v", err)
	}
	encSecret, err := s.Cipher.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("encrypt webhook secret: %v", err)
	}
	repo, err := s.Store.CreateRepository(state.CreateRepositoryInput{
		FullName:               in.fullName,
		WebhookKey:             in.webhookKey,
		Enabled:                in.enabled,
		EncryptedGitHubToken:   encToken,
		EncryptedWebhookSecret: encSecret,
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

func newTestServer(t *testing.T, launcher runner.Runner) *orchestrator.Server {
	t.Helper()

	dataDir, err := os.MkdirTemp("/tmp", "rascald-test-")
	if err != nil {
		t.Fatalf("create short test data dir: %v", err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(dataDir); removeErr != nil {
			t.Fatalf("remove short test data dir: %v", removeErr)
		}
	})
	return newTestServerWithPaths(t, launcher, dataDir, filepath.Join(dataDir, "state.db"), "test-instance")
}

func newTestServerWithPaths(t *testing.T, launcher runner.Runner, dataDir, statePath, instanceID string) *orchestrator.Server {
	t.Helper()

	cfg := config.ServerConfig{
		DataDir:              dataDir,
		StatePath:            statePath,
		MaxRuns:              200,
		RunnerMode:           runner.ModeNoop,
		CredentialRenewEvery: 20 * time.Millisecond,
	}
	if err := prepareTestStatePath(cfg.StatePath); err != nil {
		t.Fatalf("prepare test state path: %v", err)
	}
	store, err := state.NewWithoutMigrate(cfg.StatePath, cfg.MaxRuns)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	cipher, err := credentials.NewAESCipher("test-secret")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	s := orchestrator.NewServer(
		cfg,
		store,
		launcher,
		ghapi.NewAPIClient(""),
		nil,
		cipher,
		strings.TrimSpace(instanceID),
	)
	s.MaxConcurrent = runtime.NumCPU()
	s.SupervisorInterval = 10 * time.Millisecond
	s.RetryBackoff = func(_ int) time.Duration {
		return 10 * time.Millisecond
	}
	s.RepositoryResolver = repositories.NewResolver(store, cipher)
	if err := s.StartRunResultReporter(); err != nil {
		t.Fatalf("start run result reporter: %v", err)
	}
	t.Cleanup(func() {
		s.BeginDrain()
		s.StopRunSupervisors()
		if err := s.WaitForNoActiveSupervisors(2 * time.Second); err != nil {
			t.Fatalf("wait for test supervisors to stop: %v", err)
		}
		if err := s.StopRunResultReporter(); err != nil {
			t.Fatalf("stop run result reporter: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close test state store: %v", err)
		}
	})
	return s
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

func waitForRunExecution(t *testing.T, s *orchestrator.Server, runID string) state.RunExecution {
	t.Helper()
	var execRec state.RunExecution
	waitFor(t, 2*time.Second, func() bool {
		rec, ok := s.Store.GetRunExecution(runID)
		if !ok {
			return false
		}
		if rec.Status != state.RunExecutionStatusRunning {
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

func mustMarshalJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json payload: %v", err)
	}
	return data
}

func testIssue(number int, title, body string, labels []string, isPR bool) ghapi.Issue {
	issue := ghapi.Issue{
		Number: number,
		Title:  title,
		Body:   body,
		State:  "open",
		Labels: make([]ghapi.Label, 0, len(labels)),
	}
	for _, label := range labels {
		issue.Labels = append(issue.Labels, ghapi.Label{Name: label})
	}
	if isPR {
		issue.PullRequest = &ghapi.PullRequestRef{}
	}
	return issue
}

func testPullRequest(number int, merged bool, baseRef, headRef string) ghapi.PullRequest {
	pr := ghapi.PullRequest{
		Number: number,
		Merged: merged,
		State:  "open",
	}
	pr.Base.Ref = baseRef
	pr.Head.Ref = headRef
	return pr
}

func issuesEventPayload(t *testing.T, action, repo, sender, label string, issue ghapi.Issue) []byte {
	t.Helper()
	return mustMarshalJSONBytes(t, ghapi.IssuesEvent{
		Action:     action,
		Label:      ghapi.Label{Name: label},
		Issue:      issue,
		Repository: ghapi.Repository{FullName: repo},
		Sender:     ghapi.User{Login: sender},
	})
}

func issueCommentEventPayload(t *testing.T, action, repo, sender string, issue ghapi.Issue, comment ghapi.Comment, bodyFrom string) []byte {
	t.Helper()
	event := ghapi.IssueCommentEvent{
		Action:     action,
		Issue:      issue,
		Comment:    comment,
		Repository: ghapi.Repository{FullName: repo},
		Sender:     ghapi.User{Login: sender},
	}
	if bodyFrom != "" {
		event.Changes.Body = &ghapi.IssueCommentBodyChange{From: bodyFrom}
	}
	return mustMarshalJSONBytes(t, event)
}

func pullRequestReviewEventPayload(t *testing.T, action, repo, sender string, review ghapi.Review, pr ghapi.PullRequest) []byte {
	t.Helper()
	return mustMarshalJSONBytes(t, ghapi.PullRequestReviewEvent{
		Action:      action,
		Review:      review,
		PullRequest: pr,
		Repository:  ghapi.Repository{FullName: repo},
		Sender:      ghapi.User{Login: sender},
	})
}

func pullRequestReviewCommentEventPayload(t *testing.T, action, repo, sender string, comment ghapi.ReviewComment, pr ghapi.PullRequest, bodyFrom string) []byte {
	t.Helper()
	event := ghapi.PullRequestReviewCommentEvent{
		Action:      action,
		Comment:     comment,
		PullRequest: pr,
		Repository:  ghapi.Repository{FullName: repo},
		Sender:      ghapi.User{Login: sender},
	}
	if bodyFrom != "" {
		event.Changes.Body = &ghapi.IssueCommentBodyChange{From: bodyFrom}
	}
	return mustMarshalJSONBytes(t, event)
}

func pullRequestReviewThreadEventPayload(t *testing.T, action, repo, sender string, thread ghapi.ReviewThread, pr ghapi.PullRequest) []byte {
	t.Helper()
	return mustMarshalJSONBytes(t, ghapi.PullRequestReviewThreadEvent{
		Action:      action,
		Thread:      thread,
		PullRequest: pr,
		Repository:  ghapi.Repository{FullName: repo},
		Sender:      ghapi.User{Login: sender},
	})
}

func pullRequestEventPayload(t *testing.T, action, repo, sender string, pr ghapi.PullRequest) []byte {
	t.Helper()
	return mustMarshalJSONBytes(t, ghapi.PullRequestEvent{
		Action:      action,
		PullRequest: pr,
		Repository:  ghapi.Repository{FullName: repo},
		Sender:      ghapi.User{Login: sender},
	})
}

func checkRunEventPayload(t *testing.T, action, repo, sender string, check ghapi.CheckRun) []byte {
	t.Helper()
	return mustMarshalJSONBytes(t, ghapi.CheckRunEvent{
		Action:     action,
		CheckRun:   check,
		Repository: ghapi.Repository{FullName: repo},
		Sender:     ghapi.User{Login: sender},
	})
}

func checkSuiteEventPayload(t *testing.T, action, repo, sender string, suite ghapi.CheckSuite) []byte {
	t.Helper()
	return mustMarshalJSONBytes(t, ghapi.CheckSuiteEvent{
		Action:     action,
		CheckSuite: suite,
		Repository: ghapi.Repository{FullName: repo},
		Sender:     ghapi.User{Login: sender},
	})
}

func scopedWebhookRequest(t *testing.T, webhookKey, eventType, deliveryID, secret string, payload []byte) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github/"+webhookKey, bytes.NewReader(payload))
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

func waitForServerIdle(t *testing.T, s *orchestrator.Server) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool {
		return s.ActiveRunCount() == 0
	}, "server idle")
}

func markRunSucceeded(t *testing.T, s *orchestrator.Server, runID string) {
	t.Helper()
	if _, err := s.Store.SetRunStatus(runID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set run running before success: %v", err)
	}
	if _, err := s.Store.SetRunStatus(runID, state.StatusSucceeded, ""); err != nil {
		t.Fatalf("set run succeeded: %v", err)
	}
}

func markRunReview(t *testing.T, s *orchestrator.Server, runID string) {
	t.Helper()
	if _, err := s.Store.SetRunStatus(runID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set run running before review: %v", err)
	}
	if _, err := s.Store.SetRunStatus(runID, state.StatusReview, ""); err != nil {
		t.Fatalf("set run review: %v", err)
	}
}

func TestHandleWebhookRecordsDeliveryOnlyAfterSuccess(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	deliveryID := "delivery-1"

	badReq := webhookRequest(t, []byte("{"), "issues", deliveryID, "")
	badRec := httptest.NewRecorder()
	s.HandleWebhook(badRec, badReq)
	if badRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for bad payload, got %d", badRec.Code)
	}
	if s.Store.DeliverySeen(deliveryID) {
		t.Fatal("delivery should not be recorded when processing fails")
	}

	goodPayload := issuesEventPayload(t, "labeled", "owner/repo", "dev", "rascal", testIssue(7, "Title", "Body", nil, false))
	goodReq := webhookRequest(t, goodPayload, "issues", deliveryID, "")
	goodRec := httptest.NewRecorder()
	s.HandleWebhook(goodRec, goodReq)
	if goodRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for good payload, got %d", goodRec.Code)
	}
	if !s.Store.DeliverySeen(deliveryID) {
		t.Fatal("delivery should be recorded after successful processing")
	}
	if got := len(s.Store.ListRuns(10)); got != 1 {
		t.Fatalf("expected one run, got %d", got)
	}

	dupReq := webhookRequest(t, goodPayload, "issues", deliveryID, "")
	dupRec := httptest.NewRecorder()
	s.HandleWebhook(dupRec, dupReq)
	if dupRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for duplicate delivery, got %d", dupRec.Code)
	}
	if got := len(s.Store.ListRuns(10)); got != 1 {
		t.Fatalf("expected one run after duplicate, got %d", got)
	}
}

func TestHandleWebhookIgnoresIssueLabeledOnPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	payload := issuesEventPayload(t, "labeled", "owner/repo", "dev", "rascal", testIssue(7, "Title", "Body", nil, true))

	req := webhookRequest(t, payload, "issues", "delivery-pr", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if got := len(s.Store.ListRuns(10)); got != 0 {
		t.Fatalf("expected zero runs, got %d", got)
	}
}

func TestHandleWebhookIssueClosedCancelsRunsAndCompletesTask(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
	taskID := "owner/repo#7"

	runningRun, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: taskID, Repo: "owner/repo", Instruction: "work", IssueNumber: 7})
	if err != nil {
		t.Fatalf("create running run: %v", err)
	}
	queuedRun, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: taskID, Repo: "owner/repo", Instruction: "queued", IssueNumber: 7})
	if err != nil {
		t.Fatalf("create queued run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")
	_ = waitForRunExecution(t, s, runningRun.ID)

	payload := issuesEventPayload(t, "closed", "owner/repo", "dev", "", testIssue(7, "Title", "Body", []string{"rascal"}, false))
	req := webhookRequest(t, payload, "issues", "delivery-closed", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for closed issue event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return s.Store.IsTaskCompleted(taskID) }, "task marked completed")
	waitFor(t, time.Second, func() bool {
		r, ok := s.Store.GetRun(queuedRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "queued run canceled")
	waitFor(t, 3*time.Second, func() bool {
		r, ok := s.Store.GetRun(runningRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "running run canceled")

	close(waitCh)
}

func TestHandleWebhookIssueReopenedReenablesTask(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	taskID := "owner/repo#7"

	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", IssueNumber: 7}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	if err := s.Store.MarkTaskCompleted(taskID); err != nil {
		t.Fatalf("mark task completed: %v", err)
	}

	payload := issuesEventPayload(t, "reopened", "owner/repo", "dev", "", testIssue(7, "Title", "Body", []string{"rascal"}, false))
	req := webhookRequest(t, payload, "issues", "delivery-reopened", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for reopened issue event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return !s.Store.IsTaskCompleted(taskID) }, "task reopened")
	waitFor(t, time.Second, func() bool { return len(s.Store.ListRuns(10)) == 1 }, "run queued")
}

func TestHandleWebhookIssueEditedRequeuesRuns(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
	taskID := "owner/repo#7"

	runningRun, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: taskID, Repo: "owner/repo", Instruction: "work", IssueNumber: 7})
	if err != nil {
		t.Fatalf("create running run: %v", err)
	}
	queuedRun, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: taskID, Repo: "owner/repo", Instruction: "stale", IssueNumber: 7})
	if err != nil {
		t.Fatalf("create queued run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")
	_ = waitForRunExecution(t, s, runningRun.ID)

	payload := issuesEventPayload(t, "edited", "owner/repo", "dev", "", testIssue(7, "New Title", "New Body", []string{"rascal"}, false))
	req := webhookRequest(t, payload, "issues", "delivery-edited", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for edited issue event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool {
		r, ok := s.Store.GetRun(queuedRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "queued run canceled")

	var editedRun state.Run
	waitFor(t, time.Second, func() bool {
		for _, run := range s.Store.ListRuns(20) {
			if run.Trigger == "issue_edited" {
				editedRun = run
				return true
			}
		}
		return false
	}, "issue edited run queued")

	if editedRun.Instruction != "New Title\n\nNew Body" {
		t.Fatalf("expected updated task text, got %q", editedRun.Instruction)
	}
	if editedRun.TaskID != taskID {
		t.Fatalf("expected edited run task id %q, got %q", taskID, editedRun.TaskID)
	}
	if editedRun.ID == runningRun.ID || editedRun.ID == queuedRun.ID {
		t.Fatalf("expected new run for edit, got existing run id %q", editedRun.ID)
	}

	close(waitCh)
	waitFor(t, 3*time.Second, func() bool {
		r, ok := s.Store.GetRun(editedRun.ID)
		return ok && state.IsFinalRunStatus(r.Status)
	}, "edited run completed")

}

func TestHandleWebhookIssueLabeledMigratesTaskBackend(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	s.Config.TaskSession.Mode = agentrt.SessionModeAll
	defer waitForServerIdle(t, s)

	const taskID = "owner/repo#7"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:           taskID,
		Repo:         "owner/repo",
		AgentRuntime: agentrt.RuntimeCodex,
		IssueNumber:  7,
	}); err != nil {
		t.Fatalf("upsert legacy task: %v", err)
	}
	if _, err := s.Store.UpsertTaskSession(state.UpsertTaskSessionInput{
		TaskID:           taskID,
		AgentRuntime:     agentrt.RuntimeCodex,
		RuntimeSessionID: "legacy-codex-session",
		SessionKey:       "legacy",
		SessionRoot:      filepath.Join(t.TempDir(), "legacy-session"),
		LastRunID:        "run_legacy",
	}); err != nil {
		t.Fatalf("upsert legacy task session: %v", err)
	}

	payload := issuesEventPayload(t, "labeled", "owner/repo", "dev", "rascal", testIssue(7, "Title", "Body", nil, false))
	req := webhookRequest(t, payload, "issues", "delivery-backend-migrate", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for migrated labeled issue event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return len(s.Store.ListRuns(10)) == 1 }, "run queued")

	run := s.Store.ListRuns(10)[0]
	if run.AgentRuntime != agentrt.RuntimeGooseCodex {
		t.Fatalf("run backend = %s, want %s", run.AgentRuntime, agentrt.RuntimeGooseCodex)
	}

	task, ok := s.Store.GetTask(taskID)
	if !ok {
		t.Fatalf("missing task %s", taskID)
	}
	if task.AgentRuntime != agentrt.RuntimeGooseCodex {
		t.Fatalf("task backend = %s, want %s", task.AgentRuntime, agentrt.RuntimeGooseCodex)
	}

	var session state.TaskSession
	waitFor(t, time.Second, func() bool {
		var ok bool
		session, ok = s.Store.GetTaskSession(taskID)
		return ok
	}, "migrated task session")
	if session.AgentRuntime != agentrt.RuntimeGooseCodex {
		t.Fatalf("task session backend = %s, want %s", session.AgentRuntime, agentrt.RuntimeGooseCodex)
	}
	if session.RuntimeSessionID == "" {
		t.Fatal("task session id should be set after runtime migration")
	}
	if session.RuntimeSessionID == "legacy-codex-session" {
		t.Fatalf("task session id = %q, want a fresh goose session id", session.RuntimeSessionID)
	}
}

func TestHandleWebhookIssueUnlabeledRemovesPastReactions(t *testing.T) {
	t.Parallel()
	fakeGH := &fakeGitHubClient{}
	fakeGH.addIssueReaction("owner/repo", 7, ghapi.ReactionEyes)
	fakeGH.addIssueReaction("owner/repo", 7, ghapi.ReactionRocket)
	fakeGH.addIssueReaction("owner/repo", 8, ghapi.ReactionEyes)

	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	payload := issuesEventPayload(t, "unlabeled", "owner/repo", "dev", "rascal", testIssue(7, "Title", "Body", nil, false))
	req := webhookRequest(t, payload, "issues", "delivery-unlabeled", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
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
	s := newTestServer(t, &fakeRunner{})
	for i := 1; i <= 3; i++ {
		_, err := s.Store.AddRun(state.CreateRunInput{
			ID:          fmt.Sprintf("run_%d", i),
			TaskID:      fmt.Sprintf("task_%d", i),
			Repo:        "owner/repo",
			Instruction: fmt.Sprintf("Task %d", i),
		})
		if err != nil {
			t.Fatalf("add run %d: %v", i, err)
		}
	}

	limitReq := httptest.NewRequest(http.MethodGet, "/v1/runs?limit=2", nil)
	limitRec := httptest.NewRecorder()
	s.HandleListRuns(limitRec, limitReq)
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
	s.HandleListRuns(allRec, allReq)
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
	s := newTestServer(t, &fakeRunner{})
	for i := 1; i <= 2; i++ {
		_, err := s.Store.AddRun(state.CreateRunInput{
			ID:          fmt.Sprintf("run_all_%d", i),
			TaskID:      fmt.Sprintf("task_all_%d", i),
			Repo:        "owner/repo",
			Instruction: fmt.Sprintf("Task all %d", i),
		})
		if err != nil {
			t.Fatalf("add run %d: %v", i, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs?all=true&limit=bad", nil)
	rec := httptest.NewRecorder()
	s.HandleListRuns(rec, req)
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
	s := newTestServer(t, &fakeRunner{})

	req := httptest.NewRequest(http.MethodGet, "/v1/runs?all=notabool", nil)
	rec := httptest.NewRecorder()
	s.HandleListRuns(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid all query, got %d", rec.Code)
	}
}

func TestHandleWebhookInactiveSlotIsSkipped(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	slotFile := filepath.Join(t.TempDir(), "active_slot")
	if err := os.WriteFile(slotFile, []byte("green\n"), 0o644); err != nil {
		t.Fatalf("write active slot file: %v", err)
	}
	s.Config.Slot = "blue"
	s.Config.ActiveSlotPath = slotFile

	payload := issuesEventPayload(t, "labeled", "owner/repo", "dev", "rascal", testIssue(7, "Title", "Body", nil, false))
	req := webhookRequest(t, payload, "issues", "delivery-slot", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for inactive slot skip, got %d", rec.Code)
	}
	if got := len(s.Store.ListRuns(10)); got != 0 {
		t.Fatalf("expected no runs when inactive slot handles webhook, got %d", got)
	}
}

func TestHandleWebhookSignatureValidation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.Config.GitHubWebhookSecret = "secret"
	payload := issuesEventPayload(t, "labeled", "owner/repo", "dev", "rascal", testIssue(7, "Title", "Body", nil, false))

	badReq := webhookRequest(t, payload, "issues", "sig-1", "wrong-secret")
	badRec := httptest.NewRecorder()
	s.HandleWebhook(badRec, badReq)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid signature, got %d", badRec.Code)
	}

	goodReq := webhookRequest(t, payload, "issues", "sig-2", "secret")
	goodRec := httptest.NewRecorder()
	s.HandleWebhook(goodRec, goodReq)
	if goodRec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for valid signature, got %d", goodRec.Code)
	}
}

func TestHandleWebhookLegacyIssueCommentRejectsRegisteredRepoWhenPRCommentsDisabled(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.Config.GitHubWebhookSecret = "legacy-secret"

	mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:             "owner/repo",
		webhookKey:           "11111111111111111111111111111111",
		enabled:              true,
		allowManual:          true,
		allowIssueLabel:      true,
		allowIssueEdit:       true,
		allowPRComment:       false,
		allowPRReview:        true,
		allowPRReviewComment: true,
	})
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:          "owner/repo#7",
		Repo:        "owner/repo",
		IssueNumber: 17,
		PRNumber:    7,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	payload := issueCommentEventPayload(
		t,
		"created",
		"owner/repo",
		"alice",
		testIssue(7, "", "", nil, true),
		ghapi.Comment{
			ID:   101,
			Body: "please fix this",
			User: ghapi.User{Login: "alice"},
		},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-legacy-comment-disabled", "legacy-secret")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", rec.Code, rec.Body.String())
	}

	var out struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Accepted {
		t.Fatal("expected accepted=false when repo policy disables pr_comment on legacy endpoint")
	}
	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == runtrigger.NamePRComment {
			t.Fatalf("expected no pr_comment run for disabled repo policy")
		}
	}
}

func TestHandleWebhookLegacyPullRequestReviewRejectsRegisteredRepoWhenReviewsDisabled(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.Config.GitHubWebhookSecret = "legacy-secret"

	mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:             "owner/repo",
		webhookKey:           "22222222222222222222222222222222",
		enabled:              true,
		allowManual:          true,
		allowIssueLabel:      true,
		allowIssueEdit:       true,
		allowPRComment:       true,
		allowPRReview:        false,
		allowPRReviewComment: true,
	})
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:          "owner/repo#11",
		Repo:        "owner/repo",
		IssueNumber: 31,
		PRNumber:    11,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	payload := pullRequestReviewEventPayload(
		t,
		"submitted",
		"owner/repo",
		"bob",
		ghapi.Review{
			ID:    303,
			Body:  "needs changes",
			State: "changes_requested",
			User:  ghapi.User{Login: "bob"},
		},
		testPullRequest(11, false, "", ""),
	)
	req := webhookRequest(t, payload, "pull_request_review", "delivery-legacy-review-disabled", "legacy-secret")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", rec.Code, rec.Body.String())
	}

	var out struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Accepted {
		t.Fatal("expected accepted=false when repo policy disables pr_review on legacy endpoint")
	}
	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == runtrigger.NamePRReview {
			t.Fatalf("expected no pr_review run for disabled repo policy")
		}
	}
}

func TestHandleWebhookScopedRejectsRegisteredRepoWhenReviewCommentsDisabled(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	repo := mustCreateRepositoryFixture(t, s, repoFixtureInput{
		fullName:             "owner/repo",
		webhookKey:           "33333333333333333333333333333333",
		webhookSecret:        "repo-secret",
		enabled:              true,
		allowManual:          true,
		allowIssueLabel:      true,
		allowIssueEdit:       true,
		allowPRComment:       true,
		allowPRReview:        true,
		allowPRReviewComment: false,
	})
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:          "owner/repo#12",
		Repo:        "owner/repo",
		IssueNumber: 44,
		PRNumber:    12,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	line := 515
	payload := pullRequestReviewCommentEventPayload(
		t,
		"created",
		"owner/repo",
		"eve",
		ghapi.ReviewComment{
			ID:   404,
			Body: "Please rename this helper",
			Path: "cmd/rascald/main.go",
			Line: &line,
			User: ghapi.User{Login: "eve"},
		},
		testPullRequest(12, false, "", ""),
		"",
	)
	req := scopedWebhookRequest(t, repo.WebhookKey, "pull_request_review_comment", "delivery-scoped-review-comment-disabled", "repo-secret", payload)
	rec := httptest.NewRecorder()
	s.HandleWebhookScoped(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", rec.Code, rec.Body.String())
	}

	var out struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Accepted {
		t.Fatal("expected accepted=false when repo policy disables pr_review_comment on scoped endpoint")
	}
	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == runtrigger.NamePRReviewComment {
			t.Fatalf("expected no pr_review_comment run for disabled repo policy")
		}
	}
}

func TestHandleWebhookIssueCommentUsesExistingPRTaskAndLastBranches(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#7"
		issueNum = 16
		prNum    = 7
		baseRef  = "develop"
		headRef  = "rascal/task-7"
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "seed_run",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "seed",
		BaseBranch:  baseRef,
		HeadBranch:  headRef,
		Trigger:     runtrigger.NameCLI,
		RunDir:      filepath.Join(t.TempDir(), "seed_run"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	markRunSucceeded(t, s, seedRun.ID)

	payload := issueCommentEventPayload(t, "created", "owner/repo", "alice",
		testIssue(7, "", "", nil, true),
		ghapi.Comment{ID: 101, Body: "  please address review notes  ", User: ghapi.User{Login: "alice"}},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.Store.ListRuns(20)
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
	task, ok := s.Store.GetTask(taskID)
	if !ok {
		t.Fatalf("expected task %q", taskID)
	}
	if task.IssueNumber != issueNum {
		t.Fatalf("task issue number = %d, want %d", task.IssueNumber, issueNum)
	}
}

func TestHandleWebhookIssueCommentEditedUsesUpdatedContext(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#17"
		issueNum = 23
		prNum    = 17
		baseRef  = "main"
		headRef  = "rascal/pr-17"
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "seed_run_edited",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "seed",
		BaseBranch:  baseRef,
		HeadBranch:  headRef,
		Trigger:     runtrigger.NameCLI,
		RunDir:      filepath.Join(t.TempDir(), "seed_run_edited"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	markRunSucceeded(t, s, seedRun.ID)

	payload := issueCommentEventPayload(t, "edited", "owner/repo", "alice",
		testIssue(17, "", "", nil, true),
		ghapi.Comment{ID: 202, Body: "  updated feedback  ", User: ghapi.User{Login: "alice"}},
		"prior feedback",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-edited", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.Store.ListRuns(20)
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	payload := issueCommentEventPayload(t, "edited", "owner/repo", "alice",
		testIssue(9, "", "", nil, true),
		ghapi.Comment{ID: 303, Body: "  same feedback  ", User: ghapi.User{Login: "alice"}},
		"same feedback",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-nochange", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == "pr_comment" {
			t.Fatalf("expected no pr_comment run for unchanged edit")
		}
	}
}

func TestHandleWebhookIssueCommentIgnoresUnmanagedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	payload := issueCommentEventPayload(t, "created", "owner/repo", "alice",
		testIssue(44, "", "", nil, true),
		ghapi.Comment{ID: 707, Body: "please fix this", User: ghapi.User{Login: "alice"}},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-unmanaged", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == "pr_comment" {
			t.Fatalf("expected no pr_comment run for unmanaged pr")
		}
	}
	if got := fakeGH.postedIssueCommentReactions(); len(got) != 0 {
		t.Fatalf("expected no issue comment reactions for unmanaged pr, got %+v", got)
	}
}

func TestHandleWebhookIssueCommentIgnoresClosedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	taskID := "owner/repo#44"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", IssueNumber: 44, PRNumber: 44}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	closedPRIssue := testIssue(44, "", "", nil, true)
	closedPRIssue.State = "closed"
	payload := issueCommentEventPayload(t, "created", "owner/repo", "alice",
		closedPRIssue,
		ghapi.Comment{ID: 808, Body: "please fix this", User: ghapi.User{Login: "alice"}},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-closed-pr", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == "pr_comment" {
			t.Fatalf("expected no pr_comment run for closed pr")
		}
	}
	if got := fakeGH.postedIssueCommentReactions(); len(got) != 0 {
		t.Fatalf("expected no issue comment reactions for closed pr, got %+v", got)
	}
}

func TestHandleWebhookIssueCommentResolvesPRBranchWithoutPriorRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#96"
		issueNum = 96
		prNum    = 96
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	s.GitHub = &fakeGitHubClient{
		pullData: ghapi.PullRequest{
			Number: prNum,
			Base: struct {
				Ref string `json:"ref"`
			}{Ref: "main"},
			Head: struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			}{Ref: "add-goreleaser"},
		},
	}

	payload := issueCommentEventPayload(t, "created", "owner/repo", "alice",
		testIssue(96, "", "", nil, true),
		ghapi.Comment{ID: 101, Body: "please address review notes", User: ghapi.User{Login: "alice"}},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-resolve-branch", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.Store.ListRuns(20)
		for _, run := range runs {
			if run.Trigger == "pr_comment" {
				got = run
				return true
			}
		}
		return false
	}, "pr_comment run created with resolved branch")

	if got.BaseBranch != "main" {
		t.Fatalf("base branch = %q, want main", got.BaseBranch)
	}
	if got.HeadBranch != "add-goreleaser" {
		t.Fatalf("head branch = %q, want add-goreleaser", got.HeadBranch)
	}
}

func TestHandleWebhookPullRequestReviewUsesStateFallbackContext(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#11"
		issueNum = 31
		prNum    = 11
		baseRef  = "main"
		headRef  = "rascal/pr-11"
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "seed_review",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "seed",
		BaseBranch:  baseRef,
		HeadBranch:  headRef,
		Trigger:     runtrigger.NameCLI,
		RunDir:      filepath.Join(t.TempDir(), "seed_review"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	markRunSucceeded(t, s, seedRun.ID)

	payload := pullRequestReviewEventPayload(t, "submitted", "owner/repo", "bob",
		ghapi.Review{ID: 303, Body: "   ", State: "changes_requested", User: ghapi.User{Login: "bob"}},
		testPullRequest(11, false, "", ""),
	)
	req := webhookRequest(t, payload, "pull_request_review", "delivery-review", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.Store.ListRuns(20)
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

func TestHandleWebhookPullRequestReviewUsesPayloadPRBranchesWithoutPriorRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#97"
		issueNum = 23
		prNum    = 97
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	payload := pullRequestReviewEventPayload(t, "submitted", "owner/repo", "bob",
		ghapi.Review{ID: 303, Body: "needs a small fix", State: "changes_requested", User: ghapi.User{Login: "bob"}},
		testPullRequest(97, false, "main", "add-goreleaser"),
	)
	req := webhookRequest(t, payload, "pull_request_review", "delivery-review-payload-branch", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.Store.ListRuns(20)
		for _, run := range runs {
			if run.Trigger == "pr_review" {
				got = run
				return true
			}
		}
		return false
	}, "pr_review run created with payload branch")

	if got.BaseBranch != "main" {
		t.Fatalf("base branch = %q, want main", got.BaseBranch)
	}
	if got.HeadBranch != "add-goreleaser" {
		t.Fatalf("head branch = %q, want add-goreleaser", got.HeadBranch)
	}
}

func TestHandleWebhookPullRequestReviewIgnoresUnmanagedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	payload := pullRequestReviewEventPayload(t, "submitted", "owner/repo", "bob",
		ghapi.Review{ID: 808, Body: "needs changes", State: "changes_requested", User: ghapi.User{Login: "bob"}},
		testPullRequest(45, false, "", ""),
	)
	req := webhookRequest(t, payload, "pull_request_review", "delivery-review-unmanaged", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#12"
		issueNum = 44
		prNum    = 12
		baseRef  = "main"
		headRef  = "rascal/pr-12"
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "seed_review_comment",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "seed",
		BaseBranch:  baseRef,
		HeadBranch:  headRef,
		Trigger:     runtrigger.NameCLI,
		RunDir:      filepath.Join(t.TempDir(), "seed_review_comment"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	markRunSucceeded(t, s, seedRun.ID)

	line515 := 515
	startLine512 := 512
	payload := pullRequestReviewCommentEventPayload(t, "created", "owner/repo", "eve",
		ghapi.ReviewComment{ID: 404, Body: "Please rename this helper", Path: "cmd/rascald/main.go", Line: &line515, StartLine: &startLine512, User: ghapi.User{Login: "eve"}},
		testPullRequest(12, false, "", ""),
		"",
	)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.Store.ListRuns(20)
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#13"
		issueNum = 45
		prNum    = 13
		baseRef  = "main"
		headRef  = "rascal/pr-13"
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "seed_review_comment_edited",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "seed",
		BaseBranch:  baseRef,
		HeadBranch:  headRef,
		Trigger:     runtrigger.NameCLI,
		RunDir:      filepath.Join(t.TempDir(), "seed_review_comment_edited"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	markRunSucceeded(t, s, seedRun.ID)

	line600 := 600
	payload := pullRequestReviewCommentEventPayload(t, "edited", "owner/repo", "eve",
		ghapi.ReviewComment{ID: 505, Body: "Refined inline feedback", Path: "cmd/rascald/main.go", Line: &line600, User: ghapi.User{Login: "eve"}},
		testPullRequest(13, false, "", ""),
		"Old inline feedback",
	)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment-edited", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.Store.ListRuns(20)
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	line601 := 601
	payload := pullRequestReviewCommentEventPayload(t, "edited", "owner/repo", "eve",
		ghapi.ReviewComment{ID: 506, Body: "  same inline feedback  ", Path: "cmd/rascald/main.go", Line: &line601, User: ghapi.User{Login: "eve"}},
		testPullRequest(13, false, "", ""),
		"same inline feedback",
	)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment-nochange", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == "pr_review_comment" {
			t.Fatalf("expected no pr_review_comment run for unchanged edit")
		}
	}
}

func TestHandleWebhookPullRequestReviewCommentIgnoresUnmanagedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	line515 := 515
	payload := pullRequestReviewCommentEventPayload(t, "created", "owner/repo", "eve",
		ghapi.ReviewComment{ID: 909, Body: "Please rename this helper", Path: "cmd/rascald/main.go", Line: &line515, User: ghapi.User{Login: "eve"}},
		testPullRequest(46, false, "", ""),
		"",
	)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment-unmanaged", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo     = "owner/repo"
		taskID   = "owner/repo#14"
		issueNum = 46
		prNum    = 14
		baseRef  = "main"
		headRef  = "rascal/pr-14"
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: issueNum, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	seedRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "seed_review_thread",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "seed",
		BaseBranch:  baseRef,
		HeadBranch:  headRef,
		Trigger:     runtrigger.NameCLI,
		RunDir:      filepath.Join(t.TempDir(), "seed_review_thread"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	markRunSucceeded(t, s, seedRun.ID)

	line777 := 777
	startLine775 := 775
	payload := pullRequestReviewThreadEventPayload(t, "unresolved", "owner/repo", "eve",
		ghapi.ReviewThread{
			ID:        12,
			Path:      "cmd/rascald/main.go",
			Line:      &line777,
			StartLine: &startLine775,
			Comments:  []ghapi.ReviewComment{{ID: 1, Body: "Please split this logic", User: ghapi.User{Login: "eve"}}},
		},
		testPullRequest(14, false, "", ""),
	)
	req := webhookRequest(t, payload, "pull_request_review_thread", "delivery-review-thread-unresolved", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var got state.Run
	waitFor(t, time.Second, func() bool {
		runs := s.Store.ListRuns(20)
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	const (
		repo   = "owner/repo"
		taskID = "owner/repo#15"
		prNum  = 15
	)
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: repo, IssueNumber: 47, PRNumber: prNum}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	threadRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "queued_review_thread",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "Address unresolved review thread",
		Trigger:     "pr_review_thread",
		RunDir:      filepath.Join(t.TempDir(), "queued_review_thread"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed thread run: %v", err)
	}
	if err := os.MkdirAll(threadRun.RunDir, 0o755); err != nil {
		t.Fatalf("create thread run dir: %v", err)
	}
	if err := s.WriteRunResponseTarget(threadRun, &orchestrator.RunResponseTarget{
		Repo:           repo,
		IssueNumber:    prNum,
		Trigger:        "pr_review_thread",
		ReviewThreadID: 13,
	}); err != nil {
		t.Fatalf("write thread run target: %v", err)
	}
	otherThreadRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "queued_review_thread_other",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "Address another unresolved review thread",
		Trigger:     "pr_review_thread",
		RunDir:      filepath.Join(t.TempDir(), "queued_review_thread_other"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed other thread run: %v", err)
	}
	if err := os.MkdirAll(otherThreadRun.RunDir, 0o755); err != nil {
		t.Fatalf("create other thread run dir: %v", err)
	}
	if err := s.WriteRunResponseTarget(otherThreadRun, &orchestrator.RunResponseTarget{
		Repo:           repo,
		IssueNumber:    prNum,
		Trigger:        "pr_review_thread",
		ReviewThreadID: 99,
	}); err != nil {
		t.Fatalf("write other thread run target: %v", err)
	}
	otherRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "queued_pr_comment",
		TaskID:      taskID,
		Repo:        repo,
		Instruction: "Address PR feedback",
		Trigger:     "pr_comment",
		RunDir:      filepath.Join(t.TempDir(), "queued_pr_comment"),
		PRNumber:    prNum,
	})
	if err != nil {
		t.Fatalf("seed comment run: %v", err)
	}

	line800 := 800
	payload := pullRequestReviewThreadEventPayload(t, "resolved", "owner/repo", "eve",
		ghapi.ReviewThread{ID: 13, Path: "cmd/rascald/main.go", Line: &line800},
		testPullRequest(15, false, "", ""),
	)
	req := webhookRequest(t, payload, "pull_request_review_thread", "delivery-review-thread-resolved", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	updatedThreadRun, ok := s.Store.GetRun(threadRun.ID)
	if !ok {
		t.Fatalf("missing run %s", threadRun.ID)
	}
	if updatedThreadRun.Status != state.StatusCanceled {
		t.Fatalf("thread run status = %s, want %s", updatedThreadRun.Status, state.StatusCanceled)
	}

	updatedOtherThreadRun, ok := s.Store.GetRun(otherThreadRun.ID)
	if !ok {
		t.Fatalf("missing run %s", otherThreadRun.ID)
	}
	if updatedOtherThreadRun.Status != state.StatusQueued {
		t.Fatalf("non-matching thread run status = %s, want %s", updatedOtherThreadRun.Status, state.StatusQueued)
	}

	updatedOtherRun, ok := s.Store.GetRun(otherRun.ID)
	if !ok {
		t.Fatalf("missing run %s", otherRun.ID)
	}
	if updatedOtherRun.Status != state.StatusQueued {
		t.Fatalf("non-thread run status = %s, want %s", updatedOtherRun.Status, state.StatusQueued)
	}
}

func TestCreateAndQueueRunDoesNotCreateRunDirWhenEnqueueFails(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})

	runsDir := filepath.Join(s.Config.DataDir, "runs")
	if err := s.Store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	_, err := s.CreateAndQueueRun(orchestrator.RunRequest{
		TaskID:      "owner/repo#101",
		Repo:        "owner/repo",
		Instruction: "fail before enqueue persists",
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	payload := issueCommentEventPayload(t, "created", "owner/repo", "human",
		testIssue(9, "", "", nil, true),
		ghapi.Comment{ID: 501, Body: "please fix", User: ghapi.User{Login: "rascal[bot]"}},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-bot", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if got := len(s.Store.ListRuns(10)); got != 0 {
		t.Fatalf("expected zero runs for bot-authored comment, got %d", got)
	}
}

func TestHandleWebhookIssueCommentIgnoresRascalAutomationComment(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	payload := issueCommentEventPayload(t, "created", "owner/repo", "rascal",
		testIssue(9, "", "", nil, true),
		ghapi.Comment{ID: 502, Body: "<!-- rascal:completion-comment -->\n\nRascal run `run_123` completed in 12s.", User: ghapi.User{Login: "rascal"}},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-automation", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if got := len(s.Store.ListRuns(10)); got != 0 {
		t.Fatalf("expected zero runs for rascal automation comment, got %d", got)
	}
}

func TestHandleWebhookIssueCommentIgnoresNonOwnerPRFollowup(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.Config.GitHubOwnerLogin = "owner"
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	taskID := "owner/repo#44"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", IssueNumber: 44, PRNumber: 44}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	payload := issueCommentEventPayload(t, "created", "owner/repo", "alice",
		testIssue(44, "", "", nil, true),
		ghapi.Comment{ID: 901, Body: "please fix this", User: ghapi.User{Login: "alice"}},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-non-owner", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == runtrigger.NamePRComment {
			t.Fatalf("expected no pr_comment run for non-owner")
		}
	}
	if got := fakeGH.postedIssueCommentReactions(); len(got) != 0 {
		t.Fatalf("expected no issue comment reactions for non-owner, got %+v", got)
	}
}

func TestHandleWebhookIssueCommentAllowsOwnerPRFollowup(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.Config.GitHubOwnerLogin = "owner"

	taskID := "owner/repo#45"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", IssueNumber: 45, PRNumber: 45}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	payload := issueCommentEventPayload(t, "created", "owner/repo", "owner",
		testIssue(45, "", "", nil, true),
		ghapi.Comment{ID: 902, Body: "please fix this", User: ghapi.User{Login: "owner"}},
		"",
	)
	req := webhookRequest(t, payload, "issue_comment", "delivery-comment-owner", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool {
		for _, run := range s.Store.ListRuns(10) {
			if run.Trigger == runtrigger.NamePRComment {
				return true
			}
		}
		return false
	}, "pr_comment run created for owner")
}

func TestHandleWebhookPullRequestReviewIgnoresNonOwnerPRFollowup(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.Config.GitHubOwnerLogin = "owner"
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: "owner/repo#46", Repo: "owner/repo", IssueNumber: 46, PRNumber: 46}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	payload := pullRequestReviewEventPayload(t, "submitted", "owner/repo", "alice",
		ghapi.Review{ID: 903, Body: "needs changes", State: "changes_requested", User: ghapi.User{Login: "alice"}},
		testPullRequest(46, false, "", ""),
	)
	req := webhookRequest(t, payload, "pull_request_review", "delivery-review-non-owner", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == runtrigger.NamePRReview {
			t.Fatalf("expected no pr_review run for non-owner")
		}
	}
	if got := fakeGH.postedPullRequestReviewReactions(); len(got) != 0 {
		t.Fatalf("expected no review reactions for non-owner, got %+v", got)
	}
}

func TestHandleWebhookPullRequestReviewCommentIgnoresNonOwnerPRFollowup(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.Config.GitHubOwnerLogin = "owner"
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: "owner/repo#47", Repo: "owner/repo", IssueNumber: 47, PRNumber: 47}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	line515 := 515
	payload := pullRequestReviewCommentEventPayload(t, "created", "owner/repo", "alice",
		ghapi.ReviewComment{ID: 904, Body: "Please rename this helper", Path: "cmd/rascald/main.go", Line: &line515, User: ghapi.User{Login: "alice"}},
		testPullRequest(47, false, "", ""),
		"",
	)
	req := webhookRequest(t, payload, "pull_request_review_comment", "delivery-review-comment-non-owner", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == runtrigger.NamePRReviewComment {
			t.Fatalf("expected no pr_review_comment run for non-owner")
		}
	}
	if got := fakeGH.postedPullRequestReviewCommentReactions(); len(got) != 0 {
		t.Fatalf("expected no review comment reactions for non-owner, got %+v", got)
	}
}

func TestHandleWebhookPullRequestReviewThreadIgnoresNonOwnerPRFollowup(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	s.Config.GitHubOwnerLogin = "owner"

	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: "owner/repo#48", Repo: "owner/repo", IssueNumber: 48, PRNumber: 48}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	line777 := 777
	startLine775 := 775
	payload := pullRequestReviewThreadEventPayload(t, "unresolved", "owner/repo", "alice",
		ghapi.ReviewThread{
			ID:        905,
			Path:      "cmd/rascald/main.go",
			Line:      &line777,
			StartLine: &startLine775,
			Comments:  []ghapi.ReviewComment{{ID: 1, Body: "Please split this logic", User: ghapi.User{Login: "alice"}}},
		},
		testPullRequest(48, false, "", ""),
	)
	req := webhookRequest(t, payload, "pull_request_review_thread", "delivery-review-thread-non-owner", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	for _, run := range s.Store.ListRuns(10) {
		if run.Trigger == runtrigger.NamePRReviewThread {
			t.Fatalf("expected no pr_review_thread run for non-owner")
		}
	}
}
func TestMergedPRMarksTaskCompleteAndCancelsQueuedRuns(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)
	defer close(waitCh)
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"
	taskID := "owner/repo#123"

	_, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: taskID, Repo: "owner/repo", Instruction: "first", PRNumber: 55})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	queuedRun, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: taskID, Repo: "owner/repo", Instruction: "queued", PRNumber: 55})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")

	if err := s.Store.SetTaskPR(taskID, "owner/repo", 55); err != nil {
		t.Fatalf("set task pr: %v", err)
	}
	awaitingRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_awaiting_merge",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "await merge",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 55,
		PRNumber:    55,
	})
	if err != nil {
		t.Fatalf("add awaiting run: %v", err)
	}
	markRunReview(t, s, awaitingRun.ID)

	payload := pullRequestEventPayload(t, "closed", "owner/repo", "dev", testPullRequest(55, true, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-merged", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for merged pr event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return s.Store.IsTaskCompleted(taskID) }, "task marked completed")
	waitFor(t, time.Second, func() bool {
		r, ok := s.Store.GetRun(queuedRun.ID)
		return ok && r.Status == state.StatusCanceled
	}, "queued run canceled")
	waitFor(t, time.Second, func() bool {
		r, ok := s.Store.GetRun(awaitingRun.ID)
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	taskID := "owner/repo#123"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 55}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_case_insensitive_merge",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "await merge",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 55,
		PRNumber:    55,
	})
	if err != nil {
		t.Fatalf("add awaiting run: %v", err)
	}
	markRunReview(t, s, run.ID)

	payload := pullRequestEventPayload(t, "closed", "Owner/Repo", "dev", testPullRequest(55, true, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-merged-mixed-case", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for mixed-case merged pr event, got %d", rec.Code)
	}

	waitFor(t, time.Second, func() bool { return s.Store.IsTaskCompleted(taskID) }, "task marked completed")
	waitFor(t, time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "awaiting-feedback run marked succeeded on merge")
}

func TestPullRequestClosedIgnoresUnmanagedPR(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"

	payload := pullRequestEventPayload(t, "closed", "owner/repo", "dev", testPullRequest(456, true, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-merged-unmanaged", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for unmanaged pr event, got %d", rec.Code)
	}

	if _, ok := s.Store.FindTaskByPR("owner/repo", 456); ok {
		t.Fatal("expected no task to be created for unmanaged pr")
	}
	if got := fakeGH.postedReactions(); len(got) != 0 {
		t.Fatalf("expected no issue reactions for unmanaged pr, got %+v", got)
	}
}

func TestClosedUnmergedPRCancelsAwaitingFeedbackRuns(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	fakeGH := &fakeGitHubClient{}
	s.GitHub = fakeGH
	s.Config.GitHubToken = "token"
	taskID := "owner/repo#987"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 99}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_unmerged",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "wait for merge",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 99,
		PRNumber:    99,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	markRunReview(t, s, run.ID)

	payload := pullRequestEventPayload(t, "closed", "owner/repo", "dev", testPullRequest(99, false, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-closed-unmerged", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for closed unmerged pr event, got %d", rec.Code)
	}

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if updated.Status != state.StatusCanceled {
		t.Fatalf("expected canceled status, got %s", updated.Status)
	}
	if updated.StatusReason != state.RunStatusReasonPRClosed {
		t.Fatalf("expected status reason %q, got %q", state.RunStatusReasonPRClosed, updated.StatusReason)
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

func TestClosedUnmergedPRCancelsRunningRuns(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	taskID := "owner/repo#1001"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 1001}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "work in progress",
		IssueNumber: 1001,
		PRNumber:    1001,
		Trigger:     runtrigger.NamePRComment,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	payload := pullRequestEventPayload(t, "closed", "owner/repo", "dev", testPullRequest(1001, false, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-closed-running-pr", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for closed unmerged pr event, got %d", rec.Code)
	}

	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusCanceled
	}, "running pr run canceled")
	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if updated.StatusReason != state.RunStatusReasonPRClosed {
		t.Fatalf("expected status reason %q, got %q", state.RunStatusReasonPRClosed, updated.StatusReason)
	}
	if !strings.Contains(updated.Error, "pull request closed") {
		t.Fatalf("expected closed pr cancel reason, got %q", updated.Error)
	}
}

func TestClosedUnmergedEventDoesNotDowngradeMergedRunState(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	taskID := "owner/repo#321"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 321}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_merged_guard",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "already merged",
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
	if _, err := s.Store.UpdateRun(run.ID, func(r *state.Run) error {
		r.PRStatus = state.PRStatusMerged
		return nil
	}); err != nil {
		t.Fatalf("set merged pr status: %v", err)
	}

	payload := pullRequestEventPayload(t, "closed", "owner/repo", "dev", testPullRequest(321, false, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-stale-closed", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for stale closed event, got %d", rec.Code)
	}

	updated, ok := s.Store.GetRun(run.ID)
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
	s := newTestServer(t, &fakeRunner{})
	taskID := "owner/repo#654"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 654}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_reopened_guard",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "already merged",
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
	if _, err := s.Store.UpdateRun(run.ID, func(r *state.Run) error {
		r.PRStatus = state.PRStatusMerged
		return nil
	}); err != nil {
		t.Fatalf("set merged pr status: %v", err)
	}

	payload := pullRequestEventPayload(t, "reopened", "owner/repo", "dev", testPullRequest(654, false, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-stale-reopened", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for stale reopened event, got %d", rec.Code)
	}

	updated, ok := s.Store.GetRun(run.ID)
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

func TestConvertedToDraftCancelsRunningRunsAndMarksTaskDraft(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	taskID := "owner/repo#1200"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 1200}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "work in progress",
		IssueNumber: 1200,
		PRNumber:    1200,
		Trigger:     runtrigger.NamePRComment,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	payload := pullRequestEventPayload(t, "converted_to_draft", "owner/repo", "dev", testPullRequest(1200, false, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-draft-running-pr", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for converted_to_draft event, got %d", rec.Code)
	}

	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusCanceled
	}, "running pr run canceled for draft conversion")
	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if updated.StatusReason != state.RunStatusReasonPRDraft {
		t.Fatalf("expected status reason %q, got %q", state.RunStatusReasonPRDraft, updated.StatusReason)
	}
	if !strings.Contains(updated.Error, "converted to draft") {
		t.Fatalf("expected draft cancel reason, got %q", updated.Error)
	}
	task, ok := s.Store.FindTaskByPR("owner/repo", 1200)
	if !ok {
		t.Fatal("expected task for draft PR")
	}
	if !task.PRDraft {
		t.Fatal("expected task to be marked draft")
	}
}

func TestReadyForReviewResumesQueuedDraftPRRuns(t *testing.T) {
	t.Parallel()
	launcher := &fakeRunner{}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	taskID := "owner/repo#1300"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 1300, PRDraft: true}); err != nil {
		t.Fatalf("upsert draft task: %v", err)
	}
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_draft_resume",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "resume when ready",
		Trigger:     runtrigger.NamePRComment,
		RunDir:      t.TempDir(),
		IssueNumber: 1300,
		PRNumber:    1300,
	})
	if err != nil {
		t.Fatalf("add queued run: %v", err)
	}

	s.ScheduleRuns("")
	queued, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if queued.Status != state.StatusQueued {
		t.Fatalf("draft run status after schedule = %s, want queued", queued.Status)
	}
	if calls := launcher.Calls(); calls != 0 {
		t.Fatalf("expected no launches while draft, got %d", calls)
	}

	payload := pullRequestEventPayload(t, "ready_for_review", "owner/repo", "dev", testPullRequest(1300, false, "", ""))
	req := webhookRequest(t, payload, "pull_request", "delivery-ready-for-review", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for ready_for_review event, got %d", rec.Code)
	}

	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status != state.StatusQueued && launcher.Calls() == 1
	}, "draft pr queued run resumed after ready_for_review")
	task, ok := s.Store.FindTaskByPR("owner/repo", 1300)
	if !ok {
		t.Fatal("expected task for ready PR")
	}
	if task.PRDraft {
		t.Fatal("expected task draft flag to be cleared")
	}
	if calls := launcher.Calls(); calls != 1 {
		t.Fatalf("expected exactly one launch after ready_for_review, got %d", calls)
	}
}

func TestPullRequestSynchronizeCancelsQueuedRunAndQueuesReplacement(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	taskID := "owner/repo#1350"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 1350}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	staleRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_pr_sync_stale",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "Address PR #1350 feedback",
		Trigger:     runtrigger.NamePRComment,
		RunDir:      t.TempDir(),
		IssueNumber: 1350,
		PRNumber:    1350,
		BaseBranch:  "main",
		HeadBranch:  "rascal/owner-repo-1350-old",
		HeadSHA:     "old-sha",
	})
	if err != nil {
		t.Fatalf("add stale run: %v", err)
	}

	pr := testPullRequest(1350, false, "main", "rascal/owner-repo-1350-new")
	pr.Head.SHA = "new-sha"
	payload := pullRequestEventPayload(t, "synchronize", "owner/repo", "dev", pr)
	req := webhookRequest(t, payload, "pull_request", "delivery-pr-synchronize-queued", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for synchronize event, got %d", rec.Code)
	}

	waitFor(t, 3*time.Second, func() bool {
		return len(s.Store.ListRuns(10)) == 2
	}, "replacement run created after synchronize")

	updated, ok := s.Store.GetRun(staleRun.ID)
	if !ok {
		t.Fatalf("run %s not found", staleRun.ID)
	}
	if updated.Status != state.StatusCanceled {
		t.Fatalf("stale run status = %s, want canceled", updated.Status)
	}
	if updated.StatusReason != state.RunStatusReasonPRSynchronized {
		t.Fatalf("stale run status reason = %q, want %q", updated.StatusReason, state.RunStatusReasonPRSynchronized)
	}

	replacement, ok := s.Store.LastRunForTask(taskID)
	if !ok {
		t.Fatal("expected replacement run")
	}
	if replacement.ID == staleRun.ID {
		t.Fatal("expected a new replacement run, got stale run")
	}
	if replacement.Trigger != runtrigger.NamePRSynchronize {
		t.Fatalf("trigger = %q, want %q", replacement.Trigger, runtrigger.NamePRSynchronize)
	}
	if replacement.BaseBranch != "main" || replacement.HeadBranch != "rascal/owner-repo-1350-new" {
		t.Fatalf("replacement branches = %q -> %q, want main -> rascal/owner-repo-1350-new", replacement.BaseBranch, replacement.HeadBranch)
	}
	if replacement.HeadSHA != "new-sha" {
		t.Fatalf("replacement head sha = %q, want new-sha", replacement.HeadSHA)
	}
	if replacement.Instruction != staleRun.Instruction {
		t.Fatalf("replacement instruction = %q, want %q", replacement.Instruction, staleRun.Instruction)
	}
}

func TestPullRequestSynchronizeCancelsRunningRunAndQueuesReplacement(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	taskID := "owner/repo#1351"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", PRNumber: 1351}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "Address PR #1351 feedback",
		IssueNumber: 1351,
		PRNumber:    1351,
		Trigger:     runtrigger.NamePRComment,
		BaseBranch:  "main",
		HeadBranch:  "rascal/owner-repo-1351-old",
		HeadSHA:     "old-sha",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	pr := testPullRequest(1351, false, "main", "rascal/owner-repo-1351-new")
	pr.Head.SHA = "new-sha"
	payload := pullRequestEventPayload(t, "synchronize", "owner/repo", "dev", pr)
	req := webhookRequest(t, payload, "pull_request", "delivery-pr-synchronize-running", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for synchronize event, got %d", rec.Code)
	}

	waitFor(t, 3*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusCanceled && launcher.Calls() == 2
	}, "running synchronize run canceled and replacement launched")

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if updated.StatusReason != state.RunStatusReasonPRSynchronized {
		t.Fatalf("status reason = %q, want %q", updated.StatusReason, state.RunStatusReasonPRSynchronized)
	}
	if !strings.Contains(updated.Error, "synchronized") {
		t.Fatalf("expected synchronized cancel reason, got %q", updated.Error)
	}

	replacement, ok := s.Store.LastRunForTask(taskID)
	if !ok {
		t.Fatal("expected replacement run")
	}
	if replacement.ID == run.ID {
		t.Fatal("expected a new replacement run, got original run")
	}
	if replacement.Trigger != runtrigger.NamePRSynchronize {
		t.Fatalf("trigger = %q, want %q", replacement.Trigger, runtrigger.NamePRSynchronize)
	}
	if replacement.HeadBranch != "rascal/owner-repo-1351-new" || replacement.HeadSHA != "new-sha" {
		t.Fatalf("replacement branch/sha = %q / %q, want rascal/owner-repo-1351-new / new-sha", replacement.HeadBranch, replacement.HeadSHA)
	}
}

func TestHandleWebhookCheckRunFailedQueuesManagedPRRepairRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	taskID := "owner/repo#1400"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", IssueNumber: 1400, PRNumber: 1401}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	payload := checkRunEventPayload(t, "completed", "owner/repo", "github-actions[bot]", ghapi.CheckRun{
		Name:       "test",
		Status:     "completed",
		Conclusion: "failure",
		HeadSHA:    "abc123",
		CheckSuite: ghapi.CheckSuiteRef{HeadBranch: "rascal/task-1400"},
		PullRequests: []ghapi.CheckPullRequest{{
			Number: 1401,
			Base: struct {
				Ref string `json:"ref"`
			}{Ref: "main"},
			Head: struct {
				Ref string `json:"ref"`
			}{Ref: "rascal/task-1400"},
		}},
		Output: ghapi.CheckOutput{Summary: "tests failed"},
	})
	req := webhookRequest(t, payload, "check_run", "delivery-check-run-failed", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for failed check_run event, got %d", rec.Code)
	}

	run, ok := s.Store.LastRunForTask(taskID)
	if !ok {
		t.Fatal("expected ci repair run to be queued")
	}
	if run.Trigger != runtrigger.NamePRCheckFailure {
		t.Fatalf("trigger = %q, want %q", run.Trigger, runtrigger.NamePRCheckFailure)
	}
	if run.PRNumber != 1401 {
		t.Fatalf("pr number = %d, want 1401", run.PRNumber)
	}
	if run.HeadSHA != "abc123" {
		t.Fatalf("head sha = %q, want %q", run.HeadSHA, "abc123")
	}
	if !strings.Contains(run.Context, "tests failed") {
		t.Fatalf("expected check output in context, got %q", run.Context)
	}
}

func TestHandleWebhookCheckSuiteFailedQueuesBranchRepairRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	taskID := "owner/repo#1500"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", IssueNumber: 1500}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	existingRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_existing_branch",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "existing work",
		Trigger:     runtrigger.NameIssueLabel,
		RunDir:      t.TempDir(),
		IssueNumber: 1500,
		HeadBranch:  "rascal/task-1500-aaaa",
		BaseBranch:  "main",
	})
	if err != nil {
		t.Fatalf("add existing run: %v", err)
	}
	markRunSucceeded(t, s, existingRun.ID)

	payload := checkSuiteEventPayload(t, "completed", "owner/repo", "github-actions[bot]", ghapi.CheckSuite{
		Status:     "completed",
		Conclusion: "failure",
		HeadBranch: "rascal/task-1500-aaaa",
		HeadSHA:    "def456",
	})
	req := webhookRequest(t, payload, "check_suite", "delivery-check-suite-failed", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for failed check_suite event, got %d", rec.Code)
	}

	run, ok := s.Store.LastRunForTask(taskID)
	if !ok {
		t.Fatal("expected branch repair run to be queued")
	}
	if run.Trigger != runtrigger.NamePRCheckFailure {
		t.Fatalf("trigger = %q, want %q", run.Trigger, runtrigger.NamePRCheckFailure)
	}
	if run.HeadBranch != "rascal/task-1500-aaaa" {
		t.Fatalf("head branch = %q, want rascal/task-1500-aaaa", run.HeadBranch)
	}
	if run.HeadSHA != "def456" {
		t.Fatalf("head sha = %q, want %q", run.HeadSHA, "def456")
	}
}

func TestHandleWebhookCheckRunFailedSkipsDuplicateHeadSHA(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	taskID := "owner/repo#1600"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", IssueNumber: 1600, PRNumber: 1601}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_previous_check_failure",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "previous ci repair",
		Trigger:     runtrigger.NamePRCheckFailure,
		RunDir:      t.TempDir(),
		IssueNumber: 1600,
		PRNumber:    1601,
		HeadBranch:  "rascal/task-1600",
		BaseBranch:  "main",
	})
	if err != nil {
		t.Fatalf("add previous run: %v", err)
	}
	if _, err := s.Store.UpdateRun(run.ID, func(r *state.Run) error {
		r.HeadSHA = "same-sha"
		return nil
	}); err != nil {
		t.Fatalf("set head sha: %v", err)
	}
	markRunSucceeded(t, s, run.ID)

	payload := checkRunEventPayload(t, "completed", "owner/repo", "github-actions[bot]", ghapi.CheckRun{
		Name:       "test",
		Status:     "completed",
		Conclusion: "failure",
		HeadSHA:    "same-sha",
		CheckSuite: ghapi.CheckSuiteRef{HeadBranch: "rascal/task-1600"},
		PullRequests: []ghapi.CheckPullRequest{{
			Number: 1601,
		}},
	})
	req := webhookRequest(t, payload, "check_run", "delivery-check-run-duplicate", "")
	rec := httptest.NewRecorder()
	s.HandleWebhook(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for duplicate failed check_run event, got %d", rec.Code)
	}

	runs := s.Store.ListRuns(100)
	count := 0
	for _, current := range runs {
		if current.TaskID == taskID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("task run count = %d, want 1", count)
	}
}

func TestHandleTaskSubresourcesGet(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	taskID := "owner/repo#22"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:          taskID,
		Repo:        "owner/repo",
		IssueNumber: 22,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/owner%2Frepo%2322", nil)
	rec := httptest.NewRecorder()
	s.HandleTaskSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHandleCreateTaskRespectsProvidedTaskID(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		strings.NewReader(`{"task_id":"owner/repo#99","repo":"owner/repo","task":"follow-up","base_branch":"main"}`),
	)
	rec := httptest.NewRecorder()
	s.HandleCreateTask(rec, req)
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
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		strings.NewReader(`{"task_id":"owner/repo#100","repo":"owner/repo","task":"quiet debug","base_branch":"main","debug":false}`),
	)
	rec := httptest.NewRecorder()
	s.HandleCreateTask(rec, req)
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

func TestHandleCreateTaskRejectsInvalidTrigger(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		strings.NewReader(`{"repo":"owner/repo","task":"bad trigger","trigger":"issue"}`),
	)
	rec := httptest.NewRecorder()
	s.HandleCreateTask(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleCreateTaskRetryHydratesIssueContextFromSourceRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	taskID := "owner/repo#170"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:          taskID,
		Repo:        "owner/repo",
		IssueNumber: 170,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	sourceRunDir := filepath.Join(s.Config.DataDir, "runs", "run_source_issue")
	if err := os.MkdirAll(sourceRunDir, 0o755); err != nil {
		t.Fatalf("mkdir source run dir: %v", err)
	}
	sourceRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_source_issue",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "Fix retry context",
		BaseBranch:  "main",
		HeadBranch:  "rascal/owner-repo-170-source",
		Trigger:     runtrigger.NameIssueAPI,
		RunDir:      sourceRunDir,
		IssueNumber: 170,
	})
	if err != nil {
		t.Fatalf("add source run: %v", err)
	}
	if err := s.WriteRunResponseTarget(sourceRun, &orchestrator.RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 170,
		RequestedBy: "alice",
		Trigger:     runtrigger.NameIssueAPI,
	}); err != nil {
		t.Fatalf("write source response target: %v", err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		strings.NewReader(`{"task_id":"owner/repo#170","source_run_id":"run_source_issue","repo":"owner/repo","task":"Fix retry context","base_branch":"main","trigger":"retry"}`),
	)
	rec := httptest.NewRecorder()
	s.HandleCreateTask(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", rec.Code, rec.Body.String())
	}

	var out struct {
		Run state.Run `json:"run"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Run.IssueNumber != 170 {
		t.Fatalf("issue_number = %d, want 170", out.Run.IssueNumber)
	}
	if out.Run.HeadBranch != "rascal/owner-repo-170-source" {
		t.Fatalf("head_branch = %q, want rascal/owner-repo-170-source", out.Run.HeadBranch)
	}
	target, ok, err := s.Store.GetRunResponseTarget(out.Run.ID)
	if err != nil {
		t.Fatalf("get retry response target: %v", err)
	}
	if !ok {
		t.Fatal("expected retry response target")
	}
	if target.IssueNumber != 170 {
		t.Fatalf("response target issue = %d, want 170", target.IssueNumber)
	}
	if target.Trigger != runtrigger.NameIssueAPI {
		t.Fatalf("response target trigger = %q, want %q", target.Trigger, runtrigger.NameIssueAPI)
	}
}

func TestHandleCreateTaskRetryHydratesPRContextFromSourceRun(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	taskID := "owner/repo#170"
	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:          taskID,
		Repo:        "owner/repo",
		IssueNumber: 170,
		PRNumber:    88,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	sourceRunDir := filepath.Join(s.Config.DataDir, "runs", "run_source_pr")
	if err := os.MkdirAll(sourceRunDir, 0o755); err != nil {
		t.Fatalf("mkdir source run dir: %v", err)
	}
	sourceRun, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_source_pr",
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "Continue existing PR",
		BaseBranch:  "main",
		HeadBranch:  "rascal/owner-repo-170-pr",
		Trigger:     runtrigger.NamePRComment,
		RunDir:      sourceRunDir,
		IssueNumber: 170,
		PRNumber:    88,
		PRStatus:    state.PRStatusOpen,
	})
	if err != nil {
		t.Fatalf("add source run: %v", err)
	}
	if err := s.WriteRunResponseTarget(sourceRun, &orchestrator.RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 88,
		RequestedBy: "alice",
		Trigger:     runtrigger.NamePRComment,
	}); err != nil {
		t.Fatalf("write source response target: %v", err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks",
		strings.NewReader(`{"task_id":"owner/repo#170","source_run_id":"run_source_pr","repo":"owner/repo","task":"Continue existing PR","base_branch":"main","trigger":"retry"}`),
	)
	rec := httptest.NewRecorder()
	s.HandleCreateTask(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", rec.Code, rec.Body.String())
	}

	var out struct {
		Run state.Run `json:"run"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Run.IssueNumber != 170 {
		t.Fatalf("issue_number = %d, want 170", out.Run.IssueNumber)
	}
	if out.Run.PRNumber != 88 {
		t.Fatalf("pr_number = %d, want 88", out.Run.PRNumber)
	}
	if out.Run.PRStatus != state.PRStatusOpen {
		t.Fatalf("pr_status = %q, want %q", out.Run.PRStatus, state.PRStatusOpen)
	}
	if out.Run.HeadBranch != "rascal/owner-repo-170-pr" {
		t.Fatalf("head_branch = %q, want rascal/owner-repo-170-pr", out.Run.HeadBranch)
	}
	target, ok, err := s.Store.GetRunResponseTarget(out.Run.ID)
	if err != nil {
		t.Fatalf("get retry response target: %v", err)
	}
	if !ok {
		t.Fatal("expected retry response target")
	}
	if target.IssueNumber != 88 {
		t.Fatalf("response target issue = %d, want 88", target.IssueNumber)
	}
	if target.Trigger != runtrigger.NamePRComment {
		t.Fatalf("response target trigger = %q, want %q", target.Trigger, runtrigger.NamePRComment)
	}
}

func TestHandleCreateIssueTaskNormalizesRepositoryCase(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/tasks/issue",
		strings.NewReader(`{"repo":"Owner/Repo","issue_number":7}`),
	)
	rec := httptest.NewRecorder()
	s.HandleCreateIssueTask(rec, req)
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

	task, ok := s.Store.GetTask("owner/repo#7")
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
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	first, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "t1", Repo: "owner/repo", Instruction: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	second, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "t1", Repo: "owner/repo", Instruction: "second"})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run start")

	rec := httptest.NewRecorder()
	s.HandleCancelRun(rec, second.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for queued cancel, got %d", rec.Code)
	}

	updated, ok := s.Store.GetRun(second.ID)
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
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "active-cancel", Repo: "owner/repo", Instruction: "cancel me"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	rec := httptest.NewRecorder()
	s.HandleCancelRun(rec, run.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for active cancel, got %d", rec.Code)
	}

	close(waitCh)
	waitFor(t, 2*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusCanceled && strings.Contains(updated.Error, "canceled by user")
	}, "active run canceled with user reason")
}

func TestCanceledRunDoesNotTransitionToSuccess(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	cleanupDone := make(chan struct{})
	launcher := &stubbornRunner{
		wait: done,
		res: fakeRunResult{
			PRNumber: 42,
			PRURL:    "https://example.com/pr/42",
		},
	}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "cancel-guard", Repo: "owner/repo", Instruction: "guard cancel"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	s.AfterRunCleanup = func(runID string) {
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
	s.HandleCancelRun(rec, run.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for active cancel, got %d", rec.Code)
	}

	close(done)
	waitFor(t, 2*time.Second, func() bool {
		current, ok := s.Store.GetRun(run.ID)
		return ok && current.Status == state.StatusCanceled
	}, "run remains canceled after launcher returns success")

	current, ok := s.Store.GetRun(run.ID)
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
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "drain-reason", Repo: "owner/repo", Instruction: "drain"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	s.CancelActiveRuns("orchestrator shutdown drain timeout")
	close(waitCh)

	waitFor(t, 2*time.Second, func() bool {
		current, ok := s.Store.GetRun(run.ID)
		return ok && current.Status == state.StatusCanceled && strings.Contains(current.Error, "drain timeout")
	}, "run canceled with drain reason")
}

func TestBeginDeployDrainWaitsForActiveRunsWithoutCanceling(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "deploy-drain", Repo: "owner/repo", Instruction: "let it finish"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	done := beginDeployDrain(nil, s)

	select {
	case <-done:
		t.Fatal("deploy drain should wait for the active run to finish")
	case <-time.After(300 * time.Millisecond):
	}

	close(waitCh)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deploy drain did not finish after active run completed")
	}

	waitFor(t, 2*time.Second, func() bool {
		current, ok := s.Store.GetRun(run.ID)
		return ok && current.Status == state.StatusSucceeded
	}, "run succeeded after deploy drain")
}

func TestReclaimForDeployCancelsActiveRunsWithDeployReason(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer waitForServerIdle(t, s)

	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "deploy-reclaim", Repo: "owner/repo", Instruction: "reclaim it"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForRunExecution(t, s, run.ID)

	reclaimForDeploy(nil, s, 2*time.Second)

	waitFor(t, 2*time.Second, func() bool {
		current, ok := s.Store.GetRun(run.ID)
		return ok &&
			current.Status == state.StatusCanceled &&
			current.StatusReason == state.RunStatusReasonDeployReclaimed &&
			strings.Contains(current.Error, deployReclaimCancelReason)
	}, "run canceled with deploy reclaim reason")
}

func TestStopRunSupervisorsCatchesInFlightSupervisorRegistration(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s := newTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	reachedBeforeSupervise := make(chan struct{})
	releaseBeforeSupervise := make(chan struct{})
	s.BeforeSupervise = func(_ string) {
		close(reachedBeforeSupervise)
		<-releaseBeforeSupervise
	}

	run, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "task_stop_supervisor_race", Repo: "owner/repo", Instruction: "stop supervisor race"})
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

	s.BeginDrain()
	s.StopRunSupervisors()
	close(releaseBeforeSupervise)

	if err := s.WaitForNoActiveRuns(500 * time.Millisecond); err != nil {
		t.Fatalf("wait for idle after stop supervisors: %v", err)
	}
}

func TestHandleReadyReflectsDrainingState(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	readyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRec := httptest.NewRecorder()
	s.HandleReady(readyRec, readyReq)
	if readyRec.Code != http.StatusOK {
		t.Fatalf("expected ready 200 before drain, got %d", readyRec.Code)
	}

	s.BeginDrain()

	notReadyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	notReadyRec := httptest.NewRecorder()
	s.HandleReady(notReadyRec, notReadyReq)
	if notReadyRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected ready 503 during drain, got %d", notReadyRec.Code)
	}
}

func TestCreateAndQueueRunRejectedWhenDraining(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	s.BeginDrain()
	_, err := s.CreateAndQueueRun(orchestrator.RunRequest{
		TaskID:      "owner/repo#1",
		Repo:        "owner/repo",
		Instruction: "should be rejected",
	})
	if !errors.Is(err, orchestrator.ErrServerDraining) {
		t.Fatalf("expected orchestrator.ErrServerDraining, got %v", err)
	}
}

func TestBeginDrainLeavesQueuedRunsForNextSlot(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &fakeRunner{waitCh: waitCh}
	s := newTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForServerIdle(t, s)
	}()

	first, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "owner/repo#drain", Repo: "owner/repo", Instruction: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	queued, err := s.CreateAndQueueRun(orchestrator.RunRequest{TaskID: "owner/repo#drain", Repo: "owner/repo", Instruction: "queued"})
	if err != nil {
		t.Fatalf("create queued run: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to be active")
	_ = waitForRunExecution(t, s, first.ID)

	s.BeginDrain()

	waitFor(t, time.Second, func() bool {
		r, ok := s.Store.GetRun(queued.ID)
		return ok && r.Status == state.StatusQueued
	}, "queued run remains queued during drain")
}
