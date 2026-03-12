-- name: UpsertTask :exec
INSERT INTO tasks (
  id,
  repo,
  agent_backend,
  issue_number,
  pr_number,
  status,
  last_run_id,
  created_at,
  updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  repo = excluded.repo,
  agent_backend = excluded.agent_backend,
  issue_number = CASE WHEN excluded.issue_number > 0 THEN excluded.issue_number ELSE tasks.issue_number END,
  pr_number = CASE WHEN excluded.pr_number > 0 THEN excluded.pr_number ELSE tasks.pr_number END,
  updated_at = excluded.updated_at;

-- name: GetTask :one
SELECT
  tasks.id,
  tasks.repo,
  tasks.agent_backend,
  tasks.issue_number,
  tasks.pr_number,
  tasks.status,
  EXISTS(
    SELECT 1
    FROM runs
    WHERE runs.task_id = tasks.id
      AND runs.status = 'queued'
  ) AS pending_input,
  tasks.last_run_id,
  tasks.created_at,
  tasks.updated_at
FROM tasks
WHERE tasks.id = ?;

-- name: FindTaskByPR :one
SELECT
  tasks.id,
  tasks.repo,
  tasks.agent_backend,
  tasks.issue_number,
  tasks.pr_number,
  tasks.status,
  EXISTS(
    SELECT 1
    FROM runs
    WHERE runs.task_id = tasks.id
      AND runs.status = 'queued'
  ) AS pending_input,
  tasks.last_run_id,
  tasks.created_at,
  tasks.updated_at
FROM tasks
WHERE tasks.repo = ? AND tasks.pr_number = ?;

-- name: SetTaskPR :execrows
UPDATE tasks
SET pr_number = ?, updated_at = ?
WHERE id = ?;

-- name: MarkTaskCompleted :execrows
UPDATE tasks
SET status = 'completed', updated_at = ?
WHERE id = ?;

-- name: MarkTaskOpen :execrows
UPDATE tasks
SET status = 'open', updated_at = ?
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
  agent_backend,
  base_branch,
  head_branch,
  trigger,
  debug,
  status,
  run_dir,
  issue_number,
  pr_number,
  pr_url,
  pr_status,
  head_sha,
  context,
  error,
  created_at,
  updated_at,
  started_at,
  completed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING seq, id, task_id, repo, task, agent_backend, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, pr_status, head_sha, context, error, created_at, updated_at, started_at, completed_at;

-- name: GetRun :one
SELECT seq, id, task_id, repo, task, agent_backend, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, pr_status, head_sha, context, error, created_at, updated_at, started_at, completed_at
FROM runs
WHERE id = ?;

-- name: GetRunResponseTarget :one
SELECT response_target_repo, response_target_issue_number, response_target_requested_by, response_target_trigger, response_target_review_thread_id
FROM runs
WHERE id = ?;

-- name: SetRunResponseTarget :execrows
UPDATE runs
SET
  response_target_repo = sqlc.arg(response_target_repo),
  response_target_issue_number = sqlc.arg(response_target_issue_number),
  response_target_requested_by = sqlc.arg(response_target_requested_by),
  response_target_trigger = sqlc.arg(response_target_trigger),
  response_target_review_thread_id = sqlc.arg(response_target_review_thread_id),
  updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: ListRuns :many
SELECT seq, id, task_id, repo, task, agent_backend, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, pr_status, head_sha, context, error, created_at, updated_at, started_at, completed_at
FROM runs
ORDER BY seq DESC
LIMIT ?;

-- name: ListRunningRuns :many
SELECT seq, id, task_id, repo, task, agent_backend, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, pr_status, head_sha, context, error, created_at, updated_at, started_at, completed_at
FROM runs
WHERE status = 'running'
ORDER BY seq DESC;

-- name: LastRunForTask :one
SELECT seq, id, task_id, repo, task, agent_backend, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, pr_status, head_sha, context, error, created_at, updated_at, started_at, completed_at
FROM runs
WHERE task_id = ?
ORDER BY seq DESC
LIMIT 1;

-- name: ActiveRunForTask :one
SELECT seq, id, task_id, repo, task, agent_backend, base_branch, head_branch, trigger, debug, status, run_dir, issue_number, pr_number, pr_url, pr_status, head_sha, context, error, created_at, updated_at, started_at, completed_at
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
  agent_backend = ?,
  base_branch = ?,
  head_branch = ?,
  trigger = ?,
  debug = ?,
  status = ?,
  run_dir = ?,
  issue_number = ?,
  pr_number = ?,
  pr_url = ?,
  pr_status = ?,
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

-- name: CancelQueuedReviewThreadRuns :execrows
UPDATE runs
SET status = 'canceled', error = ?, updated_at = ?, completed_at = ?
WHERE task_id = ?
  AND repo = ?
  AND pr_number = ?
  AND trigger = 'pr_review_thread'
  AND status = 'queued'
  AND response_target_review_thread_id = ?;

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

-- name: ClaimNextQueuedRunForTask :one
UPDATE runs
SET status = 'running', error = '', updated_at = sqlc.arg(updated_at), started_at = sqlc.arg(started_at)
WHERE id = (
  SELECT r.id
  FROM runs AS r
  WHERE r.status = 'queued'
    AND r.task_id = sqlc.arg(task_id)
    AND NOT EXISTS (
      SELECT 1
      FROM runs AS other
      WHERE other.task_id = r.task_id
        AND other.status = 'running'
    )
    AND NOT EXISTS (
      SELECT 1
      FROM run_cancels AS rc
      WHERE rc.run_id = r.id
    )
  ORDER BY r.created_at ASC, r.seq ASC
  LIMIT 1
)
  AND status = 'queued'
  AND NOT EXISTS (
    SELECT 1
    FROM runs AS other
    WHERE other.task_id = runs.task_id
      AND other.status = 'running'
      AND other.id <> runs.id
  )
RETURNING
  seq,
  id,
  task_id,
  repo,
  task,
  agent_backend,
  base_branch,
  head_branch,
  trigger,
  debug,
  status,
  run_dir,
  issue_number,
  pr_number,
  pr_url,
  pr_status,
  head_sha,
  context,
  error,
  created_at,
  updated_at,
  started_at,
  completed_at;

-- name: ClaimNextQueuedRun :one
UPDATE runs
SET status = 'running', error = '', updated_at = sqlc.arg(updated_at), started_at = sqlc.arg(started_at)
WHERE id = (
  SELECT r.id
  FROM runs AS r
  WHERE r.status = 'queued'
    AND NOT EXISTS (
      SELECT 1
      FROM runs AS other
      WHERE other.task_id = r.task_id
        AND other.status = 'running'
    )
    AND NOT EXISTS (
      SELECT 1
      FROM run_cancels AS rc
      WHERE rc.run_id = r.id
    )
  ORDER BY r.created_at ASC, r.seq ASC
  LIMIT 1
)
  AND status = 'queued'
  AND NOT EXISTS (
    SELECT 1
    FROM runs AS other
    WHERE other.task_id = runs.task_id
      AND other.status = 'running'
      AND other.id <> runs.id
  )
RETURNING
  seq,
  id,
  task_id,
  repo,
  task,
  agent_backend,
  base_branch,
  head_branch,
  trigger,
  debug,
  status,
  run_dir,
  issue_number,
  pr_number,
  pr_url,
  pr_status,
  head_sha,
  context,
  error,
  created_at,
  updated_at,
  started_at,
  completed_at;

-- name: TrimOldRuns :exec
DELETE FROM runs
WHERE id IN (
  SELECT id
  FROM runs
  ORDER BY seq DESC
  LIMIT -1 OFFSET ?
);

-- name: UpsertRunLease :exec
INSERT INTO run_leases (run_id, owner_id, heartbeat_at, lease_expires_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
  owner_id = excluded.owner_id,
  heartbeat_at = excluded.heartbeat_at,
  lease_expires_at = excluded.lease_expires_at;

-- name: RenewRunLease :execrows
UPDATE run_leases
SET
  heartbeat_at = ?,
  lease_expires_at = ?
WHERE run_id = ? AND owner_id = ?;

-- name: DeleteRunLease :execrows
DELETE FROM run_leases
WHERE run_id = ?;

-- name: DeleteRunLeaseForOwner :execrows
DELETE FROM run_leases
WHERE run_id = ? AND owner_id = ?;

-- name: GetRunLease :one
SELECT run_id, owner_id, heartbeat_at, lease_expires_at
FROM run_leases
WHERE run_id = ?;

-- name: CountRunLeasesByOwner :one
SELECT COUNT(*)
FROM run_leases
WHERE owner_id = ?;

-- name: UpsertRunExecution :exec
INSERT INTO run_executions (
  run_id,
  backend,
  container_name,
  container_id,
  status,
  exit_code,
  created_at,
  updated_at,
  last_observed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
  backend = excluded.backend,
  container_name = excluded.container_name,
  container_id = excluded.container_id,
  status = excluded.status,
  exit_code = excluded.exit_code,
  updated_at = excluded.updated_at,
  last_observed_at = excluded.last_observed_at;

-- name: UpdateRunExecutionState :execrows
UPDATE run_executions
SET
  status = ?,
  exit_code = ?,
  updated_at = ?,
  last_observed_at = ?
WHERE run_id = ?;

-- name: GetRunExecution :one
SELECT run_id, backend, container_name, container_id, status, exit_code, created_at, updated_at, last_observed_at
FROM run_executions
WHERE run_id = ?;

-- name: DeleteRunExecution :execrows
DELETE FROM run_executions
WHERE run_id = ?;

-- name: UpsertRunTokenUsage :one
INSERT INTO run_token_usage (
  run_id,
  backend,
  provider,
  model,
  total_tokens,
  input_tokens,
  output_tokens,
  cached_input_tokens,
  reasoning_output_tokens,
  raw_usage_json,
  captured_at,
  created_at,
  updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
  backend = excluded.backend,
  provider = excluded.provider,
  model = excluded.model,
  total_tokens = excluded.total_tokens,
  input_tokens = excluded.input_tokens,
  output_tokens = excluded.output_tokens,
  cached_input_tokens = excluded.cached_input_tokens,
  reasoning_output_tokens = excluded.reasoning_output_tokens,
  raw_usage_json = excluded.raw_usage_json,
  captured_at = excluded.captured_at,
  updated_at = excluded.updated_at
RETURNING
  run_id,
  backend,
  provider,
  model,
  total_tokens,
  input_tokens,
  output_tokens,
  cached_input_tokens,
  reasoning_output_tokens,
  raw_usage_json,
  captured_at,
  created_at,
  updated_at;

-- name: GetRunTokenUsage :one
SELECT
  run_id,
  backend,
  provider,
  model,
  total_tokens,
  input_tokens,
  output_tokens,
  cached_input_tokens,
  reasoning_output_tokens,
  raw_usage_json,
  captured_at,
  created_at,
  updated_at
FROM run_token_usage
WHERE run_id = ?;

-- name: UpsertRunCancel :exec
INSERT INTO run_cancels (run_id, reason, source, requested_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
  reason = excluded.reason,
  source = excluded.source,
  requested_at = excluded.requested_at;

-- name: GetRunCancel :one
SELECT run_id, reason, source, requested_at
FROM run_cancels
WHERE run_id = ?;

-- name: DeleteRunCancel :execrows
DELETE FROM run_cancels
WHERE run_id = ?;

-- name: UpsertSchedulerPause :one
INSERT INTO scheduler_pauses (
  scope,
  reason,
  paused_until,
  created_at,
  updated_at
)
VALUES (
  sqlc.arg(scope),
  sqlc.arg(reason),
  sqlc.arg(paused_until),
  sqlc.arg(created_at),
  sqlc.arg(updated_at)
)
ON CONFLICT(scope) DO UPDATE SET
  paused_until = CASE
    WHEN scheduler_pauses.paused_until > excluded.paused_until THEN scheduler_pauses.paused_until
    ELSE excluded.paused_until
  END,
  reason = CASE
    WHEN scheduler_pauses.paused_until > excluded.paused_until THEN scheduler_pauses.reason
    ELSE excluded.reason
  END,
  updated_at = excluded.updated_at
RETURNING
  scope,
  reason,
  paused_until,
  created_at,
  updated_at;

-- name: GetActiveSchedulerPause :one
SELECT
  scope,
  reason,
  paused_until,
  created_at,
  updated_at
FROM scheduler_pauses
WHERE scope = ?
  AND paused_until > ?;

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

-- name: UpsertTaskAgentSession :exec
INSERT INTO task_agent_sessions (
  task_id,
  agent_backend,
  backend_session_id,
  session_key,
  session_root,
  last_run_id,
  created_at,
  updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(task_id) DO UPDATE SET
  agent_backend = excluded.agent_backend,
  backend_session_id = excluded.backend_session_id,
  session_key = excluded.session_key,
  session_root = excluded.session_root,
  last_run_id = excluded.last_run_id,
  updated_at = excluded.updated_at;

-- name: GetTaskAgentSession :one
SELECT
  task_id,
  agent_backend,
  backend_session_id,
  session_key,
  session_root,
  last_run_id,
  created_at,
  updated_at
FROM task_agent_sessions
WHERE task_id = ?;

-- name: DeleteTaskAgentSession :execrows
DELETE FROM task_agent_sessions
WHERE task_id = ?;

-- name: SetTaskCreatedByUser :execrows
UPDATE tasks
SET created_by_user_id = ?, updated_at = ?
WHERE id = ?;

-- name: SetRunCreatedByUser :execrows
UPDATE runs
SET created_by_user_id = ?, updated_at = ?
WHERE id = ?;

-- name: SetRunCredentialID :execrows
UPDATE runs
SET credential_id = ?, updated_at = ?
WHERE id = ?;

-- name: GetRunCredentialInfo :one
SELECT id, created_by_user_id, credential_id
FROM runs
WHERE id = ?;

-- name: UpsertUser :exec
INSERT INTO users (
  id,
  external_login,
  role,
  created_at,
  updated_at
)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  external_login = excluded.external_login,
  role = excluded.role,
  updated_at = excluded.updated_at;

-- name: GetUserByID :one
SELECT id, external_login, role, created_at, updated_at
FROM users
WHERE id = ?;

-- name: GetUserByExternalLogin :one
SELECT id, external_login, role, created_at, updated_at
FROM users
WHERE external_login = ?;

-- name: UpsertAPIKey :exec
INSERT INTO api_keys (
  id,
  user_id,
  key_hash,
  label,
  created_at,
  last_used_at,
  disabled_at
)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(key_hash) DO UPDATE SET
  user_id = excluded.user_id,
  label = excluded.label,
  disabled_at = excluded.disabled_at;

-- name: GetAPIKeyPrincipal :one
SELECT
  api_keys.id AS api_key_id,
  api_keys.user_id,
  users.external_login,
  users.role
FROM api_keys
JOIN users ON users.id = api_keys.user_id
WHERE api_keys.key_hash = ?
  AND api_keys.disabled_at IS NULL;

-- name: TouchAPIKeyLastUsed :execrows
UPDATE api_keys
SET last_used_at = ?
WHERE id = ?;

-- name: CreateCodexCredential :exec
INSERT INTO codex_credentials (
  id,
  owner_user_id,
  scope,
  encrypted_auth_blob,
  weight,
  status,
  cooldown_until,
  last_error,
  created_at,
  updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateCodexCredential :execrows
UPDATE codex_credentials
SET
  owner_user_id = ?,
  scope = ?,
  encrypted_auth_blob = ?,
  weight = ?,
  status = ?,
  cooldown_until = ?,
  last_error = ?,
  updated_at = ?
WHERE id = ?;

-- name: SetCodexCredentialStatus :execrows
UPDATE codex_credentials
SET
  status = ?,
  cooldown_until = ?,
  last_error = ?,
  updated_at = ?
WHERE id = ?;

-- name: GetCodexCredential :one
SELECT
  id,
  owner_user_id,
  scope,
  encrypted_auth_blob,
  weight,
  status,
  cooldown_until,
  last_error,
  created_at,
  updated_at
FROM codex_credentials
WHERE id = ?;

-- name: ListCodexCredentialsByOwner :many
SELECT
  id,
  owner_user_id,
  scope,
  encrypted_auth_blob,
  weight,
  status,
  cooldown_until,
  last_error,
  created_at,
  updated_at
FROM codex_credentials
WHERE owner_user_id = ?
ORDER BY created_at DESC;

-- name: ListSharedCodexCredentials :many
SELECT
  id,
  owner_user_id,
  scope,
  encrypted_auth_blob,
  weight,
  status,
  cooldown_until,
  last_error,
  created_at,
  updated_at
FROM codex_credentials
WHERE scope = 'shared'
ORDER BY created_at DESC;

-- name: ListAllCodexCredentials :many
SELECT
  id,
  owner_user_id,
  scope,
  encrypted_auth_blob,
  weight,
  status,
  cooldown_until,
  last_error,
  created_at,
  updated_at
FROM codex_credentials
ORDER BY created_at DESC;

-- name: ListCredentialCandidates :many
SELECT
  c.id,
  c.owner_user_id,
  c.scope,
  c.weight,
  c.status,
  c.cooldown_until,
  CAST(COALESCE((
    SELECT COUNT(*)
    FROM credential_leases AS l
    WHERE l.credential_id = c.id
      AND l.released_at IS NULL
      AND l.expires_at > sqlc.arg(now)
  ), 0) AS INTEGER) AS active_leases,
  CAST(COALESCE((
    SELECT SUM(u.tokens)
    FROM credential_usage AS u
    WHERE u.credential_id = c.id
      AND u.window_start >= sqlc.arg(usage_window_start)
  ), 0) AS INTEGER) AS usage_tokens,
  CAST(COALESCE((
    SELECT SUM(u.runs)
    FROM credential_usage AS u
    WHERE u.credential_id = c.id
      AND u.window_start >= sqlc.arg(usage_window_start)
  ), 0) AS INTEGER) AS usage_runs,
  c.last_error,
  c.created_at,
  c.updated_at
FROM codex_credentials AS c
WHERE c.status = 'active'
  AND (c.cooldown_until IS NULL OR c.cooldown_until <= sqlc.arg(now))
  AND (c.scope = 'shared' OR c.owner_user_id = sqlc.arg(requester_user_id))
ORDER BY c.created_at ASC, c.id ASC;

-- name: TryCreateCredentialLease :execrows
INSERT INTO credential_leases (
  id,
  credential_id,
  run_id,
  user_id,
  strategy,
  acquired_at,
  expires_at,
  released_at
)
SELECT
  sqlc.arg(id),
  sqlc.arg(credential_id),
  sqlc.arg(run_id),
  sqlc.arg(user_id),
  sqlc.arg(strategy),
  sqlc.arg(acquired_at),
  sqlc.arg(expires_at),
  NULL
WHERE EXISTS (
  SELECT 1
  FROM codex_credentials AS c
  WHERE c.id = sqlc.arg(credential_id)
    AND c.status = 'active'
    AND (c.cooldown_until IS NULL OR c.cooldown_until <= sqlc.arg(now))
    AND (
      c.scope = 'shared'
      OR (c.scope = 'personal' AND c.owner_user_id = sqlc.arg(user_id))
    )
)
AND NOT EXISTS (
  SELECT 1
  FROM credential_leases AS existing
  WHERE existing.run_id = ?3
    AND existing.released_at IS NULL
    AND existing.expires_at > ?8
);

-- name: GetCredentialLease :one
SELECT id, credential_id, run_id, user_id, strategy, acquired_at, expires_at, released_at
FROM credential_leases
WHERE id = ?;

-- name: GetActiveCredentialLeaseByRunID :one
SELECT id, credential_id, run_id, user_id, strategy, acquired_at, expires_at, released_at
FROM credential_leases
WHERE run_id = ?
  AND released_at IS NULL
ORDER BY acquired_at DESC
LIMIT 1;

-- name: RenewCredentialLease :execrows
UPDATE credential_leases
SET expires_at = ?
WHERE id = ?
  AND released_at IS NULL
  AND expires_at > ?;

-- name: ReleaseCredentialLease :one
UPDATE credential_leases
SET released_at = ?
WHERE id = ?
  AND released_at IS NULL
RETURNING id, credential_id, run_id, user_id, strategy, acquired_at, expires_at, released_at;

-- name: ReleaseCredentialLeaseByRunID :execrows
UPDATE credential_leases
SET released_at = ?
WHERE run_id = ?
  AND released_at IS NULL;

-- name: ReclaimExpiredCredentialLeases :execrows
UPDATE credential_leases
SET released_at = ?
WHERE released_at IS NULL
  AND expires_at <= ?;

-- name: UpsertCredentialUsage :exec
INSERT INTO credential_usage (
  credential_id,
  window_start,
  tokens,
  runs
)
VALUES (?, ?, ?, ?)
ON CONFLICT(credential_id, window_start) DO UPDATE SET
  tokens = credential_usage.tokens + excluded.tokens,
  runs = credential_usage.runs + excluded.runs;
