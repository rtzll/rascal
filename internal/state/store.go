package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
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
	// Use a single shared SQLite connection so pragmas apply consistently and
	// writes don't contend across pooled connections in tests/CI.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set sqlite busy_timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite foreign_keys: %w", err)
	}

	if err := goose.SetDialect("sqlite3"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure goose sqlite dialect: %w", err)
	}
	goose.SetBaseFS(migrationsFS)
	if err := goose.Up(db, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	s := &Store{
		path:    path,
		maxRuns: maxRuns,
		db:      db,
		q:       sqlitegen.New(db),
	}
	return s, nil
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
	in.Repo = strings.TrimSpace(in.Repo)
	if in.ID == "" || in.Repo == "" {
		return Task{}, fmt.Errorf("task id and repo are required")
	}
	now := time.Now().UTC().UnixNano()
	row, err := s.q.UpsertTask(context.Background(), sqlitegen.UpsertTaskParams{
		ID:           in.ID,
		Repo:         in.Repo,
		IssueNumber:  int64(in.IssueNumber),
		PrNumber:     int64(in.PRNumber),
		Status:       string(TaskOpen),
		PendingInput: false,
		LastRunID:    "",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		return Task{}, err
	}
	return fromDBTask(row), nil
}

func (s *Store) GetTask(taskID string) (Task, bool) {
	row, err := s.q.GetTask(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, false
		}
		return Task{}, false
	}
	return fromDBTask(row), true
}

func (s *Store) FindTaskByPR(repo string, prNumber int) (Task, bool) {
	row, err := s.q.FindTaskByPR(context.Background(), sqlitegen.FindTaskByPRParams{
		Repo:     strings.TrimSpace(repo),
		PrNumber: int64(prNumber),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, false
		}
		return Task{}, false
	}
	return fromDBTask(row), true
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
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task %q not found", taskID)
	}
	return nil
}

func (s *Store) SetTaskPendingInput(taskID string, pending bool) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task id is required")
	}
	rows, err := s.q.SetTaskPendingInput(context.Background(), sqlitegen.SetTaskPendingInputParams{
		PendingInput: pending,
		UpdatedAt:    time.Now().UTC().UnixNano(),
		ID:           taskID,
	})
	if err != nil {
		return err
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
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task %q not found", taskID)
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
	in.Repo = strings.TrimSpace(in.Repo)
	if in.ID == "" || in.TaskID == "" || in.Repo == "" {
		return Run{}, fmt.Errorf("id, task_id and repo are required")
	}

	now := time.Now().UTC()
	baseBranch := strings.TrimSpace(in.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	trigger := strings.TrimSpace(in.Trigger)
	if trigger == "" {
		trigger = "cli"
	}
	debugEnabled := true
	if in.Debug != nil {
		debugEnabled = *in.Debug
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.UpsertTask(context.Background(), sqlitegen.UpsertTaskParams{
		ID:           in.TaskID,
		Repo:         in.Repo,
		IssueNumber:  int64(in.IssueNumber),
		PrNumber:     int64(in.PRNumber),
		Status:       string(TaskOpen),
		PendingInput: false,
		LastRunID:    "",
		CreatedAt:    now.UnixNano(),
		UpdatedAt:    now.UnixNano(),
	}); err != nil {
		return Run{}, err
	}

	row, err := qtx.InsertRun(context.Background(), sqlitegen.InsertRunParams{
		ID:          in.ID,
		TaskID:      in.TaskID,
		Repo:        in.Repo,
		Task:        in.Task,
		BaseBranch:  baseBranch,
		HeadBranch:  in.HeadBranch,
		Trigger:     trigger,
		Debug:       debugEnabled,
		Status:      string(StatusQueued),
		RunDir:      in.RunDir,
		IssueNumber: int64(in.IssueNumber),
		PrNumber:    int64(in.PRNumber),
		PrUrl:       "",
		HeadSha:     "",
		Context:     in.Context,
		Error:       "",
		CreatedAt:   now.UnixNano(),
		UpdatedAt:   now.UnixNano(),
		StartedAt:   sql.NullInt64{},
		CompletedAt: sql.NullInt64{},
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: runs.id") {
			return Run{}, fmt.Errorf("run %q already exists", in.ID)
		}
		return Run{}, err
	}

	if _, err := qtx.SetTaskLastRun(context.Background(), sqlitegen.SetTaskLastRunParams{
		LastRunID:   row.ID,
		UpdatedAt:   now.UnixNano(),
		IssueNumber: int64(in.IssueNumber),
		PrNumber:    int64(in.PRNumber),
		ID:          in.TaskID,
	}); err != nil {
		return Run{}, err
	}
	if err := qtx.TrimOldRuns(context.Background(), int64(s.maxRuns)); err != nil {
		return Run{}, err
	}

	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return fromDBRun(row), nil
}

func (s *Store) GetRun(id string) (Run, bool) {
	row, err := s.q.GetRun(context.Background(), strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false
		}
		return Run{}, false
	}
	return fromDBRun(row), true
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
		out = append(out, fromDBRun(r))
	}
	return out
}

func (s *Store) UpdateRun(id string, fn func(*Run) error) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	row, err := qtx.GetRun(context.Background(), strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, fmt.Errorf("run %q not found", id)
		}
		return Run{}, err
	}
	r := fromDBRun(row)
	if err := fn(&r); err != nil {
		return Run{}, err
	}
	r.UpdatedAt = time.Now().UTC()

	rows, err := qtx.UpdateRun(context.Background(), toDBUpdateRunParams(r))
	if err != nil {
		return Run{}, err
	}
	if rows == 0 {
		return Run{}, fmt.Errorf("run %q not found", id)
	}

	if _, err := qtx.SetTaskLastRun(context.Background(), sqlitegen.SetTaskLastRunParams{
		LastRunID:   r.ID,
		UpdatedAt:   r.UpdatedAt.UnixNano(),
		IssueNumber: int64(r.IssueNumber),
		PrNumber:    int64(r.PRNumber),
		ID:          r.TaskID,
	}); err != nil {
		return Run{}, err
	}
	if r.PRNumber > 0 {
		_, err = qtx.SetTaskPR(context.Background(), sqlitegen.SetTaskPRParams{
			PrNumber:  int64(r.PRNumber),
			UpdatedAt: r.UpdatedAt.UnixNano(),
			ID:        r.TaskID,
		})
		if err != nil {
			return Run{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Run{}, err
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
		if status == StatusSucceeded || status == StatusFailed || status == StatusCanceled || status == StatusAwaitingFeedback {
			r.CompletedAt = &now
		}
		r.Error = errText
		return nil
	})
}

func (s *Store) ActiveRunForTask(taskID string) (Run, bool) {
	row, err := s.q.ActiveRunForTask(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false
		}
		return Run{}, false
	}
	return fromDBRun(row), true
}

func (s *Store) LastRunForTask(taskID string) (Run, bool) {
	row, err := s.q.LastRunForTask(context.Background(), strings.TrimSpace(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false
		}
		return Run{}, false
	}
	return fromDBRun(row), true
}

func (s *Store) CancelQueuedRuns(taskID, reason string) error {
	now := time.Now().UTC().UnixNano()
	return s.q.CancelQueuedRuns(context.Background(), sqlitegen.CancelQueuedRunsParams{
		Error:       reason,
		UpdatedAt:   now,
		CompletedAt: sql.NullInt64{Int64: now, Valid: true},
		TaskID:      strings.TrimSpace(taskID),
	})
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

// RecordDelivery stores a processed delivery id.
func (s *Store) RecordDelivery(deliveryID string) error {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return nil
	}
	if err := s.q.RecordDelivery(context.Background(), sqlitegen.RecordDeliveryParams{
		ID:     deliveryID,
		SeenAt: time.Now().UTC().UnixNano(),
	}); err != nil {
		return err
	}
	count, err := s.q.CountDeliveries(context.Background())
	if err != nil {
		return err
	}
	if count <= maxDeliveries {
		return nil
	}
	return s.q.DeleteOldestDeliveries(context.Background(), count-maxDeliveries)
}

func fromDBTask(t sqlitegen.Task) Task {
	return Task{
		ID:           t.ID,
		Repo:         t.Repo,
		IssueNumber:  int(t.IssueNumber),
		PRNumber:     int(t.PrNumber),
		Status:       TaskStatus(t.Status),
		PendingInput: t.PendingInput,
		LastRunID:    t.LastRunID,
		CreatedAt:    time.Unix(0, t.CreatedAt).UTC(),
		UpdatedAt:    time.Unix(0, t.UpdatedAt).UTC(),
	}
}

func fromDBRun(r sqlitegen.Run) Run {
	out := Run{
		ID:          r.ID,
		TaskID:      r.TaskID,
		Repo:        r.Repo,
		Task:        r.Task,
		BaseBranch:  r.BaseBranch,
		HeadBranch:  r.HeadBranch,
		Trigger:     r.Trigger,
		Debug:       r.Debug,
		Status:      RunStatus(r.Status),
		RunDir:      r.RunDir,
		IssueNumber: int(r.IssueNumber),
		PRNumber:    int(r.PrNumber),
		PRURL:       r.PrUrl,
		HeadSHA:     r.HeadSha,
		Context:     r.Context,
		Error:       r.Error,
		CreatedAt:   time.Unix(0, r.CreatedAt).UTC(),
		UpdatedAt:   time.Unix(0, r.UpdatedAt).UTC(),
	}
	if r.StartedAt.Valid {
		t := time.Unix(0, r.StartedAt.Int64).UTC()
		out.StartedAt = &t
	}
	if r.CompletedAt.Valid {
		t := time.Unix(0, r.CompletedAt.Int64).UTC()
		out.CompletedAt = &t
	}
	return out
}

func toDBUpdateRunParams(r Run) sqlitegen.UpdateRunParams {
	return sqlitegen.UpdateRunParams{
		TaskID:      r.TaskID,
		Repo:        r.Repo,
		Task:        r.Task,
		BaseBranch:  r.BaseBranch,
		HeadBranch:  r.HeadBranch,
		Trigger:     r.Trigger,
		Debug:       r.Debug,
		Status:      string(r.Status),
		RunDir:      r.RunDir,
		IssueNumber: int64(r.IssueNumber),
		PrNumber:    int64(r.PRNumber),
		PrUrl:       r.PRURL,
		HeadSha:     r.HeadSHA,
		Context:     r.Context,
		Error:       r.Error,
		CreatedAt:   fallbackUnixNano(r.CreatedAt, time.Now().UTC()),
		UpdatedAt:   fallbackUnixNano(r.UpdatedAt, r.CreatedAt),
		StartedAt:   toNullInt64(r.StartedAt),
		CompletedAt: toNullInt64(r.CompletedAt),
		ID:          r.ID,
	}
}

func toNullInt64(t *time.Time) sql.NullInt64 {
	if t == nil || t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().UnixNano(), Valid: true}
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
