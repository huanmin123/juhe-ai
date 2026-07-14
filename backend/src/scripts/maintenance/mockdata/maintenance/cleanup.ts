import { runtimeConfig } from '../../../../config/runtime.js'
import { modelCheckObservationHmac } from '../../../../modules/model-checks/model-checks-observation-security.js'
import {
  codexContextStateShardIndexes,
  getBusinessDatabase,
  getCodexContextStateShardDatabase,
  getUsageCatalogDatabase
} from '../../../../storage/database.js'
import { cleanupUnreferencedAuditPayloadBlobs } from '../../../../storage/repositories.js'
import {
  deleteUsageRecordShardEntries,
  getUsageRecordShardDatabase,
  listUsageRecordShardLocations
} from '../../../../storage/usage-record-shards.js'
import {
  chunks,
  idPrefix,
  namePrefix,
  tracePrefix,
  type SqlValue
} from '../shared.js'

type Database = ReturnType<typeof getBusinessDatabase>

export function cleanupMockdata(businessDatabase: Database, datasetDatabase: Database, statsDatabase: Database, adminId: string): void {
  const mockUserIds = selectIds(businessDatabase, "SELECT id FROM system_accounts WHERE username LIKE 'mockdata_%'")
  const mockAccountIds = selectIds(businessDatabase, 'SELECT id FROM accounts WHERE name LIKE ?', `${namePrefix}%`)
  const mockApiKeyIds = selectIds(businessDatabase, 'SELECT id FROM api_keys WHERE name LIKE ?', `${namePrefix}%`)
  cleanupDatasetMockdata(datasetDatabase, mockAccountIds, mockApiKeyIds)
  cleanupStatsMockdata(statsDatabase, mockAccountIds)
  cleanupCodexContextStateMockdata()
  cleanupBusinessMockdata(businessDatabase, adminId, mockUserIds)
}

function cleanupBusinessMockdata(database: Database, adminId: string, mockUserIds: string[]): void {
  const likeName = `${namePrefix}%`
  database.exec('BEGIN')
  try {
    const mockAnnouncementIds = selectIds(database, 'SELECT id FROM announcements WHERE title LIKE ?', likeName)
    deleteWhereIn(database, 'announcement_reads', 'announcement_id', mockAnnouncementIds)
    deleteWhereIn(database, 'announcements', 'id', mockAnnouncementIds)

    const mockExternalSourceIds = selectIds(database, 'SELECT id FROM external_integration_sources WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'external_integration_source_tokens', 'source_ref_id', mockExternalSourceIds)
    deleteWhereIn(database, 'external_integration_sources', 'id', mockExternalSourceIds)

    const mockResponseInspectionPolicyIds = selectIds(database, 'SELECT id FROM response_inspection_policies WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'response_inspection_policies', 'id', mockResponseInspectionPolicyIds)

    const mockCustomProviderModelIds = selectIds(database, 'SELECT id FROM custom_provider_models WHERE model LIKE ?', 'mockdata-%')
    database.prepare('DELETE FROM provider_system_default_health_check_models WHERE model LIKE ?').run('mockdata-%')
    deleteWhereIn(database, 'custom_provider_models', 'id', mockCustomProviderModelIds)

    const mockAccountTestTaskIds = selectIds(database, 'SELECT id FROM account_test_tasks WHERE id LIKE ? OR account_name LIKE ? OR status_message LIKE ?', `${idPrefix}%`, likeName, `${namePrefix}%`)
    const mockAccountTestSessionIds = selectIdsForChunks(database, mockAccountTestTaskIds, 'SELECT session_id AS id FROM account_test_session_tasks WHERE task_id IN ({placeholders})')
    deleteWhereIn(database, 'account_test_session_tasks', 'task_id', mockAccountTestTaskIds)
    deleteWhereIn(database, 'account_test_tasks', 'id', mockAccountTestTaskIds)
    deleteWhereIn(database, 'account_test_sessions', 'id', mockAccountTestSessionIds)

    database.prepare('DELETE FROM account_schedule_status_events WHERE event_key LIKE ?').run(`${idPrefix}%`)
    database.prepare('DELETE FROM api_key_schedule_status_events WHERE event_key LIKE ?').run(`${idPrefix}%`)

    const mockRuntimeAuthorizationIds = selectIds(database, 'SELECT id FROM resource_authorizations WHERE created_by = ? AND remark LIKE ?', adminId, likeName)
    deleteWhereIn(database, 'resource_authorization_sources', 'authorization_id', mockRuntimeAuthorizationIds)
    const mockGrantIds = selectIds(database, 'SELECT id FROM resource_authorization_grants WHERE created_by = ? AND remark LIKE ?', adminId, likeName)
    deleteWhereIn(database, 'resource_authorization_grants', 'id', mockGrantIds)

    const mockApiKeyIds = selectIds(database, 'SELECT id FROM api_keys WHERE name LIKE ?', likeName)
    const mockRouteStrategyIds = selectIdsForChunks(database, mockApiKeyIds, 'SELECT route_strategy_id FROM api_keys WHERE id IN ({placeholders})')
    const mockNamedRouteStrategyIds = selectIds(database, 'SELECT id FROM route_strategies WHERE name LIKE ?', likeName)
    const allMockRouteStrategyIds = uniqueIds([...mockRouteStrategyIds, ...mockNamedRouteStrategyIds])
    deleteWhereIn(database, 'api_keys', 'id', mockApiKeyIds)
    deleteWhereIn(database, 'route_strategy_groups', 'route_strategy_id', allMockRouteStrategyIds)
    deleteWhereIn(database, 'route_strategies', 'id', allMockRouteStrategyIds)

    const mockGroupIds = selectIds(database, 'SELECT id FROM groups WHERE name LIKE ?', likeName)
    const mockAccountIds = selectIds(database, 'SELECT id FROM accounts WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'group_accounts', 'group_id', mockGroupIds)
    deleteWhereIn(database, 'group_accounts', 'account_id', mockAccountIds)
    deleteWhereIn(database, 'accounts', 'id', mockAccountIds)
    deleteWhereIn(database, 'groups', 'id', mockGroupIds)

    const userGroupIds = selectIdsForChunks(database, mockUserIds, 'SELECT id FROM groups WHERE system_account_id IN ({placeholders})')
    const userApiKeyIds = selectIdsForChunks(database, mockUserIds, 'SELECT id FROM api_keys WHERE system_account_id IN ({placeholders})')
    const userRouteStrategyIds = selectIdsForChunks(database, userApiKeyIds, 'SELECT route_strategy_id FROM api_keys WHERE id IN ({placeholders})')
    deleteWhereIn(database, 'group_accounts', 'group_id', userGroupIds)
    deleteWhereIn(database, 'route_strategy_groups', 'group_id', userGroupIds)
    deleteWhereIn(database, 'api_keys', 'id', userApiKeyIds)
    deleteWhereIn(database, 'route_strategy_groups', 'route_strategy_id', userRouteStrategyIds)
    deleteWhereIn(database, 'route_strategies', 'id', userRouteStrategyIds)
    deleteWhereIn(database, 'groups', 'id', userGroupIds)

    deleteWhereIn(database, 'resource_authorizations', 'id', mockRuntimeAuthorizationIds)

    const mockTeamIds = selectIds(database, 'SELECT id FROM system_teams WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'system_team_members', 'team_id', mockTeamIds)
    deleteWhereIn(database, 'system_teams', 'id', mockTeamIds)

    const mockProxyIds = selectIds(database, 'SELECT id FROM proxy_profiles WHERE name LIKE ?', likeName)
    deleteWhereIn(database, 'proxy_profiles', 'id', mockProxyIds)

    deleteWhereIn(database, 'system_sessions', 'system_account_id', mockUserIds)
    database.prepare('DELETE FROM system_sessions WHERE id LIKE ?').run(`${idPrefix}%`)
    deleteWhereIn(database, 'system_accounts', 'id', mockUserIds)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function cleanupDatasetMockdata(database: Database, mockAccountIds: string[], mockApiKeyIds: string[]): void {
  database.exec('BEGIN')
  try {
    cleanupUsageRecordShardMockdata()

    deleteWhereIn(database, 'account_record_cleanup_targets', 'account_id', mockAccountIds)
    deleteWhereIn(database, 'api_key_record_cleanup_targets', 'api_key_id', mockApiKeyIds)

    database.prepare(`
      DELETE FROM public_api_logs
      WHERE id LIKE ?
         OR trace_id LIKE ?
         OR source_name LIKE ?
    `).run(`${idPrefix}%`, `${tracePrefix}%`, `${namePrefix}%`)

    database.prepare(`
      DELETE FROM account_record_cleanup_targets
      WHERE last_blocked_reason LIKE ?
         OR last_error_message LIKE ?
    `).run(`${namePrefix}%`, 'Mockdata%')
    database.prepare(`
      DELETE FROM api_key_record_cleanup_targets
      WHERE last_blocked_reason LIKE ?
         OR last_error_message LIKE ?
    `).run(`${namePrefix}%`, 'Mockdata%')

    database.prepare(`
      DELETE FROM audit_error_groups
      WHERE first_event_id LIKE ?
        OR last_event_id LIKE ?
        OR sample_event_id LIKE ?
        OR last_message LIKE ?
    `).run(`${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`, 'Mockdata%')

    const auditIds = selectIds(database, 'SELECT id FROM audit_logs WHERE id LIKE ? OR trace_id LIKE ?', `${idPrefix}%`, `${tracePrefix}%`)
    deleteWhereIn(database, 'audit_logs', 'id', auditIds)

    const operationIds = selectIds(database, 'SELECT id FROM operation_logs WHERE id LIKE ? OR trace_id LIKE ?', `${idPrefix}%`, `${tracePrefix}%`)
    deleteWhereIn(database, 'operation_logs', 'id', operationIds)

    const runtimeIds = selectIds(database, 'SELECT id FROM runtime_logs WHERE id LIKE ? OR trace_id LIKE ?', `${idPrefix}%`, `${tracePrefix}%`)
    deleteWhereIn(database, 'runtime_logs', 'id', runtimeIds)
    database.prepare('DELETE FROM runtime_log_file_cursors WHERE log_file LIKE ? OR file_identity LIKE ?').run(`${idPrefix}%`, `${tracePrefix}%`)

    const modelCheckRunIds = selectIds(database, 'SELECT id FROM model_check_runs WHERE id LIKE ? OR trace_id LIKE ?', `${idPrefix}%`, `${tracePrefix}%`)
    deleteWhereIn(database, 'model_check_items', 'run_id', modelCheckRunIds)
    deleteWhereIn(database, 'model_check_runs', 'id', modelCheckRunIds)
    database.prepare('DELETE FROM model_check_items WHERE id LIKE ? OR trace_id LIKE ?').run(`${idPrefix}%`, `${tracePrefix}%`)

    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
  cleanupUnreferencedAuditPayloadBlobs(10000)
}

function cleanupStatsMockdata(database: Database, mockAccountIds: string[]): void {
  database.exec('BEGIN')
  try {
    const mockClientIpPolicyIds = selectIds(database, 'SELECT id FROM client_ip_policies WHERE id LIKE ? OR reason LIKE ? OR disabled_reason LIKE ?', `${idPrefix}%`, `${namePrefix}%`, `${namePrefix}%`)
    deleteWhereIn(database, 'client_ip_policy_hits', 'policy_id', mockClientIpPolicyIds)
    deleteWhereIn(database, 'client_ip_policies', 'id', mockClientIpPolicyIds)
    deleteWhereIn(database, 'account_usage_snapshots', 'account_id', mockAccountIds)
    for (const tableName of [
      'model_token_integrity_windows',
      'model_token_integrity_rounds',
      'model_trust_window_sources',
      'model_identity_source_features',
      'model_paired_similarity_windows',
      'model_account_trust_results'
    ]) {
      deleteWhereIn(database, tableName, 'account_id', mockAccountIds)
    }
    database.prepare(`
      DELETE FROM model_identity_baseline_versions
      WHERE population_key_hmac = ?
    `).run(modelCheckObservationHmac('mockdata-model-trust-population', 'population'))
    database.prepare(`
      DELETE FROM stats_job_state
      WHERE job_name = 'model-trust-observation-aggregation'
    `).run()
    deleteWhereIn(database, 'account_quality_dirty_accounts', 'account_id', mockAccountIds)
    database.prepare(`
      DELETE FROM client_ip_range_window_dirty_ips
      WHERE ip_hash IN (
        SELECT ip_hash FROM client_ip_registry
        WHERE client_ip LIKE '10.10.%'
           OR client_ip LIKE '10.20.%'
      )
         OR ip_hash LIKE ?
    `).run(`${idPrefix}%`)
    database.prepare(`
      DELETE FROM client_ip_account_range_window_dirty_ips
      WHERE ip_hash IN (
        SELECT ip_hash FROM client_ip_registry
        WHERE client_ip LIKE '10.10.%'
           OR client_ip LIKE '10.20.%'
      )
         OR ip_hash LIKE ?
    `).run(`${idPrefix}%`)
    database.prepare('DELETE FROM usage_record_cleanup_deductions WHERE usage_id LIKE ? OR record_json LIKE ?').run(`${idPrefix}%`, `%${namePrefix}%`)
    database.prepare('DELETE FROM background_job_leases WHERE lease_key LIKE ? OR owner_id LIKE ? OR run_id LIKE ?').run(`${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`)
    database.prepare('DELETE FROM background_task_runs WHERE run_id LIKE ? OR lease_key LIKE ? OR owner_id LIKE ? OR params_json LIKE ? OR result_json LIKE ?').run(`${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`, `%${idPrefix}%`, `%${idPrefix}%`)
    database.prepare('DELETE FROM system_metrics_samples WHERE id LIKE ?').run(`${idPrefix}%`)
    database.prepare('DELETE FROM process_event_loop_samples WHERE id LIKE ?').run(`${idPrefix}%`)
    database.prepare('DELETE FROM database_storage_snapshots WHERE id LIKE ?').run(`${idPrefix}%`)
    database.prepare('DELETE FROM table_storage_snapshots WHERE id LIKE ?').run(`${idPrefix}%`)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function cleanupUsageRecordShardMockdata(): void {
  assertSqliteMockdataMaintenance('cleanupUsageRecordShardMockdata')
  const mockUsageIds = selectIds(
    getUsageCatalogDatabase(),
    'SELECT usage_id AS id FROM usage_record_shard_entries WHERE usage_id LIKE ?',
    `${idPrefix}%`
  )
  for (const location of listUsageRecordShardLocations()) {
    getUsageRecordShardDatabase(location)
      .prepare("DELETE FROM usage_records WHERE id LIKE ? OR trace_id LIKE ?")
      .run(`${idPrefix}%`, `${tracePrefix}%`)
  }
  deleteUsageRecordShardEntries(mockUsageIds)
}

function cleanupCodexContextStateMockdata(): void {
  assertSqliteMockdataMaintenance('cleanupCodexContextStateMockdata')
  for (const shardIndex of codexContextStateShardIndexes()) {
    const database = getCodexContextStateShardDatabase(shardIndex)
    database.exec('BEGIN')
    try {
      database.prepare(`
        DELETE FROM codex_context_compacts
        WHERE compact_id LIKE ?
           OR session_id LIKE ?
           OR source_response_id LIKE ?
           OR storage_key LIKE ?
      `).run(`${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`)
      database.prepare(`
        DELETE FROM codex_context_responses
        WHERE response_id LIKE ?
           OR session_id LIKE ?
           OR previous_response_id LIKE ?
           OR storage_key LIKE ?
      `).run(`${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`)
      database.prepare(`
        DELETE FROM codex_context_sessions
        WHERE id LIKE ?
           OR source_response_id LIKE ?
           OR latest_response_id LIKE ?
           OR latest_compact_id LIKE ?
      `).run(`${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`, `${idPrefix}%`)
      database.exec('COMMIT')
    } catch (error) {
      database.exec('ROLLBACK')
      throw error
    }
  }
}

function assertSqliteMockdataMaintenance(operation: string): void {
  if (runtimeConfig.databaseDriver === 'postgres' || runtimeConfig.runtimeMode === 'performance') {
    throw new Error(`高性能 PG + Redis 模式禁止调用 SQLite mockdata usage shard 清理入口：${operation}`)
  }
}

function selectIds(database: Database, sql: string, ...params: SqlValue[]): string[] {
  return (database.prepare(sql).all(...params) as unknown as Array<{ id?: string }>)
    .map((row) => row.id)
    .filter((id): id is string => Boolean(id))
}

function selectIdsForChunks(database: Database, ids: string[], sqlTemplate: string): string[] {
  const output = new Set<string>()
  for (const chunk of chunks(ids, 800)) {
    if (!chunk.length) continue
    const placeholders = chunk.map(() => '?').join(',')
    for (const id of selectIds(database, sqlTemplate.replace('{placeholders}', placeholders), ...chunk)) {
      output.add(id)
    }
  }
  return [...output]
}

function uniqueIds(ids: string[]): string[] {
  return [...new Set(ids.filter(Boolean))]
}

function deleteWhereIn(database: Database, tableName: string, columnName: string, ids: string[]): void {
  for (const chunk of chunks([...new Set(ids.filter(Boolean))], 800)) {
    if (!chunk.length) continue
    const placeholders = chunk.map(() => '?').join(',')
    database.prepare(`DELETE FROM ${tableName} WHERE ${columnName} IN (${placeholders})`).run(...chunk)
  }
}
