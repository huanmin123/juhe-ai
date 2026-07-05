import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'
import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { UsageRecordInput } from '../../storage/usage-records.repository.js'

interface PressureResult {
  name: string
  operations: number
  durationMs: number
  opsPerSecond: number
  details?: Record<string, unknown>
}

const tempRoot = resolve(tmpdir(), `juhe-ai-sqlite-pressure-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const rawRows = envInteger('JUHE_AI_SQLITE_PRESSURE_RAW_ROWS', 10_000, 1, 1_000_000)
const usageRows = envInteger('JUHE_AI_SQLITE_PRESSURE_USAGE_ROWS', 5_000, 1, 1_000_000)
const codexRows = envInteger('JUHE_AI_SQLITE_PRESSURE_CODEX_ROWS', 2_000, 1, 1_000_000)
const batchSize = envInteger('JUHE_AI_SQLITE_PRESSURE_BATCH_SIZE', 500, 1, 10_000)
const shardCounts = integerList(process.env.JUHE_AI_SQLITE_PRESSURE_SHARDS ?? '8,16,32')

runtimeConfig.secret = 'sqlite-pressure-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
logger.level = 'silent'
mkdirSync(tempRoot, { recursive: true })

const databaseModule = await import('../../storage/database.js')
const usageRecords = await import('../../storage/usage-records.repository.js')
const codexState = await import('../../storage/codex-context-state-writer-pool.js')
const usageWriterPool = await import('../../storage/usage-record-writer-pool.js')

try {
  const results: PressureResult[] = []
  results.push(await runMainDatabasePressure('business', 'db-service', () => databaseModule.getBusinessDatabase()))
  databaseModule.closeStorageDatabases()
  results.push(await runMainDatabasePressure('dataset', 'worker', () => {
    runtimeConfig.workerRole = 'ingest-worker'
    return databaseModule.getDatasetDatabase()
  }))
  databaseModule.closeStorageDatabases()
  results.push(await runMainDatabasePressure('usage-catalog', 'worker', () => {
    runtimeConfig.workerRole = 'ingest-worker'
    return databaseModule.getUsageCatalogDatabase()
  }))
  databaseModule.closeStorageDatabases()
  results.push(await runMainDatabasePressure('stats', 'worker', () => {
    runtimeConfig.workerRole = 'stats-worker'
    return databaseModule.getStatsDatabase()
  }))
  databaseModule.closeStorageDatabases()

  for (const shardCount of shardCounts) {
    results.push(await runUsageShardPressure(shardCount, 0))
    await usageWriterPool.closeUsageRecordWriterPool()
    databaseModule.closeStorageDatabases()
    results.push(await runUsageShardPressure(shardCount, Math.min(shardCount, 8)))
    await usageWriterPool.closeUsageRecordWriterPool()
    databaseModule.closeStorageDatabases()
  }

  for (const shardCount of shardCounts) {
    results.push(await runCodexContextPressure(shardCount, 0))
    await codexState.closeCodexContextStateWriterPool()
    databaseModule.closeStorageDatabases()
    results.push(await runCodexContextPressure(shardCount, Math.min(shardCount, 16)))
    await codexState.closeCodexContextStateWriterPool()
    databaseModule.closeStorageDatabases()
  }

  console.log(JSON.stringify({
    tempRoot,
    rawRows,
    usageRows,
    codexRows,
    batchSize,
    shardCounts,
    results
  }, null, 2))
} finally {
  await usageWriterPool.closeUsageRecordWriterPool()
  await codexState.closeCodexContextStateWriterPool()
  databaseModule.closeStorageDatabases()
  if (process.env.JUHE_AI_SQLITE_PRESSURE_KEEP_DATA !== 'true') {
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function runMainDatabasePressure(
  name: 'business' | 'dataset' | 'usage-catalog' | 'stats',
  processRole: typeof runtimeConfig.processRole,
  openDatabase: () => DatabaseSync
): Promise<PressureResult> {
  runtimeConfig.processRole = processRole
  runtimeConfig.databasePath = join(tempRoot, `${name}-business.sqlite3`)
  runtimeConfig.datasetDatabasePath = join(tempRoot, `${name}-dataset.sqlite3`)
  runtimeConfig.usageCatalogDatabasePath = join(tempRoot, `${name}-usage-catalog.sqlite3`)
  runtimeConfig.statsDatabasePath = join(tempRoot, `${name}-stats.sqlite3`)
  const database = openDatabase()
  database.exec(`
    CREATE TABLE IF NOT EXISTS __sqlite_pressure_events (
      id TEXT PRIMARY KEY,
      bucket TEXT NOT NULL,
      value INTEGER NOT NULL,
      payload TEXT NOT NULL,
      created_at TEXT NOT NULL
    );
  `)
  const statement = database.prepare(`
    INSERT INTO __sqlite_pressure_events (id, bucket, value, payload, created_at)
    VALUES (?, ?, ?, ?, ?)
    ON CONFLICT(id) DO UPDATE SET value = excluded.value, payload = excluded.payload, created_at = excluded.created_at
  `)
  const durationMs = measure(() => {
    for (let offset = 0; offset < rawRows; offset += batchSize) {
      const transactionStarted = databaseModule.beginDatabaseTransaction(database)
      try {
        for (let index = offset; index < Math.min(rawRows, offset + batchSize); index += 1) {
          statement.run(`${name}_${index}`, `bucket_${index % 64}`, index, `payload_${index}`, new Date(1_779_000_000_000 + index).toISOString())
        }
        databaseModule.commitDatabaseTransaction(database, transactionStarted)
      } catch (error) {
        databaseModule.rollbackDatabaseTransaction(database, transactionStarted)
        throw error
      }
    }
  })
  return result(`${name}_raw_sqlite_writes`, rawRows, durationMs, {
    database: name
  })
}

async function runUsageShardPressure(shardCount: number, poolSize: number): Promise<PressureResult> {
  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ingest-worker'
  runtimeConfig.databasePath = join(tempRoot, `usage-${shardCount}-${poolSize}-business.sqlite3`)
  runtimeConfig.datasetDatabasePath = join(tempRoot, `usage-${shardCount}-${poolSize}-dataset.sqlite3`)
  runtimeConfig.usageCatalogDatabasePath = join(tempRoot, `usage-${shardCount}-${poolSize}-usage-catalog.sqlite3`)
  runtimeConfig.statsDatabasePath = join(tempRoot, `usage-${shardCount}-${poolSize}-stats.sqlite3`)
  runtimeConfig.usageShardRoot = join(tempRoot, `usage-${shardCount}-${poolSize}-shards`)
  runtimeConfig.usageShardCount = shardCount
  runtimeConfig.usageRecordWriterPoolEnabled = poolSize > 0
  runtimeConfig.usageRecordWriterPoolSize = poolSize
  runtimeConfig.usageRecordWriterQueueMaxItems = Math.max(5000, usageRows * 2)
  const durationMs = poolSize > 0 ? await measureAsync(async () => {
    await Promise.all(usageRecordBatches(shardCount).map(async (batch) => {
      await usageRecords.createUsageRecordsBatchAsync(batch)
    }))
  }) : measure(() => {
    for (const batch of usageRecordBatches(shardCount)) {
      usageRecords.createUsageRecordsBatch(batch)
    }
  })
  const runtime = usageWriterPool.getUsageRecordWriterPoolRuntime()
  await usageWriterPool.closeUsageRecordWriterPool()
  return result('usage_shard_repository_writes', usageRows, durationMs, {
    shardCount,
    poolSize,
    runtime
  })
}

function usageRecordBatches(shardCount: number): UsageRecordBatch[] {
  const batches: UsageRecordBatch[] = []
  for (let offset = 0; offset < usageRows; offset += batchSize) {
    batches.push(Array.from({ length: Math.min(batchSize, usageRows - offset) }, (_, localIndex) => {
      const index = offset + localIndex
      const createdAt = new Date(1_779_100_000_000 + index).toISOString()
      return {
        id: `usage_20260622_s${String(index % shardCount).padStart(3, '0')}_${index}_pressure`,
        systemAccountId: 'sys_pressure',
        traceId: `trace_usage_pressure_${index}`,
        trafficSource: 'gateway',
        endpoint: '/v1/chat/completions',
        providerCode: 'openai',
        model: 'pressure-model',
        stream: index % 2 === 0,
        statusCode: 200,
        success: true,
        durationMs: 100 + (index % 50),
        inputTokens: 100,
        outputTokens: 50,
        costUsd: 0.001,
        createdAt
      } satisfies UsageRecordInput
    }))
  }
  return batches
}

type UsageRecordBatch = UsageRecordInput[]

async function runCodexContextPressure(shardCount: number, poolSize: number): Promise<PressureResult> {
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.workerRole = 'ingest-worker'
  runtimeConfig.databasePath = join(tempRoot, `codex-${shardCount}-${poolSize}-business.sqlite3`)
  runtimeConfig.datasetDatabasePath = join(tempRoot, `codex-${shardCount}-${poolSize}-dataset.sqlite3`)
  runtimeConfig.usageCatalogDatabasePath = join(tempRoot, `codex-${shardCount}-${poolSize}-usage-catalog.sqlite3`)
  runtimeConfig.statsDatabasePath = join(tempRoot, `codex-${shardCount}-${poolSize}-stats.sqlite3`)
  runtimeConfig.codexContextRoot = join(tempRoot, `codex-${shardCount}-${poolSize}`)
  runtimeConfig.codexContextStateShardRoot = join(tempRoot, `codex-${shardCount}-${poolSize}`, 'state-shards')
  runtimeConfig.codexContextStateShardCount = shardCount
  runtimeConfig.codexContextStateWriterPoolEnabled = poolSize > 0
  runtimeConfig.codexContextStateWriterPoolSize = poolSize
  runtimeConfig.codexContextStateWriterQueueMaxItems = Math.max(5000, codexRows * 4)
  const startedAt = performance.now()
  const writes: Array<Promise<unknown>> = []
  for (let index = 0; index < codexRows; index += 1) {
    writes.push(codexState.saveCodexContextResponseStateIndexWithWriterPool({
      responseId: `resp_pressure_${shardCount}_${poolSize}_${index}`,
      sessionId: `session_pressure_${index % Math.max(1, Math.floor(codexRows / 16))}`,
      previousResponseId: undefined,
      systemAccountId: 'sys_pressure',
      groupId: 'group_pressure',
      providerCode: 'openai',
      upstreamAccountId: 'acct_pressure',
      model: 'pressure-model',
      upstreamModel: 'pressure-model',
      storageKey: `sessions/session_pressure_${index}/segments/2026062200.json.gz`,
      storageOffsetBytes: 0,
      sha256: '0'.repeat(64),
      rawSizeBytes: 100,
      compressedSizeBytes: 80,
      compression: 'gzip',
      schemaVersion: 1,
      createdAt: new Date(1_779_200_000_000 + index).toISOString(),
      expiresAt: new Date(1_779_300_000_000 + index).toISOString()
    }))
  }
  await Promise.all(writes)
  const runtime = codexState.getCodexContextStateWriterPoolRuntime()
  await codexState.closeCodexContextStateWriterPool()
  return result('codex_context_state_writes', codexRows, performance.now() - startedAt, {
    shardCount,
    poolSize,
    runtime
  })
}

function measure(run: () => void): number {
  const startedAt = performance.now()
  run()
  return performance.now() - startedAt
}

async function measureAsync(run: () => Promise<void>): Promise<number> {
  const startedAt = performance.now()
  await run()
  return performance.now() - startedAt
}

function result(name: string, operations: number, durationMs: number, details?: Record<string, unknown>): PressureResult {
  return {
    name,
    operations,
    durationMs: round(durationMs, 2),
    opsPerSecond: round(operations / Math.max(durationMs / 1000, 0.001), 2),
    details
  }
}

function envInteger(name: string, fallback: number, min: number, max: number): number {
  const value = Number(process.env[name])
  if (!Number.isFinite(value)) return fallback
  return Math.max(min, Math.min(max, Math.trunc(value)))
}

function integerList(value: string): number[] {
  const values = value
    .split(',')
    .map((item) => Number(item.trim()))
    .filter((item) => Number.isFinite(item) && item > 0)
    .map((item) => Math.trunc(item))
  return values.length ? [...new Set(values)] : [8, 16, 32]
}

function round(value: number, digits: number): number {
  const factor = 10 ** digits
  return Math.round(value * factor) / factor
}
