import { availableParallelism } from 'node:os'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Worker } from 'node:worker_threads'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'
import { HeadIndexedQueue } from '../background/ipc-head-queue.js'
import { readAuditLogSettings } from './audit-log-settings.js'

type AuditLogTransportMode = 'ipc' | 'redis_stream'

interface AuditLogTransportJob {
  id: number
  mode: AuditLogTransportMode
  input: AuditLogInput
  sourceBytes: number
  enqueuedAtMs: number
  startedAtMs?: number
  resolve: (value: AuditLogInput | string) => void
  reject: (error: Error) => void
  timer?: NodeJS.Timeout
}

interface AuditLogTransportWorkerResponse {
  id: number
  ok: boolean
  prepared?: AuditLogInput
  encoded?: string
  errorMessage?: string
}

interface AuditLogTransportWorkerSlot {
  id: number
  worker: Worker
  activeJob?: AuditLogTransportJob
  closing: boolean
}

export interface AuditLogTransportRuntime {
  queuedJobs: number
  queuedBytes: number
  activeJobs: number
  activeBytes: number
  workerCount: number
  completedCount: number
  failedCount: number
  rejectedCount: number
}

export class AuditLogTransportError extends Error {
}

export class AuditLogTransportQueueFullError extends AuditLogTransportError {
}

const currentModulePath = fileURLToPath(import.meta.url)
const currentModuleDir = dirname(currentModulePath)
const workerSourcePath = resolve(currentModuleDir, './audit-log-transport-worker.ts')
const workerDistPath = resolve(currentModuleDir, './audit-log-transport-worker.js')
const auditLogTransportMaxQueuedJobs = runtimeConfig.background.auditLogTransportMaxQueuedJobs
const auditLogTransportMaxTotalBytes = runtimeConfig.background.auditLogTransportMaxTotalMb * 1024 * 1024
const auditLogTransportMaxActiveBytes = runtimeConfig.background.auditLogTransportMaxActiveMb * 1024 * 1024
const auditLogTransportMaxJobBytes = runtimeConfig.background.auditLogTransportMaxJobMb * 1024 * 1024
const auditLogTransportJobTimeoutMs = 60_000

let nextJobId = 1
let nextWorkerSlotId = 1
let queuedBytes = 0
let completedCount = 0
let failedCount = 0
let rejectedCount = 0
const queuedJobs = new HeadIndexedQueue<AuditLogTransportJob>()
const workerSlots: AuditLogTransportWorkerSlot[] = []

export function prepareAuditLogForIpcInWorker(input: AuditLogInput): Promise<AuditLogInput> {
  return enqueueAuditLogTransportJob(input, 'ipc') as Promise<AuditLogInput>
}

export function encodeAuditLogForRedisStreamInWorker(input: AuditLogInput): Promise<string> {
  return enqueueAuditLogTransportJob(input, 'redis_stream') as Promise<string>
}

export function getAuditLogTransportRuntime(): AuditLogTransportRuntime {
  return {
    queuedJobs: queuedJobs.length,
    queuedBytes,
    activeJobs: workerSlots.filter((slot) => Boolean(slot.activeJob)).length,
    activeBytes: activeAuditLogTransportBytes(),
    workerCount: workerSlots.length,
    completedCount,
    failedCount,
    rejectedCount
  }
}

export async function waitForAuditLogTransportIdle(timeoutMs = 10_000): Promise<boolean> {
  const deadline = Date.now() + Math.max(1, timeoutMs)
  while (queuedJobs.length > 0 || activeAuditLogTransportBytes() > 0) {
    if (Date.now() >= deadline) return false
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
  return true
}

export async function stopAuditLogTransportWorker(): Promise<void> {
  const slots = workerSlots.splice(0, workerSlots.length)
  while (queuedJobs.length > 0) {
    const job = shiftQueuedJob()
    job?.reject(new AuditLogTransportError('审计传输 worker 已关闭'))
  }
  queuedBytes = 0
  for (const slot of slots) {
    slot.closing = true
    if (slot.activeJob) {
      const job = slot.activeJob
      slot.activeJob = undefined
      clearJobTimer(job)
      job.reject(new AuditLogTransportError('审计传输 worker 已关闭'))
    }
    slot.worker.removeAllListeners()
  }
  await Promise.all(slots.map((slot) => slot.worker.terminate()))
}

function enqueueAuditLogTransportJob(input: AuditLogInput, mode: AuditLogTransportMode): Promise<AuditLogInput | string> {
  return new Promise((resolve, reject) => {
    if (!readAuditLogSettings().enabled) {
      reject(new AuditLogTransportError('原始审计已关闭，拒绝创建传输 worker 任务'))
      return
    }
    const sourceBytes = auditLogTransportSourceBytes(input)
    if (
      sourceBytes > auditLogTransportMaxJobBytes
      || queuedJobs.length >= auditLogTransportMaxQueuedJobs
      || queuedBytes + activeAuditLogTransportBytes() + sourceBytes > auditLogTransportMaxTotalBytes
    ) {
      rejectedCount += 1
      reject(new AuditLogTransportQueueFullError('审计传输 worker 队列已满或单条记录超过处理上限'))
      return
    }
    const job: AuditLogTransportJob = {
      id: nextJobId++,
      mode,
      input,
      sourceBytes,
      enqueuedAtMs: Date.now(),
      resolve,
      reject
    }
    queuedJobs.push(job)
    queuedBytes += sourceBytes
    pumpAuditLogTransportQueue()
  })
}

function pumpAuditLogTransportQueue(): void {
  if (queuedJobs.length === 0) return
  ensureAuditLogTransportWorkerPool()
  while (queuedJobs.length > 0) {
    const slot = workerSlots.find((item) => !item.activeJob && !item.closing)
    if (!slot) return
    const nextJob = queuedJobs.at(0)
    if (!nextJob || !canStartAuditLogTransportJob(nextJob)) return
    const job = shiftQueuedJob()
    if (!job) return
    startAuditLogTransportJob(slot, job)
  }
}

function ensureAuditLogTransportWorkerPool(): void {
  const targetSize = Math.min(2, Math.max(1, availableParallelism() - 1))
  while (workerSlots.length < targetSize) {
    workerSlots.push(createAuditLogTransportWorkerSlot())
  }
}

function createAuditLogTransportWorkerSlot(): AuditLogTransportWorkerSlot {
  const settings = readAuditLogSettings()
  const slot: AuditLogTransportWorkerSlot = {
    id: nextWorkerSlotId++,
    worker: new Worker(resolveAuditLogTransportWorkerPath(), {
      execArgv: auditLogTransportWorkerExecArgv(),
      workerData: {
        successFullBodyLimitBytes: settings.successFullBodyLimitBytes,
        problemFullBodyLimitBytes: settings.problemFullBodyLimitBytes
      }
    }),
    closing: false
  }
  slot.worker.unref()
  slot.worker.on('message', (message: AuditLogTransportWorkerResponse) => handleAuditLogTransportWorkerMessage(slot, message))
  slot.worker.on('error', (error) => failAuditLogTransportWorkerSlot(slot, error))
  slot.worker.on('exit', (code) => {
    removeAuditLogTransportWorkerSlot(slot)
    if (slot.closing) return
    if (slot.activeJob) {
      const job = slot.activeJob
      slot.activeJob = undefined
      failAuditLogTransportJob(job, new Error(`审计传输 worker 已退出，退出码 ${code}`))
    }
    logger.warn({
      event: 'audit_log_transport_worker_exited',
      workerSlotId: slot.id,
      exitCode: code
    }, '审计传输 worker 异常退出')
    pumpAuditLogTransportQueue()
  })
  return slot
}

function startAuditLogTransportJob(slot: AuditLogTransportWorkerSlot, job: AuditLogTransportJob): void {
  slot.activeJob = job
  job.startedAtMs = Date.now()
  job.timer = setTimeout(() => {
    if (slot.activeJob?.id !== job.id) return
    slot.activeJob = undefined
    failAuditLogTransportJob(job, new Error('审计传输 worker 处理超时'))
    restartAuditLogTransportWorkerSlot(slot)
  }, auditLogTransportJobTimeoutMs)
  job.timer.unref()
  try {
    slot.worker.postMessage({
      id: job.id,
      mode: job.mode,
      input: job.input
    })
  } catch (error) {
    slot.activeJob = undefined
    failAuditLogTransportJob(job, error instanceof Error ? error : new Error(String(error)))
    restartAuditLogTransportWorkerSlot(slot)
  }
}

function handleAuditLogTransportWorkerMessage(
  slot: AuditLogTransportWorkerSlot,
  message: AuditLogTransportWorkerResponse
): void {
  const job = slot.activeJob
  if (!job || message.id !== job.id) return
  slot.activeJob = undefined
  clearJobTimer(job)
  if (!message.ok) {
    failAuditLogTransportJob(job, new Error(message.errorMessage ?? '审计传输 worker 处理失败'))
    pumpAuditLogTransportQueue()
    return
  }
  if (job.mode === 'redis_stream' && typeof message.encoded === 'string') {
    completedCount += 1
    job.resolve(message.encoded)
  } else if (job.mode === 'ipc' && message.prepared) {
    completedCount += 1
    job.resolve(rehydrateAuditLogBuffers(message.prepared))
  } else {
    failAuditLogTransportJob(job, new Error('审计传输 worker 返回结果不完整'))
  }
  pumpAuditLogTransportQueue()
}

function failAuditLogTransportWorkerSlot(slot: AuditLogTransportWorkerSlot, error: Error): void {
  if (slot.closing) return
  if (slot.activeJob) {
    const job = slot.activeJob
    slot.activeJob = undefined
    failAuditLogTransportJob(job, error)
  }
  restartAuditLogTransportWorkerSlot(slot)
}

function failAuditLogTransportJob(job: AuditLogTransportJob, error: Error): void {
  clearJobTimer(job)
  failedCount += 1
  logger.warn(errorLogFields(error, {
    event: 'audit_log_transport_worker_failed',
    jobId: job.id,
    mode: job.mode,
    sourceBytes: job.sourceBytes,
    queuedWaitMs: job.startedAtMs ? job.startedAtMs - job.enqueuedAtMs : undefined,
    workerDurationMs: job.startedAtMs ? Date.now() - job.startedAtMs : undefined
  }), '审计传输 worker 处理失败')
  job.reject(error instanceof AuditLogTransportError ? error : new AuditLogTransportError(error.message))
}

function restartAuditLogTransportWorkerSlot(slot: AuditLogTransportWorkerSlot): void {
  if (slot.closing) return
  slot.closing = true
  removeAuditLogTransportWorkerSlot(slot)
  slot.worker.removeAllListeners()
  void slot.worker.terminate().finally(() => pumpAuditLogTransportQueue())
}

function removeAuditLogTransportWorkerSlot(slot: AuditLogTransportWorkerSlot): void {
  const index = workerSlots.indexOf(slot)
  if (index >= 0) workerSlots.splice(index, 1)
}

function canStartAuditLogTransportJob(job: AuditLogTransportJob): boolean {
  const activeBytes = activeAuditLogTransportBytes()
  return activeBytes === 0 || activeBytes + job.sourceBytes <= auditLogTransportMaxActiveBytes
}

function activeAuditLogTransportBytes(): number {
  return workerSlots.reduce((total, slot) => total + (slot.activeJob?.sourceBytes ?? 0), 0)
}

function shiftQueuedJob(): AuditLogTransportJob | undefined {
  const job = queuedJobs.shift()
  if (job) queuedBytes = Math.max(0, queuedBytes - job.sourceBytes)
  return job
}

function clearJobTimer(job: AuditLogTransportJob): void {
  if (!job.timer) return
  clearTimeout(job.timer)
  job.timer = undefined
}

function resolveAuditLogTransportWorkerPath(): string {
  return currentModulePath.endsWith('.ts') ? workerSourcePath : workerDistPath
}

function auditLogTransportWorkerExecArgv(): string[] {
  const execArgv = stripNodeEvalExecArgv(process.execArgv.filter((arg) => !arg.startsWith('--inspect')))
  if (!currentModulePath.endsWith('.ts') || execArgv.some((arg) => arg.includes('tsx'))) {
    return execArgv
  }
  return [...execArgv, '--import', 'tsx']
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

function auditLogTransportSourceBytes(input: AuditLogInput): number {
  let bytes = 2048 + input.attempts.length * 1024
  for (const payload of input.payloads) {
    const body = payload.body
    bytes += Buffer.isBuffer(body)
      ? body.byteLength
      : typeof body === 'string'
        ? Math.min(auditLogTransportMaxJobBytes, body.length * 4)
        : 0
    bytes += payload.headers ? 64 * 1024 : 512
    if (bytes > auditLogTransportMaxJobBytes) return bytes
  }
  return bytes
}

function rehydrateAuditLogBuffers(input: AuditLogInput): AuditLogInput {
  return {
    ...input,
    payloads: input.payloads.map((payload) => {
      const body = payload.body as unknown
      return {
        ...payload,
        body: body instanceof Uint8Array && !Buffer.isBuffer(body)
          ? Buffer.from(body.buffer, body.byteOffset, body.byteLength)
          : payload.body
      }
    })
  }
}
