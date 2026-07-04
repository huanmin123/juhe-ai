import { fork } from 'node:child_process'
import { availableParallelism } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../config/runtime.js'
import { KeyedChildProcessPool } from '../shared/keyed-child-process-pool.js'
import {
  codexContextStateShardCount,
  codexContextStateShardRootPath,
  statsDatabasePath,
  usageCatalogDatabasePath
} from './database.js'
import type {
  SqliteReadWorkerOperation,
  SqliteReadWorkerOperationResult
} from './sqlite-read-worker-pool.types.js'

export interface SqliteReadWorkerPoolRuntime {
  enabled: boolean
  workerCount: number
  queueLength: number
  activeJobs: number
  handledJobs: number
  failedJobs: number
  rejectedJobs: number
  timedOutJobs: number
  restartedWorkers: number
  oldestQueuedMs: number
  maxQueueWaitMs: number
  maxRunMs: number
}

const currentModulePath = fileURLToPath(import.meta.url)
const currentModuleDir = dirname(currentModulePath)
const workerSourcePath = resolve(currentModuleDir, './sqlite-read-worker.ts')
const workerDistPath = resolve(currentModuleDir, './sqlite-read-worker.js')

const readWorkerPool = new KeyedChildProcessPool<SqliteReadWorkerOperation>({
  name: 'SQLite read',
  createWorker: createReadWorkerChild,
  targetSize: targetReadWorkerPoolSize,
  queueMaxItems: () => runtimeConfig.sqliteReadWorkerQueueMaxItems,
  shardIndexForOperation,
  operationType: (operation) => operation.type
})

export function sqliteReadWorkerPoolEnabled(): boolean {
  return runtimeConfig.databaseDriver === 'sqlite'
    && runtimeConfig.processRole === 'db-service'
}

export function getSqliteReadWorkerPoolRuntime(): SqliteReadWorkerPoolRuntime {
  const runtime = readWorkerPool.runtime()
  return {
    enabled: sqliteReadWorkerPoolEnabled(),
    workerCount: runtime.workerCount,
    queueLength: runtime.queueLength,
    activeJobs: runtime.activeJobs,
    handledJobs: runtime.handledJobs,
    failedJobs: runtime.failedJobs,
    rejectedJobs: runtime.rejectedJobs,
    timedOutJobs: runtime.timedOutJobs,
    restartedWorkers: runtime.restartedWorkers,
    oldestQueuedMs: runtime.oldestQueuedMs,
    maxQueueWaitMs: runtime.maxQueueWaitMs,
    maxRunMs: runtime.maxRunMs
  }
}

export async function closeSqliteReadWorkerPool(): Promise<void> {
  await readWorkerPool.close()
}

export async function requestSqliteReadWorker<T extends SqliteReadWorkerOperation>(
  operation: T
): Promise<SqliteReadWorkerOperationResult<T>> {
  if (!sqliteReadWorkerPoolEnabled()) {
    throw new Error(`SQLite read worker pool 未启用，不能投递 ${operation.type}`)
  }
  return await readWorkerPool.request(operation) as SqliteReadWorkerOperationResult<T>
}

function targetReadWorkerPoolSize(): number {
  const configured = Math.trunc(runtimeConfig.sqliteReadWorkerPoolSize)
  const fallback = Math.max(2, Math.min(availableParallelism(), 4))
  return Math.max(1, Math.min(configured > 0 ? configured : fallback, 64))
}

function createReadWorkerChild() {
  return fork(resolveReadWorkerPath(), [], {
    execArgv: readWorkerExecArgv(),
    env: readWorkerEnv(),
    stdio: ['ignore', 'ignore', 'ignore', 'ipc']
  })
}

function resolveReadWorkerPath(): string {
  return currentModulePath.endsWith('.ts') ? workerSourcePath : workerDistPath
}

function readWorkerExecArgv(): string[] {
  const execArgv = process.execArgv.filter((arg) => !arg.startsWith('--inspect'))
  if (!currentModulePath.endsWith('.ts') || execArgv.some((arg) => arg.includes('tsx'))) {
    return execArgv
  }
  return [...execArgv, '--import', 'tsx']
}

function readWorkerEnv(): NodeJS.ProcessEnv {
  return {
    ...process.env,
    JUHE_AI_RUNTIME_MODE: runtimeConfig.runtimeMode,
    JUHE_AI_PROCESS_ROLE: 'worker',
    JUHE_AI_WORKER_ROLE: 'ops-worker',
    JUHE_AI_DATABASE_DRIVER: 'sqlite',
    JUHE_AI_CACHE_DRIVER: runtimeConfig.cacheDriver,
    JUHE_AI_RUNTIME_STATE_DRIVER: runtimeConfig.runtimeStateDriver,
    JUHE_AI_QUEUE_DRIVER: runtimeConfig.queueDriver,
    JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath,
    JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath,
    JUHE_AI_USAGE_CATALOG_DATABASE_PATH: usageCatalogDatabasePath(),
    JUHE_AI_STATS_DATABASE_PATH: statsDatabasePath(),
    JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot,
    JUHE_AI_USAGE_SHARD_COUNT: String(runtimeConfig.usageShardCount),
    JUHE_AI_CODEX_CONTEXT_ROOT: runtimeConfig.codexContextRoot,
    JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: codexContextStateShardRootPath(),
    JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT: String(codexContextStateShardCount()),
    JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT: 'true',
    JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE: String(runtimeConfig.sqliteReadWorkerPoolSize),
    JUHE_AI_SQLITE_READ_WORKER_QUEUE_MAX_ITEMS: String(runtimeConfig.sqliteReadWorkerQueueMaxItems),
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

function shardIndexForOperation(operation: SqliteReadWorkerOperation): number {
  return hashText(JSON.stringify(operation))
}

function hashText(text: string): number {
  let hash = 2166136261
  for (let index = 0; index < text.length; index += 1) {
    hash ^= text.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return hash >>> 0
}
