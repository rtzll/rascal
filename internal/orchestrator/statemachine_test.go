package orchestrator_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/orchestrator"
	"github.com/rtzll/rascal/internal/state"
)

func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func seedRun(t *testing.T, store *state.Store, runID, taskID string) state.Run {
	t.Helper()
	if _, err := store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := store.AddRun(state.CreateRunInput{
		ID:          runID,
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "test instruction",
		BaseBranch:  "main",
		RunDir:      filepath.Join(t.TempDir(), runID),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	return run
}

// --- Valid lifecycle paths ---

func TestLifecycle_QueuedToRunningToSucceeded(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	run, err := sm.Transition("run_1", state.StatusRunning)
	if err != nil {
		t.Fatalf("queued→running: %v", err)
	}
	if run.Status != state.StatusRunning {
		t.Fatalf("status = %s, want running", run.Status)
	}
	if run.StartedAt == nil {
		t.Fatal("StartedAt should be set when entering running")
	}
	if run.CompletedAt != nil {
		t.Fatal("CompletedAt should be nil when running")
	}

	run, err = sm.Transition("run_1", state.StatusSucceeded)
	if err != nil {
		t.Fatalf("running→succeeded: %v", err)
	}
	if run.Status != state.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", run.Status)
	}
	if run.CompletedAt == nil {
		t.Fatal("CompletedAt should be set on final status")
	}
}

func TestLifecycle_QueuedToRunningToFailed(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatalf("queued→running: %v", err)
	}
	run, err := sm.Transition("run_1", state.StatusFailed, orchestrator.WithError("container crashed"))
	if err != nil {
		t.Fatalf("running→failed: %v", err)
	}
	if run.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed", run.Status)
	}
	if run.Error != "container crashed" {
		t.Fatalf("error = %q, want %q", run.Error, "container crashed")
	}
	if run.CompletedAt == nil {
		t.Fatal("CompletedAt should be set on failed")
	}
}

func TestLifecycle_QueuedToRunningToCanceled(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatalf("queued→running: %v", err)
	}
	run, err := sm.Transition("run_1", state.StatusCanceled,
		orchestrator.WithError("canceled by user"),
		orchestrator.WithReason(state.RunStatusReasonUserCanceled),
	)
	if err != nil {
		t.Fatalf("running→canceled: %v", err)
	}
	if run.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", run.Status)
	}
	if run.StatusReason != state.RunStatusReasonUserCanceled {
		t.Fatalf("reason = %q, want user_canceled", run.StatusReason)
	}
}

func TestLifecycle_QueuedToRunningToReviewToSucceeded(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatalf("queued→running: %v", err)
	}
	run, err := sm.Transition("run_1", state.StatusReview)
	if err != nil {
		t.Fatalf("running→review: %v", err)
	}
	if run.Status != state.StatusReview {
		t.Fatalf("status = %s, want review", run.Status)
	}
	if run.CompletedAt == nil {
		t.Fatal("CompletedAt should be set for review (final status)")
	}

	run, err = sm.Transition("run_1", state.StatusSucceeded)
	if err != nil {
		t.Fatalf("review→succeeded: %v", err)
	}
	if run.Status != state.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", run.Status)
	}
}

func TestLifecycle_QueuedToRunningToReviewToCanceled(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatalf("queued→running: %v", err)
	}
	if _, err := sm.Transition("run_1", state.StatusReview); err != nil {
		t.Fatalf("running→review: %v", err)
	}
	run, err := sm.Transition("run_1", state.StatusCanceled,
		orchestrator.WithError("PR closed"),
		orchestrator.WithReason(state.RunStatusReasonPRClosed),
	)
	if err != nil {
		t.Fatalf("review→canceled: %v", err)
	}
	if run.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", run.Status)
	}
	if run.StatusReason != state.RunStatusReasonPRClosed {
		t.Fatalf("reason = %q, want pr_closed", run.StatusReason)
	}
}

func TestLifecycle_QueuedToCanceled(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	run, err := sm.Transition("run_1", state.StatusCanceled,
		orchestrator.WithError("task completed"),
		orchestrator.WithReason(state.RunStatusReasonTaskCompleted),
	)
	if err != nil {
		t.Fatalf("queued→canceled: %v", err)
	}
	if run.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", run.Status)
	}
	if run.StartedAt != nil {
		t.Fatal("StartedAt should be nil for a run that never started")
	}
	if run.CompletedAt == nil {
		t.Fatal("CompletedAt should be set on canceled")
	}
}

func TestLifecycle_QueuedToFailed(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	run, err := sm.Transition("run_1", state.StatusFailed,
		orchestrator.WithError("no credentials available"),
	)
	if err != nil {
		t.Fatalf("queued→failed: %v", err)
	}
	if run.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed", run.Status)
	}
	if run.StartedAt != nil {
		t.Fatal("StartedAt should be nil for a run that never started")
	}
}

func TestLifecycle_RunningToQueued_Requeue(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatalf("queued→running: %v", err)
	}
	if err := sm.Requeue("run_1"); err != nil {
		t.Fatalf("requeue: %v", err)
	}

	run, ok := store.GetRun("run_1")
	if !ok {
		t.Fatal("run not found after requeue")
	}
	if run.Status != state.StatusQueued {
		t.Fatalf("status = %s, want queued", run.Status)
	}
	if run.Error != "" {
		t.Fatalf("error should be cleared, got %q", run.Error)
	}
	if run.StartedAt != nil {
		t.Fatal("StartedAt should be cleared on requeue")
	}
	if run.CompletedAt != nil {
		t.Fatal("CompletedAt should be cleared on requeue")
	}
}

// --- Invalid transitions ---

func TestTransition_FailedToSucceeded_Rejected(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.Transition("run_1", state.StatusFailed, orchestrator.WithError("crash")); err != nil {
		t.Fatal(err)
	}
	_, err := sm.Transition("run_1", state.StatusSucceeded)
	if err == nil {
		t.Fatal("expected failed→succeeded to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid run status transition") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTransition_CanceledToRunning_Rejected(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusCanceled, orchestrator.WithError("canceled")); err != nil {
		t.Fatal(err)
	}
	_, err := sm.Transition("run_1", state.StatusRunning)
	if err == nil {
		t.Fatal("expected canceled→running to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid run status transition") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTransition_SucceededToFailed_Rejected(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.Transition("run_1", state.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	_, err := sm.Transition("run_1", state.StatusFailed, orchestrator.WithError("oops"))
	if err == nil {
		t.Fatal("expected succeeded→failed to be rejected")
	}
}

func TestTransition_ReviewToRunning_Rejected(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.Transition("run_1", state.StatusReview); err != nil {
		t.Fatal(err)
	}
	_, err := sm.Transition("run_1", state.StatusRunning)
	if err == nil {
		t.Fatal("expected review→running to be rejected")
	}
}

// --- TransitionBatch ---

func TestTransitionBatch_SetsFieldsAtomically(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatal(err)
	}
	run, err := sm.TransitionBatch("run_1", state.StatusReview, func(r *state.Run) {
		r.PRNumber = 42
		r.PRURL = "https://github.com/owner/repo/pull/42"
		r.PRStatus = state.PRStatusOpen
	})
	if err != nil {
		t.Fatalf("transition batch: %v", err)
	}
	if run.Status != state.StatusReview {
		t.Fatalf("status = %s, want review", run.Status)
	}
	if run.PRNumber != 42 {
		t.Fatalf("PRNumber = %d, want 42", run.PRNumber)
	}
	if run.PRURL != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("PRURL = %q", run.PRURL)
	}
	if run.PRStatus != state.PRStatusOpen {
		t.Fatalf("PRStatus = %q, want open", run.PRStatus)
	}
}

func TestTransitionBatch_RejectsInvalidTransition(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.Transition("run_1", state.StatusFailed, orchestrator.WithError("crash")); err != nil {
		t.Fatal(err)
	}

	// Attempt to resurrect a failed run to succeeded via TransitionBatch
	_, err := sm.TransitionBatch("run_1", state.StatusSucceeded, func(r *state.Run) {
		r.PRStatus = state.PRStatusMerged
	})
	if err == nil {
		t.Fatal("expected failed→succeeded via TransitionBatch to be rejected")
	}

	// Verify run is still failed
	run, ok := store.GetRun("run_1")
	if !ok {
		t.Fatal("run not found")
	}
	if run.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed (should not be resurrected)", run.Status)
	}
}

// --- Reconciliation scenarios ---

func TestReconcile_PRMerged_ReviewToSucceeded(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.TransitionBatch("run_1", state.StatusReview, func(r *state.Run) {
		r.PRNumber = 10
		r.PRStatus = state.PRStatusOpen
	}); err != nil {
		t.Fatal(err)
	}

	run, err := sm.TransitionBatch("run_1", state.StatusSucceeded, func(r *state.Run) {
		r.PRStatus = state.PRStatusMerged
	})
	if err != nil {
		t.Fatalf("review→succeeded on merge: %v", err)
	}
	if run.Status != state.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", run.Status)
	}
	if run.PRStatus != state.PRStatusMerged {
		t.Fatalf("PRStatus = %q, want merged", run.PRStatus)
	}
}

func TestReconcile_PRClosed_ReviewToCanceled(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.TransitionBatch("run_1", state.StatusReview, func(r *state.Run) {
		r.PRStatus = state.PRStatusOpen
	}); err != nil {
		t.Fatal(err)
	}

	run, err := sm.TransitionBatch("run_1", state.StatusCanceled, func(r *state.Run) {
		r.PRStatus = state.PRStatusClosedUnmerged
	}, orchestrator.WithError("PR closed without merge"), orchestrator.WithReason(state.RunStatusReasonPRClosed))
	if err != nil {
		t.Fatalf("review→canceled on close: %v", err)
	}
	if run.Status != state.StatusCanceled {
		t.Fatalf("status = %s, want canceled", run.Status)
	}
	if run.PRStatus != state.PRStatusClosedUnmerged {
		t.Fatalf("PRStatus = %q, want closed_unmerged", run.PRStatus)
	}
}

func TestReconcile_PRMerged_FailedStaysFailed(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	if _, err := sm.Transition("run_1", state.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.Transition("run_1", state.StatusFailed, orchestrator.WithError("crash")); err != nil {
		t.Fatal(err)
	}

	// Attempting to transition a failed run to succeeded (simulating PR merge on a failed run)
	// should be rejected — the run should stay failed.
	_, err := sm.TransitionBatch("run_1", state.StatusSucceeded, func(r *state.Run) {
		r.PRStatus = state.PRStatusMerged
	})
	if err == nil {
		t.Fatal("expected failed→succeeded to be rejected even on PR merge")
	}

	run, ok := store.GetRun("run_1")
	if !ok {
		t.Fatal("run not found")
	}
	if run.Status != state.StatusFailed {
		t.Fatalf("status = %s, want failed (must not resurrect)", run.Status)
	}
}

// --- Timestamp invariants ---

func TestTimestamp_StartedAtSetOnRunning(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	run, err := sm.Transition("run_1", state.StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	if run.StartedAt == nil {
		t.Fatal("StartedAt must be set when entering running")
	}
}

func TestTimestamp_CompletedAtSetOnAllFinalStatuses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status state.RunStatus
	}{
		{"review", state.StatusReview},
		{"succeeded", state.StatusSucceeded},
		{"failed", state.StatusFailed},
		{"canceled", state.StatusCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t)
			sm := orchestrator.NewRunStateMachine(store)
			runID := "run_" + tc.name
			seedRun(t, store, runID, "task_"+tc.name)

			if _, err := sm.Transition(runID, state.StatusRunning); err != nil {
				t.Fatal(err)
			}
			run, err := sm.Transition(runID, tc.status)
			if err != nil {
				t.Fatalf("running→%s: %v", tc.name, err)
			}
			if run.CompletedAt == nil {
				t.Fatalf("CompletedAt must be set for %s", tc.name)
			}
		})
	}
}

func TestTimestamp_CompletedAtNilOnNonFinal(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	run, err := sm.Transition("run_1", state.StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	if run.CompletedAt != nil {
		t.Fatal("CompletedAt must be nil for running")
	}
}

// --- StatusReason ---

func TestStatusReason_OnlySavedForFinalStatuses(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	// Setting a reason on a non-final status should be ignored
	run, err := sm.Transition("run_1", state.StatusRunning,
		orchestrator.WithReason(state.RunStatusReasonUserCanceled),
	)
	if err != nil {
		t.Fatal(err)
	}
	if run.StatusReason != state.RunStatusReasonNone {
		t.Fatalf("reason = %q, want empty for non-final status", run.StatusReason)
	}
}

// --- Error propagation ---

func TestTransition_RunNotFound(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)

	_, err := sm.Transition("nonexistent", state.StatusRunning)
	if err == nil {
		t.Fatal("expected error for nonexistent run")
	}
}

func TestRequeue_NoOpIfNotRunning(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	sm := orchestrator.NewRunStateMachine(store)
	seedRun(t, store, "run_1", "task_1")

	// Requeue on a queued run should be a no-op (not an error)
	if err := sm.Requeue("run_1"); err != nil {
		t.Fatalf("requeue on queued run: %v", err)
	}
	run, ok := store.GetRun("run_1")
	if !ok {
		t.Fatal("run not found")
	}
	if run.Status != state.StatusQueued {
		t.Fatalf("status = %s, want queued", run.Status)
	}
}
