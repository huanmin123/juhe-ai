import type { DatabaseSync } from 'node:sqlite'

export function applyBusinessSchema(database: DatabaseSync): void {
  database.exec(`
    PRAGMA foreign_keys = ON;
    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS system_accounts (
      id TEXT PRIMARY KEY,
      username TEXT NOT NULL UNIQUE,
      display_name TEXT NOT NULL,
      description TEXT,
      role TEXT NOT NULL DEFAULT 'user',
      status TEXT NOT NULL DEFAULT 'active',
      password_hash TEXT NOT NULL,
      must_change_password INTEGER NOT NULL DEFAULT 0,
      image_generation_enabled INTEGER NOT NULL DEFAULT 0,
      last_login_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS system_sessions (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      token_hash TEXT NOT NULL UNIQUE,
      expires_at TEXT NOT NULL,
      created_at TEXT NOT NULL,
      last_seen_at TEXT NOT NULL,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS global_settings (
      key TEXT PRIMARY KEY,
      value_json TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS request_quota_hourly_window_configs (
      window_hours INTEGER PRIMARY KEY,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      CHECK (window_hours BETWEEN 1 AND 720)
    );

    CREATE TABLE IF NOT EXISTS providers (
      id TEXT PRIMARY KEY,
      code TEXT NOT NULL UNIQUE,
      name TEXT NOT NULL,
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      base_url TEXT NOT NULL,
      account_types_json TEXT NOT NULL,
      capabilities_json TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS proxy_profiles (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      type TEXT NOT NULL,
      host TEXT NOT NULL,
      port INTEGER NOT NULL,
      username TEXT,
      password_encrypted TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      test_status TEXT NOT NULL DEFAULT 'unknown',
      latency_ms INTEGER,
      outbound_ip TEXT,
      outbound_region TEXT,
      last_test_message TEXT,
      last_tested_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS error_policies (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      rules_json TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS stream_intercept_policies (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      priority INTEGER NOT NULL DEFAULT 100,
      provider_code TEXT NOT NULL,
      match_json TEXT NOT NULL DEFAULT '{}',
      action TEXT NOT NULL DEFAULT 'avoid_account_ttl',
      avoidance_ttl_seconds INTEGER,
      notes TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS external_integration_sources (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      scopes_json TEXT NOT NULL DEFAULT '[]',
      allowed_target_usernames_json TEXT NOT NULL DEFAULT '[]',
      rate_limits_json TEXT NOT NULL DEFAULT '[]',
      expires_at TEXT,
      notes TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS external_integration_source_tokens (
      id TEXT PRIMARY KEY,
      source_ref_id TEXT NOT NULL,
      name TEXT NOT NULL,
      token_hash TEXT NOT NULL UNIQUE,
      token_prefix TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      scopes_json TEXT NOT NULL DEFAULT '[]',
      expires_at TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      revoked_at TEXT,
      FOREIGN KEY (source_ref_id) REFERENCES external_integration_sources(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS accounts (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      name TEXT NOT NULL,
      type TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      credentials_encrypted TEXT NOT NULL,
      credential_fingerprint TEXT,
      credential_mask TEXT NOT NULL DEFAULT '',
      oauth_access_token_expires_at TEXT,
      oauth_refresh_token_present INTEGER NOT NULL DEFAULT 0,
      proxy_profile_id TEXT,
      concurrency_limit INTEGER NOT NULL DEFAULT 20,
      error_policy_id TEXT,
      priority INTEGER NOT NULL DEFAULT 0,
      super_priority_enabled INTEGER NOT NULL DEFAULT 0,
      fallback_enabled INTEGER NOT NULL DEFAULT 0,
      schedulable INTEGER NOT NULL DEFAULT 1,
      availability_schedule_json TEXT,
      notes TEXT,
      account_expires_at TEXT,
      last_used_at TEXT,
      cooldown_until TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_last_at TEXT,
      cooldown_retest_last_status_code INTEGER,
      stream_failure_count INTEGER NOT NULL DEFAULT 0,
      stream_failure_window_started_at TEXT,
      authorization_instance_source_account_id TEXT,
      authorization_instance_authorization_id TEXT,
      authorization_instance_owner_system_account_id TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (proxy_profile_id) REFERENCES proxy_profiles(id),
      FOREIGN KEY (error_policy_id) REFERENCES error_policies(id),
      FOREIGN KEY (authorization_instance_source_account_id) REFERENCES accounts(id),
      FOREIGN KEY (authorization_instance_authorization_id) REFERENCES resource_authorizations(id)
    );

    CREATE TABLE IF NOT EXISTS account_supported_models (
      account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      model TEXT NOT NULL,
      created_at TEXT NOT NULL,
      PRIMARY KEY (account_id, model),
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS system_teams (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      description TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS system_team_members (
      id TEXT PRIMARY KEY,
      team_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      member_role TEXT NOT NULL DEFAULT 'member',
      status TEXT NOT NULL DEFAULT 'active',
      joined_at TEXT NOT NULL,
      removed_at TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (team_id) REFERENCES system_teams(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS resource_authorizations (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      grantee_system_account_id TEXT NOT NULL,
      scope TEXT NOT NULL DEFAULT 'use',
      status TEXT NOT NULL DEFAULT 'active',
      effective_source_type TEXT,
      effective_source_team_id TEXT,
      activated_at TEXT,
      last_source_changed_at TEXT,
      remark TEXT,
      expires_at TEXT,
      limits_json TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      revoked_reason TEXT,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (grantee_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS resource_authorization_sources (
      id TEXT PRIMARY KEY,
      authorization_id TEXT NOT NULL,
      source_type TEXT NOT NULL,
      source_team_id TEXT,
      status TEXT NOT NULL DEFAULT 'active',
      activated_at TEXT,
      ended_at TEXT,
      ended_reason TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (authorization_id) REFERENCES resource_authorizations(id) ON DELETE CASCADE,
      FOREIGN KEY (source_team_id) REFERENCES system_teams(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS resource_authorization_grants (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      grantee_type TEXT NOT NULL,
      grantee_system_account_id TEXT,
      grantee_team_id TEXT,
      scope TEXT NOT NULL DEFAULT 'use',
      status TEXT NOT NULL DEFAULT 'active',
      remark TEXT,
      expires_at TEXT,
      limits_json TEXT,
      created_by TEXT NOT NULL,
      created_at TEXT NOT NULL,
      revoked_by TEXT,
      revoked_at TEXT,
      updated_at TEXT NOT NULL,
      CHECK (
        (grantee_type = 'system_account' AND grantee_system_account_id IS NOT NULL AND grantee_team_id IS NULL)
        OR
        (grantee_type = 'team' AND grantee_team_id IS NOT NULL AND grantee_system_account_id IS NULL)
      ),
      FOREIGN KEY (grantee_system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (grantee_team_id) REFERENCES system_teams(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS groups (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      description TEXT,
      enabled INTEGER NOT NULL DEFAULT 1,
      is_default INTEGER NOT NULL DEFAULT 0,
      group_type TEXT NOT NULL DEFAULT 'personal',
      scheduling_policy_json TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (provider_code) REFERENCES providers(code)
    );

    CREATE TABLE IF NOT EXISTS group_accounts (
      system_account_id TEXT NOT NULL,
      group_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_authorization_id TEXT,
      local_priority INTEGER NOT NULL DEFAULT 0,
      local_super_priority_enabled INTEGER NOT NULL DEFAULT 0,
      local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
      enabled INTEGER NOT NULL DEFAULT 1,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (group_id, account_id),
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (account_authorization_id) REFERENCES resource_authorizations(id)
    );

    CREATE TABLE IF NOT EXISTS group_account_stats_dirty (
      group_id TEXT PRIMARY KEY,
      reason TEXT,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS external_integration_account_push_records (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      group_id TEXT NOT NULL,
      external_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE (system_account_id, provider_code, group_id, external_id),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (provider_code) REFERENCES providers(code),
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE,
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS api_keys (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL,
      description TEXT,
      key_hash TEXT NOT NULL UNIQUE,
      key_prefix TEXT NOT NULL,
      key_secret_encrypted TEXT NOT NULL,
      status TEXT NOT NULL DEFAULT 'active',
      group_route_strategy TEXT NOT NULL DEFAULT 'priority_failover',
      expires_at TEXT,
      quota_limits_json TEXT,
      availability_schedule_json TEXT,
      last_used_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
    );

    CREATE TABLE IF NOT EXISTS api_key_group_bindings (
      id TEXT PRIMARY KEY,
      api_key_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      group_id TEXT NOT NULL,
      priority INTEGER NOT NULL DEFAULT 1,
      weight INTEGER NOT NULL DEFAULT 1,
      status TEXT NOT NULL DEFAULT 'active',
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE,
      FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS system_settings (
      system_account_id TEXT NOT NULL,
      key TEXT NOT NULL,
      value_json TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (system_account_id, key),
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE TABLE IF NOT EXISTS announcements (
      id TEXT PRIMARY KEY,
      title TEXT NOT NULL,
      content TEXT NOT NULL,
      level TEXT NOT NULL DEFAULT 'info',
      status TEXT NOT NULL DEFAULT 'draft',
      created_by TEXT NOT NULL,
      updated_by TEXT,
      published_at TEXT,
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      FOREIGN KEY (created_by) REFERENCES system_accounts(id),
      FOREIGN KEY (updated_by) REFERENCES system_accounts(id)
    );

    CREATE TABLE IF NOT EXISTS announcement_reads (
      announcement_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      read_at TEXT NOT NULL,
      PRIMARY KEY (announcement_id, system_account_id),
      FOREIGN KEY (announcement_id) REFERENCES announcements(id) ON DELETE CASCADE,
      FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE
    );

    CREATE INDEX IF NOT EXISTS idx_accounts_provider_status ON accounts(provider_code, status);
    CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code);
    CREATE INDEX IF NOT EXISTS idx_system_sessions_expires_at ON system_sessions(expires_at);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username));
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_display_name_unique_lower ON system_accounts(lower(display_name));
    CREATE INDEX IF NOT EXISTS idx_stream_intercept_policies_enabled_priority ON stream_intercept_policies(enabled, priority, updated_at DESC, id);
    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_updated ON external_integration_sources(updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status_updated ON external_integration_sources(status, updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_name_lookup ON external_integration_sources(name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_external_integration_source_tokens_source ON external_integration_source_tokens(source_ref_id, status, expires_at);
    CREATE INDEX IF NOT EXISTS idx_system_accounts_updated_lookup ON system_accounts(updated_at, id);
    CREATE INDEX IF NOT EXISTS idx_system_accounts_username_lookup ON system_accounts(username COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_system_accounts_display_name_lookup ON system_accounts(display_name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint ON accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_provider_name_unique_lower ON accounts(system_account_id, provider_code, lower(name));
    CREATE INDEX IF NOT EXISTS idx_accounts_name_lookup ON accounts(name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_name_lookup ON accounts(system_account_id, name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_provider_lookup ON accounts(provider_code COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_provider_lookup ON accounts(system_account_id, provider_code COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_type_lookup ON accounts(type COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_type_lookup ON accounts(system_account_id, type COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account ON accounts(system_account_id);
    CREATE INDEX IF NOT EXISTS idx_accounts_proxy_profile ON accounts(proxy_profile_id, id);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_last_used ON accounts(system_account_id, last_used_at);
    CREATE INDEX IF NOT EXISTS idx_accounts_system_account_concurrency ON accounts(system_account_id, concurrency_limit);
    CREATE INDEX IF NOT EXISTS idx_accounts_expiry_sweep
      ON accounts(account_expires_at ASC, updated_at ASC, id ASC)
      WHERE account_expires_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_owner_expiry_sweep
      ON accounts(system_account_id, account_expires_at ASC, updated_at ASC, id ASC)
      WHERE account_expires_at IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_accounts_super_priority ON accounts(super_priority_enabled, status, priority);
    CREATE INDEX IF NOT EXISTS idx_accounts_dispatch_priority ON accounts(fallback_enabled, super_priority_enabled, status, priority);
    CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_due
      ON accounts(provider_code, type, oauth_refresh_token_present, oauth_access_token_expires_at, status, id);
    CREATE INDEX IF NOT EXISTS idx_account_supported_models_provider_model ON account_supported_models(provider_code, model, account_id);
    CREATE INDEX IF NOT EXISTS idx_groups_system_account ON groups(system_account_id);
    CREATE INDEX IF NOT EXISTS idx_groups_updated ON groups(updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_groups_system_account_updated ON groups(system_account_id, updated_at DESC, id DESC);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique_lower ON groups(system_account_id, provider_code, lower(name));
    CREATE INDEX IF NOT EXISTS idx_groups_name_lookup ON groups(name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_groups_system_account_name_lookup ON groups(system_account_id, name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_groups_provider_name_lookup ON groups(provider_code, name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_groups_system_account_provider_name_lookup ON groups(system_account_id, provider_code, name COLLATE NOCASE, id);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_default_unique ON groups(system_account_id, provider_code) WHERE is_default = 1;
    CREATE INDEX IF NOT EXISTS idx_system_teams_status ON system_teams(status, updated_at);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique ON system_teams(name);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique_lower ON system_teams(lower(name));
    CREATE INDEX IF NOT EXISTS idx_system_teams_name_lookup ON system_teams(name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_system_team_members_team ON system_team_members(team_id, status);
    CREATE INDEX IF NOT EXISTS idx_system_teams_list_order ON system_teams(status, updated_at DESC, name ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_system_team_members_team_status_joined ON system_team_members(team_id, status, joined_at ASC, id ASC);
    CREATE INDEX IF NOT EXISTS idx_system_team_members_account ON system_team_members(system_account_id, status);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_system_team_members_active_unique ON system_team_members(team_id, system_account_id) WHERE status = 'active';
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_resource ON resource_authorizations(resource_type, resource_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_owner ON resource_authorizations(resource_owner_system_account_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_grantee ON resource_authorizations(grantee_system_account_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_expires_at ON resource_authorizations(expires_at, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorizations_quota_snapshot
      ON resource_authorizations(status, updated_at DESC, id)
      WHERE limits_json IS NOT NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_authorization ON resource_authorization_sources(authorization_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_team ON resource_authorization_sources(source_team_id, status);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_account_authorization ON group_accounts(account_authorization_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_group_enabled ON group_accounts(group_id, enabled, account_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_account_scope_enabled ON group_accounts(account_id, system_account_id, enabled);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_scope_enabled_updated ON group_accounts(system_account_id, account_id, enabled, updated_at DESC);
    CREATE INDEX IF NOT EXISTS idx_group_account_stats_dirty_updated ON group_account_stats_dirty(updated_at);
    CREATE INDEX IF NOT EXISTS idx_external_account_push_records_account ON external_integration_account_push_records(account_id);
    CREATE INDEX IF NOT EXISTS idx_api_keys_system_account ON api_keys(system_account_id);
    CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_updated ON api_keys(system_account_id, updated_at DESC, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_api_keys_quota_snapshot
      ON api_keys(status, updated_at DESC, id)
      WHERE quota_limits_json IS NOT NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique_lower ON api_keys(system_account_id, lower(name));
    CREATE INDEX IF NOT EXISTS idx_api_keys_name_lookup ON api_keys(name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_lookup ON api_keys(system_account_id, name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_api_key_group_bindings_api_key_priority ON api_key_group_bindings(api_key_id, status, priority);
    CREATE INDEX IF NOT EXISTS idx_api_key_group_bindings_gateway_route
      ON api_key_group_bindings(api_key_id, system_account_id, priority ASC, created_at ASC, id ASC)
      WHERE status = 'active';
    CREATE INDEX IF NOT EXISTS idx_api_key_group_bindings_group ON api_key_group_bindings(group_id);
    CREATE INDEX IF NOT EXISTS idx_api_key_group_bindings_owner_key ON api_key_group_bindings(system_account_id, api_key_id);
    CREATE INDEX IF NOT EXISTS idx_api_key_group_bindings_owner_group_key ON api_key_group_bindings(system_account_id, group_id, api_key_id);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner ON resource_authorization_grants(resource_owner_system_account_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource ON resource_authorization_grants(resource_type, resource_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user ON resource_authorization_grants(grantee_system_account_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team ON resource_authorization_grants(grantee_team_id, status);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_created ON resource_authorization_grants(created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner_created ON resource_authorization_grants(resource_owner_system_account_id, status, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource_created ON resource_authorization_grants(resource_type, resource_id, status, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user_created ON resource_authorization_grants(grantee_system_account_id, status, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team_created ON resource_authorization_grants(grantee_team_id, status, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_team_quota_snapshot
      ON resource_authorization_grants(resource_type, resource_id, grantee_team_id, status, updated_at DESC, id)
      WHERE grantee_type = 'team' AND limits_json IS NOT NULL;
    CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_expiry_sweep
      ON resource_authorization_grants(expires_at ASC, updated_at ASC, id ASC)
      WHERE status IN ('active', 'paused') AND expires_at IS NOT NULL;
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_user_unique ON resource_authorization_grants(resource_type, resource_id, grantee_system_account_id) WHERE status = 'active' AND grantee_type = 'system_account';
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_team_unique ON resource_authorization_grants(resource_type, resource_id, grantee_team_id) WHERE status = 'active' AND grantee_type = 'team';
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_manual_unique ON resource_authorization_sources(authorization_id, source_type) WHERE status = 'active' AND source_type = 'manual';
    CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_team_unique ON resource_authorization_sources(authorization_id, source_type, source_team_id) WHERE status = 'active' AND source_type = 'team';
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_system_account ON proxy_profiles(system_account_id);
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_updated ON proxy_profiles(updated_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_enabled_name_lookup ON proxy_profiles(enabled, name COLLATE NOCASE, updated_at DESC, id ASC);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_profiles_name_unique_lower ON proxy_profiles(lower(name));
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_name_lookup ON proxy_profiles(name COLLATE NOCASE, id);
    CREATE INDEX IF NOT EXISTS idx_announcements_public ON announcements(status, published_at DESC, created_at DESC);
    CREATE INDEX IF NOT EXISTS idx_announcements_admin ON announcements(updated_at DESC, created_at DESC);
    CREATE INDEX IF NOT EXISTS idx_announcements_admin_page ON announcements(updated_at DESC, created_at DESC, id DESC);
    CREATE INDEX IF NOT EXISTS idx_announcement_reads_account ON announcement_reads(system_account_id, read_at DESC);
  `)
  ensureStreamInterceptPolicyIndexes(database)
  ensureExternalIntegrationSourceIndexes(database)
  ensureApiKeyGroupBindingUniqueIndexes(database)
  ensureAuthorizationInstanceIndexes(database)
}

function ensureExternalIntegrationSourceIndexes(database: DatabaseSync): void {
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status ON external_integration_sources(status, name);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_external_integration_sources_name_unique_lower ON external_integration_sources(lower(name));
  `)
}

function ensureStreamInterceptPolicyIndexes(database: DatabaseSync): void {
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_stream_intercept_policies_enabled_priority ON stream_intercept_policies(enabled, priority, updated_at DESC, id);
    CREATE INDEX IF NOT EXISTS idx_stream_intercept_policies_provider_priority ON stream_intercept_policies(provider_code, priority, updated_at DESC, id);
  `)
}

function ensureApiKeyGroupBindingUniqueIndexes(database: DatabaseSync): void {
  database.exec(`
    CREATE UNIQUE INDEX IF NOT EXISTS idx_api_key_group_bindings_key_group_unique ON api_key_group_bindings(api_key_id, group_id);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_api_key_group_bindings_active_priority_unique ON api_key_group_bindings(api_key_id, priority) WHERE status = 'active';
  `)
}

function ensureAuthorizationInstanceIndexes(database: DatabaseSync): void {
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_authorization ON accounts(authorization_instance_authorization_id);
    CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_source ON accounts(authorization_instance_source_account_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_priority ON group_accounts(group_id, enabled, local_fallback_enabled, local_super_priority_enabled, local_priority, created_at, account_id);
    CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC);
  `)
}
