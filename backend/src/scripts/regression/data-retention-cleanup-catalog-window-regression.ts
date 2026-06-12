import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-data-retention-cleanup-catalog-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const shardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = shardRoot
runtimeConfig.secret = 'data-retention-cleanup-catalog-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, dataRetention] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/data-retention.repository.js')
])

try {
  seedShardsWithCatalogEntries(64)

  const registryReads = captureUsageShardRegistryReads()
  const cursorReads = captureUsageShardCursorReads()
  const preview = dataRetention.inspectProcessedUsageRecordsCleanupBefore('2024-01-02T00:00:00.000Z', 10)
  assert.equal(preview.blockedReason, undefined, '所有活跃 shard 都有统计安全游标时不应阻塞清理预览')
  assert.equal(preview.eligibleRows, 10, '清理预览应只返回首批候选数量')
  assert.equal(preview.hasMore, true, '候选超过批次上限时应返回 hasMore=true')
  assert.equal(existsSync(shardRoot), false, '清理预览应只读 catalog，不应打开或创建任意 usage shard DB 文件')
  assertCatalogCleanupQueryUsesCreatedSortIndex()
  assertUsageShardCleanupFloorCursorUsesIndex()

  databaseModule.getStatsDatabase()
    .prepare("DELETE FROM stats_job_state WHERE scope_type = 'usage_shard' AND scope_id = '20240101:s0' AND job_name = 'client_ip_stats_aggregation'")
    .run()
  const blockedPreview = dataRetention.inspectProcessedUsageRecordsCleanupBefore('2024-01-02T00:00:00.000Z', 10)
  assert.match(blockedPreview.blockedReason ?? '', /统计安全游标尚未建立/, '缺少任一必需统计安全游标且存在旧 catalog 记录时应阻塞')
  assert.equal(blockedPreview.eligibleRows, 0, '阻塞时不应返回可清理数量')
  assert.equal(existsSync(shardRoot), false, '阻塞判断也不应打开 usage shard DB 文件')
  assert(registryReads.length > 0, '回归应捕获 usage shard catalog 查询')
  assert(registryReads.every((call) => /\bLIMIT\b/i.test(call.sql)), '清理预览不应无界读取 active usage shard registry')
  const cursorShardKeyReads = cursorReads.filter((call) => /\bSELECT\s+scope_id\b/i.test(call.sql))
  assert(cursorShardKeyReads.length > 0, '回归应捕获统计安全游标 shard key 查询')
  assert(cursorShardKeyReads.every((call) => /\bscope_id\s+IN\s*\(/i.test(call.sql)), '统计安全游标检查必须按候选 shard key 有界查询')
  assert(cursorShardKeyReads.every((call) => call.rowCount <= 10), '统计安全游标检查不应返回超过当前清理批次的 cursor 行')

  restoreUsageShardCleanupCursors('20240101:s0', 'usage_20240101_s000_000001')
  const orphanCandidateIds = listCleanupCandidateUsageIds('2024-01-02T00:00:00.000Z', 10)
  const catalogCountBeforeOrphanCleanup = usageShardCatalogEntryCount()
  const orphanCleanup = dataRetention.cleanupProcessedUsageRecordsBeforeWithResult('2024-01-02T00:00:00.000Z', 10)
  assert.equal(orphanCleanup.blockedReason, undefined, '统计安全游标恢复后应允许清理 orphan catalog 候选')
  assert.equal(orphanCleanup.deletedRows, 0, 'shard 表行已不存在时不应虚增实际删除行数')
  assert.equal(usageShardCatalogEntryCount(), catalogCountBeforeOrphanCleanup - orphanCandidateIds.length, '已处理候选即使 shard 行缺失也应删除 catalog 目录项，避免反复进入清理窗口')
  assert.equal(usageShardCatalogEntryCountForIds(orphanCandidateIds), 0, '本批 orphan catalog ID 不应在后续清理中重复出现')

  console.log('使用记录保留清理 catalog 窗口回归通过：预览基于目录库候选窗口，不再打开全部 usage shard')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function captureUsageShardRegistryReads(): Array<{ sql: string; rowCount: number; params: unknown[] }> {
  const database = databaseModule.getDatasetDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const calls: Array<{ sql: string; rowCount: number; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bSELECT\b/i.test(sql) && /\busage_record_shards\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        const rows = originalAll(...params) as unknown[]
        calls.push({ sql, rowCount: rows.length, params })
        return rows
      }) as typeof statement.all
      const originalGet = statement.get.bind(statement) as typeof statement.get
      statement.get = ((...params: SQLInputValue[]) => {
        const row = originalGet(...params)
        calls.push({ sql, rowCount: row ? 1 : 0, params })
        return row
      }) as typeof statement.get
    }
    return statement
  }) as typeof database.prepare
  return calls
}

function captureUsageShardCursorReads(): Array<{ sql: string; rowCount: number; params: unknown[] }> {
  const database = databaseModule.getStatsDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const calls: Array<{ sql: string; rowCount: number; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bSELECT\b/i.test(sql) && /\bFROM\s+stats_job_state\b/i.test(sql) && /\bscope_type\s*=\s*'usage_shard'/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        const rows = originalAll(...params) as unknown[]
        calls.push({ sql, rowCount: rows.length, params })
        return rows
      }) as typeof statement.all
      const originalGet = statement.get.bind(statement) as typeof statement.get
      statement.get = ((...params: SQLInputValue[]) => {
        const row = originalGet(...params)
        calls.push({ sql, rowCount: row ? 1 : 0, params })
        return row
      }) as typeof statement.get
    }
    return statement
  }) as typeof database.prepare
  return calls
}

function listCleanupCandidateUsageIds(cutoffCreatedAt: string, limit: number): string[] {
  return databaseModule.getDatasetDatabase()
    .prepare(`
      SELECT ue.usage_id
      FROM usage_record_shard_entries ue
      JOIN usage_record_shards s ON s.shard_key = ue.shard_key
      WHERE s.status = 'active'
        AND ue.created_at < ?
      ORDER BY ue.created_at ASC, ue.usage_id ASC
      LIMIT ?
    `)
    .all(cutoffCreatedAt, limit)
    .map((row) => String((row as { usage_id?: unknown }).usage_id ?? ''))
    .filter(Boolean)
}

function usageShardCatalogEntryCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM usage_record_shard_entries')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function usageShardCatalogEntryCountForIds(ids: string[]): number {
  if (!ids.length) return 0
  const placeholders = ids.map(() => '?').join(', ')
  const row = databaseModule.getDatasetDatabase()
    .prepare(`SELECT COUNT(*) AS total FROM usage_record_shard_entries WHERE usage_id IN (${placeholders})`)
    .get(...ids) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function restoreUsageShardCleanupCursors(shardKey: string, cursorId: string): void {
  const now = '2024-01-02T00:00:00.000Z'
  const statement = databaseModule.getStatsDatabase().prepare(`
    INSERT INTO stats_job_state (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at
    )
    VALUES ('usage_shard', ?, ?, '2024-01-01T00:00:59.000Z', ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      updated_at = excluded.updated_at
  `)
  statement.run(shardKey, 'usage_stats_aggregation', cursorId, now, now)
  statement.run(shardKey, 'client_ip_stats_aggregation', cursorId, now, now)
}

function seedShardsWithCatalogEntries(count: number): void {
  const datasetDatabase = databaseModule.getDatasetDatabase()
  const statsDatabase = databaseModule.getStatsDatabase()
  const now = '2024-01-02T00:00:00.000Z'
  const shardStatement = datasetDatabase.prepare(`
    INSERT INTO usage_record_shards (
      shard_key, bucket_date, shard_id, file_path, schema_version, status, first_seen_at, created_at, updated_at
    )
    VALUES (?, '2024-01-01', ?, ?, 1, 'active', ?, ?, ?)
  `)
  const entryStatement = datasetDatabase.prepare(`
    INSERT INTO usage_record_shard_entries (
      usage_id, shard_key, trace_id, system_account_id, traffic_source, success, created_at, indexed_at
    )
    VALUES (?, ?, ?, 'sys_admin', 'api_key', 1, ?, ?)
  `)
  const cursorStatement = statsDatabase.prepare(`
    INSERT INTO stats_job_state (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at
    )
    VALUES ('usage_shard', ?, ?, '2024-01-01T00:00:59.000Z', ?, ?, ?)
  `)
  for (let index = 0; index < count; index += 1) {
    const shardKey = `20240101:s${index}`
    const usageId = `usage_20240101_s${String(index).padStart(3, '0')}_000001`
    shardStatement.run(shardKey, index, join(shardRoot, `shard-${index}.sqlite3`), now, now, now)
    entryStatement.run(usageId, shardKey, `trace_${usageId}`, `2024-01-01T00:00:${String(index % 60).padStart(2, '0')}.000Z`, now)
    cursorStatement.run(shardKey, 'usage_stats_aggregation', usageId, now, now)
    cursorStatement.run(shardKey, 'client_ip_stats_aggregation', usageId, now, now)
  }
}

function assertCatalogCleanupQueryUsesCreatedSortIndex(): void {
  const details = databaseModule.getDatasetDatabase()
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT ue.usage_id, ue.created_at, s.shard_key, s.bucket_date, s.shard_id, s.file_path
      FROM usage_record_shard_entries ue
      JOIN usage_record_shards s ON s.shard_key = ue.shard_key
      WHERE s.status = 'active'
        AND ue.created_at < ?
        AND (ue.created_at < ? OR (ue.created_at = ? AND ue.usage_id <= ?))
      ORDER BY ue.created_at ASC, ue.usage_id ASC
      LIMIT ?
    `)
    .all('2024-01-02T00:00:00.000Z', '2024-01-01T00:00:59.000Z', '2024-01-01T00:00:59.000Z', 'usage_20240101_s999_999999', 10)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes('idx_usage_record_shard_entries_created_sort'), `清理候选 catalog 查询应使用 created_at 排序索引，实际计划：${details}`)
  assert(!/USE TEMP B-TREE/i.test(details), `清理候选 catalog 查询不应使用临时排序，实际计划：${details}`)
}

function assertUsageShardCleanupFloorCursorUsesIndex(): void {
  const details = databaseModule.getStatsDatabase()
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT cursor_created_at, cursor_id
      FROM stats_job_state
      WHERE scope_type = 'usage_shard'
        AND job_name IN (?, ?)
        AND cursor_created_at IS NOT NULL
        AND cursor_id IS NOT NULL
      ORDER BY cursor_created_at ASC, cursor_id ASC
      LIMIT 1
    `)
    .all('usage_stats_aggregation', 'client_ip_stats_aggregation')
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes('idx_stats_job_state_usage_shard_cursor_floor_any_job'), `统计安全 floor cursor 查询应使用跨 job 排序专用索引，实际计划：${details}`)
  assert(!/USE TEMP B-TREE/i.test(details), `统计安全 floor cursor 查询不应使用临时排序，实际计划：${details}`)
}
