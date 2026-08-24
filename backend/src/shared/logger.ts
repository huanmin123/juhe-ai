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

import { runtimeConfig, type RuntimeConfig } from '../config/runtime.js'
import { LOG_EVENT_VERSION } from './logging/log-event-contract.js'
import { passiveScheduleDelayMs } from './passive-schedule-jitter.js'
import {
  drainProcessDiagnosticAsync,
  writeProcessDiagnosticAsync,
  writeProcessFatalDiagnostic
} from './process-fatal-diagnostic.js'
import { isCurrentRuntimeLogFileName, isRotatedRuntimeLogFileName } from './runtime-log-file-name.js'

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

export interface LogDestinationErrorMetadata {
  at: string
  destination: string
  operation: string
  name: string
  code?: string
  message: string
  syscall?: string
  path?: string
}

export interface LogDestinationStats {
  name: string
  errorCount: number
  backpressureSignalCount: number
  dropCount: number
  failureDropCount: number
  pendingCount: number
  pendingBytes: number
  failurePendingBytes: number
  needDrain: boolean
  lastError?: LogDestinationErrorMetadata
  lastDrop?: LogDropMetadata
}

export interface LogPublisherStats {
  errorCount: number
  backpressureSignalCount: number
  dropCount: number
  failureDropCount: number
  pendingCount: number
  pendingBytes: number
  failurePendingBytes: number
  needDrain: boolean
  degraded: boolean
  lastError?: LogDestinationErrorMetadata
  lastDrop?: LogDropMetadata
  destinations: LogDestinationStats[]
}

export interface LogDropMetadata {
  at: string
  destination: string
  level: string
  byteLength: number
  preview: string
  previewBytes: number
}

interface MutableLogDestinationStats extends LogDestinationStats {
  lastError?: LogDestinationErrorMetadata
  lastDrop?: LogDropMetadata
}

interface NamedLogDestination {
  name: string
  stream: Writable
}

interface LogStreamBudgetOptions {
  maxPendingBytes: number
  failureReserveBytes: number
  maxFailureSnapshotBytes: number
  onFailureDrop?: (metadata: LogDropMetadata) => void
  onDestinationError?: (metadata: LogDestinationErrorMetadata, error: unknown) => void
}

const defaultLogStreamBudgetOptions: LogStreamBudgetOptions = {
  maxPendingBytes: 64 * 1_024 * 1_024,
  failureReserveBytes: 8 * 1_024 * 1_024,
  maxFailureSnapshotBytes: 4 * 1_024
}

const errorLevelMarker = Buffer.from('"level":"error"')
const fatalLevelMarker = Buffer.from('"level":"fatal"')
const warnLevelMarker = Buffer.from('"level":"warn"')

class LogStreamDiagnostics {
  private readonly destinations = new Map<string, MutableLogDestinationStats>()
  private readonly seenErrors = new WeakSet<object>()
  private readonly diagnosedErrorDestinations = new Set<string>()
  private lastError: LogDestinationErrorMetadata | undefined
  private lastDrop: LogDropMetadata | undefined

  constructor(
    private readonly onDestinationError?: (metadata: LogDestinationErrorMetadata, error: unknown) => void
  ) {
  }

  register(destination: string): void {
    this.state(destination)
  }

  writeStarted(destination: string, byteLength: number, failure: boolean): void {
    const state = this.state(destination)
    state.pendingCount += 1
    state.pendingBytes += byteLength
    if (failure) state.failurePendingBytes += byteLength
  }

  writeCompleted(destination: string, byteLength: number, failure: boolean): void {
    const state = this.state(destination)
    state.pendingCount = Math.max(0, state.pendingCount - 1)
    state.pendingBytes = Math.max(0, state.pendingBytes - byteLength)
    if (failure) state.failurePendingBytes = Math.max(0, state.failurePendingBytes - byteLength)
  }

  backpressureSignaled(destination: string): void {
    const state = this.state(destination)
    state.backpressureSignalCount += 1
    state.needDrain = true
  }

  drained(destination: string): void {
    this.state(destination).needDrain = false
  }

  recordError(destination: string, operation: string, error: unknown): void {
    if (typeof error === 'object' && error !== null) {
      if (this.seenErrors.has(error)) return
      this.seenErrors.add(error)
    }
    const metadata = boundedLogDestinationError(destination, operation, error)
    const state = this.state(destination)
    state.errorCount += 1
    state.lastError = metadata
    this.lastError = metadata
    if (!this.diagnosedErrorDestinations.has(destination)) {
      this.diagnosedErrorDestinations.add(destination)
      try {
        this.onDestinationError?.(metadata, error)
      } catch {
      }
    }
  }

  recordDrop(destination: string, level: string, chunk: Buffer, maxPreviewBytes: number): LogDropMetadata {
    const state = this.state(destination)
    const failure = isFailureLogLevel(level)
    const previewBuffer = chunk.subarray(0, Math.max(0, maxPreviewBytes))
    const metadata: LogDropMetadata = {
      at: new Date().toISOString(),
      destination: boundedDiagnosticText(destination, 128),
      level: boundedDiagnosticText(level, 32),
      byteLength: chunk.byteLength,
      preview: previewBuffer.toString('utf8').replace(/\uFFFD$/u, ''),
      previewBytes: previewBuffer.byteLength
    }
    state.dropCount += 1
    if (failure) state.failureDropCount += 1
    state.lastDrop = metadata
    this.lastDrop = metadata
    return metadata
  }

  snapshot(): LogPublisherStats {
    const destinations = [...this.destinations.values()].map((state) => ({
      ...state,
      lastError: state.lastError ? { ...state.lastError } : undefined,
      lastDrop: state.lastDrop ? { ...state.lastDrop } : undefined
    }))
    const errorCount = destinations.reduce((total, state) => total + state.errorCount, 0)
    const backpressureSignalCount = destinations.reduce(
      (total, state) => total + state.backpressureSignalCount,
      0
    )
    const dropCount = destinations.reduce((total, state) => total + state.dropCount, 0)
    const failureDropCount = destinations.reduce((total, state) => total + state.failureDropCount, 0)
    const pendingCount = destinations.reduce((total, state) => total + state.pendingCount, 0)
    const pendingBytes = destinations.reduce((total, state) => total + state.pendingBytes, 0)
    const failurePendingBytes = destinations.reduce((total, state) => total + state.failurePendingBytes, 0)
    const needDrain = destinations.some((state) => state.needDrain)
    return {
      errorCount,
      backpressureSignalCount,
      dropCount,
      failureDropCount,
      pendingCount,
      pendingBytes,
      failurePendingBytes,
      needDrain,
      degraded: errorCount > 0 || needDrain || dropCount > 0,
      lastError: this.lastError ? { ...this.lastError } : undefined,
      lastDrop: this.lastDrop ? { ...this.lastDrop } : undefined,
      destinations
    }
  }

  destinationSnapshot(destination: string): LogDestinationStats {
    const state = this.state(destination)
    return {
      ...state,
      lastError: state.lastError ? { ...state.lastError } : undefined,
      lastDrop: state.lastDrop ? { ...state.lastDrop } : undefined
    }
  }

  private state(destination: string): MutableLogDestinationStats {
    const existing = this.destinations.get(destination)
    if (existing) return existing
    const created: MutableLogDestinationStats = {
      name: destination,
      errorCount: 0,
      backpressureSignalCount: 0,
      dropCount: 0,
      failureDropCount: 0,
      pendingCount: 0,
      pendingBytes: 0,
      failurePendingBytes: 0,
      needDrain: false
    }
    this.destinations.set(destination, created)
    return created
  }
}

function boundedLogDestinationError(
  destination: string,
  operation: string,
  error: unknown
): LogDestinationErrorMetadata {
  const errorRecord = typeof error === 'object' && error !== null
    ? error as Record<string, unknown>
    : undefined
  return {
      at: new Date().toISOString(),
    destination: boundedDiagnosticText(destination, 128),
    operation: boundedDiagnosticText(operation, 64),
    name: boundedDiagnosticText(error instanceof Error ? error.name : 'Error', 128),
    ...(typeof errorRecord?.code === 'string'
      ? { code: boundedDiagnosticText(errorRecord.code, 128) }
      : {}),
    message: boundedDiagnosticText(error instanceof Error ? error.message : String(error), 1_024),
    ...(typeof errorRecord?.syscall === 'string'
      ? { syscall: boundedDiagnosticText(errorRecord.syscall, 128) }
      : {}),
    ...(typeof errorRecord?.path === 'string'
      ? { path: boundedDiagnosticText(errorRecord.path, 1_024) }
      : {})
  }
}

function boundedDiagnosticText(value: string, maxLength: number): string {
  return value.length <= maxLength ? value : value.slice(0, maxLength)
}

function logChunkLevel(chunk: Buffer): string {
  if (chunk.includes(fatalLevelMarker)) return 'fatal'
  if (chunk.includes(errorLevelMarker)) return 'error'
  if (chunk.includes(warnLevelMarker)) return 'warn'
  return 'info'
}

function isFailureLogLevel(level: string): boolean {
  return level === 'error' || level === 'fatal'
}

type RotatedLogCleanupProtectionPredicate = (
  path: string,
  fileSize: number,
  fileIdentity: string
) => Promise<boolean>

let rotatedLogCleanupProtectionPredicate: RotatedLogCleanupProtectionPredicate | undefined

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
    'juhe-ai.db-service.log',
    'juhe-ai.ingest-worker.log',
    'juhe-ai.stats-worker.log',
    'juhe-ai.ops-worker.log',
    'juhe-ai.temporary-maintenance-worker.log'
  ])

  constructor(
    private readonly options: RotatingFileLogStreamOptions,
    private readonly diagnostics?: LogStreamDiagnostics,
    private readonly destinationName = 'file'
  ) {
    super()
    this.diagnostics?.register(this.destinationName)
    this.on('error', (error) => {
      this.diagnostics?.recordError(this.destinationName, 'rotating_stream', error)
    })
    mkdirSync(options.directory, { recursive: true })
    this.currentPath = join(options.directory, options.fileName)
    this.currentSize = this.readCurrentSize()
    this.stream = this.openStream()
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
      retentionDays: this.options.retentionDays,
      canDeleteRotatedFile: rotatedLogCleanupProtectionPredicate
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

  _writev(
    chunks: Array<{ chunk: Buffer | string; encoding: BufferEncoding }>,
    callback: (error?: Error | null) => void
  ): void {
    try {
      const buffers = chunks.map(({ chunk, encoding }) => (
        Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk, encoding)
      ))
      this.writeBufferVector(buffers, callback)
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

  private writeBufferVector(buffers: Buffer[], callback: (error?: Error | null) => void): void {
    if (buffers.length === 0) {
      callback()
      return
    }
    const availableBytes = Math.max(0, this.options.maxFileBytes - this.currentSize)
    if (this.currentSize > 0 && buffers[0]!.byteLength > availableBytes) {
      this.rotate((error) => {
        if (error) {
          callback(error)
          return
        }
        this.writeBufferVector(buffers, callback)
      })
      return
    }
    let selectedCount = 0
    let selectedBytes = 0
    const capacity = this.currentSize === 0 ? this.options.maxFileBytes : availableBytes
    while (selectedCount < buffers.length) {
      const nextBytes = buffers[selectedCount]!.byteLength
      if (selectedCount > 0 && selectedBytes + nextBytes > capacity) break
      if (selectedCount === 0 && nextBytes > capacity && this.currentSize === 0) {
        selectedCount = 1
        selectedBytes = nextBytes
        break
      }
      if (selectedBytes + nextBytes > capacity) break
      selectedBytes += nextBytes
      selectedCount += 1
    }
    if (selectedCount === 0) {
      this.rotate((error) => {
        if (error) {
          callback(error)
          return
        }
        this.writeBufferVector(buffers, callback)
      })
      return
    }
    const selected = selectedCount === 1 ? buffers[0]! : Buffer.concat(buffers.slice(0, selectedCount), selectedBytes)
    this.writeBuffer(selected, (error) => {
      if (error || selectedCount >= buffers.length) {
        callback(error)
        return
      }
      this.rotate((rotateError) => {
        if (rotateError) {
          callback(rotateError)
          return
        }
        this.writeBufferVector(buffers.slice(selectedCount), callback)
      })
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
    this.stream = this.openStream()
  }

  private openStream(): WriteStream {
    const stream = createWriteStream(this.currentPath, { flags: 'a' })
    stream.on('error', (error) => {
      this.diagnostics?.recordError(this.destinationName, 'file_stream', error)
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
    return join(this.options.directory, rotatedLogFileName(this.options.fileName, new Date(), randomUUID()))
  }

}

export function rotatedLogFileName(fileName: string, timestamp: Date, uniqueId: string): string {
  const timestampText = timestamp
    .toISOString()
    .replace(/[-:]/g, '')
    .replace(/\.\d{3}Z$/, 'Z')
  const baseName = fileName.endsWith('.log') ? fileName.slice(0, -'.log'.length) : fileName
  return `${baseName}.${timestampText}.${uniqueId}.log`
}

class MultiDestinationLogStream extends Writable {
  private readonly listenerBindings: Array<{
    stream: Writable
    onError: (error: Error) => void
    onDrain: () => void
  }> = []
  private readonly failureDropDiagnosticEmitted = new Set<string>()

  constructor(
    private readonly destinations: NamedLogDestination[],
    private readonly diagnostics: LogStreamDiagnostics,
    private readonly budgetOptions: LogStreamBudgetOptions = defaultLogStreamBudgetOptions
  ) {
    super()
    this.on('error', (error) => {
      this.diagnostics.recordError('multiplexer', 'stream', error)
    })
    for (const destination of destinations) {
      this.diagnostics.register(destination.name)
      const onError = (error: Error) => {
        this.diagnostics.recordError(destination.name, 'destination', error)
      }
      const onDrain = () => {
        this.diagnostics.drained(destination.name)
      }
      destination.stream.on('error', onError)
      destination.stream.on('drain', onDrain)
      this.listenerBindings.push({ stream: destination.stream, onError, onDrain })
    }
  }

  _write(chunk: Buffer | string, encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    const rawChunk = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk, encoding)
    const level = logChunkLevel(rawChunk)
    const failure = isFailureLogLevel(level)
    for (const destination of this.destinations) {
      const byteLength = rawChunk.byteLength
      const destinationState = this.diagnostics.destinationSnapshot(destination.name)
      const normalPendingBytes = destinationState.pendingBytes - destinationState.failurePendingBytes
      const exceedsBudget = failure
        ? destinationState.failurePendingBytes + byteLength > this.budgetOptions.failureReserveBytes
        : normalPendingBytes + byteLength > this.budgetOptions.maxPendingBytes
      if (exceedsBudget) {
        const drop = this.diagnostics.recordDrop(
          destination.name,
          level,
          rawChunk,
          this.budgetOptions.maxFailureSnapshotBytes
        )
        if (failure && !this.failureDropDiagnosticEmitted.has(destination.name)) {
          this.failureDropDiagnosticEmitted.add(destination.name)
          try {
            this.budgetOptions.onFailureDrop?.(drop)
          } catch {
          }
        }
        continue
      }
      this.diagnostics.writeStarted(destination.name, byteLength, failure)
      try {
        const accepted = destination.stream.write(rawChunk, (error?: Error | null) => {
          this.diagnostics.writeCompleted(destination.name, byteLength, failure)
          if (error) this.diagnostics.recordError(destination.name, 'write', error)
        })
        if (!accepted) this.diagnostics.backpressureSignaled(destination.name)
      } catch (error) {
        this.diagnostics.writeCompleted(destination.name, byteLength, failure)
        this.diagnostics.recordError(destination.name, 'write', error)
      }
    }
    callback()
  }

  _destroy(error: Error | null, callback: (error?: Error | null) => void): void {
    for (const binding of this.listenerBindings) {
      binding.stream.off('error', binding.onError)
      binding.stream.off('drain', binding.onDrain)
    }
    callback(error)
  }
}

export function createObservedLogStreamForTest(
  destinations: NamedLogDestination[],
  budgetOptions: Partial<LogStreamBudgetOptions> = {}
): {
  stream: Writable
  stats: () => LogPublisherStats
} {
  const diagnostics = new LogStreamDiagnostics(budgetOptions.onDestinationError)
  const stream = new MultiDestinationLogStream(destinations, diagnostics, {
    ...defaultLogStreamBudgetOptions,
    ...budgetOptions
  })
  return {
    stream,
    stats: () => diagnostics.snapshot()
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
  canDeleteRotatedFile?: RotatedLogCleanupProtectionPredicate
}): Promise<LogMaintenanceResult> {
  const currentFileCount = await countCurrentLogFiles(options.directory, options.protectedCurrentFileNames)
  const maxRotatedFiles = Math.max(0, options.maxFiles - currentFileCount)
  const expiresBefore = Date.now() - options.retentionDays * 24 * 60 * 60 * 1000
  const retainedRotatedFiles: LogFileMtime[] = []
  let protectedRotatedFileCount = 0
  let scannedFileCount = 0
  let deletedFileCount = 0

  try {
    const directory = await opendir(options.directory)
    for await (const entry of directory) {
      scannedFileCount += 1
      if (scannedFileCount % logDirectoryScanYieldEvery === 0) {
        await yieldImmediate()
      }
      if (!entry.isFile() || !isRotatedRuntimeLogFileName(entry.name)) {
        continue
      }
      const path = join(options.directory, entry.name)
      let mtimeMs: number
      let fileSize: number
      let fileIdentity: string
      try {
        const fileStats = await stat(path)
        if (!fileStats.isFile()) {
          continue
        }
        mtimeMs = fileStats.mtimeMs
        fileSize = fileStats.size
        fileIdentity = [fileStats.dev, fileStats.ino, Math.trunc(fileStats.birthtimeMs)].join(':')
      } catch {
        continue
      }

      const canDelete = options.canDeleteRotatedFile
        ? await options.canDeleteRotatedFile(path, fileSize, fileIdentity).catch(() => false)
        : false
      if (!canDelete) {
        protectedRotatedFileCount += 1
        const allowedDeletableFiles = Math.max(0, maxRotatedFiles - protectedRotatedFileCount)
        while (retainedRotatedFiles.length > allowedDeletableFiles) {
          const overflow = retainedRotatedFiles.pop()
          if (overflow) deletedFileCount += await unlinkIfExists(overflow.path)
        }
        continue
      }

      if (mtimeMs < expiresBefore || maxRotatedFiles <= 0) {
        deletedFileCount += await unlinkIfExists(path)
        continue
      }

      const overflow = retainNewestFile(
        retainedRotatedFiles,
        { path, mtimeMs },
        Math.max(0, maxRotatedFiles - protectedRotatedFileCount)
      )
      if (overflow) {
        deletedFileCount += await unlinkIfExists(overflow.path)
      }
    }
  } catch {
  }

  return {
    scannedFileCount,
    currentFileCount,
    retainedRotatedFileCount: protectedRotatedFileCount + retainedRotatedFiles.length,
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
      if (entry.isFile() && (protectedCurrentFileNames.has(entry.name) || isCurrentRuntimeLogFileName(entry.name))) {
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

export function setRotatedLogCleanupProtectionPredicate(predicate: RotatedLogCleanupProtectionPredicate): void {
  rotatedLogCleanupProtectionPredicate = predicate
}

export async function cleanupRotatedLogFilesForTest(options: {
  directory: string
  protectedCurrentFileNames?: Iterable<string>
  maxFiles: number
  retentionDays: number
  canDeleteRotatedFile?: RotatedLogCleanupProtectionPredicate
}): Promise<LogMaintenanceResult> {
  return cleanupRotatedLogFiles({
    directory: options.directory,
    protectedCurrentFileNames: new Set(options.protectedCurrentFileNames ?? [
      'juhe-ai.log',
      'juhe-ai.worker.log',
      'juhe-ai.db-service.log',
      'juhe-ai.ingest-worker.log',
      'juhe-ai.stats-worker.log',
      'juhe-ai.ops-worker.log',
      'juhe-ai.temporary-maintenance-worker.log'
    ]),
    maxFiles: options.maxFiles,
    retentionDays: options.retentionDays,
    canDeleteRotatedFile: options.canDeleteRotatedFile ?? (async () => true)
  })
}

const logStreamDiagnostics = new LogStreamDiagnostics((metadata, error) => {
  const diagnosticError = Object.assign(
    new Error(`日志目的地 ${metadata.destination} 在 ${metadata.operation} 阶段失败：${metadata.message}`, {
      cause: error
    }),
    metadata.code ? { code: metadata.code } : {}
  )
  writeProcessDiagnosticAsync({
    event: 'log_destination_failed',
    error: diagnosticError,
    processRole: runtimeConfig.processRole,
    pid: process.pid,
    maxBytes: defaultLogStreamBudgetOptions.maxFailureSnapshotBytes
  }, 'critical')
})

const fileLogStream = runtimeConfig.log.fileEnabled
  ? new RotatingFileLogStream({
    directory: runtimeConfig.log.directory,
    fileName: runtimeLogFileName(),
    maxFileBytes: runtimeConfig.log.maxFileBytes,
    retentionDays: runtimeConfig.log.retentionDays,
    maxFiles: runtimeConfig.log.maxFiles
  }, logStreamDiagnostics, 'file')
  : undefined

const logDestinations: NamedLogDestination[] = [
  ...(runtimeConfig.log.consoleEnabled ? [{ name: 'console', stream: process.stdout }] : []),
  ...(fileLogStream ? [{ name: 'file', stream: fileLogStream }] : [])
]

const loggerOptions: LoggerOptions = {
  level: runtimeConfig.log.level,
  base: undefined,
  timestamp: () => `,"time":"${new Date().toISOString()}"`,
  mixin: () => ({
    version: LOG_EVENT_VERSION,
    service: 'juhe-ai',
    role: runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole
      ? runtimeConfig.workerRole
      : runtimeConfig.processRole,
    instanceId: runtimeConfig.instanceId,
    ...(runtimeConfig.processRole === 'worker'
      ? { workerReplicaIndex: runtimeConfig.workerReplicaIndex }
      : {})
  }),
  formatters: {
    level: (label) => ({ level: label })
  },
  serializers: {
    err: pino.stdSerializers.err
  }
}

const multiDestinationLogStream = logDestinations.length > 0
  ? new MultiDestinationLogStream(logDestinations, logStreamDiagnostics, {
    ...defaultLogStreamBudgetOptions,
    onFailureDrop: (drop) => {
      writeProcessDiagnosticAsync({
        event: 'log_failure_reserve_exhausted',
        error: new Error(drop.preview || `日志失败预留已耗尽，原始字节数 ${drop.byteLength}`),
        processRole: runtimeConfig.processRole,
        pid: process.pid,
        maxBytes: defaultLogStreamBudgetOptions.maxFailureSnapshotBytes
      }, 'critical')
    }
  })
  : undefined

export const logger: Logger = pino(
  loggerOptions,
  multiDestinationLogStream ?? new Writable({ write: (_chunk, _encoding, callback) => callback() })
)

function runtimeLogFileName(): string {
  const legacyName = runtimeConfig.processRole === 'worker'
    ? runtimeConfig.workerRole === 'worker'
      ? 'juhe-ai.worker.log'
      : `juhe-ai.${runtimeConfig.workerRole}.log`
    : runtimeConfig.processRole === 'db-service'
      ? 'juhe-ai.db-service.log'
      : 'juhe-ai.log'
  if (runtimeConfig.runtimeMode !== 'performance') return legacyName
  const baseName = legacyName.slice(0, -'.log'.length)
  return `${baseName}.${runtimeConfig.instanceId}.log`
}

export function logPublisherStats(): LogPublisherStats {
  return logStreamDiagnostics.snapshot()
}

export function startLogMaintenance(): void {
  if (!isLogMaintenanceOwner()) return
  if (logMaintenanceTimer) return
  if (!fileLogStream) return
  logMaintenanceKickoff = setImmediate(() => {
    logMaintenanceKickoff = undefined
    fileLogStream.cleanup()
  })
  scheduleNextLogMaintenance()
}

let fatalProcessExitStarted = false
let logMaintenanceTimer: NodeJS.Timeout | undefined
let logMaintenanceKickoff: NodeJS.Immediate | undefined

function scheduleNextLogMaintenance(): void {
  const stream = fileLogStream
  if (!stream) return
  const intervalMs = runtimeConfig.log.cleanupIntervalMinutes * 60 * 1000
  logMaintenanceTimer = setTimeout(() => {
    logMaintenanceTimer = undefined
    stream.cleanup()
    scheduleNextLogMaintenance()
  }, passiveScheduleDelayMs(intervalMs))
  logMaintenanceTimer.unref()
}

type LogMaintenanceOwnerContext = Pick<RuntimeConfig, 'runtimeMode' | 'processRole' | 'workerRole' | 'workerReplicaIndex'>

export function isLogMaintenanceOwner(context: LogMaintenanceOwnerContext = runtimeConfig): boolean {
  if (context.processRole !== 'worker' || context.workerReplicaIndex !== 0) return false
  return context.runtimeMode === 'performance'
    ? context.workerRole === 'log-worker'
    : context.workerRole === 'ingest-worker'
}

export function installProcessLogHandlers(): void {
  process.on('unhandledRejection', (reason) => {
    logger.error(errorLogFields(reason, { event: 'process_unhandled_rejection' }), '未处理的 Promise 拒绝')
  })

  process.on('uncaughtException', (error) => {
    if (fatalProcessExitStarted) return
    fatalProcessExitStarted = true
    const errorCode = nodeErrorCode(error)
    try {
      writeProcessFatalDiagnostic({
        event: 'process_uncaught_exception',
        error,
        processRole: runtimeConfig.processRole,
        pid: process.pid,
        epipeSource: errorCode === 'EPIPE' ? 'unattributed' : undefined,
        secrets: [runtimeConfig.secret]
      })
      try {
        logger.fatal(errorLogFields(error, {
          event: 'process_uncaught_exception',
          epipeSource: errorCode === 'EPIPE' ? 'unattributed' : undefined
        }), '未捕获异常')
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

function nodeErrorCode(error: unknown): string | undefined {
  if (!error || typeof error !== 'object') return undefined
  const code = (error as { code?: unknown }).code
  return typeof code === 'string' ? code : undefined
}

export async function closeLogger(timeoutMs = 30_000): Promise<void> {
  if (logMaintenanceKickoff) {
    clearImmediate(logMaintenanceKickoff)
    logMaintenanceKickoff = undefined
  }
  if (logMaintenanceTimer) {
    clearTimeout(logMaintenanceTimer)
    logMaintenanceTimer = undefined
  }
  if (!fileLogStream || fileLogStream.writableEnded || fileLogStream.destroyed) {
    await drainProcessDiagnosticAsync()
    return
  }
  await new Promise<void>((resolve) => {
    let settled = false
    const settle = () => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      fileLogStream.off('error', settle)
      fileLogStream.off('finish', settle)
      resolve()
    }
    const timeout = setTimeout(() => {
      writeProcessDiagnosticAsync({
        event: 'log_shutdown_drain_timeout',
        error: new Error(`日志关闭排空超过 ${timeoutMs}ms`),
        processRole: runtimeConfig.processRole,
        pid: process.pid
      })
      fileLogStream.destroy()
      settle()
    }, Math.max(1, timeoutMs))
    fileLogStream.once('error', settle)
    fileLogStream.once('finish', settle)
    fileLogStream.end()
  })
  await drainProcessDiagnosticAsync()
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
