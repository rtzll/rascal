package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/credentials"
	credentialstrategies "github.com/rtzll/rascal/internal/credentials/strategies"
	"github.com/rtzll/rascal/internal/credentialstrategy"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runner"
	agentrt "github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

type executionFakeRunResult struct {
	PRNumber int
	PRURL    string
	HeadSHA  string
	ExitCode int
	Error    string
}

type executionFakeRunner struct {
	mu     sync.Mutex
	calls  int
	specs  []runner.Spec
	errSeq []error
	resSeq []executionFakeRunResult
	execs  map[string]*executionFakeExecution
	nextID int
}

type executionFakeExecution struct {
	handle    runner.ExecutionHandle
	spec      runner.Spec
	result    executionFakeRunResult
	finalized bool
}

func (f *executionFakeRunner) StartDetached(_ context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	callIdx := f.calls
	f.calls++
	f.specs = append(f.specs, spec)

	if callIdx < len(f.errSeq) && f.errSeq[callIdx] != nil {
		return runner.ExecutionHandle{}, f.errSeq[callIdx]
	}

	result := executionFakeRunResult{}
	if callIdx < len(f.resSeq) {
		result = f.resSeq[callIdx]
	}
	if f.execs == nil {
		f.execs = make(map[string]*executionFakeExecution)
	}
	f.nextID++
	handle := runner.ExecutionHandle{
		Backend: runner.ExecutionBackendNoop,
		ID:      fmt.Sprintf("exec-%d", f.nextID),
		Name:    fmt.Sprintf("rascal-%s", spec.RunID),
	}
	execRec := &executionFakeExecution{
		handle: handle,
		spec:   spec,
		result: result,
	}
	f.execs[handle.ID] = execRec
	f.execs[handle.Name] = execRec
	return handle, nil
}

func (f *executionFakeRunner) Inspect(_ context.Context, handle runner.ExecutionHandle) (runner.ExecutionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	execRec, ok := f.execs[handle.ID]
	if !ok {
		execRec, ok = f.execs[handle.Name]
	}
	if !ok {
		return runner.ExecutionState{}, runner.ErrExecutionNotFound
	}
	if !execRec.finalized {
		if err := writeExecutionFakeMeta(execRec.spec, execRec.result); err != nil {
			return runner.ExecutionState{}, err
		}
		execRec.finalized = true
	}
	exitCode := execRec.result.ExitCode
	return runner.ExecutionState{Running: false, ExitCode: &exitCode}, nil
}

func (f *executionFakeRunner) Stop(_ context.Context, handle runner.ExecutionHandle, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.execs[handle.ID]; ok {
		return nil
	}
	if _, ok := f.execs[handle.Name]; ok {
		return nil
	}
	return runner.ErrExecutionNotFound
}

func (f *executionFakeRunner) Remove(_ context.Context, handle runner.ExecutionHandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if execRec, ok := f.execs[handle.ID]; ok {
		delete(f.execs, execRec.handle.ID)
		delete(f.execs, execRec.handle.Name)
		return nil
	}
	if execRec, ok := f.execs[handle.Name]; ok {
		delete(f.execs, execRec.handle.ID)
		delete(f.execs, execRec.handle.Name)
	}
	return nil
}

func (f *executionFakeRunner) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func writeExecutionFakeMeta(spec runner.Spec, res executionFakeRunResult) error {
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

func newExecutionTestServer(t *testing.T, launcher runner.Runner) *Server {
	t.Helper()

	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")
	store, err := state.New(statePath, 200)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}

	cipher, err := credentials.NewAESCipher("execution-test-secret")
	if err != nil {
		t.Fatalf("new credential cipher: %v", err)
	}
	strategy, err := credentialstrategies.ByName(credentialstrategy.DefaultName)
	if err != nil {
		t.Fatalf("credential strategy: %v", err)
	}
	broker := credentials.NewBroker(store, strategy, cipher, 5*time.Second)

	cfg := config.ServerConfig{
		DataDir:              dataDir,
		StatePath:            statePath,
		MaxRuns:              200,
		RunnerMode:           runner.ModeNoop,
		CredentialRenewEvery: 20 * time.Millisecond,
	}
	s := NewServer(
		cfg,
		store,
		launcher,
		ghapi.NewAPIClient(""),
		broker,
		cipher,
		"execution-test-instance",
	)
	s.SupervisorInterval = 10 * time.Millisecond
	s.RetryBackoff = func(_ int) time.Duration {
		return 10 * time.Millisecond
	}
	if err := s.StartRunResultReporter(); err != nil {
		t.Fatalf("start run result reporter: %v", err)
	}
	seedExecutionSharedCredential(t, store, cipher)
	t.Cleanup(func() {
		s.BeginDrain()
		s.StopRunSupervisors()
		if err := s.WaitForNoActiveSupervisors(2 * time.Second); err != nil {
			t.Fatalf("wait for supervisors to stop: %v", err)
		}
		if err := s.StopRunResultReporter(); err != nil {
			t.Fatalf("stop run result reporter: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close state store: %v", err)
		}
	})
	return s
}

func seedExecutionSharedCredential(t *testing.T, store *state.Store, cipher credentials.Cipher) {
	t.Helper()
	blob, err := cipher.Encrypt([]byte(`{"token":"test"}`))
	if err != nil {
		t.Fatalf("encrypt auth blob: %v", err)
	}
	if _, err := store.CreateCredential(state.CreateCredentialInput{
		ID:                "cred_execution_codex",
		Scope:             state.CredentialScopeShared,
		Provider:          string(agentrt.RuntimeCodex.Provider()),
		EncryptedAuthBlob: blob,
		Weight:            1,
		Status:            state.CredentialStatusActive,
	}); err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create shared credential: %v", err)
	}
}

func waitForExecutionCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
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

func waitForExecutionServerIdle(t *testing.T, s *Server) {
	t.Helper()
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		return s.ActiveRunCount() == 0
	}, "server idle")
}

func TestExecuteRunRetriesLauncherFailure(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		errSeq: []error{
			errors.New("transient launcher error"),
			nil,
		},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)
	s.Config.RunnerMaxAttempts = 2

	run, err := s.CreateAndQueueRun(RunRequest{
		TaskID:      "owner/repo#retry",
		Repo:        "owner/repo",
		Instruction: "retry task",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	waitForExecutionCondition(t, 4*time.Second, func() bool {
		r, ok := s.Store.GetRun(run.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "run to succeed after retry")

	if calls := launcher.Calls(); calls != 2 {
		t.Fatalf("expected 2 launcher calls, got %d", calls)
	}
}

func TestExecuteRunSetsTaskSessionSpecForPROnlyCommentTrigger(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	s.Config.AgentRuntime = agentrt.RuntimeGooseCodex
	sessionRoot := filepath.Join(t.TempDir(), "goose-sessions")
	s.Config.TaskSession = config.TaskSessionConfig{
		Mode:    agentrt.SessionModePROnly,
		Root:    sessionRoot,
		TTLDays: 0,
	}

	run, err := s.CreateAndQueueRun(RunRequest{
		TaskID:      "owner/repo#123",
		Repo:        "owner/repo",
		Instruction: "Address PR #123 feedback",
		Trigger:     "pr_comment",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	waitForExecutionCondition(t, 2*time.Second, func() bool {
		r, ok := s.Store.GetRun(run.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "run completion")

	if launcher.Calls() != 1 {
		t.Fatalf("expected 1 launcher call, got %d", launcher.Calls())
	}
	spec := launcher.specs[0]
	if !spec.TaskSession.Resume {
		t.Fatal("expected TaskSession.Resume=true for pr-only comment trigger")
	}
	if spec.TaskSession.TaskKey == "" {
		t.Fatal("expected TaskSession.TaskKey to be populated")
	}
	if spec.TaskSession.RuntimeSessionID == "" {
		t.Fatal("expected TaskSession.RuntimeSessionID to be populated")
	}
	if !strings.HasPrefix(spec.TaskSession.TaskDir, sessionRoot+string(os.PathSeparator)) {
		t.Fatalf("unexpected TaskSession.TaskDir %q (root %q)", spec.TaskSession.TaskDir, sessionRoot)
	}
}

func TestExecuteRunDisablesTaskSessionSpecForNonPROnlyTrigger(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	s.Config.AgentRuntime = agentrt.RuntimeGooseCodex
	s.Config.TaskSession = config.TaskSessionConfig{
		Mode:    agentrt.SessionModePROnly,
		Root:    filepath.Join(t.TempDir(), "goose-sessions"),
		TTLDays: 0,
	}

	run, err := s.CreateAndQueueRun(RunRequest{
		TaskID:      "owner/repo#124",
		Repo:        "owner/repo",
		Instruction: "Initial issue run",
		Trigger:     "issue_label",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	waitForExecutionCondition(t, 2*time.Second, func() bool {
		r, ok := s.Store.GetRun(run.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "run completion")

	if launcher.Calls() != 1 {
		t.Fatalf("expected 1 launcher call, got %d", launcher.Calls())
	}
	spec := launcher.specs[0]
	if spec.TaskSession.Resume {
		t.Fatal("expected TaskSession.Resume=false for non PR-only trigger")
	}
	if spec.TaskSession.TaskDir != "" || spec.TaskSession.TaskKey != "" || spec.TaskSession.RuntimeSessionID != "" {
		t.Fatalf("expected empty agent session fields when resume disabled, got dir=%q key=%q name=%q", spec.TaskSession.TaskDir, spec.TaskSession.TaskKey, spec.TaskSession.RuntimeSessionID)
	}
}
