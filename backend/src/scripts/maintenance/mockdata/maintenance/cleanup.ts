import { getBusinessDatabase, getUsageCatalogDatabase } from '../../../../storage/database.js'
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
    deleteWhereIn(database, 'custom_provider_models', 'id', mockCustomProviderModelIds)

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
