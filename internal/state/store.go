package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state/sqlitegen"
	_ "modernc.org/sqlite"
)

const (
	sqliteDriverName = "sqlite"
	maxDeliveries    = 1000
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	mu      sync.RWMutex
	path    string
	maxRuns int
	db      *sql.DB
	q       *sqlitegen.Queries
}

func New(path string, maxRuns int) (*Store, error) {
	return newStore(path, maxRuns, true)
}

func NewWithoutMigrate(path string, maxRuns int) (*Store, error) {
	return newStore(path, maxRuns, false)
}

func newStore(path string, maxRuns int, runMigrations bool) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("state path is required")
	}
	if maxRuns <= 0 {
		maxRuns = 200
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	db, err := sql.Open(sqliteDriverName, path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	closeDBWithError := func(step string, cause error) (*Store, error) {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("%s: %w (close sqlite: %v)", step, cause, closeErr)
		}
		return nil, fmt.Errorf("%s: %w", step, cause)
	}
	// Use a single shared SQLite connection so pragmas apply consistently and
	// writes don't contend across pooled connections in tests/CI.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return closeDBWithError("enable sqlite WAL mode", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		return closeDBWithError("set sqlite busy_timeout", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON;"); err != nil {
		return closeDBWithError("enable sqlite foreign_keys", err)
	}

	if runMigrations {
		if err := goose.SetDialect("sqlite3"); err != nil {
			return closeDBWithError("configure goose sqlite dialect", err)
		}
		goose.SetBaseFS(migrationsFS)
		if err := goose.Up(db, "migrations"); err != nil {
			return closeDBWithError("run migrations", err)
		}
	}

	s := &Store{
		path:    path,
		maxRuns: maxRuns,
		db:      db,
		q:       sqlitegen.New(db),
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}
	return nil
}

func NewRunID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("create run id: %w", err)
	}
	return "run_" + hex.EncodeToString(buf), nil
}

func (s *Store) UpsertTask(in UpsertTaskInput) (Task, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.Repo = NormalizeRepo(in.Repo)
	in.AgentBackend = agent.NormalizeBackend(string(in.AgentBackend))
	if in.ID == "" || in.Repo == "" {
		return Task{}, fmt.Errorf("task id and repo are required")
	}
	now := time.Now().UTC().UnixNano()
	if err := s.q.UpsertTask(context.Background(), sqlitegen.UpsertTaskParams{
		ID:           in.ID,
		Repo:         in.Repo,
		AgentBackend: in.AgentBackend.String(),
		IssueNumber:  int64(in.IssueNumber),
		PrNumber:     int64(in.PRNumber),
		Status:       string(TaskOpen),
		LastRunID:    "",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		return Task{}, fmt.Errorf("upsert task %q: %w", in.ID, err)
	}
	row, err := s.q.GetTask(context.Background(), in.ID)
	if err != nil {
		return Task{}, fmt.Errorf("load task %q after upsert: %w", in.ID, err)
	}
	return fromDBGetTaskRow(row), nil
}

func (s *Store) GetTask(taskID string) (Task, bool) {
	row, err := s.q.GetTask(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, false
		}
		return Task{}, false
	}
	return fromDBGetTaskRow(row), true
}

func (s *Store) FindTaskByPR(repo string, prNumber int) (Task, bool) {
	row, err := s.q.FindTaskByPR(context.Background(), sqlitegen.FindTaskByPRParams{
		Repo:     NormalizeRepo(repo),
		PrNumber: int64(prNumber),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, false
		}
		return Task{}, false
	}
	return fromDBFindTaskByPRRow(row), true
}

func (s *Store) SetTaskPR(taskID, _ string, prNumber int) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task id is required")
	}
	if prNumber <= 0 {
		return nil
	}
	rows, err := s.q.SetTaskPR(context.Background(), sqlitegen.SetTaskPRParams{
		PrNumber:  int64(prNumber),
		UpdatedAt: time.Now().UTC().UnixNano(),
		ID:        taskID,
	})
	if err != nil {
		return fmt.Errorf("set PR number for task %q: %w", taskID, err)
	}
	if rows == 0 {
		return fmt.Errorf("task %q not found", taskID)
	}
	return nil
}

func (s *Store) MarkTaskCompleted(taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task id is required")
	}
	rows, err := s.q.MarkTaskCompleted(context.Background(), sqlitegen.MarkTaskCompletedParams{
		UpdatedAt: time.Now().UTC().UnixNano(),
		ID:        taskID,
	})
	if err != nil {
		return fmt.Errorf("mark task %q completed: %w", taskID, err)
	}
	if rows == 0 {
		return fmt.Errorf("task %q not found", taskID)
	}
	return nil
}

func (s *Store) MarkTaskOpen(taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task id is required")
	}
	rows, err := s.q.MarkTaskOpen(context.Background(), sqlitegen.MarkTaskOpenParams{
		UpdatedAt: time.Now().UTC().UnixNano(),
		ID:        taskID,
	})
	if err != nil {
		return fmt.Errorf("mark task %q open: %w", taskID, err)
	}
	if rows == 0 {
		return fmt.Errorf("task %q not found", taskID)
	}
	return nil
}

func (s *Store) UpsertTaskAgentSession(in UpsertTaskAgentSessionInput) (TaskAgentSession, error) {
	in.TaskID = strings.TrimSpace(in.TaskID)
	in.AgentBackend = agent.NormalizeBackend(string(in.AgentBackend))
	in.BackendSessionID = strings.TrimSpace(in.BackendSessionID)
	in.SessionKey = strings.TrimSpace(in.SessionKey)
	in.SessionRoot = strings.TrimSpace(in.SessionRoot)
	in.LastRunID = strings.TrimSpace(in.LastRunID)
	if in.TaskID == "" {
		return TaskAgentSession{}, fmt.Errorf("task id is required")
	}

	now := time.Now().UTC().UnixNano()
	if err := s.q.UpsertTaskAgentSession(context.Background(), sqlitegen.UpsertTaskAgentSessionParams{
		TaskID:           in.TaskID,
		AgentBackend:     in.AgentBackend.String(),
		BackendSessionID: in.BackendSessionID,
		SessionKey:       in.SessionKey,
		SessionRoot:      in.SessionRoot,
		LastRunID:        in.LastRunID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		return TaskAgentSession{}, fmt.Errorf("upsert task agent session for task %q: %w", in.TaskID, err)
	}
	row, err := s.q.GetTaskAgentSession(context.Background(), in.TaskID)
	if err != nil {
		return TaskAgentSession{}, fmt.Errorf("load task agent session for task %q: %w", in.TaskID, err)
	}
	return fromDBTaskAgentSession(row), nil
}

func (s *Store) GetTaskAgentSession(taskID string) (TaskAgentSession, bool) {
	row, err := s.q.GetTaskAgentSession(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TaskAgentSession{}, false
		}
		return TaskAgentSession{}, false
	}
	return fromDBTaskAgentSession(row), true
}

func (s *Store) DeleteTaskAgentSession(taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	_, err := s.q.DeleteTaskAgentSession(context.Background(), taskID)
	if err != nil {
		return fmt.Errorf("delete task agent session for task %q: %w", taskID, err)
	}
	return nil
}

func (s *Store) IsTaskCompleted(taskID string) bool {
	ok, err := s.q.IsTaskCompleted(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		return false
	}
	return ok
}

func (s *Store) AddRun(in CreateRunInput) (Run, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.TaskID = strings.TrimSpace(in.TaskID)
	in.Repo = NormalizeRepo(in.Repo)
	in.AgentBackend = agent.NormalizeBackend(string(in.AgentBackend))
	if in.ID == "" || in.TaskID == "" || in.Repo == "" {
		return Run{}, fmt.Errorf("id, task_id and repo are required")
	}

	now := time.Now().UTC()
	baseBranch := strings.TrimSpace(in.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	trigger, err := runtrigger.ParseOrDefault(in.Trigger.String(), runtrigger.NameCLI)
	if err != nil {
		return Run{}, fmt.Errorf("invalid trigger: %w", err)
	}
	debugEnabled := true
	if in.Debug != nil {
		debugEnabled = *in.Debug
	}
	prStatus := normalizePRStatus(in.PRStatus)
	if prStatus == PRStatusNone && in.PRNumber > 0 {
		prStatus = PRStatusOpen
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin create run transaction for task %q: %w", in.TaskID, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback create run transaction: %v", rollbackErr)
		}
	}()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.GetTask(context.Background(), in.TaskID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("load task %q before creating run: %w", in.TaskID, err)
	}

	if err := qtx.UpsertTask(context.Background(), sqlitegen.UpsertTaskParams{
		ID:           in.TaskID,
		Repo:         in.Repo,
		AgentBackend: in.AgentBackend.String(),
		IssueNumber:  int64(in.IssueNumber),
		PrNumber:     int64(in.PRNumber),
		Status:       string(TaskOpen),
		LastRunID:    "",
		CreatedAt:    now.UnixNano(),
		UpdatedAt:    now.UnixNano(),
	}); err != nil {
		return Run{}, fmt.Errorf("upsert task %q while creating run %q: %w", in.TaskID, in.ID, err)
	}

	row, err := qtx.InsertRun(context.Background(), sqlitegen.InsertRunParams{
		ID:           in.ID,
		TaskID:       in.TaskID,
		Repo:         in.Repo,
		Task:         in.Task,
		AgentBackend: in.AgentBackend.String(),
		BaseBranch:   baseBranch,
		HeadBranch:   in.HeadBranch,
		Trigger:      trigger.String(),
		Debug:        debugEnabled,
		Status:       string(StatusQueued),
		RunDir:       in.RunDir,
		IssueNumber:  int64(in.IssueNumber),
		PrNumber:     int64(in.PRNumber),
		PrUrl:        "",
		PrStatus:     string(prStatus),
		HeadSha:      "",
		Context:      in.Context,
		Error:        "",
		CreatedAt:    now.UnixNano(),
		UpdatedAt:    now.UnixNano(),
		StartedAt:    sql.NullInt64{},
		CompletedAt:  sql.NullInt64{},
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: runs.id") {
			return Run{}, fmt.Errorf("run %q already exists", in.ID)
		}
		return Run{}, fmt.Errorf("insert run %q: %w", in.ID, err)
	}

	if _, err := qtx.SetTaskLastRun(context.Background(), sqlitegen.SetTaskLastRunParams{
		LastRunID:   row.ID,
		UpdatedAt:   now.UnixNano(),
		IssueNumber: optionalPositiveInt64(in.IssueNumber),
		PrNumber:    optionalPositiveInt64(in.PRNumber),
		ID:          in.TaskID,
	}); err != nil {
		return Run{}, fmt.Errorf("set last run for task %q to %q: %w", in.TaskID, row.ID, err)
	}
	if err := qtx.TrimOldRuns(context.Background(), int64(s.maxRuns)); err != nil {
		return Run{}, fmt.Errorf("trim old runs after creating run %q: %w", in.ID, err)
	}

	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit create run transaction for run %q: %w", in.ID, err)
	}
	return fromDBInsertRunRow(row), nil
}

func (s *Store) GetRun(id string) (Run, bool) {
	row, err := s.q.GetRun(context.Background(), strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false
		}
		return Run{}, false
	}
	return fromDBGetRunRow(row), true
}

func (s *Store) ListRuns(limit int) []Run {
	if limit <= 0 {
		limit = s.maxRuns
	}
	rows, err := s.q.ListRuns(context.Background(), int64(limit))
	if err != nil {
		return nil
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDBListRunsRow(r))
	}
	return out
}

func (s *Store) ListRunningRuns() []Run {
	rows, err := s.q.ListRunningRuns(context.Background())
	if err != nil {
		return nil
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDBListRunningRunsRow(r))
	}
	return out
}

func (s *Store) UpdateRun(id string, fn func(*Run) error) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, fmt.Errorf("begin update run transaction for run %q: %w", id, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf("rollback update run transaction: %v", rollbackErr)
		}
	}()
	qtx := s.q.WithTx(tx)

	row, err := qtx.GetRun(context.Background(), strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, fmt.Errorf("run %q not found", id)
		}
		return Run{}, fmt.Errorf("load run %q for update: %w", id, err)
	}
	r := fromDBGetRunRow(row)
	prevStatus := r.Status
	if err := fn(&r); err != nil {
		return Run{}, fmt.Errorf("apply run update for %q: %w", id, err)
	}
	if err := ValidateRunStatusTransition(prevStatus, r.Status); err != nil {
		return Run{}, fmt.Errorf("validate run status transition for %q: %w", id, err)
	}
	r.UpdatedAt = time.Now().UTC()

	rows, err := qtx.UpdateRun(context.Background(), toDBUpdateRunParams(r))
	if err != nil {
		return Run{}, fmt.Errorf("update run %q: %w", id, err)
	}
	if rows == 0 {
		return Run{}, fmt.Errorf("run %q not found", id)
	}

	if _, err := qtx.SetTaskLastRun(context.Background(), sqlitegen.SetTaskLastRunParams{
		LastRunID:   r.ID,
		UpdatedAt:   r.UpdatedAt.UnixNano(),
		IssueNumber: optionalPositiveInt64(r.IssueNumber),
		PrNumber:    optionalPositiveInt64(r.PRNumber),
		ID:          r.TaskID,
	}); err != nil {
		return Run{}, fmt.Errorf("set last run for task %q to %q: %w", r.TaskID, r.ID, err)
	}
	if r.PRNumber > 0 {
		_, err = qtx.SetTaskPR(context.Background(), sqlitegen.SetTaskPRParams{
			PrNumber:  int64(r.PRNumber),
			UpdatedAt: r.UpdatedAt.UnixNano(),
			ID:        r.TaskID,
		})
		if err != nil {
			return Run{}, fmt.Errorf("set PR number for task %q from run %q: %w", r.TaskID, r.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("commit update run transaction for run %q: %w", id, err)
	}
	return r, nil
}

func (s *Store) SetRunStatus(runID string, status RunStatus, errText string) (Run, error) {
	return s.UpdateRun(runID, func(r *Run) error {
		now := time.Now().UTC()
		r.Status = status
		if status == StatusRunning {
			r.StartedAt = &now
		}
		if IsFinalRunStatus(status) {
			r.CompletedAt = &now
		}
		r.Error = errText
		return nil
	})
}

func (s *Store) ClaimRunStart(runID string) (Run, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Run{}, false, fmt.Errorf("run id is required")
	}
	now := time.Now().UTC().UnixNano()
	rows, err := s.q.ClaimRunStart(context.Background(), sqlitegen.ClaimRunStartParams{
		UpdatedAt: now,
		StartedAt: sql.NullInt64{Int64: now, Valid: true},
		ID:        runID,
	})
	if err != nil {
		return Run{}, false, fmt.Errorf("claim run start for run %q: %w", runID, err)
	}
	row, err := s.q.GetRun(context.Background(), runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, fmt.Errorf("run %q not found", runID)
		}
		return Run{}, false, fmt.Errorf("load run %q after claim start: %w", runID, err)
	}
	return fromDBGetRunRow(row), rows > 0, nil
}

func (s *Store) ClaimNextQueuedRun(preferredTaskID string) (Run, bool, error) {
	now := time.Now().UTC().UnixNano()
	preferredTaskID = strings.TrimSpace(preferredTaskID)
	if preferredTaskID != "" {
		row, err := s.q.ClaimNextQueuedRunForTask(context.Background(), sqlitegen.ClaimNextQueuedRunForTaskParams{
			UpdatedAt: now,
			StartedAt: sql.NullInt64{Int64: now, Valid: true},
			TaskID:    preferredTaskID,
		})
		if err == nil {
			return fromDBClaimNextQueuedRunForTaskRow(row), true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, fmt.Errorf("claim next queued run for task %q: %w", preferredTaskID, err)
		}
	}

	row, err := s.q.ClaimNextQueuedRun(context.Background(), sqlitegen.ClaimNextQueuedRunParams{
		UpdatedAt: now,
		StartedAt: sql.NullInt64{Int64: now, Valid: true},
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, nil
		}
		return Run{}, false, fmt.Errorf("claim next queued run: %w", err)
	}
	return fromDBClaimNextQueuedRunRow(row), true, nil
}

func (s *Store) UpsertRunLease(runID, ownerID string, ttl time.Duration) error {
	runID = strings.TrimSpace(runID)
	ownerID = strings.TrimSpace(ownerID)
	if runID == "" {
		return fmt.Errorf("run id is required")
	}
	if ownerID == "" {
		return fmt.Errorf("owner id is required")
	}
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	now := time.Now().UTC()
	if err := s.q.UpsertRunLease(context.Background(), sqlitegen.UpsertRunLeaseParams{
		RunID:          runID,
		OwnerID:        ownerID,
		HeartbeatAt:    now.UnixNano(),
		LeaseExpiresAt: now.Add(ttl).UnixNano(),
	}); err != nil {
		return fmt.Errorf("upsert run lease for run %q: %w", runID, err)
	}
	return nil
}

func (s *Store) RenewRunLease(runID, ownerID string, ttl time.Duration) (bool, error) {
	runID = strings.TrimSpace(runID)
	ownerID = strings.TrimSpace(ownerID)
	if runID == "" {
		return false, fmt.Errorf("run id is required")
	}
	if ownerID == "" {
		return false, fmt.Errorf("owner id is required")
	}
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	now := time.Now().UTC()
	rows, err := s.q.RenewRunLease(context.Background(), sqlitegen.RenewRunLeaseParams{
		HeartbeatAt:    now.UnixNano(),
		LeaseExpiresAt: now.Add(ttl).UnixNano(),
		RunID:          runID,
		OwnerID:        ownerID,
	})
	if err != nil {
		return false, fmt.Errorf("renew run lease for run %q owner %q: %w", runID, ownerID, err)
	}
	return rows > 0, nil
}

func (s *Store) DeleteRunLease(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	_, err := s.q.DeleteRunLease(context.Background(), runID)
	if err != nil {
		return fmt.Errorf("delete run lease for run %q: %w", runID, err)
	}
	return nil
}

func (s *Store) DeleteRunLeaseForOwner(runID, ownerID string) error {
	runID = strings.TrimSpace(runID)
	ownerID = strings.TrimSpace(ownerID)
	if runID == "" || ownerID == "" {
		return nil
	}
	_, err := s.q.DeleteRunLeaseForOwner(context.Background(), sqlitegen.DeleteRunLeaseForOwnerParams{
		RunID:   runID,
		OwnerID: ownerID,
	})
	if err != nil {
		return fmt.Errorf("delete run lease for run %q owner %q: %w", runID, ownerID, err)
	}
	return nil
}

func (s *Store) GetRunLease(runID string) (RunLease, bool) {
	row, err := s.q.GetRunLease(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunLease{}, false
		}
		return RunLease{}, false
	}
	return RunLease{
		RunID:          row.RunID,
		OwnerID:        row.OwnerID,
		HeartbeatAt:    time.Unix(0, row.HeartbeatAt).UTC(),
		LeaseExpiresAt: time.Unix(0, row.LeaseExpiresAt).UTC(),
	}, true
}

func (s *Store) CountRunLeasesByOwner(ownerID string) int {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return 0
	}
	count, err := s.q.CountRunLeasesByOwner(context.Background(), ownerID)
	if err != nil {
		return 0
	}
	return int(count)
}

func (s *Store) UpsertRunExecution(exec RunExecution) (RunExecution, error) {
	exec.RunID = strings.TrimSpace(exec.RunID)
	exec.Backend = strings.TrimSpace(exec.Backend)
	exec.ContainerName = strings.TrimSpace(exec.ContainerName)
	exec.ContainerID = strings.TrimSpace(exec.ContainerID)
	exec.Status = strings.TrimSpace(exec.Status)
	if exec.RunID == "" {
		return RunExecution{}, fmt.Errorf("run id is required")
	}
	if exec.Backend == "" {
		exec.Backend = "docker"
	}
	if exec.ContainerName == "" {
		return RunExecution{}, fmt.Errorf("container name is required")
	}
	if exec.ContainerID == "" {
		return RunExecution{}, fmt.Errorf("container id is required")
	}
	if exec.Status == "" {
		exec.Status = "created"
	}
	now := time.Now().UTC()
	if err := s.q.UpsertRunExecution(context.Background(), sqlitegen.UpsertRunExecutionParams{
		RunID:          exec.RunID,
		Backend:        exec.Backend,
		ContainerName:  exec.ContainerName,
		ContainerID:    exec.ContainerID,
		Status:         exec.Status,
		ExitCode:       int64(exec.ExitCode),
		CreatedAt:      now.UnixNano(),
		UpdatedAt:      now.UnixNano(),
		LastObservedAt: now.UnixNano(),
	}); err != nil {
		return RunExecution{}, fmt.Errorf("upsert run execution for run %q: %w", exec.RunID, err)
	}
	row, err := s.q.GetRunExecution(context.Background(), exec.RunID)
	if err != nil {
		return RunExecution{}, fmt.Errorf("load run execution for run %q: %w", exec.RunID, err)
	}
	return fromDBRunExecution(row), nil
}

func (s *Store) UpdateRunExecutionState(runID, status string, exitCode int, lastObservedAt time.Time) (RunExecution, error) {
	runID = strings.TrimSpace(runID)
	status = strings.TrimSpace(status)
	if runID == "" {
		return RunExecution{}, fmt.Errorf("run id is required")
	}
	if status == "" {
		status = "created"
	}
	if lastObservedAt.IsZero() {
		lastObservedAt = time.Now().UTC()
	}
	rows, err := s.q.UpdateRunExecutionState(context.Background(), sqlitegen.UpdateRunExecutionStateParams{
		Status:         status,
		ExitCode:       int64(exitCode),
		UpdatedAt:      time.Now().UTC().UnixNano(),
		LastObservedAt: lastObservedAt.UTC().UnixNano(),
		RunID:          runID,
	})
	if err != nil {
		return RunExecution{}, fmt.Errorf("update run execution state for run %q: %w", runID, err)
	}
	if rows == 0 {
		return RunExecution{}, fmt.Errorf("run execution %q not found", runID)
	}
	row, err := s.q.GetRunExecution(context.Background(), runID)
	if err != nil {
		return RunExecution{}, fmt.Errorf("load run execution for run %q after update: %w", runID, err)
	}
	return fromDBRunExecution(row), nil
}

func (s *Store) GetRunExecution(runID string) (RunExecution, bool) {
	row, err := s.q.GetRunExecution(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunExecution{}, false
		}
		return RunExecution{}, false
	}
	return fromDBRunExecution(row), true
}

func (s *Store) DeleteRunExecution(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	_, err := s.q.DeleteRunExecution(context.Background(), runID)
	if err != nil {
		return fmt.Errorf("delete run execution for run %q: %w", runID, err)
	}
	return nil
}

func (s *Store) UpsertRunTokenUsage(usage RunTokenUsage) (RunTokenUsage, error) {
	usage.RunID = strings.TrimSpace(usage.RunID)
	usage.Backend = strings.TrimSpace(usage.Backend)
	usage.Provider = strings.TrimSpace(usage.Provider)
	usage.Model = strings.TrimSpace(usage.Model)
	usage.RawUsageJSON = strings.TrimSpace(usage.RawUsageJSON)
	if usage.RunID == "" {
		return RunTokenUsage{}, fmt.Errorf("run id is required")
	}
	now := time.Now().UTC()
	if usage.CapturedAt.IsZero() {
		usage.CapturedAt = now
	}
	row, err := s.q.UpsertRunTokenUsage(context.Background(), sqlitegen.UpsertRunTokenUsageParams{
		RunID:                 usage.RunID,
		Backend:               usage.Backend,
		Provider:              usage.Provider,
		Model:                 usage.Model,
		TotalTokens:           usage.TotalTokens,
		InputTokens:           toNullInt64Value(usage.InputTokens),
		OutputTokens:          toNullInt64Value(usage.OutputTokens),
		CachedInputTokens:     toNullInt64Value(usage.CachedInputTokens),
		ReasoningOutputTokens: toNullInt64Value(usage.ReasoningOutputTokens),
		RawUsageJson:          usage.RawUsageJSON,
		CapturedAt:            usage.CapturedAt.UTC().UnixNano(),
		CreatedAt:             now.UnixNano(),
		UpdatedAt:             now.UnixNano(),
	})
	if err != nil {
		return RunTokenUsage{}, fmt.Errorf("upsert run token usage for run %q: %w", usage.RunID, err)
	}
	return fromDBRunTokenUsage(row), nil
}

func (s *Store) GetRunTokenUsage(runID string) (RunTokenUsage, bool) {
	row, err := s.q.GetRunTokenUsage(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunTokenUsage{}, false
		}
		return RunTokenUsage{}, false
	}
	return fromDBRunTokenUsage(row), true
}

func (s *Store) RequestRunCancel(runID, reason, source string) error {
	runID = strings.TrimSpace(runID)
	reason = strings.TrimSpace(reason)
	source = strings.TrimSpace(source)
	if runID == "" {
		return fmt.Errorf("run id is required")
	}
	if reason == "" {
		reason = "canceled"
	}
	if source == "" {
		source = "system"
	}
	if err := s.q.UpsertRunCancel(context.Background(), sqlitegen.UpsertRunCancelParams{
		RunID:       runID,
		Reason:      reason,
		Source:      source,
		RequestedAt: time.Now().UTC().UnixNano(),
	}); err != nil {
		return fmt.Errorf("request cancel for run %q: %w", runID, err)
	}
	return nil
}

func (s *Store) GetRunCancel(runID string) (RunCancelRequest, bool) {
	row, err := s.q.GetRunCancel(context.Background(), strings.TrimSpace(runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunCancelRequest{}, false
		}
		return RunCancelRequest{}, false
	}
	return RunCancelRequest{
		RunID:       row.RunID,
		Reason:      row.Reason,
		Source:      row.Source,
		RequestedAt: time.Unix(0, row.RequestedAt).UTC(),
	}, true
}

func (s *Store) ClearRunCancel(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	_, err := s.q.DeleteRunCancel(context.Background(), runID)
	if err != nil {
		return fmt.Errorf("clear cancel request for run %q: %w", runID, err)
	}
	return nil
}

func (s *Store) PauseScheduler(scope, reason string, until time.Time) (time.Time, error) {
	scope = strings.TrimSpace(scope)
	reason = strings.TrimSpace(reason)
	if scope == "" {
		return time.Time{}, fmt.Errorf("pause scope is required")
	}
	if until.IsZero() {
		return time.Time{}, fmt.Errorf("pause deadline is required")
	}
	until = until.UTC()
	now := time.Now().UTC()
	row, err := s.q.UpsertSchedulerPause(context.Background(), sqlitegen.UpsertSchedulerPauseParams{
		Scope:       scope,
		Reason:      reason,
		PausedUntil: until.UnixNano(),
		CreatedAt:   now.UnixNano(),
		UpdatedAt:   now.UnixNano(),
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("pause scheduler for scope %q: %w", scope, err)
	}
	return time.Unix(0, row.PausedUntil).UTC(), nil
}

func (s *Store) ActiveSchedulerPause(scope string, now time.Time) (time.Time, string, bool, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return time.Time{}, "", false, fmt.Errorf("pause scope is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row, err := s.q.GetActiveSchedulerPause(context.Background(), sqlitegen.GetActiveSchedulerPauseParams{
		Scope:       scope,
		PausedUntil: now.UTC().UnixNano(),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, "", false, nil
		}
		return time.Time{}, "", false, fmt.Errorf("get active scheduler pause for scope %q: %w", scope, err)
	}
	return time.Unix(0, row.PausedUntil).UTC(), strings.TrimSpace(row.Reason), true, nil
}

func (s *Store) ActiveRunForTask(taskID string) (Run, bool) {
	row, err := s.q.ActiveRunForTask(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false
		}
		return Run{}, false
	}
	return fromDBActiveRunForTaskRow(row), true
}

func (s *Store) LastRunForTask(taskID string) (Run, bool) {
	row, err := s.q.LastRunForTask(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false
		}
		return Run{}, false
	}
	return fromDBLastRunForTaskRow(row), true
}

func (s *Store) CancelQueuedRuns(taskID, reason string) error {
	now := time.Now().UTC().UnixNano()
	if err := s.q.CancelQueuedRuns(context.Background(), sqlitegen.CancelQueuedRunsParams{
		Error:       reason,
		UpdatedAt:   now,
		CompletedAt: sql.NullInt64{Int64: now, Valid: true},
		TaskID:      strings.TrimSpace(taskID),
	}); err != nil {
		return fmt.Errorf("cancel queued runs for task %q: %w", strings.TrimSpace(taskID), err)
	}
	return nil
}

// DeliverySeen returns true if the delivery was already processed.
func (s *Store) DeliverySeen(deliveryID string) bool {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return false
	}
	exists, err := s.q.DeliverySeen(context.Background(), deliveryID)
	if err != nil {
		return false
	}
	return exists > 0
}

type DeliveryClaim struct {
	ID    string
	Token string
}

func (s *Store) ClaimDelivery(deliveryID, claimedBy string) (DeliveryClaim, bool, error) {
	deliveryID = strings.TrimSpace(deliveryID)
	claimedBy = strings.TrimSpace(claimedBy)
	if deliveryID == "" {
		return DeliveryClaim{}, false, fmt.Errorf("delivery id is required")
	}
	if claimedBy == "" {
		claimedBy = "rascald"
	}
	token, err := newClaimToken()
	if err != nil {
		return DeliveryClaim{}, false, fmt.Errorf("create claim token for delivery %q: %w", deliveryID, err)
	}
	now := time.Now().UTC()
	row, err := s.q.ClaimDelivery(context.Background(), sqlitegen.ClaimDeliveryParams{
		ID:         deliveryID,
		ClaimToken: token,
		ClaimedBy:  claimedBy,
		ClaimedAt:  now.UnixNano(),
		SeenAt:     now.UnixNano(),
	})
	if err != nil {
		return DeliveryClaim{}, false, fmt.Errorf("claim delivery %q: %w", deliveryID, err)
	}
	if err := s.trimDeliveriesIfNeeded(); err != nil {
		return DeliveryClaim{}, false, fmt.Errorf("trim deliveries after claiming %q: %w", deliveryID, err)
	}
	return DeliveryClaim{ID: deliveryID, Token: token}, row.Status == "processing" && row.ClaimToken == token, nil
}

func (s *Store) CompleteDeliveryClaim(claim DeliveryClaim) error {
	claim.ID = strings.TrimSpace(claim.ID)
	claim.Token = strings.TrimSpace(claim.Token)
	if claim.ID == "" || claim.Token == "" {
		return fmt.Errorf("delivery claim is required")
	}
	now := time.Now().UTC().UnixNano()
	rows, err := s.q.CompleteDeliveryClaim(context.Background(), sqlitegen.CompleteDeliveryClaimParams{
		ProcessedAt: sql.NullInt64{Int64: now, Valid: true},
		SeenAt:      now,
		ID:          claim.ID,
		ClaimToken:  claim.Token,
	})
	if err != nil {
		return fmt.Errorf("complete delivery claim %q: %w", claim.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("delivery claim %q is no longer active", claim.ID)
	}
	return nil
}

func (s *Store) ReleaseDeliveryClaim(claim DeliveryClaim) error {
	claim.ID = strings.TrimSpace(claim.ID)
	claim.Token = strings.TrimSpace(claim.Token)
	if claim.ID == "" || claim.Token == "" {
		return nil
	}
	_, err := s.q.ReleaseDeliveryClaim(context.Background(), sqlitegen.ReleaseDeliveryClaimParams{
		ID:         claim.ID,
		ClaimToken: claim.Token,
	})
	if err != nil {
		return fmt.Errorf("release delivery claim %q: %w", claim.ID, err)
	}
	return nil
}

// RecordDelivery stores a processed delivery id.
func (s *Store) RecordDelivery(deliveryID string) error {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return nil
	}
	now := time.Now().UTC().UnixNano()
	if err := s.q.RecordDelivery(context.Background(), sqlitegen.RecordDeliveryParams{
		ID:          deliveryID,
		ProcessedAt: sql.NullInt64{Int64: now, Valid: true},
		SeenAt:      now,
	}); err != nil {
		return fmt.Errorf("record delivery %q: %w", deliveryID, err)
	}
	return s.trimDeliveriesIfNeeded()
}

func (s *Store) trimDeliveriesIfNeeded() error {
	count, err := s.q.CountDeliveries(context.Background())
	if err != nil {
		return fmt.Errorf("count deliveries: %w", err)
	}
	if count <= maxDeliveries {
		return nil
	}
	if err := s.q.DeleteOldestDeliveries(context.Background(), count-maxDeliveries); err != nil {
		return fmt.Errorf("delete %d oldest deliveries: %w", count-maxDeliveries, err)
	}
	return nil
}

func newClaimToken() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("create delivery claim token: %w", err)
	}
	return "claim_" + hex.EncodeToString(buf), nil
}

func fromDBTaskParts(id, repo, agentBackend string, issueNumber, prNumber int64, status string, pendingInput int64, lastRunID string, createdAt, updatedAt int64) Task {
	return Task{
		ID:           id,
		Repo:         repo,
		AgentBackend: agent.NormalizeBackend(agentBackend),
		IssueNumber:  int(issueNumber),
		PRNumber:     int(prNumber),
		Status:       TaskStatus(status),
		PendingInput: pendingInput != 0,
		LastRunID:    lastRunID,
		CreatedAt:    time.Unix(0, createdAt).UTC(),
		UpdatedAt:    time.Unix(0, updatedAt).UTC(),
	}
}

func fromDBGetTaskRow(t sqlitegen.GetTaskRow) Task {
	return fromDBTaskParts(t.ID, t.Repo, t.AgentBackend, t.IssueNumber, t.PrNumber, t.Status, t.PendingInput, t.LastRunID, t.CreatedAt, t.UpdatedAt)
}

func fromDBFindTaskByPRRow(t sqlitegen.FindTaskByPRRow) Task {
	return fromDBTaskParts(t.ID, t.Repo, t.AgentBackend, t.IssueNumber, t.PrNumber, t.Status, t.PendingInput, t.LastRunID, t.CreatedAt, t.UpdatedAt)
}

func fromDBRunParts(id, taskID, repo, task, agentBackend, baseBranch, headBranch, trigger string, debug bool, status, runDir string, issueNumber, prNumber int64, prURL, prStatus, headSHA, contextValue, errText string, createdAt, updatedAt int64, startedAt, completedAt sql.NullInt64) Run {
	out := Run{
		ID:           id,
		TaskID:       taskID,
		Repo:         repo,
		Task:         task,
		AgentBackend: agent.NormalizeBackend(agentBackend),
		BaseBranch:   baseBranch,
		HeadBranch:   headBranch,
		Trigger:      runtrigger.Normalize(trigger),
		Debug:        debug,
		Status:       RunStatus(status),
		RunDir:       runDir,
		IssueNumber:  int(issueNumber),
		PRNumber:     int(prNumber),
		PRURL:        prURL,
		PRStatus:     normalizePRStatus(PRStatus(prStatus)),
		HeadSHA:      headSHA,
		Context:      contextValue,
		Error:        errText,
		CreatedAt:    time.Unix(0, createdAt).UTC(),
		UpdatedAt:    time.Unix(0, updatedAt).UTC(),
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

func fromDBInsertRunRow(r sqlitegen.InsertRunRow) Run {
	return fromDBRunParts(r.ID, r.TaskID, r.Repo, r.Task, r.AgentBackend, r.BaseBranch, r.HeadBranch, r.Trigger, r.Debug, r.Status, r.RunDir, r.IssueNumber, r.PrNumber, r.PrUrl, r.PrStatus, r.HeadSha, r.Context, r.Error, r.CreatedAt, r.UpdatedAt, r.StartedAt, r.CompletedAt)
}

func fromDBGetRunRow(r sqlitegen.GetRunRow) Run {
	return fromDBRunParts(r.ID, r.TaskID, r.Repo, r.Task, r.AgentBackend, r.BaseBranch, r.HeadBranch, r.Trigger, r.Debug, r.Status, r.RunDir, r.IssueNumber, r.PrNumber, r.PrUrl, r.PrStatus, r.HeadSha, r.Context, r.Error, r.CreatedAt, r.UpdatedAt, r.StartedAt, r.CompletedAt)
}

func fromDBListRunsRow(r sqlitegen.ListRunsRow) Run {
	return fromDBRunParts(r.ID, r.TaskID, r.Repo, r.Task, r.AgentBackend, r.BaseBranch, r.HeadBranch, r.Trigger, r.Debug, r.Status, r.RunDir, r.IssueNumber, r.PrNumber, r.PrUrl, r.PrStatus, r.HeadSha, r.Context, r.Error, r.CreatedAt, r.UpdatedAt, r.StartedAt, r.CompletedAt)
}

func fromDBListRunningRunsRow(r sqlitegen.ListRunningRunsRow) Run {
	return fromDBRunParts(r.ID, r.TaskID, r.Repo, r.Task, r.AgentBackend, r.BaseBranch, r.HeadBranch, r.Trigger, r.Debug, r.Status, r.RunDir, r.IssueNumber, r.PrNumber, r.PrUrl, r.PrStatus, r.HeadSha, r.Context, r.Error, r.CreatedAt, r.UpdatedAt, r.StartedAt, r.CompletedAt)
}

func fromDBLastRunForTaskRow(r sqlitegen.LastRunForTaskRow) Run {
	return fromDBRunParts(r.ID, r.TaskID, r.Repo, r.Task, r.AgentBackend, r.BaseBranch, r.HeadBranch, r.Trigger, r.Debug, r.Status, r.RunDir, r.IssueNumber, r.PrNumber, r.PrUrl, r.PrStatus, r.HeadSha, r.Context, r.Error, r.CreatedAt, r.UpdatedAt, r.StartedAt, r.CompletedAt)
}

func fromDBActiveRunForTaskRow(r sqlitegen.ActiveRunForTaskRow) Run {
	return fromDBRunParts(r.ID, r.TaskID, r.Repo, r.Task, r.AgentBackend, r.BaseBranch, r.HeadBranch, r.Trigger, r.Debug, r.Status, r.RunDir, r.IssueNumber, r.PrNumber, r.PrUrl, r.PrStatus, r.HeadSha, r.Context, r.Error, r.CreatedAt, r.UpdatedAt, r.StartedAt, r.CompletedAt)
}

func fromDBClaimNextQueuedRunRow(r sqlitegen.ClaimNextQueuedRunRow) Run {
	return fromDBRunParts(r.ID, r.TaskID, r.Repo, r.Task, r.AgentBackend, r.BaseBranch, r.HeadBranch, r.Trigger, r.Debug, r.Status, r.RunDir, r.IssueNumber, r.PrNumber, r.PrUrl, r.PrStatus, r.HeadSha, r.Context, r.Error, r.CreatedAt, r.UpdatedAt, r.StartedAt, r.CompletedAt)
}

func fromDBClaimNextQueuedRunForTaskRow(r sqlitegen.ClaimNextQueuedRunForTaskRow) Run {
	return fromDBRunParts(r.ID, r.TaskID, r.Repo, r.Task, r.AgentBackend, r.BaseBranch, r.HeadBranch, r.Trigger, r.Debug, r.Status, r.RunDir, r.IssueNumber, r.PrNumber, r.PrUrl, r.PrStatus, r.HeadSha, r.Context, r.Error, r.CreatedAt, r.UpdatedAt, r.StartedAt, r.CompletedAt)
}

func fromDBRunExecution(r sqlitegen.RunExecution) RunExecution {
	return RunExecution{
		RunID:          r.RunID,
		Backend:        r.Backend,
		ContainerName:  r.ContainerName,
		ContainerID:    r.ContainerID,
		Status:         r.Status,
		ExitCode:       int(r.ExitCode),
		CreatedAt:      time.Unix(0, r.CreatedAt).UTC(),
		UpdatedAt:      time.Unix(0, r.UpdatedAt).UTC(),
		LastObservedAt: time.Unix(0, r.LastObservedAt).UTC(),
	}
}

func fromDBTaskAgentSession(s sqlitegen.TaskAgentSession) TaskAgentSession {
	return TaskAgentSession{
		TaskID:           s.TaskID,
		AgentBackend:     agent.NormalizeBackend(s.AgentBackend),
		BackendSessionID: s.BackendSessionID,
		SessionKey:       s.SessionKey,
		SessionRoot:      s.SessionRoot,
		LastRunID:        s.LastRunID,
		CreatedAt:        time.Unix(0, s.CreatedAt).UTC(),
		UpdatedAt:        time.Unix(0, s.UpdatedAt).UTC(),
	}
}

func toDBUpdateRunParams(r Run) sqlitegen.UpdateRunParams {
	prStatus := normalizePRStatus(r.PRStatus)
	return sqlitegen.UpdateRunParams{
		TaskID:       r.TaskID,
		Repo:         r.Repo,
		Task:         r.Task,
		AgentBackend: agent.NormalizeBackend(string(r.AgentBackend)).String(),
		BaseBranch:   r.BaseBranch,
		HeadBranch:   r.HeadBranch,
		Trigger:      r.Trigger.String(),
		Debug:        r.Debug,
		Status:       string(r.Status),
		RunDir:       r.RunDir,
		IssueNumber:  int64(r.IssueNumber),
		PrNumber:     int64(r.PRNumber),
		PrUrl:        r.PRURL,
		PrStatus:     string(prStatus),
		HeadSha:      r.HeadSHA,
		Context:      r.Context,
		Error:        r.Error,
		CreatedAt:    fallbackUnixNano(r.CreatedAt, time.Now().UTC()),
		UpdatedAt:    fallbackUnixNano(r.UpdatedAt, r.CreatedAt),
		StartedAt:    toNullInt64(r.StartedAt),
		CompletedAt:  toNullInt64(r.CompletedAt),
		ID:           r.ID,
	}
}

func optionalPositiveInt64(v int) sql.NullInt64 {
	if v <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(v), Valid: true}
}

func fromDBRunTokenUsage(row sqlitegen.RunTokenUsage) RunTokenUsage {
	return RunTokenUsage{
		RunID:                 row.RunID,
		Backend:               row.Backend,
		Provider:              row.Provider,
		Model:                 row.Model,
		TotalTokens:           row.TotalTokens,
		InputTokens:           fromNullInt64Value(row.InputTokens),
		OutputTokens:          fromNullInt64Value(row.OutputTokens),
		CachedInputTokens:     fromNullInt64Value(row.CachedInputTokens),
		ReasoningOutputTokens: fromNullInt64Value(row.ReasoningOutputTokens),
		RawUsageJSON:          row.RawUsageJson,
		CapturedAt:            time.Unix(0, row.CapturedAt).UTC(),
		CreatedAt:             time.Unix(0, row.CreatedAt).UTC(),
		UpdatedAt:             time.Unix(0, row.UpdatedAt).UTC(),
	}
}

func normalizePRStatus(in PRStatus) PRStatus {
	switch in {
	case PRStatusOpen, PRStatusMerged, PRStatusClosedUnmerged:
		return in
	default:
		return PRStatusNone
	}
}

func toNullInt64(t *time.Time) sql.NullInt64 {
	if t == nil || t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().UnixNano(), Valid: true}
}

func toNullInt64Value(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func fromNullInt64Value(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	out := v.Int64
	return &out
}

func fallbackUnixNano(t time.Time, fallback time.Time) int64 {
	if !t.IsZero() {
		return t.UTC().UnixNano()
	}
	if !fallback.IsZero() {
		return fallback.UTC().UnixNano()
	}
	return time.Now().UTC().UnixNano()
}
