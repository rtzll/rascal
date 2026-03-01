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

-- name: ClaimRunStart :execrows
UPDATE runs
SET status = 'running', error = '', updated_at = ?, started_at = ?
WHERE id = ?
  AND status = 'queued'
  AND NOT EXISTS (
    SELECT 1
    FROM runs AS other
    WHERE other.task_id = runs.task_id
      AND other.status = 'running'
      AND other.id <> runs.id
  );

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

-- name: ClaimDelivery :one
INSERT INTO deliveries (
  id,
  status,
  claim_token,
  claimed_by,
  claimed_at,
  processed_at,
  seen_at,
  last_error
)
VALUES (
  sqlc.arg(id),
  'processing',
  sqlc.arg(claim_token),
  sqlc.arg(claimed_by),
  sqlc.arg(claimed_at),
  NULL,
  sqlc.arg(seen_at),
  ''
)
ON CONFLICT(id) DO UPDATE SET
  status = CASE
    WHEN deliveries.status = 'processed' THEN deliveries.status
    WHEN deliveries.status = 'processing' AND deliveries.claimed_at >= (excluded.claimed_at - 600000000000) THEN deliveries.status
    ELSE 'processing'
  END,
  claim_token = CASE
    WHEN deliveries.status = 'processed' THEN deliveries.claim_token
    WHEN deliveries.status = 'processing' AND deliveries.claimed_at >= (excluded.claimed_at - 600000000000) THEN deliveries.claim_token
    ELSE excluded.claim_token
  END,
  claimed_by = CASE
    WHEN deliveries.status = 'processed' THEN deliveries.claimed_by
    WHEN deliveries.status = 'processing' AND deliveries.claimed_at >= (excluded.claimed_at - 600000000000) THEN deliveries.claimed_by
    ELSE excluded.claimed_by
  END,
  claimed_at = CASE
    WHEN deliveries.status = 'processed' THEN deliveries.claimed_at
    WHEN deliveries.status = 'processing' AND deliveries.claimed_at >= (excluded.claimed_at - 600000000000) THEN deliveries.claimed_at
    ELSE excluded.claimed_at
  END,
  last_error = CASE
    WHEN deliveries.status = 'processed' THEN deliveries.last_error
    WHEN deliveries.status = 'processing' AND deliveries.claimed_at >= (excluded.claimed_at - 600000000000) THEN deliveries.last_error
    ELSE ''
  END
RETURNING status, claim_token;

-- name: CompleteDeliveryClaim :execrows
UPDATE deliveries
SET
  status = 'processed',
  claim_token = '',
  claimed_by = '',
  claimed_at = 0,
  processed_at = ?,
  seen_at = ?,
  last_error = ''
WHERE id = ? AND claim_token = ?;

-- name: ReleaseDeliveryClaim :execrows
DELETE FROM deliveries
WHERE id = ? AND claim_token = ?;

-- name: RecordDelivery :exec
INSERT OR REPLACE INTO deliveries (
  id,
  status,
  claim_token,
  claimed_by,
  claimed_at,
  processed_at,
  seen_at,
  last_error
)
VALUES (?, 'processed', '', '', 0, ?, ?, '');

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
