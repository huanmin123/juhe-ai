import { type Dir, type Dirent, type Stats } from 'node:fs'
import { open, opendir, stat as statFile } from 'node:fs/promises'
import { join } from 'node:path'
import { setImmediate as yieldImmediate } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import {
  createRuntimeLogsBatchAsync,
  getRuntimeLogFileCursorAsync,
  getRuntimeLogFileCursorByIdentityAsync,
  upsertRuntimeLogFileCursorAsync,
  runtimeLogIndexRetentionDays,
  type RuntimeLogFileCursor,
  type RuntimeLogFileCursorInput,
  type RuntimeLogIndexInput
} from '../../storage/runtime-log-index.repository.js'
import { setRotatedLogCleanupProtectionPredicate } from '../../shared/logger.js'
import { writeBoundedProcessDiagnosticLineAsync } from '../../shared/process-fatal-diagnostic.js'
import { parseRuntimeLogFileName } from '../../shared/runtime-log-file-name.js'
import { parseRuntimeLogLineForIndex } from './runtime-log-line-parser.js'

let importStarted = false
let pollTimer: NodeJS.Timeout | undefined
let activePollPromise: Promise<void> | undefined
let pollRunCount = 0

const runtimeLogTailPollIntervalMs = 1000
const runtimeLogTailMaxBytesPerFile = 1024 * 1024
const runtimeLogTailMaxLinesPerFile = 5000
const runtimeLogImportBatchSize = 500
const runtimeLogDiscoveryMaxFiles = 2048
const runtimeLogDiscoveryMaxEntriesPerPoll = 2048
const runtimeLogDiscoveryYieldEvery = 100
const runtimeLogCompletedCacheRenewalMs = 60 * 60 * 1000
const runtimeLogCompletedCacheMaxEntries = 4096
const runtimeLogFailureDiagnosticMaxBytes = 32 * 1024
const runtimeLogFailureDiagnosticTextMaxBytes = 8 * 1024
const runtimeLogFailureDiagnosticPathMaxBytes = 4 * 1024
const runtimeLogFailureDiagnosticMaxCauseDepth = 4
let runtimeLogDiscoveryDirectory: string | undefined
let runtimeLogDiscoveryHandle: Dir | undefined
let runtimeLogDiscoveryLastReadCount = 0
let runtimeLogDiscoveryPendingEntry: Dirent | undefined
const completedRuntimeLogFiles = new Map<string, { identity: string; size: number; mtimeMs: number; renewedAtMs: number }>()

export interface RuntimeLogFileImportRuntime {
  queueLength: number
  queueBytes: number
  retentionDays: number
  discoveredFileCount: number
  pendingFileCount: number
  pendingBytes: number
  oldestPendingMtime?: string
  currentFile?: string
  currentOffset: number
  lastReadAt?: string
  lastCommitAt?: string
  lastError?: string
  protectedRotatedFileCount: number
}

const runtimeLogFileImportRuntime: RuntimeLogFileImportRuntime = {
  queueLength: 0,
  queueBytes: 0,
  retentionDays: runtimeLogIndexRetentionDays,
  discoveredFileCount: 0,
  pendingFileCount: 0,
  pendingBytes: 0,
  currentOffset: 0,
  protectedRotatedFileCount: 0
}

export interface ActiveRuntimeLogFile {
  path: string
  role: string
  kind?: 'current' | 'rotated'
}

export interface RuntimeLogFileImportTestDependencies {
  getCursor?: (logFile: string) => Promise<RuntimeLogFileCursor | undefined>
  getCursorByIdentity?: (fileIdentity: string) => Promise<RuntimeLogFileCursor | undefined>
  upsertCursor?: (input: RuntimeLogFileCursorInput) => Promise<void>
  createBatch?: (inputs: RuntimeLogIndexInput[]) => Promise<void>
  batchSize?: number
  nowMs?: () => number
}

interface RuntimeLogFileImportFailureSnapshot {
  path?: string
  fileRole?: string
  kind?: ActiveRuntimeLogFile['kind']
  fileIdentity?: string
  truncationGeneration?: number
  cursorOffset?: number
  lineNumber?: number
  fileSize?: number
  fileMtimeMs?: number
  batchConfiguredSize?: number
  batchPendingCount?: number
  batchFlushedOffset?: number
  batchFlushedLineNumber?: number
  phase: string
}

export function createRuntimeLogFileImportTestDependencies(
  dependencies: RuntimeLogFileImportTestDependencies
): RuntimeLogFileImportTestDependencies {
  return dependencies
}

export function startRuntimeLogFileImport(): void {
	if (runtimeConfig.log.indexOwner !== 'node') {
		return
	}
  if (!runtimeConfig.log.indexEnabled) {
    // 没有索引 cursor 时，轮转文件按普通保留策略清理，避免全部被永久保护。
    setRotatedLogCleanupProtectionPredicate(async () => true)
    return
  }
  if (runtimeConfig.runtimeMode === 'performance' && !runtimeConfig.log.fileEnabled) {
    throw new Error('高性能模式必须启用 JUHE_AI_LOG_FILE_ENABLED，否则 runtime_logs 没有耐久索引来源')
  }
  if (importStarted || !runtimeConfig.log.fileEnabled) {
    return
  }
  importStarted = true
  setRotatedLogCleanupProtectionPredicate(async (path, fileSize, fileIdentity) => {
    const cursor = fileIdentity
      ? await getRuntimeLogFileCursorByIdentityAsync(fileIdentity)
      : await getRuntimeLogFileCursorAsync(path)
    return Boolean(cursor && cursor.cursorOffset >= fileSize && !cursor.lastErrorMessage)
  })

  setImmediate(() => {
    if (importStarted) void pollRuntimeLogFiles()
  })
}

export async function stopRuntimeLogFileImport(options: { drainTimeoutMs?: number } = {}): Promise<void> {
	if (runtimeConfig.log.indexOwner !== 'node') {
		return
	}
  importStarted = false
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = undefined
  }
  const activePoll = activePollPromise
  if (activePoll) {
    await waitForRuntimeLogPoll(activePoll, options.drainTimeoutMs ?? 5_000)
  }
  await closeRuntimeLogDiscoveryHandle()
}

function pollRuntimeLogFiles(): Promise<void> {
  if (!importStarted) return Promise.resolve()
  if (activePollPromise) return activePollPromise
  const promise = runRuntimeLogFilePoll().finally(() => {
    if (activePollPromise === promise) activePollPromise = undefined
    if (importStarted) scheduleNextPoll()
  })
  activePollPromise = promise
  return promise
}

async function runRuntimeLogFilePoll(): Promise<void> {
  pollRunCount += 1
  try {
    const files = await discoverRuntimeLogFiles()
    runtimeLogFileImportRuntime.discoveredFileCount = files.length
    runtimeLogFileImportRuntime.pendingFileCount = 0
    runtimeLogFileImportRuntime.pendingBytes = 0
    runtimeLogFileImportRuntime.oldestPendingMtime = undefined
    runtimeLogFileImportRuntime.protectedRotatedFileCount = 0
    let pollError: string | undefined
    for (const file of files) {
      if (!importStarted) break
      const succeeded = await importRuntimeLogFileDelta(file)
      if (!succeeded && !pollError) pollError = runtimeLogFileImportRuntime.lastError
    }
    runtimeLogFileImportRuntime.lastError = pollError
  } catch (error) {
    const message = runtimeLogFailureMessage(error)
    runtimeLogFileImportRuntime.lastError = message
    writeRuntimeLogFileImportFailureDiagnostic(error, {
      path: runtimeConfig.log.directory,
      kind: 'current',
      cursorOffset: runtimeLogFileImportRuntime.currentOffset,
      batchConfiguredSize: runtimeLogImportBatchSize,
      phase: 'directory.discover'
    })
  }
}

function scheduleNextPoll(): void {
  if (!importStarted || pollTimer) return
  pollTimer = setTimeout(() => {
    pollTimer = undefined
    if (importStarted) void pollRuntimeLogFiles()
  }, runtimeLogTailPollIntervalMs)
  pollTimer.unref()
}

async function discoverRuntimeLogFiles(): Promise<ActiveRuntimeLogFile[]> {
  const discovered: ActiveRuntimeLogFile[] = []
  if (runtimeLogDiscoveryDirectory !== runtimeConfig.log.directory) {
    await closeRuntimeLogDiscoveryHandle()
    runtimeLogDiscoveryDirectory = runtimeConfig.log.directory
  }
  if (!runtimeLogDiscoveryHandle) {
    runtimeLogDiscoveryHandle = await opendir(runtimeConfig.log.directory)
  }
  let readCount = 0
  let reachedEof = false
  try {
    while (readCount < runtimeLogDiscoveryMaxEntriesPerPoll) {
      const entry = runtimeLogDiscoveryPendingEntry ?? await runtimeLogDiscoveryHandle.read()
      runtimeLogDiscoveryPendingEntry = undefined
      if (!entry) {
        reachedEof = true
        break
      }
      readCount += 1
      if (readCount % runtimeLogDiscoveryYieldEvery === 0) await yieldImmediate()
      if (!entry.isFile()) continue
      const match = runtimeLogFileRole(entry.name)
      if (!match) continue
      if (discovered.length >= runtimeLogDiscoveryMaxFiles) {
        break
      }
      discovered.push({ path: join(runtimeConfig.log.directory, entry.name), role: match.role, kind: match.kind })
    }
    if (readCount >= runtimeLogDiscoveryMaxEntriesPerPoll) {
      const lookahead = await runtimeLogDiscoveryHandle.read()
      if (lookahead) runtimeLogDiscoveryPendingEntry = lookahead
      else reachedEof = true
    }
  } catch (error) {
    await closeRuntimeLogDiscoveryHandle()
    throw error
  }
  runtimeLogDiscoveryLastReadCount = readCount
  if (reachedEof) await closeRuntimeLogDiscoveryHandle()
  return discovered.sort((left, right) => {
    if (left.kind !== right.kind) return left.kind === 'rotated' ? -1 : 1
    return left.path.localeCompare(right.path)
  })
}

async function closeRuntimeLogDiscoveryHandle(): Promise<void> {
  const handle = runtimeLogDiscoveryHandle
  runtimeLogDiscoveryHandle = undefined
  runtimeLogDiscoveryPendingEntry = undefined
  if (handle) await handle.close().catch(() => undefined)
}

function runtimeLogFileRole(fileName: string): { role: string; kind: 'current' | 'rotated' } | undefined {
  const match = parseRuntimeLogFileName(fileName)
  return match ? { role: match.role, kind: match.kind } : undefined
}

async function waitForRuntimeLogPoll(poll: Promise<void>, timeoutMs: number): Promise<void> {
  await new Promise<void>((resolve) => {
    let settled = false
    const settle = (): void => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      resolve()
    }
    const timeout = setTimeout(settle, Math.max(1, timeoutMs))
    timeout.unref()
    void poll.then(settle, settle)
  })
}

async function importRuntimeLogFileDelta(file: ActiveRuntimeLogFile, dependencies: RuntimeLogFileImportTestDependencies = {}): Promise<boolean> {
  let cursorOffsetForMetrics = 0
  const failureSnapshot: RuntimeLogFileImportFailureSnapshot = {
    path: file.path,
    fileRole: file.role,
    kind: file.kind,
    truncationGeneration: 0,
    cursorOffset: 0,
    lineNumber: 0,
    batchConfiguredSize: dependencies.batchSize ?? runtimeLogImportBatchSize,
    batchPendingCount: 0,
    phase: 'file.stat'
  }
  try {
    const stats = await statFile(file.path)
    if (!stats.isFile()) return true
    const identity = runtimeLogFileIdentity(stats)
    Object.assign(failureSnapshot, {
      fileIdentity: identity,
      fileSize: stats.size,
      fileMtimeMs: Math.trunc(stats.mtimeMs),
      phase: 'cursor.resolve'
    })
    const completed = completedRuntimeLogFiles.get(file.path)
    const completedCacheMissing = completed === undefined
    const nowMs = dependencies.nowMs?.() ?? Date.now()
    const completedFileMatches = completed?.identity === identity && completed.size === stats.size && completed.mtimeMs === Math.trunc(stats.mtimeMs)
    const completedCacheExpired = Boolean(completedFileMatches && completed && nowMs - completed.renewedAtMs >= runtimeLogCompletedCacheRenewalMs)
    if (completedFileMatches && completed && !completedCacheExpired) {
      return true
    }
    completedRuntimeLogFiles.delete(file.path)
    const cursor = await resolveRuntimeLogFileCursor(file, stats, identity, dependencies)
    const startOffset = cursor.cursorOffset
    cursorOffsetForMetrics = startOffset
    Object.assign(failureSnapshot, {
      truncationGeneration: cursor.truncationGeneration,
      cursorOffset: startOffset,
      lineNumber: cursor.lineNumber,
      batchFlushedOffset: startOffset,
      batchFlushedLineNumber: cursor.lineNumber,
      phase: 'file.read'
    })
    if (stats.size <= startOffset) {
      if (completedCacheMissing || completedCacheExpired || cursor.fileSize !== stats.size || cursor.fileMtimeMs !== Math.trunc(stats.mtimeMs) || cursor.lastErrorMessage) {
        failureSnapshot.phase = 'cursor.renew'
        await persistCursor({ ...cursor, fileIdentity: identity, fileSize: stats.size, fileMtimeMs: Math.trunc(stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: undefined }, dependencies)
      }
      runtimeLogFileImportRuntime.currentFile = file.path
      runtimeLogFileImportRuntime.currentOffset = startOffset
      markRuntimeLogFileCompleted(file.path, identity, stats, nowMs)
      recordPendingRuntimeLogFile(file, stats, startOffset)
      return true
    }

    const endOffset = Math.min(stats.size, startOffset + runtimeLogTailMaxBytesPerFile)
    const result = await readRuntimeLogFileLines(file, identity, {
      startOffset,
      endOffset,
      initialLineNumber: cursor.lineNumber,
      batchSize: dependencies.batchSize ?? runtimeLogImportBatchSize,
      createBatch: dependencies.createBatch ?? createRuntimeLogsBatchAsync,
      upsertCursor: dependencies.upsertCursor ?? upsertRuntimeLogFileCursorAsync,
      cursor,
      stats,
      failureSnapshot
    })
    if (result.staleFile) return true
    if (result.flushFailed) {
      runtimeLogFileImportRuntime.lastError = '运行日志索引写入失败，游标已保留在最近一次成功写入位置，等待下一轮重试'
      failureSnapshot.phase = 'cursor.persist_failure_state'
      await persistCursor({ ...cursor, fileIdentity: identity, cursorOffset: result.flushedOffset, lineNumber: result.flushedLineNumber, fileSize: stats.size, fileMtimeMs: Math.trunc(stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: '运行日志索引写入失败，游标已保留在最近一次成功写入位置，等待下一轮重试' }, dependencies)
      recordPendingRuntimeLogFile(file, stats, result.flushedOffset)
      return false
    }
    failureSnapshot.phase = 'cursor.finalize'
    await persistCursor({ ...cursor, fileIdentity: identity, cursorOffset: result.nextOffset, lineNumber: result.nextLineNumber, fileSize: stats.size, fileMtimeMs: Math.trunc(stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: undefined }, dependencies)
    runtimeLogFileImportRuntime.currentFile = file.path
    runtimeLogFileImportRuntime.currentOffset = result.nextOffset
    runtimeLogFileImportRuntime.lastReadAt = nowIso()
    runtimeLogFileImportRuntime.lastCommitAt = nowIso()
    if (result.nextOffset >= stats.size) markRuntimeLogFileCompleted(file.path, identity, stats, nowMs)
    recordPendingRuntimeLogFile(file, stats, result.nextOffset)
    return true
  } catch (error) {
    if (isMissingFileError(error)) return true
    const message = runtimeLogFailureMessage(error)
    runtimeLogFileImportRuntime.lastError = message
    completedRuntimeLogFiles.delete(file.path)
    try {
      const stats = await statFile(file.path)
      recordPendingRuntimeLogFile(file, stats, cursorOffsetForMetrics)
    } catch {
    }
    writeRuntimeLogFileImportFailureDiagnostic(error, failureSnapshot)
    return false
  }
}

async function resolveRuntimeLogFileCursor(file: ActiveRuntimeLogFile, stats: Stats, identity: string, dependencies: RuntimeLogFileImportTestDependencies): Promise<RuntimeLogFileCursor> {
  const existing = await (dependencies.getCursor ?? getRuntimeLogFileCursorAsync)(file.path)
  if (existing && existing.fileIdentity === identity) {
    if (stats.size < existing.cursorOffset || stats.size < existing.fileSize) {
      const reset = resetRuntimeLogFileCursor(existing, file.path, identity, stats)
      await persistCursor(reset, dependencies)
      return reset
    }
    return existing
  }
  if (existing && existing.fileIdentity !== identity) {
    if (existing.fileIdentity) {
      await persistCursor({
        ...existing,
        logFile: displacedRuntimeLogFileCursorKey(existing.fileIdentity)
      }, dependencies)
    }
    const timestamp = nowIso()
    const replacement: RuntimeLogFileCursor = {
      logFile: file.path,
      fileIdentity: identity,
      cursorOffset: 0,
      lineNumber: 0,
      fileSize: stats.size,
      truncationGeneration: 0,
      fileMtimeMs: Math.trunc(stats.mtimeMs),
      lastReadAt: timestamp,
      lastErrorMessage: undefined,
      createdAt: timestamp,
      updatedAt: timestamp
    }
    await persistCursor(replacement, dependencies)
    return replacement
  }
  const identityCursor = await (dependencies.getCursorByIdentity ?? getRuntimeLogFileCursorByIdentityAsync)(identity)
  if (identityCursor) {
    if (stats.size < identityCursor.cursorOffset || stats.size < identityCursor.fileSize) {
      const reset = resetRuntimeLogFileCursor(identityCursor, file.path, identity, stats)
      await persistCursor(reset, dependencies)
      return reset
    }
    const relocated = { ...identityCursor, logFile: file.path, fileIdentity: identity }
    if (identityCursor.logFile !== file.path) {
      await persistCursor(relocated, dependencies)
    }
    return relocated
  }
  const timestamp = nowIso()
  const offset = isRotatedRuntimeLogFile(file) ? 0 : stats.size
  const initial: RuntimeLogFileCursor = {
    logFile: file.path,
    fileIdentity: identity,
    cursorOffset: offset,
    lineNumber: 0,
    fileSize: stats.size,
    truncationGeneration: 0,
    fileMtimeMs: Math.trunc(stats.mtimeMs),
    lastReadAt: timestamp,
    lastErrorMessage: undefined,
    createdAt: timestamp,
    updatedAt: timestamp
  }
  await persistCursor(initial, dependencies)
  return initial
}

function resetRuntimeLogFileCursor(
  cursor: RuntimeLogFileCursor,
  logFile: string,
  fileIdentity: string,
  stats: Stats
): RuntimeLogFileCursor {
  return {
    ...cursor,
    logFile,
    fileIdentity,
    cursorOffset: 0,
    lineNumber: 0,
    fileSize: stats.size,
    truncationGeneration: cursor.truncationGeneration + 1,
    fileMtimeMs: Math.trunc(stats.mtimeMs)
  }
}

function isRotatedRuntimeLogFile(file: ActiveRuntimeLogFile): boolean {
  return file.kind === 'rotated'
    || file.role.endsWith('-rotated')
    || /\.\d{8}T\d{6}Z\.[0-9a-f-]+\.log$/i.test(file.path)
}

async function readRuntimeLogFileLines(file: ActiveRuntimeLogFile, identity: string, input: {
  startOffset: number
  endOffset: number
  initialLineNumber: number
  batchSize: number
  createBatch: (inputs: RuntimeLogIndexInput[]) => Promise<void>
  upsertCursor: (input: RuntimeLogFileCursorInput) => Promise<void>
  cursor: RuntimeLogFileCursor
  stats: Stats
  failureSnapshot: RuntimeLogFileImportFailureSnapshot
}): Promise<{ nextOffset: number; nextLineNumber: number; flushedOffset: number; flushedLineNumber: number; flushFailed: boolean; staleFile?: boolean }> {
  input.failureSnapshot.phase = 'file.open'
  const handle = await open(file.path, 'r')
  try {
    input.failureSnapshot.phase = 'file.identity.verify'
    const openedStats = await handle.stat()
    if (runtimeLogFileIdentity(openedStats) !== identity) {
      return { nextOffset: input.startOffset, nextLineNumber: input.initialLineNumber, flushedOffset: input.startOffset, flushedLineNumber: input.initialLineNumber, flushFailed: false, staleFile: true }
    }
    const stream = handle.createReadStream({
      start: input.startOffset,
      end: Math.max(input.startOffset, input.stats.size - 1),
      autoClose: false
    })
  let nextOffset = input.startOffset
  let nextLineNumber = input.initialLineNumber
  let flushedOffset = input.startOffset
  let flushedLineNumber = input.initialLineNumber
  let pendingLineChunks: Buffer<ArrayBufferLike>[] = []
  let pendingLineBytes = 0
  let batch: RuntimeLogIndexInput[] = []
  let completeLines = 0

  const flushBatch = async (): Promise<boolean> => {
    if (batch.length > 0) {
      input.failureSnapshot.phase = 'batch.store'
      input.failureSnapshot.batchPendingCount = batch.length
      try {
        await input.createBatch(batch)
      } catch (error) {
        writeRuntimeLogFileImportFailureDiagnostic(error, input.failureSnapshot)
        return false
      }
      batch = []
      input.failureSnapshot.batchPendingCount = 0
    }
    flushedOffset = nextOffset
    flushedLineNumber = nextLineNumber
    Object.assign(input.failureSnapshot, {
      phase: 'cursor.persist',
      cursorOffset: flushedOffset,
      lineNumber: flushedLineNumber,
      batchFlushedOffset: flushedOffset,
      batchFlushedLineNumber: flushedLineNumber
    })
    await input.upsertCursor({ ...input.cursor, logFile: file.path, fileIdentity: identity, cursorOffset: flushedOffset, lineNumber: flushedLineNumber, fileSize: input.stats.size, fileMtimeMs: Math.trunc(input.stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: undefined })
    input.failureSnapshot.phase = 'file.read'
    return true
  }

  for await (const chunk of stream) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    let cursor = 0
    while (cursor < buffer.length) {
      const newlineIndex = buffer.indexOf(10, cursor)
      if (newlineIndex < 0) {
        const fragment = buffer.subarray(cursor)
        if (fragment.length > 0) {
          pendingLineChunks.push(fragment)
          pendingLineBytes += fragment.length
        }
        break
      }
      const fragment = buffer.subarray(cursor, newlineIndex)
      if (fragment.length > 0) {
        pendingLineChunks.push(fragment)
        pendingLineBytes += fragment.length
      }
      const pendingLine = pendingLineChunks.length === 1
        ? pendingLineChunks[0]
        : Buffer.concat(pendingLineChunks, pendingLineBytes)
      const lineStartOffset = nextOffset
      const lineNumber = nextLineNumber + 1
      nextOffset += pendingLineBytes + 1
      nextLineNumber = lineNumber
      completeLines += 1
      const parsed = parseRuntimeLogLineForIndex(trimTrailingCarriageReturn(pendingLine).toString('utf8'), {
        sourceKey: input.cursor.truncationGeneration > 0
          ? `${identity}:${input.cursor.truncationGeneration}:${lineStartOffset}`
          : `${identity}:${lineStartOffset}`,
        logFile: file.path,
        logOffset: lineStartOffset,
        lineNumber
      })
      if (parsed) batch.push(parsed)
      pendingLineChunks = []
      pendingLineBytes = 0
      cursor = newlineIndex + 1
      if (completeLines % Math.max(1, input.batchSize) === 0) {
        if (!(await flushBatch())) {
          return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: true }
        }
      }
      if (completeLines >= runtimeLogTailMaxLinesPerFile || nextOffset >= input.endOffset) break
    }
    if (completeLines >= runtimeLogTailMaxLinesPerFile || nextOffset >= input.endOffset) break
  }
  if (completeLines > 0 && (nextOffset !== flushedOffset || batch.length > 0)) {
    if (!(await flushBatch())) {
      return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: true }
    }
  }
    return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: false }
  } finally {
    await handle.close().catch(() => undefined)
  }
}

function writeRuntimeLogFileImportFailureDiagnostic(error: unknown, snapshot: RuntimeLogFileImportFailureSnapshot): void {
  let line: string
  try {
    line = boundedRuntimeLogFailureJsonLine({
      version: 1,
      service: 'juhe-ai',
      role: 'ingest-worker',
      event: 'runtime_log_file_import_failed',
      time: nowIso(),
    path: boundedRuntimeLogFailureText(snapshot.path, runtimeLogFailureDiagnosticPathMaxBytes),
      fileRole: boundedRuntimeLogFailureText(snapshot.fileRole, 128),
      kind: snapshot.kind,
      fileIdentity: boundedRuntimeLogFailureText(snapshot.fileIdentity, 512),
      truncationGeneration: snapshot.truncationGeneration,
      cursorOffset: snapshot.cursorOffset,
      lineNumber: snapshot.lineNumber,
      fileSize: snapshot.fileSize,
      fileMtimeMs: snapshot.fileMtimeMs,
      batch: {
        configuredSize: snapshot.batchConfiguredSize,
        pendingCount: snapshot.batchPendingCount,
        flushedOffset: snapshot.batchFlushedOffset,
        flushedLineNumber: snapshot.batchFlushedLineNumber
      },
      phase: boundedRuntimeLogFailureText(snapshot.phase, 128),
      error: boundedRuntimeLogFailureError(error)
    })
  } catch {
    line = `${JSON.stringify({
      version: 1,
      service: 'juhe-ai',
      role: 'ingest-worker',
      event: 'runtime_log_file_import_failed',
      time: new Date().toISOString(),
      path: '[unavailable]',
      kind: null,
      fileRole: null,
      fileIdentity: null,
      truncationGeneration: null,
      cursorOffset: null,
      batch: null,
      phase: 'diagnostic.serialize',
      error: { name: 'Error', message: 'runtime log importer diagnostic serialization failed', stack: null, cause: null, code: 'DIAGNOSTIC_SERIALIZE_FAILED' }
    })}\n`
  }
  try {
    writeBoundedProcessDiagnosticLineAsync(line)
  } catch {
    // The bounded stderr fallback must not route its own failure back into the consumed spool.
  }
}

function boundedRuntimeLogFailureError(error: unknown, depth = 0): Record<string, unknown> {
  const value = error instanceof Error ? error : undefined
  const objectValue = typeof error === 'object' && error !== null ? error as Record<string, unknown> : undefined
  const cause = safeRuntimeLogFailureProperty(objectValue, 'cause')
  return {
    name: boundedRuntimeLogFailureText(stringErrorProperty(objectValue, 'name') ?? (value ? 'Error' : 'NonErrorThrown'), 128) ?? 'Error',
    message: boundedRuntimeLogFailureText(stringErrorProperty(objectValue, 'message') ?? runtimeLogFailureMessage(error), runtimeLogFailureDiagnosticTextMaxBytes) ?? '',
    stack: boundedRuntimeLogFailureText(stringErrorProperty(objectValue, 'stack'), runtimeLogFailureDiagnosticTextMaxBytes) ?? null,
    code: boundedRuntimeLogFailureText(stringErrorProperty(objectValue, 'code'), 256) ?? null,
    cause: cause !== undefined && depth < runtimeLogFailureDiagnosticMaxCauseDepth
      ? boundedRuntimeLogFailureError(cause, depth + 1)
      : null
  }
}

function stringErrorProperty(value: Record<string, unknown> | undefined, key: string): string | undefined {
  const property = safeRuntimeLogFailureProperty(value, key)
  return typeof property === 'string' || typeof property === 'number' ? String(property) : undefined
}

function safeRuntimeLogFailureProperty(value: Record<string, unknown> | undefined, key: string): unknown {
  if (!value) return undefined
  let current: object | null = value
  for (let depth = 0; current && depth < 8; depth += 1) {
    try {
      const descriptor = Object.getOwnPropertyDescriptor(current, key)
      if (descriptor) {
        if ('value' in descriptor) return descriptor.value
        try {
          return descriptor.get?.call(value)
        } catch {
          return `[unreadable ${key}: accessor threw]`
        }
      }
      current = Object.getPrototypeOf(current) as object | null
    } catch {
      return `[unreadable ${key}]`
    }
  }
  return undefined
}

function runtimeLogFailureMessage(error: unknown): string {
  const objectValue = typeof error === 'object' && error !== null ? error as Record<string, unknown> : undefined
  const message = stringErrorProperty(objectValue, 'message')
  if (message !== undefined) return message
  try {
    return String(error)
  } catch {
    return '[unprintable thrown value]'
  }
}

function boundedRuntimeLogFailureText(value: string | undefined, maxBytes: number): string | undefined {
  if (value === undefined) return undefined
  if (value.length <= maxBytes && Buffer.byteLength(value, 'utf8') <= maxBytes) return value
  const suffix = '...[truncated]'
  const prefixBytes = Math.max(0, maxBytes - Buffer.byteLength(suffix))
  let low = 0
  let high = Math.min(value.length, prefixBytes)
  while (low < high) {
    const midpoint = Math.ceil((low + high) / 2)
    if (Buffer.byteLength(value.slice(0, midpoint), 'utf8') <= prefixBytes) low = midpoint
    else high = midpoint - 1
  }
  let end = low
  if (end > 0) {
    const last = value.charCodeAt(end - 1)
    if (last >= 0xD800 && last <= 0xDBFF) end -= 1
  }
  return value.slice(0, end) + suffix
}

function boundedRuntimeLogFailureJsonLine(event: Record<string, unknown>): string {
  let line = `${JSON.stringify(event)}\n`
  if (Buffer.byteLength(line, 'utf8') <= runtimeLogFailureDiagnosticMaxBytes) return line
  const reduced = {
    ...event,
    path: boundedRuntimeLogFailureText(typeof event.path === 'string' ? event.path : undefined, 1024),
    error: compactRuntimeLogFailureError(event.error),
    diagnosticTruncated: true
  }
  line = `${JSON.stringify(reduced)}\n`
  if (Buffer.byteLength(line, 'utf8') <= runtimeLogFailureDiagnosticMaxBytes) return line
  const minimal = {
    ...reduced,
    path: '[truncated]',
    error: compactRuntimeLogFailureError(event.error, 1, 1024, 512),
    diagnosticTruncated: true
  }
  line = `${JSON.stringify(minimal)}\n`
  if (Buffer.byteLength(line, 'utf8') <= runtimeLogFailureDiagnosticMaxBytes) return line
  return `${JSON.stringify({
    version: 1,
    service: 'juhe-ai',
    role: 'ingest-worker',
    event: 'runtime_log_file_import_failed',
    time: nowIso(),
    path: '[truncated]',
    phase: 'diagnostic.serialize',
    error: { name: 'Error', message: '[truncated]', stack: '[truncated]', code: 'DIAGNOSTIC_SERIALIZE_LIMIT', cause: undefined },
    diagnosticTruncated: true
  })}\n`
}

function compactRuntimeLogFailureError(value: unknown, maxDepth = 4, stackBytes = 2 * 1024, messageBytes = 1024): Record<string, unknown> {
  const error = typeof value === 'object' && value !== null ? value as Record<string, unknown> : {}
  const cause = safeRuntimeLogFailureProperty(error, 'cause')
  return {
    name: boundedRuntimeLogFailureText(stringErrorProperty(error, 'name') ?? 'Error', 128) ?? 'Error',
    message: boundedRuntimeLogFailureText(stringErrorProperty(error, 'message'), messageBytes) ?? '',
    stack: boundedRuntimeLogFailureText(stringErrorProperty(error, 'stack'), stackBytes) ?? null,
    code: boundedRuntimeLogFailureText(stringErrorProperty(error, 'code'), 256) ?? null,
    cause: cause !== undefined && maxDepth > 0
      ? compactRuntimeLogFailureError(cause, maxDepth - 1, stackBytes, messageBytes)
      : null
  }
}

async function persistCursor(input: RuntimeLogFileCursorInput, dependencies: RuntimeLogFileImportTestDependencies): Promise<void> {
  await (dependencies.upsertCursor ?? upsertRuntimeLogFileCursorAsync)(input)
}

function recordPendingRuntimeLogFile(file: ActiveRuntimeLogFile, stats: Stats, cursorOffset: number): void {
  const pendingBytes = Math.max(0, stats.size - cursorOffset)
  if (pendingBytes <= 0) return
  runtimeLogFileImportRuntime.pendingFileCount += 1
  runtimeLogFileImportRuntime.pendingBytes += pendingBytes
  if (isRotatedRuntimeLogFile(file)) runtimeLogFileImportRuntime.protectedRotatedFileCount += 1
  const mtime = new Date(stats.mtimeMs).toISOString()
  if (!runtimeLogFileImportRuntime.oldestPendingMtime || mtime < runtimeLogFileImportRuntime.oldestPendingMtime) {
    runtimeLogFileImportRuntime.oldestPendingMtime = mtime
  }
}

function markRuntimeLogFileCompleted(path: string, identity: string, stats: Stats, renewedAtMs: number): void {
  completedRuntimeLogFiles.set(path, {
    identity,
    size: stats.size,
    mtimeMs: Math.trunc(stats.mtimeMs),
    renewedAtMs
  })
  while (completedRuntimeLogFiles.size > runtimeLogCompletedCacheMaxEntries) {
    const oldestPath = completedRuntimeLogFiles.keys().next().value
    if (oldestPath === undefined) break
    completedRuntimeLogFiles.delete(oldestPath)
  }
}


function runtimeLogFileIdentity(stats: Stats): string {
  return [stats.dev, stats.ino, Math.trunc(stats.birthtimeMs)].join(':')
}

function displacedRuntimeLogFileCursorKey(fileIdentity: string): string {
  return `__runtime_log_identity__:${fileIdentity}`
}

function isMissingFileError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && (error as { code?: unknown }).code === 'ENOENT'
}

function trimTrailingCarriageReturn(buffer: Buffer<ArrayBufferLike>): Buffer<ArrayBufferLike> {
  return buffer.length > 0 && buffer[buffer.length - 1] === 13 ? buffer.subarray(0, buffer.length - 1) : buffer
}

export function activeRuntimeLogFilesForTest(): ActiveRuntimeLogFile[] {
  return [
    { path: join(runtimeConfig.log.directory, 'juhe-ai.log'), role: 'server', kind: 'current' },
    { path: join(runtimeConfig.log.directory, 'juhe-ai.worker.log'), role: 'worker', kind: 'current' },
    { path: join(runtimeConfig.log.directory, 'juhe-ai.db-service.log'), role: 'db-service', kind: 'current' },
    { path: join(runtimeConfig.log.directory, 'juhe-ai.ingest-worker.log'), role: 'ingest-worker', kind: 'current' },
    { path: join(runtimeConfig.log.directory, 'juhe-ai.stats-worker.log'), role: 'stats-worker', kind: 'current' },
    { path: join(runtimeConfig.log.directory, 'juhe-ai.ops-worker.log'), role: 'ops-worker', kind: 'current' }
  ]
}

export async function discoverRuntimeLogFilesForTest(): Promise<ActiveRuntimeLogFile[]> {
  return await discoverRuntimeLogFiles()
}

export async function resetRuntimeLogFileDiscoveryForTest(): Promise<void> {
  await closeRuntimeLogDiscoveryHandle()
  runtimeLogDiscoveryDirectory = undefined
  runtimeLogDiscoveryLastReadCount = 0
  pollRunCount = 0
  completedRuntimeLogFiles.clear()
}

export function getRuntimeLogFileImportLifecycleForTest(): {
  started: boolean
  pollScheduled: boolean
  pollRunning: boolean
  pollRunCount: number
} {
  return {
    started: importStarted,
    pollScheduled: Boolean(pollTimer),
    pollRunning: Boolean(activePollPromise),
    pollRunCount
  }
}

export function getRuntimeLogDiscoveryReadCountForTest(): number {
  return runtimeLogDiscoveryLastReadCount
}

export function getRuntimeLogFileImportRuntime(): RuntimeLogFileImportRuntime {
  return { ...runtimeLogFileImportRuntime }
}

export async function importRuntimeLogFileDeltaForTest(file: ActiveRuntimeLogFile, dependencies?: RuntimeLogFileImportTestDependencies): Promise<void> {
  await importRuntimeLogFileDelta(file, dependencies)
}
