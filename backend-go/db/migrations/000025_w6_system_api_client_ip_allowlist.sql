-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_stats.client_ip_registry (
  ip_hash text PRIMARY KEY,
  bucket_no integer NOT NULL,
  aggregate_ip_key text NOT NULL,
  client_ip text NOT NULL,
  ip_version integer NOT NULL,
  first_seen_at text NOT NULL,
  last_seen_at text NOT NULL,
  created_at text NOT NULL,
  updated_at text NOT NULL
);

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

CREATE INDEX IF NOT EXISTS idx_client_ip_registry_bucket
  ON juhe_stats.client_ip_registry(bucket_no, ip_hash);

CREATE INDEX IF NOT EXISTS idx_client_ip_registry_last_seen
  ON juhe_stats.client_ip_registry(last_seen_at DESC, ip_hash);

CREATE INDEX IF NOT EXISTS idx_client_ip_registry_ip
  ON juhe_stats.client_ip_registry(aggregate_ip_key);

CREATE INDEX IF NOT EXISTS idx_client_ip_registry_client_ip
  ON juhe_stats.client_ip_registry(client_ip);

CREATE UNIQUE INDEX IF NOT EXISTS idx_client_ip_policies_active_unique
  ON juhe_stats.client_ip_policies(ip_hash)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_client_ip_policies_active
  ON juhe_stats.client_ip_policies(status, policy_type, ip_hash, expires_at);

-- +goose Down
-- no-op: client IP registry and policies are shared operational data.
