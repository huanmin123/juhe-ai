import { availableParallelism } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Worker } from 'node:worker_threads'

import { errorLogFields, logger } from '../../shared/logger.js'
import { gatewayJsonBodyLargeWarningBytes, type GatewayJsonBodyMetadata } from './openai-gateway-request-body.js'
import {
  OpenAIOAuthCodexAdapterError,
  type NormalizedCodexBody,
  type OpenAIOAuthCodexNormalizeInput
} from './openai-oauth-codex-normalizer.js'

type GatewayJsonWorkerJobType =
  | 'extract_json_body_metadata'
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

interface GatewayJsonWorkerSlot {
  id: number
  worker: Worker
  activeJob?: GatewayJsonWorkerJob
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

export class GatewayJsonWorkerQueueFullError extends Error {
}

let nextJobId = 1
let nextWorkerSlotId = 1
const workerSlots: GatewayJsonWorkerSlot[] = []
const queuedJobs = new HeadIndexedQueue<GatewayJsonWorkerJob>()
let queuedJobsBytes = 0

export function parseGatewayJsonBodyInWorker(rawBody: Buffer, timeoutMs = 30000, signal?: AbortSignal): Promise<unknown> {
  return enqueueGatewayJsonWorkerJob({
    type: 'parse_json_body',
    rawBody,
    timeoutMs,
    signal
  })
}

export function extractGatewayJsonBodyMetadataInWorker(rawBody: Buffer, timeoutMs = 30000, signal?: AbortSignal): Promise<GatewayJsonBodyMetadata> {
  return enqueueGatewayJsonWorkerJob<GatewayJsonBodyMetadata>({
    type: 'extract_json_body_metadata',
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

export function isGatewayJsonWorkerQueueFullError(error: unknown): error is GatewayJsonWorkerQueueFullError {
  return error instanceof GatewayJsonWorkerQueueFullError
}

export async function stopGatewayJsonParseWorker(): Promise<void> {
  const currentSlots = workerSlots.splice(0, workerSlots.length)
  for (const slot of currentSlots) {
    const currentJob = slot.activeJob
    slot.activeJob = undefined
    if (currentJob) {
      clearJobTimer(currentJob)
      removeAbortListener(currentJob)
      currentJob.reject(new Error('网关 JSON worker 已关闭'))
    }
  }
  while (queuedJobs.length > 0) {
    const job = shiftQueuedJob()
    if (job) {
      clearJobTimer(job)
      removeAbortListener(job)
      job.reject(new Error('网关 JSON worker 已关闭'))
    }
  }
  queuedJobsBytes = 0
  await Promise.all(currentSlots.map(async (slot) => {
    slot.worker.removeAllListeners()
    await slot.worker.terminate()
  }))
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
    if (!canQueueGatewayJsonWorkerJob(job)) {
      reject(new GatewayJsonWorkerQueueFullError('网关 JSON worker 队列已满，请稍后重试'))
      return
    }
    if (input.signal) {
      job.abortListener = () => cancelJob(job)
      input.signal.addEventListener('abort', job.abortListener, { once: true })
    }
    pushQueuedJob(job)
    pumpJsonWorkerQueue()
  })
}

function pumpJsonWorkerQueue(): void {
  if (queuedJobs.length === 0) {
    return
  }

  try {
    ensureWorkerPool()
  } catch (error) {
    failNextQueuedJob(error instanceof Error ? error : new Error(String(error)))
    return
  }

  while (queuedJobs.length > 0) {
    const slot = idleWorkerSlot()
    if (!slot) {
      return
    }
    const job = shiftQueuedJob()
    if (!job) {
      return
    }
    startJobOnWorkerSlot(slot, job)
  }
}

function ensureWorkerPool(): void {
  const targetSize = gatewayJsonWorkerPoolSize()
  while (workerSlots.length < targetSize) {
    workerSlots.push(createWorkerSlot())
  }
}

function createWorkerSlot(): GatewayJsonWorkerSlot {
  const slot: GatewayJsonWorkerSlot = {
    id: nextWorkerSlotId++,
    worker: new Worker(resolveGatewayJsonWorkerPath(), {
      execArgv: process.execArgv.filter((arg) => !arg.startsWith('--inspect'))
    })
  }
  slot.worker.unref()
  slot.worker.on('message', (message: GatewayJsonWorkerResponse) => handleWorkerMessage(slot, message))
  slot.worker.on('error', (error) => {
    failActiveJob(slot, error, true)
  })
  slot.worker.on('exit', (code) => {
    removeWorkerSlot(slot)
    if (code !== 0) {
      logger.warn({
        event: 'gateway_json_parse_worker_exited',
        workerSlotId: slot.id,
        exitCode: code
      }, '网关 JSON worker 异常退出')
    }
    if (slot.activeJob) {
      const job = slot.activeJob
      slot.activeJob = undefined
      failJob(job, new Error(`网关 JSON worker 已退出，退出码 ${code}`), false, slot)
    }
    pumpJsonWorkerQueue()
  })
  return slot
}

function startJobOnWorkerSlot(slot: GatewayJsonWorkerSlot, job: GatewayJsonWorkerJob): void {
  slot.activeJob = job
  try {
    job.startedAtMs = Date.now()
    startJobTimer(job)
    slot.worker.postMessage({
      id: job.id,
      type: job.type,
      rawBody: job.rawBody,
      normalizeInput: job.normalizeInput
    })
  } catch (error) {
    failJob(job, error instanceof Error ? error : new Error(String(error)), true, slot)
  }
}

function idleWorkerSlot(): GatewayJsonWorkerSlot | undefined {
  return workerSlots.find((slot) => !slot.activeJob)
}

function removeWorkerSlot(slot: GatewayJsonWorkerSlot): void {
  const index = workerSlots.indexOf(slot)
  if (index >= 0) {
    workerSlots.splice(index, 1)
  }
}

function resolveGatewayJsonWorkerPath(): string {
  if (currentModulePath.endsWith('.ts')) {
    return workerSourcePath
  }
  return workerDistPath
}

function handleWorkerMessage(slot: GatewayJsonWorkerSlot, message: GatewayJsonWorkerResponse): void {
  const job = slot.activeJob
  if (!job || message.id !== job.id) {
    return
  }

  clearJobTimer(job)
  removeAbortListener(job)
  slot.activeJob = undefined
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

function pushQueuedJob(job: GatewayJsonWorkerJob): void {
  queuedJobs.push(job)
  queuedJobsBytes += gatewayJsonWorkerJobBytes(job)
}

function shiftQueuedJob(): GatewayJsonWorkerJob | undefined {
  const job = queuedJobs.shift()
  if (job) {
    queuedJobsBytes = Math.max(0, queuedJobsBytes - gatewayJsonWorkerJobBytes(job))
  }
  return job
}

function removeQueuedJobAt(index: number): GatewayJsonWorkerJob | undefined {
  const job = queuedJobs.removeAt(index)
  if (job) {
    queuedJobsBytes = Math.max(0, queuedJobsBytes - gatewayJsonWorkerJobBytes(job))
  }
  return job
}

function canQueueGatewayJsonWorkerJob(job: GatewayJsonWorkerJob): boolean {
  const jobBytes = gatewayJsonWorkerJobBytes(job)
  return queuedJobs.length < gatewayJsonWorkerMaxQueuedJobs
    && queuedJobsBytes + jobBytes <= gatewayJsonWorkerMaxQueuedBytes
}

function gatewayJsonWorkerJobBytes(job: GatewayJsonWorkerJob): number {
  return job.rawBody.byteLength + 512
}

function failActiveJob(slot: GatewayJsonWorkerSlot, error: Error, restartWorker: boolean): void {
  if (slot.activeJob) {
    failJob(slot.activeJob, error, restartWorker, slot)
    return
  }
  if (restartWorker) {
    restartJsonWorker(slot)
  }
}

function failNextQueuedJob(error: Error): void {
  const job = shiftQueuedJob()
  if (!job) {
    return
  }
  failJob(job, error, false)
}

function failJob(job: GatewayJsonWorkerJob, error: Error, restartWorker: boolean, slot?: GatewayJsonWorkerSlot): void {
  const activeSlot = slot ?? activeWorkerSlotForJob(job)
  const wasActive = activeSlot?.activeJob?.id === job.id
  if (wasActive) {
    activeSlot.activeJob = undefined
  } else {
    const queuedIndex = queuedJobs.findIndex((item) => item.id === job.id)
    if (queuedIndex >= 0) {
      removeQueuedJobAt(queuedIndex)
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
    queuedJobs: queuedJobs.length,
    queuedBytes: queuedJobsBytes
  }), '网关 JSON worker 失败')
  job.reject(error)
  if (restartWorker && activeSlot) {
    restartJsonWorker(activeSlot)
  }
  pumpJsonWorkerQueue()
}

function cancelJob(job: GatewayJsonWorkerJob): void {
  const activeSlot = activeWorkerSlotForJob(job)
  const wasActive = activeSlot?.activeJob?.id === job.id
  if (wasActive) {
    activeSlot.activeJob = undefined
  } else {
    const queuedIndex = queuedJobs.findIndex((item) => item.id === job.id)
    if (queuedIndex >= 0) {
      removeQueuedJobAt(queuedIndex)
    }
  }
  clearJobTimer(job)
  removeAbortListener(job)
  job.reject(new Error('网关 JSON worker 任务已取消'))
  if (wasActive && activeSlot) {
    restartJsonWorker(activeSlot)
  }
  pumpJsonWorkerQueue()
}

function activeWorkerSlotForJob(job: GatewayJsonWorkerJob): GatewayJsonWorkerSlot | undefined {
  return workerSlots.find((slot) => slot.activeJob?.id === job.id)
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
    queuedJobs: queuedJobs.length,
    queuedBytes: queuedJobsBytes
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

function restartJsonWorker(slot: GatewayJsonWorkerSlot): void {
  removeWorkerSlot(slot)
  slot.worker.removeAllListeners()
  void slot.worker.terminate().catch(() => undefined)
}

function gatewayJsonWorkerPoolSize(): number {
  return Math.max(1, Math.min(4, availableParallelism()))
}

const gatewayJsonWorkerSlowQueueWaitMs = 500
const gatewayJsonWorkerSlowDurationMs = 1000
const gatewayJsonWorkerMaxQueuedJobs = 128
const gatewayJsonWorkerMaxQueuedBytes = 256 * 1024 * 1024
