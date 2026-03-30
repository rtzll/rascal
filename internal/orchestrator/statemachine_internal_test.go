package orchestrator

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/state"
)

type failingRunStateStore struct {
	run state.Run
	err error
}

func (s *failingRunStateStore) SetRunStatusWithReason(string, state.RunStatus, string, state.RunStatusReason) (state.Run, error) {
	return s.run, s.err
}

func (s *failingRunStateStore) UpdateRun(string, func(*state.Run) error) (state.Run, error) {
	return s.run, s.err
}

func seedStateMachineRun(t *testing.T, runID, taskID string) (*state.Store, string) {
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
	if _, err := store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	if _, err := store.AddRun(state.CreateRunInput{
		ID:          runID,
		TaskID:      taskID,
		Repo:        "owner/repo",
		Instruction: "transition",
		BaseBranch:  "main",
		RunDir:      t.TempDir(),
	}); err != nil {
		t.Fatalf("add run: %v", err)
	}
	return store, runID
}

func moveRunToStatus(t *testing.T, sm *RunStateMachine, runID string, status state.RunStatus) {
	t.Helper()
	switch status {
	case state.StatusQueued:
		return
	case state.StatusRunning:
		if _, err := sm.Transition(runID, state.StatusRunning); err != nil {
			t.Fatalf("transition to running: %v", err)
		}
	case state.StatusReview:
		moveRunToStatus(t, sm, runID, state.StatusRunning)
		if _, err := sm.Transition(runID, state.StatusReview); err != nil {
			t.Fatalf("transition to review: %v", err)
		}
	case state.StatusSucceeded:
		moveRunToStatus(t, sm, runID, state.StatusRunning)
		if _, err := sm.Transition(runID, state.StatusSucceeded); err != nil {
			t.Fatalf("transition to succeeded: %v", err)
		}
	case state.StatusFailed:
		moveRunToStatus(t, sm, runID, state.StatusRunning)
		if _, err := sm.Transition(runID, state.StatusFailed, WithError("failed")); err != nil {
			t.Fatalf("transition to failed: %v", err)
		}
	case state.StatusCanceled:
		if _, err := sm.Transition(runID, state.StatusCanceled, WithError("canceled")); err != nil {
			t.Fatalf("transition to canceled: %v", err)
		}
	default:
		t.Fatalf("unsupported status %s", status)
	}
}

func TestRunStateMachineReturnsStoreErrors(t *testing.T) {
	t.Parallel()
	sm := NewRunStateMachine(&failingRunStateStore{
		run: state.Run{ID: "run_error"},
		err: errors.New("db unavailable"),
	})

	_, err := sm.Transition("run_error", state.StatusRunning)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got == "" || !stateMachineContainsAll(got, "transition run", "db unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunStateMachineAllowsEveryDeclaredTransition(t *testing.T) {
	t.Parallel()

	transitions := map[state.RunStatus][]state.RunStatus{
		state.StatusQueued:    {state.StatusQueued, state.StatusRunning, state.StatusFailed, state.StatusCanceled},
		state.StatusRunning:   {state.StatusQueued, state.StatusRunning, state.StatusReview, state.StatusSucceeded, state.StatusFailed, state.StatusCanceled},
		state.StatusReview:    {state.StatusReview, state.StatusSucceeded, state.StatusCanceled},
		state.StatusSucceeded: {state.StatusSucceeded},
		state.StatusFailed:    {state.StatusFailed},
		state.StatusCanceled:  {state.StatusCanceled},
	}

	for from, next := range transitions {
		from := from
		next := next
		t.Run(string(from), func(t *testing.T) {
			for _, to := range next {
				to := to
				t.Run(string(to), func(t *testing.T) {
					store, runID := seedStateMachineRun(t, "run_"+string(from)+"_"+string(to), "task_"+string(from)+"_"+string(to))
					sm := NewRunStateMachine(store)
					moveRunToStatus(t, sm, runID, from)

					switch to {
					case state.StatusCanceled:
						if _, err := sm.Transition(runID, to, WithError("canceled")); err != nil {
							t.Fatalf("transition %s -> %s: %v", from, to, err)
						}
					case state.StatusFailed:
						if _, err := sm.Transition(runID, to, WithError("failed")); err != nil {
							t.Fatalf("transition %s -> %s: %v", from, to, err)
						}
					default:
						if _, err := sm.Transition(runID, to); err != nil {
							t.Fatalf("transition %s -> %s: %v", from, to, err)
						}
					}
				})
			}
		})
	}
}

func stateMachineContainsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
