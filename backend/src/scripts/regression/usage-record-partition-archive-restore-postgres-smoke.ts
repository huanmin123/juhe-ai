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

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'PG 使用记录分区归档恢复 smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const cleanupCursorJobNames = ['usage_stats_aggregation', 'client_ip_stats_aggregation'] as const
const marker = `usage_archive_restore_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const providerCode = `provider_${marker}`
const modelName = `model_${marker}`
let originalCleanupCursorRows: StatsJobStateRow[] | undefined
let smokePartitionName: string | undefined

const pool = await getPostgresPool()
const client = createPostgresDatabaseClient(pool)

try {
  const startDate = await chooseSmokePartitionStartDate()
  const dateKey = isoDateToDateKey(startDate)
  const partitionName = postgresUsageRecordPartitionName(dateKey)
  smokePartitionName = partitionName
  const bounds = postgresUsageRecordPartitionBounds(dateKey)
  const createdAt = `${bounds.startDate}T01:02:03.000Z`
  const cutoffAt = `${bounds.endDate}T00:00:00.000Z`
  const usageIds = [
    `usage_${dateKey}_s000_${marker}_a`,
    `usage_${dateKey}_s000_${marker}_b`
  ]

  assert.equal(await tableExists('juhe_usage', partitionName), false, 'smoke 分区日期不应已有热表')
  assert.equal(await tableExists('juhe_archive', partitionName), false, 'smoke 分区日期不应已有归档表')

  await seedCleanupCursorRows(cutoffAt)
  await ensurePostgresUsageRecordPartitions(client, [createdAt])
  await seedUsageRows(usageIds, createdAt)

  const cleanup = await cleanupProcessedUsageRecordsBeforeWithResultAsync(cutoffAt, 100)
  assert.equal(cleanup.blockedReason, undefined, 'PG 分区归档不应被统计安全游标阻塞')
  assert.equal(cleanup.droppedPartitions, 1, 'PG 清理应归档一个整日分区')
  assert.equal(cleanup.archivedPartitions, 1, 'PG 清理应返回 archivedPartitions=1')
  assert.equal(cleanup.deletedRows, usageIds.length, 'PG 分区归档应按分区行数返回 deletedRows')
  assert.equal(cleanup.hasMore, false, 'smoke 只构造一个可归档分区，不应遗留下一批')

  assert.equal(await tableExists('juhe_usage', partitionName), false, '归档后热库分区表应被移走')
  assert.equal(await parentUsageCount(usageIds), 0, '归档后 usage_records 父表不应查到样本')
  assert.equal(await tableExists('juhe_archive', partitionName), true, '归档后 archive schema 应存在分区表')

  const archiveRows = await archivedUsageRows(partitionName, usageIds)
  assert.equal(archiveRows.length, usageIds.length, '归档表应保留全部样本行')
  assert.deepEqual(new Set(archiveRows.map((row) => row.id)), new Set(usageIds), '归档表样本 ID 应一致')
  assert.ok(archiveRows.every((row) => row.created_at === createdAt), '归档表 created_at 边界应保持不变')

  const manifest = await latestManifest(partitionName)
  assert.equal(manifest?.domain, 'usage_records', '归档 manifest domain 应为 usage_records')
  assert.equal(manifest?.archive_action, 'detach_partition', '归档 manifest 动作应记录 detach_partition')
  assert.equal(manifest?.storage_uri, `postgres:juhe_archive.${partitionName}`, '归档 manifest 应记录 archive storage_uri')
  assert.equal(manifest?.partition_name, partitionName, '归档 manifest 应记录分区名')
  assert.equal(manifest?.range_start, bounds.startDate, '归档 manifest 应记录分区起始日期')
  assert.equal(manifest?.range_end, bounds.endDate, '归档 manifest 应记录分区结束日期')
  assert.equal(Number(manifest?.row_count ?? 0), usageIds.length, '归档 manifest 应记录分区行数')
  assert.equal(manifest?.status, 'archived', '归档 manifest 状态应为 archived')

  await restoreArchivedPartition(partitionName, bounds.startDate, bounds.endDate)
  assert.equal(await tableExists('juhe_archive', partitionName), false, '恢复后 archive schema 不应保留该表')
  assert.equal(await tableExists('juhe_usage', partitionName), true, '恢复后热库分区表应重新存在')
  assert.ok(
    (await listPostgresUsageRecordPartitions(client)).some((partition) => partition.partitionName === partitionName),
    '恢复后分区列表应重新包含该日分区'
  )
  assert.equal(await parentUsageCount(usageIds), usageIds.length, '恢复后 usage_records 父表应重新查到样本')

  console.log(JSON.stringify({
    message: 'PG 使用记录分区归档恢复 smoke 通过',
    partitionName,
    rowCount: usageIds.length,
    archiveStorageUri: manifest?.storage_uri
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

interface ArchiveManifestRow {
  domain?: string | null
  archive_action?: string | null
  storage_uri?: string | null
  partition_name?: string | null
  range_start?: string | null
  range_end?: string | null
  row_count?: string | number | null
  status?: string | null
}

async function chooseSmokePartitionStartDate(): Promise<string> {
  const hotStartDates = (await listPostgresUsageRecordPartitions(client)).map((partition) => partition.startDate)
  const archiveStartDates = (await client.query<{ table_name?: string | null }>(`
    SELECT child.relname AS table_name
    FROM pg_class child
    JOIN pg_namespace namespace ON namespace.oid = child.relnamespace
    WHERE namespace.nspname = 'juhe_archive'
      AND child.relkind = 'r'
      AND child.relname LIKE 'usage_records________'
  `))
    .map((row) => usagePartitionStartDateFromName(row.table_name))
    .filter((value): value is string => Boolean(value))

  const earliest = [...hotStartDates, ...archiveStartDates].sort()[0] ?? '2000-01-01'
  const startDate = addIsoDateDays(earliest, -10)
  if (startDate < '1900-01-01') {
    throw new Error(`PG 分区归档恢复 smoke 无法选择安全历史日期，当前最早分区：${earliest}`)
  }
  return startDate
}

async function seedCleanupCursorRows(cursorCreatedAt: string): Promise<void> {
  originalCleanupCursorRows = await client.query<StatsJobStateRow>(`
    SELECT scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at,
      last_error_message, lag_seconds, updated_at
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ANY(?::text[])
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
      ) VALUES (?, ?, ?, 'gateway', '127.0.0.1', ?, ?, ?, ?, 200, 1, ?, ?, ?, ?, 'owner', ?)
      ON CONFLICT(created_at, id) DO NOTHING
    `, [
      usageId,
      systemAccountId,
      `trace_${usageId}`,
      `api_key_${marker}`,
      `account_${marker}`,
      providerCode,
      modelName,
      index + 1,
      index + 2,
      0.001 * (index + 1),
      systemAccountId,
      createdAt
    ])
  }
}

async function archivedUsageRows(partitionName: string, usageIds: readonly string[]): Promise<Array<{ id: string; created_at: string }>> {
  return client.query<{ id: string; created_at: string }>(`
    SELECT id, created_at
    FROM juhe_archive.${quoteIdentifier(partitionName)}
    WHERE id = ANY(?::text[])
    ORDER BY id ASC
  `, [usageIds])
}

async function latestManifest(partitionName: string): Promise<ArchiveManifestRow | undefined> {
  return client.one<ArchiveManifestRow>(`
    SELECT domain, archive_action, storage_uri, partition_name, range_start, range_end, row_count, status
    FROM juhe_stats.data_archive_manifests
    WHERE domain = 'usage_records'
      AND partition_name = ?
    ORDER BY archived_at DESC, id DESC
    LIMIT 1
  `, [partitionName])
}

async function restoreArchivedPartition(partitionName: string, startDate: string, endDate: string): Promise<void> {
  await client.execute(`ALTER TABLE juhe_archive.${quoteIdentifier(partitionName)} SET SCHEMA juhe_usage`)
  await client.execute(`
    ALTER TABLE juhe_usage.usage_records
    ATTACH PARTITION juhe_usage.${quoteIdentifier(partitionName)}
    FOR VALUES FROM ('${startDate}') TO ('${endDate}')
  `)
}

async function parentUsageCount(usageIds: readonly string[]): Promise<number> {
  const row = await client.one<{ count?: string | number }>(`
    SELECT COUNT(*) AS count
    FROM juhe_usage.usage_records
    WHERE id = ANY(?::text[])
  `, [usageIds])
  return Number(row?.count ?? 0)
}

async function tableExists(schemaName: string, tableName: string): Promise<boolean> {
  const row = await client.one<{ exists?: boolean | null }>(`
    SELECT EXISTS (
      SELECT 1
      FROM pg_class child
      JOIN pg_namespace namespace ON namespace.oid = child.relnamespace
      WHERE namespace.nspname = ?
        AND child.relname = ?
        AND child.relkind IN ('r', 'p')
    ) AS exists
  `, [schemaName, tableName])
  return row?.exists === true
}

async function cleanupSmokeArtifacts(): Promise<void> {
  const partitionNames = new Set<string>()
  if (smokePartitionName) {
    partitionNames.add(smokePartitionName)
  }

  const manifestPartitionNames = (await client.query<{ table_name?: string | null }>(`
    SELECT child.relname AS table_name
    FROM pg_class child
    JOIN pg_namespace namespace ON namespace.oid = child.relnamespace
    WHERE namespace.nspname IN ('juhe_usage', 'juhe_archive')
      AND child.relname LIKE 'usage_records________'
      AND child.relname IN (
        SELECT DISTINCT partition_name
        FROM juhe_stats.data_archive_manifests
        WHERE domain = 'usage_records'
          AND id LIKE 'archive_%'
          AND storage_uri LIKE 'postgres:juhe_archive.usage_records_%'
          AND partition_name IS NOT NULL
          AND manifest_json::text LIKE ?
      )
  `, [`%${marker}%`]))
    .map((row) => String(row.table_name ?? '').trim())
    .filter((value) => /^usage_records_\d{8}$/.test(value))
  for (const partitionName of manifestPartitionNames) {
    partitionNames.add(partitionName)
  }

  for (const partitionName of partitionNames) {
    await client.execute(`DROP TABLE IF EXISTS juhe_archive.${quoteIdentifier(partitionName)}`)
    await client.execute(`DROP TABLE IF EXISTS juhe_usage.${quoteIdentifier(partitionName)}`)
    await client.execute('DELETE FROM juhe_stats.data_archive_manifests WHERE domain = ? AND partition_name = ?', ['usage_records', partitionName])
  }

  await client.execute(`
    DELETE FROM juhe_usage.usage_records
    WHERE system_account_id = ?
      OR id LIKE ?
  `, [systemAccountId, `usage_%_s000_${marker}_%`])
  await client.execute(`
    DELETE FROM juhe_stats.data_archive_manifests
    WHERE domain = 'usage_records'
      AND (
        storage_uri LIKE ?
        OR manifest_json::text LIKE ?
      )
  `, [`%${marker}%`, `%${marker}%`])
}

async function restoreCleanupCursorRows(): Promise<void> {
  if (!originalCleanupCursorRows) return
  for (const jobName of cleanupCursorJobNames) {
    const row = originalCleanupCursorRows.find((candidate) => candidate.job_name === jobName)
    if (!row) {
      await client.execute(`
        DELETE FROM juhe_stats.stats_job_state
        WHERE scope_type = 'global'
          AND scope_id = ''
          AND job_name = ?
      `, [jobName])
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
      row.scope_type,
      row.scope_id,
      row.job_name,
      row.cursor_created_at ?? null,
      row.cursor_id ?? null,
      row.last_success_at ?? null,
      row.last_error_message ?? null,
      row.lag_seconds ?? null,
      row.updated_at
    ])
  }
}

function usagePartitionStartDateFromName(tableName: string | null | undefined): string | undefined {
  const match = /^usage_records_(\d{4})(\d{2})(\d{2})$/.exec(String(tableName ?? '').trim())
  if (!match) return undefined
  return `${match[1]}-${match[2]}-${match[3]}`
}

function isoDateToDateKey(value: string): string {
  return value.replace(/-/g, '')
}

function addIsoDateDays(value: string, days: number): string {
  const date = new Date(`${value}T00:00:00.000Z`)
  date.setUTCDate(date.getUTCDate() + days)
  return date.toISOString().slice(0, 10)
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
