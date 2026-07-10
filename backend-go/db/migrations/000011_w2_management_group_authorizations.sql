-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
  id text PRIMARY KEY,
  resource_type text NOT NULL CHECK (resource_type IN ('group', 'account')),
  resource_id text NOT NULL,
  resource_owner_system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  grantee_system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  scope text NOT NULL DEFAULT 'use' CHECK (scope = 'use'),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'expired', 'revoked', 'returned')),
  effective_source_type text CHECK (effective_source_type IS NULL OR effective_source_type IN ('manual', 'team')),
  effective_source_team_id text,
  activated_at timestamptz,
  last_source_changed_at timestamptz,
  remark text,
  expires_at timestamptz,
  limits_json text CHECK (limits_json IS NULL OR jsonb_typeof(limits_json::jsonb) = 'object'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  revoked_by text,
  revoked_at timestamptz,
  revoked_reason text,
  updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_business.group_authorization_settings (
  authorization_id text PRIMARY KEY REFERENCES juhe_business.resource_authorizations(id) ON DELETE CASCADE,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  group_id text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  group_type text NOT NULL DEFAULT 'personal' CHECK (group_type IN ('personal', 'high_concurrency')),
  scheduling_policy_json text CHECK (
    scheduling_policy_json IS NULL OR jsonb_typeof(scheduling_policy_json::jsonb) = 'object'
  ),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  FOREIGN KEY (group_id)
    REFERENCES juhe_business.groups(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_resource_authorizations_resource
  ON juhe_business.resource_authorizations(resource_type, resource_id, status);
CREATE INDEX IF NOT EXISTS idx_resource_authorizations_owner
  ON juhe_business.resource_authorizations(resource_owner_system_account_id, status);
CREATE INDEX IF NOT EXISTS idx_resource_authorizations_grantee
  ON juhe_business.resource_authorizations(grantee_system_account_id, resource_type, status, resource_id);
CREATE INDEX IF NOT EXISTS idx_resource_authorizations_expires_at
  ON juhe_business.resource_authorizations(expires_at, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique
  ON juhe_business.resource_authorizations(resource_type, resource_id, grantee_system_account_id);
CREATE INDEX IF NOT EXISTS idx_group_authorization_settings_scope_group
  ON juhe_business.group_authorization_settings(system_account_id, group_id);

-- +goose Down
-- no-op: resource authorizations are business data.
