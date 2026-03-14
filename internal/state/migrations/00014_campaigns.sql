-- +goose Up
CREATE TABLE IF NOT EXISTS campaigns (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  max_concurrent INTEGER NOT NULL DEFAULT 1,
  stop_after_failures INTEGER NOT NULL DEFAULT 1,
  continue_on_failure INTEGER NOT NULL DEFAULT 0,
  skip_if_open_pr INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  started_at INTEGER,
  completed_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_campaigns_state_updated ON campaigns (state, updated_at DESC);

CREATE TABLE IF NOT EXISTS campaign_items (
  id TEXT PRIMARY KEY,
  campaign_id TEXT NOT NULL,
  item_order INTEGER NOT NULL,
  repo TEXT NOT NULL,
  task TEXT NOT NULL,
  task_id TEXT NOT NULL DEFAULT '',
  base_branch TEXT NOT NULL DEFAULT '',
  backend_override TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  skip_reason TEXT NOT NULL DEFAULT '',
  failure_reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  FOREIGN KEY(campaign_id) REFERENCES campaigns(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_campaign_items_campaign_order ON campaign_items (campaign_id, item_order);
CREATE INDEX IF NOT EXISTS idx_campaign_items_campaign_state ON campaign_items (campaign_id, state, item_order ASC);
CREATE INDEX IF NOT EXISTS idx_campaign_items_run_id ON campaign_items (run_id);

-- +goose Down
DROP TABLE IF EXISTS campaign_items;
DROP TABLE IF EXISTS campaigns;
