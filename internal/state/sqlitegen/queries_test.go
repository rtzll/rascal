package sqlitegen

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const schemaDDL = `
CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'open',
  pending_input BOOLEAN NOT NULL DEFAULT 0,
  last_run_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX idx_tasks_repo_pr ON tasks (repo, pr_number);

CREATE TABLE runs (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  task_id TEXT NOT NULL,
  repo TEXT NOT NULL,
  task TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  head_branch TEXT NOT NULL,
  trigger TEXT NOT NULL,
  debug BOOLEAN NOT NULL DEFAULT 1,
  status TEXT NOT NULL,
  run_dir TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  pr_url TEXT NOT NULL DEFAULT '',
  head_sha TEXT NOT NULL DEFAULT '',
  context TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  started_at INTEGER,
  completed_at INTEGER
);

CREATE INDEX idx_runs_status_seq ON runs (status, seq DESC);
CREATE INDEX idx_runs_task_seq ON runs (task_id, seq DESC);

CREATE TABLE deliveries (
  id TEXT PRIMARY KEY,
  seen_at INTEGER NOT NULL
);

CREATE INDEX idx_deliveries_seen_at ON deliveries (seen_at ASC);
`

func newQueriesForTest(t *testing.T) (*sql.DB, *Queries) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		t.Fatalf("set WAL mode: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000;"); err != nil {
		t.Fatalf("set busy timeout: %v", err)
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db, New(db)
}

func TestQueriesCoverage(t *testing.T) {
	_, q := newQueriesForTest(t)
	ctx := context.Background()

	now := int64(1_700_000_000_000_000_000)
	later := now + 10

	upserted, err := q.UpsertTask(ctx, UpsertTaskParams{
		ID:           "task_1",
		Repo:         "owner/repo",
		IssueNumber:  1,
		PrNumber:     0,
		Status:       "open",
		PendingInput: false,
		LastRunID:    "",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}
	if upserted.ID != "task_1" {
		t.Fatalf("unexpected upserted task id: %s", upserted.ID)
	}

	gotTask, err := q.GetTask(ctx, "task_1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if gotTask.Repo != "owner/repo" {
		t.Fatalf("unexpected repo: %s", gotTask.Repo)
	}

	if _, err := q.SetTaskPR(ctx, SetTaskPRParams{PrNumber: 77, UpdatedAt: later, ID: "task_1"}); err != nil {
		t.Fatalf("SetTaskPR: %v", err)
	}

	byPR, err := q.FindTaskByPR(ctx, FindTaskByPRParams{Repo: "owner/repo", PrNumber: 77})
	if err != nil {
		t.Fatalf("FindTaskByPR: %v", err)
	}
	if byPR.ID != "task_1" {
		t.Fatalf("unexpected task by PR: %s", byPR.ID)
	}

	if _, err := q.SetTaskPendingInput(ctx, SetTaskPendingInputParams{PendingInput: true, UpdatedAt: later + 1, ID: "task_1"}); err != nil {
		t.Fatalf("SetTaskPendingInput: %v", err)
	}
	if _, err := q.SetTaskLastRun(ctx, SetTaskLastRunParams{LastRunID: "run_2", UpdatedAt: later + 2, IssueNumber: int64(2), PrNumber: int64(77), ID: "task_1"}); err != nil {
		t.Fatalf("SetTaskLastRun: %v", err)
	}

	completed, err := q.IsTaskCompleted(ctx, "task_1")
	if err != nil {
		t.Fatalf("IsTaskCompleted (before): %v", err)
	}
	if completed {
		t.Fatalf("expected task to be open before MarkTaskCompleted")
	}

	if _, err := q.MarkTaskCompleted(ctx, MarkTaskCompletedParams{UpdatedAt: later + 3, ID: "task_1"}); err != nil {
		t.Fatalf("MarkTaskCompleted: %v", err)
	}

	completed, err = q.IsTaskCompleted(ctx, "task_1")
	if err != nil {
		t.Fatalf("IsTaskCompleted (after): %v", err)
	}
	if !completed {
		t.Fatalf("expected task to be completed after MarkTaskCompleted")
	}

	run1, err := q.InsertRun(ctx, InsertRunParams{
		ID:          "run_1",
		TaskID:      "task_1",
		Repo:        "owner/repo",
		Task:        "first",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-1-run-1",
		Trigger:     "cli",
		Debug:       true,
		Status:      "queued",
		RunDir:      "/tmp/run_1",
		IssueNumber: 1,
		PrNumber:    77,
		PrUrl:       "",
		HeadSha:     "",
		Context:     "",
		Error:       "",
		CreatedAt:   later + 10,
		UpdatedAt:   later + 10,
		StartedAt:   sql.NullInt64{},
		CompletedAt: sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("InsertRun run_1: %v", err)
	}

	run2, err := q.InsertRun(ctx, InsertRunParams{
		ID:          "run_2",
		TaskID:      "task_1",
		Repo:        "owner/repo",
		Task:        "second",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-1-run-2",
		Trigger:     "cli",
		Debug:       true,
		Status:      "queued",
		RunDir:      "/tmp/run_2",
		IssueNumber: 1,
		PrNumber:    77,
		PrUrl:       "",
		HeadSha:     "",
		Context:     "",
		Error:       "",
		CreatedAt:   later + 20,
		UpdatedAt:   later + 20,
		StartedAt:   sql.NullInt64{},
		CompletedAt: sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("InsertRun run_2: %v", err)
	}

	gotRun, err := q.GetRun(ctx, "run_1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if gotRun.ID != "run_1" {
		t.Fatalf("unexpected run id: %s", gotRun.ID)
	}

	runs, err := q.ListRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}

	last, err := q.LastRunForTask(ctx, "task_1")
	if err != nil {
		t.Fatalf("LastRunForTask: %v", err)
	}
	if last.ID != "run_2" {
		t.Fatalf("expected run_2 to be last run, got %s", last.ID)
	}

	active, err := q.ActiveRunForTask(ctx, "task_1")
	if err != nil {
		t.Fatalf("ActiveRunForTask: %v", err)
	}
	if active.ID != "run_2" {
		t.Fatalf("expected run_2 to be active run, got %s", active.ID)
	}

	if _, err := q.UpdateRun(ctx, UpdateRunParams{
		TaskID:      run2.TaskID,
		Repo:        run2.Repo,
		Task:        run2.Task,
		BaseBranch:  run2.BaseBranch,
		HeadBranch:  run2.HeadBranch,
		Trigger:     run2.Trigger,
		Debug:       run2.Debug,
		Status:      "running",
		RunDir:      run2.RunDir,
		IssueNumber: run2.IssueNumber,
		PrNumber:    run2.PrNumber,
		PrUrl:       run2.PrUrl,
		HeadSha:     run2.HeadSha,
		Context:     run2.Context,
		Error:       run2.Error,
		CreatedAt:   run2.CreatedAt,
		UpdatedAt:   run2.UpdatedAt + 1,
		StartedAt:   sql.NullInt64{Int64: later + 21, Valid: true},
		CompletedAt: sql.NullInt64{},
		ID:          run2.ID,
	}); err != nil {
		t.Fatalf("UpdateRun: %v", err)
	}

	if err := q.CancelQueuedRuns(ctx, CancelQueuedRunsParams{Error: "stop", UpdatedAt: later + 22, CompletedAt: sql.NullInt64{Int64: later + 22, Valid: true}, TaskID: "task_1"}); err != nil {
		t.Fatalf("CancelQueuedRuns: %v", err)
	}

	gotRun1, err := q.GetRun(ctx, run1.ID)
	if err != nil {
		t.Fatalf("GetRun run_1 after cancel: %v", err)
	}
	if gotRun1.Status != "canceled" {
		t.Fatalf("expected run_1 canceled, got %s", gotRun1.Status)
	}

	if err := q.TrimOldRuns(ctx, 1); err != nil {
		t.Fatalf("TrimOldRuns: %v", err)
	}

	runs, err = q.ListRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListRuns after trim: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run after trim, got %d", len(runs))
	}

	seen, err := q.DeliverySeen(ctx, "delivery_1")
	if err != nil {
		t.Fatalf("DeliverySeen before record: %v", err)
	}
	if seen != 0 {
		t.Fatalf("expected delivery to be unseen, got %d", seen)
	}

	if err := q.RecordDelivery(ctx, RecordDeliveryParams{ID: "delivery_1", SeenAt: later + 100}); err != nil {
		t.Fatalf("RecordDelivery delivery_1: %v", err)
	}
	if err := q.RecordDelivery(ctx, RecordDeliveryParams{ID: "delivery_2", SeenAt: later + 200}); err != nil {
		t.Fatalf("RecordDelivery delivery_2: %v", err)
	}

	count, err := q.CountDeliveries(ctx)
	if err != nil {
		t.Fatalf("CountDeliveries: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 deliveries, got %d", count)
	}

	if err := q.DeleteOldestDeliveries(ctx, 1); err != nil {
		t.Fatalf("DeleteOldestDeliveries: %v", err)
	}

	count, err = q.CountDeliveries(ctx)
	if err != nil {
		t.Fatalf("CountDeliveries after delete: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 delivery after delete, got %d", count)
	}

	seen, err = q.DeliverySeen(ctx, "delivery_1")
	if err != nil {
		t.Fatalf("DeliverySeen delivery_1 after delete: %v", err)
	}
	if seen != 0 {
		t.Fatalf("expected delivery_1 to be deleted, got %d", seen)
	}
}
