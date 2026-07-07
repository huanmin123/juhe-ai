-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.system_accounts (
  id text PRIMARY KEY,
  username text NOT NULL,
  display_name text NOT NULL,
  description text,
  role text NOT NULL DEFAULT 'user' CHECK (role IN ('super_admin', 'admin', 'user')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  password_hash text NOT NULL,
  must_change_password boolean NOT NULL DEFAULT false,
  image_generation_enabled boolean NOT NULL DEFAULT false,
  last_login_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower
  ON juhe_business.system_accounts (lower(username));
CREATE INDEX IF NOT EXISTS idx_system_accounts_updated_lookup
  ON juhe_business.system_accounts (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_system_accounts_username_lookup
  ON juhe_business.system_accounts (username COLLATE "C", id);
CREATE INDEX IF NOT EXISTS idx_system_accounts_display_name_lookup
  ON juhe_business.system_accounts (display_name COLLATE "C", id);

CREATE TABLE IF NOT EXISTS juhe_business.providers (
  id text PRIMARY KEY,
  code text NOT NULL UNIQUE,
  name text NOT NULL,
  description text,
  parent_code text REFERENCES juhe_business.providers(code),
  enabled boolean NOT NULL DEFAULT true,
  default_supported_models_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(default_supported_models_json::jsonb) = 'array'),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_business.groups (
  id text PRIMARY KEY,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  name text NOT NULL,
  provider_code text NOT NULL REFERENCES juhe_business.providers(code),
  description text,
  enabled boolean NOT NULL DEFAULT true,
  is_default boolean NOT NULL DEFAULT false,
  group_type text NOT NULL DEFAULT 'personal' CHECK (group_type IN ('personal', 'high_concurrency')),
  scheduling_policy_json text,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_groups_provider
  ON juhe_business.groups (provider_code);
CREATE INDEX IF NOT EXISTS idx_groups_system_account
  ON juhe_business.groups (system_account_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_id_owner_unique
  ON juhe_business.groups (id, system_account_id);
CREATE INDEX IF NOT EXISTS idx_groups_updated
  ON juhe_business.groups (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_groups_system_account_updated
  ON juhe_business.groups (system_account_id, updated_at DESC, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique
  ON juhe_business.groups (system_account_id, provider_code, name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique_lower
  ON juhe_business.groups (system_account_id, provider_code, lower(name));
CREATE INDEX IF NOT EXISTS idx_groups_name_lookup
  ON juhe_business.groups (name COLLATE "C", id);
CREATE INDEX IF NOT EXISTS idx_groups_system_account_name_lookup
  ON juhe_business.groups (system_account_id, name COLLATE "C", id);
CREATE INDEX IF NOT EXISTS idx_groups_provider_name_lookup
  ON juhe_business.groups (provider_code, name COLLATE "C", id);
CREATE INDEX IF NOT EXISTS idx_groups_system_account_provider_name_lookup
  ON juhe_business.groups (system_account_id, provider_code, name COLLATE "C", id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_default_unique
  ON juhe_business.groups (system_account_id, provider_code)
  WHERE is_default = true;

CREATE TABLE IF NOT EXISTS juhe_business.group_accounts (
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  group_id text NOT NULL,
  account_id text NOT NULL,
  account_authorization_id text,
  local_priority integer NOT NULL DEFAULT 0,
  local_super_priority_enabled boolean NOT NULL DEFAULT false,
  local_fallback_enabled boolean NOT NULL DEFAULT false,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (group_id, account_id),
  FOREIGN KEY (group_id, system_account_id)
    REFERENCES juhe_business.groups(id, system_account_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_group_accounts_owner_group_enabled
  ON juhe_business.group_accounts (system_account_id, group_id, enabled, account_id);
CREATE INDEX IF NOT EXISTS idx_group_accounts_group_enabled
  ON juhe_business.group_accounts (group_id, enabled, account_id);
CREATE INDEX IF NOT EXISTS idx_group_accounts_account_scope_enabled
  ON juhe_business.group_accounts (account_id, system_account_id, enabled);
CREATE INDEX IF NOT EXISTS idx_group_accounts_scope_enabled_updated
  ON juhe_business.group_accounts (system_account_id, account_id, enabled, updated_at DESC);

CREATE TABLE IF NOT EXISTS juhe_business.route_strategies (
  id text PRIMARY KEY,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  name text NOT NULL,
  description text,
  mode text NOT NULL DEFAULT 'normal' CHECK (mode IN ('normal', 'hybrid_smart', 'weighted', 'failover', 'round_robin')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  is_default boolean NOT NULL DEFAULT false,
  config_json text CHECK (config_json IS NULL OR jsonb_typeof(config_json::jsonb) = 'object'),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_route_strategies_owner_mode
  ON juhe_business.route_strategies (system_account_id, mode, status, updated_at DESC, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_id_owner_unique
  ON juhe_business.route_strategies (id, system_account_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_owner_name_unique
  ON juhe_business.route_strategies (system_account_id, name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_owner_name_unique_lower
  ON juhe_business.route_strategies (system_account_id, lower(name));
CREATE INDEX IF NOT EXISTS idx_route_strategies_owner_name_lookup
  ON juhe_business.route_strategies (system_account_id, name COLLATE "C", id);

CREATE TABLE IF NOT EXISTS juhe_business.route_strategy_groups (
  id text PRIMARY KEY,
  route_strategy_id text NOT NULL,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  group_id text NOT NULL,
  priority integer NOT NULL DEFAULT 1 CHECK (priority > 0),
  weight integer NOT NULL DEFAULT 1 CHECK (weight BETWEEN 1 AND 100),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  FOREIGN KEY (route_strategy_id, system_account_id)
    REFERENCES juhe_business.route_strategies(id, system_account_id) ON DELETE CASCADE,
  FOREIGN KEY (group_id, system_account_id)
    REFERENCES juhe_business.groups(id, system_account_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_strategy_priority
  ON juhe_business.route_strategy_groups (route_strategy_id, status, priority ASC, created_at ASC, id ASC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategy_groups_unique
  ON juhe_business.route_strategy_groups (route_strategy_id, group_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategy_groups_active_priority_unique
  ON juhe_business.route_strategy_groups (route_strategy_id, priority)
  WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_group_strategy
  ON juhe_business.route_strategy_groups (group_id, route_strategy_id);
CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_owner_group
  ON juhe_business.route_strategy_groups (system_account_id, group_id, route_strategy_id);

CREATE TABLE IF NOT EXISTS juhe_business.api_keys (
  id text PRIMARY KEY,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  route_strategy_id text NOT NULL,
  name text NOT NULL,
  description text,
  key_hash text NOT NULL,
  key_prefix text NOT NULL,
  key_suffix text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  is_default boolean NOT NULL DEFAULT false,
  expires_at timestamptz,
  quota_limits_json text CHECK (quota_limits_json IS NULL OR jsonb_typeof(quota_limits_json::jsonb) = 'object'),
  availability_schedule_json text CHECK (availability_schedule_json IS NULL OR jsonb_typeof(availability_schedule_json::jsonb) = 'object'),
  availability_schedule_next_check_at timestamptz,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  FOREIGN KEY (route_strategy_id, system_account_id)
    REFERENCES juhe_business.route_strategies(id, system_account_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_id_owner_unique
  ON juhe_business.api_keys (id, system_account_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_key_hash_unique
  ON juhe_business.api_keys (key_hash);
CREATE INDEX IF NOT EXISTS idx_api_keys_route_strategy
  ON juhe_business.api_keys (route_strategy_id, system_account_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique_lower
  ON juhe_business.api_keys (system_account_id, lower(name));
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_route_default_unique
  ON juhe_business.api_keys (route_strategy_id)
  WHERE is_default = true;
CREATE INDEX IF NOT EXISTS idx_api_keys_owner_default_updated
  ON juhe_business.api_keys (system_account_id, is_default DESC, updated_at DESC, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_api_keys_owner_status_updated
  ON juhe_business.api_keys (system_account_id, status, updated_at DESC, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_api_keys_owner_route_updated
  ON juhe_business.api_keys (system_account_id, route_strategy_id, updated_at DESC, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_api_keys_owner_name_lookup
  ON juhe_business.api_keys (system_account_id, name COLLATE "C", id);
CREATE INDEX IF NOT EXISTS idx_api_keys_quota_snapshot
  ON juhe_business.api_keys (status, updated_at DESC, id)
  WHERE quota_limits_json IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_api_keys_availability_schedule_next_check
  ON juhe_business.api_keys (availability_schedule_next_check_at ASC, id ASC)
  WHERE availability_schedule_json IS NOT NULL;

INSERT INTO juhe_business.providers (
  id, code, name, description, enabled, default_supported_models_json, created_at, updated_at
) VALUES
  ('provider_openai', 'openai', 'OpenAI', 'OpenAI provider', true, '[]', now(), now()),
  ('provider_gpt', 'gpt', 'OpenAI Compatible', 'OpenAI compatible provider', true, '[]', now(), now()),
  ('provider_deepseek', 'deepseek', 'DeepSeek', 'DeepSeek provider', true, '[]', now(), now()),
  ('provider_anthropic', 'anthropic', 'Anthropic', 'Anthropic provider', true, '[]', now(), now()),
  ('provider_gemini', 'gemini', 'Gemini', 'Google Gemini provider', true, '[]', now(), now()),
  ('provider_glm', 'glm', 'GLM', 'GLM provider', true, '[]', now(), now()),
  ('provider_hybrid', 'hybrid', 'Hybrid', 'Hybrid provider', true, '[]', now(), now())
ON CONFLICT (code) DO UPDATE SET
  name = EXCLUDED.name,
  enabled = EXCLUDED.enabled,
  updated_at = EXCLUDED.updated_at;

-- +goose Down
-- no-op: W1b public group tables are business data.
