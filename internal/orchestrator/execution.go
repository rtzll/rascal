package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/defaults"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

func (s *Server) RecoverRunningRuns() {
	now := time.Now().UTC()
	runs := s.Store.ListRunningRuns()
	for _, run := range runs {
		if exec, ok := s.Store.GetRunExecution(run.ID); ok {
			s.recoverDetachedRun(run, exec)
			continue
		}
		if reason, statusReason, ok := s.pendingRunCancelStatus(run.ID); ok {
			if _, err := s.SM.Transition(run.ID, state.StatusCanceled, WithError(reason), WithReason(statusReason)); err != nil {
				log.Printf("recover run %s cancel failed: %v", run.ID, err)
			}
			s.clearRunCancelBestEffort(run.ID)
			continue
		}

		lease, hasLease := s.Store.GetRunLease(run.ID)
		if hasLease {
			if lease.LeaseExpiresAt.After(now) {
				continue
			}
			s.deleteRunLeaseBestEffort(run.ID)
			if err := s.SM.Requeue(run.ID); err != nil {
				log.Printf("recover run %s after expired lease: %v", run.ID, err)
			}
			continue
		}

		// If there is no lease yet but start time is very recent, keep current
		// state to avoid racing an in-flight lease write.
		if run.StartedAt != nil && run.StartedAt.After(now.Add(-runLeaseTTL)) {
			continue
		}
		if err := s.SM.Requeue(run.ID); err != nil {
			log.Printf("recover run %s without lease: %v", run.ID, err)
		}
	}
}

func (s *Server) recoverDetachedRun(run state.Run, execRec state.RunExecution) {
	handle := runExecutionHandle(execRec)
	credentialHandle, _, credentialErr := s.credentialManager().ActiveHandleForRun(run.ID, run.RunDir, run.AgentRuntime)
	if credentialErr != nil {
		log.Printf("recover run %s lookup credential handle failed: %v", run.ID, credentialErr)
	}
	inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	execState, err := s.Runner.Inspect(inspectCtx, handle)
	switch {
	case errors.Is(err, runner.ErrExecutionNotFound):
		s.failRunForMissingExecution(run, "detached container missing during adoption", credentialHandle)
		return
	case err != nil:
		log.Printf("recover run %s inspect failed, adopting with retry loop: %v", run.ID, err)
		if err := s.Store.UpsertRunLease(run.ID, s.InstanceID, runLeaseTTL); err != nil {
			log.Printf("recover run %s claim run lease failed: %v", run.ID, err)
			return
		}
		go s.superviseDetachedRunLoop(run.ID, execRec, credentialHandle)
		return
	}

	if execState.Running {
		if _, err := s.Store.UpdateRunExecutionState(run.ID, state.RunExecutionStatusRunning, 0, time.Now().UTC()); err != nil {
			log.Printf("recover run %s update execution running state failed: %v", run.ID, err)
		}
		if err := s.Store.UpsertRunLease(run.ID, s.InstanceID, runLeaseTTL); err != nil {
			log.Printf("recover run %s claim run lease failed: %v", run.ID, err)
			return
		}
		go s.superviseDetachedRunLoop(run.ID, execRec, credentialHandle)
		return
	}

	exitCode := 0
	if execState.ExitCode != nil {
		exitCode = *execState.ExitCode
	}
	if _, err := s.Store.UpdateRunExecutionState(run.ID, state.RunExecutionStatusExited, exitCode, time.Now().UTC()); err != nil {
		log.Printf("recover run %s update execution exited state failed: %v", run.ID, err)
	}
	s.finalizeDetachedRun(run.ID, execRec, exitCode, credentialHandle)
}

func (s *Server) failRunForMissingExecution(run state.Run, reason string, credentialHandle *credentials.CredentialHandle) {
	updated, err := s.SM.Transition(run.ID, state.StatusFailed, WithError(reason))
	if err != nil {
		log.Printf("run %s fail for missing execution failed: %v", run.ID, err)
		// Fall back to persisted state for downstream use.
		if got, ok := s.Store.GetRun(run.ID); ok {
			updated = got
		} else {
			updated = run
		}
	}
	s.deleteRunExecutionBestEffort(run.ID)
	s.deleteRunLeaseBestEffort(run.ID)
	if credentialHandle != nil {
		if err := credentialHandle.Release(context.Background()); err != nil {
			log.Printf("run %s release credential after missing execution failed: %v", run.ID, err)
		}
	}
	s.finishRun(updated)
}

func (s *Server) ExecuteRun(runID string) {
	run, ok := s.Store.GetRun(runID)
	if !ok {
		return
	}
	if reason, statusReason, ok := s.pendingRunCancelStatus(runID); ok {
		updated := s.transitionOrGetRun(run, state.StatusCanceled, WithError(reason), WithReason(statusReason))
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}

	if s.Store.IsTaskCompleted(run.TaskID) {
		updated := s.transitionOrGetRun(run, state.StatusCanceled, WithError("task is already completed"), WithReason(state.RunStatusReasonTaskCompleted))
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}

	if run.Status == state.StatusQueued {
		claimedRun, claimed, err := s.Store.ClaimRunStart(runID)
		if err != nil {
			updated := s.transitionOrGetRun(run, state.StatusFailed, WithError(err.Error()))
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
		run = claimedRun
		if !claimed {
			if run.Status != state.StatusQueued {
				s.finishRun(run)
				return
			}
			return
		}
	}
	if run.Status != state.StatusRunning {
		s.finishRun(run)
		return
	}
	s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionEyes)

	if err := s.Store.UpsertRunLease(run.ID, s.InstanceID, runLeaseTTL); err != nil {
		updated := s.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("claim run lease: %v", err)))
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}
	defer func() {
		if err := s.Store.DeleteRunLeaseForOwner(run.ID, s.InstanceID); err != nil {
			log.Printf("failed to delete run lease for %s: %v", run.ID, err)
		}
	}()

	runCredentialInfo, _ := s.Store.GetRunCredentialInfo(run.ID)
	requesterID := strings.TrimSpace(runCredentialInfo.CreatedByUserID)
	if requesterID == "" {
		requesterID = "system"
	}
	credentialHandle, err := s.credentialManager().PrepareCredential(context.Background(), credentials.CredentialRequest{
		RunID:   run.ID,
		RunDir:  run.RunDir,
		UserID:  requesterID,
		Runtime: run.AgentRuntime,
	})
	if err != nil {
		errText := fmt.Sprintf("acquire credential lease: %v", err)
		if errors.Is(err, credentials.ErrNoCredentialAvailable) {
			errText = "acquire credential lease: no credentials available"
		}
		updated := s.transitionOrGetRun(run, state.StatusFailed, WithError(errText))
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}

	if reason, statusReason, ok := s.pendingRunCancelStatus(runID); ok {
		updated := s.transitionOrGetRun(run, state.StatusCanceled, WithError(reason), WithReason(statusReason))
		if credentialHandle != nil {
			if err := credentialHandle.Release(context.Background()); err != nil {
				log.Printf("run %s release credential after cancel failed: %v", run.ID, err)
			}
		}
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}

	sessionMode := s.Config.EffectiveTaskSessionMode()
	if sessionMode != runtime.SessionModeOff {
		s.cleanupAgentSessionsBestEffort()
	}

	sessionResume := runtime.SessionEnabled(sessionMode, runtrigger.Normalize(run.Trigger.String()))
	sessionTaskKey := ""
	sessionTaskDir := ""
	runtimeSessionID := ""
	sessionRoot := strings.TrimSpace(s.Config.EffectiveTaskSessionRoot())
	if sessionRoot == "" {
		sessionRoot = filepath.Join(s.Config.DataDir, defaults.AgentSessionDirName)
	}
	if sessionResume {
		sessionTaskKey = runtime.SessionTaskKey(run.Repo, run.TaskID)
		sessionTaskDir = filepath.Join(sessionRoot, sessionTaskKey)
		if existing, ok := s.Store.GetTaskSession(run.TaskID); ok {
			if existing.AgentRuntime == run.AgentRuntime {
				runtimeSessionID = strings.TrimSpace(existing.RuntimeSessionID)
			} else if err := s.Store.DeleteTaskSession(run.TaskID); err != nil {
				log.Printf("run %s failed to clear stale %s session for task %s: %v", run.ID, existing.AgentRuntime, run.TaskID, err)
			}
		}
		if runtimeSessionID == "" && run.AgentRuntime.Harness() == runtime.HarnessGoose {
			runtimeSessionID = runner.TaskSessionName(run.Repo, run.TaskID)
		}
		if err := os.MkdirAll(sessionTaskDir, 0o755); err != nil {
			updated := s.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("create agent session dir: %v", err)))
			if credentialHandle != nil {
				if err := credentialHandle.Release(context.Background()); err != nil {
					log.Printf("run %s release credential after session dir failure failed: %v", run.ID, err)
				}
			}
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
		if _, err := s.Store.UpsertTaskSession(state.UpsertTaskSessionInput{
			TaskID:           run.TaskID,
			AgentRuntime:     run.AgentRuntime,
			RuntimeSessionID: runtimeSessionID,
			SessionKey:       sessionTaskKey,
			SessionRoot:      sessionTaskDir,
			LastRunID:        run.ID,
		}); err != nil {
			updated := s.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("persist agent session: %v", err)))
			if credentialHandle != nil {
				if err := credentialHandle.Release(context.Background()); err != nil {
					log.Printf("run %s release credential after session persist failure failed: %v", run.ID, err)
				}
			}
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
	}

	spec := runner.Spec{
		RunID:                  run.ID,
		TaskID:                 run.TaskID,
		Repo:                   run.Repo,
		Instruction:            run.Instruction,
		AgentRuntime:           run.AgentRuntime,
		RunnerImage:            s.Config.RunnerImageForRuntime(run.AgentRuntime),
		BaseBranch:             run.BaseBranch,
		HeadBranch:             run.HeadBranch,
		Trigger:                runtrigger.Normalize(run.Trigger.String()),
		RunDir:                 run.RunDir,
		IssueNumber:            run.IssueNumber,
		PRNumber:               run.PRNumber,
		Context:                run.Context,
		Debug:                  run.Debug,
		ResultReportSocketPath: s.RunResultSocketPath(),
		TaskSession: runner.TaskSessionSpec{
			Mode:             sessionMode,
			Resume:           sessionResume,
			TaskDir:          sessionTaskDir,
			TaskKey:          sessionTaskKey,
			RuntimeSessionID: runtimeSessionID,
		},
	}
	log.Printf("run %s runtime=%s session_mode=%s resume=%t key=%s session_id=%s", run.ID, run.AgentRuntime, sessionMode, sessionResume, sessionTaskKey, runtimeSessionID)
	execRec, hasExec := s.Store.GetRunExecution(run.ID)
	if !hasExec {
		// Persist a deterministic handle before launch so the next slot can
		// adopt the container even if this process exits mid-startup.
		pendingHandle := runner.ExecutionHandleForRun(run.ID)
		if _, err := s.Store.UpsertRunExecution(state.RunExecution{
			RunID:         run.ID,
			Backend:       state.NormalizeRunExecutionBackend(state.RunExecutionBackend(string(pendingHandle.Backend))),
			ContainerName: pendingHandle.Name,
			ContainerID:   pendingHandle.Name,
			Status:        state.RunExecutionStatusCreated,
			ExitCode:      0,
		}); err != nil {
			if credentialHandle != nil {
				if err := credentialHandle.Release(context.Background()); err != nil {
					log.Printf("run %s release credential after persist execution failure failed: %v", run.ID, err)
				}
			}
			updated := s.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("persist run execution: %v", err)))
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}

		handle, err := s.startDetachedWithRetry(context.Background(), spec)
		if err != nil {
			s.deleteRunExecutionBestEffort(run.ID)
			if credentialHandle != nil {
				if releaseErr := credentialHandle.Release(context.Background()); releaseErr != nil {
					log.Printf("run %s release credential after start failure failed: %v", run.ID, releaseErr)
				}
			}
			updated := s.transitionOrGetRun(run, state.StatusFailed, WithError(err.Error()))
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
		execRec, err = s.Store.UpsertRunExecution(state.RunExecution{
			RunID:         run.ID,
			Backend:       state.NormalizeRunExecutionBackend(state.RunExecutionBackend(string(handle.Backend))),
			ContainerName: strings.TrimSpace(handle.Name),
			ContainerID:   strings.TrimSpace(handle.ID),
			Status:        state.RunExecutionStatusRunning,
			ExitCode:      0,
		})
		if err != nil {
			s.stopRunExecutionBestEffort(run.ID, "failed to persist run execution")
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
			s.removeRunExecutionBestEffort(stopCtx, handle, run.ID, "cleanup failed persisted execution")
			stopCancel()
			if credentialHandle != nil {
				if releaseErr := credentialHandle.Release(context.Background()); releaseErr != nil {
					log.Printf("run %s release credential after execution persist cleanup failed: %v", run.ID, releaseErr)
				}
			}
			updated := s.transitionOrGetRun(run, state.StatusFailed, WithError(fmt.Sprintf("persist run execution: %v", err)))
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
	}

	if s.BeforeSupervise != nil {
		s.BeforeSupervise(run.ID)
	}
	s.PostRunStartCommentBestEffort(run, sessionMode, sessionResume)
	s.superviseDetachedRunLoop(run.ID, execRec, credentialHandle)
}

func (s *Server) superviseDetachedRunLoop(runID string, execRec state.RunExecution, credentialHandle *credentials.CredentialHandle) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.StopSupervisors {
		s.mu.Unlock()
		cancel()
		return
	}
	if _, exists := s.runCancels[runID]; exists {
		s.mu.Unlock()
		cancel()
		return
	}
	s.runCancels[runID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.runCancels, runID)
		s.mu.Unlock()
		if err := s.Store.DeleteRunLeaseForOwner(runID, s.InstanceID); err != nil {
			log.Printf("failed to delete run lease for %s: %v", runID, err)
		}
		if s.AfterRunCleanup != nil {
			s.AfterRunCleanup(runID)
		}
		if credentialHandle != nil {
			if err := credentialHandle.Release(context.Background()); err != nil {
				log.Printf("failed to release credential lease for %s: %v", runID, err)
			}
		}
	}()

	s.superviseRun(ctx, runID, execRec, credentialHandle)
}

func (s *Server) superviseRun(ctx context.Context, runID string, execRec state.RunExecution, credentialHandle *credentials.CredentialHandle) {
	interval := s.supervisorTick()
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
	credentialRenewEvery := s.Config.CredentialRenewEvery
	if credentialRenewEvery <= 0 {
		credentialRenewEvery = 30 * time.Second
	}
	nextCredentialRenewAt := time.Now().UTC().Add(credentialRenewEvery)
	stopRequested := false
	handle := runExecutionHandle(execRec)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().UTC().Before(nextRenewAt) {
				// Continue with inspect/cancel handling on every tick.
			} else {
				ok, err := s.Store.RenewRunLease(runID, s.InstanceID, runLeaseTTL)
				if err != nil {
					log.Printf("run %s lease heartbeat failed: %v", runID, err)
					nextRenewAt = time.Now().UTC().Add(renewEvery)
					continue
				}
				if !ok {
					log.Printf("run %s lease ownership lost; stopping local supervision", runID)
					return
				}
				nextRenewAt = time.Now().UTC().Add(renewEvery)
			}
			if credentialHandle != nil && !time.Now().UTC().Before(nextCredentialRenewAt) {
				if err := credentialHandle.Renew(ctx); err != nil {
					log.Printf("run %s credential lease renew failed: %v", runID, err)
					if cancelErr := s.Store.RequestRunCancel(runID, "credential lease lost", "broker"); cancelErr != nil {
						log.Printf("run %s request cancel after credential lease loss failed: %v", runID, cancelErr)
					}
					if !stopRequested {
						stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
						stopErr := s.Runner.Stop(stopCtx, handle, 10*time.Second)
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
			execState, err := s.Runner.Inspect(ctx, handle)
			if errors.Is(err, runner.ErrExecutionNotFound) {
				run, ok := s.Store.GetRun(runID)
				if ok {
					s.failRunForMissingExecution(run, "detached container missing during adoption", credentialHandle)
				}
				return
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("run %s inspect failed: %v", runID, err)
				}
				continue
			}

			if execState.Running {
				execStatus := state.RunExecutionStatusRunning
				if reason, _, ok := s.pendingRunCancelStatus(runID); ok {
					execStatus = state.RunExecutionStatusStopping
					if !stopRequested {
						stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
						stopErr := s.Runner.Stop(stopCtx, handle, 10*time.Second)
						stopCancel()
						if stopErr != nil && !errors.Is(stopErr, runner.ErrExecutionNotFound) && !errors.Is(stopErr, context.Canceled) {
							log.Printf("run %s stop failed: %v", runID, stopErr)
						}
						log.Printf("run %s cancel requested: %s", runID, reason)
						stopRequested = true
					}
				}
				if _, err := s.Store.UpdateRunExecutionState(runID, execStatus, 0, now); err != nil {
					log.Printf("run %s update execution state %q failed: %v", runID, execStatus, err)
				}
				continue
			}

			exitCode := 0
			if execState.ExitCode != nil {
				exitCode = *execState.ExitCode
			}
			if _, err := s.Store.UpdateRunExecutionState(runID, state.RunExecutionStatusExited, exitCode, now); err != nil {
				log.Printf("run %s update execution exited state failed: %v", runID, err)
			}
			s.finalizeDetachedRun(runID, execRec, exitCode, credentialHandle)
			return
		}
	}
}

func (s *Server) finalizeDetachedRun(runID string, execRec state.RunExecution, observedExitCode int, credentialHandle *credentials.CredentialHandle) {
	run, ok := s.Store.GetRun(runID)
	if !ok {
		s.cleanupDetachedExecution(runID, execRec)
		return
	}

	if state.IsFinalRunStatus(run.Status) {
		s.cleanupDetachedExecution(runID, execRec)
		s.finishRun(run)
		return
	}

	if latestExec, ok := s.Store.GetRunExecution(runID); ok {
		execRec = latestExec
	}

	metaPath := filepath.Join(run.RunDir, "meta.json")
	meta, metaErr := loadDetachedRunMeta(execRec, run, observedExitCode, metaPath)
	if metaErr != nil {
		log.Printf("run %s using fallback detached run metadata: %v", run.ID, metaErr)
	} else if meta.ExitCode == 0 && observedExitCode != 0 && execRec.ReportedAt == nil {
		meta.ExitCode = observedExitCode
	}
	if strings.TrimSpace(meta.TaskSessionID) != "" {
		existing, _ := s.Store.GetTaskSession(run.TaskID)
		sessionKey := ""
		sessionRoot := ""
		if existing.AgentRuntime == run.AgentRuntime {
			sessionKey = existing.SessionKey
			sessionRoot = existing.SessionRoot
		}
		if _, err := s.Store.UpsertTaskSession(state.UpsertTaskSessionInput{
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
			effectiveRetryAt := s.pauseWorkersUntil(retryAt, fmt.Sprintf("run %s hit provider usage limit: %s", run.ID, reason))
			if err := s.SM.Requeue(run.ID); err != nil {
				log.Printf("run %s usage-limit requeue failed: %v", run.ID, err)
			} else {
				log.Printf("run %s requeued after usage limit; scheduling resumes at %s", run.ID, effectiveRetryAt.Format(time.RFC3339))
				s.cleanupDetachedExecution(runID, execRec)
				if updated, ok := s.Store.GetRun(run.ID); ok {
					s.finishRun(updated)
					return
				}
				run.Status = state.StatusQueued
				run.Error = ""
				run.StartedAt = nil
				run.CompletedAt = nil
				s.finishRun(run)
				return
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
	if reason, canceledReason, canceled := s.pendingRunCancelStatus(runID); canceled && status == state.StatusFailed {
		// Cancellation should explain a stopped execution, but it should not
		// overwrite a successful terminal result that raced with the request.
		status = state.StatusCanceled
		errText = reason
		statusReason = canceledReason
	}
	tokenUsage, hasTokenUsage, tokenUsageErr := loadRunTokenUsage(run)
	if tokenUsageErr != nil {
		log.Printf("run %s parse token usage failed: %v", run.ID, tokenUsageErr)
	}

	updated, err := s.SM.TransitionBatch(run.ID, status, func(r *state.Run) {
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
		updated = s.transitionOrGetRun(run, state.StatusFailed, WithError(err.Error()))
	}
	if hasTokenUsage {
		if _, err := s.Store.UpsertRunTokenUsage(tokenUsage); err != nil {
			log.Printf("run %s persist token usage failed: %v", updated.ID, err)
		}
	}

	s.notifyRunTerminalGitHubBestEffort(updated)
	if updated.PRNumber > 0 {
		s.setTaskPRBestEffort(updated.TaskID, updated.Repo, updated.PRNumber)
	}
	if updated.Status == state.StatusFailed && credentialHandle != nil {
		if err := credentialHandle.MarkAuthFailure(context.Background(), updated.Error, 5*time.Minute); err != nil {
			log.Printf("run %s set credential cooldown failed: %v", updated.ID, err)
		} else if credentials.IsAuthFailure(updated.Error) && strings.TrimSpace(credentialHandle.CredentialID) != "" {
			log.Printf("audit event=credential_cooldown run_id=%s credential_id=%s", updated.ID, credentialHandle.CredentialID)
		}
	}
	s.cleanupDetachedExecution(runID, execRec)
	s.finishRun(updated)
}

func loadDetachedRunMeta(execRec state.RunExecution, run state.Run, observedExitCode int, metaPath string) (runner.Meta, error) {
	if execRec.ReportedAt != nil {
		return runner.Meta{
			RunID:         run.ID,
			TaskID:        run.TaskID,
			Repo:          run.Repo,
			BaseBranch:    run.BaseBranch,
			HeadBranch:    run.HeadBranch,
			PRNumber:      execRec.PRNumber,
			PRURL:         execRec.PRURL,
			HeadSHA:       execRec.HeadSHA,
			TaskSessionID: execRec.TaskSessionID,
			ExitCode:      execRec.ExitCode,
			Error:         execRec.ErrorText,
		}, nil
	}
	meta, err := runner.ReadMeta(metaPath)
	if err == nil {
		return meta, nil
	}
	fallback := runner.Meta{
		RunID:      run.ID,
		TaskID:     run.TaskID,
		Repo:       run.Repo,
		BaseBranch: run.BaseBranch,
		HeadBranch: run.HeadBranch,
		ExitCode:   observedExitCode,
	}
	if observedExitCode != 0 {
		fallback.Error = fmt.Sprintf("docker runner failed with exit code %d", observedExitCode)
	}
	if writeErr := runner.WriteMeta(metaPath, fallback); writeErr != nil {
		log.Printf("run %s write fallback meta failed: %v", run.ID, writeErr)
	}
	return fallback, fmt.Errorf("read detached run meta %s: %w", metaPath, err)
}

func (s *Server) cleanupDetachedExecution(runID string, execRec state.RunExecution) {
	removeCtx, removeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := s.Runner.Remove(removeCtx, runExecutionHandle(execRec))
	removeCancel()
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s remove detached container failed: %v", runID, err)
	}
	if err := s.Store.DeleteRunExecution(runID); err != nil {
		log.Printf("run %s clear execution state failed: %v", runID, err)
	}
	if err := s.Store.DeleteRunLease(runID); err != nil {
		log.Printf("run %s clear run lease failed: %v", runID, err)
	}
	if run, ok := s.Store.GetRun(runID); ok {
		_, authPath := credentials.AuthPath(run.RunDir, run.AgentRuntime)
		if err := os.Remove(authPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("run %s remove auth file failed: %v", runID, err)
		}
	}
}

func (s *Server) stopRunExecutionBestEffort(runID string, note string) {
	execRec, ok := s.Store.GetRunExecution(runID)
	if !ok {
		return
	}
	if _, err := s.Store.UpdateRunExecutionState(runID, state.RunExecutionStatusStopping, execRec.ExitCode, time.Now().UTC()); err != nil {
		log.Printf("run %s mark execution stopping failed: %v", runID, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := s.Runner.Stop(stopCtx, runExecutionHandle(execRec), 10*time.Second)
	stopCancel()
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s stop execution failed (%s): %v", runID, note, err)
	}
}

func runExecutionHandle(execRec state.RunExecution) runner.ExecutionHandle {
	return runner.ExecutionHandle{
		Backend: runner.ExecutionBackend(strings.TrimSpace(string(execRec.Backend))),
		ID:      strings.TrimSpace(execRec.ContainerID),
		Name:    strings.TrimSpace(execRec.ContainerName),
	}
}

func (s *Server) startDetachedWithRetry(ctx context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
	maxAttempts := s.Config.RunnerMaxAttempts
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
		handle, err = s.Runner.StartDetached(ctx, spec)
		if err == nil {
			return handle, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return handle, context.Canceled
		}
		if attempt == maxAttempts {
			break
		}
		backoff := s.startRetryBackoff(attempt)
		log.Printf("run %s attempt %d/%d failed: %v (retrying in %s)", spec.RunID, attempt, maxAttempts, err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return handle, context.Canceled
		case <-timer.C:
		}
	}
	return handle, fmt.Errorf("start detached run %s: %w", spec.RunID, err)
}

func (s *Server) StopRunSupervisors() {
	s.mu.Lock()
	s.StopSupervisors = true
	cancels := make([]context.CancelFunc, 0, len(s.runCancels))
	for _, cancel := range s.runCancels {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *Server) cancelRunningTaskRuns(taskID, reason string, statusReason state.RunStatusReason) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range s.Store.ListRunningRuns() {
		if run.TaskID != taskID {
			continue
		}
		if err := s.Store.RequestRunCancel(run.ID, reason, string(state.NormalizeRunStatusReason(statusReason))); err != nil {
			log.Printf("failed to request run cancel for %s: %v", run.ID, err)
			continue
		}
		s.stopRunExecutionBestEffort(run.ID, "task cancellation")
	}
}

func (s *Server) CancelActiveRuns(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range s.Store.ListRunningRuns() {
		s.requestRunCancelBestEffort(run.ID, reason, "shutdown")
		s.stopRunExecutionBestEffort(run.ID, "shutdown cancellation")
	}
}
