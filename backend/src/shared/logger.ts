import { randomUUID } from 'node:crypto'
import {
  createWriteStream,
  existsSync,
  mkdirSync,
  renameSync,
  statSync,
  type WriteStream
} from 'node:fs'
import { readdir, stat, unlink } from 'node:fs/promises'
import { join } from 'node:path'
import { Writable } from 'node:stream'

import pino, { type Logger, type LoggerOptions } from 'pino'

import { runtimeConfig } from '../config/runtime.js'
import { RuntimeLogIndexStream } from '../modules/runtime-logs/runtime-log-stream.js'

interface RotatingFileLogStreamOptions {
  directory: string
  fileName: string
  maxFileBytes: number
  retentionDays: number
  maxFiles: number
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
    const rotatedFiles = await this.listRotatedFiles()
    const currentFileCount = (await this.listCurrentLogFiles()).length
    const maxRotatedFiles = Math.max(0, this.options.maxFiles - currentFileCount)
    const expiresBefore = Date.now() - this.options.retentionDays * 24 * 60 * 60 * 1000
    const expiredFiles = rotatedFiles.filter((file) => file.mtimeMs < expiresBefore)
    const overflowFiles = rotatedFiles
      .filter((file) => file.mtimeMs >= expiresBefore)
      .sort((left, right) => right.mtimeMs - left.mtimeMs)
      .slice(maxRotatedFiles)

    for (const file of [...expiredFiles, ...overflowFiles]) {
      try {
        await unlink(file.path)
      } catch {
      }
    }
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
    if (!existsSync(this.currentPath)) {
      this.currentSize = 0
      this.stream = this.openStream()
      callback()
      return
    }

    const rotatedPath = this.nextRotatedPath()
    try {
      renameSync(this.currentPath, rotatedPath)
    } catch {
      const fallbackPath = this.nextRotatedPath()
      renameSync(this.currentPath, fallbackPath)
    }
    this.currentSize = 0
    this.cleanup()
    this.stream = this.openStream()
    callback()
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
      return existsSync(this.currentPath) ? statSync(this.currentPath).size : 0
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

  private async listRotatedFiles(): Promise<Array<{ path: string; mtimeMs: number }>> {
    try {
      const fileNames = await readdir(this.options.directory)
      const files: Array<{ path: string; mtimeMs: number }> = []
      for (const fileName of fileNames) {
        if (!/^juhe-ai\.\d{8}T\d{6}Z\.[0-9a-f-]+\.log$/i.test(fileName)) {
          continue
        }
        const path = join(this.options.directory, fileName)
        try {
          files.push({ path, mtimeMs: (await stat(path)).mtimeMs })
        } catch {
        }
      }
      return files
    } catch {
      return []
    }
  }

  private async listCurrentLogFiles(): Promise<Array<{ path: string; mtimeMs: number }>> {
    try {
      const fileNames = await readdir(this.options.directory)
      const files: Array<{ path: string; mtimeMs: number }> = []
      for (const fileName of fileNames) {
        if (!this.protectedCurrentFileNames.has(fileName)) {
          continue
        }
        const path = join(this.options.directory, fileName)
        try {
          files.push({ path, mtimeMs: (await stat(path)).mtimeMs })
        } catch {
        }
      }
      return files
    } catch {
      return []
    }
  }
}

class MultiDestinationLogStream extends Writable {
  constructor(private readonly destinations: Writable[]) {
    super()
  }

  _write(chunk: Buffer | string, encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    for (const destination of this.destinations) {
      try {
        destination.write(chunk, encoding)
      } catch {
      }
    }
    callback()
  }
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
const runtimeLogIndexStream = new RuntimeLogIndexStream()

const logDestinations: Writable[] = [
  ...(runtimeConfig.log.consoleEnabled ? [process.stdout] : []),
  ...(fileLogStream ? [fileLogStream] : []),
  runtimeLogIndexStream
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
  },
  redact: {
    censor: '[redacted]',
    paths: [
      'authorization',
      'cookie',
      'set-cookie',
      'password',
      'secret',
      'token',
      'accessToken',
      'refreshToken',
      'apiKey',
      'headers.authorization',
      'headers.cookie',
      'headers.set-cookie',
      'req.headers.authorization',
      'req.headers.cookie',
      'req.headers.set-cookie',
      '*.authorization',
      '*.cookie',
      '*.password',
      '*.secret',
      '*.token',
      '*.accessToken',
      '*.refreshToken',
      '*.apiKey'
    ]
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

export function installProcessLogHandlers(): void {
  process.on('unhandledRejection', (reason) => {
    logger.error(errorLogFields(reason, { event: 'process_unhandled_rejection' }), '未处理的 Promise 拒绝')
  })

  process.on('uncaughtException', (error) => {
    logger.fatal({ event: 'process_uncaught_exception', err: error }, '未捕获异常')
    setImmediate(() => process.exit(1))
  })
}

export function errorLogFields(error: unknown, fields: Record<string, unknown> = {}): Record<string, unknown> {
  if (error instanceof Error) {
    return { ...fields, err: error }
  }
  return { ...fields, errorMessage: String(error) }
}
