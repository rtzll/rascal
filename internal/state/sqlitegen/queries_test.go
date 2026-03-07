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
  agent_backend TEXT NOT NULL DEFAULT 'goose',
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'open',
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
  agent_backend TEXT NOT NULL DEFAULT 'goose',
  base_branch TEXT NOT NULL,
  head_branch TEXT NOT NULL,
  trigger TEXT NOT NULL,
  debug BOOLEAN NOT NULL DEFAULT 1,
  status TEXT NOT NULL,
  run_dir TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  pr_url TEXT NOT NULL DEFAULT '',
  pr_status TEXT NOT NULL DEFAULT 'none',
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

CREATE TABLE task_agent_sessions (
  task_id TEXT PRIMARY KEY,
  agent_backend TEXT NOT NULL,
  backend_session_id TEXT NOT NULL DEFAULT '',
  session_key TEXT NOT NULL DEFAULT '',
  session_root TEXT NOT NULL DEFAULT '',
  last_run_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX idx_task_agent_sessions_backend_updated ON task_agent_sessions (agent_backend, updated_at DESC);

CREATE TABLE run_leases (
  run_id TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  heartbeat_at INTEGER NOT NULL,
  lease_expires_at INTEGER NOT NULL
);

CREATE INDEX idx_run_leases_expires ON run_leases (lease_expires_at ASC);

CREATE TABLE run_executions (
  run_id TEXT PRIMARY KEY,
  backend TEXT NOT NULL,
  container_name TEXT NOT NULL,
  container_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'created',
  exit_code INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_observed_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_run_executions_container_id ON run_executions (container_id);
CREATE INDEX idx_run_executions_status ON run_executions (status);

CREATE TABLE run_cancels (
  run_id TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  source TEXT NOT NULL,
  requested_at INTEGER NOT NULL
);

CREATE TABLE deliveries (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL DEFAULT 'processing',
  claim_token TEXT NOT NULL DEFAULT '',
  claimed_by TEXT NOT NULL DEFAULT '',
  claimed_at INTEGER NOT NULL DEFAULT 0,
  processed_at INTEGER,
  seen_at INTEGER NOT NULL,
  last_error TEXT NOT NULL DEFAULT ''
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

	err := q.UpsertTask(ctx, UpsertTaskParams{
		ID:           "task_1",
		Repo:         "owner/repo",
		AgentBackend: "goose",
		IssueNumber:  1,
		PrNumber:     0,
		Status:       "open",
		LastRunID:    "",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("UpsertTask: %v", err)
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
		ID:           "run_1",
		TaskID:       "task_1",
		Repo:         "owner/repo",
		Task:         "first",
		AgentBackend: "goose",
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-1-run-1",
		Trigger:      "cli",
		Debug:        true,
		Status:       "queued",
		RunDir:       "/tmp/run_1",
		IssueNumber:  1,
		PrNumber:     77,
		PrUrl:        "",
		HeadSha:      "",
		Context:      "",
		Error:        "",
		CreatedAt:    later + 10,
		UpdatedAt:    later + 10,
		StartedAt:    sql.NullInt64{},
		CompletedAt:  sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("InsertRun run_1: %v", err)
	}

	run2, err := q.InsertRun(ctx, InsertRunParams{
		ID:           "run_2",
		TaskID:       "task_1",
		Repo:         "owner/repo",
		Task:         "second",
		AgentBackend: "goose",
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-1-run-2",
		Trigger:      "cli",
		Debug:        true,
		Status:       "queued",
		RunDir:       "/tmp/run_2",
		IssueNumber:  1,
		PrNumber:     77,
		PrUrl:        "",
		HeadSha:      "",
		Context:      "",
		Error:        "",
		CreatedAt:    later + 20,
		UpdatedAt:    later + 20,
		StartedAt:    sql.NullInt64{},
		CompletedAt:  sql.NullInt64{},
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

	if rows, err := q.ClaimRunStart(ctx, ClaimRunStartParams{
		UpdatedAt: later + 20,
		StartedAt: sql.NullInt64{Int64: later + 20, Valid: true},
		ID:        run1.ID,
	}); err != nil {
		t.Fatalf("ClaimRunStart run_1: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected run_1 claim rows=1, got %d", rows)
	}
	if rows, err := q.ClaimRunStart(ctx, ClaimRunStartParams{
		UpdatedAt: later + 21,
		StartedAt: sql.NullInt64{Int64: later + 21, Valid: true},
		ID:        run2.ID,
	}); err != nil {
		t.Fatalf("ClaimRunStart run_2 while run_1 active: %v", err)
	} else if rows != 0 {
		t.Fatalf("expected run_2 claim rows=0 while run_1 active, got %d", rows)
	}

	if _, err := q.UpdateRun(ctx, UpdateRunParams{
		TaskID:       run2.TaskID,
		Repo:         run2.Repo,
		Task:         run2.Task,
		AgentBackend: run2.AgentBackend,
		BaseBranch:   run2.BaseBranch,
		HeadBranch:   run2.HeadBranch,
		Trigger:      run2.Trigger,
		Debug:        run2.Debug,
		Status:       "queued",
		RunDir:       run2.RunDir,
		IssueNumber:  run2.IssueNumber,
		PrNumber:     run2.PrNumber,
		PrUrl:        run2.PrUrl,
		HeadSha:      run2.HeadSha,
		Context:      run2.Context,
		Error:        run2.Error,
		CreatedAt:    run2.CreatedAt,
		UpdatedAt:    run2.UpdatedAt + 2,
		StartedAt:    sql.NullInt64{},
		CompletedAt:  sql.NullInt64{},
		ID:           run2.ID,
	}); err != nil {
		t.Fatalf("UpdateRun: %v", err)
	}

	if err := q.UpsertTask(ctx, UpsertTaskParams{
		ID:           "task_2",
		Repo:         "owner/repo",
		AgentBackend: "goose",
		IssueNumber:  0,
		PrNumber:     0,
		Status:       "open",
		LastRunID:    "",
		CreatedAt:    later + 22,
		UpdatedAt:    later + 22,
	}); err != nil {
		t.Fatalf("UpsertTask task_2: %v", err)
	}

	if _, err := q.InsertRun(ctx, InsertRunParams{
		ID:           "run_3",
		TaskID:       "task_2",
		Repo:         "owner/repo",
		Task:         "third task",
		AgentBackend: "goose",
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-2",
		Trigger:      "cli",
		Debug:        true,
		Status:       "queued",
		RunDir:       "/tmp/run_3",
		IssueNumber:  0,
		PrNumber:     0,
		PrUrl:        "",
		HeadSha:      "",
		Context:      "",
		Error:        "",
		CreatedAt:    later + 22,
		UpdatedAt:    later + 22,
		StartedAt:    sql.NullInt64{},
		CompletedAt:  sql.NullInt64{},
	}); err != nil {
		t.Fatalf("InsertRun run_3: %v", err)
	}

	claimedNext, err := q.ClaimNextQueuedRunForTask(ctx, ClaimNextQueuedRunForTaskParams{
		UpdatedAt: later + 22,
		StartedAt: sql.NullInt64{Int64: later + 22, Valid: true},
		TaskID:    "task_2",
	})
	if err != nil {
		t.Fatalf("ClaimNextQueuedRunForTask: %v", err)
	}
	if claimedNext.ID != "run_3" {
		t.Fatalf("expected run_3 claim, got %s", claimedNext.ID)
	}
	taskWithQueue, err := q.GetTask(ctx, "task_1")
	if err != nil {
		t.Fatalf("GetTask task_1 with queued runs: %v", err)
	}
	if taskWithQueue.PendingInput == 0 {
		t.Fatal("expected task_1 pending_input=true with queued runs")
	}

	if err := q.CancelQueuedRuns(ctx, CancelQueuedRunsParams{Error: "stop", UpdatedAt: later + 23, CompletedAt: sql.NullInt64{Int64: later + 23, Valid: true}, TaskID: "task_1"}); err != nil {
		t.Fatalf("CancelQueuedRuns: %v", err)
	}

	gotRun1, err := q.GetRun(ctx, run2.ID)
	if err != nil {
		t.Fatalf("GetRun run_2 after cancel: %v", err)
	}
	if gotRun1.Status != "canceled" {
		t.Fatalf("expected run_2 canceled, got %s", gotRun1.Status)
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

	if err := q.UpsertRunLease(ctx, UpsertRunLeaseParams{
		RunID:          "run_lease_1",
		OwnerID:        "instance-a",
		HeartbeatAt:    later + 50,
		LeaseExpiresAt: later + 80,
	}); err != nil {
		t.Fatalf("UpsertRunLease: %v", err)
	}
	if rows, err := q.RenewRunLease(ctx, RenewRunLeaseParams{
		HeartbeatAt:    later + 55,
		LeaseExpiresAt: later + 85,
		RunID:          "run_lease_1",
		OwnerID:        "instance-a",
	}); err != nil {
		t.Fatalf("RenewRunLease owner: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected RenewRunLease rows=1 for owner, got %d", rows)
	}
	if rows, err := q.RenewRunLease(ctx, RenewRunLeaseParams{
		HeartbeatAt:    later + 56,
		LeaseExpiresAt: later + 86,
		RunID:          "run_lease_1",
		OwnerID:        "instance-b",
	}); err != nil {
		t.Fatalf("RenewRunLease non-owner: %v", err)
	} else if rows != 0 {
		t.Fatalf("expected RenewRunLease rows=0 for non-owner, got %d", rows)
	}
	leaseRow, err := q.GetRunLease(ctx, "run_lease_1")
	if err != nil {
		t.Fatalf("GetRunLease: %v", err)
	}
	if leaseRow.OwnerID != "instance-a" {
		t.Fatalf("unexpected run lease owner: %s", leaseRow.OwnerID)
	}
	leaseCount, err := q.CountRunLeasesByOwner(ctx, "instance-a")
	if err != nil {
		t.Fatalf("CountRunLeasesByOwner: %v", err)
	}
	if leaseCount != 1 {
		t.Fatalf("expected lease count 1 for instance-a, got %d", leaseCount)
	}
	if rows, err := q.DeleteRunLease(ctx, "run_lease_1"); err != nil {
		t.Fatalf("DeleteRunLease: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected DeleteRunLease rows=1, got %d", rows)
	}

	if err := q.UpsertRunLease(ctx, UpsertRunLeaseParams{
		RunID:          "run_lease_owner_1",
		OwnerID:        "instance-a",
		HeartbeatAt:    later + 50,
		LeaseExpiresAt: later + 60,
	}); err != nil {
		t.Fatalf("UpsertRunLease owner delete coverage: %v", err)
	}
	if rows, err := q.DeleteRunLeaseForOwner(ctx, DeleteRunLeaseForOwnerParams{
		RunID:   "run_lease_owner_1",
		OwnerID: "instance-b",
	}); err != nil {
		t.Fatalf("DeleteRunLeaseForOwner wrong owner: %v", err)
	} else if rows != 0 {
		t.Fatalf("expected DeleteRunLeaseForOwner wrong owner rows=0, got %d", rows)
	}
	if rows, err := q.DeleteRunLeaseForOwner(ctx, DeleteRunLeaseForOwnerParams{
		RunID:   "run_lease_owner_1",
		OwnerID: "instance-a",
	}); err != nil {
		t.Fatalf("DeleteRunLeaseForOwner: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected DeleteRunLeaseForOwner rows=1, got %d", rows)
	}

	if err := q.UpsertRunExecution(ctx, UpsertRunExecutionParams{
		RunID:          "run_exec_1",
		Backend:        "docker",
		ContainerName:  "rascal-run_exec_1",
		ContainerID:    "container-1",
		Status:         "running",
		ExitCode:       0,
		CreatedAt:      later + 60,
		UpdatedAt:      later + 60,
		LastObservedAt: later + 60,
	}); err != nil {
		t.Fatalf("UpsertRunExecution: %v", err)
	}
	if rows, err := q.UpdateRunExecutionState(ctx, UpdateRunExecutionStateParams{
		Status:         "exited",
		ExitCode:       137,
		UpdatedAt:      later + 61,
		LastObservedAt: later + 61,
		RunID:          "run_exec_1",
	}); err != nil {
		t.Fatalf("UpdateRunExecutionState: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected UpdateRunExecutionState rows=1, got %d", rows)
	}
	execRow, err := q.GetRunExecution(ctx, "run_exec_1")
	if err != nil {
		t.Fatalf("GetRunExecution: %v", err)
	}
	if execRow.Status != "exited" || execRow.ExitCode != 137 {
		t.Fatalf("unexpected run execution state: status=%s exit=%d", execRow.Status, execRow.ExitCode)
	}
	if rows, err := q.DeleteRunExecution(ctx, "run_exec_1"); err != nil {
		t.Fatalf("DeleteRunExecution: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected DeleteRunExecution rows=1, got %d", rows)
	}

	if err := q.UpsertRunCancel(ctx, UpsertRunCancelParams{
		RunID:       "run_cancel_1",
		Reason:      "canceled by user",
		Source:      "user",
		RequestedAt: later + 57,
	}); err != nil {
		t.Fatalf("UpsertRunCancel: %v", err)
	}
	if err := q.UpsertRunCancel(ctx, UpsertRunCancelParams{
		RunID:       "run_cancel_1",
		Reason:      "orchestrator shutdown",
		Source:      "shutdown",
		RequestedAt: later + 58,
	}); err != nil {
		t.Fatalf("UpsertRunCancel update: %v", err)
	}
	cancelRow, err := q.GetRunCancel(ctx, "run_cancel_1")
	if err != nil {
		t.Fatalf("GetRunCancel: %v", err)
	}
	if cancelRow.Source != "shutdown" {
		t.Fatalf("expected updated cancel source shutdown, got %q", cancelRow.Source)
	}
	if rows, err := q.DeleteRunCancel(ctx, "run_cancel_1"); err != nil {
		t.Fatalf("DeleteRunCancel: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected DeleteRunCancel rows=1, got %d", rows)
	}

	seen, err := q.DeliverySeen(ctx, "delivery_1")
	if err != nil {
		t.Fatalf("DeliverySeen before record: %v", err)
	}
	if seen != 0 {
		t.Fatalf("expected delivery to be unseen, got %d", seen)
	}

	delivery1Token := "claim_1"
	claim1, err := q.ClaimDelivery(ctx, ClaimDeliveryParams{
		ID:         "delivery_1",
		ClaimToken: delivery1Token,
		ClaimedBy:  "rascald-a",
		ClaimedAt:  later + 100,
		SeenAt:     later + 100,
	})
	if err != nil {
		t.Fatalf("ClaimDelivery delivery_1: %v", err)
	}
	if claim1.Status != "processing" || claim1.ClaimToken != delivery1Token {
		t.Fatalf("expected claimed delivery_1 token=%s, got status=%s token=%s", delivery1Token, claim1.Status, claim1.ClaimToken)
	}

	dupClaim, err := q.ClaimDelivery(ctx, ClaimDeliveryParams{
		ID:         "delivery_1",
		ClaimToken: "claim_2",
		ClaimedBy:  "rascald-b",
		ClaimedAt:  later + 101,
		SeenAt:     later + 101,
	})
	if err != nil {
		t.Fatalf("ClaimDelivery duplicate delivery_1: %v", err)
	}
	if dupClaim.ClaimToken != delivery1Token {
		t.Fatalf("expected duplicate claim token to remain %s, got %s", delivery1Token, dupClaim.ClaimToken)
	}

	if rows, err := q.ReleaseDeliveryClaim(ctx, ReleaseDeliveryClaimParams{
		ID:         "delivery_1",
		ClaimToken: delivery1Token,
	}); err != nil {
		t.Fatalf("ReleaseDeliveryClaim delivery_1: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected release rows=1, got %d", rows)
	}

	claimAfterRelease, err := q.ClaimDelivery(ctx, ClaimDeliveryParams{
		ID:         "delivery_1",
		ClaimToken: "claim_3",
		ClaimedBy:  "rascald-c",
		ClaimedAt:  later + 102,
		SeenAt:     later + 102,
	})
	if err != nil {
		t.Fatalf("ClaimDelivery after release delivery_1: %v", err)
	}
	if claimAfterRelease.ClaimToken != "claim_3" {
		t.Fatalf("expected claim_3 token after release, got %s", claimAfterRelease.ClaimToken)
	}

	if rows, err := q.CompleteDeliveryClaim(ctx, CompleteDeliveryClaimParams{
		ProcessedAt: sql.NullInt64{Int64: later + 103, Valid: true},
		SeenAt:      later + 103,
		ID:          "delivery_1",
		ClaimToken:  "claim_3",
	}); err != nil {
		t.Fatalf("CompleteDeliveryClaim delivery_1: %v", err)
	} else if rows != 1 {
		t.Fatalf("expected complete rows=1, got %d", rows)
	}

	if err := q.RecordDelivery(ctx, RecordDeliveryParams{
		ID:          "delivery_2",
		ProcessedAt: sql.NullInt64{Int64: later + 200, Valid: true},
		SeenAt:      later + 200,
	}); err != nil {
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
