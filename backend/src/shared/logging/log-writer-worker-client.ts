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

interface WorkerReply {
  id?: number
  type: 'ack' | 'closed' | 'error'
  error?: string
}

const currentModulePath = fileURLToPath(import.meta.url)

export class LogWriterWorkerClient {
  private readonly worker: Worker
  private readonly callbacks = new Map<number, (error?: Error | null) => void>()
  private nextID = 1
  private closeResolve?: () => void
  private closeReject?: (error: Error) => void
  private closed = false

  constructor(options: LogWriterWorkerOptions) {
    this.worker = new Worker(resolveWorkerPath(), { execArgv: workerExecArgv(), workerData: options })
    this.worker.on('message', (message: WorkerReply) => this.handleMessage(message))
    this.worker.on('error', (error) => this.fail(error))
    this.worker.on('exit', (code) => {
      if (!this.closed && code !== 0) this.fail(new Error(`日志 writer worker 异常退出: ${code}`))
    })
    this.worker.unref()
  }

  write(chunk: Buffer, callback: (error?: Error | null) => void): void {
    if (this.closed) {
      callback(new Error('日志 writer worker 已关闭'))
      return
    }
    const id = this.nextID++
    this.worker.ref()
    this.callbacks.set(id, callback)
    try {
      this.worker.postMessage({ type: 'write', id, chunk })
    } catch (error) {
      this.callbacks.delete(id)
      callback(error instanceof Error ? error : new Error(String(error)))
    }
  }

  close(): Promise<void> {
    if (this.closed) return Promise.resolve()
    this.closed = true
    this.worker.ref()
    return new Promise<void>((resolve, reject) => {
      this.closeResolve = resolve
      this.closeReject = reject
      this.worker.postMessage({ type: 'close' })
    })
  }

  private handleMessage(message: WorkerReply): void {
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
    this.closeReject?.(error)
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
