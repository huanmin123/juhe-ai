import { createReadStream, type Dir, type Dirent, type Stats } from 'node:fs'
import { opendir, stat as statFile } from 'node:fs/promises'
import { join } from 'node:path'
import { setImmediate as yieldImmediate } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import {
  createRuntimeLogsBatchAsync,
  getRuntimeLogFileCursorAsync,
  getRuntimeLogFileCursorByIdentityAsync,
  upsertRuntimeLogFileCursorAsync,
  type RuntimeLogFileCursor,
  type RuntimeLogFileCursorInput,
  type RuntimeLogIndexInput
} from '../../storage/runtime-logs.repository.js'
import { setRotatedLogCleanupProtectionPredicate } from '../../shared/logger.js'
import { parseRuntimeLogLineForIndex } from './runtime-log-index-queue.service.js'

let importStarted = false
let pollTimer: NodeJS.Timeout | undefined
let activePollPromise: Promise<void> | undefined

const runtimeLogTailPollIntervalMs = 1000
const runtimeLogTailMaxBytesPerFile = 1024 * 1024
const runtimeLogTailMaxOversizedLineScanBytes = 1024 * 1024
const runtimeLogTailMaxLinesPerFile = 5000
const runtimeLogImportBatchSize = 500
const runtimeLogDiscoveryMaxFiles = 2048
const runtimeLogDiscoveryMaxEntriesPerPoll = 2048
const runtimeLogDiscoveryYieldEvery = 100
const runtimeLogCurrentRoles: Record<string, string> = {
  'juhe-ai.log': 'server',
  'juhe-ai.worker.log': 'worker',
  'juhe-ai.db-service.log': 'db-service',
  'juhe-ai.ingest-worker.log': 'ingest-worker',
  'juhe-ai.stats-worker.log': 'stats-worker',
  'juhe-ai.ops-worker.log': 'ops-worker',
  'juhe-ai.temporary-maintenance-worker.log': 'temporary-maintenance-worker'
}
let runtimeLogDiscoveryDirectory: string | undefined
let runtimeLogDiscoveryHandle: Dir | undefined
let runtimeLogDiscoveryLastReadCount = 0
let runtimeLogDiscoveryPendingEntry: Dirent | undefined

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
}

export function createRuntimeLogFileImportTestDependencies(
  dependencies: RuntimeLogFileImportTestDependencies
): RuntimeLogFileImportTestDependencies {
  return dependencies
}

export function startRuntimeLogFileImport(): void {
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
    void pollRuntimeLogFiles()
  })
}

function pollRuntimeLogFiles(): Promise<void> {
  if (activePollPromise) return activePollPromise
  const promise = runRuntimeLogFilePoll().finally(() => {
    if (activePollPromise === promise) activePollPromise = undefined
    scheduleNextPoll()
  })
  activePollPromise = promise
  return promise
}

async function runRuntimeLogFilePoll(): Promise<void> {
  try {
    const files = await discoverRuntimeLogFiles()
    for (const file of files) await importRuntimeLogFileDelta(file)
  } catch (error) {
    process.stderr.write(`[runtime-log-index] 日志目录发现失败：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function scheduleNextPoll(): void {
  if (pollTimer) return
  pollTimer = setTimeout(() => {
    pollTimer = undefined
    void pollRuntimeLogFiles()
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
  const currentRole = runtimeLogCurrentRoles[fileName]
  if (currentRole) return { role: currentRole, kind: 'current' }

  const match = /^(.+?)\.(\d{8}T\d{6}Z)\.[0-9a-f-]+\.log$/i.exec(fileName)
  if (!match) return undefined
  const baseName = `${match[1]}.log`
  const role = runtimeLogCurrentRoles[baseName]
  return role ? { role, kind: 'rotated' } : undefined
}

async function importRuntimeLogFileDelta(file: ActiveRuntimeLogFile, dependencies: RuntimeLogFileImportTestDependencies = {}): Promise<void> {
  try {
    const stats = await statFile(file.path)
    if (!stats.isFile()) return
    const identity = runtimeLogFileIdentity(stats)
    const cursor = await resolveRuntimeLogFileCursor(file, stats, identity, dependencies)
    const startOffset = cursor.cursorOffset
    if (stats.size <= startOffset) {
      await persistCursor({ ...cursor, fileIdentity: identity, fileSize: stats.size, fileMtimeMs: Math.trunc(stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: undefined }, dependencies)
      return
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
      stats
    })
    if (result.flushFailed) {
      await persistCursor({ ...cursor, fileIdentity: identity, cursorOffset: result.flushedOffset, lineNumber: result.flushedLineNumber, fileSize: stats.size, fileMtimeMs: Math.trunc(stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: '运行日志索引写入失败，游标已保留在最近一次成功写入位置，等待下一轮重试' }, dependencies)
      return
    }
    if (result.nextOffset === startOffset && endOffset < stats.size) {
      const skippedLine = await skipOversizedRuntimeLogFileLine(file.path, { searchOffset: endOffset, fileSize: stats.size, initialLineNumber: cursor.lineNumber, maxScanBytes: runtimeLogTailMaxOversizedLineScanBytes })
      if (skippedLine) {
        await persistCursor({ ...cursor, fileIdentity: identity, cursorOffset: skippedLine.nextOffset, lineNumber: skippedLine.nextLineNumber, fileSize: stats.size, fileMtimeMs: Math.trunc(stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: '单行日志超过运行日志增量读取窗口，已跳过完整超长行后继续追尾' }, dependencies)
      } else {
        await persistCursor({ ...cursor, fileIdentity: identity, fileSize: stats.size, fileMtimeMs: Math.trunc(stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: '单行日志超过运行日志增量读取窗口，等待完整换行后继续追尾' }, dependencies)
      }
      return
    }

    await persistCursor({ ...cursor, fileIdentity: identity, cursorOffset: result.nextOffset, lineNumber: result.nextLineNumber, fileSize: stats.size, fileMtimeMs: Math.trunc(stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: undefined }, dependencies)
  } catch (error) {
    if (isMissingFileError(error)) return
    process.stderr.write(`[runtime-log-index] 增量读取日志文件失败 ${file.path}：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

async function resolveRuntimeLogFileCursor(file: ActiveRuntimeLogFile, stats: Stats, identity: string, dependencies: RuntimeLogFileImportTestDependencies): Promise<RuntimeLogFileCursor> {
  const existing = await (dependencies.getCursor ?? getRuntimeLogFileCursorAsync)(file.path)
  if (existing && existing.fileIdentity === identity) {
    return stats.size < existing.cursorOffset || stats.size < existing.fileSize
      ? resetRuntimeLogFileCursor(existing, file.path, identity, stats)
      : existing
  }
  const identityCursor = await (dependencies.getCursorByIdentity ?? getRuntimeLogFileCursorByIdentityAsync)(identity)
  if (identityCursor) {
    return stats.size < identityCursor.cursorOffset || stats.size < identityCursor.fileSize
      ? resetRuntimeLogFileCursor(identityCursor, file.path, identity, stats)
      : { ...identityCursor, logFile: file.path }
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
}): Promise<{ nextOffset: number; nextLineNumber: number; flushedOffset: number; flushedLineNumber: number; flushFailed: boolean }> {
  const stream = createReadStream(file.path, { start: input.startOffset, end: Math.max(input.startOffset, input.endOffset - 1) })
  let nextOffset = input.startOffset
  let nextLineNumber = input.initialLineNumber
  let flushedOffset = input.startOffset
  let flushedLineNumber = input.initialLineNumber
  let pendingLine: Buffer<ArrayBufferLike> = Buffer.alloc(0)
  let batch: RuntimeLogIndexInput[] = []
  let completeLines = 0

  const flushBatch = async (): Promise<boolean> => {
    if (batch.length > 0) {
      try {
        await input.createBatch(batch)
      } catch {
        return false
      }
      batch = []
    }
    flushedOffset = nextOffset
    flushedLineNumber = nextLineNumber
    await input.upsertCursor({ ...input.cursor, logFile: file.path, fileIdentity: identity, cursorOffset: flushedOffset, lineNumber: flushedLineNumber, fileSize: input.stats.size, fileMtimeMs: Math.trunc(input.stats.mtimeMs), lastReadAt: nowIso(), lastErrorMessage: undefined })
    return true
  }

  for await (const chunk of stream) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    let cursor = 0
    while (cursor < buffer.length) {
      const newlineIndex = buffer.indexOf(10, cursor)
      if (newlineIndex < 0) {
        pendingLine = concatLineBuffer(pendingLine, buffer.subarray(cursor))
        break
      }
      pendingLine = concatLineBuffer(pendingLine, buffer.subarray(cursor, newlineIndex))
      const lineStartOffset = nextOffset
      const lineNumber = nextLineNumber + 1
      nextOffset += pendingLine.length + 1
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
      pendingLine = Buffer.alloc(0)
      cursor = newlineIndex + 1
      if (completeLines % Math.max(1, input.batchSize) === 0) {
        if (!(await flushBatch())) return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: true }
      }
      if (completeLines >= runtimeLogTailMaxLinesPerFile) break
    }
    if (completeLines >= runtimeLogTailMaxLinesPerFile) break
  }
  if (completeLines > 0 && (nextOffset !== flushedOffset || batch.length > 0)) {
    if (!(await flushBatch())) return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: true }
  }
  return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: false }
}

async function persistCursor(input: RuntimeLogFileCursorInput, dependencies: RuntimeLogFileImportTestDependencies): Promise<void> {
  await (dependencies.upsertCursor ?? upsertRuntimeLogFileCursorAsync)(input)
}

async function skipOversizedRuntimeLogFileLine(logPath: string, input: { searchOffset: number; fileSize: number; initialLineNumber: number; maxScanBytes: number }): Promise<{ nextOffset: number; nextLineNumber: number } | undefined> {
  if (input.searchOffset >= input.fileSize) return undefined
  const endOffset = Math.min(input.fileSize, input.searchOffset + Math.max(1, input.maxScanBytes))
  const stream = createReadStream(logPath, { start: input.searchOffset, end: endOffset - 1 })
  let chunkStartOffset = input.searchOffset
  for await (const chunk of stream) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    const newlineIndex = buffer.indexOf(10)
    if (newlineIndex >= 0) return { nextOffset: chunkStartOffset + newlineIndex + 1, nextLineNumber: input.initialLineNumber + 1 }
    chunkStartOffset += buffer.length
  }
  return undefined
}

function runtimeLogFileIdentity(stats: Stats): string {
  return [stats.dev, stats.ino, Math.trunc(stats.birthtimeMs)].join(':')
}

function isMissingFileError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && (error as { code?: unknown }).code === 'ENOENT'
}

function concatLineBuffer(left: Buffer<ArrayBufferLike>, right: Buffer<ArrayBufferLike>): Buffer<ArrayBufferLike> {
  if (right.length === 0) return left
  if (left.length === 0) return right
  return Buffer.concat([left, right], left.length + right.length)
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
}

export function getRuntimeLogDiscoveryReadCountForTest(): number {
  return runtimeLogDiscoveryLastReadCount
}

export async function importRuntimeLogFileDeltaForTest(file: ActiveRuntimeLogFile, dependencies?: RuntimeLogFileImportTestDependencies): Promise<void> {
  await importRuntimeLogFileDelta(file, dependencies)
}
