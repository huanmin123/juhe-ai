import { existsSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Worker } from 'node:worker_threads'

import { errorLogFields, logger } from '../../shared/logger.js'
import { gatewayJsonBodyLargeWarningBytes } from './openai-gateway-request-body.js'
import {
  OpenAIOAuthCodexAdapterError,
  type NormalizedCodexBody,
  type OpenAIOAuthCodexNormalizeInput
} from './openai-oauth-codex-normalizer.js'

type GatewayJsonWorkerJobType =
  | 'parse_json_body'
  | 'normalize_openai_oauth_codex_body'

interface GatewayJsonWorkerJob {
  id: number
  type: GatewayJsonWorkerJobType
  rawBody: Buffer
  normalizeInput?: OpenAIOAuthCodexNormalizeInput
  enqueuedAtMs: number
  startedAtMs?: number
  resolve: (value: unknown) => void
  reject: (error: Error) => void
  timeoutMs: number
  signal?: AbortSignal
  abortListener?: () => void
  timer?: NodeJS.Timeout
}

interface GatewayJsonWorkerResponse {
  id: number
  ok: boolean
  value?: unknown
  errorMessage?: string
  errorCode?: string
  errorStatusCode?: number
  errorType?: string
}

const currentModulePath = fileURLToPath(import.meta.url)
const currentModuleDir = dirname(currentModulePath)
const workerSourcePath = resolve(currentModuleDir, 'openai-gateway-json-worker.ts')
const workerDistPath = resolve(currentModuleDir, 'openai-gateway-json-worker.js')

class HeadIndexedQueue<T> {
  private items: T[] = []
  private headIndex = 0

  get length(): number {
    return this.items.length - this.headIndex
  }

  push(item: T): void {
    this.items.push(item)
  }

  shift(): T | undefined {
    if (this.length <= 0) {
      return undefined
    }
    const item = this.items[this.headIndex]
    this.headIndex += 1
    this.compactConsumedItems()
    return item
  }

  findIndex(predicate: (item: T) => boolean): number {
    for (let index = 0; index < this.length; index += 1) {
      if (predicate(this.items[this.headIndex + index])) {
        return index
      }
    }
    return -1
  }

  removeAt(index: number): T | undefined {
    if (index < 0 || index >= this.length) {
      return undefined
    }
    const physicalIndex = this.headIndex + index
    if (physicalIndex === this.headIndex) {
      return this.shift()
    }
    const [item] = this.items.splice(physicalIndex, 1)
    return item
  }

  private compactConsumedItems(): void {
    if (this.headIndex === 0) {
      return
    }
    if (this.headIndex >= this.items.length) {
      this.items = []
      this.headIndex = 0
      return
    }
    if (this.headIndex > 64 && this.headIndex * 2 > this.items.length) {
      this.items = this.items.slice(this.headIndex)
      this.headIndex = 0
    }
  }
}

let worker: Worker | undefined
let nextJobId = 1
let activeJob: GatewayJsonWorkerJob | undefined
const queuedJobs = new HeadIndexedQueue<GatewayJsonWorkerJob>()

export function parseGatewayJsonBodyInWorker(rawBody: Buffer, timeoutMs = 30000, signal?: AbortSignal): Promise<unknown> {
  return enqueueGatewayJsonWorkerJob({
    type: 'parse_json_body',
    rawBody,
    timeoutMs,
    signal
  })
}

export function normalizeOpenAIOAuthCodexBodyInWorker(
  rawBody: Buffer,
  normalizeInput: OpenAIOAuthCodexNormalizeInput,
  timeoutMs = 30000,
  signal?: AbortSignal
): Promise<NormalizedCodexBody> {
  return enqueueGatewayJsonWorkerJob<NormalizedCodexBody>({
    type: 'normalize_openai_oauth_codex_body',
    rawBody,
    normalizeInput,
    timeoutMs,
    signal
  })
}

export async function stopGatewayJsonParseWorker(): Promise<void> {
  const currentWorker = worker
  const currentJob = activeJob
  worker = undefined
  activeJob = undefined
  if (currentJob) {
    clearJobTimer(currentJob)
    removeAbortListener(currentJob)
    currentJob.reject(new Error('网关 JSON worker 已关闭'))
  }
  while (queuedJobs.length > 0) {
    const job = queuedJobs.shift()
    if (job) {
      clearJobTimer(job)
      removeAbortListener(job)
      job.reject(new Error('网关 JSON worker 已关闭'))
    }
  }
  if (currentWorker) {
    currentWorker.removeAllListeners()
    await currentWorker.terminate()
  }
}

function enqueueGatewayJsonWorkerJob<TValue>(input: {
  type: GatewayJsonWorkerJobType
  rawBody: Buffer
  normalizeInput?: OpenAIOAuthCodexNormalizeInput
  timeoutMs: number
  signal?: AbortSignal
}): Promise<TValue> {
  return new Promise((resolve, reject) => {
    if (input.signal?.aborted) {
      reject(new Error('网关 JSON worker 任务已取消'))
      return
    }
    const job: GatewayJsonWorkerJob = {
      id: nextJobId++,
      type: input.type,
      rawBody: input.rawBody,
      normalizeInput: input.normalizeInput,
      enqueuedAtMs: Date.now(),
      resolve: (value) => resolve(value as TValue),
      reject,
      timeoutMs: input.timeoutMs,
      signal: input.signal
    }
    if (input.signal) {
      job.abortListener = () => cancelJob(job)
      input.signal.addEventListener('abort', job.abortListener, { once: true })
    }
    queuedJobs.push(job)
    pumpJsonWorkerQueue()
  })
}

function pumpJsonWorkerQueue(): void {
  if (activeJob || queuedJobs.length === 0) {
    return
  }

  const job = queuedJobs.shift()
  if (!job) {
    return
  }
  activeJob = job

  try {
    job.startedAtMs = Date.now()
    startJobTimer(job)
    ensureWorker().postMessage({
      id: job.id,
      type: job.type,
      rawBody: job.rawBody,
      normalizeInput: job.normalizeInput
    })
  } catch (error) {
    failJob(job, error instanceof Error ? error : new Error(String(error)), true)
  }
}

function ensureWorker(): Worker {
  if (worker) {
    return worker
  }

  worker = new Worker(resolveGatewayJsonWorkerPath(), {
    execArgv: process.execArgv.filter((arg) => !arg.startsWith('--inspect'))
  })
  worker.unref()
  worker.on('message', handleWorkerMessage)
  worker.on('error', (error) => {
    failActiveJob(error, true)
  })
  worker.on('exit', (code) => {
    const exitedWorker = worker
    worker = undefined
    if (code !== 0) {
      logger.warn({
        event: 'gateway_json_parse_worker_exited',
        exitCode: code
      }, '网关 JSON worker 异常退出')
    }
    if (exitedWorker && activeJob) {
      failActiveJob(new Error(`网关 JSON worker 已退出，退出码 ${code}`), false)
    }
    pumpJsonWorkerQueue()
  })
  return worker
}

function resolveGatewayJsonWorkerPath(): string {
  if (currentModulePath.endsWith('.ts')) {
    return workerSourcePath
  }
  if (existsSync(workerDistPath)) {
    return workerDistPath
  }
  return workerSourcePath
}

function handleWorkerMessage(message: GatewayJsonWorkerResponse): void {
  const job = activeJob
  if (!job || message.id !== job.id) {
    return
  }

  clearJobTimer(job)
  removeAbortListener(job)
  activeJob = undefined
  if (message.ok) {
    logJobCompleted(job)
    job.resolve(message.value)
  } else {
    job.reject(workerResponseError(job, message))
  }
  pumpJsonWorkerQueue()
}

function workerResponseError(job: GatewayJsonWorkerJob, message: GatewayJsonWorkerResponse): Error {
  const errorMessage = message.errorMessage ?? '网关 JSON 请求体必须是有效 JSON'
  if (
    job.type === 'normalize_openai_oauth_codex_body'
    && message.errorCode === 'invalid_openai_oauth_codex_request'
  ) {
    return new OpenAIOAuthCodexAdapterError(errorMessage, message.errorCode, {
      statusCode: message.errorStatusCode,
      type: message.errorType
    })
  }
  return new Error(errorMessage)
}

function failActiveJob(error: Error, restartWorker: boolean): void {
  if (activeJob) {
    failJob(activeJob, error, restartWorker)
    return
  }
  if (restartWorker) {
    restartJsonWorker()
  }
}

function failJob(job: GatewayJsonWorkerJob, error: Error, restartWorker: boolean): void {
  const wasActive = activeJob?.id === job.id
  if (wasActive) {
    activeJob = undefined
  } else {
    const queuedIndex = queuedJobs.findIndex((item) => item.id === job.id)
    if (queuedIndex >= 0) {
      queuedJobs.removeAt(queuedIndex)
    }
  }
  clearJobTimer(job)
  removeAbortListener(job)
  logger.warn(errorLogFields(error, {
    event: 'gateway_json_parse_worker_failed',
    jobId: job.id,
    jobType: job.type,
    rawBodyBytes: job.rawBody.byteLength,
    queuedWaitMs: job.startedAtMs ? job.startedAtMs - job.enqueuedAtMs : undefined,
    workerDurationMs: job.startedAtMs ? Date.now() - job.startedAtMs : undefined,
    totalMs: Date.now() - job.enqueuedAtMs,
    queuedJobs: queuedJobs.length
  }), '网关 JSON worker 失败')
  job.reject(error)
  if (restartWorker) {
    restartJsonWorker()
  }
  pumpJsonWorkerQueue()
}

function cancelJob(job: GatewayJsonWorkerJob): void {
  const wasActive = activeJob?.id === job.id
  if (wasActive) {
    activeJob = undefined
  } else {
    const queuedIndex = queuedJobs.findIndex((item) => item.id === job.id)
    if (queuedIndex >= 0) {
      queuedJobs.removeAt(queuedIndex)
    }
  }
  clearJobTimer(job)
  removeAbortListener(job)
  job.reject(new Error('网关 JSON worker 任务已取消'))
  if (wasActive) {
    restartJsonWorker()
  }
  pumpJsonWorkerQueue()
}

function logJobCompleted(job: GatewayJsonWorkerJob): void {
  const now = Date.now()
  const queuedWaitMs = job.startedAtMs ? job.startedAtMs - job.enqueuedAtMs : 0
  const workerDurationMs = job.startedAtMs ? now - job.startedAtMs : undefined
  const totalMs = now - job.enqueuedAtMs
  const fields = {
    event: 'gateway_json_worker_job_completed',
    jobId: job.id,
    jobType: job.type,
    rawBodyBytes: job.rawBody.byteLength,
    queuedWaitMs,
    workerDurationMs,
    totalMs,
    queuedJobs: queuedJobs.length
  }
  if (queuedWaitMs >= gatewayJsonWorkerSlowQueueWaitMs || (workerDurationMs ?? 0) >= gatewayJsonWorkerSlowDurationMs) {
    logger.warn(fields, '网关 JSON worker 任务耗时偏高')
    return
  }
  if (job.rawBody.byteLength > gatewayJsonBodyLargeWarningBytes) {
    logger.info(fields, '网关 JSON worker 任务完成')
  }
}

function startJobTimer(job: GatewayJsonWorkerJob): void {
  clearJobTimer(job)
  job.timer = setTimeout(() => {
    failJob(job, new Error(`网关 JSON worker ${job.timeoutMs}ms 超时`), true)
  }, job.timeoutMs)
  job.timer.unref()
}

function clearJobTimer(job: GatewayJsonWorkerJob): void {
  if (!job.timer) {
    return
  }
  clearTimeout(job.timer)
  job.timer = undefined
}

function removeAbortListener(job: GatewayJsonWorkerJob): void {
  if (!job.signal || !job.abortListener) {
    return
  }
  job.signal.removeEventListener('abort', job.abortListener)
  job.abortListener = undefined
}

function restartJsonWorker(): void {
  const currentWorker = worker
  worker = undefined
  if (currentWorker) {
    currentWorker.removeAllListeners()
    void currentWorker.terminate().catch(() => undefined)
  }
}

const gatewayJsonWorkerSlowQueueWaitMs = 500
const gatewayJsonWorkerSlowDurationMs = 1000
