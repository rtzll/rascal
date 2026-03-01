-- name: UpsertTask :one
INSERT INTO tasks (
  id,
  repo,
  issue_number,
  pr_number,
  status,
  pending_input,
  last_run_id,
  created_at,
  updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  repo = excluded.repo,
  issue_number = CASE WHEN excluded.issue_number > 0 THEN excluded.issue_number ELSE tasks.issue_number END,
  pr_number = CASE WHEN excluded.pr_number > 0 THEN excluded.pr_number ELSE tasks.pr_number END,
  updated_at = excluded.updated_at
RETURNING id, repo, issue_number, pr_number, status, pending_input, last_run_id, created_at, updated_at;

-- name: GetTask :one
SELECT id, repo, issue_number, pr_number, status, pending_input, last_run_id, created_at, updated_at
FROM tasks
WHERE id = ?;

-- name: FindTaskByPR :one
SELECT id, repo, issue_number, pr_number, status, pending_input, last_run_id, created_at, updated_at
FROM tasks
WHERE repo = ? AND pr_number = ?;

-- name: SetTaskPR :execrows
UPDATE tasks
SET pr_number = ?, updated_at = ?
WHERE id = ?;

-- name: SetTaskPendingInput :execrows
UPDATE tasks
SET pending_input = ?, updated_at = ?
WHERE id = ?;

-- name: MarkTaskCompleted :execrows
UPDATE tasks
SET status = 'completed', pending_input = 0, updated_at = ?
WHERE id = ?;

-- name: SetTaskLastRun :execrows
UPDATE tasks
SET
  last_run_id = sqlc.arg(last_run_id),
  updated_at = sqlc.arg(updated_at),
  issue_number = CASE WHEN sqlc.arg(issue_number) > 0 THEN sqlc.arg(issue_number) ELSE issue_number END,
  pr_number = CASE WHEN sqlc.arg(pr_number) > 0 THEN sqlc.arg(pr_number) ELSE pr_number END
WHERE id = sqlc.arg(id);

-- name: IsTaskCompleted :one
SELECT status = 'completed'
FROM tasks
WHERE id = ?;

-- name: InsertRun :one
INSERT INTO runs (
  id,
  task_id,
  repo,
  task,
  base_branch,
  head_branch,
  trigger,
  debug,
  status,
  run_dir,
  issue_number,
  pr_number,
  pr_url,
  head_sha,
  context,
  error,
  created_at,
  updated_at,
  started_at,
  completed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING seq, id, task_id, repo, task, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, head_sha, context, error, created_at, updated_at, started_at, completed_at;

-- name: GetRun :one
SELECT seq, id, task_id, repo, task, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, head_sha, context, error, created_at, updated_at, started_at, completed_at
FROM runs
WHERE id = ?;

-- name: ListRuns :many
SELECT seq, id, task_id, repo, task, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, head_sha, context, error, created_at, updated_at, started_at, completed_at
FROM runs
ORDER BY seq DESC
LIMIT ?;

-- name: LastRunForTask :one
SELECT seq, id, task_id, repo, task, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, head_sha, context, error, created_at, updated_at, started_at, completed_at
FROM runs
WHERE task_id = ?
ORDER BY seq DESC
LIMIT 1;

-- name: ActiveRunForTask :one
SELECT seq, id, task_id, repo, task, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, head_sha, context, error, created_at, updated_at, started_at, completed_at
FROM runs
WHERE task_id = ? AND status IN ('queued', 'running')
ORDER BY seq DESC
LIMIT 1;

-- name: UpdateRun :execrows
UPDATE runs
SET
  task_id = ?,
  repo = ?,
  task = ?,
  base_branch = ?,
  head_branch = ?,
  trigger = ?,
  debug = ?,
  status = ?,
  run_dir = ?,
  issue_number = ?,
  pr_number = ?,
  pr_url = ?,
  head_sha = ?,
  context = ?,
  error = ?,
  created_at = ?,
  updated_at = ?,
  started_at = ?,
  completed_at = ?
WHERE id = ?;

-- name: CancelQueuedRuns :exec
UPDATE runs
SET status = 'canceled', error = ?, updated_at = ?, completed_at = ?
WHERE task_id = ? AND status = 'queued';

-- name: TrimOldRuns :exec
DELETE FROM runs
WHERE id IN (
  SELECT id
  FROM runs
  ORDER BY seq DESC
  LIMIT -1 OFFSET ?
);

-- name: DeliverySeen :one
SELECT EXISTS(SELECT 1 FROM deliveries WHERE id = ?);

-- name: RecordDelivery :exec
INSERT OR IGNORE INTO deliveries (id, seen_at)
VALUES (?, ?);

-- name: CountDeliveries :one
SELECT COUNT(*)
FROM deliveries;

-- name: DeleteOldestDeliveries :exec
DELETE FROM deliveries
WHERE id IN (
  SELECT id
  FROM deliveries
  ORDER BY seen_at ASC
  LIMIT ?
);
