import assert from 'node:assert/strict'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { closeRedisClients, createDedicatedRedisClient } from '../../shared/redis-client.js'
import { cleanupProcessedUsageRecordsBeforeWithResultAsync } from '../../storage/data-retention.repository.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  ensurePostgresUsageRecordPartitions,
  listPostgresUsageRecordPartitions,
  postgresUsageRecordPartitionBounds,
  postgresUsageRecordPartitionName
} from '../../storage/postgres-usage-record-partitions.js'

interface PressureConfig {
  usageRows: number
  usageUsers: number
  usageDays: number
  cleanupRows: number
  rangeWindowRows: number
  batchSize: number
  queryIterations: number
  queryConcurrency: number
  cleanup: boolean
  maxAllowedDeadlocks: number
  maxAllowedUsageQueryP95Ms: number
  maxAllowedRangeQueryP95Ms: number
  reportPath?: string
}

interface TimedResult<T> {
  durationMs: number
  value: T
}

interface QueryMetric {
  operation: string
  latencyMs: number
  rows: number
  ok: boolean
  error?: string
}

interface OperationSummary {
  count: number
  ok: number
  errors: number
  p50Ms: number
  p95Ms: number
  p99Ms: number
  maxMs: number
  averageRows: number
}

interface RelationSize {
  schema: string
  table: string
  tableBytes: number
  indexBytes: number
  totalBytes: number
  estimatedRows: number
}

interface ExplainReport {
  label: string
  plan: string
  usesExpectedIndex: boolean
  hasSeqScan: boolean
  expectedIndexes: string[]
}

interface RedisReport {
  cachePingMs: number
  statePingMs: number
  queuePingMs: number
}

interface PressureReport {
  mode: {
    runtimeMode: string
    databaseDriver: string
    cacheDriver: string
    runtimeStateDriver: string
    queueDriver: string
  }
  config: PressureConfig
  marker: string
  startedAt: string
  finishedAt: string
  durationMs: number
  seed: {
    hotPartitions: string[]
    cleanupPartition: string
    usageRows: number
    cleanupRows: number
    rangeWindowRows: number
    insertUsageRowsMs: number
    insertUsageRowsPerSecond: number
    insertRangeWindowsMs: number
    insertRangeWindowsPerSecond: number
  }
  queries: {
    total: OperationSummary
    operations: Record<string, OperationSummary>
    usageQueryP95Ms: number
    rangeQueryP95Ms: number
  }
  retentionDrop: {
    durationMs: number
    deletedRows: number
    droppedPartitions: number
    hasMore: boolean
    partitionRemoved: boolean
  }
  relationSizes: RelationSize[]
  explain: ExplainReport[]
  postgres: {
    deadlocksBefore: number
    deadlocksAfter: number
    deadlocksDelta: number
  }
  redis: RedisReport
  catalogMarkerEntries: number
  pass: boolean
  violations: string[]
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

const usageColumns = [
  'id',
  'system_account_id',
  'trace_id',
  'traffic_source',
  'client_ip',
  'api_key_id',
  'group_id',
  'account_id',
  'endpoint',
  'provider_code',
  'model',
  'status_code',
  'success',
  'input_tokens',
  'output_tokens',
  'cost_usd',
  'account_owner_system_account_id',
  'account_access_type',
  'created_at'
] as const

const rangeWindowColumns = [
  'system_account_id',
  'scope_type',
  'scope_id',
  'start_date',
  'end_date',
  'request_count',
  'success_count',
  'error_count',
  'input_tokens',
  'output_tokens',
  'total_cost_usd',
  'duration_ms_sum',
  'duration_ms_count',
  'duration_ms_max',
  'first_token_ms_sum',
  'first_token_ms_count',
  'first_token_ms_max',
  'active_days',
  'last_used_at',
  'updated_at'
] as const

const cleanupCursorJobNames = ['usage_stats_aggregation', 'client_ip_stats_aggregation'] as const

logger.level = 'silent'

assert.equal(runtimeConfig.runtimeMode, 'performance', 'usage 热数据压测需要 JUHE_AI_RUNTIME_MODE=performance')
assert.equal(runtimeConfig.databaseDriver, 'postgres', 'usage 热数据压测需要 JUHE_AI_DATABASE_DRIVER=postgres')
assert.equal(runtimeConfig.cacheDriver, 'redis', 'usage 热数据压测需要 Redis cache')
assert.equal(runtimeConfig.runtimeStateDriver, 'redis', 'usage 热数据压测需要 Redis runtime state')
assert.equal(runtimeConfig.queueDriver, 'redis_stream', 'usage 热数据压测需要 Redis Stream queue')

const config = loadConfig()
const marker = `usage_pressure_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountPrefix = `sys_${marker}`
const sampledAt = new Date().toISOString()
const hotPartitions: string[] = []
let cleanupPartitionName: string | undefined
let originalCleanupCursorRows: StatsJobStateRow[] | undefined
let exitCode = 0

const pool = await getPostgresPool()
const client = createPostgresDatabaseClient(pool)

try {
  const report = await runPressure()
  outputReport(report)
  if (!report.pass) {
    exitCode = 1
  }
} catch (error) {
  exitCode = 1
  console.error(error instanceof Error ? error.stack ?? error.message : error)
} finally {
  if (config.cleanup) {
    await cleanupPressureArtifacts().catch((error) => {
      console.error(error instanceof Error ? error.message : error)
    })
  }
  await restoreCleanupCursorRows().catch(() => undefined)
  await closeRedisClients().catch(() => undefined)
  await closePostgresPool().catch(() => undefined)
}

process.exit(exitCode)

async function runPressure(): Promise<PressureReport> {
  const startedAt = new Date()
  const startedAtMs = performance.now()
  const redis = await sampleRedis()
  const deadlocksBefore = await queryDeadlocks()
  const hotDates = await chooseUnusedHotDates(config.usageDays)
  const cleanupDate = await chooseUnusedCleanupDate()
  const hotCreatedAts = hotDates.map((date) => `${date}T00:00:00.000Z`)
  const cleanupBounds = postgresUsageRecordPartitionBounds(isoDateToDateKey(cleanupDate))
  const cleanupCreatedAt = `${cleanupBounds.startDate}T01:00:00.000Z`
  cleanupPartitionName = postgresUsageRecordPartitionName(isoDateToDateKey(cleanupDate))
  hotPartitions.push(...hotDates.map((date) => postgresUsageRecordPartitionName(isoDateToDateKey(date))))

  await ensurePostgresUsageRecordPartitions(client, [...hotCreatedAts, cleanupCreatedAt])

  const usageSeed = await timed(async () => {
    await insertUsageRows(config.usageRows, hotDates, 'hot')
    await analyzeTables(hotPartitions.map((partition) => ['juhe_usage', partition] as const))
  })
  const cleanupSeed = await timed(async () => {
    await insertUsageRows(config.cleanupRows, [cleanupDate], 'retention-drop')
    await analyzeTables([[ 'juhe_usage', cleanupPartitionName! ]])
  })
  const rangeSeed = await timed(async () => {
    await insertRangeWindowRows(config.rangeWindowRows, hotDates)
    await pool.query('ANALYZE juhe_stats.usage_scope_range_windows')
  })
  const actualRangeWindowRows = await countRangeWindowRows()

  const explainBeforeQueries = await explainHotQueries(hotDates)
  const queryMetrics = await runQueryPressure(hotDates)
  const relationSizes = await readRelationSizes([
    ...hotPartitions.map((partition) => ['juhe_usage', partition] as const),
    ['juhe_usage', cleanupPartitionName!] as const,
    ['juhe_stats', 'usage_scope_range_windows'] as const,
    ['juhe_usage', 'usage_record_shard_entries'] as const
  ])

  await seedCleanupCursorRows(`${cleanupBounds.endDate}T00:00:00.000Z`)
  const retentionDrop = await timed(async () => cleanupProcessedUsageRecordsBeforeWithResultAsync(`${cleanupBounds.endDate}T00:00:00.000Z`, Math.max(config.cleanupRows, 1000)))
  const partitionRemoved = !await tableExists('juhe_usage', cleanupPartitionName!)
  const catalogMarkerEntries = await countCatalogMarkerEntries()
  const deadlocksAfter = await queryDeadlocks()
  const querySummary = summarizeQueries(queryMetrics)
  const finishedAt = new Date()
  const violations = collectViolations({
    querySummary,
    explain: explainBeforeQueries,
    deadlocksDelta: Math.max(0, deadlocksAfter - deadlocksBefore),
    catalogMarkerEntries,
    cleanupDeletedRows: retentionDrop.value.deletedRows,
    partitionRemoved
  })

  return {
    mode: {
      runtimeMode: runtimeConfig.runtimeMode,
      databaseDriver: runtimeConfig.databaseDriver,
      cacheDriver: runtimeConfig.cacheDriver,
      runtimeStateDriver: runtimeConfig.runtimeStateDriver,
      queueDriver: runtimeConfig.queueDriver
    },
    config,
    marker,
    startedAt: startedAt.toISOString(),
    finishedAt: finishedAt.toISOString(),
    durationMs: round(performance.now() - startedAtMs),
    seed: {
      hotPartitions,
      cleanupPartition: cleanupPartitionName!,
      usageRows: config.usageRows,
      cleanupRows: config.cleanupRows,
      rangeWindowRows: actualRangeWindowRows,
      insertUsageRowsMs: round(usageSeed.durationMs + cleanupSeed.durationMs),
      insertUsageRowsPerSecond: round((config.usageRows + config.cleanupRows) / Math.max((usageSeed.durationMs + cleanupSeed.durationMs) / 1000, 0.001)),
      insertRangeWindowsMs: round(rangeSeed.durationMs),
      insertRangeWindowsPerSecond: round(actualRangeWindowRows / Math.max(rangeSeed.durationMs / 1000, 0.001))
    },
    queries: querySummary,
    retentionDrop: {
      durationMs: round(retentionDrop.durationMs),
      deletedRows: retentionDrop.value.deletedRows,
      droppedPartitions: retentionDrop.value.droppedPartitions ?? 0,
      hasMore: retentionDrop.value.hasMore,
      partitionRemoved
    },
    relationSizes,
    explain: explainBeforeQueries,
    postgres: {
      deadlocksBefore,
      deadlocksAfter,
      deadlocksDelta: Math.max(0, deadlocksAfter - deadlocksBefore)
    },
    redis,
    catalogMarkerEntries,
    pass: violations.length === 0,
    violations
  }
}

async function insertUsageRows(totalRows: number, dates: string[], phase: 'hot' | 'retention-drop'): Promise<void> {
  if (totalRows <= 0) return
  for (let offset = 0; offset < totalRows; offset += config.batchSize) {
    const rows = Array.from({ length: Math.min(config.batchSize, totalRows - offset) }, (_item, localIndex) => {
      const index = offset + localIndex
      const date = dates[index % dates.length]!
      const dateKey = isoDateToDateKey(date)
      const userIndex = index % config.usageUsers
      const systemAccountId = `${systemAccountPrefix}_${userIndex}`
      const seconds = index % 86400
      const createdAt = `${date}T${String(Math.floor(seconds / 3600)).padStart(2, '0')}:${String(Math.floor((seconds % 3600) / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}.000Z`
      return [
        `usage_${dateKey}_s${String(index % 64).padStart(3, '0')}_${marker}_${phase}_${index.toString(36)}`,
        systemAccountId,
        `trace_${marker}_${phase}_${index % 4096}_${index.toString(36)}`,
        'gateway',
        `10.${userIndex % 255}.${Math.floor(index / 255) % 255}.${index % 255}`,
        `api_key_${marker}_${index % Math.max(1, Math.floor(config.usageUsers / 2))}`,
        `group_${marker}_${index % Math.max(1, Math.floor(config.usageUsers / 4))}`,
        `account_${marker}_${index % Math.max(1, config.usageUsers)}`,
        index % 2 === 0 ? '/v1/chat/completions' : '/v1/responses',
        'openai',
        index % 3 === 0 ? 'gpt-5.5' : 'gpt-5-mini',
        index % 97 === 0 ? 500 : 200,
        index % 97 === 0 ? 0 : 1,
        50 + (index % 800),
        20 + (index % 400),
        Number(((50 + (index % 800) + 20 + (index % 400)) * 0.000001).toFixed(6)),
        systemAccountId,
        'owner',
        createdAt
      ]
    })
    await pool.query(`
      INSERT INTO juhe_usage.usage_records (${usageColumns.join(', ')})
      VALUES ${placeholders(rows.length, usageColumns.length)}
      ON CONFLICT(created_at, id) DO NOTHING
    `, rows.flat())
  }
}

async function insertRangeWindowRows(totalRows: number, dates: string[]): Promise<void> {
  if (totalRows <= 0) return
  const startDate = dates[0]!
  const endDate = dates[dates.length - 1]!
  const scopeTypes = ['account', 'api_key', 'group'] as const
  for (let offset = 0; offset < totalRows; offset += config.batchSize) {
    const rows = Array.from({ length: Math.min(config.batchSize, totalRows - offset) }, (_item, localIndex) => {
      const index = offset + localIndex
      const systemAccountId = `${systemAccountPrefix}_${index % config.usageUsers}`
      const scopeType = scopeTypes[index % scopeTypes.length]!
      const requestCount = 1 + (index % 10000)
      return [
        systemAccountId,
        scopeType,
        `${scopeType}_${marker}_${index}`,
        startDate,
        endDate,
        requestCount,
        requestCount - (index % 17),
        index % 17,
        requestCount * 10,
        requestCount * 4,
        Number((requestCount * 0.00002).toFixed(6)),
        requestCount * 120,
        requestCount,
        300 + (index % 1000),
        requestCount * 30,
        requestCount,
        80 + (index % 300),
        Math.min(31, dates.length),
        `${endDate}T23:59:59.000Z`,
        new Date().toISOString()
      ]
    })
    await pool.query(`
      INSERT INTO juhe_stats.usage_scope_range_windows (${rangeWindowColumns.join(', ')})
      VALUES ${placeholders(rows.length, rangeWindowColumns.length)}
      ON CONFLICT(system_account_id, scope_type, scope_id, start_date, end_date) DO UPDATE SET
        request_count = EXCLUDED.request_count,
        success_count = EXCLUDED.success_count,
        error_count = EXCLUDED.error_count,
        input_tokens = EXCLUDED.input_tokens,
        output_tokens = EXCLUDED.output_tokens,
        total_cost_usd = EXCLUDED.total_cost_usd,
        duration_ms_sum = EXCLUDED.duration_ms_sum,
        duration_ms_count = EXCLUDED.duration_ms_count,
        duration_ms_max = EXCLUDED.duration_ms_max,
        first_token_ms_sum = EXCLUDED.first_token_ms_sum,
        first_token_ms_count = EXCLUDED.first_token_ms_count,
        first_token_ms_max = EXCLUDED.first_token_ms_max,
        active_days = EXCLUDED.active_days,
        last_used_at = EXCLUDED.last_used_at,
        updated_at = EXCLUDED.updated_at
    `, rows.flat())
  }
}

async function runQueryPressure(dates: string[]): Promise<QueryMetric[]> {
  const metrics: QueryMetric[] = []
  let nextIndex = 0
  await Promise.all(Array.from({ length: config.queryConcurrency }, async () => {
    for (;;) {
      const index = nextIndex
      nextIndex += 1
      if (index >= config.queryIterations) return
      const operation = queryOperation(index, dates)
      const started = performance.now()
      try {
        const result = await pool.query(operation.sql, operation.params)
        metrics.push({
          operation: operation.name,
          latencyMs: performance.now() - started,
          rows: result.rows.length,
          ok: true
        })
      } catch (error) {
        metrics.push({
          operation: operation.name,
          latencyMs: performance.now() - started,
          rows: 0,
          ok: false,
          error: error instanceof Error ? error.message : String(error)
        })
      }
    }
  }))
  return metrics
}

function queryOperation(index: number, dates: string[]): { name: string; sql: string; params: unknown[] } {
  const userIndex = index % config.usageUsers
  const systemAccountId = `${systemAccountPrefix}_${userIndex}`
  const date = dates[index % dates.length]!
  const nextDate = addIsoDateDays(date, 1)
  const tracePrefix = `trace_${marker}_hot_${index % 4096}_`
  const accountId = `account_${marker}_${index % Math.max(1, config.usageUsers)}`
  const apiKeyId = `api_key_${marker}_${index % Math.max(1, Math.floor(config.usageUsers / 2))}`
  const windowKey = `${dates[0]}:${dates[dates.length - 1]}`
  switch (index % 6) {
    case 0:
      return {
        name: 'usage:user-date',
        sql: `
          SELECT id, created_at
          FROM juhe_usage.usage_records
          WHERE system_account_id = $1
            AND created_at >= $2
            AND created_at < $3
          ORDER BY created_at DESC, id DESC
          LIMIT 50
        `,
        params: [systemAccountId, date, nextDate]
      }
    case 1:
      return {
        name: 'usage:trace-prefix',
        sql: `
          SELECT id, created_at
          FROM juhe_usage.usage_records
          WHERE system_account_id = $1
            AND trace_id COLLATE "C" >= $2
            AND trace_id COLLATE "C" < $3
          ORDER BY created_at DESC, id DESC
          LIMIT 50
        `,
        params: [systemAccountId, tracePrefix, textPrefixUpperBound(tracePrefix)]
      }
    case 2:
      return {
        name: 'usage:account-date',
        sql: `
          SELECT id, created_at
          FROM juhe_usage.usage_records
          WHERE system_account_id = $1
            AND account_id = $2
            AND created_at >= $3
            AND created_at < $4
          ORDER BY created_at DESC, id DESC
          LIMIT 50
        `,
        params: [systemAccountId, accountId, date, nextDate]
      }
    case 3:
      return {
        name: 'usage:api-key-date',
        sql: `
          SELECT id, created_at
          FROM juhe_usage.usage_records
          WHERE system_account_id = $1
            AND api_key_id = $2
            AND created_at >= $3
            AND created_at < $4
          ORDER BY created_at DESC, id DESC
          LIMIT 50
        `,
        params: [systemAccountId, apiKeyId, date, nextDate]
      }
    case 4:
      return {
        name: 'range-window:top-scopes',
        sql: `
          SELECT scope_id, request_count, total_cost_usd
          FROM juhe_stats.usage_scope_range_windows
          WHERE system_account_id = $1
            AND scope_type = 'account'
            AND window_key = $2
          ORDER BY request_count DESC, total_cost_usd DESC, (input_tokens + output_tokens) DESC, last_used_at DESC, scope_id
          LIMIT 50
        `,
        params: [systemAccountId, windowKey]
      }
    default:
      return {
        name: 'range-window:lookup',
        sql: `
          SELECT scope_id, request_count, total_cost_usd
          FROM juhe_stats.usage_scope_range_windows
          WHERE system_account_id = $1
            AND scope_type = 'account'
            AND scope_id = $2
            AND window_key = $3
          LIMIT 1
        `,
        params: [systemAccountId, lookupAccountScopeId(index), windowKey]
      }
  }
}

async function explainHotQueries(dates: string[]): Promise<ExplainReport[]> {
  const date = dates[0]!
  const nextDate = addIsoDateDays(date, 1)
  const systemAccountId = `${systemAccountPrefix}_0`
  const tracePrefix = `trace_${marker}_hot_0_`
  const windowKey = `${dates[0]}:${dates[dates.length - 1]}`
  const reports: ExplainReport[] = []
  reports.push(await explainQuery('usage user/date list', `
    SELECT id
    FROM juhe_usage.usage_records
    WHERE system_account_id = $1
      AND created_at >= $2
      AND created_at < $3
    ORDER BY created_at DESC, id DESC
    LIMIT 50
  `, [systemAccountId, date, nextDate], ['system_account_id_created_at_id_idx', 'idx_usage_records_system_account_created_sort']))
  reports.push(await explainQuery('usage trace prefix', `
    SELECT id
    FROM juhe_usage.usage_records
    WHERE system_account_id = $1
      AND trace_id COLLATE "C" >= $2
      AND trace_id COLLATE "C" < $3
    ORDER BY created_at DESC, id DESC
    LIMIT 50
  `, [systemAccountId, tracePrefix, textPrefixUpperBound(tracePrefix)], ['system_account_id_trace_id_created__idx', 'idx_usage_records_system_trace_c_created_sort']))
  reports.push(await explainQuery('usage account/date list', `
    SELECT id
    FROM juhe_usage.usage_records
    WHERE system_account_id = $1
      AND account_id = $2
      AND created_at >= $3
      AND created_at < $4
    ORDER BY created_at DESC, id DESC
    LIMIT 50
  `, [systemAccountId, `account_${marker}_0`, date, nextDate], ['idx_usage_records_system_account_account_created_sort', 'system_account_id_account_id_created_idx']))
  reports.push(await explainQuery('range window top scopes', `
    SELECT scope_id
    FROM juhe_stats.usage_scope_range_windows
    WHERE system_account_id = $1
      AND scope_type = 'account'
      AND window_key = $2
    ORDER BY request_count DESC, total_cost_usd DESC, (input_tokens + output_tokens) DESC, last_used_at DESC, scope_id
    LIMIT 50
  `, [systemAccountId, windowKey], [
    'idx_usage_scope_range_windows_account_usage_order',
    'idx_usage_scope_range_windows_range_lookup',
    'usage_scope_range_windows_pkey'
  ]))
  reports.push(await explainQuery('range window lookup', `
    SELECT scope_id
    FROM juhe_stats.usage_scope_range_windows
    WHERE system_account_id = $1
      AND scope_type = 'account'
      AND scope_id = $2
      AND window_key = $3
    LIMIT 1
  `, [systemAccountId, lookupAccountScopeId(0), windowKey], [
    'idx_usage_scope_range_windows_lookup',
    'idx_usage_scope_range_windows_range_lookup',
    'usage_scope_range_windows_pkey'
  ]))
  return reports
}

async function explainQuery(label: string, sql: string, params: unknown[], expectedIndexes: string[]): Promise<ExplainReport> {
  const connection = await pool.connect()
  try {
    await connection.query('BEGIN')
    await connection.query('SET LOCAL enable_seqscan = off')
    const result = await connection.query(`EXPLAIN (COSTS OFF) ${sql}`, params)
    await connection.query('ROLLBACK')
    const plan = result.rows.map((row: Record<string, unknown>) => String(row['QUERY PLAN'] ?? '')).join('\n')
    return {
      label,
      plan,
      usesExpectedIndex: expectedIndexes.some((indexName) => plan.includes(indexName)),
      hasSeqScan: /\bSeq Scan\b/i.test(plan),
      expectedIndexes
    }
  } catch (error) {
    await connection.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection.release()
  }
}

async function chooseUnusedHotDates(days: number): Promise<string[]> {
  const base = '2098-01-01'
  for (let offset = 0; offset < 700; offset += 1) {
    const dates = Array.from({ length: days }, (_item, index) => addIsoDateDays(base, offset + index))
    const available = await datesAvailable(dates)
    if (available) return dates
  }
  throw new Error('无法为 usage 热数据压测选择空闲 future 分区日期')
}

async function chooseUnusedCleanupDate(): Promise<string> {
  const partitions = await listPostgresUsageRecordPartitions(client)
  const earliest = partitions.map((partition) => partition.startDate).sort()[0] ?? '2000-01-01'
  for (let offset = 30; offset < 700; offset += 1) {
    const candidate = addIsoDateDays(earliest, -offset)
    if (candidate < '1900-01-01') break
    if (await datesAvailable([candidate])) return candidate
  }
  throw new Error(`无法为 usage 保留期清理压测选择空闲历史分区日期，当前最早分区：${earliest}`)
}

async function datesAvailable(dates: string[]): Promise<boolean> {
  for (const date of dates) {
    const partitionName = postgresUsageRecordPartitionName(isoDateToDateKey(date))
    if (await tableExists('juhe_usage', partitionName)) return false
  }
  return true
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

async function readRelationSizes(tables: Array<readonly [string, string]>): Promise<RelationSize[]> {
  const result: RelationSize[] = []
  for (const [schema, table] of tables) {
    if (!await tableExists(schema, table)) continue
    const row = await pool.query(`
      SELECT
        pg_relation_size($1::regclass) AS table_bytes,
        pg_indexes_size($1::regclass) AS index_bytes,
        pg_total_relation_size($1::regclass) AS total_bytes,
        COALESCE(reltuples, 0) AS estimated_rows
      FROM pg_class
      WHERE oid = $1::regclass
    `, [`${quoteIdentifier(schema)}.${quoteIdentifier(table)}`])
    const value = row.rows[0] ?? {}
    result.push({
      schema,
      table,
      tableBytes: numberValue(value.table_bytes),
      indexBytes: numberValue(value.index_bytes),
      totalBytes: numberValue(value.total_bytes),
      estimatedRows: Math.max(0, Math.round(numberValue(value.estimated_rows)))
    })
  }
  return result
}

async function analyzeTables(tables: Array<readonly [string, string]>): Promise<void> {
  for (const [schema, table] of tables) {
    if (!await tableExists(schema, table)) continue
    await pool.query(`ANALYZE ${quoteIdentifier(schema)}.${quoteIdentifier(table)}`)
  }
}

async function cleanupPressureArtifacts(): Promise<void> {
  for (const partition of [...hotPartitions, ...(cleanupPartitionName ? [cleanupPartitionName] : [])]) {
    await pool.query(`DROP TABLE IF EXISTS juhe_usage.${quoteIdentifier(partition)}`)
  }
  await pool.query('DELETE FROM juhe_stats.usage_scope_range_windows WHERE system_account_id LIKE $1', [`${systemAccountPrefix}_%`])
  await pool.query('DELETE FROM juhe_stats.usage_range_window_requests WHERE system_account_id LIKE $1', [`${systemAccountPrefix}_%`])
}

async function countCatalogMarkerEntries(): Promise<number> {
  const result = await pool.query('SELECT COUNT(*) AS count FROM juhe_usage.usage_record_shard_entries WHERE usage_id LIKE $1', [`%${marker}%`])
  return numberValue(result.rows[0]?.count)
}

async function countRangeWindowRows(): Promise<number> {
  const result = await pool.query('SELECT COUNT(*) AS count FROM juhe_stats.usage_scope_range_windows WHERE system_account_id LIKE $1', [`${systemAccountPrefix}_%`])
  return numberValue(result.rows[0]?.count)
}

async function tableExists(schemaName: string, tableName: string): Promise<boolean> {
  const result = await pool.query(`
    SELECT EXISTS (
      SELECT 1
      FROM pg_class child
      JOIN pg_namespace namespace ON namespace.oid = child.relnamespace
      WHERE namespace.nspname = $1
        AND child.relname = $2
        AND child.relkind IN ('r', 'p')
    ) AS exists
  `, [schemaName, tableName])
  return result.rows[0]?.exists === true
}

async function sampleRedis(): Promise<RedisReport> {
  const [cachePingMs, statePingMs, queuePingMs] = await Promise.all([
    pingRedis(runtimeConfig.redis.cacheUrl),
    pingRedis(runtimeConfig.redis.stateUrl),
    pingRedis(runtimeConfig.redis.queueUrl)
  ])
  return { cachePingMs, statePingMs, queuePingMs }
}

async function pingRedis(url: string | undefined): Promise<number> {
  assert.ok(url, 'usage 热数据压测需要 Redis URL')
  const redisClient = await createDedicatedRedisClient(url)
  const started = performance.now()
  try {
    const result = await redisClient.sendCommand(['PING'])
    assert.equal(result, 'PONG', 'Redis PING 应返回 PONG')
    return round(performance.now() - started)
  } finally {
    await redisClient.quit?.().catch(() => undefined)
    try {
      redisClient.destroy?.()
    } catch {
      // node-redis throws when destroy() is called after a clean quit().
    }
  }
}

async function queryDeadlocks(): Promise<number> {
  const result = await pool.query(`
    SELECT deadlocks
    FROM pg_stat_database
    WHERE datname = current_database()
    LIMIT 1
  `)
  return numberValue(result.rows[0]?.deadlocks)
}

function summarizeQueries(metrics: QueryMetric[]): PressureReport['queries'] {
  const operations: Record<string, OperationSummary> = {}
  for (const operation of new Set(metrics.map((metric) => metric.operation))) {
    operations[operation] = summarizeOperation(metrics.filter((metric) => metric.operation === operation))
  }
  const total = summarizeOperation(metrics)
  const usageQueryP95Ms = percentile(metrics
    .filter((metric) => metric.operation.startsWith('usage:'))
    .map((metric) => metric.latencyMs), 0.95)
  const rangeQueryP95Ms = percentile(metrics
    .filter((metric) => metric.operation.startsWith('range-window:'))
    .map((metric) => metric.latencyMs), 0.95)
  return {
    total,
    operations,
    usageQueryP95Ms,
    rangeQueryP95Ms
  }
}

function summarizeOperation(metrics: QueryMetric[]): OperationSummary {
  const latencies = metrics.map((metric) => metric.latencyMs)
  return {
    count: metrics.length,
    ok: metrics.filter((metric) => metric.ok).length,
    errors: metrics.filter((metric) => !metric.ok).length,
    p50Ms: percentile(latencies, 0.50),
    p95Ms: percentile(latencies, 0.95),
    p99Ms: percentile(latencies, 0.99),
    maxMs: percentile(latencies, 1),
    averageRows: round(metrics.reduce((total, metric) => total + metric.rows, 0) / Math.max(metrics.length, 1))
  }
}

function collectViolations(input: {
  querySummary: PressureReport['queries']
  explain: ExplainReport[]
  deadlocksDelta: number
  catalogMarkerEntries: number
  cleanupDeletedRows: number
  partitionRemoved: boolean
}): string[] {
  const violations: string[] = []
  if (input.querySummary.total.errors > 0) {
    violations.push(`query errors ${input.querySummary.total.errors} > 0`)
  }
  if (input.querySummary.usageQueryP95Ms > config.maxAllowedUsageQueryP95Ms) {
    violations.push(`usage query p95 ${input.querySummary.usageQueryP95Ms}ms > ${config.maxAllowedUsageQueryP95Ms}ms`)
  }
  if (input.querySummary.rangeQueryP95Ms > config.maxAllowedRangeQueryP95Ms) {
    violations.push(`range window query p95 ${input.querySummary.rangeQueryP95Ms}ms > ${config.maxAllowedRangeQueryP95Ms}ms`)
  }
  if (input.deadlocksDelta > config.maxAllowedDeadlocks) {
    violations.push(`PostgreSQL deadlocks delta ${input.deadlocksDelta} > ${config.maxAllowedDeadlocks}`)
  }
  for (const explain of input.explain) {
    if (explain.hasSeqScan) {
      violations.push(`${explain.label} has Seq Scan`)
    }
    if (!explain.usesExpectedIndex) {
      violations.push(`${explain.label} did not use expected index (${explain.expectedIndexes.join(' / ')})`)
    }
  }
  if (input.catalogMarkerEntries !== 0) {
    violations.push(`PG usage catalog marker entries ${input.catalogMarkerEntries} should be 0`)
  }
  if (input.cleanupDeletedRows !== config.cleanupRows) {
    violations.push(`retention drop deletedRows ${input.cleanupDeletedRows} !== cleanupRows ${config.cleanupRows}`)
  }
  if (!input.partitionRemoved) {
    violations.push('retention cleanup partition still exists after DETACH/DROP')
  }
  return violations
}

function loadConfig(): PressureConfig {
  const reportPath = process.env.JUHE_USAGE_PRESSURE_REPORT_PATH?.trim()
  return {
    usageRows: intEnv('JUHE_USAGE_PRESSURE_ROWS', 200_000, 1, 5_000_000),
    usageUsers: intEnv('JUHE_USAGE_PRESSURE_USERS', 200, 1, 50_000),
    usageDays: intEnv('JUHE_USAGE_PRESSURE_DAYS', 3, 1, 31),
    cleanupRows: intEnv('JUHE_USAGE_PRESSURE_CLEANUP_ROWS', 50_000, 1, 2_000_000),
    rangeWindowRows: intEnv('JUHE_USAGE_PRESSURE_RANGE_WINDOW_ROWS', 100_000, 1, 2_000_000),
    batchSize: intEnv('JUHE_USAGE_PRESSURE_BATCH_SIZE', 1000, 1, 3000),
    queryIterations: intEnv('JUHE_USAGE_PRESSURE_QUERY_ITERATIONS', 3000, 1, 200_000),
    queryConcurrency: intEnv('JUHE_USAGE_PRESSURE_QUERY_CONCURRENCY', 32, 1, 500),
    cleanup: boolEnv('JUHE_USAGE_PRESSURE_CLEANUP', true),
    maxAllowedDeadlocks: intEnv('JUHE_USAGE_PRESSURE_MAX_DEADLOCKS', 0, 0, 1000),
    maxAllowedUsageQueryP95Ms: numberEnv('JUHE_USAGE_PRESSURE_MAX_USAGE_QUERY_P95_MS', 1200, 1, 120_000),
    maxAllowedRangeQueryP95Ms: numberEnv('JUHE_USAGE_PRESSURE_MAX_RANGE_QUERY_P95_MS', 1200, 1, 120_000),
    ...(reportPath ? { reportPath: resolve(reportPath) } : {})
  }
}

function outputReport(report: PressureReport): void {
  const text = JSON.stringify(report, null, 2)
  if (report.config.reportPath) {
    mkdirSync(dirname(report.config.reportPath), { recursive: true })
    writeFileSync(report.config.reportPath, `${text}\n`, 'utf8')
  }
  console.log(text)
}

async function timed<T>(run: () => Promise<T>): Promise<TimedResult<T>> {
  const started = performance.now()
  const value = await run()
  return {
    durationMs: performance.now() - started,
    value
  }
}

function placeholders(rowCount: number, columnCount: number): string {
  return Array.from({ length: rowCount }, (_row, rowIndex) => {
    const values = Array.from({ length: columnCount }, (_column, columnIndex) => `$${rowIndex * columnCount + columnIndex + 1}`)
    return `(${values.join(', ')})`
  }).join(', ')
}

function intEnv(name: string, fallback: number, min: number, max: number): number {
  const parsed = Number(process.env[name])
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, Math.trunc(parsed)))
}

function numberEnv(name: string, fallback: number, min: number, max: number): number {
  const parsed = Number(process.env[name])
  if (!Number.isFinite(parsed)) return fallback
  return Math.max(min, Math.min(max, parsed))
}

function boolEnv(name: string, fallback: boolean): boolean {
  const value = process.env[name]?.trim().toLowerCase()
  if (!value) return fallback
  if (['1', 'true', 'yes', 'on'].includes(value)) return true
  if (['0', 'false', 'no', 'off'].includes(value)) return false
  return fallback
}

function usagePartitionStartDateFromName(value: unknown): string | undefined {
  const match = /^usage_records_(\d{4})(\d{2})(\d{2})$/.exec(String(value ?? '').trim())
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

function textPrefixUpperBound(value: string): string {
  return `${value}\uffff`
}

function lookupAccountScopeId(index: number): string {
  const userIndex = index % config.usageUsers
  for (let candidate = userIndex; candidate < config.rangeWindowRows; candidate += config.usageUsers) {
    if (candidate % 3 === 0) {
      return `account_${marker}_${candidate}`
    }
  }
  return `account_${marker}_0`
}

function percentile(samples: number[], p: number): number {
  if (!samples.length) return 0
  const ordered = [...samples].sort((a, b) => a - b)
  const index = Math.min(ordered.length - 1, Math.max(0, Math.ceil(ordered.length * p) - 1))
  return round(ordered[index] ?? 0)
}

function numberValue(value: unknown): number {
  if (typeof value === 'number') return Number.isFinite(value) ? value : 0
  if (typeof value === 'bigint') return Number(value)
  if (typeof value === 'string') {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : 0
  }
  return 0
}

function round(value: number): number {
  return Math.round(value * 100) / 100
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
