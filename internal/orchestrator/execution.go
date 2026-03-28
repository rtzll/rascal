package orchestrator

import (
	"context"
	"log"
	"strings"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

func (s *Server) RecoverRunningRuns() {
	if s.Supervisor == nil {
		return
	}
	s.syncComponents()
	if err := s.Supervisor.Recover(context.Background()); err != nil {
		log.Printf("recover running runs failed: %v", err)
	}
}

func credentialAuthPath(runDir string, rt runtime.Runtime) (dir, file string) {
	return credentials.AuthPath(runDir, runner.SecretsDir(runDir), rt)
}

func (s *Server) ExecuteRun(runID string) {
	if s.Supervisor == nil {
		return
	}
	s.syncComponents()
	if err := s.Supervisor.Execute(context.Background(), runID); err != nil {
		log.Printf("execute run %s failed: %v", runID, err)
	}
}

func (s *Server) StopRunSupervisors() {
	if s.Supervisor != nil {
		s.syncComponents()
		s.Supervisor.Stop()
	}
}

func (s *Server) cancelRunningTaskRuns(taskID, reason string, statusReason state.RunStatusReason) {
	if s.Supervisor != nil {
		s.syncComponents()
		s.Supervisor.CancelRunningTaskRuns(taskID, reason, statusReason)
	}
}

func (s *Server) CancelActiveRuns(reason string) {
	s.CancelActiveRunsWithReason(reason, state.RunStatusReasonShutdown)
}

func (s *Server) CancelActiveRunsWithReason(reason string, statusReason state.RunStatusReason) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range s.Store.ListRunningRuns() {
		if err := s.Store.RequestRunCancel(run.ID, reason, string(state.NormalizeRunStatusReason(statusReason))); err != nil {
			log.Printf("run %s request cancel failed: %v", run.ID, err)
		}
		s.stopRunExecutionBestEffort(run.ID, string(state.NormalizeRunStatusReason(statusReason))+" cancellation")
	}
}

func (s *Server) stopRunExecutionBestEffort(runID string, note string) {
	if s.Supervisor != nil {
		s.syncComponents()
		s.Supervisor.stopRunExecutionBestEffort(runID, note)
	}
}

func runExecutionHandle(execRec state.RunExecution) runner.ExecutionHandle {
	return runner.ExecutionHandle{
		Backend: runner.ExecutionBackend(strings.TrimSpace(string(execRec.Backend))),
		ID:      strings.TrimSpace(execRec.ContainerID),
		Name:    strings.TrimSpace(execRec.ContainerName),
	}
}
