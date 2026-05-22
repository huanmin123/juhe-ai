import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-deleted-api-key-aggregation-cleanup-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'deleted-api-key-aggregation-cleanup-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsRepository, apiKeyRecordCleanup] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/api-key-record-cleanup.js')
])

const apiKeyId = 'key_deleted_before_aggregation'
const usageRecordId = 'usage_deleted_key_before_aggregation'
const baseCreatedAtMs = Date.now() - 2 * 24 * 60 * 60 * 1000
const createdAt = new Date(baseCreatedAtMs).toISOString()
const createdDate = createdAt.slice(0, 10)
const fallbackCreatedAt = new Date(baseCreatedAtMs + 60 * 60 * 1000).toISOString()
const pendingCreatedAt = new Date(baseCreatedAtMs + 2 * 60 * 60 * 1000).toISOString()
const largeCreatedAt = new Date(baseCreatedAtMs + 3 * 60 * 60 * 1000).toISOString()

try {
  seedUsageRecord(usageRecordId, apiKeyId, createdAt)
  seedAuditLog('audit_deleted_key_before_aggregation', apiKeyId, createdAt)
  seedAuditErrorGroup('audit_group_deleted_key_before_aggregation', apiKeyId, createdAt)

  const processed = usageStatsRepository.aggregateUsageStatsBatch(10)
  assert.equal(processed, 1, '删除后的 API Key 历史使用记录仍应作为事实参与聚合')
  usageStatsRepository.refreshUsageQuotaHourlyWindowsCache()
  usageStatsRepository.refreshUsageRankSnapshots()
  assert.equal(
    usageStatsTotal('sys_admin', 'system_account', 'sys_admin'),
    1,
    '系统账户统计应包含删除前已产生但删除后才聚合的使用记录'
  )
  assert.equal(
    usageStatsTotal('sys_admin', 'api_key', apiKeyId),
    1,
    'API Key 维度统计可临时存在，后续删除清理会负责扣减'
  )
  seedAuthorizationUsageRangeWindow('owner_deleted_key')
  assert.equal(usageOverviewSummaryRequestCount('sys_admin'), 1, '删除前概览窗口应包含这把 Key 的历史消耗')
  assert.equal(usageScopeRangeWindowRequestCount('sys_admin', 'api_key', apiKeyId), 1, '删除前账户用量范围窗口应包含 API Key 维度消耗')
  assert.equal(usageQuotaHourlyWindowCost('sys_admin', 'api_key', apiKeyId), 0.12, '删除前额度小时窗口应包含 API Key 成本')
  assert.equal(usageRankSnapshotMetric('sys_admin', 'api_key', apiKeyId), 0.12, '删除前 API Key 排行快照应包含 API Key 成本')
  assert.equal(authorizationUserUsageRangeWindowRequestCount('owner_deleted_key'), 1, '删除前授权用户范围窗口应可见历史消耗')

  apiKeyRecordCleanup.cleanupDeletedApiKeyRelatedRecordData({
    apiKeyId,
    systemAccountId: 'sys_admin'
  })

  assert.equal(usageRecordExists(usageRecordId), false, 'API Key 删除清理应删除关联使用记录')
  assert.equal(auditLogExists('audit_deleted_key_before_aggregation'), false, 'API Key 删除清理应删除关联原始审计日志')
  assert.equal(auditErrorGroupExists('audit_group_deleted_key_before_aggregation'), false, 'API Key 删除清理应删除关联审计错误组')
  assert.equal(usageStatsTotal('sys_admin', 'system_account', 'sys_admin'), 0, '清理已聚合使用记录时应反向扣减系统账户统计')
  assert.equal(usageStatsTotal('sys_admin', 'api_key', apiKeyId), 0, '清理后不应残留 API Key 维度统计')
  assert.equal(usageOverviewSummaryRequestCount('sys_admin'), 0, '清理完成后概览窗口不应残留这把 Key 的历史消耗')
  assert.equal(usageScopeRangeWindowRequestCount('sys_admin', 'api_key', apiKeyId), 0, '清理完成后账户用量范围窗口不应残留 API Key 维度消耗')
  assert.equal(usageQuotaHourlyWindowCost('sys_admin', 'api_key', apiKeyId), 0, '清理完成后额度小时窗口不应残留 API Key 成本')
  assert.equal(usageRankSnapshotMetric('sys_admin', 'api_key', apiKeyId), 0, '清理完成后 API Key 排行快照不应残留 API Key 成本')
  assert.equal(authorizationUserUsageRangeWindowRequestCount('owner_deleted_key'), 0, '清理完成后授权用户范围窗口不应残留旧授权消耗')

  seedUsageRecord('usage_deleted_key_after_queue_full', 'key_deleted_after_queue_full', fallbackCreatedAt)
  usageStatsRepository.aggregateUsageStatsBatch(10)
  runtimeConfig.processRole = 'server'
  const recordMaintenanceQueue = await import('../../modules/record-maintenance/record-maintenance-queue.service.js')
  const apiKeyCleanupService = await import('../../modules/api-keys/api-key-cleanup.service.js')
  for (let index = 0; index < 5000; index += 1) {
    const queued = recordMaintenanceQueue.enqueueRecordMaintenanceJobWithResult({
      type: 'usage_records_cleanup',
      id: `recmaint_fill_${index}`,
      cutoffAt: '2000-01-01T00:00:00.000Z',
      batchSize: 100,
      maxBatches: 1
    })
    assert.equal(queued.queued, true, `背景队列填充任务 ${index} 应保持可入队`)
  }

  const fallbackResult = apiKeyCleanupService.submitApiKeyRelatedCleanup({
    apiKeyId: 'key_deleted_after_queue_full',
    systemAccountId: 'sys_admin'
  })
  assert.equal(fallbackResult.queued, false, '队列满时 API Key 关联清理应显式知道未投递 worker')
  assert.equal(usageRecordExists('usage_deleted_key_after_queue_full'), true, '队列满时应保留关联使用记录等待后台维护任务')
  assert.equal(cleanupTargetExists('key_deleted_after_queue_full'), true, '队列满时应持久登记清理目标，等待后台重试')
  assert.equal(usageStatsTotal('sys_admin', 'system_account', 'sys_admin'), 1, '队列满时不应在请求链路同步扣减统计聚合')
  assert.equal(usageStatsTotal('sys_admin', 'api_key', 'key_deleted_after_queue_full'), 1, '队列满时 API Key 维度统计应等待后台清理扣减')
  apiKeyRecordCleanup.cleanupDeletedApiKeyRelatedRecordData({
    apiKeyId: 'key_deleted_after_queue_full',
    systemAccountId: 'sys_admin'
  })
  assert.equal(usageRecordExists('usage_deleted_key_after_queue_full'), false, '后台重试清理应删除关联使用记录')
  assert.equal(cleanupTargetExists('key_deleted_after_queue_full'), false, '后台重试清理完成后应移除清理目标')

  const pendingApiKeyId = 'key_deleted_cleanup_pending_cursor'
  const pendingUsageRecordId = 'usage_deleted_key_pending_cursor'
  seedUsageRecord(pendingUsageRecordId, pendingApiKeyId, pendingCreatedAt)
  seedAuditLog('audit_deleted_key_pending_cursor', pendingApiKeyId, pendingCreatedAt)
  const pendingResult = apiKeyRecordCleanup.cleanupDeletedApiKeyRelatedRecordData({
    apiKeyId: pendingApiKeyId,
    systemAccountId: 'sys_admin'
  })
  assert.equal(pendingResult.hasMore, true, '统计安全游标尚未覆盖时清理应留下待重试标记')
  assert.equal(usageRecordExists(pendingUsageRecordId), true, '统计安全游标尚未覆盖的记录不能被 API Key 清理提前删除')
  assert.equal(auditLogExists('audit_deleted_key_pending_cursor'), false, '原始审计日志不依赖统计安全游标，应先按批次删除')
  assert.equal(cleanupTargetExists(pendingApiKeyId), true, '未完成的 API Key 清理目标应持久登记，等待 worker 后续重试')

  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(10), 1, '待清理记录应先被统计聚合处理')
  const retrySummary = apiKeyRecordCleanup.cleanupPendingDeletedApiKeyRecordTargets(10)
  assert.equal(retrySummary.completed, 1, '统计游标追平后持久待清理目标应可完成')
  assert.equal(usageRecordExists(pendingUsageRecordId), false, '统计游标追平后待重试清理应删除关联使用记录')
  assert.equal(cleanupTargetExists(pendingApiKeyId), false, '清理完成后应移除待重试标记')
  assert.equal(usageStatsTotal('sys_admin', 'system_account', 'sys_admin'), 0, '待重试清理完成后应反向扣减系统账户统计')
  assert.equal(usageStatsTotal('sys_admin', 'api_key', pendingApiKeyId), 0, '待重试清理完成后应反向扣减 API Key 维度统计')

  const largeApiKeyId = 'key_deleted_cleanup_large_batch'
  for (let index = 0; index < 1200; index += 1) {
    seedUsageRecord(
      `usage_deleted_key_large_${String(index).padStart(4, '0')}`,
      largeApiKeyId,
      largeCreatedAt
    )
  }
  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(2000), 1200, '大批量待清理记录应先全部完成统计聚合')
  assert.equal(
    usageStatsTotal('sys_admin', 'api_key', largeApiKeyId),
    1200,
    '大批量 API Key 维度统计应先累积完整历史记录'
  )

  const firstLargeCleanup = apiKeyRecordCleanup.cleanupDeletedApiKeyRelatedRecordData({
    apiKeyId: largeApiKeyId,
    systemAccountId: 'sys_admin'
  })
  assert.equal(firstLargeCleanup.deletedRows, 1000, '单次 API Key 关联清理最多处理一个有界批次，避免长事务')
  assert.equal(firstLargeCleanup.hasMore, true, '单批次后仍有剩余记录时应保留待重试状态')
  assert.equal(cleanupTargetExists(largeApiKeyId), true, '单批次未清完时应保留清理目标')
  assert.equal(usageStatsTotal('sys_admin', 'api_key', largeApiKeyId), 200, '单批次清理应只扣减已删除批次统计')

  const secondLargeCleanup = apiKeyRecordCleanup.cleanupDeletedApiKeyRelatedRecordData({
    apiKeyId: largeApiKeyId,
    systemAccountId: 'sys_admin'
  })
  assert.equal(secondLargeCleanup.deletedRows, 200, '后续重试应继续清理剩余批次')
  assert.equal(secondLargeCleanup.hasMore, false, '剩余批次清完后不应继续标记 hasMore')
  assert.equal(cleanupTargetExists(largeApiKeyId), false, '清理完成后应移除大批量清理目标')
  assert.equal(usageStatsTotal('sys_admin', 'api_key', largeApiKeyId), 0, '大批量清理完成后 API Key 维度统计应归零')

  console.log('已删除 API Key 聚合清理回归通过：历史事实先聚合，删除清理再按游标扣减')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUsageRecord(id: string, apiKeyIdInput: string, createdAtInput: string): void {
  databaseModule.getDatasetDatabase()
    .prepare(`
      INSERT INTO usage_records (
        id, system_account_id, trace_id, api_key_id, endpoint, provider_code, model,
        stream, success, input_tokens, output_tokens, cost_usd, created_at
      ) VALUES (?, 'sys_admin', ?, ?, '/v1/chat/completions', 'openai', 'gpt-regression', 0, 1, 10, 20, 0.12, ?)
    `)
    .run(id, `trace_${id}`, apiKeyIdInput, createdAtInput)
}

function seedAuthorizationUsageRangeWindow(systemAccountId: string): void {
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO authorization_user_usage_range_windows (
        system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id,
        resource_filter_type, resource_filter_id, request_count, input_tokens, output_tokens,
        cache_read_tokens, cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      ) VALUES (?, ?, ?, '', 'grantee_deleted_key', 'account', 'account_deleted_key', 1, 10, 20, 0, 0, 0.12, ?, ?)
    `)
    .run(systemAccountId, createdDate, createdDate, createdAt, createdAt)
}

function seedAuditLog(id: string, apiKeyIdInput: string, createdAtInput: string): void {
  databaseModule.getDatasetDatabase()
    .prepare(`
      INSERT INTO audit_logs (
        id, trace_id, system_account_id, api_key_id, method, path, audit_outcome,
        success, sample_bucket, sample_reason, started_at, ended_at, created_at
      ) VALUES (?, ?, 'sys_admin', ?, 'POST', '/v1/chat/completions', 'success', 1, 0, 'regression', ?, ?, ?)
    `)
    .run(id, `trace_${id}`, apiKeyIdInput, createdAtInput, createdAtInput, createdAtInput)
}

function seedAuditErrorGroup(id: string, apiKeyIdInput: string, createdAtInput: string): void {
  databaseModule.getDatasetDatabase()
    .prepare(`
      INSERT INTO audit_error_groups (
        id, fingerprint, window_started_at, window_ended_at, system_account_id, api_key_id,
        count, created_at, updated_at
      ) VALUES (?, ?, ?, ?, 'sys_admin', ?, 1, ?, ?)
    `)
    .run(id, `fp_${id}`, createdAtInput, createdAtInput, apiKeyIdInput, createdAtInput, createdAtInput)
}

function usageStatsTotal(systemAccountId: string, scopeType: string, scopeId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT request_count
      FROM usage_stats_totals
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `)
    .get(systemAccountId, scopeType, scopeId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageOverviewSummaryRequestCount(systemAccountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(request_count), 0) AS request_count
      FROM usage_overview_summary_windows
      WHERE system_account_id = ?
    `)
    .get(systemAccountId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageScopeRangeWindowRequestCount(systemAccountId: string, scopeType: string, scopeId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(request_count), 0) AS request_count
      FROM usage_scope_range_windows
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `)
    .get(systemAccountId, scopeType, scopeId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageQuotaHourlyWindowCost(systemAccountId: string, scopeType: string, scopeId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(total_cost_usd), 0) AS total_cost_usd
      FROM usage_quota_hourly_windows
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `)
    .get(systemAccountId, scopeType, scopeId) as { total_cost_usd?: number } | undefined
  return Number(row?.total_cost_usd ?? 0)
}

function usageRankSnapshotMetric(systemAccountId: string, scopeType: string, scopeId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(metric_value), 0) AS metric_value
      FROM usage_rank_snapshots
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `)
    .get(systemAccountId, scopeType, scopeId) as { metric_value?: number } | undefined
  return Number(row?.metric_value ?? 0)
}

function authorizationUserUsageRangeWindowRequestCount(systemAccountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(request_count), 0) AS request_count
      FROM authorization_user_usage_range_windows
      WHERE system_account_id = ?
    `)
    .get(systemAccountId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageRecordExists(id: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT id FROM usage_records WHERE id = ?')
    .get(id) as { id?: string } | undefined
  return Boolean(row?.id)
}

function auditLogExists(id: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT id FROM audit_logs WHERE id = ?')
    .get(id) as { id?: string } | undefined
  return Boolean(row?.id)
}

function auditErrorGroupExists(id: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT id FROM audit_error_groups WHERE id = ?')
    .get(id) as { id?: string } | undefined
  return Boolean(row?.id)
}

function cleanupTargetExists(apiKeyIdInput: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT api_key_id FROM api_key_record_cleanup_targets WHERE api_key_id = ?')
    .get(apiKeyIdInput) as { api_key_id?: string } | undefined
  return Boolean(row?.api_key_id)
}
