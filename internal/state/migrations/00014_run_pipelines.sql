-- +goose Up
CREATE TABLE IF NOT EXISTS run_pipelines (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL UNIQUE,
  repo TEXT NOT NULL,
  task TEXT NOT NULL,
  base_branch TEXT NOT NULL,
  head_branch TEXT NOT NULL,
  trigger TEXT NOT NULL,
  issue_number INTEGER NOT NULL DEFAULT 0,
  pr_number INTEGER NOT NULL DEFAULT 0,
  context TEXT NOT NULL DEFAULT '',
  debug BOOLEAN NOT NULL DEFAULT 1,
  created_by_user_id TEXT NOT NULL DEFAULT '',
  artifact_dir TEXT NOT NULL,
  status TEXT NOT NULL,
  active_phase TEXT NOT NULL DEFAULT '',
  failed_phase TEXT NOT NULL DEFAULT '',
  cancel_requested BOOLEAN NOT NULL DEFAULT 0,
  max_phases INTEGER NOT NULL,
  max_child_runs_per_phase INTEGER NOT NULL,
  total_child_runs INTEGER NOT NULL DEFAULT 0,
  token_budget_total INTEGER NOT NULL DEFAULT 0,
  token_budget_used INTEGER NOT NULL DEFAULT 0,
  deadline_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  completed_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_run_pipelines_status_created ON run_pipelines (status, created_at ASC);

CREATE TABLE IF NOT EXISTS run_pipeline_phases (
  pipeline_id TEXT NOT NULL,
  phase_name TEXT NOT NULL,
  phase_order INTEGER NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT 1,
  state TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  child_index INTEGER NOT NULL DEFAULT 0,
  artifact_paths TEXT NOT NULL DEFAULT '[]',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  started_at INTEGER,
  completed_at INTEGER,
  PRIMARY KEY (pipeline_id, phase_name),
  FOREIGN KEY(pipeline_id) REFERENCES run_pipelines(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_pipeline_phases_pipeline_order ON run_pipeline_phases (pipeline_id, phase_order ASC);
CREATE INDEX IF NOT EXISTS idx_run_pipeline_phases_run_id ON run_pipeline_phases (run_id);

CREATE TABLE IF NOT EXISTS run_lineage (
  run_id TEXT PRIMARY KEY,
  parent_pipeline_id TEXT NOT NULL,
  phase_name TEXT NOT NULL,
  phase_order INTEGER NOT NULL,
  child_index INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  FOREIGN KEY(parent_pipeline_id) REFERENCES run_pipelines(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_run_lineage_parent_phase ON run_lineage (parent_pipeline_id, phase_order ASC, child_index ASC);

-- +goose Down
DROP INDEX IF EXISTS idx_run_lineage_parent_phase;
DROP TABLE IF EXISTS run_lineage;
DROP INDEX IF EXISTS idx_run_pipeline_phases_run_id;
DROP INDEX IF EXISTS idx_run_pipeline_phases_pipeline_order;
DROP TABLE IF EXISTS run_pipeline_phases;
DROP INDEX IF EXISTS idx_run_pipelines_status_created;
DROP TABLE IF EXISTS run_pipelines;
