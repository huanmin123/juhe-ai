import { createReadStream, existsSync, statSync, type Stats } from 'node:fs'
import { join } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { nowIso } from '../../storage/database.js'
import {
  getRuntimeLogFileCursor,
  upsertRuntimeLogFileCursor,
  type RuntimeLogFileCursor
} from '../../storage/runtime-logs.repository.js'
import { flushRuntimeLogIndexQueue, enqueueRuntimeLogLineLocal } from './runtime-log-index-queue.service.js'

let importStarted = false
let pollTimer: NodeJS.Timeout | undefined
let polling = false

const runtimeLogImportFlushEveryLines = 500
const runtimeLogTailPollIntervalMs = 1000
const runtimeLogTailMaxBytesPerFile = 1024 * 1024
const runtimeLogTailMaxLinesPerFile = 5000

interface ActiveRuntimeLogFile {
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
    { path: join(runtimeConfig.log.directory, 'juhe-ai.worker.log'), role: 'worker-current' }
  ]
}

async function importRuntimeLogFileDelta(file: ActiveRuntimeLogFile): Promise<void> {
  if (!existsSync(file.path)) {
    return
  }

  try {
    const stats = statSync(file.path)
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
    if (result.nextOffset === startOffset && endOffset < stats.size) {
      saveRuntimeLogFileCursor({
        ...cursor,
        cursorOffset: endOffset,
        fileSize: stats.size,
        fileMtimeMs: Math.trunc(stats.mtimeMs),
        lastReadAt: nowIso(),
        lastErrorMessage: '单行日志超过运行日志增量读取窗口，已跳过当前窗口继续追尾'
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
    flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false })
  } catch (error) {
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
}): Promise<{ nextOffset: number; nextLineNumber: number }> {
  const stream = createReadStream(logPath, {
    start: input.startOffset,
    end: Math.max(input.startOffset, input.endOffset - 1)
  })
  let nextOffset = input.startOffset
  let nextLineNumber = input.initialLineNumber
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
      nextOffset += pendingLine.length + 1
      nextLineNumber += 1
      importedLineCount += 1
      enqueueRuntimeLogLineLocal(trimTrailingCarriageReturn(pendingLine).toString('utf8'))
      pendingLine = Buffer.alloc(0)
      cursor = newlineIndex + 1

      if (importedLineCount % runtimeLogImportFlushEveryLines === 0) {
        flushRuntimeLogIndexQueue({ drain: true, retryOnFailure: false })
      }
      if (importedLineCount >= runtimeLogTailMaxLinesPerFile) {
        return { nextOffset, nextLineNumber }
      }
    }
    if (importedLineCount >= runtimeLogTailMaxLinesPerFile) {
      break
    }
  }

  return { nextOffset, nextLineNumber }
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

function saveRuntimeLogFileCursor(input: Parameters<typeof upsertRuntimeLogFileCursor>[0]): void {
  upsertRuntimeLogFileCursor(input)
}
