import { fork } from 'node:child_process'
import { availableParallelism } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../config/runtime.js'
import { KeyedChildProcessPool } from '../shared/keyed-child-process-pool.js'
import {
  usageCatalogDatabasePath,
} from './database.js'
import {
  usageRecordShardCount,
  writeUsageRecordShardRows,
  type UsageRecordShardLocation,
  type UsageRecordShardWriteResult,
  type UsageRecordShardWriteRow
} from './usage-record-shards.js'
import type {
  UsageRecordWriterOperation,
  UsageRecordWriterOperationResult
} from './usage-record-writer-pool.types.js'

export interface UsageRecordWriterPoolRuntime {
  enabled: boolean
  workerCount: number
  queueLength: number
  activeJobs: number
  handledJobs: number
  failedJobs: number
  rejectedJobs: number
  oldestQueuedMs: number
  maxQueueWaitMs: number
  maxRunMs: number
}

const currentModulePath = fileURLToPath(import.meta.url)
const currentModuleDir = dirname(currentModulePath)
const workerSourcePath = resolve(currentModuleDir, './usage-record-writer-worker.ts')
const workerDistPath = resolve(currentModuleDir, './usage-record-writer-worker.js')

const writerPool = new KeyedChildProcessPool<UsageRecordWriterOperation>({
  name: 'Usage record',
  createWorker: createWriterChild,
  targetSize: targetWriterPoolSize,
  queueMaxItems: () => runtimeConfig.usageRecordWriterQueueMaxItems,
  shardIndexForOperation: (operation) => operation.location.shardId,
  operationType: (operation) => operation.type
})

export function usageRecordWriterPoolEnabled(): boolean {
  return runtimeConfig.databaseDriver === 'sqlite'
    && runtimeConfig.processRole === 'worker'
    && runtimeConfig.workerRole === 'ingest-worker'
    && runtimeConfig.usageRecordWriterPoolEnabled
    && usageRecordShardCount() > 1
}

export function getUsageRecordWriterPoolRuntime(): UsageRecordWriterPoolRuntime {
  const runtime = writerPool.runtime()
  return {
    enabled: usageRecordWriterPoolEnabled(),
    workerCount: runtime.workerCount,
    queueLength: runtime.queueLength,
    activeJobs: runtime.activeJobs,
    handledJobs: runtime.handledJobs,
    failedJobs: runtime.failedJobs,
    rejectedJobs: runtime.rejectedJobs,
    oldestQueuedMs: runtime.oldestQueuedMs,
    maxQueueWaitMs: runtime.maxQueueWaitMs,
    maxRunMs: runtime.maxRunMs
  }
}

export async function closeUsageRecordWriterPool(): Promise<void> {
  await writerPool.close()
}

export async function writeUsageRecordShardRowsWithWriterPool(
  location: UsageRecordShardLocation,
  rows: UsageRecordShardWriteRow[]
): Promise<UsageRecordShardWriteResult> {
  if (!usageRecordWriterPoolEnabled()) {
    return writeUsageRecordShardRows(location, rows)
  }
  return await requestUsageRecordWriter({
    type: 'write_usage_records',
    location,
    rows
  })
}

export async function requestUsageRecordWriter<T extends UsageRecordWriterOperation>(
  operation: T
): Promise<UsageRecordWriterOperationResult<T>> {
  if (!usageRecordWriterPoolEnabled()) {
    return runUsageRecordWriterOperationLocally(operation) as UsageRecordWriterOperationResult<T>
  }
  return await writerPool.request(operation) as UsageRecordWriterOperationResult<T>
}

function runUsageRecordWriterOperationLocally(operation: UsageRecordWriterOperation): unknown {
  if (operation.type === 'write_usage_records') {
    return writeUsageRecordShardRows(operation.location, operation.rows)
  }
  throw new Error(`未知 usage record writer 操作：${JSON.stringify(operation)}`)
}

function targetWriterPoolSize(): number {
  const configured = Math.trunc(runtimeConfig.usageRecordWriterPoolSize)
  const fallback = Math.min(usageRecordShardCount(), Math.max(2, Math.min(availableParallelism(), 8)))
  return Math.max(1, Math.min(configured > 0 ? configured : fallback, usageRecordShardCount(), 64))
}

function createWriterChild() {
  return fork(resolveWriterWorkerPath(), [], {
    execArgv: writerWorkerExecArgv(),
    env: writerWorkerEnv(),
    stdio: ['ignore', 'ignore', 'ignore', 'ipc']
  })
}

function resolveWriterWorkerPath(): string {
  return currentModulePath.endsWith('.ts') ? workerSourcePath : workerDistPath
}

function writerWorkerExecArgv(): string[] {
  const execArgv = process.execArgv.filter((arg) => !arg.startsWith('--inspect'))
  if (!currentModulePath.endsWith('.ts') || execArgv.some((arg) => arg.includes('tsx'))) {
    return execArgv
  }
  return [...execArgv, '--import', 'tsx']
}

function writerWorkerEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    JUHE_AI_PROCESS_ROLE: 'worker',
    JUHE_AI_WORKER_ROLE: 'ingest-worker',
    JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath,
    JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath,
    JUHE_AI_USAGE_CATALOG_DATABASE_PATH: usageCatalogDatabasePath(),
    JUHE_AI_STATS_DATABASE_PATH: runtimeConfig.statsDatabasePath,
    JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot,
    JUHE_AI_USAGE_SHARD_COUNT: String(runtimeConfig.usageShardCount),
    JUHE_AI_USAGE_RECORD_WRITER_POOL_ENABLED: 'false',
    JUHE_AI_USAGE_RECORD_WRITER_POOL_SIZE: String(runtimeConfig.usageRecordWriterPoolSize),
    JUHE_AI_USAGE_RECORD_WRITER_QUEUE_MAX_ITEMS: String(runtimeConfig.usageRecordWriterQueueMaxItems),
    JUHE_AI_SECRET: runtimeConfig.secret,
    JUHE_AI_LOG_LEVEL: runtimeConfig.log.level,
    JUHE_AI_LOG_DIR: runtimeConfig.log.directory,
    JUHE_AI_LOG_FILE_ENABLED: runtimeConfig.log.fileEnabled ? 'true' : 'false',
    JUHE_AI_LOG_CONSOLE_ENABLED: runtimeConfig.log.consoleEnabled ? 'true' : 'false',
    JUHE_AI_LOG_MAX_FILE_MB: String(Math.max(1, Math.round(runtimeConfig.log.maxFileBytes / 1024 / 1024))),
    JUHE_AI_LOG_RETENTION_DAYS: String(runtimeConfig.log.retentionDays),
    JUHE_AI_LOG_MAX_FILES: String(runtimeConfig.log.maxFiles),
    JUHE_AI_LOG_CLEANUP_INTERVAL_MINUTES: String(runtimeConfig.log.cleanupIntervalMinutes)
  }
}
