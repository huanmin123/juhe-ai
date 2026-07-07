-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.proxy_profiles (
  id text PRIMARY KEY,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  name text NOT NULL,
  description text,
  type text NOT NULL CHECK (type IN ('http', 'https', 'socks5', 'socks5h')),
  host text NOT NULL,
  port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
  username text,
  password_encrypted text,
  enabled boolean NOT NULL DEFAULT true,
  test_status text NOT NULL DEFAULT 'unknown' CHECK (test_status IN ('unknown', 'passed', 'warning', 'failed')),
  latency_ms integer CHECK (latency_ms IS NULL OR latency_ms >= 0),
  outbound_ip text,
  outbound_region text,
  last_test_message text,
  last_tested_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_proxy_profiles_system_account
  ON juhe_business.proxy_profiles(system_account_id);
CREATE INDEX IF NOT EXISTS idx_proxy_profiles_updated
  ON juhe_business.proxy_profiles(updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_proxy_profiles_enabled_name_lookup
  ON juhe_business.proxy_profiles(enabled, name COLLATE "C", updated_at DESC, id ASC);
CREATE INDEX IF NOT EXISTS idx_proxy_profiles_name_lookup
  ON juhe_business.proxy_profiles(name COLLATE "C", id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_profiles_name_unique
  ON juhe_business.proxy_profiles(name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_profiles_name_unique_lower
  ON juhe_business.proxy_profiles(lower(name));

-- +goose Down
-- no-op: W2 proxy profiles contain operational configuration.
