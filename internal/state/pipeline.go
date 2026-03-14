package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/state/sqlitegen"
)

func NewPipelineID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("create pipeline id: %w", err)
	}
	return "pipe_" + hex.EncodeToString(buf), nil
}

func (s *Store) CreateRunPipeline(in CreateRunPipelineInput) (RunPipeline, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.TaskID = strings.TrimSpace(in.TaskID)
	in.Repo = NormalizeRepo(in.Repo)
	in.Task = strings.TrimSpace(in.Task)
	in.BaseBranch = strings.TrimSpace(in.BaseBranch)
	in.HeadBranch = strings.TrimSpace(in.HeadBranch)
	in.Trigger = strings.TrimSpace(in.Trigger)
	in.Context = strings.TrimSpace(in.Context)
	in.CreatedByUserID = strings.TrimSpace(in.CreatedByUserID)
	in.ArtifactDir = strings.TrimSpace(in.ArtifactDir)
	if in.ID == "" || in.TaskID == "" || in.Repo == "" || in.Task == "" {
		return RunPipeline{}, fmt.Errorf("id, task_id, repo, and task are required")
	}
	if in.BaseBranch == "" {
		in.BaseBranch = "main"
	}
	if in.HeadBranch == "" {
		return RunPipeline{}, fmt.Errorf("head branch is required")
	}
	if in.Trigger == "" {
		in.Trigger = "cli"
	}
	if in.ArtifactDir == "" {
		return RunPipeline{}, fmt.Errorf("artifact directory is required")
	}
	cfg, err := NormalizeRunPipelineConfig(in.Config)
	if err != nil {
		return RunPipeline{}, err
	}
	if !cfg.Enabled {
		return RunPipeline{}, fmt.Errorf("pipeline config must be enabled")
	}

	now := time.Now().UTC()
	deadline := sql.NullInt64{}
	if cfg.WallClockBudget > 0 {
		deadline = sql.NullInt64{Int64: now.Add(cfg.WallClockBudget).UnixNano(), Valid: true}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return RunPipeline{}, fmt.Errorf("begin create pipeline transaction for task %q: %w", in.TaskID, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback create pipeline transaction: %v", rollbackErr)
		}
	}()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.GetRunPipelineByTask(context.Background(), in.TaskID); err == nil {
		return RunPipeline{}, fmt.Errorf("pipeline already exists for task %q", in.TaskID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return RunPipeline{}, fmt.Errorf("load pipeline for task %q: %w", in.TaskID, err)
	}

	row, err := qtx.InsertRunPipeline(context.Background(), sqlitegen.InsertRunPipelineParams{
		ID:                   in.ID,
		TaskID:               in.TaskID,
		Repo:                 in.Repo,
		Task:                 in.Task,
		BaseBranch:           in.BaseBranch,
		HeadBranch:           in.HeadBranch,
		Trigger:              in.Trigger,
		IssueNumber:          int64(in.IssueNumber),
		PrNumber:             int64(in.PRNumber),
		Context:              in.Context,
		Debug:                in.Debug,
		CreatedByUserID:      in.CreatedByUserID,
		ArtifactDir:          in.ArtifactDir,
		Status:               string(PipelineStatusPending),
		ActivePhase:          "",
		FailedPhase:          "",
		CancelRequested:      false,
		MaxPhases:            int64(cfg.MaxPhases),
		MaxChildRunsPerPhase: int64(cfg.MaxChildRunsPerPhase),
		TotalChildRuns:       0,
		TokenBudgetTotal:     cfg.TokenBudgetTotal,
		TokenBudgetUsed:      0,
		DeadlineAt:           deadline,
		CreatedAt:            now.UnixNano(),
		UpdatedAt:            now.UnixNano(),
		CompletedAt:          sql.NullInt64{},
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: run_pipelines.task_id") {
			return RunPipeline{}, fmt.Errorf("pipeline already exists for task %q", in.TaskID)
		}
		return RunPipeline{}, fmt.Errorf("insert pipeline %q: %w", in.ID, err)
	}

	enabled := make(map[PipelinePhaseName]struct{}, len(cfg.Phases))
	for _, phase := range cfg.Phases {
		enabled[phase] = struct{}{}
	}
	for _, phaseName := range FixedPipelinePhases() {
		stateValue := PipelinePhaseStatePending
		completedAt := sql.NullInt64{}
		if _, ok := enabled[phaseName]; !ok {
			stateValue = PipelinePhaseStateSkipped
			completedAt = sql.NullInt64{Int64: now.UnixNano(), Valid: true}
		}
		if _, err := qtx.InsertRunPipelinePhase(context.Background(), sqlitegen.InsertRunPipelinePhaseParams{
			PipelineID:    in.ID,
			PhaseName:     string(phaseName),
			PhaseOrder:    int64(PipelinePhasePosition(phaseName)),
			Enabled:       stateValue != PipelinePhaseStateSkipped,
			State:         string(stateValue),
			RunID:         "",
			ChildIndex:    0,
			ArtifactPaths: "[]",
			Error:         "",
			CreatedAt:     now.UnixNano(),
			UpdatedAt:     now.UnixNano(),
			StartedAt:     sql.NullInt64{},
			CompletedAt:   completedAt,
		}); err != nil {
			return RunPipeline{}, fmt.Errorf("insert pipeline phase %q for %q: %w", phaseName, in.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return RunPipeline{}, fmt.Errorf("commit create pipeline transaction for %q: %w", in.ID, err)
	}
	return fromDBRunPipelineRow(row), nil
}

func (s *Store) GetRunPipeline(id string) (RunPipeline, bool) {
	row, err := s.q.GetRunPipeline(context.Background(), strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunPipeline{}, false
		}
		return RunPipeline{}, false
	}
	return fromDBRunPipelineRow(row), true
}

func (s *Store) GetRunPipelineByTask(taskID string) (RunPipeline, bool) {
	row, err := s.q.GetRunPipelineByTask(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunPipeline{}, false
		}
		return RunPipeline{}, false
	}
	return fromDBRunPipelineByTaskRow(row), true
}

func (s *Store) GetRunPipelineDetail(id string) (RunPipeline, bool) {
	pipeline, ok := s.GetRunPipeline(id)
	if !ok {
		return RunPipeline{}, false
	}
	pipeline.Phases = s.ListRunPipelinePhases(id)
	return pipeline, true
}

func (s *Store) GetRunPipelineDetailByTask(taskID string) (RunPipeline, bool) {
	pipeline, ok := s.GetRunPipelineByTask(taskID)
	if !ok {
		return RunPipeline{}, false
	}
	pipeline.Phases = s.ListRunPipelinePhases(pipeline.ID)
	return pipeline, true
}

func (s *Store) ListIncompleteRunPipelines() []RunPipeline {
	rows, err := s.q.ListIncompleteRunPipelines(context.Background())
	if err != nil {
		return nil
	}
	out := make([]RunPipeline, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBListIncompleteRunPipelinesRow(row))
	}
	return out
}

func (s *Store) ListRunPipelinePhases(pipelineID string) []RunPipelinePhase {
	rows, err := s.q.ListRunPipelinePhases(context.Background(), strings.TrimSpace(pipelineID))
	if err != nil {
		return nil
	}
	out := make([]RunPipelinePhase, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBRunPipelinePhaseRow(row))
	}
	return out
}

func (s *Store) GetRunPipelinePhase(pipelineID string, phaseName PipelinePhaseName) (RunPipelinePhase, bool) {
	row, err := s.q.GetRunPipelinePhase(context.Background(), sqlitegen.GetRunPipelinePhaseParams{
		PipelineID: strings.TrimSpace(pipelineID),
		PhaseName:  string(phaseName),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunPipelinePhase{}, false
		}
		return RunPipelinePhase{}, false
	}
	return fromDBRunPipelinePhaseRow(row), true
}

func (s *Store) StartRunPipelinePhaseChild(in StartRunPipelinePhaseChildInput) (Run, RunLineage, error) {
	in.PipelineID = strings.TrimSpace(in.PipelineID)
	if in.PipelineID == "" {
		return Run{}, RunLineage{}, fmt.Errorf("pipeline id is required")
	}
	if PipelinePhasePosition(in.PhaseName) == 0 {
		return Run{}, RunLineage{}, fmt.Errorf("phase name is required")
	}
	in.Run.ID = strings.TrimSpace(in.Run.ID)
	in.Run.TaskID = strings.TrimSpace(in.Run.TaskID)
	in.Run.Repo = NormalizeRepo(in.Run.Repo)
	if in.Run.ID == "" || in.Run.TaskID == "" || in.Run.Repo == "" {
		return Run{}, RunLineage{}, fmt.Errorf("run id, task id, and repo are required")
	}

	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, RunLineage{}, fmt.Errorf("begin start pipeline phase transaction for %q: %w", in.PipelineID, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback start pipeline phase transaction: %v", rollbackErr)
		}
	}()
	qtx := s.q.WithTx(tx)

	pipelineRow, err := qtx.GetRunPipeline(context.Background(), in.PipelineID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, RunLineage{}, fmt.Errorf("pipeline %q not found", in.PipelineID)
		}
		return Run{}, RunLineage{}, fmt.Errorf("load pipeline %q: %w", in.PipelineID, err)
	}
	pipeline := fromDBRunPipelineRow(pipelineRow)
	if pipeline.Status == PipelineStatusSucceeded || pipeline.Status == PipelineStatusFailed || pipeline.Status == PipelineStatusCanceled {
		return Run{}, RunLineage{}, fmt.Errorf("pipeline %q is already completed", in.PipelineID)
	}
	if pipeline.CancelRequested {
		return Run{}, RunLineage{}, fmt.Errorf("pipeline %q is canceling", in.PipelineID)
	}
	if pipeline.TotalChildRuns >= pipeline.MaxPhases {
		return Run{}, RunLineage{}, fmt.Errorf("pipeline %q exhausted max phases", in.PipelineID)
	}

	phaseRow, err := qtx.GetRunPipelinePhase(context.Background(), sqlitegen.GetRunPipelinePhaseParams{
		PipelineID: in.PipelineID,
		PhaseName:  string(in.PhaseName),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, RunLineage{}, fmt.Errorf("pipeline phase %q not found", in.PhaseName)
		}
		return Run{}, RunLineage{}, fmt.Errorf("load pipeline phase %q: %w", in.PhaseName, err)
	}
	phase := fromDBRunPipelinePhaseRow(phaseRow)
	if !phase.Enabled {
		return Run{}, RunLineage{}, fmt.Errorf("pipeline phase %q is disabled", in.PhaseName)
	}
	if phase.State != PipelinePhaseStatePending {
		return Run{}, RunLineage{}, fmt.Errorf("pipeline phase %q is already %s", in.PhaseName, phase.State)
	}
	if phase.ChildIndex >= pipeline.MaxChildRunsPerPhase {
		return Run{}, RunLineage{}, fmt.Errorf("pipeline phase %q exhausted child run budget", in.PhaseName)
	}

	debugEnabled := true
	if in.Run.Debug != nil {
		debugEnabled = *in.Run.Debug
	}
	prStatus := normalizePRStatus(in.Run.PRStatus)
	if prStatus == PRStatusNone && in.Run.PRNumber > 0 {
		prStatus = PRStatusOpen
	}

	runRow, err := qtx.InsertRun(context.Background(), sqlitegen.InsertRunParams{
		ID:           in.Run.ID,
		TaskID:       in.Run.TaskID,
		Repo:         in.Run.Repo,
		Task:         in.Run.Task,
		AgentBackend: in.Run.AgentBackend.String(),
		BaseBranch:   in.Run.BaseBranch,
		HeadBranch:   in.Run.HeadBranch,
		Trigger:      in.Run.Trigger.String(),
		Debug:        debugEnabled,
		Status:       string(StatusQueued),
		RunDir:       in.Run.RunDir,
		IssueNumber:  int64(in.Run.IssueNumber),
		PrNumber:     int64(in.Run.PRNumber),
		PrUrl:        "",
		PrStatus:     string(prStatus),
		HeadSha:      "",
		Context:      in.Run.Context,
		Error:        "",
		CreatedAt:    now.UnixNano(),
		UpdatedAt:    now.UnixNano(),
		StartedAt:    sql.NullInt64{},
		CompletedAt:  sql.NullInt64{},
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: runs.id") {
			return Run{}, RunLineage{}, fmt.Errorf("run %q already exists", in.Run.ID)
		}
		return Run{}, RunLineage{}, fmt.Errorf("insert run %q: %w", in.Run.ID, err)
	}

	lineage := RunLineage{
		RunID:            in.Run.ID,
		ParentPipelineID: in.PipelineID,
		PhaseName:        in.PhaseName,
		PhaseOrder:       phase.PhaseOrder,
		ChildIndex:       phase.ChildIndex + 1,
		CreatedAt:        now,
	}
	if err := qtx.InsertRunLineage(context.Background(), sqlitegen.InsertRunLineageParams{
		RunID:            lineage.RunID,
		ParentPipelineID: lineage.ParentPipelineID,
		PhaseName:        string(lineage.PhaseName),
		PhaseOrder:       int64(lineage.PhaseOrder),
		ChildIndex:       int64(lineage.ChildIndex),
		CreatedAt:        now.UnixNano(),
	}); err != nil {
		return Run{}, RunLineage{}, fmt.Errorf("insert run lineage for %q: %w", in.Run.ID, err)
	}

	phase.State = PipelinePhaseStateRunning
	phase.RunID = in.Run.ID
	phase.ChildIndex++
	phase.Error = ""
	phase.StartedAt = &now
	phase.CompletedAt = nil
	if err := updateRunPipelinePhaseTx(qtx, phase, now); err != nil {
		return Run{}, RunLineage{}, err
	}

	pipeline.Status = PipelineStatusRunning
	pipeline.ActivePhase = in.PhaseName
	pipeline.TotalChildRuns++
	pipeline.UpdatedAt = now
	pipeline.CompletedAt = nil
	if err := updateRunPipelineTx(qtx, pipeline); err != nil {
		return Run{}, RunLineage{}, err
	}

	if _, err := qtx.SetTaskLastRun(context.Background(), sqlitegen.SetTaskLastRunParams{
		LastRunID:   runRow.ID,
		UpdatedAt:   now.UnixNano(),
		IssueNumber: optionalPositiveInt64(in.Run.IssueNumber),
		PrNumber:    optionalPositiveInt64(in.Run.PRNumber),
		ID:          in.Run.TaskID,
	}); err != nil {
		return Run{}, RunLineage{}, fmt.Errorf("set task %q last run to %q: %w", in.Run.TaskID, in.Run.ID, err)
	}
	if err := qtx.TrimOldRuns(context.Background(), int64(s.maxRuns)); err != nil {
		return Run{}, RunLineage{}, fmt.Errorf("trim old runs after pipeline child %q: %w", in.Run.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return Run{}, RunLineage{}, fmt.Errorf("commit start pipeline phase transaction for %q: %w", in.PipelineID, err)
	}

	return fromDBInsertRunRow(runRow), lineage, nil
}

func (s *Store) FinalizeRunPipelinePhase(in FinalizeRunPipelinePhaseInput) (RunPipeline, RunPipelinePhase, error) {
	in.PipelineID = strings.TrimSpace(in.PipelineID)
	in.Error = strings.TrimSpace(in.Error)
	if in.PipelineID == "" {
		return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("pipeline id is required")
	}
	if PipelinePhasePosition(in.PhaseName) == 0 {
		return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("phase name is required")
	}

	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("begin finalize pipeline phase transaction for %q: %w", in.PipelineID, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback finalize pipeline phase transaction: %v", rollbackErr)
		}
	}()
	qtx := s.q.WithTx(tx)

	pipelineRow, err := qtx.GetRunPipeline(context.Background(), in.PipelineID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("pipeline %q not found", in.PipelineID)
		}
		return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("load pipeline %q: %w", in.PipelineID, err)
	}
	pipeline := fromDBRunPipelineRow(pipelineRow)

	phaseRow, err := qtx.GetRunPipelinePhase(context.Background(), sqlitegen.GetRunPipelinePhaseParams{
		PipelineID: in.PipelineID,
		PhaseName:  string(in.PhaseName),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("pipeline phase %q not found", in.PhaseName)
		}
		return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("load pipeline phase %q: %w", in.PhaseName, err)
	}
	phase := fromDBRunPipelinePhaseRow(phaseRow)
	if phase.State == PipelinePhaseStateSucceeded || phase.State == PipelinePhaseStateFailed || phase.State == PipelinePhaseStateSkipped || phase.State == PipelinePhaseStateCanceled {
		if err := tx.Commit(); err != nil {
			return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("commit no-op finalize pipeline phase for %q: %w", in.PipelineID, err)
		}
		return pipeline, phase, nil
	}

	phase.State = in.State
	phase.ArtifactPaths = append([]string(nil), in.ArtifactPaths...)
	phase.Error = in.Error
	phase.CompletedAt = &now
	if phase.StartedAt == nil {
		phase.StartedAt = &now
	}
	if err := updateRunPipelinePhaseTx(qtx, phase, now); err != nil {
		return RunPipeline{}, RunPipelinePhase{}, err
	}

	if in.TokenUsageDelta > 0 {
		pipeline.TokenBudgetUsed += in.TokenUsageDelta
	}
	pipeline.UpdatedAt = now
	pipeline.ActivePhase = ""

	phases, err := qtx.ListRunPipelinePhases(context.Background(), in.PipelineID)
	if err != nil {
		return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("list pipeline phases for %q: %w", in.PipelineID, err)
	}
	phaseState := make([]RunPipelinePhase, 0, len(phases))
	for _, row := range phases {
		current := fromDBRunPipelinePhaseRow(row)
		if current.PhaseName == phase.PhaseName {
			current = phase
		}
		phaseState = append(phaseState, current)
	}

	switch in.State {
	case PipelinePhaseStateSucceeded:
		if pipeline.CancelRequested {
			pipeline.Status = PipelineStatusCanceled
			pipeline.CompletedAt = &now
			for i := range phaseState {
				if phaseState[i].State == PipelinePhaseStatePending {
					phaseState[i].State = PipelinePhaseStateCanceled
					phaseState[i].Error = "pipeline canceled"
					phaseState[i].CompletedAt = &now
					if err := updateRunPipelinePhaseTx(qtx, phaseState[i], now); err != nil {
						return RunPipeline{}, RunPipelinePhase{}, err
					}
				}
			}
			break
		}

		next := nextPendingPipelinePhase(phaseState, phase.PhaseOrder)
		if next == nil {
			pipeline.Status = PipelineStatusSucceeded
			pipeline.CompletedAt = &now
			break
		}
		if reason := pipelineLimitFailure(pipeline, *next, now); reason != "" {
			pipeline.Status = PipelineStatusFailed
			pipeline.FailedPhase = next.PhaseName
			pipeline.CompletedAt = &now
			next.State = PipelinePhaseStateSkipped
			next.Error = reason
			next.CompletedAt = &now
			if err := updateRunPipelinePhaseTx(qtx, *next, now); err != nil {
				return RunPipeline{}, RunPipelinePhase{}, err
			}
			break
		}
		pipeline.Status = PipelineStatusRunning
		pipeline.ActivePhase = next.PhaseName
	case PipelinePhaseStateCanceled:
		pipeline.Status = PipelineStatusCanceled
		pipeline.CompletedAt = &now
		for i := range phaseState {
			if phaseState[i].PhaseOrder <= phase.PhaseOrder || phaseState[i].State != PipelinePhaseStatePending {
				continue
			}
			phaseState[i].State = PipelinePhaseStateCanceled
			phaseState[i].Error = "pipeline canceled"
			phaseState[i].CompletedAt = &now
			if err := updateRunPipelinePhaseTx(qtx, phaseState[i], now); err != nil {
				return RunPipeline{}, RunPipelinePhase{}, err
			}
		}
	default:
		pipeline.Status = PipelineStatusFailed
		pipeline.FailedPhase = phase.PhaseName
		pipeline.CompletedAt = &now
		for i := range phaseState {
			if phaseState[i].PhaseOrder <= phase.PhaseOrder || phaseState[i].State != PipelinePhaseStatePending {
				continue
			}
			phaseState[i].State = PipelinePhaseStateSkipped
			phaseState[i].Error = "pipeline stopped on earlier phase failure"
			phaseState[i].CompletedAt = &now
			if err := updateRunPipelinePhaseTx(qtx, phaseState[i], now); err != nil {
				return RunPipeline{}, RunPipelinePhase{}, err
			}
		}
	}

	if err := updateRunPipelineTx(qtx, pipeline); err != nil {
		return RunPipeline{}, RunPipelinePhase{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunPipeline{}, RunPipelinePhase{}, fmt.Errorf("commit finalize pipeline phase transaction for %q: %w", in.PipelineID, err)
	}

	return pipeline, phase, nil
}

func (s *Store) RequestRunPipelineCancel(pipelineID string) (RunPipeline, error) {
	pipelineID = strings.TrimSpace(pipelineID)
	if pipelineID == "" {
		return RunPipeline{}, fmt.Errorf("pipeline id is required")
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return RunPipeline{}, fmt.Errorf("begin cancel pipeline transaction for %q: %w", pipelineID, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback cancel pipeline transaction: %v", rollbackErr)
		}
	}()
	qtx := s.q.WithTx(tx)

	pipelineRow, err := qtx.GetRunPipeline(context.Background(), pipelineID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunPipeline{}, fmt.Errorf("pipeline %q not found", pipelineID)
		}
		return RunPipeline{}, fmt.Errorf("load pipeline %q: %w", pipelineID, err)
	}
	pipeline := fromDBRunPipelineRow(pipelineRow)
	if pipeline.Status == PipelineStatusSucceeded || pipeline.Status == PipelineStatusFailed || pipeline.Status == PipelineStatusCanceled {
		if err := tx.Commit(); err != nil {
			return RunPipeline{}, fmt.Errorf("commit no-op cancel pipeline for %q: %w", pipelineID, err)
		}
		return pipeline, nil
	}

	pipeline.CancelRequested = true
	pipeline.UpdatedAt = now

	phases, err := qtx.ListRunPipelinePhases(context.Background(), pipelineID)
	if err != nil {
		return RunPipeline{}, fmt.Errorf("list pipeline phases for %q: %w", pipelineID, err)
	}
	activeRunning := false
	for _, row := range phases {
		phase := fromDBRunPipelinePhaseRow(row)
		if phase.State == PipelinePhaseStateRunning {
			activeRunning = true
			break
		}
	}
	if !activeRunning {
		pipeline.Status = PipelineStatusCanceled
		pipeline.CompletedAt = &now
		pipeline.ActivePhase = ""
		for _, row := range phases {
			phase := fromDBRunPipelinePhaseRow(row)
			if phase.State != PipelinePhaseStatePending {
				continue
			}
			phase.State = PipelinePhaseStateCanceled
			phase.Error = "pipeline canceled"
			phase.CompletedAt = &now
			if err := updateRunPipelinePhaseTx(qtx, phase, now); err != nil {
				return RunPipeline{}, err
			}
		}
	}

	if err := updateRunPipelineTx(qtx, pipeline); err != nil {
		return RunPipeline{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunPipeline{}, fmt.Errorf("commit cancel pipeline transaction for %q: %w", pipelineID, err)
	}
	return pipeline, nil
}

func (s *Store) GetRunLineage(runID string) (RunLineage, bool) {
	row, err := s.q.GetRunLineage(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunLineage{}, false
		}
		return RunLineage{}, false
	}
	return fromDBRunLineageRow(row), true
}

func updateRunPipelineTx(qtx *sqlitegen.Queries, pipeline RunPipeline) error {
	rows, err := qtx.UpdateRunPipeline(context.Background(), toDBUpdateRunPipelineParams(pipeline))
	if err != nil {
		return fmt.Errorf("update pipeline %q: %w", pipeline.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("pipeline %q not found", pipeline.ID)
	}
	return nil
}

func updateRunPipelinePhaseTx(qtx *sqlitegen.Queries, phase RunPipelinePhase, now time.Time) error {
	phase.UpdatedAt = now
	rows, err := qtx.UpdateRunPipelinePhase(context.Background(), toDBUpdateRunPipelinePhaseParams(phase))
	if err != nil {
		return fmt.Errorf("update pipeline phase %q for pipeline %q: %w", phase.PhaseName, phase.PipelineID, err)
	}
	if rows == 0 {
		return fmt.Errorf("pipeline phase %q for pipeline %q not found", phase.PhaseName, phase.PipelineID)
	}
	return nil
}

func nextPendingPipelinePhase(phases []RunPipelinePhase, completedOrder int) *RunPipelinePhase {
	for i := range phases {
		if phases[i].PhaseOrder <= completedOrder || phases[i].State != PipelinePhaseStatePending {
			continue
		}
		return &phases[i]
	}
	return nil
}

func pipelineLimitFailure(pipeline RunPipeline, next RunPipelinePhase, now time.Time) string {
	if pipeline.TotalChildRuns >= pipeline.MaxPhases {
		return fmt.Sprintf("pipeline phase budget exhausted before %s", next.PhaseName)
	}
	if pipeline.TokenBudgetTotal > 0 && pipeline.TokenBudgetUsed >= pipeline.TokenBudgetTotal {
		return fmt.Sprintf("pipeline token budget exhausted before %s", next.PhaseName)
	}
	if pipeline.DeadlineAt != nil && now.After(*pipeline.DeadlineAt) {
		return fmt.Sprintf("pipeline wall-clock budget exhausted before %s", next.PhaseName)
	}
	return ""
}

func fromDBRunPipelineRow(row sqlitegen.RunPipeline) RunPipeline {
	return fromDBRunPipelineParts(row.ID, row.TaskID, row.Repo, row.Task, row.BaseBranch, row.HeadBranch, row.Trigger, row.IssueNumber, row.PrNumber, row.Context, row.Debug, row.CreatedByUserID, row.ArtifactDir, row.Status, row.ActivePhase, row.FailedPhase, row.CancelRequested, row.MaxPhases, row.MaxChildRunsPerPhase, row.TotalChildRuns, row.TokenBudgetTotal, row.TokenBudgetUsed, row.DeadlineAt, row.CreatedAt, row.UpdatedAt, row.CompletedAt)
}

func fromDBRunPipelineByTaskRow(row sqlitegen.RunPipeline) RunPipeline {
	return fromDBRunPipelineParts(row.ID, row.TaskID, row.Repo, row.Task, row.BaseBranch, row.HeadBranch, row.Trigger, row.IssueNumber, row.PrNumber, row.Context, row.Debug, row.CreatedByUserID, row.ArtifactDir, row.Status, row.ActivePhase, row.FailedPhase, row.CancelRequested, row.MaxPhases, row.MaxChildRunsPerPhase, row.TotalChildRuns, row.TokenBudgetTotal, row.TokenBudgetUsed, row.DeadlineAt, row.CreatedAt, row.UpdatedAt, row.CompletedAt)
}

func fromDBListIncompleteRunPipelinesRow(row sqlitegen.RunPipeline) RunPipeline {
	return fromDBRunPipelineParts(row.ID, row.TaskID, row.Repo, row.Task, row.BaseBranch, row.HeadBranch, row.Trigger, row.IssueNumber, row.PrNumber, row.Context, row.Debug, row.CreatedByUserID, row.ArtifactDir, row.Status, row.ActivePhase, row.FailedPhase, row.CancelRequested, row.MaxPhases, row.MaxChildRunsPerPhase, row.TotalChildRuns, row.TokenBudgetTotal, row.TokenBudgetUsed, row.DeadlineAt, row.CreatedAt, row.UpdatedAt, row.CompletedAt)
}

func fromDBRunPipelineParts(id, taskID, repo, task, baseBranch, headBranch, trigger string, issueNumber, prNumber int64, contextValue string, debug bool, createdByUserID, artifactDir, status, activePhase, failedPhase string, cancelRequested bool, maxPhases, maxChildRunsPerPhase, totalChildRuns int64, tokenBudgetTotal, tokenBudgetUsed int64, deadlineAt sql.NullInt64, createdAt, updatedAt int64, completedAt sql.NullInt64) RunPipeline {
	out := RunPipeline{
		ID:                   id,
		TaskID:               taskID,
		Repo:                 repo,
		Task:                 task,
		BaseBranch:           baseBranch,
		HeadBranch:           headBranch,
		Trigger:              trigger,
		IssueNumber:          int(issueNumber),
		PRNumber:             int(prNumber),
		Context:              contextValue,
		Debug:                debug,
		CreatedByUserID:      createdByUserID,
		ArtifactDir:          artifactDir,
		Status:               RunPipelineStatus(status),
		ActivePhase:          PipelinePhaseName(activePhase),
		FailedPhase:          PipelinePhaseName(failedPhase),
		CancelRequested:      cancelRequested,
		MaxPhases:            int(maxPhases),
		MaxChildRunsPerPhase: int(maxChildRunsPerPhase),
		TotalChildRuns:       int(totalChildRuns),
		TokenBudgetTotal:     tokenBudgetTotal,
		TokenBudgetUsed:      tokenBudgetUsed,
		CreatedAt:            time.Unix(0, createdAt).UTC(),
		UpdatedAt:            time.Unix(0, updatedAt).UTC(),
	}
	if deadlineAt.Valid {
		t := time.Unix(0, deadlineAt.Int64).UTC()
		out.DeadlineAt = &t
	}
	if completedAt.Valid {
		t := time.Unix(0, completedAt.Int64).UTC()
		out.CompletedAt = &t
	}
	return out
}

func fromDBRunPipelinePhaseRow(row sqlitegen.RunPipelinePhase) RunPipelinePhase {
	return fromDBRunPipelinePhaseParts(row.PipelineID, row.PhaseName, row.PhaseOrder, row.Enabled, row.State, row.RunID, row.ChildIndex, row.ArtifactPaths, row.Error, row.CreatedAt, row.UpdatedAt, row.StartedAt, row.CompletedAt)
}

func fromDBRunPipelinePhaseParts(pipelineID, phaseName string, phaseOrder int64, enabled bool, stateValue, runID string, childIndex int64, artifactPathsJSON, errText string, createdAt, updatedAt int64, startedAt, completedAt sql.NullInt64) RunPipelinePhase {
	out := RunPipelinePhase{
		PipelineID:    pipelineID,
		PhaseName:     PipelinePhaseName(phaseName),
		PhaseOrder:    int(phaseOrder),
		Enabled:       enabled,
		State:         RunPipelinePhaseState(stateValue),
		RunID:         runID,
		ChildIndex:    int(childIndex),
		ArtifactPaths: decodeArtifactPaths(artifactPathsJSON),
		Error:         errText,
		CreatedAt:     time.Unix(0, createdAt).UTC(),
		UpdatedAt:     time.Unix(0, updatedAt).UTC(),
	}
	if startedAt.Valid {
		t := time.Unix(0, startedAt.Int64).UTC()
		out.StartedAt = &t
	}
	if completedAt.Valid {
		t := time.Unix(0, completedAt.Int64).UTC()
		out.CompletedAt = &t
	}
	return out
}

func fromDBRunLineageRow(row sqlitegen.RunLineage) RunLineage {
	return RunLineage{
		RunID:            row.RunID,
		ParentPipelineID: row.ParentPipelineID,
		PhaseName:        PipelinePhaseName(row.PhaseName),
		PhaseOrder:       int(row.PhaseOrder),
		ChildIndex:       int(row.ChildIndex),
		CreatedAt:        time.Unix(0, row.CreatedAt).UTC(),
	}
}

func toDBUpdateRunPipelineParams(p RunPipeline) sqlitegen.UpdateRunPipelineParams {
	return sqlitegen.UpdateRunPipelineParams{
		TaskID:               p.TaskID,
		Repo:                 p.Repo,
		Task:                 p.Task,
		BaseBranch:           p.BaseBranch,
		HeadBranch:           p.HeadBranch,
		Trigger:              p.Trigger,
		IssueNumber:          int64(p.IssueNumber),
		PrNumber:             int64(p.PRNumber),
		Context:              p.Context,
		Debug:                p.Debug,
		CreatedByUserID:      p.CreatedByUserID,
		ArtifactDir:          p.ArtifactDir,
		Status:               string(p.Status),
		ActivePhase:          string(p.ActivePhase),
		FailedPhase:          string(p.FailedPhase),
		CancelRequested:      p.CancelRequested,
		MaxPhases:            int64(p.MaxPhases),
		MaxChildRunsPerPhase: int64(p.MaxChildRunsPerPhase),
		TotalChildRuns:       int64(p.TotalChildRuns),
		TokenBudgetTotal:     p.TokenBudgetTotal,
		TokenBudgetUsed:      p.TokenBudgetUsed,
		DeadlineAt:           toNullInt64(p.DeadlineAt),
		CreatedAt:            fallbackUnixNano(p.CreatedAt, time.Now().UTC()),
		UpdatedAt:            fallbackUnixNano(p.UpdatedAt, p.CreatedAt),
		CompletedAt:          toNullInt64(p.CompletedAt),
		ID:                   p.ID,
	}
}

func toDBUpdateRunPipelinePhaseParams(p RunPipelinePhase) sqlitegen.UpdateRunPipelinePhaseParams {
	return sqlitegen.UpdateRunPipelinePhaseParams{
		PhaseOrder:    int64(p.PhaseOrder),
		Enabled:       p.Enabled,
		State:         string(p.State),
		RunID:         p.RunID,
		ChildIndex:    int64(p.ChildIndex),
		ArtifactPaths: encodeArtifactPaths(p.ArtifactPaths),
		Error:         p.Error,
		CreatedAt:     fallbackUnixNano(p.CreatedAt, time.Now().UTC()),
		UpdatedAt:     fallbackUnixNano(p.UpdatedAt, p.CreatedAt),
		StartedAt:     toNullInt64(p.StartedAt),
		CompletedAt:   toNullInt64(p.CompletedAt),
		PipelineID:    p.PipelineID,
		PhaseName:     string(p.PhaseName),
	}
}

func encodeArtifactPaths(paths []string) string {
	if len(paths) == 0 {
		return "[]"
	}
	data, err := json.Marshal(paths)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func decodeArtifactPaths(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
