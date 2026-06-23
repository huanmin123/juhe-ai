import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-non-business-cleanup-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'non-business-cleanup-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const oldIso = '2000-01-01T00:00:00.000Z'
const recentIso = '2099-01-01T00:00:00.000Z'
const cutoffIso = '2001-01-01T00:00:00.000Z'
const oldAuditBlobStorageKey = `zz/non-business-cleanup-old-${Date.now()}.blob`
const recentAuditBlobStorageKey = `zz/non-business-cleanup-recent-${Date.now()}.blob`

const [databaseModule, dataRetention, usageShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/data-retention.repository.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  const businessDatabase = databaseModule.getBusinessDatabase()
  const businessRowsBefore = tableCount(businessDatabase, 'system_accounts')
  seedDatasetRows()
  seedStatsRows()
  seedAuditBlob(oldAuditBlobStorageKey, oldIso)
  seedAuditBlob(recentAuditBlobStorageKey, recentIso)
  const oldShardFilePath = seedUsageShard()

  const result = await dataRetention.cleanupNonBusinessDataBeforeWithResult({
    cutoffAt: cutoffIso,
    limit: 100
  })

  assert.equal(tableCount(databaseModule.getDatasetDatabase(), 'public_api_logs', "id = 'publog_old_non_business_cleanup'"), 0, '旧数据集日志应被硬清理')
  assert.equal(tableCount(databaseModule.getDatasetDatabase(), 'public_api_logs', "id = 'publog_recent_non_business_cleanup'"), 1, 'cutoff 之后的数据集日志应保留')
  assert.equal(tableCount(databaseModule.getDatasetDatabase(), 'dynamic_dataset_cleanup_rows', "id = 'dynamic_old_non_business_cleanup'"), 0, '旧动态数据集表记录应被硬清理')
  assert.equal(tableCount(databaseModule.getDatasetDatabase(), 'dynamic_dataset_cleanup_rows', "id = 'dynamic_recent_non_business_cleanup'"), 1, 'cutoff 之后的动态数据集表记录应保留')
  assert.equal(tableCount(databaseModule.getStatsDatabase(), 'usage_stats_totals', "scope_id = 'old_non_business_cleanup'"), 0, '旧统计缓存应被硬清理')
  assert.equal(tableCount(databaseModule.getStatsDatabase(), 'usage_stats_totals', "scope_id = 'recent_non_business_cleanup'"), 1, 'cutoff 之后的统计缓存应保留')
  assert.equal(tableCount(databaseModule.getStatsDatabase(), 'dynamic_stats_cleanup_rows', "id = 'dynamic_old_non_business_cleanup'"), 0, '旧动态统计表记录应被硬清理')
  assert.equal(tableCount(databaseModule.getStatsDatabase(), 'dynamic_stats_cleanup_rows', "id = 'dynamic_recent_non_business_cleanup'"), 1, 'cutoff 之后的动态统计表记录应保留')
  assert.equal(tableCount(databaseModule.getDatasetDatabase(), 'audit_payload_blobs', "id = 'audblob_old_non_business_cleanup'"), 0, '旧审计 blob 元数据应被硬清理')
  assert.equal(existsSync(auditBlobFilePath(oldAuditBlobStorageKey)), false, '旧审计 blob 外部文件应被删除')
  assert.equal(tableCount(databaseModule.getDatasetDatabase(), 'audit_payload_blobs', "id = 'audblob_recent_non_business_cleanup'"), 1, 'cutoff 之后的审计 blob 元数据应保留')
  assert.equal(existsSync(auditBlobFilePath(recentAuditBlobStorageKey)), true, 'cutoff 之后的审计 blob 外部文件应保留')
  assert.equal(tableCount(databaseModule.getUsageCatalogDatabase(), 'usage_record_shards'), 0, '旧 usage shard 目录应被硬清理')
  assert.equal(existsSync(oldShardFilePath), false, '旧 usage shard SQLite 文件应被删除')
  assert.equal(tableCount(businessDatabase, 'system_accounts'), businessRowsBefore, '非业务数据硬清理不应清业务库')
  assert(result.deletedRows >= 4, '硬清理结果应累计删除行数')
  assert(result.deletedFiles >= 2, '硬清理结果应累计删除外部文件数')

  console.log('非业务数据硬清理回归通过：业务库保留，数据集库、usage catalog、统计库、usage shard 和审计 blob 文件按 cutoff 清理')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(auditBlobFilePath(oldAuditBlobStorageKey), { force: true })
  rmSync(auditBlobFilePath(recentAuditBlobStorageKey), { force: true })
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedDatasetRows(): void {
  const datasetDatabase = databaseModule.getDatasetDatabase()
  datasetDatabase.exec(`
    CREATE TABLE IF NOT EXISTS dynamic_dataset_cleanup_rows (
      id TEXT PRIMARY KEY,
      created_at TEXT NOT NULL
    )
  `)
  const insert = datasetDatabase.prepare(`
    INSERT INTO public_api_logs (
      id, method, path, success, request_size_bytes, response_size_bytes,
      request_capture_status, response_capture_status, request_data_json, response_data_json,
      started_at, ended_at, created_at
    ) VALUES (?, 'GET', '/health', 1, 0, 0, 'empty', 'empty', '{}', '{}', ?, ?, ?)
  `)
  insert.run('publog_old_non_business_cleanup', oldIso, oldIso, oldIso)
  insert.run('publog_recent_non_business_cleanup', recentIso, recentIso, recentIso)
  const insertDynamic = datasetDatabase.prepare(`
    INSERT INTO dynamic_dataset_cleanup_rows (id, created_at)
    VALUES (?, ?)
  `)
  insertDynamic.run('dynamic_old_non_business_cleanup', oldIso)
  insertDynamic.run('dynamic_recent_non_business_cleanup', recentIso)
}

function seedStatsRows(): void {
  const statsDatabase = databaseModule.getStatsDatabase()
  statsDatabase.exec(`
    CREATE TABLE IF NOT EXISTS dynamic_stats_cleanup_rows (
      id TEXT PRIMARY KEY,
      stat_date TEXT NOT NULL
    )
  `)
  const insert = statsDatabase.prepare(`
    INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, updated_at)
    VALUES ('sys_admin', 'api_key', ?, 1, ?)
  `)
  insert.run('old_non_business_cleanup', oldIso)
  insert.run('recent_non_business_cleanup', recentIso)
  const insertDynamic = statsDatabase.prepare(`
    INSERT INTO dynamic_stats_cleanup_rows (id, stat_date)
    VALUES (?, ?)
  `)
  insertDynamic.run('dynamic_old_non_business_cleanup', '2000-01-01')
  insertDynamic.run('dynamic_recent_non_business_cleanup', '2099-01-01')
}

function seedAuditBlob(storageKey: string, createdAt: string): void {
  const id = storageKey === oldAuditBlobStorageKey ? 'audblob_old_non_business_cleanup' : 'audblob_recent_non_business_cleanup'
  const filePath = auditBlobFilePath(storageKey)
  mkdirSync(dirname(filePath), { recursive: true })
  writeFileSync(filePath, Buffer.from(id))
  databaseModule.getDatasetDatabase().prepare(`
    INSERT INTO audit_payload_blobs (
      id, sha256, raw_size_bytes, compressed_size_bytes, content_type, compression,
      storage_key, ref_count, first_seen_at, last_seen_at, created_at
    ) VALUES (?, ?, 1, 1, 'text/plain', 'none', ?, 0, ?, ?, ?)
  `).run(id, id, storageKey, createdAt, createdAt, createdAt)
}

function seedUsageShard(): string {
  const usageId = 'usage_20000101_s00_non_business_cleanup'
  const location = usageShards.usageRecordShardLocationForRecord(usageId, oldIso)
  usageShards.getUsageRecordShardDatabase(location).prepare(`
    INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, created_at)
    VALUES (?, 'sys_admin', 'trace_non_business_cleanup', 'gateway', ?)
  `).run(usageId, oldIso)
  usageShards.recordUsageRecordShardEntries([{
    id: usageId,
    shardKey: location.shardKey,
    systemAccountId: 'sys_admin',
    traceId: 'trace_non_business_cleanup',
    trafficSource: 'gateway',
    success: true,
    createdAt: oldIso
  }])
  assert.equal(existsSync(location.filePath), true, '测试前 usage shard 文件应存在')
  return location.filePath
}

function auditBlobFilePath(storageKey: string): string {
  return resolve(backendRoot, 'data', 'audit', 'blobs', storageKey)
}

function tableCount(database: ReturnType<typeof databaseModule.getDatasetDatabase>, tableName: string, whereClause = '1 = 1'): number {
  const row = database.prepare(`SELECT COUNT(*) AS total FROM ${tableName} WHERE ${whereClause}`).get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
