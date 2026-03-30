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
	"github.com/rtzll/rascal/internal/runner"
	agentrt "github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

type testRunResult struct {
	PRNumber int
	PRURL    string
	HeadSHA  string
	ExitCode int
	Error    string
}

type testExecution struct {
	handle    runner.ExecutionHandle
	spec      runner.Spec
	waitCh    <-chan struct{}
	result    testRunResult
	stopped   bool
	finalized bool
}

type testRunner struct {
	mu       sync.Mutex
	calls    int
	waitCh   <-chan struct{}
	result   testRunResult
	startErr error
	execs    map[string]*testExecution
	nextID   int
}

func (r *testRunner) StartDetached(_ context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErr != nil {
		return runner.ExecutionHandle{}, r.startErr
	}
	r.calls++
	r.nextID++
	if r.execs == nil {
		r.execs = make(map[string]*testExecution)
	}
	handle := runner.ExecutionHandle{
		Backend: runner.ExecutionBackend("fake"),
		ID:      fmt.Sprintf("exec-%d", r.nextID),
		Name:    fmt.Sprintf("rascal-%s", spec.RunID),
	}
	execRec := &testExecution{
		handle: handle,
		spec:   spec,
		waitCh: r.waitCh,
		result: r.result,
	}
	r.execs[handle.ID] = execRec
	r.execs[handle.Name] = execRec
	return handle, nil
}

func (r *testRunner) lookup(handle runner.ExecutionHandle) (*testExecution, bool) {
	if execRec, ok := r.execs[handle.ID]; ok {
		return execRec, true
	}
	if execRec, ok := r.execs[handle.Name]; ok {
		return execRec, true
	}
	return nil, false
}

func (r *testRunner) Inspect(_ context.Context, handle runner.ExecutionHandle) (runner.ExecutionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	execRec, ok := r.lookup(handle)
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
		if err := runner.WriteMeta(filepath.Join(execRec.spec.RunDir, "meta.json"), runner.Meta{
			RunID:      execRec.spec.RunID,
			TaskID:     execRec.spec.TaskID,
			Repo:       execRec.spec.Repo,
			BaseBranch: execRec.spec.BaseBranch,
			HeadBranch: execRec.spec.HeadBranch,
			PRNumber:   execRec.result.PRNumber,
			PRURL:      execRec.result.PRURL,
			HeadSHA:    execRec.result.HeadSHA,
			ExitCode:   execRec.result.ExitCode,
			Error:      execRec.result.Error,
		}); err != nil {
			return runner.ExecutionState{}, fmt.Errorf("write test meta: %w", err)
		}
		execRec.finalized = true
	}
	exitCode := execRec.result.ExitCode
	return runner.ExecutionState{Running: false, ExitCode: &exitCode}, nil
}

func (r *testRunner) Stop(_ context.Context, handle runner.ExecutionHandle, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	execRec, ok := r.lookup(handle)
	if !ok {
		return runner.ErrExecutionNotFound
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

func (r *testRunner) Remove(_ context.Context, handle runner.ExecutionHandle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	execRec, ok := r.lookup(handle)
	if !ok {
		return nil
	}
	delete(r.execs, execRec.handle.ID)
	delete(r.execs, execRec.handle.Name)
	return nil
}

func (r *testRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type testBroker struct {
	mu         sync.Mutex
	acquireErr error
	renewErr   error
	released   []string
	acquired   []string
}

func (b *testBroker) Acquire(_ context.Context, req credentials.AcquireRequest) (credentials.Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.acquireErr != nil {
		return credentials.Lease{}, b.acquireErr
	}
	leaseID := "lease-" + req.RunID
	b.acquired = append(b.acquired, leaseID)
	return credentials.Lease{
		ID:           leaseID,
		CredentialID: "cred-1",
		RunID:        req.RunID,
		UserID:       req.UserID,
		Strategy:     "test",
		AuthBlob:     []byte(`{"token":"ok"}`),
	}, nil
}

func (b *testBroker) Renew(_ context.Context, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.renewErr
}

func (b *testBroker) Release(_ context.Context, leaseID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.released = append(b.released, leaseID)
	return nil
}

type recordingNotifier struct {
	mu        sync.Mutex
	started   []string
	terminal  []state.Run
	reactions []string
}

func (n *recordingNotifier) AddIssueReaction(repo string, issueNumber int, reaction string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reactions = append(n.reactions, fmt.Sprintf("%s#%d:%s", repo, issueNumber, reaction))
}

func (n *recordingNotifier) CleanupAgentSessions() {}

func (n *recordingNotifier) NotifyRunStarted(run state.Run, _ agentrt.SessionMode, _ bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.started = append(n.started, run.ID)
}

func (n *recordingNotifier) NotifyRunTerminal(run state.Run) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.terminal = append(n.terminal, run)
}

type recordingExecutor struct {
	mu     sync.Mutex
	runIDs []string
}

func (e *recordingExecutor) Execute(_ context.Context, runID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runIDs = append(e.runIDs, runID)
	return nil
}

func (e *recordingExecutor) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.runIDs))
	copy(out, e.runIDs)
	return out
}

func newLifecycleStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func seedLifecycleRun(t *testing.T, store *state.Store, runID, taskID string) state.Run {
	t.Helper()
	if _, err := store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo", AgentRuntime: agentrt.RuntimeGooseCodex}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := store.AddRun(state.CreateRunInput{
		ID:           runID,
		TaskID:       taskID,
		Repo:         "owner/repo",
		Instruction:  "test instruction",
		AgentRuntime: agentrt.RuntimeGooseCodex,
		BaseBranch:   "main",
		HeadBranch:   "rascal/test",
		IssueNumber:  42,
		RunDir:       filepath.Join(t.TempDir(), runID),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := os.MkdirAll(run.RunDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	return run
}

func newTestExecutionSupervisor(t *testing.T, store *state.Store, runRunner runner.Runner, broker credentials.CredentialBroker, notifier RunNotifier) *ExecutionSupervisor {
	t.Helper()
	cfg := config.ServerConfig{
		DataDir:              t.TempDir(),
		RunnerMaxAttempts:    1,
		CredentialRenewEvery: 20 * time.Millisecond,
	}
	es := NewExecutionSupervisor(
		func() config.ServerConfig { return cfg },
		store,
		runRunner,
		broker,
		notifier,
		NewRunStateMachine(store),
		"test-instance",
	)
	es.Tick = func() time.Duration { return 10 * time.Millisecond }
	es.StartRetryBackoff = func(_ int) time.Duration { return 5 * time.Millisecond }
	es.OnRunFinished = func(state.Run) {}
	es.PauseScheduler = func(until time.Time, _ string) time.Time {
		if until.IsZero() {
			return time.Now().UTC().Add(time.Minute)
		}
		return until.UTC()
	}
	return es
}

func newTestRunScheduler(store *state.Store, executor runExecutor, draining func() bool, limit int) *RunScheduler {
	rs := NewRunScheduler(store, NewRunStateMachine(store), executor, "test-instance")
	rs.ConcurrencyLimit = func() int { return limit }
	rs.IsDraining = draining
	return rs
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

func waitForRunExecution(t *testing.T, store *state.Store, runID string) state.RunExecution {
	t.Helper()
	var execRec state.RunExecution
	waitForCondition(t, 2*time.Second, func() bool {
		rec, ok := store.GetRunExecution(runID)
		if !ok {
			return false
		}
		if rec.Status != state.RunExecutionStatusRunning {
			return false
		}
		execRec = rec
		return true
	}, "run execution")
	return execRec
}

func TestExecutionSupervisorExecuteHappyPath(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_happy", "task_happy")
	notifier := &recordingNotifier{}
	runRunner := &testRunner{}
	supervisor := newTestExecutionSupervisor(t, store, runRunner, nil, notifier)

	if err := supervisor.Execute(context.Background(), run.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	updated, ok := store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", updated.Status)
	}
	if runRunner.Calls() != 1 {
		t.Fatalf("runner calls = %d, want 1", runRunner.Calls())
	}
	if len(notifier.started) != 1 || notifier.started[0] != run.ID {
		t.Fatalf("unexpected started notifications: %+v", notifier.started)
	}
	if len(notifier.terminal) != 1 || notifier.terminal[0].Status != state.StatusSucceeded {
		t.Fatalf("unexpected terminal notifications: %+v", notifier.terminal)
	}
}

func TestExecutionSupervisorExecuteCancelBeforeStart(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_cancel_before_start", "task_cancel_before_start")
	if err := store.RequestRunCancel(run.ID, "canceled by user", "user"); err != nil {
		t.Fatalf("request cancel: %v", err)
	}

	supervisor := newTestExecutionSupervisor(t, store, &testRunner{}, nil, &recordingNotifier{})
	if err := supervisor.Execute(context.Background(), run.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", updated.Status)
	}
	if updated.StatusReason != state.RunStatusReasonUserCanceled {
		t.Fatalf("reason = %s, want user_canceled", updated.StatusReason)
	}
}

func TestExecutionSupervisorExecuteCancelDuringRun(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_cancel_during", "task_cancel_during")
	waitCh := make(chan struct{})
	runRunner := &testRunner{waitCh: waitCh}
	supervisor := newTestExecutionSupervisor(t, store, runRunner, nil, &recordingNotifier{})

	done := make(chan error, 1)
	go func() {
		done <- supervisor.Execute(context.Background(), run.ID)
	}()

	_ = waitForRunExecution(t, store, run.ID)
	if err := store.RequestRunCancel(run.ID, "canceled by user", "user"); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		execRec, ok := store.GetRunExecution(run.ID)
		return ok && execRec.Status == state.RunExecutionStatusStopping
	}, "execution stopping")
	close(waitCh)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for execute")
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", updated.Status)
	}
	if updated.StatusReason != state.RunStatusReasonUserCanceled {
		t.Fatalf("reason = %s, want user_canceled", updated.StatusReason)
	}
}

func TestExecutionSupervisorExecuteCredentialAcquireFailure(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_credential_failure", "task_credential_failure")
	runRunner := &testRunner{}
	supervisor := newTestExecutionSupervisor(t, store, runRunner, &testBroker{acquireErr: credentials.ErrNoCredentialAvailable}, &recordingNotifier{})

	if err := supervisor.Execute(context.Background(), run.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	if !strings.Contains(updated.Error, "acquire credential lease: no credentials available") {
		t.Fatalf("unexpected error: %q", updated.Error)
	}
	if runRunner.Calls() != 0 {
		t.Fatalf("runner calls = %d, want 0", runRunner.Calls())
	}
}

func TestExecutionSupervisorExecuteCredentialLeaseRenewalFailure(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_renew_failure", "task_renew_failure")
	waitCh := make(chan struct{})
	runRunner := &testRunner{waitCh: waitCh}
	supervisor := newTestExecutionSupervisor(t, store, runRunner, &testBroker{renewErr: credentials.ErrLeaseLost}, &recordingNotifier{})

	done := make(chan error, 1)
	go func() {
		done <- supervisor.Execute(context.Background(), run.ID)
	}()

	_ = waitForRunExecution(t, store, run.ID)
	waitForCondition(t, 2*time.Second, func() bool {
		updated, ok := store.GetRun(run.ID)
		return ok && updated.Status == state.StatusCanceled
	}, "credential lease cancel")
	close(waitCh)

	if err := <-done; err != nil {
		t.Fatalf("execute: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", updated.Status)
	}
	if updated.StatusReason != state.RunStatusReasonCredentialLeaseLost {
		t.Fatalf("reason = %s, want credential_lease_lost", updated.StatusReason)
	}
}

func TestExecutionSupervisorExecuteContainerCrash(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_crash", "task_crash")
	supervisor := newTestExecutionSupervisor(t, store, &testRunner{result: testRunResult{ExitCode: 17, Error: "runner crashed"}}, nil, &recordingNotifier{})

	if err := supervisor.Execute(context.Background(), run.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	if !strings.Contains(updated.Error, "runner crashed") {
		t.Fatalf("unexpected error: %q", updated.Error)
	}
}

func TestExecutionSupervisorExecuteTaskAlreadyCompleted(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_task_completed", "task_completed")
	if err := store.MarkTaskCompleted(run.TaskID); err != nil {
		t.Fatalf("mark task completed: %v", err)
	}
	supervisor := newTestExecutionSupervisor(t, store, &testRunner{}, nil, &recordingNotifier{})

	if err := supervisor.Execute(context.Background(), run.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", updated.Status)
	}
	if updated.StatusReason != state.RunStatusReasonTaskCompleted {
		t.Fatalf("reason = %s, want task_completed", updated.StatusReason)
	}
}

func TestExecutionSupervisorRecoverDetachedContainerStillRunning(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_recover_running", "task_recover_running")
	if _, err := store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	waitCh := make(chan struct{})
	runRunner := &testRunner{waitCh: waitCh}
	handle, err := runRunner.StartDetached(context.Background(), runner.Spec{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Instruction: run.Instruction,
		BaseBranch:  run.BaseBranch,
		HeadBranch:  run.HeadBranch,
		RunDir:      run.RunDir,
	})
	if err != nil {
		t.Fatalf("start detached: %v", err)
	}
	if _, err := store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       state.RunExecutionBackendDocker,
		ContainerName: handle.Name,
		ContainerID:   handle.ID,
		Status:        state.RunExecutionStatusRunning,
	}); err != nil {
		t.Fatalf("upsert execution: %v", err)
	}
	supervisor := newTestExecutionSupervisor(t, store, runRunner, nil, &recordingNotifier{})

	if err := supervisor.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		lease, ok := store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "test-instance"
	}, "lease adoption")

	close(waitCh)
	waitForCondition(t, 3*time.Second, func() bool {
		updated, ok := store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "run completion after adoption")
}

func TestExecutionSupervisorRecoverDetachedContainerExited(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_recover_exited", "task_recover_exited")
	if _, err := store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	runRunner := &testRunner{}
	handle, err := runRunner.StartDetached(context.Background(), runner.Spec{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Instruction: run.Instruction,
		BaseBranch:  run.BaseBranch,
		HeadBranch:  run.HeadBranch,
		RunDir:      run.RunDir,
	})
	if err != nil {
		t.Fatalf("start detached: %v", err)
	}
	if _, err := store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       state.RunExecutionBackendDocker,
		ContainerName: handle.Name,
		ContainerID:   handle.ID,
		Status:        state.RunExecutionStatusRunning,
	}); err != nil {
		t.Fatalf("upsert execution: %v", err)
	}
	supervisor := newTestExecutionSupervisor(t, store, runRunner, nil, &recordingNotifier{})

	if err := supervisor.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", updated.Status)
	}
}

func TestExecutionSupervisorRecoverDetachedContainerMissing(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_recover_missing", "task_recover_missing")
	if _, err := store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       state.RunExecutionBackendDocker,
		ContainerName: "rascal-run_recover_missing",
		ContainerID:   "missing-container",
		Status:        state.RunExecutionStatusRunning,
	}); err != nil {
		t.Fatalf("upsert execution: %v", err)
	}
	supervisor := newTestExecutionSupervisor(t, store, &testRunner{}, nil, &recordingNotifier{})

	if err := supervisor.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	if !strings.Contains(updated.Error, "detached container missing during adoption") {
		t.Fatalf("unexpected error: %q", updated.Error)
	}
}

func TestExecutionSupervisorRecoverExpiredLeaseRequeues(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_recover_expired_lease", "task_recover_expired_lease")
	if _, err := store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if err := store.UpsertRunLease(run.ID, "other-instance", time.Nanosecond); err != nil {
		t.Fatalf("upsert lease: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	supervisor := newTestExecutionSupervisor(t, store, &testRunner{}, nil, &recordingNotifier{})

	if err := supervisor.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusQueued {
		t.Fatalf("status = %s, want queued", updated.Status)
	}
}

func TestExecutionSupervisorRecoverNoLeaseRecentStartLeavesRunAlone(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_recover_recent_start", "task_recover_recent_start")
	if _, err := store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	recent := time.Now().UTC().Add(-runLeaseTTL / 2)
	if _, err := store.UpdateRun(run.ID, func(r *state.Run) error {
		r.StartedAt = &recent
		return nil
	}); err != nil {
		t.Fatalf("set recent started_at: %v", err)
	}
	supervisor := newTestExecutionSupervisor(t, store, &testRunner{}, nil, &recordingNotifier{})

	if err := supervisor.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusRunning {
		t.Fatalf("status = %s, want running", updated.Status)
	}
}

func TestRunSchedulerRespectsMaxConcurrency(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	seedLifecycleRun(t, store, "run_sched_1", "task_sched_1")
	seedLifecycleRun(t, store, "run_sched_2", "task_sched_2")
	seedLifecycleRun(t, store, "run_sched_3", "task_sched_3")
	executor := &recordingExecutor{}
	scheduler := newTestRunScheduler(store, executor, func() bool { return false }, 2)

	if err := scheduler.Schedule(context.Background(), ""); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	waitForCondition(t, time.Second, func() bool {
		return len(executor.Calls()) == 2
	}, "two scheduled runs")

	if got := len(executor.Calls()); got != 2 {
		t.Fatalf("executor calls = %d, want 2", got)
	}
	if got := scheduler.ActiveRunCount(); got != 2 {
		t.Fatalf("active run count = %d, want 2", got)
	}
}

func TestRunSchedulerPauseAndResume(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	seedLifecycleRun(t, store, "run_sched_pause", "task_sched_pause")
	executor := &recordingExecutor{}
	scheduler := newTestRunScheduler(store, executor, func() bool { return false }, 1)

	scheduler.Pause(time.Now().UTC().Add(time.Minute), "test pause")
	if err := scheduler.Schedule(context.Background(), ""); err != nil {
		t.Fatalf("schedule while paused: %v", err)
	}
	if got := len(executor.Calls()); got != 0 {
		t.Fatalf("executor calls while paused = %d, want 0", got)
	}

	if err := scheduler.Resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitForCondition(t, time.Second, func() bool {
		return len(executor.Calls()) == 1
	}, "resume scheduling")
}

func TestRunSchedulerDrainingPreventsNewRuns(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	seedLifecycleRun(t, store, "run_sched_drain", "task_sched_drain")
	executor := &recordingExecutor{}
	scheduler := newTestRunScheduler(store, executor, func() bool { return true }, 1)

	if err := scheduler.Schedule(context.Background(), ""); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got := len(executor.Calls()); got != 0 {
		t.Fatalf("executor calls = %d, want 0", got)
	}
}

func TestRunSchedulerCancelledRunSkipped(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_sched_cancelled", "task_sched_cancelled")
	if err := store.RequestRunCancel(run.ID, "canceled by user", "user"); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	executor := &recordingExecutor{}
	scheduler := newTestRunScheduler(store, executor, func() bool { return false }, 1)

	if err := scheduler.Schedule(context.Background(), ""); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", updated.Status)
	}
	if len(executor.Calls()) != 0 {
		t.Fatalf("unexpected executor calls: %+v", executor.Calls())
	}
}

func TestExecutionSupervisorExecuteStartFailureTransitionsRun(t *testing.T) {
	t.Parallel()
	store := newLifecycleStore(t)
	run := seedLifecycleRun(t, store, "run_start_failure", "task_start_failure")
	supervisor := newTestExecutionSupervisor(t, store, &testRunner{startErr: errors.New("boom")}, nil, &recordingNotifier{})

	if err := supervisor.Execute(context.Background(), run.ID); err != nil {
		t.Fatalf("execute: %v", err)
	}

	updated, _ := store.GetRun(run.ID)
	if updated.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed", updated.Status)
	}
	if !strings.Contains(updated.Error, "start detached run") {
		t.Fatalf("unexpected error: %q", updated.Error)
	}
}
