package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

const pipelineContextFile = "pipeline_context.json"

func pipelineConfigFromAPI(req *api.RunPipelineRequest) (state.RunPipelineConfig, error) {
	if req == nil || !req.Enabled {
		return state.RunPipelineConfig{}, nil
	}
	cfg := state.RunPipelineConfig{
		Enabled:              req.Enabled,
		MaxPhases:            req.MaxPhases,
		MaxChildRunsPerPhase: req.MaxChildRunsPerPhase,
		TokenBudgetTotal:     req.TokenBudgetTotal,
	}
	if req.WallClockBudgetSecs > 0 {
		cfg.WallClockBudget = time.Duration(req.WallClockBudgetSecs) * time.Second
	}
	if len(req.Phases) > 0 {
		cfg.Phases = make([]state.PipelinePhaseName, 0, len(req.Phases))
		for _, raw := range req.Phases {
			phaseName, ok := state.ParsePipelinePhaseName(raw)
			if !ok {
				return state.RunPipelineConfig{}, fmt.Errorf("invalid pipeline phase %q", raw)
			}
			cfg.Phases = append(cfg.Phases, phaseName)
		}
	}
	normalized, err := state.NormalizeRunPipelineConfig(cfg)
	if err != nil {
		return state.RunPipelineConfig{}, fmt.Errorf("normalize pipeline config: %w", err)
	}
	return normalized, nil
}

func (s *server) createAndQueuePipeline(req runRequest, raw *api.RunPipelineRequest) (state.Run, state.RunPipeline, error) {
	if s.isDraining() {
		return state.Run{}, state.RunPipeline{}, errServerDraining
	}
	cfg, err := pipelineConfigFromAPI(raw)
	if err != nil {
		return state.Run{}, state.RunPipeline{}, err
	}
	if !cfg.Enabled {
		return state.Run{}, state.RunPipeline{}, fmt.Errorf("pipeline config must be enabled")
	}

	req.Repo = state.NormalizeRepo(req.Repo)
	req.Task = strings.TrimSpace(req.Task)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.BaseBranch = strings.TrimSpace(req.BaseBranch)
	req.HeadBranch = strings.TrimSpace(req.HeadBranch)
	req.Context = strings.TrimSpace(req.Context)
	req.CreatedByUserID = strings.TrimSpace(req.CreatedByUserID)
	if req.Repo == "" || req.Task == "" {
		return state.Run{}, state.RunPipeline{}, fmt.Errorf("repo and task are required")
	}
	if req.CreatedByUserID == "" {
		req.CreatedByUserID = "system"
	}
	if req.Trigger == "" {
		req.Trigger = runtrigger.NameCLI
	} else {
		req.Trigger = runtrigger.Normalize(req.Trigger.String())
		if !req.Trigger.IsKnown() {
			return state.Run{}, state.RunPipeline{}, fmt.Errorf("unknown workflow trigger %q", req.Trigger)
		}
	}

	pipelineID, err := state.NewPipelineID()
	if err != nil {
		return state.Run{}, state.RunPipeline{}, fmt.Errorf("create pipeline ID: %w", err)
	}
	if req.TaskID == "" {
		req.TaskID = pipelineID
	}
	if s.store.IsTaskCompleted(req.TaskID) {
		return state.Run{}, state.RunPipeline{}, errTaskCompleted
	}
	if existingTask, ok := s.store.GetTask(req.TaskID); ok && existingTask.AgentBackend != s.cfg.AgentBackend {
		if err := s.store.DeleteTaskAgentSession(req.TaskID); err != nil {
			return state.Run{}, state.RunPipeline{}, fmt.Errorf("clear stale task session for backend migration: %w", err)
		}
	}
	lastRun, hasLastRun := s.store.LastRunForTask(req.TaskID)
	if req.BaseBranch == "" {
		if hasLastRun && lastRun.BaseBranch != "" {
			req.BaseBranch = lastRun.BaseBranch
		} else {
			req.BaseBranch = "main"
		}
	}
	if req.HeadBranch == "" {
		req.HeadBranch = buildHeadBranch(req.TaskID, req.Task, pipelineID)
	}

	if _, err := s.store.UpsertTask(state.UpsertTaskInput{
		ID:           req.TaskID,
		Repo:         req.Repo,
		AgentBackend: s.cfg.AgentBackend,
		IssueNumber:  req.IssueNumber,
		PRNumber:     req.PRNumber,
	}); err != nil {
		return state.Run{}, state.RunPipeline{}, fmt.Errorf("upsert task: %w", err)
	}
	if err := s.store.SetTaskCreatedByUser(req.TaskID, req.CreatedByUserID); err != nil {
		return state.Run{}, state.RunPipeline{}, fmt.Errorf("set task requester: %w", err)
	}

	artifactDir := filepath.Join(s.cfg.DataDir, "pipelines", pipelineID, "artifacts")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return state.Run{}, state.RunPipeline{}, fmt.Errorf("create pipeline artifact dir: %w", err)
	}

	pipeline, err := s.store.CreateRunPipeline(state.CreateRunPipelineInput{
		ID:              pipelineID,
		TaskID:          req.TaskID,
		Repo:            req.Repo,
		Task:            req.Task,
		BaseBranch:      req.BaseBranch,
		HeadBranch:      req.HeadBranch,
		Trigger:         req.Trigger.String(),
		IssueNumber:     req.IssueNumber,
		PRNumber:        req.PRNumber,
		Context:         req.Context,
		Debug:           req.Debug == nil || *req.Debug,
		CreatedByUserID: req.CreatedByUserID,
		ArtifactDir:     artifactDir,
		Config:          cfg,
	})
	if err != nil {
		return state.Run{}, state.RunPipeline{}, fmt.Errorf("persist pipeline: %w", err)
	}

	run, launched, err := s.launchNextPipelinePhase(pipeline.ID)
	if err != nil {
		return state.Run{}, state.RunPipeline{}, err
	}
	if !launched {
		return state.Run{}, state.RunPipeline{}, fmt.Errorf("pipeline %s did not launch an initial phase", pipeline.ID)
	}
	if detail, ok := s.store.GetRunPipelineDetail(pipeline.ID); ok {
		pipeline = detail
	}
	return run, pipeline, nil
}

func (s *server) launchNextPipelinePhase(pipelineID string) (state.Run, bool, error) {
	pipeline, ok := s.store.GetRunPipelineDetail(strings.TrimSpace(pipelineID))
	if !ok {
		return state.Run{}, false, fmt.Errorf("pipeline %q not found", pipelineID)
	}
	if pipeline.Status == state.PipelineStatusSucceeded || pipeline.Status == state.PipelineStatusFailed || pipeline.Status == state.PipelineStatusCanceled {
		return state.Run{}, false, nil
	}
	for _, phase := range pipeline.Phases {
		if phase.State == state.PipelinePhaseStateRunning && strings.TrimSpace(phase.RunID) != "" {
			return state.Run{}, false, nil
		}
	}
	if pipeline.CancelRequested {
		return state.Run{}, false, nil
	}

	var nextPhase *state.RunPipelinePhase
	for i := range pipeline.Phases {
		if pipeline.Phases[i].State == state.PipelinePhaseStatePending {
			nextPhase = &pipeline.Phases[i]
			break
		}
	}
	if nextPhase == nil {
		return state.Run{}, false, nil
	}

	runID, err := state.NewRunID()
	if err != nil {
		return state.Run{}, false, fmt.Errorf("create run ID: %w", err)
	}
	runDir := filepath.Join(s.cfg.DataDir, "runs", runID)
	prNumber := s.pipelinePRNumber(pipeline)
	prStatus := state.PRStatusNone
	if prNumber > 0 {
		prStatus = state.PRStatusOpen
	}
	debug := boolPtr(pipeline.Debug)
	run, _, err := s.store.StartRunPipelinePhaseChild(state.StartRunPipelinePhaseChildInput{
		PipelineID: pipeline.ID,
		PhaseName:  nextPhase.PhaseName,
		Run: state.CreateRunInput{
			ID:           runID,
			TaskID:       pipeline.TaskID,
			Repo:         pipeline.Repo,
			Task:         pipeline.Task,
			AgentBackend: s.cfg.AgentBackend,
			BaseBranch:   pipeline.BaseBranch,
			HeadBranch:   pipeline.HeadBranch,
			Trigger:      runtrigger.Normalize(pipeline.Trigger),
			RunDir:       runDir,
			IssueNumber:  pipeline.IssueNumber,
			PRNumber:     prNumber,
			PRStatus:     prStatus,
			Context:      pipeline.Context,
			Debug:        debug,
		},
	})
	if err != nil {
		return state.Run{}, false, fmt.Errorf("create pipeline phase run: %w", err)
	}
	if err := s.store.SetRunCreatedByUser(run.ID, pipeline.CreatedByUserID); err != nil {
		return state.Run{}, false, fmt.Errorf("set run requester: %w", err)
	}
	if err := s.writeRunFiles(run); err != nil {
		updated := s.setRunStatusWithFallback(run, state.StatusFailed, err.Error())
		s.handlePipelineRunFinalized(updated)
		s.finishRun(updated)
		return state.Run{}, false, fmt.Errorf("prepare pipeline run files: %w", err)
	}
	s.scheduleRuns(run.TaskID)
	return run, true, nil
}

func (s *server) pipelinePRNumber(pipeline state.RunPipeline) int {
	prNumber := pipeline.PRNumber
	for _, phase := range pipeline.Phases {
		if strings.TrimSpace(phase.RunID) == "" {
			continue
		}
		run, ok := s.store.GetRun(phase.RunID)
		if !ok {
			continue
		}
		prNumber = maxInt(prNumber, run.PRNumber)
	}
	return prNumber
}

func (s *server) pipelineRunFiles(run state.Run) (map[string]any, string, error) {
	lineage, ok := s.store.GetRunLineage(run.ID)
	if !ok {
		return nil, "", nil
	}
	pipeline, ok := s.store.GetRunPipelineDetail(lineage.ParentPipelineID)
	if !ok {
		return nil, "", fmt.Errorf("pipeline %q not found for run %s", lineage.ParentPipelineID, run.ID)
	}

	handoffDir := filepath.Join(run.RunDir, "handoff")
	if err := os.MkdirAll(handoffDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create handoff dir: %w", err)
	}

	type inputArtifact struct {
		Phase        string `json:"phase"`
		RelativePath string `json:"relative_path"`
	}
	inputs := make([]inputArtifact, 0)
	for _, phase := range pipeline.Phases {
		if phase.PhaseOrder >= lineage.PhaseOrder {
			continue
		}
		for _, sourcePath := range phase.ArtifactPaths {
			base := filepath.Base(sourcePath)
			targetDir := filepath.Join(handoffDir, string(phase.PhaseName))
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				return nil, "", fmt.Errorf("create phase handoff dir: %w", err)
			}
			targetPath := filepath.Join(targetDir, base)
			if err := copyFile(targetPath, sourcePath); err != nil {
				return nil, "", fmt.Errorf("copy artifact %s: %w", sourcePath, err)
			}
			inputs = append(inputs, inputArtifact{
				Phase:        string(phase.PhaseName),
				RelativePath: filepath.ToSlash(filepath.Join("handoff", string(phase.PhaseName), base)),
			})
		}
	}

	ctx := map[string]any{
		"pipeline_id":              pipeline.ID,
		"phase":                    lineage.PhaseName,
		"phase_order":              lineage.PhaseOrder,
		"max_phases":               pipeline.MaxPhases,
		"max_child_runs_per_phase": pipeline.MaxChildRunsPerPhase,
		"head_branch":              pipeline.HeadBranch,
		"base_branch":              pipeline.BaseBranch,
		"required_output":          state.PipelinePhaseArtifactName(lineage.PhaseName),
		"input_artifacts":          inputs,
	}
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("marshal pipeline context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, pipelineContextFile), data, 0o644); err != nil {
		return nil, "", fmt.Errorf("write pipeline context: %w", err)
	}

	var b strings.Builder
	b.WriteString("## Pipeline Phase\n\n")
	fmt.Fprintf(&b, "- Parent pipeline: `%s`\n", pipeline.ID)
	fmt.Fprintf(&b, "- Phase: `%s` (%d/%d)\n", lineage.PhaseName, lineage.PhaseOrder, len(state.FixedPipelinePhases()))
	fmt.Fprintf(&b, "- Shared base branch: `%s`\n", pipeline.BaseBranch)
	fmt.Fprintf(&b, "- Shared head branch: `%s`\n", pipeline.HeadBranch)
	b.WriteString("- Read `/rascal-meta/pipeline_context.json` before starting.\n")
	if len(inputs) == 0 {
		b.WriteString("- There are no prior phase artifacts for this phase.\n")
	} else {
		b.WriteString("- Input artifacts are mounted under `/rascal-meta/handoff/`.\n")
	}
	fmt.Fprintf(&b, "- You must write `/rascal-meta/%s` before finishing.\n", state.PipelinePhaseArtifactName(lineage.PhaseName))
	switch lineage.PhaseName {
	case state.PipelinePhasePlan:
		b.WriteString("- Act as the planner. Define the approach, files likely to change, risks, and acceptance criteria.\n")
		b.WriteString("- Do not rely on implicit transcript state; the next phase will consume the artifact you write.\n")
	case state.PipelinePhaseImplement:
		b.WriteString("- Act as the implementer. Read the planner artifact, make the requested code changes, and summarize what changed.\n")
		b.WriteString("- If you modify code, commit and push the shared head branch before finishing so the verifier can inspect the same result.\n")
	case state.PipelinePhaseVerify:
		b.WriteString("- Act as the verifier. Read prior artifacts, inspect the resulting branch state, and record findings plus checks run.\n")
		b.WriteString("- Do not make broad code changes unless required to complete verification; prefer reporting concrete findings in the artifact.\n")
	}
	return ctx, b.String(), nil
}

func (s *server) handlePipelineRunFinalized(run state.Run) {
	lineage, ok := s.store.GetRunLineage(run.ID)
	if !ok {
		return
	}
	pipeline, ok := s.store.GetRunPipelineDetail(lineage.ParentPipelineID)
	if !ok {
		log.Printf("run %s pipeline %s not found during finalization", run.ID, lineage.ParentPipelineID)
		return
	}

	phaseState := state.PipelinePhaseStateFailed
	switch run.Status {
	case state.StatusSucceeded, state.StatusReview:
		phaseState = state.PipelinePhaseStateSucceeded
	case state.StatusCanceled:
		phaseState = state.PipelinePhaseStateCanceled
	}

	artifactPaths := []string(nil)
	errText := strings.TrimSpace(run.Error)
	if phaseState == state.PipelinePhaseStateSucceeded {
		paths, err := s.capturePipelineArtifacts(pipeline, lineage, run)
		if err != nil {
			phaseState = state.PipelinePhaseStateFailed
			errText = err.Error()
		} else {
			artifactPaths = paths
		}
	}

	var tokenDelta int64
	if usage, ok := s.store.GetRunTokenUsage(run.ID); ok && usage.TotalTokens > 0 {
		tokenDelta = usage.TotalTokens
	}

	updatedPipeline, _, err := s.store.FinalizeRunPipelinePhase(state.FinalizeRunPipelinePhaseInput{
		PipelineID:      pipeline.ID,
		PhaseName:       lineage.PhaseName,
		State:           phaseState,
		ArtifactPaths:   artifactPaths,
		Error:           errText,
		TokenUsageDelta: tokenDelta,
	})
	if err != nil {
		log.Printf("run %s finalize pipeline phase failed: %v", run.ID, err)
		return
	}
	if updatedPipeline.Status == state.PipelineStatusRunning {
		if _, _, err := s.launchNextPipelinePhase(updatedPipeline.ID); err != nil {
			log.Printf("pipeline %s launch next phase failed: %v", updatedPipeline.ID, err)
		}
	}
}

func (s *server) capturePipelineArtifacts(pipeline state.RunPipeline, lineage state.RunLineage, run state.Run) ([]string, error) {
	artifactName := state.PipelinePhaseArtifactName(lineage.PhaseName)
	if strings.TrimSpace(artifactName) == "" {
		return nil, fmt.Errorf("no artifact is defined for phase %s", lineage.PhaseName)
	}
	sourcePath := filepath.Join(run.RunDir, artifactName)
	if _, err := os.Stat(sourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("pipeline phase %s did not produce %s", lineage.PhaseName, artifactName)
		}
		return nil, fmt.Errorf("stat pipeline artifact %s: %w", sourcePath, err)
	}
	destDir := filepath.Join(pipeline.ArtifactDir, string(lineage.PhaseName))
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create pipeline artifact dir: %w", err)
	}
	destPath := filepath.Join(destDir, artifactName)
	if err := copyFile(destPath, sourcePath); err != nil {
		return nil, fmt.Errorf("copy pipeline artifact: %w", err)
	}
	return []string{destPath}, nil
}

func (s *server) recoverPipelines() {
	for _, pipeline := range s.store.ListIncompleteRunPipelines() {
		detail, ok := s.store.GetRunPipelineDetail(pipeline.ID)
		if !ok {
			continue
		}
		if detail.CancelRequested {
			s.cancelActivePipelineChild(detail)
		}

		reconciled := false
		for _, phase := range detail.Phases {
			if phase.State != state.PipelinePhaseStateRunning || strings.TrimSpace(phase.RunID) == "" {
				continue
			}
			run, ok := s.store.GetRun(phase.RunID)
			if !ok {
				_, _, err := s.store.FinalizeRunPipelinePhase(state.FinalizeRunPipelinePhaseInput{
					PipelineID: detail.ID,
					PhaseName:  phase.PhaseName,
					State:      state.PipelinePhaseStateFailed,
					Error:      "pipeline child run record missing during recovery",
				})
				if err != nil {
					log.Printf("pipeline %s recovery finalize missing child failed: %v", detail.ID, err)
				}
				reconciled = true
				break
			}
			if run.Status == state.StatusQueued || run.Status == state.StatusRunning {
				reconciled = true
				break
			}
			s.handlePipelineRunFinalized(run)
			reconciled = true
			break
		}
		if reconciled {
			continue
		}
		if _, _, err := s.launchNextPipelinePhase(detail.ID); err != nil {
			log.Printf("pipeline %s recovery launch failed: %v", detail.ID, err)
		}
	}
}

func (s *server) cancelActivePipelineChild(pipeline state.RunPipeline) {
	for _, phase := range pipeline.Phases {
		if phase.State != state.PipelinePhaseStateRunning || strings.TrimSpace(phase.RunID) == "" {
			continue
		}
		run, ok := s.store.GetRun(phase.RunID)
		if !ok || state.IsFinalRunStatus(run.Status) {
			continue
		}
		if err := s.store.RequestRunCancel(run.ID, "canceled by pipeline parent", "pipeline"); err != nil {
			log.Printf("run %s request cancel for pipeline %s failed: %v", run.ID, pipeline.ID, err)
			return
		}
		if run.Status == state.StatusQueued {
			updated, err := s.store.SetRunStatus(run.ID, state.StatusCanceled, "canceled by pipeline parent")
			if err != nil {
				log.Printf("run %s queued cancel for pipeline %s failed: %v", run.ID, pipeline.ID, err)
				return
			}
			s.finishRun(updated)
			return
		}
		s.stopRunExecutionBestEffort(run.ID, "pipeline parent cancel requested")
		return
	}
}

func (s *server) handleCancelTask(w http.ResponseWriter, taskID string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		http.Error(w, "task id is required", http.StatusBadRequest)
		return
	}
	if pipeline, ok := s.store.GetRunPipelineByTask(taskID); ok {
		updated, err := s.store.RequestRunPipelineCancel(pipeline.ID)
		if err != nil {
			http.Error(w, "failed to cancel pipeline", http.StatusInternalServerError)
			return
		}
		if detail, ok := s.store.GetRunPipelineDetail(updated.ID); ok {
			s.cancelActivePipelineChild(detail)
		}
		accepted := true
		writeJSON(w, http.StatusAccepted, api.AcceptedResponse{Accepted: &accepted})
		return
	}
	s.cancelQueuedRunsBestEffort(taskID, "task canceled")
	s.cancelRunningTaskRuns(taskID, "task canceled")
	accepted := true
	writeJSON(w, http.StatusAccepted, api.AcceptedResponse{Accepted: &accepted})
}

func copyFile(dst, src string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer func() {
		if closeErr := in.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close source file: %w", closeErr)
		}
	}()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close destination file: %w", closeErr)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy file bytes: %w", err)
	}
	if err := out.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod destination file: %w", err)
	}
	return nil
}

func (s *server) isPipelineChildRun(runID string) bool {
	_, ok := s.store.GetRunLineage(runID)
	return ok
}
