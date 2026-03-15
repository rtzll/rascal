package main

import (
	"fmt"
	"time"

	"github.com/rtzll/rascal/internal/orchestrator"
	"github.com/rtzll/rascal/internal/state"
)

type server = orchestrator.Server
type runRequest = orchestrator.RunRequest
type runResponseTarget = orchestrator.RunResponseTarget
type RunContextFile = orchestrator.RunContextFile
type RunCommentMarker = orchestrator.RunCommentMarker

var errServerDraining = orchestrator.ErrServerDraining

const runLeaseTTL = 90 * time.Second
const runStartCommentBodyMarker = "<!-- rascal:start-comment -->"
const runCompletionCommentBodyMarker = "<!-- rascal:completion-comment -->"
const workerPauseScope = "workers"

func instructionText(run state.Run) string {
	return orchestrator.InstructionText(run)
}

func cleanupStaleAgentSessionDirs(root string, ttlDays int, now time.Time) (int, error) {
	removed, err := orchestrator.CleanupStaleAgentSessionDirs(root, ttlDays, now)
	if err != nil {
		return 0, fmt.Errorf("cleanup stale agent session dirs: %w", err)
	}
	return removed, nil
}

func parseUsageLimitRetryAt(corpus string, now time.Time) (time.Time, string) {
	return orchestrator.ParseUsageLimitRetryAt(corpus, now)
}

func loadRunResponseTarget(runDir string) (runResponseTarget, bool, error) {
	target, ok, err := orchestrator.LoadRunResponseTarget(runDir)
	if err != nil {
		return runResponseTarget{}, false, fmt.Errorf("load run response target: %w", err)
	}
	return target, ok, nil
}

func RunStartCommentMarkerPath(runDir string) string {
	return orchestrator.RunStartCommentMarkerPath(runDir)
}

func RunCompletionCommentMarkerPath(runDir string) string {
	return orchestrator.RunCompletionCommentMarkerPath(runDir)
}

func RunFailureCommentMarkerPath(runDir string) string {
	return orchestrator.RunFailureCommentMarkerPath(runDir)
}

func buildHeadBranch(taskID, task, runID string) string {
	return orchestrator.BuildHeadBranch(taskID, task, runID)
}
