package orchestrator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

func TestExecuteRunHonorsPersistedCancelBeforeStart(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_pre_cancel",
		TaskID:      "task_pre_cancel",
		Repo:        "owner/repo",
		Instruction: "should not start",
		BaseBranch:  "main",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.Store.RequestRunCancel(run.ID, "persisted cancel", "user"); err != nil {
		t.Fatalf("request run cancel: %v", err)
	}

	s.ExecuteRun(run.ID)

	if calls := launcher.Calls(); calls != 0 {
		t.Fatalf("expected launcher not to start, got calls=%d", calls)
	}
	updated, ok := s.Store.GetRun(run.ID)
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
	launcher := &executionFakeRunner{waitCh: waitCh}
	s := newExecutionTestServer(t, launcher)
	defer func() {
		close(waitCh)
		waitForExecutionServerIdle(t, s)
	}()

	run, err := s.CreateAndQueueRun(RunRequest{TaskID: "persisted-cancel", Repo: "owner/repo", Instruction: "cancel while running"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForExecutionRunExecution(t, s, run.ID)

	if err := s.Store.RequestRunCancel(run.ID, "cancel from store", "user"); err != nil {
		t.Fatalf("request run cancel: %v", err)
	}

	waitForExecutionCondition(t, 4*time.Second, func() bool {
		current, ok := s.Store.GetRun(run.ID)
		return ok && current.Status == state.StatusCanceled
	}, "run canceled from persisted request")
	current, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if !strings.Contains(current.Error, "cancel from store") {
		t.Fatalf("expected persisted cancel reason in run error, got %q", current.Error)
	}
}

func TestRecoverQueueStateAppliesPersistedCancel(t *testing.T) {
	t.Parallel()
	s := newExecutionTestServer(t, &executionFakeRunner{})
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_recover_cancel",
		TaskID:      "task_recover_cancel",
		Repo:        "owner/repo",
		Instruction: "recover queued cancel",
		BaseBranch:  "main",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := s.Store.RequestRunCancel(run.ID, "queued canceled before restart", "user"); err != nil {
		t.Fatalf("request run cancel: %v", err)
	}

	s.RecoverQueuedCancels()
	updated, ok := s.Store.GetRun(run.ID)
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
	s := newExecutionTestServer(t, &executionFakeRunner{})
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_recover_expired_lease",
		TaskID:      "task_recover_expired_lease",
		Repo:        "owner/repo",
		Instruction: "recover running expired lease",
		BaseBranch:  "main",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.Store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if err := s.Store.UpsertRunLease(run.ID, "other-instance", time.Nanosecond); err != nil {
		t.Fatalf("upsert run lease: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	s.RecoverRunningRuns()

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusQueued {
		t.Fatalf("expected queued status after recovery, got %s", updated.Status)
	}
	if updated.StartedAt != nil {
		t.Fatalf("expected started_at cleared on requeue")
	}
	if _, ok := s.Store.GetRunLease(run.ID); ok {
		t.Fatalf("expected stale run lease deleted")
	}
}

func TestRecoverRunningRunValidLeaseKeepsRunning(t *testing.T) {
	t.Parallel()
	s := newExecutionTestServer(t, &executionFakeRunner{})
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_recover_valid_lease",
		TaskID:      "task_recover_valid_lease",
		Repo:        "owner/repo",
		Instruction: "recover running valid lease",
		BaseBranch:  "main",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.Store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if err := s.Store.UpsertRunLease(run.ID, "other-instance", 2*time.Minute); err != nil {
		t.Fatalf("upsert run lease: %v", err)
	}

	s.RecoverRunningRuns()

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusRunning {
		t.Fatalf("expected running status with valid lease, got %s", updated.Status)
	}
}

func TestRecoverRunningRunWithoutLeaseOldStartRequeues(t *testing.T) {
	t.Parallel()
	s := newExecutionTestServer(t, &executionFakeRunner{})
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_recover_no_lease_old",
		TaskID:      "task_recover_no_lease_old",
		Repo:        "owner/repo",
		Instruction: "recover running no lease old start",
		BaseBranch:  "main",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := s.Store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	oldStart := time.Now().UTC().Add(-2 * RunLeaseTTL)
	if _, err := s.Store.UpdateRun(run.ID, func(r *state.Run) error {
		r.StartedAt = &oldStart
		return nil
	}); err != nil {
		t.Fatalf("set old started_at: %v", err)
	}

	s.RecoverRunningRuns()

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusQueued {
		t.Fatalf("expected queued status without lease and old start, got %s", updated.Status)
	}
}

func TestLateCancelDoesNotOverwriteSuccessfulCompletion(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	launcher := &executionFakeRunner{waitCh: waitCh}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.CreateAndQueueRun(RunRequest{TaskID: "task_late_cancel_success", Repo: "owner/repo", Instruction: "late cancel success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = waitForExecutionRunExecution(t, s, run.ID)

	close(waitCh)

	rec := httptest.NewRecorder()
	s.HandleCancelRun(rec, run.ID)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for running cancel, got %d", rec.Code)
	}

	waitForExecutionCondition(t, 3*time.Second, func() bool {
		updated, ok := s.Store.GetRun(run.ID)
		return ok && updated.Status == state.StatusSucceeded
	}, "successful completion wins over late cancel")
}
