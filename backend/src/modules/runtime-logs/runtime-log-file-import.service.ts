import { createReadStream, type Stats } from 'node:fs'
import { stat as statFile } from 'node:fs/promises'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import {
  getRuntimeLogFileCursor,
  upsertRuntimeLogFileCursor,
  type RuntimeLogFileCursor
} from '../../storage/runtime-logs.repository.js'
import { flushRuntimeLogIndexQueueAsync, enqueueRuntimeLogLineLocal } from './runtime-log-index-queue.service.js'

let importStarted = false
let pollTimer: NodeJS.Timeout | undefined
let polling = false

const runtimeLogImportFlushEveryLines = 500
const runtimeLogTailPollIntervalMs = 1000
const runtimeLogTailMaxBytesPerFile = 1024 * 1024
const runtimeLogTailMaxOversizedLineScanBytes = 1024 * 1024
const runtimeLogTailMaxLinesPerFile = 5000

export interface ActiveRuntimeLogFile {
  path: string
  role: string
}

export function startRuntimeLogFileImport(): void {
  if (importStarted || !runtimeConfig.log.fileEnabled) {
    return
  }
  importStarted = true

  setImmediate(() => {
    void pollRuntimeLogFiles()
  })
}

async function pollRuntimeLogFiles(): Promise<void> {
  if (polling) {
    scheduleNextPoll()
    return
  }

  polling = true
  try {
    for (const file of activeRuntimeLogFiles()) {
      await importRuntimeLogFileDelta(file)
    }
  } finally {
    polling = false
    scheduleNextPoll()
  }
}

function scheduleNextPoll(): void {
  if (pollTimer) {
    return
  }
  pollTimer = setTimeout(() => {
    pollTimer = undefined
    void pollRuntimeLogFiles()
  }, runtimeLogTailPollIntervalMs)
  pollTimer.unref()
}

function activeRuntimeLogFiles(): ActiveRuntimeLogFile[] {
  return [
    { path: join(runtimeConfig.log.directory, 'juhe-ai.log'), role: 'server-current' },
    { path: join(runtimeConfig.log.directory, 'juhe-ai.worker.log'), role: 'worker-current' },
    { path: join(runtimeConfig.log.directory, 'juhe-ai.db-service.log'), role: 'db-service-current' }
  ]
}

async function importRuntimeLogFileDelta(file: ActiveRuntimeLogFile): Promise<void> {
  try {
    const stats = await statFile(file.path)
    if (!stats.isFile()) {
      return
    }

    const cursor = resolveRuntimeLogFileCursor(file, stats)
    const startOffset = cursor.cursorOffset
    if (stats.size <= startOffset) {
      saveRuntimeLogFileCursor({
        ...cursor,
        fileSize: stats.size,
        fileMtimeMs: Math.trunc(stats.mtimeMs),
        lastReadAt: nowIso(),
        lastErrorMessage: undefined
      })
      return
    }

    const endOffset = Math.min(stats.size, startOffset + runtimeLogTailMaxBytesPerFile)
    const result = await readRuntimeLogFileLines(file.path, {
      startOffset,
      endOffset,
      initialLineNumber: cursor.lineNumber
    })
    if (result.flushFailed) {
      saveRuntimeLogFileCursor({
        ...cursor,
        cursorOffset: result.flushedOffset,
        lineNumber: result.flushedLineNumber,
        fileSize: stats.size,
        fileMtimeMs: Math.trunc(stats.mtimeMs),
        lastReadAt: nowIso(),
        lastErrorMessage: '运行日志索引写入失败，游标已保留在最近一次成功写入位置，等待下一轮重试'
      })
      return
    }
    if (result.nextOffset === startOffset && endOffset < stats.size) {
      const skippedLine = await skipOversizedRuntimeLogFileLine(file.path, {
        searchOffset: endOffset,
        fileSize: stats.size,
        initialLineNumber: cursor.lineNumber,
        maxScanBytes: runtimeLogTailMaxOversizedLineScanBytes
      })
      if (skippedLine) {
        saveRuntimeLogFileCursor({
          ...cursor,
          cursorOffset: skippedLine.nextOffset,
          lineNumber: skippedLine.nextLineNumber,
          fileSize: stats.size,
          fileMtimeMs: Math.trunc(stats.mtimeMs),
          lastReadAt: nowIso(),
          lastErrorMessage: '单行日志超过运行日志增量读取窗口，已跳过完整超长行后继续追尾'
        })
        return
      }

      saveRuntimeLogFileCursor({
        ...cursor,
        fileSize: stats.size,
        fileMtimeMs: Math.trunc(stats.mtimeMs),
        lastReadAt: nowIso(),
        lastErrorMessage: '单行日志超过运行日志增量读取窗口，等待完整换行后继续追尾'
      })
      return
    }

    const finalFlushSucceeded = await flushRuntimeLogIndexQueueAsync({ drain: true, retryOnFailure: false })
    if (!finalFlushSucceeded) {
      saveRuntimeLogFileCursor({
        ...cursor,
        cursorOffset: result.flushedOffset,
        lineNumber: result.flushedLineNumber,
        fileSize: stats.size,
        fileMtimeMs: Math.trunc(stats.mtimeMs),
        lastReadAt: nowIso(),
        lastErrorMessage: '运行日志索引写入失败，游标已保留在最近一次成功写入位置，等待下一轮重试'
      })
      return
    }

    saveRuntimeLogFileCursor({
      logFile: file.path,
      fileIdentity: runtimeLogFileIdentity(file, stats),
      cursorOffset: result.nextOffset,
      lineNumber: result.nextLineNumber,
      fileSize: stats.size,
      fileMtimeMs: Math.trunc(stats.mtimeMs),
      lastReadAt: nowIso(),
      lastErrorMessage: undefined
    })
  } catch (error) {
    if (isMissingFileError(error)) {
      return
    }
    const cursor = getRuntimeLogFileCursor(file.path)
    saveRuntimeLogFileCursor({
      logFile: file.path,
      fileIdentity: cursor?.fileIdentity ?? file.role,
      cursorOffset: cursor?.cursorOffset ?? 0,
      lineNumber: cursor?.lineNumber ?? 0,
      fileSize: cursor?.fileSize ?? 0,
      fileMtimeMs: cursor?.fileMtimeMs,
      lastReadAt: cursor?.lastReadAt,
      lastErrorMessage: error instanceof Error ? error.message : String(error)
    })
    process.stderr.write(`[runtime-log-index] 增量读取当前日志文件失败 ${file.path}：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function isMissingFileError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && (error as { code?: unknown }).code === 'ENOENT'
}

function resolveRuntimeLogFileCursor(file: ActiveRuntimeLogFile, stats: Stats): RuntimeLogFileCursor {
  const existing = getRuntimeLogFileCursor(file.path)
  const fileIdentity = runtimeLogFileIdentity(file, stats)
  if (!existing) {
    const timestamp = nowIso()
    const initial = {
      logFile: file.path,
      fileIdentity,
      cursorOffset: stats.size,
      lineNumber: 0,
      fileSize: stats.size,
      fileMtimeMs: Math.trunc(stats.mtimeMs),
      lastReadAt: timestamp,
      lastErrorMessage: undefined,
      createdAt: timestamp,
      updatedAt: timestamp
    }
    saveRuntimeLogFileCursor(initial)
    return initial
  }

  if (existing.fileIdentity !== fileIdentity || stats.size < existing.cursorOffset) {
    return {
      ...existing,
      fileIdentity,
      cursorOffset: 0,
      lineNumber: 0,
      fileSize: stats.size,
      fileMtimeMs: Math.trunc(stats.mtimeMs)
    }
  }

  return existing
}

async function readRuntimeLogFileLines(logPath: string, input: {
  startOffset: number
  endOffset: number
  initialLineNumber: number
}): Promise<{
  nextOffset: number
  nextLineNumber: number
  flushedOffset: number
  flushedLineNumber: number
  flushFailed: boolean
}> {
  const stream = createReadStream(logPath, {
    start: input.startOffset,
    end: Math.max(input.startOffset, input.endOffset - 1)
  })
  let nextOffset = input.startOffset
  let nextLineNumber = input.initialLineNumber
  let flushedOffset = input.startOffset
  let flushedLineNumber = input.initialLineNumber
  let importedLineCount = 0
  let pendingLine: Buffer<ArrayBufferLike> = Buffer.alloc(0)

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
      importedLineCount += 1
      enqueueRuntimeLogLineLocal(trimTrailingCarriageReturn(pendingLine).toString('utf8'), {
        sourceKey: runtimeLogFileSourceKey(logPath, lineStartOffset),
        logFile: logPath,
        logOffset: lineStartOffset,
        lineNumber
      })
      pendingLine = Buffer.alloc(0)
      cursor = newlineIndex + 1

      if (importedLineCount % runtimeLogImportFlushEveryLines === 0) {
        if (!(await flushRuntimeLogIndexQueueAsync({ drain: true, retryOnFailure: false }))) {
          return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: true }
        }
        flushedOffset = nextOffset
        flushedLineNumber = nextLineNumber
      }
      if (importedLineCount >= runtimeLogTailMaxLinesPerFile) {
        return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: false }
      }
    }
    if (importedLineCount >= runtimeLogTailMaxLinesPerFile) {
      break
    }
  }

  return { nextOffset, nextLineNumber, flushedOffset, flushedLineNumber, flushFailed: false }
}

async function skipOversizedRuntimeLogFileLine(logPath: string, input: {
  searchOffset: number
  fileSize: number
  initialLineNumber: number
  maxScanBytes: number
}): Promise<{ nextOffset: number; nextLineNumber: number } | undefined> {
  if (input.searchOffset >= input.fileSize) {
    return undefined
  }

  const endOffset = Math.min(input.fileSize, input.searchOffset + Math.max(1, input.maxScanBytes))
  const stream = createReadStream(logPath, {
    start: input.searchOffset,
    end: endOffset - 1
  })
  let chunkStartOffset = input.searchOffset
  for await (const chunk of stream) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    const newlineIndex = buffer.indexOf(10)
    if (newlineIndex >= 0) {
      return {
        nextOffset: chunkStartOffset + newlineIndex + 1,
        nextLineNumber: input.initialLineNumber + 1
      }
    }
    chunkStartOffset += buffer.length
  }

  return undefined
}

function runtimeLogFileIdentity(file: ActiveRuntimeLogFile, stats: Stats): string {
  return [
    file.role,
    stats.dev,
    stats.ino,
    Math.trunc(stats.birthtimeMs)
  ].join(':')
}

function concatLineBuffer(left: Buffer<ArrayBufferLike>, right: Buffer<ArrayBufferLike>): Buffer<ArrayBufferLike> {
  if (right.length === 0) return left
  if (left.length === 0) return right
  return Buffer.concat([left, right], left.length + right.length)
}

function trimTrailingCarriageReturn(buffer: Buffer<ArrayBufferLike>): Buffer<ArrayBufferLike> {
  return buffer.length > 0 && buffer[buffer.length - 1] === 13
    ? buffer.subarray(0, buffer.length - 1)
    : buffer
}

function runtimeLogFileSourceKey(logPath: string, lineStartOffset: number): string {
  return `${logPath}:${lineStartOffset}`
}

function saveRuntimeLogFileCursor(input: Parameters<typeof upsertRuntimeLogFileCursor>[0]): void {
  upsertRuntimeLogFileCursor(input)
}

export function activeRuntimeLogFilesForTest(): ActiveRuntimeLogFile[] {
  return activeRuntimeLogFiles()
}

export async function importRuntimeLogFileDeltaForTest(file: ActiveRuntimeLogFile): Promise<void> {
  await importRuntimeLogFileDelta(file)
}
