import { randomUUID } from 'node:crypto'
import {
  appendFileSync,
  existsSync,
  mkdirSync,
  readdirSync,
  renameSync,
  statSync,
  unlinkSync
} from 'node:fs'
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
  private currentSize = 0

  constructor(private readonly options: RotatingFileLogStreamOptions) {
    super()
    mkdirSync(options.directory, { recursive: true })
    this.currentPath = join(options.directory, options.fileName)
    this.currentSize = this.readCurrentSize()
    this.cleanup()
  }

  cleanup(): void {
    const rotatedFiles = this.listRotatedFiles()
    const expiresBefore = Date.now() - this.options.retentionDays * 24 * 60 * 60 * 1000
    const expiredFiles = rotatedFiles.filter((file) => file.mtimeMs < expiresBefore)
    const overflowFiles = rotatedFiles
      .filter((file) => file.mtimeMs >= expiresBefore)
      .sort((left, right) => right.mtimeMs - left.mtimeMs)
      .slice(this.options.maxFiles)

    for (const file of [...expiredFiles, ...overflowFiles]) {
      try {
        unlinkSync(file.path)
      } catch {
      }
    }
  }

  _write(chunk: Buffer | string, encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    try {
      const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk, encoding)
      if (this.currentSize > 0 && this.currentSize + buffer.byteLength > this.options.maxFileBytes) {
        this.rotate()
      }
      appendFileSync(this.currentPath, buffer)
      this.currentSize += buffer.byteLength
      callback()
    } catch (error) {
      callback(error instanceof Error ? error : new Error(String(error)))
    }
  }

  private rotate(): void {
    if (!existsSync(this.currentPath)) {
      this.currentSize = 0
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

  private listRotatedFiles(): Array<{ path: string; mtimeMs: number }> {
    try {
      return readdirSync(this.options.directory)
        .filter((fileName) => /^juhe-ai\.\d{8}T\d{6}Z\.[0-9a-f-]+\.log$/i.test(fileName))
        .map((fileName) => {
          const path = join(this.options.directory, fileName)
          return { path, mtimeMs: statSync(path).mtimeMs }
        })
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
    fileName: runtimeConfig.processRole === 'worker' ? 'juhe-ai.worker.log' : 'juhe-ai.log',
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
    logger.error(errorLogFields(reason, { event: 'process_unhandled_rejection' }), 'Unhandled promise rejection')
  })

  process.on('uncaughtException', (error) => {
    logger.fatal({ event: 'process_uncaught_exception', err: error }, 'Uncaught exception')
    setImmediate(() => process.exit(1))
  })
}

export function errorLogFields(error: unknown, fields: Record<string, unknown> = {}): Record<string, unknown> {
  if (error instanceof Error) {
    return { ...fields, err: error }
  }
  return { ...fields, errorMessage: String(error) }
}
