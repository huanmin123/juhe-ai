import { fileURLToPath } from 'node:url'
import { Worker } from 'node:worker_threads'

export interface LogWriterWorkerOptions {
  directory: string
  fileName: string
  fileEnabled: boolean
  consoleEnabled: boolean
  maxFileBytes: number
  retentionDays: number
  maxFiles: number
}

export interface LogWriterWorkerClientOptions extends LogWriterWorkerOptions {
  onLine?: (chunk: Buffer, options?: LogWriterWorkerLineOptions) => void
}

export interface LogWriterWorkerLineOptions {
  logFile?: string
  logFileIdentity?: string
  logOffset?: number
  parsedMetadata?: LogWriterWorkerParsedMetadata
}

export interface LogWriterWorkerParsedMetadata {
  time?: string
  level?: string | number
  traceId?: string
  event?: string
  message?: string
  errorMessage?: string
}

interface WorkerReply {
  id?: number
  type: 'ack' | 'closed' | 'error' | 'line'
  error?: string
  chunk?: Uint8Array
  chunks?: Uint8Array[]
  lineOptions?: LogWriterWorkerLineOptions
  lineOptionsList?: LogWriterWorkerLineOptions[]
}

const currentModulePath = fileURLToPath(import.meta.url)

export class LogWriterWorkerClient {
  private readonly worker: Worker
  private readonly callbacks = new Map<number, (error?: Error | null) => void>()
  private nextID = 1
  private closeResolve?: () => void
  private closeReject?: () => void
  private closed = false
  private readonly onLine?: (chunk: Buffer, options?: LogWriterWorkerLineOptions) => void

  constructor(options: LogWriterWorkerClientOptions) {
    const { onLine, ...workerOptions } = options
    this.onLine = onLine
    this.worker = new Worker(resolveWorkerPath(), { execArgv: workerExecArgv(), workerData: workerOptions })
    this.worker.on('message', (message: WorkerReply) => this.handleMessage(message))
    this.worker.on('error', (error) => this.fail(error))
    this.worker.on('exit', (code) => {
      if (code !== 0 && (this.callbacks.size > 0 || this.closeReject)) this.fail(new Error(`日志 writer worker 异常退出: ${code}`))
    })
    this.worker.unref()
  }

  write(chunk: Buffer, callback: (error?: Error | null) => void): void {
    this.postWriteMessage({ type: 'write', chunk }, callback)
  }

  writeBatch(chunks: Buffer[], callback: (error?: Error | null) => void): void {
    if (chunks.length === 0) {
      callback()
      return
    }
    this.postWriteMessage({ type: 'write_batch', chunks }, callback)
  }

  private postWriteMessage(
    payload: { type: 'write'; chunk: Buffer } | { type: 'write_batch'; chunks: Buffer[] },
    callback: (error?: Error | null) => void
  ): void {
    if (this.closed) {
      callback(new Error('日志 writer worker 已关闭'))
      return
    }
    const id = this.nextID++
    this.worker.ref()
    this.callbacks.set(id, callback)
    try {
      this.worker.postMessage({ ...payload, id })
    } catch (error) {
      this.callbacks.delete(id)
      callback(error instanceof Error ? error : new Error(String(error)))
    }
  }

  async close(timeoutMs = 3_000): Promise<boolean> {
    if (this.closed) return true
    this.closed = true
    this.worker.ref()
    const closePromise = new Promise<boolean>((resolve) => {
      this.closeResolve = () => resolve(true)
      this.closeReject = () => resolve(false)
      try {
        this.worker.postMessage({ type: 'close' })
      } catch {
        this.forceClose()
        resolve(false)
      }
    })
    let timeout: NodeJS.Timeout | undefined
    const result = await Promise.race([
      closePromise,
      new Promise<boolean>((resolve) => {
        timeout = setTimeout(() => {
          this.forceClose()
          resolve(false)
        }, Math.max(0, timeoutMs))
      })
    ])
    if (timeout) clearTimeout(timeout)
    return result
  }

  forceClose(): void {
    this.closed = true
    const error = new Error('日志 writer worker 关闭超时')
    this.fail(error)
    this.worker.unref()
    void this.worker.terminate().catch(() => undefined)
  }

  private handleMessage(message: WorkerReply): void {
    if (message.type === 'line' && message.chunk) {
      this.onLine?.(Buffer.from(message.chunk.buffer, message.chunk.byteOffset, message.chunk.byteLength), message.lineOptions)
      return
    }
    if (message.type === 'line' && message.chunks) {
      for (const [index, chunk] of message.chunks.entries()) {
        this.onLine?.(
          Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength),
          message.lineOptionsList?.[index]
        )
      }
      return
    }
    if (message.type === 'ack' && message.id !== undefined) {
      const callback = this.callbacks.get(message.id)
      if (!callback) return
      this.callbacks.delete(message.id)
      callback(message.error ? new Error(message.error) : undefined)
      if (this.callbacks.size === 0 && !this.closed) this.worker.unref()
      return
    }
    if (message.type === 'closed') {
      this.closeResolve?.()
      this.closeResolve = undefined
      this.closeReject = undefined
      return
    }
    if (message.type === 'error') this.fail(new Error(message.error ?? '日志 writer worker 失败'))
  }

  private fail(error: Error): void {
    for (const callback of this.callbacks.values()) callback(error)
    this.callbacks.clear()
    this.closeReject?.()
    this.closeResolve = undefined
    this.closeReject = undefined
  }
}

function resolveWorkerPath(): string {
  return currentModulePath.endsWith('.ts')
    ? currentModulePath.replace(/log-writer-worker-client\.ts$/, 'log-writer-worker.ts')
    : currentModulePath.replace(/log-writer-worker-client\.js$/, 'log-writer-worker.js')
}

function workerExecArgv(): string[] {
  const args = stripNodeEvalExecArgv(process.execArgv.filter((arg) => !arg.startsWith('--inspect')))
  if (!currentModulePath.endsWith('.ts') || args.some((arg) => arg.includes('tsx'))) return args
  return [...args, '--import', 'tsx']
}

function stripNodeEvalExecArgv(args: string[]): string[] {
  const output: string[] = []
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (arg === '-e' || arg === '--eval' || arg === '-p' || arg === '--print') {
      index += 1
      continue
    }
    if (arg.startsWith('--eval=') || arg.startsWith('--print=')) continue
    output.push(arg)
  }
  return output
}
