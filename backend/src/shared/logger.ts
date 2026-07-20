import { randomUUID } from 'node:crypto'
import {
  createWriteStream,
  mkdirSync,
  statSync,
  type WriteStream
} from 'node:fs'
import { access, opendir, rename, stat, unlink } from 'node:fs/promises'
import { join } from 'node:path'
import { Writable } from 'node:stream'
import { setImmediate as yieldImmediate } from 'node:timers/promises'

import pino, { type Logger, type LoggerOptions } from 'pino'

import { runtimeConfig } from '../config/runtime.js'
import { writeProcessFatalDiagnostic } from './process-fatal-diagnostic.js'

interface RotatingFileLogStreamOptions {
  directory: string
  fileName: string
  maxFileBytes: number
  retentionDays: number
  maxFiles: number
}

const logDirectoryScanYieldEvery = 100

interface LogFileMtime {
  path: string
  mtimeMs: number
}

export interface LogMaintenanceResult {
  scannedFileCount: number
  currentFileCount: number
  retainedRotatedFileCount: number
  deletedFileCount: number
}

class RotatingFileLogStream extends Writable {
  private readonly currentPath: string
  private stream: WriteStream
  private currentSize = 0
  private cleanupRunning = false
  private cleanupQueued = false
  private readonly protectedCurrentFileNames = new Set([
    'juhe-ai.log',
    'juhe-ai.worker.log',
    'juhe-ai.db-service.log'
  ])

  constructor(private readonly options: RotatingFileLogStreamOptions) {
    super()
    mkdirSync(options.directory, { recursive: true })
    this.currentPath = join(options.directory, options.fileName)
    this.currentSize = this.readCurrentSize()
    this.stream = this.openStream()
    this.cleanup()
  }

  cleanup(): void {
    if (this.cleanupRunning) {
      this.cleanupQueued = true
      return
    }
    this.cleanupRunning = true
    void this.cleanupAsync().finally(() => {
      this.cleanupRunning = false
      if (this.cleanupQueued) {
        this.cleanupQueued = false
        this.cleanup()
      }
    })
  }

  private async cleanupAsync(): Promise<void> {
    await cleanupRotatedLogFiles({
      directory: this.options.directory,
      protectedCurrentFileNames: this.protectedCurrentFileNames,
      maxFiles: this.options.maxFiles,
      retentionDays: this.options.retentionDays
    })
  }

  _write(chunk: Buffer | string, encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    try {
      const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk, encoding)
      if (this.currentSize > 0 && this.currentSize + buffer.byteLength > this.options.maxFileBytes) {
        this.rotate((error) => {
          if (error) {
            callback(error)
            return
          }
          this.writeBuffer(buffer, callback)
        })
        return
      }
      this.writeBuffer(buffer, callback)
    } catch (error) {
      callback(error instanceof Error ? error : new Error(String(error)))
    }
  }

  _final(callback: (error?: Error | null) => void): void {
    this.closeStream(this.stream, callback)
  }

  _destroy(error: Error | null, callback: (error?: Error | null) => void): void {
    this.stream.destroy()
    callback(error)
  }

  private writeBuffer(buffer: Buffer, callback: (error?: Error | null) => void): void {
    this.currentSize += buffer.byteLength
    this.stream.write(buffer, (error?: Error | null) => {
      callback(error ?? undefined)
    })
  }

  private rotate(callback: (error?: Error | null) => void): void {
    this.closeStream(this.stream, (closeError) => {
      if (closeError) {
        callback(closeError)
        return
      }

      this.rotateClosedFile(callback)
    })
  }

  private rotateClosedFile(callback: (error?: Error | null) => void): void {
    void this.rotateClosedFileAsync()
      .then(() => callback())
      .catch((error) => callback(error instanceof Error ? error : new Error(String(error))))
  }

  private async rotateClosedFileAsync(): Promise<void> {
    if (!(await pathExists(this.currentPath))) {
      this.currentSize = 0
      this.stream = this.openStream()
      return
    }

    const rotatedPath = this.nextRotatedPath()
    try {
      await rename(this.currentPath, rotatedPath)
    } catch {
      const fallbackPath = this.nextRotatedPath()
      await rename(this.currentPath, fallbackPath)
    }
    this.currentSize = 0
    this.cleanup()
    this.stream = this.openStream()
  }

  private openStream(): WriteStream {
    const stream = createWriteStream(this.currentPath, { flags: 'a' })
    stream.on('error', () => {
    })
    return stream
  }

  private closeStream(stream: WriteStream, callback: (error?: Error | null) => void): void {
    let settled = false
    const settle = (error?: Error | null) => {
      if (settled) return
      settled = true
      stream.off('error', onError)
      stream.off('close', onClose)
      callback(error)
    }
    const onError = (error: Error) => settle(error)
    const onClose = () => settle()

    stream.once('error', onError)
    stream.once('close', onClose)
    stream.end()
  }

  private readCurrentSize(): number {
    try {
      return statSync(this.currentPath).size
    } catch {
      return 0
    }
  }

  private nextRotatedPath(): string {
    const timestamp = new Date()
      .toISOString()
      .replace(/[-:]/g, '')
      .replace(/\.\d{3}Z$/, 'Z')
    return join(this.options.directory, `juhe-ai.${timestamp}.${randomUUID()}.log`)
  }

}

class MultiDestinationLogStream extends Writable {
  constructor(private readonly destinations: Writable[]) {
    super()
  }

  _write(chunk: Buffer | string, encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    const rawChunk = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk, encoding)
    for (const destination of this.destinations) {
      try {
        destination.write(rawChunk)
      } catch {
      }
    }
    callback()
  }
}

async function pathExists(path: string): Promise<boolean> {
  try {
    await access(path)
    return true
  } catch {
    return false
  }
}

async function cleanupRotatedLogFiles(options: {
  directory: string
  protectedCurrentFileNames: ReadonlySet<string>
  maxFiles: number
  retentionDays: number
}): Promise<LogMaintenanceResult> {
  const currentFileCount = await countCurrentLogFiles(options.directory, options.protectedCurrentFileNames)
  const maxRotatedFiles = Math.max(0, options.maxFiles - currentFileCount)
  const expiresBefore = Date.now() - options.retentionDays * 24 * 60 * 60 * 1000
  const retainedRotatedFiles: LogFileMtime[] = []
  let scannedFileCount = 0
  let deletedFileCount = 0

  try {
    const directory = await opendir(options.directory)
    for await (const entry of directory) {
      scannedFileCount += 1
      if (scannedFileCount % logDirectoryScanYieldEvery === 0) {
        await yieldImmediate()
      }
      if (!entry.isFile() || !isRotatedJuheLogFileName(entry.name)) {
        continue
      }
      const path = join(options.directory, entry.name)
      let mtimeMs: number
      try {
        const fileStats = await stat(path)
        if (!fileStats.isFile()) {
          continue
        }
        mtimeMs = fileStats.mtimeMs
      } catch {
        continue
      }

      if (mtimeMs < expiresBefore || maxRotatedFiles <= 0) {
        deletedFileCount += await unlinkIfExists(path)
        continue
      }

      const overflow = retainNewestFile(retainedRotatedFiles, { path, mtimeMs }, maxRotatedFiles)
      if (overflow) {
        deletedFileCount += await unlinkIfExists(overflow.path)
      }
    }
  } catch {
  }

  return {
    scannedFileCount,
    currentFileCount,
    retainedRotatedFileCount: retainedRotatedFiles.length,
    deletedFileCount
  }
}

async function countCurrentLogFiles(directoryPath: string, protectedCurrentFileNames: ReadonlySet<string>): Promise<number> {
  let count = 0
  let scannedFileCount = 0
  try {
    const directory = await opendir(directoryPath)
    for await (const entry of directory) {
      scannedFileCount += 1
      if (scannedFileCount % logDirectoryScanYieldEvery === 0) {
        await yieldImmediate()
      }
      if (entry.isFile() && protectedCurrentFileNames.has(entry.name)) {
        count += 1
      }
    }
  } catch {
  }
  return count
}

function retainNewestFile(files: LogFileMtime[], file: LogFileMtime, maxFiles: number): LogFileMtime | undefined {
  if (maxFiles <= 0) {
    return file
  }

  let insertIndex = files.length
  for (let index = 0; index < files.length; index += 1) {
    if (file.mtimeMs > files[index].mtimeMs) {
      insertIndex = index
      break
    }
  }
  if (insertIndex >= maxFiles) {
    return file
  }
  files.splice(insertIndex, 0, file)
  return files.length > maxFiles ? files.pop() : undefined
}

async function unlinkIfExists(path: string): Promise<number> {
  try {
    await unlink(path)
    return 1
  } catch {
    return 0
  }
}

function isRotatedJuheLogFileName(fileName: string): boolean {
  return /^juhe-ai\.\d{8}T\d{6}Z\.[0-9a-f-]+\.log$/i.test(fileName)
}

export async function cleanupRotatedLogFilesForTest(options: {
  directory: string
  protectedCurrentFileNames?: Iterable<string>
  maxFiles: number
  retentionDays: number
}): Promise<LogMaintenanceResult> {
  return cleanupRotatedLogFiles({
    directory: options.directory,
    protectedCurrentFileNames: new Set(options.protectedCurrentFileNames ?? [
      'juhe-ai.log',
      'juhe-ai.worker.log',
      'juhe-ai.db-service.log'
    ]),
    maxFiles: options.maxFiles,
    retentionDays: options.retentionDays
  })
}

const fileLogStream = runtimeConfig.log.fileEnabled
  ? new RotatingFileLogStream({
    directory: runtimeConfig.log.directory,
    fileName: runtimeConfig.processRole === 'worker'
      ? 'juhe-ai.worker.log'
      : runtimeConfig.processRole === 'db-service'
        ? 'juhe-ai.db-service.log'
        : 'juhe-ai.log',
    maxFileBytes: runtimeConfig.log.maxFileBytes,
    retentionDays: runtimeConfig.log.retentionDays,
    maxFiles: runtimeConfig.log.maxFiles
  })
  : undefined

const logDestinations: Writable[] = [
  ...(runtimeConfig.log.consoleEnabled ? [process.stdout] : []),
  ...(fileLogStream ? [fileLogStream] : [])
]

const loggerOptions: LoggerOptions = {
  level: runtimeConfig.log.level,
  base: undefined,
  timestamp: pino.stdTimeFunctions.isoTime,
  formatters: {
    level: (label) => ({ level: label })
  },
  serializers: {
    err: pino.stdSerializers.err
  }
}

export const logger: Logger = pino(
  loggerOptions,
  logDestinations.length > 0 ? new MultiDestinationLogStream(logDestinations) : new Writable({ write: (_chunk, _encoding, callback) => callback() })
)

export function startLogMaintenance(): void {
  if (!fileLogStream) {
    return
  }

  fileLogStream.cleanup()
  const timer = setInterval(() => fileLogStream.cleanup(), runtimeConfig.log.cleanupIntervalMinutes * 60 * 1000)
  timer.unref()
}

let fatalProcessExitStarted = false

export function installProcessLogHandlers(): void {
  process.on('unhandledRejection', (reason) => {
    logger.error(errorLogFields(reason, { event: 'process_unhandled_rejection' }), '未处理的 Promise 拒绝')
  })

  process.on('uncaughtException', (error) => {
    if (fatalProcessExitStarted) return
    fatalProcessExitStarted = true
    try {
      writeProcessFatalDiagnostic({
        event: 'process_uncaught_exception',
        error,
        processRole: runtimeConfig.processRole,
        pid: process.pid,
        secrets: [runtimeConfig.secret]
      })
      try {
        logger.fatal(errorLogFields(error, { event: 'process_uncaught_exception' }), '未捕获异常')
      } catch (loggingError) {
        writeProcessFatalDiagnostic({
          event: 'process_uncaught_exception_logging_failed',
          error: loggingError,
          processRole: runtimeConfig.processRole,
          pid: process.pid,
          secrets: [runtimeConfig.secret]
        })
      }
    } finally {
      setImmediate(() => process.exit(1))
    }
  })
}

export function errorLogFields(error: unknown, fields: Record<string, unknown> = {}): Record<string, unknown> {
  if (error instanceof Error) {
    return { ...fields, err: pino.stdSerializers.err(error) }
  }
  return { ...fields, errorMessage: String(error) }
}

function redactSensitiveLogText(value: string): string {
  return value
}

export function redactSensitiveLogTextForTest(value: string): string {
  return redactSensitiveLogText(value)
}
