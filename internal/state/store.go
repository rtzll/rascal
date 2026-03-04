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
	if err := s.q.UpsertTask(context.Background(), sqlitegen.UpsertTaskParams{
		ID:          in.ID,
		Repo:        in.Repo,
		IssueNumber: int64(in.IssueNumber),
		PrNumber:    int64(in.PRNumber),
		Status:      string(TaskOpen),
		LastRunID:   "",
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		return Task{}, err
	}
	row, err := s.q.GetTask(context.Background(), in.ID)
	if err != nil {
		return Task{}, err
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
		Repo:     strings.TrimSpace(repo),
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

func (s *Store) MarkTaskOpen(taskID string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("task id is required")
	}
	res, err := s.db.ExecContext(context.Background(), "UPDATE tasks SET status = 'open', updated_at = ? WHERE id = ?", time.Now().UTC().UnixNano(), taskID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
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
	prStatus := normalizePRStatus(in.PRStatus)
	if prStatus == PRStatusNone && in.PRNumber > 0 {
		prStatus = PRStatusOpen
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.UpsertTask(context.Background(), sqlitegen.UpsertTaskParams{
		ID:          in.TaskID,
		Repo:        in.Repo,
		IssueNumber: int64(in.IssueNumber),
		PrNumber:    int64(in.PRNumber),
		Status:      string(TaskOpen),
		LastRunID:   "",
		CreatedAt:   now.UnixNano(),
		UpdatedAt:   now.UnixNano(),
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
		PrStatus:    string(prStatus),
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

func (s *Store) ListRunningRuns() []Run {
	rows, err := s.q.ListRunningRuns(context.Background())
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
		return Run{}, false, err
	}
	row, err := s.q.GetRun(context.Background(), runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, fmt.Errorf("run %q not found", runID)
		}
		return Run{}, false, err
	}
	return fromDBRun(row), rows > 0, nil
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
			return fromDBRun(row), true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, err
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
		return Run{}, false, err
	}
	return fromDBRun(row), true, nil
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
	return s.q.UpsertRunLease(context.Background(), sqlitegen.UpsertRunLeaseParams{
		RunID:          runID,
		OwnerID:        ownerID,
		HeartbeatAt:    now.UnixNano(),
		LeaseExpiresAt: now.Add(ttl).UnixNano(),
	})
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
		return false, err
	}
	return rows > 0, nil
}

func (s *Store) DeleteRunLease(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	_, err := s.q.DeleteRunLease(context.Background(), runID)
	return err
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
	return s.q.UpsertRunCancel(context.Background(), sqlitegen.UpsertRunCancelParams{
		RunID:       runID,
		Reason:      reason,
		Source:      source,
		RequestedAt: time.Now().UTC().UnixNano(),
	})
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
	return err
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
		return DeliveryClaim{}, false, err
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
		return DeliveryClaim{}, false, err
	}
	if err := s.trimDeliveriesIfNeeded(); err != nil {
		return DeliveryClaim{}, false, err
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
		return err
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
	return err
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
		return err
	}
	return s.trimDeliveriesIfNeeded()
}

func (s *Store) trimDeliveriesIfNeeded() error {
	count, err := s.q.CountDeliveries(context.Background())
	if err != nil {
		return err
	}
	if count <= maxDeliveries {
		return nil
	}
	return s.q.DeleteOldestDeliveries(context.Background(), count-maxDeliveries)
}

func newClaimToken() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("create delivery claim token: %w", err)
	}
	return "claim_" + hex.EncodeToString(buf), nil
}

func fromDBTaskParts(id, repo string, issueNumber, prNumber int64, status string, pendingInput int64, lastRunID string, createdAt, updatedAt int64) Task {
	return Task{
		ID:           id,
		Repo:         repo,
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
	return fromDBTaskParts(t.ID, t.Repo, t.IssueNumber, t.PrNumber, t.Status, t.PendingInput, t.LastRunID, t.CreatedAt, t.UpdatedAt)
}

func fromDBFindTaskByPRRow(t sqlitegen.FindTaskByPRRow) Task {
	return fromDBTaskParts(t.ID, t.Repo, t.IssueNumber, t.PrNumber, t.Status, t.PendingInput, t.LastRunID, t.CreatedAt, t.UpdatedAt)
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
		PRStatus:    normalizePRStatus(PRStatus(r.PrStatus)),
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
	prStatus := normalizePRStatus(r.PRStatus)
	if prStatus == PRStatusNone && r.PRNumber > 0 {
		switch r.Status {
		case StatusSucceeded:
			prStatus = PRStatusMerged
		case StatusCanceled:
			prStatus = PRStatusClosedUnmerged
		default:
			prStatus = PRStatusOpen
		}
	}
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
		PrStatus:    string(prStatus),
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

func fallbackUnixNano(t time.Time, fallback time.Time) int64 {
	if !t.IsZero() {
		return t.UTC().UnixNano()
	}
	if !fallback.IsZero() {
		return fallback.UTC().UnixNano()
	}
	return time.Now().UTC().UnixNano()
}
