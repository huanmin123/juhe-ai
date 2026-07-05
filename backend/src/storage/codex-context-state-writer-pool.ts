import { fork } from 'node:child_process'
import { availableParallelism } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../config/runtime.js'
import { KeyedBatchBuffer } from '../shared/keyed-batch-buffer.js'
import { KeyedChildProcessPool } from '../shared/keyed-child-process-pool.js'
import { errorLogFields, logger } from '../shared/logger.js'
import {
  codexContextStateShardCount,
  codexContextStateShardIndexForKey,
  nowIso,
  usageCatalogDatabasePath
} from './database.js'
import type {
  CodexContextCompactReadResult,
  CodexContextCompactStateIndex,
  CodexContextCompactStateIndexInput,
  CodexContextExpiredStateCleanupResult,
  CodexContextResponseChainReadResult,
  CodexContextResponseStateIndex,
  CodexContextResponseStateIndexInput,
  CodexContextStateBoundary
} from './codex-context-state.repository.js'
import {
  cleanupExpiredCodexContextStates,
  cleanupExpiredCodexContextStatesInShard,
  normalizeCodexContextCompactStateIndexInput,
  normalizeCodexContextResponseStateIndexInput,
  readCodexContextCompactState,
  readCodexContextCompactStateRow,
  readCodexContextResponseStateChain,
  readCodexContextResponseStateRow,
  saveCodexContextCompactStateIndex,
  saveCodexContextCompactStateIndexRows,
  saveCodexContextCompactStateIndexRow,
  saveCodexContextResponseStateIndex,
  saveCodexContextResponseStateIndexRows,
  saveCodexContextResponseStateIndexRow,
  touchCodexContextCompactStateRows,
  touchCodexContextCompactStateRow,
  touchCodexContextSessionStates,
  touchCodexContextResponseStateRows,
  touchCodexContextSessionState,
  upsertCodexContextCompactSessionIndexes,
  upsertCodexContextCompactSessionIndex,
  upsertCodexContextResponseSessionIndexes,
  upsertCodexContextResponseSessionIndex
} from './codex-context-state.repository.js'
import type {
  CodexContextStateWriterOperation,
  CodexContextStateWriterOperationResult
} from './codex-context-state-writer-pool.types.js'

export interface CodexContextStateWriterPoolRuntime {
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
  batchKeyCount: number
  batchItemCount: number
  flushedBatches: number
  flushedBatchItems: number
  failedBatches: number
}

const currentModulePath = fileURLToPath(import.meta.url)
const currentModuleDir = dirname(currentModulePath)
const workerSourcePath = resolve(currentModuleDir, './codex-context-state-writer-worker.ts')
const workerDistPath = resolve(currentModuleDir, './codex-context-state-writer-worker.js')
const codexContextStateWriterOperationTypes = new Set<CodexContextStateWriterOperation['type']>([
  'save_response_session',
  'save_response_sessions',
  'save_response_row',
  'save_response_rows',
  'save_compact_session',
  'save_compact_sessions',
  'save_compact_row',
  'save_compact_rows',
  'read_response_row',
  'read_compact_row',
  'touch_session',
  'touch_sessions',
  'touch_response_rows',
  'touch_compact_row',
  'touch_compact_rows',
  'cleanup_expired_states',
  'cleanup_expired_states_shard'
])
const writerBatchDelayMs = 2
const writerBatchMaxItems = 256
const writerPool = new KeyedChildProcessPool<CodexContextStateWriterOperation>({
  name: 'Responses bridge state index',
  createWorker: createWriterChild,
  targetSize: targetWriterPoolSize,
  queueMaxItems: () => runtimeConfig.codexContextStateWriterQueueMaxItems,
  shardIndexForOperation,
  operationType: (operation) => operation.type
})
const responseSessionSaveBatch = new KeyedBatchBuffer<CodexContextResponseStateIndex>({
  name: 'Responses bridge response session save',
  maxItems: writerBatchMaxItems,
  delayMs: writerBatchDelayMs,
  flush: async (_key, rows) => {
    await requestCodexContextStateWriter({ type: 'save_response_sessions', inputs: rows })
  }
})
const responseRowSaveBatch = new KeyedBatchBuffer<CodexContextResponseStateIndex>({
  name: 'Responses bridge response row save',
  maxItems: writerBatchMaxItems,
  delayMs: writerBatchDelayMs,
  flush: async (_key, rows) => {
    await requestCodexContextStateWriter({ type: 'save_response_rows', inputs: rows })
  }
})
const compactSessionSaveBatch = new KeyedBatchBuffer<CodexContextCompactStateIndex>({
  name: 'Responses bridge compact session save',
  maxItems: writerBatchMaxItems,
  delayMs: writerBatchDelayMs,
  flush: async (_key, rows) => {
    await requestCodexContextStateWriter({ type: 'save_compact_sessions', inputs: rows })
  }
})
const compactRowSaveBatch = new KeyedBatchBuffer<CodexContextCompactStateIndex>({
  name: 'Responses bridge compact row save',
  maxItems: writerBatchMaxItems,
  delayMs: writerBatchDelayMs,
  flush: async (_key, rows) => {
    await requestCodexContextStateWriter({ type: 'save_compact_rows', inputs: rows })
  }
})
const sessionTouchBatch = new KeyedBatchBuffer<{ sessionId: string; now: string; refreshExpiresAt: string }>({
  name: 'Responses bridge session touch',
  maxItems: writerBatchMaxItems,
  delayMs: writerBatchDelayMs,
  flush: async (_key, touches) => {
    await requestCodexContextStateWriter({ type: 'touch_sessions', touches })
  }
})
const responseTouchBatch = new KeyedBatchBuffer<{ responseIds: string[]; now: string; refreshExpiresAt: string }>({
  name: 'Responses bridge response touch',
  maxItems: writerBatchMaxItems,
  delayMs: writerBatchDelayMs,
  flush: async (_key, touches) => {
    const responseIds = uniqueStrings(touches.flatMap((touch) => touch.responseIds))
    if (responseIds.length === 0) return
    await requestCodexContextStateWriter({
      type: 'touch_response_rows',
      responseIds,
      now: maxIso(touches.map((touch) => touch.now)),
      refreshExpiresAt: maxIso(touches.map((touch) => touch.refreshExpiresAt))
    })
  }
})
const compactTouchBatch = new KeyedBatchBuffer<{ compactId: string; now: string; refreshExpiresAt: string }>({
  name: 'Responses bridge compact touch',
  maxItems: writerBatchMaxItems,
  delayMs: writerBatchDelayMs,
  flush: async (_key, touches) => {
    await requestCodexContextStateWriter({ type: 'touch_compact_rows', touches })
  }
})
const writerBatches = [
  responseSessionSaveBatch,
  responseRowSaveBatch,
  compactSessionSaveBatch,
  compactRowSaveBatch,
  sessionTouchBatch,
  responseTouchBatch,
  compactTouchBatch
]
let cleanupShardCursor = 0

export function codexContextStateWriterPoolEnabled(): boolean {
  return runtimeConfig.databaseDriver === 'sqlite'
    && runtimeConfig.processRole === 'db-service'
    && runtimeConfig.codexContextStateWriterPoolEnabled
    && codexContextStateShardCount() > 1
}

export function isCodexContextStateWriterPoolOperation(operation: { type: string }): boolean {
  return codexContextStateWriterOperationTypes.has(operation.type as CodexContextStateWriterOperation['type'])
}

export function getCodexContextStateWriterPoolRuntime(): CodexContextStateWriterPoolRuntime {
  const runtime = writerPool.runtime()
  const batchRuntime = writerBatches
    .map((batch) => batch.runtime())
    .reduce((total, current) => ({
      keyCount: total.keyCount + current.keyCount,
      itemCount: total.itemCount + current.itemCount,
      flushedBatches: total.flushedBatches + current.flushedBatches,
      flushedItems: total.flushedItems + current.flushedItems,
      failedBatches: total.failedBatches + current.failedBatches
    }), {
      keyCount: 0,
      itemCount: 0,
      flushedBatches: 0,
      flushedItems: 0,
      failedBatches: 0
    })
  return {
    enabled: codexContextStateWriterPoolEnabled(),
    workerCount: runtime.workerCount,
    queueLength: runtime.queueLength,
    activeJobs: runtime.activeJobs,
    handledJobs: runtime.handledJobs,
    failedJobs: runtime.failedJobs,
    rejectedJobs: runtime.rejectedJobs,
    oldestQueuedMs: runtime.oldestQueuedMs,
    maxQueueWaitMs: runtime.maxQueueWaitMs,
    maxRunMs: runtime.maxRunMs,
    batchKeyCount: batchRuntime.keyCount,
    batchItemCount: batchRuntime.itemCount,
    flushedBatches: batchRuntime.flushedBatches,
    flushedBatchItems: batchRuntime.flushedItems,
    failedBatches: batchRuntime.failedBatches
  }
}

export async function closeCodexContextStateWriterPool(): Promise<void> {
  await Promise.allSettled(writerBatches.map(async (batch) => {
    await batch.flushAll()
    batch.resetMetrics()
  }))
  await writerPool.close()
}

export async function saveCodexContextResponseStateIndexWithWriterPool(input: CodexContextResponseStateIndexInput): Promise<CodexContextResponseStateIndex> {
  if (!codexContextStateWriterPoolEnabled()) {
    return saveCodexContextResponseStateIndex(input)
  }
  const now = input.createdAt ?? nowIso()
  const row = normalizeCodexContextResponseStateIndexInput(input, now)
  await Promise.all([
    responseSessionSaveBatch.enqueue(shardKey(row.sessionId), row),
    responseRowSaveBatch.enqueue(shardKey(row.responseId), row)
  ])
  return row
}

export async function saveCodexContextCompactStateIndexWithWriterPool(input: CodexContextCompactStateIndexInput): Promise<CodexContextCompactStateIndex> {
  if (!codexContextStateWriterPoolEnabled()) {
    return saveCodexContextCompactStateIndex(input)
  }
  const now = input.createdAt ?? nowIso()
  const row = normalizeCodexContextCompactStateIndexInput(input, now)
  await Promise.all([
    compactSessionSaveBatch.enqueue(shardKey(row.sessionId), row),
    compactRowSaveBatch.enqueue(shardKey(row.compactId), row)
  ])
  return row
}

export async function readCodexContextResponseStateChainWithWriterPool(input: {
  responseId: string
  boundary: CodexContextStateBoundary
  maxDepth?: number
  now?: string
  refreshExpiresAt?: string
}): Promise<CodexContextResponseChainReadResult> {
  if (!codexContextStateWriterPoolEnabled()) {
    return readCodexContextResponseStateChain(input)
  }

  const responseId = requiredText(input.responseId, 'responseId')
  const now = input.now ?? nowIso()
  const maxDepth = Math.max(1, Math.min(Math.trunc(input.maxDepth ?? 64), 256))
  const rows: CodexContextResponseStateIndex[] = []
  let cursor: string | undefined = responseId
  for (let depth = 0; cursor && depth < maxDepth; depth += 1) {
    const mapped: CodexContextResponseStateIndex | undefined = await requestCodexContextStateWriter({
      type: 'read_response_row',
      responseId: cursor
    })
    if (!mapped) {
      return {
        outcome: rows.length === 0 ? 'not_found' : 'chain_broken',
        responseId: cursor
      }
    }
    if (mapped.expiresAt < now) {
      return {
        outcome: 'expired',
        responseId: mapped.responseId,
        sessionId: mapped.sessionId
      }
    }
    if (!matchesBoundary(mapped, input.boundary)) {
      return {
        outcome: 'boundary_mismatch',
        responseId: mapped.responseId,
        sessionId: mapped.sessionId
      }
    }
    rows.push(mapped)
    cursor = mapped.previousResponseId
  }
  if (cursor) {
    return {
      outcome: 'chain_too_deep',
      responseId: cursor,
      sessionId: rows[0]?.sessionId
    }
  }

  const orderedRows = rows.reverse()
  touchResponseChainWithWriterPool(orderedRows, now, input.refreshExpiresAt ?? now)
  return {
    outcome: 'found',
    sessionId: orderedRows[0]?.sessionId ?? responseId,
    responses: orderedRows
  }
}

export async function readCodexContextCompactStateWithWriterPool(input: {
  compactId: string
  boundary: CodexContextStateBoundary
  now?: string
  refreshExpiresAt?: string
}): Promise<CodexContextCompactReadResult> {
  if (!codexContextStateWriterPoolEnabled()) {
    return readCodexContextCompactState(input)
  }

  const compactId = requiredText(input.compactId, 'compactId')
  const now = input.now ?? nowIso()
  const mapped: CodexContextCompactStateIndex | undefined = await requestCodexContextStateWriter({
    type: 'read_compact_row',
    compactId
  })
  if (!mapped) {
    return { outcome: 'not_found', compactId }
  }
  if (mapped.expiresAt < now) {
    return { outcome: 'expired', compactId, sessionId: mapped.sessionId }
  }
  if (!matchesBoundary(mapped, input.boundary)) {
    return { outcome: 'boundary_mismatch', compactId, sessionId: mapped.sessionId }
  }
  scheduleCodexContextTouch(compactTouchBatch.enqueue(shardKey(compactId), {
    compactId,
    now,
    refreshExpiresAt: input.refreshExpiresAt ?? now
  }), 'compact', compactId)
  scheduleCodexContextTouch(sessionTouchBatch.enqueue(shardKey(mapped.sessionId), {
    sessionId: mapped.sessionId,
    now,
    refreshExpiresAt: input.refreshExpiresAt ?? now
  }), 'session', mapped.sessionId)
  return { outcome: 'found', compact: mapped }
}

export async function cleanupExpiredCodexContextStatesWithWriterPool(input: {
  expiredBefore?: string
  limit?: number
} = {}): Promise<CodexContextExpiredStateCleanupResult> {
  if (!codexContextStateWriterPoolEnabled()) {
    return cleanupExpiredCodexContextStates(input)
  }
  const shardIndex = nextCleanupShardIndex()
  return await requestCodexContextStateWriter({
    type: 'cleanup_expired_states_shard',
    shardIndex,
    expiredBefore: input.expiredBefore,
    limit: input.limit
  })
}

export async function requestCodexContextStateWriter<T extends CodexContextStateWriterOperation>(
  operation: T
): Promise<CodexContextStateWriterOperationResult<T>> {
  if (!codexContextStateWriterPoolEnabled()) {
    return runCodexContextStateWriterOperationLocally(operation) as CodexContextStateWriterOperationResult<T>
  }
  if (operation.type === 'cleanup_expired_states' || operation.type === 'cleanup_expired_states_shard') {
    return await writerPool.requestExclusive(operation) as CodexContextStateWriterOperationResult<T>
  }
  return await writerPool.request(operation) as CodexContextStateWriterOperationResult<T>
}

function targetWriterPoolSize(): number {
  const configured = Math.trunc(runtimeConfig.codexContextStateWriterPoolSize)
  const fallback = Math.min(codexContextStateShardCount(), Math.max(2, availableParallelism()))
  return Math.max(1, Math.min(configured > 0 ? configured : fallback, codexContextStateShardCount(), 64))
}

function createWriterChild() {
  return fork(resolveWriterWorkerPath(), [], {
    execArgv: writerWorkerExecArgv(),
    env: writerWorkerEnv(),
    stdio: ['ignore', 'ignore', 'ignore', 'ipc']
  })
}

function shardIndexForOperation(operation: CodexContextStateWriterOperation): number {
  switch (operation.type) {
    case 'save_response_session':
      return codexContextStateShardIndexForKey(operation.input.sessionId)
    case 'save_response_sessions':
      return codexContextStateShardIndexForKey(operation.inputs[0]?.sessionId ?? '')
    case 'save_response_row':
      return codexContextStateShardIndexForKey(operation.input.responseId)
    case 'save_response_rows':
      return codexContextStateShardIndexForKey(operation.inputs[0]?.responseId ?? '')
    case 'save_compact_session':
      return codexContextStateShardIndexForKey(operation.input.sessionId)
    case 'save_compact_sessions':
      return codexContextStateShardIndexForKey(operation.inputs[0]?.sessionId ?? '')
    case 'save_compact_row':
      return codexContextStateShardIndexForKey(operation.input.compactId)
    case 'save_compact_rows':
      return codexContextStateShardIndexForKey(operation.inputs[0]?.compactId ?? '')
    case 'read_response_row':
      return codexContextStateShardIndexForKey(operation.responseId)
    case 'read_compact_row':
      return codexContextStateShardIndexForKey(operation.compactId)
    case 'touch_session':
      return codexContextStateShardIndexForKey(operation.sessionId)
    case 'touch_sessions':
      return codexContextStateShardIndexForKey(operation.touches[0]?.sessionId ?? '')
    case 'touch_response_rows':
      return operation.responseIds.length > 0 ? codexContextStateShardIndexForKey(operation.responseIds[0]) : 0
    case 'touch_compact_row':
      return codexContextStateShardIndexForKey(operation.compactId)
    case 'touch_compact_rows':
      return codexContextStateShardIndexForKey(operation.touches[0]?.compactId ?? '')
    case 'cleanup_expired_states':
      return 0
    case 'cleanup_expired_states_shard':
      return operation.shardIndex
    default:
      return assertNever(operation)
  }
}

function touchResponseChainWithWriterPool(rows: CodexContextResponseStateIndex[], now: string, refreshExpiresAt: string): void {
  if (rows.length === 0) return
  const sessionId = rows[0]?.sessionId
  if (sessionId) {
    scheduleCodexContextTouch(sessionTouchBatch.enqueue(shardKey(sessionId), {
      sessionId,
      now,
      refreshExpiresAt
    }), 'session', sessionId)
  }
  for (const responseIds of groupResponseIdsByShard(rows.map((row) => row.responseId)).values()) {
    if (responseIds.length === 0) continue
    scheduleCodexContextTouch(responseTouchBatch.enqueue(shardKey(responseIds[0] ?? ''), {
      responseIds,
      now,
      refreshExpiresAt
    }), 'response_rows', responseIds[0])
  }
}

function groupResponseIdsByShard(responseIds: string[]): Map<number, string[]> {
  const grouped = new Map<number, string[]>()
  for (const responseId of responseIds) {
    const shardIndex = codexContextStateShardIndexForKey(responseId)
    const existing = grouped.get(shardIndex)
    if (existing) {
      existing.push(responseId)
    } else {
      grouped.set(shardIndex, [responseId])
    }
  }
  return grouped
}

function shardKey(value: string): string {
  return String(codexContextStateShardIndexForKey(value))
}

function nextCleanupShardIndex(): number {
  const count = codexContextStateShardCount()
  if (count <= 1) return 0
  const shardIndex = cleanupShardCursor % count
  cleanupShardCursor = (cleanupShardCursor + 1) % count
  return shardIndex
}

function uniqueStrings(values: string[]): string[] {
  const unique = new Set<string>()
  for (const value of values) {
    const text = value.trim()
    if (text) unique.add(text)
  }
  return [...unique]
}

function maxIso(values: string[]): string {
  let max = ''
  for (const value of values) {
    if (value > max) max = value
  }
  return max || nowIso()
}

function scheduleCodexContextTouch(promise: Promise<void>, kind: string, key: string | undefined): void {
  void promise.catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'codex_context_touch_batch_failed',
      touchKind: kind,
      touchKey: key
    }), 'Responses 桥接状态 last_used 刷新失败，已保留读取结果')
  })
}

function runCodexContextStateWriterOperationLocally(operation: CodexContextStateWriterOperation): unknown {
  switch (operation.type) {
    case 'save_response_session':
      upsertCodexContextResponseSessionIndex(operation.input)
      return operation.input
    case 'save_response_sessions':
      upsertCodexContextResponseSessionIndexes(operation.inputs)
      return { saved: operation.inputs.length }
    case 'save_response_row':
      return saveCodexContextResponseStateIndexRow(operation.input)
    case 'save_response_rows':
      saveCodexContextResponseStateIndexRows(operation.inputs)
      return { saved: operation.inputs.length }
    case 'save_compact_session':
      upsertCodexContextCompactSessionIndex(operation.input)
      return operation.input
    case 'save_compact_sessions':
      upsertCodexContextCompactSessionIndexes(operation.inputs)
      return { saved: operation.inputs.length }
    case 'save_compact_row':
      return saveCodexContextCompactStateIndexRow(operation.input)
    case 'save_compact_rows':
      saveCodexContextCompactStateIndexRows(operation.inputs)
      return { saved: operation.inputs.length }
    case 'read_response_row':
      return readCodexContextResponseStateRow(operation.responseId)
    case 'read_compact_row':
      return readCodexContextCompactStateRow(operation.compactId)
    case 'touch_session':
      touchCodexContextSessionState(operation.sessionId, operation.now, operation.refreshExpiresAt)
      return { touched: true }
    case 'touch_sessions':
      touchCodexContextSessionStates(operation.touches)
      return { touched: operation.touches.length }
    case 'touch_response_rows':
      touchCodexContextResponseStateRows(operation.responseIds, operation.now, operation.refreshExpiresAt)
      return { touched: operation.responseIds.length }
    case 'touch_compact_row':
      touchCodexContextCompactStateRow(operation.compactId, operation.now, operation.refreshExpiresAt)
      return { touched: true }
    case 'touch_compact_rows':
      touchCodexContextCompactStateRows(operation.touches)
      return { touched: operation.touches.length }
    case 'cleanup_expired_states':
      return cleanupExpiredCodexContextStates({
        expiredBefore: operation.expiredBefore,
        limit: operation.limit
      })
    case 'cleanup_expired_states_shard':
      return cleanupExpiredCodexContextStatesInShard({
        shardIndex: operation.shardIndex,
        expiredBefore: operation.expiredBefore,
        limit: operation.limit
      })
    default:
      return assertNever(operation)
  }
}

function matchesBoundary(row: CodexContextStateBoundary, boundary: CodexContextStateBoundary): boolean {
  return row.systemAccountId === boundary.systemAccountId
    && (row.apiKeyId ?? '') === (boundary.apiKeyId ?? '')
    && row.groupId === boundary.groupId
    && row.providerCode === boundary.providerCode
}

function requiredText(value: unknown, label: string): string {
  const text = typeof value === 'string' ? value.trim() : ''
  if (!text) {
    throw new Error(`${label} 不能为空`)
  }
  return text
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
    JUHE_AI_PROCESS_ROLE: 'db-service',
    JUHE_AI_WORKER_ROLE: runtimeConfig.workerRole,
    JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath,
    JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath,
    JUHE_AI_USAGE_CATALOG_DATABASE_PATH: usageCatalogDatabasePath(),
    JUHE_AI_STATS_DATABASE_PATH: runtimeConfig.statsDatabasePath,
    JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot,
    JUHE_AI_CODEX_CONTEXT_ROOT: runtimeConfig.codexContextRoot,
    JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: runtimeConfig.codexContextStateShardRoot,
    JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT: String(runtimeConfig.codexContextStateShardCount),
    JUHE_AI_CODEX_CONTEXT_STATE_WRITER_POOL_ENABLED: 'false',
    JUHE_AI_CODEX_CONTEXT_STATE_WRITER_POOL_SIZE: String(runtimeConfig.codexContextStateWriterPoolSize),
    JUHE_AI_CODEX_CONTEXT_STATE_WRITER_QUEUE_MAX_ITEMS: String(runtimeConfig.codexContextStateWriterQueueMaxItems),
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

function assertNever(value: never): never {
  throw new Error(`未知 Responses bridge state writer 操作：${JSON.stringify(value)}`)
}
