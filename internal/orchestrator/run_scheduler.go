package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

type runExecutor interface {
	Execute(ctx context.Context, runID string) error
}

type RunScheduler struct {
	Store            RunStore
	SM               *RunStateMachine
	Supervisor       runExecutor
	InstanceID       string
	ConcurrencyLimit func() int
	IsDraining       func() bool

	mu          sync.Mutex
	scheduleMu  sync.Mutex
	resumeTimer *time.Timer
	resumeAt    time.Time
}

func NewRunScheduler(store RunStore, sm *RunStateMachine, supervisor runExecutor, instanceID string) *RunScheduler {
	return &RunScheduler{
		Store:      store,
		SM:         sm,
		Supervisor: supervisor,
		InstanceID: strings.TrimSpace(instanceID),
	}
}

func (rs *RunScheduler) concurrencyLimit() int {
	if rs != nil && rs.ConcurrencyLimit != nil {
		if limit := rs.ConcurrencyLimit(); limit > 0 {
			return limit
		}
	}
	return 1
}

func (rs *RunScheduler) isDraining() bool {
	return rs != nil && rs.IsDraining != nil && rs.IsDraining()
}

func (rs *RunScheduler) ActiveRunCount() int {
	return rs.Store.CountRunLeasesByOwner(rs.InstanceID)
}

func (rs *RunScheduler) ActivePause() (time.Time, string, bool) {
	pauseUntil, reason, ok, err := rs.Store.ActiveSchedulerPause(schedulerPauseScope, time.Now().UTC())
	if err != nil {
		log.Printf("load active worker pause failed: %v", err)
		return time.Time{}, "", false
	}
	return pauseUntil, reason, ok
}

func (rs *RunScheduler) Pause(until time.Time, reason string) time.Time {
	return rs.PauseUntil(until, reason)
}

func (rs *RunScheduler) PauseUntil(until time.Time, reason string) time.Time {
	if until.IsZero() {
		until = time.Now().UTC().Add(defaultUsageLimitPause)
	}
	effective, err := rs.Store.PauseScheduler(schedulerPauseScope, reason, until)
	if err != nil {
		log.Printf("persist worker pause until %s failed: %v", until.Format(time.RFC3339), err)
		effective = until.UTC()
	}
	rs.ensureResumeTimer(effective)
	return effective
}

func (rs *RunScheduler) Resume(ctx context.Context) error {
	rs.mu.Lock()
	if rs.resumeTimer != nil {
		rs.resumeTimer.Stop()
		rs.resumeTimer = nil
	}
	rs.resumeAt = time.Time{}
	rs.mu.Unlock()
	if err := rs.Store.ClearSchedulerPause(schedulerPauseScope); err != nil {
		return fmt.Errorf("clear scheduler pause: %w", err)
	}
	return rs.Schedule(ctx, "")
}

func (rs *RunScheduler) StopResumeTimer() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.resumeTimer != nil {
		rs.resumeTimer.Stop()
		rs.resumeTimer = nil
	}
	rs.resumeAt = time.Time{}
}

func (rs *RunScheduler) ensureResumeTimer(until time.Time) {
	if until.IsZero() {
		return
	}
	until = until.UTC()
	delay := time.Until(until)
	if delay < 0 {
		delay = 0
	}

	rs.mu.Lock()
	if rs.isDraining() {
		rs.mu.Unlock()
		return
	}
	if !rs.resumeAt.IsZero() && rs.resumeAt.Equal(until) {
		rs.mu.Unlock()
		return
	}
	if rs.resumeTimer != nil {
		rs.resumeTimer.Stop()
	}
	rs.resumeAt = until
	rs.resumeTimer = time.AfterFunc(delay, func() {
		rs.mu.Lock()
		if !rs.resumeAt.Equal(until) {
			rs.mu.Unlock()
			return
		}
		rs.resumeAt = time.Time{}
		rs.resumeTimer = nil
		rs.mu.Unlock()
		if rs.isDraining() {
			return
		}
		if err := rs.Schedule(context.Background(), ""); err != nil {
			log.Printf("resume scheduling failed: %v", err)
		}
	})
	rs.mu.Unlock()
}

func (rs *RunScheduler) Schedule(ctx context.Context, preferredTaskID string) error {
	if rs.isDraining() {
		return nil
	}
	preferredTaskID = strings.TrimSpace(preferredTaskID)

	for _, run := range rs.Store.ListRuns(10000) {
		if run.Status != state.StatusQueued {
			continue
		}
		if reason, statusReason, ok := pendingRunCancelStatusFromStore(rs.Store, run.ID); ok {
			if _, err := rs.SM.Transition(run.ID, state.StatusCanceled, WithError(reason), WithReason(statusReason)); err != nil {
				return fmt.Errorf("cancel queued run %s during schedule: %w", run.ID, err)
			}
			if err := rs.Store.ClearRunCancel(run.ID); err != nil {
				log.Printf("run %s clear cancel request failed: %v", run.ID, err)
			}
		}
	}

	if pauseUntil, pauseReason, paused := rs.ActivePause(); paused {
		rs.ensureResumeTimer(pauseUntil)
		log.Printf("run scheduling paused until %s: %s", pauseUntil.Format(time.RFC3339), pauseReason)
		return nil
	}

	rs.scheduleMu.Lock()
	defer rs.scheduleMu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("schedule runs: %w", err)
		}
		if pauseUntil, pauseReason, paused := rs.ActivePause(); paused {
			rs.ensureResumeTimer(pauseUntil)
			log.Printf("run scheduling paused until %s: %s", pauseUntil.Format(time.RFC3339), pauseReason)
			return nil
		}
		if rs.isDraining() || rs.ActiveRunCount() >= rs.concurrencyLimit() {
			return nil
		}

		run, claimed, err := rs.Store.ClaimNextQueuedRun(preferredTaskID)
		preferredTaskID = ""
		if err != nil {
			return fmt.Errorf("claim next queued run: %w", err)
		}
		if !claimed {
			return nil
		}

		if reason, statusReason, ok := pendingRunCancelStatusFromStore(rs.Store, run.ID); ok {
			if _, err := rs.SM.Transition(run.ID, state.StatusCanceled, WithError(reason), WithReason(statusReason)); err != nil {
				return fmt.Errorf("cancel claimed run %s during schedule: %w", run.ID, err)
			}
			if err := rs.Store.ClearRunCancel(run.ID); err != nil {
				log.Printf("run %s clear cancel request failed: %v", run.ID, err)
			}
			continue
		}

		if rs.isDraining() {
			if _, err := rs.SM.Transition(run.ID, state.StatusCanceled, WithError("orchestrator shutting down"), WithReason(state.RunStatusReasonShutdown)); err != nil {
				return fmt.Errorf("cancel draining run %s during schedule: %w", run.ID, err)
			}
			return nil
		}
		if err := rs.Store.UpsertRunLease(run.ID, rs.InstanceID, runLeaseTTL); err != nil {
			if _, transErr := rs.SM.Transition(run.ID, state.StatusFailed, WithError(fmt.Sprintf("claim run lease: %v", err))); transErr != nil {
				return errors.Join(err, transErr)
			}
			continue
		}

		go func(runID string) {
			if err := rs.Supervisor.Execute(context.Background(), runID); err != nil {
				log.Printf("execute run %s failed: %v", runID, err)
			}
		}(run.ID)
	}
}
