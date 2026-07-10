-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_sources (
  id text PRIMARY KEY,
  authorization_id text NOT NULL REFERENCES juhe_business.resource_authorizations(id) ON DELETE CASCADE,
  source_type text NOT NULL CHECK (source_type IN ('manual', 'team')),
  source_team_id text REFERENCES juhe_business.system_teams(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'superseded', 'revoked')),
  activated_at timestamptz,
  ended_at timestamptz,
  ended_reason text,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  revoked_by text,
  revoked_at timestamptz,
  updated_at timestamptz NOT NULL,
  CHECK (
    (source_type = 'manual' AND source_team_id IS NULL)
    OR
    (source_type = 'team' AND source_team_id IS NOT NULL)
  )
);

CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_grants (
  id text PRIMARY KEY,
  resource_type text NOT NULL CHECK (resource_type IN ('group', 'account')),
  resource_id text NOT NULL,
  resource_owner_system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  grantee_type text NOT NULL CHECK (grantee_type IN ('system_account', 'team')),
  grantee_system_account_id text REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  grantee_team_id text REFERENCES juhe_business.system_teams(id) ON DELETE CASCADE,
  scope text NOT NULL DEFAULT 'use' CHECK (scope = 'use'),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'expired', 'revoked', 'returned')),
  remark text,
  expires_at timestamptz,
  limits_json text CHECK (limits_json IS NULL OR jsonb_typeof(limits_json::jsonb) = 'object'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  revoked_by text,
  revoked_at timestamptz,
  updated_at timestamptz NOT NULL,
  CHECK (
    (grantee_type = 'system_account' AND grantee_system_account_id IS NOT NULL AND grantee_team_id IS NULL)
    OR
    (grantee_type = 'team' AND grantee_team_id IS NOT NULL AND grantee_system_account_id IS NULL)
  )
);

CREATE INDEX IF NOT EXISTS idx_resource_authorizations_quota_snapshot
  ON juhe_business.resource_authorizations(status, updated_at DESC, id)
  WHERE limits_json IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_authorization
  ON juhe_business.resource_authorization_sources(authorization_id, status);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_team
  ON juhe_business.resource_authorization_sources(source_team_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_manual_unique
  ON juhe_business.resource_authorization_sources(authorization_id, source_type)
  WHERE status = 'active' AND source_type = 'manual';
CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_team_unique
  ON juhe_business.resource_authorization_sources(authorization_id, source_type, source_team_id)
  WHERE status = 'active' AND source_type = 'team';

CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner
  ON juhe_business.resource_authorization_grants(resource_owner_system_account_id, status);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource
  ON juhe_business.resource_authorization_grants(resource_type, resource_id, status);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user
  ON juhe_business.resource_authorization_grants(grantee_system_account_id, status);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team
  ON juhe_business.resource_authorization_grants(grantee_team_id, status);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_created
  ON juhe_business.resource_authorization_grants(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner_created
  ON juhe_business.resource_authorization_grants(resource_owner_system_account_id, status, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource_created
  ON juhe_business.resource_authorization_grants(resource_type, resource_id, status, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user_created
  ON juhe_business.resource_authorization_grants(grantee_system_account_id, status, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team_created
  ON juhe_business.resource_authorization_grants(grantee_team_id, status, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_team_quota_snapshot
  ON juhe_business.resource_authorization_grants(resource_type, resource_id, grantee_team_id, status, updated_at DESC, id)
  WHERE grantee_type = 'team' AND limits_json IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_expiry_sweep
  ON juhe_business.resource_authorization_grants(expires_at ASC, updated_at ASC, id ASC)
  WHERE status IN ('active', 'paused') AND expires_at IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_user_unique
  ON juhe_business.resource_authorization_grants(resource_type, resource_id, grantee_system_account_id)
  WHERE status = 'active' AND grantee_type = 'system_account';
CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_team_unique
  ON juhe_business.resource_authorization_grants(resource_type, resource_id, grantee_team_id)
  WHERE status = 'active' AND grantee_type = 'team';

-- +goose Down
-- no-op: authorization sources and grants are business data.
