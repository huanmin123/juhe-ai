-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.system_sessions (
  id text PRIMARY KEY,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  token_hash text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_system_sessions_token_hash
  ON juhe_business.system_sessions(token_hash);
CREATE INDEX IF NOT EXISTS idx_system_sessions_expires_at
  ON juhe_business.system_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_system_sessions_account
  ON juhe_business.system_sessions(system_account_id);

-- +goose Down
-- no-op: system sessions are runtime authentication state.
