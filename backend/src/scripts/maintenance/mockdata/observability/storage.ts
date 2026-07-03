import { runtimeConfig } from '../../../../config/runtime.js'
import { datasetDatabasePath, getBusinessDatabase, getDatasetDatabase, getStatsDatabase, getUsageCatalogDatabase, statsDatabasePath, usageCatalogDatabasePath } from '../../../../storage/database.js'
import {
  dayMs,
  idPrefix,
  minuteMs,
  tableStorageValues,
  type CreatedMockdata,
  type MockdataOptions
} from '../shared.js'

type Database = ReturnType<typeof getBusinessDatabase>

export function createStorageMockdata(_created: CreatedMockdata, options: MockdataOptions): void {
  const database = getStatsDatabase()
  const now = Date.now() - 10 * minuteMs
  const insertDatabase = database.prepare(`
    INSERT INTO database_storage_snapshots (
      id, database_role, database_path, sampled_at, file_bytes, wal_bytes, shm_bytes,
      page_size, page_count, freelist_count, used_bytes, free_bytes, table_count, index_count, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const insertTable = database.prepare(`
    INSERT INTO table_storage_snapshots (
      id, database_role, table_name, sampled_at, row_count, table_bytes, index_bytes, total_bytes,
      page_count, index_count, growth_bytes_1h, growth_rows_1h, growth_bytes_24h, growth_rows_24h, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const businessDatabase = getBusinessDatabase()
  const datasetDatabase = getDatasetDatabase()
  const usageCatalogDatabase = getUsageCatalogDatabase()
  const businessTables = [
    'accounts',
    'account_schedule_status_events',
    'account_test_sessions',
    'account_test_session_tasks',
    'account_test_tasks',
    'account_supported_models',
    'groups',
    'group_authorization_settings',
    'group_accounts',
    'api_keys',
    'api_key_schedule_status_events',
    'route_strategies',
    'route_strategy_groups',
    'openai_compatible_files',
    'openai_compatible_vector_stores',
    'openai_compatible_vector_store_files',
    'openai_compatible_vector_store_chunks',
    'provider_default_test_models',
    'proxy_profiles',
    'response_inspection_policies',
    'external_integration_sources',
    'external_integration_source_tokens',
    'resource_authorization_grants',
    'resource_authorizations',
    'resource_authorization_sources',
    'system_accounts',
    'system_sessions',
    'system_teams',
    'system_team_members',
    'announcements',
    'announcement_reads'
  ] as const
  const usageCatalogTables = [
    'usage_record_shards',
    'usage_record_shard_entries',
    'usage_record_account_shards',
    'usage_record_api_key_shards'
  ] as const
  const datasetTables = [
    'audit_logs',
    'audit_log_attempts',
    'audit_payload_refs',
    'audit_payload_blobs',
    'audit_error_groups',
    'operation_logs',
    'operation_log_targets',
    'operation_log_viewers',
    'operation_log_summary_search_terms',
    'runtime_logs',
    'runtime_log_event_facets',
    'runtime_log_level_facets',
    'runtime_log_facet_summary',
    'runtime_log_file_cursors',
    'public_api_logs',
    'model_check_runs',
    'model_check_items',
    'account_record_cleanup_targets',
    'api_key_record_cleanup_targets'
  ] as const
  const statsTables = [
    'usage_stats_daily',
    'usage_stats_hourly',
    'usage_stats_monthly',
    'usage_model_rank_windows',
    'usage_error_rank_windows',
    'usage_overview_summary_windows',
    'usage_scope_range_windows',
    'authorization_team_usage_range_windows',
    'authorization_user_usage_range_windows',
    'ai_performance_summary_windows',
    'usage_quota_hourly_windows',
    'account_quality_dirty_accounts',
    'account_quality_scores',
    'account_quality_minute_stats',
    'background_task_runs',
    'background_job_leases',
    'client_ip_registry',
    'client_ip_stats_daily',
    'client_ip_usage_range_windows',
    'client_ip_range_window_dirty_ips',
    'client_ip_account_stats_daily',
    'client_ip_account_usage_range_windows',
    'client_ip_account_range_window_dirty_ips',
    'client_ip_policies',
    'client_ip_policy_hits',
    'usage_record_cleanup_deductions',
    'system_metrics_samples',
    'system_metrics_hourly',
    'process_event_loop_samples',
    'process_event_loop_hourly'
  ] as const
  const databaseTargets = [
    { role: 'business', path: runtimeConfig.databasePath, baseBytes: 80_000_000, growthBytes: 1_200_000, tableCount: 20, indexCount: 38 },
    { role: 'usage-catalog', path: usageCatalogDatabasePath(), baseBytes: 120_000_000, growthBytes: 4_500_000, tableCount: 4, indexCount: 29 },
    { role: 'dataset', path: datasetDatabasePath(), baseBytes: 220_000_000, growthBytes: 7_000_000, tableCount: 24, indexCount: 48 },
    { role: 'stats', path: statsDatabasePath(), baseBytes: 90_000_000, growthBytes: 1_500_000, tableCount: 36, indexCount: 62 }
  ] as const

  database.exec('BEGIN')
  try {
    for (let dayIndex = 0; dayIndex < options.days; dayIndex += 1) {
      const sampledAt = new Date(now - (options.days - dayIndex - 1) * dayMs).toISOString()
      for (const target of databaseTargets) {
        const fileBytes = target.baseBytes + dayIndex * target.growthBytes
        insertDatabase.run(
          `${idPrefix}storage_db_${target.role}_${String(dayIndex + 1).padStart(2, '0')}`,
          target.role,
          target.path,
          sampledAt,
          fileBytes,
          Math.floor(fileBytes * 0.08),
          32768,
          4096,
          Math.ceil(fileBytes / 4096),
          128 + dayIndex,
          Math.floor(fileBytes * 0.82),
          Math.floor(fileBytes * 0.18),
          target.tableCount,
          target.indexCount,
          sampledAt
        )
      }
      for (const tableName of businessTables) {
        const baseRows = tableRowCount(businessDatabase, tableName)
        const rows = baseRows + dayIndex * 2
        insertTable.run(...tableStorageValues('business', tableName, dayIndex, sampledAt, rows, 24_000 + rows * 900))
      }
      for (const tableName of datasetTables) {
        const baseRows = tableRowCount(datasetDatabase, tableName)
        const rows = baseRows + dayIndex * 12
        insertTable.run(...tableStorageValues('dataset', tableName, dayIndex, sampledAt, rows, 80_000 + rows * 1100))
      }
      for (const tableName of usageCatalogTables) {
        const baseRows = tableRowCount(usageCatalogDatabase, tableName)
        const rows = baseRows + dayIndex * 12
        insertTable.run(...tableStorageValues('usage-catalog', tableName, dayIndex, sampledAt, rows, 80_000 + rows * 1100))
      }
      for (const tableName of statsTables) {
        const baseRows = tableRowCount(database, tableName)
        const rows = baseRows + dayIndex * 4
        insertTable.run(...tableStorageValues('stats', tableName, dayIndex, sampledAt, rows, 60_000 + rows * 700))
      }
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function tableRowCount(database: Database, tableName: string): number {
  const row = database.prepare(`SELECT COUNT(*) AS total FROM ${tableName}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
