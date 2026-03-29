package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

func TestExecuteRunPersistsRunExecutionHandle(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &executionFakeRunner{waitCh: waitCh}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.CreateAndQueueRun(RunRequest{TaskID: "task_exec_handle", Repo: "owner/repo", Instruction: "persist execution handle"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	execRec := waitForExecutionRunExecution(t, s, run.ID)
	if execRec.Backend == "" || execRec.ContainerID == "" || execRec.ContainerName == "" {
		t.Fatalf("unexpected execution record: %+v", execRec)
	}

	close(waitCh)
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "run completion")
}

func TestRecoverRunningRunAdoptsDetachedExecution(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &executionFakeRunner{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s1 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	run, err := s1.CreateAndQueueRun(RunRequest{TaskID: "task_adopt", Repo: "owner/repo", Instruction: "adopt detached"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	execRec := waitForExecutionRunExecution(t, s1, run.ID)

	waitForExecutionCondition(t, 2*time.Second, func() bool {
		lease, ok := s1.Store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-a"
	}, "instance-a lease ownership")

	s1.BeginDrain()
	s1.StopRunSupervisors()
	if err := s1.Store.DeleteRunLease(run.ID); err != nil {
		t.Fatalf("delete s1 lease: %v", err)
	}

	s2 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-b")
	defer waitForExecutionServerIdle(t, s2)
	s2.RecoverRunningRuns()

	waitForExecutionCondition(t, 2*time.Second, func() bool {
		lease, ok := s2.Store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-b"
	}, "instance-b lease ownership")

	adoptedExec, ok := s2.Store.GetRunExecution(run.ID)
	if !ok {
		t.Fatalf("expected execution after adoption")
	}
	if adoptedExec.ContainerID != execRec.ContainerID {
		t.Fatalf("expected same container id after adoption: got %s want %s", adoptedExec.ContainerID, execRec.ContainerID)
	}

	close(waitCh)
	waitForExecutionCondition(t, 3*time.Second, func() bool {
		updated, ok := s2.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "adopted run completion")
}

func TestDrainReleaseDoesNotDeleteAdoptedLease(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &executionFakeRunner{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s1 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	run, err := s1.CreateAndQueueRun(RunRequest{TaskID: "task_safe_lease_release", Repo: "owner/repo", Instruction: "safe lease release"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForExecutionRunExecution(t, s1, run.ID)

	s2 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-b")
	defer waitForExecutionServerIdle(t, s2)
	s2.RecoverRunningRuns()

	waitForExecutionCondition(t, 2*time.Second, func() bool {
		lease, ok := s2.Store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-b"
	}, "instance-b lease ownership")

	s1.BeginDrain()
	s1.StopRunSupervisors()
	if err := s1.WaitForNoActiveRuns(3 * time.Second); err != nil {
		t.Fatalf("wait for s1 idle: %v", err)
	}

	lease, ok := s2.Store.GetRunLease(run.ID)
	if !ok || lease.OwnerID != "instance-b" {
		t.Fatalf("expected adopted lease to remain with instance-b, got %+v ok=%t", lease, ok)
	}

	close(waitCh)
	waitForExecutionCondition(t, 3*time.Second, func() bool {
		updated, ok := s2.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "completion after safe lease release")
}

func TestRecoverRunningRunFinalizesExitedDetachedExecution(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_recover_exited_exec",
		TaskID:      "task_recover_exited_exec",
		Repo:        "owner/repo",
		Instruction: "recover exited detached run",
		BaseBranch:  "main",
		HeadBranch:  "rascal/recover-exited",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.Store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}

	handle, err := launcher.StartDetached(context.Background(), runner.Spec{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Instruction: run.Instruction,
		BaseBranch:  run.BaseBranch,
		HeadBranch:  run.HeadBranch,
		Trigger:     runtrigger.Normalize(run.Trigger.String()),
		RunDir:      run.RunDir,
		IssueNumber: run.IssueNumber,
		PRNumber:    run.PRNumber,
		Context:     run.Context,
		Debug:       run.Debug,
	})
	if err != nil {
		t.Fatalf("start detached fake execution: %v", err)
	}
	if _, err := s.Store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       state.NormalizeRunExecutionBackend(state.RunExecutionBackend(string(handle.Backend))),
		ContainerName: handle.Name,
		ContainerID:   handle.ID,
		Status:        state.RunExecutionStatusRunning,
	}); err != nil {
		t.Fatalf("upsert run execution: %v", err)
	}

	s.RecoverRunningRuns()
	waitForExecutionCondition(t, 3*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "recover exited execution finalization")
	if _, ok := s.Store.GetRunExecution(run.ID); ok {
		t.Fatalf("expected execution record to be removed after finalization")
	}
}

func TestRecoverRunningRunMissingDetachedExecutionFails(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_recover_missing_exec",
		TaskID:      "task_recover_missing_exec",
		Repo:        "owner/repo",
		Instruction: "recover missing detached run",
		BaseBranch:  "main",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.Store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := s.Store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       state.RunExecutionBackendDocker,
		ContainerName: "rascal-run_recover_missing_exec",
		ContainerID:   "missing-execution-id",
		Status:        state.RunExecutionStatusRunning,
	}); err != nil {
		t.Fatalf("upsert run execution: %v", err)
	}

	s.RecoverRunningRuns()
	waitForExecutionCondition(t, 3*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusFailed && strings.Contains(updated.Error, "detached container missing during adoption")
	}, "recover missing execution failure")
	if _, ok := s.Store.GetRunExecution(run.ID); ok {
		t.Fatalf("expected missing execution record to be cleared")
	}
}

func TestRecoverRunningRunAdoptsByStableContainerName(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &executionFakeRunner{waitCh: waitCh}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_recover_by_name",
		TaskID:      "task_recover_by_name",
		Repo:        "owner/repo",
		Instruction: "recover by stable name",
		BaseBranch:  "main",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.Store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}

	handle, err := launcher.StartDetached(context.Background(), runner.Spec{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Instruction: run.Instruction,
		BaseBranch:  run.BaseBranch,
		RunDir:      run.RunDir,
	})
	if err != nil {
		t.Fatalf("start detached fake execution: %v", err)
	}
	if _, err := s.Store.UpsertRunExecution(state.RunExecution{
		RunID:         run.ID,
		Backend:       state.NormalizeRunExecutionBackend(state.RunExecutionBackend(string(handle.Backend))),
		ContainerName: handle.Name,
		ContainerID:   handle.Name,
		Status:        state.RunExecutionStatusCreated,
	}); err != nil {
		t.Fatalf("upsert placeholder execution: %v", err)
	}

	s.RecoverRunningRuns()
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		lease, ok := s.Store.GetRunLease(run.ID)
		return ok && lease.OwnerID == s.InstanceID
	}, "name-based adoption lease ownership")

	close(waitCh)
	waitForExecutionCondition(t, 3*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "name-based adoption completion")
}

func TestCancelRunWorksAfterAdoptionByDifferentInstance(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &executionFakeRunner{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s1 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	run, err := s1.CreateAndQueueRun(RunRequest{TaskID: "task_cancel_adopt", Repo: "owner/repo", Instruction: "cancel after adopt"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForExecutionRunExecution(t, s1, run.ID)

	s1.BeginDrain()
	s1.StopRunSupervisors()
	if err := s1.WaitForNoActiveRuns(3 * time.Second); err != nil {
		t.Fatalf("wait for s1 idle: %v", err)
	}

	s2 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-b")
	defer waitForExecutionServerIdle(t, s2)
	s2.RecoverRunningRuns()
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		lease, ok := s2.Store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-b"
	}, "instance-b lease ownership")

	rec := httptest.NewRecorder()
	s2.HandleCancelRun(rec, run.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for adopted run cancel, got %d", rec.Code)
	}

	waitForExecutionCondition(t, 3*time.Second, func() bool {
		updated, ok := s2.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusCanceled && strings.Contains(updated.Error, "canceled by user")
	}, "adopted run canceled")
}

func TestRepeatedHandoffPreservesDetachedExecutionHandle(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &executionFakeRunner{waitCh: waitCh}
	dataDir := t.TempDir()
	statePath := filepath.Join(dataDir, "state.db")

	s1 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-a")
	run, err := s1.CreateAndQueueRun(RunRequest{TaskID: "task_repeated_handoff", Repo: "owner/repo", Instruction: "repeated handoff"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	execRec := waitForExecutionRunExecution(t, s1, run.ID)

	s1.BeginDrain()
	s1.StopRunSupervisors()
	if err := s1.WaitForNoActiveRuns(3 * time.Second); err != nil {
		t.Fatalf("wait for s1 idle: %v", err)
	}

	s2 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-b")
	s2.RecoverRunningRuns()
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		lease, ok := s2.Store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-b"
	}, "instance-b lease ownership")
	midExec, ok := s2.Store.GetRunExecution(run.ID)
	if !ok {
		t.Fatalf("expected execution after first handoff")
	}
	if midExec.ContainerID != execRec.ContainerID {
		t.Fatalf("expected same container id after first handoff: got %s want %s", midExec.ContainerID, execRec.ContainerID)
	}

	s2.BeginDrain()
	s2.StopRunSupervisors()
	if err := s2.Store.DeleteRunLease(run.ID); err != nil {
		t.Fatalf("delete s2 lease: %v", err)
	}

	s3 := newExecutionTestServerWithPaths(t, launcher, dataDir, statePath, "instance-c")
	defer waitForExecutionServerIdle(t, s3)
	s3.RecoverRunningRuns()
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		lease, ok := s3.Store.GetRunLease(run.ID)
		return ok && lease.OwnerID == "instance-c"
	}, "instance-c lease ownership")
	lastExec, ok := s3.Store.GetRunExecution(run.ID)
	if !ok {
		t.Fatalf("expected execution after second handoff")
	}
	if lastExec.ContainerID != execRec.ContainerID {
		t.Fatalf("expected same container id after second handoff: got %s want %s", lastExec.ContainerID, execRec.ContainerID)
	}

	close(waitCh)
	waitForExecutionCondition(t, 3*time.Second, func() bool {
		updated, ok := s3.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "run completion after repeated handoff")
}

func TestDrainStopsSupervisionWithoutCancelingDetachedRun(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &executionFakeRunner{waitCh: waitCh}
	s := newExecutionTestServer(t, launcher)

	run, err := s.CreateAndQueueRun(RunRequest{TaskID: "task_drain_detached", Repo: "owner/repo", Instruction: "drain without cancel"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	execRec := waitForExecutionRunExecution(t, s, run.ID)

	s.BeginDrain()
	s.StopRunSupervisors()
	if err := s.WaitForNoActiveRuns(3 * time.Second); err != nil {
		t.Fatalf("wait for no active runs: %v", err)
	}

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusRunning {
		t.Fatalf("expected running status after drain without cancellation, got %s", updated.Status)
	}
	storedExec, ok := s.Store.GetRunExecution(run.ID)
	if !ok {
		t.Fatalf("expected execution to remain stored while detached run is still active")
	}
	if storedExec.ContainerID != execRec.ContainerID {
		t.Fatalf("expected detached execution handle to remain unchanged: got %s want %s", storedExec.ContainerID, execRec.ContainerID)
	}

	close(waitCh)
}
