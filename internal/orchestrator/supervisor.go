package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/defaults"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

type ExecutionSupervisor struct {
	Config              func() config.ServerConfig
	Store               RunStore
	Runner              runner.Runner
	Broker              credentials.CredentialBroker
	Notifier            RunNotifier
	SM                  *RunStateMachine
	InstanceID          string
	Tick                func() time.Duration
	StartRetryBackoff   func(attempt int) time.Duration
	PauseScheduler      func(until time.Time, reason string) time.Time
	OnRunFinished       func(run state.Run)
	BeforeSuperviseHook func(runID string)
	AfterRunCleanupHook func(runID string)

	mu            sync.Mutex
	runCancels    map[string]context.CancelFunc
	stopRequested bool
}

func NewExecutionSupervisor(configProvider func() config.ServerConfig, store RunStore, runRunner runner.Runner, broker credentials.CredentialBroker, notifier RunNotifier, sm *RunStateMachine, instanceID string) *ExecutionSupervisor {
	return &ExecutionSupervisor{
		Config:     configProvider,
		Store:      store,
		Runner:     runRunner,
		Broker:     broker,
		Notifier:   notifier,
		SM:         sm,
		InstanceID: strings.TrimSpace(instanceID),
		runCancels: make(map[string]context.CancelFunc),
	}
}

func (es *ExecutionSupervisor) config() config.ServerConfig {
	if es != nil && es.Config != nil {
		return es.Config()
	}
	return config.ServerConfig{}
}

func (es *ExecutionSupervisor) supervisorTick() time.Duration {
	if es != nil && es.Tick != nil {
		if interval := es.Tick(); interval > 0 {
			return interval
		}
	}
	return runSupervisorTick
}

func (es *ExecutionSupervisor) startRetryBackoff(attempt int) time.Duration {
	if es != nil && es.StartRetryBackoff != nil {
		if backoff := es.StartRetryBackoff(attempt); backoff > 0 {
			return backoff
		}
		return time.Millisecond
	}
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(attempt) * time.Second
}

func (es *ExecutionSupervisor) addIssueReaction(repo string, issueNumber int, reaction string) {
	if es.Notifier != nil {
		es.Notifier.AddIssueReaction(repo, issueNumber, reaction)
	}
}

func (es *ExecutionSupervisor) cleanupAgentSessions() {
	if es.Notifier != nil {
		es.Notifier.CleanupAgentSessions()
	}
}

func (es *ExecutionSupervisor) notifyRunStarted(run state.Run, sessionMode runtime.SessionMode, sessionResume bool) {
	if es.Notifier != nil {
		es.Notifier.NotifyRunStarted(run, sessionMode, sessionResume)
	}
}

func (es *ExecutionSupervisor) notifyRunTerminal(run state.Run) {
	if es.Notifier != nil {
		es.Notifier.NotifyRunTerminal(run)
	}
}

func (es *ExecutionSupervisor) finishRun(run state.Run) {
	if es.OnRunFinished != nil {
		es.OnRunFinished(run)
	}
}

func pendingRunCancelStatusFromStore(store RunStore, runID string) (string, state.RunStatusReason, bool) {
	if store == nil {
		return "", state.RunStatusReasonNone, false
	}
	req, ok := store.GetRunCancel(runID)
	if !ok {
		return "", state.RunStatusReasonNone, false
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "canceled"
	}
	return reason, statusReasonFromCancelSource(req.Source), true
}

func (es *ExecutionSupervisor) pendingRunCancelStatus(runID string) (string, state.RunStatusReason, bool) {
	return pendingRunCancelStatusFromStore(es.Store, runID)
}

func (es *ExecutionSupervisor) clearRunCancelBestEffort(runID string) {
	if err := es.Store.ClearRunCancel(runID); err != nil {
		log.Printf("run %s clear cancel request failed: %v", runID, err)
	}
}

func (es *ExecutionSupervisor) deleteRunLeaseBestEffort(runID string) {
	if err := es.Store.DeleteRunLease(runID); err != nil {
		log.Printf("run %s delete run lease failed: %v", runID, err)
	}
}

func (es *ExecutionSupervisor) deleteRunExecutionBestEffort(runID string) {
	if err := es.Store.DeleteRunExecution(runID); err != nil {
		log.Printf("run %s delete run execution failed: %v", runID, err)
	}
}

func (es *ExecutionSupervisor) transitionOrGetRun(fallback state.Run, to state.RunStatus, opts ...TransitionOption) (state.Run, error) {
	updated, err := es.SM.Transition(fallback.ID, to, opts...)
	if err != nil {
		if got, ok := es.Store.GetRun(fallback.ID); ok {
			return got, err
		}
		return fallback, err
	}
	return updated, nil
}

func (es *ExecutionSupervisor) activeCredentialLeaseIDForRun(runID string) string {
	lease, ok, err := es.Store.GetActiveCredentialLeaseByRunID(runID)
	if err != nil || !ok {
		return ""
	}
	return lease.ID
}

func (es *ExecutionSupervisor) Recover(ctx context.Context) error {
	var firstErr error
	now := time.Now().UTC()
	for _, run := range es.Store.ListRunningRuns() {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("recover running runs: %w", err)
		}
		if execRec, ok := es.Store.GetRunExecution(run.ID); ok {
			if err := es.recoverDetachedRun(ctx, run, execRec); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if reason, statusReason, ok := es.pendingRunCancelStatus(run.ID); ok {
			if _, err := es.SM.Transition(run.ID, state.StatusCanceled, WithError(reason), WithReason(statusReason)); err != nil && firstErr == nil {
				firstErr = err
			}
			es.clearRunCancelBestEffort(run.ID)
			continue
		}

		lease, hasLease := es.Store.GetRunLease(run.ID)
		if hasLease {
			if lease.LeaseExpiresAt.After(now) {
				continue
			}
			es.deleteRunLeaseBestEffort(run.ID)
			if err := es.SM.Requeue(run.ID); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}

		if run.StartedAt != nil && run.StartedAt.After(now.Add(-runLeaseTTL)) {
			continue
		}
		if err := es.SM.Requeue(run.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (es *ExecutionSupervisor) recoverDetachedRun(ctx context.Context, run state.Run, execRec state.RunExecution) error {
	handle := runExecutionHandle(execRec)
	inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	execState, err := es.Runner.Inspect(inspectCtx, handle)
	switch {
	case errors.Is(err, runner.ErrExecutionNotFound):
		return es.failRunForMissingExecution(run, "detached container missing during adoption")
	case err != nil:
		log.Printf("recover run %s inspect failed, adopting with retry loop: %v", run.ID, err)
		if err := es.Store.UpsertRunLease(run.ID, es.InstanceID, runLeaseTTL); err != nil {
			return fmt.Errorf("recover run %s claim run lease: %w", run.ID, err)
		}
		go es.runSupervision(run.ID, execRec, es.activeCredentialLeaseIDForRun(run.ID))
		return nil
	}

	if execState.Running {
		if _, err := es.Store.UpdateRunExecutionState(run.ID, state.RunExecutionStatusRunning, 0, time.Now().UTC()); err != nil {
			return fmt.Errorf("recover run %s update execution running state: %w", run.ID, err)
		}
		if err := es.Store.UpsertRunLease(run.ID, es.InstanceID, runLeaseTTL); err != nil {
			return fmt.Errorf("recover run %s claim run lease: %w", run.ID, err)
		}
		go es.runSupervision(run.ID, execRec, es.activeCredentialLeaseIDForRun(run.ID))
		return nil
	}

	exitCode := 0
	if execState.ExitCode != nil {
		exitCode = *execState.ExitCode
	}
	if _, err := es.Store.UpdateRunExecutionState(run.ID, state.RunExecutionStatusExited, exitCode, time.Now().UTC()); err != nil {
		return fmt.Errorf("recover run %s update execution exited state: %w", run.ID, err)
	}
	return es.finalizeDetachedRun(run.ID, execRec, exitCode)
}

func (es *ExecutionSupervisor) failRunForMissingExecution(run state.Run, reason string) error {
	updated, err := es.transitionOrGetRun(run, state.StatusFailed, WithError(reason))
	if err != nil {
		log.Printf("run %s fail for missing execution failed: %v", run.ID, err)
	}
	es.deleteRunExecutionBestEffort(run.ID)
	es.deleteRunLeaseBestEffort(run.ID)
	es.finishRun(updated)
	if err != nil {
		return fmt.Errorf("transition run %s after missing execution: %w", run.ID, err)
	}
	return nil
}

func (es *ExecutionSupervisor) prepareRunCredentialAuth(runID, runDir, requesterUserID string, rt runtime.Runtime) (string, error) {
	requesterUserID = strings.TrimSpace(requesterUserID)
	if requesterUserID == "" {
		requesterUserID = "system"
	}
	authDir, authPath := credentialAuthPath(runDir, rt)
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		return "", fmt.Errorf("create auth dir: %w", err)
	}

	if es.Broker != nil {
		lease, err := es.Broker.Acquire(context.Background(), credentials.AcquireRequest{
			RunID:    runID,
			UserID:   requesterUserID,
			Provider: string(rt.Provider()),
		})
		if err == nil {
			tmpFile, err := os.CreateTemp(authDir, "auth-*.tmp")
			if err != nil {
				if releaseErr := es.Broker.Release(context.Background(), lease.ID); releaseErr != nil {
					log.Printf("release credential lease %s after temp auth file create failure failed: %v", lease.ID, releaseErr)
				}
				return "", fmt.Errorf("create temp auth file: %w", err)
			}
			tmpPath := tmpFile.Name()
			cleanupTemp := func() {
				if removeErr := os.Remove(tmpPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					log.Printf("remove temp auth file %s failed: %v", tmpPath, removeErr)
				}
			}
			if _, err := tmpFile.Write(lease.AuthBlob); err != nil {
				if closeErr := tmpFile.Close(); closeErr != nil {
					log.Printf("close temp auth file %s after write failure failed: %v", tmpPath, closeErr)
				}
				cleanupTemp()
				if releaseErr := es.Broker.Release(context.Background(), lease.ID); releaseErr != nil {
					log.Printf("release credential lease %s after auth write failure failed: %v", lease.ID, releaseErr)
				}
				return "", fmt.Errorf("write broker auth file: %w", err)
			}
			if err := tmpFile.Chmod(0o600); err != nil {
				if closeErr := tmpFile.Close(); closeErr != nil {
					log.Printf("close temp auth file %s after chmod failure failed: %v", tmpPath, closeErr)
				}
				cleanupTemp()
				if releaseErr := es.Broker.Release(context.Background(), lease.ID); releaseErr != nil {
					log.Printf("release credential lease %s after auth chmod failure failed: %v", lease.ID, releaseErr)
				}
				return "", fmt.Errorf("chmod broker auth file: %w", err)
			}
			if err := tmpFile.Close(); err != nil {
				cleanupTemp()
				if releaseErr := es.Broker.Release(context.Background(), lease.ID); releaseErr != nil {
					log.Printf("release credential lease %s after auth close failure failed: %v", lease.ID, releaseErr)
				}
				return "", fmt.Errorf("close broker auth file: %w", err)
			}
			if err := os.Rename(tmpPath, authPath); err != nil {
				cleanupTemp()
				if releaseErr := es.Broker.Release(context.Background(), lease.ID); releaseErr != nil {
					log.Printf("release credential lease %s after auth rename failure failed: %v", lease.ID, releaseErr)
				}
				return "", fmt.Errorf("publish broker auth file: %w", err)
			}
			if err := os.Chmod(authPath, 0o600); err != nil {
				if releaseErr := es.Broker.Release(context.Background(), lease.ID); releaseErr != nil {
					log.Printf("release credential lease %s after auth final chmod failure failed: %v", lease.ID, releaseErr)
				}
				return "", fmt.Errorf("chmod published broker auth file: %w", err)
			}
			log.Printf("audit event=credential_lease_acquired run_id=%s credential_id=%s user_id=%s lease_id=%s strategy=%s", runID, lease.CredentialID, requesterUserID, lease.ID, lease.Strategy)
			return lease.ID, nil
		}
		if !errors.Is(err, credentials.ErrNoCredentialAvailable) {
			return "", fmt.Errorf("acquire broker credential: %w", err)
		}
		return "", credentials.ErrNoCredentialAvailable
	}
	return "", nil
}

func (es *ExecutionSupervisor) cleanupRunCredentialAuth(runDir, credentialLeaseID string, rt runtime.Runtime) {
	if strings.TrimSpace(credentialLeaseID) != "" && es.Broker != nil {
		if err := es.Broker.Release(context.Background(), credentialLeaseID); err != nil {
			log.Printf("release credential lease %s failed: %v", credentialLeaseID, err)
		}
	}
	_, authPath := credentialAuthPath(runDir, rt)
	if err := os.Remove(authPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove ephemeral auth file %s failed: %v", authPath, err)
	}
}

func (es *ExecutionSupervisor) Execute(ctx context.Context, runID string) error {
	run, ok := es.Store.GetRun(runID)
	if !ok {
		return nil
	}
	if reason, statusReason, ok := es.pendingRunCancelStatus(runID); ok {
		updated, err := es.transitionOrGetRun(run, state.StatusCanceled, WithError(reason), WithReason(statusReason))
		es.notifyRunTerminal(updated)
		es.finishRun(updated)
		if err != nil {
			return fmt.Errorf("cancel run %s before execution: %w", run.ID, err)
		}
		return nil
	}

	if es.Store.IsTaskCompleted(run.TaskID) {
		updated, err := es.transitionOrGetRun(run, state.StatusCanceled, WithError("task is already completed"), WithReason(state.RunStatusReasonTaskCompleted))
		es.notifyRunTerminal(updated)
		es.finishRun(updated)
		if err != nil {
			return fmt.Errorf("cancel run %s for completed task: %w", run.ID, err)
		}
		return nil
	}

	if run.Status == state.StatusQueued {
		claimedRun, claimed, err := es.Store.ClaimRunStart(runID)
		if err != nil {
			updated, transErr := es.transitionOrGetRun(run, state.StatusFailed, WithError(err.Error()))
			es.notifyRunTerminal(updated)
			es.finishRun(updated)
			if transErr != nil {
				return errors.Join(err, transErr)
			}
			return nil
		}
		run = claimedRun
		if !claimed {
			if run.Status != state.StatusQueued {
				es.finishRun(run)
			}
			return nil
		}
	}
	if run.Status != state.StatusRunning {
		es.finishRun(run)
		return nil
	}
	es.addIssueReaction(run.Repo, run.IssueNumber, ghapi.ReactionEyes)

	if err := es.Store.UpsertRunLease(run.ID, es.InstanceID, runLeaseTTL); err != nil {
		updated, transErr := es.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("claim run lease: %v", err)))
		es.notifyRunTerminal(updated)
		es.finishRun(updated)
		return errors.Join(err, transErr)
	}
	defer func() {
		if err := es.Store.DeleteRunLeaseForOwner(run.ID, es.InstanceID); err != nil {
			log.Printf("failed to delete run lease for %s: %v", run.ID, err)
		}
	}()

	runCredentialInfo, _ := es.Store.GetRunCredentialInfo(run.ID)
	requesterID := strings.TrimSpace(runCredentialInfo.CreatedByUserID)
	if requesterID == "" {
		requesterID = "system"
	}
	credentialLeaseID, err := es.prepareRunCredentialAuth(run.ID, run.RunDir, requesterID, run.AgentRuntime)
	if err != nil {
		updated, transErr := es.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("acquire credential lease: %v", err)))
		es.notifyRunTerminal(updated)
		es.finishRun(updated)
		if transErr != nil {
			return fmt.Errorf("fail run %s after credential acquire error: %w", run.ID, transErr)
		}
		return nil
	}
	defer es.cleanupRunCredentialAuth(run.RunDir, credentialLeaseID, run.AgentRuntime)

	if reason, statusReason, ok := es.pendingRunCancelStatus(runID); ok {
		updated, err := es.transitionOrGetRun(run, state.StatusCanceled, WithError(reason), WithReason(statusReason))
		es.notifyRunTerminal(updated)
		es.finishRun(updated)
		if err != nil {
			return fmt.Errorf("cancel run %s after credential setup: %w", run.ID, err)
		}
		return nil
	}

	cfg := es.config()
	sessionMode := cfg.EffectiveTaskSessionMode()
	if sessionMode != runtime.SessionModeOff {
		es.cleanupAgentSessions()
	}

	sessionResume := runtime.SessionEnabled(sessionMode, runtrigger.Normalize(run.Trigger.String()))
	sessionTaskKey := ""
	sessionTaskDir := ""
	backendSessionID := ""
	sessionRoot := strings.TrimSpace(cfg.EffectiveTaskSessionRoot())
	if sessionRoot == "" {
		sessionRoot = filepath.Join(cfg.DataDir, defaults.AgentSessionDirName)
	}
	if sessionResume {
		sessionTaskKey = runtime.SessionTaskKey(run.Repo, run.TaskID)
		sessionTaskDir = filepath.Join(sessionRoot, sessionTaskKey)
		if existing, ok := es.Store.GetTaskSession(run.TaskID); ok {
			if existing.AgentRuntime == run.AgentRuntime {
				backendSessionID = strings.TrimSpace(existing.RuntimeSessionID)
			} else if err := es.Store.DeleteTaskSession(run.TaskID); err != nil {
				log.Printf("run %s failed to clear stale %s session for task %s: %v", run.ID, existing.AgentRuntime, run.TaskID, err)
			}
		}
		if backendSessionID == "" && run.AgentRuntime.Harness() == runtime.HarnessGoose {
			backendSessionID = runner.TaskSessionName(run.Repo, run.TaskID)
		}
		if err := os.MkdirAll(sessionTaskDir, 0o755); err != nil {
			updated, transErr := es.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("create agent session dir: %v", err)))
			es.notifyRunTerminal(updated)
			es.finishRun(updated)
			return transErr
		}
		if _, err := es.Store.UpsertTaskSession(state.UpsertTaskSessionInput{
			TaskID:           run.TaskID,
			AgentRuntime:     run.AgentRuntime,
			RuntimeSessionID: backendSessionID,
			SessionKey:       sessionTaskKey,
			SessionRoot:      sessionTaskDir,
			LastRunID:        run.ID,
		}); err != nil {
			updated, transErr := es.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("persist agent session: %v", err)))
			es.notifyRunTerminal(updated)
			es.finishRun(updated)
			return transErr
		}
	}

	spec := runner.Spec{
		RunID:        run.ID,
		TaskID:       run.TaskID,
		Repo:         run.Repo,
		Instruction:  run.Instruction,
		AgentRuntime: run.AgentRuntime,
		RunnerImage:  cfg.RunnerImageForRuntime(run.AgentRuntime),
		BaseBranch:   run.BaseBranch,
		HeadBranch:   run.HeadBranch,
		Trigger:      runtrigger.Normalize(run.Trigger.String()),
		RunDir:       run.RunDir,
		SecretsDir:   runner.SecretsDir(run.RunDir),
		IssueNumber:  run.IssueNumber,
		PRNumber:     run.PRNumber,
		Context:      run.Context,
		Debug:        run.Debug,
		ResultReportSocketPath: func() string {
			if provider, ok := es.Notifier.(interface{ RunResultSocketPath() string }); ok {
				return provider.RunResultSocketPath()
			}
			return ""
		}(),
		TaskSession: runner.TaskSessionSpec{
			Mode:             sessionMode,
			Resume:           sessionResume,
			TaskDir:          sessionTaskDir,
			TaskKey:          sessionTaskKey,
			RuntimeSessionID: backendSessionID,
		},
	}
	log.Printf("run %s backend=%s session_mode=%s resume=%t key=%s session_id=%s", run.ID, run.AgentRuntime, sessionMode, sessionResume, sessionTaskKey, backendSessionID)
	execRec, hasExec := es.Store.GetRunExecution(run.ID)
	if !hasExec {
		pendingHandle := runner.ExecutionHandleForRun(run.ID)
		if _, err := es.Store.UpsertRunExecution(state.RunExecution{
			RunID:         run.ID,
			Backend:       state.NormalizeRunExecutionBackend(state.RunExecutionBackend(string(pendingHandle.Backend))),
			ContainerName: pendingHandle.Name,
			ContainerID:   pendingHandle.Name,
			Status:        state.RunExecutionStatusCreated,
			ExitCode:      0,
		}); err != nil {
			updated, transErr := es.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("persist run execution: %v", err)))
			es.notifyRunTerminal(updated)
			es.finishRun(updated)
			return transErr
		}

		handle, err := es.startDetachedWithRetry(ctx, spec)
		if err != nil {
			es.deleteRunExecutionBestEffort(run.ID)
			updated, transErr := es.transitionOrGetRun(run, state.StatusFailed, WithError(err.Error()))
			es.notifyRunTerminal(updated)
			es.finishRun(updated)
			return transErr
		}
		execRec, err = es.Store.UpsertRunExecution(state.RunExecution{
			RunID:         run.ID,
			Backend:       state.NormalizeRunExecutionBackend(state.RunExecutionBackend(string(handle.Backend))),
			ContainerName: strings.TrimSpace(handle.Name),
			ContainerID:   strings.TrimSpace(handle.ID),
			Status:        state.RunExecutionStatusRunning,
			ExitCode:      0,
		})
		if err != nil {
			es.stopRunExecutionBestEffort(run.ID, "failed to persist run execution")
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
			es.removeRunExecutionBestEffort(stopCtx, handle, run.ID, "cleanup failed persisted execution")
			stopCancel()
			updated, transErr := es.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("persist run execution: %v", err)))
			es.notifyRunTerminal(updated)
			es.finishRun(updated)
			return transErr
		}
	}

	if es.BeforeSuperviseHook != nil {
		es.BeforeSuperviseHook(run.ID)
	}
	es.notifyRunStarted(run, sessionMode, sessionResume)
	return es.Supervise(ctx, run.ID, execRec, credentialLeaseID)
}

func (es *ExecutionSupervisor) runSupervision(runID string, execRec state.RunExecution, credentialLeaseID string) {
	if err := es.Supervise(context.Background(), runID, execRec, credentialLeaseID); err != nil {
		log.Printf("run %s supervision failed: %v", runID, err)
	}
}

func (es *ExecutionSupervisor) Supervise(ctx context.Context, runID string, execRec state.RunExecution, credentialLeaseID string) error {
	superviseCtx, cancel := context.WithCancel(ctx)
	es.mu.Lock()
	if es.stopRequested {
		es.mu.Unlock()
		cancel()
		return nil
	}
	if _, exists := es.runCancels[runID]; exists {
		es.mu.Unlock()
		cancel()
		return nil
	}
	es.runCancels[runID] = cancel
	es.mu.Unlock()
	defer func() {
		es.mu.Lock()
		delete(es.runCancels, runID)
		es.mu.Unlock()
		if err := es.Store.DeleteRunLeaseForOwner(runID, es.InstanceID); err != nil {
			log.Printf("failed to delete run lease for %s: %v", runID, err)
		}
		if es.AfterRunCleanupHook != nil {
			es.AfterRunCleanupHook(runID)
		}
		if credentialLeaseID != "" && es.Broker != nil {
			if err := es.Broker.Release(context.Background(), credentialLeaseID); err != nil {
				log.Printf("failed to release credential lease for %s: %v", runID, err)
			}
		}
	}()

	return es.superviseRun(superviseCtx, runID, execRec, credentialLeaseID)
}

func (es *ExecutionSupervisor) superviseRun(ctx context.Context, runID string, execRec state.RunExecution, credentialLeaseID string) error {
	interval := es.supervisorTick()
	if interval <= 0 {
		interval = time.Second
	}
	renewEvery := runLeaseTTL / 3
	if renewEvery <= 0 {
		renewEvery = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	nextRenewAt := time.Now().UTC().Add(renewEvery)
	cfg := es.config()
	credentialRenewEvery := cfg.CredentialRenewEvery
	if credentialRenewEvery <= 0 {
		credentialRenewEvery = 30 * time.Second
	}
	nextCredentialRenewAt := time.Now().UTC().Add(credentialRenewEvery)
	stopRequested := false
	handle := runExecutionHandle(execRec)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !time.Now().UTC().Before(nextRenewAt) {
				ok, err := es.Store.RenewRunLease(runID, es.InstanceID, runLeaseTTL)
				if err != nil {
					log.Printf("run %s lease heartbeat failed: %v", runID, err)
					nextRenewAt = time.Now().UTC().Add(renewEvery)
					continue
				}
				if !ok {
					log.Printf("run %s lease ownership lost; stopping local supervision", runID)
					return nil
				}
				nextRenewAt = time.Now().UTC().Add(renewEvery)
			}
			if credentialLeaseID != "" && es.Broker != nil && !time.Now().UTC().Before(nextCredentialRenewAt) {
				if err := es.Broker.Renew(ctx, credentialLeaseID); err != nil {
					log.Printf("run %s credential lease renew failed: %v", runID, err)
					if cancelErr := es.Store.RequestRunCancel(runID, "credential lease lost", "broker"); cancelErr != nil {
						log.Printf("run %s request cancel after credential lease loss failed: %v", runID, cancelErr)
					}
					if !stopRequested {
						stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
						stopErr := es.Runner.Stop(stopCtx, handle, 10*time.Second)
						stopCancel()
						if stopErr != nil && !errors.Is(stopErr, runner.ErrExecutionNotFound) && !errors.Is(stopErr, context.Canceled) {
							log.Printf("run %s stop after credential lease loss failed: %v", runID, stopErr)
						}
						stopRequested = true
					}
				}
				nextCredentialRenewAt = time.Now().UTC().Add(credentialRenewEvery)
			}

			now := time.Now().UTC()
			execState, err := es.Runner.Inspect(ctx, handle)
			if errors.Is(err, runner.ErrExecutionNotFound) {
				run, ok := es.Store.GetRun(runID)
				if ok {
					return es.failRunForMissingExecution(run, "detached container missing during adoption")
				}
				return nil
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("run %s inspect failed: %v", runID, err)
				}
				continue
			}

			if execState.Running {
				execStatus := state.RunExecutionStatusRunning
				if reason, _, ok := es.pendingRunCancelStatus(runID); ok {
					execStatus = state.RunExecutionStatusStopping
					if !stopRequested {
						stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
						stopErr := es.Runner.Stop(stopCtx, handle, 10*time.Second)
						stopCancel()
						if stopErr != nil && !errors.Is(stopErr, runner.ErrExecutionNotFound) && !errors.Is(stopErr, context.Canceled) {
							log.Printf("run %s stop failed: %v", runID, stopErr)
						}
						log.Printf("run %s cancel requested: %s", runID, reason)
						stopRequested = true
					}
				}
				if _, err := es.Store.UpdateRunExecutionState(runID, execStatus, 0, now); err != nil {
					log.Printf("run %s update execution state %q failed: %v", runID, execStatus, err)
				}
				continue
			}

			exitCode := 0
			if execState.ExitCode != nil {
				exitCode = *execState.ExitCode
			}
			if _, err := es.Store.UpdateRunExecutionState(runID, state.RunExecutionStatusExited, exitCode, now); err != nil {
				log.Printf("run %s update execution exited state failed: %v", runID, err)
			}
			return es.finalizeDetachedRun(runID, execRec, exitCode)
		}
	}
}

func (es *ExecutionSupervisor) finalizeDetachedRun(runID string, execRec state.RunExecution, observedExitCode int) error {
	run, ok := es.Store.GetRun(runID)
	if !ok {
		es.cleanupDetachedExecution(runID, execRec)
		return nil
	}

	if state.IsFinalRunStatus(run.Status) {
		es.cleanupDetachedExecution(runID, execRec)
		es.finishRun(run)
		return nil
	}

	metaPath := filepath.Join(run.RunDir, "meta.json")
	meta, metaErr := runner.ReadMeta(metaPath)
	if metaErr != nil {
		meta = runner.Meta{
			RunID:      run.ID,
			TaskID:     run.TaskID,
			Repo:       run.Repo,
			BaseBranch: run.BaseBranch,
			HeadBranch: run.HeadBranch,
			ExitCode:   observedExitCode,
		}
		if observedExitCode != 0 {
			meta.Error = fmt.Sprintf("docker runner failed with exit code %d", observedExitCode)
		}
		if writeErr := runner.WriteMeta(metaPath, meta); writeErr != nil {
			log.Printf("run %s write fallback meta failed: %v", run.ID, writeErr)
		}
	}
	if meta.ExitCode == 0 && observedExitCode != 0 {
		meta.ExitCode = observedExitCode
	}
	if strings.TrimSpace(meta.TaskSessionID) != "" {
		existing, _ := es.Store.GetTaskSession(run.TaskID)
		sessionKey := ""
		sessionRoot := ""
		if existing.AgentRuntime == run.AgentRuntime {
			sessionKey = existing.SessionKey
			sessionRoot = existing.SessionRoot
		}
		if _, err := es.Store.UpsertTaskSession(state.UpsertTaskSessionInput{
			TaskID:           run.TaskID,
			AgentRuntime:     run.AgentRuntime,
			RuntimeSessionID: strings.TrimSpace(meta.TaskSessionID),
			SessionKey:       sessionKey,
			SessionRoot:      sessionRoot,
			LastRunID:        run.ID,
		}); err != nil {
			log.Printf("run %s failed to persist resolved task session id %q: %v", run.ID, meta.TaskSessionID, err)
		}
	}

	runFailed := meta.ExitCode != 0 || strings.TrimSpace(meta.Error) != ""
	if runFailed {
		if retryAt, reason, ok := detectUsageLimitPause(run, meta.Error); ok {
			effectiveRetryAt := retryAt
			if es.PauseScheduler != nil {
				effectiveRetryAt = es.PauseScheduler(retryAt, fmt.Sprintf("run %s hit provider usage limit: %s", run.ID, reason))
			}
			if err := es.SM.Requeue(run.ID); err != nil {
				log.Printf("run %s usage-limit requeue failed: %v", run.ID, err)
			} else {
				log.Printf("run %s requeued after usage limit; scheduling resumes at %s", run.ID, effectiveRetryAt.Format(time.RFC3339))
				es.cleanupDetachedExecution(runID, execRec)
				if updated, ok := es.Store.GetRun(run.ID); ok {
					es.finishRun(updated)
					return nil
				}
				run.Status = state.StatusQueued
				run.Error = ""
				run.StartedAt = nil
				run.CompletedAt = nil
				es.finishRun(run)
				return nil
			}
		}
	}

	status := state.StatusSucceeded
	prStatus := state.PRStatusNone
	errText := ""
	if meta.ExitCode != 0 || strings.TrimSpace(meta.Error) != "" {
		status = state.StatusFailed
		if strings.TrimSpace(meta.Error) != "" {
			errText = strings.TrimSpace(meta.Error)
		} else {
			errText = fmt.Sprintf("docker runner failed with exit code %d", meta.ExitCode)
		}
	} else if meta.PRNumber > 0 || strings.TrimSpace(meta.PRURL) != "" || run.PRNumber > 0 || strings.TrimSpace(run.PRURL) != "" {
		status = state.StatusReview
		prStatus = state.PRStatusOpen
	}
	statusReason := state.RunStatusReasonNone
	if reason, canceledReason, canceled := es.pendingRunCancelStatus(runID); canceled && status == state.StatusFailed {
		status = state.StatusCanceled
		errText = reason
		statusReason = canceledReason
	}
	tokenUsage, hasTokenUsage, tokenUsageErr := loadRunTokenUsage(run)
	if tokenUsageErr != nil {
		log.Printf("run %s parse token usage failed: %v", run.ID, tokenUsageErr)
	}

	updated, err := es.SM.TransitionBatch(run.ID, status, func(r *state.Run) {
		r.PRNumber = maxInt(r.PRNumber, meta.PRNumber)
		if strings.TrimSpace(meta.PRURL) != "" {
			r.PRURL = strings.TrimSpace(meta.PRURL)
		}
		if strings.TrimSpace(meta.HeadSHA) != "" {
			r.HeadSHA = strings.TrimSpace(meta.HeadSHA)
		}
		r.PRStatus = prStatus
	}, WithError(errText), WithReason(statusReason))
	if err != nil {
		log.Printf("failed to persist detached run result for %s: %v", run.ID, err)
		var fallbackErr error
		updated, fallbackErr = es.transitionOrGetRun(run, state.StatusFailed, WithError(err.Error()))
		if fallbackErr != nil {
			log.Printf("run %s fallback failure transition failed: %v", run.ID, fallbackErr)
		}
	}
	if hasTokenUsage {
		if _, err := es.Store.UpsertRunTokenUsage(tokenUsage); err != nil {
			log.Printf("run %s persist token usage failed: %v", updated.ID, err)
		}
	}

	es.notifyRunTerminal(updated)
	if updated.PRNumber > 0 {
		if err := es.Store.SetTaskPR(updated.TaskID, updated.Repo, updated.PRNumber); err != nil {
			log.Printf("task %s set PR #%d failed: %v", updated.TaskID, updated.PRNumber, err)
		}
	}
	if updated.Status == state.StatusFailed {
		info, ok := es.Store.GetRunCredentialInfo(updated.ID)
		if ok && strings.TrimSpace(info.CredentialID) != "" && credentials.IsAuthFailure(updated.Error) {
			until := time.Now().UTC().Add(5 * time.Minute)
			if err := es.Store.SetCredentialStatus(info.CredentialID, state.CredentialStatusCooldown, &until, updated.Error); err != nil {
				log.Printf("run %s set credential cooldown failed: %v", updated.ID, err)
			} else {
				log.Printf("audit event=credential_cooldown run_id=%s credential_id=%s until=%s", updated.ID, info.CredentialID, until.Format(time.RFC3339))
			}
		}
	}
	es.cleanupDetachedExecution(runID, execRec)
	es.finishRun(updated)
	if err != nil {
		return fmt.Errorf("finalize detached run %s: %w", run.ID, err)
	}
	return nil
}

func (es *ExecutionSupervisor) cleanupDetachedExecution(runID string, execRec state.RunExecution) {
	removeCtx, removeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := es.Runner.Remove(removeCtx, runExecutionHandle(execRec))
	removeCancel()
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s remove detached container failed: %v", runID, err)
	}
	if err := es.Store.DeleteRunExecution(runID); err != nil {
		log.Printf("run %s clear execution state failed: %v", runID, err)
	}
	if err := es.Store.DeleteRunLease(runID); err != nil {
		log.Printf("run %s clear run lease failed: %v", runID, err)
	}
	if run, ok := es.Store.GetRun(runID); ok {
		_, authPath := credentialAuthPath(run.RunDir, run.AgentRuntime)
		if err := os.Remove(authPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("run %s remove auth file failed: %v", runID, err)
		}
	}
}

func (es *ExecutionSupervisor) stopRunExecutionBestEffort(runID string, note string) {
	execRec, ok := es.Store.GetRunExecution(runID)
	if !ok {
		return
	}
	if _, err := es.Store.UpdateRunExecutionState(runID, state.RunExecutionStatusStopping, execRec.ExitCode, time.Now().UTC()); err != nil {
		log.Printf("run %s mark execution stopping failed: %v", runID, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := es.Runner.Stop(stopCtx, runExecutionHandle(execRec), 10*time.Second)
	stopCancel()
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s stop execution failed (%s): %v", runID, note, err)
	}
}

func (es *ExecutionSupervisor) removeRunExecutionBestEffort(ctx context.Context, handle runner.ExecutionHandle, runID, note string) {
	err := es.Runner.Remove(ctx, handle)
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s remove execution failed (%s): %v", runID, note, err)
	}
}

func (es *ExecutionSupervisor) startDetachedWithRetry(ctx context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
	cfg := es.config()
	maxAttempts := cfg.RunnerMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var (
		handle runner.ExecutionHandle
		err    error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return handle, fmt.Errorf("check start-detached context: %w", err)
		}
		handle, err = es.Runner.StartDetached(ctx, spec)
		if err == nil {
			return handle, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return handle, fmt.Errorf("start detached run %s canceled: %w", spec.RunID, context.Canceled)
		}
		if attempt == maxAttempts {
			break
		}
		backoff := es.startRetryBackoff(attempt)
		log.Printf("run %s attempt %d/%d failed: %v (retrying in %s)", spec.RunID, attempt, maxAttempts, err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return handle, fmt.Errorf("start detached run %s canceled during backoff: %w", spec.RunID, context.Canceled)
		case <-timer.C:
		}
	}
	return handle, fmt.Errorf("start detached run %s: %w", spec.RunID, err)
}

func (es *ExecutionSupervisor) Stop() {
	es.mu.Lock()
	es.stopRequested = true
	cancels := make([]context.CancelFunc, 0, len(es.runCancels))
	for _, cancel := range es.runCancels {
		cancels = append(cancels, cancel)
	}
	es.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (es *ExecutionSupervisor) CancelRunningTaskRuns(taskID, reason string, statusReason state.RunStatusReason) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range es.Store.ListRunningRuns() {
		if run.TaskID != taskID {
			continue
		}
		if err := es.Store.RequestRunCancel(run.ID, reason, string(state.NormalizeRunStatusReason(statusReason))); err != nil {
			log.Printf("failed to request run cancel for %s: %v", run.ID, err)
			continue
		}
		es.stopRunExecutionBestEffort(run.ID, "task cancellation")
	}
}

func (es *ExecutionSupervisor) CancelActiveRuns(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range es.Store.ListRunningRuns() {
		if err := es.Store.RequestRunCancel(run.ID, reason, "shutdown"); err != nil {
			log.Printf("run %s request cancel failed: %v", run.ID, err)
		}
		es.stopRunExecutionBestEffort(run.ID, "shutdown cancellation")
	}
}
