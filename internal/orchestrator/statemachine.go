package orchestrator

import (
	"fmt"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

// RunStateMachine centralizes all run status transitions. It enforces valid
// transitions and manages timestamp invariants (StartedAt, CompletedAt).
// All orchestrator code should use this instead of calling Store status
// methods directly.
type RunStateMachine struct {
	store *state.Store
}

func NewRunStateMachine(store *state.Store) *RunStateMachine {
	return &RunStateMachine{store: store}
}

type transitionConfig struct {
	reason  state.RunStatusReason
	errText string
}

// TransitionOption configures a status transition.
type TransitionOption func(*transitionConfig)

// WithReason sets the StatusReason for the transition (only retained for final statuses).
func WithReason(reason state.RunStatusReason) TransitionOption {
	return func(c *transitionConfig) { c.reason = reason }
}

// WithError sets the error text on the run.
func WithError(errText string) TransitionOption {
	return func(c *transitionConfig) { c.errText = errText }
}

// Transition changes a run's status with validation and proper timestamps.
// Errors are always returned, never swallowed.
func (sm *RunStateMachine) Transition(runID string, to state.RunStatus, opts ...TransitionOption) (state.Run, error) {
	cfg := transitionConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	run, err := sm.store.SetRunStatusWithReason(runID, to, cfg.errText, cfg.reason)
	if err != nil {
		return run, fmt.Errorf("transition run %q to %q: %w", runID, to, err)
	}
	return run, nil
}

// TransitionBatch applies a status change together with additional field
// mutations atomically. The mutations callback may modify fields like
// PRNumber, PRURL, HeadSHA, PRStatus — but must NOT modify Status,
// StatusReason, CompletedAt, or StartedAt, as those are managed by the
// state machine via the status transition.
func (sm *RunStateMachine) TransitionBatch(runID string, to state.RunStatus, mutations func(*state.Run), opts ...TransitionOption) (state.Run, error) {
	cfg := transitionConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	run, err := sm.store.UpdateRun(runID, func(r *state.Run) error {
		if mutations != nil {
			mutations(r)
		}
		now := time.Now().UTC()
		r.Status = to
		r.Error = cfg.errText
		if state.IsFinalRunStatus(to) {
			r.StatusReason = cfg.reason
			r.CompletedAt = &now
		} else {
			r.StatusReason = state.RunStatusReasonNone
		}
		if to == state.StatusRunning {
			r.StartedAt = &now
		}
		return nil
	})
	if err != nil {
		return run, fmt.Errorf("transition run %q to %q: %w", runID, to, err)
	}
	return run, nil
}

// Requeue transitions a running run back to queued, clearing error and timestamps.
func (sm *RunStateMachine) Requeue(runID string) error {
	_, err := sm.store.UpdateRun(runID, func(r *state.Run) error {
		if r.Status != state.StatusRunning {
			return nil
		}
		r.Status = state.StatusQueued
		r.Error = ""
		r.StatusReason = state.RunStatusReasonNone
		r.StartedAt = nil
		r.CompletedAt = nil
		return nil
	})
	if err != nil {
		return fmt.Errorf("requeue run %q: %w", runID, err)
	}
	return nil
}
