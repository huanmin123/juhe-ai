-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.global_settings (
  key text PRIMARY KEY,
  value_json text NOT NULL,
  updated_at timestamptz NOT NULL
);

INSERT INTO juhe_business.global_settings (key, value_json, updated_at)
VALUES
  ('appName', '"聚合 AI"', now()),
  ('appIcon', '"/__aisys__/brand-icon.svg"', now())
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id text NOT NULL,
  key text NOT NULL,
  value_json text NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (system_account_id, key)
);

INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
VALUES
  ('sys_admin', 'systemApiRateLimitIpReadPerMinute', '600', now()),
  ('sys_admin', 'systemApiRateLimitIpReadBurstPer10Seconds', '120', now())
ON CONFLICT (system_account_id, key) DO NOTHING;

-- +goose Down
-- no-op: global_settings and system_settings are existing business tables in migrated PostgreSQL deployments.
