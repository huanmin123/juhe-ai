-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_policies (
  id text PRIMARY KEY,
  ip_hash text NOT NULL,
  policy_type text NOT NULL,
  status text NOT NULL,
  reason text,
  expires_at text,
  created_by_system_account_id text NOT NULL,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  disabled_at text,
  disabled_by_system_account_id text,
  disabled_reason text
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_client_ip_policies_active_unique
  ON juhe_stats.client_ip_policies(ip_hash)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_client_ip_policies_active
  ON juhe_stats.client_ip_policies(status, policy_type, ip_hash, expires_at);

-- +goose Down
-- no-op: client IP policies are shared operational configuration.
