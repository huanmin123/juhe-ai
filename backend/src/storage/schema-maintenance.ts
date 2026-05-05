import type { DatabaseSync } from 'node:sqlite'

const ensuredColumns: Array<{ tableName: string; columnName: string; columnType: string }> = [
  { tableName: 'system_accounts', columnName: 'description', columnType: 'TEXT' },
  { tableName: 'providers', columnName: 'description', columnType: 'TEXT' },
  { tableName: 'proxy_profiles', columnName: 'description', columnType: 'TEXT' },
  { tableName: 'api_keys', columnName: 'description', columnType: 'TEXT' },
  { tableName: 'group_accounts', columnName: 'account_authorization_id', columnType: 'TEXT' },
  { tableName: 'api_keys', columnName: 'group_authorization_id', columnType: 'TEXT' }
]

const schemaIndexStatements = [
  'CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code);',
  'CREATE INDEX IF NOT EXISTS idx_group_account_stats_group ON group_account_stats(group_id);',
  'CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username));',
  'CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_display_name_unique_lower ON system_accounts(lower(display_name));',
  'CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint ON accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL;',
  'CREATE INDEX IF NOT EXISTS idx_accounts_system_account ON accounts(system_account_id);',
  'CREATE INDEX IF NOT EXISTS idx_accounts_system_account_last_used ON accounts(system_account_id, last_used_at);',
  'CREATE INDEX IF NOT EXISTS idx_accounts_system_account_concurrency ON accounts(system_account_id, concurrency_limit);',
  'CREATE INDEX IF NOT EXISTS idx_groups_system_account ON groups(system_account_id);',
  'CREATE INDEX IF NOT EXISTS idx_system_teams_status ON system_teams(status, updated_at);',
  'CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique ON system_teams(name);',
  'CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique_lower ON system_teams(lower(name));',
  'CREATE INDEX IF NOT EXISTS idx_system_team_members_team ON system_team_members(team_id, status);',
  'CREATE INDEX IF NOT EXISTS idx_system_team_members_account ON system_team_members(system_account_id, status);',
  "CREATE UNIQUE INDEX IF NOT EXISTS idx_system_team_members_active_unique ON system_team_members(team_id, system_account_id) WHERE status = 'active';",
  'CREATE INDEX IF NOT EXISTS idx_resource_authorizations_resource ON resource_authorizations(resource_type, resource_id, status);',
  'CREATE INDEX IF NOT EXISTS idx_resource_authorizations_owner ON resource_authorizations(resource_owner_system_account_id, status);',
  'CREATE INDEX IF NOT EXISTS idx_resource_authorizations_grantee ON resource_authorizations(grantee_system_account_id, status);',
  'CREATE INDEX IF NOT EXISTS idx_resource_authorizations_expires_at ON resource_authorizations(expires_at, status);',
  'CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id);',
  'CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_authorization ON resource_authorization_sources(authorization_id, status);',
  'CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_team ON resource_authorization_sources(source_team_id, status);',
  'CREATE INDEX IF NOT EXISTS idx_group_accounts_account_authorization ON group_accounts(account_authorization_id);',
  'CREATE INDEX IF NOT EXISTS idx_api_keys_group_authorization ON api_keys(group_authorization_id);',
  'CREATE INDEX IF NOT EXISTS idx_team_resource_authorization_grants_team ON team_resource_authorization_grants(team_id, status);',
  'CREATE INDEX IF NOT EXISTS idx_team_resource_authorization_grants_resource ON team_resource_authorization_grants(resource_type, resource_id, status);',
  'CREATE INDEX IF NOT EXISTS idx_team_resource_authorization_grants_owner ON team_resource_authorization_grants(resource_owner_system_account_id, status);',
  "CREATE UNIQUE INDEX IF NOT EXISTS idx_team_resource_authorization_grants_active_unique ON team_resource_authorization_grants(resource_type, resource_id, team_id) WHERE status = 'active';",
  "CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_manual_unique ON resource_authorization_sources(authorization_id, source_type) WHERE status = 'active' AND source_type = 'manual';",
  "CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_team_unique ON resource_authorization_sources(authorization_id, source_type, source_team_id) WHERE status = 'active' AND source_type = 'team';",
  'CREATE INDEX IF NOT EXISTS idx_api_keys_system_account ON api_keys(system_account_id);',
  'CREATE INDEX IF NOT EXISTS idx_proxy_profiles_system_account ON proxy_profiles(system_account_id);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_at ON usage_records(system_account_id, created_at);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_sort ON usage_records(system_account_id, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_account_owner ON usage_records(account_owner_system_account_id, account_id, created_at);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_group_owner ON usage_records(group_owner_system_account_id, group_id, created_at);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_account_authorization ON usage_records(account_authorization_id, created_at);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_group_authorization ON usage_records(group_authorization_id, created_at);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_first_token_sort ON usage_records(first_token_ms, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_duration_sort ON usage_records(duration_ms, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_cost_sort ON usage_records(cost_usd, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_first_token_sort ON usage_records(system_account_id, first_token_ms, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_duration_sort ON usage_records(system_account_id, duration_ms, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_cost_sort ON usage_records(system_account_id, cost_usd, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_trace_id ON audit_logs(trace_id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_system_account_created ON audit_logs(system_account_id, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_outcome_created ON audit_logs(audit_outcome, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_status_created ON audit_logs(final_status_code, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_path_created ON audit_logs(path, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_api_key_created ON audit_logs(api_key_id, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_group_created ON audit_logs(group_id, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_logs_account_created ON audit_logs(account_id, created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_audit_log_attempts_log_index ON audit_log_attempts(audit_log_id, attempt_index);',
  'CREATE INDEX IF NOT EXISTS idx_audit_log_payloads_log_part ON audit_log_payloads(audit_log_id, part_type, sequence_index);',
  'CREATE INDEX IF NOT EXISTS idx_audit_log_payloads_log_sequence ON audit_log_payloads(audit_log_id, sequence_index);',
  'CREATE INDEX IF NOT EXISTS idx_runtime_logs_time ON runtime_logs(time DESC, id DESC);',
  'CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_id_time ON runtime_logs(trace_id, time DESC, id DESC);',
  'CREATE INDEX IF NOT EXISTS idx_runtime_logs_level_time ON runtime_logs(level, time DESC, id DESC);',
  'CREATE INDEX IF NOT EXISTS idx_runtime_logs_event_time ON runtime_logs(event, time DESC, id DESC);',
  'CREATE INDEX IF NOT EXISTS idx_runtime_logs_created_at ON runtime_logs(created_at);',
  'CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_kind ON account_usage_snapshots(kind, updated_at);',
  'CREATE INDEX IF NOT EXISTS idx_usage_records_stats_cursor ON usage_records(created_at, id);',
  'CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_scope_date ON usage_stats_daily(system_account_id, scope_type, scope_id, stat_date);',
  'CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_date ON usage_stats_daily(stat_date);',
  'CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_scope_hour ON usage_stats_hourly(system_account_id, scope_type, scope_id, stat_hour);',
  'CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_hour ON usage_stats_hourly(stat_hour);',
  'CREATE INDEX IF NOT EXISTS idx_usage_model_daily_date ON usage_model_daily(system_account_id, stat_date, model);',
  'CREATE INDEX IF NOT EXISTS idx_usage_model_daily_stat_date ON usage_model_daily(stat_date);',
  'CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_model_daily_account_date_provider_model ON usage_model_daily(system_account_id, stat_date, provider_code, model);',
  'CREATE INDEX IF NOT EXISTS idx_usage_model_hourly_hour ON usage_model_hourly(system_account_id, stat_hour, model);',
  'CREATE INDEX IF NOT EXISTS idx_usage_model_hourly_stat_hour ON usage_model_hourly(stat_hour);',
  'CREATE INDEX IF NOT EXISTS idx_usage_error_daily_date ON usage_error_daily(system_account_id, stat_date, error_code);',
  'CREATE INDEX IF NOT EXISTS idx_usage_error_daily_stat_date ON usage_error_daily(stat_date);',
  'CREATE INDEX IF NOT EXISTS idx_usage_error_hourly_hour ON usage_error_hourly(system_account_id, stat_hour, error_code);',
  'CREATE INDEX IF NOT EXISTS idx_usage_error_hourly_stat_hour ON usage_error_hourly(stat_hour);',
  'CREATE INDEX IF NOT EXISTS idx_system_metrics_samples_sampled_at ON system_metrics_samples(sampled_at);'
]

export function ensureSchemaMaintenance(database: DatabaseSync): void {
  for (const statement of schemaIndexStatements.slice(0, 2)) {
    database.exec(statement)
  }

  for (const column of ensuredColumns) {
    ensureColumn(database, column.tableName, column.columnName, column.columnType)
  }

  for (const statement of schemaIndexStatements.slice(2)) {
    database.exec(statement)
  }
}

function ensureColumn(database: DatabaseSync, tableName: string, columnName: string, columnType: string): void {
  const rows = database.prepare(`PRAGMA table_info(${tableName})`).all() as unknown as Array<{ name?: string }>
  if (rows.some((row) => row.name === columnName)) {
    return
  }
  database.exec(`ALTER TABLE ${tableName} ADD COLUMN ${columnName} ${columnType}`)
}
