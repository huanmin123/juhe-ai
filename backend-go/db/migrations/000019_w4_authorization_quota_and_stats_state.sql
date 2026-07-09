-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.request_quota_hourly_window_configs (
  window_hours integer PRIMARY KEY CHECK (window_hours BETWEEN 1 AND 720),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

INSERT INTO juhe_business.request_quota_hourly_window_configs (window_hours, created_at, updated_at)
VALUES
  (1, NOW(), NOW()),
  (3, NOW(), NOW()),
  (6, NOW(), NOW()),
  (12, NOW(), NOW()),
  (24, NOW(), NOW()),
  (72, NOW(), NOW()),
  (168, NOW(), NOW()),
  (720, NOW(), NOW())
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
  group_id text PRIMARY KEY,
  reason text,
  updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_group_account_stats_dirty_updated
  ON juhe_business.group_account_stats_dirty(updated_at);

-- +goose Down
-- no-op: quota windows and dirty markers are runtime state.
