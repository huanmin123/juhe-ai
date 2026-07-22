import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { cleanupProcessedUsageRecordsBeforeWithResultAsync } from '../../storage/data-retention.repository.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  ensurePostgresUsageRecordPartitions,
  listPostgresUsageRecordPartitions,
  postgresUsageRecordPartitionBounds,
  postgresUsageRecordPartitionName
} from '../../storage/postgres-usage-record-partitions.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'PG 使用记录分区删除 smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const cleanupCursorJobNames = ['usage_stats_aggregation', 'client_ip_stats_aggregation'] as const
const marker = `usage_partition_drop_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
let originalCleanupCursorRows: StatsJobStateRow[] | undefined
let smokePartitionName: string | undefined

const pool = await getPostgresPool()
const client = createPostgresDatabaseClient(pool)

try {
  const startDate = await chooseSmokePartitionStartDate()
  const dateKey = startDate.replace(/-/g, '')
  const partitionName = postgresUsageRecordPartitionName(dateKey)
  smokePartitionName = partitionName
  const bounds = postgresUsageRecordPartitionBounds(dateKey)
  const createdAt = `${bounds.startDate}T01:02:03.000Z`
  const cutoffAt = `${bounds.endDate}T00:00:00.000Z`
  const usageIds = [`usage_${dateKey}_s000_${marker}_a`, `usage_${dateKey}_s000_${marker}_b`]

  assert.equal(await tableExists('juhe_usage', partitionName), false, 'smoke 历史日期不应已有热分区')
  assert.equal(await tableExists('juhe_archive', partitionName), false, 'smoke 历史日期不应已有冷归档表')

  await seedCleanupCursorRows(cutoffAt)
  await ensurePostgresUsageRecordPartitions(client, [createdAt])
  await seedUsageRows(usageIds, createdAt)

  const cleanup = await cleanupProcessedUsageRecordsBeforeWithResultAsync(cutoffAt, 100)
  assert.equal(cleanup.blockedReason, undefined, 'PG 分区删除不应被已追平的统计游标阻塞')
  assert.equal(cleanup.droppedPartitions, 1, 'PG 清理应直接删除一个整日分区')
  assert.equal(cleanup.deletedRows, usageIds.length, 'PG 分区删除应按分区行数返回 deletedRows')
  assert.equal(cleanup.hasMore, false, 'smoke 只构造一个可删除分区，不应遗留下一批')

  assert.equal(await tableExists('juhe_usage', partitionName), false, '清理后热 schema 不应保留目标分区')
  assert.equal(await tableExists('juhe_archive', partitionName), false, '清理后不得把目标分区移动到 juhe_archive')
  assert.equal(await parentUsageCount(usageIds), 0, '清理后 usage_records 父表不应查到样本')

  console.log(JSON.stringify({
    message: 'PG 使用记录整日分区直接删除 smoke 通过',
    partitionName,
    rowCount: usageIds.length
  }))
} finally {
  await cleanupSmokeArtifacts().catch(() => undefined)
  await restoreCleanupCursorRows().catch(() => undefined)
  await closePostgresPool()
}

interface StatsJobStateRow {
  scope_type: string
  scope_id: string
  job_name: string
  cursor_created_at?: string | null
  cursor_id?: string | null
  last_success_at?: string | null
  last_error_message?: string | null
  lag_seconds?: string | number | null
  updated_at: string
}

async function chooseSmokePartitionStartDate(): Promise<string> {
  const earliest = (await listPostgresUsageRecordPartitions(client)).map((partition) => partition.startDate).sort()[0] ?? '2000-01-01'
  const startDate = addIsoDateDays(earliest, -10)
  if (startDate < '1900-01-01') throw new Error(`PG 分区删除 smoke 无法选择安全历史日期，当前最早分区：${earliest}`)
  return startDate
}

async function seedCleanupCursorRows(cursorCreatedAt: string): Promise<void> {
  originalCleanupCursorRows = await client.query<StatsJobStateRow>(`
    SELECT scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at,
      last_error_message, lag_seconds, updated_at
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = ANY(?::text[])
  `, [[...cleanupCursorJobNames]])

  const now = new Date().toISOString()
  for (const jobName of cleanupCursorJobNames) {
    await client.execute(`
      INSERT INTO juhe_stats.stats_job_state (
        scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at,
        last_error_message, lag_seconds, updated_at
      ) VALUES ('global', '', ?, ?, ?, ?, NULL, 0, ?)
      ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
        cursor_created_at = EXCLUDED.cursor_created_at,
        cursor_id = EXCLUDED.cursor_id,
        last_success_at = EXCLUDED.last_success_at,
        last_error_message = NULL,
        lag_seconds = 0,
        updated_at = EXCLUDED.updated_at
    `, [jobName, cursorCreatedAt, `${marker}_${jobName}_cursor`, cursorCreatedAt, now])
  }
}

async function seedUsageRows(usageIds: readonly string[], createdAt: string): Promise<void> {
  for (const [index, usageId] of usageIds.entries()) {
    await client.execute(`
      INSERT INTO juhe_usage.usage_records (
        id, system_account_id, trace_id, traffic_source, client_ip, api_key_id, account_id,
        provider_code, model, status_code, success, input_tokens, output_tokens, cost_usd,
        account_owner_system_account_id, account_access_type, created_at
      ) VALUES (?, ?, ?, 'gateway', '127.0.0.1', ?, ?, 'gpt', 'gpt-5.5', 200, 1, ?, ?, ?, ?, 'owner', ?)
    `, [
      usageId,
      systemAccountId,
      `trace_${usageId}`,
      `api_key_${marker}`,
      `account_${marker}`,
      index + 1,
      index + 2,
      0.001 * (index + 1),
      systemAccountId,
      createdAt
    ])
  }
}

async function parentUsageCount(usageIds: readonly string[]): Promise<number> {
  const row = await client.one<{ count?: string | number }>(`
    SELECT COUNT(*) AS count FROM juhe_usage.usage_records WHERE id = ANY(?::text[])
  `, [usageIds])
  return Number(row?.count ?? 0)
}

async function tableExists(schemaName: string, tableName: string): Promise<boolean> {
  const row = await client.one<{ exists?: boolean | null }>(`
    SELECT EXISTS (
      SELECT 1 FROM pg_class child
      JOIN pg_namespace namespace ON namespace.oid = child.relnamespace
      WHERE namespace.nspname = ? AND child.relname = ? AND child.relkind IN ('r', 'p')
    ) AS exists
  `, [schemaName, tableName])
  return row?.exists === true
}

async function cleanupSmokeArtifacts(): Promise<void> {
  if (smokePartitionName) {
    await client.execute(`DROP TABLE IF EXISTS juhe_usage.${quoteIdentifier(smokePartitionName)}`)
  }
  await client.execute(`DELETE FROM juhe_usage.usage_records WHERE system_account_id = ? OR id LIKE ?`, [
    systemAccountId,
    `usage_%_s000_${marker}_%`
  ])
}

async function restoreCleanupCursorRows(): Promise<void> {
  if (!originalCleanupCursorRows) return
  for (const jobName of cleanupCursorJobNames) {
    const row = originalCleanupCursorRows.find((candidate) => candidate.job_name === jobName)
    if (!row) {
      await client.execute(`DELETE FROM juhe_stats.stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?`, [jobName])
      continue
    }
    await client.execute(`
      INSERT INTO juhe_stats.stats_job_state (
        scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at,
        last_error_message, lag_seconds, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
        cursor_created_at = EXCLUDED.cursor_created_at,
        cursor_id = EXCLUDED.cursor_id,
        last_success_at = EXCLUDED.last_success_at,
        last_error_message = EXCLUDED.last_error_message,
        lag_seconds = EXCLUDED.lag_seconds,
        updated_at = EXCLUDED.updated_at
    `, [
      row.scope_type, row.scope_id, row.job_name, row.cursor_created_at ?? null,
      row.cursor_id ?? null, row.last_success_at ?? null, row.last_error_message ?? null,
      row.lag_seconds ?? null, row.updated_at
    ])
  }
}

function addIsoDateDays(value: string, days: number): string {
  const date = new Date(`${value}T00:00:00.000Z`)
  date.setUTCDate(date.getUTCDate() + days)
  return date.toISOString().slice(0, 10)
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
